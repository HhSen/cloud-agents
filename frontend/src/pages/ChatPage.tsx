import { ChatInput } from '@/components/ChatInput'
import { ChatMessage } from '@/components/ChatMessage'
import { StatusBadge } from '@/components/StatusBadge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useChat } from '@/hooks/useChat'
import { getAuthUsername } from '@/lib/auth'
import { useEffect, useRef } from 'react'

export function ChatPage() {
  const [username, setUsername] = useState<string | null>(getAuthUsername())
  const [inputValue, setInputValue] = useState('')
  const { messages, sandboxState, sending, sendMessage, approvePermission, answerQuestion } = useChat(username ?? '')
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  return (
    <div className="flex flex-col h-[100dvh] max-w-3xl mx-auto">
      <header className="flex items-center justify-between px-4 py-3 border-b border-neutral-200">
        <span className="font-semibold text-sm">Lucas</span>
        <StatusBadge state={sandboxState} />
      </header>

      <ScrollArea className="flex-1">
        <div className="p-4 space-y-4">
          {messages.length === 0 ? (
            <div className="flex items-center justify-center h-full min-h-[60dvh]">
              <p className="text-neutral-400 text-sm">What can I help you with?</p>
            </div>
          ) : (
            messages.map(msg => (
              <ChatMessage
                key={msg.id}
                message={msg}
                onApprovePermission={msg.status === 'requesting' ? approvePermission : undefined}
                onAnswerQuestion={msg.status === 'asking' ? answerQuestion : undefined}
              />
            ))
          )}
          <div ref={bottomRef} />
        </div>
      </ScrollArea>

      <ChatInput onSend={sendMessage} disabled={sending} />
    </div>
  )
}
