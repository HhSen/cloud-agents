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

	"github.com/your-org/platform-backend/internal/task"
)

// ---- mock types ----

type mockStore struct {
	mu   sync.Mutex
	task *task.Task
}

func (m *mockStore) Create(username string, env map[string]string) *task.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := task.NewStore()
	m.task = s.Create(username, env)
	return m.task
}

func (m *mockStore) Get(id string) *task.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.task != nil && m.task.ID == id {
		return m.task
	}
	return nil
}

func (m *mockStore) Delete(_ string) {}

type mockManager struct {
	provisionErr error
	calls        atomic.Int32
	sandboxAlive bool
	aliveErr     error
}

func (m *mockManager) ProvisionForTask(_ context.Context, _ *task.Task) error {
	m.calls.Add(1)
	return m.provisionErr
}

func (m *mockManager) DeleteSandbox(_ context.Context, _ string) error { return nil }

func (m *mockManager) IsSandboxAlive(_ context.Context, _ string) (bool, error) {
	return m.sandboxAlive, m.aliveErr
}

type mockProxy struct {
	err error
}

func (m *mockProxy) StreamMessage(_ context.Context, _ *task.Task, _ string, w http.ResponseWriter) error {
	return m.err
}

// ---- helpers ----

func newHandler(store TaskStore, mgr SandboxManager, proxy MessageProxy) *Handler {
	return NewHandler(store, mgr, proxy, nil)
}

func taskWithSandbox(sandboxID, sessionID string) *task.Task {
	s := task.NewStore()
	t := s.Create("", nil)
	t.SetRunning(sandboxID, "http://proxy/", map[string]string{})
	if sessionID != "" {
		t.SetAgentSessionID(sessionID)
	}
	return t
}

// provisionedTask returns a task that went through EnsureProvisioned so
// provisioned=true, simulating a session that was previously fully provisioned.
func provisionedTask(sandboxID string) *task.Task {
	s := task.NewStore()
	t := s.Create("", nil)
	t.EnsureProvisioned(func() error {
		t.SetRunning(sandboxID, "http://proxy/", map[string]string{})
		return nil
	})
	return t
}

// ---- CreateTask ----

func TestCreateTask_NoBody(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks", nil)
	rw := httptest.NewRecorder()
	h.CreateTask(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rw.Code)
	}
	var body map[string]string
	json.NewDecoder(rw.Body).Decode(&body)
	if body["id"] == "" {
		t.Error("expected non-empty id in response")
	}
}

func TestCreateTask_WithEnv(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks",
		strings.NewReader(`{"env":{"FOO":"bar"}}`))
	rw := httptest.NewRecorder()
	h.CreateTask(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rw.Code)
	}
	store.mu.Lock()
	env := store.task.ExtraEnv()
	store.mu.Unlock()
	if env["FOO"] != "bar" {
		t.Errorf("expected FOO=bar in task env, got %v", env)
	}
}

func TestCreateTask_WithUsername(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks",
		strings.NewReader(`{"username":"alice"}`))
	rw := httptest.NewRecorder()
	h.CreateTask(rw, req)

	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rw.Code)
	}
	store.mu.Lock()
	username := store.task.Username
	store.mu.Unlock()
	if username != "alice" {
		t.Errorf("expected Username=alice, got %q", username)
	}
}

func TestCreateTask_InvalidJSON(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks",
		strings.NewReader(`{bad json`))
	rw := httptest.NewRecorder()
	h.CreateTask(rw, req)

	// Invalid JSON is ignored; task still created.
	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201 even with bad JSON, got %d", rw.Code)
	}
}

// ---- GetTask ----

func TestGetTask_Found(t *testing.T) {
	tsk := taskWithSandbox("sb-1", "sess-1")
	store := &mockStore{task: tsk}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+tsk.ID, nil)
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.GetTask(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	var body map[string]any
	json.NewDecoder(rw.Body).Decode(&body)
	if body["sandbox_state"] != "running" {
		t.Errorf("expected sandbox_state=running, got %v", body["sandbox_state"])
	}
	if body["sandbox_id"] != "sb-1" {
		t.Errorf("expected sandbox_id=sb-1, got %v", body["sandbox_id"])
	}
	if body["agent_session_id"] != "sess-1" {
		t.Errorf("expected agent_session_id=sess-1, got %v", body["agent_session_id"])
	}
}

func TestGetTask_NotFound(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/missing", nil)
	req.SetPathValue("id", "missing")
	rw := httptest.NewRecorder()
	h.GetTask(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

// ---- SendMessage ----

func TestSendMessage_NotFound(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/missing/messages",
		strings.NewReader(`{"prompt":"hi"}`))
	req.SetPathValue("id", "missing")
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

func TestSendMessage_EmptyPrompt(t *testing.T) {
	tsk := taskWithSandbox("", "")
	store := &mockStore{task: tsk}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tsk.ID+"/messages",
		strings.NewReader(`{"prompt":""}`))
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
}

func TestSendMessage_NoPromptField(t *testing.T) {
	tsk := taskWithSandbox("", "")
	store := &mockStore{task: tsk}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tsk.ID+"/messages",
		strings.NewReader(`{}`))
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rw.Code)
	}
}

