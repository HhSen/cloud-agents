# Configuration Reference

The server loads its configuration from a YAML file (`config.yaml` by default; override with `-config <path>`).

Copy `config.example.yaml` as a starting point:

```bash
cp config.example.yaml config.yaml
# fill in required fields, then:
go run ./cmd/server
```

---

## `server`

| Field         | Default                   | Description                                                                  |
| ------------- | ------------------------- | ---------------------------------------------------------------------------- |
| `port`        | `"8091"`                  | TCP port the HTTP server listens on                                          |
| `cors_origin` | `"http://localhost:5173"` | `Access-Control-Allow-Origin` header value; set to `"*"` to allow any origin |

---

## `sandbox`

| Field           | Required | Default                                | Description                                                                                            |
| --------------- | -------- | -------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `api_key`       | ✓        | —                                      | API key sent as `OPEN-SANDBOX-API-KEY` header on all lifecycle calls (`POST/GET/DELETE /v1/sandboxes`) |
| `server_url`    |          | `"http://localhost:8080"`              | Base URL of the OpenSandbox server                                                                     |
| `image`         |          | `"opensandbox/code-interpreter:local"` | Container image used when creating a new sandbox                                                       |
| `platform.os`   |          | —                                      | Target OS for the sandbox container (e.g. `linux`). Both `os` and `arch` must be set together          |
| `platform.arch` |          | —                                      | Target architecture (e.g. `amd64`, `arm64`). Both `os` and `arch` must be set together                 |

### Platform override

The `platform` block is optional. When both `os` and `arch` are set, the values are forwarded to the OpenSandbox `POST /v1/sandboxes` request. This is useful when the backend host and the sandbox host have different architectures (e.g. running on Apple Silicon while sandboxes run on x86-64 Linux):

```yaml
sandbox:
  platform:
    os: linux
    arch: amd64
```

---

## `anthropic`

| Field                        | Description                                                                                          |
| ---------------------------- | ---------------------------------------------------------------------------------------------------- |
| `base_url`                   | Custom API base URL. Leave empty to use `api.anthropic.com`. Injected as `ANTHROPIC_BASE_URL` if set |
| `model`                      | Model identifier (e.g. `claude-sonnet-4-6`). Injected as `ANTHROPIC_MODEL` if set                    |
| `disable_experimental_betas` | Set to `"1"` to inject `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1` into sandboxes                     |

There is no global `api_key` field. Each user sets their own Anthropic API key via the Settings page (`PUT /api/user/settings`). The key is stored AES-256-GCM encrypted in `users.anthropic_api_key_enc` and injected as `ANTHROPIC_API_KEY` into the user's sandbox at provision time. See [`anthropic-key-management.md`](anthropic-key-management.md).

The `base_url` field is useful when routing traffic through an internal proxy or a compatible API gateway instead of calling Anthropic directly.

---

## `redis`

**Required.** The server refuses to start if `url` is empty.

| Field | Required | Description                                                          |
| ----- | -------- | -------------------------------------------------------------------- |
| `url` | ✓        | Redis connection URL. Format: `redis://[:password@]host[:port][/db]` |

Redis stores two categories of data:

| Key pattern              | Contents                                                                                       | Durability                      |
| ------------------------ | ---------------------------------------------------------------------------------------------- | ------------------------------- |
| `sandbox:{task_id}`      | Ephemeral sandbox routing fields (`sandbox_id`, `proxy_base_url`, `proxy_headers`). 7-day TTL. | Cleared on sandbox reset/delete |
| `task-lock:{task_id}`    | Distributed provisioning lock. 30 s TTL.                                                       | Auto-released on crash          |
| `cli_login_session:{id}` | OIDC CLI login token. 5 min TTL.                                                               | OIDC flow only                  |

The server pings Redis at startup and exits immediately if it is unreachable.

```yaml
redis:
  url: "redis://localhost:6379"        # no auth
  # url: "redis://:secret@host:6379"   # with password
  # url: "redis://host:6379/1"         # database 1
```

