package task

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/l-lab/cloud-agents/internal/db"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newMySQLTestRepo(t *testing.T) (*MySQLRepository, *miniredis.Miniredis) {
	t.Helper()

	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gormDB.AutoMigrate(&db.User{}, &db.Task{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(s.Close)

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { rdb.Close() })

	return NewMySQLRepository(gormDB, rdb), s
}

// mustInsertUser creates a user row in the test DB and returns its ID.
func mustInsertUser(t *testing.T, repo *MySQLRepository, username string) uint {
	t.Helper()
	u := db.User{UserName: username, Email: username + "@test.local", PasswordHash: "x", IsActive: true}
	if err := repo.db.Create(&u).Error; err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	return u.ID
}

func TestMySQLCreate_StoresTask(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "alice")

	tsk, err := repo.Create(ctx, "alice", map[string]string{"FOO": "bar"}, "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tsk.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if tsk.Username != "alice" {
		t.Fatalf("expected Username=alice, got %q", tsk.Username)
	}
	if tsk.GetState() != StateNew {
		t.Fatalf("expected StateNew, got %v", tsk.GetState())
	}
	if tsk.ExtraEnv()["FOO"] != "bar" {
		t.Fatalf("expected FOO=bar in ExtraEnv, got %v", tsk.ExtraEnv())
	}
}

func TestMySQLGet_Found(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "bob")

	created, _ := repo.Create(ctx, "bob", nil, "", "")
	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected task, got nil")
	}
	if got.ID != created.ID {
		t.Fatalf("ID mismatch: got %q, want %q", got.ID, created.ID)
	}
	if got.Username != "bob" {
		t.Fatalf("Username mismatch: got %q, want bob", got.Username)
	}
}

func TestMySQLGet_Missing(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	got, err := repo.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for missing task")
	}
}

func TestMySQLDelete_Removes(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	if err := repo.Delete(ctx, tsk.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := repo.Get(ctx, tsk.ID)
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after Delete")
	}
}

func TestMySQLSetRunning_Persists(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	headers := map[string]string{"Authorization": "Bearer tok"}
	tsk.SetRunning("sb-1", "http://proxy/", headers)

	got, err := repo.Get(ctx, tsk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GetState() != StateRunning {
		t.Fatalf("expected StateRunning, got %v", got.GetState())
	}
	if got.GetSandboxID() != "sb-1" {
		t.Fatalf("expected sandboxID=sb-1, got %q", got.GetSandboxID())
	}
	url, hdrs := got.GetProxyInfo()
	if url != "http://proxy/" {
		t.Fatalf("unexpected proxy URL: %s", url)
	}
	if hdrs["Authorization"] != "Bearer tok" {
		t.Fatalf("unexpected headers: %v", hdrs)
	}
}

func TestMySQLSetSessionID_WriteOnce(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	tsk.SetSessionID("first")
	tsk.SetSessionID("second") // should be a no-op

	got, _ := repo.Get(ctx, tsk.ID)
	if got.GetSessionID() != "first" {
		t.Errorf("expected session_id=first (write-once), got %q", got.GetSessionID())
	}
}

func TestMySQLEnsureProvisioned_CalledOnce(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")

	var callCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tsk.EnsureProvisioned(func() error {
				callCount.Add(1)
				tsk.SetRunning("sb-test", "http://proxy/", nil)
				return nil
			})
		}()
	}
	wg.Wait()

	if callCount.Load() != 1 {
		t.Fatalf("expected fn called once, called %d times", callCount.Load())
	}
	got, _ := repo.Get(ctx, tsk.ID)
	if !got.provisioned {
		t.Error("expected provisioned=true after EnsureProvisioned")
	}
}

func TestMySQLEnsureProvisioned_StateNotPersistedFails(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	err := tsk.EnsureProvisioned(func() error {
		// fn succeeds but never calls SetRunning — state stays StateNew.
		return nil
	})
	if err == nil {
		t.Fatal("expected error when state not persisted, got nil")
	}

	got, _ := repo.Get(ctx, tsk.ID)
	if got.provisioned {
		t.Error("expected provisioned=false after state-verification failure")
	}
}

