import { useEffect, useRef, useState, type FormEvent } from 'react'
import { LoaderCircle } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import { buildStructuredToolMessage, parseStructuredToolMessage } from '../services/tool-message'
import type { RenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'
import type { LiveRunOverlay, MessageSnapshot, PendingUserMessage } from '../../state/desktop-v3-cache-types'
import type { StructuredToolMessage, ToolMessageState } from '../types/chat'
import {
  clearDesktopV3ExistingMessageOperation,
  continueDesktopV3Conversation,
  createDesktopV3ExistingMessageOperation,
  loadDesktopV3ExistingMessageOperation,
  persistDesktopV3ExistingMessageOperation,
  type DesktopV3ExistingMessageOperation,
} from '../../session-v3/existing-session-flow'

export interface DesktopV3ExistingConversationPaneProps {
  sessionId: string
  initialHydrateStatus: 'idle' | 'loading' | 'cached' | 'ready' | 'error'
  renderedMessages: RenderedSessionMessages
  messagesLoaded: boolean
  metadata?: Record<string, unknown>
}

export function DesktopV3ExistingConversationPane({
  sessionId,
  initialHydrateStatus,
  renderedMessages,
  messagesLoaded,
  metadata,
}: DesktopV3ExistingConversationPaneProps) {
  const normalizedSessionId = sessionId.trim()
  const operationRef = useRef<DesktopV3ExistingMessageOperation | null>(
    loadDesktopV3ExistingMessageOperation(normalizedSessionId),
  )
  const [draft, setDraft] = useState(operationRef.current?.request.content ?? '')
  const [sendError, setSendError] = useState<string | null>(null)
  const [sending, setSending] = useState(false)
  const scrollTailRef = useRef<HTMLDivElement | null>(null)

  const retainedOperation = operationRef.current
  const hasRetainedOperation = Boolean(retainedOperation)
  const hasMessages = renderedMessages.committed.length > 0
    || renderedMessages.pendingUser.length > 0
    || renderedMessages.liveRuns.length > 0
  const canSend = Boolean(normalizedSessionId && !sending && (hasRetainedOperation || draft.trim()))

  useEffect(() => {
    const operation = loadDesktopV3ExistingMessageOperation(normalizedSessionId)
    operationRef.current = operation
    setDraft(operation?.request.content ?? '')
    setSendError(null)
  }, [normalizedSessionId])

  useEffect(() => {
    scrollTailRef.current?.scrollIntoView({ block: 'end' })
  }, [normalizedSessionId, renderedMessages.committed.length, renderedMessages.pendingUser.length, renderedMessages.liveRuns.length])

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!normalizedSessionId || sending) return

    setSending(true)
    setSendError(null)
    try {
      const operation = operationRef.current
        ?? createDesktopV3ExistingMessageOperation({
          sessionId: normalizedSessionId,
          prompt: draft,
          metadata,
        })
      operationRef.current = operation
      setDraft(operation.request.content)
      persistDesktopV3ExistingMessageOperation(operation)

      await continueDesktopV3Conversation(operation)
      clearDesktopV3ExistingMessageOperation(normalizedSessionId, operation.operationId)
      operationRef.current = null
      setDraft('')
    } catch (error) {
      setSendError(error instanceof Error ? error.message : String(error))
    } finally {
      setSending(false)
    }
  }

  function handleAbandonRetainedOperation() {
    const operation = operationRef.current
    if (!operation) return
    const confirmed = window.confirm(
      'Abandon this pending existing-message operation? The message or run may already exist. Retrying is the safest way to avoid duplicates.',
    )
    if (!confirmed) return
    clearDesktopV3ExistingMessageOperation(normalizedSessionId, operation.operationId)
    operationRef.current = null
    setDraft('')
    setSendError(null)
  }

  if (!normalizedSessionId) {
    return <DesktopV3ChatStateCard title="Select a session" description="Choose a session from the sidebar to view its conversation." />
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]" data-testid="desktop-v3-existing-conversation-pane">
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-6 sm:px-8">
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-5">
          {initialHydrateStatus === 'loading' && !messagesLoaded ? (
            <DesktopV3ChatInlineState title="Loading conversation…" description="Hydrating cached message tails." />
          ) : null}
          {initialHydrateStatus === 'error' && !messagesLoaded && !hasMessages ? (
            <DesktopV3ChatInlineState title="Conversation unavailable" description="Initial message hydrate failed. You can still send from this session while cached state recovers." tone="error" />
          ) : null}
          {!hasMessages && initialHydrateStatus !== 'loading' && initialHydrateStatus !== 'error' ? (
            <DesktopV3ChatInlineState title="Empty conversation" description="Send a message to continue this session." />
          ) : null}
          {renderedMessages.committed.map((message) => (
            <DesktopV3CommittedMessage key={message.id} message={message} />
          ))}
          {renderedMessages.pendingUser.map((message) => (
            <DesktopV3PendingUserMessage key={message.clientRequestId} message={message} />
          ))}
          {renderedMessages.liveRuns.map((run) => (
            <DesktopV3LiveRun key={run.runId} run={run} />
          ))}
          <div ref={scrollTailRef} />
        </div>
      </div>

      {hasRetainedOperation ? (
        <div className="border-t border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text-muted)] sm:px-8">
          <div className="mx-auto flex w-full max-w-3xl items-center justify-between gap-3">
            <span>A pending message is retained for safe retry. Edits are disabled until it succeeds or you abandon it.</span>
            <Button type="button" variant="ghost" onClick={handleAbandonRetainedOperation} disabled={sending}>
              Abandon retained message
            </Button>
          </div>
        </div>
      ) : null}

      <form onSubmit={handleSubmit} className="border-t border-[var(--app-border)] bg-[var(--app-bg)] px-4 pb-[calc(var(--app-safe-area-bottom)_+_1rem)] pt-3 sm:px-8" data-testid="desktop-v3-existing-composer">
        <div className="mx-auto flex w-full max-w-3xl items-end gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 shadow-sm focus-within:border-[var(--app-border-strong)]">
          <textarea
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' && !event.shiftKey) {
                event.preventDefault()
                event.currentTarget.form?.requestSubmit()
              }
            }}
            rows={1}
            placeholder={hasRetainedOperation ? 'Retained message' : 'Message Swarm…'}
            aria-label="Continue Desktop V3 conversation"
            data-testid="desktop-v3-existing-input"
            disabled={sending || hasRetainedOperation}
            className="max-h-40 min-h-10 flex-1 resize-none bg-transparent py-2 text-sm leading-5 text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-subtle)] disabled:opacity-80"
          />
          <Button type="submit" disabled={!canSend} className="h-10 rounded-xl px-4" data-testid="desktop-v3-existing-send">
            {sending ? <LoaderCircle size={16} className="animate-spin" /> : hasRetainedOperation ? 'Retry message' : 'Send'}
          </Button>
        </div>
        {sendError ? (
          <div className="mx-auto mt-2 w-full max-w-3xl text-xs text-[var(--app-danger)]" role="alert">
            {sendError}
          </div>
        ) : null}
      </form>
    </div>
  )
}

