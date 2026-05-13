# Plan: SSO / OIDC Authentication Integration

## Status: Implemented

## Scope

Add full authentication to the Go backend:

- **OIDC** (standard OpenID Connect / OAuth2 authorization-code flow)
- **Didi SSO** (proprietary dual-domain protocol)
- **CLI OIDC** (polling-based browser handoff for the CLI)
- **JWT middleware** protecting all existing task routes
- **MySQL + GORM** for the User table (replaces in-memory-only identity)

This plan assumes the Gin migration (`plans/gin-migration.md`) has been applied first. All new routes are registered on the Gin engine.

---

## Python → Go library mapping

| Python (l-flow) | Go equivalent | Notes |
| --- | --- | --- |
| `authlib` (OIDC/JWKS) | `github.com/coreos/go-oidc/v3/oidc` | Discovery, JWKS, id_token verify; JWKS auto-refreshes |
| `pyjwt` / `python-jose` | `github.com/golang-jwt/jwt/v5` | state JWT + app JWT |
| SQLAlchemy | `gorm.io/gorm` + `gorm.io/driver/mysql` | ORM; GORM auto-migrate for User table |
| `passlib[bcrypt]` | `golang.org/x/crypto/bcrypt` | Password hash for SSO/OIDC users |
| `redis` (CLI sessions) | `github.com/go-redis/redis/v8` | Already in `go.mod` |
| `httpx` | `net/http` | Already used for sandbox client |
| `pydantic-settings` | `pkg/config/config.go` (YAML) | Extend existing config struct |

---

## New directory layout

```text
backend/
├── internal/
│   ├── auth/
│   │   ├── middleware.go       # JWT + API-key bearer middleware (Gin)
│   │   ├── token.go            # create / verify app JWT
│   │   ├── apikey.go           # SHA-256 API key verify
│   │   └── context.go          # set/get User from gin.Context
│   ├── oidc/
│   │   ├── service.go          # OIDC client: discovery, JWKS, token exchange
│   │   └── handlers.go         # /login, /callback, /cli-login, /cli-callback, /cli-poll
│   ├── sso/
│   │   ├── service.go          # Didi SSO HTTP client: check_code, check_user_ticket
│   │   └── handlers.go         # /login, /callback
│   └── db/
│       ├── mysql.go            # Open + AutoMigrate
│       └── user.go             # User GORM model + find_or_create helper
├── pkg/config/
│   └── config.go               # Extended with MySQL, OIDC, SSO, Auth blocks
└── cmd/server/main.go          # Wire DB, auth routes, auth middleware
```

---

## Step 1: Dependencies

```bash
cd backend
go get gorm.io/gorm
go get gorm.io/driver/mysql
go get github.com/coreos/go-oidc/v3/oidc
go get github.com/golang-jwt/jwt/v5
go get golang.org/x/crypto/bcrypt
```

---

## Step 2: Config extension (`pkg/config/config.go`)

Add to the `Config` struct:

```go
type Config struct {
    // ... existing fields ...
    MySQL  MySQLConfig  `yaml:"mysql"`
    Auth   AuthConfig   `yaml:"auth"`
    OIDC   OIDCConfig   `yaml:"oidc"`
    SSO    SSOConfig    `yaml:"sso"`
}

type MySQLConfig struct {
    DSN string `yaml:"dsn"` // e.g. user:pass@tcp(host:3306)/dbname?parseTime=true
}

type AuthConfig struct {
    SecretKey        string `yaml:"secret_key"`          // app JWT signing key
    OIDCStateSecret  string `yaml:"oidc_state_secret"`   // separate key for state JWT
    TokenTTLSeconds  int    `yaml:"token_ttl_seconds"`   // default 86400
    StateTTLSeconds  int    `yaml:"state_ttl_seconds"`   // default 600
    FrontendURL      string `yaml:"frontend_url"`        // redirect base after auth
}

type OIDCConfig struct {
    ClientID         string `yaml:"client_id"`
    ClientSecret     string `yaml:"client_secret"`
    DiscoveryURL     string `yaml:"discovery_url"`
    RedirectURI      string `yaml:"redirect_uri"`
    CLIRedirectURI   string `yaml:"cli_redirect_uri"`
}

type SSOConfig struct {
    BaseURL     string `yaml:"base_url"`    // https://mis.diditaxi.com.cn
    AppID       string `yaml:"app_id"`
    AppKey      string `yaml:"app_key"`
    CallbackURL string `yaml:"callback_url"`
}
```

