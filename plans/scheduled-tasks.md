# Scheduled Tasks

Allow users to create scheduled tasks: a prompt template that fires on a cron schedule (or once at a specific time). Each firing produces a **first-class `Task`** — identical to a manually-created task — with its own sandbox, SSE stream, OFS-persisted session transcript, and the ability to continue chatting with the agent at any point.

## Goals

- Users can create, edit, enable/disable, and delete schedules via UI
- Schedules support cron expressions and one-shot datetime triggers
- Each execution is a full `Task`: own sandbox, own `session_id`, own OFS transcript
- While a run is active, user can open it and chat with the agent in real time (live SSE)
- After a run completes, user can open it and resume the conversation (history replay → continue)
- Run tasks appear in the normal task sidebar alongside manually created tasks
- Concurrent runs respect a configurable concurrency policy (skip or allow)
- Full run history visible per schedule, each run linked back to its schedule
- Notifications on failure (webhook, in-app)

---

## Data model

### New table: `scheduled_tasks`

```sql
CREATE TABLE scheduled_tasks (
    id            VARCHAR(36)  NOT NULL PRIMARY KEY,
    user_id       BIGINT       NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title         VARCHAR(255) NOT NULL DEFAULT '',
    prompt        TEXT         NOT NULL,
    cron_expr     VARCHAR(100) NOT NULL,           -- cron expression or '@once'
    run_at        DATETIME     DEFAULT NULL,       -- set only for one-shot '@once'
    extra_env     TEXT         DEFAULT NULL,       -- JSON map same as tasks.extra_env
    git_url       VARCHAR(512) DEFAULT NULL,
    timeout_secs  INT          NOT NULL DEFAULT 1800,  -- max run wall time
    concurrency   TINYINT      NOT NULL DEFAULT 0, -- 0=skip, 1=allow
    enabled       TINYINT(1)   NOT NULL DEFAULT 1,
    last_run_at   DATETIME     DEFAULT NULL,
    next_run_at   DATETIME     DEFAULT NULL,
    created_at    DATETIME     NOT NULL,
    updated_at    DATETIME     NOT NULL,
    INDEX idx_user_id (user_id),
    INDEX idx_next_run (next_run_at, enabled)
);
```

### `tasks` table — add `schedule_id` column

```sql
ALTER TABLE tasks ADD COLUMN schedule_id VARCHAR(36) DEFAULT NULL;
ALTER TABLE tasks ADD INDEX idx_schedule_id (schedule_id);
```

This links execution tasks back to their parent schedule. No FK constraint (schedule may be deleted while runs are kept).

### Go models

`internal/db/scheduled_task.go`:

```go
type ScheduledTask struct {
    ID           string     `gorm:"primaryKey;size:36"`
    UserID       uint       `gorm:"not null;index"`
    User         User       `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
    Title        string     `gorm:"size:255"`
    Prompt       string     `gorm:"type:text;not null"`
    CronExpr     string     `gorm:"size:100;not null"`
    RunAt        *time.Time `gorm:"default:null"`
    ExtraEnv     string     `gorm:"type:text"`
    GitURL       string     `gorm:"size:512"`
    TimeoutSecs  int        `gorm:"not null;default:1800"`
    Concurrency  int        `gorm:"not null;default:0"` // 0=skip, 1=allow
    Enabled      bool       `gorm:"not null;default:true"`
    LastRunAt    *time.Time
    NextRunAt    *time.Time
    CreatedAt    time.Time
    UpdatedAt    time.Time
}
```

Add `ScheduleID *string` to `db.Task`.

---

## Backend

### Package: `internal/schedule/`

New package owning all scheduler logic.

#### `scheduler.go` — top-level runner

```go
type Scheduler struct {
    cron    *cron.Cron
    db      *gorm.DB
    taskSvc TaskService   // interface — creates task + sends first message
    entries map[string]cron.EntryID
    mu      sync.Mutex
}

