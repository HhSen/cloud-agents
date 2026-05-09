package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/your-org/platform-backend/internal/conversation"
)

// mockLC is a test double for the lifecycleClient interface.
type mockLC struct {
	createInfo  *SandboxInfo
	createErr   error
	capturedReq CreateSandboxRequest

	getResponses []SandboxState // returned in order, last one repeated
	getCallCount int
	getErr       error

	deleteErr error
	deletedID string
}

func (m *mockLC) CreateSandbox(_ context.Context, req CreateSandboxRequest) (*SandboxInfo, error) {
	m.capturedReq = req
	return m.createInfo, m.createErr
}

func (m *mockLC) GetSandbox(_ context.Context, id string) (*SandboxInfo, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	idx := m.getCallCount
	if idx >= len(m.getResponses) {
		idx = len(m.getResponses) - 1
	}
	m.getCallCount++
	state := m.getResponses[idx]
	return &SandboxInfo{
		ID:     id,
		Status: SandboxStatus{State: state},
	}, nil
}

func (m *mockLC) DeleteSandbox(_ context.Context, id string) error {
	m.deletedID = id
	return m.deleteErr
}

// noopHealthChecker always succeeds immediately, letting existing tests bypass the
// health check without making real HTTP requests.
type noopHealthChecker struct{}

func (n *noopHealthChecker) WaitForHealth(_ context.Context, _ string, _ map[string]string) error {
	return nil
}

// errHealthChecker always returns a fixed error.
type errHealthChecker struct{ err error }

func (e *errHealthChecker) WaitForHealth(_ context.Context, _ string, _ map[string]string) error {
	return e.err
}

func newTestManager(lc lifecycleClient, serverURL, apiKey string, baseEnv map[string]string) *Manager {
	return &Manager{
		lc:            lc,
		serverURL:     serverURL,
		apiKey:        apiKey,
		baseEnv:       baseEnv,
		sandboxImage:  "test-image:latest",
		agentPort:     DefaultAgentPort,
		healthChecker: &noopHealthChecker{},
	}
}

func sandboxConv(extraEnv map[string]string) *conversation.Conversation {
	s := conversation.NewStore()
	return s.Create(extraEnv)
}

func TestProvision_MergesEnv(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sb1"},
		getResponses: []SandboxState{StateRunning},
	}
	mgr := newTestManager(lc, "http://srv", "key", map[string]string{"FOO": "base", "STATIC": "yes"})
	conv := sandboxConv(map[string]string{"FOO": "override", "BAR": "new"})

	if err := mgr.ProvisionForConversation(context.Background(), conv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lc.capturedReq.Env["FOO"] != "override" {
		t.Errorf("FOO should be overridden to 'override', got %q", lc.capturedReq.Env["FOO"])
	}
	if lc.capturedReq.Env["BAR"] != "new" {
		t.Errorf("BAR should be 'new', got %q", lc.capturedReq.Env["BAR"])
	}
	if lc.capturedReq.Env["STATIC"] != "yes" {
		t.Errorf("STATIC from baseEnv should be 'yes', got %q", lc.capturedReq.Env["STATIC"])
	}
}

func TestProvision_SetsRunning(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sb42"},
		getResponses: []SandboxState{StateRunning},
	}
	mgr := newTestManager(lc, "http://myserver", "k", nil)
	conv := sandboxConv(nil)

	if err := mgr.ProvisionForConversation(context.Background(), conv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if conv.GetState() != conversation.StateRunning {
		t.Fatalf("expected StateRunning, got %v", conv.GetState())
	}
}

func TestProvision_ProxyURL(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sb99"},
		getResponses: []SandboxState{StateRunning},
	}
	mgr := newTestManager(lc, "http://myserver", "k", nil)
	conv := sandboxConv(nil)

	if err := mgr.ProvisionForConversation(context.Background(), conv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantURL := "http://myserver/sandboxes/sb99/proxy/3000"
	gotURL, _ := conv.GetProxyInfo()
	if gotURL != wantURL {
		t.Errorf("proxy URL = %q, want %q", gotURL, wantURL)
	}
}

