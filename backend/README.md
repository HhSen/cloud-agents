# Backend Reference

Go HTTP server that sits between the browser and OpenSandbox. It manages conversation state, provisions sandboxes on demand, and proxies SSE streams from the claude-agent-server running inside each sandbox.

---

## Running

```bash
cd backend

# copy the example and fill in required fields
cp config.example.yaml config.yaml

go run ./cmd/server
# or: go run ./cmd/server -config /path/to/config.yaml
```

Build a binary:

```bash
go build -o bin/server ./cmd/server
./bin/server
```

---

## Configuration (`config.yaml`)

Config is loaded from a YAML file (`config.yaml` by default; override with `-config <path>`).

```yaml
server:
  port: "8081"          # default
  cors_origin: "http://localhost:5173"  # default

sandbox:
  api_key: your-opensandbox-api-key   # required
  server_url: "http://localhost:8080"  # default
  image: "opensandbox/claude-agent-server:latest"  # default
  # Optional — both os and arch must be set to take effect
  # platform:
  #   os: linux
  #   arch: amd64

anthropic:
  api_key: your-anthropic-api-key   # required — injected into sandbox as ANTHROPIC_API_KEY
  base_url: ""   # optional — leave empty for api.anthropic.com
  model: ""      # optional — injected as ANTHROPIC_MODEL
  disable_experimental_betas: ""  # set to "1" to disable — injected as CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS

orangefs:
  addr: ""    # optional — injected as ORANGEFS_RS_ADDR
  volume: ""  # optional — injected as ORANGEFS_VOLUME
```

See `config.example.yaml` for the full annotated template.

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
│   │   ├── manager.go            # sandbox lifecycle: create → poll → health-check → proxy URL
│   │   └── proxy.go              # SSE pipe from claude-agent-server to browser
│   └── conversation/
│       └── store.go              # in-memory conversation state + sync primitives
├── pkg/
│   ├── config/
│   │   └── config.go             # YAML config loader with defaults
│   └── constants/
│       └── constants.go          # default Anthropic base URL and model
├── go.mod
└── go.sum
```

---

## API Endpoints

| Method   | Path                                  | Status | Body / Response                                              |
|----------|---------------------------------------|--------|--------------------------------------------------------------|
| `POST`   | `/api/conversations`                  | 201    | `{ "env": {...} }` (optional) → `{ "id": "<uuid>" }`       |
| `POST`   | `/api/conversations/:id/messages`     | 200    | `{ "prompt": "..." }` → `text/event-stream` (SSE)           |
| `GET`    | `/api/conversations/:id`              | 200    | `{ id, sandboxState, sandboxId, agentSessionId }`           |
| `DELETE` | `/api/conversations/:id`              | 204    | tears down sandbox, removes conversation from store         |
| `GET`    | `/health`                             | 200    | `{ "status": "ok" }`                                        |

`sandboxState` values: `"provisioning"` · `"running"` · `"error"`

The optional `env` body on `POST /api/conversations` merges additional environment variables into the sandbox at provision time (overrides the base env from config).

### SSE stream format (proxied verbatim from claude-agent-server)

```
event: session.init
data: {"sessionId":"abc123","model":"claude-sonnet-4-6",...}

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
  │  POST /v1/sandboxes              → create sandbox
  │  GET  /v1/sandboxes/:id          → poll until state == "Running"
  │  GET  {serverURL}/sandboxes/:id/proxy/3000/health  → poll until {"healthy":true}
  │
  │  proxyBaseURL = {serverURL}/sandboxes/:id/proxy/3000  (constructed directly)
  │
  │  POST {proxyBaseURL}/sessions                       → first message
  │  POST {proxyBaseURL}/sessions/:sid/messages         → follow-up
  │  ← pipe SSE back verbatim
  ▼
OpenSandbox server (:8080)
  └─ /sandboxes/:id/proxy/3000  →  container port 3000  →  claude-agent-server
