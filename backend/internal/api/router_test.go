package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/your-org/platform-backend/internal/conversation"
)

func TestCORS_Preflight(t *testing.T) {
	router := NewRouter(&mockStore{}, &mockManager{}, "http://example.com")

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for preflight, got %d", rw.Code)
	}
	if got := rw.Header().Get("Access-Control-Allow-Origin"); got != "http://example.com" {
		t.Errorf("expected Access-Control-Allow-Origin=http://example.com, got %q", got)
	}
	if got := rw.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("expected Access-Control-Allow-Methods to be set")
	}
}

func TestCORS_SimpleRequest(t *testing.T) {
	router := NewRouter(&mockStore{}, &mockManager{}, "http://example.com")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if got := rw.Header().Get("Access-Control-Allow-Origin"); got != "http://example.com" {
		t.Errorf("expected CORS header on GET response, got %q", got)
	}
}

func TestCORS_Wildcard(t *testing.T) {
	router := NewRouter(&mockStore{}, &mockManager{}, "*")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if got := rw.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected Access-Control-Allow-Origin=*, got %q", got)
	}
}

// routerMockProxy satisfies MessageProxy for the router-level test in NewRouter.
// NewRouter creates its own sandbox.NewProxy(), so these router tests don't use
// a mock proxy — they test CORS only and hit /health which needs no proxy.

// Ensure mockStore satisfies ConversationStore for router tests (already defined in handlers_test.go).
var _ ConversationStore = (*mockStore)(nil)
var _ SandboxManager = (*mockManager)(nil)

// routerMockManager satisfies SandboxManager without recording calls.
type routerMockManager struct{}

func (m *routerMockManager) ProvisionForConversation(_ context.Context, _ *conversation.Conversation) error {
	return nil
}
func (m *routerMockManager) DeleteSandbox(_ context.Context, _ string) error { return nil }
