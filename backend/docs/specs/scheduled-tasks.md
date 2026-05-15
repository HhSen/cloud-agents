# Scheduled Tasks

Scheduled tasks let users define recurring or one-shot agent executions triggered by a cron expression or a specific datetime. Each firing creates a **first-class Task** — it has its own sandbox, its own SSE stream, its own OFS-persisted session transcript, and it is visible and resumable in the normal chat UI exactly like a manually created task.

**Depends on:** `data-management.md` — task state machine and MySQL/Redis task storage.

---

## Overview

```
User creates a Schedule (POST /api/schedules)
  → stored in scheduled_tasks table
  → Scheduler registers cron entry

Cron fires at the scheduled time
  → runFire creates a child Task (tasks.schedule_id = schedule.id)
  → sandbox provisioned, agent runs schedule.prompt
  → transcript written to OFS under TASK_ID (same as any other task)
  → session is resumable from the chat UI

User opens chat sidebar
  → tasks with schedule_id show a calendar icon
  → clicking opens the full chat session
```

---

## Database

### New table: `scheduled_tasks`

GORM model: `internal/db/scheduled_task.go`

```go
type ScheduledTask struct {
    ID          string     // UUID primary key
    UserID      uint       // FK → users.id, ON DELETE CASCADE
    Title       string     // display name (optional)
    Prompt      string     // text sent to the agent on each firing
    CronExpr    string     // robfig/cron expression or "@once"
    RunAt       *time.Time // non-null only for @once schedules
    ExtraEnv    string     // JSON map[string]string; "" means none
    GitURL      string     // optional; cloned into sandbox at provision time
    TimeoutSecs int        // default 1800; range [60, 86400]
    Concurrency int        // 0=skip if running, 1=allow parallel runs
    Enabled     bool       // false stops the cron entry without deleting the record
    LastRunAt   *time.Time
    NextRunAt   *time.Time
    CreatedAt, UpdatedAt time.Time
}
```

`db.Open` calls `AutoMigrate(&ScheduledTask{})` on startup.

### Modified table: `tasks`

A new nullable column links each run back to its parent schedule:

```sql
ALTER TABLE tasks ADD COLUMN schedule_id VARCHAR(36) DEFAULT NULL;
CREATE INDEX idx_tasks_schedule_id ON tasks(schedule_id);
```

GORM field added to `internal/db/task.go`:

```go
ScheduleID *string `gorm:"column:schedule_id;size:36;default:null;index"`
```

`""` is never written; the column is either `NULL` (no schedule) or a valid UUID.

---

## Task Repository Changes

`internal/task/repository.go`:

- `Create` signature gains a final `scheduleID string` parameter:
  ```go
  Create(ctx context.Context, username string, extraEnv map[string]string,
         gitURL string, scheduleID string) (*Task, error)
  ```
  Pass `""` for manually created tasks.

- New method on the `Repository` interface:
  ```go
  ListBySchedule(ctx context.Context, scheduleID string) ([]TaskSummary, error)
  ```
  Returns all tasks with a matching `schedule_id`, newest first. Used by `GET /api/schedules/:id/runs`.

- `TaskSummary` gains `ScheduleID string` populated from the DB column.

Both `MemoryRepository` and `MySQLRepository` implement the new signature.

---

## `internal/schedule` Package

### `service.go` — CRUD

`Service` owns all CRUD operations on `scheduled_tasks`. It calls `Scheduler.Reload` or `Scheduler.Remove` after every write so the running cron always reflects the DB state.

```
Service.Create  → validate, INSERT, call scheduler.Reload(id)
Service.Update  → validate, UPDATE, call scheduler.Remove(id) + scheduler.Reload(id)
Service.Delete  → DELETE, call scheduler.Remove(id)
Service.Toggle  → UPDATE enabled=true/false, then Reload or Remove
Service.Get     → SELECT by id + userID (ownership check)
Service.List    → SELECT by userID
```

**Validation rules (enforced in `Create` and `Update`):**

| Field | Rule |
|---|---|
| `cron_expr` | Must parse via `robfig/cron` parser (descriptors allowed: `@daily`, `@hourly`, etc.) or equal `"@once"` |
| `run_at` | Required and must be in the future when `cron_expr == "@once"` |
| `timeout_secs` | 0 defaults to 1800; valid range `[60, 86400]` |
| `concurrency` | Must be `0` (skip) or `1` (allow parallel) |

`ValidateCronExpr(expr string) error` is exported for use in other packages.

`ErrNotFound` is a sentinel returned by `Get` when the record does not exist (maps to HTTP 404).

### `scheduler.go` — Cron Runner

`Scheduler` owns the `robfig/cron.Cron` instance and a `map[scheduleID]cron.EntryID`.

