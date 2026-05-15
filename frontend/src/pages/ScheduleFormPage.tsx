import { useCallback, useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { createSchedule, getSchedule, updateSchedule } from '@/api/client'
import type { CreateSchedulePayload } from '@/api/client'
import { describeCron } from '@/lib/cron'

type Mode = 'create' | 'edit'

const DEFAULT_TIMEOUT = 1800

export function ScheduleFormPage({ mode }: { mode: Mode }) {
  const navigate = useNavigate()
  const { id } = useParams<{ id: string }>()

  const [title, setTitle] = useState('')
  const [prompt, setPrompt] = useState('')
  const [scheduleType, setScheduleType] = useState<'recurring' | 'once'>('recurring')
  const [cronExpr, setCronExpr] = useState('@daily')
  const [runAt, setRunAt] = useState('')
  const [gitUrl, setGitUrl] = useState('')
  const [timeoutSecs, setTimeoutSecs] = useState(DEFAULT_TIMEOUT)
  const [concurrency, setConcurrency] = useState(0)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (mode === 'edit' && id) {
      getSchedule(id).then(s => {
        setTitle(s.title ?? '')
        setPrompt(s.prompt)
        if (s.cron_expr === '@once') {
          setScheduleType('once')
          setRunAt(s.run_at ? s.run_at.slice(0, 16) : '')
        } else {
          setScheduleType('recurring')
          setCronExpr(s.cron_expr)
        }
        setGitUrl(s.git_url ?? '')
        setTimeoutSecs(s.timeout_secs)
        setConcurrency(s.concurrency)
      }).catch(() => navigate('/schedules'))
    }
  }, [mode, id, navigate])

  const cronDescription = scheduleType === 'recurring' ? describeCron(cronExpr) : ''

  const handleSubmit = useCallback(async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setSaving(true)
    try {
      const payload: CreateSchedulePayload = {
        title: title || undefined,
        prompt,
        cron_expr: scheduleType === 'once' ? '@once' : cronExpr,
        run_at: scheduleType === 'once' && runAt ? new Date(runAt).toISOString() : undefined,
        git_url: gitUrl || undefined,
        timeout_secs: timeoutSecs,
        concurrency,
      }
      if (mode === 'create') {
        const s = await createSchedule(payload)
        navigate(`/schedules/${s.id}`)
      } else if (id) {
        await updateSchedule(id, payload)
        navigate(`/schedules/${id}`)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save schedule')
    } finally {
      setSaving(false)
    }
  }, [mode, id, title, prompt, scheduleType, cronExpr, runAt, gitUrl, timeoutSecs, concurrency, navigate])

  return (
    <div className="min-h-screen bg-white">
      <header className="flex items-center gap-3 px-4 py-3 border-b border-neutral-200">
        <button
          onClick={() => navigate(mode === 'edit' && id ? `/schedules/${id}` : '/schedules')}
          className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
        >
          <ArrowLeft size={16} />
        </button>
        <span className="font-semibold text-sm">{mode === 'create' ? 'New Schedule' : 'Edit Schedule'}</span>
      </header>

      <form onSubmit={handleSubmit} className="max-w-lg mx-auto px-4 py-6 space-y-5">
        {error && (
          <div className="text-sm text-red-600 bg-red-50 border border-red-200 rounded px-3 py-2">{error}</div>
        )}

        <div className="space-y-1">
          <label className="text-xs font-medium text-neutral-600">Title (optional)</label>
          <input
            type="text"
            value={title}
            onChange={e => setTitle(e.target.value)}
            placeholder="e.g. Daily standup summary"
            className="w-full px-3 py-2 text-sm border border-neutral-200 rounded focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs font-medium text-neutral-600">Prompt <span className="text-red-500">*</span></label>
          <textarea
            required
            value={prompt}
            onChange={e => setPrompt(e.target.value)}
            placeholder="Summarize today's commits and open PRs..."
            rows={5}
            className="w-full px-3 py-2 text-sm border border-neutral-200 rounded focus:outline-none focus:ring-2 focus:ring-blue-500 resize-y font-mono"
          />
        </div>

        <div className="space-y-2">
          <label className="text-xs font-medium text-neutral-600">Schedule type</label>
          <div className="flex gap-2">
            {(['recurring', 'once'] as const).map(t => (
              <button
                key={t}
                type="button"
                onClick={() => setScheduleType(t)}
                className={`px-3 py-1.5 text-xs rounded border transition-colors ${
                  scheduleType === t
                    ? 'bg-neutral-900 text-white border-neutral-900'
                    : 'border-neutral-200 text-neutral-600 hover:border-neutral-400'
                }`}
              >
                {t === 'recurring' ? 'Recurring (cron)' : 'One-time'}
              </button>
            ))}
          </div>
        </div>

        {scheduleType === 'recurring' ? (
          <div className="space-y-1">
            <label className="text-xs font-medium text-neutral-600">Cron expression</label>
            <input
              type="text"
              value={cronExpr}
              onChange={e => setCronExpr(e.target.value)}
              placeholder="@daily"
              className="w-full px-3 py-2 text-sm border border-neutral-200 rounded focus:outline-none focus:ring-2 focus:ring-blue-500 font-mono"
            />
            {cronDescription && (
              <p className="text-xs text-neutral-400">{cronDescription}</p>
            )}
          </div>
        ) : (
          <div className="space-y-1">
            <label className="text-xs font-medium text-neutral-600">Run at <span className="text-red-500">*</span></label>
            <input
              type="datetime-local"
              required={scheduleType === 'once'}
              value={runAt}
              onChange={e => setRunAt(e.target.value)}
              className="w-full px-3 py-2 text-sm border border-neutral-200 rounded focus:outline-none focus:ring-2 focus:ring-blue-500"
            />
          </div>
        )}

        <div className="space-y-1">
          <label className="text-xs font-medium text-neutral-600">Git URL (optional)</label>
          <input
            type="text"
            value={gitUrl}
            onChange={e => setGitUrl(e.target.value)}
            placeholder="https://github.com/org/repo"
            className="w-full px-3 py-2 text-sm border border-neutral-200 rounded focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs font-medium text-neutral-600">
            Timeout: {Math.floor(timeoutSecs / 60)} min
          </label>
          <input
            type="range"
            min={60}
            max={86400}
            step={60}
            value={timeoutSecs}
            onChange={e => setTimeoutSecs(Number(e.target.value))}
            className="w-full"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs font-medium text-neutral-600">If a run is already active</label>
          <div className="flex gap-2">
            {[
              { value: 0, label: 'Skip new run' },
              { value: 1, label: 'Allow parallel' },
            ].map(opt => (
              <button
                key={opt.value}
                type="button"
                onClick={() => setConcurrency(opt.value)}
                className={`px-3 py-1.5 text-xs rounded border transition-colors ${
                  concurrency === opt.value
                    ? 'bg-neutral-900 text-white border-neutral-900'
                    : 'border-neutral-200 text-neutral-600 hover:border-neutral-400'
                }`}
              >
                {opt.label}
              </button>
            ))}
          </div>
        </div>

        <div className="pt-2 flex justify-end gap-2">
          <button
            type="button"
            onClick={() => navigate(mode === 'edit' && id ? `/schedules/${id}` : '/schedules')}
            className="px-4 py-2 text-sm border border-neutral-200 rounded hover:bg-neutral-50 transition-colors"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={saving}
            className="px-4 py-2 text-sm bg-neutral-900 text-white rounded hover:bg-neutral-700 disabled:opacity-50 transition-colors"
          >
            {saving ? 'Saving…' : mode === 'create' ? 'Create' : 'Save'}
          </button>
        </div>
      </form>
    </div>
  )
}
