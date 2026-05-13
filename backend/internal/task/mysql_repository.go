package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/your-org/platform-backend/pkg/logger"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/your-org/platform-backend/internal/db"
	"gorm.io/gorm"
)

// sandboxKey is the Redis hash key for ephemeral sandbox fields of a task.
func sandboxKey(id string) string { return "sandbox:" + id }

// MySQLRepository persists task fields in MySQL and stores ephemeral sandbox
// data (sandbox_id, proxy_base_url, proxy_headers) in Redis under sandbox:{id}.
type MySQLRepository struct {
	db  *gorm.DB
	rdb *redis.Client
}

func NewMySQLRepository(db *gorm.DB, rdb *redis.Client) *MySQLRepository {
	return &MySQLRepository{db: db, rdb: rdb}
}

func (r *MySQLRepository) Create(ctx context.Context, username string, extraEnv map[string]string) (*Task, error) {
	var user db.User
	if err := r.db.WithContext(ctx).Where("user_name = ?", username).First(&user).Error; err != nil {
		return nil, fmt.Errorf("look up user %s: %w", username, err)
	}

	id := uuid.New().String()

	extraEnvJSON, err := json.Marshal(extraEnv)
	if err != nil {
		return nil, fmt.Errorf("marshal extra_env: %w", err)
	}

	rec := db.Task{
		ID:       id,
		UserID:   user.ID,
		State:    int(StateNew),
		ExtraEnv: string(extraEnvJSON),
	}
	if err := r.db.WithContext(ctx).Create(&rec).Error; err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}

	t := &Task{
		ID:       id,
		Username: username,
		UserID:   user.ID,
		state:    StateNew,
		extraEnv: extraEnv,
	}
	t.ops = &mysqlTaskOps{db: r.db, rdb: r.rdb, lock: redisLock{rdb: r.rdb, taskID: id}}
	return t, nil
}

// Get returns nil, nil when the task does not exist.
func (r *MySQLRepository) Get(ctx context.Context, id string) (*Task, error) {
	var rec db.Task
	if err := r.db.WithContext(ctx).Preload("User").Where("id = ?", id).First(&rec).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get task %s: %w", id, err)
	}

	var extraEnv map[string]string
	if rec.ExtraEnv != "" && rec.ExtraEnv != "null" {
		if err := json.Unmarshal([]byte(rec.ExtraEnv), &extraEnv); err != nil {
			return nil, fmt.Errorf("unmarshal extra_env for task %s: %w", id, err)
		}
	}

	// Read ephemeral sandbox fields from Redis.
	var sandboxID, proxyBaseURL string
	var proxyHeaders map[string]string
	fields, err := r.rdb.HGetAll(ctx, sandboxKey(id)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("get sandbox fields for task %s: %w", id, err)
	}
	if len(fields) > 0 {
		sandboxID = fields["sandbox_id"]
		proxyBaseURL = fields["proxy_base_url"]
		if h := fields["proxy_headers"]; h != "" && h != "{}" {
			if err := json.Unmarshal([]byte(h), &proxyHeaders); err != nil {
				return nil, fmt.Errorf("unmarshal proxy_headers for task %s: %w", id, err)
			}
		}
	}

	t := &Task{
		ID:           id,
		Username:     rec.User.UserName,
		UserID:       rec.UserID,
		state:        State(rec.State),
		sandboxID:    sandboxID,
		proxyBaseURL: proxyBaseURL,
		proxyHeaders: proxyHeaders,
		sessionID:    rec.SessionID,
		title:        rec.Title,
		extraEnv:     extraEnv,
		provisioned:  rec.Provisioned,
	}
	t.ops = &mysqlTaskOps{db: r.db, rdb: r.rdb, lock: redisLock{rdb: r.rdb, taskID: id}}
	return t, nil
}