func New(db *gorm.DB, taskSvc TaskService) *Scheduler
func (s *Scheduler) Start(ctx context.Context)
func (s *Scheduler) Stop()
func (s *Scheduler) Reload(scheduleID string)  // called after create/update/delete
```

On `Start`:
1. Load all `enabled=1` schedules from DB.
2. Register each with `robfig/cron`.
3. Spin a goroutine watching a reload channel for live updates.

#### `runner.go` — per-schedule fire logic

```go
func (s *Scheduler) fire(schedID string)
```

1. Load schedule from DB (re-fetch to get latest state).
2. If `concurrency=skip`: count running child tasks where `schedule_id=schedID` and state is not terminal; skip if >0.
3. Create a new `Task` via `taskSvc.CreateTask(...)` with `ScheduleID=&schedID`. The new task is a normal task — it uses the same `task.Repository`, gets its own UUID, and is immediately visible in the user's task list.
4. Auto-title: set `task.Title` to `"[schedule name] – <date>"` so it's identifiable in the sidebar.
5. Provision sandbox via `taskSvc.EnsureProvisioned(...)` (same `sandbox.Manager` path as the HTTP handler).
6. Send first message = `schedule.Prompt` via `taskSvc.StreamMessage(...)`. The SSE response is consumed internally (discarded or logged); the transcript is written to OFS as normal by the existing sandbox/proxy layer.
7. Update `scheduled_tasks.last_run_at = now`, `next_run_at = nextFromCron`.
8. Enforce timeout: after `TimeoutSecs`, call `manager.DeleteSandbox(...)` if the task is still in `StateRunning`.

**The run task is a normal task after step 3.** The scheduler only fires it; all subsequent lifecycle (sandbox health, session_id write-once, OFS history, SSE streaming, permission responses) is handled by the existing layers unchanged.

#### `service.go` — DB CRUD

```go
type Service struct { db *gorm.DB; scheduler *Scheduler }

func (s *Service) Create(ctx context.Context, userID uint, req CreateRequest) (*ScheduledTask, error)
func (s *Service) Update(ctx context.Context, id string, userID uint, req UpdateRequest) (*ScheduledTask, error)
func (s *Service) Delete(ctx context.Context, id string, userID uint) error
func (s *Service) Get(ctx context.Context, id string, userID uint) (*ScheduledTask, error)
func (s *Service) List(ctx context.Context, userID uint) ([]ScheduledTask, error)
func (s *Service) ListRuns(ctx context.Context, schedID string, userID uint) ([]task.TaskSummary, error)
func (s *Service) Toggle(ctx context.Context, id string, userID uint, enabled bool) error
```

`Create` / `Update` call `scheduler.Reload(id)` after DB write so the cron runner picks up changes immediately without restart.

#### `TaskService` interface

```go
type TaskService interface {
    CreateTask(ctx context.Context, username string, extraEnv map[string]string, gitURL string, scheduleID string) (*task.Task, error)
    EnsureProvisioned(ctx context.Context, t *task.Task) error
    // StreamMessage sends prompt, consumes the SSE response internally (transcript
    // is persisted to OFS by the proxy layer as usual), and returns when the agent
    // signals session.completed.
    StreamMessage(ctx context.Context, t *task.Task, prompt string) error
}
```

Implemented by wiring `task.Repository` + `sandbox.Manager` + `MessageProxy` — the exact same deps the HTTP handler has. No new infrastructure.

### API endpoints

Add to `internal/api/handlers.go` and register in `internal/api/router.go` under the auth-required group:

| Method | Path | Description |
|--------|------|-------------|
| `GET`    | `/api/schedules`                    | List user's schedules |
| `POST`   | `/api/schedules`                    | Create schedule |
| `GET`    | `/api/schedules/:id`                | Get single schedule |
| `PUT`    | `/api/schedules/:id`                | Update schedule |
| `DELETE` | `/api/schedules/:id`                | Delete schedule |
| `POST`   | `/api/schedules/:id/enable`         | Enable schedule |
| `POST`   | `/api/schedules/:id/disable`        | Disable schedule |
| `POST`   | `/api/schedules/:id/run`            | Trigger manual run now |
| `GET`    | `/api/schedules/:id/runs`           | List past executions (TaskSummary[]) |

**Request/response types:**

```go
type CreateScheduleRequest struct {
    Title       string            `json:"title"`
    Prompt      string            `json:"prompt" binding:"required"`
    CronExpr    string            `json:"cron_expr" binding:"required"`
    RunAt       *time.Time        `json:"run_at"`
    ExtraEnv    map[string]string `json:"extra_env"`
    GitURL      string            `json:"git_url"`
    TimeoutSecs int               `json:"timeout_secs"`
    Concurrency int               `json:"concurrency"` // 0=skip,1=allow
}

