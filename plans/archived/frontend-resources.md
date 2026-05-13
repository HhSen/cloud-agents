# Frontend: Resources Management Page

## Overview

Add a `/resources` page where authenticated users can manage their skills and MCP server resources. These are injected into every new sandbox at provision time.

---

## New Route

```
/resources   → ResourcesPage (protected)
```

Add to `App.tsx`:
```tsx
<Route path="/resources" element={<ProtectedRoute><ResourcesPage /></ProtectedRoute>} />
```

Navigation: add a "Resources" link (or icon button) in the `ChatPage` header alongside the existing sidebar toggle.

---

## API Client additions (`src/api/client.ts`)

```ts
export interface Resource {
  id: number
  kind: 'skill' | 'mcp'
  name: string
  ofs_path: string
  meta: Record<string, unknown>
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface CreateResourcePayload {
  kind: 'skill' | 'mcp'
  name: string
  content: string
  meta?: Record<string, unknown>
}

export interface UpdateResourcePayload {
  content?: string
  meta?: Record<string, unknown>
  is_active?: boolean
}

export async function listResources(): Promise<Resource[]>
export async function createResource(payload: CreateResourcePayload): Promise<Resource>
export async function updateResource(id: number, payload: UpdateResourcePayload): Promise<Resource>
export async function deleteResource(id: number): Promise<void>
```

All four calls include `authHeaders()`. `listResources` throws on non-2xx; `createResource` surfaces the error message from the server (validation errors). `deleteResource` ignores 404.

---

## File additions

```
frontend/src/
├── pages/
│   └── ResourcesPage.tsx        # new
├── components/
│   └── ResourceForm.tsx         # inline create/edit form
└── api/client.ts                # extended (not new)
```

---

## shadcn components to add

```bash
npx shadcn@latest add input switch tabs dialog
```

- `input` — name field in create form
- `switch` — active toggle per row
- `tabs` — "Skills" / "MCP Servers" tab switcher
- `dialog` — confirm-delete modal

---

## `ResourcesPage.tsx` design

```
┌──────────────────────────────────────────────────────┐
│  ← Back     Resources                                 │
├──────────────────────────────────────────────────────┤
│  [Skills]  [MCP Servers]                             │  ← Tabs
├──────────────────────────────────────────────────────┤
│  + Add Skill                                          │  ← per-tab add button
│                                                       │
│  ┌────────────────────────────────────────────────┐  │
│  │  my-search          [Active ●]  [Edit] [Delete] │  │
│  │  code-reviewer      [Active ●]  [Edit] [Delete] │  │
│  │  old-skill          [Inactive]  [Edit] [Delete] │  │
│  └────────────────────────────────────────────────┘  │
│                                                       │
│  [ResourceForm — shown inline below list when open]   │
└──────────────────────────────────────────────────────┘
```

State:
```ts
const [tab, setTab] = useState<'skill' | 'mcp'>('skill')
const [resources, setResources] = useState<Resource[]>([])
const [loading, setLoading] = useState(true)
const [formState, setFormState] = useState<'closed' | 'create' | number>('closed')
// number = editing resource id
const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
```

Load on mount: `listResources()` → store in state.  
Filter displayed list by `tab` value.

### Active toggle

`<Switch>` calls `updateResource(id, { is_active: !current })` inline — no separate save button.  
Optimistic update: flip the local state immediately, revert on error.

### Edit flow

Clicking "Edit" sets `formState` to the resource id. The form appears inline below the list, pre-populated with the resource's content/meta. Save calls `updateResource`.

### Delete flow

Clicking "Delete" sets `deleteTarget`. A `<Dialog>` asks "Delete {name}? This cannot be undone." Confirming calls `deleteResource(id)` and removes the row.

---

## `ResourceForm.tsx`

```
┌────────────────────────────────────────┐
│  Name: [_______________]               │  ← hidden on edit (name is immutable)
│  Content:                              │
│  [large <Textarea> for skill markdown  │
│   or JSON for MCP]                     │
│                                        │
│            [Cancel]  [Save]            │
└────────────────────────────────────────┘
```

Props:
```ts
interface ResourceFormProps {
  kind: 'skill' | 'mcp'
  initial?: { name: string; content: string }  // undefined = create mode
  onSave: (name: string, content: string) => Promise<void>
  onCancel: () => void
}
```

Validation (client-side):
- `name` must match `^[a-zA-Z0-9_-]+$` — show inline error if not
- For MCP: attempt `JSON.parse(content)` and show "Must be valid JSON" if it fails
- Disabled state on the Save button while the async call is in flight

---

## Navigation from ChatPage

In `ChatPage.tsx` header, add a link icon button (lucide `Settings2` or `Blocks`) next to the sidebar toggle that navigates to `/resources`. Use `useNavigate` from react-router-dom.

---

## Implementation order

1. Extend `api/client.ts` with the four resource functions and `Resource` type
2. Add shadcn components (`input`, `switch`, `tabs`, `dialog`)
3. Build `ResourceForm.tsx`
4. Build `ResourcesPage.tsx` (list + form + delete dialog)
5. Add route in `App.tsx`
6. Add nav button in `ChatPage.tsx` header

---

## Out of scope

- OFS content preview (read-only view of what's stored in S3)
- Resource reordering / priority
- Per-resource usage history
