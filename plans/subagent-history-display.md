# Subagent History Display

## Problem

The history replay (`chainBuilder.ts`) currently passes all entries — both main-agent (`isSidechain: false`) and subagent (`isSidechain: true`) — through the same flat filter. The result is a single linear `Message[]` where orchestrator reasoning and subagent internal tool calls/responses are interleaved. Users cannot tell which agent said what.

## Root Cause

`buildMessages` filters only on `type === 'user' | 'assistant'` and skips `isToolResultOnlyEntry` entries. It never looks at `isSidechain`.

## Proposed Design

**Primary principle:** The main chat thread shows only the orchestrator's conversation (`isSidechain: false`). Each `Agent(...)` tool use becomes a collapsible subagent card in-place, containing the full inner conversation.

```
User message
└─ Assistant message (orchestrator)
   ├─ [text]: "Let me take a quick look…"
   └─ [Agent card — collapsed by default]
        Header: Explore · "Quick project overview"
        Summary: "Agent Server is a Go-based multi-agent…"
        ▶ Expand to see 4 steps
        ─────── expanded ───────
        ├─ Assistant (Explore): "I'll explore…"
        │  └─ Bash: ls -la /workspace/…
        ├─ Assistant (Explore): "Now let me check README…"
        │  └─ Read: README.md
        └─ Assistant (Explore): [final summary text]
```

No live-streaming changes are required; this only affects history replay.

## Data Model (session entries)

Key relationships in the raw history JSON:

| Entry | Key fields |
|---|---|
| Main agent `assistant` with Agent call | `isSidechain: false`, `message.content[].type === 'tool_use'`, `name === 'Agent'`, `id = toolUseId` |
| Main agent `user` tool result | `isSidechain: false`, `message.content[].type === 'tool_result'`, `tool_use_id = toolUseId`, `toolUseResult.agentId`, `toolUseResult.content[0].text` (summary) |
| Subagent entries | `isSidechain: true`, `agentId` on AssistantEntry |
| `agent_metadata` | `type: 'agent_metadata'`, no uuid, `agentType`, `description` |

Connection path:
```
toolUseId (Agent call in main assistant)
  → toolUseId (in user tool_result entry) → toolUseResult.agentId
    → sidechain entries with matching agentId
```

## Implementation Plan

### 1. Update `types/session.ts`

Add fields observed in the history but not yet typed:

```ts
// On AssistantEntry
agentId?: string         // present when isSidechain: true (subagent messages)
attributionAgent?: string // agent type label on sidechain entries

// On UserEntry (extend existing envelope)
toolUseResult?: {
  // existing AskUserQuestion shape:
  questions?: ...
  answers?: ...
  // new: agent tool result shape:
  status?: string
  agentId?: string
  agentType?: string
  content?: Array<{ type: 'text'; text: string }>
  totalTokens?: number
  totalToolUseCount?: number
}
sourceToolAssistantUUID?: string  // uuid of assistant entry that called the tool

// New entry type
export interface AgentMetadataEntry {
  type: 'agent_metadata'
  agentType: string
  description: string
}

// Add AgentMetadataEntry to SessionEntry union
```

### 2. Update `types.ts`

```ts
export interface SubagentMessage {
  id: string
  role: 'user' | 'assistant'
  text: string
  toolUseBlocks?: ToolUseBlock[]
}

export interface SubagentTrace {
  agentType: string       // e.g. "Explore"
  description: string     // e.g. "Quick project overview"
  summary: string         // toolUseResult.content[0].text
  totalTokens?: number
  messages: SubagentMessage[]
}

// Extend ToolUseBlock
export interface ToolUseBlock {
  id: string
  name: string
  input: Record<string, unknown>
  subagentTrace?: SubagentTrace  // only present when name === 'Agent'
}
```

### 3. Rewrite `chainBuilder.ts`

New `buildMessages` algorithm:

