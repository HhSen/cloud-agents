import type { SessionEntry, UserEntry, AssistantEntry } from '@/types/session'
import type { Message, AnsweredQuestion, ToolUseBlock } from '@/types'

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
      return { id: tb.id, name: tb.name, input: tb.input as Record<string, unknown> }
    })

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

/** Convert raw session entries from the history API into renderable Message objects. */
export function buildMessages(entries: SessionEntry[]): Message[] {
  // Build a lookup from tool_use_id → answered question data from AskUserQuestion responses
  const toolResultMap = new Map<string, AnsweredQuestion>()
  for (const entry of entries) {
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

  return entries
    .filter((e): e is UserEntry | AssistantEntry => {
      if (e.type === 'user') return !isToolResultOnlyEntry(e as UserEntry)
      return e.type === 'assistant'
    })
    .map(e =>
      e.type === 'user'
        ? userEntryToMessage(e as UserEntry)
        : assistantEntryToMessage(e as AssistantEntry, toolResultMap),
    )
}
