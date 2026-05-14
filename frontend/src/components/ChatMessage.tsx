import { AlertCircle, Bot, ChevronDown, ChevronRight, Wrench } from 'lucide-react'
import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { cn } from '@/lib/utils'
import type { AnsweredQuestion, Message, PermissionRequest, Question, SubagentMessage, ThinkingBlock, ToolUseBlock } from '@/types'

interface Props {
  message: Message
  onApprovePermission?: (approved: boolean) => void
  onAnswerQuestion?: (answers: Record<string, string | string[]>) => void
}

function primaryParam(name: string, input: Record<string, unknown>): string | null {
  const key = name === 'Bash' ? 'command'
    : name === 'Write' || name === 'Read' || name === 'Edit' ? 'file_path'
    : Object.keys(input)[0]
  if (!key) return null
  const val = input[key]
  return typeof val === 'string' ? val : null
}

function ThinkingCard({ blocks }: { blocks: ThinkingBlock[] }) {
  const [expanded, setExpanded] = useState(false)
  const text = blocks.map(b => b.thinking).join('\n\n')

  return (
    <div className="rounded-lg border border-neutral-200 bg-neutral-50 overflow-hidden text-xs mb-2">
      <button
        onClick={() => setExpanded(e => !e)}
        className="flex w-full items-center gap-1.5 px-2.5 py-1.5 text-neutral-500 hover:text-neutral-700"
      >
        <ChevronRight className={cn('h-3 w-3 transition-transform flex-shrink-0', expanded && 'rotate-90')} />
        <span className="font-medium italic">Thinking…</span>
      </button>
      {expanded && (
        <div className="border-t border-neutral-200 px-2.5 py-2">
          <p className="whitespace-pre-wrap text-[11px] text-neutral-500 italic">{text}</p>
        </div>
      )}
    </div>
  )
}

