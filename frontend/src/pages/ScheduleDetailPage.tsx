import { useCallback, useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, Calendar, PenLine, Play, ToggleLeft, ToggleRight, Trash2 } from 'lucide-react'
import {
  getSchedule, deleteSchedule, enableSchedule, disableSchedule,
  runScheduleNow, listScheduleRuns,
} from '@/api/client'
import type { Schedule, ScheduleRun } from '@/api/client'
import { describeCron, formatNextRun } from '@/lib/cron'
import { cn } from '@/lib/utils'

function stateColor(state: string) {
  if (state === 'active' || state === 'idle') return 'text-green-600'
  if (state.includes('error')) return 'text-red-500'
  if (state.includes('provision') || state.includes('resum')) return 'text-yellow-500'
  return 'text-neutral-500'
}

function duration(run: ScheduleRun): string {
  if (!run.created_at || !run.updated_at) return '—'
  const ms = new Date(run.updated_at).getTime() - new Date(run.created_at).getTime()
  if (ms < 0) return '—'
  const secs = Math.floor(ms / 1000)
  if (secs < 60) return `${secs}s`
  return `${Math.floor(secs / 60)}m ${secs % 60}s`
}

export function ScheduleDetailPage() {
  const navigate = useNavigate()
  const { id } = useParams<{ id: string }>()

  const [schedule, setSchedule] = useState<Schedule | null>(null)
  const [runs, setRuns] = useState<ScheduleRun[]>([])
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(() => {
    if (!id) return
    Promise.all([getSchedule(id), listScheduleRuns(id)])
      .then(([s, r]) => { setSchedule(s); setRuns(r) })
      .catch(() => navigate('/schedules'))
      .finally(() => setLoading(false))
  }, [id, navigate])

  useEffect(() => { refresh() }, [refresh])

  const handleToggle = useCallback(async () => {
    if (!schedule) return
    if (schedule.enabled) await disableSchedule(schedule.id)
    else await enableSchedule(schedule.id)
    refresh()
  }, [schedule, refresh])

  const handleDelete = useCallback(async () => {
    if (!schedule) return
    if (!window.confirm('Delete this schedule? Existing run tasks will be kept.')) return
    await deleteSchedule(schedule.id)
    navigate('/schedules')
  }, [schedule, navigate])

  const handleRunNow = useCallback(async () => {
    if (!id) return
    const { task_id } = await runScheduleNow(id)
    navigate(`/?task=${task_id}`)
  }, [id, navigate])

  if (loading || !schedule) return null

  return (
    <div className="min-h-screen bg-white">
      <header className="flex items-center gap-3 px-4 py-3 border-b border-neutral-200">
        <button
          onClick={() => navigate('/schedules')}
          className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
        >
          <ArrowLeft size={16} />
        </button>
        <div className="flex items-center gap-2">
          <Calendar size={16} className="text-neutral-500" />
          <span className="font-semibold text-sm">{schedule.title || 'Untitled'}</span>
        </div>
        <div className="ml-auto flex items-center gap-1.5">
          <button
            onClick={handleToggle}
            className={cn('p-1.5 rounded transition-colors', schedule.enabled ? 'text-blue-600 hover:bg-blue-50' : 'text-neutral-400 hover:bg-neutral-100')}
            title={schedule.enabled ? 'Disable' : 'Enable'}
          >
            {schedule.enabled ? <ToggleRight size={18} /> : <ToggleLeft size={18} />}
          </button>
          <button
            onClick={() => navigate(`/schedules/${schedule.id}/edit`)}
            className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
            title="Edit"
          >
            <PenLine size={15} />
          </button>
          <button
            onClick={handleDelete}
            className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-red-500 transition-colors"
            title="Delete"
          >
            <Trash2 size={15} />
          </button>
        </div>
      </header>

      <div className="max-w-2xl mx-auto px-4 py-6 space-y-6">
        {/* Meta */}
        <div className="space-y-1 text-sm text-neutral-600">
          <div className="flex items-center gap-4">
            <span className="font-mono text-xs bg-neutral-100 px-2 py-0.5 rounded">{schedule.cron_expr}</span>
            <span className="text-neutral-400">{describeCron(schedule.cron_expr)}</span>
          </div>
          <div className="flex items-center gap-4 text-xs text-neutral-400">
            <span>Next run: {formatNextRun(schedule.next_run_at)}</span>
            {schedule.last_run_at && (
              <span>Last run: {new Date(schedule.last_run_at).toLocaleString()}</span>
            )}
          </div>
        </div>

        {/* Prompt */}
        <div className="space-y-1">
          <p className="text-xs font-medium text-neutral-500 uppercase tracking-wide">Prompt</p>
          <pre className="text-sm text-neutral-700 bg-neutral-50 border border-neutral-200 rounded p-3 whitespace-pre-wrap font-mono overflow-auto max-h-40">
            {schedule.prompt}
          </pre>
        </div>

        {/* Run now */}
        <button
          onClick={handleRunNow}
          className="flex items-center gap-2 px-4 py-2 text-sm bg-neutral-900 text-white rounded hover:bg-neutral-700 transition-colors"
        >
          <Play size={14} />
          Run now
        </button>

        {/* Run history */}
        <div>
          <p className="text-xs font-medium text-neutral-500 uppercase tracking-wide mb-2">Run history</p>
          {runs.length === 0 ? (
            <p className="text-sm text-neutral-400">No runs yet.</p>
          ) : (
            <div className="divide-y divide-neutral-100 border border-neutral-200 rounded">
              {runs.map(run => (
                <div
                  key={run.id}
                  className="flex items-center gap-3 px-3 py-2.5 hover:bg-neutral-50 cursor-pointer"
                  onClick={() => navigate(`/?task=${run.id}`)}
                >
                  <span className="text-xs text-neutral-400 w-36 shrink-0">
                    {new Date(run.created_at).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}
                  </span>
                  <span className={cn('text-xs font-medium w-20 shrink-0', stateColor(run.state))}>
                    {run.state}
                  </span>
                  <span className="text-xs text-neutral-400 flex-1 truncate">
                    {run.title || '—'}
                  </span>
                  <span className="text-xs text-neutral-400 shrink-0">{duration(run)}</span>
                  <span className="text-xs text-blue-500 shrink-0">Open →</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
