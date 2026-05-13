import { useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

interface ResourceFormProps {
  kind: 'skill' | 'mcp'
  initial?: { name: string; content: string }
  onSave: (name: string, content: string) => Promise<void>
  onSaveZip?: (name: string, file: File) => Promise<void>
  onCancel: () => void
}

type SkillMode = 'editor' | 'zip'

export function ResourceForm({ kind, initial, onSave, onSaveZip, onCancel }: ResourceFormProps) {
  const isEdit = initial !== undefined
  const [name, setName] = useState(initial?.name ?? '')
  const [content, setContent] = useState(initial?.content ?? '')
  const [mode, setMode] = useState<SkillMode>('editor')
  const [zipFile, setZipFile] = useState<File | null>(null)
  const [nameError, setNameError] = useState('')
  const [contentError, setContentError] = useState('')
  const [saving, setSaving] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const validate = (): boolean => {
    let ok = true
    if (!isEdit) {
      if (!/^[a-zA-Z0-9_-]+$/.test(name)) {
        setNameError('Only letters, numbers, _ and - are allowed')
        ok = false
      } else {
        setNameError('')
      }
    }
    if (kind === 'skill' && mode === 'zip') {
      if (!zipFile) {
        setContentError('Please select a zip file')
        ok = false
      } else {
        setContentError('')
      }
    } else if (kind === 'mcp') {
      try {
        JSON.parse(content)
        setContentError('')
      } catch {
        setContentError('Must be valid JSON')
        ok = false
      }
    } else {
      setContentError('')
    }
    return ok
  }

  const handleSave = async () => {
    if (!validate()) return
    setSaving(true)
    try {
      if (kind === 'skill' && mode === 'zip' && onSaveZip && zipFile) {
        await onSaveZip(name, zipFile)
      } else {
        await onSave(name, content)
      }
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="border border-neutral-200 rounded-lg p-4 space-y-3 bg-neutral-50">
      {!isEdit && (
        <div className="space-y-1">
          <label className="text-xs font-medium text-neutral-600">Name</label>
          <Input
            value={name}
            onChange={e => setName(e.target.value)}
            placeholder={kind === 'skill' ? 'my-skill' : 'my-mcp-server'}
          />
          {nameError && <p className="text-xs text-red-500">{nameError}</p>}
        </div>
      )}

      {kind === 'skill' && !isEdit && (
        <div className="flex gap-1 p-0.5 bg-neutral-100 rounded-md w-fit">
          <button
            type="button"
            onClick={() => { setMode('editor'); setContentError('') }}
            className={`px-3 py-1 text-xs rounded transition-colors ${
              mode === 'editor'
                ? 'bg-white text-neutral-900 shadow-sm font-medium'
                : 'text-neutral-500 hover:text-neutral-700'
            }`}
          >
            Editor
          </button>
          <button
            type="button"
            onClick={() => { setMode('zip'); setContentError('') }}
            className={`px-3 py-1 text-xs rounded transition-colors ${
              mode === 'zip'
                ? 'bg-white text-neutral-900 shadow-sm font-medium'
                : 'text-neutral-500 hover:text-neutral-700'
            }`}
          >
            Zip Upload
          </button>
        </div>
      )}

      {kind === 'skill' && !isEdit && mode === 'zip' ? (
        <div className="space-y-1">
          <label className="text-xs font-medium text-neutral-600">Zip Archive</label>
          <div
            className="border border-dashed border-neutral-300 rounded-lg p-6 text-center cursor-pointer hover:border-neutral-400 hover:bg-neutral-100 transition-colors"
            onClick={() => fileInputRef.current?.click()}
          >
            {zipFile ? (
              <div className="space-y-1">
                <p className="text-sm font-medium text-neutral-800">{zipFile.name}</p>
                <p className="text-xs text-neutral-400">{(zipFile.size / 1024).toFixed(1)} KB — click to replace</p>
              </div>
            ) : (
              <div className="space-y-1">
                <p className="text-sm text-neutral-500">Click to select a zip file</p>
                <p className="text-xs text-neutral-400">Must contain SKILL.md at root — max 20 files, 1 MiB each</p>
              </div>
            )}
          </div>
          <input
            ref={fileInputRef}
            type="file"
            accept=".zip,application/zip"
            className="hidden"
            onChange={e => {
              const f = e.target.files?.[0] ?? null
              setZipFile(f)
              setContentError('')
            }}
          />
          {contentError && <p className="text-xs text-red-500">{contentError}</p>}
        </div>
      ) : (
        <div className="space-y-1">
          <label className="text-xs font-medium text-neutral-600">
            {kind === 'skill' ? 'Content (Markdown)' : 'Config (JSON)'}
          </label>
          <Textarea
            value={content}
            onChange={e => setContent(e.target.value)}
            placeholder={kind === 'skill' ? '# My Skill\n\nDescribe what this skill does...' : '{\n  "command": "...",\n  "args": []\n}'}
            className="min-h-[120px] font-mono text-sm"
          />
          {contentError && <p className="text-xs text-red-500">{contentError}</p>}
        </div>
      )}

      <div className="flex justify-end gap-2">
        <Button variant="outline" size="sm" onClick={onCancel} disabled={saving}>
          Cancel
        </Button>
        <Button size="sm" onClick={handleSave} disabled={saving}>
          {saving ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </div>
  )
}
