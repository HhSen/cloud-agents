import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, Calendar, PenLine, Plus, ToggleLeft, ToggleRight, Trash2 } from 'lucide-react'
import { listSchedules, deleteSchedule, enableSchedule, disableSchedule } from '@/api/client'
import type { Schedule } from '@/api/client'
import { describeCron, formatNextRun } from '@/lib/cron'
import { cn } from '@/lib/utils'

export function SchedulesPage() {
  const navigate = useNavigate()
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(() => {
    listSchedules().then(setSchedules).catch(() => {}).finally(() => setLoading(false))
  }, [])

  useEffect(() => { refresh() }, [refresh])

  const handleToggle = useCallback(async (s: Schedule) => {
    try {
      if (s.enabled) await disableSchedule(s.id)
      else await enableSchedule(s.id)
      refresh()
    } catch { /* ignore */ }
  }, [refresh])

  const handleDelete = useCallback(async (id: string) => {
    if (!window.confirm('Delete this schedule and all its run history? This cannot be undone.')) return
    await deleteSchedule(id)
    refresh()
  }, [refresh])

  return (
    <div className="min-h-screen bg-white">
      <header className="flex items-center gap-3 px-4 py-3 border-b border-neutral-200">
        <button
          onClick={() => navigate('/')}
          className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
        >
          <ArrowLeft size={16} />
        </button>
        <div className="flex items-center gap-2">
          <Calendar size={16} className="text-neutral-500" />
          <span className="font-semibold text-sm">Schedules</span>
        </div>
        <div className="ml-auto">
          <button
            onClick={() => navigate('/schedules/new')}
            className="flex items-center gap-1.5 px-3 py-1.5 text-sm bg-neutral-900 text-white rounded hover:bg-neutral-700 transition-colors"
          >
            <Plus size={14} />
            New Schedule
          </button>
        </div>
      </header>

      <div className="max-w-2xl mx-auto px-4 py-6">
        {loading ? (
          <p className="text-sm text-neutral-400">Loading…</p>
        ) : schedules.length === 0 ? (
          <div className="text-center py-16">
            <Calendar size={32} className="mx-auto text-neutral-300 mb-3" />
            <p className="text-sm text-neutral-500">No schedules yet</p>
            <button
              onClick={() => navigate('/schedules/new')}
              className="mt-4 text-sm text-blue-600 hover:underline"
            >
              Create your first schedule
            </button>
          </div>
        ) : (
          <div className="divide-y divide-neutral-100">
            {schedules.map(s => (
              <div
                key={s.id}
                className="py-3 flex items-center gap-3 group cursor-pointer hover:bg-neutral-50 -mx-2 px-2 rounded"
                onClick={() => navigate(`/schedules/${s.id}`)}
              >
                <button
                  onClick={e => { e.stopPropagation(); handleToggle(s) }}
                  className={cn('shrink-0 transition-colors', s.enabled ? 'text-blue-600' : 'text-neutral-300')}
                  title={s.enabled ? 'Disable' : 'Enable'}
                >
                  {s.enabled ? <ToggleRight size={22} /> : <ToggleLeft size={22} />}
                </button>

                <div className="flex-1 min-w-0">
                  <div className="text-sm font-medium text-neutral-800 truncate">{s.title || 'Untitled'}</div>
                  <div className="text-xs text-neutral-400 mt-0.5 truncate">{describeCron(s.cron_expr)}</div>
                </div>

                <div className="text-xs text-neutral-400 shrink-0">
                  {s.enabled ? formatNextRun(s.next_run_at) : 'disabled'}
                </div>

                <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity shrink-0">
                  <button
                    onClick={e => { e.stopPropagation(); navigate(`/schedules/${s.id}/edit`) }}
                    className="p-1 rounded hover:bg-neutral-200 text-neutral-400 hover:text-neutral-700"
                    title="Edit"
                  >
                    <PenLine size={13} />
                  </button>
                  <button
                    onClick={e => { e.stopPropagation(); handleDelete(s.id) }}
                    className="p-1 rounded hover:bg-neutral-200 text-neutral-400 hover:text-red-500"
                    title="Delete"
                  >
                    <Trash2 size={13} />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