See [redis-storage.md](redis-storage.md) for the full storage architecture, key operations, and a lifecycle walkthrough.

---

## `orangefs`

OrangeFS provides persistent conversation history storage (S3-compatible) and the in-sandbox file system service.

| Field        | Description                                                                                                                      |
| ------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| `addr`       | Internal `host:port` of the OrangeFS RPC service. Injected into sandboxes as `ORANGEFS_RS_ADDR`                                  |
| `token`      | Auth token for the OrangeFS service. Injected into sandboxes as `ORANGEFS_TOKEN`                                                 |
| `volume`     | OrangeFS volume name. Injected into sandboxes as `ORANGEFS_VOLUME`                                                               |
| `endpoint`   | Public S3-compatible endpoint URL. Used by the backend's own S3 client to fetch session history for `GET /api/tasks/:id/history` |
| `access_key` | S3 access key for the backend client                                                                                             |
| `secret_key` | S3 secret key for the backend client                                                                                             |

The `addr`, `token`, and `volume` fields are forwarded into each sandbox at provision time. The `endpoint`, `access_key`, and `secret_key` fields are used only by the backend process itself when reading conversation history from OFS.

All OrangeFS fields are optional. If left empty:
- `GET /api/tasks/:id/history` returns an error
- Sandboxes start without OFS env vars (history is not persisted)

See [ofsspec.md](ofsspec.md) for the OFS file layout for session history.

---

## `mysql`

**Required.** The server refuses to start if `dsn` is empty.

| Field | Required | Description                                                                 |
| ----- | -------- | --------------------------------------------------------------------------- |
| `dsn` | ✓        | MySQL DSN. Format: `user:pass@tcp(host:port)/dbname?parseTime=true&loc=UTC` |

`db.Open` runs `AutoMigrate` on startup to create/update the following tables:

| Table             | Purpose                                                                                  |
| ----------------- | ---------------------------------------------------------------------------------------- |
| `users`           | User accounts (authentication)                                                           |
| `tasks`           | Task state: id, username, state, title, session_id, extra_env, provisioned, schedule_id |
| `scheduled_tasks` | Schedule templates: cron expression, prompt, enabled flag, last/next run timestamps      |

Ephemeral sandbox routing data (`sandbox_id`, `proxy_base_url`, `proxy_headers`) is **not** stored in MySQL — it lives in Redis under `sandbox:{task_id}`. See [redis-storage.md](redis-storage.md).

---

## `auth`

**Required** (all fields must be set when MySQL is configured).

| Field               | Required            | Description                                                                                             |
| ------------------- | ------------------- | ------------------------------------------------------------------------------------------------------- |
| `secret_key`        | ✓                   | HS256 signing key for app JWTs. Use a long random hex string. Rotate to invalidate all active sessions. |
| `oidc_state_secret` | ✓ when OIDC enabled | Separate HS256 key for OIDC state JWTs. Must not equal `secret_key`.                                    |
| `token_ttl_seconds` | ✓                   | Lifetime of issued app JWTs. Default: `86400` (24 h)                                                    |
| `state_ttl_seconds` | ✓ when OIDC enabled | Lifetime of OIDC state JWTs. Default: `600` (10 min)                                                    |
| `frontend_url`      | ✓                   | Frontend origin (e.g. `http://localhost:5173`). Used as the redirect base after successful auth.        |

---

## `oidc`

Optional. OIDC routes are registered only when `client_id` and `discovery_url` are both non-empty.

| Field              | Description                                                                                            |
| ------------------ | ------------------------------------------------------------------------------------------------------ |
| `client_id`        | OAuth2 client ID registered with the IdP                                                               |
| `client_secret`    | OAuth2 client secret                                                                                   |
| `discovery_url`    | OIDC discovery document URL (e.g. `https://accounts.google.com/.well-known/openid-configuration`)      |
| `redirect_uri`     | Browser callback URL (e.g. `http://your-service/api/auth/oidc/callback`). Must match IdP registration. |
| `cli_redirect_uri` | CLI flow callback URL (e.g. `http://your-service/api/auth/oidc/cli-callback`). Required for CLI login. |

