# OrangeFS Session File Storage Specification

## Overview

The sandbox platform mounts an OrangeFS (OFS) volume — an S3-compatible cloud file storage — inside each sandbox container. The Claude Code agent running in the sandbox writes its session and conversation data to this volume, making history durable across ephemeral sandbox restarts.

**Key identity mapping:**

| Identifier | Source | Role |
|---|---|---|
| `task_id` | Backend UUID (assigned at `POST /api/tasks`) | Top-level OFS namespace key |
| `username` | Request body of `POST /api/tasks` | Owner; determines working directory path |
| `sandbox_id` | OpenSandbox container ID | Ephemeral — can be destroyed and recreated |
| Claude Code `sessionId` | Emitted in SSE `session.init` event | Internal to Claude Code; lives *inside* the OFS namespace |

The OFS mount is set up at **sandbox creation time**, before any Claude Code session exists. The `task_id` is therefore the stable top-level key — not the Claude Code session ID, which is only created later when the agent starts.

---

## OFS Configuration

Credentials live in `config.yaml` under `orangefs` and are loaded into `OrangeFSConfig` (`pkg/config/config.go`):

```yaml
orangefs:
  addr: "10.88.151.122:8030"                   # injected into sandbox as ORANGEFS_RS_ADDR
  endpoint: "https://s3-yspu.didistatic.com"   # public S3 endpoint for backend client
  volume: "ofs-llab-lflow"                     # S3 bucket name
  access_key: "..."
  secret_key: "..."
```

Two separate addresses:

| Config field | Purpose |
|---|---|
| `addr` | Internal resource server — injected into sandbox as `ORANGEFS_RS_ADDR` so the agent can mount the volume |
| `endpoint` | Public S3 endpoint — used by the backend `storage.Client` to read history over the S3 API |

The backend never mounts OFS itself; mounting is handled by the agent inside the sandbox.

---

## Environment Variables Injected at Sandbox Creation

`manager.ProvisionForTask` (`internal/sandbox/manager.go`) injects these into every sandbox:

| Variable | Value | Purpose |
|---|---|---|
| `ORANGEFS_RS_ADDR` | `orangefs.addr` | OFS resource server for in-container mount |
| `ORANGEFS_VOLUME` | `orangefs.volume` | OFS bucket/volume to mount |
| `USERNAME` | `task.Username` | Used by entrypoint to set working directory |
| `TASK_ID` | `task.ID` | Used by entrypoint to set working directory |

The entrypoint script sets the CWD to:
```
/workspace/{SANDBOX_USER}/{TASK_ID}/
```

---

## File Storage Layout

Claude Code writes files under `~/.claude/` relative to the OFS mount root. The mount root is namespaced by `task_id`:

```
{task_id}/                                  # top-level S3 key prefix
├── sessions/
│   └── {pid}.json                                  # Claude Code process record
├── projects/
│   └── {cwd-encoded}/                              # CWD path with '/' replaced by '-'
│       ├── {uuid}.jsonl                            # conversation message history (JSONL)
│       ├── {uuid}/
│       │   └── subagents/
│       │       ├── agent-{id}.jsonl                # subagent conversation transcript
│       │       └── agent-{id}.meta.json            # subagent type + description
│       └── memory/                                 # project-scoped memory files
├── session-env/
│   └── {session-uuid}/                             # per-session shell environment snapshot
├── file-history/
│   └── {session-uuid}/                             # per-session tracked file backups
├── transcripts/
│   └── ses_{id}.jsonl                              # session-level transcript
├── history.jsonl                                   # global command history
└── settings.json                                   # global configuration
```

### Working Directory Encoding

CWD is `/workspace/{username}/{task_id}/`. The encoded path segment — each `/` replaced by `-`, leading `/` included, trailing `/` dropped:

```
-workspace-{username}-{task_id}
```

### Task JSONL Key

Because each sandbox runs exactly one task, there is exactly one `.jsonl` file directly under the `projects/` prefix. Its full S3 key is:

```
{task_id}/projects/-workspace-{username}-{task_id}/{uuid}.jsonl
```

The `{uuid}` is Claude Code's internal JSONL file identifier, distinct from both `task_id` and the Claude Code `sessionId`. Use `ListHistory` to discover it.

---

## File Formats