function DesktopV3CommittedMessage({ message }: { message: MessageSnapshot }) {
  const role = message.role || 'message'
  const toolMessage = parseStructuredToolMessage(message.content)
  if (toolMessage || role === 'tool') {
    return <DesktopV3ToolMessage content={message.content} toolMessage={toolMessage} />
  }
  if (role === 'user') {
    return <DesktopV3UserMessage content={message.content} />
  }
  if (role === 'assistant' || role === 'reasoning') {
    return <DesktopV3AssistantMessage content={message.content} role={role} />
  }
  return (
    <div className="flex justify-start">
      <div className="max-w-[78%] rounded-2xl border border-[var(--app-border)] px-4 py-3 text-sm text-[var(--app-text)]">
        <div className="mb-1 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">{role}</div>
        <ChatMarkdown content={message.content} />
      </div>
    </div>
  )
}

function DesktopV3UserMessage({ content, pendingLabel }: { content: string; pendingLabel?: string }) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[78%] rounded-3xl bg-[var(--app-primary)] px-4 py-3 text-sm leading-6 text-[var(--app-primary-text)] shadow-sm">
        <div className="whitespace-pre-wrap break-words">{content}</div>
        {pendingLabel ? <div className="mt-1 text-right text-[10px] uppercase tracking-[0.12em] opacity-70">{pendingLabel}</div> : null}
      </div>
    </div>
  )
}

