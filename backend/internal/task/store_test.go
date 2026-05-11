package task

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCreate_StoresTask(t *testing.T) {
	s := NewStore()
	env := map[string]string{"FOO": "bar"}
	task := s.Create("alice", env)

	if task.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if task.Username != "alice" {
		t.Fatalf("expected Username=alice, got %q", task.Username)
	}
	if task.GetState() != StateNew {
		t.Fatalf("expected StateNew, got %v", task.GetState())
	}
	got := task.ExtraEnv()
	if got["FOO"] != "bar" {
		t.Fatalf("expected FOO=bar in ExtraEnv, got %v", got)
	}
}

func TestCreate_NilEnv(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	if task.ExtraEnv() != nil {
		t.Fatalf("expected nil ExtraEnv, got %v", task.ExtraEnv())
	}
}

func TestGet_Missing(t *testing.T) {
	s := NewStore()
	if s.Get("nonexistent") != nil {
		t.Fatal("expected nil for missing task")
	}
}

func TestGet_Found(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	got := s.Get(task.ID)
	if got != task {
		t.Fatal("Get did not return the created task")
	}
}

func TestDelete_Removes(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	s.Delete(task.ID)
	if s.Get(task.ID) != nil {
		t.Fatal("expected nil after Delete")
	}
}

func TestSetRunning(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)

	headers := map[string]string{"Authorization": "Bearer tok"}
	task.SetRunning("sandbox-1", "http://proxy/", headers)

	if task.GetState() != StateRunning {
		t.Fatalf("expected StateRunning, got %v", task.GetState())
	}
	url, hdrs := task.GetProxyInfo()
	if url != "http://proxy/" {
		t.Fatalf("unexpected proxy URL: %s", url)
	}
	if hdrs["Authorization"] != "Bearer tok" {
		t.Fatalf("unexpected headers: %v", hdrs)
	}
	if task.GetSandboxID() != "sandbox-1" {
		t.Fatalf("unexpected sandboxID: %s", task.GetSandboxID())
	}
}

func TestSetError(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	task.SetError()
	if task.GetState() != StateError {
		t.Fatalf("expected StateError, got %v", task.GetState())
	}
}

func TestEnsureProvisioned_CalledOnce(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)

	var callCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task.EnsureProvisioned(func() error {
				callCount.Add(1)
				return nil
			})
		}()
	}
	wg.Wait()

	if callCount.Load() != 1 {
		t.Fatalf("expected fn called once, called %d times", callCount.Load())
	}
}

// TestEnsureProvisioned_FailedFnRetried verifies that when fn returns an error,
// provisioned stays false and each subsequent caller retries fn independently.
func TestEnsureProvisioned_FailedFnRetried(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	want := errors.New("provision failed")

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			errs[idx] = task.EnsureProvisioned(func() error { return want })
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, want) {
			t.Fatalf("goroutine %d: expected provision error, got %v", i, err)
		}
	}
}

func TestResetForReprovisioning_ClearsState(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)

	if err := task.EnsureProvisioned(func() error {
		task.SetRunning("sb-1", "http://proxy/", map[string]string{"k": "v"})
		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	task.SetAgentSessionID("sess-1")

	if task.GetState() != StateRunning {
		t.Fatalf("expected StateRunning before reset, got %v", task.GetState())
	}

	task.ResetForReprovisioning()

	if task.GetState() != StateNew {
		t.Errorf("expected StateNew after reset, got %v", task.GetState())
	}
	if task.GetSandboxID() != "" {
		t.Errorf("expected empty sandboxID after reset, got %q", task.GetSandboxID())
	}
	url, _ := task.GetProxyInfo()
	if url != "" {
		t.Errorf("expected empty proxyBaseURL after reset, got %q", url)
	}
	if task.GetAgentSessionID() != "" {
		t.Errorf("expected empty agentSessionID after reset, got %q", task.GetAgentSessionID())
	}
}

func TestResetIfExpired_ResetsWhenNotAlive(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	task.EnsureProvisioned(func() error {
		task.SetRunning("sb-1", "http://proxy/", map[string]string{})
		return nil
	})

	err := task.ResetIfExpired(func(_ string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.GetState() != StateNew {
		t.Errorf("expected StateNew after expiry reset, got %v", task.GetState())
	}
	if task.GetSandboxID() != "" {
		t.Errorf("expected empty sandboxID after expiry reset, got %q", task.GetSandboxID())
	}
}

func TestResetIfExpired_NoResetWhenAlive(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	task.EnsureProvisioned(func() error {
		task.SetRunning("sb-1", "http://proxy/", map[string]string{})
		return nil
	})

	err := task.ResetIfExpired(func(_ string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.GetState() != StateRunning {
		t.Errorf("expected StateRunning when sandbox is alive, got %v", task.GetState())
	}
}

func TestResetIfExpired_NoResetWhenNotProvisioned(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	called := false

	err := task.ResetIfExpired(func(_ string) (bool, error) {
		called = true
		return false, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("expected isAlive not called when task is unprovisioned")
	}
}

func TestResetIfExpired_ErrorPreservesState(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	task.EnsureProvisioned(func() error {
		task.SetRunning("sb-1", "http://proxy/", map[string]string{})
		return nil
	})
	want := errors.New("network timeout")

	err := task.ResetIfExpired(func(_ string) (bool, error) { return false, want })
	if !errors.Is(err, want) {
		t.Fatalf("expected error %v, got %v", want, err)
	}
	if task.GetState() != StateRunning {
		t.Errorf("expected StateRunning preserved on check error, got %v", task.GetState())
	}
}

func TestResetIfExpired_ConcurrentSafeOnlyResetsOnce(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)
	task.EnsureProvisioned(func() error {
		task.SetRunning("sb-expired", "http://proxy/", map[string]string{})
		return nil
	})

	var wg sync.WaitGroup
	var resetCount atomic.Int32
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task.ResetIfExpired(func(_ string) (bool, error) {
				resetCount.Add(1)
				return false, nil
			})
		}()
	}
	wg.Wait()

	if resetCount.Load() != 1 {
		t.Errorf("expected isAlive called exactly once, called %d times", resetCount.Load())
	}
}

func TestResetForReprovisioning_AllowsReprovision(t *testing.T) {
	s := NewStore()
	task := s.Create("", nil)

	var callCount atomic.Int32

	task.EnsureProvisioned(func() error {
		callCount.Add(1)
		task.SetRunning("sb-1", "http://proxy/", map[string]string{})
		return nil
	})
	if callCount.Load() != 1 {
		t.Fatalf("expected fn called once, got %d", callCount.Load())
	}

	task.ResetForReprovisioning()
	task.EnsureProvisioned(func() error {
		callCount.Add(1)
		task.SetRunning("sb-2", "http://proxy2/", map[string]string{})
		return nil
	})
	if callCount.Load() != 2 {
		t.Fatalf("expected fn called twice after reset, got %d", callCount.Load())
	}
	if task.GetSandboxID() != "sb-2" {
		t.Errorf("expected sandboxID=sb-2 after re-provision, got %q", task.GetSandboxID())
	}
}

func TestStateString(t *testing.T) {
	cases := []struct {
		state State
		want  string
	}{
		{StateNew, "provisioning"},
		{StateProvisioning, "provisioning"},
		{StateRunning, "running"},
		{StateError, "error"},
		{State(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.state.String(); got != tc.want {
			t.Errorf("State(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}
