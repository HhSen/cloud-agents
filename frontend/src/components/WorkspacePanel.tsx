import { useCallback, useEffect, useState } from 'react'
import { ChevronDown, ChevronRight, File, Folder, RefreshCw, X } from 'lucide-react'
import { listDir, readFile } from '@/api/client'
import type { FileInfo } from '@/api/client'
import { cn } from '@/lib/utils'

interface WorkspacePanelProps {
  taskId: string
  cwd: string
  refreshToken: number
}

interface TreeNode extends FileInfo {
  children?: TreeNode[]
  expanded?: boolean
  loading?: boolean
}

function isBinary(text: string): boolean {
  const sample = text.slice(0, 512)
  for (let i = 0; i < sample.length; i++) {
    if (sample.charCodeAt(i) === 0) return true
  }
  return false
}

interface FileNodeProps {
  node: TreeNode
  depth: number
  taskId: string
  onToggle: (node: TreeNode) => void
  onSelect: (node: TreeNode) => void
  selectedPath: string | null
}

function FileNode({ node, depth, taskId, onToggle, onSelect, selectedPath }: FileNodeProps) {
  const isSelected = selectedPath === node.path

  return (
    <div>
      <button
        onClick={() => node.isDir ? onToggle(node) : onSelect(node)}
        className={cn(
          'w-full flex items-center gap-1 px-2 py-0.5 text-xs hover:bg-neutral-100 text-left truncate',
          isSelected && 'bg-neutral-100 font-medium',
        )}
        style={{ paddingLeft: `${8 + depth * 12}px` }}
        title={node.path}
      >
        {node.isDir ? (
          <>
            {node.loading ? (
              <RefreshCw size={12} className="animate-spin text-neutral-400 shrink-0" />
            ) : node.expanded ? (
              <ChevronDown size={12} className="text-neutral-400 shrink-0" />
            ) : (
              <ChevronRight size={12} className="text-neutral-400 shrink-0" />
            )}
            <Folder size={12} className="text-amber-500 shrink-0" />
          </>
        ) : (
          <>
            <span className="w-3 shrink-0" />
            <File size={12} className="text-neutral-400 shrink-0" />
          </>
        )}
        <span className="truncate">{node.name}</span>
      </button>
      {node.isDir && node.expanded && node.children && (
        <div>
          {node.children.map(child => (
            <FileNode
              key={child.path}
              node={child}
              depth={depth + 1}
              taskId={taskId}
              onToggle={onToggle}
              onSelect={onSelect}
              selectedPath={selectedPath}
            />
          ))}
          {node.children.length === 0 && (
            <div
              className="text-xs text-neutral-400 italic px-2 py-0.5"
              style={{ paddingLeft: `${8 + (depth + 1) * 12}px` }}
            >
              empty
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function WorkspacePanel({ taskId, cwd, refreshToken }: WorkspacePanelProps) {
  const [roots, setRoots] = useState<TreeNode[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [fileContent, setFileContent] = useState<string | null>(null)
  const [fileLoading, setFileLoading] = useState(false)

  const fetchRoot = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const entries = await listDir(taskId, cwd)
      const sorted = [...entries].sort((a, b) => {
        if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
        return a.name.localeCompare(b.name)
      })
      setRoots(sorted.map(e => ({ ...e })))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load workspace')
    } finally {
      setLoading(false)
    }
  }, [taskId, cwd])

  useEffect(() => {
    fetchRoot()
  }, [fetchRoot, refreshToken])

  const updateNodeInTree = useCallback((nodes: TreeNode[], targetPath: string, update: Partial<TreeNode>): TreeNode[] => {
    return nodes.map(n => {
      if (n.path === targetPath) return { ...n, ...update }
      if (n.children) return { ...n, children: updateNodeInTree(n.children, targetPath, update) }
      return n
    })
  }, [])

  const handleToggle = useCallback(async (node: TreeNode) => {
    if (node.expanded) {
      setRoots(prev => updateNodeInTree(prev, node.path, { expanded: false }))
      return
    }
    if (node.children) {
      setRoots(prev => updateNodeInTree(prev, node.path, { expanded: true }))
      return
    }
    setRoots(prev => updateNodeInTree(prev, node.path, { loading: true, expanded: true }))
    try {
      const entries = await listDir(taskId, node.path)
      const sorted = [...entries].sort((a, b) => {
        if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
        return a.name.localeCompare(b.name)
      })
      const children = sorted.map(e => ({ ...e }))
      setRoots(prev => updateNodeInTree(prev, node.path, { loading: false, children }))
    } catch {
      setRoots(prev => updateNodeInTree(prev, node.path, { loading: false, expanded: false }))
    }
  }, [taskId, updateNodeInTree])

  const handleSelect = useCallback(async (node: TreeNode) => {
    if (selectedPath === node.path) {
      setSelectedPath(null)
      setFileContent(null)
      return
    }
    setSelectedPath(node.path)
    setFileContent(null)
    setFileLoading(true)
    try {
      const text = await readFile(taskId, node.path)
      setFileContent(text)
    } catch {
      setFileContent(null)
    } finally {
      setFileLoading(false)
    }
  }, [taskId, selectedPath])

  const displayCwd = cwd.length > 32 ? '…' + cwd.slice(-30) : cwd

  return (
    <div className="w-72 h-[100dvh] border-l border-neutral-200 flex flex-col overflow-hidden bg-white">
      <div className="flex items-center justify-between px-3 py-2 border-b border-neutral-200 shrink-0">
        <span className="text-xs text-neutral-500 font-mono truncate" title={cwd}>{displayCwd}</span>
        <button
          onClick={fetchRoot}
          disabled={loading}
          className="p-1 rounded hover:bg-neutral-100 text-neutral-400 hover:text-neutral-600 disabled:opacity-50 transition-colors shrink-0"
          title="Refresh"
        >
          <RefreshCw size={13} className={loading ? 'animate-spin' : ''} />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto">
        {error ? (
          <p className="text-xs text-red-500 px-3 py-2">{error}</p>
        ) : loading && roots.length === 0 ? (
          <p className="text-xs text-neutral-400 px-3 py-2">Loading…</p>
        ) : roots.length === 0 ? (
          <p className="text-xs text-neutral-400 px-3 py-2 italic">Empty directory</p>
        ) : (
          <div className="py-1">
            {roots.map(node => (
              <FileNode
                key={node.path}
                node={node}
                depth={0}
                taskId={taskId}
                onToggle={handleToggle}
                onSelect={handleSelect}
                selectedPath={selectedPath}
              />
            ))}
          </div>
        )}
      </div>

      {selectedPath && (
        <div className="border-t border-neutral-200 flex flex-col" style={{ maxHeight: '40%' }}>
          <div className="flex items-center justify-between px-3 py-1.5 bg-neutral-50 border-b border-neutral-200 shrink-0">
            <span className="text-xs font-mono text-neutral-600 truncate" title={selectedPath}>
              {selectedPath.split('/').pop()}
            </span>
            <button
              onClick={() => { setSelectedPath(null); setFileContent(null) }}
              className="p-0.5 rounded hover:bg-neutral-200 text-neutral-400 hover:text-neutral-600 transition-colors shrink-0"
            >
              <X size={12} />
            </button>
          </div>
          <div className="overflow-auto flex-1 p-2">
            {fileLoading ? (
              <p className="text-xs text-neutral-400">Loading…</p>
            ) : fileContent === null ? (
              <p className="text-xs text-neutral-400 italic">Failed to load file</p>
            ) : isBinary(fileContent) ? (
              <p className="text-xs text-neutral-400 italic">Binary file, preview not available</p>
            ) : (
              <pre className="text-xs font-mono text-neutral-700 whitespace-pre-wrap break-all">{fileContent}</pre>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
