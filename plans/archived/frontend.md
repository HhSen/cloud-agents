# Frontend Plan (Vite + React + shadcn/ui)

Location: `/Users/didi/lucas/frontend/`

## Key References
- Existing console for stack reference: `../Opensandbox/console/`
- Backend API contract: `./overview.md` → "API Contract" section
- SSE events to render: `./overview.md` → "claude-agent-server API" section

---

## Step 1 — Scaffold

```bash
npm create vite@latest frontend -- --template react-ts
cd frontend
npm install
```

**Install dependencies (match console stack):**
```bash
npm install tailwindcss @tailwindcss/vite
npm install class-variance-authority clsx tailwind-merge
npm install lucide-react
npm install @radix-ui/react-scroll-area @radix-ui/react-slot
npm install react-router-dom
npm install --save-dev @types/node   # required for vite.config.ts path alias
```

**Init shadcn:**
```bash
npx shadcn@latest init
```
When prompted: TypeScript yes, Tailwind CSS v4, base color neutral, `src/components/ui`.

**Add initial shadcn components:**
```bash
npx shadcn@latest add button textarea scroll-area badge
```

---

## Step 2 — File structure

```
frontend/src/
├── api/
│   └── client.ts              # fetch wrapper + SSE helper + auth headers
├── components/
│   ├── ui/                    # shadcn primitives (auto-generated)
│   ├── ChatMessage.tsx        # one message bubble
│   ├── ChatInput.tsx          # textarea + send button
│   ├── StatusBadge.tsx        # "Starting workspace…" indicator
│   └── ProtectedRoute.tsx     # redirects to /login if no valid JWT
├── hooks/
│   └── useChat.ts             # all state + SSE logic
├── lib/
│   └── auth.ts                # JWT read/write in localStorage
├── pages/
│   ├── ChatPage.tsx           # root page (username from JWT)
│   ├── LoginPage.tsx          # SSO / OIDC / dev login buttons
│   └── SSOCallbackPage.tsx    # handles /login/sso and /login/oidc callbacks
├── types.ts                   # shared types
├── App.tsx                    # BrowserRouter with route definitions
└── main.tsx
```

---

## Step 3 — Types (`src/types.ts`)

```ts
export type Role = 'user' | 'assistant'

export type MessageStatus = 'streaming' | 'done' | 'error'

export interface Message {
  id: string
  role: Role
  text: string
  status: MessageStatus
  toolActivity?: ToolActivity[]
}

export interface ToolActivity {
  description: string
  toolName?: string
  done: boolean
}

export type SandboxState = 'idle' | 'provisioning' | 'running' | 'error'
```

---

## Step 3.5 — Auth helpers (`src/lib/auth.ts`)