```

**Authorization:**
- Lifecycle API (`POST/GET/DELETE /v1/sandboxes`): `OPEN-SANDBOX-API-KEY: <key>` header
- Proxy requests (`POST {proxyBaseURL}/...`): `Authorization: Bearer <key>` header

---

## Conversation State Machine

```
StateNew
  │
  │  first SendMessage call
  ▼
StateProvisioning  ←── sync.Once ensures only one goroutine runs this
  │
  │  1. sandbox Created (POST /v1/sandboxes)
  │  2. poll until Running (GET /v1/sandboxes/:id, 2s interval, 90s timeout)
  │  3. health-check agent server (GET {proxyBaseURL}/health, 2s interval, 60s timeout)
  ▼
StateRunning
  │
  │  DELETE /api/conversations/:id
  ▼
(removed from store)
```

Both `StateNew` and `StateProvisioning` serialize to `"provisioning"` in the API response. `StateError` is set if `ProvisionForConversation` returns an error; subsequent message attempts return 502.

---

## Key Patterns

### Lazy provisioning with sync.Once

The sandbox is created only when the first message arrives. `sync.Once` on the `Conversation` struct ensures concurrent requests to the same conversation block until provisioning finishes.

```go
err := conv.EnsureProvisioned(func() error {
    return h.manager.ProvisionForConversation(provisionCtx, conv)
})
```

`context.Background()` is used for provisioning: if the client disconnects, the sandbox creation continues so a reconnect can reuse it.

### Proxy URL construction

After the sandbox reaches `Running`, the proxy URL is constructed directly — no `GetEndpoint` call needed:

```
proxyBaseURL = {serverURL}/sandboxes/{sandboxID}/proxy/3000
```

A health check (`GET {proxyBaseURL}/health`) polls until `{"healthy": true}` before the conversation enters `StateRunning`. This bridges the gap between the container starting and the agent server being ready.

### SSE proxy with session ID extraction

The proxy pipes the upstream response line by line. On the `session.init` event it extracts `sessionId` and stores it on the conversation, which becomes the path for all follow-up messages.

```
First message:    POST {proxyBaseURL}/sessions
Follow-up:        POST {proxyBaseURL}/sessions/{agentSessionID}/messages
```

### Native HTTP client (no SDK)

The backend calls the OpenSandbox lifecycle API directly via `net/http` (`internal/sandbox/client.go`), giving access to the full API surface — including `platform` constraints.

```go
info, err := lc.CreateSandbox(ctx, CreateSandboxRequest{
    Image:          &ImageSpec{URI: image},
    Platform:       &PlatformSpec{OS: "linux", Arch: "amd64"}, // optional
    Timeout:        &timeout,  // 3600s
    ResourceLimits: ResourceLimits{"cpu": "500m", "memory": "512Mi"},
    Entrypoint:     []string{"/entrypoint.sh"},
    Env:            env,
})
```

### Sandbox environment

The manager builds the sandbox env by merging config-level fields (static for all conversations) with per-conversation `extraEnv` (from the `POST /api/conversations` body):

| Env var | Source |
|---|---|
| `ANTHROPIC_API_KEY` | `anthropic.api_key` (required) |
| `PORT` | hardcoded `3000` |
| `ANTHROPIC_BASE_URL` | `anthropic.base_url` (if non-empty) |
| `ANTHROPIC_MODEL` | `anthropic.model` (if non-empty) |
| `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS` | `anthropic.disable_experimental_betas` (if non-empty) |
| `ORANGEFS_RS_ADDR` | `orangefs.addr` (if non-empty) |
| `ORANGEFS_VOLUME` | `orangefs.volume` (if non-empty) |

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

# create conversation with extra env
curl -X POST http://localhost:8081/api/conversations \
  -H "Content-Type: application/json" \
  -d '{"env": {"MY_VAR": "value"}}'

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
