import type { AgentMetadataEntry, SessionEntry, UserEntry, AssistantEntry } from '@/types/session'
import type { Message, AnsweredQuestion, SubagentMessage, SubagentTrace, ThinkingBlock, ToolUseBlock } from '@/types'

function userEntryToMessage(entry: UserEntry): Message {
  const content = entry.message.content
  let text = ''
  if (typeof content === 'string') {
    text = content
  } else if (Array.isArray(content)) {
    text = content
      .filter((b): b is { type: 'text'; text: string } => b.type === 'text')
      .map(b => b.text)
      .join('')
  }
  return { id: entry.uuid, role: 'user', text, status: 'done' }
}

function assistantEntryToMessage(
  entry: AssistantEntry,
  toolResultMap: Map<string, AnsweredQuestion>,
  subagentTraceMap: Map<string, SubagentTrace>,
): Message {
  const content = entry.message.content
  const text = content
    .filter(b => b.type === 'text')
    .map(b => (b as { type: 'text'; text: string }).text)
    .join('')
  const toolUseBlocks: ToolUseBlock[] = content
    .filter(b => b.type === 'tool_use')
    .map(b => {
      const tb = b as { type: 'tool_use'; id: string; name: string; input: unknown }
      const block: ToolUseBlock = { id: tb.id, name: tb.name, input: tb.input as Record<string, unknown> }
      if (tb.name === 'Agent') {
        const trace = subagentTraceMap.get(tb.id)
        if (trace) block.subagentTrace = trace
      }
      return block
    })

  const thinkingBlocks: ThinkingBlock[] = content
    .filter(b => b.type === 'thinking')
    .map(b => ({ thinking: (b as { type: 'thinking'; thinking: string }).thinking }))

  const answeredQuestions: AnsweredQuestion[] = []
  for (const block of content) {
    if (block.type === 'tool_use' && (block as { name?: string }).name === 'AskUserQuestion') {
      const result = toolResultMap.get((block as { id: string }).id)
      if (result && result.questions.length > 0) {
        answeredQuestions.push(result)
      }
    }
  }

  return {
    id: entry.uuid,
    role: 'assistant',
    text,
    status: 'done',
    toolUseBlocks,
    ...(thinkingBlocks.length > 0 ? { thinkingBlocks } : {}),
    ...(answeredQuestions.length > 0 ? { answeredQuestions } : {}),
  }
}

function isToolResultOnlyEntry(entry: UserEntry): boolean {
  const content = entry.message.content
  if (!Array.isArray(content)) return false
  const hasText = content.some(b => (b as { type: string }).type === 'text')
  const hasToolResult = content.some(b => (b as { type: string }).type === 'tool_result')
  return hasToolResult && !hasText
}

function buildSubagentMessages(entries: AssistantEntry[]): SubagentMessage[] {
  return entries.map(entry => {
    const content = entry.message.content
    const text = content
      .filter(b => b.type === 'text')
      .map(b => (b as { type: 'text'; text: string }).text)
      .join('')
    const toolUseBlocks: ToolUseBlock[] = content
      .filter(b => b.type === 'tool_use')
      .map(b => {
        const tb = b as { type: 'tool_use'; id: string; name: string; input: unknown }
        return { id: tb.id, name: tb.name, input: tb.input as Record<string, unknown> }
      })
    return {
      id: entry.uuid,
      role: 'assistant' as const,
      text,
      ...(toolUseBlocks.length > 0 ? { toolUseBlocks } : {}),
    }
  })
}

/** Convert raw session entries from the history API into renderable Message objects. */
export function buildMessages(entries: SessionEntry[]): Message[] {
  // ── Separate main chain from sidechain ──────────────────────────────────────
  const mainEntries: (UserEntry | AssistantEntry)[] = []
  const sidechainGroups = new Map<string, AssistantEntry[]>()

  for (const entry of entries) {
    if (entry.type !== 'user' && entry.type !== 'assistant') continue
    const e = entry as UserEntry | AssistantEntry
    if (!e.isSidechain) {
      mainEntries.push(e)
    } else if (entry.type === 'assistant') {
      const ae = entry as AssistantEntry
      const id = ae.agentId
      if (id) {
        const group = sidechainGroups.get(id)
        if (group) {
          group.push(ae)
        } else {
          sidechainGroups.set(id, [ae])
        }
      }
    }
  }

  // ── Build Map<agentId, SubagentMessage[]> ───────────────────────────────────
  const sidechainMsgMap = new Map<string, SubagentMessage[]>()
  for (const [agentId, assistantEntries] of sidechainGroups) {
    sidechainMsgMap.set(agentId, buildSubagentMessages(assistantEntries))
  }

  // ── Collect agent_metadata entries (description lookup) ────────────────────
  const agentMetaByType = new Map<string, string>()
  for (const entry of entries) {
    if (entry.type === 'agent_metadata') {
      const meta = entry as AgentMetadataEntry
      agentMetaByType.set(meta.agentType, meta.description)
    }
  }

  // ── Build toolUseId → SubagentTrace from main user tool_result entries ──────
  const subagentTraceMap = new Map<string, SubagentTrace>()
  for (const entry of mainEntries) {
    if (entry.type !== 'user') continue
    const userEntry = entry as UserEntry
    const result = userEntry.tool_use_result
    if (!result?.agentId) continue
    const content = userEntry.message.content
    if (!Array.isArray(content)) continue
    for (const block of content) {
      const b = block as { type: string; tool_use_id?: string }
      if (b.type !== 'tool_result' || !b.tool_use_id) continue
      const agentId = result.agentId!
      const summary = result.content?.[0]?.text ?? ''
      const agentType = result.agentType ?? ''
      const description = agentMetaByType.get(agentType) ?? ''
      const messages = sidechainMsgMap.get(agentId) ?? []
      subagentTraceMap.set(b.tool_use_id, {
        agentType,
        description,
        summary,
        totalTokens: result.totalTokens,
        messages,
      })
    }
  }

  // ── Build AskUserQuestion toolResultMap ─────────────────────────────────────
  const toolResultMap = new Map<string, AnsweredQuestion>()
  for (const entry of mainEntries) {
    if (entry.type !== 'user') continue
    const userEntry = entry as UserEntry
    if (!userEntry.tool_use_result?.questions?.length) continue
    const content = userEntry.message.content
    if (!Array.isArray(content)) continue
    for (const block of content) {
      const b = block as { type: string; tool_use_id?: string }
      if (b.type === 'tool_result' && b.tool_use_id && userEntry.tool_use_result) {
        toolResultMap.set(b.tool_use_id, {
          questions: userEntry.tool_use_result.questions as AnsweredQuestion['questions'],
          answers: (userEntry.tool_use_result.answers ?? {}) as Record<string, string | string[]>,
        })
      }
    }
  }

  // ── Build main messages (isSidechain: false only) ───────────────────────────
  return mainEntries
    .filter(e => {
      if (e.type === 'user') return !isToolResultOnlyEntry(e as UserEntry)
      return true
    })
    .map(e =>
      e.type === 'user'
        ? userEntryToMessage(e as UserEntry)
        : assistantEntryToMessage(e as AssistantEntry, toolResultMap, subagentTraceMap),
    )
}