---

## `sso`

Optional. SSO routes are registered only when `app_id` is non-empty.

| Field          | Description                                                                                                                                      |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `base_url`     | SSO base URL. Default: `https://mis.diditaxi.com.cn`                                                                                             |
| `app_id`       | App ID assigned by UPM when registering the application                                                                                          |
| `app_key`      | App key assigned by UPM                                                                                                                          |
| `callback_url` | Callback URL registered in UPM (e.g. `http://your-service/api/auth/sso/callback`). Must exactly match UPM registration — not passed dynamically. |

> **Note:** The `callback_url` value must match what is registered in UPM for the given `app_id`. The SSO server uses the UPM-registered value; this field is purely for documentation and operational reference.

---

## `schedule`

Optional. Controls the cron scheduler.

| Field | Default | Description |
|---|---|---|
| `enabled` | `true` | Set to `false` to disable the cron runner (e.g. on read-only replicas). CRUD endpoints remain available; no schedules fire. |
| `max_concurrent` | `50` | Global cap on simultaneous schedule-triggered sandbox runs. |

```yaml
schedule:
  enabled: true
  max_concurrent: 50
```

See [`scheduled-tasks.md`](scheduled-tasks.md) for the full design.

---

## `security`

Controls per-user secret encryption. The same key is used for both SSH private keys and Anthropic API keys.

| Field            | Description                                                                                                                                                                      |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ssh_key_secret` | 32-byte AES-256 key encoded as 64 hex characters. Used to encrypt/decrypt per-user secrets stored in `users.ssh_private_key_enc` and `users.anthropic_api_key_enc`. Generate with `openssl rand -hex 32`. |

**Startup behaviour:**
- If `ssh_key_secret` is blank and no users have a stored SSH key: server starts normally (feature is inactive).
- If `ssh_key_secret` is blank and any user has a stored SSH key: server exits with an error. This prevents silently failing to decrypt keys after a configuration change.

When configured, `mgr.WithSSHKeys(gormDB, sshKeySecret)` is called at startup so the sandbox manager can decrypt and inject both SSH keys and Anthropic API keys at provision time.

See [`ssh-key-management.md`](ssh-key-management.md) and [`anthropic-key-management.md`](anthropic-key-management.md) for the full designs.

---

## Full annotated example

```yaml
server:
  port: "8091"
  cors_origin: "http://localhost:5173"

sandbox:
  api_key: your-opensandbox-api-key
  server_url: "http://localhost:8080"
  image: "opensandbox/code-interpreter:local"
  # platform:
  #   os: linux
  #   arch: amd64

anthropic:
  base_url: ""
  model: ""
  disable_experimental_betas: ""
  # Note: Anthropic API keys are set per-user in Settings, not here.

redis:
  url: "redis://localhost:6379"  # required — sandbox mapping + distributed locks

orangefs:
  addr: ""
  token: ""
  endpoint: ""
  volume: ""
  access_key: ""
  secret_key: ""

mysql:
  dsn: "user:pass@tcp(localhost:3306)/l_lab?parseTime=true&loc=UTC"

auth:
  secret_key: "<long-random-hex>"
  oidc_state_secret: "<another-long-random-hex>"   # required only if oidc is enabled
  token_ttl_seconds: 86400
  state_ttl_seconds: 600
  frontend_url: "http://localhost:5173"

oidc:
  client_id: ""
  client_secret: ""
  discovery_url: ""       # e.g. https://accounts.google.com/.well-known/openid-configuration
  redirect_uri: ""        # e.g. http://localhost:8091/api/auth/oidc/callback
  cli_redirect_uri: ""    # e.g. http://localhost:8091/api/auth/oidc/cli-callback

sso:
  base_url: "https://mis.diditaxi.com.cn"
  app_id: ""
  app_key: ""
  callback_url: ""        # must match UPM registration

security:
  ssh_key_secret: ""      # 32-byte hex; generate with: openssl rand -hex 32

schedule:
  enabled: true
  max_concurrent: 50
```