```
1. Separate entries:
   - mainEntries = entries where isSidechain === false
   - sidechainGroups = Map<agentId, AssistantEntry[]>
     (collect isSidechain: true AssistantEntry grouped by agentId)

2. Build subagent traces:
   For each (agentId, entries) in sidechainGroups:
     Build SubagentMessage[] from the entries (text + tool_use blocks only)
     → Map<agentId, SubagentMessage[]>

3. Build toolUseId → SubagentTrace map:
   For each mainEntry of type 'user' with toolUseResult.agentId:
     toolUseId = message.content[i].tool_use_id
     agentId   = toolUseResult.agentId
     summary   = toolUseResult.content[0].text
     agentType = toolUseResult.agentType
     messages  = sidechainMsgMap.get(agentId)
     → Map<toolUseId, SubagentTrace>

4. Build main messages (isSidechain: false only):
   - Filter: same as current (skip tool-result-only user entries)
   - For AssistantEntry: when building ToolUseBlock for Agent tool uses,
     look up subagentTraceMap.get(block.id) and attach as subagentTrace
   - For AssistantEntry: also need agent_metadata description:
     find AgentMetadataEntry near the tool call, use its description if toolUseResult
     description is missing
```

The existing `toolResultMap` (for AskUserQuestion) continues to work unchanged — it only looks at `tool_use_result.questions`.

### 4. Update `ChatMessage.tsx`

Add `AgentToolUseCard` component:

```tsx
function AgentToolUseCard({ block }: { block: ToolUseBlock }) {
  // block.subagentTrace is guaranteed present
  const [expanded, setExpanded] = useState(false)
  const trace = block.subagentTrace!
  const stepCount = trace.messages.filter(m => m.role === 'assistant').length

  return (
    <div className="rounded-lg border border-violet-200 bg-violet-50 overflow-hidden text-xs">
      {/* Header: agent type + description */}
      <div className="flex items-center gap-1.5 px-2.5 py-1.5 bg-violet-100 border-b border-violet-200">
        <Bot className="h-3 w-3 text-violet-500 flex-shrink-0" />
        <span className="font-semibold text-violet-800">{trace.agentType}</span>
        {trace.description && (
          <span className="text-violet-600 truncate">— {trace.description}</span>
        )}
        {trace.totalTokens && (
          <span className="ml-auto text-violet-400">{trace.totalTokens.toLocaleString()} tokens</span>
        )}
      </div>

      {/* Summary (always visible) */}
      {trace.summary && (
        <div className="px-2.5 py-1.5 text-neutral-600 line-clamp-3">
          {trace.summary}
        </div>
      )}

      {/* Expand toggle */}
      {trace.messages.length > 0 && (
        <button
          onClick={() => setExpanded(e => !e)}
          className="flex items-center gap-0.5 px-2.5 pb-1.5 text-violet-500 hover:text-violet-700"
        >
          <ChevronRight className={cn('h-3 w-3 transition-transform', expanded && 'rotate-90')} />
          {stepCount} step{stepCount !== 1 ? 's' : ''}
        </button>
      )}

      {/* Nested subagent conversation */}
      {expanded && (
        <div className="border-t border-violet-200 px-2.5 py-2 space-y-2">
          {trace.messages.map((msg) => (
            <SubagentMessageRow key={msg.id} msg={msg} />
          ))}
        </div>
      )}
    </div>
  )
}
```

`SubagentMessageRow` renders a compact version: assistant messages show text + tool_use badges inline (no full ToolUseCard recursion needed).

Update `ToolUseCard` dispatcher in `ChatMessage.tsx`:
```tsx
// In the existing ToolUseCard or the place that renders toolUseBlocks:
if (block.name === 'Agent' && block.subagentTrace) {
  return <AgentToolUseCard block={block} />
}
// otherwise render existing ToolUseCard
```

## Out of Scope

- Live SSE streaming: subagent events during an active session are already handled by `task.started`/`task.progress` tool activity. This plan is history-replay only.
- Multi-level nesting (subagent spawning sub-subagents): the plan handles one level; deeper nesting can be added later.

## Files Changed

| File | Change |
|---|---|
| `frontend/src/types/session.ts` | Add `agentId`, `attributionAgent`, `sourceToolAssistantUUID`, `toolUseResult` agent fields, `AgentMetadataEntry` |
| `frontend/src/types.ts` | Add `SubagentMessage`, `SubagentTrace`; extend `ToolUseBlock` with `subagentTrace?` |
| `frontend/src/lib/chainBuilder.ts` | Rewrite `buildMessages` to separate main/sidechain, build traces, attach to Agent blocks |
| `frontend/src/components/ChatMessage.tsx` | Add `AgentToolUseCard`, `SubagentMessageRow`; dispatch Agent blocks to new card |
