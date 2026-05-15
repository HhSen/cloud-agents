package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/l-lab/cloud-agents/internal/db"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTestDB opens a SQLite in-memory DB with the schedule schema migrated.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := g.AutoMigrate(&db.User{}, &db.Task{}, &db.ScheduledTask{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return g
}

// insertUser creates a minimal user row and returns its ID.
func insertUser(t *testing.T, g *gorm.DB, username string) uint {
	t.Helper()
	u := db.User{UserName: username, Email: username + "@test.local", PasswordHash: "x", IsActive: true}
	if err := g.Create(&u).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return u.ID
}

// newTestService builds a Service wired to a real Scheduler backed by the same DB.
// The Scheduler is not started, so no cron jobs actually fire.
func newTestService(t *testing.T, g *gorm.DB) *Service {
	t.Helper()
	// taskSvc is nil because the scheduler is never started and fire() is never called.
	sched := NewScheduler(g, nil)
	return NewService(g, sched)
}

// ---- ValidateCronExpr ----

func TestValidateCronExpr_Valid(t *testing.T) {
	cases := []string{"@daily", "@hourly", "*/5 * * * *", "0 9 * * 1-5", "@once"}
	for _, expr := range cases {
		if err := ValidateCronExpr(expr); err != nil {
			t.Errorf("expected valid expr %q, got error: %v", expr, err)
		}
	}
}

func TestValidateCronExpr_Invalid(t *testing.T) {
	cases := []string{"not-a-cron", "99 * * * *", "* * * * * * *"}
	for _, expr := range cases {
		if err := ValidateCronExpr(expr); err == nil {
			t.Errorf("expected invalid expr %q to return error", expr)
		}
	}
}

// ---- Service.Create ----

func TestService_Create_Valid(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	rec, err := svc.Create(context.Background(), uid, CreateRequest{
		Prompt:   "do something",
		CronExpr: "@daily",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.ID == "" {
		t.Error("expected non-empty ID")
	}
	if rec.CronExpr != "@daily" {
		t.Errorf("CronExpr = %q, want @daily", rec.CronExpr)
	}
	if rec.TimeoutSecs != 1800 {
		t.Errorf("TimeoutSecs = %d, want 1800 (default)", rec.TimeoutSecs)
	}
	if !rec.Enabled {
		t.Error("expected Enabled=true by default")
	}
	if rec.NextRunAt == nil {
		t.Error("expected NextRunAt to be computed for @daily")
	}
}

func TestService_Create_InvalidCron(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "bob")
	svc := newTestService(t, g)

	_, err := svc.Create(context.Background(), uid, CreateRequest{
		Prompt:   "do something",
		CronExpr: "not-valid",
	})
	if err == nil {
		t.Fatal("expected error for invalid cron_expr")
	}
}

func TestService_Create_OnceRequiresRunAt(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "bob")
	svc := newTestService(t, g)

	_, err := svc.Create(context.Background(), uid, CreateRequest{
		Prompt:   "once task",
		CronExpr: "@once",
		// RunAt intentionally omitted
	})
	if err == nil {
		t.Fatal("expected error when run_at missing for @once")
	}
}

func TestService_Create_OncePastRunAt(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "bob")
	svc := newTestService(t, g)

	past := time.Now().Add(-time.Hour)
	_, err := svc.Create(context.Background(), uid, CreateRequest{
		Prompt:   "once task",
		CronExpr: "@once",
		RunAt:    &past,
	})
	if err == nil {
		t.Fatal("expected error when run_at is in the past")
	}
}

func TestService_Create_InvalidTimeout(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "bob")
	svc := newTestService(t, g)

	_, err := svc.Create(context.Background(), uid, CreateRequest{
		Prompt:      "p",
		CronExpr:    "@daily",
		TimeoutSecs: 10, // below minimum of 60
	})
	if err == nil {
		t.Fatal("expected error for timeout_secs < 60")
	}
}

func TestService_Create_InvalidConcurrency(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "bob")
	svc := newTestService(t, g)

	_, err := svc.Create(context.Background(), uid, CreateRequest{
		Prompt:      "p",
		CronExpr:    "@daily",
		TimeoutSecs: 300,
		Concurrency: 5,
	})
	if err == nil {
		t.Fatal("expected error for concurrency=5")
	}
}

// ---- Service.Get ----

