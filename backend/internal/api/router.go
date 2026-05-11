package api

import (
	"net/http"

	"github.com/your-org/platform-backend/internal/sandbox"
)

// NewRouter builds the HTTP handler for the tasks API.
//
// Routes registered:
//
//	POST   /api/tasks                  – create a task
//	POST   /api/tasks/{id}/messages    – send a message (streaming)
//	GET    /api/tasks/{id}             – get task state
//	GET    /api/tasks/{id}/history     – get conversation history (requires fileStore)
//	DELETE /api/tasks/{id}             – delete a task
//	GET    /health                     – liveness probe
//
// All routes are wrapped with CORS middleware that allows requests from
// corsOrigin with methods GET, POST, DELETE, and OPTIONS.
func NewRouter(store TaskStore, mgr SandboxManager, corsOrigin string, fileStore FileStore) http.Handler {
	h := NewHandler(store, mgr, sandbox.NewProxy(), fileStore)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/tasks", h.CreateTask)
	mux.HandleFunc("POST /api/tasks/{id}/messages", h.SendMessage)
	mux.HandleFunc("GET /api/tasks/{id}", h.GetTask)
	mux.HandleFunc("GET /api/tasks/{id}/history", h.GetTaskHistory)
	mux.HandleFunc("DELETE /api/tasks/{id}", h.DeleteTask)
	mux.HandleFunc("GET /health", h.Health)

	return corsMiddleware(corsOrigin, mux)
}

// corsMiddleware sets CORS response headers for every request and short-circuits
// pre-flight OPTIONS requests with 204 No Content.
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
