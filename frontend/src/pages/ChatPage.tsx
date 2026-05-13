import { useCallback, useEffect, useRef, useState } from 'react'
import { PanelLeft, Blocks } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { ChatInput } from '@/components/ChatInput'
import { ChatMessage } from '@/components/ChatMessage'
import { HistorySidepanel } from '@/components/HistorySidepanel'
import { StatusBadge } from '@/components/StatusBadge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useChat } from '@/hooks/useChat'
import { getHistory, listTasks } from '@/api/client'
import type { TaskSummary } from '@/api/client'
import { getAuthUsername } from '@/lib/auth'
import { buildMessages } from '@/lib/chainBuilder'
import { cn } from '@/lib/utils'

export function ChatPage() {
  const navigate = useNavigate()
  const username = getAuthUsername() ?? ''
  const { messages, taskId, sandboxState, sending, sendMessage, approvePermission, answerQuestion, newChat, loadTask } =
    useChat(username)

  const [sidebarOpen, setSidebarOpen] = useState(true)
  const [tasks, setTasks] = useState<TaskSummary[]>([])
  const bottomRef = useRef<HTMLDivElement>(null)

  const refreshTasks = useCallback(() => {
    listTasks().then(setTasks).catch(() => {})
  }, [])

  useEffect(() => {
    refreshTasks()
  }, [refreshTasks])

  // Refresh sidebar after each new task is created or a message is sent
  useEffect(() => {
    if (taskId) refreshTasks()
  }, [taskId, refreshTasks])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  const handleSelectTask = useCallback(async (id: string) => {
    try {
      const entries = await getHistory(id)
      const msgs = buildMessages(entries)
      loadTask(id, msgs)
    } catch {
      // silently ignore — history unavailable for this task
    }
  }, [loadTask])

  const handleNewChat = useCallback(() => {
    newChat()
  }, [newChat])

  return (
    <div className="flex h-[100dvh]">
      {sidebarOpen && (
        <HistorySidepanel
          tasks={tasks}
          activeTaskId={taskId}
          onSelectTask={handleSelectTask}
          onNewChat={handleNewChat}
        />
      )}

      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        <div className={cn('flex flex-col h-full w-full mx-auto', sidebarOpen ? 'max-w-2xl' : 'max-w-3xl')}>
          <header className="flex items-center justify-between px-4 py-3 border-b border-neutral-200">
            <div className="flex items-center gap-2">
              <button
                onClick={() => setSidebarOpen(v => !v)}
                className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
                title={sidebarOpen ? 'Close sidebar' : 'Open sidebar'}
              >
                <PanelLeft size={16} />
              </button>
              <span className="font-semibold text-sm">Lucas</span>
            </div>
            <div className="flex items-center gap-1">
                <button
                  onClick={() => navigate('/resources')}
                  className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
                  title="Manage resources"
                >
                  <Blocks size={16} />
                </button>
                <StatusBadge state={sandboxState} />
              </div>
          </header>

          <ScrollArea className="flex-1">
            <div className="p-4 space-y-4">
              {messages.length === 0 ? (
                <div className="flex items-center justify-center h-full min-h-[60dvh]">
                  <p className="text-neutral-400 text-sm">What can I help you with?</p>
                </div>
              ) : (
                messages.map(msg => (
                  <ChatMessage
                    key={msg.id}
                    message={msg}
                    onApprovePermission={msg.status === 'requesting' ? approvePermission : undefined}
                    onAnswerQuestion={msg.status === 'asking' ? answerQuestion : undefined}
                  />
                ))
              )}
              <div ref={bottomRef} />
            </div>
          </ScrollArea>

          <ChatInput onSend={sendMessage} disabled={sending} />
        </div>
      </div>
    </div>
  )
}
