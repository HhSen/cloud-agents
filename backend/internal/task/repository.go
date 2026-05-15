package task

import (
	"context"
	"time"
)

// Repository is the storage interface for Tasks. Implementations:
//   - MemoryRepository — local dev / unit tests (no external deps)
//   - RedisRepository  — legacy all-Redis storage (tasks + sandbox in Redis)
//   - MySQLRepository  — production: durable fields in MySQL, sandbox mapping in Redis
type Repository interface {
	// Create creates a new task. scheduleID is empty for manually-created tasks.
	Create(ctx context.Context, username string, extraEnv map[string]string, gitURL string, scheduleID string) (*Task, error)
	// Get returns nil, nil when the task does not exist.
	Get(ctx context.Context, id string) (*Task, error)
	Delete(ctx context.Context, id string) error
	// List returns summaries for all tasks owned by username, newest first.
	List(ctx context.Context, username string) ([]TaskSummary, error)
	// ListBySchedule returns summaries for all tasks spawned by a given schedule.
	ListBySchedule(ctx context.Context, scheduleID string) ([]TaskSummary, error)
}

// TaskSummary is a lightweight projection of Task used for listing.
type TaskSummary struct {
	ID         string
	Title      string
	State      string
	GitURL     string
	ErrorMsg   string
	ScheduleID string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// taskOps is the optional persistence hook for a Task.
// nil means all state lives in the Task struct's own fields and mutexes.
// RedisRepository wires redisTaskOps; MySQLRepository wires mysqlTaskOps.
type taskOps interface {
	persistRunning(sandboxID, proxyBaseURL string, proxyHeaders map[string]string)
	persistProvisioning()
	persistError(msg string)
	// persistSessionID writes sessionID only if the field is currently empty.
	// Returns true if the value was actually written (was empty before).
	persistSessionID(sessionID string) bool
	ensureProvisioned(fn func() error) error
	// resetIfExpired returns (wasReset, err). wasReset=true means the task
	// fields were cleared in Redis and the caller should update its local snapshot.
	resetIfExpired(isAlive func(string) (bool, error)) (bool, error)
	resetForReprovisioning()
	persistTitle(title string)
}