`config.example.yaml` additions:

```yaml
mysql:
  dsn: "user:pass@tcp(localhost:3306)/lucas?parseTime=true&loc=UTC"

auth:
  secret_key: "<random-long-string>"
  oidc_state_secret: "<another-random-string>"
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
  callback_url: ""        # e.g. http://your-service/api/auth/sso/callback — must match UPM registration
```

---

## Step 3: MySQL — User model (`internal/db/user.go`)

```go
package db

import (
    "crypto/sha256"
    "fmt"
    "time"

    "golang.org/x/crypto/bcrypt"
    "gorm.io/gorm"
)

// AuthSource enumerates the identity providers.
type AuthSource string

const (
    AuthSourcePassword AuthSource = "password"
    AuthSourceOIDC     AuthSource = "oidc"
    AuthSourceSSO      AuthSource = "sso"
    AuthSourceDev      AuthSource = "dev"      // dev login only — never set in production
    AuthSourceUnknown  AuthSource = "unknown"
)

type User struct {
    ID           uint       `gorm:"primaryKey;autoIncrement"`
    UserName     string     `gorm:"uniqueIndex;size:100;not null"`
    Email        string     `gorm:"size:255;not null"`
    PasswordHash string     `gorm:"size:255;not null"`
    IsActive     bool       `gorm:"default:true"`
    AuthSource   AuthSource `gorm:"size:20;not null;default:'unknown'"`
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

// FindOrCreate looks up a user by username; creates one if absent.
// For SSO/OIDC users pass a random UUID hash as passwordHash.
func FindOrCreate(db *gorm.DB, userName, email string, src AuthSource) (*User, error) {
    var u User
    res := db.Where("user_name = ?", userName).First(&u)
    if res.Error == nil {
        // update stale fields
        changed := false
        if u.Email != email {
            u.Email = email
            changed = true
        }
        if u.AuthSource == AuthSourceUnknown {
            u.AuthSource = src
            changed = true
        }
        if changed {
            db.Save(&u)
        }
        return &u, nil
    }
    if res.Error != gorm.ErrRecordNotFound {
        return nil, res.Error
    }

    // SSO/OIDC users have no password — store bcrypt of a random UUID
    randHash, _ := bcrypt.GenerateFromPassword([]byte(randomUUID()), bcrypt.DefaultCost)
    u = User{
        UserName:     userName,
        Email:        email,
        PasswordHash: string(randHash),
        IsActive:     true,
        AuthSource:   src,
    }
    if err := db.Create(&u).Error; err != nil {
        return nil, err
    }
    return &u, nil
}

// HashAPIKey returns the SHA-256 hex digest used to store/verify API keys.
func HashAPIKey(key string) string {
    return fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}

func randomUUID() string { /* use github.com/google/uuid */ return "" }
```

MySQL connection (`internal/db/mysql.go`):

```go
package db

import (
    "gorm.io/driver/mysql"
    "gorm.io/gorm"
)

func Open(dsn string) (*gorm.DB, error) {
    db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
    if err != nil {
        return nil, err
    }
    if err := db.AutoMigrate(&User{}); err != nil {
        return nil, err
    }
    return db, nil
}
```

---

## Step 4: App JWT (`internal/auth/token.go`)

