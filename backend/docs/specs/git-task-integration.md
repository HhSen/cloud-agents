# Git Task Integration

Tasks can optionally specify a git repository URL at creation time. The platform clones the repository into the sandbox working directory during provisioning so the agent starts with the full codebase in scope.

**Depends on:** `ssh-key-management.md` — SSH key injection must run before the clone step so that private repositories are accessible.

---

## Overview

```
User fills New Task dialog → POST /api/tasks { git_url, title? }
  → git_url stored on tasks row, title derived from repo name if not provided
  → task state = pending

First message → EnsureProvisioned
  → sandbox created
  → SSH key injected (if configured)
  → git clone <git_url> .  into /workspace/{username}/{task_id}/
  → task state = running

Agent starts inside the cloned repository
```

---

## Database

Two columns added to `tasks`:

```sql
ALTER TABLE tasks ADD COLUMN git_url   VARCHAR(512) DEFAULT NULL;
ALTER TABLE tasks ADD COLUMN error_msg TEXT         DEFAULT NULL;
```

GORM model (`internal/db/task.go`):

```go
GitURL   string `gorm:"column:git_url;size:512"`
ErrorMsg string `gorm:"column:error_msg;type:text"`
```

`AutoMigrate` in `db.Open` creates both columns on first start. `""` means unset for both fields; `NULL` is never written.

`error_msg` captures the failure reason when provisioning enters `StateError`. It is set atomically with the state transition and persisted to MySQL + Redis.

---

## Task Store

`internal/task/store.go`:

- `Task` struct gains `gitURL string` and `errorMsg string` fields (mutex-protected)
- `SetError(msg string)` sets both `state = StateError` and `errorMsg`, then calls `ops.persistError(msg)`
- `GetGitURL() string` / `GetErrorMsg() string` — mutex-safe read accessors
- `Store.Create(ctx, username, extraEnv, gitURL)` — `gitURL` threaded through to all three repository backends

`internal/task/repository.go`:

- `Repository.Create` interface signature: `Create(ctx, username, extraEnv map[string]string, gitURL string) (*Task, error)`
- `taskOps.persistError` interface signature: `persistError(msg string)`
- `TaskSummary` gains `GitURL string` and `ErrorMsg string` for list responses

---

## API

### POST /api/tasks

Request body gains an optional field:

```json
{
  "username": "alice",
  "title":    "optional",
  "git_url":  "git@github.com:org/repo.git",
  "env":      {}
}
```

**Validation** (handler, before `repo.Create`):

1. `git_url` must match `^(https?://|git@|ssh://)[^\s;|&$`()\n\r<>]+$`  
   Rejects anything that doesn't start with a recognised protocol, and rejects whitespace and shell metacharacters regardless of where they appear in the URL.

2. If `git_url` starts with `git@` or `ssh://` and `(u == nil || u.SSHPrivateKeyEnc == "")`, return:
   ```
   400 Bad Request
   { "error": "private repo requires an SSH key — add one in Settings" }
   ```
   This check runs in the handler, not at provision time, so the task is never created in a state that will always fail.

3. If `title` is empty and `git_url` is set, `title` is derived from the last path segment of the URL with the `.git` suffix stripped:
   ```
   https://github.com/org/my-project.git  →  my-project
   git@github.com:org/my-project.git      →  my-project
   ```

### GET /api/tasks/:id

Response includes:

```json
{
  "git_url":   "git@github.com:org/repo.git",
  "error_msg": ""
}
```

Both fields are omitted when empty (`omitempty`).

### GET /api/tasks

Each item in the list includes `git_url` and `error_msg` (omitted when empty).

---

## Sandbox Provisioning — Clone Step

Implemented in `internal/sandbox/gitclone.go`. Called from `Manager.ProvisionForTask` after SSH key injection and after `injectResources`, before `t.SetRunning`:

```go
// manager.go (ProvisionForTask)
if gitURL := t.GetGitURL(); gitURL != "" {
    taskCWD := fmt.Sprintf("/workspace/%s/%s", t.Username, t.ID)
    if err := m.cloneRepo(ctx, sandboxID, gitURL, taskCWD); err != nil {
        return fmt.Errorf("clone repo: %w", err)
    }
}
```

`cloneRepo` uses the execd command endpoint, which streams NDJSON events until the process exits:

```
POST {serverURL}/sandboxes/{id}/proxy/44772/command
{
  "command": "git clone '<git_url>' .",
  "cwd":     "/workspace/{username}/{task_id}",
  "timeout": 300000,
  "envs":    {"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
}
→ (SSE stream — text/event-stream — one JSON object per line, blank lines as separators)
  {"type":"init",  "text":"<id>",         "timestamp":...}
  {"type":"ping",  "text":"pong",          "timestamp":...}
  {"type":"stderr","text":"Cloning...",    "timestamp":...}
  {"type":"stdout","text":"...",           "timestamp":...}
  {"type":"error", "error":{"ename":"CommandExecError","evalue":"128",...}, "timestamp":...}
```

