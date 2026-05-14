# SSH Key Management

Users can store a private SSH key in their account. The platform encrypts the key at rest, then injects it into each sandbox at provision time so that `git clone` operations against private repositories work automatically.

---

## Architecture

```
User pastes key → PUT /api/user/settings
  → ssh.ParseRawPrivateKey validates the PEM
  → AES-256-GCM encrypt (server-side key from config)
  → UPDATE users SET ssh_private_key_enc = ?

First message → EnsureProvisioned (manager.go)
  → sandbox created and Running
  → agent server health-check passes
  → maybeInjectSSHKey: SELECT users WHERE user_name = ?
      → crypto.Decrypt(ssh_private_key_enc, config.Security.SSHKeySecret)
      → POST /sandboxes/:id/proxy/44772/directories  { "/root/.ssh": {mode:700} }
      → POST /sandboxes/:id/proxy/44772/files/upload  /root/.ssh/id_rsa  (mode 600)
      → POST /sandboxes/:id/proxy/44772/files/upload  /root/.ssh/config  (mode 600)
  → kindsRepo injection (resources)
  → task.SetRunning(...)
```

Injection failure is **non-fatal**: a `WARN` is logged and provisioning continues. The agent starts without the key rather than blocking the task entirely. Users without a stored key skip injection silently.

---

## Database

Column added to `users`:

```sql
ALTER TABLE users ADD COLUMN ssh_private_key_enc TEXT DEFAULT NULL;
```

GORM model (`internal/db/user.go`):

```go
SSHPrivateKeyEnc string `gorm:"column:ssh_private_key_enc;type:text"`
```

`AutoMigrate` in `db.Open` creates the column on first start. `""` (empty string) means no key is configured. `NULL` is never written; all clears write `""`.

---

## Encryption

**Algorithm:** AES-256-GCM  
**Implementation:** `internal/crypto/aes.go` — `Encrypt(plaintext, keyHex)` / `Decrypt(ciphertext, keyHex)`  
**Key source:** `config.Security.SSHKeySecret` — a 32-byte (64 hex-char) value from `config.yaml`  
**Wire format:** `base64url(nonce || ciphertext || GCM-tag)`, where `nonce` is 12 random bytes generated fresh per encryption call

```
plaintext  = raw PEM bytes  (e.g. "-----BEGIN OPENSSH PRIVATE KEY-----\n...")
nonce      = 12 random bytes (from crypto/rand, generated per call)
ciphertext = AES-256-GCM seal of plaintext under nonce
stored     = base64url(nonce || ciphertext || tag)  — written to ssh_private_key_enc
```

Generate a key:

```bash
openssl rand -hex 32
```

---

## Configuration

```yaml
security:
  ssh_key_secret: ""   # 32-byte hex; generate with: openssl rand -hex 32
```

**Startup check:** at server startup, if `ssh_key_secret` is blank and any user row has a non-empty `ssh_private_key_enc`, the server logs an error and exits. This prevents silently failing to decrypt keys after a misconfiguration:

```go
// cmd/server/main.go
if cfg.Security.SSHKeySecret == "" {
    var count int64
    gormDB.Model(&db.User{}).Where("ssh_private_key_enc != ''").Count(&count)
    if count > 0 {
        logger.Error("security.ssh_key_secret must be set — some users have stored SSH keys")
        os.Exit(1)
    }
}
```

When `ssh_key_secret` is blank and no users have stored keys the server starts normally (the feature is simply inactive).

---

## API Endpoints

Both routes are under the `AuthMiddleware` protected group.

### GET /api/user/settings

Returns whether the authenticated user has an SSH key stored.

```
GET /api/user/settings
Authorization: Bearer <token>

200 OK
{ "has_ssh_key": true }
```

Key material is **never returned**. `has_ssh_key` is read directly from the `db.User` loaded by `BearerAuth` middleware — no extra DB query.

### PUT /api/user/settings

Save or clear the user's SSH private key.

```
PUT /api/user/settings
Authorization: Bearer <token>
Content-Type: application/json

{ "ssh_private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\n..." }
```

