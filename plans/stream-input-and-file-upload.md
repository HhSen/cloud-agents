# Stream Input & File Upload

Two new features exposed by `claude-agent-server` that we need to wire through the lucas stack:

1. **Stream input (steering)** — inject a message into an *already-running* agent without opening a new SSE connection
2. **File upload while prompting** — attach images / files to a message; the agent-server accepts `PromptContent` as an array of typed content blocks

---

## Background

### What the agent-server already supports

**`POST /sessions/:id/messages`** has dual behaviour:

| Condition | Server action | HTTP response |
|-----------|---------------|---------------|
| No active run | Starts a new run | 200/SSE stream (if `stream:true`) |
| Active run exists | Calls `queryHandle.streamInput(prompt, priority)` | **202 Accepted** (no body; events flow on the already-open SSE connection) |

Priority values: `'now' | 'next' | 'later'`.

**`PromptContent`** type: `string | ContentBlockParam[]`

```typescript
type ContentBlockParam =
  | { type: 'text'; text: string }
  | { type: 'image'; source: { type: 'base64'; media_type: 'image/jpeg'|'image/png'|'image/gif'|'image/webp'; data: string }
                            | { type: 'url'; url: string } }
```

### Current lucas architecture

```
frontend sendMessage(prompt)
  → POST /api/tasks/:id/messages  { prompt: string }
  → proxy.StreamMessage(prompt string)
    → POST agent/sessions/:id/messages  { prompt, stream:true, ... }
    ← SSE stream (proxied verbatim)
  ← SSE stream (consumed by useChat parseSSE)
```

The frontend model is **one SSE connection per sendMessage call**, closed on `session.completed`.

---

## Feature 1 — Stream Input (Steering)

### Problem

When the agent is already running, the user's next message must be *steered into* the active run rather than queuing a new SSE stream. The 202 response carries no body; the injected message's effects arrive on the **existing** open SSE connection.

### Design

Add a dedicated **steer endpoint** rather than overloading the existing messages endpoint. This keeps the two code paths clean and avoids any ambiguity in the SSE / non-SSE split.

```
POST /api/tasks/:id/steer
Body: { "prompt": string, "priority"?: "now" | "next" | "later" }
Response: 202 { "ok": true }   — or 409 if no active run
```

### Backend changes

**`internal/api/handlers_tasks.go`**

Add `SteerMessage` handler:

```go
type SteerMessageRequest struct {
    Prompt   string `json:"prompt"   binding:"required"`
    Priority string `json:"priority"` // "now" | "next" | "later", optional
}

func (h *TaskHandler) SteerMessage(c *gin.Context) {
    id := c.Param("id")
    var req SteerMessageRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(400, gin.H{"error": err.Error()}); return
    }
    t, err := h.tasks.Get(c, id)
    // ownership check …
    if err := h.proxy.SteerMessage(c.Request.Context(), t, req.Prompt, req.Priority); err != nil {
        // proxy returns ErrNoActiveRun → 409
        c.JSON(errorStatus(err), gin.H{"error": err.Error()}); return
    }
    c.JSON(202, gin.H{"ok": true})
}
```

Register route: `authed.POST("/tasks/:id/steer", taskHandler.SteerMessage)`

**`internal/sandbox/proxy.go`**

Add `SteerMessage` function (no SSE):

```go
var ErrNoActiveRun = errors.New("no active run for session")

func (p *Proxy) SteerMessage(ctx context.Context, t *task.Task, prompt, priority string) error {
    base, headers, err := t.GetProxyInfo()
    if err != nil { return err }
    sessionID := t.SessionID()
    if sessionID == "" { return ErrNoActiveRun }

    body := map[string]any{
        "prompt": prompt,
        "stream": false,
    }
    if priority != "" {
        body["priority"] = priority
    }

    data, _ := json.Marshal(body)
    req, _ := http.NewRequestWithContext(ctx, "POST",
        base+"/sessions/"+sessionID+"/messages", bytes.NewReader(data))
    for k, v := range headers { req.Header.Set(k, v) }
    req.Header.Set("Content-Type", "application/json")

    resp, err := p.client.Do(req)
    if err != nil { return err }
    defer resp.Body.Close()

    if resp.StatusCode == 202 { return nil }
    if resp.StatusCode == 404 { return ErrNoActiveRun }
    return fmt.Errorf("steer: unexpected status %d", resp.StatusCode)
}
```

