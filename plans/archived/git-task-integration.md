# Git Task Integration

Allow each task to optionally specify a git repository URL. When the task is first provisioned the platform clones the repo into the sandbox working directory so the agent starts with the full codebase in scope.

**Depends on:** `ssh-key-management.md` being implemented first (SSH key injection into the sandbox).

---

## Goals

- `git_url` is optional on task creation; tasks without it behave exactly as today.
- Repo is cloned once, at provision time, into `$CWD` before the agent starts.
- Supports both `https://` (public repos) and `git@` / `ssh://` (private repos via stored SSH key).
- Task title defaults to the repo name if not provided.

---

## Backend

### 1. DB — `tasks` table

Add one column:

```sql
ALTER TABLE tasks ADD COLUMN git_url VARCHAR(512) DEFAULT NULL;
```

GORM model (`db/task.go`): add `GitURL sql.NullString \`gorm:"column:git_url"\``.

### 2. Task store

`internal/task/store.go` — add `GitURL string` to the `Task` struct.
`internal/task/repository.go` — `Create` signature gains `gitURL string`; passed through to the model.

The `List` / `Get` methods already hydrate full rows — no changes needed there.

### 3. API — task creation

`POST /api/tasks` request body gains an optional field:

```json
{
  "title": "optional",
  "git_url": "git@github.com:org/repo.git",
  "env": {}
}
```

Handler changes (`internal/api/handlers.go`):
1. Validate `git_url` if present: must be a non-empty string matching `^(https?://|git@|ssh://)`.
2. If `title` is empty and `git_url` is set, derive title from the last path segment (e.g. `org/repo.git` → `repo`).
3. Pass `gitURL` to `repo.Create`.

Update Swagger annotation for `POST /api/tasks`.

### 4. Sandbox provisioning — clone step

`internal/sandbox/manager.go`, `EnsureProvisioned` (after SSH key injection, before returning):

```go
if task.GitURL != "" {
    if err := cloneRepo(ctx, execdBaseURL, task.GitURL, cwd); err != nil {
        // fail fast — mark StateError immediately, no retry
        repo.SetError(task.ID, err.Error())
        return err
    }
}
```

`cloneRepo` uses the execd `/command` endpoint (proxied at `/api/tasks/:id/execd/command`):

```
POST /command
{
  "command": "git clone <git_url> .",
  "cwd": "/workspace/{username}/{task_id}",
  "background": true,
  "timeout": 300000
}
```

Run background (returns a command ID), then poll `GET /command/status/{id}` every 2 s until `running: false`. If `exit_code != 0`, return the error immediately — no retry.

Full clone (no `--depth=1`).

**SSH key prerequisite check:** if `git_url` starts with `git@` or `ssh://` and the user has no SSH key stored, return an error before even creating the sandbox: `"private repo requires an SSH key — add one in Settings"`. Check happens in the `POST /api/tasks` handler, not at provision time.

### 5. Error surfacing — fast fail

Clone failure transitions the task to `StateError` immediately. No retry logic. Add `ErrorMsg string` to the `Task` struct and a `SET error_msg = ?` update when entering error state.

The frontend polls task state; extend the task response to include `error_msg` so the UI can display a specific reason ("clone failed: repository not found") instead of a generic error banner.

---

## Frontend

### Task creation dialog

Currently, a task is created silently on the first message. Introduce a lightweight creation dialog for users who want to specify a git URL.

**Trigger:** "New task" button in the sidebar (replace the current implicit creation).

```
┌──────────────────────────────────┐
│  New Task                        │
│                                  │
│  Title (optional)                │
│  ┌──────────────────────────┐    │
│  │                          │    │
│  └──────────────────────────┘    │
│                                  │
│  Git Repository (optional)       │
│  ┌──────────────────────────┐    │
│  │  https://… or git@…      │    │
│  └──────────────────────────┘    │
│  ⓘ Private repos require an SSH  │
│    key configured in Settings.   │
│                                  │
│              [Cancel]  [Create]  │
└──────────────────────────────────┘
```

- If the user sends the first message without opening the dialog, existing implicit-creation path is preserved (no `git_url`).
- After `Create`, task is provisioned and the user lands on the chat for that task. A spinner / "Cloning repository…" status is shown while `task.state === "provisioning"`.

### Task list item

Show a small git icon + truncated repo name next to tasks that have a `git_url`.

### API client

Add `git_url?: string` to the `CreateTaskRequest` type and pass it through `createTask()` in `src/api/client.ts`.

---

## Data flow summary

```
User fills dialog → POST /api/tasks { git_url }
  → DB row: tasks.git_url = "git@github.com:…"
  → task.State = provisioning

First message → EnsureProvisioned
  → spin up sandbox
  → decrypt user SSH key
  → inject key into container ~/.ssh/
  → git clone <git_url> . (full history) into /workspace/{user}/{task_id}/
  → task.State = running

Agent starts inside cloned repo
```

---

## Task breakdown

1. DB migration — `git_url` column, update `db.Task` and task store
2. `POST /api/tasks` handler — accept + validate `git_url`, derive title
3. Sandbox manager — `cloneRepo` step after health-check
4. SSH key prerequisite check + error surfacing
5. Frontend: New Task dialog with git URL field
6. Frontend: provisioning spinner / "Cloning…" status in chat
7. Frontend: git icon in task list items
