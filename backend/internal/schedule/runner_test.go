package schedule

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/l-lab/cloud-agents/internal/db"
	"github.com/l-lab/cloud-agents/internal/task"
)

// ---- mock TaskService ----

type mockTaskSvc struct {
	mu           sync.Mutex
	created      []*task.Task
	provisionErr error
	streamErr    error
	// done is closed by StreamMessage (or EnsureProvisioned on error) so tests can
	// wait for the async goroutine launched by runFire to complete.
	done chan struct{}
}

func newMockTaskSvc() *mockTaskSvc {
	return &mockTaskSvc{done: make(chan struct{})}
}

func (m *mockTaskSvc) CreateTask(_ context.Context, username string, extraEnv map[string]string, gitURL string, scheduleID string) (*task.Task, error) {
	s := task.NewStore()
	t := s.Create(username, extraEnv, gitURL)
	m.mu.Lock()
	m.created = append(m.created, t)
	m.mu.Unlock()
	return t, nil
}

func (m *mockTaskSvc) EnsureProvisioned(_ context.Context, t *task.Task) error {
	if m.provisionErr != nil {
		// Signal done so test doesn't hang — goroutine returns after SetError.
		select {
		case <-m.done:
		default:
			close(m.done)
		}
		return m.provisionErr
	}
	t.SetRunning("sandbox-1", "http://fake/", map[string]string{})
	return nil
}

func (m *mockTaskSvc) StreamMessage(_ context.Context, _ *task.Task, _ string) error {
	select {
	case <-m.done:
	default:
		close(m.done)
	}
	return m.streamErr
}

// waitDone waits for the background goroutine to complete, with a generous timeout
// to avoid test flakiness.
func (m *mockTaskSvc) waitDone(t *testing.T) {
	t.Helper()
	select {
	case <-m.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for runFire goroutine")
	}
}

// ---- helpers ----

// ---- runFire tests ----
// runFire is called directly — no cron timer needed.

func TestRunFire_CreatesTaskAndRuns(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")

	rec := &db.ScheduledTask{
		ID: "sched-1", UserID: uid, Title: "daily check",
		Prompt: "say hello", CronExpr: "@daily",
		TimeoutSecs: 1800, Enabled: true,
	}
	if err := g.Create(rec).Error; err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	svc := newMockTaskSvc()
	if err := runFire(context.Background(), g, svc, rec.ID); err != nil {
		t.Fatalf("runFire: %v", err)
	}

	svc.waitDone(t)

	svc.mu.Lock()
	created := svc.created
	svc.mu.Unlock()

	if len(created) != 1 {
		t.Fatalf("expected 1 task created, got %d", len(created))
	}

	// Auto-title must start with the schedule title.
	title := created[0].GetTitle()
	if !strings.HasPrefix(title, "daily check – ") {
		t.Errorf("unexpected auto-title: %q", title)
	}
}

func TestRunFire_UpdatesLastRunAt(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")

	rec := &db.ScheduledTask{
		ID: "sched-last", UserID: uid,
		Prompt: "p", CronExpr: "@daily",
		TimeoutSecs: 1800, Enabled: true,
	}
	g.Create(rec)

	svc := newMockTaskSvc()
	before := time.Now()
	if err := runFire(context.Background(), g, svc, rec.ID); err != nil {
		t.Fatalf("runFire: %v", err)
	}
	svc.waitDone(t)

	var updated db.ScheduledTask
	g.Where("id = ?", rec.ID).First(&updated)
	if updated.LastRunAt == nil || updated.LastRunAt.Before(before) {
		t.Errorf("expected last_run_at to be set to ~now, got %v", updated.LastRunAt)
	}
}

