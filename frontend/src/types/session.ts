// Types for session entries returned by GET /api/tasks/:id/history.
//
// The backend passes through raw NDJSON lines from S3 without re-serializing,
// so the shape matches what Claude Code writes to disk. The disk format differs
// from the SDK's streaming types in one key way: envelope fields use camelCase
// (sessionId, parentUuid) whereas the SDK's SDKMessage uses snake_case
// (session_id, parent_tool_use_id). The message *payload* fields (BetaMessage,
// MessageParam) are identical and we reference them via the SDK import.

import type { SDKAssistantMessage, SDKUserMessage, SDKResultMessage, SDKSystemMessage } from '@anthropic-ai/claude-agent-sdk'

// ─── Shared disk envelope ─────────────────────────────────────────────────────
// Fields present on every conversation entry written to disk.

interface DiskEnvelope {
  uuid: string
  parentUuid: string | null
  sessionId: string
  timestamp: string
  isSidechain: boolean

  // Sandbox origin (set by the SDK on every entry it writes)
  userType?: 'external' | 'internal' | string
  entrypoint?: 'sdk-ts' | string
  version?: string
  cwd?: string
  gitBranch?: string

  // Chain / display control
  isMeta?: boolean
  isCompactSummary?: boolean
  teamName?: string

  // Session-level metadata (last-wins across entries)
  customTitle?: string
  aiTitle?: string
  lastPrompt?: string
  summary?: string
}

// ─── type: "user" ─────────────────────────────────────────────────────────────

export type UserEntry = DiskEnvelope & {
  type: 'user'
  promptId: string
  permissionMode: 'default' | 'auto' | 'bypassPermissions' | 'acceptEdits' | string
  // message payload is identical to the SDK streaming type
  message: SDKUserMessage['message']
  // Present on tool_result user entries — covers both AskUserQuestion and Agent tool results
  tool_use_result?: {
    // AskUserQuestion result shape
    questions?: Array<{
      question: string
      header: string
      options: Array<{ label: string; description: string }>
      multiSelect: boolean
    }>
    answers?: Record<string, string | string[]>
    // Agent tool result shape
    status?: string
    agentId?: string
    agentType?: string
    content?: Array<{ type: 'text'; text: string }>
    totalTokens?: number
    totalToolUseCount?: number
  }
}

// ─── type: "assistant" ───────────────────────────────────────────────────────

export type AssistantEntry = DiskEnvelope & {
  type: 'assistant'
  // message payload is identical to the SDK streaming type (BetaMessage)
  message: SDKAssistantMessage['message']
  error?: SDKAssistantMessage['error']
  parent_tool_use_id?: string
  // Subagent-specific fields (present when isSidechain: true)
  agentId?: string
  attributionAgent?: string
  sourceToolAssistantUUID?: string
}

// ─── type: "attachment" ──────────────────────────────────────────────────────

export interface SkillListingAttachment {
  type: 'skill_listing'
  content: string
  skillCount: number
  isInitial: boolean
}

export type AttachmentEntry = DiskEnvelope & {
  type: 'attachment'
  attachment: SkillListingAttachment | { type: string; [key: string]: unknown }
}

// ─── type: "system" / "result" ───────────────────────────────────────────────
// These share their payload shape with the SDK types; the envelope differs.

export type SystemEntry = DiskEnvelope & Omit<SDKSystemMessage, 'session_id' | 'uuid'>
export type ResultEntry = DiskEnvelope & Omit<SDKResultMessage, 'session_id' | 'uuid'>

// ─── type: "progress" ────────────────────────────────────────────────────────

export type ProgressEntry = DiskEnvelope & {
  type: 'progress'
  [key: string]: unknown
}

// ─── Metadata-only types (no uuid, skipped by chain builder) ─────────────────

export interface TagEntry {
  type: 'tag'
  tag: string
  timestamp?: string
}

export interface AgentMetadataEntry {
  type: 'agent_metadata'
  agentType: string
  description: string
}

// ─── Sandbox-operational types (observed in practice, not in schema docs) ─────

export interface QueueOperationEntry {
  type: 'queue-operation'
  operation: 'enqueue' | 'dequeue'
  timestamp: string
  sessionId: string
}

export interface LastPromptEntry {
  type: 'last-prompt'
  lastPrompt: string
  leafUuid: string
  sessionId: string
}

// ─── Unions ───────────────────────────────────────────────────────────────────

/** Every entry type that may appear in the history response. */
export type SessionEntry =
  | UserEntry
  | AssistantEntry
  | AttachmentEntry
  | SystemEntry
  | ResultEntry
  | ProgressEntry
  | TagEntry
  | AgentMetadataEntry
  | QueueOperationEntry
  | LastPromptEntry

/** Entries with a uuid that participate in chain building. */
export type ConversationEntry = UserEntry | AssistantEntry | AttachmentEntry | ProgressEntry

/** Only the entries shown to users after chain reconstruction. */
export type VisibleEntry = UserEntry | AssistantEntry