### Frontend changes

**`src/api/client.ts`** — add:

```typescript
export async function steerMessage(
  taskId: string,
  prompt: string,
  priority?: 'now' | 'next' | 'later',
): Promise<void> {
  await apiFetch(`/api/tasks/${taskId}/steer`, {
    method: 'POST',
    body: JSON.stringify({ prompt, priority }),
  })
}
```

**`src/hooks/useChat.ts`** — in `sendMessage`:

```typescript
// If the agent is already processing, inject as a steering message
if (isSending && taskId) {
  await steerMessage(taskId, prompt, 'now')
  // Optimistically append the user turn so the user sees it immediately.
  // Events keep flowing on the existing SSE connection.
  appendUserMessage(prompt)
  return
}
// … existing new-run logic below
```

**UI indicator** (`ChatPage.tsx` or chat input component):

When `isSending`, the submit button label/tooltip changes to "Steer" and a small badge reads *"Agent is running — your message will be injected"*.

---

## Feature 2 — File Upload While Prompting

### Problem

The agent-server accepts `prompt` as a `ContentBlockParam[]` (text + image blocks). The lucas backend currently only passes a plain string. We need to:

1. Accept file uploads at the `/messages` endpoint
2. Convert them to base64 content blocks
3. Forward the structured prompt to the agent-server

### Scope for v1