func TestSendMessage_ProvisionError(t *testing.T) {
	s := task.NewStore()
	tsk := s.Create("", nil)
	store := &mockStore{task: tsk}
	mgr := &mockManager{provisionErr: errors.New("quota exceeded")}
	h := newHandler(store, mgr, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tsk.ID+"/messages",
		strings.NewReader(`{"prompt":"hi"}`))
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rw.Code)
	}
	if tsk.GetState() != task.StateError {
		t.Errorf("expected StateError after provision failure, got %v", tsk.GetState())
	}
}

func TestSendMessage_Success(t *testing.T) {
	s := task.NewStore()
	tsk := s.Create("", nil)
	store := &mockStore{task: tsk}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tsk.ID+"/messages",
		strings.NewReader(`{"prompt":"hello"}`))
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
}

func TestSendMessage_ClientDisconnect(t *testing.T) {
	s := task.NewStore()
	tsk := s.Create("", nil)
	store := &mockStore{task: tsk}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled — simulates client disconnect

	h := newHandler(store, &mockManager{}, &mockProxy{err: nil})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tsk.ID+"/messages",
		strings.NewReader(`{"prompt":"hi"}`))
	req = req.WithContext(ctx)
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()

	h.SendMessage(rw, req)
}

func TestSendMessage_ProvisionCalledOnce(t *testing.T) {
	s := task.NewStore()
	tsk := s.Create("", nil)
	store := &mockStore{task: tsk}
	mgr := &mockManager{}
	h := newHandler(store, mgr, &mockProxy{})

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tsk.ID+"/messages",
				strings.NewReader(`{"prompt":"hi"}`))
			req.SetPathValue("id", tsk.ID)
			rw := httptest.NewRecorder()
			h.SendMessage(rw, req)
		}()
	}
	wg.Wait()

	if mgr.calls.Load() != 1 {
		t.Errorf("expected ProvisionForTask called once, called %d times", mgr.calls.Load())
	}
}

func TestSendMessage_SandboxAlive_NoReprovision(t *testing.T) {
	tsk := provisionedTask("sb-alive")
	store := &mockStore{task: tsk}
	mgr := &mockManager{sandboxAlive: true}
	h := newHandler(store, mgr, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tsk.ID+"/messages",
		strings.NewReader(`{"prompt":"hi"}`))
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rw.Code)
	}
	if mgr.calls.Load() != 0 {
		t.Errorf("expected no re-provisioning when sandbox is alive, got %d calls", mgr.calls.Load())
	}
}

func TestSendMessage_SandboxExpired_Reprovisions(t *testing.T) {
	tsk := provisionedTask("sb-expired")
	store := &mockStore{task: tsk}
	mgr := &mockManager{sandboxAlive: false}
	h := newHandler(store, mgr, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tsk.ID+"/messages",
		strings.NewReader(`{"prompt":"hello after expiry"}`))
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200 after re-provision, got %d", rw.Code)
	}
	if mgr.calls.Load() != 1 {
		t.Errorf("expected ProvisionForTask called once for re-provision, got %d", mgr.calls.Load())
	}
}

func TestSendMessage_SandboxAliveCheckError_Continues(t *testing.T) {
	tsk := provisionedTask("sb-check-err")
	store := &mockStore{task: tsk}
	mgr := &mockManager{sandboxAlive: false, aliveErr: errors.New("network timeout")}
	h := newHandler(store, mgr, &mockProxy{})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+tsk.ID+"/messages",
		strings.NewReader(`{"prompt":"hi"}`))
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.SendMessage(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200 when alive check errors, got %d", rw.Code)
	}
	if mgr.calls.Load() != 0 {
		t.Errorf("expected no re-provisioning on alive check error, got %d calls", mgr.calls.Load())
	}
}

// ---- DeleteTask ----

func TestDeleteTask_NotFound(t *testing.T) {
	store := &mockStore{}
	h := newHandler(store, &mockManager{}, &mockProxy{})

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/missing", nil)
	req.SetPathValue("id", "missing")
	rw := httptest.NewRecorder()
	h.DeleteTask(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rw.Code)
	}
}

func TestDeleteTask_NoSandbox(t *testing.T) {
	s := task.NewStore()
	tsk := s.Create("", nil)
	store := &mockStore{task: tsk}
	mgr := &mockManager{}
	h := newHandler(store, mgr, &mockProxy{})

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/"+tsk.ID, nil)
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.DeleteTask(rw, req)

	if rw.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rw.Code)
	}
	if mgr.calls.Load() != 0 {
		t.Error("DeleteSandbox should not be called when sandboxID is empty")
	}
}

func TestDeleteTask_WithSandbox(t *testing.T) {
	tsk := taskWithSandbox("sb-del", "")
	store := &mockStore{task: tsk}
	var deletedID string
	mgr := &deletingManager{onDelete: func(id string) { deletedID = id }}
	h := newHandler(store, mgr, &mockProxy{})

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/"+tsk.ID, nil)
	req.SetPathValue("id", tsk.ID)
	rw := httptest.NewRecorder()
	h.DeleteTask(rw, req)

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

func (m *deletingManager) ProvisionForTask(_ context.Context, _ *task.Task) error { return nil }
func (m *deletingManager) DeleteSandbox(_ context.Context, id string) error {
	if m.onDelete != nil {
		m.onDelete(id)
	}
	return nil
}
func (m *deletingManager) IsSandboxAlive(_ context.Context, _ string) (bool, error) {
	return true, nil
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
