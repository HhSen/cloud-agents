import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { getUserSettings, updateUserSettings } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'

export function SettingsPage() {
  const navigate = useNavigate()
  const [keyValue, setKeyValue] = useState('')
  const [hasKey, setHasKey] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [success, setSuccess] = useState<string | null>(null)

  useEffect(() => {
    getUserSettings()
      .then(s => setHasKey(s.has_ssh_key))
      .catch(() => setError('Failed to load settings.'))
      .finally(() => setLoading(false))
  }, [])

  const handleSave = async () => {
    if (!keyValue.trim()) return
    setSaving(true)
    setError(null)
    setSuccess(null)
    try {
      await updateUserSettings({ ssh_private_key: keyValue.trim() })
      setHasKey(true)
      setKeyValue('')
      setSuccess('SSH key saved.')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save key')
    } finally {
      setSaving(false)
    }
  }

  const handleClear = async () => {
    setSaving(true)
    setError(null)
    setSuccess(null)
    try {
      await updateUserSettings({ ssh_private_key: '' })
      setHasKey(false)
      setKeyValue('')
      setSuccess('SSH key removed.')
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to clear key')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="min-h-screen bg-white">
      <header className="flex items-center gap-3 px-6 py-4 border-b border-neutral-200">
        <button
          onClick={() => navigate('/')}
          className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
          title="Back"
        >
          <ArrowLeft size={16} />
        </button>
        <span className="font-semibold text-sm">Settings</span>
      </header>

      <div className="max-w-xl mx-auto px-6 py-8 space-y-6">
        <div>
          <div className="flex items-center justify-between mb-1">
            <h2 className="text-sm font-medium text-neutral-800">SSH Private Key</h2>
            {!loading && (
              <span className={`text-xs px-2 py-0.5 rounded-full font-medium ${hasKey ? 'bg-green-100 text-green-700' : 'bg-neutral-100 text-neutral-500'}`}>
                {hasKey ? 'Key configured' : 'No key'}
              </span>
            )}
          </div>
          <p className="text-xs text-neutral-500 mb-3">
            Used to clone private git repositories. The key is encrypted at rest and never returned by the server.
          </p>
          <Textarea
            className="font-mono text-xs h-40 resize-none"
            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
            value={keyValue}
            onChange={e => setKeyValue(e.target.value)}
          />
          {error && <p className="mt-2 text-xs text-red-600">{error}</p>}
          {success && <p className="mt-2 text-xs text-green-600">{success}</p>}
          <div className="flex gap-2 mt-3">
            {hasKey && (
              <Button variant="outline" size="sm" onClick={handleClear} disabled={saving}>
                Clear
              </Button>
            )}
            <Button size="sm" onClick={handleSave} disabled={saving || !keyValue.trim()}>
              Save
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}
