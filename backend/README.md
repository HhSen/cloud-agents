# Backend Reference

Go HTTP server that sits between the browser and OpenSandbox. It manages task and session state, provisions sandboxes on demand, and proxies SSE streams from the claude-agent-server running inside each sandbox.

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
  port: "8081"                          # default
  cors_origin: "http://localhost:5173"  # default

sandbox:
  api_key: your-opensandbox-api-key     # required
  server_url: "http://localhost:8080"   # default
  image: "opensandbox/claude-agent-server:latest"  # default
  # Optional — both os and arch must be set to take effect
  # platform:
  #   os: linux
  #   arch: amd64

anthropic:
  api_key: your-anthropic-api-key  # required — injected into sandbox as ANTHROPIC_API_KEY
  base_url: ""                     # optional — leave empty for api.anthropic.com
  model: ""                        # optional — injected as ANTHROPIC_MODEL
  disable_experimental_betas: ""   # set to "1" to disable

# Task store — omit or leave url empty to use the in-memory store (lost on restart).
# Set url to enable Redis persistence across restarts and multiple instances.
redis:
  url: ""  # e.g. redis://localhost:6379

orangefs:
  addr: ""        # optional — injected as ORANGEFS_RS_ADDR
  token: ""       # optional — injected as ORANGEFS_TOKEN
  endpoint: ""    # optional — S3-compatible endpoint for history storage
  volume: ""
  access_key: ""
  secret_key: ""
```

See `config.example.yaml` for the full annotated template and [docs/specs/configuration.md](docs/specs/configuration.md) for field-by-field reference.

### Task store selection at startup

| `redis.url` | Store used | Persistence |
|---|---|---|
| empty (default) | In-memory (`MemoryRepository`) | Lost on restart |
| set | Redis (`RedisRepository`) | Survives restarts; shared across instances |

When Redis is configured the server pings it at startup and exits immediately if unreachable.

---

## File Structure

```
backend/
├── cmd/server/
│   └── main.go                    # entry point: load config, wire deps, start server
├── internal/
│   ├── api/
│   │   ├── router.go              # ServeMux routes + CORS middleware
│   │   ├── handlers.go            # HTTP handlers (one per endpoint)
│   │   └── types.go               # request / response structs
│   ├── sandbox/
│   │   ├── client.go              # HTTP client for OpenSandbox lifecycle API
│   │   ├── manager.go             # sandbox lifecycle: create → poll → health-check
│   │   └── proxy.go               # SSE pipe from claude-agent-server to browser
│   ├── storage/
│   │   └── client.go              # OFS (S3-compatible) client for conversation history
│   └── task/
│       ├── repository.go          # Repository interface + taskOps interface
│       ├── store.go               # Task struct, in-process mutation methods, MemoryRepository
│       └── redis_repository.go    # RedisRepository + distributed lock (redisTaskOps)
├── pkg/
│   ├── config/
│   │   └── config.go              # YAML config loader with defaults
│   └── constants/
│       └── constants.go
├── docs/
│   ├── docs.go                    # generated Swagger registration (do not edit manually)
│   ├── swagger.json               # generated OpenAPI 2.0 spec
│   ├── swagger.yaml               # generated OpenAPI 2.0 spec (YAML)
│   └── specs/
│       ├── configuration.md       # Full configuration field reference
│       ├── resource-mapping.md    # Task / Sandbox / Session lifecycle and invariants
│       ├── redis-storage.md       # Redis data model and key operations
│       └── ofsspec.md             # OFS file layout for session history
├── go.mod
└── go.sum
```

---

## API Endpoints

Interactive docs (Swagger UI) available at **`http://localhost:8081/swagger/index.html`** when the server is running.

| Method | Path | Status | Description |
|---|---|---|---|
| `POST` | `/api/tasks` | 201 | Create a task → `{ "id": "<uuid>" }` |
| `POST` | `/api/tasks/:id/messages` | 200 | Send a message (SSE stream) |
| `GET` | `/api/tasks/:id` | 200 | Get task state |
| `GET` | `/api/tasks/:id/history` | 200 | Get conversation history from OFS |
| `DELETE` | `/api/tasks/:id` | 204 | Delete task and tear down sandbox |
| `GET` | `/health` | 200 | Liveness probe → `{ "status": "ok" }` |
| `GET` | `/swagger/*` | — | Swagger UI |

