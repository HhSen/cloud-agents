import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

interface Props {
  open: boolean
  onClose: () => void
  onCreate: (options: { title?: string; gitUrl?: string }) => Promise<void>
}

export function NewTaskDialog({ open, onClose, onCreate }: Props) {
  const [title, setTitle] = useState('')
  const [gitUrl, setGitUrl] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!open) return null

  const handleCreate = async () => {
    setCreating(true)
    setError(null)
    try {
      await onCreate({
        title: title.trim() || undefined,
        gitUrl: gitUrl.trim() || undefined,
      })
      setTitle('')
      setGitUrl('')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to create task')
      setCreating(false)
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !creating) handleCreate()
    if (e.key === 'Escape') onClose()
  }

  const isPrivateUrl = gitUrl.trim().startsWith('git@') || gitUrl.trim().startsWith('ssh://')

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/30"
      onClick={e => { if (e.target === e.currentTarget) onClose() }}
    >
      <div
        className="bg-white rounded-lg shadow-lg w-full max-w-sm mx-4 p-5 space-y-4"
        onKeyDown={handleKeyDown}
      >
        <h2 className="text-sm font-semibold text-neutral-900">New Task</h2>

        <div className="space-y-1">
          <label className="text-xs text-neutral-500">Title (optional)</label>
          <Input
            autoFocus
            placeholder="Untitled"
            value={title}
            onChange={e => setTitle(e.target.value)}
            disabled={creating}
            className="text-sm h-8"
          />
        </div>

        <div className="space-y-1">
          <label className="text-xs text-neutral-500">Git Repository (optional)</label>
          <Input
            placeholder="https://… or git@…"
            value={gitUrl}
            onChange={e => setGitUrl(e.target.value)}
            disabled={creating}
            className={cn('text-sm h-8 font-mono', gitUrl && 'text-xs')}
          />
          {isPrivateUrl && (
            <p className="text-xs text-neutral-400">
              ⓘ Private repos require an SSH key configured in{' '}
              <a href="/settings" className="underline text-neutral-600" onClick={onClose}>Settings</a>.
            </p>
          )}
        </div>

        {error && <p className="text-xs text-red-600">{error}</p>}

        <div className="flex justify-end gap-2 pt-1">
          <Button variant="outline" size="sm" onClick={onClose} disabled={creating}>
            Cancel
          </Button>
          <Button size="sm" onClick={handleCreate} disabled={creating}>
            Create
          </Button>
        </div>
      </div>
    </div>
  )
}