### `sessions/{pid}.json`

One JSON object per file. Records process metadata for the Claude Code process.

```json
{
  "pid": 62137,
  "sessionId": "86613a9f-11bf-4a23-b32a-6c5cf76d2cee",
  "cwd": "/workspace/alice/conv-uuid-456",
  "startedAt": 1778307176338,
  "procStart": "Sat May  9 06:12:55 2026",
  "version": "2.1.119",
  "peerProtocol": 1,
  "kind": "interactive",
  "entrypoint": "cli",
  "status": "busy",
  "updatedAt": 1778315027087
}
```

### `projects/{cwd-encoded}/{uuid}.jsonl`

JSONL — one JSON object per line. Entry types:

| `type` | Description |
|--------|-------------|
| `user` | User turn: `message.role = "user"`, `uuid`, `parentUuid`, `timestamp` |
| `assistant` | Assistant turn: `message.model`, `message.content[]` (text / thinking / tool blocks) |
| `system` | System prompt snapshot |
| `attachment` | File attachment metadata |
| `file-history-snapshot` | Tracked file state snapshot at a given message |
| `last-prompt` | Most recent raw user prompt (used for resume) |

Sample `user` entry:

```json
{
  "type": "user",
  "uuid": "0ab269f4-1046-4981-b3c7-010ac62f81d0",
  "parentUuid": null,
  "isSidechain": false,
  "isMeta": false,
  "timestamp": "2026-05-09T06:12:42.697Z",
  "message": {
    "role": "user",
    "content": "Hello"
  }
}
```

Sample `assistant` entry:

```json
{
  "type": "assistant",
  "uuid": "d8e34a11-...",
  "parentUuid": "0ab269f4-...",
  "timestamp": "2026-05-09T06:12:45.000Z",
  "message": {
    "id": "msg_bdrk_01V48...",
    "model": "claude-sonnet-4-6",
    "role": "assistant",
    "type": "message",
    "content": [
      { "type": "text", "text": "Hello! How can I help?" }
    ]
  }
}
```

Entries with `isMeta: true` are internal bookkeeping and are excluded by the storage client.

---

## Storage Client

Implemented in `internal/storage/client.go` using `aws-sdk-go-v2`. Initialised in `main.go` from `orangefs.endpoint/access_key/secret_key`.

### Interface

```go
type OFSClient interface {
    // ListHistory returns top-level .jsonl keys under
    // "<taskID>/projects/". Subagent transcripts are excluded.
    ListHistory(ctx context.Context, taskID string) ([]string, error)

    // GetHistory downloads and parses a Claude Code JSONL history file by its S3 key.
    GetHistory(ctx context.Context, key string) ([]ConversationEntry, error)

    // GetSessionMeta returns the Claude Code process record for the given task.
    GetSessionMeta(ctx context.Context, taskID string) (*SessionMeta, error)
}
```

### Retrieving History for a Task

```go
// t.ID is the backend Task UUID = top-level OFS key prefix
keys, err := fileStore.ListHistory(ctx, t.ID)
if err != nil || len(keys) == 0 {
    // OFS not configured, sandbox not yet started, or no history written
}

entries, err := fileStore.GetHistory(ctx, keys[0])
```

### Connectivity Test

```
go test ./internal/storage/ -v -run TestConnection
```

---

## S3 Key Reference

| Object | S3 Key |
|--------|--------|
| Claude Code process record | `{task_id}/sessions/{pid}.json` |
| Task history | `{task_id}/projects/-workspace-{username}-{task_id}/{uuid}.jsonl` |
| Subagent transcript | `{task_id}/projects/-workspace-{username}-{task_id}/{uuid}/subagents/agent-{id}.jsonl` |
| Subagent metadata | `{task_id}/projects/-workspace-{username}-{task_id}/{uuid}/subagents/agent-{id}.meta.json` |
| Session transcript | `{task_id}/transcripts/ses_{id}.jsonl` |
| Global history | `{task_id}/history.jsonl` |
| Memory files | `{task_id}/projects/-workspace-{username}-{task_id}/memory/{file}` |

`{task_id}` = backend `Task.ID` (UUID assigned at `POST /api/tasks`).
`{uuid}` = Claude Code's internal JSONL file identifier (discovered via `ListHistory`).
