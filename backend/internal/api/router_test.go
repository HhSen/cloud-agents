package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS_Preflight(t *testing.T) {
	router := NewRouter(&mockStore{}, &mockManager{}, "http://example.com", nil)

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
	router := NewRouter(&mockStore{}, &mockManager{}, "http://example.com", nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if got := rw.Header().Get("Access-Control-Allow-Origin"); got != "http://example.com" {
		t.Errorf("expected CORS header on GET response, got %q", got)
	}
}

func TestCORS_Wildcard(t *testing.T) {
	router := NewRouter(&mockStore{}, &mockManager{}, "*", nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if got := rw.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected Access-Control-Allow-Origin=*, got %q", got)
	}
}

// Ensure mockStore and mockManager satisfy the interfaces (compile-time check).
var _ TaskStore = (*mockStore)(nil)
var _ SandboxManager = (*mockManager)(nil)
