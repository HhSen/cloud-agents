package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/your-org/platform-backend/pkg/logger"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

func taskKey(id string) string { return "task:" + id }

// RedisRepository stores tasks as Redis hashes at key task:{id}.
type RedisRepository struct {
	rdb *redis.Client
}

func NewRedisRepository(rdb *redis.Client) *RedisRepository {
	return &RedisRepository{rdb: rdb}
}

func (r *RedisRepository) Create(ctx context.Context, username string, extraEnv map[string]string, gitURL string) (*Task, error) {
	id := uuid.New().String()

	extraEnvJSON, err := json.Marshal(extraEnv)
	if err != nil {
		return nil, fmt.Errorf("marshal extra_env: %w", err)
	}

	if err := r.rdb.HSet(ctx, taskKey(id),
		"id", id,
		"username", username,
		"state", int(StateNew),
		"sandbox_id", "",
		"proxy_base_url", "",
		"proxy_headers", "{}",
		"session_id", "",
		"extra_env", string(extraEnvJSON),
		"provisioned", "0",
		"git_url", gitURL,
	).Err(); err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}

	t := &Task{
		ID:       id,
		Username: username,
		state:    StateNew,
		extraEnv: extraEnv,
		gitURL:   gitURL,
	}
	t.ops = &redisTaskOps{redisLock{rdb: r.rdb, taskID: id}}
	return t, nil
}

