# Redis Task Store Migration Plan

> **Superseded.** This plan described the migration from in-memory to Redis storage, which was completed. Task storage has since been migrated again: durable task fields now live in **MySQL** (`tasks` table) and only ephemeral sandbox routing data (`sandbox_id`, `proxy_base_url`, `proxy_headers`) remains in Redis under `sandbox:{task_id}`. See [docs/specs/redis-storage.md](../backend/docs/specs/redis-storage.md) for the current architecture.

---

## Goal

Migrate `task.Store` from an in-memory `map[string]*Task` to Redis so that task state
(sandbox ID, session ID, proxy info, lifecycle state) survives server restarts and is
accessible across multiple backend instances.

## Background

The in-memory `Store` (`internal/task/store.go`) is the single source of truth for:

| Field | Volatility | Notes |
|---|---|---|
| `ID`, `Username` | Immutable | Set at creation |
| `state` | Mutable | Sandbox liveness state machine |
| `sandboxID`, `proxyBaseURL`, `proxyHeaders` | Transient | Cleared on sandbox destroy |
| `sessionID` | Write-once | Never cleared once set (invariant 4) |
| `extraEnv` | Immutable | Set at creation |
| `provisioned` | Mutable | Reset when sandbox dies |

Loss of this store on restart means tasks are orphaned: the API returns 404 for live
sandboxes, and session history becomes unreachable without a session_id.

---

## Design Decisions

### 1. Redis data model

Each task is stored as a Redis hash at key `task:{task_id}`:

```
task:{task_id}
  id             string
  username       string
  state          int (0=New, 1=Provisioning, 2=Running, 3=Error)
  sandbox_id     string
  proxy_base_url string
  proxy_headers  JSON string (map[string]string)
  session_id     string
  extra_env      JSON string (map[string]string)
  provisioned    "0" | "1"
```

All fields always present (empty string for nulls). Single hash per task keeps reads
atomic (`HGETALL`) and avoids multi-key coordination for normal lookups.

### 2. TTL strategy

Tasks are "permanent" per spec but Redis memory is bounded. Proposed:

Tasks persist indefinitely until `DELETE /api/tasks/:id`. No TTL is set on task keys.

### 3. Locking for provisioning

The in-memory `provisionMu sync.Mutex` serialises `EnsureProvisioned` within a process.
In Redis, this becomes a per-task distributed lock:

```
lock key:  task-lock:{task_id}
acquire:   SET task-lock:{task_id} {holder} NX PX 30000
release:   DEL (only if value == holder, via Lua script)
```

- Lock TTL: 30 s (covers worst-case sandbox provision time; server crash auto-releases).
- Holder: `{hostname}:{pid}` to avoid cross-process release bugs.
- `EnsureProvisioned` acquires the lock, checks `provisioned` field in Redis, runs fn if
  false, writes `provisioned=1`.
- If lock acquire fails (another instance is provisioning), caller retries with
  exponential backoff (e.g. 50 ms → 100 ms → 200 ms … up to a configured deadline,
  default 30 s to match the lock TTL).

### 4. Atomic state mutations

State fields (`state`, `sandboxID`, etc.) are written individually with `HSET`. Race
conditions between concurrent reads and writes are acceptable for transient fields
(`sandboxID`, `proxyBaseURL`) because:

- The provisioning lock already serialises the critical path.
- `sessionID` is write-once: use a Lua script that does `HSETNX` (set if not exists) to
  enforce the invariant atomically even across instances.

No multi-field transactions (WATCH/MULTI/EXEC) needed for individual field updates.

### 5. In-process caching

The current design reads task state frequently (e.g., every `SendMessage` call). A pure
Redis round-trip on every read adds ~1 ms per hop. Two options:

**Option A — Pure Redis (recommended for v1):**
Every read is a Redis `HGETALL`. Simpler, always consistent. Acceptable at current
scale; revisit if latency becomes a concern.

**Option B — Local cache + Redis:**
Cache `*Task` in a local map with short TTL (e.g., 5 s), invalidate via Redis pub/sub
on mutations. More complex, adds pub/sub dependency.