Support **images only** (JPEG, PNG, GIF, WEBP — exactly what the agent-server's schema allows). Text / PDF attachments are out-of-scope for now.

### API change

Switch `POST /api/tasks/:id/messages` from `application/json` to **`multipart/form-data`**:

| Field | Type | Notes |
|-------|------|-------|
| `prompt` | text | Required text portion |
| `files` | file (0–N) | Optional images |

No change to the steer endpoint — steering messages are always text-only.

### Backend changes

**`internal/api/handlers_tasks.go`** — `SendMessage` handler:

```go
func (h *TaskHandler) SendMessage(c *gin.Context) {
    id := c.Param("id")

    // Accept both multipart and JSON for backwards compat.
    var promptText string
    var contentBlocks []proxy.ContentBlock

    ct := c.ContentType()
    if strings.HasPrefix(ct, "multipart/form-data") {
        form, err := c.MultipartForm()
        if err != nil { c.JSON(400, gin.H{"error": err.Error()}); return }
        prompts := form.Value["prompt"]
        if len(prompts) == 0 { c.JSON(400, gin.H{"error": "prompt required"}); return }
        promptText = prompts[0]

        for _, fh := range form.File["files"] {
            f, err := fh.Open()
            if err != nil { c.JSON(400, gin.H{"error": err.Error()}); return }
            data, _ := io.ReadAll(f); f.Close()
            mime := fh.Header.Get("Content-Type")
            if !isSupportedImageMIME(mime) {
                c.JSON(400, gin.H{"error": "unsupported file type: " + mime}); return
            }
            contentBlocks = append(contentBlocks, proxy.ContentBlock{
                Type: "image",
                Source: proxy.ImageSource{
                    Type:      "base64",
                    MediaType: mime,
                    Data:      base64.StdEncoding.EncodeToString(data),
                },
            })
        }
    } else {
        var req struct { Prompt string `json:"prompt" binding:"required"` }
        if err := c.ShouldBindJSON(&req); err != nil {
            c.JSON(400, gin.H{"error": err.Error()}); return
        }
        promptText = req.Prompt
    }

    // … existing ownership / provisioning logic …

    if err := h.proxy.StreamMessage(c.Request.Context(), t, promptText, contentBlocks, c.Writer); err != nil {
        // …
    }
}

func isSupportedImageMIME(m string) bool {
    switch m {
    case "image/jpeg", "image/png", "image/gif", "image/webp": return true
    }
    return false
}
```

**`internal/sandbox/proxy.go`** — new types + updated `StreamMessage`:

```go
type ImageSource struct {
    Type      string `json:"type"`       // "base64"
    MediaType string `json:"media_type"`
    Data      string `json:"data"`
}

type ContentBlock struct {
    Type   string      `json:"type"`             // "text" or "image"
    Text   string      `json:"text,omitempty"`
    Source ImageSource `json:"source,omitempty"`
}

// StreamMessage now accepts optional content blocks.
// If blocks is nil, prompt is sent as a plain string (existing behaviour).
// Otherwise, a text block is prepended and the array is sent.
func (p *Proxy) StreamMessage(ctx context.Context, t *task.Task, prompt string, blocks []ContentBlock, w http.ResponseWriter) error {
    var promptPayload any = prompt
    if len(blocks) > 0 {
        full := []ContentBlock{{Type: "text", Text: prompt}}
        full = append(full, blocks...)
        promptPayload = full
    }
    body := map[string]any{
        "prompt":                 promptPayload,
        "stream":                 true,
        "includePartialMessages": true,
        "forkSession":            false,
        "options":                buildOptions(t),
    }
    // … rest unchanged
}
```

Bump the multipart limit in `app.go` (or the Go handler) to **20 MB**.

### Frontend changes

**`src/api/client.ts`** — change `sendMessage`:

```typescript
export async function sendMessage(
  taskId: string,
  prompt: string,
  files?: File[],
): Promise<Response> {
  if (files && files.length > 0) {
    const form = new FormData()
    form.append('prompt', prompt)
    for (const f of files) form.append('files', f)
    return fetch(`${BASE}/api/tasks/${taskId}/messages`, {
      method: 'POST',
      headers: authHeaders(), // no Content-Type — browser sets multipart boundary
      body: form,
    })
  }
  return fetch(`${BASE}/api/tasks/${taskId}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ prompt }),
  })
}
```

**`src/hooks/useChat.ts`** — thread `files?: File[]` through `sendMessage`:

```typescript
async function sendMessage(prompt: string, files?: File[]) {
  // …
  const response = await apiSendMessage(id, prompt, files)
  // … rest unchanged
}
```

**Chat input component** — add attachment button:

```
[ 📎 ] [ textarea ················· ] [ Send ]
```

- Click `📎` → `<input type="file" accept="image/*" multiple hidden>` trigger
- Selected files shown as thumbnail chips above the textarea (removable with ×)
- On send, pass files array; clear chips after send
- Show file size warning if any file > 5 MB

**Message display** — render image attachments in the user bubble:

```tsx
{msg.attachments?.map(a => (
  <img key={a.name} src={URL.createObjectURL(a.blob)} className="max-h-48 rounded" />
))}
```

Store `attachments` as `{ name: string; blob: Blob }[]` on the `Message` type for local preview (not persisted in history replay — images are already in the NDJSON content blocks).

---

## Sequencing

| Step | Scope | Complexity |
|------|-------|------------|
| 1 | `proxy.SteerMessage()` + `/api/tasks/:id/steer` route | Low |
| 2 | Frontend `steerMessage()` + `useChat` routing logic | Low |
| 3 | UI steering indicator | Low |
| 4 | `proxy.ContentBlock` types + updated `StreamMessage` signature | Low |
| 5 | Multipart parsing in `SendMessage` handler | Medium |
| 6 | Frontend `FormData` send + file input UI | Medium |
| 7 | Image thumbnail display in message bubbles | Low |

Steps 1–3 are independent of 4–7 and can be done in parallel.

---

## Out of Scope

- PDF / text file attachments (requires document content block type)
- Persisting attachment previews in history replay
- File size / count limits enforced at the agent-server level (lucas enforces 5 MB per file, 4 files max in v1)
- `priority` selector UI (defaults to `'now'`; can expose later)
