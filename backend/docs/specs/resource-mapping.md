# Resource Mapping: Task, Sandbox, Session

## Overview

Three distinct resources form the execution model. Their creation times differ, and their lifetimes are independent.

| Resource | ID field | Created at | Lifetime |
|---|---|---|---|
| **Task** | `task_id` | `POST /api/tasks` | Permanent — the stable top-level entity |
| **Sandbox** | `sandbox_id` | Sandbox provisioning | Ephemeral — can be paused, destroyed, and recreated |
| **Session** | `session_id` | First user message to claude-agent-server | Durable via OFS — survives sandbox destruction |

---

## Relationships

```
Task (1)
 ├── Sandbox (0..N over time)   ← ephemeral execution environment
 └── Session (0..1)             ← lazy: created on first user message
```

- A task owns at most **one session** at a time. The session is the stable conversation identity.
- A task may be served by **many sandboxes** over its lifetime as sandboxes are paused or destroyed and recreated.
- A sandbox runs at most **one session** at a time, but the session belongs to the task, not to the sandbox.

---

## Lifecycle

### Task
```
POST /api/tasks
  → task_id assigned
  → OFS namespace {task_id}/ created at sandbox provisioning
  → session_id: null until first user message
  → current_sandbox_id: null until sandbox provisioned
```

### Sandbox
```
Provisioned for task
  → sandbox_id assigned, task_id injected as env var TASK_ID
  → OFS volume mounted at {task_id}/ inside container
  → agent server (claude-agent-server) starts inside sandbox

Can be: paused → destroyed → recreated
  → new sandbox_id on each creation
  → same task_id and same OFS mount: conversation state survives
```

### Session
```
User sends first message to POST /sessions on claude-agent-server
  → session_id assigned (agent UUID, emitted in SSE session.init)
  → backend writes session_id onto Task record (persisted permanently)
  → agent server writes via S3 API:
      {username}/.claude/sessions/{pid}.json          ← process record (includes session_id)
      {username}/history/{encoded_cwd}/{session_id}/part-*.ndjson  ← conversation parts

Subsequent messages: POST /sessions/{session_id}/messages

Sandbox destroyed:
  → session process ends, history parts remain in OFS under {username}/history/
  → task.session_id is NOT cleared — history is still readable via OFS S3 API

New sandbox provisioned for same task:
  → agent server resumes from OFS history (S3 parts)
  → session_id on the task record remains valid for OFS history lookup
  → backend updates task.session_id only if the agent server issues a new one
```

---

## Identity Resolution

### Finding the session for a task

The backend discovers the current session by reading OFS via S3:

```go
// 1. Read the process record to get session_id
meta, err := fileStore.GetSessionMeta(ctx, t.Username, t.ID)
// meta.SessionID is the agent session_id; meta.CWD == "/workspace/{username}/{task_id}"

// 2. Or list history parts to get session prefixes
keys, err := fileStore.ListHistory(ctx, t.Username, t.ID)
// keys[0] = "{username}/history/-workspace-{username}-{task_id}/{session_id}/"
```

`session_id` is **null until the first user message** arrives at the agent server. Once set, it is kept on the Task record permanently — no sandbox is required to read session history via OFS.

```go
// When no sandbox is active, read history directly:
keys, err := fileStore.ListHistory(ctx, t.Username, t.ID)
entries, err := fileStore.GetHistory(ctx, keys[0])
```

### Key field mapping

| Backend field | Source | Persistence |
|---|---|---|
| `task_id` | Backend UUID (`POST /api/tasks`) | Permanent; injected into sandbox as `TASK_ID`; OFS workspace subpath; never changes |
| `sandbox_id` | OpenSandbox container ID | Transient; null when no sandbox; changes on recreate |
| `session_id` | SSE `session.init`.sessionId | Null until first message; **never cleared once set** |
| OFS history | Agent server writes via S3 API | Under `{username}/history/{encoded_cwd}/{session_id}/`; survives sandbox destruction |

---

## Invariants

1. `task_id` is the **only stable identifier** across sandbox recreations.
2. `sandbox_id` is transient — the backend must not use it as a durable session key.
3. `session_id` is null at task creation time; the backend must handle this state.
4. `session_id` is **never cleared** once written to the Task record. A task with no active sandbox but a set `session_id` can still serve history reads from OFS.
5. OFS data under `{username}/history/` and `{username}/.claude/` outlives any individual sandbox or session process.

---

## State Table

| Task state | sandbox_id | session_id | Description |
|---|---|---|---|
| `pending` | null | null | Task created, no sandbox yet |
| `provisioning` | assigned | null | Sandbox starting, agent server not yet ready |
| `idle` | assigned | null | Sandbox running, no message sent yet |
| `active` | assigned | assigned | Session live, agent processing messages |
| `paused` | null | retained | Sandbox destroyed; session history readable from OFS via session_id |
| `resuming` | new id | retained | New sandbox starting; session_id unchanged until agent server confirms |

---

## Relationship to OFS

The OFS spec (`ofsspec.md`) describes the file layout. The mapping here determines how to reach it:

- OFS S3 namespace for a user: `{username}/`
- Session history parts: `{username}/history/-workspace-{username}-{task_id}/{session_id}/part-*.ndjson`
- Session process record: `{username}/.claude/sessions/{pid}.json`
- Agent workspace (FUSE only, not S3-accessible from backend): OFS subpath `{username}/workspaces/{task_id}` → `/workspace/{username}/{task_id}/` inside container

The `username` + `task_id` pair is the join key between the backend's task record, the sandbox FUSE workspace, and the OFS S3 session history.