func TestService_Get_Found(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	created, _ := svc.Create(context.Background(), uid, CreateRequest{
		Prompt: "do something", CronExpr: "@daily",
	})

	got, err := svc.Get(context.Background(), created.ID, uid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, created.ID)
	}
}

func TestService_Get_NotFound(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	_, err := svc.Get(context.Background(), "nonexistent-id", uid)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestService_Get_WrongUser(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	uid2 := insertUser(t, g, "bob")
	svc := newTestService(t, g)

	created, _ := svc.Create(context.Background(), uid, CreateRequest{
		Prompt: "private", CronExpr: "@daily",
	})

	_, err := svc.Get(context.Background(), created.ID, uid2)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}
}

// ---- Service.Update ----

func TestService_Update_MutatesFields(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	created, _ := svc.Create(context.Background(), uid, CreateRequest{
		Title: "old title", Prompt: "old prompt", CronExpr: "@daily",
	})

	newTitle := "new title"
	newPrompt := "new prompt"
	updated, err := svc.Update(context.Background(), created.ID, uid, UpdateRequest{
		Title:  &newTitle,
		Prompt: &newPrompt,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "new title" {
		t.Errorf("Title = %q, want %q", updated.Title, "new title")
	}
	if updated.Prompt != "new prompt" {
		t.Errorf("Prompt = %q, want %q", updated.Prompt, "new prompt")
	}
}

func TestService_Update_NotFound(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	newTitle := "x"
	_, err := svc.Update(context.Background(), "nonexistent", uid, UpdateRequest{Title: &newTitle})
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestService_Update_InvalidTimeout(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	created, _ := svc.Create(context.Background(), uid, CreateRequest{
		Prompt: "p", CronExpr: "@daily",
	})

	bad := 30
	_, err := svc.Update(context.Background(), created.ID, uid, UpdateRequest{TimeoutSecs: &bad})
	if err == nil {
		t.Fatal("expected error for timeout_secs < 60")
	}
}

// ---- Service.Delete ----

func TestService_Delete_RemovesRecord(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	created, _ := svc.Create(context.Background(), uid, CreateRequest{
		Prompt: "p", CronExpr: "@daily",
	})

	if err := svc.Delete(context.Background(), created.ID, uid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := svc.Get(context.Background(), created.ID, uid)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestService_Delete_NotFound(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	if err := svc.Delete(context.Background(), "nonexistent", uid); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---- Service.Toggle ----

func TestService_Toggle_Enable(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	created, _ := svc.Create(context.Background(), uid, CreateRequest{
		Prompt: "p", CronExpr: "@daily",
	})
	// Disable first.
	if err := svc.Toggle(context.Background(), created.ID, uid, false); err != nil {
		t.Fatalf("Toggle(false): %v", err)
	}
	// Re-enable.
	if err := svc.Toggle(context.Background(), created.ID, uid, true); err != nil {
		t.Fatalf("Toggle(true): %v", err)
	}
	got, _ := svc.Get(context.Background(), created.ID, uid)
	if !got.Enabled {
		t.Error("expected Enabled=true after toggle(true)")
	}
}

func TestService_Toggle_Disable(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	svc := newTestService(t, g)

	created, _ := svc.Create(context.Background(), uid, CreateRequest{
		Prompt: "p", CronExpr: "@daily",
	})
	if err := svc.Toggle(context.Background(), created.ID, uid, false); err != nil {
		t.Fatalf("Toggle(false): %v", err)
	}
	got, _ := svc.Get(context.Background(), created.ID, uid)
	if got.Enabled {
		t.Error("expected Enabled=false after toggle(false)")
	}
}

// ---- Service.List ----

func TestService_List_ReturnsUserRecords(t *testing.T) {
	g := newTestDB(t)
	uid := insertUser(t, g, "alice")
	uid2 := insertUser(t, g, "bob")
	svc := newTestService(t, g)

	svc.Create(context.Background(), uid, CreateRequest{Prompt: "a", CronExpr: "@daily"})
	svc.Create(context.Background(), uid, CreateRequest{Prompt: "b", CronExpr: "@daily"})
	svc.Create(context.Background(), uid2, CreateRequest{Prompt: "c", CronExpr: "@daily"})

	recs, err := svc.List(context.Background(), uid)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("expected 2 records for alice, got %d", len(recs))
	}
}