function DesktopV3PendingUserMessage({ message }: { message: PendingUserMessage }) {
  return <DesktopV3UserMessage content={message.content} pendingLabel={message.status === 'failed' ? message.error || 'failed' : 'sending'} />
}

function DesktopV3AssistantMessage({ content, role }: { content: string; role: string }) {
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[82%] text-sm leading-6 text-[var(--app-text)]">
        {role === 'reasoning' ? <div className="mb-1 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">reasoning</div> : null}
        <ChatMarkdown content={content} />
      </div>
    </div>
  )
}

function DesktopV3ToolMessage({ content, toolMessage }: { content: string; toolMessage: StructuredToolMessage | null }) {
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[82%]">
        <ChatMarkdown content={content} toolMessage={toolMessage ?? undefined} />
      </div>
    </div>
  )
}

function DesktopV3LiveRun({ run }: { run: LiveRunOverlay }) {
  const toolCalls = Object.values(run.toolCallsByCallId).sort((left, right) => left.updatedAt - right.updatedAt || left.callId.localeCompare(right.callId))
  const reasoningContent = [run.reasoning?.summary, run.reasoning?.text]
    .filter((value): value is string => Boolean(value?.trim()))
    .join('\n\n')
  return (
    <div className="flex flex-col gap-3">
      {reasoningContent ? (
        <DesktopV3AssistantMessage content={reasoningContent} role="reasoning" />
      ) : null}
      {toolCalls.map((tool) => (
        <DesktopV3LiveToolCall key={tool.callId} tool={tool} />
      ))}
      {run.assistantDraft?.content ? (
        <DesktopV3AssistantMessage content={run.assistantDraft.content} role="assistant" />
      ) : run.status === 'running' || run.status === 'pending_executor' ? (
        <div className="flex justify-start text-xs text-[var(--app-text-subtle)]">
          <span className="inline-flex items-center gap-2">
            <LoaderCircle size={13} className="animate-spin" /> assistant is working
          </span>
        </div>
      ) : null}
    </div>
  )
}

function DesktopV3LiveToolCall({ tool }: { tool: LiveRunOverlay['toolCallsByCallId'][string] }) {
  const state: ToolMessageState = tool.status === 'failed' || tool.status === 'error' ? 'error' : tool.status === 'completed' || tool.status === 'done' || tool.status === 'cancelled' || tool.status === 'canceled' ? 'done' : 'running'
  const output = tool.outputText?.trim() ?? ''
  const args = tool.argumentsText?.trim() ?? ''
  const error = tool.errorText?.trim() || (state === 'error' ? output : '')
  const parsed = buildStructuredToolMessage({
    pathId: 'run.v3.provider-tool-result.v1',
    tool: tool.toolName || 'tool',
    callId: tool.callId,
    toolInstanceId: tool.toolInstanceId,
    argumentsText: args,
    outputText: output,
    error,
    durationMs: tool.durationMs,
    state,
  })
  return <DesktopV3ToolMessage content="" toolMessage={parsed} />
}

function DesktopV3ChatInlineState({ title, description, tone = 'muted' }: { title: string; description: string; tone?: 'muted' | 'error' }) {
  return (
    <div className={cn('py-16 text-center', tone === 'error' ? 'text-[var(--app-danger)]' : 'text-[var(--app-text-muted)]')}>
      <div className="text-sm font-semibold text-[var(--app-text)]">{title}</div>
      <p className="mt-2 text-sm">{description}</p>
    </div>
  )
}

function DesktopV3ChatStateCard({ title, description }: { title: string; description: string }) {
  return (
    <div className="flex h-full flex-1 items-center justify-center px-6 text-center">
      <div className="max-w-lg">
        <div className="text-lg font-semibold">{title}</div>
        <p className="mt-2 text-sm text-[var(--app-text-muted)]">{description}</p>
      </div>
    </div>
  )
}