### POST /api/tasks — request body (optional)

```json
{ "username": "alice", "env": { "KEY": "VALUE" } }
```

`env` merges additional environment variables into the sandbox at provision time (overrides the base env from config).

### GET /api/tasks/:id — response

```json
{
  "id": "a1b2c3...",
  "username": "alice",
  "state": "active",
  "sandbox_id": "sb-xyz",
  "session_id": "sess-abc"
}
```

`state` values: `pending` · `provisioning` · `idle` · `active` · `paused` · `resuming` · `error`

See [docs/specs/resource-mapping.md](docs/specs/resource-mapping.md) for the full state table.

### SSE stream format (proxied verbatim from claude-agent-server)

```
event: session.init
data: {"sessionId":"abc123","model":"claude-sonnet-4-6",...}

event: message.assistant
data: {"text":"Hello!","uuid":"..."}

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
  │  POST /api/tasks/:id/messages  { prompt }
  ▼
Go backend (:8081)
  │
  │  [first message — EnsureProvisioned]
  │  POST /v1/sandboxes              → create sandbox
  │  GET  /v1/sandboxes/:id          → poll until state == "Running"
  │  GET  {serverURL}/sandboxes/:id/proxy/3000/health  → poll until {"healthy":true}
  │  task.SetRunning(sandboxID, proxyBaseURL, headers) persisted to store
  │
  │  proxyBaseURL = {serverURL}/sandboxes/:id/proxy/3000
  │
  │  POST {proxyBaseURL}/sessions                       → first message
  │  POST {proxyBaseURL}/sessions/:sid/messages         → follow-up
  │  ← pipe SSE back verbatim; extract session.init → task.SetSessionID
  ▼
OpenSandbox server (:8080)
  └─ /sandboxes/:id/proxy/3000  →  container port 3000  →  claude-agent-server
```

**Authorization:**
- Lifecycle API (`POST/GET/DELETE /v1/sandboxes`): `OPEN-SANDBOX-API-KEY: <key>` header
- Proxy requests (`POST {proxyBaseURL}/...`): `Authorization: Bearer <key>` header

---

## Task State Machine

```
StateNew (0)
  │
  │  first SendMessage → SetProvisioning()
  ▼
StateProvisioning (1)  ←── EnsureProvisioned / distributed lock ensures one runner
  │
  │  1. CreateSandbox (POST /v1/sandboxes)
  │  2. Poll until Running (2 s interval, 90 s timeout)
  │  3. Health-check agent server (2 s interval, 60 s timeout)
  │  4. SetRunning(sandboxID, proxyBaseURL, headers)
  ▼
StateRunning (2)  ←── sandbox alive
  │
  │  [sandbox expires or is destroyed]
  │  ResetIfExpired → clears sandbox fields, back to StateNew
  │
  │  [DELETE /api/tasks/:id]
  ▼
(removed from store)
```

The API-visible state label is derived from the sandbox `state` combined with whether `session_id` is set. For example, `StateNew + session_id set = "paused"`.

`StateError (3)` is set when provisioning fails; subsequent message attempts return 502.

---

## Key Patterns

### Lazy provisioning with distributed lock

The sandbox is created only when the first message arrives. `EnsureProvisioned` on `Task` serialises concurrent callers:

- **In-memory store**: uses an in-process `sync.Mutex` (`provisionMu`).
- **Redis store**: acquires `task-lock:{id}` (SET NX, 30 s TTL), checks `provisioned` field in Redis, runs fn if `"0"`, verifies `state == Running` was persisted, then sets `provisioned=1`. Lock is released via Lua CAS on success or error.

```go
err = t.EnsureProvisioned(func() error {
    return h.manager.ProvisionForTask(context.Background(), t)
})
```

