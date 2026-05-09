import { useState, useCallback } from 'react'
import { createConversation, sendMessage as apiSendMessage } from '@/api/client'
import type { Message, SandboxState, ToolActivity } from '@/types'

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

export function useChat() {
  const [messages, setMessages] = useState<Message[]>([])
  const [convId, setConvId] = useState<string | null>(null)
  const [sandboxState, setSandboxState] = useState<SandboxState>('idle')
  const [sending, setSending] = useState(false)

  const sendMessage = useCallback(async (prompt: string) => {
    setSending(true)

    let id = convId
    if (!id) {
      id = await createConversation()
      setConvId(id)
    }

    const userMsgId = makeId()
    const assistantMsgId = makeId()

    setMessages(prev => [
      ...prev,
      { id: userMsgId, role: 'user', text: prompt, status: 'done' },
      { id: assistantMsgId, role: 'assistant', text: '', status: 'streaming', toolActivity: [] },
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
            break
          }
          case 'message.assistant': {
            const text = (data as { text?: string }).text ?? ''
            if (text) {
              setMessages(prev =>
                prev.map(m =>
                  m.id === assistantMsgId ? { ...m, text: m.text + text } : m
                )
              )
            }
            break
          }
          case 'session.status': {
            if ((data as { status?: string }).status === 'idle') {
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
  }, [convId])

  return { messages, sandboxState, sending, sendMessage }
}
