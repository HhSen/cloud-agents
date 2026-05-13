import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

interface ResourceFormProps {
  kind: 'skill' | 'mcp'
  initial?: { name: string; content: string }
  onSave: (name: string, content: string) => Promise<void>
  onCancel: () => void
}

export function ResourceForm({ kind, initial, onSave, onCancel }: ResourceFormProps) {
  const isEdit = initial !== undefined
  const [name, setName] = useState(initial?.name ?? '')
  const [content, setContent] = useState(initial?.content ?? '')
  const [nameError, setNameError] = useState('')
  const [contentError, setContentError] = useState('')
  const [saving, setSaving] = useState(false)

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
    if (kind === 'mcp') {
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
      await onSave(name, content)
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
