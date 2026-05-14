import { useCallback, useEffect, useRef, useState } from 'react'
import { PanelLeft, Blocks, FolderOpen, Settings } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { ChatInput } from '@/components/ChatInput'
import { ChatMessage } from '@/components/ChatMessage'
import { HistorySidepanel } from '@/components/HistorySidepanel'
import { NewTaskDialog } from '@/components/NewTaskDialog'
import { StatusBadge } from '@/components/StatusBadge'
import { WorkspacePanel } from '@/components/WorkspacePanel'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useChat } from '@/hooks/useChat'
import { createTask, deleteTask, getHistory, getTask, listTasks } from '@/api/client'
import type { TaskSummary } from '@/api/client'
import { getAuthUsername } from '@/lib/auth'
import { buildMessages } from '@/lib/chainBuilder'
import { cn } from '@/lib/utils'

export function ChatPage() {
  const navigate = useNavigate()
  const username = getAuthUsername() ?? ''

  const [workspaceOpen, setWorkspaceOpen] = useState(false)
  const [refreshToken, setRefreshToken] = useState(0)

  const handleSessionCompleted = useCallback(() => {
    setRefreshToken(t => t + 1)
  }, [])

  const { messages, taskId, cwd, sandboxState, sending, sendMessage, approvePermission, answerQuestion, newChat, loadTask, startTask } =
    useChat(username, handleSessionCompleted)

  const [sidebarOpen, setSidebarOpen] = useState(true)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [sidebarWidth, setSidebarWidth] = useState(240)
  const [workspaceWidth, setWorkspaceWidth] = useState(288)
  const [resizing, setResizing] = useState(false)
  const [tasks, setTasks] = useState<TaskSummary[]>([])
  const bottomRef = useRef<HTMLDivElement>(null)

  const refreshTasks = useCallback(() => {
    listTasks().then(setTasks).catch(() => {})
  }, [])

  useEffect(() => {
    refreshTasks()
  }, [refreshTasks])

  useEffect(() => {
    if (taskId) refreshTasks()
  }, [taskId, refreshTasks])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  const handleSelectTask = useCallback(async (id: string) => {
    try {
      const [entries, task] = await Promise.all([getHistory(id), getTask(id)])
      const msgs = buildMessages(entries)
      loadTask(id, msgs, task.cwd)
    } catch {
      // silently ignore — history unavailable for this task
    }
  }, [loadTask])

  const handleNewChat = useCallback(() => {
    setDialogOpen(true)
  }, [])

  const handleDialogCreate = useCallback(async ({ title, gitUrl }: { title?: string; gitUrl?: string }) => {
    const id = await createTask(username, { title, gitUrl })
    startTask(id)
    refreshTasks()
    setDialogOpen(false)
  }, [username, startTask, refreshTasks])

  const handleDeleteTask = useCallback(async (id: string) => {
    await deleteTask(id)
    if (taskId === id) newChat()
    refreshTasks()
  }, [taskId, newChat, refreshTasks])

  const handleResizeStart = (side: 'left' | 'right', startWidth: number) => (e: React.MouseEvent) => {
    e.preventDefault()
    setResizing(true)
    document.body.style.cursor = 'col-resize'
    const startX = e.clientX
    const onMove = (ev: MouseEvent) => {
      const delta = ev.clientX - startX
      const next = Math.max(160, Math.min(480, startWidth + (side === 'left' ? delta : -delta)))
      if (side === 'left') setSidebarWidth(next)
      else setWorkspaceWidth(next)
    }
    const onUp = () => {
      setResizing(false)
      document.body.style.cursor = ''
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
  }

  return (
    <div className={cn('flex h-[100dvh]', resizing && 'select-none')}>
      <NewTaskDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        onCreate={handleDialogCreate}
      />
      {sidebarOpen && (
        <>
          <div style={{ width: sidebarWidth }} className="flex-shrink-0 h-full overflow-hidden">
            <HistorySidepanel
              tasks={tasks}
              activeTaskId={taskId}
              onSelectTask={handleSelectTask}
              onNewChat={handleNewChat}
              onDeleteTask={handleDeleteTask}
            />
          </div>
          <div
            className="w-1 flex-shrink-0 bg-neutral-200 hover:bg-blue-400 cursor-col-resize transition-colors"
            onMouseDown={handleResizeStart('left', sidebarWidth)}
          />
        </>
      )}

      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        <header className="flex items-center justify-between px-4 py-3 border-b border-neutral-200 shrink-0">
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
            <button
              onClick={() => navigate('/settings')}
              className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
              title="Settings"
            >
              <Settings size={16} />
            </button>
            <button
              onClick={() => setWorkspaceOpen(v => !v)}
              className={cn(
                'p-1.5 rounded hover:bg-neutral-100 transition-colors',
                workspaceOpen
                  ? 'text-neutral-700 bg-neutral-100'
                  : 'text-neutral-500 hover:text-neutral-700',
              )}
              title={workspaceOpen ? 'Close workspace' : 'Open workspace'}
            >
              <FolderOpen size={16} />
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

      {workspaceOpen && taskId && cwd && (
        <>
          <div
            className="w-1 flex-shrink-0 bg-neutral-200 hover:bg-blue-400 cursor-col-resize transition-colors"
            onMouseDown={handleResizeStart('right', workspaceWidth)}
          />
          <div style={{ width: workspaceWidth }} className="flex-shrink-0 h-full overflow-hidden">
            <WorkspacePanel
              taskId={taskId}
              cwd={cwd}
              refreshToken={refreshToken}
            />
          </div>
        </>
      )}
    </div>
  )
}
