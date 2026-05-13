package task

import (
	"context"
	"fmt"
	"os"

	"github.com/your-org/platform-backend/pkg/logger"
	"time"

	"github.com/go-redis/redis/v8"
)

func lockKey(id string) string { return "task-lock:" + id }

// releaseLockScript releases the lock only if the current holder matches.
var releaseLockScript = redis.NewScript(`
	if redis.call("GET", KEYS[1]) == ARGV[1] then
		redis.call("DEL", KEYS[1])
		return 1
	end
	return 0
`)

// redisLock implements distributed per-task locking via Redis SetNX/Lua.
type redisLock struct {
	rdb    *redis.Client
	taskID string
}

func (l *redisLock) holder() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

func (l *redisLock) release(h string) {
	ctx := context.Background()
	if err := releaseLockScript.Run(ctx, l.rdb, []string{lockKey(l.taskID)}, h).Err(); err != nil {
		logger.Default().Error("redis: release lock", "task_id", l.taskID, "err", err)
	}
}

// withLock acquires the per-task Redis lock and calls fn while holding it.
// Retries with exponential backoff (50 ms → 100 ms → … capped at 5 s) up to deadline.
func (l *redisLock) withLock(ctx context.Context, deadline time.Duration, fn func() error) error {
	const lockTTL = 30 * time.Second
	h := l.holder()
	delay := 50 * time.Millisecond
	start := time.Now()

	for {
		acquired, err := l.rdb.SetNX(ctx, lockKey(l.taskID), h, lockTTL).Result()
		if err != nil {
			return fmt.Errorf("acquire lock for task %s: %w", l.taskID, err)
		}
		if acquired {
			break
		}

		if time.Since(start) >= deadline {
			return fmt.Errorf("failed to acquire lock for task %s within %s", l.taskID, deadline)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		delay *= 2
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
	}

	defer l.release(h)
	return fn()
}
