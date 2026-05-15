export type Role = 'user' | 'assistant'

export type MessageStatus = 'streaming' | 'done' | 'error' | 'requesting' | 'asking'

export interface ThinkingBlock {
  thinking: string
}

export interface SubagentMessage {
  id: string
  role: 'user' | 'assistant'
  text: string
  toolUseBlocks?: ToolUseBlock[]
}

export interface SubagentTrace {
  agentType: string
  description: string
  summary: string
  totalTokens?: number
  messages: SubagentMessage[]
}

export interface ToolUseBlock {
  id: string
  name: string
  input: Record<string, unknown>
  subagentTrace?: SubagentTrace
}

export interface PermissionRequest {
  toolName: string
  toolInput: Record<string, unknown>
  toolUseId: string
  blockedPath?: string | null
  decisionReason?: string | null
}

export interface QuestionOption {
  label: string
  description: string
}

export interface Question {
  question: string
  header: string
  options: QuestionOption[]
  multiSelect: boolean
}

export interface AnsweredQuestion {
  questions: Question[]
  answers: Record<string, string | string[]>
}

export interface Message {
  id: string
  role: Role
  text: string
  status: MessageStatus
  toolActivity?: ToolActivity[]
  toolUseBlocks?: ToolUseBlock[]
  thinkingBlocks?: ThinkingBlock[]
  permissionRequest?: PermissionRequest
  pendingQuestions?: Question[]
  answeredQuestions?: AnsweredQuestion[]
  attachments?: { name: string; blob: Blob }[]
}

export interface ToolActivity {
  description: string
  toolName?: string
  done: boolean
}

export type SandboxState = 'idle' | 'provisioning' | 'running' | 'error'