func TestProvision_AuthHeader(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sb1"},
		getResponses: []SandboxState{StateRunning},
	}
	mgr := newTestManager(lc, "http://srv", "myapikey", nil)
	conv := sandboxConv(nil)

	if err := mgr.ProvisionForConversation(context.Background(), conv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, headers := conv.GetProxyInfo()
	if headers["Authorization"] != "Bearer myapikey" {
		t.Errorf("expected Authorization header, got %v", headers)
	}
}

func TestProvision_NoAuthHeader(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sb1"},
		getResponses: []SandboxState{StateRunning},
	}
	mgr := newTestManager(lc, "http://srv", "", nil)
	conv := sandboxConv(nil)

	if err := mgr.ProvisionForConversation(context.Background(), conv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, headers := conv.GetProxyInfo()
	if _, ok := headers["Authorization"]; ok {
		t.Error("expected no Authorization header when apiKey is empty")
	}
}

func TestProvision_TerminalStateFails(t *testing.T) {
	for _, state := range []SandboxState{StateFailed, StateTerminated} {
		t.Run(string(state), func(t *testing.T) {
			lc := &mockLC{
				createInfo:   &SandboxInfo{ID: "sb1"},
				getResponses: []SandboxState{state},
			}
			mgr := newTestManager(lc, "http://srv", "k", nil)
			conv := sandboxConv(nil)

			err := mgr.ProvisionForConversation(context.Background(), conv)
			if err == nil {
				t.Fatal("expected error for terminal state, got nil")
			}
		})
	}
}

func TestProvision_TimeoutFails(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sb1"},
		getResponses: []SandboxState{StatePending},
	}
	mgr := newTestManager(lc, "http://srv", "k", nil)
	conv := sandboxConv(nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	err := mgr.ProvisionForConversation(ctx, conv)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestProvision_CreateError(t *testing.T) {
	lc := &mockLC{
		createErr: errors.New("quota exceeded"),
	}
	mgr := newTestManager(lc, "http://srv", "k", nil)
	conv := sandboxConv(nil)

	err := mgr.ProvisionForConversation(context.Background(), conv)
	if err == nil {
		t.Fatal("expected error from CreateSandbox, got nil")
	}
}

func TestDeleteSandbox_Delegates(t *testing.T) {
	lc := &mockLC{}
	mgr := newTestManager(lc, "http://srv", "k", nil)

	if err := mgr.DeleteSandbox(context.Background(), "sandbox-xyz"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lc.deletedID != "sandbox-xyz" {
		t.Errorf("expected deletedID=sandbox-xyz, got %q", lc.deletedID)
	}
}

func TestProvision_Platform(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sb1"},
		getResponses: []SandboxState{StateRunning},
	}
	mgr := newTestManager(lc, "http://srv", "k", nil)
	mgr.platform = &PlatformSpec{OS: "linux", Arch: "amd64"}
	conv := sandboxConv(nil)

	if err := mgr.ProvisionForConversation(context.Background(), conv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lc.capturedReq.Platform == nil {
		t.Fatal("expected platform to be set in request")
	}
	if lc.capturedReq.Platform.OS != "linux" || lc.capturedReq.Platform.Arch != "amd64" {
		t.Errorf("platform = %+v, want {linux amd64}", lc.capturedReq.Platform)
	}
}

func TestProvision_NoPlatform(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sb1"},
		getResponses: []SandboxState{StateRunning},
	}
	mgr := newTestManager(lc, "http://srv", "k", nil)
	conv := sandboxConv(nil)

	if err := mgr.ProvisionForConversation(context.Background(), conv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if lc.capturedReq.Platform != nil {
		t.Errorf("expected no platform in request, got %+v", lc.capturedReq.Platform)
	}
}

func TestProvision_HealthCheckError(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sb1"},
		getResponses: []SandboxState{StateRunning},
	}
	mgr := newTestManager(lc, "http://srv", "k", nil)
	mgr.healthChecker = &errHealthChecker{err: errors.New("server never came up")}
	conv := sandboxConv(nil)

	err := mgr.ProvisionForConversation(context.Background(), conv)
	if err == nil {
		t.Fatal("expected error when health check fails, got nil")
	}
	if conv.GetState() == conversation.StateRunning {
		t.Error("conversation should not be Running when health check fails")
	}
}

// --- httpHealthChecker tests using httptest.Server ---

func healthyServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"healthy": true, "service": "test", "host": "localhost", "port": 3000, "timestamp": "2024-01-01T00:00:00Z"})
	}))
}

