# Backend Plan (Go)

Location: `/Users/didi/lucas/backend/`

## Key References
- OpenSandbox Go SDK: `../Opensandbox/sdks/sandbox/go` (module `github.com/alibaba/OpenSandbox/sdks/sandbox/go`)
- OpenSandbox Lifecycle API spec: `../Opensandbox/specs/sandbox-lifecycle.yml`
- claude-agent-server OpenAPI: `../Opensandbox/sandboxes/claude-agent-server/docs/openapi.json`
- Overview + API contract: `./overview.md`

---

## Step 1 — Scaffold the module

```
backend/
├── cmd/server/main.go
├── internal/
│   ├── api/
│   │   ├── router.go
│   │   └── handlers.go
│   ├── sandbox/
│   │   ├── manager.go
│   │   └── proxy.go
│   └── conversation/
│       └── store.go
├── go.mod
└── go.sum
```

**go.mod** — use `replace` for local Go SDK:
```go
module github.com/your-org/platform-backend

go 1.21

require (
    github.com/alibaba/OpenSandbox/sdks/sandbox/go v0.0.0
)

replace github.com/alibaba/OpenSandbox/sdks/sandbox/go => ../Opensandbox/sdks/sandbox/go
```

Run `go mod tidy` to pull in transitive deps.

---

## Step 2 — Config & entry point (`cmd/server/main.go`)

Read env vars at startup (no config file needed for v1):

```go
type Config struct {
    Port                string // default "8081"
    OpenSandboxURL      string // e.g. "http://localhost:8080"
    OpenSandboxAPIKey   string
    AnthropicAPIKey     string
    SandboxImage        string // e.g. "opensandbox/claude-agent-server:latest"
    CORSOrigin          string // e.g. "http://localhost:5173"
}
```

`main.go` responsibilities:
1. Load config from env
2. Build `opensandbox.ConnectionConfig{Domain: cfg.OpenSandboxURL, APIKey: cfg.OpenSandboxAPIKey}`
3. Construct `sandbox.Manager` and `conversation.Store`
4. Build router and start `http.ListenAndServe`

---

## Step 3 — Conversation store (`internal/conversation/store.go`)

In-memory state (good enough for v1, no persistence needed):

```go
type ConversationState int
const (
    StateNew          ConversationState = iota
    StateProvisioning                   // sandbox being created
    StateRunning                        // sandbox up, agent ready
    StateError
)

type Conversation struct {
    ID             string
    State          ConversationState
    SandboxID      string
    ProxyBaseURL   string            // http://localhost:8080/sandboxes/:id/proxy/3000
    ProxyHeaders   map[string]string // headers returned by endpoint resolver
    AgentSessionID string            // set after first SSE session.init event
}

type Store struct {
    mu    sync.RWMutex
    convs map[string]*Conversation
}
```

Methods needed: `Create() *Conversation`, `Get(id) *Conversation`, `Delete(id)`.

---

## Step 4 — Sandbox manager (`internal/sandbox/manager.go`)

Wraps the OpenSandbox Go SDK. Responsibilities:
1. Create sandbox via SDK
2. Poll until `Running` with backoff
3. Resolve proxy URL for port 3000
4. Store results in conversation

**Key SDK usage:**

```go
import opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"

// Create
sb, err := opensandbox.CreateSandbox(ctx, connConfig, opensandbox.SandboxCreateOptions{
    Image:   cfg.SandboxImage,
    Timeout: &timeout,               // seconds, int
    Env: map[string]string{
        "ANTHROPIC_API_KEY": cfg.AnthropicAPIKey,
        "PORT":              "3000",
    },
})

// Poll (every 2s, timeout 90s)
for {
    info, err := sb.Info()
    if info.Status.State == "Running" { break }
    if info.Status.State == "Failed"  { return error }
    time.Sleep(2 * time.Second)
}

// Resolve proxy URL
endpoint, err := sb.GetEndpoint(ctx, 3000)
// endpoint.Endpoint = "localhost:8080/sandboxes/<id>/proxy/3000"
// endpoint.Headers  = map[string]string{"...": "..."}

proxyBaseURL := "http://" + endpoint.Endpoint
```

