import { useEffect, useRef, useState } from 'react'
import { Paperclip, SendHorizonal, X, Zap } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'

interface FileItem {
  file: File
  url: string
}

interface Props {
  onSend: (prompt: string, files?: File[]) => void
  isSteering?: boolean
}

export function ChatInput({ onSend, isSteering }: Props) {
  const [value, setValue] = useState('')
  const [fileItems, setFileItems] = useState<FileItem[]>([])
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const fileItemsRef = useRef<FileItem[]>(fileItems)
  fileItemsRef.current = fileItems

  useEffect(() => {
    return () => fileItemsRef.current.forEach(fi => URL.revokeObjectURL(fi.url))
  }, [])

  function autoResize() {
    const el = textareaRef.current
    if (!el) return
    el.style.height = 'auto'
    const maxHeight = 6 * 24 + 16 // ~6 lines
    el.style.height = Math.min(el.scrollHeight, maxHeight) + 'px'
  }

  function handleChange(e: React.ChangeEvent<HTMLTextAreaElement>) {
    setValue(e.target.value)
    autoResize()
  }

  function submit() {
    const trimmed = value.trim()
    if (!trimmed) return
    const files = fileItems.map(fi => fi.file)
    onSend(trimmed, isSteering ? undefined : files.length > 0 ? files : undefined)
    setValue('')
    fileItems.forEach(fi => URL.revokeObjectURL(fi.url))
    setFileItems([])
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto'
    }
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submit()
    }
  }

  function handleFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    const picked = Array.from(e.target.files ?? [])
    setFileItems(prev => {
      const newItems = picked.map(f => ({ file: f, url: URL.createObjectURL(f) }))
      const combined = [...prev, ...newItems]
      if (combined.length > 4) {
        combined.slice(4).forEach(item => URL.revokeObjectURL(item.url))
      }
      return combined.slice(0, 4)
    })
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  function removeFile(index: number) {
    setFileItems(prev => {
      URL.revokeObjectURL(prev[index].url)
      return prev.filter((_, i) => i !== index)
    })
  }

  const fileSizeWarning = fileItems.some(fi => fi.file.size > 5 * 1024 * 1024)

  return (
    <div className="flex flex-col gap-2 p-4 border-t border-neutral-200 bg-white">
      {isSteering && (
        <div className="flex items-center gap-1.5 text-xs text-amber-600 bg-amber-50 border border-amber-200 rounded-md px-2.5 py-1.5">
          <Zap className="h-3 w-3 flex-shrink-0" />
          <span>Agent is running — your message will be injected</span>
        </div>
      )}

      {fileItems.length > 0 && !isSteering && (
        <div className="flex flex-wrap gap-1.5">
          {fileItems.map((fi, i) => (
            <div key={i} className="relative flex items-center gap-1 rounded-lg border border-neutral-200 bg-neutral-50 px-2 py-1 text-xs text-neutral-700">
              <img
                src={fi.url}
                alt={fi.file.name}
                className="h-8 w-8 rounded object-cover"
              />
              <span className="max-w-[80px] truncate">{fi.file.name}</span>
              <button
                onClick={() => removeFile(i)}
                className="ml-0.5 rounded-full hover:bg-neutral-200 p-0.5"
              >
                <X className="h-3 w-3" />
              </button>
            </div>
          ))}
          {fileSizeWarning && (
            <span className="self-center text-xs text-amber-600">Some files exceed 5 MB</span>
          )}
        </div>
      )}

      <div className="flex items-end gap-2">
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*"
          multiple
          className="hidden"
          onChange={handleFileChange}
        />
        {!isSteering && (
          <button
            type="button"
            onClick={() => fileInputRef.current?.click()}
            className="flex-shrink-0 p-1.5 rounded hover:bg-neutral-100 text-neutral-500 hover:text-neutral-700 transition-colors"
            title="Attach image"
          >
            <Paperclip className="h-4 w-4" />
          </button>
        )}
        <Textarea
          ref={textareaRef}
          value={value}
          onChange={handleChange}
          onKeyDown={handleKeyDown}
          placeholder={isSteering ? 'Inject a message…' : 'Ask anything…'}
          rows={1}
          className="resize-none overflow-hidden flex-1 min-h-[40px]"
        />
        <Button
          size="icon"
          onClick={submit}
          disabled={!value.trim()}
          className="flex-shrink-0"
          title={isSteering ? 'Steer' : 'Send'}
        >
          {isSteering ? (
            <Zap className="h-4 w-4" />
          ) : (
            <SendHorizonal className="h-4 w-4" />
          )}
        </Button>
      </div>
    </div>
  )
}
