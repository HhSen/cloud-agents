package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/your-org/platform-backend/internal/task"
)

// agentQueryOptions mirrors the QueryOptions schema accepted by claude-agent-server.
type agentQueryOptions struct {
	CWD                  string   `json:"cwd,omitempty"`
	Model                string   `json:"model,omitempty"`
	PermissionMode       string   `json:"permissionMode,omitempty"`
	SystemPrompt         string   `json:"systemPrompt,omitempty"`
	AppendSystemPrompt   string   `json:"appendSystemPrompt,omitempty"`
	AllowedTools         []string `json:"allowedTools,omitempty"`
	DisallowedTools      []string `json:"disallowedTools,omitempty"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
	MaxTurns             int      `json:"maxTurns,omitempty"`
	EnableFileCheckpointing bool  `json:"enableFileCheckpointing,omitempty"`
}

// agentRequest is the body for both POST /sessions and POST /sessions/:id/messages.
type agentRequest struct {
	Prompt  string            `json:"prompt"`
	Stream  bool              `json:"stream"`
	Options agentQueryOptions `json:"options,omitempty"`
}

type Proxy struct {
	client *http.Client
}

func NewProxy() *Proxy {
	return &Proxy{client: &http.Client{}}
}

// StreamMessage forwards a prompt to the claude-agent-server and pipes the SSE
// response back to w. It extracts the agentSessionID from the session.init event
// on the first message.
func (p *Proxy) StreamMessage(ctx context.Context, t *task.Task, prompt string, w http.ResponseWriter) error {
	proxyBaseURL, proxyHeaders := t.GetProxyInfo()
	agentSessionID := t.GetAgentSessionID()

	var upstreamURL string
	if agentSessionID == "" {
		upstreamURL = proxyBaseURL + "/sessions"
	} else {
		upstreamURL = proxyBaseURL + "/sessions/" + agentSessionID + "/messages"
	}

	body, err := json.Marshal(agentRequest{
		Prompt:  prompt,
		Stream:  true,
		Options: agentQueryOptions{CWD: fmt.Sprintf("/workspace/%s/%s", t.Username, t.ID)},
	})
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
				t.SetAgentSessionID(payload.SessionID)
				log.Printf("task %s: agent session ID = %s", t.ID, payload.SessionID)
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
	return nil
}
