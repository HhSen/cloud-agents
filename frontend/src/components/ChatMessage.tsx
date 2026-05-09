import { AlertCircle, ChevronDown } from 'lucide-react'
import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { cn } from '@/lib/utils'
import type { Message } from '@/types'

interface Props {
  message: Message
}

export function ChatMessage({ message }: Props) {
  const [toolsOpen, setToolsOpen] = useState(false)
  const isUser = message.role === 'user'

  return (
    <div className={cn('flex', isUser ? 'justify-end' : 'justify-start')}>
      <div
        className={cn(
          'max-w-[80%] rounded-2xl px-4 py-3 text-sm',
          isUser
            ? 'bg-neutral-900 text-neutral-50'
            : 'bg-neutral-100 text-neutral-900',
          message.status === 'error' && 'bg-red-50 text-red-800 border border-red-200'
        )}
      >
        {message.status === 'error' && (
          <div className="flex items-center gap-1.5 mb-1 text-red-600">
            <AlertCircle className="h-3.5 w-3.5" />
            <span className="text-xs font-medium">Error</span>
          </div>
        )}

        {isUser ? (
          <p className="whitespace-pre-wrap">{message.text}</p>
        ) : (
          <div className="prose prose-sm prose-neutral max-w-none">
            <ReactMarkdown>{message.text}</ReactMarkdown>
            {message.status === 'streaming' && message.text && (
              <span className="inline-block w-0.5 h-4 bg-neutral-400 animate-pulse ml-0.5 align-text-bottom" />
            )}
          </div>
        )}

        {!isUser && message.toolActivity && message.toolActivity.length > 0 && (
          <div className="mt-2 border-t border-neutral-200 pt-2">
            <button
              onClick={() => setToolsOpen(o => !o)}
              className="flex items-center gap-1 text-xs text-neutral-500 hover:text-neutral-700"
            >
              <ChevronDown
                className={cn('h-3 w-3 transition-transform', toolsOpen && 'rotate-180')}
              />
              {message.toolActivity.length} tool{message.toolActivity.length !== 1 ? 's' : ''} used
            </button>
            {toolsOpen && (
              <ul className="mt-1.5 space-y-1">
                {message.toolActivity.map((a, i) => (
                  <li key={i} className="flex items-start gap-1.5 text-xs text-neutral-500">
                    <span className={cn('mt-0.5 h-1.5 w-1.5 rounded-full flex-shrink-0', a.done ? 'bg-green-400' : 'bg-neutral-300 animate-pulse')} />
                    <span>
                      {a.toolName && <strong className="font-medium">{a.toolName}</strong>}
                      {a.toolName && ' — '}
                      {a.description}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
