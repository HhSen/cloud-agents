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

| Field | Default | Description |
|---|---|---|
| `port` | `"8081"` | TCP port the HTTP server listens on |
| `cors_origin` | `"http://localhost:5173"` | `Access-Control-Allow-Origin` header value; set to `"*"` to allow any origin |

---

## `sandbox`

| Field | Required | Default | Description |
|---|---|---|---|
| `api_key` | ✓ | — | API key sent as `OPEN-SANDBOX-API-KEY` header on all lifecycle calls (`POST/GET/DELETE /v1/sandboxes`) |
| `server_url` | | `"http://localhost:8080"` | Base URL of the OpenSandbox server |
| `image` | | `"opensandbox/code-interpreter:local"` | Container image used when creating a new sandbox |
| `platform.os` | | — | Target OS for the sandbox container (e.g. `linux`). Both `os` and `arch` must be set together |
| `platform.arch` | | — | Target architecture (e.g. `amd64`, `arm64`). Both `os` and `arch` must be set together |

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

| Field | Required | Description |
|---|---|---|
| `api_key` | ✓ | Anthropic API key. Injected into every sandbox as `ANTHROPIC_API_KEY` |
| `base_url` | | Custom API base URL. Leave empty to use `api.anthropic.com`. Injected as `ANTHROPIC_BASE_URL` if set |
| `model` | | Model identifier (e.g. `claude-sonnet-4-6`). Injected as `ANTHROPIC_MODEL` if set |
| `disable_experimental_betas` | | Set to `"1"` to inject `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1` into sandboxes |

The `base_url` field is useful when routing traffic through an internal proxy or a compatible API gateway instead of calling Anthropic directly.

---

## `redis`

| Field | Default | Description |
|---|---|---|
| `url` | `""` | Redis connection URL. Empty = use in-memory store |

### Task store selection

| `redis.url` | Store used | Persistence |
|---|---|---|
| empty (default) | In-memory (`MemoryRepository`) | Lost on restart |
| set | Redis (`RedisRepository`) | Survives restarts; shared across instances |

When `redis.url` is set, the server pings Redis at startup and exits immediately if it is unreachable.

**URL format:** `redis://[:password@]host[:port][/db]`

```yaml
redis:
  url: "redis://localhost:6379"        # no auth
  # url: "redis://:secret@host:6379"   # with password
  # url: "redis://host:6379/1"         # database 1
```

See [redis-storage.md](redis-storage.md) for the full Redis data model and key operations.

---

## `orangefs`

OrangeFS provides persistent conversation history storage (S3-compatible) and the in-sandbox file system service.

| Field | Description |
|---|---|
| `addr` | Internal `host:port` of the OrangeFS RPC service. Injected into sandboxes as `ORANGEFS_RS_ADDR` |
| `token` | Auth token for the OrangeFS service. Injected into sandboxes as `ORANGEFS_TOKEN` |
| `volume` | OrangeFS volume name. Injected into sandboxes as `ORANGEFS_VOLUME` |
| `endpoint` | Public S3-compatible endpoint URL. Used by the backend's own S3 client to fetch session history for `GET /api/tasks/:id/history` |
| `access_key` | S3 access key for the backend client |
| `secret_key` | S3 secret key for the backend client |

The `addr`, `token`, and `volume` fields are forwarded into each sandbox at provision time. The `endpoint`, `access_key`, and `secret_key` fields are used only by the backend process itself when reading conversation history from OFS.

All OrangeFS fields are optional. If left empty:
- `GET /api/tasks/:id/history` returns an error
- Sandboxes start without OFS env vars (history is not persisted)

See [ofsspec.md](ofsspec.md) for the OFS file layout for session history.

---

## `mysql`

**Required.** The server refuses to start if `dsn` is empty.

| Field | Required | Description |
|---|---|---|
| `dsn` | ✓ | MySQL DSN. Format: `user:pass@tcp(host:port)/dbname?parseTime=true&loc=UTC` |

`db.Open` runs `AutoMigrate` on startup to create/update the `users` table automatically.

---

## `auth`

**Required** (all fields must be set when MySQL is configured).

| Field | Required | Description |
|---|---|---|
| `secret_key` | ✓ | HS256 signing key for app JWTs. Use a long random hex string. Rotate to invalidate all active sessions. |
| `oidc_state_secret` | ✓ when OIDC enabled | Separate HS256 key for OIDC state JWTs. Must not equal `secret_key`. |
| `token_ttl_seconds` | ✓ | Lifetime of issued app JWTs. Default: `86400` (24 h) |
| `state_ttl_seconds` | ✓ when OIDC enabled | Lifetime of OIDC state JWTs. Default: `600` (10 min) |
| `frontend_url` | ✓ | Frontend origin (e.g. `http://localhost:5173`). Used as the redirect base after successful auth. |

---

## `oidc`

Optional. OIDC routes are registered only when `client_id` and `discovery_url` are both non-empty.

| Field | Description |
|---|---|
| `client_id` | OAuth2 client ID registered with the IdP |
| `client_secret` | OAuth2 client secret |
| `discovery_url` | OIDC discovery document URL (e.g. `https://accounts.google.com/.well-known/openid-configuration`) |
| `redirect_uri` | Browser callback URL (e.g. `http://your-service/api/auth/oidc/callback`). Must match IdP registration. |
| `cli_redirect_uri` | CLI flow callback URL (e.g. `http://your-service/api/auth/oidc/cli-callback`). Required for CLI login. |

---

## `sso`

Optional. SSO routes are registered only when `app_id` is non-empty.

| Field | Description |
|---|---|
| `base_url` | SSO base URL. Default: `https://mis.diditaxi.com.cn` |
| `app_id` | App ID assigned by UPM when registering the application |
| `app_key` | App key assigned by UPM |
| `callback_url` | Callback URL registered in UPM (e.g. `http://your-service/api/auth/sso/callback`). Must exactly match UPM registration — not passed dynamically. |

> **Note:** The `callback_url` value must match what is registered in UPM for the given `app_id`. The SSO server uses the UPM-registered value; this field is purely for documentation and operational reference.

---

## Full annotated example

```yaml
server:
  port: "8081"
  cors_origin: "http://localhost:5173"

sandbox:
  api_key: your-opensandbox-api-key
  server_url: "http://localhost:8080"
  image: "opensandbox/code-interpreter:local"
  # platform:
  #   os: linux
  #   arch: amd64

anthropic:
  api_key: your-anthropic-api-key
  base_url: ""
  model: ""
  disable_experimental_betas: ""

redis:
  url: ""  # set to redis://localhost:6379 to enable persistence

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
  redirect_uri: ""        # e.g. http://localhost:8081/api/auth/oidc/callback
  cli_redirect_uri: ""    # e.g. http://localhost:8081/api/auth/oidc/cli-callback

sso:
  base_url: "https://mis.diditaxi.com.cn"
  app_id: ""
  app_key: ""
  callback_url: ""        # must match UPM registration
```
