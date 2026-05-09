# Frontend

Vite + React + TypeScript chat interface. Talks to the Go backend at `:8081` via a Vite dev proxy — no CORS configuration needed.

## Running

```bash
cd frontend
npm run dev      # http://localhost:5173
npm run build    # type-check + production bundle
npm run lint     # ESLint
```

## Stack

| Layer | Choice |
|---|---|
| Bundler | Vite 6 |
| UI | React 19 + TypeScript |
| Styling | Tailwind CSS v4 (`@tailwindcss/vite`) |
| Components | shadcn/ui (neutral palette) |
| Icons | lucide-react |
| Markdown | react-markdown |

Font: Inter (loaded from `rsms.me/inter` CDN, applied via `@theme { --font-sans }` in `index.css`).

## File structure

```
src/
├── api/
│   └── client.ts          # fetch wrappers for the backend REST API
├── components/
│   ├── ui/                # shadcn primitives (Button, Textarea, ScrollArea, Badge)
│   ├── ChatMessage.tsx    # single message bubble
│   ├── ChatInput.tsx      # auto-resizing textarea + send button
│   └── StatusBadge.tsx    # sandbox connection indicator
├── hooks/
│   └── useChat.ts         # all state + SSE streaming logic
├── lib/
│   └── utils.ts           # cn() helper (clsx + tailwind-merge)
├── pages/
│   └── ChatPage.tsx       # root page layout
├── types.ts               # shared TypeScript types
├── App.tsx
├── index.css              # Tailwind import + Inter @theme
└── main.tsx
```

## API client (`src/api/client.ts`)

Thin fetch wrappers. Base URL comes from `VITE_API_BASE` (defaults to `''`, so the Vite proxy handles routing in dev).

| Function | Description |
|---|---|
| `createConversation()` | `POST /api/conversations` → returns conversation `id` |
| `sendMessage(convId, prompt)` | `POST /api/conversations/:id/messages` → returns raw `Response` for stream reading |
| `deleteConversation(convId)` | `DELETE /api/conversations/:id` |

`sendMessage` returns the raw `Response` rather than parsed data so the caller can read the body as a stream.

## State & SSE (`src/hooks/useChat.ts`)

`useChat()` is the single source of truth for all chat state.

```ts
const { messages, sandboxState, sending, sendMessage } = useChat()
```

| Value | Type | Description |
|---|---|---|
| `messages` | `Message[]` | Full conversation history |
| `sandboxState` | `SandboxState` | `idle \| provisioning \| running \| error` |
| `sending` | `boolean` | True while a message is in-flight |
| `sendMessage(prompt)` | `(string) => void` | Send a message and stream the response |

### `sendMessage` flow

1. If no conversation exists yet, calls `createConversation()` and stores the id.
2. Appends the user message and an empty assistant message (status `streaming`) to the list.
3. Sets `sandboxState` to `provisioning` (backend may need to cold-start the sandbox).
4. POSTs to the backend and reads the SSE stream via `parseSSE`.

### SSE event handling

| Event | Action |
|---|---|
| `session.init` | Sets `sandboxState` → `running` |
| `message.assistant` | Appends `data.text` to the assistant message (delta, not replace) |
| `session.status` (idle) | Marks assistant message `done` |
| `task.started` | Pushes a new `ToolActivity{done: false}` onto the message |
| `task.progress` | Updates the last `ToolActivity` with current description and tool name |
| `result` | Marks assistant message `done` |
| `session.completed` | Marks all tool activities `done`, re-enables input |
| `error` | Marks assistant message `error`, sets `sandboxState` → `error` |

`session.completed` (not `result`) is the signal used to re-enable the input, matching the backend's terminal event.

### SSE parser

`parseSSE(response)` is an async generator that reads `response.body` as a `ReadableStream`, decodes chunks, and yields `{ event, data }` pairs on each complete `event:` / `data:` block. Partial chunks are buffered across reads.

## Components

### `ChatPage` (`src/pages/ChatPage.tsx`)

Root layout. Three-row flex column filling `100dvh` (dynamic viewport — avoids mobile browser chrome issues). App title is **"Lucas"**.

```
┌──────────────────────────────┐
│  "Lucas"  |  StatusBadge     │  ← header (border-b)
├──────────────────────────────┤
│  ScrollArea (flex-1)         │
│    empty state or messages   │
├──────────────────────────────┤
│  ChatInput (border-t)        │
└──────────────────────────────┘
```

Auto-scrolls to the bottom whenever `messages` changes.

### `ChatMessage` (`src/components/ChatMessage.tsx`)

Renders one message bubble.

- **User** — right-aligned, dark neutral bubble, plain text (`whitespace-pre-wrap`).
- **Assistant** — left-aligned, light neutral bubble, rendered as Markdown via `react-markdown`. Appends a blinking cursor while `status === 'streaming'`.
- **Error** — red tint + `AlertCircle` icon regardless of role.
- **Tool activity** — shown below the assistant text behind a toggle button (`ChevronDown` icon + count label). When expanded, each item has a pulsing neutral dot while `done === false`, green dot when complete, with tool name bolded before the description.

### `ChatInput` (`src/components/ChatInput.tsx`)

Textarea that auto-resizes up to ~6 lines. Submits on `Enter`; `Shift+Enter` inserts a newline. Both the send button and Enter submit are disabled while `sending === true` or the input is empty/whitespace-only.

### `StatusBadge` (`src/components/StatusBadge.tsx`)

Renders nothing when `idle`. Otherwise shows a colored dot + label:

| State | Dot | Label |
|---|---|---|
| `provisioning` | pulsing neutral | Starting workspace… |
| `running` | green | Connected |
| `error` | red | Connection error |

## Configuration

### Vite proxy (`vite.config.ts`)

```ts
server: {
  proxy: {
    '/api': { target: 'http://localhost:8081', changeOrigin: true }
  }
}
```

All `/api/*` requests from the browser are forwarded to the Go backend. `VITE_API_BASE` can be left unset in dev.

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `VITE_API_BASE` | `''` | Backend base URL. Leave empty when using the Vite proxy. Set to `http://localhost:8081` for environments without a proxy. |

### Path alias

`@/*` maps to `src/*`, configured in `tsconfig.app.json` and `vite.config.ts`.

## Tooling

- **TypeScript** — strict mode, `moduleResolution: bundler`, `noUncheckedSideEffectImports`.
- **ESLint** — flat config (`eslint.config.js`) with `typescript-eslint`, `eslint-plugin-react-hooks`, `eslint-plugin-react-refresh`. The `react-refresh` export warning is suppressed for `src/components/ui/` (shadcn components intentionally export both component and variant helpers).
- **shadcn** — configured in `components.json` with neutral base color and `cssVariables: true`. Add new components with `npx shadcn@latest add <name>`.
