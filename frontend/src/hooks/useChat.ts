import { useState, useCallback, useRef } from 'react'
import { createTask, sendMessage as apiSendMessage, respondToPermission, respondToQuestion as apiRespondToQuestion } from '@/api/client'
import type { Message, PermissionRequest, Question, SandboxState, ToolActivity, ToolUseBlock } from '@/types'

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
      if (line.startsWith('event:')) {
        currentEvent = line.slice(6).trim()
      } else if (line.startsWith('data:')) {
        const data = JSON.parse(line.slice(5).trim()) as Record<string, unknown>
        yield { event: currentEvent, data }
        currentEvent = ''
      }
    }
  }
}

function makeId() {
  return Math.random().toString(36).slice(2)
}

export function useChat(username: string, onSessionCompleted?: () => void) {
  const [messages, setMessages] = useState<Message[]>([])
  const [taskId, setTaskId] = useState<string | null>(null)
  const [cwd, setCwd] = useState<string | null>(null)
  const [sandboxState, setSandboxState] = useState<SandboxState>('idle')
  const [sending, setSending] = useState(false)
  const currentAssistantMsgIdRef = useRef<string | null>(null)

  const approvePermission = useCallback(async (approved: boolean) => {
    if (!taskId) return
    const msgId = currentAssistantMsgIdRef.current
    await respondToPermission(taskId, approved ? 'allow' : 'deny')
    if (msgId) {
      setMessages(prev =>
        prev.map(m => m.id === msgId ? { ...m, status: 'streaming', permissionRequest: undefined } : m)
      )
    }
  }, [taskId])

  const answerQuestion = useCallback(async (answers: Record<string, string | string[]>) => {
    if (!taskId) return
    const msgId = currentAssistantMsgIdRef.current
    await apiRespondToQuestion(taskId, answers)
    if (msgId) {
      setMessages(prev =>
        prev.map(m => m.id === msgId ? { ...m, status: 'streaming', pendingQuestions: undefined } : m)
      )
    }
  }, [taskId])

  const sendMessage = useCallback(async (prompt: string) => {
    setSending(true)

    let id = taskId
    if (!id) {
      id = await createTask(username)
      setTaskId(id)
    }

    const userMsgId = makeId()
    const assistantMsgId = makeId()
    currentAssistantMsgIdRef.current = assistantMsgId

    setMessages(prev => [
      ...prev,
      { id: userMsgId, role: 'user', text: prompt, status: 'done' },
      { id: assistantMsgId, role: 'assistant', text: '', status: 'streaming', toolActivity: [], toolUseBlocks: [] },
    ])

    setSandboxState('provisioning')

    try {
      const response = await apiSendMessage(id, prompt)

      if (!response.ok) {
        setMessages(prev =>
          prev.map(m => m.id === assistantMsgId ? { ...m, status: 'error' } : m)
        )
        setSandboxState('error')
        setSending(false)
        return
      }

      for await (const { event, data } of parseSSE(response)) {
        switch (event) {
          case 'session.init': {
            setSandboxState('running')
            const initData = data as { cwd?: string }
            if (initData.cwd) setCwd(initData.cwd)
            break
          }
          case 'message.assistant': {
            const msgData = data as {
              text?: string
              message?: { content?: Array<{ type: string; id?: string; name?: string; input?: Record<string, unknown> }> }
            }
            const text = msgData.text ?? ''
            const toolUseBlocks: ToolUseBlock[] = (msgData.message?.content ?? [])
              .filter(b => b.type === 'tool_use')
              .map(b => ({ id: b.id!, name: b.name!, input: b.input ?? {} }))

            setMessages(prev =>
              prev.map(m => {
                if (m.id !== assistantMsgId) return m
                return {
                  ...m,
                  text: text ? m.text + text : m.text,
                  toolUseBlocks: toolUseBlocks.length > 0
                    ? [...(m.toolUseBlocks ?? []), ...toolUseBlocks]
                    : m.toolUseBlocks,
                }
              })
            )
            break
          }
          case 'permission.requested': {
            const d = data as {
              toolName: string
              toolInput: Record<string, unknown>
              toolUseId: string
              blockedPath?: string | null
              decisionReason?: string | null
            }
            const permissionRequest: PermissionRequest = {
              toolName: d.toolName,
              toolInput: d.toolInput,
              toolUseId: d.toolUseId,
              blockedPath: d.blockedPath,
              decisionReason: d.decisionReason,
            }
            setMessages(prev =>
              prev.map(m => m.id !== assistantMsgId ? m : { ...m, status: 'requesting', permissionRequest })
            )
            break
          }
          case 'question.asked': {
            const d = data as { questions: Question[] }
            setMessages(prev =>
              prev.map(m => m.id !== assistantMsgId ? m : { ...m, status: 'asking', pendingQuestions: d.questions })
            )
            break
          }
          case 'session.status': {
            const status = (data as { status?: string }).status
            if (status === 'idle') {
              setMessages(prev =>
                prev.map(m => m.id === assistantMsgId ? { ...m, status: 'done' } : m)
              )
            }
            break
          }
          case 'task.started': {
            const activity: ToolActivity = {
              description: (data as { description?: string }).description ?? '',
              toolName: undefined,
              done: false,
            }
            setMessages(prev =>
              prev.map(m =>
                m.id === assistantMsgId
                  ? { ...m, toolActivity: [...(m.toolActivity ?? []), activity] }
                  : m
              )
            )
            break
          }
          case 'task.progress': {
            const description = (data as { description?: string }).description ?? ''
            const toolName = (data as { lastToolName?: string }).lastToolName
            setMessages(prev =>
              prev.map(m => {
                if (m.id !== assistantMsgId) return m
                const activities = [...(m.toolActivity ?? [])]
                if (activities.length > 0) {
                  activities[activities.length - 1] = { ...activities[activities.length - 1], description, toolName }
                }
                return { ...m, toolActivity: activities }
              })
            )
            break
          }
          case 'result': {
            setMessages(prev =>
              prev.map(m => m.id === assistantMsgId ? { ...m, status: 'done' } : m)
            )
            break
          }
          case 'session.completed': {
            setMessages(prev =>
              prev.map(m => {
                if (m.id !== assistantMsgId) return m
                const activities = (m.toolActivity ?? []).map(a => ({ ...a, done: true }))
                return { ...m, status: 'done', toolActivity: activities }
              })
            )
            setSending(false)
            onSessionCompleted?.()
            break
          }
          case 'error': {
            setMessages(prev =>
              prev.map(m => m.id === assistantMsgId ? { ...m, status: 'error' } : m)
            )
            setSandboxState('error')
            setSending(false)
            break
          }
        }
      }
    } catch {
      setMessages(prev =>
        prev.map(m => m.id === assistantMsgId ? { ...m, status: 'error' } : m)
      )
      setSandboxState('error')
      setSending(false)
    }
  }, [taskId, username])

  const newChat = useCallback(() => {
    setTaskId(null)
    setMessages([])
    setCwd(null)
    setSandboxState('idle')
    setSending(false)
    currentAssistantMsgIdRef.current = null
  }, [])

  const loadTask = useCallback((tid: string, historyMessages: Message[], taskCwd?: string) => {
    setTaskId(tid)
    setMessages(historyMessages)
    if (taskCwd) setCwd(taskCwd)
    setSandboxState('idle')
    setSending(false)
    currentAssistantMsgIdRef.current = null
  }, [])

  return { messages, taskId, cwd, sandboxState, sending, sendMessage, approvePermission, answerQuestion, newChat, loadTask }
}
