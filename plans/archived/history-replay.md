# History Replay Plan

> **Status: implemented** (approach changed from original plan — see "What was actually built" below).

## What was actually built

The final implementation took a different approach from the original plan below. Instead of a backend
`ParseHistory` transformer, the full raw JSON is passed through and the frontend does the conversion:

**Backend**
- `storage.Client.GetHistory` returns `[]json.RawMessage` (verbatim NDJSON, only `isMeta:true` lines filtered).
- `GET /api/tasks/:id/history` returns the raw JSON array unchanged.
- `GET /api/tasks` (new) lists tasks for the authenticated user, returning `[{id, title, state, created_at, updated_at}]`.

**Frontend**
- `@anthropic-ai/claude-agent-sdk` added as a devDependency; its exported SDK types (`SDKAssistantMessage`, `SDKUserMessage`, etc.) are used directly via the `DiskEnvelope` intersection pattern in `src/types/session.ts`.
- `src/lib/chainBuilder.ts` — `buildMessages(SessionEntry[]): Message[]` converts history entries to the same `Message` type the live chat uses.
- `src/components/HistorySidepanel.tsx` — left sidebar listing tasks with titles and dates.
- `src/pages/ChatPage.tsx` — two-column layout; selecting a task loads its history via `getHistory` → `buildMessages` → `useChat.loadTask`, making it immediately resumable.
- `useChat` gains `taskId`, `loadTask(id, messages)`, and `newChat()`.

---

## Original plan (superseded)

### Goal

`GET /api/tasks/:id/history` currently returns raw `[]storage.ConversationEntry` where
`Message` is `json.RawMessage`. The frontend cannot display this directly — it needs the same
`Message[]` shape that the live SSE stream produces. This plan defines the typed structs,
the transformation logic, and the frontend changes needed.

---

## 1. JSONL entry types (backend)

The current `ConversationEntry` uses `json.RawMessage` for `Message`. We need concrete Go
types for every field that matters for replay. The existing reference notes are preserved at
the bottom of this file.

### 1a. Content blocks

```go
// backend/internal/api/history_types.go

type ContentBlock struct {
    Type       string          `json:"type"`
    // text
    Text       string          `json:"text,omitempty"`
    // thinking
    Thinking   string          `json:"thinking,omitempty"`
    Signature  string          `json:"signature,omitempty"`
    // tool_use / server_tool_use
    ID         string          `json:"id,omitempty"`
    Name       string          `json:"name,omitempty"`
    Input      json.RawMessage `json:"input,omitempty"`
    // tool_result
    ToolUseID  string          `json:"tool_use_id,omitempty"`
    Content    json.RawMessage `json:"content,omitempty"`
    IsError    bool            `json:"is_error,omitempty"`
}
```

### 1b. Message payloads

```go
type UserMessagePayload struct {
    Content json.RawMessage `json:"content"` // string or []ContentBlock
}

type AssistantMessagePayload struct {
    ID         string         `json:"id"`
    Model      string         `json:"model"`
    StopReason string         `json:"stop_reason"`
    Usage      struct {
        InputTokens  int `json:"input_tokens"`
        OutputTokens int `json:"output_tokens"`
    } `json:"usage"`
    Content []ContentBlock `json:"content"`
}
```

### 1c. Top-level JSONL entry (typed)

Extend `storage.ConversationEntry` (or define a parallel type in the API layer):

```go
type JSONLEntry struct {
    Type              string          `json:"type"`               // user | assistant | system | result | …
    Subtype           string          `json:"subtype,omitempty"`  // for system entries
    UUID              string          `json:"uuid,omitempty"`
    ParentUUID        string          `json:"parentUuid,omitempty"`
    Timestamp         string          `json:"timestamp,omitempty"`
    IsMeta            bool            `json:"isMeta,omitempty"`
    SessionID         string          `json:"session_id,omitempty"`
    ParentToolUseID   string          `json:"parent_tool_use_id,omitempty"`

    // user / assistant
    Message json.RawMessage `json:"message,omitempty"`

    // system subtypes
    TaskID      string `json:"task_id,omitempty"`
    Description string `json:"description,omitempty"`
    LastToolName string `json:"last_tool_name,omitempty"`
    Status      string `json:"status,omitempty"` // for task_notification: completed | failed | stopped
    Summary     string `json:"summary,omitempty"`

    // result
    Subtype2      string  `json:"subtype,omitempty"` // success | error_max_turns | …  (same field as system Subtype)
    IsError       bool    `json:"is_error,omitempty"`
    TotalCostUSD  float64 `json:"total_cost_usd,omitempty"`
}
```