Note: `GetEndpoint` returns the path/host without scheme. Prepend `http://`.

**Public method signature:**
```go
func (m *Manager) ProvisionForConversation(ctx context.Context, conv *conversation.Conversation) error
```

Updates `conv.SandboxID`, `conv.ProxyBaseURL`, `conv.ProxyHeaders`, sets `conv.State = StateRunning`.

---

## Step 5 — SSE proxy (`internal/sandbox/proxy.go`)

Responsibilities:
1. Build the upstream request to claude-agent-server (through OpenSandbox proxy)
2. Pipe the SSE response stream back to the browser HTTP response
3. Extract `agentSessionID` from the `session.init` event and persist it

**Upstream URL logic:**
```
First message:    POST {conv.ProxyBaseURL}/sessions
Follow-up:        POST {conv.ProxyBaseURL}/sessions/{conv.AgentSessionID}/messages
```

**Request body to claude-agent-server:**
```json
{ "prompt": "<user text>", "stream": true }
```

**SSE parsing to extract sessionId:**
Read the stream line by line. When you see:
```
event: session.init
data: {"sessionId":"abc123",...}
```
Parse the data JSON, save `sessionId` → `conv.AgentSessionID`.  
Then keep piping ALL lines verbatim to the browser.

**Browser response headers to set:**
```
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
X-Accel-Buffering: no
```

**Forward `conv.ProxyHeaders`** as request headers to the upstream call.

---

## Step 6 — HTTP handlers (`internal/api/handlers.go`)

```go
func (h *Handler) CreateConversation(w http.ResponseWriter, r *http.Request)
    // generate uuid, create Conversation{State: StateNew}, return {id}

func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request)
    // 1. get conv from store (404 if missing)
    // 2. if conv.State == StateNew:
    //      set State = StateProvisioning
    //      call manager.ProvisionForConversation(ctx, conv) — blocks until Running or error
    //      on error: set State = StateError, return 502
    // 3. call proxy.StreamMessage(ctx, conv, prompt, w)

func (h *Handler) GetConversation(w http.ResponseWriter, r *http.Request)
    // return conv state + sandboxId

func (h *Handler) DeleteConversation(w http.ResponseWriter, r *http.Request)
    // call opensandbox delete sandbox API, remove from store

func (h *Handler) Health(w http.ResponseWriter, r *http.Request)
    // return {"status":"ok"}
```

---

## Step 7 — Router (`internal/api/router.go`)

Use `net/http` stdlib mux (or `github.com/go-chi/chi/v5`):

```
POST   /api/conversations            → CreateConversation
POST   /api/conversations/{id}/messages → SendMessage
GET    /api/conversations/{id}       → GetConversation
DELETE /api/conversations/{id}       → DeleteConversation
GET    /health                       → Health
```

Add CORS middleware: allow `cfg.CORSOrigin`, methods `GET,POST,DELETE,OPTIONS`, headers `Content-Type`.

---

## Step 8 — Run & verify

```bash
cd backend
go mod tidy
go build ./...                    # should compile clean
go run ./cmd/server

# Quick smoke tests (replace values as needed):
curl -X POST http://localhost:8081/api/conversations
# → {"id":"<uuid>"}

curl -X POST http://localhost:8081/api/conversations/<id>/messages \
  -H "Content-Type: application/json" \
  -d '{"prompt":"say hello"}' \
  --no-buffer
# → SSE stream with session.init, message.assistant, etc.
```

---

## Notes & Gotchas

- `ProvisionForConversation` will take 10-30s on cold start. The frontend must handle this latency (show "Starting workspace…" status).
- Concurrent messages to the same conversation while provisioning: use a mutex or a `sync.Once` per conversation so only one goroutine provisions.
- The `ProxyBaseURL` from `GetEndpoint` may not include the scheme — always prepend `http://`.
- claude-agent-server's `session.completed` is the terminal SSE event; close the pipe after receiving it.
- If the client disconnects mid-stream, cancel the upstream request via `ctx` cancellation.
- `AgentSessionID` is parsed from the first SSE `session.init` event's JSON `data` field. This ID must be saved before the conversation can accept a second message.