JWT stored in `localStorage` under key `lucas_token`. Payload is decoded client-side (no signature verification — that is the backend's responsibility).

```ts
const TOKEN_KEY = 'lucas_token'

export function getToken(): string | null { return localStorage.getItem(TOKEN_KEY) }
export function setToken(t: string): void  { localStorage.setItem(TOKEN_KEY, t) }
export function clearToken(): void         { localStorage.removeItem(TOKEN_KEY) }

function decodeToken(token: string): { user_name: string; exp: number } | null {
  try {
    return JSON.parse(atob(token.split('.')[1]))
  } catch { return null }
}

export function getAuthUsername(): string | null {
  const token = getToken()
  if (!token) return null
  const payload = decodeToken(token)
  if (!payload || payload.exp * 1000 <= Date.now()) return null
  return payload.user_name
}
```

---

## Step 3.6 — Auth routing (`src/App.tsx`)

`BrowserRouter` with four routes. `/login/oidc` reuses `SSOCallbackPage` — the handler only reads the token from the URL fragment.

```tsx
<BrowserRouter>
  <Routes>
    <Route path="/login"      element={<LoginPage />} />
    <Route path="/login/sso"  element={<SSOCallbackPage />} />
    <Route path="/login/oidc" element={<SSOCallbackPage />} />
    <Route path="/"           element={<ProtectedRoute><ChatPage /></ProtectedRoute>} />
  </Routes>
</BrowserRouter>
```

### `ProtectedRoute`

Synchronous check — no loading state needed because `getAuthUsername()` reads only localStorage:

```tsx
export function ProtectedRoute({ children }: { children: React.ReactNode }) {
  if (!getAuthUsername()) return <Navigate to="/login" replace />
  return <>{children}</>
}
```

### `LoginPage`

Fetches `GET /api/runtime-config` on mount; renders login options based on response:

```
{ loginMode: "sso" | "oidc" | "password" | "", devLogin: bool }
```

- `loginMode` includes `"sso"` → SSO button that calls `GET /api/auth/sso/login`
- `loginMode` includes `"oidc"` → OIDC button that navigates to `GET /api/auth/oidc/login`
- `devLogin === true` → username text input + Continue button → calls `GET /api/auth/dev/login?username=<name>`

Dev login is only offered when `SSOService == nil && OIDCService == nil` (set in `runtimeConfigHandler`).

### `SSOCallbackPage`

Reads the token from the **URL hash** (fragment), not query params. Using the fragment prevents the token from appearing in server access logs, browser history, or `Referer` headers.

```tsx
const { hash } = useLocation()
const token = new URLSearchParams(hash.slice(1)).get('access_token')
if (token) { setToken(token); navigate('/', { replace: true }) }
else        { navigate('/login', { replace: true }) }
```

Backend redirects: `{frontend_url}/login/sso#access_token=<jwt>`

---

## Step 4 — API client (`src/api/client.ts`)

```ts
const BASE = import.meta.env.VITE_API_BASE ?? ''

export interface RuntimeConfig {
  loginMode: string
  devLogin: boolean
}

export async function getRuntimeConfig(): Promise<RuntimeConfig> {
  const res = await fetch(`${BASE}/api/runtime-config`)
  if (!res.ok) throw new Error('Failed to fetch runtime config')
  return res.json()
}

function authHeaders(): Record<string, string> {
  const token = getToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export async function createTask(): Promise<string> {
  const res = await fetch(`${BASE}/api/tasks`, {
    method: 'POST',
    headers: authHeaders(),
  })
  if (!res.ok) throw new Error('Failed to create task')
  const { id } = await res.json()
  return id
}

// Returns the raw Response so the caller can read the body as a stream
export async function sendMessage(taskId: string, prompt: string): Promise<Response> {
  return fetch(`${BASE}/api/tasks/${taskId}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders() },
    body: JSON.stringify({ prompt }),
  })
}

export async function deleteTask(taskId: string): Promise<void> {
  await fetch(`${BASE}/api/tasks/${taskId}`, {
    method: 'DELETE',
    headers: authHeaders(),
  })
}
```

---

## Step 5 — SSE parsing

SSE format from claude-agent-server (proxied verbatim by backend):
```
event: session.init
data: {"sessionId":"abc","model":"claude-opus-4-7",...}

event: message.assistant
data: {"text":"Hello!","uuid":"..."}

event: session.status
data: {"status":"running",...}

event: task.started
data: {"description":"Running bash command","taskType":"tool_use",...}

event: task.progress
data: {"description":"...","lastToolName":"bash",...}

event: result
data: {"totalCostUsd":0.002,"numTurns":1,"stopReason":"end_turn",...}

event: session.completed
data: {"sessionId":"abc"}

event: error
data: {"message":"...","code":500}
```

**Parser logic** (in `useChat.ts`):

Read `response.body` as a `ReadableStream`, decode line by line, track current `event:` name, parse `data:` JSON on double-newline boundary.

```ts
async function* parseSSE(response: Response) {
  const reader = response.body!.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let currentEvent = ''

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const lines = buffer.split('\n')
    buffer = lines.pop() ?? ''
    for (const line of lines) {
      if (line.startsWith('event:')) currentEvent = line.slice(6).trim()
      else if (line.startsWith('data:')) {
        const data = JSON.parse(line.slice(5).trim())
        yield { event: currentEvent, data }
        currentEvent = ''
      }
    }
  }
}
```

---

## Step 6 — Chat hook (`src/hooks/useChat.ts`)

State managed here:
```ts
const [messages, setMessages] = useState<Message[]>([])
const [convId, setConvId] = useState<string | null>(null)
const [sandboxState, setSandboxState] = useState<SandboxState>('idle')
const [sending, setSending] = useState(false)
```

**`sendMessage(prompt)` flow:**

```
1. if convId == null → createConversation() → setConvId
2. Append user message to messages
3. Append empty assistant message with status='streaming'
4. setSandboxState('provisioning')  ← shown in UI until session.init arrives
5. POST /api/conversations/:id/messages
6. For each SSE event:
   - session.init     → setSandboxState('running')
   - message.assistant → append .text to assistant message (streaming)
   - session.status   → if status=='idle', mark message done
   - task.started     → push ToolActivity{done:false} to assistant message
   - task.progress    → update last ToolActivity
   - result           → mark assistant message status='done'
   - session.completed → setSending(false)
   - error            → mark assistant message status='error'
