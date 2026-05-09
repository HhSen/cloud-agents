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
