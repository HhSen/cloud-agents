# Messaging: Send, Steer, and File Attachments

## Overview

The messaging layer connects the frontend to claude-agent-server via the Go backend. It covers two distinct interaction modes and one content-enrichment mechanism:

| Feature | Endpoint | Purpose |
|---|---|---|
| **Send message** | `POST /api/tasks/:id/messages` | Start or resume a conversation; returns SSE stream |
| **Steer message** | `POST /api/tasks/:id/steer` | Inject into an already-running agent without opening a new SSE stream |
| **File attachments** | multipart body on `/messages` | Attach images to a prompt as base64 content blocks |

---

## Send Message

### Request

Accepts either `application/json` or `multipart/form-data`. Both paths are supported for backwards compatibility; JSON is the baseline, multipart is used when files are attached.

**JSON (no files)**
```http
POST /api/tasks/:id/messages
Content-Type: application/json

{
  "prompt": "string",
  "permissionMode": "default"|"acceptEdits"|"bypassPermissions"|"plan"|"dontAsk"|"auto"  // optional
}
```

**Multipart (with files)**
```http
POST /api/tasks/:id/messages
Content-Type: multipart/form-data; boundary=...

prompt=<text>
permissionMode=<mode>  (optional)
files=<image-file>  (0–4 files, each ≤ 5 MB, images only)
```

Supported MIME types for `files`: `image/jpeg`, `image/png`, `image/gif`, `image/webp`.

### File constraints

| Constraint | Limit | Enforced by |
|---|---|---|
| File count | max 4 | Backend handler |
| File size | max 5 MB each | Backend handler (`fh.Size` check before reading) |
| File type | image/jpeg, png, gif, webp | Backend handler (`isSupportedImageMIME`) |

The frontend also enforces the 4-file cap and shows a 5 MB advisory warning, but the backend enforces both limits independently.

### Content block encoding

When files are attached, the handler converts the prompt + images into a `ContentBlock[]` payload before forwarding to the proxy:

```
plain string prompt          → promptPayload = "string"
prompt + image files         → promptPayload = [
                                 {type:"text", text:"..."},
                                 {type:"image", source:{type:"base64", media_type:"image/png", data:"<b64>"}},
                                 ...
                               ]
```

`ContentBlock.Source` is a pointer type (`*ImageSource`) so the `omitempty` JSON tag functions correctly — text blocks do not emit a `source` field.

### Response

200 SSE stream. The `Content-Type` is `text/event-stream`; the backend proxies the agent-server stream verbatim. The terminal event is `session.completed`.

### Error responses

| Status | Condition |
|---|---|
| 400 | Missing prompt, unsupported MIME type, file too large, too many files |
| 404 | Task not found |
| 502 | Sandbox provisioning failed or upstream error |

---

## Steer Message

Injects a text prompt into an already-running agent session. The agent-server processes it via `queryHandle.streamInput(prompt, priority)` and its effects (tokens, tool calls, etc.) arrive on the **existing open SSE connection** — no new stream is opened.

### When to use

The frontend detects `sending === true` (an SSE stream is open) and routes the user's next message to `/steer` instead of `/messages`. The optimistic user message is shown immediately in the UI; if the steer call fails (non-2xx) the message is marked `status: 'error'`.

Steering is **text-only**. File attachments are not forwarded on the steer path.

### Request

```http
POST /api/tasks/:id/steer
Content-Type: application/json

{
  "prompt": "string",           // required
  "priority": "now"|"next"|"later"  // optional; defaults to no priority field
}
```

`priority` is validated: if present, must be exactly `"now"`, `"next"`, or `"later"`. Any other value returns 400.

### Response

| Status | Condition |
|---|---|
| 202 | Message injected; `{"ok": true}` |
| 400 | Missing prompt or invalid priority value |
| 404 | Task not found |
| 409 | No active agent run (`ErrNoActiveRun`) — task has no session ID |
| 502 | Upstream error from agent-server |

The 409 case occurs when the frontend routes to `/steer` but the session ID is not set (e.g. first message is still provisioning). The frontend handles this by marking the optimistic message as `error`.

---

## Proxy Layer (`internal/sandbox/proxy.go`)

The `Proxy` struct forwards both message types to claude-agent-server.

### `StreamMessage`

