package conversation

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

type Conversation struct {
	ID string

	mu             sync.RWMutex
	state          State
	sandboxID      string
	proxyBaseURL   string
	proxyHeaders   map[string]string
	agentSessionID string
	extraEnv       map[string]string // per-request env vars merged into sandbox at provision time

	// once ensures exactly one goroutine runs provisioning; others block until done.
	once         sync.Once
	provisionErr error
}

func (c *Conversation) GetState() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Conversation) SetRunning(sandboxID, proxyBaseURL string, proxyHeaders map[string]string) {
	c.mu.Lock()
	c.state = StateRunning
	c.sandboxID = sandboxID
	c.proxyBaseURL = proxyBaseURL
	c.proxyHeaders = proxyHeaders
	c.mu.Unlock()
}

func (c *Conversation) SetError() {
	c.mu.Lock()
	c.state = StateError
	c.mu.Unlock()
}

func (c *Conversation) SetProvisioning() {
	c.mu.Lock()
	c.state = StateProvisioning
	c.mu.Unlock()
}

func (c *Conversation) GetProxyInfo() (baseURL string, headers map[string]string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.proxyBaseURL, c.proxyHeaders
}

func (c *Conversation) GetAgentSessionID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.agentSessionID
}

func (c *Conversation) SetAgentSessionID(id string) {
	c.mu.Lock()
	c.agentSessionID = id
	c.mu.Unlock()
}

func (c *Conversation) GetSandboxID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sandboxID
}

func (c *Conversation) ExtraEnv() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.extraEnv
}

// EnsureProvisioned calls fn exactly once. Concurrent callers block until fn returns.
func (c *Conversation) EnsureProvisioned(fn func() error) error {
	c.once.Do(func() {
		c.provisionErr = fn()
	})
	return c.provisionErr
}

func (c *Conversation) Info() (id, sandboxID, agentSessionID string, state State) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ID, c.sandboxID, c.agentSessionID, c.state
}

type Store struct {
	mu    sync.RWMutex
	convs map[string]*Conversation
}

func NewStore() *Store {
	return &Store{convs: make(map[string]*Conversation)}
}

func (s *Store) Create(extraEnv map[string]string) *Conversation {
	conv := &Conversation{
		ID:       uuid.New().String(),
		state:    StateNew,
		extraEnv: extraEnv,
	}
	s.mu.Lock()
	s.convs[conv.ID] = conv
	s.mu.Unlock()
	return conv
}

func (s *Store) Get(id string) *Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.convs[id]
}

func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.convs, id)
	s.mu.Unlock()
}