func TestHTTPHealthChecker_HealthyImmediately(t *testing.T) {
	srv := healthyServer(t)
	defer srv.Close()

	hc := newHTTPHealthChecker(srv.Client())
	err := hc.WaitForHealth(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestHTTPHealthChecker_RetriesBeforeSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			// Return not-yet-healthy response for first two calls.
			json.NewEncoder(w).Encode(map[string]any{"healthy": false})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"healthy": true, "service": "test", "host": "localhost", "port": 3000, "timestamp": "2024-01-01T00:00:00Z"})
	}))
	defer srv.Close()

	hc := newHTTPHealthChecker(srv.Client())
	hc.client.Timeout = 500 * time.Millisecond
	err := hc.WaitForHealth(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls.Load() < 3 {
		t.Errorf("expected at least 3 calls, got %d", calls.Load())
	}
}

func TestHTTPHealthChecker_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"healthy": false})
	}))
	defer srv.Close()

	hc := newHTTPHealthChecker(srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := hc.WaitForHealth(ctx, srv.URL, nil)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestHTTPHealthChecker_AuthError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	hc := newHTTPHealthChecker(srv.Client())
	err := hc.WaitForHealth(context.Background(), srv.URL, nil)
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) || statusErr.code != http.StatusUnauthorized {
		t.Errorf("expected httpStatusError with 401, got %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 call before short-circuit, got %d", n)
	}
}

func TestHTTPHealthChecker_ForbiddenError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	hc := newHTTPHealthChecker(srv.Client())
	err := hc.WaitForHealth(context.Background(), srv.URL, nil)
	if err == nil {
		t.Fatal("expected error on 403, got nil")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 call before short-circuit, got %d", n)
	}
}

func TestHTTPHealthChecker_ForwardsAuthHeader(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"healthy": true, "service": "test", "host": "localhost", "port": 3000, "timestamp": "2024-01-01T00:00:00Z"})
	}))
	defer srv.Close()

	hc := newHTTPHealthChecker(srv.Client())
	headers := map[string]string{"Authorization": "Bearer testkey"}
	if err := hc.WaitForHealth(context.Background(), srv.URL, headers); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedAuth != "Bearer testkey" {
		t.Errorf("Authorization header = %q, want %q", capturedAuth, "Bearer testkey")
	}
}

func TestHTTPHealthChecker_Non200Retried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"healthy": true, "service": "test", "host": "localhost", "port": 3000, "timestamp": "2024-01-01T00:00:00Z"})
	}))
	defer srv.Close()

	hc := newHTTPHealthChecker(srv.Client())
	hc.client.Timeout = 500 * time.Millisecond
	if err := hc.WaitForHealth(context.Background(), srv.URL, nil); err != nil {
		t.Fatalf("expected success after 503 retry, got %v", err)
	}
	if calls.Load() < 2 {
		t.Errorf("expected at least 2 calls, got %d", calls.Load())
	}
}

func TestProvision_AgentPort(t *testing.T) {
	lc := &mockLC{
		createInfo:   &SandboxInfo{ID: "sbport"},
		getResponses: []SandboxState{StateRunning},
	}
	mgr := newTestManager(lc, "http://srv", "k", nil)
	mgr.agentPort = 8080
	conv := sandboxConv(nil)

	if err := mgr.ProvisionForConversation(context.Background(), conv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantURL := "http://srv/sandboxes/sbport/proxy/8080"
	gotURL, _ := conv.GetProxyInfo()
	if gotURL != wantURL {
		t.Errorf("proxy URL = %q, want %q", gotURL, wantURL)
	}
}

func TestHTTPHealthChecker_HealthCheckURLPassedToChecker(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"healthy": true, "service": "test", "host": "localhost", "port": 3000, "timestamp": "2024-01-01T00:00:00Z"})
	}))
	defer srv.Close()

	hc := newHTTPHealthChecker(srv.Client())
	proxyBaseURL := fmt.Sprintf("%s/sandboxes/sb1/proxy/3000", srv.URL)
	if err := hc.WaitForHealth(context.Background(), proxyBaseURL, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedPath != "/sandboxes/sb1/proxy/3000/health" {
		t.Errorf("health check path = %q, want %q", capturedPath, "/sandboxes/sb1/proxy/3000/health")
	}
}