`context.Background()` is used so provisioning survives client disconnects.

### Sandbox expiry detection

Before every message, `ResetIfExpired` checks whether the current sandbox is still alive:

```go
t.ResetIfExpired(func(sandboxID string) (bool, error) {
    return h.manager.IsSandboxAlive(ctx, sandboxID)
})
```

If the sandbox has expired, all sandbox fields (`state`, `sandbox_id`, `proxy_base_url`, `proxy_headers`, `provisioned`) are cleared, and the next `EnsureProvisioned` call re-provisions a new one. `session_id` is **never** cleared — the existing session history in OFS remains accessible.

### Task persistence

Task state is stored in one of two backends, selected at startup:

| Backend | Key type | Locking |
|---|---|---|
| `MemoryRepository` | `map[string]*Task` | `sync.RWMutex` + per-task `sync.Mutex` |
| `RedisRepository` | Hash `task:{id}` | Redis lock `task-lock:{id}` |

See [docs/specs/redis-storage.md](docs/specs/redis-storage.md) for the full Redis data model, key operations, and a lifecycle walkthrough.

### Session ID (write-once)

`session_id` is extracted from the `session.init` SSE event on the first message and stored on the task. It is **never cleared or replaced** once set (invariant #4 in resource-mapping.md). This enables history reads from OFS even when no sandbox is active.

- In-memory: in-process mutex check (`if sessionID == "" { set }`).
- Redis: Lua `HSETNX` script enforces atomicity across instances.

### Sandbox environment

The manager builds the sandbox env by merging config-level fields with per-task `extraEnv`:

| Env var | Source |
|---|---|
| `ANTHROPIC_API_KEY` | `anthropic.api_key` (required) |
| `PORT` | hardcoded `3000` |
| `USERNAME` | task `username` field |
| `TASK_ID` | task `id` — keys OFS storage |
| `ANTHROPIC_BASE_URL` | `anthropic.base_url` (if set) |
| `ANTHROPIC_MODEL` | `anthropic.model` (if set) |
| `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS` | `anthropic.disable_experimental_betas` (if set) |
| `ORANGEFS_RS_ADDR` | `orangefs.addr` (if set) |
| `ORANGEFS_TOKEN` | `orangefs.token` (if set) |
| `ORANGEFS_VOLUME` | `orangefs.volume` (if set) |

---

## Smoke Tests

```bash
# health
curl http://localhost:8081/health

# create task
curl -X POST http://localhost:8081/api/tasks
# → {"id":"<uuid>"}

# create task with username and extra env
curl -X POST http://localhost:8081/api/tasks \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","env":{"MY_VAR":"value"}}'

# send message (streams SSE)
curl -X POST http://localhost:8081/api/tasks/<id>/messages \
  -H "Content-Type: application/json" \
  -d '{"prompt":"say hello"}' \
  --no-buffer

# get task state
curl http://localhost:8081/api/tasks/<id>

# get conversation history (requires OFS)
curl http://localhost:8081/api/tasks/<id>/history

# delete task + tear down sandbox
curl -X DELETE http://localhost:8081/api/tasks/<id>
```

---

## API Documentation

The Swagger spec is generated from Go comment annotations by [`swag`](https://github.com/swaggo/swag). The generated files in `docs/` are committed and should be regenerated whenever handler annotations change.

```bash
# Install swag CLI (one-time)
go install github.com/swaggo/swag/cmd/swag@latest

# Regenerate from the backend root
swag init -g cmd/server/main.go --output docs --parseDependency --parseInternal
```

General API metadata (`@title`, `@version`, `@host`, `@BasePath`) lives at the top of `cmd/server/main.go`. Per-endpoint annotations (`@Summary`, `@Param`, `@Success`, etc.) are in `internal/api/handlers.go`.

---

## Adding a New Endpoint

1. Add a handler method to `Handler` in `internal/api/handlers.go` with Swagger annotations
2. Register the route in `internal/api/router.go`
3. Run `swag init -g cmd/server/main.go --output docs --parseDependency --parseInternal` to regenerate docs