```

---

## Step 7 — Components

### `ChatMessage.tsx`

- User messages: right-aligned, neutral bubble
- Assistant messages: left-aligned, renders text as markdown (use `react-markdown` or simple `<p>`)
- If `toolActivity` present: show collapsed accordion with tool steps
- Status `'streaming'`: show blinking cursor appended to text
- Status `'error'`: red tint + error icon

### `ChatInput.tsx`

- `<Textarea>` auto-resizes (up to ~6 lines)
- `<Button>` with send icon (lucide `SendHorizonal`)
- Disabled while `sending === true`
- Submit on `Enter` (not shift+enter), `Shift+Enter` inserts newline
- Placeholder: `"Ask anything…"`

### `StatusBadge.tsx`

Small badge shown in chat header:
- `idle` → nothing shown
- `provisioning` → pulsing dot + "Starting workspace…"
- `running` → green dot + "Connected"
- `error` → red dot + "Connection error"

### `ChatPage.tsx`

Username comes from the JWT (no prompt at runtime):

```tsx
const username = getAuthUsername() ?? ''
```

```
┌─────────────────────────────────┐
│  [StatusBadge]                  │  ← header
├─────────────────────────────────┤
│                                 │
│   [empty state or messages]     │  ← ScrollArea, fills height
│                                 │
├─────────────────────────────────┤
│  [ChatInput]                    │  ← sticky bottom
└─────────────────────────────────┘
```

Empty state: centered text `"What can I help you with?"` in muted color.

Auto-scroll to bottom on new message/chunk.

---

## Step 8 — Vite config (`vite.config.ts`)

```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'path'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(__dirname, 'src') },
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8091',
        changeOrigin: true,
      }
    }
  }
})
```

Using Vite proxy avoids CORS entirely in dev. `VITE_API_BASE` can stay empty.

`path` is a Node.js built-in. `@types/node` must be installed and `tsconfig.node.json` must include `"types": ["node"]` — otherwise `vite build` fails with a type error on `__dirname`.

---

## Step 9 — Run & verify

```bash
cd frontend
npm install
npm run dev
# opens http://localhost:5173
```

Checklist:
- [ ] Unauthenticated visit to `/` redirects to `/login`
- [ ] Dev login with any username issues a JWT and redirects to `/`
- [ ] SSO/OIDC callback at `/login/sso` or `/login/oidc` reads `#access_token=` from fragment, stores token, redirects to `/`
- [ ] Expired/missing token causes ProtectedRoute to redirect to `/login`
- [ ] Page loads with empty state message after login
- [ ] No network requests on load (no sandbox created yet)
- [ ] Typing and submitting first message triggers `POST /api/tasks` then `POST /api/tasks/:id/messages`
- [ ] StatusBadge shows "Starting workspace…" while sandbox provisions
- [ ] Assistant text streams in incrementally
- [ ] Tool activity appears as collapsed items
- [ ] Second message reuses same task (no new `/tasks` POST)
- [ ] Input is disabled while a message is in-flight

---

## Notes & Gotchas

- `message.assistant` events carry partial `text` — each event is a delta to **append**, not replace.
- Some `message.assistant` events have empty `text` (tool-use messages) — skip them.
- `session.completed` is the safe signal to enable the input again, not `result`.
- On mobile viewports, `100vh` is unreliable; use `100dvh` (dynamic viewport height) for the chat container height.
- shadcn's `ScrollArea` needs an explicit height on its parent; set it to `calc(100dvh - <input-height>)`.
