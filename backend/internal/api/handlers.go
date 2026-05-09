package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/your-org/platform-backend/internal/conversation"
)

type ConversationStore interface {
	Create(extraEnv map[string]string) *conversation.Conversation
	Get(id string) *conversation.Conversation
	Delete(id string)
}

type SandboxManager interface {
	ProvisionForConversation(ctx context.Context, conv *conversation.Conversation) error
	DeleteSandbox(ctx context.Context, sandboxID string) error
}

type MessageProxy interface {
	StreamMessage(ctx context.Context, conv *conversation.Conversation, prompt string, w http.ResponseWriter) error
}

type Handler struct {
	store   ConversationStore
	manager SandboxManager
	proxy   MessageProxy
}

func NewHandler(store ConversationStore, mgr SandboxManager, proxy MessageProxy) *Handler {
	return &Handler{
		store:   store,
		manager: mgr,
		proxy:   proxy,
	}
}

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

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
