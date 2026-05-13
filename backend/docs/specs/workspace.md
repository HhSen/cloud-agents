# Workspace Panel: OFS-Backed Filesystem Access

## Overview

The workspace panel lets the frontend browse the task's working directory. It is a read-only file tree with an inline file viewer. The panel appears as a third column on the right side of `ChatPage` and works for both active and history sessions.

```
┌──────────────┬──────────────────────┬──────────────────┐
│  History     │    Session view       │   Workspace      │
│  sidepanel   │    (chat messages)    │   (file tree)    │
└──────────────┴──────────────────────┴──────────────────┘
```

---

## Request Path

Filesystem operations go directly to **OFS (OrangeFS)** via S3 — no sandbox required.

```
Frontend
  │  GET /api/tasks/:id/workspace/files?path=...
  │  GET /api/tasks/:id/workspace/file?path=...
  ▼
Go backend  (WorkspaceFiles / WorkspaceFile handlers)
  │  ListObjectsV2 (with Delimiter: /) / GetObject
  ▼
OFS S3 endpoint  (ORANGEFS_ENDPOINT)
  └─ bucket: {volume}  prefix: {username}/workspaces/{task_id}/...
```

The legacy execd proxy route (`ANY /api/tasks/:id/execd/*path`) is **kept** for non-workspace execd operations and is not removed.

---

## S3 Key Mapping

| Concept | S3 key |
|---|---|
| Workspace root | `{username}/workspaces/{task_id}/` |
| File at CWD-relative path `p` | `{username}/workspaces/{task_id}/{p}` |
| Subdirectory `dir/` | Listed as a CommonPrefix with `Delimiter: /` |

Given `cwd = /workspace/{username}/{task_id}` and a requested path `p`:
- Strip the CWD prefix from `p` to get `relPath`
- S3 key: `{username}/workspaces/{task_id}/{relPath}`

---

## Backend: WorkspaceFiles / WorkspaceFile Handlers

### `WorkspaceFiles` — list a directory

**Route**: `GET /api/tasks/:id/workspace/files?path=<absolute-dir>` (protected)

1. Resolves task → 404 if not found.
2. Returns 409 if `workspaceReader` is not configured (OFS not set up).
3. Calls `storage.Client.ListWorkspace(ctx, username, taskID, path)`.
4. Returns `[]FileInfo` (same JSON shape as execd `files/search`).

### `WorkspaceFile` — download a file

**Route**: `GET /api/tasks/:id/workspace/file?path=<absolute-file>` (protected)

1. Resolves task → 404 if not found.
2. Returns 409 if `workspaceReader` not configured.
3. Calls `storage.Client.GetWorkspaceFile(ctx, username, taskID, path)`.
4. Streams raw bytes as `application/octet-stream`.

**Handler fields** added to `api.Handler`:

| Field | Type | Purpose |
|---|---|---|
| `workspaceReader` | `WorkspaceReader` | OFS S3 client for workspace browsing |

Populated in `NewRouter` via `h.withWorkspace(deps.WorkspaceReader)` when `deps.WorkspaceReader != nil`.

---

## Storage Methods

In `backend/internal/storage/client.go`:

```go
// ListWorkspace returns one level of entries under subpath inside the task workspace.
func (c *Client) ListWorkspace(ctx context.Context, username, taskID, subpath string) ([]WorkspaceEntry, error)

// GetWorkspaceFile returns the raw bytes of a workspace file.
func (c *Client) GetWorkspaceFile(ctx context.Context, username, taskID, filePath string) ([]byte, error)
```

`ListWorkspace` uses `ListObjectsV2` with `Delimiter: /` for one-level directory listing.

---

## CWD: Working Directory

The workspace CWD is `/workspace/{username}/{task_id}`, derived from:

1. **Active session**: `session.init` SSE event emits `cwd`; `useChat.ts` extracts and stores it.
2. **History session**: `GET /api/tasks/:id` response now includes `"cwd"` field:
   ```go
   CWD: fmt.Sprintf("/workspace/%s/%s", t.Username, id)
   ```
   `ChatPage.handleSelectTask` fetches the task in parallel with history and passes `task.cwd` to `loadTask`.

`WorkspacePanel` renders as soon as both `taskId` and `cwd` are set — for history sessions this happens immediately on task load.

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
| OFS not configured | Backend returns 409; frontend shows error message in panel |
| File not found in OFS | Backend returns 404; frontend shows *"Failed to load file"* |
| Empty directory | Frontend renders *"empty"* label under the expanded node |
| Binary file | Frontend renders *"Binary file, preview not available"* instead of raw content |
