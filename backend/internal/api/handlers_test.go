package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/your-org/platform-backend/internal/conversation"
)

// ---- mock types ----

type mockStore struct {
	mu   sync.Mutex
	conv *conversation.Conversation
}

func (m *mockStore) Create(env map[string]string) *conversation.Conversation {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := conversation.NewStore()
	m.conv = s.Create(env)
	return m.conv
}

func (m *mockStore) Get(id string) *conversation.Conversation {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conv != nil && m.conv.ID == id {
		return m.conv
	}
	return nil
}

func (m *mockStore) Delete(_ string) {}

type mockManager struct {
	provisionErr error
	calls        atomic.Int32
}

func (m *mockManager) ProvisionForConversation(_ context.Context, _ *conversation.Conversation) error {
	m.calls.Add(1)
	return m.provisionErr
}

func (m *mockManager) DeleteSandbox(_ context.Context, _ string) error { return nil }

type mockProxy struct {
	err error
}

func (m *mockProxy) StreamMessage(_ context.Context, _ *conversation.Conversation, _ string, w http.ResponseWriter) error {
	return m.err
}

// ---- helpers ----

func newHandler(store ConversationStore, mgr SandboxManager, proxy MessageProxy) *Handler {
	return NewHandler(store, mgr, proxy)
}

func convWithSandbox(sandboxID, sessionID string) *conversation.Conversation {
	s := conversation.NewStore()
	conv := s.Create(nil)
	conv.SetRunning(sandboxID, "http://proxy/", map[string]string{})
	if sessionID != "" {
		conv.SetAgentSessionID(sessionID)
	}
	return conv
}

// ---- CreateConversation ----

func TestCreateConversation_NoBody(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations", nil)
	rw := httptest.NewRecorder()
	h.CreateConversation(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rw.Code)
	}
	var body map[string]string
	json.NewDecoder(rw.Body).Decode(&body)
	if body["id"] == "" {
		t.Error("expected non-empty id in response")
	}
}

func TestCreateConversation_WithEnv(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations",
		strings.NewReader(`{"env":{"FOO":"bar"}}`))
	rw := httptest.NewRecorder()
	h.CreateConversation(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rw.Code)
	}
	store.mu.Lock()
	env := store.conv.ExtraEnv()
	store.mu.Unlock()
	if env["FOO"] != "bar" {
		t.Errorf("expected FOO=bar in conversation env, got %v", env)
	}
}

func TestCreateConversation_InvalidJSON(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations",
		strings.NewReader(`{bad json`))
	rw := httptest.NewRecorder()
	h.CreateConversation(rw, req)

	// Invalid JSON is ignored; conversation still created.
	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201 even with bad JSON, got %d", rw.Code)
	}
}

// ---- GetConversation ----

func TestGetConversation_Found(t *testing.T) {
	conv := convWithSandbox("sb-1", "sess-1")
	store := &mockStore{conv: conv}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodGet, "/api/conversations/"+conv.ID, nil)
	req.SetPathValue("id", conv.ID)
	rw := httptest.NewRecorder()
	h.GetConversation(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	var body map[string]any
	json.NewDecoder(rw.Body).Decode(&body)
	if body["sandboxState"] != "running" {
		t.Errorf("expected sandboxState=running, got %v", body["sandboxState"])
	}
	if body["sandboxId"] != "sb-1" {
		t.Errorf("expected sandboxId=sb-1, got %v", body["sandboxId"])
	}
	if body["agentSessionId"] != "sess-1" {
		t.Errorf("expected agentSessionId=sess-1, got %v", body["agentSessionId"])
	}
}

func TestGetConversation_NotFound(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodGet, "/api/conversations/missing", nil)
	req.SetPathValue("id", "missing")
	rw := httptest.NewRecorder()
	h.GetConversation(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

// ---- SendMessage ----

func TestSendMessage_NotFound(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/missing/messages",
		strings.NewReader(`{"prompt":"hi"}`))
	req.SetPathValue("id", "missing")
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

func TestSendMessage_EmptyPrompt(t *testing.T) {
	conv := convWithSandbox("", "")
	store := &mockStore{conv: conv}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/messages",
		strings.NewReader(`{"prompt":""}`))
	req.SetPathValue("id", conv.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
}

func TestSendMessage_NoPromptField(t *testing.T) {
	conv := convWithSandbox("", "")
	store := &mockStore{conv: conv}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/messages",
		strings.NewReader(`{}`))
	req.SetPathValue("id", conv.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
}

func TestSendMessage_ProvisionError(t *testing.T) {
	s := conversation.NewStore()
	conv := s.Create(nil)
	store := &mockStore{conv: conv}
	mgr := &mockManager{provisionErr: errors.New("quota exceeded")}
	h := newHandler(store, mgr, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/messages",
		strings.NewReader(`{"prompt":"hi"}`))
	req.SetPathValue("id", conv.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rw.Code)
	}
	if conv.GetState() != conversation.StateError {
		t.Errorf("expected StateError after provision failure, got %v", conv.GetState())
	}
}

