# Workspace Panel: Migrate from execd to OFS S3

## Problem

The current workspace panel calls `ExecdProxy` → execd (port 44772 inside the sandbox). This requires an active sandbox. For **history sessions** (sandbox not running), the panel shows a 409 "sandbox not running" placeholder and is effectively unusable.

---

## Key Insight

The workspace files are FUSE-mounted inside the sandbox, but the underlying storage is OFS (OrangeFS), which is S3-compatible. The S3 layout is:

```
{username}/workspaces/{task_id}/<relative-file-path>
```

The backend already holds OFS S3 credentials and uses them for history reads (`storage.Client`). We can extend the same client to browse workspace files via `ListObjectsV2` (directory listing) and `GetObject` (file download) — no sandbox required.

---

## Architecture After Migration

```
Frontend
  │  GET /api/tasks/:id/workspace/files?path=...
  │  GET /api/tasks/:id/workspace/file?path=...
  ▼
Go backend  (WorkspaceFiles / WorkspaceFile handlers)
  │  ListObjectsV2 / GetObject
  ▼
OFS S3 endpoint  (ORANGEFS_ENDPOINT)
  └─ bucket: {volume}  prefix: {username}/workspaces/{task_id}/...
```

The execd route (`/api/tasks/:id/execd/*path`) is **kept** to avoid breaking anything during migration, but the workspace panel switches to the new OFS-backed endpoints.

---

## S3 Key Mapping

| Concept | S3 key |
|---|---|
| Workspace root | `{username}/workspaces/{task_id}/` |
| File at CWD-relative path `p` | `{username}/workspaces/{task_id}/{p}` |
| Subdirectory `dir/` | Listed as a CommonPrefix in `ListObjectsV2` with delimiter `/` |

Given `cwd = /workspace/{username}/{task_id}` and a requested path `p` (absolute or relative):
- Strip the CWD prefix from `p` to get `relPath`
- S3 key: `{username}/workspaces/{task_id}/{relPath}`

For directory listing use `Delimiter: "/"` to get one-level entries (files as `Contents`, subdirs as `CommonPrefixes`).

---

## Backend Changes

### 1. Extend `storage.Client` with workspace methods

File: `backend/internal/storage/client.go`

Add two new methods (not on `OFSClient` interface — use a narrow new interface):

```go
// WorkspaceEntry is a single entry returned by ListWorkspace.
type WorkspaceEntry struct {
    Path    string    // absolute CWD-relative path  e.g. /workspace/alice/task-abc/src
    Name    string    // last path segment
    IsDir   bool
    Size    int64
    ModTime time.Time
}

// ListWorkspace returns one level of entries under subpath inside the task workspace.
// subpath is an absolute path like "/workspace/{username}/{task_id}/src".
func (c *Client) ListWorkspace(ctx context.Context, username, taskID, subpath string) ([]WorkspaceEntry, error)

// GetWorkspaceFile returns the raw bytes of a workspace file.
// filePath is an absolute path like "/workspace/{username}/{task_id}/src/main.py".
func (c *Client) GetWorkspaceFile(ctx context.Context, username, taskID, filePath string) ([]byte, error)
```

**`ListWorkspace` implementation**:
1. Compute `prefix = "{username}/workspaces/{task_id}/{relPath}/"` (where `relPath` = subpath stripped of CWD prefix)
2. Call `ListObjectsV2` with `Prefix: prefix, Delimiter: "/"`, paginate
3. For each `CommonPrefix` (subdirectory) → emit `WorkspaceEntry{IsDir: true, ...}`
4. For each `Contents` entry (file) → emit `WorkspaceEntry{IsDir: false, Size: ..., ModTime: ...}`
5. Strip bucket prefix from returned paths to reconstruct absolute CWD paths

**`GetWorkspaceFile` implementation**:
1. Compute `key = "{username}/workspaces/{task_id}/{relPath}"`
2. `GetObject` → read body → return bytes

Add a narrow interface for injection into the handler:

```go
// WorkspaceReader is the subset of storage.Client used by WorkspaceFiles/WorkspaceFile handlers.
type WorkspaceReader interface {
    ListWorkspace(ctx context.Context, username, taskID, subpath string) ([]WorkspaceEntry, error)
    GetWorkspaceFile(ctx context.Context, username, taskID, filePath string) ([]byte, error)
}
```

### 2. New handlers in `handlers.go`

**`WorkspaceFiles`** — list a directory:

```
GET /api/tasks/:id/workspace/files?path=<absolute-dir>
```

Response: `[]FileInfo` — same shape as execd `files/search`, so the frontend `listDir` function needs no JSON parsing changes.

```go
// @Summary      List workspace directory via OFS
// @Tags         tasks
// @Param        id    path   string  true  "Task ID"
// @Param        path  query  string  true  "Absolute directory path"
// @Success      200  {array}   FileInfo
// @Failure      404  {object}  errorResponse  "task not found"
// @Failure      409  {object}  errorResponse  "OFS not configured"
// @Router       /api/tasks/{id}/workspace/files [get]
func (h *Handler) WorkspaceFiles(c *gin.Context) { ... }
```

**`WorkspaceFile`** — download a file:

```
GET /api/tasks/:id/workspace/file?path=<absolute-file>
```

Response: raw bytes (same as execd `files/download`).