func (r *MySQLRepository) List(ctx context.Context, username string) ([]TaskSummary, error) {
	var user db.User
	if err := r.db.WithContext(ctx).Where("user_name = ?", username).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("look up user %s: %w", username, err)
	}

	var records []db.Task
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", user.ID).
		Order("updated_at DESC").
		Limit(100).
		Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list tasks for %s: %w", username, err)
	}
	summaries := make([]TaskSummary, len(records))
	for i, rec := range records {
		summaries[i] = TaskSummary{
			ID:        rec.ID,
			Title:     rec.Title,
			State:     StateString(State(rec.State), rec.SessionID != ""),
			CreatedAt: rec.CreatedAt,
			UpdatedAt: rec.UpdatedAt,
		}
	}
	return summaries, nil
}

func (r *MySQLRepository) Delete(ctx context.Context, id string) error {
	if err := r.db.WithContext(ctx).Delete(&db.Task{}, "id = ?", id).Error; err != nil {
		return fmt.Errorf("delete task %s: %w", id, err)
	}
	// Best-effort cleanup of Redis sandbox data and lock key.
	if err := r.rdb.Del(ctx, sandboxKey(id), lockKey(id)).Err(); err != nil {
		logger.Default().Warn("mysql repo: redis cleanup failed", "task_id", id, "err", err)
	}
	return nil
}

// ---- mysqlTaskOps implements taskOps ----

type mysqlTaskOps struct {
	db   *gorm.DB
	rdb  *redis.Client
	lock redisLock
}

const sandboxTTL = 7 * 24 * time.Hour

func (o *mysqlTaskOps) persistRunning(sandboxID, proxyBaseURL string, proxyHeaders map[string]string) {
	headersJSON, _ := json.Marshal(proxyHeaders)
	ctx := context.Background()
	if err := o.db.WithContext(ctx).Model(&db.Task{}).
		Where("id = ?", o.lock.taskID).
		Update("state", int(StateRunning)).Error; err != nil {
		logger.Default().Error("mysql: persist running", "task_id", o.lock.taskID, "err", err)
		return
	}
	key := sandboxKey(o.lock.taskID)
	pipe := o.rdb.Pipeline()
	pipe.HSet(ctx, key,
		"sandbox_id", sandboxID,
		"proxy_base_url", proxyBaseURL,
		"proxy_headers", string(headersJSON),
	)
	pipe.Expire(ctx, key, sandboxTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		logger.Default().Error("redis: persist sandbox", "task_id", o.lock.taskID, "err", err)
	}
}

func (o *mysqlTaskOps) persistProvisioning() {
	ctx := context.Background()
	if err := o.db.WithContext(ctx).Model(&db.Task{}).
		Where("id = ?", o.lock.taskID).
		Update("state", int(StateProvisioning)).Error; err != nil {
		logger.Default().Error("mysql: persist provisioning", "task_id", o.lock.taskID, "err", err)
	}
}

func (o *mysqlTaskOps) persistError() {
	ctx := context.Background()
	if err := o.db.WithContext(ctx).Model(&db.Task{}).
		Where("id = ?", o.lock.taskID).
		Update("state", int(StateError)).Error; err != nil {
		logger.Default().Error("mysql: persist error state", "task_id", o.lock.taskID, "err", err)
	}
}

// persistSessionID writes sessionID only if the field is currently empty (write-once invariant).
// Uses a conditional UPDATE so the check-and-set is atomic at the database level.
func (o *mysqlTaskOps) persistSessionID(sessionID string) bool {
	ctx := context.Background()
	result := o.db.WithContext(ctx).Model(&db.Task{}).
		Where("id = ? AND session_id = ''", o.lock.taskID).
		Update("session_id", sessionID)
	if result.Error != nil {
		logger.Default().Error("mysql: persist session_id", "task_id", o.lock.taskID, "err", result.Error)
		return false
	}
	return result.RowsAffected == 1
}