```
Start(ctx)  → SELECT all enabled schedules → register each → c.Start()
Stop()      → c.Stop() (drains in-flight jobs)
Reload(id)  → Remove(id) → re-read DB → register (no-op if not found or disabled)
Remove(id)  → c.Remove(entryID); delete from map
```

**Recurring schedules** are registered with `c.AddFunc(cronExpr, func() { fire(id) })`.

**One-shot (`@once`) schedules** use a custom `cron.Schedule` implementation:

```go
type onceSpec struct { t time.Time }

func (o onceSpec) Next(after time.Time) time.Time {
    if after.Before(o.t) { return o.t }
    return time.Time{} // zero = never again
}
```

`s.c.Schedule(onceSpec{t: *rec.RunAt}, &onceJob{...})` is used instead of `AddFunc` because `AddJob` requires a string spec. After firing, `onceJob.Run` calls `s.Remove(id)` to clean up its own entry, then delegates to `fire`.

### `runner.go` — Fire Logic

`runFire(ctx, gormDB, taskSvc, schedID)` is the single function called on each cron tick:

```
1. Load db.ScheduledTask (must be enabled)
2. If Concurrency == 0: count active tasks with this schedule_id
      state NOT IN (StateError, StateNew) → count > 0 → return nil (skip)
3. Load db.User by UserID to get username
4. Unmarshal ExtraEnv JSON
5. taskSvc.CreateTask(ctx, username, extraEnv, gitURL, schedID)
6. t.SetTitle("<Title> – YYYY-MM-DD HH:mm")
7. UPDATE scheduled_tasks: last_run_at = now, next_run_at = sched.Next(now)
      For @once: also set enabled=false, next_run_at=nil
8. go func(): context.WithTimeout(Background(), TimeoutSecs)
      EnsureProvisioned → ProvisionForTask
      StreamMessage(ctx, t, Prompt, discardResponseWriter{})
```

The goroutine uses `context.Background()` (not the request context) so provisioning survives client disconnects. `discardResponseWriter` satisfies `http.ResponseWriter` + `http.Flusher` by discarding all bytes — the transcript is still written to OFS because the proxy writes to the `*task.Task` pointer directly, not to the writer.

**`TaskService` interface** (implemented by `TaskServiceImpl`):

```go
type TaskService interface {
    CreateTask(ctx, username, extraEnv, gitURL, scheduleID) (*task.Task, error)
    EnsureProvisioned(ctx, t) error
    StreamMessage(ctx, t, prompt) error
}
```

`TaskServiceImpl` wraps `task.Repository`, `SandboxManager`, and `Proxy`.

---

## API

All schedule endpoints require a valid Bearer token. The user's schedule visibility is scoped to their own `user_id`.

### `GET /api/schedules`

Returns all schedules for the authenticated user.

**Response** `200 []scheduleResponse`

### `POST /api/schedules`

Creates a new schedule.

**Request body** (`createScheduleRequest`):

```json
{
  "title":        "optional display name",
  "prompt":       "Summarize today's commits",
  "cron_expr":    "@daily",
  "run_at":       null,
  "git_url":      "",
  "timeout_secs": 1800,
  "concurrency":  0,
  "extra_env":    {}
}
```

`prompt` and `cron_expr` are required. For `@once`, `run_at` (RFC3339) is required and must be in the future.

**Response** `201 scheduleResponse`

### `GET /api/schedules/:id`

**Response** `200 scheduleResponse` or `404`.

### `PUT /api/schedules/:id`

Partial update — only non-nil pointer fields are applied.

**Request body** (`updateScheduleRequest`): same fields as create but all are optional pointers.

**Response** `200 scheduleResponse`.

### `DELETE /api/schedules/:id`

Deletes the schedule. Existing run tasks are **not** deleted.

**Response** `204`.

### `POST /api/schedules/:id/enable` / `POST /api/schedules/:id/disable`

Toggles the `enabled` flag and re-registers or removes the cron entry.

**Response** `200 scheduleResponse`.

### `POST /api/schedules/:id/run`

Fires the schedule immediately (ignores concurrency policy). Creates a task and returns its ID.

**Response** `200 { "task_id": "<uuid>" }`.

**Frontend** navigates to `/?task=<task_id>` so the user sees the live session.

### `GET /api/schedules/:id/runs`

Lists all tasks created by this schedule (newest first).

**Response** `200 []runListItem`:

```json
[
  {
    "id":         "<task_uuid>",
    "title":      "Daily standup – 2026-05-14 08:00",
    "state":      "active",
    "error_msg":  "",
    "created_at": "...",
    "updated_at": "..."
  }
]
```

`state` is the derived API label (`pending`, `provisioning`, `idle`, `active`, `error`, `resuming`, `paused`) computed the same way as `GET /api/tasks`.

### `scheduleResponse` shape