The `ssh_private_key` field is **required**. Omitting it returns `400` to prevent accidentally clearing a stored key when a client sends a partial update body. To explicitly clear:

```json
{ "ssh_private_key": "" }
```

**Validation (on non-empty value):**
1. `ssh.ParseRawPrivateKey` verifies the PEM is a valid, unencrypted private key. Passphrase-protected keys are rejected (`400`) — they cannot be injected automatically.
2. AES-256-GCM encrypt with `config.Security.SSHKeySecret`.
3. `UPDATE users SET ssh_private_key_enc = ?`.

**Error responses:**

| Status | Body | Cause |
|--------|------|-------|
| `400` | `ssh_private_key is required` | Field absent from body |
| `400` | `invalid SSH private key: <reason>` | PEM parse failure / passphrase-protected |
| `500` | `SSH key encryption not configured` | `ssh_key_secret` is blank in config |
| `503` | `user settings not configured` | MySQL not wired (`DB == nil`) |

---

## Sandbox Injection

Implemented in `internal/sandbox/sshsetup.go`, called from `Manager.ProvisionForTask` after the health-check passes and before `injectResources`:

```go
// manager.go (ProvisionForTask)
if m.gormDB != nil && m.sshKeySecret != "" && t.Username != "" {
    if err := m.maybeInjectSSHKey(ctx, sandboxID, t.Username); err != nil {
        slog.WarnContext(ctx, "SSH key injection failed (continuing)", ...)
    }
}
```

`maybeInjectSSHKey` performs:

1. `SELECT * FROM users WHERE user_name = ?` — load user record
2. If `SSHPrivateKeyEnc == ""`, return immediately (no-op)
3. `crypto.Decrypt(encKey, sshKeySecret)` — AES-256-GCM decrypt to raw PEM bytes
4. Call `InjectSSHKey(ctx, sandboxID, pemBytes)`:

```
POST /sandboxes/{id}/proxy/44772/directories
Body: { "/root/.ssh": { "mode": 700 } }

POST /sandboxes/{id}/proxy/44772/files/upload   (multipart)
  metadata: { "path": "/root/.ssh/id_rsa", "mode": 600 }
  file:     <raw PEM bytes>

POST /sandboxes/{id}/proxy/44772/files/upload   (multipart)
  metadata: { "path": "/root/.ssh/config", "mode": 600 }
  file:     "Host *\n  StrictHostKeyChecking accept-new\n"
```

**SSH config note:** `StrictHostKeyChecking accept-new` means:
- First connection to a new host: key is accepted and cached
- Subsequent connections: key must match the cached value (changed-host attacks are blocked)
- Does not disable host verification the way `StrictHostKeyChecking no` would

**Wiring in main.go:**

```go
if cfg.Security.SSHKeySecret != "" {
    mgr.WithSSHKeys(gormDB, cfg.Security.SSHKeySecret)
}
```

When `WithSSHKeys` is not called, `m.gormDB == nil`, and the injection block in `ProvisionForTask` is skipped entirely.

---

## Security Properties

| Property | Mechanism |
|----------|-----------|
| Key never stored in plaintext | AES-256-GCM encrypt before every `UPDATE` |
| Key never returned in API responses | `GET /api/user/settings` returns only `{ has_ssh_key: bool }` |
| Key never logged | `maybeInjectSSHKey` logs only `sandboxID` and `username` |
| Tampering detected | GCM authentication tag — modified ciphertext fails decryption |
| Passphrase-protected keys blocked | `ssh.ParseRawPrivateKey` rejects them at save time (400) |
| Mis-config detected at startup | Server exits if stored keys exist but `ssh_key_secret` is blank |
| Host verification on first connect | `StrictHostKeyChecking accept-new` — accepts new, rejects changed |

---

## Related Documents

- [`data-management.md`](data-management.md) — `users` schema including `ssh_private_key_enc`
- [`configuration.md`](configuration.md) — `security.ssh_key_secret` config field
- [`resource-mapping.md`](resource-mapping.md) — Sandbox provision lifecycle (injection order)
