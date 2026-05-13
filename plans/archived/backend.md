# Backend Reference Plan (Go)

Location: `/Users/didi/lucas/backend/`

> **Status**: implemented. This document describes the as-built design.

## Key References
- OpenSandbox Lifecycle API spec: `../Opensandbox/specs/sandbox-lifecycle.yml`
- claude-agent-server OpenAPI: `../Opensandbox/sandboxes/claude-agent-server/docs/openapi.json`
- Overview + API contract: `./overview.md`
- Runtime reference: `../backend/README.md`

---

## File Structure

```
backend/
├── cmd/server/main.go                  # entry point: load config, wire deps, start server
├── internal/
│   ├── api/
│   │   ├── router.go                   # ServeMux routes + CORS middleware
│   │   └── handlers.go                 # HTTP handlers
│   ├── sandbox/
│   │   ├── client.go                   # native HTTP client for OpenSandbox lifecycle API + types
│   │   ├── manager.go                  # sandbox lifecycle: create → poll → health-check → ready
│   │   └── proxy.go                    # SSE pipe from claude-agent-server to browser
│   └── conversation/
│       └── store.go                    # in-memory conversation state + sync primitives
├── pkg/
│   ├── config/
│   │   └── config.go                   # YAML config loader with defaults
│   └── constants/
│       └── constants.go                # default Anthropic base URL and model
├── docs/                               # generated Swagger/OpenAPI 2.0 spec (do not edit manually)
│   ├── docs.go
│   ├── swagger.json
│   └── swagger.yaml
├── go.mod                              # module: github.com/your-org/platform-backend, go 1.22
└── go.sum
```

---

## Config (`pkg/config/config.go`)

Config is loaded from a YAML file (`config.yaml` by default). The loader sets these defaults before unmarshalling:

| Field | Default |
|---|---|
| `server.port` | `"8081"` |
| `server.cors_origin` | `"http://localhost:5173"` |
| `sandbox.server_url` | `"http://localhost:8080"` |
| `sandbox.image` | `"opensandbox/claude-agent-server:latest"` |

All other fields are empty/nil by default.

Config struct shape:

```go
type Config struct {
    Server    ServerConfig
    Sandbox   SandboxConfig    // api_key, server_url, image, platform (optional)
    Anthropic AnthropicConfig  // api_key, base_url, model, disable_experimental_betas
    OrangeFS  OrangeFSConfig   // addr, volume
}
```

---

## Entry Point (`cmd/server/main.go`)

1. Parse `-config` flag (default `config.yaml`)
2. Load config via `config.Load`
3. Build `baseEnv` map: always includes `ANTHROPIC_API_KEY` and `PORT=3000`; conditionally adds `ANTHROPIC_BASE_URL`, `ANTHROPIC_MODEL`, `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS`, `ORANGEFS_RS_ADDR`, `ORANGEFS_VOLUME` if non-empty
4. Construct `PlatformSpec` if both `platform.os` and `platform.arch` are set
5. Wire `conversation.Store`, `sandbox.Manager`, `api.Router`
6. `http.ListenAndServe`

---

## Conversation Store (`internal/conversation/store.go`)

In-memory state protected by `sync.RWMutex`:

```go
type State int
const (
    StateNew          State = iota
    StateProvisioning       // sandbox being created
    StateRunning            // sandbox up, agent ready
    StateError
)

type Conversation struct {
    ID             string
    state          State
    sandboxID      string
    proxyBaseURL   string
    proxyHeaders   map[string]string
    agentSessionID string
    extraEnv       map[string]string  // per-conversation env from POST body
    once           sync.Once
    provisionErr   error
}
```

`State.String()` maps both `StateNew` and `StateProvisioning` → `"provisioning"`.

`Store.Create(extraEnv)` accepts optional extra env vars that are merged into the sandbox at provision time. `EnsureProvisioned(fn)` calls `fn` exactly once; concurrent callers block until done.

---

## Sandbox Manager (`internal/sandbox/manager.go`)

Uses a native HTTP client (`internal/sandbox/client.go`) — no SDK.

**`ProvisionForConversation` flow:**