> **Note:** `storage.ConversationEntry.Message` stays `json.RawMessage`; the typed parsing
> happens entirely in the API layer. No changes needed to the storage package.

---

## 2. Output type — HistoryMessage

The history endpoint returns a slice of these instead of raw entries. This mirrors the
frontend `Message` type closely so the component layer needs no changes.

```go
// backend/internal/api/history_types.go

type HistoryToolUseBlock struct {
    ID    string          `json:"id"`
    Name  string          `json:"name"`
    Input json.RawMessage `json:"input"`
}

type HistoryToolActivity struct {
    Description string `json:"description"`
    ToolName    string `json:"toolName,omitempty"`
    Done        bool   `json:"done"`
}

type HistoryMessage struct {
    Role         string                `json:"role"`   // "user" | "assistant"
    Text         string                `json:"text"`
    Status       string                `json:"status"` // always "done"
    ToolUseBlocks []HistoryToolUseBlock `json:"toolUseBlocks,omitempty"`
    ToolActivity  []HistoryToolActivity `json:"toolActivity,omitempty"`
}
```

---

## 3. Transformation algorithm

Implemented as `ParseHistory(entries []storage.ConversationEntry) []HistoryMessage` in
`backend/internal/api/history.go`.

```
pending = nil  // current assistant HistoryMessage being accumulated

for each entry:
  skip: isMeta=true, type=stream_event, type=rate_limit_event

  type == "user":
    parse UserMessagePayload from entry.Message
    if content is a plain string OR contains at least one "text" block:
      flush pending assistant message → append to output
      append user HistoryMessage{role:"user", text: …, status:"done"}
    if content contains only "tool_result" blocks:
      skip (internal tool feedback, not user-visible)

  type == "assistant":
    if pending == nil: pending = &HistoryMessage{role:"assistant", status:"done"}
    parse AssistantMessagePayload from entry.Message
    for each ContentBlock:
      "text"     → pending.Text += block.Text
      "tool_use" → append HistoryToolUseBlock to pending.ToolUseBlocks

  type == "system":
    if pending == nil: pending = &HistoryMessage{role:"assistant", status:"done"}
    subtype == "task_started":
      append HistoryToolActivity{Description: entry.Description, Done: false}
    subtype == "task_progress":
      if len(pending.ToolActivity) > 0:
        last activity.Description = entry.Description
        last activity.ToolName   = entry.LastToolName
    subtype == "task_notification" where entry.Status == "completed"|"failed"|"stopped":
      mark all pending.ToolActivity as Done=true

  type == "result":
    if pending != nil:
      mark all pending.ToolActivity as Done=true
      flush pending → append to output
      pending = nil

end of loop: flush pending if non-nil
```

---

## 4. Backend file layout

| File | Change |
|---|---|
| `backend/internal/api/history_types.go` | New — `JSONLEntry`, `ContentBlock`, payload types, `HistoryMessage`, `HistoryToolUseBlock`, `HistoryToolActivity` |
| `backend/internal/api/history.go` | New — `ParseHistory([]storage.ConversationEntry) []HistoryMessage` |
| `backend/internal/api/history_test.go` | New — unit tests with fixture JSONL strings |
| `backend/internal/api/handlers.go` | Update `GetTaskHistory`: call `ParseHistory(entries)` before `c.JSON(…)` |
| `backend/docs/` | Regenerate swagger after handler signature changes |

The swagger `@Success` annotation for `GetTaskHistory` changes from
`{array} storage.ConversationEntry` → `{array} api.HistoryMessage`.

---

## 5. Frontend changes

### 5a. `frontend/src/types.ts`

Add `HistoryMessage` (or reuse `Message` with `status: "done"` — structurally compatible):

