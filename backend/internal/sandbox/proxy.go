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

	"github.com/your-org/platform-backend/internal/conversation"
)

type Proxy struct {
	client *http.Client
}

func NewProxy() *Proxy {
	return &Proxy{client: &http.Client{}}
}

// StreamMessage forwards a prompt to the claude-agent-server and pipes the SSE
// response back to w. It extracts the agentSessionID from the session.init event
// on the first message.
func (p *Proxy) StreamMessage(ctx context.Context, conv *conversation.Conversation, prompt string, w http.ResponseWriter) error {
	proxyBaseURL, proxyHeaders := conv.GetProxyInfo()
	agentSessionID := conv.GetAgentSessionID()

	var upstreamURL string
	if agentSessionID == "" {
		upstreamURL = proxyBaseURL + "/sessions"
	} else {
		upstreamURL = proxyBaseURL + "/sessions/" + agentSessionID + "/messages"
	}

	body, err := json.Marshal(map[string]any{
		"prompt": prompt,
		"stream": true,
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
			// Event separator — reset current event name and forward blank line.
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
				conv.SetAgentSessionID(payload.SessionID)
				log.Printf("conv %s: agent session ID = %s", conv.ID, payload.SessionID)
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
