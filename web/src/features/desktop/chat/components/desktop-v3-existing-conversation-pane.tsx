import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowDown, CheckCircle2, Loader2, LoaderCircle, XCircle } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import { buildStructuredToolMessage, parseStructuredToolMessage } from '../services/tool-message'
import type { RenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'
import type { LiveRunOverlay, MessageSnapshot, PendingUserMessage } from '../../state/desktop-v3-cache-types'
import { dispatchDesktopV3Cache, useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import type { DesktopPermissionRecord, DesktopSessionRecord } from '../../types/realtime'
import type { StructuredToolMessage, ToolMessageState, AgentProfileRecord, AgentStateRecord, ModelOptionRecord, SessionPreferenceRecord } from '../types/chat'
import { getDesktopSessionStopTarget, resolveDesktopChatRouteFromSession, type DesktopChatRoute } from '../services/chat-routing'
import { agentStateQueryOptions, draftModelQueryKey, modelOptionsQueryOptions, uiSettingsQueryKey, uiSettingsQueryOptions } from '../../../queries/query-options'
import { normalizeSessionMode, normalizeThinkingTagsEnabled, type DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import { saveThinkingTagsSetting } from '../../settings/swarm/mutations/save-thinking-tags-setting'
import { supportsCodexFastMode, formatContextWindow, effectiveContextWindow } from '../services/model-options'
import { DesktopV3AgenticComposer } from './desktop-v3-agentic-composer'
import { DesktopV3ChatHeader } from './desktop-v3-chat-header'
import { buildDesktopV3RunStatusModel, type DesktopV3RunStatusModel } from './desktop-v3-run-status'
import {
  sessionV3AgentSettingsMutationResponse,
  sessionV3ModeSettingsMutationResponse,
  sessionV3PreferenceSettingsMutationResponse,
  updateSessionV3Agent,
  updateSessionV3Mode,
  updateSessionV3Preference,
  stopSessionV3Run,
} from '../../session-v3/api'
import {
  clearDesktopV3ExistingMessageOperation,
  continueDesktopV3Conversation,
  createDesktopV3ExistingMessageOperation,
  loadDesktopV3ExistingMessageOperation,
  persistDesktopV3ExistingMessageOperation,
  type DesktopV3ExistingMessageOperation,
} from '../../session-v3/existing-session-flow'
import { compactDesktopV3Session } from '../../session-v3/compact-session-flow'
import { resolveSessionPermission, sendSessionMessage, updateDraftModelPreference } from '../queries/chat-queries'
import { DesktopPermissionModal } from '../../permissions/components/desktop-permission-modal'
import { permissionRequiresApproval } from '../../permissions/services/permission-payload'

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

type NormalizedUsageSummary = {
  contextWindow: number
  remainingTokens: number
  totalTokens: number
  updatedAt: number
}

function normalizeUsageSummary(value: unknown): NormalizedUsageSummary | null {
  const record = value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
  if (!record) return null
  const contextWindow = finiteNumber(record.context_window ?? record.contextWindow)
  const totalTokens = finiteNumber(record.total_tokens ?? record.totalTokens)
  const remainingTokens = finiteNumber(record.remaining_tokens ?? record.remainingTokens)
  const updatedAt = finiteNumber(record.updated_at ?? record.updatedAt)
  if (contextWindow <= 0 && totalTokens <= 0 && remainingTokens <= 0 && updatedAt <= 0) return null
  return { contextWindow, remainingTokens, totalTokens, updatedAt }
}

function finiteNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, value) : 0
}

function formatDesktopV3ContextLabel(contextWindow: number, remainingTokens?: number): string {
  if (contextWindow <= 0) return 'ctx'
  if (typeof remainingTokens === 'number') {
    return `${formatContextWindow(remainingTokens)} / ${formatContextWindow(contextWindow)} ctx`
  }
  return `${formatContextWindow(contextWindow)} ctx`
}

function formatDesktopV3ContextTooltip(contextWindow: number, usage: NormalizedUsageSummary | null): string {
  if (usage && contextWindow > 0) {
    return `Remaining context ${formatContextWindow(usage.remainingTokens)} of ${formatContextWindow(contextWindow)}. Total tokens ${usage.totalTokens.toLocaleString()}.`
  }
  if (contextWindow > 0) return `Context window ${formatContextWindow(contextWindow)}`
  return 'Context window unavailable'
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

type AgentModelLockState = {
  profile: AgentProfileRecord | null
  locked: boolean
  agentName: string
  provider: string
  model: string
  thinking: string
  disabledReason: string
}

function findAgentProfile(agents: AgentProfileRecord[], agentName: string): AgentProfileRecord | null {
  const normalizedAgentName = agentName.trim()
  if (!normalizedAgentName) return null
  return agents.find((agent) => agent.name === normalizedAgentName)
    ?? agents.find((agent) => agent.name.trim().toLowerCase() === normalizedAgentName.toLowerCase())
    ?? null
}

export function resolveDesktopV3AgentModelLock(agents: AgentProfileRecord[], selectedAgentName: string): AgentModelLockState {
  const profile = findAgentProfile(agents, selectedAgentName)
  const provider = profile?.provider.trim() ?? ''
  const model = profile?.model.trim() ?? ''
  const agentName = profile?.name.trim() || selectedAgentName.trim()
  const locked = Boolean(provider && model)
  return {
    profile,
    locked,
    agentName,
    provider,
    model,
    thinking: profile?.thinking.trim() ?? '',
    disabledReason: locked && agentName
      ? `To change models for ${agentName}, set the model to Default in Settings → Agents.`
      : '',
  }
}

function preferenceFromAgentModelLock(lock: AgentModelLockState, current: SessionPreferenceRecord, modelOptions: ModelOptionRecord[]): SessionPreferenceRecord {
  if (!lock.locked) return current
  const matchingOption = modelOptions.find((option) => option.provider === lock.provider && option.model === lock.model) ?? null
  return {
    ...current,
    provider: lock.provider,
    model: lock.model,
    thinking: lock.thinking || current.thinking || matchingOption?.thinking || '',
    contextMode: matchingOption?.contextMode ?? '',
  }
}

function fastToggleFromPreference(preference: SessionPreferenceRecord): 'on' | 'off' {
  return preference.serviceTier.trim().toLowerCase() === 'fast' ? 'on' : 'off'
}

function thinkingForDraftDefault(thinking: string): string {
  const normalized = thinking.trim()
  return normalized || 'off'
}

function preferencesEqual(left: SessionPreferenceRecord, right: SessionPreferenceRecord): boolean {
  return left.provider === right.provider
    && left.model === right.model
    && left.thinking === right.thinking
    && left.serviceTier === right.serviceTier
    && left.contextMode === right.contextMode
}

function firstNonEmpty(...values: string[]): string {
  for (const value of values) {
    const normalized = value.trim()
    if (normalized) return normalized
  }
  return ''
}

function comparePendingPermissions(left: DesktopPermissionRecord, right: DesktopPermissionRecord): number {
  return (left.permissionRequestedAt || left.createdAt || left.updatedAt || 0) - (right.permissionRequestedAt || right.createdAt || right.updatedAt || 0)
    || left.id.localeCompare(right.id)
}

type DesktopV3InputSettingsSnapshot = {
  sessionId: string
  mode: DesktopSessionMode
  agent: string
  preference: SessionPreferenceRecord
}

function buildDesktopV3ExistingSettingsSnapshot(input: {
  sessionId: string
  metadata?: Record<string, unknown>
  session?: DesktopSessionRecord | null
  cacheSession?: { mode?: string; metadata?: Record<string, unknown> } | null
  cachedPreference: SessionPreferenceRecord
  fallbackAgent: string
}): DesktopV3InputSettingsSnapshot {
  return {
    sessionId: input.sessionId,
    mode: normalizeSessionMode(input.session?.mode || input.cacheSession?.mode),
    agent: firstNonEmpty(
      metadataString(input.metadata, 'agent_name'),
      metadataString(input.metadata, 'resolved_agent_name'),
      input.fallbackAgent,
    ),
    preference: input.cachedPreference,
  }
}

function settingsSnapshotEqual(left: DesktopV3InputSettingsSnapshot, right: DesktopV3InputSettingsSnapshot): boolean {
  return left.sessionId === right.sessionId
    && left.mode === right.mode
    && left.agent === right.agent
    && preferencesEqual(left.preference, right.preference)
}

function formatSettingsChangeSummary(input: {
  previous: DesktopV3InputSettingsSnapshot
  next: DesktopV3InputSettingsSnapshot
  previousModelLabel: string
  nextModelLabel: string
}): string {
  const changes: string[] = []
  if (input.previous.mode !== input.next.mode) changes.push(`mode ${input.previous.mode || 'auto'} → ${input.next.mode || 'auto'}`)
  if (input.previous.agent !== input.next.agent) changes.push(`agent ${input.previous.agent || 'default'} → ${input.next.agent || 'default'}`)
  if (!preferencesEqual(input.previous.preference, input.next.preference)) {
    const previousThinking = input.previous.preference.thinking || 'off'
    const nextThinking = input.next.preference.thinking || 'off'
    const previousFast = fastToggleFromPreference(input.previous.preference)
    const nextFast = fastToggleFromPreference(input.next.preference)
    changes.push(`model ${input.previousModelLabel || 'default'} → ${input.nextModelLabel || 'default'}`)
    if (previousThinking !== nextThinking) changes.push(`thinking ${previousThinking} → ${nextThinking}`)
    if (previousFast !== nextFast) changes.push(`fast ${previousFast} → ${nextFast}`)
  }
  return changes.join('; ')
}

type DesktopV3RenderItem =
  | { type: 'message'; message: MessageSnapshot; timelineSeq?: number }
  | { type: 'pending-user'; message: PendingUserMessage; timelineSeq?: number }
  | { type: 'live-assistant'; id: string; content: string; timelineSeq?: number }
  | { type: 'live-reasoning'; id: string; text: string; summary: string; state: NonNullable<LiveRunOverlay['reasoning']>['state']; startedAt: number | null; completedAt?: number | null; timelineSeq?: number }
  | { type: 'live-tool'; id: string; tool: LiveRunOverlay['toolCallsByCallId'][string]; timelineSeq?: number }
  | { type: 'live-working'; id: string; timelineSeq?: number }

type DesktopV3ScrollBehavior = 'auto' | 'smooth'

const DESKTOP_V3_BOTTOM_BUFFER_PX = 140

function desktopV3BottomDistance(element: HTMLElement): number {
  return Math.max(0, element.scrollHeight - element.scrollTop - element.clientHeight)
}

function useDesktopV3StickyBottomScroll(options: { resetKey: string; itemCount: number; bottomBufferPx?: number }) {
  const bottomBufferPx = options.bottomBufferPx ?? DESKTOP_V3_BOTTOM_BUFFER_PX
  const scrollContainerRef = useRef<HTMLDivElement | null>(null)
  const contentRef = useRef<HTMLDivElement | null>(null)
  const autoFollowRef = useRef(true)
  const smoothFollowUntilRef = useRef(0)
  const frameRef = useRef<number | null>(null)
  const lastScrollHeightRef = useRef(0)
  const [isAtBottom, setIsAtBottom] = useState(true)
  const [hasUnseenLatest, setHasUnseenLatest] = useState(false)

  const cancelScheduledScroll = useCallback(() => {
    if (frameRef.current === null) return
    window.cancelAnimationFrame(frameRef.current)
    frameRef.current = null
  }, [])

  const setPinnedStateFromElement = useCallback((element: HTMLElement) => {
    const pinned = desktopV3BottomDistance(element) <= bottomBufferPx
    const keepFollowingSmoothJump = !pinned && smoothFollowUntilRef.current > Date.now()
    autoFollowRef.current = pinned || keepFollowingSmoothJump
    setIsAtBottom(pinned || keepFollowingSmoothJump)
    if (pinned) setHasUnseenLatest(false)
    return pinned
  }, [bottomBufferPx])

  const scrollToBottom = useCallback((behavior: DesktopV3ScrollBehavior = 'auto') => {
    const element = scrollContainerRef.current
    if (!element) return
    autoFollowRef.current = true
    setIsAtBottom(true)
    setHasUnseenLatest(false)
    if (behavior === 'smooth') {
      smoothFollowUntilRef.current = Date.now() + 1200
      element.scrollTo({ top: element.scrollHeight, behavior: 'smooth' })
      return
    }
    smoothFollowUntilRef.current = 0
    element.scrollTop = element.scrollHeight
  }, [])

  const scheduleAutoFollow = useCallback((scheduleOptions: { forceUnseen?: boolean } = {}) => {
    const element = scrollContainerRef.current
    const nextScrollHeight = element?.scrollHeight ?? 0
    const contentAdvanced = scheduleOptions.forceUnseen || nextScrollHeight > lastScrollHeightRef.current + 1
    lastScrollHeightRef.current = nextScrollHeight
    if (!autoFollowRef.current) {
      if (contentAdvanced) setHasUnseenLatest(true)
      return
    }
    if (frameRef.current !== null) return
    frameRef.current = window.requestAnimationFrame(() => {
      frameRef.current = null
      if (!autoFollowRef.current) return
      scrollToBottom('auto')
    })
  }, [scrollToBottom])

  useEffect(() => {
    const element = scrollContainerRef.current
    if (!element) return
    const handleScroll = () => {
      setPinnedStateFromElement(element)
    }
    handleScroll()
    element.addEventListener('scroll', handleScroll, { passive: true })
    return () => element.removeEventListener('scroll', handleScroll)
  }, [setPinnedStateFromElement])

  useEffect(() => {
    autoFollowRef.current = true
    lastScrollHeightRef.current = scrollContainerRef.current?.scrollHeight ?? 0
    setIsAtBottom(true)
    setHasUnseenLatest(false)
    scrollToBottom('auto')
  }, [options.resetKey, scrollToBottom])

  useEffect(() => {
    scheduleAutoFollow({ forceUnseen: true })
  }, [options.itemCount, scheduleAutoFollow])

  useEffect(() => {
    const scrollElement = scrollContainerRef.current
    const contentElement = contentRef.current
    if (!scrollElement || !contentElement) return
    const handleObservedResize = () => scheduleAutoFollow()
    const handleObservedMutation = () => scheduleAutoFollow({ forceUnseen: true })
    const resizeObserver = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(handleObservedResize)
    resizeObserver?.observe(scrollElement)
    resizeObserver?.observe(contentElement)
    const mutationObserver = typeof MutationObserver === 'undefined' ? null : new MutationObserver(handleObservedMutation)
    mutationObserver?.observe(contentElement, { childList: true, subtree: true, characterData: true })
    handleObservedResize()
    return () => {
      resizeObserver?.disconnect()
      mutationObserver?.disconnect()
      cancelScheduledScroll()
    }
  }, [cancelScheduledScroll, scheduleAutoFollow])

  return {
    scrollContainerRef,
    contentRef,
    isAtBottom,
    hasUnseenLatest,
    scrollToBottom,
  }
}

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

export function isDesktopV3ManualCompactionAckMessage(message: MessageSnapshot): boolean {
  if ((message.role || '').trim().toLowerCase() !== 'assistant') return false
  const metadataSource = typeof message.metadata?.source === 'string'
    ? message.metadata.source.trim().toLowerCase()
    : ''
  if (metadataSource === 'manual_context_compaction_ack') return true
  return message.content.trim().startsWith('Manual context compact complete (Compact #')
}

export function buildDesktopV3LiveRunRenderItems(run: LiveRunOverlay, options: { assistantMessages?: Set<string>; reasoningMessages?: Set<string> } = {}): DesktopV3RenderItem[] {
  const items: DesktopV3RenderItem[] = []
  for (const segment of run.assistantSegments ?? []) {
    const content = segment.content
    if (!content.trim()) continue
    if (options.assistantMessages?.has(normalizeReplayContent(content))) continue
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
  const committedMessages = renderedMessages.committed.filter((message) => !isDesktopV3ManualCompactionAckMessage(message))
  const assistantMessages = canonicalContentSet(committedMessages, 'assistant')
  const reasoningMessages = canonicalContentSet(committedMessages, 'reasoning')
  const items: DesktopV3RenderItem[] = [
    ...committedMessages.map((message) => ({ type: 'message' as const, message, timelineSeq: message.global_seq })),
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
  onOpenChats?: () => void
  onNewSession?: () => void
  onCompactingChange?: (sessionId: string, startedAt: number | null) => void
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
  onOpenChats,
  onNewSession,
  onCompactingChange,
}: DesktopV3ExistingConversationPaneProps) {
  const normalizedSessionId = sessionId.trim()
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const queryClient = useQueryClient()
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
  const rawCachedPreference = useDesktopV3CacheSelector((state) => state.preferencesBySession[normalizedSessionId])
  const rawCachedUsage = useDesktopV3CacheSelector((state) => state.usageBySession[normalizedSessionId])
  const cachedPreference = useMemo(() => normalizePreference(rawCachedPreference), [rawCachedPreference])
  const cacheSession = useDesktopV3CacheSelector((state) => {
    const record = state.sessionsById[normalizedSessionId]
    return record?.kind === 'full' ? record.session : null
  })
  const storedOperation = operationRef.current
  const sessionMode = session?.mode || cacheSession?.mode || 'auto'
  const pendingPermissions = useDesktopV3CacheSelector((state) => (
    [...(state.permissionsBySession[normalizedSessionId] ?? [])]
      .filter((permission) => permissionRequiresApproval(permission, sessionMode))
      .sort(comparePendingPermissions)
  ))
  const selectedPermission = pendingPermissions[0] ?? null
  const currentRun = renderedMessages.liveRuns.find((run) => run.status === 'running' || run.status === 'pending_executor') ?? null
  const pendingStartAt = !renderedMessages.currentRunIntent
    ? renderedMessages.pendingUser.find((message) => message.status === 'pending')?.createdAt ?? storedOperation?.createdAt ?? null
    : null
  const canonicalRunStatusModel = buildDesktopV3RunStatusModel({
    currentRunIntent: renderedMessages.currentRunIntent,
    latestRunIntent: renderedMessages.latestRunIntent,
    liveRuns: renderedMessages.liveRuns,
    pendingStartAt,
  })
  const sessionMetadata = session?.metadata ?? cacheSession?.metadata ?? metadata
  const settingsBaseline = useMemo(() => buildDesktopV3ExistingSettingsSnapshot({
    sessionId: normalizedSessionId,
    metadata: sessionMetadata,
    session,
    cacheSession,
    cachedPreference,
    fallbackAgent: agentState.activePrimary,
  }), [agentState.activePrimary, cacheSession, cachedPreference, normalizedSessionId, session, sessionMetadata])
  const [draft, setDraft] = useState(storedOperation?.request.content ?? '')
  const [sendError, setSendError] = useState<string | null>(null)
  const [sending, setSending] = useState(false)
  const [compactStartedAt, setCompactStartedAt] = useState<number | null>(null)
  const [thinkingTagsSaving, setThinkingTagsSaving] = useState(false)
  const [restartingWithSettings, setRestartingWithSettings] = useState(false)
  const [mode, setMode] = useState<DesktopSessionMode>(settingsBaseline.mode)
  const [selectedAgent, setSelectedAgent] = useState(settingsBaseline.agent)
  const [preference, setPreference] = useState<SessionPreferenceRecord>(settingsBaseline.preference)
  const initializedSettingsSessionRef = useRef('')
  const localSettingsDirtyRef = useRef({ agent: false, mode: false, preference: false })
  const unlockedPreferenceRef = useRef<SessionPreferenceRecord>(settingsBaseline.preference)

  const hasStoredOperation = Boolean(storedOperation)
  const hasMessages = renderedMessages.committed.length > 0
    || renderedMessages.pendingUser.length > 0
    || renderedMessages.liveRuns.length > 0
  const selectedAgentModelLock = useMemo(
    () => resolveDesktopV3AgentModelLock(agentState.profiles, selectedAgent),
    [agentState.profiles, selectedAgent],
  )
  const selectedModelKey = optionKey(preference.provider, preference.model, preference.contextMode)
  const selectedModelOption = modelOptions.find((option) => option.key === selectedModelKey) ?? null
  const hasResolvedPreference = Boolean(preference.provider.trim() && preference.model.trim() && preference.thinking.trim())
  const selectedModelAvailable = Boolean(selectedModelOption && hasResolvedPreference)
  const baselineModelKey = optionKey(settingsBaseline.preference.provider, settingsBaseline.preference.model, settingsBaseline.preference.contextMode)
  const baselineModelOption = modelOptions.find((option) => option.key === baselineModelKey) ?? null
  const fastSupported = selectedModelOption ? supportsCodexFastMode(selectedModelOption.provider, selectedModelOption.model) : false
  const cachedUsage = useMemo(() => normalizeUsageSummary(rawCachedUsage), [rawCachedUsage])
  const selectedContextWindow = selectedModelOption
    ? effectiveContextWindow(selectedModelOption.provider, selectedModelOption.model, selectedModelOption.contextMode, selectedModelOption.contextWindow)
    : 0
  const effectiveContextWindowValue = cachedUsage?.contextWindow && cachedUsage.contextWindow > 0
    ? cachedUsage.contextWindow
    : selectedContextWindow
  const contextLabel = formatDesktopV3ContextLabel(effectiveContextWindowValue, cachedUsage?.remainingTokens)
  const contextTooltip = formatDesktopV3ContextTooltip(effectiveContextWindowValue, cachedUsage)
  const workspaceSettingsMatch = matchRoute({ to: '/$workspaceSlug/settings', fuzzy: false })
    ?? matchRoute({ to: '/$workspaceSlug/$sessionId', fuzzy: false })
    ?? matchRoute({ to: '/$workspaceSlug', fuzzy: false })
  const routeWorkspaceSlug = workspaceSettingsMatch && 'workspaceSlug' in workspaceSettingsMatch
    ? String(workspaceSettingsMatch.workspaceSlug ?? '').trim()
    : ''
  const route = useMemo(
    () => resolveDesktopChatRouteFromSession(session ?? null, routeOptions, routeOptions[0] ?? null),
    [routeOptions, session],
  )
  const compacting = compactStartedAt !== null
  const canSend = Boolean(normalizedSessionId && !sending && !compacting && selectedAgent.trim() && selectedModelAvailable && (hasStoredOperation || draft.trim()))
  const visibleSettingsSnapshot = useMemo<DesktopV3InputSettingsSnapshot>(() => ({
    sessionId: normalizedSessionId,
    mode,
    agent: selectedAgent.trim(),
    preference,
  }), [mode, normalizedSessionId, preference, selectedAgent])
  const visibleSettingsChanged = !settingsSnapshotEqual(settingsBaseline, visibleSettingsSnapshot)
  const settingsChangeSummary = visibleSettingsChanged
    ? formatSettingsChangeSummary({
      previous: settingsBaseline,
      next: visibleSettingsSnapshot,
      previousModelLabel: baselineModelOption?.label || settingsBaseline.preference.model,
      nextModelLabel: selectedModelOption?.label || visibleSettingsSnapshot.preference.model,
    })
    : ''
  const showRestartSettingsAction = Boolean(currentRun && visibleSettingsChanged)
  const renderItems = useMemo(() => buildDesktopV3ConversationRenderItems(renderedMessages), [renderedMessages])
  const showConversationLoading = initialHydrateStatus === 'loading' && !messagesLoaded && !hasMessages && !hasStoredOperation
  const {
    scrollContainerRef,
    contentRef,
    isAtBottom,
    hasUnseenLatest,
    scrollToBottom,
  } = useDesktopV3StickyBottomScroll({ resetKey: normalizedSessionId, itemCount: renderItems.length })
  const hasRunningReasoning = renderedMessages.liveRuns.some((run) => {
    if (run.reasoning?.state === 'running') return true
    return Object.values(run.reasoningByKey ?? {}).some((reasoning) => reasoning.state === 'running')
  })
  const runStatusModel: DesktopV3RunStatusModel | null = compactStartedAt !== null
    ? { kind: 'active', label: 'Compacting', startedAt: compactStartedAt, active: true }
    : canonicalRunStatusModel
  const statusTimerActive = Boolean(runStatusModel?.active) || hasRunningReasoning || compacting
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
    if (!statusTimerActive) return
    const timer = window.setInterval(() => setTimerNow(Date.now()), 250)
    return () => window.clearInterval(timer)
  }, [statusTimerActive])

  useEffect(() => {
    if (!normalizedSessionId) return
    const sessionChanged = initializedSettingsSessionRef.current !== normalizedSessionId
    if (sessionChanged) {
      initializedSettingsSessionRef.current = normalizedSessionId
      localSettingsDirtyRef.current = { agent: false, mode: false, preference: false }
      setMode(settingsBaseline.mode)
      setSelectedAgent(settingsBaseline.agent)
      setPreference(settingsBaseline.preference)
      unlockedPreferenceRef.current = settingsBaseline.preference
      return
    }
    if (!localSettingsDirtyRef.current.mode) {
      setMode((current) => current === settingsBaseline.mode ? current : settingsBaseline.mode)
    }
    if (!localSettingsDirtyRef.current.agent && settingsBaseline.agent) {
      setSelectedAgent((current) => current === settingsBaseline.agent ? current : settingsBaseline.agent)
    }
    if (!localSettingsDirtyRef.current.preference && (settingsBaseline.preference.provider || settingsBaseline.preference.model)) {
      unlockedPreferenceRef.current = settingsBaseline.preference
      setPreference((current) => preferencesEqual(current, settingsBaseline.preference) ? current : settingsBaseline.preference)
    }
  }, [normalizedSessionId, settingsBaseline])

  useEffect(() => {
    if (!selectedAgentModelLock.locked) return
    setPreference((current) => preferenceFromAgentModelLock(selectedAgentModelLock, current, modelOptions))
  }, [modelOptions, selectedAgentModelLock])

  function handleModeChange(nextMode: DesktopSessionMode) {
    localSettingsDirtyRef.current.mode = true
    setMode(nextMode)
  }

  function handleAgentSelect(agentName: string) {
    localSettingsDirtyRef.current.agent = true
    localSettingsDirtyRef.current.preference = true
    setSelectedAgent(agentName)
    const lock = resolveDesktopV3AgentModelLock(agentState.profiles, agentName)
    if (lock.locked) {
      setPreference((current) => {
        unlockedPreferenceRef.current = current
        return preferenceFromAgentModelLock(lock, current, modelOptions)
      })
      return
    }
    setPreference(unlockedPreferenceRef.current)
  }

  function handleOpenAgentSettings() {
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/settings', params: { workspaceSlug: routeWorkspaceSlug }, search: { tab: 'agents' } })
      return
    }
    void navigate({ to: '/settings', search: { tab: 'agents' } })
  }

  function handleModelSelect(key: string) {
    if (selectedAgentModelLock.locked) return
    localSettingsDirtyRef.current.preference = true
    const option = modelOptions.find((candidate) => candidate.key === key) ?? null
    setPreference((current) => {
      const next = preferenceFromOption(option, current)
      unlockedPreferenceRef.current = next
      return next
    })
  }

  function handleThinkingChange(value: string) {
    if (selectedAgentModelLock.locked) return
    localSettingsDirtyRef.current.preference = true
    setPreference((current) => {
      const next = { ...current, thinking: value.trim() === 'off' ? '' : value.trim() }
      unlockedPreferenceRef.current = next
      return next
    })
  }

  function handleFastChange(value: 'on' | 'off') {
    if (selectedAgentModelLock.locked) return
    localSettingsDirtyRef.current.preference = true
    setPreference((current) => {
      const next = {
        ...current,
        serviceTier: value === 'on' && fastSupported ? 'fast' : '',
      }
      unlockedPreferenceRef.current = next
      return next
    })
  }

  async function persistDraftModelDefault(nextPreference: SessionPreferenceRecord) {
    if (selectedAgentModelLock.locked) return
    if (!nextPreference.provider.trim() || !nextPreference.model.trim()) return
    const updated = await updateDraftModelPreference({
      ...nextPreference,
      thinking: thinkingForDraftDefault(nextPreference.thinking),
    })
    queryClient.setQueryData(draftModelQueryKey(), updated)
  }

  async function persistVisibleSettings() {
    if (!normalizedSessionId) return
    if (settingsBaseline.mode !== mode) {
      const modeResponse = await updateSessionV3Mode(normalizedSessionId, mode)
      dispatchDesktopV3Cache({
        type: 'mutation.sessionSettingsResult',
        raw: sessionV3ModeSettingsMutationResponse(modeResponse, normalizedSessionId, mode),
      })
    }
    const currentAgent = settingsBaseline.agent.trim()
    const nextAgent = selectedAgent.trim()
    if (nextAgent && nextAgent !== currentAgent) {
      const agentResponse = await updateSessionV3Agent(normalizedSessionId, nextAgent)
      dispatchDesktopV3Cache({
        type: 'mutation.sessionSettingsResult',
        raw: sessionV3AgentSettingsMutationResponse(agentResponse, normalizedSessionId),
      })
    }
    if (!preferencesEqual(preference, settingsBaseline.preference)) {
      const preferenceResponse = await updateSessionV3Preference(normalizedSessionId, preference)
      dispatchDesktopV3Cache({
        type: 'mutation.sessionSettingsResult',
        raw: sessionV3PreferenceSettingsMutationResponse(preferenceResponse, normalizedSessionId),
      })
      await persistDraftModelDefault(preference)
    }
  }

  async function handleSubmit() {
    if (!normalizedSessionId || sending || compacting) return

    setSending(true)
    setSendError(null)
    scrollToBottom('smooth')
    try {
      if (!selectedModelAvailable) {
        throw new Error('Select a model and thinking level before sending')
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

  async function handleCompact(note: string) {
    if (!normalizedSessionId || compacting || sending || currentRun) return

    const startedAt = Date.now()
    setCompactStartedAt(startedAt)
    onCompactingChange?.(normalizedSessionId, startedAt)
    setSendError(null)
    scrollToBottom('smooth')
    try {
      await persistVisibleSettings()
      await compactDesktopV3Session({
        sessionId: normalizedSessionId,
        note,
        agentName: selectedAgent || '',
      })
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : String(error))
      }
    } finally {
      if (mountedRef.current) setCompactStartedAt(null)
      onCompactingChange?.(normalizedSessionId, null)
    }
  }

  async function handleThinkingTagsToggle(enabled: boolean) {
    if (thinkingTagsSaving) return
    setThinkingTagsSaving(true)
    setSendError(null)
    try {
      const updated = await saveThinkingTagsSetting(enabled)
      queryClient.setQueryData(uiSettingsQueryKey(), updated)
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : 'Failed to update thinking tags setting')
      }
    } finally {
      if (mountedRef.current) setThinkingTagsSaving(false)
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

  async function handleResolvePermission(
    action: 'approve' | 'deny' | 'approve_always' | 'always_allow' | 'always_deny',
    reason: string,
    approvedArguments?: Record<string, unknown>,
  ) {
    const permission = selectedPermission
    if (!permission) return
    const resolved = await resolveSessionPermission(permission.sessionId, permission.id, action, reason, approvedArguments, { sessionApi: 'v3' })
    dispatchDesktopV3Cache({
      type: 'permission.resolveResult',
      sessionId: permission.sessionId,
      permissionId: permission.id,
      permission: resolved,
    })
  }

  async function handleRestartWithSettings() {
    if (!normalizedSessionId || !currentRun?.runId || restartingWithSettings || !settingsChangeSummary) return
    setRestartingWithSettings(true)
    setSendError(null)
    try {
      const stopRequest = resolveDesktopV3StopRunRequest({ route, runId: currentRun.runId })
      await stopSessionV3Run(normalizedSessionId, stopRequest)
      await persistVisibleSettings()
      await sendSessionMessage(
        normalizedSessionId,
        'system',
        `User has changed their settings (${settingsChangeSummary}); please continue with the new settings.`,
        route,
        { sessionApi: 'v3' },
      )
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : String(error))
      }
    } finally {
      if (mountedRef.current) setRestartingWithSettings(false)
    }
  }

  if (!normalizedSessionId) {
    return <DesktopV3ChatStateCard title="Select a session" description="Choose a session from the sidebar to view its conversation." />
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]" data-testid="desktop-v3-existing-conversation-pane">
      <DesktopV3ChatHeader
        title={session?.title || cacheSession?.title || 'Conversation'}
        workspaceName={session?.workspaceName || cacheSession?.workspace_name || 'Workspace'}
        mode={mode}
        runStatus={runStatusModel}
        runStatusNow={timerNow}
        onOpenChats={onOpenChats}
        onNewSession={onNewSession}
      />
      <div className="relative min-h-0 flex-1">
        <div ref={scrollContainerRef} className="h-full min-h-0 overflow-y-auto py-6 [scrollbar-gutter:stable]">
          <div ref={contentRef} className="mx-auto flex min-h-full w-full max-w-[70rem] flex-col gap-5 px-8 sm:px-12">
          {showConversationLoading ? (
            <DesktopV3ConversationLoadingSpinner />
          ) : null}
          {initialHydrateStatus === 'error' && !messagesLoaded && !hasMessages ? (
            <DesktopV3ChatInlineState title="Conversation unavailable" description="Initial message hydrate failed. You can still send from this session while cached state recovers." tone="error" />
          ) : null}
          {compacting ? (
            <DesktopV3CompactPendingState />
          ) : null}
          {messagesLoaded && !hasMessages ? (
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
            <div aria-hidden="true" />
          </div>
        </div>
        {!isAtBottom && hasUnseenLatest ? (
          <button
            type="button"
            aria-label="Jump to latest message"
            title="Jump to latest message"
            onClick={() => scrollToBottom('smooth')}
            className="absolute bottom-5 right-5 z-10 inline-flex h-10 w-10 items-center justify-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface-elevated)] text-[var(--app-text)] shadow-lg transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
          >
            <ArrowDown size={18} aria-hidden="true" />
          </button>
        ) : null}
      </div>

      <DesktopPermissionModal
        key={`permission:${normalizedSessionId}`}
        open={Boolean(selectedPermission)}
        permission={selectedPermission}
        pendingCount={pendingPermissions.length}
        sessionMode={sessionMode}
        onOpenChange={() => undefined}
        onResolve={handleResolvePermission}
      />

      <DesktopV3AgenticComposer
        draft={draft}
        onDraftChange={setDraft}
        placeholder="Message Swarm…"
        inputLabel="Continue Desktop V3 conversation"
        disabled={sending || compacting || restartingWithSettings}
        busy={sending || compacting || restartingWithSettings}
        canSubmit={canSend}
        canStop={Boolean(currentRun)}
        error={sendError}
        onSubmit={handleSubmit}
        onStop={handleStop}
        onCompact={handleCompact}
        mode={mode}
        onModeChange={handleModeChange}
        currentAgent={selectedAgent || 'Agent'}
        selectedPrimaryAgent={selectedAgent || ''}
        agents={agentState.profiles}
        onAgentSelect={handleAgentSelect}
        modelOptions={modelOptions}
        selectedModelKey={selectedModelKey}
        selectedModelAvailable={selectedModelAvailable}
        onModelSelect={handleModelSelect}
        modelPickerDisabled={selectedAgentModelLock.locked}
        modelPickerDisabledReason={selectedAgentModelLock.disabledReason}
        modelLockNotice={selectedAgentModelLock.locked ? selectedAgentModelLock.disabledReason : ''}
        onOpenAgentSettings={handleOpenAgentSettings}
        thinking={preference.thinking}
        onThinkingChange={handleThinkingChange}
        thinkingTagsEnabled={thinkingTagsEnabled}
        onThinkingTagsToggle={(enabled) => { void handleThinkingTagsToggle(enabled) }}
        thinkingTagsBusy={thinkingTagsSaving}
        fast={fastToggleFromPreference(preference)}
        onFastChange={handleFastChange}
        route={route}
        routeOptions={routeOptions}
        routeTitle="Changing the route starts a new session in this workspace."
        contextLabel={contextLabel}
        contextTooltip={contextTooltip}
        compactDisabled={compacting || sending || Boolean(currentRun)}
        settingsActionLabel={showRestartSettingsAction ? 'Restart loop with new settings?' : ''}
        settingsActionBusy={restartingWithSettings}
        onSettingsAction={showRestartSettingsAction ? handleRestartWithSettings : undefined}
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
      return null
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
      <div className="max-w-[calc(100%-2rem)] rounded-2xl border border-[var(--app-border)] px-4 py-3 text-sm text-[var(--app-text)]">
        <div className="mb-1 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">{role}</div>
        <ChatMarkdown content={message.content} />
      </div>
    </div>
  )
}