- `stdout`/`stderr` events carry process output lines in `text`.
- A terminal `error` event with `error.evalue` set to the exit code string signals non-zero exit.
- Absence of an `error` event (stream closes normally) means exit 0 (success).

`PATH` is passed via `envs` (the execd API field for environment injection) because the execd execution environment uses a minimal PATH that does not include `/usr/bin`, preventing git from finding the `ssh` binary for SSH-protocol URLs.

The URL is single-quoted in the command string (shell metacharacter defense-in-depth alongside the API-layer regex). A dedicated `http.Client` with a 315-second timeout is used so execd can stream the full output before the connection closes.

On non-zero exit, `cloneRepo` returns an error containing the accumulated `stderr` (or `stdout` if stderr is empty). The caller in `ProvisionForTask` wraps and returns the error, causing `EnsureProvisioned` to call `t.SetError(err.Error())`, transitioning the task to `StateError` with the failure message persisted.

**No retry** — clone failure is fast-fail. The user must fix the URL or SSH key configuration and create a new task.

### Clone ordering

```
sandbox created
  ↓
health-check passes
  ↓
maybeInjectSSHKey        ← SSH key must be in place before clone (private repos)
  ↓
cloneRepo                ← must run before injectResources: git clone requires an empty
  ↓                         workspace directory; injectResources writes .claude/ into it
injectResources          ← overlaid on top of the cloned repo
  ↓
t.SetRunning(...)
```

---

## Security

### URL Validation

`isValidGitURL` uses a strict allowlist regex:

```go
var validGitURL = regexp.MustCompile(`^(https?://|git@|ssh://)[^\s;|&$` + "`" + `()\n\r<>]+$`)
```

This ensures:
- URL starts with a recognised protocol prefix
- No whitespace, semicolons, pipes, ampersands, dollar signs, backticks, parentheses, or angle brackets anywhere in the URL body

These characters could otherwise be interpreted as shell operators or substitutions if the URL is passed to a shell.

### Shell Quoting

In `gitclone.go`, the URL is wrapped in single quotes before being placed in the command string:

```go
quotedURL := "'" + strings.ReplaceAll(gitURL, "'", `'\''`) + "'"
Command: "git clone " + quotedURL + " ."
```

This provides defense-in-depth: even if a URL containing a single quote somehow passed validation, the quoting would neutralise it.

### Private Repo Gate

The check `isPrivateGitURL(body.GitURL) && (u == nil || u.SSHPrivateKeyEnc == "")` blocks private-protocol URLs when:
- The auth middleware is not active (`u == nil`, development mode)
- The authenticated user has no SSH key configured

This ensures the task is never created in a state where provisioning will always fail with an opaque clone error.

---

## Frontend

### NewTaskDialog (`src/components/NewTaskDialog.tsx`)

A modal dialog triggered by the "New task" button in the history sidebar (replaces the previous implicit task creation on first message). The implicit path — creating a task on the first message without a `git_url` — is preserved for users who skip the dialog.

Fields:
- **Title** (optional) — defaults to repo name if a `git_url` is provided
- **Git Repository** (optional) — accepts `https://…`, `git@…`, or `ssh://…`

UX details:
- Enter submits; Escape / backdrop-click cancels
- When the URL starts with `git@` or `ssh://`, an inline note appears: *"Private repos require an SSH key configured in Settings"*
- API error messages (e.g. missing SSH key, invalid URL) are surfaced in the dialog

### History Sidebar

Tasks with a `git_url` receive:
- A `<GitBranch>` icon prepended to the title
- The repository name shown as the subtitle (replacing the last-updated timestamp)

### API Client

`createTask(username, options?)` accepts `options.gitUrl` and serialises it as `git_url` in the request body. HTTP 4xx/5xx error messages from the server are propagated to callers (the dialog displays them).

`TaskSummary` and `Task` interfaces both include optional `git_url?: string` and `error_msg?: string`.

---

## Error Surfacing

When a clone fails at provision time:

1. `cloneRepo` returns an error with `stderr` detail
2. `ProvisionForTask` wraps and returns the error
3. `EnsureProvisioned` calls `t.SetError(err.Error())`
4. `SetError` writes `state=3` and `error_msg=<detail>` to MySQL and Redis atomically

`error_msg` is returned in both `GET /api/tasks/:id` and `GET /api/tasks` so the frontend can display the specific failure reason (e.g. `"git clone failed (exit 128): repository not found"`).

---

## Related Documents

- [`ssh-key-management.md`](ssh-key-management.md) — SSH key encryption, storage, and sandbox injection (prerequisite)
- [`data-management.md`](data-management.md) — `tasks` schema including `git_url` and `error_msg`
- [`resource-mapping.md`](resource-mapping.md) — Sandbox provision lifecycle and clone step ordering