```go
package auth

import (
    "time"

    "github.com/golang-jwt/jwt/v5"
)

type Claims struct {
    UserID   uint   `json:"user_id"`
    UserName string `json:"user_name"`
    jwt.RegisteredClaims
}

func CreateToken(secretKey string, ttl time.Duration, userID uint, userName string) (string, error) {
    claims := Claims{
        UserID:   userID,
        UserName: userName,
        RegisteredClaims: jwt.RegisteredClaims{
            ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
            IssuedAt:  jwt.NewNumericDate(time.Now()),
        },
    }
    return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secretKey))
}

func VerifyToken(secretKey, tokenStr string) (*Claims, error) {
    tok, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
        // Pin algorithm to prevent algorithm confusion attacks
        if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
        }
        return []byte(secretKey), nil
    })
    if err != nil || !tok.Valid {
        return nil, err
    }
    return tok.Claims.(*Claims), nil
}
```

---

## Step 5: Auth middleware (`internal/auth/middleware.go`)

```go
// BearerAuth extracts the Authorization header token and sets the User on the
// Gin context. Returns 401 if the token is missing or invalid.
func BearerAuth(secretKey string, db *gorm.DB) gin.HandlerFunc {
    return func(c *gin.Context) {
        raw := c.GetHeader("Authorization")
        if raw == "" || !strings.HasPrefix(raw, "Bearer ") {
            c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
            return
        }
        token := strings.TrimPrefix(raw, "Bearer ")

        claims, err := auth.VerifyToken(secretKey, token)
        if err != nil {
            c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
            return
        }

        var u db.User
        if err := gormDB.First(&u, claims.UserID).Error; err != nil || !u.IsActive {
            c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "user not found"})
            return
        }

        SetUser(c, &u)
        c.Next()
    }
}
```

`internal/auth/context.go` stores/retrieves `*db.User` from `gin.Context` using a typed key.

---

## Step 6: OIDC service (`internal/oidc/service.go`)

Uses `coreos/go-oidc/v3` which handles discovery, JWKS caching, and id_token verification.

```go
type OIDCService struct {
    clientID     string
    clientSecret string
    redirectURI  string
    provider     *oidc.Provider   // lazy-init on first call
    verifier     *oidc.IDTokenVerifier
    mu           sync.Mutex
}

// GetProvider initialises the provider from the discovery URL (cached after first call).
func (s *OIDCService) GetProvider(ctx context.Context) (*oidc.Provider, error) { ... }

// AuthURL returns the authorization endpoint URL with state + nonce.
// The nonce is embedded in the auth request via oidc.Nonce(nonce) so the IdP
// embeds it in the returned id_token for later verification.
func (s *OIDCService) AuthURL(ctx context.Context, redirectURI, state, nonce string) (string, error) { ... }

// ExchangeCode exchanges an authorization code for tokens.
func (s *OIDCService) ExchangeCode(ctx context.Context, code, redirectURI string) (*oauth2.Token, error) { ... }

// VerifyIDToken verifies the id_token signature, issuer, audience, and nonce.
func (s *OIDCService) VerifyIDToken(ctx context.Context, rawIDToken, nonce string) (*oidc.IDToken, error) { ... }
```

Key design decisions (mirroring the l-flow Python service):

- Provider is fetched once and cached in memory (`sync.Mutex` guards lazy-init).
- `go-oidc` handles JWKS caching and rotation automatically — no manual `self._jwks = None` trick needed; the library's `KeySet` re-fetches on unknown key IDs.
- Nonce is passed through the state JWT and re-validated inside `VerifyIDToken`.

---

## Step 7: OIDC handlers (`internal/oidc/handlers.go`)

### State JWT encoding

```go
type statePayload struct {
    Nonce       string `json:"nonce"`
    Redirect    string `json:"redirect,omitempty"`
    CLISession  string `json:"cli_session,omitempty"`
    jwt.RegisteredClaims
}
```

### Routes

```text
GET  /api/auth/oidc/login         → redirect to provider
GET  /api/auth/oidc/callback      → verify state+code, issue app JWT, redirect to /login/oidc
POST /api/auth/oidc/cli-login     → body {session_id}, write Redis pending, return {auth_url}
GET  /api/auth/oidc/cli-callback  → same verify chain, write Redis completed+token
GET  /api/auth/oidc/cli-poll      → poll Redis, return {status, token?}
```

#### CLI flow detail

Redis key: `cli_login_session:{session_id}` — TTL 5 min

