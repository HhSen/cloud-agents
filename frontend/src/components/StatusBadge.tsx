import type { SandboxState } from '@/types'

interface Props {
  state: SandboxState
}

export function StatusBadge({ state }: Props) {
  if (state === 'idle') return null

  if (state === 'provisioning') {
    return (
      <div className="flex items-center gap-1.5 text-sm text-neutral-500">
        <span className="h-2 w-2 rounded-full bg-neutral-400 animate-pulse" />
        Starting workspace…
      </div>
    )
  }

  if (state === 'running') {
    return (
      <div className="flex items-center gap-1.5 text-sm text-green-600">
        <span className="h-2 w-2 rounded-full bg-green-500" />
        Connected
      </div>
    )
  }

  return (
    <div className="flex items-center gap-1.5 text-sm text-red-600">
      <span className="h-2 w-2 rounded-full bg-red-500" />
      Connection error
    </div>
  )
}
