package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/your-org/platform-backend/internal/storage"
	"github.com/your-org/platform-backend/internal/task"
)

// TaskStore manages the lifecycle of Task records in memory.
type TaskStore interface {
	// Create initialises a new Task owned by username with optional extra environment
	// variables and returns it. The caller owns the returned pointer.
	Create(username string, extraEnv map[string]string) *task.Task
	// Get returns the Task with the given ID, or nil if it does not exist.
	Get(id string) *task.Task
	// Delete removes the Task with the given ID from the store.
	Delete(id string)
}

// SandboxManager provisions and tears down the compute sandbox that backs a task.
type SandboxManager interface {
	// ProvisionForTask allocates a sandbox for t and attaches its ID to t.
	ProvisionForTask(ctx context.Context, t *task.Task) error
	// DeleteSandbox destroys the sandbox identified by sandboxID.
	DeleteSandbox(ctx context.Context, sandboxID string) error
	// IsSandboxAlive reports whether sandboxID is still in Running state.
	// Returns (false, nil) when the sandbox has expired or been terminated.
	IsSandboxAlive(ctx context.Context, sandboxID string) (bool, error)
}

// FileStore retrieves task history from OFS-backed file storage.
type FileStore interface {
	ListHistory(ctx context.Context, username, taskID string) ([]string, error)
	GetHistory(ctx context.Context, key string) ([]storage.ConversationEntry, error)
	GetSessionMeta(ctx context.Context, username, taskID string) (*storage.SessionMeta, error)
}

// MessageProxy streams a prompt from the client through to the task's sandbox.
type MessageProxy interface {
	// StreamMessage forwards prompt to the sandbox associated with t and writes
	// the streamed response directly to w.
	StreamMessage(ctx context.Context, t *task.Task, prompt string, w http.ResponseWriter) error
}

// Handler wires together the store, sandbox manager, message proxy, and file store
// to serve the tasks REST API.
type Handler struct {
	store     TaskStore
	manager   SandboxManager
	proxy     MessageProxy
	fileStore FileStore
}

// NewHandler constructs a Handler from its dependencies.
func NewHandler(store TaskStore, mgr SandboxManager, proxy MessageProxy, fileStore FileStore) *Handler {
	return &Handler{
		store:     store,
		manager:   mgr,
		proxy:     proxy,
		fileStore: fileStore,
	}
}

// CreateTask handles POST /api/tasks.
//
// Request body (optional JSON):
//
//	{ "username": "alice", "env": { "KEY": "VALUE" } }
//
// Response 201 JSON:
//
//	{ "id": "<task-id>" }
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	var body createTaskRequest
	// body is optional — ignore decode errors
	json.NewDecoder(r.Body).Decode(&body)

	t := h.store.Create(body.Username, body.Env)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(createTaskResponse{ID: t.ID})
}

// SendMessage handles POST /api/tasks/{id}/messages.
//
// Lazily provisions the task's sandbox on first use, then streams the
// assistant response back to the caller. Provisioning runs under a background
// context so that a client disconnect does not abort it.
//
// Request body (JSON):
//
//	{ "prompt": "<user message>" }
//
// Response: streamed assistant output (content-type set by the proxy).
// Errors:
//   - 400 Bad Request  – prompt missing or body unreadable
//   - 404 Not Found    – unknown task ID
//   - 502 Bad Gateway  – sandbox provisioning failed
func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t := h.store.Get(id)
	if t == nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	var body sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	// Sandboxes expire silently via TTL. ResetIfExpired checks liveness under the
	// provisioning lock so a concurrent re-provision cannot be stomped by a racing reset.
	if err := t.ResetIfExpired(func(sandboxID string) (bool, error) {
		aliveCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		alive, err := h.manager.IsSandboxAlive(aliveCtx, sandboxID)
		if err != nil {
			log.Printf("sandbox status check failed for task %s sandbox %s: %v", id, sandboxID, err)
			return true, err // treat check error as alive — proxy will surface real errors
		}
		if !alive {
			log.Printf("sandbox %s expired for task %s, re-provisioning", sandboxID, id)
		}
		return alive, nil
	}); err != nil {
		log.Printf("sandbox liveness check error for task %s, proceeding: %v", id, err)
	}

	if t.GetState() == task.StateNew {
		t.SetProvisioning()
	}

	// Use background context so provisioning survives client disconnects.
	provisionCtx := context.Background()
	err := t.EnsureProvisioned(func() error {
		return h.manager.ProvisionForTask(provisionCtx, t)
	})
	if err != nil {
		t.SetError()
		log.Printf("provision failed for task %s: %v", id, err)
		http.Error(w, "failed to provision sandbox", http.StatusBadGateway)
		return
	}

	if err := h.proxy.StreamMessage(r.Context(), t, body.Prompt, w); err != nil {
		if r.Context().Err() != nil {
			return // client disconnected
		}
		log.Printf("stream error for task %s: %v", id, err)
	}
}

// GetTask handles GET /api/tasks/{id}.
//
// Response 200 JSON:
//
//	{
//	  "id":               "<task-id>",
//	  "username":         "<username>",
//	  "sandbox_state":    "<state-string>",
//	  "sandbox_id":       "<sandbox-id>",
//	  "agent_session_id": "<session-id>"
//	}
//
// Errors:
//   - 404 Not Found – unknown task ID
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t := h.store.Get(id)
	if t == nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	_, sandboxID, agentSessionID, state := t.Info()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(getTaskResponse{
		ID:             id,
		Username:       t.Username,
		SandboxState:   state.String(),
		SandboxID:      sandboxID,
		AgentSessionID: agentSessionID,
	})
}

// DeleteTask handles DELETE /api/tasks/{id}.
//
// Removes the task from the store and asynchronously destroys its sandbox.
// Sandbox deletion errors are logged but do not affect the response.
//
// Response 204 No Content on success.
// Errors:
//   - 404 Not Found – unknown task ID
func (h *Handler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t := h.store.Get(id)
	if t == nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	sandboxID := t.GetSandboxID()
	h.store.Delete(id)

	if sandboxID != "" {
		if err := h.manager.DeleteSandbox(context.Background(), sandboxID); err != nil {
			log.Printf("delete sandbox %s: %v", sandboxID, err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetTaskHistory handles GET /api/tasks/{id}/history.
//
// Returns the full conversation history stored in OFS for the task.
//
// Response 200 JSON: array of ConversationEntry objects (may be empty if no
// history has been written yet).
// Errors:
//   - 404 Not Found           – unknown task ID
//   - 503 Service Unavailable – file storage not configured
//   - 500 Internal Error      – storage read failure
func (h *Handler) GetTaskHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t := h.store.Get(id)
	if t == nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	if h.fileStore == nil {
		http.Error(w, "history storage not configured", http.StatusServiceUnavailable)
		return
	}

	keys, err := h.fileStore.ListHistory(r.Context(), t.Username, id)
	if err != nil {
		log.Printf("list history for task %s: %v", id, err)
		http.Error(w, "failed to list history", http.StatusInternalServerError)
		return
	}

	entries := make([]storage.ConversationEntry, 0)
	if len(keys) > 0 {
		entries, err = h.fileStore.GetHistory(r.Context(), keys[0])
		if err != nil {
			log.Printf("get history for task %s: %v", id, err)
			http.Error(w, "failed to get history", http.StatusInternalServerError)
			return
		}
		if entries == nil {
			entries = make([]storage.ConversationEntry, 0)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// Health handles GET /health.
//
// Response 200 JSON:
//
//	{ "status": "ok" }
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{Status: "ok"})
}
