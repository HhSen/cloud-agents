# Workspace Panel — Right Sidepanel for Sandbox Files [COMPLETED 2026-05-13]

## Goal

Add a **Workspace** panel on the right side of `ChatPage` so the final layout is:

```
┌──────────────┬──────────────────────┬──────────────────┐
│  History     │    Session view       │   Workspace      │
│  sidepanel   │    (chat messages)    │   (file tree)    │
│  (existing)  │    (existing)         │   (new)          │
└──────────────┴──────────────────────┴──────────────────┘
```

Scope of this plan: read-only file tree + file viewer. No upload or terminal in this phase.

---

## Architecture Overview

The Execd API (port 44772) runs inside the sandbox. It is reached via the same
OpenSandbox proxy pattern used for the agent server:

```
Frontend
  │  GET /api/tasks/:id/execd/files/search?path=...
  ▼
Go backend
  │  derives execd proxy URL from task's proxyBaseURL:
  │    {serverURL}/sandboxes/{sandboxID}/proxy/44772
  │  forwards request with lifecycle API auth headers
  ▼
OpenSandbox server (:8080)
  └─ /sandboxes/:id/proxy/44772  →  container:44772  →  execd
```

**Execd auth**: Identical to the existing `writeFile`/`makeDirAll` calls in
`manager.go` — send `X-OPEN-SANDBOX-API-KEY: <apiKey>` directly to
`{serverURL}/sandboxes/{sandboxID}/proxy/44772/...`. No separate execd token.

---

## Backend Changes

### 1. Derive execd proxy URL from task

The handler needs the sandbox ID and the lifecycle `serverURL`+`apiKey`.
`task.GetSandboxID()` already provides the sandbox ID. The handler gets
`serverURL` and `apiKey` via fields on `Handler` (same values the manager uses).

Execd URL pattern (matches existing `writeFile`/`makeDirAll`):
```
{serverURL}/sandboxes/{sandboxID}/proxy/44772/{subpath}
```

### 2. New handler: `ExecdProxy`

File: `backend/internal/api/handlers.go`

```go
// @Summary      Proxy execd filesystem/command API
// @Tags         tasks
// @Param        id    path  string  true  "Task ID"
// @Param        path  path  string  true  "Execd API path (e.g. files/search)"
// @Router       /api/tasks/{id}/execd/{path} [get]
func (h *Handler) ExecdProxy(c *gin.Context) {
    taskID := c.Param("id")
    subpath := c.Param("path")       // e.g. "/files/search"

    t, err := h.store.Get(c.Request.Context(), taskID)
    if err != nil { c.JSON(404, gin.H{"error": "task not found"}); return }

    sandboxID := t.GetSandboxID()
    if sandboxID == "" { c.JSON(409, gin.H{"error": "sandbox not running"}); return }

    target := fmt.Sprintf("%s/sandboxes/%s/proxy/44772%s", h.serverURL, sandboxID, subpath)
    if q := c.Request.URL.RawQuery; q != "" {
        target += "?" + q
    }

    req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target, c.Request.Body)
    if err != nil { c.JSON(500, gin.H{"error": err.Error()}); return }

    req.Header.Set("X-OPEN-SANDBOX-API-KEY", h.sandboxAPIKey)
    if ct := c.GetHeader("Content-Type"); ct != "" {
        req.Header.Set("Content-Type", ct)
    }

    resp, err := h.httpClient.Do(req)
    if err != nil { c.JSON(502, gin.H{"error": err.Error()}); return }
    defer resp.Body.Close()

    // Copy response headers and body verbatim
    for k, vs := range resp.Header {
        for _, v := range vs { c.Header(k, v) }
    }
    c.Status(resp.StatusCode)
    io.Copy(c.Writer, resp.Body)
}
```

The handler needs an `*http.Client` on `Handler` — add one (reuse existing
client or create with sane timeout, e.g. 30 s).

### 3. Register route

In `router.go`, under the protected group:

```go
protected.Any("/tasks/:id/execd/*path", h.ExecdProxy)
```

Using `Any` lets the proxy forward GET (file search, download) and POST
(commands, if needed later) without separate registrations.

---

## Frontend Changes

### 4. Expose `cwd` from `useChat.ts`

The `session.init` SSE event contains `{ sessionId, cwd, ... }`. Currently the
hook only extracts `sessionId`. Add `cwd`:

```ts
// In useChat.ts state
const [cwd, setCwd] = useState<string | null>(null)

// In SSE parser, on 'session.init':
if (data.cwd) setCwd(data.cwd)

// Return from hook:
return { ..., cwd }
```

### 5. New API client functions (`src/api/client.ts`)

```ts
export interface FileInfo {
  path: string
  name: string
  isDir: boolean
  size: number
  mode: string
  modTime: string
}

// List one directory level (glob '*' = one level, '**' = recursive)
export async function listDir(taskId: string, dir: string): Promise<FileInfo[]> {
  const params = new URLSearchParams({ path: dir, pattern: '*' })
  const res = await fetch(`${BASE}/api/tasks/${taskId}/execd/files/search?${params}`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to list directory')
  return res.json() as Promise<FileInfo[]>
}

// Read file content as text (uses download, returns string)
export async function readFile(taskId: string, filePath: string): Promise<string> {
  const params = new URLSearchParams({ path: filePath })
  const res = await fetch(`${BASE}/api/tasks/${taskId}/execd/files/download?${params}`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to read file')
  return res.text()
}
```

