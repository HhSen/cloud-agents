package task

import (
	"context"
	"sync"
)

// State tracks sandbox liveness only. The full task state exposed to callers
// is derived by combining State with sessionID presence (see computeStateStr).
//
//	State       sessionID=""   sessionID set
//	StateNew    pending        paused
//	Provision   provisioning   resuming
//	Running     idle           active
type State int

const (
	StateNew          State = iota
	StateProvisioning       // sandbox being created
	StateRunning            // sandbox up, agent ready
	StateError
)

// String returns the base sandbox-lifecycle name. For the full spec state label
// (which also reflects session presence) use Task.Info() or computeStateStr.
func (s State) String() string {
	switch s {
	case StateNew:
		return "pending"
	case StateProvisioning:
		return "provisioning"
	case StateRunning:
		return "idle"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// Task is the stable top-level entity. Its ID is the permanent join key across
// sandboxes and OFS storage. See docs/resource-mapping.md for the full lifecycle.
type Task struct {
	ID       string
	Username string // owner; immutable after construction
	UserID   uint   // owner's DB primary key; immutable after construction

	mu           sync.RWMutex
	state        State  // sandbox liveness (see State constants above)
	sandboxID    string // transient: cleared on sandbox destroy, set on each new provision
	proxyBaseURL string
	proxyHeaders map[string]string
	// sessionID is the Claude Code UUID from the SSE session.init event.
	// Null until the first user message; never cleared once set (invariant 4 in resource-mapping.md).
	sessionID string
	title     string            // set by the sandbox once the task has a name
	extraEnv  map[string]string // per-request env vars merged into sandbox at provision time
	gitURL    string            // optional repo to clone at provision time; immutable after creation
	errorMsg  string            // last error message; set when state transitions to StateError

	scheduleID string // empty for manually-created tasks; immutable after creation

	// provisionMu serialises provisioning and reset. Lock order: provisionMu → mu.
	// Not used when ops != nil (Redis lock replaces it).
	provisionMu sync.Mutex
	provisioned bool

	// ops is set by RedisRepository so mutation methods persist to Redis.
	// nil means all state is managed in-process by the fields above.
	ops taskOps
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
	if t.ops != nil {
		t.ops.persistRunning(sandboxID, proxyBaseURL, proxyHeaders)
	}
}

func (t *Task) SetError(msg string) {
	t.mu.Lock()
	t.state = StateError
	t.errorMsg = msg
	t.mu.Unlock()
	if t.ops != nil {
		t.ops.persistError(msg)
	}
}

func (t *Task) GetGitURL() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.gitURL
}

func (t *Task) GetErrorMsg() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.errorMsg
}

func (t *Task) SetProvisioning() {
	t.mu.Lock()
	t.state = StateProvisioning
	t.mu.Unlock()
	if t.ops != nil {
		t.ops.persistProvisioning()
	}
}

func (t *Task) GetProxyInfo() (baseURL string, headers map[string]string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.proxyBaseURL, t.proxyHeaders
}

func (t *Task) GetSessionID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sessionID
}

// SetSessionID records the Claude Code session UUID on the task. A non-empty id is written
// only once: if sessionID is already set, the call is a no-op. This enforces the invariant
// that session_id is never cleared or replaced once established.
func (t *Task) SetSessionID(id string) {
	if t.ops != nil {
		// Lua HSETNX enforces write-once atomically across instances.
		if t.ops.persistSessionID(id) {
			t.mu.Lock()
			t.sessionID = id
			t.mu.Unlock()
		}
		return
	}
	t.mu.Lock()
	if t.sessionID == "" {
		t.sessionID = id
	}
	t.mu.Unlock()
}

func (t *Task) GetSandboxID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sandboxID
}

func (t *Task) GetTitle() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.title
}

func (t *Task) SetTitle(title string) {
	t.mu.Lock()
	t.title = title
	t.mu.Unlock()
	if t.ops != nil {
		t.ops.persistTitle(title)
	}
}

func (t *Task) ExtraEnv() map[string]string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.extraEnv
}

// EnsureProvisioned calls fn if not yet provisioned. Concurrent callers block until fn returns.
// Unlike sync.Once, a failed fn leaves provisioned=false so the next caller retries.
func (t *Task) EnsureProvisioned(fn func() error) error {
	if t.ops != nil {
		return t.ops.ensureProvisioned(fn)
	}
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
	if t.ops != nil {
		t.ops.resetForReprovisioning()
		t.mu.Lock()
		t.state = StateNew
		t.sandboxID = ""
		t.proxyBaseURL = ""
		t.proxyHeaders = nil
		t.mu.Unlock()
		t.provisioned = false
		return
	}
	t.provisionMu.Lock()
	defer t.provisionMu.Unlock()
	t.resetLocked()
}

