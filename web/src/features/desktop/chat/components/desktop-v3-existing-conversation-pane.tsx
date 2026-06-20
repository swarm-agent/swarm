import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { LoaderCircle } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import { buildStructuredToolMessage, parseStructuredToolMessage } from '../services/tool-message'
import type { RenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'
import type { LiveRunOverlay, MessageSnapshot, PendingUserMessage } from '../../state/desktop-v3-cache-types'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import type { DesktopSessionRecord } from '../../types/realtime'
import type { StructuredToolMessage, ToolMessageState, AgentStateRecord, ModelOptionRecord, SessionPreferenceRecord } from '../types/chat'
import { resolveDesktopChatRouteFromSession, type DesktopChatRoute } from '../services/chat-routing'
import { agentStateQueryOptions, modelOptionsQueryOptions, uiSettingsQueryOptions } from '../../../queries/query-options'
import { normalizeThinkingTagsEnabled } from '../../settings/swarm/types/swarm-settings'
import { supportsCodexFastMode, formatContextWindow, effectiveContextWindow } from '../services/model-options'
import { DesktopV3AgenticComposer } from './desktop-v3-agentic-composer'
import { updateSessionV3Agent, updateSessionV3Mode, updateSessionV3Preference, stopSessionV3Run } from '../../session-v3/api'
import {
  clearDesktopV3ExistingMessageOperation,
  continueDesktopV3Conversation,
  createDesktopV3ExistingMessageOperation,
  loadDesktopV3ExistingMessageOperation,
  persistDesktopV3ExistingMessageOperation,
  type DesktopV3ExistingMessageOperation,
} from '../../session-v3/existing-session-flow'

const EMPTY_AGENT_STATE: AgentStateRecord = {
  profiles: [],
  activePrimary: '',
  activeSubagent: {},
  version: 0,
  providerDefaultsPreview: null,
  toolInventory: null,
}

function optionKey(provider: string, model: string, contextMode = ''): string {
  return `${provider}:${model}:${contextMode.trim().toLowerCase()}`
}

function metadataString(metadata: Record<string, unknown> | null | undefined, key: string): string {
  const value = metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function normalizePreference(value: unknown): SessionPreferenceRecord {
  const record = value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
  const nested = record.preference && typeof record.preference === 'object' && !Array.isArray(record.preference)
    ? record.preference as Record<string, unknown>
    : record
  return {
    provider: String(nested.provider ?? '').trim(),
    model: String(nested.model ?? '').trim(),
    thinking: String(nested.thinking ?? '').trim(),
    serviceTier: String(nested.serviceTier ?? nested.service_tier ?? '').trim(),
    contextMode: String(nested.contextMode ?? nested.context_mode ?? '').trim(),
    updatedAt: typeof nested.updatedAt === 'number' ? nested.updatedAt : typeof nested.updated_at === 'number' ? nested.updated_at : 0,
  }
}

function preferenceFromOption(option: ModelOptionRecord | null, current: SessionPreferenceRecord): SessionPreferenceRecord {
  if (!option) return current
  return {
    ...current,
    provider: option.provider,
    model: option.model,
    thinking: current.thinking || option.thinking,
    contextMode: option.contextMode,
  }
}

function fastToggleFromPreference(preference: SessionPreferenceRecord): 'on' | 'off' {
  return preference.serviceTier.trim().toLowerCase() === 'fast' ? 'on' : 'off'
}

function preferencesEqual(left: SessionPreferenceRecord, right: SessionPreferenceRecord): boolean {
  return left.provider === right.provider
    && left.model === right.model
    && left.thinking === right.thinking
    && left.serviceTier === right.serviceTier
    && left.contextMode === right.contextMode
}

export interface DesktopV3ExistingConversationPaneProps {
  sessionId: string
  initialHydrateStatus: 'idle' | 'loading' | 'cached' | 'ready' | 'error'
  renderedMessages: RenderedSessionMessages
  messagesLoaded: boolean
  metadata?: Record<string, unknown>
  session?: DesktopSessionRecord | null
  routeOptions?: DesktopChatRoute[]
}

export function completeDesktopV3ExistingMessage(input: {
  sessionId: string
  operation: DesktopV3ExistingMessageOperation
  mountedRef: { current: boolean }
  setOperation: (operation: DesktopV3ExistingMessageOperation | null) => void
  setDraft: (draft: string) => void
}): void {
  clearDesktopV3ExistingMessageOperation(input.sessionId, input.operation.operationId)
  if (!input.mountedRef.current) return
  input.setOperation(null)
  input.setDraft('')
}

export function DesktopV3ExistingConversationPane({
  sessionId,
  initialHydrateStatus,
  renderedMessages,
  messagesLoaded,
  metadata,
  session,
  routeOptions = [],
}: DesktopV3ExistingConversationPaneProps) {
  const normalizedSessionId = sessionId.trim()
  const mountedRef = useRef(true)
  const operationRef = useRef<DesktopV3ExistingMessageOperation | null>(
    loadDesktopV3ExistingMessageOperation(normalizedSessionId),
  )
  const agentStateQuery = useQuery(agentStateQueryOptions())
  const modelOptionsQuery = useQuery(modelOptionsQueryOptions())
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())
  const agentState = agentStateQuery.data ?? EMPTY_AGENT_STATE
  const modelOptions = modelOptionsQuery.data ?? []
  const thinkingTagsEnabled = normalizeThinkingTagsEnabled(uiSettingsQuery.data)
  const cachedPreference = useDesktopV3CacheSelector((state) => normalizePreference(state.preferencesBySession[normalizedSessionId]))
  const cacheSession = useDesktopV3CacheSelector((state) => {
    const record = state.sessionsById[normalizedSessionId]
    return record?.kind === 'full' ? record.session : null
  })
  const currentRun = renderedMessages.liveRuns.find((run) => run.status === 'running' || run.status === 'pending_executor') ?? null
  const sessionMetadata = session?.metadata ?? cacheSession?.metadata ?? metadata
  const initialAgent = metadataString(sessionMetadata, 'agent_name') || metadataString(sessionMetadata, 'resolved_agent_name') || agentState.activePrimary || ''
  const retainedOperation = operationRef.current
  const [draft, setDraft] = useState(retainedOperation?.request.content ?? '')
  const [sendError, setSendError] = useState<string | null>(null)
  const [sending, setSending] = useState(false)
  const [mode, setMode] = useState<'auto' | 'plan'>((session?.mode || cacheSession?.mode) === 'plan' ? 'plan' : 'auto')
  const [selectedAgent, setSelectedAgent] = useState(initialAgent)
  const [preference, setPreference] = useState<SessionPreferenceRecord>(cachedPreference)
  const scrollTailRef = useRef<HTMLDivElement | null>(null)

  const hasRetainedOperation = Boolean(retainedOperation)
  const hasMessages = renderedMessages.committed.length > 0
    || renderedMessages.pendingUser.length > 0
    || renderedMessages.liveRuns.length > 0
  const selectedModelKey = optionKey(preference.provider, preference.model, preference.contextMode)
  const selectedModelOption = modelOptions.find((option) => option.key === selectedModelKey) ?? null
  const selectedModelAvailable = Boolean(selectedModelOption)
  const fastSupported = selectedModelOption ? supportsCodexFastMode(selectedModelOption.provider, selectedModelOption.model) : false
  const selectedContextWindow = selectedModelOption
    ? effectiveContextWindow(selectedModelOption.provider, selectedModelOption.model, selectedModelOption.contextMode, selectedModelOption.contextWindow)
    : 0
  const contextLabel = selectedContextWindow > 0 ? `${formatContextWindow(selectedContextWindow)} ctx` : 'ctx'
  const route = useMemo(
    () => resolveDesktopChatRouteFromSession(session ?? null, routeOptions, routeOptions[0] ?? null),
    [routeOptions, session],
  )
  const canSend = Boolean(normalizedSessionId && !sending && selectedAgent.trim() && selectedModelAvailable && (hasRetainedOperation || draft.trim()))

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  useEffect(() => {
    const operation = loadDesktopV3ExistingMessageOperation(normalizedSessionId)
    operationRef.current = operation
    setDraft(operation?.request.content ?? '')
    setSendError(null)
  }, [normalizedSessionId])

  useEffect(() => {
    scrollTailRef.current?.scrollIntoView({ block: 'end' })
  }, [normalizedSessionId, renderedMessages.committed.length, renderedMessages.pendingUser.length, renderedMessages.liveRuns.length])

  useEffect(() => {
    setMode((session?.mode || cacheSession?.mode) === 'plan' ? 'plan' : 'auto')
  }, [cacheSession?.mode, session?.mode])

  useEffect(() => {
    if (initialAgent) setSelectedAgent(initialAgent)
  }, [initialAgent])

  useEffect(() => {
    setPreference((current) => preferencesEqual(current, cachedPreference) ? current : cachedPreference)
  }, [cachedPreference])

  function handleModelSelect(key: string) {
    const option = modelOptions.find((candidate) => candidate.key === key) ?? null
    setPreference((current) => preferenceFromOption(option, current))
  }

  function handleThinkingChange(value: string) {
    setPreference((current) => ({ ...current, thinking: value.trim() === 'off' ? '' : value.trim() }))
  }

  function handleFastChange(value: 'on' | 'off') {
    setPreference((current) => ({
      ...current,
      serviceTier: value === 'on' && fastSupported ? 'fast' : '',
    }))
  }

  async function persistVisibleSettings() {
    if (!normalizedSessionId) return
    const tasks: Array<Promise<unknown>> = []
    if ((session?.mode || cacheSession?.mode || 'auto') !== mode) {
      tasks.push(updateSessionV3Mode(normalizedSessionId, mode))
    }
    const currentAgent = initialAgent.trim()
    if (selectedAgent.trim() && selectedAgent.trim() !== currentAgent) {
      tasks.push(updateSessionV3Agent(normalizedSessionId, selectedAgent.trim()))
    }
    if (!preferencesEqual(preference, cachedPreference)) {
      tasks.push(updateSessionV3Preference(normalizedSessionId, preference))
    }
    if (tasks.length > 0) await Promise.all(tasks)
  }

  async function handleSubmit() {
    if (!normalizedSessionId || sending) return

    setSending(true)
    setSendError(null)
    try {
      if (!selectedModelAvailable) {
        throw new Error('Select a model before sending')
      }
      await persistVisibleSettings()
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
      completeDesktopV3ExistingMessage({
        sessionId: normalizedSessionId,
        operation,
        mountedRef,
        setOperation: (nextOperation) => {
          operationRef.current = nextOperation
        },
        setDraft,
      })
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : String(error))
      }
    } finally {
      if (mountedRef.current) {
        setSending(false)
      }
    }
  }

  async function handleStop() {
    if (!normalizedSessionId || !currentRun?.runId) return
    try {
      await stopSessionV3Run(normalizedSessionId, { runId: currentRun.runId })
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : String(error))
      }
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

      <DesktopV3AgenticComposer
        draft={draft}
        onDraftChange={setDraft}
        placeholder={hasRetainedOperation ? 'Retained message' : 'Message Swarm…'}
        inputLabel="Continue Desktop V3 conversation"
        disabled={sending}
        locked={hasRetainedOperation}
        busy={sending}
        canSubmit={canSend}
        canStop={Boolean(currentRun)}
        error={sendError}
        retainedNotice={hasRetainedOperation ? 'A pending message is retained for safe retry. Edits are disabled until it succeeds or you abandon it.' : null}
        onAbandonRetained={handleAbandonRetainedOperation}
        onSubmit={handleSubmit}
        onStop={handleStop}
        mode={mode}
        onModeChange={setMode}
        currentAgent={selectedAgent || agentState.activePrimary || 'Agent'}
        selectedPrimaryAgent={selectedAgent || agentState.activePrimary || ''}
        agents={agentState.profiles}
        onAgentSelect={setSelectedAgent}
        modelOptions={modelOptions}
        selectedModelKey={selectedModelKey}
        selectedModelAvailable={selectedModelAvailable}
        onModelSelect={handleModelSelect}
        thinking={preference.thinking}
        onThinkingChange={handleThinkingChange}
        thinkingTagsEnabled={thinkingTagsEnabled}
        fast={fastToggleFromPreference(preference)}
        onFastChange={handleFastChange}
        route={route}
        routeOptions={routeOptions}
        routeTitle="Changing the route starts a new session in this workspace."
        contextLabel={contextLabel}
        compactDisabled
      />
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
