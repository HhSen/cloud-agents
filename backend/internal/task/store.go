package task

import (
	"sync"

	"github.com/google/uuid"
)

type State int

const (
	StateNew          State = iota
	StateProvisioning       // sandbox being created
	StateRunning            // sandbox up, agent ready
	StateError
)

func (s State) String() string {
	switch s {
	case StateNew, StateProvisioning:
		return "provisioning"
	case StateRunning:
		return "running"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

type Task struct {
	ID       string
	Username string // owner; immutable after construction

	mu             sync.RWMutex
	state          State
	sandboxID      string
	proxyBaseURL   string
	proxyHeaders   map[string]string
	agentSessionID string
	extraEnv       map[string]string // per-request env vars merged into sandbox at provision time

	// provisionMu serialises provisioning and reset. Lock order: provisionMu → mu.
	provisionMu sync.Mutex
	provisioned bool
}

func (t *Task) GetState() State {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state
}

func (t *Task) SetRunning(sandboxID, proxyBaseURL string, proxyHeaders map[string]string) {
	t.mu.Lock()
	t.state = StateRunning
	t.sandboxID = sandboxID
	t.proxyBaseURL = proxyBaseURL
	t.proxyHeaders = proxyHeaders
	t.mu.Unlock()
}

func (t *Task) SetError() {
	t.mu.Lock()
	t.state = StateError
	t.mu.Unlock()
}

func (t *Task) SetProvisioning() {
	t.mu.Lock()
	t.state = StateProvisioning
	t.mu.Unlock()
}

func (t *Task) GetProxyInfo() (baseURL string, headers map[string]string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.proxyBaseURL, t.proxyHeaders
}

func (t *Task) GetAgentSessionID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.agentSessionID
}

func (t *Task) SetAgentSessionID(id string) {
	t.mu.Lock()
	t.agentSessionID = id
	t.mu.Unlock()
}

func (t *Task) GetSandboxID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sandboxID
}

func (t *Task) ExtraEnv() map[string]string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.extraEnv
}

// EnsureProvisioned calls fn if not yet provisioned. Concurrent callers block until fn returns.
// Unlike sync.Once, a failed fn leaves provisioned=false so the next caller retries.
func (t *Task) EnsureProvisioned(fn func() error) error {
	t.provisionMu.Lock()
	defer t.provisionMu.Unlock()
	if t.provisioned {
		return nil
	}
	if err := fn(); err != nil {
		return err
	}
	t.provisioned = true
	return nil
}

// ResetForReprovisioning clears all sandbox state so a new sandbox can be allocated.
func (t *Task) ResetForReprovisioning() {
	t.provisionMu.Lock()
	defer t.provisionMu.Unlock()
	t.resetLocked()
}

// ResetIfExpired atomically checks sandbox liveness and clears state if the sandbox
// is no longer Running. isAlive is called while provisionMu is held, preventing a
// concurrent re-provision from being stomped by a racing expiry reset. Returns the
// error from isAlive, if any; on error the state is NOT reset.
func (t *Task) ResetIfExpired(isAlive func(sandboxID string) (bool, error)) error {
	t.provisionMu.Lock()
	defer t.provisionMu.Unlock()
	if !t.provisioned {
		return nil
	}
	t.mu.RLock()
	id := t.sandboxID
	t.mu.RUnlock()
	if id == "" {
		return nil
	}
	alive, err := isAlive(id)
	if err != nil {
		return err
	}
	if !alive {
		t.resetLocked()
	}
	return nil
}

// resetLocked clears all sandbox state. Caller must hold provisionMu.
func (t *Task) resetLocked() {
	t.provisioned = false
	t.mu.Lock()
	t.state = StateNew
	t.sandboxID = ""
	t.proxyBaseURL = ""
	t.proxyHeaders = nil
	t.agentSessionID = ""
	t.mu.Unlock()
}

func (t *Task) Info() (id, sandboxID, agentSessionID string, state State) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ID, t.sandboxID, t.agentSessionID, t.state
}

type Store struct {
	mu    sync.RWMutex
	tasks map[string]*Task
}

func NewStore() *Store {
	return &Store{tasks: make(map[string]*Task)}
}

func (s *Store) Create(username string, extraEnv map[string]string) *Task {
	t := &Task{
		ID:       uuid.New().String(),
		Username: username,
		state:    StateNew,
		extraEnv: extraEnv,
	}
	s.mu.Lock()
	s.tasks[t.ID] = t
	s.mu.Unlock()
	return t
}

func (s *Store) Get(id string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tasks[id]
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.tasks, id)
	s.mu.Unlock()
}
