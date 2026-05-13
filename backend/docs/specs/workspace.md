# Workspace Panel: Sandbox Filesystem Access

## Overview

The workspace panel lets the frontend browse the sandbox's working directory in real time. It is a read-only file tree with an inline file viewer. The panel appears as a third column on the right side of `ChatPage`.

```
┌──────────────┬──────────────────────┬──────────────────┐
│  History     │    Session view       │   Workspace      │
│  sidepanel   │    (chat messages)    │   (file tree)    │
└──────────────┴──────────────────────┴──────────────────┘
```

---

## Request Path

All filesystem operations are proxied through the backend to the **execd** daemon running on port 44772 inside the sandbox container.

```
Frontend
  │  GET /api/tasks/:id/execd/files/search?path=...
  ▼
Go backend  (ExecdProxy handler)
  │  GET {serverURL}/sandboxes/{sandboxID}/proxy/44772/files/search?path=...
  │  Header: X-OPEN-SANDBOX-API-KEY: <apiKey>
  ▼
OpenSandbox server (:8080)
  └─ /sandboxes/:id/proxy/44772  →  container:44772  →  execd
```

The backend constructs the execd URL directly from:

```go
target := fmt.Sprintf("%s/sandboxes/%s/proxy/44772%s", h.serverURL, sandboxID, subpath)
```

This is identical to the pattern used by the existing `writeFile` / `makeDirAll` calls in `sandbox/manager.go`. No separate execd access token is required — the OpenSandbox proxy accepts the lifecycle API key (`X-OPEN-SANDBOX-API-KEY`) and forwards the request internally.

---

## Backend: ExecdProxy Handler

**Route**: `ANY /api/tasks/:id/execd/*path` (protected, bearer auth)

The handler:
1. Looks up the task by `:id`; returns 404 if not found.
2. Reads `task.GetSandboxID()`; returns 409 if the sandbox is not running.
3. Constructs the execd target URL and forwards the original method + query string + body.
4. Sets `X-OPEN-SANDBOX-API-KEY` on the upstream request.
5. Copies the upstream response status, headers, and body verbatim to the client.

`ANY` registration lets the proxy forward both `GET` (file listing, download) and `POST` (directories, future commands) without separate route entries.

**Handler fields** added to `api.Handler`:

| Field | Type | Purpose |
|---|---|---|
| `serverURL` | `string` | OpenSandbox lifecycle server base URL |
| `sandboxAPIKey` | `string` | Value for `X-OPEN-SANDBOX-API-KEY` header |
| `httpClient` | `*http.Client` | Reused HTTP client (30 s timeout) |

These are populated in `NewRouter` via `h.withExecd(cfg.Sandbox.ServerURL, cfg.Sandbox.APIKey)`.

---

## Execd Filesystem Endpoints Used

| Operation | Method | Path | Key params |
|---|---|---|---|
| List directory | `GET` | `/files/search` | `path=<dir>`, `pattern=*` |
| Read file | `GET` | `/files/download` | `path=<file>` |

`/files/search` with `pattern=*` returns one level of directory entries. The frontend triggers a second request per directory node when the user expands it (lazy loading).

`/files/download` returns raw file bytes. Binary files are detected client-side by scanning the first 512 bytes for null characters.

---

## CWD: Working Directory

The sandbox CWD is set at provisioning time:

```go
// sandbox/proxy.go
CWD: fmt.Sprintf("/workspace/%s/%s", t.Username, t.ID)
```

The claude-agent-server emits it in the `session.init` SSE event:

```
event: session.init
data: {"sessionId": "...", "cwd": "/workspace/alice/task-abc123", ...}
```

The frontend hook (`useChat.ts`) extracts `data.cwd` from this event and exposes it as `cwd`. `ChatPage` passes `cwd` as a prop to `WorkspacePanel`. The workspace panel is only rendered (and the file tree only fetched) once both `taskId` and `cwd` are non-null.

---

## Refresh Strategy

| Trigger | Mechanism |
|---|---|
| Manual | Refresh button in panel header calls `fetchRoot()` |
| After agent completes | `session.completed` SSE → `onSessionCompleted` callback → increments `refreshToken` in `ChatPage` → passed as prop to `WorkspacePanel` → `useEffect` refetches root |

There is no polling during agent execution to avoid racing with in-progress file writes.

---

## Error States

| Condition | Behaviour |
|---|---|
| Sandbox not running | Backend returns 409; frontend shows placeholder: *"Workspace available when sandbox is running"* |
| execd unreachable | Backend returns 502; frontend shows error message in the panel |
| Empty directory | Frontend renders *"empty"* label under the expanded node |
| Binary file | Frontend renders *"Binary file, preview not available"* instead of raw content |