function SubagentMessageRow({ msg }: { msg: SubagentMessage }) {
  return (
    <div className="space-y-1">
      {msg.text && (
        <p className="text-[11px] text-neutral-600 whitespace-pre-wrap">{msg.text}</p>
      )}
      {msg.toolUseBlocks && msg.toolUseBlocks.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {msg.toolUseBlocks.map(b => (
            <span
              key={b.id}
              className="inline-flex items-center gap-0.5 rounded bg-violet-100 px-1.5 py-0.5 text-[10px] font-medium text-violet-700"
            >
              <Wrench className="h-2.5 w-2.5" />
              {b.name}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

function AgentToolUseCard({ block }: { block: ToolUseBlock }) {
  const [expanded, setExpanded] = useState(false)
  const trace = block.subagentTrace!
  const stepCount = trace.messages.length

  return (
    <div className="rounded-lg border border-violet-200 bg-violet-50 overflow-hidden text-xs">
      <div className="flex items-center gap-1.5 px-2.5 py-1.5 bg-violet-100 border-b border-violet-200">
        <Bot className="h-3 w-3 text-violet-500 flex-shrink-0" />
        <span className="font-semibold text-violet-800">{trace.agentType || 'Agent'}</span>
        {trace.description && (
          <span className="text-violet-600 truncate">— {trace.description}</span>
        )}
        {trace.totalTokens != null && (
          <span className="ml-auto flex-shrink-0 text-violet-400">{trace.totalTokens.toLocaleString()} tokens</span>
        )}
      </div>

      {trace.summary && (
        <div className="px-2.5 py-1.5 text-neutral-600 line-clamp-3">{trace.summary}</div>
      )}

      {stepCount > 0 && (
        <button
          onClick={() => setExpanded(e => !e)}
          className="flex items-center gap-0.5 px-2.5 pb-1.5 text-violet-500 hover:text-violet-700"
        >
          <ChevronRight className={cn('h-3 w-3 transition-transform', expanded && 'rotate-90')} />
          {stepCount} step{stepCount !== 1 ? 's' : ''}
        </button>
      )}

      {expanded && (
        <div className="border-t border-violet-200 px-2.5 py-2 space-y-2">
          {trace.messages.map(msg => (
            <SubagentMessageRow key={msg.id} msg={msg} />
          ))}
        </div>
      )}
    </div>
  )
}

function ToolUseCard({ block }: { block: ToolUseBlock }) {
  const [expanded, setExpanded] = useState(false)
  const primary = primaryParam(block.name, block.input)
  const paramCount = Object.keys(block.input).length

  return (
    <div className="rounded-lg border border-neutral-200 bg-white overflow-hidden text-xs">
      <div className="flex items-center gap-1.5 px-2.5 py-1.5 bg-neutral-50 border-b border-neutral-100">
        <Wrench className="h-3 w-3 text-neutral-500 flex-shrink-0" />
        <span className="font-semibold text-neutral-700">{block.name}</span>
      </div>
      <div className="px-2.5 py-1.5">
        {primary && (
          <p className="text-neutral-500 truncate font-mono text-[11px]">{primary}</p>
        )}
        {paramCount > 0 && (
          <button
            onClick={() => setExpanded(e => !e)}
            className="mt-1 flex items-center gap-0.5 text-neutral-400 hover:text-neutral-600"
          >
            <ChevronRight className={cn('h-3 w-3 transition-transform', expanded && 'rotate-90')} />
            {paramCount} param{paramCount !== 1 ? 's' : ''}
          </button>
        )}
        {expanded && (
          <pre className="mt-1.5 text-[11px] text-neutral-600 overflow-auto rounded bg-neutral-50 p-2 max-h-48">
            {JSON.stringify(block.input, null, 2)}
          </pre>
        )}
      </div>
    </div>
  )
}

function PermissionCard({
  request,
  onApprove,
}: {
  request: PermissionRequest
  onApprove: (approved: boolean) => void
}) {
  const [decided, setDecided] = useState<'allow' | 'deny' | null>(null)
  const primary = primaryParam(request.toolName, request.toolInput)
  const paramCount = Object.keys(request.toolInput).length
  const [expanded, setExpanded] = useState(false)

  function handleDecide(approved: boolean) {
    setDecided(approved ? 'allow' : 'deny')
    onApprove(approved)
  }

  return (
    <div className={cn(
      'rounded-lg border overflow-hidden text-xs',
      !decided ? 'border-amber-300 bg-amber-50'
        : decided === 'deny' ? 'border-red-200 bg-red-50'
        : 'border-neutral-200 bg-white',
    )}>
      <div className={cn(
        'flex items-center gap-1.5 px-2.5 py-1.5',
        !decided ? 'bg-amber-100 border-b border-amber-200'
          : decided === 'deny' ? 'bg-red-100 border-b border-red-200'
          : 'bg-neutral-50 border-b border-neutral-100',
      )}>
        <Wrench className="h-3 w-3 text-neutral-500 flex-shrink-0" />
        <span className="font-semibold text-neutral-700">{request.toolName}</span>
        {!decided && <span className="ml-auto text-amber-600 font-medium">Awaiting permission</span>}
        {decided === 'allow' && <span className="ml-auto text-green-600 font-medium">Allowed</span>}
        {decided === 'deny' && <span className="ml-auto text-red-600 font-medium">Denied</span>}
      </div>

      <div className="px-2.5 py-1.5">
        {primary && (
          <p className="text-neutral-500 truncate font-mono text-[11px]">{primary}</p>
        )}
        {request.decisionReason && (
          <p className="mt-1 text-amber-700 text-[11px]">{request.decisionReason}</p>
        )}
        {request.blockedPath && (
          <p className="mt-0.5 font-mono text-[11px] text-neutral-500 truncate">{request.blockedPath}</p>
        )}
        {paramCount > 0 && (
          <button
            onClick={() => setExpanded(e => !e)}
            className="mt-1 flex items-center gap-0.5 text-neutral-400 hover:text-neutral-600"
          >
            <ChevronRight className={cn('h-3 w-3 transition-transform', expanded && 'rotate-90')} />
            {paramCount} param{paramCount !== 1 ? 's' : ''}
          </button>
        )}
        {expanded && (
          <pre className="mt-1.5 text-[11px] text-neutral-600 overflow-auto rounded bg-neutral-50 p-2 max-h-48">
            {JSON.stringify(request.toolInput, null, 2)}
          </pre>
        )}
      </div>

      {!decided && (
        <div className="flex gap-2 px-2.5 pb-2.5">
          <button
            onClick={() => handleDecide(true)}
            className="flex-1 rounded-md bg-green-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-green-700 active:bg-green-800"
          >
            Allow
          </button>
          <button
            onClick={() => handleDecide(false)}
            className="flex-1 rounded-md bg-red-100 px-3 py-1.5 text-xs font-medium text-red-700 hover:bg-red-200 active:bg-red-300"
          >
            Deny
          </button>
        </div>
      )}
    </div>
  )
}

function QuestionCard({
  questions,
  onRespond,
  initialAnswers,
}: {
  questions: Question[]
  onRespond: (answers: Record<string, string | string[]>) => void
  initialAnswers?: Record<string, string | string[]>
}) {
  const [answers, setAnswers] = useState<Record<string, string | string[]>>(() =>
    initialAnswers ?? Object.fromEntries(questions.map(q => [q.question, q.multiSelect ? [] : '']))
  )
  const [submitted, setSubmitted] = useState(!!initialAnswers)

  function toggle(question: string, label: string, multiSelect: boolean) {
    setAnswers(prev => {
      if (multiSelect) {
        const current = (prev[question] as string[]) ?? []
        return {
          ...prev,
          [question]: current.includes(label) ? current.filter(l => l !== label) : [...current, label],
        }
      }
      return { ...prev, [question]: label }
    })
  }

  function isSelected(question: string, label: string): boolean {
    const val = answers[question]
    return Array.isArray(val) ? val.includes(label) : val === label
  }

  function canSubmit(): boolean {
    return questions.every(q => {
      const val = answers[q.question]
      return Array.isArray(val) ? val.length > 0 : val !== ''
    })
  }

  function handleSubmit() {
    setSubmitted(true)
    onRespond(answers)
  }

  return (
    <div className="rounded-lg border border-blue-300 bg-blue-50 overflow-hidden text-xs">
      <div className="flex items-center gap-1.5 px-2.5 py-1.5 bg-blue-100 border-b border-blue-200">
        <span className="font-semibold text-blue-800">
          {submitted ? 'Question answered' : 'Claude is asking'}
        </span>
      </div>
      <div className="p-2.5 space-y-3">
        {questions.map((q) => (
          <div key={q.question}>
            <p className="font-medium text-neutral-700 mb-1.5">{q.question}</p>
            <div className="space-y-1">
              {q.options.map(opt => (
                <button
                  key={opt.label}
                  disabled={submitted}
                  onClick={() => toggle(q.question, opt.label, q.multiSelect)}
                  className={cn(
                    'w-full text-left rounded-md px-2.5 py-1.5 border transition-colors',
                    isSelected(q.question, opt.label)
                      ? 'border-blue-400 bg-blue-100 text-blue-800'
                      : 'border-neutral-200 bg-white text-neutral-700 hover:border-neutral-300',
                    submitted && 'cursor-default opacity-70',
                  )}
                >
                  <span className="font-medium">{opt.label}</span>
                  {opt.description && (
                    <span className="ml-1.5 text-neutral-500">{opt.description}</span>
                  )}
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
      {!submitted && (
        <div className="px-2.5 pb-2.5">
          <button
            onClick={handleSubmit}
            disabled={!canSubmit()}
            className="w-full rounded-md bg-blue-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-blue-700 active:bg-blue-800 disabled:opacity-40 disabled:cursor-not-allowed"
          >
            Submit
          </button>
        </div>
      )}
    </div>
  )
}

export function ChatMessage({ message, onApprovePermission, onAnswerQuestion }: Props) {
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
          <>
            {message.permissionRequest && onApprovePermission && (
              <div className="mb-2">
                <PermissionCard
                  request={message.permissionRequest}
                  onApprove={onApprovePermission}
                />
              </div>
            )}

            {message.pendingQuestions && message.pendingQuestions.length > 0 && onAnswerQuestion && (
              <div className="mb-2">
                <QuestionCard
                  questions={message.pendingQuestions}
                  onRespond={onAnswerQuestion}
                />
              </div>
            )}

            {message.answeredQuestions?.map((aq: AnsweredQuestion, i: number) => (
              <div key={i} className="mb-2">
                <QuestionCard
                  questions={aq.questions}
                  initialAnswers={aq.answers}
                  onRespond={() => {}}
                />
              </div>
            ))}

            {message.thinkingBlocks && message.thinkingBlocks.length > 0 && (
              <ThinkingCard blocks={message.thinkingBlocks} />
            )}

            {message.toolUseBlocks && message.toolUseBlocks.length > 0 && (
              <div className="mb-2 space-y-1.5">
                {message.toolUseBlocks.map((block) =>
                  block.name === 'Agent' && block.subagentTrace
                    ? <AgentToolUseCard key={block.id} block={block} />
                    : <ToolUseCard key={block.id} block={block} />
                )}
              </div>
            )}

            {message.text && (
              <div className="prose prose-sm prose-neutral max-w-none">
                <ReactMarkdown>{message.text}</ReactMarkdown>
                {message.status === 'streaming' && (
                  <span className="inline-block w-0.5 h-4 bg-neutral-400 animate-pulse ml-0.5 align-text-bottom" />
                )}
              </div>
            )}

            {message.status === 'streaming' && !message.text && (
              <span className="inline-block w-0.5 h-4 bg-neutral-400 animate-pulse align-text-bottom" />
            )}
          </>
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
