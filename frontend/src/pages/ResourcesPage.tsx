import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { ResourceForm } from '@/components/ResourceForm'
import {
  listResources,
  createResource,
  createSkillFromZip,
  updateResource,
  deleteResource,
  getSkillContent,
} from '@/api/client'
import type { Resource } from '@/api/client'

export function ResourcesPage() {
  const navigate = useNavigate()
  const [tab, setTab] = useState<'skill' | 'mcp'>('skill')
  const [resources, setResources] = useState<Resource[]>([])
  const [loading, setLoading] = useState(true)
  const [formState, setFormState] = useState<'closed' | 'create' | number>('closed')
  const [deleteTarget, setDeleteTarget] = useState<number | null>(null)
  const [deleteConfirming, setDeleteConfirming] = useState(false)
  const [viewTarget, setViewTarget] = useState<Resource | null>(null)
  const [viewContent, setViewContent] = useState<string | null>(null)
  const [viewLoading, setViewLoading] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    listResources()
      .then(setResources)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => {
    load()
  }, [load])

  const deleteTargetResource = resources.find(r => r.id === deleteTarget)

  const handleToggleActive = async (r: Resource) => {
    const optimistic = resources.map(x => x.id === r.id ? { ...x, is_active: !r.is_active } : x)
    setResources(optimistic)
    try {
      await updateResource(r.id, { is_active: !r.is_active })
    } catch {
      setResources(resources)
    }
  }

  const handleView = async (r: Resource) => {
    setViewTarget(r)
    setViewContent(null)
    setViewLoading(true)
    try {
      const content = await getSkillContent(r.id)
      setViewContent(content)
    } catch {
      setViewContent('Failed to load SKILL.md content.')
    } finally {
      setViewLoading(false)
    }
  }

  const handleCreate = async (name: string, content: string) => {
    const created = await createResource({ kind: tab, name, content })
    setResources(prev => [...prev, created])
    setFormState('closed')
  }

  const handleCreateZip = async (name: string, file: File) => {
    const created = await createSkillFromZip(name, file)
    setResources(prev => [...prev, created])
    setFormState('closed')
  }

  const handleUpdate = async (id: number, content: string) => {
    const updated = await updateResource(id, { content })
    setResources(prev => prev.map(r => r.id === id ? updated : r))
    setFormState('closed')
  }

  const handleDelete = async () => {
    if (deleteTarget === null) return
    setDeleteConfirming(true)
    try {
      await deleteResource(deleteTarget)
      setResources(prev => prev.filter(r => r.id !== deleteTarget))
      setDeleteTarget(null)
    } finally {
      setDeleteConfirming(false)
    }
  }

  const tabContent = (kind: 'skill' | 'mcp') => {
    const list = resources.filter(r => r.kind === kind)
    const isCurrentTab = tab === kind
    const showForm = isCurrentTab && formState !== 'closed'

    return (
      <div className="space-y-3">
        {!showForm && (
          <Button
            size="sm"
            variant="outline"
            onClick={() => setFormState('create')}
          >
            + Add {kind === 'skill' ? 'Skill' : 'MCP Server'}
          </Button>
        )}

        {list.length === 0 && !showForm ? (
          <p className="text-sm text-neutral-400 py-4">No {kind === 'skill' ? 'skills' : 'MCP servers'} yet.</p>
        ) : (
          <div className="divide-y divide-neutral-100 border border-neutral-200 rounded-lg overflow-hidden">
            {list.map(r => (
              <div key={r.id} className="flex items-center gap-3 px-4 py-3 bg-white hover:bg-neutral-50">
                <span className="flex-1 text-sm font-medium truncate">{r.name}</span>
                {r.kind === 'skill' && (() => {
                  const files = (r.meta as { files?: string[] }).files
                  const count = files && files.length > 0 ? files.length : 1
                  return count > 1 ? (
                    <span className="text-xs text-neutral-400">{count} files</span>
                  ) : null
                })()}
                <div className="flex items-center gap-1.5 text-xs text-neutral-500">
                  <Switch
                    checked={r.is_active}
                    onCheckedChange={() => handleToggleActive(r)}
                  />
                  <span>{r.is_active ? 'Active' : 'Inactive'}</span>
                </div>
                {r.kind === 'skill' && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-xs h-7 px-2"
                    onClick={() => handleView(r)}
                  >
                    View
                  </Button>
                )}
                {r.kind === 'mcp' && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-xs h-7 px-2"
                    onClick={() => setFormState(r.id)}
                  >
                    Edit
                  </Button>
                )}
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-xs h-7 px-2 text-red-500 hover:text-red-600 hover:bg-red-50"
                  onClick={() => setDeleteTarget(r.id)}
                >
                  Delete
                </Button>
              </div>
            ))}
          </div>
        )}

        {showForm && formState === 'create' && (
          <ResourceForm
            kind={kind}
            onSave={handleCreate}
            onSaveZip={kind === 'skill' ? handleCreateZip : undefined}
            onCancel={() => setFormState('closed')}
          />
        )}

        {showForm && typeof formState === 'number' && (() => {
          const editing = list.find(r => r.id === formState)
          if (!editing) return null
          return (
            <ResourceForm
              kind={kind}
              initial={{ name: editing.name, content: '' }}
              onSave={(_, content) => handleUpdate(editing.id, content)}
              onCancel={() => setFormState('closed')}
            />
          )
        })()}
      </div>
    )
  }

  return (
    <div className="max-w-2xl mx-auto px-4 py-6 space-y-6">
      <header className="flex items-center gap-3">
        <button
          onClick={() => navigate('/')}
          className="p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
        >
          <ArrowLeft size={16} />
        </button>
        <h1 className="text-lg font-semibold">Resources</h1>
      </header>

      {loading ? (
        <p className="text-sm text-neutral-400">Loading…</p>
      ) : (
        <Tabs value={tab} onValueChange={v => { setTab(v as 'skill' | 'mcp'); setFormState('closed') }}>
          <TabsList>
            <TabsTrigger value="skill">Skills</TabsTrigger>
            <TabsTrigger value="mcp">MCP Servers</TabsTrigger>
          </TabsList>
          <TabsContent value="skill">{tabContent('skill')}</TabsContent>
          <TabsContent value="mcp">{tabContent('mcp')}</TabsContent>
        </Tabs>
      )}

      <Dialog open={viewTarget !== null} onOpenChange={open => { if (!open) { setViewTarget(null); setViewContent(null) } }}>
        <DialogContent className="max-w-2xl max-h-[80vh] flex flex-col">
          <DialogHeader>
            <DialogTitle>{viewTarget?.name} — SKILL.md</DialogTitle>
            <DialogDescription>Read-only view of the skill instruction file.</DialogDescription>
          </DialogHeader>
          <div className="flex-1 overflow-auto rounded border border-neutral-200 bg-neutral-50 p-3 min-h-0">
            {viewLoading ? (
              <p className="text-sm text-neutral-400">Loading…</p>
            ) : (
              <pre className="text-xs font-mono whitespace-pre-wrap break-words text-neutral-800">{viewContent}</pre>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => { setViewTarget(null); setViewContent(null) }}>
              Close
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={deleteTarget !== null} onOpenChange={open => { if (!open) setDeleteTarget(null) }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {deleteTargetResource?.name}?</DialogTitle>
            <DialogDescription>This cannot be undone.</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)} disabled={deleteConfirming}>
              Cancel
            </Button>
            <Button
              variant="outline"
              className="border-red-200 text-red-600 hover:bg-red-50 hover:text-red-700"
              onClick={handleDelete}
              disabled={deleteConfirming}
            >
              {deleteConfirming ? 'Deleting…' : 'Delete'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