```go
// @Summary      Download workspace file via OFS
// @Tags         tasks
// @Param        id    path   string  true  "Task ID"
// @Param        path  query  string  true  "Absolute file path"
// @Success      200
// @Failure      404  {object}  errorResponse  "task not found or file not found"
// @Router       /api/tasks/{id}/workspace/file [get]
func (h *Handler) WorkspaceFile(c *gin.Context) { ... }
```

Add `workspaceReader WorkspaceReader` field to `Handler` and a `withWorkspace(r WorkspaceReader)` setter.

### 3. Register routes in `router.go`

```go
protected.GET("/tasks/:id/workspace/files", h.WorkspaceFiles)
protected.GET("/tasks/:id/workspace/file",  h.WorkspaceFile)
```

Wire up in `NewRouter`:
```go
h.withWorkspace(storageClient)  // *storage.Client satisfies WorkspaceReader
```

---

## Frontend Changes

### 4. Update `src/api/client.ts`

Replace execd calls in `listDir` / `readFile` with the new OFS-backed endpoints:

```ts
// was: /api/tasks/${taskId}/execd/files/search?path=...&pattern=*
export async function listDir(taskId: string, dir: string): Promise<FileInfo[]> {
  const params = new URLSearchParams({ path: dir })
  const res = await fetch(`${BASE}/api/tasks/${taskId}/workspace/files?${params}`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to list directory')
  return res.json() as Promise<FileInfo[]>
}

// was: /api/tasks/${taskId}/execd/files/download?path=...
export async function readFile(taskId: string, filePath: string): Promise<string> {
  const params = new URLSearchParams({ path: filePath })
  const res = await fetch(`${BASE}/api/tasks/${taskId}/workspace/file?${params}`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to read file')
  return res.text()
}
```

No other frontend files need changing. `WorkspacePanel`, `ChatPage`, and `useChat` are unaffected.

### 5. Update error placeholder in `WorkspacePanel`

The 409 fallback message "Workspace available when sandbox is running" becomes stale. Two options:
- **Remove it**: the OFS path never 409s due to sandbox state (it 409s only if OFS is not configured, which is a config error, not a normal state).
- **Keep but relabel**: change to "Workspace unavailable" for any non-200 response.

Prefer removing the sandbox-specific message since the dependency is gone.

---

## Handling the `cwd` Dependency

`WorkspacePanel` still needs `cwd` as the root path to list. Currently `cwd` comes from `session.init` SSE, which requires an active session.

For history sessions without an active sandbox, `cwd` is never emitted. **Fix**: derive `cwd` from the task record instead:

- `GET /api/tasks/:id` already returns the task; add `cwd` to the response:
  ```go
  CWD: fmt.Sprintf("/workspace/%s/%s", t.Username, t.ID),
  ```
- `ChatPage` / `useChat` can set `cwd` from the task load response as a fallback if `session.init` hasn't fired yet.

This way `WorkspacePanel` renders immediately when a history task is loaded, before any SSE fires.

---

## What Stays

- `ExecdProxy` handler and `/api/tasks/:id/execd/*path` route — kept intact, not removed. It may still be useful for commands or non-workspace execd operations.
- Refresh strategy (manual button + `session.completed` trigger) — unchanged.
- `WorkspacePanel` component and `FileNode` tree — unchanged.

---

## File Checklist

**Backend**
- [x] `backend/internal/storage/client.go`: add `WorkspaceEntry`, `ListWorkspace`, `GetWorkspaceFile`
- [ ] `backend/internal/storage/client_test.go`: unit tests for `ListWorkspace` / `GetWorkspaceFile`
- [x] `backend/internal/api/handlers.go`: add `WorkspaceReader` interface, `WorkspaceFiles`, `WorkspaceFile` handlers + swagger annotations; add `workspaceReader` field + `withWorkspace` setter
- [x] `backend/internal/api/router.go`: register new GET routes; wire `h.withWorkspace(deps.WorkspaceReader)` in `NewRouter`
- [x] `backend/internal/api/types.go`: add `FileInfo` type; add `CWD` field to `getTaskResponse`
- [x] `backend/cmd/server/main.go`: wire `WorkspaceReader: ofsClient`

**Frontend**
- [x] `frontend/src/api/client.ts`: add `getTask`; update `listDir` and `readFile` to call new `/workspace/files` and `/workspace/file` endpoints
- [x] `frontend/src/hooks/useChat.ts`: update `loadTask` to accept optional `cwd` and set it
- [x] `frontend/src/pages/ChatPage.tsx`: fetch task alongside history in `handleSelectTask`; remove sandbox-specific placeholder message

**Spec**
- [x] `backend/docs/specs/workspace.md`: updated to reflect OFS-backed routes and cwd fallback

---

## Open Questions

1. **OFS eventual consistency**: Files written by the agent may not be immediately visible via S3 `ListObjectsV2` (FUSE flushes vs S3 visibility). Is this a concern in practice with OrangeFS? If yes, we may need a short post-`session.completed` delay before the auto-refresh.

2. **`cwd` in task API response**: Confirm whether adding `cwd` to `GET /api/tasks/:id` response needs a types update in `frontend/src/types/` and the task list response.

3. **Large workspaces**: `ListObjectsV2` with `Delimiter` is one-level — same lazy-load behaviour as before. No change needed, but confirm OFS S3 supports `Delimiter` correctly (it does for standard S3 semantics).
