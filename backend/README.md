# Backend Reference

Go HTTP server that sits between the browser and OpenSandbox. It manages conversation state, provisions sandboxes on demand, and proxies SSE streams from the claude-agent-server running inside each sandbox.

---

## Running

```bash
cd backend

# required
export OPENSANDBOX_API_KEY=...
export ANTHROPIC_API_KEY=...

# optional (these are the defaults)
export OPENSANDBOX_SERVER_URL=http://localhost:8080
export SANDBOX_IMAGE=opensandbox/claude-agent-server:latest
export PORT=8081
export CORS_ORIGIN=http://localhost:5173

# optional platform constraint (omit to let the server decide)
export SANDBOX_PLATFORM_OS=linux
export SANDBOX_PLATFORM_ARCH=amd64

go run ./cmd/server
```

Build a binary:

```bash
go build -o bin/server ./cmd/server
./bin/server
```

---

## File Structure

```
backend/
├── cmd/server/
│   └── main.go                   # entry point: load config, wire deps, start server
├── internal/
│   ├── api/
│   │   ├── router.go             # ServeMux routes + CORS middleware
│   │   └── handlers.go           # HTTP handlers (one per endpoint)
│   ├── sandbox/
│   │   ├── client.go             # native HTTP client for OpenSandbox lifecycle API + types
│   │   ├── manager.go            # sandbox lifecycle: create → poll → build proxy URL
│   │   └── proxy.go              # SSE pipe from claude-agent-server to browser
│   └── conversation/
│       └── store.go              # in-memory conversation state + sync primitives
├── go.mod
└── go.sum
```

---

## API Endpoints

| Method   | Path                                  | Status | Body / Response                                              |
|----------|---------------------------------------|--------|--------------------------------------------------------------|
| `POST`   | `/api/conversations`                  | 201    | `{}` → `{ "id": "<uuid>" }`                                 |
| `POST`   | `/api/conversations/:id/messages`     | 200    | `{ "prompt": "..." }` → `text/event-stream` (SSE)           |
| `GET`    | `/api/conversations/:id`              | 200    | `{ id, sandboxState, sandboxId, agentSessionId }`           |
| `DELETE` | `/api/conversations/:id`              | 204    | tears down sandbox, removes conversation from store         |
| `GET`    | `/health`                             | 200    | `{ "status": "ok" }`                                        |

`sandboxState` values: `"provisioning"` · `"running"` · `"error"`

### SSE stream format (proxied verbatim from claude-agent-server)

```
event: session.init
data: {"sessionId":"abc123","model":"claude-opus-4-7",...}

event: message.assistant
data: {"text":"Hello!","uuid":"..."}

event: session.status
data: {"status":"running"}

event: task.started
data: {"description":"Running bash command","taskType":"tool_use"}

event: task.progress
data: {"description":"...","lastToolName":"bash"}

event: result
data: {"totalCostUsd":0.002,"numTurns":1,"stopReason":"end_turn"}

event: session.completed
data: {"sessionId":"abc123"}

event: error
data: {"message":"...","code":500}
```

---

## Architecture

```
Browser
  │
  │  POST /api/conversations/:id/messages  { prompt }
  ▼
Go backend (:8081)
  │
  │  [first message only]
  │  POST /v1/sandboxes         → create sandbox
  │  GET  /v1/sandboxes/:id     → poll until state == "Running"
  │
  │  POST {serverURL}/sandboxes/:id/proxy/3000/sessions               → first message
  │  POST {serverURL}/sandboxes/:id/proxy/3000/sessions/:sid/messages → follow-up
  │  ← pipe SSE back verbatim
  ▼
OpenSandbox server (:8080)
  └─ /sandboxes/:id/proxy/3000  →  container port 3000  →  claude-agent-server
```

---

## Conversation State Machine

```
StateNew
  │
  │  first SendMessage call
  ▼
StateProvisioning  ←── sync.Once ensures only one goroutine runs this
  │
  │  sandbox Running + endpoint resolved
  ▼
StateRunning
  │
  │  DELETE /api/conversations/:id
  ▼
(removed from store)
```

`StateError` is set if `ProvisionForConversation` returns an error. The conversation stays in the store and returns 502 on subsequent message attempts.