// Get fetches a task from Redis. Returns nil, nil if the task does not exist.
func (r *RedisRepository) Get(ctx context.Context, id string) (*Task, error) {
	fields, err := r.rdb.HGetAll(ctx, taskKey(id)).Result()
	if err != nil {
		return nil, fmt.Errorf("get task %s: %w", id, err)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return unmarshalTask(r.rdb, fields)
}

func (r *RedisRepository) Delete(ctx context.Context, id string) error {
	if err := r.rdb.Del(ctx, taskKey(id), lockKey(id)).Err(); err != nil {
		return fmt.Errorf("delete task %s: %w", id, err)
	}
	return nil
}

func unmarshalTask(rdb *redis.Client, fields map[string]string) (*Task, error) {
	id := fields["id"]

	stateInt, _ := strconv.Atoi(fields["state"])

	var proxyHeaders map[string]string
	if h := fields["proxy_headers"]; h != "" && h != "{}" {
		if err := json.Unmarshal([]byte(h), &proxyHeaders); err != nil {
			return nil, fmt.Errorf("unmarshal proxy_headers: %w", err)
		}
	}

	var extraEnv map[string]string
	if e := fields["extra_env"]; e != "" && e != "null" {
		if err := json.Unmarshal([]byte(e), &extraEnv); err != nil {
			return nil, fmt.Errorf("unmarshal extra_env: %w", err)
		}
	}

	t := &Task{
		ID:           id,
		Username:     fields["username"],
		state:        State(stateInt),
		sandboxID:    fields["sandbox_id"],
		proxyBaseURL: fields["proxy_base_url"],
		proxyHeaders: proxyHeaders,
		sessionID:    fields["session_id"],
		extraEnv:     extraEnv,
		provisioned:  fields["provisioned"] == "1",
		gitURL:       fields["git_url"],
		errorMsg:     fields["error_msg"],
	}
	t.ops = &redisTaskOps{redisLock{rdb: rdb, taskID: id}}
	return t, nil
}

// ---- redisTaskOps implements taskOps ----

type redisTaskOps struct {
	redisLock
}

func (o *redisTaskOps) persistRunning(sandboxID, proxyBaseURL string, proxyHeaders map[string]string) {
	headersJSON, _ := json.Marshal(proxyHeaders)
	ctx := context.Background()
	if err := o.rdb.HSet(ctx, taskKey(o.taskID),
		"state", int(StateRunning),
		"sandbox_id", sandboxID,
		"proxy_base_url", proxyBaseURL,
		"proxy_headers", string(headersJSON),
	).Err(); err != nil {
		logger.Default().Error("redis: persist running", "task_id", o.taskID, "err", err)
	}
}

func (o *redisTaskOps) persistProvisioning() {
	ctx := context.Background()
	if err := o.rdb.HSet(ctx, taskKey(o.taskID), "state", int(StateProvisioning)).Err(); err != nil {
		logger.Default().Error("redis: persist provisioning", "task_id", o.taskID, "err", err)
	}
}

func (o *redisTaskOps) persistError(msg string) {
	ctx := context.Background()
	if err := o.rdb.HSet(ctx, taskKey(o.taskID), "state", int(StateError), "error_msg", msg).Err(); err != nil {
		logger.Default().Error("redis: persist error state", "task_id", o.taskID, "err", err)
	}
}

// setSessionScript sets session_id only if it is currently empty (write-once invariant).
var setSessionScript = redis.NewScript(`
	if redis.call("HGET", KEYS[1], "session_id") == "" then
		redis.call("HSET", KEYS[1], "session_id", ARGV[1])
		return 1
	end
	return 0
`)

func (o *redisTaskOps) persistSessionID(sessionID string) bool {
	ctx := context.Background()
	result, err := setSessionScript.Run(ctx, o.rdb, []string{taskKey(o.taskID)}, sessionID).Int()
	if err != nil {
		logger.Default().Error("redis: persist session_id", "task_id", o.taskID, "err", err)
		return false
	}
	return result == 1
}


// persistTitle is a no-op for RedisRepository; title durability requires MySQLRepository.
func (o *redisTaskOps) persistTitle(_ string) {}

func (o *redisTaskOps) ensureProvisioned(fn func() error) error {
	ctx := context.Background()
	return o.withLock(ctx, 30*time.Second, func() error {
		provisioned, err := o.rdb.HGet(ctx, taskKey(o.taskID), "provisioned").Result()
		if err != nil && err != redis.Nil {
			return fmt.Errorf("check provisioned for task %s: %w", o.taskID, err)
		}
		if provisioned == "1" {
			return nil
		}
		if err := fn(); err != nil {
			return err
		}
		// Verify fn() actually persisted the running state before committing provisioned=1.
		// This catches silent failures in persistRunning and prevents the task from being
		// stuck as provisioned=1 with no sandbox, which would block all future retries.
		state, err := o.rdb.HGet(ctx, taskKey(o.taskID), "state").Result()
		if err != nil && err != redis.Nil {
			return fmt.Errorf("verify state for task %s after provisioning: %w", o.taskID, err)
		}
		if state != strconv.Itoa(int(StateRunning)) {
			return fmt.Errorf("task %s: provisioning completed but state not persisted (got %q, want %q)",
				o.taskID, state, strconv.Itoa(int(StateRunning)))
		}
		if err := o.rdb.HSet(ctx, taskKey(o.taskID), "provisioned", "1").Err(); err != nil {
			return fmt.Errorf("set provisioned for task %s: %w", o.taskID, err)
		}
		return nil
	})
}

func (o *redisTaskOps) resetIfExpired(isAlive func(string) (bool, error)) (bool, error) {
	ctx := context.Background()
	var wasReset bool

	err := o.withLock(ctx, 5*time.Second, func() error {
		provisioned, err := o.rdb.HGet(ctx, taskKey(o.taskID), "provisioned").Result()
		if err != nil && err != redis.Nil {
			return fmt.Errorf("check provisioned for task %s: %w", o.taskID, err)
		}
		if err == redis.Nil || provisioned != "1" {
			return nil
		}

		sandboxID, err := o.rdb.HGet(ctx, taskKey(o.taskID), "sandbox_id").Result()
		if err != nil && err != redis.Nil {
			return fmt.Errorf("get sandbox_id for task %s: %w", o.taskID, err)
		}
		if err == redis.Nil || sandboxID == "" {
			return nil
		}

		alive, err := isAlive(sandboxID)
		if err != nil {
			return err
		}

		if !alive {
			if err := o.rdb.HSet(ctx, taskKey(o.taskID),
				"state", int(StateNew),
				"sandbox_id", "",
				"proxy_base_url", "",
				"proxy_headers", "{}",
				"provisioned", "0",
			).Err(); err != nil {
				return fmt.Errorf("reset task %s: %w", o.taskID, err)
			}
			wasReset = true
		}
		return nil
	})

	return wasReset, err
}

func (o *redisTaskOps) resetForReprovisioning() {
	ctx := context.Background()
	if err := o.withLock(ctx, 30*time.Second, func() error {
		return o.rdb.HSet(ctx, taskKey(o.taskID),
			"state", int(StateNew),
			"sandbox_id", "",
			"proxy_base_url", "",
			"proxy_headers", "{}",
			"provisioned", "0",
		).Err()
	}); err != nil {
		logger.Default().Error("redis: reset for reprovisioning", "task_id", o.taskID, "err", err)
	}
}