func TestMySQLEnsureProvisioned_FailedFnRetried(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	want := errors.New("provision failed")

	err := tsk.EnsureProvisioned(func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("expected provision error, got %v", err)
	}

	got, _ := repo.Get(ctx, tsk.ID)
	if got.provisioned {
		t.Error("expected provisioned=false after fn failure")
	}
}

func TestMySQLResetIfExpired_ResetsWhenNotAlive(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	tsk.EnsureProvisioned(func() error {
		tsk.SetRunning("sb-expired", "http://proxy/", map[string]string{})
		return nil
	})

	err := tsk.ResetIfExpired(func(_ string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatalf("ResetIfExpired: %v", err)
	}

	got, _ := repo.Get(ctx, tsk.ID)
	if got.GetState() != StateNew {
		t.Errorf("expected StateNew after expiry reset, got %v", got.GetState())
	}
	if got.GetSandboxID() != "" {
		t.Errorf("expected empty sandboxID after expiry reset, got %q", got.GetSandboxID())
	}
	if got.provisioned {
		t.Error("expected provisioned=false after expiry reset")
	}
}

func TestMySQLResetIfExpired_NoResetWhenAlive(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	tsk.EnsureProvisioned(func() error {
		tsk.SetRunning("sb-alive", "http://proxy/", map[string]string{})
		return nil
	})

	err := tsk.ResetIfExpired(func(_ string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("ResetIfExpired: %v", err)
	}

	got, _ := repo.Get(ctx, tsk.ID)
	if got.GetState() != StateRunning {
		t.Errorf("expected StateRunning when sandbox is alive, got %v", got.GetState())
	}
}

func TestMySQLResetForReprovisioning_ClearsState(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	tsk.EnsureProvisioned(func() error {
		tsk.SetRunning("sb-1", "http://proxy/", map[string]string{})
		return nil
	})
	tsk.SetSessionID("sess-1")

	tsk.ResetForReprovisioning()

	got, _ := repo.Get(ctx, tsk.ID)
	if got.GetState() != StateNew {
		t.Errorf("expected StateNew after reset, got %v", got.GetState())
	}
	if got.GetSandboxID() != "" {
		t.Errorf("expected empty sandboxID after reset, got %q", got.GetSandboxID())
	}
	// sessionID must be retained — never cleared on reset.
	if got.GetSessionID() != "sess-1" {
		t.Errorf("expected sessionID retained after reset, got %q", got.GetSessionID())
	}
}

func TestMySQLPersistTitle(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	tsk.SetTitle("My project")

	got, _ := repo.Get(ctx, tsk.ID)
	if got.GetTitle() != "My project" {
		t.Errorf("expected title=My project, got %q", got.GetTitle())
	}
}

func TestMySQLDelete_CleansRedis(t *testing.T) {
	repo, _ := newMySQLTestRepo(t)
	ctx := context.Background()
	mustInsertUser(t, repo, "testuser")

	tsk, _ := repo.Create(ctx, "testuser", nil, "", "")
	tsk.SetRunning("sb-del", "http://proxy/", nil)

	// Confirm sandbox hash exists in Redis.
	fields, err := repo.rdb.HGetAll(ctx, sandboxKey(tsk.ID)).Result()
	if err != nil || len(fields) == 0 {
		t.Fatalf("expected sandbox hash to exist before delete, err=%v fields=%v", err, fields)
	}

	if err := repo.Delete(ctx, tsk.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Redis sandbox hash should be gone.
	fields, _ = repo.rdb.HGetAll(ctx, sandboxKey(tsk.ID)).Result()
	if len(fields) != 0 {
		t.Errorf("expected sandbox hash deleted, still has fields: %v", fields)
	}
}