---

## Key Patterns

### Lazy provisioning with sync.Once

The sandbox is created only when the first message arrives. `sync.Once` on the `Conversation` struct ensures concurrent requests to the same conversation block until provisioning finishes — no double-provisioning, no mutex soup.

```go
// handlers.go
err := conv.EnsureProvisioned(func() error {
    return h.manager.ProvisionForConversation(context.Background(), conv)
})
```

`context.Background()` is intentional: if the client disconnects mid-provision, the sandbox creation continues so a reconnect can reuse it.

### SSE proxy with session ID extraction

The proxy pipes the upstream response line-by-line. On the first `session.init` event it extracts and stores the `sessionId`, which becomes the session path for all follow-up messages.

```
First message:    POST {proxyBaseURL}/sessions
Follow-up:        POST {proxyBaseURL}/sessions/{agentSessionID}/messages
```

Client disconnection is handled by `r.Context()` cancellation, which aborts the upstream `http.Client` request.

### Native HTTP client (no SDK)

The backend calls the OpenSandbox lifecycle API directly via `net/http` rather than the SDK, giving access to the full API surface — including `platform` constraints not exposed by the SDK.

After the sandbox reaches `Running`, the proxy URL is constructed directly from the server URL — no `GetEndpoint` call needed. The OpenSandbox server's built-in proxy (`/sandboxes/:id/proxy/:port/:path`) forwards requests into the container.

```go
// internal/sandbox/client.go
lc := newAPILifecycleClient(serverURL, apiKey)

info, err := lc.CreateSandbox(ctx, CreateSandboxRequest{
    Image:          &ImageSpec{URI: image},
    Platform:       &PlatformSpec{OS: "linux", Arch: "amd64"}, // optional
    ResourceLimits: ResourceLimits{"cpu": "500m", "memory": "512Mi"},
    Entrypoint:     []string{"/entrypoint.sh"},
    Env:            env,
})
// poll lc.GetSandbox until state == StateRunning

proxyBaseURL := serverURL + "/sandboxes/" + sandboxID + "/proxy/3000"
// auth forwarded via: Authorization: Bearer <apiKey>
```

---

## Environment Variables

| Variable                 | Default                                      | Description                                        |
|--------------------------|----------------------------------------------|----------------------------------------------------|
| `PORT`                   | `8081`                                       | Port the server listens on                         |
| `OPENSANDBOX_SERVER_URL` | `http://localhost:8080`                      | OpenSandbox lifecycle API base URL                 |
| `OPENSANDBOX_API_KEY`    | _(required)_                                 | Auth key for `OPEN-SANDBOX-API-KEY` header         |
| `ANTHROPIC_API_KEY`      | _(required)_                                 | Injected into sandbox env at creation              |
| `SANDBOX_IMAGE`          | `opensandbox/claude-agent-server:latest`     | Container image for agent sandboxes                |
| `SANDBOX_PLATFORM_OS`    | _(omitted)_                                  | Sandbox platform OS (`linux`). Both OS and Arch must be set to take effect. |
| `SANDBOX_PLATFORM_ARCH`  | _(omitted)_                                  | Sandbox platform architecture (`amd64`, `arm64`). Both OS and Arch must be set to take effect. |
| `CORS_ORIGIN`            | `http://localhost:5173`                      | Allowed CORS origin                                |

---

## Adding a New Endpoint

1. Add a handler method to `Handler` in `internal/api/handlers.go`
2. Register the route in `internal/api/router.go`
3. Use `r.PathValue("param")` for URL parameters (Go 1.22+ stdlib mux)

---

## Smoke Tests

```bash
# health
curl http://localhost:8081/health

# create conversation
curl -X POST http://localhost:8081/api/conversations
# → {"id":"<uuid>"}

# send message (streams SSE)
curl -X POST http://localhost:8081/api/conversations/<id>/messages \
  -H "Content-Type: application/json" \
  -d '{"prompt":"say hello"}' \
  --no-buffer

# get conversation state
curl http://localhost:8081/api/conversations/<id>

# delete conversation + tear down sandbox
curl -X DELETE http://localhost:8081/api/conversations/<id>
```