```go
// cli-login
type CLISession struct {
    Status string `json:"status"` // "pending" | "completed"
    Token  string `json:"token,omitempty"`
}
// write: Status="pending"

// cli-callback (after normal OIDC verify)
// write: Status="completed", Token=<app JWT>

// cli-poll
// completed → return token + delete key
// pending   → return status only (caller retries every 2s, max 150 tries)
```

---

## Step 8: SSO service + handlers (`internal/sso/`)

`service.go` wraps two Didi SSO HTTP calls (both POST `application/x-www-form-urlencoded`):

```go
type CheckCodeResponse struct {
    Ticket   string
    UserName string
}

type CheckTicketResponse struct {
    UID        int
    Email      string
    UserNameZH string
    UserName   string
}

func (s *Service) CheckCode(ctx context.Context, code string) (*CheckCodeResponse, error) { ... }
func (s *Service) CheckUserTicket(ctx context.Context, ticket string) (*CheckTicketResponse, error) { ... }
func (s *Service) LoginURL(jumpto string) string { ... }
```

Login URL construction (direct redirect — no double-redirect):

```go
func (s *Service) LoginURL(jumpto string) string {
    if jumpto == "" { jumpto = "/" }
    return fmt.Sprintf("%s/auth/sso/login?app_id=%s&jumpto=%s&version=1.0",
        s.cfg.BaseURL, url.QueryEscape(s.cfg.AppID), url.QueryEscape(jumpto))
}
```

Both API calls POST to `{base_url}/auth/sso/api/{check_code,check_user_ticket}` and parse the `{"errno":0,"data":{...}}` envelope; non-zero `errno` is returned as an error.

The `callback_url` is **not** passed in the login URL. It must be pre-registered in UPM for the given `app_id`. The value in config must exactly match the UPM registration.

`handlers.go` routes:

```text
GET /api/auth/sso/login      → redirect to LoginURL(?redirect= forwarded as jumpto)
GET /api/auth/sso/callback   → CheckCode → CheckUserTicket → FindOrCreate → app JWT → redirect
```

Post-login redirect uses URL fragment to avoid token appearing in server logs:
```
{frontend_url}/login/sso#access_token=<jwt>
```

---

## Step 9: Runtime config endpoint (`/api/runtime-config`)

Optional but recommended: tells the frontend which login modes are active so it can show/hide buttons.

```go
GET /api/runtime-config
→ {
    "loginMode": "all|password|oidc|sso|oidc+sso|...|none",
    "devLogin":  true,   // only when MySQL is configured but no SSO/OIDC
    "oidcLoginText": "...",
    "ssoLoginText":  "..."
  }
```

`devLogin: true` enables `GET /api/auth/dev/login?username=<name>` — a bypass endpoint registered only when no external IdP is configured. Safe in production because it requires SSO/OIDC absence.

Driven by config values (no auth required on this endpoint).

---

## Step 10: Route registration (`internal/api/router.go`)

```go
func NewRouter(deps RouterDeps) http.Handler {
    r := gin.New()
    r.Use(gin.Recovery())
    r.Use(corsMiddleware(deps.CORSOrigin))

    // ── Public auth routes ──────────────────────────────────────────
    if deps.OIDCEnabled() {
        oidcH := oidc.NewHandlers(deps.OIDCService, deps.DB, deps.Cfg.Auth, deps.Redis)
        r.GET("/api/auth/oidc/login", oidcH.Login)
        r.GET("/api/auth/oidc/callback", oidcH.Callback)
        r.POST("/api/auth/oidc/cli-login", oidcH.CLILogin)
        r.GET("/api/auth/oidc/cli-callback", oidcH.CLICallback)
        r.GET("/api/auth/oidc/cli-poll", oidcH.CLIPoll)
    }
    if deps.SSOEnabled() {
        ssoH := sso.NewHandlers(deps.SSOService, deps.DB, deps.Cfg.Auth)
        r.GET("/api/auth/sso/login", ssoH.Login)
        r.GET("/api/auth/sso/callback", ssoH.Callback)
    }
    r.GET("/api/runtime-config", runtimeConfigHandler(deps.Cfg))

    // ── Protected task routes ────────────────────────────────────────
    protected := r.Group("/api/tasks")
    protected.Use(auth.BearerAuth(deps.Cfg.Auth.SecretKey, deps.DB))
    {
        protected.POST("", taskH.CreateTask)
        protected.POST("/:id/messages", taskH.SendMessage)
        protected.GET("/:id", taskH.GetTask)
        protected.GET("/:id/history", taskH.GetTaskHistory)
        protected.DELETE("/:id", taskH.DeleteTask)
    }

    r.GET("/health", taskH.Health)
    return r
}
```