Recommend Option A for the initial migration. Cache can be added later as a pure
performance optimization behind the same interface.

### 6. Backward compatibility / cut-over

No dual-write period needed. The in-memory store loses all data on every restart anyway,
so a cut-over is not destructive: tasks that existed before the migration are already
lost at the next restart. The server simply starts writing to Redis from day one.

Provide a `REDIS_URL` env var (or `redis.url` in config.yaml). If absent, fall back to
the in-memory store so local development without Redis continues to work.

---

## Implementation Plan

### Step 1 — Extract a `TaskRepository` interface

Refactor `task.Store` so callers depend on an interface rather than the concrete struct.
No Redis code yet; this is a pure refactor to make the swap testable.

```go
// internal/task/repository.go
type Repository interface {
    Create(ctx context.Context, username string, extraEnv map[string]string) (*Task, error)
    Get(ctx context.Context, id string) (*Task, error)
    Delete(ctx context.Context, id string) error
}
```

`task.Store` becomes `task.MemoryRepository` implementing `Repository`.

**Files touched**: `internal/task/store.go`, `internal/task/repository.go`,
`internal/api/handlers.go`, `internal/sandbox/manager.go`, `cmd/server/main.go`.

**Note**: `Task` itself stays as-is (with its in-process mutexes) for the memory
implementation. The Redis implementation will use a different internal representation
(no embedded mutexes; locking is external via Redis).

---

### Step 2 — Add Redis dependency and config

```
go get github.com/go-redis/redis/v8
```

Add to `config.yaml` / `config.example.yaml`:

```yaml
redis:
  url: ""          # e.g. redis://localhost:6379; empty = use in-memory store
```

Add `RedisConfig` struct to the config package.

---

### Step 3 — Implement `RedisRepository`

New file: `internal/task/redis_repository.go`

Key methods:

| Method | Redis ops |
|---|---|
| `Create` | `HSET task:{id} ...fields`; optional `EXPIRE` |
| `Get` | `HGETALL task:{id}` → deserialize into `*Task` |
| `Delete` | `DEL task:{id}` + `DEL task-lock:{id}` |
| `Task.SetRunning` | `HSET task:{id} state 2 sandbox_id ... proxy_base_url ... proxy_headers ...` |
| `Task.SetSessionID` | Lua: `if redis.call('HGET',key,'session_id')=='' then redis.call('HSET',key,'session_id',ARGV[1]) end` |
| `Task.EnsureProvisioned` | acquire lock → check `provisioned` field → run fn → set `provisioned=1` → release lock |
| `Task.ResetIfExpired` | acquire lock → check `provisioned` → call `isAlive` → `HSET` reset fields → release lock |

`Task` returned from `Get` will be a thin struct whose mutation methods hit Redis directly
(no embedded mutex). The `EnsureProvisioned` / `ResetIfExpired` methods that previously
used `provisionMu` will acquire/release the Redis distributed lock.

---

### Step 4 — Wire up in `cmd/server/main.go`

```go
var repo task.Repository
if cfg.Redis.URL != "" {
    rdb := redis.NewClient(...)
    repo = task.NewRedisRepository(rdb)
} else {
    repo = task.NewMemoryRepository()
}
```

---

### Step 5 — Tests

- `internal/task/redis_repository_test.go`: use
  [`miniredis v2`](https://github.com/alicebob/miniredis) (compatible with go-redis v8)
  for unit tests (no real Redis needed in CI).
- Extend `store_test.go` to run the same concurrency tests (`EnsureProvisioned`,
  `ResetIfExpired`) against the Redis implementation using `miniredis`.
- Run full test suite with race detection: `go test -race ./...`.

---

## Out of scope

- Key scanning / migration of existing in-memory tasks (they are lost on restart today;
  no migration script needed).
- Redis Sentinel / Cluster support (single-node Redis sufficient for v1).
- Session history or OFS data (stored in S3 / OFS, not in-memory).

---

## Resolved decisions

1. **Task TTL**: No expiry — tasks live until explicitly deleted via API.
2. **Lock retry policy**: Exponential backoff with a 30 s deadline.
3. **Config location**: `redis.url` in `config.yaml` (consistent with all other config).
