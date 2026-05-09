package api

import (
	"net/http"

	"github.com/your-org/platform-backend/internal/sandbox"
)

func NewRouter(store ConversationStore, mgr SandboxManager, corsOrigin string) http.Handler {
	h := NewHandler(store, mgr, sandbox.NewProxy())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/conversations", h.CreateConversation)
	mux.HandleFunc("POST /api/conversations/{id}/messages", h.SendMessage)
	mux.HandleFunc("GET /api/conversations/{id}", h.GetConversation)
	mux.HandleFunc("DELETE /api/conversations/{id}", h.DeleteConversation)
	mux.HandleFunc("GET /health", h.Health)

	return corsMiddleware(corsOrigin, mux)
}

func corsMiddleware(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