1. Merge `baseEnv` with `conv.ExtraEnv()` (per-conversation overrides win)
2. `POST /v1/sandboxes` with image, platform, entrypoint, timeout (3600s), resource limits, env
3. Poll `GET /v1/sandboxes/:id` every 2s until `Running` (90s timeout); fail on `Failed`/`Terminated`
4. Construct `proxyBaseURL = {serverURL}/sandboxes/{sandboxID}/proxy/3000` directly
5. Build `proxyHeaders = {"Authorization": "Bearer <apiKey>"}` (if apiKey non-empty)
6. Health-check: poll `GET {proxyBaseURL}/health` every 2s until `{"healthy": true}` (60s timeout)
7. `conv.SetRunning(sandboxID, proxyBaseURL, proxyHeaders)`

**Auth split:**
- Lifecycle calls (`client.go`): `OPEN-SANDBOX-API-KEY: <key>`
- Proxy calls (`manager.go`, `proxy.go`): `Authorization: Bearer <key>`

---

## SSE Proxy (`internal/sandbox/proxy.go`)

**`StreamMessage(ctx, conv, prompt, w)` flow:**

1. If `conv.GetAgentSessionID() == ""`: `POST {proxyBaseURL}/sessions`
   Else: `POST {proxyBaseURL}/sessions/{agentSessionID}/messages`
2. Body: `{"prompt": "...", "stream": true}`
3. Forward `proxyHeaders` on the upstream request
4. Set browser response headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`
5. Scan line by line:
   - On `session.init` data: extract `sessionId`, call `conv.SetAgentSessionID`
   - All lines: forwarded verbatim
   - Flush after each line
6. Client disconnect handled by `ctx` cancellation

---

## API Documentation (Swagger)

Swagger UI is served at `/swagger/index.html` via `gin-swagger`. The OpenAPI 2.0 spec is generated from Go comment annotations using [`swag`](https://github.com/swaggo/swag):

```bash
swag init -g cmd/server/main.go --output docs --parseDependency --parseInternal
```

- General metadata (`@title`, `@version`, `@host`, `@BasePath`) is in `cmd/server/main.go`
- Per-endpoint annotations are in `internal/api/handlers.go`
- Generated output committed under `docs/` (`docs.go`, `swagger.json`, `swagger.yaml`)
- The docs blank-import (`_ "github.com/your-org/platform-backend/docs"`) is in `internal/api/router.go`

---

## HTTP Handlers (`internal/api/handlers.go`)

```
CreateConversation  POST /api/conversations
    body (optional): { "env": { "KEY": "VALUE" } }
    → 201 { "id": "<uuid>" }

SendMessage         POST /api/conversations/{id}/messages
    body: { "prompt": "..." }
    → SetProvisioning → EnsureProvisioned → StreamMessage
    → 502 on provision failure, 200 SSE on success

GetConversation     GET /api/conversations/{id}
    → 200 { "id", "sandboxState", "sandboxId", "agentSessionId" }

DeleteConversation  DELETE /api/conversations/{id}
    → delete from store → async DeleteSandbox → 204

Health              GET /health
    → 200 { "status": "ok" }
```

---

## Router (`internal/api/router.go`)

Uses Go 1.22+ stdlib mux (`net/http.ServeMux` with method+path patterns). CORS middleware allows `cfg.CORSOrigin`, methods `GET, POST, DELETE, OPTIONS`, headers `Content-Type`.

---

## Notes & Gotchas

- `ProvisionForConversation` takes 10-30s cold (sandbox start + health check). Frontend must handle latency (StatusBadge "Starting workspace…").
- The health check bridges the gap between the container reaching `Running` state and the agent server process being ready. Auth errors from the health endpoint are not retried.
- `AgentSessionID` is set from the first `session.init` SSE event. Until it's set, `GetAgentSessionID()` returns `""` which routes to `POST /sessions`. After it's set all subsequent messages use `POST /sessions/{sid}/messages`.
- `context.Background()` is intentional for provisioning: client disconnects don't abort sandbox creation so reconnects can reuse it.
- Per-conversation `extraEnv` (from `POST /api/conversations` body) overrides `baseEnv` keys.