// ResetIfExpired atomically checks sandbox liveness and clears state if the sandbox
// is no longer Running. isAlive is called while provisionMu is held, preventing a
// concurrent re-provision from being stomped by a racing expiry reset. Returns the
// error from isAlive, if any; on error the state is NOT reset.
func (t *Task) ResetIfExpired(isAlive func(sandboxID string) (bool, error)) error {
	if t.ops != nil {
		wasReset, err := t.ops.resetIfExpired(isAlive)
		if wasReset {
			t.mu.Lock()
			t.state = StateNew
			t.sandboxID = ""
			t.proxyBaseURL = ""
			t.proxyHeaders = nil
			t.mu.Unlock()
			t.provisioned = false
		}
		return err
	}
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
// sessionID is intentionally not cleared — per the resource-mapping spec it is
// never unset once written, enabling OFS history reads without an active sandbox.
func (t *Task) resetLocked() {
	t.provisioned = false
	t.mu.Lock()
	t.state = StateNew
	t.sandboxID = ""
	t.proxyBaseURL = ""
	t.proxyHeaders = nil
	t.mu.Unlock()
}

// StateString returns the spec state label for a given state + session presence.
// This is the package-level counterpart of computeStateStr for use without a Task.
func StateString(s State, hasSession bool) string {
	switch s {
	case StateNew:
		if hasSession {
			return "paused"
		}
		return "pending"
	case StateProvisioning:
		if hasSession {
			return "resuming"
		}
		return "provisioning"
	case StateRunning:
		if hasSession {
			return "active"
		}
		return "idle"
	case StateError:
		return "error"
	default:
		return "unknown"
	}
}

// computeStateStr returns the task state label from the spec state table.
// Must be called with t.mu held for reading.
func (t *Task) computeStateStr() string {
	return StateString(t.state, t.sessionID != "")
}

func (t *Task) Info() (id, sandboxID, sessionID, stateStr string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ID, t.sandboxID, t.sessionID, t.computeStateStr()
}

type Store struct {
	mu    sync.RWMutex
	tasks map[string]*Task
}

func NewStore() *Store {
	return &Store{tasks: make(map[string]*Task)}
}

func (s *Store) Create(username string, extraEnv map[string]string, gitURL string) *Task {
	t := &Task{
		ID:       newTaskID(),
		Username: username,
		state:    StateNew,
		extraEnv: extraEnv,
		gitURL:   gitURL,
	}
	s.mu.Lock()
	s.tasks[t.ID] = t
	s.mu.Unlock()
	return t
}

func (s *Store) CreateWithSchedule(username string, extraEnv map[string]string, gitURL, scheduleID string) *Task {
	t := s.Create(username, extraEnv, gitURL)
	t.scheduleID = scheduleID
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

// MemoryRepository wraps Store and implements Repository for use in local dev
// and unit tests. Falls back gracefully without any external dependencies.
type MemoryRepository struct {
	store *Store
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{store: NewStore()}
}

func (r *MemoryRepository) Create(_ context.Context, username string, extraEnv map[string]string, gitURL string, scheduleID string) (*Task, error) {
	if scheduleID != "" {
		return r.store.CreateWithSchedule(username, extraEnv, gitURL, scheduleID), nil
	}
	return r.store.Create(username, extraEnv, gitURL), nil
}

func (r *MemoryRepository) Get(_ context.Context, id string) (*Task, error) {
	return r.store.Get(id), nil
}

func (r *MemoryRepository) Delete(_ context.Context, id string) error {
	r.store.Delete(id)
	return nil
}

func (r *MemoryRepository) List(_ context.Context, username string) ([]TaskSummary, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	var result []TaskSummary
	for _, t := range r.store.tasks {
		if t.Username != username {
			continue
		}
		t.mu.RLock()
		result = append(result, TaskSummary{
			ID:         t.ID,
			Title:      t.title,
			State:      t.computeStateStr(),
			ScheduleID: t.scheduleID,
		})
		t.mu.RUnlock()
	}
	return result, nil
}

func (r *MemoryRepository) ListBySchedule(_ context.Context, scheduleID string) ([]TaskSummary, error) {
	r.store.mu.RLock()
	defer r.store.mu.RUnlock()
	var result []TaskSummary
	for _, t := range r.store.tasks {
		if t.scheduleID != scheduleID {
			continue
		}
		t.mu.RLock()
		result = append(result, TaskSummary{
			ID:         t.ID,
			Title:      t.title,
			State:      t.computeStateStr(),
			ScheduleID: t.scheduleID,
		})
		t.mu.RUnlock()
	}
	return result, nil
}
