package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"strings"
	"time"

	"github.com/l-lab/cloud-agents/pkg/logger"

	"github.com/l-lab/cloud-agents/internal/task"
)

// ErrNoActiveRun is returned by SteerMessage when the task has no active agent run.
var ErrNoActiveRun = errors.New("no active run for session")

// ImageSource describes the data source for an image content block.
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/jpeg" | "image/png" | "image/gif" | "image/webp"
	Data      string `json:"data"`
}

// ContentBlock is a typed content block accepted by claude-agent-server as part of a prompt.
type ContentBlock struct {
	Type   string       `json:"type"`             // "text" or "image"
	Text   string       `json:"text,omitempty"`
	Source *ImageSource `json:"source,omitempty"`
}

// agentQueryOptions mirrors the QueryOptions schema accepted by claude-agent-server.
type agentQueryOptions struct {
	CWD                   string   `json:"cwd,omitempty"`
	Model                 string   `json:"model,omitempty"`
	PermissionMode        string   `json:"permissionMode,omitempty"`
	SettingSources        []string `json:"settingSources,omitempty"`
	SystemPrompt          string   `json:"systemPrompt,omitempty"`
	AppendSystemPrompt    string   `json:"appendSystemPrompt,omitempty"`
	AllowedTools          []string `json:"allowedTools,omitempty"`
	DisallowedTools       []string `json:"disallowedTools,omitempty"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
	// Tools is either []string or ToolsPreset marshaled to JSON.
	Tools                   json.RawMessage `json:"tools,omitempty"`
	MaxTurns                int             `json:"maxTurns,omitempty"`
	EnableFileCheckpointing bool            `json:"enableFileCheckpointing,omitempty"`
}

// ToolsPreset selects a named tool preset (e.g. {Type:"preset", Preset:"claude_code"}).
type ToolsPreset struct {
	Type   string `json:"type"`
	Preset string `json:"preset"`
}

type Proxy struct {
	client *http.Client
}

func NewProxy() *Proxy {
	return &Proxy{client: &http.Client{}}
}

// StreamMessage forwards a prompt to the claude-agent-server and pipes the SSE
// response back to w. It extracts the agentSessionID from the session.init event
// on the first message. After a new session stream completes it also fetches
// the session metadata to populate the task title.
// If blocks is non-empty, a text block is prepended and the full array is sent as the prompt.
func (p *Proxy) StreamMessage(ctx context.Context, t *task.Task, prompt string, blocks []ContentBlock, permissionMode string, w http.ResponseWriter) error {
	proxyBaseURL, proxyHeaders := t.GetProxyInfo()
	sessionID := t.GetSessionID()
	isNew := sessionID == ""

	var upstreamURL string
	if isNew {
		upstreamURL = proxyBaseURL + "/sessions"
	} else {
		upstreamURL = proxyBaseURL + "/sessions/" + sessionID + "/messages"
	}

	opts := agentQueryOptions{
		CWD:            fmt.Sprintf("/workspace/%s/%s", t.Username, t.ID),
		PermissionMode: permissionMode,
	}

	var promptPayload any = prompt
	if len(blocks) > 0 {
		full := []ContentBlock{{Type: "text", Text: prompt}}
		full = append(full, blocks...)
		promptPayload = full
	}

	var reqBodyMap map[string]any
	if isNew {
		reqBodyMap = map[string]any{
			"prompt":  promptPayload,
			"stream":  true,
			"options": opts,
		}
	} else {
		reqBodyMap = map[string]any{
			"prompt":                 promptPayload,
			"stream":                 true,
			"includePartialMessages": true,
			"forkSession":            false,
			"options":                opts,
		}
	}
	body, err := json.Marshal(reqBodyMap)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range proxyHeaders {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upstream %d: %s", resp.StatusCode, b)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1 MiB
	var currentEvent string

	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}

		line := scanner.Text()

		switch {
		case line == "":
			currentEvent = ""
			fmt.Fprint(w, "\n")
		case strings.HasPrefix(line, "event:"):
			currentEvent = strings.TrimSpace(line[6:])
			fmt.Fprintf(w, "%s\n", line)
		case strings.HasPrefix(line, "data:") && currentEvent == "session.init":
			dataStr := strings.TrimSpace(line[5:])
			var payload struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal([]byte(dataStr), &payload) == nil && payload.SessionID != "" {
				t.SetSessionID(payload.SessionID)
				logger.Default().Info("session ID established", "task_id", t.ID, "session_id", payload.SessionID)
			}
			fmt.Fprintf(w, "%s\n", line)
		default:
			fmt.Fprintf(w, "%s\n", line)
		}

		if flusher != nil {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("reading stream: %w", err)
	}

	// After a new session completes, fetch its metadata to set the task title.
	if isNew {
		if sid := t.GetSessionID(); sid != "" {
			tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			title := p.fetchSessionTitle(tctx, proxyBaseURL, proxyHeaders, sid, prompt)
			t.SetTitle(title)
		}
	}

	return nil
}

// sessionMetaResponse is the JSON shape returned by GET /sessions/:sessionId.
type sessionMetaResponse struct {
	Session struct {
		Summary     string  `json:"summary"`
		CustomTitle *string `json:"customTitle"`
		FirstPrompt string  `json:"firstPrompt"`
	} `json:"session"`
}

// fetchSessionTitle calls GET /sessions/:sessionId on the sandbox and returns
// the best available title using the summary → customTitle → firstPrompt → fallback chain.
func (p *Proxy) fetchSessionTitle(ctx context.Context, proxyBaseURL string, proxyHeaders map[string]string, sessionID, fallback string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyBaseURL+"/sessions/"+sessionID, nil)
	if err != nil {
		logger.Default().Error("fetchSessionTitle: build request", "err", err)
		return fallback
	}
	for k, v := range proxyHeaders {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		logger.Default().Error("fetchSessionTitle: upstream request", "err", err)
		return fallback
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		logger.Default().Warn("fetchSessionTitle: upstream error", "status", resp.StatusCode, "session_id", sessionID)
		return fallback
	}

	var meta sessionMetaResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		logger.Default().Error("fetchSessionTitle: decode", "err", err)
		return fallback
	}

	if meta.Session.Summary != "" {
		return meta.Session.Summary
	}
	if meta.Session.CustomTitle != nil && *meta.Session.CustomTitle != "" {
		return *meta.Session.CustomTitle
	}
	if meta.Session.FirstPrompt != "" {
		return meta.Session.FirstPrompt
	}
	return fallback
}

// SteerMessage injects a prompt into an already-running agent session without
// opening a new SSE stream. The injected message's effects arrive on the
// existing open SSE connection.
func (p *Proxy) SteerMessage(ctx context.Context, t *task.Task, prompt, priority string) error {
	proxyBaseURL, proxyHeaders := t.GetProxyInfo()
	sessionID := t.GetSessionID()
	if sessionID == "" {
		return ErrNoActiveRun
	}

	bodyMap := map[string]any{
		"prompt": prompt,
		"stream": false,
	}
	if priority != "" {
		bodyMap["priority"] = priority
	}

	data, err := json.Marshal(bodyMap)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		proxyBaseURL+"/sessions/"+sessionID+"/messages", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range proxyHeaders {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("steer request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNoActiveRun
	}
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("steer: unexpected status %d: %s", resp.StatusCode, b)
}

type permissionDecisionRequest struct {
	Decision string `json:"decision"`
}

type questionAnswerRequest struct {
	Answers map[string]any `json:"answers"`
}

// RespondToPermission forwards a permission decision (allow/deny) to the
// claude-agent-server for a pending canUseTool request on the session.
func (p *Proxy) RespondToPermission(ctx context.Context, t *task.Task, decision string) error {
	proxyBaseURL, proxyHeaders := t.GetProxyInfo()
	sessionID := t.GetSessionID()
	if sessionID == "" {
		return fmt.Errorf("no session ID for task %s", t.ID)
	}

	upstreamURL := proxyBaseURL + "/sessions/" + sessionID + "/permissions/respond"

	body, err := json.Marshal(permissionDecisionRequest{Decision: decision})
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range proxyHeaders {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upstream %d: %s", resp.StatusCode, b)
	}
	return nil
}

// RespondToQuestion forwards user answers to a pending AskUserQuestion request
// on the agent session.
func (p *Proxy) RespondToQuestion(ctx context.Context, t *task.Task, answers map[string]any) error {
	proxyBaseURL, proxyHeaders := t.GetProxyInfo()
	sessionID := t.GetSessionID()
	if sessionID == "" {
		return fmt.Errorf("no session ID for task %s", t.ID)
	}

	upstreamURL := proxyBaseURL + "/sessions/" + sessionID + "/questions/respond"

	body, err := json.Marshal(questionAnswerRequest{Answers: answers})
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range proxyHeaders {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("upstream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upstream %d: %s", resp.StatusCode, b)
	}
	return nil
}