`OIDCEnabled()` / `SSOEnabled()` simply check whether the relevant config fields are non-empty.

---

## Step 11: `main.go` wiring

MySQL is **required** — the server refuses to start without it:

```go
// Startup validation
if cfg.MySQL.DSN == ""           { log.Fatalf("mysql.dsn is required") }
if cfg.Auth.SecretKey == ""      { log.Fatalf("auth.secret_key must be set") }
if cfg.Auth.TokenTTLSeconds <= 0 { log.Fatalf("auth.token_ttl_seconds must be > 0") }

// OIDC-specific validation
if cfg.OIDC.ClientID != "" && cfg.OIDC.DiscoveryURL != "" {
    if cfg.Auth.OIDCStateSecret == "" { log.Fatalf("auth.oidc_state_secret must be set when OIDC is enabled") }
    if cfg.Auth.StateTTLSeconds <= 0  { log.Fatalf("auth.state_ttl_seconds must be > 0 when OIDC is enabled") }
}

gormDB, err := db.Open(cfg.MySQL.DSN)

// Build OIDC / SSO services (nil-safe — routes only registered when non-nil)
var oidcSvc *oidcsvc.Service
if cfg.OIDC.ClientID != "" && cfg.OIDC.DiscoveryURL != "" {
    oidcSvc = oidcsvc.New(cfg.OIDC)
}
var ssoSvc *ssosvc.Service
if cfg.SSO.AppID != "" {
    ssoSvc = ssosvc.New(cfg.SSO)
}

router := api.NewRouter(api.RouterDeps{
    Store:       repo,
    Manager:     mgr,
    CORSOrigin:  cfg.Server.CORSOrigin,
    FileStore:   ofsClient,
    DB:          gormDB,
    Redis:       rdb,
    Cfg:         cfg,
    OIDCService: oidcSvc,
    SSOService:  ssoSvc,
})
```

---

## Step 12: Frontend integration

1. **Routing** (`react-router-dom`): three routes — `/login`, `/login/sso`, `/login/oidc`. The root `/` is wrapped in `<ProtectedRoute>` which checks for a valid JWT in `localStorage`; redirects to `/login` if absent or expired.

2. **Callback pages** (`/login/sso`, `/login/oidc`): both use the same `SSOCallbackPage` component. The token is passed via **URL fragment** (`#access_token=<jwt>`), not a query parameter, so it is never sent to the server or recorded in logs. Read with `useLocation().hash`.

3. **LoginPage**: fetches `/api/runtime-config` to determine which buttons to show:
   - `loginMode.includes('sso')` → "Login with SSO" button → navigates to `GET /api/auth/sso/login`
   - `loginMode.includes('oidc')` → "Login with OIDC" button → navigates to `GET /api/auth/oidc/login`
   - `devLogin: true` → username text input → navigates to `GET /api/auth/dev/login?username=<name>`

4. **API client**: attach `Authorization: Bearer <token>` header to all `/api/tasks` requests.

5. **CORS**: the existing CORS middleware allows `Authorization` header:
   ```go
   c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
   ```
   The SSO/OIDC callback is a browser redirect (not XHR) so no special CORS treatment is needed for the callback path.

---

## Security mapping

