# SSH Key Management

Allow users to store a private SSH key in their account so the platform can clone private git repositories on their behalf.

## Goals

- User can paste/save a private SSH key via the Settings UI
- Key is stored encrypted in MySQL (not plaintext)
- Key is injected into the sandbox container at provision time so `git clone` works
- Minimal footprint: no new tables, add a column to `users`

---

## Backend

### 1. DB — `users` table

Add one column:

```sql
ALTER TABLE users ADD COLUMN ssh_private_key_enc TEXT DEFAULT NULL;
```

- Value is AES-GCM encrypted at the application layer before write, decrypted on read.
- Encryption key comes from `config.yaml` → `security.ssh_key_secret` (32-byte hex).
- GORM model: add `SSHPrivateKeyEnc string \`gorm:"column:ssh_private_key_enc"\`` to `db.User`.

### 2. Crypto helper — `internal/crypto/`

New file `aes.go`:

```go
func Encrypt(plaintext, keyHex string) (string, error)
func Decrypt(ciphertext, keyHex string) (string, error)
```

Standard AES-256-GCM, base64url output. No external deps.

### 3. API endpoints

Add to `internal/api/handlers.go`:

| Method | Path | Description |
|--------|------|-------------|
| `GET`  | `/api/user/settings` | Return `{ has_ssh_key: bool }` (never return key material) |
| `PUT`  | `/api/user/settings` | Body `{ ssh_private_key?: string }` — empty string clears the key |

Both routes require `AuthMiddleware`. The PUT handler:
1. Validates the key is a valid PEM block (`ssh.ParseRawPrivateKey`).
2. Encrypts with `crypto.Encrypt`.
3. Updates the user row.

Swagger annotations required (follow existing handler pattern).

### 4. Config

Add to `config.example.yaml`:

```yaml
security:
  ssh_key_secret: ""   # 32-byte hex; generate with: openssl rand -hex 32
```

Add to `pkg/config/config.go` → `Config.Security.SSHKeySecret string`.

---

## Frontend

### Settings page

New route `/settings`, linked from the sidebar user avatar / dropdown.

Component: `src/pages/SettingsPage.tsx`

```
┌─────────────────────────────────┐
│  Settings                       │
│                                 │
│  SSH Private Key                │
│  ┌───────────────────────────┐  │
│  │  -----BEGIN OPENSSH ...   │  │
│  │  (textarea, monospace)    │  │
│  └───────────────────────────┘  │
│  [Clear]              [Save]    │
│                                 │
│  Status: ● Key configured       │
└─────────────────────────────────┘
```

- On load: `GET /api/user/settings` → show "Key configured" badge or "No key" state.
- Save: PUT with the textarea content.
- Clear: PUT with `{ ssh_private_key: "" }`.
- Never display key material fetched from server (server never returns it).

New API helper in `src/api/client.ts`:
```ts
getUserSettings(): Promise<{ has_ssh_key: boolean }>
updateUserSettings(body: { ssh_private_key?: string }): Promise<void>
```

---

## Sandbox injection

After the sandbox reaches `Running` state and execd is healthy, use the execd filesystem API (already proxied at `/api/tasks/:id/execd/*path`) to write the key directly — no entrypoint changes needed.

Sequence (new helper `internal/sandbox/sshsetup.go`):

```
1. POST /directories
   body: { "/root/.ssh": { mode: 700, owner: "root", group: "root" } }

2. POST /files/upload   (multipart)
   metadata: { "path": "/root/.ssh/id_rsa", "owner": "root", "group": "root", "mode": 600 }
   file:     <raw PEM bytes>

3. POST /files/upload   (multipart)
   metadata: { "path": "/root/.ssh/config", "owner": "root", "group": "root", "mode": 600 }
   file:     "Host *\n  StrictHostKeyChecking accept-new\n"
```

Call `InjectSSHKey(taskID, pemBytes string)` from `manager.go` right after the existing health-check passes (before returning from `EnsureProvisioned`).

No changes to the OpenSandbox entrypoint or sandbox creation payload.

---

## Security notes

- Key is never logged, never returned in API responses.
- AES-256-GCM provides authenticated encryption — detects tampering.
- `ssh_key_secret` must be set in production config; server refuses to start if blank and any user has a stored key (add startup check).
- Consider a key rotation path: re-encrypt on next login if `key_version` column differs (out of scope for v1, note for later).

---

## Task breakdown

1. `internal/crypto/aes.go` — encrypt/decrypt helpers + unit tests
2. DB migration — add `ssh_private_key_enc` column, update `db.User` model
3. Config — add `Security.SSHKeySecret`, startup validation
4. API handlers — `GET/PUT /api/user/settings`, Swagger annotations
5. Sandbox injection — decrypt + inject key before provision
6. Frontend `SettingsPage` + API client helpers
7. Sidebar link to `/settings`
