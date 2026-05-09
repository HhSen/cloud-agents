package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/your-org/platform-backend/internal/conversation"
)

func proxyConv(baseURL string, headers map[string]string, sessionID string) *conversation.Conversation {
	s := conversation.NewStore()
	conv := s.Create(nil)
	conv.SetRunning("sb1", baseURL, headers)
	if sessionID != "" {
		conv.SetAgentSessionID(sessionID)
	}
	return conv
}

func TestStreamMessage_NewSession(t *testing.T) {
	var capturedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: session.init\n")
		fmt.Fprint(w, `data: {"sessionId":"abc123"}`+"\n")
		fmt.Fprint(w, "\n")
	}))
	defer upstream.Close()

	conv := proxyConv(upstream.URL, nil, "")
	p := NewProxy()
	rw := httptest.NewRecorder()

	if err := p.StreamMessage(context.Background(), conv, "hello", rw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedPath != "/sessions" {
		t.Errorf("expected path /sessions, got %q", capturedPath)
	}
	if conv.GetAgentSessionID() != "abc123" {
		t.Errorf("expected agentSessionID=abc123, got %q", conv.GetAgentSessionID())
	}
	body := rw.Body.String()
	if !strings.Contains(body, "session.init") {
		t.Errorf("expected session.init in forwarded body, got: %q", body)
	}
}

func TestStreamMessage_ExistingSession(t *testing.T) {
	var capturedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer upstream.Close()

	conv := proxyConv(upstream.URL, nil, "existing-session")
	p := NewProxy()
	rw := httptest.NewRecorder()

	p.StreamMessage(context.Background(), conv, "hi", rw)

	want := "/sessions/existing-session/messages"
	if capturedPath != want {
		t.Errorf("expected path %q, got %q", want, capturedPath)
	}
}

func TestStreamMessage_Upstream4xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer upstream.Close()

	conv := proxyConv(upstream.URL, nil, "")
	p := NewProxy()
	rw := httptest.NewRecorder()

	err := p.StreamMessage(context.Background(), conv, "x", rw)
	if err == nil {
		t.Fatal("expected error for 4xx response, got nil")
	}
}

func TestStreamMessage_Upstream5xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "internal error detail")
	}))
	defer upstream.Close()

	conv := proxyConv(upstream.URL, nil, "")
	p := NewProxy()
	rw := httptest.NewRecorder()

	err := p.StreamMessage(context.Background(), conv, "x", rw)
	if err == nil {
		t.Fatal("expected error for 5xx response, got nil")
	}
	if !strings.Contains(err.Error(), "internal error detail") {
		t.Errorf("expected error to contain response body, got: %v", err)
	}
}

// streamHeaderSignaler signals when WriteHeader is called so we know the
// upstream request completed and the SSE scan loop has started.
type streamHeaderSignaler struct {
	*httptest.ResponseRecorder
	ch   chan struct{}
	once sync.Once
}

func (s *streamHeaderSignaler) WriteHeader(code int) {
	s.ResponseRecorder.WriteHeader(code)
	s.once.Do(func() { close(s.ch) })
}

func TestStreamMessage_ContextCancel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		// Hold the connection open until the client context cancels.
		<-r.Context().Done()
	}))
	defer upstream.Close()

	conv := proxyConv(upstream.URL, nil, "")
	p := NewProxy()

	headerWritten := make(chan struct{})
	rw := &streamHeaderSignaler{
		ResponseRecorder: httptest.NewRecorder(),
		ch:               headerWritten,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- p.StreamMessage(ctx, conv, "x", rw)
	}()

	// Wait until StreamMessage has written SSE headers to rw — this guarantees
	// client.Do has returned and the scanner loop has started.
	<-headerWritten
	cancel()

	err := <-done
	if err != nil {
		t.Errorf("expected nil on context cancel, got: %v", err)
	}
}

func TestStreamMessage_Headers(t *testing.T) {
	var capturedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer upstream.Close()

	headers := map[string]string{"Authorization": "Bearer mytoken"}
	conv := proxyConv(upstream.URL, headers, "")
	p := NewProxy()
	rw := httptest.NewRecorder()

	p.StreamMessage(context.Background(), conv, "x", rw)

	if capturedAuth != "Bearer mytoken" {
		t.Errorf("expected Authorization: Bearer mytoken, got %q", capturedAuth)
	}
}

func TestStreamMessage_SSEResponseHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer upstream.Close()

	conv := proxyConv(upstream.URL, nil, "")
	p := NewProxy()
	rw := httptest.NewRecorder()

	p.StreamMessage(context.Background(), conv, "x", rw)

	checks := map[string]string{
		"Content-Type":    "text/event-stream",
		"Cache-Control":   "no-cache",
		"Connection":      "keep-alive",
		"X-Accel-Buffering": "no",
	}
	for k, want := range checks {
		if got := rw.Header().Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
}
