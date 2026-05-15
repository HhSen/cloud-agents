import cronstrue from 'cronstrue'

export function describeCron(expr: string): string {
  if (!expr || expr === '@once') return 'One-time'
  try {
    return cronstrue.toString(expr, { throwExceptionOnParseError: true })
  } catch {
    return expr
  }
}

export function formatNextRun(nextRunAt: string | undefined): string {
  if (!nextRunAt) return '—'
  const d = new Date(nextRunAt)
  const now = new Date()
  const diffMs = d.getTime() - now.getTime()
  if (diffMs < 0) return 'overdue'
  const diffMins = Math.floor(diffMs / 60000)
  if (diffMins < 60) return `in ${diffMins}m`
  const diffHours = Math.floor(diffMins / 60)
  if (diffHours < 24) return `in ${diffHours}h`
  return d.toLocaleDateString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}
