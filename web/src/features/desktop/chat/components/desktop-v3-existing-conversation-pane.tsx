import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { CheckCircle2, LoaderCircle, XCircle } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import { buildStructuredToolMessage, parseStructuredToolMessage } from '../services/tool-message'
import type { RenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'
import type { LiveRunOverlay, MessageSnapshot, PendingUserMessage } from '../../state/desktop-v3-cache-types'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import type { DesktopSessionRecord } from '../../types/realtime'
import type { StructuredToolMessage, ToolMessageState, AgentStateRecord, ModelOptionRecord, SessionPreferenceRecord } from '../types/chat'
import { getDesktopSessionStopTarget, resolveDesktopChatRouteFromSession, type DesktopChatRoute } from '../services/chat-routing'
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

type DesktopV3RenderItem =
  | { type: 'message'; message: MessageSnapshot; timelineSeq?: number }
  | { type: 'pending-user'; message: PendingUserMessage; timelineSeq?: number }
  | { type: 'live-assistant'; id: string; content: string; timelineSeq?: number }
  | { type: 'live-reasoning'; id: string; text: string; summary: string; state: NonNullable<LiveRunOverlay['reasoning']>['state']; startedAt: number | null; completedAt?: number | null; timelineSeq?: number }
  | { type: 'live-tool'; id: string; tool: LiveRunOverlay['toolCallsByCallId'][string]; timelineSeq?: number }
  | { type: 'live-working'; id: string; timelineSeq?: number }

function numericTimelineSeq(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0
}

function renderItemTimelineSeq(item: DesktopV3RenderItem): number {
  switch (item.type) {
    case 'message':
      return numericTimelineSeq(item.timelineSeq ?? item.message.global_seq)
    case 'pending-user':
      return numericTimelineSeq(item.timelineSeq ?? item.message.createdAt)
    default:
      return numericTimelineSeq(item.timelineSeq)
  }
}

export function orderDesktopV3LiveRenderItems(items: DesktopV3RenderItem[]): DesktopV3RenderItem[] {
  return items
    .map((item, index) => ({ item, index, seq: renderItemTimelineSeq(item) }))
    .sort((left, right) => {
      const leftSequenced = left.seq > 0
      const rightSequenced = right.seq > 0
      if (leftSequenced && rightSequenced && left.seq !== right.seq) {
        return left.seq - right.seq
      }
      if (leftSequenced !== rightSequenced) {
        return leftSequenced ? -1 : 1
      }
      return left.index - right.index
    })
    .map((entry) => entry.item)
}

function reasoningElapsedLabel(startedAt: number | null, completedAt: number | null | undefined, now: number): string {
  if (typeof startedAt !== 'number' || startedAt <= 0) return ''
  const endAt = typeof completedAt === 'number' && completedAt > startedAt ? completedAt : now
  const elapsed = Math.max(0, endAt - startedAt)
  if (elapsed < 1000) return `${elapsed}ms`
  if (elapsed < 60_000) return `${(elapsed / 1000).toFixed(1)}s`
  return `${(elapsed / 60_000).toFixed(1)}m`
}

function reasoningHeadline(state: NonNullable<LiveRunOverlay['reasoning']>['state'], startedAt: number | null, completedAt: number | null | undefined, now: number): string {
  const label = state === 'error' ? 'Thinking failed' : 'Thinking'
  const elapsed = reasoningElapsedLabel(startedAt, state === 'running' ? null : completedAt, now)
  return elapsed ? `${label} · ${elapsed}` : label
}

function reasoningBody(text: string, summary: string, thinkingTagsEnabled: boolean): string {
  if (!thinkingTagsEnabled) return ''
  return text.trim() || summary.trim() || 'Thinking…'
}

function normalizeReplayContent(content: string): string {
  return content.trim().replace(/\s+/g, ' ')
}

function canonicalContentSet(messages: MessageSnapshot[], role: string): Set<string> {
  const contents = new Set<string>()
  for (const message of messages) {
    if (message.role !== role) continue
    const normalized = normalizeReplayContent(message.content)
    if (normalized) contents.add(normalized)
  }
  return contents
}

export function buildDesktopV3LiveRunRenderItems(run: LiveRunOverlay, options: { assistantMessages?: Set<string>; reasoningMessages?: Set<string> } = {}): DesktopV3RenderItem[] {
  const items: DesktopV3RenderItem[] = []
  for (const segment of run.assistantSegments ?? []) {
    const content = segment.content.trim()
    if (!content || options.assistantMessages?.has(normalizeReplayContent(content))) continue
    items.push({ type: 'live-assistant', id: segment.id, content, timelineSeq: segment.timelineSeq })
  }
  const reasoningRecords = Object.values(run.reasoningByKey ?? (run.reasoning ? { active: run.reasoning } : {}))
  for (const reasoning of reasoningRecords) {
    const text = reasoning.text.trim()
    const summary = reasoning.summary.trim()
    if (reasoning.state === 'completed' && (options.reasoningMessages?.has(normalizeReplayContent(text)) || options.reasoningMessages?.has(normalizeReplayContent(summary)))) continue
    if (!text && !summary && reasoning.state !== 'running') continue
    items.push({
      type: 'live-reasoning',
      id: `live-reasoning:${reasoning.key || reasoning.reasoningId || reasoning.reasoningKey || run.runId}`,
      text,
      summary,
      state: reasoning.state,
      startedAt: reasoning.startedAt,
      completedAt: reasoning.completedAt,
      timelineSeq: reasoning.timelineSeq,
    })
  }
  for (const tool of Object.values(run.toolCallsByCallId)) {
    items.push({ type: 'live-tool', id: `live-tool:${tool.toolInstanceId || tool.callId}`, tool, timelineSeq: tool.timelineSeq })
  }
  if (run.assistantDraft?.content) {
    items.push({ type: 'live-assistant', id: `live-assistant:${run.runId}:draft`, content: run.assistantDraft.content, timelineSeq: run.assistantDraft.timelineSeq })
  } else if (run.status === 'running' || run.status === 'pending_executor') {
    items.push({ type: 'live-working', id: `live-working:${run.runId}`, timelineSeq: (run.lastEventSeqSeen ?? 0) + 1 })
  }
  return orderDesktopV3LiveRenderItems(items)
}

export function buildDesktopV3ConversationRenderItems(renderedMessages: RenderedSessionMessages): DesktopV3RenderItem[] {
  const assistantMessages = canonicalContentSet(renderedMessages.committed, 'assistant')
  const reasoningMessages = canonicalContentSet(renderedMessages.committed, 'reasoning')
  const items: DesktopV3RenderItem[] = [
    ...renderedMessages.committed.map((message) => ({ type: 'message' as const, message, timelineSeq: message.global_seq })),
    ...renderedMessages.pendingUser.map((message) => ({ type: 'pending-user' as const, message, timelineSeq: message.createdAt })),
  ]
  for (const run of renderedMessages.liveRuns) {
    items.push(...buildDesktopV3LiveRunRenderItems(run, { assistantMessages, reasoningMessages }))
  }
  return orderDesktopV3LiveRenderItems(items)
}

export function resolveDesktopV3StopRunRequest(input: { route: DesktopChatRoute | null | undefined; runId: string | null | undefined }): { runId: string; targetSwarmId: string } {
  const runId = input.runId?.trim() ?? ''
  if (!runId) {
    throw new Error('Desktop V3 stop requires run_id')
  }
  const target = getDesktopSessionStopTarget(input.route)
  if (target.sessionApi !== 'v3') {
    throw new Error(target.unsupportedReason)
  }
  return { runId, targetSwarmId: target.targetSwarmId }
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
  const renderItems = useMemo(() => buildDesktopV3ConversationRenderItems(renderedMessages), [renderedMessages])
  const hasRunningReasoning = renderedMessages.liveRuns.some((run) => {
    if (run.reasoning?.state === 'running') return true
    return Object.values(run.reasoningByKey ?? {}).some((reasoning) => reasoning.state === 'running')
  })
  const [timerNow, setTimerNow] = useState(() => Date.now())

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
  }, [normalizedSessionId, renderItems.length])

  useEffect(() => {
    if (!hasRunningReasoning) return
    const timer = window.setInterval(() => setTimerNow(Date.now()), 250)
    return () => window.clearInterval(timer)
  }, [hasRunningReasoning])

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
      const stopRequest = resolveDesktopV3StopRunRequest({ route, runId: currentRun.runId })
      await stopSessionV3Run(normalizedSessionId, stopRequest)
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
          {renderItems.map((item, index) => (
            <DesktopV3RenderItemView
              key={item.type === 'message' ? item.message.id : item.type === 'pending-user' ? item.message.clientRequestId : item.id}
              item={item}
              thinkingTagsEnabled={thinkingTagsEnabled}
              timerNow={timerNow}
              index={index}
            />
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

function DesktopV3RenderItemView({ item, thinkingTagsEnabled, timerNow }: { item: DesktopV3RenderItem; thinkingTagsEnabled: boolean; timerNow: number; index: number }) {
  switch (item.type) {
    case 'message':
      return <DesktopV3CommittedMessage message={item.message} thinkingTagsEnabled={thinkingTagsEnabled} timerNow={timerNow} />
    case 'pending-user':
      return <DesktopV3PendingUserMessage message={item.message} />
    case 'live-assistant':
      return <DesktopV3AssistantMessage content={item.content} role="assistant" />
    case 'live-reasoning':
      return <DesktopV3ReasoningMessage item={item} thinkingTagsEnabled={thinkingTagsEnabled} timerNow={timerNow} />
    case 'live-tool':
      return <DesktopV3LiveToolCall tool={item.tool} />
    case 'live-working':
      return <DesktopV3WorkingMessage />
    default:
      return null
  }
}

function DesktopV3CommittedMessage({ message, thinkingTagsEnabled, timerNow }: { message: MessageSnapshot; thinkingTagsEnabled: boolean; timerNow: number }) {
  const role = message.role || 'message'
  const toolMessage = parseStructuredToolMessage(message.content)
  if (toolMessage || role === 'tool') {
    return <DesktopV3ToolMessage content={message.content} toolMessage={toolMessage} thinkingTagsEnabled={thinkingTagsEnabled} />
  }
  if (role === 'user') {
    return <DesktopV3UserMessage content={message.content} />
  }
  if (role === 'reasoning') {
    return (
      <DesktopV3ReasoningMessage
        item={{ type: 'live-reasoning', id: message.id, text: message.content, summary: message.content, state: 'completed', startedAt: null, completedAt: null, timelineSeq: message.global_seq }}
        thinkingTagsEnabled={thinkingTagsEnabled}
        timerNow={timerNow}
      />
    )
  }
  if (role === 'assistant') {
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

function DesktopV3ToolMessage({ content, toolMessage, thinkingTagsEnabled = true }: { content: string; toolMessage: StructuredToolMessage | null; thinkingTagsEnabled?: boolean }) {
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[82%]">
        <ChatMarkdown content={content} toolMessage={toolMessage ?? undefined} thinkingTagsEnabled={thinkingTagsEnabled} />
      </div>
    </div>
  )
}

function DesktopV3ReasoningMessage({ item, thinkingTagsEnabled, timerNow }: { item: Extract<DesktopV3RenderItem, { type: 'live-reasoning' }>; thinkingTagsEnabled: boolean; timerNow: number }) {
  const body = reasoningBody(item.text, item.summary, thinkingTagsEnabled)
  const label = reasoningHeadline(item.state, item.startedAt, item.completedAt ?? null, timerNow)
  const StateIcon = item.state === 'running' ? LoaderCircle : item.state === 'error' ? XCircle : CheckCircle2
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[82%] text-sm leading-6 text-[var(--app-text)] opacity-80">
        <div className="mb-1 inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          <StateIcon size={12} className={item.state === 'running' ? 'animate-spin text-[var(--app-primary)]' : item.state === 'error' ? 'text-[var(--app-danger)]' : 'text-[var(--app-text-subtle)]'} />
          {label}
        </div>
        {body ? <ChatMarkdown content={body} /> : null}
      </div>
    </div>
  )
}

function DesktopV3WorkingMessage() {
  return (
    <div className="flex justify-start text-xs text-[var(--app-text-subtle)]">
      <span className="inline-flex items-center gap-2">
        <LoaderCircle size={13} className="animate-spin" /> assistant is working
      </span>
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
  if (parsed && tool.timelineSeq) parsed.timelineSeq = tool.timelineSeq
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
