package task

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
)

func newTestRepo(t *testing.T) (*RedisRepository, *miniredis.Miniredis) {
	t.Helper()
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(s.Close)

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { rdb.Close() })

	return NewRedisRepository(rdb), s
}

func TestRedisCreate_StoresTask(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, err := repo.Create(ctx, "alice", map[string]string{"FOO": "bar"}, "")
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

func TestRedisGet_Found(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	created, _ := repo.Create(ctx, "bob", nil, "")
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

func TestRedisGet_Missing(t *testing.T) {
	repo, _ := newTestRepo(t)
	got, err := repo.Get(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for missing task")
	}
}

func TestRedisDelete_Removes(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "")
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

func TestRedisSetRunning_Persists(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "")
	headers := map[string]string{"Authorization": "Bearer tok"}
	tsk.SetRunning("sb-1", "http://proxy/", headers)

	// Fetch fresh from Redis and verify fields were persisted.
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

func TestRedisSetSessionID_WriteOnce(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "")
	tsk.SetSessionID("first")
	tsk.SetSessionID("second") // should be a no-op

	got, _ := repo.Get(ctx, tsk.ID)
	if got.GetSessionID() != "first" {
		t.Errorf("expected session_id=first (write-once), got %q", got.GetSessionID())
	}
}

func TestRedisEnsureProvisioned_CalledOnce(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "")

	var callCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tsk.EnsureProvisioned(func() error {
				callCount.Add(1)
				// Mirror real ProvisionForTask: SetRunning must be called so the
				// state-verification guard in ensureProvisioned is satisfied.
				tsk.SetRunning("sb-test", "http://proxy/", nil)
				return nil
			})
		}()
	}
	wg.Wait()

	if callCount.Load() != 1 {
		t.Fatalf("expected fn called once, called %d times", callCount.Load())
	}
	// Verify provisioned=1 is stored in Redis.
	got, _ := repo.Get(ctx, tsk.ID)
	if !got.provisioned {
		t.Error("expected provisioned=true in Redis after EnsureProvisioned")
	}
}

func TestRedisEnsureProvisioned_PersistRunningFailurePreventsProvisionedFlag(t *testing.T) {
	// If fn() returns nil but SetRunning was never called (simulating a silent
	// persistRunning failure), ensureProvisioned must NOT set provisioned=1.
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "")
	err := tsk.EnsureProvisioned(func() error {
		// fn succeeds but never calls SetRunning — state stays StateNew in Redis.
		return nil
	})
	if err == nil {
		t.Fatal("expected error when state not persisted, got nil")
	}

	// provisioned must remain 0 so the next call retries.
	got, _ := repo.Get(ctx, tsk.ID)
	if got.provisioned {
		t.Error("expected provisioned=false after state-verification failure")
	}
}

func TestRedisEnsureProvisioned_FailedFnRetried(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "")
	want := errors.New("provision failed")

	err := tsk.EnsureProvisioned(func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("expected provision error, got %v", err)
	}

	// After failure, provisioned should still be false so next call retries.
	got, _ := repo.Get(ctx, tsk.ID)
	if got.provisioned {
		t.Error("expected provisioned=false after fn failure")
	}
}

func TestRedisResetIfExpired_NotProvisionedSkipsCheck(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "") // provisioned=0, no sandbox

	called := false
	err := tsk.ResetIfExpired(func(_ string) (bool, error) {
		called = true
		return false, nil
	})
	if err != nil {
		t.Fatalf("ResetIfExpired: %v", err)
	}
	if called {
		t.Error("expected isAlive not called when task is unprovisioned")
	}
}

func TestRedisResetIfExpired_ResetsWhenNotAlive(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "")
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

func TestRedisResetIfExpired_NoResetWhenAlive(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "")
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

func TestRedisResetForReprovisioning_ClearsState(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	tsk, _ := repo.Create(ctx, "", nil, "")
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
	// sessionID must be retained — it is never cleared on reset.
	if got.GetSessionID() != "sess-1" {
		t.Errorf("expected sessionID retained after reset, got %q", got.GetSessionID())
	}
}

func TestRedisStateString_AllCombinations(t *testing.T) {
	cases := []struct {
		setupFn   func(*Task)
		sessionID string
		want      string
	}{
		{func(t *Task) {}, "", "pending"},
		{func(t *Task) {}, "s1", "paused"},
		{func(t *Task) { t.SetProvisioning() }, "", "provisioning"},
		{func(t *Task) { t.SetProvisioning() }, "s1", "resuming"},
		{func(t *Task) { t.SetRunning("sb", "url", nil) }, "", "idle"},
		{func(t *Task) { t.SetRunning("sb", "url", nil) }, "s1", "active"},
		{func(t *Task) { t.SetError("") }, "", "error"},
		{func(t *Task) { t.SetError("") }, "s1", "error"},
	}

	repo, _ := newTestRepo(t)
	ctx := context.Background()

	for _, tc := range cases {
		tsk, _ := repo.Create(ctx, "", nil, "")
		tc.setupFn(tsk)
		if tc.sessionID != "" {
			tsk.SetSessionID(tc.sessionID)
		}
		// Reload from Redis to verify persisted state.
		got, _ := repo.Get(ctx, tsk.ID)
		_, _, _, stateStr := got.Info()
		if stateStr != tc.want {
			t.Errorf("sessionID=%q: Info() state=%q, want %q", tc.sessionID, stateStr, tc.want)
		}
	}
}
