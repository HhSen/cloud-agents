package conversation

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCreate_StoresConversation(t *testing.T) {
	s := NewStore()
	env := map[string]string{"FOO": "bar"}
	conv := s.Create(env)

	if conv.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if conv.GetState() != StateNew {
		t.Fatalf("expected StateNew, got %v", conv.GetState())
	}
	got := conv.ExtraEnv()
	if got["FOO"] != "bar" {
		t.Fatalf("expected FOO=bar in ExtraEnv, got %v", got)
	}
}

func TestCreate_NilEnv(t *testing.T) {
	s := NewStore()
	conv := s.Create(nil)
	if conv.ExtraEnv() != nil {
		t.Fatalf("expected nil ExtraEnv, got %v", conv.ExtraEnv())
	}
}

func TestGet_Missing(t *testing.T) {
	s := NewStore()
	if s.Get("nonexistent") != nil {
		t.Fatal("expected nil for missing conversation")
	}
}

func TestGet_Found(t *testing.T) {
	s := NewStore()
	conv := s.Create(nil)
	got := s.Get(conv.ID)
	if got != conv {
		t.Fatal("Get did not return the created conversation")
	}
}

func TestDelete_Removes(t *testing.T) {
	s := NewStore()
	conv := s.Create(nil)
	s.Delete(conv.ID)
	if s.Get(conv.ID) != nil {
		t.Fatal("expected nil after Delete")
	}
}

func TestSetRunning(t *testing.T) {
	s := NewStore()
	conv := s.Create(nil)

	headers := map[string]string{"Authorization": "Bearer tok"}
	conv.SetRunning("sandbox-1", "http://proxy/", headers)

	if conv.GetState() != StateRunning {
		t.Fatalf("expected StateRunning, got %v", conv.GetState())
	}
	url, hdrs := conv.GetProxyInfo()
	if url != "http://proxy/" {
		t.Fatalf("unexpected proxy URL: %s", url)
	}
	if hdrs["Authorization"] != "Bearer tok" {
		t.Fatalf("unexpected headers: %v", hdrs)
	}
	if conv.GetSandboxID() != "sandbox-1" {
		t.Fatalf("unexpected sandboxID: %s", conv.GetSandboxID())
	}
}

func TestSetError(t *testing.T) {
	s := NewStore()
	conv := s.Create(nil)
	conv.SetError()
	if conv.GetState() != StateError {
		t.Fatalf("expected StateError, got %v", conv.GetState())
	}
}

func TestEnsureProvisioned_CalledOnce(t *testing.T) {
	s := NewStore()
	conv := s.Create(nil)

	var callCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conv.EnsureProvisioned(func() error {
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

func TestEnsureProvisioned_ErrorPropagated(t *testing.T) {
	s := NewStore()
	conv := s.Create(nil)
	want := errors.New("provision failed")

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			errs[idx] = conv.EnsureProvisioned(func() error { return want })
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, want) {
			t.Fatalf("goroutine %d: expected provision error, got %v", i, err)
		}
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