func TestRunFire_OnceSetsEnabledFalse(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")

	future := time.Now().Add(time.Minute)
	rec := &db.ScheduledTask{
		ID: "sched-once", UserID: uid,
		Prompt: "p", CronExpr: "@once", RunAt: &future,
		TimeoutSecs: 1800, Enabled: true,
	}
	g.Create(rec)

	svc := newMockTaskSvc()
	if err := runFire(context.Background(), g, svc, rec.ID); err != nil {
		t.Fatalf("runFire: %v", err)
	}
	svc.waitDone(t)

	var updated db.ScheduledTask
	g.Where("id = ?", rec.ID).First(&updated)
	if updated.Enabled {
		t.Error("expected @once schedule to be disabled after firing")
	}
	if updated.NextRunAt != nil {
		t.Errorf("expected next_run_at=nil after @once, got %v", updated.NextRunAt)
	}
}

func TestRunFire_ConcurrencySkip(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")

	rec := &db.ScheduledTask{
		ID: "sched-skip", UserID: uid,
		Prompt: "p", CronExpr: "@daily",
		TimeoutSecs: 1800, Concurrency: 0, Enabled: true,
	}
	g.Create(rec)

	// Insert a running task for this schedule (state=running=2).
	schedID := rec.ID
	activeTask := &db.Task{
		ID:         "run-active",
		UserID:     uid,
		State:      int(task.StateRunning),
		ScheduleID: &schedID,
	}
	g.Create(activeTask)

	svc := newMockTaskSvc()
	if err := runFire(context.Background(), g, svc, rec.ID); err != nil {
		t.Fatalf("runFire: %v", err)
	}

	// No goroutine is launched when skipping; done channel remains open.
	// Just verify no task was created.
	svc.mu.Lock()
	count := len(svc.created)
	svc.mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 tasks created when skipping, got %d", count)
	}
}

func TestRunFire_ConcurrencyAllow(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")

	rec := &db.ScheduledTask{
		ID: "sched-allow", UserID: uid,
		Prompt: "p", CronExpr: "@daily",
		TimeoutSecs: 1800, Concurrency: 1, Enabled: true,
	}
	g.Create(rec)

	// Insert a running task for this schedule (same as above).
	schedID := rec.ID
	activeTask := &db.Task{
		ID:         "run-active-2",
		UserID:     uid,
		State:      int(task.StateRunning),
		ScheduleID: &schedID,
	}
	g.Create(activeTask)

	svc := newMockTaskSvc()
	if err := runFire(context.Background(), g, svc, rec.ID); err != nil {
		t.Fatalf("runFire: %v", err)
	}
	svc.waitDone(t)

	svc.mu.Lock()
	count := len(svc.created)
	svc.mu.Unlock()
	if count != 1 {
		t.Errorf("concurrency=allow: expected 1 task created, got %d", count)
	}
}

func TestRunFire_DisabledScheduleErrors(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")

	rec := &db.ScheduledTask{
		ID: "sched-disabled", UserID: uid,
		Prompt: "p", CronExpr: "@daily",
		TimeoutSecs: 1800, Enabled: true,
	}
	g.Create(rec)
	// GORM skips zero-value booleans with a default tag, so update via Exec.
	g.Exec("UPDATE scheduled_tasks SET enabled = 0 WHERE id = ?", rec.ID)

	svc := newMockTaskSvc()
	err := runFire(context.Background(), g, svc, rec.ID)
	if err == nil {
		t.Fatal("expected error when schedule is disabled (not found in query)")
	}
}

func TestRunFire_AutoTitleFallsBackToID(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")

	// No title set — should fall back to schedule ID.
	rec := &db.ScheduledTask{
		ID: "sched-notitle", UserID: uid,
		Title: "", Prompt: "p", CronExpr: "@daily",
		TimeoutSecs: 1800, Enabled: true,
	}
	g.Create(rec)

	svc := newMockTaskSvc()
	if err := runFire(context.Background(), g, svc, rec.ID); err != nil {
		t.Fatalf("runFire: %v", err)
	}
	svc.waitDone(t)

	svc.mu.Lock()
	created := svc.created
	svc.mu.Unlock()
	if len(created) == 0 {
		t.Fatal("no task created")
	}
	title := created[0].GetTitle()
	if !strings.HasPrefix(title, rec.ID+" – ") {
		t.Errorf("expected title to start with schedule ID, got %q", title)
	}
}