### 6. New `WorkspacePanel` component

File: `src/components/WorkspacePanel.tsx`

Props:
```ts
interface WorkspacePanelProps {
  taskId: string
  cwd: string         // root path from session.init
  refreshToken: number  // increment to trigger a refresh (on session.completed)
}
```

Behaviour:
- On mount (and on `refreshToken` change): fetch `listDir(taskId, cwd)` to
  populate root entries.
- Renders a scrollable file tree. Each entry shows a file/folder icon
  (lucide `Folder` / `File`) and name.
- Clicking a **directory** toggles its children (lazy fetch on first expand).
- Clicking a **file** opens a slide-in text viewer below (or inline panel).
- Viewer: shows raw text with a monospace font; "Close" button to dismiss.
- Header row: shows current root path (truncated) + a manual **Refresh** button
  (lucide `RefreshCw`).
- If `taskId` is null or sandbox not running: show a muted placeholder
  `"Workspace available when sandbox is running"`.

Rough structure:

```
┌──────────────────────────────┐
│ /workspace/alice/task-xyz  ↺ │  ← header with refresh icon
├──────────────────────────────┤
│ ▼ 📁 src                     │
│   ├─ 📄 main.py              │  ← click to open viewer
│   └─ 📄 utils.py             │
│ ─ 📄 README.md               │
└──────────────────────────────┘
│ ← file viewer (if open) ──── │
│  # README                    │
│  ...                         │
└──────────────────────────────┘
```

### 7. Update `ChatPage.tsx`

#### Toggle state
```tsx
const [workspaceOpen, setWorkspaceOpen] = useState(false)
const [refreshToken, setRefreshToken] = useState(0)
```

#### Refresh on agent completion
Pass a callback into `useChat` (or lift into `ChatPage`):
```tsx
// After session.completed SSE, increment refreshToken
// Option: expose an onSessionCompleted callback in useChat, or
// watch for sandboxState transitioning back to 'running'.
```
Simplest: in the SSE loop in `useChat.ts`, after processing `session.completed`,
call an optional `onSessionCompleted` callback prop. ChatPage passes
`() => setRefreshToken(t => t + 1)`.

#### Toggle button in header
Add alongside the existing `Blocks` icon:
```tsx
<button onClick={() => setWorkspaceOpen(v => !v)} title="Toggle workspace">
  <FolderOpen size={16} />   {/* lucide icon */}
</button>
```

#### Layout
```tsx
<div className="flex h-[100dvh]">
  {sidebarOpen && <HistorySidepanel ... />}

  <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
    {/* existing chat column — constrained width when workspace is open */}
    <div className={cn('flex flex-col h-full w-full mx-auto',
      workspaceOpen ? 'max-w-xl' : sidebarOpen ? 'max-w-2xl' : 'max-w-3xl'
    )}>
      ...existing header, ScrollArea, ChatInput...
    </div>
  </div>

  {workspaceOpen && taskId && cwd && (
    <WorkspacePanel
      taskId={taskId}
      cwd={cwd}
      refreshToken={refreshToken}
    />
  )}
</div>
```

`WorkspacePanel` has a fixed width (e.g. `w-72` or `w-80`) and
`h-[100dvh] overflow-hidden flex flex-col border-l border-neutral-200`.

---

## Open Questions / Decisions Needed

1. **Execd access token**: Does the OpenSandbox proxy relay requests to execd
   without requiring `X-EXECD-ACCESS-TOKEN`? If not, how is the token surfaced
   (sandbox env var at create time)?

2. **Initial workspace open state**: Should the panel be closed by default and
   only open when the user clicks the toggle? Or auto-open when the sandbox
   first becomes `running`? Proposed: stay closed until user opens it.

3. **File viewer depth**: For this phase, show raw text only. Binary files
   show a `"Binary file, preview not available"` message (detect by checking
   for null bytes in the first 512 bytes or by MIME type from `Content-Type`
   response header).

4. **Refresh strategy**: Manual refresh + auto-refresh after `session.completed`.
   No polling during agent execution (avoids racing with agent file writes).

---

## File Checklist

**Backend**
- [x] `backend/internal/api/handlers.go`: add `ExecdProxy` handler + swagger annotations; add `serverURL`, `sandboxAPIKey`, `httpClient` fields to `Handler`; `withExecd` setter
- [x] `backend/internal/api/router.go`: call `h.withExecd(cfg.Sandbox.ServerURL, cfg.Sandbox.APIKey)`; register `Any /tasks/:id/execd/*path`

**Frontend**
- [x] `frontend/src/hooks/useChat.ts`: extract + expose `cwd` from `session.init`; add `onSessionCompleted` callback
- [x] `frontend/src/api/client.ts`: add `FileInfo`, `listDir`, `readFile`
- [x] `frontend/src/components/WorkspacePanel.tsx`: new component (lazy tree + file viewer)
- [x] `frontend/src/pages/ChatPage.tsx`: add `FolderOpen` toggle, `workspaceOpen`/`refreshToken` state, layout update
