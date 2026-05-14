# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Read Before Coding

Before touching any backend code, read the relevant spec documents in `backend/docs/specs/`. They are the authoritative source for design decisions and implementation contracts.

| Document | When to read |
|---|---|
| [`docs/specs/data-management.md`](docs/specs/data-management.md) | Any change to User, Task, or Sandbox entities |
| [`docs/specs/storage.md`](docs/specs/storage.md) | Any change to MySQL, Redis, or OFS storage |
| [`docs/specs/resource-mapping.md`](docs/specs/resource-mapping.md) | Any change to task/sandbox/session lifecycle |
| [`docs/specs/resources.md`](docs/specs/resources.md) | Any change to skill or MCP resource handling |
| [`docs/specs/ofsspec.md`](docs/specs/ofsspec.md) | Any change to OFS file access or session history |
| [`docs/specs/workspace.md`](docs/specs/workspace.md) | Any change to the workspace/filesystem proxy |
| [`docs/specs/configuration.md`](docs/specs/configuration.md) | Any change to config fields or YAML structure |

## Commands

```bash
# Dev server (default config.yaml)
go run ./cmd/server

# Custom config
go run ./cmd/server -config /path/to/config.yaml

# Build binary
go build -o bin/server ./cmd/server

# All tests with race detection (required after multi-file changes)
go test -race ./...

# Single package
go test -race ./internal/task/...

# Single test
go test -race -run TestName ./internal/task/...

# Regenerate Swagger docs (after changing handler annotations)
swag init -g cmd/server/main.go --output docs --parseDependency --parseInternal
```

## Architecture

### Dependency wiring (`cmd/server/main.go` → `internal/api/router.go`)

`RouterDeps` collects all top-level dependencies. Several features are **optional** — their nil-ness gates routes and capabilities at startup:

| Dep                       | nil means                                |
| ------------------------- | ---------------------------------------- |
| `DB`                      | Auth disabled (dev mode)                 |
| `OIDCService`             | OIDC routes not registered               |
| `SSOService`              | SSO routes not registered                |
| `Redis`                   | CLI OIDC flow unavailable                |
| `KindsRepo` + `OFSWriter` | `/api/resources` routes return 503       |
| `WorkspaceReader`         | `/api/tasks/:id/workspace/*` returns 409 |

`Handler` has the same optional wiring via `withResources`, `withWorkspace`, `withExecd` methods called inside `NewRouter`.

### Task state machine (`internal/task/store.go`)

`State` tracks sandbox liveness only. The full API state label is the **combination** of `State` × `sessionID presence`:

| State               | `sessionID == ""` | `sessionID set` |
| ------------------- | ----------------- | --------------- |
| `StateNew`          | `pending`         | `paused`        |
| `StateProvisioning` | `provisioning`    | `resuming`      |
| `StateRunning`      | `idle`            | `active`        |
| `StateError`        | `error`           | `error`         |

`session_id` is **write-once** — never cleared once set (enforced in `SetSessionID` with an in-process mutex check or a Lua HSETNX for Redis). This lets OFS history be read without an active sandbox.

### Repository backends (`internal/task/`)

| Backend            | When used   | Storage                                                    |
| ------------------ | ----------- | ---------------------------------------------------------- |
| `MemoryRepository` | dev / tests | in-process map + `sync.Mutex`                              |
| `MySQLRepository`  | production  | MySQL (durable) + Redis (ephemeral sandbox mapping + lock) |

`taskOps` is the persistence hook interface attached to each `Task`. `nil` means in-process only; `mysqlTaskOps` / `redisTaskOps` persist mutations to their backing store. Lock order: `provisionMu → mu`.

`EnsureProvisioned` is unlike `sync.Once`: a failed `fn` leaves `provisioned=false` so the next caller retries.

### Resources API (`/api/resources`)

Two resource kinds: `skill` and `mcp`.

- **skill** — SKILL.md content stored at `{username}/resources/skills/{name}/SKILL.md` in OFS. File manifest tracked in `db.Kind.Meta` as `{"files":["SKILL.md",...]}` (see `db.SkillMeta`). Max 20 files, 1 MiB each, paths validated against `^[a-zA-Z0-9_./-]+$` with no `..`/`.`/empty segments.
- **mcp** — JSON config stored at `{username}/resources/mcp/{name}.json` in OFS; the JSON is also stored in `db.Kind.Meta`.

`db.Kind` uniqueness constraint: `(user_id, kind, name)`.

### Execd proxy (`/api/tasks/:id/execd/*path`)

Proxies any method to port `44772` inside the task's sandbox (`{serverURL}/sandboxes/:id/proxy/44772{subpath}`). Used for filesystem ops (search, download, directory listing). Requires `serverURL` + `sandboxAPIKey` via `withExecd`.

### Adding a new endpoint

1. Add handler method to `Handler` in `internal/api/handlers.go` with Swagger annotations (`@Summary`, `@Param`, `@Success`, `@Router`, etc.)
2. Register the route in `internal/api/router.go` (protected group or public, as appropriate)
3. Regenerate docs: `swag init -g cmd/server/main.go --output docs --parseDependency --parseInternal`

### Testing patterns

- Redis tests use `miniredis` (`github.com/alicebob/miniredis/v2`) — no real Redis needed.
- MySQL tests use SQLite in-memory via GORM (`gorm.io/driver/sqlite`) — schema is identical.
- Smoke tests live in `internal/api/smoke_test.go` and hit the full HTTP handler stack.

## Code conventions

- Prefer concrete types over `map[string]any`; define a struct if one doesn't exist.
- Always update Swagger annotations in `internal/api/handlers.go` when adding or changing endpoints, then regenerate the docs.
- `context.Background()` is intentional for provisioning calls — it must survive client disconnects.


Actively check documents under `docs/`.