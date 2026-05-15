import { Calendar, GitBranch, PenLine, Trash2 } from 'lucide-react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { cn } from '@/lib/utils'
import type { TaskSummary } from '@/api/client'

interface Props {
  tasks: TaskSummary[]
  activeTaskId: string | null
  onSelectTask: (id: string) => void
  onNewChat: () => void
  onDeleteTask: (id: string) => void
}

function repoName(url: string): string {
  const stripped = url.replace(/\.git$/, '')
  const last = stripped.split(/[/:]/g).filter(Boolean).pop() ?? url
  return last
}

function formatDate(dateStr: string): string {
  const date = new Date(dateStr)
  const now = new Date()
  if (date.toDateString() === now.toDateString()) {
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (date.toDateString() === yesterday.toDateString()) {
    return 'Yesterday'
  }
  return date.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

export function HistorySidepanel({ tasks, activeTaskId, onSelectTask, onNewChat, onDeleteTask }: Props) {
  return (
    <div className="w-full h-full flex flex-col bg-neutral-50">
      <div className="px-3 py-3 flex items-center justify-between border-b border-neutral-200">
        <span className="text-xs font-semibold text-neutral-500 uppercase tracking-wide">History</span>
        <button
          onClick={onNewChat}
          className="p-1.5 rounded hover:bg-neutral-200 text-neutral-500 hover:text-neutral-700 transition-colors"
          title="New chat"
        >
          <PenLine size={14} />
        </button>
      </div>

      <ScrollArea className="flex-1">
        <div className="py-1">
          {tasks.map(task => (
            <div
              key={task.id}
              className={cn(
                'group relative w-full text-left px-3 py-2.5 hover:bg-neutral-100 transition-colors cursor-pointer',
                activeTaskId === task.id && 'bg-neutral-100'
              )}
              onClick={() => onSelectTask(task.id)}
            >
              <div className={cn(
                'flex items-center gap-1 truncate text-sm text-neutral-700 pr-5',
                activeTaskId === task.id && 'font-medium text-neutral-900'
              )}>
                {task.git_url && (
                  <GitBranch size={11} className="shrink-0 text-neutral-400" />
                )}
                {task.schedule_id && !task.git_url && (
                  <Calendar size={11} className="shrink-0 text-blue-400" />
                )}
                <span className="truncate">{task.title || 'Untitled'}</span>
              </div>
              <div className="text-xs text-neutral-400 mt-0.5">
                {task.git_url ? (
                  <span className="truncate">{repoName(task.git_url)}</span>
                ) : (
                  formatDate(task.updated_at)
                )}
              </div>
              <button
                onClick={e => {
                  e.stopPropagation()
                  if (window.confirm('Delete this task and all its history? This cannot be undone.')) {
                    onDeleteTask(task.id)
                  }
                }}
                className="absolute right-2 top-1/2 -translate-y-1/2 p-1 rounded opacity-0 group-hover:opacity-100 hover:bg-neutral-200 text-neutral-400 hover:text-red-500 transition-all"
                title="Delete task"
              >
                <Trash2 size={13} />
              </button>
            </div>
          ))}

          {tasks.length === 0 && (
            <p className="px-3 py-4 text-xs text-neutral-400">No previous sessions</p>
          )}
        </div>
      </ScrollArea>
    </div>
  )
}