```ts
export interface HistoryToolUseBlock {
  id: string
  name: string
  input: Record<string, unknown>
}

export interface HistoryToolActivity {
  description: string
  toolName?: string
  done: boolean
}

export interface HistoryMessage {
  role: Role
  text: string
  status: 'done'
  toolUseBlocks?: HistoryToolUseBlock[]
  toolActivity?: HistoryToolActivity[]
}
```

### 5b. `frontend/src/api/client.ts`

```ts
export async function getTaskHistory(taskId: string): Promise<HistoryMessage[]> {
  const res = await fetch(`${BASE}/api/tasks/${taskId}/history`, {
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to fetch history')
  return res.json() as Promise<HistoryMessage[]>
}
```

### 5c. `frontend/src/hooks/useChat.ts`

Add a `loadHistory(taskId: string)` path that:
1. Calls `getTaskHistory(taskId)`.
2. Maps `HistoryMessage[]` → `Message[]` (add generated `id` fields, keep `status: "done"`).
3. Sets messages state.
4. Sets `taskId` state so subsequent sends continue in the same task.

### 5d. UI entry point (to be decided separately)

History replay requires a way to pick a previous task. Options:
- **Task list page/sidebar** — lists past tasks; clicking one calls `loadHistory`.
- **URL param** — `?taskId=<id>` on the ChatPage triggers `loadHistory` on mount.

The URL-param approach is simpler and fits the current single-page layout. Recommended as
first iteration.

---

## 6. Open questions

1. **Subagent transcripts**: `ListHistory` currently returns only the root `.jsonl` (no
   subagent files). Should subagent turns appear in history? If yes, we need to fetch and
   merge `subagents/*.jsonl` sidechains, inserting them at the `task_notification` point.
   Defer to v2 unless required.

2. **Multiple JSONL files per task**: `ListHistory` can return >1 key. The handler uses
   `keys[0]`. Should we merge all? Likely yes — iterate all keys in order and concatenate
   entries before calling `ParseHistory`. Confirm with team.

3. **`tool_result` visibility**: Currently skipped (internal). Do we want to show tool
   output (e.g. file contents read by the agent)? If yes, the `HistoryMessage` type needs a
   `toolResults` field and `ChatMessage` component needs a new render path.

---

## 7. Sequence (implementation order)

1. `history_types.go` — define all types
2. `history.go` — implement `ParseHistory` with unit tests
3. Update `handlers.go` to call `ParseHistory`
4. Frontend `types.ts` + `api/client.ts`
5. `useChat.ts` — `loadHistory` path
6. URL-param entry point in `ChatPage.tsx`
7. Swagger regen

---

## Reference: JSONL format

<details>
<summary>Entry type table and field reference (from SDK docs)</summary>

Each line in a .jsonl file is one JSON object. The top-level "type" field is the discriminator.

| "type"             | SDK type                | Purpose                                                   |
|--------------------|-------------------------|-----------------------------------------------------------|
| "user"             | UserMessage             | Human turn or tool result                                 |
| "assistant"        | AssistantMessage        | Model turn                                                |
| "system"           | SystemMessage + subtypes| Agent task lifecycle, hook events                         |
| "result"           | ResultMessage           | Turn-completion summary (cost, usage, stop reason)        |
| "stream_event"     | StreamEvent             | Raw Anthropic API stream delta — skip for history replay  |
| "rate_limit_event" | RateLimitEvent          | Rate limit state — skip for history replay                |

Entries with unrecognized "type" are silently skipped (forward-compatibility).

**System subtypes:**

| "subtype"                        | Key fields                                                                               |
|----------------------------------|------------------------------------------------------------------------------------------|
| "task_started"                   | task_id, description, uuid, session_id, tool_use_id?, task_type?                         |
| "task_progress"                  | task_id, description, usage, uuid, session_id, last_tool_name?                           |
| "task_notification"              | task_id, status ("completed"/"failed"/"stopped"), summary, output_file, usage?           |
| "hook_started" / "hook_response" | subtype, hook_event_name, data                                                           |

**What is NOT in the JSONL:**
- transcript_mirror frames
- agent_metadata entries (stored in .meta.json sidecars)
- "mirror_error" system messages (synthesized by SDK at runtime)

</details>