| Threat | Mitigation |
| --- | --- |
| CSRF | state encoded as short-TTL HS256 JWT with independent `oidc_state_secret` |
| Replay attack | nonce passed to IdP via `oidc.Nonce()`, embedded in id_token, re-validated in `VerifyIDToken` |
| Algorithm confusion | `ParseWithClaims` keyfunc asserts `*jwt.SigningMethodHMAC`; rejects `alg:none` and RS256 tokens |
| JWKS rotation | `go-oidc` re-fetches on unknown key ID automatically |
| SSO code replay | 30s TTL one-time code enforced by Didi SSO service |
| Token in logs | post-auth redirect uses URL fragment (`#access_token=`), never sent to server |
| Password exposure | SSO/OIDC/dev users get `bcrypt(randomUUID)` password hash |
| Log leakage | never log raw tokens; log only `userID` or `userName` |

---

## File change summary

| File | Change |
| --- | --- |
| `go.mod` | Add gorm, go-oidc, golang-jwt, bcrypt |
| `pkg/config/config.go` | Add MySQL, Auth, OIDC, SSO config structs |
| `config.example.yaml` | Add corresponding YAML blocks |
| `internal/db/mysql.go` | New — GORM open + AutoMigrate |
| `internal/db/user.go` | New — User model + FindOrCreate + HashAPIKey |
| `internal/auth/token.go` | New — CreateToken / VerifyToken |
| `internal/auth/middleware.go` | New — BearerAuth Gin middleware |
| `internal/auth/context.go` | New — set/get User in gin.Context |
| `internal/oidc/service.go` | New — go-oidc wrapper |
| `internal/oidc/handlers.go` | New — login, callback, cli-login, cli-callback, cli-poll |
| `internal/sso/service.go` | New — Didi SSO HTTP client |
| `internal/sso/handlers.go` | New — login, callback |
| `internal/api/router.go` | Extend — auth routes + protected group |
| `internal/api/handlers.go` | Minimal — read username from auth context instead of request body |
| `cmd/server/main.go` | Extend — wire DB, OIDC/SSO services |

---

## DiDi SSO operational notes

### API documentation

The canonical SSO docs have moved from the internal wiki to the **"SSO知多少"** Cooper knowledge base:
[`cooper.didichuxing.com/knowledge/share/book/dsyVWVrcu5CY`](https://cooper.didichuxing.com/knowledge/share/book/dsyVWVrcu5CY)

The wiki links referenced during planning (`wiki.intra.xiaojukeji.com/pages/viewpage.action?pageId=...`) are legacy — use the Cooper book for the current API reference. Recommended reading order:

1. `【01】SSO实现图解&接入说明` — architecture + integration overview
2. `【03】SSO实现案例` — concrete examples
3. `【02】SSO接口文档` — API reference
4. `【04】系统接入SSO知识库FAQ`

### Load testing requirement

Before any load test or traffic spike against the SSO endpoints:

- Email **iam@didichuxing.com** at least **1 business day in advance**
- DC-notify **lvchao** or **clementzhang**, CC your direct manager
- Include in the email: exact start/end time, target QPS, which SSO interfaces are exercised, business contact

---

## Decisions

| # | Question | Decision |
| --- | --- | --- |
| 1 | MySQL required in dev? | **Yes, always required.** Removed optional/dev-mode fallback. Use `GET /api/auth/dev/login` for local dev without SSO. |
| 2 | Gin migration prerequisite? | Confirmed — Gin is already in use; proceed with `gin.HandlerFunc` middleware as written. |
| 3 | Password-based login? | Yes — `/api/auth/login` (username + password) is registered whenever MySQL is configured. |
| 4 | API key provisioning? | Out of scope here — track in a follow-up plan. |
| 5 | Default resources on new user? | No — redirect to homepage on first login; no default task resources needed. |
| 6 | SSO login URL pattern? | Direct redirect to `{base_url}/auth/sso/login?app_id=...&jumpto=...&version=1.0`. Removed the double-redirect pattern initially planned. `callback_url` is not passed in the URL — it must be pre-registered in UPM. |
| 7 | Token delivery to frontend? | URL fragment (`#access_token=`). Avoids token appearing in server logs, browser history, or Referer headers. |
| 8 | Dev login auth source? | `AuthSourceDev` (distinct from `AuthSourceSSO`). Keeps audit logs meaningful in environments where SSO is later enabled. |
