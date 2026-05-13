# Resource Mapping Plan

## Goal

Allow users to create and manage their own Skills and MCP server configs. These resources
are stored in OFS, tracked in MySQL, and injected into dynamically-provisioned sandboxes
so the Claude agent running inside can discover and use them.

---

## 1. Data Model

### `kinds` table (simplified from your draft)

```sql
CREATE TABLE `kinds` (
  `id`         int          NOT NULL AUTO_INCREMENT,
  `user_id`    int          NOT NULL,
  `kind`       varchar(50)  NOT NULL,      -- "skill" | "mcp"
  `name`       varchar(100) NOT NULL,      -- slug, e.g. "my-search"
  `ofs_path`   varchar(512) NOT NULL,      -- canonical S3 key prefix in OFS
  `meta`       json         NOT NULL,      -- kind-specific config (see below)
  `is_active`  tinyint(1)   NOT NULL DEFAULT 1,
  `created_at` datetime     DEFAULT NULL,
  `updated_at` datetime     DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_kinds_user_kind_name` (`user_id`, `kind`, `name`),
  KEY `ix_kinds_user_active` (`user_id`, `is_active`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

Differences from your draft:
- `namespace` dropped — user_id + kind + name is sufficient for uniqueness.
- `ofs_path` is an explicit column rather than a JSON field; it's queried directly when
  injecting resources, so it belongs at the row level.
- `meta` (renamed from `json`) holds kind-specific config:
  - For `skill`: `{"description": "...", "trigger": "..."}` (informational, actual content is
    in OFS)
  - For `mcp`: the full MCP server config object, e.g.
    `{"type": "stdio", "command": "npx", "args": ["..."], "env": {...}}`

---

## 2. OFS Storage Layout

```
{username}/
└── resources/               ← user-managed resource files
    ├── skills/
    │   └── {name}/
    │       └── SKILL.md     ← uploaded by user or created via API
    └── mcp/
        └── {name}.json      ← MCP server config JSON (mirrors meta.mcp)
```

- Resources live under `{username}/resources/` — separate from the per-task workspace
  mount (OFS subpath `{username}/workspaces/{task_id}`).
- `ofs_path` in the `kinds` row stores the key prefix, e.g.
  `alice/resources/skills/my-search/` or `alice/resources/mcp/github.json`.

---

## 3. Resource CRUD API (new backend routes)

```
POST   /api/resources
  body: { kind: "skill"|"mcp", name: string, content: string, meta?: object }
  → 201 { id, kind, name, ofs_path }

GET    /api/resources
  → 200 [{ id, kind, name, meta, is_active, created_at }]

PUT    /api/resources/:id
  body: { content?: string, meta?: object, is_active?: bool }
  → 200 { id, kind, name }

DELETE /api/resources/:id
  → 204  (deletes OFS object + marks row deleted)
```

Auth: same `user_id` extraction used by existing task/conversation endpoints.

**Upload flow for a skill:**
1. Validate `name` matches `^[a-zA-Z0-9_-]+$`.
2. Write `content` (SKILL.md text) to OFS at `{username}/resources/skills/{name}/SKILL.md`
   via the S3 `PutObject` API (same `storage.Client` already wired in main.go).
3. Insert row into `kinds`.

**Upload flow for an MCP config:**
1. Parse `content` or `meta` as MCP server JSON.
2. Write JSON to OFS at `{username}/resources/mcp/{name}.json`.
3. Insert row into `kinds` with `meta` = the MCP server object.

No new OFS client methods needed — `PutObject` is a one-line addition alongside the
existing `GetObject` calls in `storage.Client`.

---

## 4. Sandbox Injection (the core mechanism)

### Where it happens

The injection runs **after sandbox health-check passes and before the first `POST
/sessions`** call to the agent server. In `sandbox/manager.go`,
`ProvisionForConversation` already performs the health-check as its final step; injection
is a new step inserted immediately after.

### What gets injected

For a given `user_id`:
1. Query all active rows from `kinds` (`WHERE user_id = ? AND is_active = 1`).
2. For each `skill` row:
   - Fetch `{ofs_path}SKILL.md` from OFS via S3 `GetObject`.
   - Write to sandbox via **execd file API**:
     `PUT /files/{task_cwd}/.claude/skills/{name}/SKILL.md`
3. Compose a `.mcp.json` from all active `mcp` rows:
   ```json
   { "mcpServers": { "{name}": <meta>, ... } }
   ```
4. Write `.mcp.json` to sandbox:
   `PUT /files/{task_cwd}/.mcp.json`

`task_cwd` = `/workspace/{username}/{task_id}` — same path the FUSE workspace is mounted
at, already known after provisioning.

### Why execd file writes (not pre-seeding OFS directly)

Two alternatives were considered:

**Alt A — write resource files into the task's OFS workspace path before sandbox start.**
Problem: race-prone and requires OFS write at provision time, before execd is even
reachable. Also pollutes the agent's workspace with config files.

**Alt B — second FUSE mount for `~/.claude/` (user-level).**
Would work but requires modifying `entrypoint.sh` and the sandbox image. Operationally
heavier and shares a global `~/.claude/` across all concurrent tasks for the same user.

**Chosen: execd file API writes post-health-check.**
- execd is already running (the health-check just passed).
- No image changes needed.
- Files land in `{task_cwd}/.claude/skills/` and `{task_cwd}/.mcp.json` which is exactly
  where the Claude agent SDK looks when `setting_sources` includes `"project"` and `cwd`
  is the workspace dir.
- Files persist in OFS because `task_cwd` is inside the FUSE mount.

### execd file write call

execd runs on port 44772 inside the container. The backend reaches it via the same
OpenSandbox proxy mechanism used for the agent server (port 3000):

```
PUT {serverURL}/sandboxes/{sandboxID}/proxy/44772/files/{path}
X-OPEN-SANDBOX-API-KEY: <apiKey>
Content-Type: application/octet-stream
Body: file content bytes
```

Auth is `X-OPEN-SANDBOX-API-KEY` — the server-side key (same credential used for the
lifecycle API). The OpenSandbox proxy handles forwarding the `X-EXECD-ACCESS-TOKEN` to
execd internally; the backend never needs to manage that token directly.

The `execdBaseURL` is constructed analogously to `proxyBaseURL`:

```go
execdBaseURL = fmt.Sprintf("%s/sandboxes/%s/proxy/44772", serverURL, sandboxID)
```

Add a thin helper in `sandbox/manager.go`:

```go
func (m *Manager) writeFile(ctx context.Context, sandboxID, path string,
    content []byte) error
// PUT {execdBaseURL}/files/{path} with X-OPEN-SANDBOX-API-KEY header
```

---

## 5. Agent Discovery

The Claude agent SDK inside the sandbox is already started with:
```
cwd = /workspace/{username}/{task_id}
setting_sources = ["user", "project"]
```

With the injected files in place:
- `{cwd}/.claude/skills/{name}/SKILL.md` → discovered as project Skills
- `{cwd}/.mcp.json` → MCP servers loaded from project source

No changes needed to the sandbox image or the agent server startup config.

---

## 6. Sequence Diagram

```
User → POST /api/resources (upload skill or MCP config)
  backend → S3 PutObject {username}/resources/{kind}/{name}/...
  backend → INSERT kinds row

User → POST /api/conversations/{id}/messages (first message)
  backend → [already provisioning sandbox]
  sandbox enters Running state + health check passes
  backend → SELECT kinds WHERE user_id=? AND is_active=1
  for each skill:
    backend → S3 GetObject {ofs_path}SKILL.md
    backend → execd PUT /files/{task_cwd}/.claude/skills/{name}/SKILL.md
  backend → compose .mcp.json from mcp rows
  backend → execd PUT /files/{task_cwd}/.mcp.json
  backend → POST {proxyBaseURL}/sessions  (agent session created)
  agent SDK discovers .claude/skills/ and .mcp.json from cwd
  agent → streams response back
```

---

## 7. Open Questions

1. ~~**Execd auth**~~ — resolved: use `X-OPEN-SANDBOX-API-KEY: <apiKey>` on the proxy
   request. The OpenSandbox server injects `X-EXECD-ACCESS-TOKEN` internally.

2. **Large skill bundles**: SKILL.md is plain text, but Skills can have supporting files
   (images, data). For now scope to single-file SKILL.md only; add multi-file support
   later if needed.

3. **MCP env secrets**: MCP configs may reference secrets (e.g. `GITHUB_TOKEN`).  The
   `meta` JSON can store `{"env": {"GITHUB_TOKEN": "${GITHUB_TOKEN}"}}` with the
   `${VAR}` substitution syntax that the SDK supports. The actual secret value is supplied
   via sandbox `env` (already injectable via `POST /api/conversations` body's `env` field).
   For user-stored secrets, a separate `secrets` table or Vault integration would be
   needed in a follow-up.

4. **Injection timing for follow-up messages**: If the user enables a new resource mid-
   conversation, it won't appear until the next task (next sandbox). Acceptable for v1.

5. **Cleanup**: When `DELETE /api/resources/:id`, should the resource be removed from
   already-running sandboxes?  No for v1 — only affects new sandboxes.

---

## 8. Files to Create / Modify

| File | Change |
|------|--------|
| `backend/internal/db/kinds_repository.go` | new — CRUD for `kinds` table |
| `backend/internal/storage/client.go` | add `PutObject(ctx, key string, data []byte) error` |
| `backend/internal/sandbox/manager.go` | add `injectResources` step after health-check; add `writeFile` helper |
| `backend/internal/api/handlers.go` | add resource CRUD handlers |
| `backend/internal/api/router.go` | register `/api/resources` routes |
| `backend/internal/api/types.go` | add request/response types for resources |
| `backend/pkg/config/config.go` | no change needed (OFS already configured) |
| `plans/resource-mapping.md` | this file |
| DB migration | `CREATE TABLE kinds ...` |