```json
{
  "id":           "<uuid>",
  "title":        "Daily standup summary",
  "prompt":       "Summarize today's commits...",
  "cron_expr":    "@daily",
  "run_at":       null,
  "git_url":      "",
  "timeout_secs": 1800,
  "concurrency":  0,
  "enabled":      true,
  "last_run_at":  "2026-05-14T08:00:00Z",
  "next_run_at":  "2026-05-15T08:00:00Z",
  "created_at":   "2026-05-01T10:00:00Z"
}
```

---

## Configuration

`pkg/config/config.go` — `ScheduleConfig` under the `schedule` key:

```yaml
schedule:
  enabled:        true   # false disables the cron runner (useful on read-only replicas)
  max_concurrent: 50     # global cap on simultaneous schedule-triggered sandbox runs
```

Defaults: `enabled=true`, `max_concurrent=50`.

When `enabled=false`, `NewScheduler` is still created and passed to `NewService` (for CRUD), but `Start` is never called so no cron entries run.

---

## Wiring (`cmd/server/main.go`)

```
task.Repository  ─┐
sandbox.Manager  ─┤→ schedule.TaskServiceImpl
sandbox.Proxy    ─┘
                   ↓
              schedule.NewScheduler(gormDB, taskServiceImpl)
                   ↓
              schedule.NewService(gormDB, scheduler)
                   ↓
              api.RouterDeps{ ScheduleService: schedSvc }
                   ↓
              NewRouter → h.withSchedule(schedSvc)
                        → registers 9 schedule routes

On server start:
  if cfg.Schedule.Enabled: scheduler.Start(ctx)
On server shutdown:
  scheduler.Stop()
```

---

## Frontend

### API client (`src/api/client.ts`)

New types: `Schedule`, `CreateSchedulePayload`, `UpdateSchedulePayload`, `ScheduleRun`.

`TaskSummary` gains `schedule_id?: string`.

New functions: `listSchedules`, `getSchedule`, `createSchedule`, `updateSchedule`, `deleteSchedule`, `enableSchedule`, `disableSchedule`, `runScheduleNow`, `listScheduleRuns`.

### Routes (`src/App.tsx`)

| Route | Component |
|---|---|
| `/schedules` | `SchedulesPage` |
| `/schedules/new` | `ScheduleFormPage mode="create"` |
| `/schedules/:id` | `ScheduleDetailPage` |
| `/schedules/:id/edit` | `ScheduleFormPage mode="edit"` |

### `SchedulesPage`

Lists all user schedules with enabled toggle, delete, and link to detail. Calendar icon in the `ChatPage` header navigates here.

### `ScheduleFormPage`

Create/edit form with:
- Title (optional), Prompt (required)
- Schedule type toggle: **Recurring (cron)** / **One-time**
  - Recurring: free-form cron expression input with human-readable preview via `describeCron`
  - One-time: `<input type="datetime-local">` for `run_at`
- Git URL (optional)
- Timeout slider (1 min – 24 h)
- Concurrency selector (Skip / Allow parallel)

### `ScheduleDetailPage`

Shows schedule metadata, "Run now" button (calls `runScheduleNow` → navigates to `/?task=<id>`), and a run history table. Each row links to `/?task=<run_id>` to open the session in the chat view.

### `src/lib/cron.ts`

`describeCron(expr)` — wraps `cronstrue` to produce human-readable descriptions (`"@daily"` → `"Every day at 12:00 AM"`).  
`formatNextRun(nextRunAt?)` — formats the next scheduled fire time for display.

### Chat integration

**`HistorySidepanel`**: tasks with `schedule_id` show a blue `<Calendar>` icon prefix instead of the default `<GitBranch>` icon.

**`ChatPage`**: a `useEffect` on mount reads the `?task=<id>` query param and calls `handleSelectTask(id)` to open the session, then clears the param from the URL. This is used by "Run now" and "Open →" navigation from schedule pages.

---

## Key Invariants

| # | Invariant |
|---|-----------|
| 1 | A schedule firing creates a normal `Task` — the task layer is unchanged. The scheduler only provides the first prompt. |
| 2 | `schedule_id` on a task is immutable once set (set at `Create`, never updated). |
| 3 | Deleting a schedule leaves its child tasks intact in MySQL and OFS. |
| 4 | For `@once` schedules: `enabled` is set to `false` after firing, and the cron entry removes itself. The record remains for history. |
| 5 | `discardResponseWriter` ensures the proxy's SSE output (which contains session tracking and OFS writes) runs to completion without requiring a real HTTP client. |
| 6 | `context.Background()` is used for the goroutine, not the fire-call context, so sandbox provisioning survives beyond the cron tick's goroutine lifetime. |

---

## Related Documents

- [`data-management.md`](data-management.md) — `tasks` schema including `schedule_id`
- [`resource-mapping.md`](resource-mapping.md) — Task/Sandbox/Session lifecycle
- [`git-task-integration.md`](git-task-integration.md) — `git_url` support (used by schedules too)
- [`configuration.md`](configuration.md) — `schedule` config block
