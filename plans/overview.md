# Platform Overview

## What We're Building

A new product platform based on OpenSandbox. v1 is a chat interface where users talk to a Claude agent running inside a sandbox. The sandbox is provisioned **lazily** — only when the user sends their first message.

## Repo Layout

```
/Users/didi/lucas/
├── Opensandbox/          ← existing platform (do not modify)
│   ├── server/           ← Python FastAPI (sandbox lifecycle API on :8080)
│   └── sandboxes/claude-agent-server/  ← TypeScript agent server (runs inside sandbox on :3000)
├── backend/              ← Go HTTP server (:8081)
├── frontend/             ← Vite + React + shadcn/ui (:5173)
└── plans/                ← this directory
```

## System Architecture

```
Browser
  │
  │  POST /api/conversations
  │  POST /api/conversations/:id/messages  ← SSE stream
  ▼
Go backend (:8081)
  │
  │  [first message] → POST /v1/sandboxes           (OpenSandbox Lifecycle API)
  │  [poll until Running] GET /v1/sandboxes/:id
  │  [health check] GET {serverURL}/sandboxes/:id/proxy/3000/health
  │
  │  proxyBaseURL = {serverURL}/sandboxes/:id/proxy/3000  (constructed directly)
  │
  │  POST {proxyBaseURL}/sessions                     (first message)
  │  POST {proxyBaseURL}/sessions/:sid/messages       (subsequent messages)
  │  ← pipe SSE back to browser
  ▼
OpenSandbox server (:8080)
  └─ /sandboxes/:id/proxy/3000  →  container:3000  →  claude-agent-server
```

## Key OpenSandbox Context

### Lifecycle API (server at :8080)

Auth header: `OPEN-SANDBOX-API-KEY: <key>`

| Call | Method + Path | Notes |
|------|--------------|-------|
| Create sandbox | `POST /v1/sandboxes` | Returns 202, sandbox starts async |
| Poll status | `GET /v1/sandboxes/:id` | Poll until `status.state == "Running"` |
| Delete sandbox | `DELETE /v1/sandboxes/:id` | 204, async teardown |

Create request body:
```json
{
  "image": {"uri": "opensandbox/claude-agent-server:latest"},
  "entrypoint": ["/entrypoint.sh"],
  "timeout": 3600,
  "resourceLimits": {"cpu": "500m", "memory": "512Mi"},
  "platform": {"os": "linux", "arch": "amd64"},
  "env": {
    "ANTHROPIC_API_KEY": "<key>",
    "PORT": "3000"
  }
}
```

`platform` is optional — omit to let the server decide. The backend uses a native `net/http` client (no SDK).

### Proxy URL construction

After the sandbox reaches `Running`, the proxy base URL is constructed directly (no endpoint-resolution call needed):

```
proxyBaseURL = {serverURL}/sandboxes/{sandboxID}/proxy/3000
```

Proxy requests use `Authorization: Bearer <apiKey>`.

### Health check

Before marking the conversation `Running`, the backend polls `GET {proxyBaseURL}/health` until the agent server returns `{"healthy": true}` (2s interval, 60s timeout).

### claude-agent-server API (proxied via OpenSandbox, port 3000)

All requests go through: `{serverURL}/sandboxes/:sandboxId/proxy/3000/...`

| Call | Method + Path | Notes |
|------|--------------|-------|
| Create session + send prompt | `POST /sessions` | Body: `{prompt, stream:true}` → SSE |
| Send follow-up message | `POST /sessions/:sid/messages` | Body: `{prompt, stream:true}` → SSE |

**SSE events emitted** (stream protocol):
```
event: session.init       → {sessionId, model, cwd, ...}   ← extract sessionId here
event: message.assistant  → {text, ...}                    ← stream text to UI
event: session.status     → {status, ...}                  ← "running" / "idle"
event: task.started       → {description, taskType, ...}   ← tool activity
event: task.progress      → {description, lastToolName, ...}
event: result             → {totalCostUsd, numTurns, stopReason, ...}
event: session.completed  → terminal event
event: error              → {message, code}
```

## API Contract (Frontend ↔ Backend)

```
POST   /api/conversations
  body: { "env": { "KEY": "val" } }  (optional — extra env vars merged into sandbox)
  → 201 { id: string }

POST   /api/conversations/:id/messages
  body: { prompt: string }
  → 200 text/event-stream  (proxied verbatim from claude-agent-server)

GET    /api/conversations/:id
  → 200 { id, sandboxState: "provisioning"|"running"|"error", sandboxId?, agentSessionId? }

DELETE /api/conversations/:id
  → 204  (tears down sandbox)

GET    /health
  → 200 { status: "ok" }
```

## Configuration

The backend reads `config.yaml` (override with `-config` flag). See `backend/config.example.yaml` for the full template.

### Backend config fields

```yaml
server:
  port: "8081"
  cors_origin: "http://localhost:5173"

sandbox:
  api_key: ...         # required
  server_url: "http://localhost:8080"
  image: "opensandbox/claude-agent-server:latest"
  # platform: { os: linux, arch: amd64 }  # optional

anthropic:
  api_key: ...         # required
  base_url: ""         # optional
  model: ""            # optional
  disable_experimental_betas: ""  # set to "1" to disable

orangefs:
  addr: ""             # optional
  volume: ""           # optional
```

### Frontend
```
VITE_API_BASE=  (leave empty — Vite dev proxy handles /api/* → :8081)
```

## Verification (end-to-end)

1. Start OpenSandbox server: `cd Opensandbox && docker compose up opensandbox-server`
2. Start backend: `cd backend && go run ./cmd/server`
3. Start frontend: `cd frontend && npm run dev`
4. Open `http://localhost:5173`
5. Confirm: empty chat page titled "Lucas", no sandbox created yet
6. Type a message → send
7. StatusBadge shows "Starting workspace…" while sandbox provisions
8. Backend log: `sandbox <id> created, waiting for Running state` → `waiting for agent server health` → `sandbox <id> ready`
9. Response streams into browser
10. Send a second message — no new sandbox created (same convId reused)
11. `DELETE /api/conversations/:id` → sandbox removed in OpenSandbox
