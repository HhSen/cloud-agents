# Platform Overview

## What We're Building

A new product platform based on OpenSandbox. v1 is a chat interface where users talk to a Claude agent running inside a sandbox. The sandbox is provisioned **lazily** — only when the user sends their first message.

## Repo Layout

```
/Users/didi/lucas/
├── Opensandbox/          ← existing platform (do not modify)
│   ├── server/           ← Python FastAPI (sandbox lifecycle API on :8080)
│   ├── sandboxes/claude-agent-server/  ← TypeScript agent server (runs inside sandbox on :3000)
│   └── sdks/sandbox/go/  ← Go SDK (use via replace directive)
├── backend/              ← NEW: Go HTTP server (:8081)
├── frontend/             ← NEW: Vite + React + shadcn/ui (:5173)
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
  │  [first message] → POST /v1/sandboxes       (OpenSandbox Lifecycle API)
  │  [poll until Running] GET /v1/sandboxes/:id
  │  [resolve URL] GET /v1/sandboxes/:id/endpoints/3000
  │  → returns proxy URL: http://localhost:8080/sandboxes/:id/proxy/3000
  │
  │  POST {proxyURL}/sessions           (claude-agent-server — creates session + sends prompt)
  │  POST {proxyURL}/sessions/:sid/messages  (subsequent messages)
  │  ← pipe SSE back to browser
  ▼
OpenSandbox server (:8080)
  └─ /sandboxes/:id/proxy/3000  →  container:3000  →  claude-agent-server
```

## Key OpenSandbox Context

### Lifecycle API (Python server at :8080)

Auth header: `OPEN-SANDBOX-API-KEY: <key>`

| Call | Method + Path | Notes |
|------|--------------|-------|
| Create sandbox | `POST /v1/sandboxes` | Returns 202, sandbox starts async |
| Poll status | `GET /v1/sandboxes/:id` | Poll until `status.state == "Running"` |
| Get proxy URL | `GET /v1/sandboxes/:id/endpoints/3000` | Returns `{endpoint: "host/path", headers: {...}}` |
| Delete sandbox | `DELETE /v1/sandboxes/:id` | 204, async teardown |

Create request body (minimum):
```json
{
  "image": "opensandbox/claude-agent-server:latest",
  "entrypoint": ["/entrypoint.sh"],
  "timeout": 3600,
  "env": {
    "ANTHROPIC_API_KEY": "<key>",
    "PORT": "3000"
  }
}
```

### claude-agent-server API (proxied via OpenSandbox, port 3000)

All requests go through: `http://localhost:8080/sandboxes/:sandboxId/proxy/3000/...`

| Call | Method + Path | Notes |
|------|--------------|-------|
| Create session + send prompt | `POST /sessions` | Body: `{prompt, stream:true}` → SSE |
| Send follow-up message | `POST /sessions/:sid/messages` | Body: `{prompt, stream:true}` → SSE |
| List sessions | `GET /sessions` | — |
| Abort | `POST /sessions/:sid/abort` | — |

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

### Go SDK

Module: `github.com/alibaba/OpenSandbox/sdks/sandbox/go`  
Local path: `../Opensandbox/sdks/sandbox/go` (use `replace` in go.mod)

Key types/functions:
```go
opensandbox.CreateSandbox(ctx, connectionConfig, SandboxCreateOptions{...})
opensandbox.ConnectionConfig{Domain: "localhost:8080", APIKey: "..."}
sandbox.GetEndpoint(ctx, 3000)  // returns Endpoint{endpoint, headers}
sandbox.Info()                  // returns SandboxInfo{Status: {State: "Running"}}
```

## API Contract (Frontend ↔ Backend)

```
POST   /api/conversations
  → 201 { id: string }

POST   /api/conversations/:id/messages
  body: { prompt: string }
  → 200 text/event-stream  (proxied verbatim from claude-agent-server)

GET    /api/conversations/:id
  → 200 { id, sandboxState: "provisioning"|"running"|"error", agentSessionId? }

DELETE /api/conversations/:id
  → 204  (tears down sandbox)

GET    /health
  → 200 { status: "ok" }
```

## Environment Variables

### Backend
```
OPENSANDBOX_SERVER_URL=http://localhost:8080
OPENSANDBOX_API_KEY=...
ANTHROPIC_API_KEY=...
SANDBOX_IMAGE=opensandbox/claude-agent-server:latest
PORT=8081
CORS_ORIGIN=http://localhost:5173
```

### Frontend
```
VITE_API_BASE=http://localhost:8081
```
(Or use Vite dev proxy to avoid CORS — see frontend.md)

## Verification (end-to-end)

1. Start OpenSandbox server: `cd Opensandbox && docker compose up opensandbox-server`
2. Start backend: `cd backend && go run ./cmd/server`
3. Start frontend: `cd frontend && npm run dev`
4. Open `http://localhost:5173`
5. Confirm: empty chat page, no sandbox created yet
6. Type a message → send
7. Backend log: `creating sandbox... polling... Running... proxy URL resolved... SSE pipe open`
8. Response streams into browser
9. Send a second message — no new sandbox created (same convId reused)
10. `DELETE /api/conversations/:id` → sandbox removed in OpenSandbox
