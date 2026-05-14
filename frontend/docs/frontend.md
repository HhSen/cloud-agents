# Frontend

Vite + React + TypeScript chat interface. Talks to the Go backend at `:8091` via a Vite dev proxy — no CORS configuration needed.

## Running

```bash
cd frontend
npm run dev      # http://localhost:5173
npm run build    # type-check + production bundle
npm run lint     # ESLint
```

## Stack

| Layer      | Choice                                |
| ---------- | ------------------------------------- |
| Bundler    | Vite 6                                |
| UI         | React 19 + TypeScript                 |
| Styling    | Tailwind CSS v4 (`@tailwindcss/vite`) |
| Components | shadcn/ui (neutral palette)           |
| Icons      | lucide-react                          |
| Markdown   | react-markdown                        |

Font: Inter (loaded from `rsms.me/inter` CDN, applied via `@theme { --font-sans }` in `index.css`).

## File structure

```
src/
├── api/
│   └── client.ts              # fetch wrappers for the backend REST API
├── components/
│   ├── ui/                    # shadcn primitives (Button, Textarea, ScrollArea, Badge)
│   ├── ChatMessage.tsx        # single message bubble
│   ├── ChatInput.tsx          # auto-resizing textarea + send button
│   ├── HistorySidepanel.tsx   # left sidebar: task list + new-chat button
│   ├── ProtectedRoute.tsx     # redirects to /login if no valid JWT
│   └── StatusBadge.tsx        # sandbox connection indicator
├── hooks/
│   └── useChat.ts             # all state + SSE streaming logic
├── lib/
│   ├── auth.ts                # JWT token management (localStorage)
│   ├── chainBuilder.ts        # convert SessionEntry[] → Message[] for history replay
│   └── utils.ts               # cn() helper (clsx + tailwind-merge)
├── pages/
│   ├── ChatPage.tsx           # root page layout (with sidebar)
│   ├── LoginPage.tsx          # SSO / OIDC / dev login buttons
│   └── SSOCallbackPage.tsx    # reads #access_token= from URL fragment
├── types/
│   └── session.ts             # typed SessionEntry union from claude-agent-sdk
├── types.ts                   # shared TypeScript types (Message, ToolUseBlock, …)
├── App.tsx
├── index.css                  # Tailwind import + Inter @theme
└── main.tsx
```

## API client (`src/api/client.ts`)

Thin fetch wrappers. Base URL comes from `VITE_API_BASE` (defaults to `''`, so the Vite proxy handles routing in dev).

| Function                                | Description                                                     |
| --------------------------------------- | --------------------------------------------------------------- |
| `getRuntimeConfig()`                    | `GET /api/runtime-config` → login mode flags                    |
| `listTasks()`                           | `GET /api/tasks` → `TaskSummary[]` (newest first)               |
| `createTask(username)`                  | `POST /api/tasks` → task `id`                                   |
| `sendMessage(taskId, prompt)`           | `POST /api/tasks/:id/messages` → raw `Response` for SSE reading |
| `getHistory(taskId)`                    | `GET /api/tasks/:id/history` → `SessionEntry[]`                 |
| `deleteTask(taskId)`                    | `DELETE /api/tasks/:id`                                         |
| `respondToPermission(taskId, decision)` | `POST /api/tasks/:id/permissions`                               |
| `respondToQuestion(taskId, answers)`    | `POST /api/tasks/:id/questions`                                 |

`sendMessage` returns the raw `Response` rather than parsed data so the caller can read the body as a stream.

## State & SSE (`src/hooks/useChat.ts`)

`useChat(username)` is the single source of truth for all chat state.

```ts
const { messages, taskId, sandboxState, sending, sendMessage, loadTask, newChat, approvePermission, answerQuestion } = useChat(username)
```

| Value                         | Type                          | Description                                     |
| ----------------------------- | ----------------------------- | ----------------------------------------------- |
| `messages`                    | `Message[]`                   | Full conversation history                       |
| `taskId`                      | `string \| null`              | Current task ID; null for a fresh chat          |
| `sandboxState`                | `SandboxState`                | `idle \| provisioning \| running \| error`      |
| `sending`                     | `boolean`                     | True while a message is in-flight               |
| `sendMessage(prompt)`         | `(string) => void`            | Send a message and stream the response          |
| `loadTask(id, messages)`      | `(string, Message[]) => void` | Load history from a previous task and resume it |
| `newChat()`                   | `() => void`                  | Reset to an empty fresh chat                    |
| `approvePermission(approved)` | `(boolean) => void`           | Respond to a pending tool permission            |
| `answerQuestion(answers)`     | `(Record) => void`            | Submit answers to a pending question            |

### `sendMessage` flow

1. If `taskId` is null, calls `createTask(username)` and stores the id.
2. Appends the user message and an empty assistant message (status `streaming`) to the list.
3. Sets `sandboxState` to `provisioning` (backend may need to cold-start the sandbox).
4. POSTs to the backend and reads the SSE stream via `parseSSE`.

### SSE event handling

| Event                   | Action                                                                 |
| ----------------------- | ---------------------------------------------------------------------- |
| `session.init`          | Sets `sandboxState` → `running`                                        |
| `message.assistant`     | Appends `data.text` to the assistant message (delta, not replace)      |
| `session.status` (idle) | Marks assistant message `done`                                         |
| `task.started`          | Pushes a new `ToolActivity{done: false}` onto the message              |
| `task.progress`         | Updates the last `ToolActivity` with current description and tool name |
| `result`                | Marks assistant message `done`                                         |
| `session.completed`     | Marks all tool activities `done`, re-enables input                     |
| `error`                 | Marks assistant message `error`, sets `sandboxState` → `error`         |