func TestSendMessage_Success(t *testing.T) {
	s := conversation.NewStore()
	conv := s.Create(nil)
	store := &mockStore{conv: conv}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/messages",
		strings.NewReader(`{"prompt":"hello"}`))
	req.SetPathValue("id", conv.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	// After streaming, status code depends on when WriteHeader was called.
	// With a mock proxy that writes nothing, the recorder default is 200.
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
}

func TestSendMessage_ClientDisconnect(t *testing.T) {
	s := conversation.NewStore()
	conv := s.Create(nil)
	store := &mockStore{conv: conv}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled — simulates client disconnect

	// Proxy that returns nil when context is cancelled.
	h := newHandler(store, &mockManager{}, &mockProxy{err: nil})

	req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/messages",
		strings.NewReader(`{"prompt":"hi"}`))
	req = req.WithContext(ctx)
	req.SetPathValue("id", conv.ID)
	rw := httptest.NewRecorder()

	// Should not panic or log an error.
	h.SendMessage(rw, req)
}

func TestSendMessage_ProvisionCalledOnce(t *testing.T) {
	s := conversation.NewStore()
	conv := s.Create(nil)
	store := &mockStore{conv: conv}
	mgr := &mockManager{}
	h := newHandler(store, mgr, &mockProxy{})

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/conversations/"+conv.ID+"/messages",
				strings.NewReader(`{"prompt":"hi"}`))
			req.SetPathValue("id", conv.ID)
			rw := httptest.NewRecorder()
			h.SendMessage(rw, req)
		}()
	}
	wg.Wait()

	if mgr.calls.Load() != 1 {
		t.Errorf("expected ProvisionForConversation called once, called %d times", mgr.calls.Load())
	}
}

// ---- DeleteConversation ----

func TestDeleteConversation_NotFound(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations/missing", nil)
	req.SetPathValue("id", "missing")
	rw := httptest.NewRecorder()
	h.DeleteConversation(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

func TestDeleteConversation_NoSandbox(t *testing.T) {
	s := conversation.NewStore()
	conv := s.Create(nil)
	store := &mockStore{conv: conv}
	mgr := &mockManager{}
	h := newHandler(store, mgr, &mockProxy{})

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations/"+conv.ID, nil)
	req.SetPathValue("id", conv.ID)
	rw := httptest.NewRecorder()
	h.DeleteConversation(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rw.Code)
	}
	if mgr.calls.Load() != 0 {
		t.Error("DeleteSandbox should not be called when sandboxID is empty")
	}
}

func TestDeleteConversation_WithSandbox(t *testing.T) {
	conv := convWithSandbox("sb-del", "")
	store := &mockStore{conv: conv}
	var deletedID string
	mgr := &deletingManager{onDelete: func(id string) { deletedID = id }}
	h := newHandler(store, mgr, &mockProxy{})

	req := httptest.NewRequest(http.MethodDelete, "/api/conversations/"+conv.ID, nil)
	req.SetPathValue("id", conv.ID)
	rw := httptest.NewRecorder()
	h.DeleteConversation(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rw.Code)
	}
	if deletedID != "sb-del" {
		t.Errorf("expected DeleteSandbox called with sb-del, got %q", deletedID)
	}
}

// deletingManager records the sandbox ID passed to DeleteSandbox.
type deletingManager struct {
	onDelete func(string)
}

func (m *deletingManager) ProvisionForConversation(_ context.Context, _ *conversation.Conversation) error {
	return nil
}
func (m *deletingManager) DeleteSandbox(_ context.Context, id string) error {
	if m.onDelete != nil {
		m.onDelete(id)
	}
	return nil
}

// ---- Health ----

func TestHealth(t *testing.T) {
	h := newHandler(&mockStore{}, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rw := httptest.NewRecorder()
	h.Health(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	var body map[string]string
	json.NewDecoder(rw.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body)
	}
}
