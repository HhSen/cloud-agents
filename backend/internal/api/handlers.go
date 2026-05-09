package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/your-org/platform-backend/internal/conversation"
)

// ConversationStore manages the lifecycle of Conversation records in memory.
type ConversationStore interface {
	// Create initialises a new Conversation with optional extra environment variables
	// and returns it. The caller owns the returned pointer.
	Create(extraEnv map[string]string) *conversation.Conversation
	// Get returns the Conversation with the given ID, or nil if it does not exist.
	Get(id string) *conversation.Conversation
	// Delete removes the Conversation with the given ID from the store.
	Delete(id string)
}

// SandboxManager provisions and tears down the compute sandbox that backs a conversation.
type SandboxManager interface {
	// ProvisionForConversation allocates a sandbox for conv and attaches its ID to conv.
	ProvisionForConversation(ctx context.Context, conv *conversation.Conversation) error
	// DeleteSandbox destroys the sandbox identified by sandboxID.
	DeleteSandbox(ctx context.Context, sandboxID string) error
}

// MessageProxy streams a prompt from the client through to the conversation's sandbox.
type MessageProxy interface {
	// StreamMessage forwards prompt to the sandbox associated with conv and writes
	// the streamed response directly to w.
	StreamMessage(ctx context.Context, conv *conversation.Conversation, prompt string, w http.ResponseWriter) error
}

// Handler wires together the store, sandbox manager, and message proxy to serve
// the conversations REST API.
type Handler struct {
	store   ConversationStore
	manager SandboxManager
	proxy   MessageProxy
}

// NewHandler constructs a Handler from its three dependencies.
func NewHandler(store ConversationStore, mgr SandboxManager, proxy MessageProxy) *Handler {
	return &Handler{
		store:   store,
		manager: mgr,
		proxy:   proxy,
	}
}

// CreateConversation handles POST /api/conversations.
//
// Request body (optional JSON):
//
//	{ "env": { "KEY": "VALUE" } }
//
// Response 201 JSON:
//
//	{ "id": "<conversation-id>" }
func (h *Handler) CreateConversation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Env map[string]string `json:"env"`
	}
	// body is optional — ignore decode errors
	json.NewDecoder(r.Body).Decode(&body)

	conv := h.store.Create(body.Env)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": conv.ID})
}

// SendMessage handles POST /api/conversations/{id}/messages.
//
// Lazily provisions the conversation's sandbox on first use, then streams the
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
//   - 404 Not Found    – unknown conversation ID
//   - 502 Bad Gateway  – sandbox provisioning failed
func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conv := h.store.Get(id)
	if conv == nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		http.Error(w, "prompt is required", http.StatusBadRequest)
		return
	}

	// Mark provisioning before entering Once to give callers visibility.
	if conv.GetState() == conversation.StateNew {
		conv.SetProvisioning()
	}

	// Use background context so provisioning survives client disconnects.
	provisionCtx := context.Background()
	err := conv.EnsureProvisioned(func() error {
		return h.manager.ProvisionForConversation(provisionCtx, conv)
	})
	if err != nil {
		conv.SetError()
		log.Printf("provision failed for conv %s: %v", id, err)
		http.Error(w, "failed to provision sandbox", http.StatusBadGateway)
		return
	}

	if err := h.proxy.StreamMessage(r.Context(), conv, body.Prompt, w); err != nil {
		if r.Context().Err() != nil {
			return // client disconnected
		}
		log.Printf("stream error for conv %s: %v", id, err)
	}
}

// GetConversation handles GET /api/conversations/{id}.
//
// Response 200 JSON:
//
//	{
//	  "id":             "<conversation-id>",
//	  "sandboxState":   "<state-string>",
//	  "sandboxId":      "<sandbox-id>",
//	  "agentSessionId": "<session-id>"
//	}
//
// Errors:
//   - 404 Not Found – unknown conversation ID
func (h *Handler) GetConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conv := h.store.Get(id)
	if conv == nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	_, sandboxID, agentSessionID, state := conv.Info()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":             id,
		"sandboxState":   state.String(),
		"sandboxId":      sandboxID,
		"agentSessionId": agentSessionID,
	})
}

// DeleteConversation handles DELETE /api/conversations/{id}.
//
// Removes the conversation from the store and asynchronously destroys its
// sandbox. Sandbox deletion errors are logged but do not affect the response.
//
// Response 204 No Content on success.
// Errors:
//   - 404 Not Found – unknown conversation ID
func (h *Handler) DeleteConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conv := h.store.Get(id)
	if conv == nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}

	sandboxID := conv.GetSandboxID()
	h.store.Delete(id)

	if sandboxID != "" {
		if err := h.manager.DeleteSandbox(context.Background(), sandboxID); err != nil {
			log.Printf("delete sandbox %s: %v", sandboxID, err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// Health handles GET /health.
//
// Response 200 JSON:
//
//	{ "status": "ok" }
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