`session.completed` (not `result`) is the signal used to re-enable the input, matching the backend's terminal event.

### SSE parser

`parseSSE(response)` is an async generator that reads `response.body` as a `ReadableStream`, decodes chunks, and yields `{ event, data }` pairs on each complete `event:` / `data:` block. Partial chunks are buffered across reads.

## History replay (`src/lib/chainBuilder.ts`)

`buildMessages(entries: SessionEntry[]): Message[]` converts the raw NDJSON entries returned by `GET /api/tasks/:id/history` into the same `Message[]` shape that the live SSE stream produces.

Only `user` and `assistant` entries are processed:
- **User entries** — content is extracted from `message.content` (string or `TextBlockParam[]`).
- **Assistant entries** — text is extracted from `BetaTextBlock` blocks; `BetaToolUseBlock` blocks become `ToolUseBlock[]` on the message.

All resulting messages have `status: 'done'` so they render identically to completed live messages.

## Session types (`src/types/session.ts`)

Typed union for every entry type that can appear in a `.jsonl` history file. Uses the `@anthropic-ai/claude-agent-sdk` package (devDependency) to reuse the message payload types (`SDKAssistantMessage`, `SDKUserMessage`, etc.) rather than duplicating them.

Key types exported:
- `SessionEntry` — full union of all entry types
- `VisibleEntry` — `UserEntry | AssistantEntry` (shown in chat)
- `ConversationEntry` — entries with `uuid` (participate in chain building)

## Components

### `ChatPage` (`src/pages/ChatPage.tsx`)

Two-column layout filling `100dvh`. The left column (sidebar) is togglable; the right column holds the chat.

```
┌──────────────┬──────────────────────────────┐
│  History     │  [☰] "Lucas"  | StatusBadge  │  ← header
│  ──────────  ├──────────────────────────────┤
│  Task A      │  ScrollArea (flex-1)          │
│  Task B      │    empty state or messages    │
│  …           ├──────────────────────────────┤
│  [✏ New]     │  ChatInput                    │
└──────────────┴──────────────────────────────┘
```

On mount, `listTasks()` populates the sidebar. Clicking a task calls `getHistory(id)` → `buildMessages(entries)` → `loadTask(id, messages)`, loading its history into the chat and making it immediately resumable. Clicking the pencil icon (or the `☰` toggle removes/shows the sidebar) calls `newChat()` to reset to an empty state.

Auto-scrolls to the bottom whenever `messages` changes.

### `ChatMessage` (`src/components/ChatMessage.tsx`)

Renders one message bubble.

- **User** — right-aligned, dark neutral bubble, plain text (`whitespace-pre-wrap`).
- **Assistant** — left-aligned, light neutral bubble, rendered as Markdown via `react-markdown`. Appends a blinking cursor while `status === 'streaming'`.
- **Error** — red tint + `AlertCircle` icon regardless of role.
- **Tool activity** — shown below the assistant text behind a toggle button (`ChevronDown` icon + count label). When expanded, each item has a pulsing neutral dot while `done === false`, green dot when complete, with tool name bolded before the description.

### `ChatInput` (`src/components/ChatInput.tsx`)

Textarea that auto-resizes up to ~6 lines. Submits on `Enter`; `Shift+Enter` inserts a newline. Both the send button and Enter submit are disabled while `sending === true` or the input is empty/whitespace-only.

### `HistorySidepanel` (`src/components/HistorySidepanel.tsx`)

Left sidebar showing the authenticated user's task history. Props:

| Prop           | Type                   | Description                                         |
| -------------- | ---------------------- | --------------------------------------------------- |
| `tasks`        | `TaskSummary[]`        | List from `listTasks()`                             |
| `activeTaskId` | `string \| null`       | Highlights the currently loaded task                |
| `onSelectTask` | `(id: string) => void` | Called when a task row is clicked                   |
| `onNewChat`    | `() => void`           | Called when the pencil (new-chat) button is clicked |

Dates are formatted as time (`10:30 AM`) for today, `Yesterday` for yesterday, and `Month Day` for older entries.

### `StatusBadge` (`src/components/StatusBadge.tsx`)

Renders nothing when `idle`. Otherwise shows a colored dot + label:

| State          | Dot             | Label               |
| -------------- | --------------- | ------------------- |
| `provisioning` | pulsing neutral | Starting workspace… |
| `running`      | green           | Connected           |
| `error`        | red             | Connection error    |

## Configuration

### Vite proxy (`vite.config.ts`)

```ts
server: {
  proxy: {
    '/api': { target: 'http://localhost:8091', changeOrigin: true }
  }
}
```

All `/api/*` requests from the browser are forwarded to the Go backend. `VITE_API_BASE` can be left unset in dev.

### Environment variables

| Variable        | Default | Description                                                                                                               |
| --------------- | ------- | ------------------------------------------------------------------------------------------------------------------------- |
| `VITE_API_BASE` | `''`    | Backend base URL. Leave empty when using the Vite proxy. Set to `http://localhost:8091` for environments without a proxy. |

### Path alias

`@/*` maps to `src/*`, configured in `tsconfig.app.json` and `vite.config.ts`.

## Tooling

- **TypeScript** — strict mode, `moduleResolution: bundler`, `noUncheckedSideEffectImports`.
- **ESLint** — flat config (`eslint.config.js`) with `typescript-eslint`, `eslint-plugin-react-hooks`, `eslint-plugin-react-refresh`. The `react-refresh` export warning is suppressed for `src/components/ui/` (shadcn components intentionally export both component and variant helpers).
- **shadcn** — configured in `components.json` with neutral base color and `cssVariables: true`. Add new components with `npx shadcn@latest add <name>`.