```go
func (p *Proxy) StreamMessage(
    ctx context.Context,
    t *task.Task,
    prompt string,
    blocks []ContentBlock,
    permissionMode string,
    w http.ResponseWriter,
) error
```

- If `blocks` is nil/empty: `promptPayload = prompt` (plain string — backward-compatible)
- If `blocks` is non-empty: prepends a text block, sends `promptPayload = []ContentBlock{...}`
- New session: `POST /sessions` (creates session, emits `session.init` with `sessionId`)
- Existing session: `POST /sessions/:id/messages` with `stream:true`
- Extracts `session.init.sessionId` from the stream and calls `t.SetSessionID(...)` on first message
- After a new session completes, fetches session metadata to populate the task title

### `SteerMessage`

```go
func (p *Proxy) SteerMessage(
    ctx context.Context,
    t *task.Task,
    prompt, priority string,
) error
```

- Returns `ErrNoActiveRun` if `t.GetSessionID() == ""`
- `POST /sessions/:id/messages` with `stream:false` (+ optional `priority` field)
- Agent-server response 202 → nil; 404 → `ErrNoActiveRun`; other → error

### Content block types

```go
type ImageSource struct {
    Type      string `json:"type"`        // "base64"
    MediaType string `json:"media_type"`  // "image/jpeg" | "image/png" | "image/gif" | "image/webp"
    Data      string `json:"data"`
}

type ContentBlock struct {
    Type   string       `json:"type"`             // "text" or "image"
    Text   string       `json:"text,omitempty"`
    Source *ImageSource `json:"source,omitempty"` // pointer: omitempty works on zero-value struct
}
```

---

## Frontend Behavior

### SSE connection model

Each `/messages` call opens one SSE connection. The frontend reads it via the `parseSSE` async generator in `useChat.ts` until `session.completed`.

### Steering detection

`sendMessage` in `useChat.ts` checks `sending && taskId` at the top of its body. The `sending` state is set to `true` before the SSE connection is opened and cleared on `session.completed` or `error`. Because `sending` is in the `sendMessage` `useCallback` dependency array, the closure always sees the current value.

```
sendMessage called while sending===true && taskId set
  → optimistic user message appended (status: 'done')
  → POST /api/tasks/:id/steer { prompt, priority: 'now' }
  → success: no further action (events flow on existing SSE)
  → failure: optimistic message marked status: 'error'
```

### File attachment UI

- `ChatInput` stores `{ file: File, url: string }[]` in state
- Object URLs are created in `handleFileChange`, revoked on `removeFile`, `submit`, and component unmount
- On submit: `File[]` extracted and passed to `sendMessage(prompt, files)`
- When `isSteering === true`: attachment button hidden, files dropped, steering banner shown

### Image rendering in message bubbles

`ChatMessage` computes object URLs once per message (keyed on `message.id`) via `useEffect` with revoke-on-cleanup:

```typescript
useEffect(() => {
    const urls = message.attachments?.map(a => ({ name: a.name, url: URL.createObjectURL(a.blob) })) ?? []
    setAttachmentUrls(urls)
    return () => urls.forEach(a => URL.revokeObjectURL(a.url))
}, [message.id])
```

Attachments are stored as `{ name: string; blob: Blob }[]` on the `Message` type for local preview. They are not persisted in history replay (the images are already encoded in the NDJSON content blocks on OFS).

---

## Invariants

1. The `/steer` endpoint is text-only. File attachments are never forwarded on the steer path.
2. `ContentBlock.Source` is `*ImageSource` (pointer). The `omitempty` tag must be on a pointer to avoid serializing empty source structs for text blocks.
3. File limits (4 files, 5 MB each) are enforced on the backend independently of the frontend.
4. `priority` on steer requests must be `"now"`, `"next"`, or `"later"` if provided; any other value is rejected 400 before reaching the proxy.
5. `ErrNoActiveRun` is the sole sentinel that maps to 409; all other proxy errors map to 502.
6. `sending` must be in the `sendMessage` `useCallback` dependency array to prevent stale-closure steering misdetection.

---

## Related Documents

- [`resource-mapping.md`](resource-mapping.md) — session lifecycle and when `session_id` is set
- [`ofsspec.md`](ofsspec.md) — how conversation history (including content blocks) is stored in OFS