func (o *mysqlTaskOps) ensureProvisioned(fn func() error) error {
	ctx := context.Background()
	return o.lock.withLock(ctx, 30*time.Second, func() error {
		var rec db.Task
		if err := o.db.WithContext(ctx).Select("provisioned").
			Where("id = ?", o.lock.taskID).First(&rec).Error; err != nil {
			return fmt.Errorf("check provisioned for task %s: %w", o.lock.taskID, err)
		}
		if rec.Provisioned {
			return nil
		}
		if err := fn(); err != nil {
			return err
		}
		// Verify fn() actually persisted StateRunning before marking provisioned=true.
		// Guards against silent failures in persistRunning (same invariant as Redis impl).
		var current db.Task
		if err := o.db.WithContext(ctx).Select("state").
			Where("id = ?", o.lock.taskID).First(&current).Error; err != nil {
			return fmt.Errorf("verify state for task %s after provisioning: %w", o.lock.taskID, err)
		}
		if State(current.State) != StateRunning {
			return fmt.Errorf("task %s: provisioning completed but state not persisted (got %d, want %d)",
				o.lock.taskID, current.State, int(StateRunning))
		}
		// Also verify the sandbox hash was written to Redis; a silent Redis failure
		// in persistRunning would leave the task Running in MySQL with no proxy route.
		sandboxID, err := o.rdb.HGet(ctx, sandboxKey(o.lock.taskID), "sandbox_id").Result()
		if errors.Is(err, redis.Nil) || sandboxID == "" {
			return fmt.Errorf("task %s: provisioning completed but sandbox not mapped in Redis", o.lock.taskID)
		}
		if err != nil {
			return fmt.Errorf("verify sandbox for task %s after provisioning: %w", o.lock.taskID, err)
		}
		if err := o.db.WithContext(ctx).Model(&db.Task{}).
			Where("id = ?", o.lock.taskID).
			Update("provisioned", true).Error; err != nil {
			return fmt.Errorf("set provisioned for task %s: %w", o.lock.taskID, err)
		}
		return nil
	})
}

func (o *mysqlTaskOps) resetIfExpired(isAlive func(string) (bool, error)) (bool, error) {
	ctx := context.Background()
	var wasReset bool

	err := o.lock.withLock(ctx, 5*time.Second, func() error {
		var rec db.Task
		if err := o.db.WithContext(ctx).Select("provisioned").
			Where("id = ?", o.lock.taskID).First(&rec).Error; err != nil {
			return fmt.Errorf("check provisioned for task %s: %w", o.lock.taskID, err)
		}
		if !rec.Provisioned {
			return nil
		}

		sandboxID, err := o.rdb.HGet(ctx, sandboxKey(o.lock.taskID), "sandbox_id").Result()
		if errors.Is(err, redis.Nil) || sandboxID == "" {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get sandbox_id for task %s: %w", o.lock.taskID, err)
		}

		alive, err := isAlive(sandboxID)
		if err != nil {
			return err
		}
		if !alive {
			if err := o.db.WithContext(ctx).Model(&db.Task{}).
				Where("id = ?", o.lock.taskID).
				Select("state", "provisioned").
				Updates(&db.Task{State: int(StateNew), Provisioned: false}).Error; err != nil {
				return fmt.Errorf("reset task %s in mysql: %w", o.lock.taskID, err)
			}
			if err := o.rdb.Del(ctx, sandboxKey(o.lock.taskID)).Err(); err != nil {
				return fmt.Errorf("clear sandbox for task %s: %w", o.lock.taskID, err)
			}
			wasReset = true
		}
		return nil
	})

	return wasReset, err
}

func (o *mysqlTaskOps) persistTitle(title string) {
	ctx := context.Background()
	if err := o.db.WithContext(ctx).Model(&db.Task{}).
		Where("id = ?", o.lock.taskID).
		Update("title", title).Error; err != nil {
		logger.Default().Error("mysql: persist title", "task_id", o.lock.taskID, "err", err)
	}
}

func (o *mysqlTaskOps) resetForReprovisioning() {
	ctx := context.Background()
	if err := o.lock.withLock(ctx, 30*time.Second, func() error {
		if err := o.db.WithContext(ctx).Model(&db.Task{}).
			Where("id = ?", o.lock.taskID).
			Select("state", "provisioned").
			Updates(&db.Task{State: int(StateNew), Provisioned: false}).Error; err != nil {
			return fmt.Errorf("reset task %s in mysql: %w", o.lock.taskID, err)
		}
		return o.rdb.Del(ctx, sandboxKey(o.lock.taskID)).Err()
	}); err != nil {
		logger.Default().Error("mysql: reset for reprovisioning", "task_id", o.lock.taskID, "err", err)
	}
}