type ScheduleResponse struct {
    ID          string            `json:"id"`
    Title       string            `json:"title"`
    Prompt      string            `json:"prompt"`
    CronExpr    string            `json:"cron_expr"`
    RunAt       *time.Time        `json:"run_at"`
    ExtraEnv    map[string]string `json:"extra_env"`
    GitURL      string            `json:"git_url"`
    TimeoutSecs int               `json:"timeout_secs"`
    Concurrency int               `json:"concurrency"`
    Enabled     bool              `json:"enabled"`
    LastRunAt   *time.Time        `json:"last_run_at"`
    NextRunAt   *time.Time        `json:"next_run_at"`
    CreatedAt   time.Time         `json:"created_at"`
}
```

Validation:
- `cron_expr`: parsed with `robfig/cron`; reject if invalid. If `cron_expr == "@once"`, `run_at` is required and must be in the future.
- `timeout_secs`: 60–86400.
- `concurrency`: 0 or 1 only.

### Wiring in `cmd/server/main.go`

```go
schedSvc := schedule.NewService(gormDB, scheduler)
scheduler := schedule.New(gormDB, taskServiceImpl)
scheduler.Start(serverCtx)
defer scheduler.Stop()

// pass schedSvc into RouterDeps
```

Add `ScheduleService` to `api.RouterDeps` and `api.Handler` (same pattern as `KindsRepo`).

---

## Frontend

### New pages and routes

| Route | Component | Description |
|-------|-----------|-------------|
| `/schedules` | `SchedulesPage.tsx` | List all schedules |
| `/schedules/new` | `ScheduleFormPage.tsx` | Create form |
| `/schedules/:id` | `ScheduleDetailPage.tsx` | Detail + run history |
| `/schedules/:id/edit` | `ScheduleFormPage.tsx` | Edit form (same component, mode prop) |

Add "Schedules" entry to the sidebar nav (below "Tasks").

### `SchedulesPage` — list view

```
┌────────────────────────────────────────────────────────┐
│  Schedules                              [+ New Schedule]│
│                                                        │
│  ● Daily standup summary     @daily     Next: 09:00    │
│    Enabled                                             │
│                                                        │
│  ○ Weekly report             0 9 * * 1  Next: Mon      │
│    Disabled                                            │
│                                                        │
└────────────────────────────────────────────────────────┘
```

- Toggle switch for enable/disable (calls `/enable` or `/disable`).
- Click row → `/schedules/:id`.

### `ScheduleFormPage` — create/edit

Fields:
- Title (text)
- Prompt (textarea, large)
- Schedule type: "Recurring (cron)" | "One-time"
  - Recurring: cron expression input + human-readable preview ("runs every day at 9am")
  - One-time: datetime picker → sets `run_at`, `cron_expr="@once"`
- Git URL (optional)
- Extra env (key-value editor, same pattern as task form if it exists)
- Timeout (slider: 1min–24hrs)
- On conflict: "Skip if running" | "Allow parallel"

### `ScheduleDetailPage` — detail + history

```
┌────────────────────────────────────────────────────────┐
│  ← Schedules   Daily standup summary    [Edit] [Delete]│
│                                                        │
│  Schedule: @daily   Next run: 2026-05-15 09:00         │
│  Status: ● Enabled                        [Run Now]    │
│                                                        │
│  Prompt:                                               │
│  ┌──────────────────────────────────────────────────┐  │
│  │ Summarize today's commits and open PRs...        │  │
│  └──────────────────────────────────────────────────┘  │
│                                                        │
│  Run History                                           │
│  ┌──────────────────────────────────────────────────┐  │
│  │  2026-05-14 09:00  ● running     —        [Open] │  │
│  │  2026-05-13 09:00  ✓ completed   1m 23s   [Open] │  │
│  │  2026-05-12 09:00  ✗ error       0m 05s   [Open] │  │
│  └──────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────┘
```

**"Open" navigates to `/tasks/:runId`** — the existing `ChatPage`. No new chat UI needed. Behaviour is identical to clicking a task in the sidebar:

- **Run is active** (`state=running`): `ChatPage` connects live SSE, user can send follow-up messages to the agent in real time (same `useChat.sendMessage` path).
- **Run is complete/error**: `ChatPage` replays history from OFS via `getHistory` → `buildMessages` → `loadTask`, and the input is enabled so the user can continue the conversation (which re-provisions a sandbox on demand, per existing `EnsureProvisioned` logic).

Run tasks also appear in the **normal task sidebar** (they are regular tasks), so users can reach them there too. The sidebar entry shows the auto-generated title `"[schedule] – YYYY-MM-DD HH:mm"`.

The `TaskSummary` returned by `GET /api/schedules/:id/runs` includes `state` so the UI can render the correct status badge and duration (computed from `created_at` to `updated_at` for completed runs).

### API client additions (`src/api/client.ts`)

```ts
listSchedules(): Promise<ScheduleResponse[]>
createSchedule(body: CreateScheduleRequest): Promise<ScheduleResponse>
getSchedule(id: string): Promise<ScheduleResponse>
updateSchedule(id: string, body: Partial<CreateScheduleRequest>): Promise<ScheduleResponse>
deleteSchedule(id: string): Promise<void>
enableSchedule(id: string): Promise<void>
disableSchedule(id: string): Promise<void>
// Returns the task_id of the spawned run so the UI can navigate directly to /tasks/:id
runScheduleNow(id: string): Promise<{ task_id: string }>
// TaskSummary already typed in the codebase; add schedule_id field
listScheduleRuns(id: string): Promise<TaskSummary[]>
```

`TaskSummary` needs a `schedule_id?: string` field added to the existing type (both backend struct and frontend type) so:
1. The sidebar can show a small calendar icon on tasks spawned by a schedule.
2. `ChatPage` can show a breadcrumb "← Daily standup summary" linking back to the schedule detail.

### Cron expression helper

Small utility `src/lib/cron.ts` wrapping `cronstrue` (npm) to produce human-readable descriptions:

```ts
import cronstrue from 'cronstrue'
export function describeCron(expr: string): string
```

---

## Notifications (v1: failure only)

When a run ends in `StateError`, write a notification record and surface it in-app. Webhook support (POST to a user-configured URL) can be v2.

In-app: a notification badge on the sidebar "Schedules" nav item; clicking it navigates to the failed run.

---

## Config additions (`pkg/config/config.go`)

```yaml
schedule:
  enabled: true           # set false to disable scheduler (e.g. read-only replicas)
  max_concurrent: 50      # global cap on simultaneous schedule-triggered runs
```

---

## Task breakdown

1. **DB migration** — `scheduled_tasks` table + `schedule_id` on `tasks`; update GORM models
2. **`internal/schedule` package** — `Scheduler`, `Service`, `TaskService` interface
3. **API handlers + router** — all 9 endpoints, Swagger annotations, regenerate docs
4. **Wiring** — `cmd/server/main.go` + `api.RouterDeps`
5. **Timeout enforcement** — goroutine-per-run or periodic sweeper kills overdue sandboxes
6. **Frontend: API client** — `src/api/client.ts` additions + TypeScript types
7. **Frontend: SchedulesPage** — list with toggle
8. **Frontend: ScheduleFormPage** — create/edit with cron preview
9. **Frontend: ScheduleDetailPage** — detail + run history
10. **Sidebar nav entry** — link to `/schedules`
11. **Tests** — `internal/schedule/` unit tests (mocked TaskService); smoke test for API
