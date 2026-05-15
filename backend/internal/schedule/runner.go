package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/internal/sandbox"
	"github.com/l-lab/cloud-agents/internal/task"
	"github.com/l-lab/cloud-agents/pkg/logger"
	"gorm.io/gorm"
)

// TaskService is the minimal interface needed by the scheduler to create and run tasks.
// Implemented by taskServiceImpl which wraps the real task.Repository + sandbox.Manager + proxy.
type TaskService interface {
	CreateTask(ctx context.Context, username string, extraEnv map[string]string, gitURL string, scheduleID string) (*task.Task, error)
	EnsureProvisioned(ctx context.Context, t *task.Task) error
	// StreamMessage sends the prompt to the agent and consumes the SSE response internally.
	// The transcript is written to OFS by the existing proxy layer; this method blocks
	// until the agent signals session.completed or the context is cancelled.
	StreamMessage(ctx context.Context, t *task.Task, prompt string) error
}

// runFire is the core fire logic for a single schedule execution.
func runFire(ctx context.Context, gormDB *gorm.DB, taskSvc TaskService, schedID string) error {
	var rec db.ScheduledTask
	if err := gormDB.WithContext(ctx).Where("id = ? AND enabled = ?", schedID, true).First(&rec).Error; err != nil {
		return fmt.Errorf("load schedule %s: %w", schedID, err)
	}

	// Concurrency=skip: abort if a run for this schedule is still active.
	if rec.Concurrency == 0 {
		var count int64
		gormDB.WithContext(ctx).Model(&db.Task{}).
			Where("schedule_id = ? AND state NOT IN (?, ?)", schedID, int(task.StateError), int(task.StateNew)).
			Count(&count)
		if count > 0 {
			logger.Default().Info("schedule: skipping fire — previous run still active", "id", schedID)
			return nil
		}
	}

	// Resolve user name from UserID.
	var user db.User
	if err := gormDB.WithContext(ctx).Where("id = ?", rec.UserID).First(&user).Error; err != nil {
		return fmt.Errorf("load user for schedule %s: %w", schedID, err)
	}

	var extraEnv map[string]string
	if rec.ExtraEnv != "" && rec.ExtraEnv != "null" {
		if err := json.Unmarshal([]byte(rec.ExtraEnv), &extraEnv); err != nil {
			return fmt.Errorf("unmarshal extra_env for schedule %s: %w", schedID, err)
		}
	}

	t, err := taskSvc.CreateTask(ctx, user.UserName, extraEnv, rec.GitURL, schedID)
	if err != nil {
		return fmt.Errorf("create task for schedule %s: %w", schedID, err)
	}

	// Auto-title: "[schedule title] – YYYY-MM-DD HH:mm"
	title := rec.Title
	if title == "" {
		title = schedID
	}
	t.SetTitle(fmt.Sprintf("%s – %s", title, time.Now().Format("2006-01-02 15:04")))

	// Update last_run_at + next_run_at.
	now := time.Now()
	updates := map[string]any{"last_run_at": now}
	if rec.CronExpr != "@once" {
		sched, err := parser.Parse(rec.CronExpr)
		if err == nil {
			next := sched.Next(now)
			updates["next_run_at"] = next
		}
	} else {
		// One-shot: disable after firing.
		updates["enabled"] = false
		updates["next_run_at"] = nil
	}
	gormDB.WithContext(ctx).Model(&db.ScheduledTask{}).Where("id = ?", schedID).Updates(updates)

	// Provision and run the agent in a goroutine so fire() returns quickly.
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), time.Duration(rec.TimeoutSecs)*time.Second)
		defer cancel()

		if err := taskSvc.EnsureProvisioned(runCtx, t); err != nil {
			t.SetError(err.Error())
			logger.Default().Error("schedule: provision failed", "schedule_id", schedID, "task_id", t.ID, "err", err)
			return
		}

		if err := taskSvc.StreamMessage(runCtx, t, rec.Prompt); err != nil {
			logger.Default().Error("schedule: stream error", "schedule_id", schedID, "task_id", t.ID, "err", err)
		}
	}()

	return nil
}

// ---- onceSpec + onceJob: cron schedule that fires once at a specific time ----

// onceSpec implements cron.Schedule for a one-shot fire at a specific time.
type onceSpec struct {
	t time.Time
}

func (o onceSpec) Next(after time.Time) time.Time {
	if after.Before(o.t) {
		return o.t
	}
	return time.Time{} // zero means "never again"
}

// onceJob wraps the fire call and removes itself from the scheduler after firing.
type onceJob struct {
	s       *Scheduler
	schedID string
}

func (j *onceJob) Run() {
	j.s.Remove(j.schedID)
	j.s.fire(j.schedID)
}

// ---- TaskServiceImpl: wraps real task/sandbox deps ----

// SandboxManager is the subset of sandbox.Manager used by TaskServiceImpl.
type SandboxManager interface {
	ProvisionForTask(ctx context.Context, t *task.Task) error
}

// Proxy is the subset of sandbox.Proxy used by TaskServiceImpl.
type Proxy interface {
	StreamMessage(ctx context.Context, t *task.Task, prompt string, blocks []sandbox.ContentBlock, w http.ResponseWriter) error
}

// TaskServiceImpl implements TaskService using the real infrastructure.
type TaskServiceImpl struct {
	Repo    task.Repository
	Manager SandboxManager
	Proxy   Proxy
}

func (ts *TaskServiceImpl) CreateTask(ctx context.Context, username string, extraEnv map[string]string, gitURL string, scheduleID string) (*task.Task, error) {
	return ts.Repo.Create(ctx, username, extraEnv, gitURL, scheduleID)
}

func (ts *TaskServiceImpl) EnsureProvisioned(ctx context.Context, t *task.Task) error {
	t.SetProvisioning()
	return t.EnsureProvisioned(func() error {
		return ts.Manager.ProvisionForTask(ctx, t)
	})
}

// StreamMessage forwards the prompt to the agent and discards the SSE response body
// (the transcript is already persisted to OFS by the existing proxy layer).
func (ts *TaskServiceImpl) StreamMessage(ctx context.Context, t *task.Task, prompt string) error {
	// Drain SSE into a discard writer — the proxy still writes session.init,
	// persists session_id, etc., because it uses the Task pointer directly.
	w := &discardResponseWriter{}
	return ts.Proxy.StreamMessage(ctx, t, prompt, nil, w)
}

// discardResponseWriter satisfies http.ResponseWriter + http.Flusher by discarding all output.
type discardResponseWriter struct {
	header http.Header
}

func (d *discardResponseWriter) Header() http.Header {
	if d.header == nil {
		d.header = make(http.Header)
	}
	return d.header
}

func (d *discardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardResponseWriter) WriteHeader(_ int)           {}
func (d *discardResponseWriter) Flush()                      {}

// Ensure TaskServiceImpl satisfies TaskService at compile time.
var _ TaskService = (*TaskServiceImpl)(nil)