function DesktopV3UserMessage({ content, pendingLabel }: { content: string; pendingLabel?: string }) {
  return (
    <div className="flex justify-end pr-0">
      <div className="max-w-[70%] rounded-3xl bg-[var(--app-primary)] px-4 py-3 text-sm leading-6 text-[var(--app-primary-text)] shadow-sm">
        <div className="whitespace-pre-wrap break-words">{content}</div>
        {pendingLabel ? <div className="mt-1 text-right text-[10px] uppercase tracking-[0.12em] opacity-70">{pendingLabel}</div> : null}
      </div>
    </div>
  )
}

function DesktopV3PendingUserMessage({ message }: { message: PendingUserMessage }) {
  return <DesktopV3UserMessage content={message.content} pendingLabel={message.status === 'failed' ? message.error || 'failed' : undefined} />
}

function DesktopV3CompactPendingState() {
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[calc(100%-2rem)] text-sm leading-6 text-[var(--app-text)] opacity-80">
        <div className="mb-1 inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          <LoaderCircle size={12} className="animate-spin text-[var(--app-primary)]" />
          Compacting context
        </div>
        <div className="text-[var(--app-text-muted)]">Waiting for the backend compact cursor…</div>
      </div>
    </div>
  )
}

function DesktopV3AssistantMessage({ content, role }: { content: string; role: string }) {
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[calc(100%-2rem)] text-sm leading-6 text-[var(--app-text)]">
        {role === 'reasoning' ? <div className="mb-1 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">reasoning</div> : null}
        <ChatMarkdown content={content} />
      </div>
    </div>
  )
}

function DesktopV3ToolMessage({ content, toolMessage, thinkingTagsEnabled = true }: { content: string; toolMessage: StructuredToolMessage | null; thinkingTagsEnabled?: boolean }) {
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[calc(100%-2rem)]">
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
      <div className="min-w-0 max-w-[calc(100%-2rem)] text-sm leading-6 text-[var(--app-text)] opacity-80">
        <div className="mb-1 inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          <StateIcon size={12} className={item.state === 'running' ? 'animate-spin text-[var(--app-primary)]' : item.state === 'error' ? 'text-[var(--app-danger)]' : 'text-[var(--app-text-subtle)]'} />
          {label}
        </div>
        {body ? <ChatMarkdown content={body} /> : null}
      </div>
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

function DesktopV3ConversationLoadingSpinner() {
  return (
    <div className="flex min-h-full items-center justify-center py-16" role="status" aria-label="Loading conversation">
      <Loader2 size={28} strokeWidth={2.2} className="animate-spin text-[var(--app-primary)]" />
    </div>
  )
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
