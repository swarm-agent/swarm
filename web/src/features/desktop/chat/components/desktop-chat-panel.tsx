import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type DragEvent as ReactDragEvent, type ReactNode } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Clock3, Home, ListChecks, LoaderCircle, MessageSquareText, Mic, Minimize2, Plus, Save, Send, Settings2, ShieldAlert, Sparkles, Square, X } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Textarea } from '../../../../components/ui/textarea'
import { getDesktopV3RealtimeDiagnostics, useDesktopUiStore } from '../../state/desktop-ui-store'
import {
  agentStateQueryOptions,
  draftModelQueryOptions,
  modelOptionsQueryOptions,
  sessionPermissionsQueryOptions,
  sessionPreferenceQueryOptions,
  sessionUsageQueryOptions,
  uiSettingsQueryKey,
  uiSettingsQueryOptions,
} from '../../../queries/query-options'
import {
  createSession,
  resolveSessionPermission,
  startSessionRun,
  updateDraftModelPreference,
} from '../queries/chat-queries'
import { fetchAndApplyDesktopV3PlanSnapshot, fetchAndApplyDesktopV3SessionMessagesTail, saveDesktopV3SessionPlan, updateDesktopV3SessionAgent, updateDesktopV3SessionMetadata, updateDesktopV3SessionMode, updateDesktopV3SessionPreference } from '../../state/desktop-v3-session-api'
import type { AgentModelPolicyRecord, AgentProfileRecord, AgentStateRecord, ChatMessageRecord, ModelOptionRecord, ResolvedSessionPreference, DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord } from '../types/chat'
import type { DesktopLiveAssistantSegment, DesktopLiveReasoningRecord, DesktopLiveToolRecord, DesktopRunIntentRecord, DesktopSessionRecord } from '../../types/realtime'
import { Card } from '../../../../components/ui/card'
import { ChatMarkdown } from './chat-markdown'
import { buildStructuredToolMessage } from '../services/tool-message'
import { ModelPicker } from './model-picker'
import { ModePicker } from './mode-picker'
import { ThinkingPicker } from './thinking-picker'
import { RoutePicker } from './route-picker'
import { supportsCodexFastMode, formatContextWindow, effectiveContextWindow } from '../services/model-options'
import { AgentPicker } from './agent-picker'
import { DesktopPermissionModal } from '../../permissions/components/desktop-permission-modal'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { saveThinkingTagsSetting } from '../../settings/swarm/mutations/save-thinking-tags-setting'
import { saveDefaultWorkspaceRoute } from '../../settings/swarm/mutations/save-default-workspace-route'
import { defaultWorkspaceRouteId, normalizeDefaultNewSessionMode, normalizeThinkingTagsEnabled } from '../../settings/swarm/types/swarm-settings'
import { permissionRequiresApproval } from '../../permissions/services/permission-payload'
import { DesktopPlanModal } from './desktop-plan-modal'
import { DesktopSlashCommandPanel } from './desktop-slash-command-panel'
import { DesktopMentionPanel } from './desktop-mention-panel'
import { COMPACT_THRESHOLD_METADATA_KEY, parseCompactCommandInput } from '../services/compact-command'
import {
  chatMentionCandidates,
  mentionHasArgs,
  mentionPaletteActive,
  mentionPaletteQuery,
  normalizeMentionSubagents,
  parseTargetedSubagentPrompt,
} from '../services/subagent-mentions'
import {
  buildDesktopChatRouteOptions,
  desktopChatRoutesEqual,
  isManagedHostDesktopChatRoute,
  resolveDesktopChatRouteById,
  resolveDesktopChatRouteFromSession,
} from '../services/chat-routing'
import { buildDesktopSlashPaletteState, type DesktopSlashCommand } from '../services/slash-commands'
import { mergeMessageIntoCache } from '../services/message-cache'
import type { SettingsTabID } from '../../settings/types/settings-tabs'
import type { QuickSettingsTabID } from '../../settings/components/desktop-quick-settings-modal'
import type { WorkspaceOverviewTopologyRoute } from '../../../workspaces/launcher/types/workspace-overview'
import { ImageSessionSidebar, type ImageSessionSidebarState } from '../../tools/components/image-session-sidebar'
import { commitWorkspaceChanges } from '../../git/api'
import {
  useDesktopActiveRun,
  useDesktopMessages,
  useDesktopPlan,
  useDesktopPlanRevisions,
  useDesktopPreference,
  useDesktopSession,
} from '../../state/desktop-state-store'

const THINKING_OPTIONS = ['off', 'low', 'medium', 'high', 'xhigh']
const FAST_ON_OFF_OPTIONS = ['off', 'on']
const TODO_DRAG_MIME = 'application/x-swarm-workspace-todo'
const DICTATION_RESTART_DELAY_MS = 180
const DICTATION_FINAL_FLUSH_MS = 450
const ALWAYS_APPLY_SAVED_NOTICE_MS = 5_000
const RETURN_TO_SCROLL_LOCK_MIN_DISTANCE_PX = 48
type SpeechRecognitionConstructor = new () => SpeechRecognitionLike

type SpeechRecognitionWindow = Window & typeof globalThis & {
  SpeechRecognition?: SpeechRecognitionConstructor
  webkitSpeechRecognition?: SpeechRecognitionConstructor
}

type SpeechRecognitionAlternativeLike = {
  transcript: string
  confidence?: number
}

type SpeechRecognitionResultLike = {
  readonly isFinal: boolean
  readonly length: number
  [index: number]: SpeechRecognitionAlternativeLike | undefined
}

type SpeechRecognitionResultListLike = {
  readonly length: number
  [index: number]: SpeechRecognitionResultLike | undefined
}

type SpeechRecognitionResultEventLike = Event & {
  readonly resultIndex: number
  readonly results: SpeechRecognitionResultListLike
}

type SpeechRecognitionErrorEventLike = Event & {
  readonly error?: string
  readonly message?: string
}

type SpeechRecognitionLike = {
  continuous: boolean
  interimResults: boolean
  lang: string
  maxAlternatives: number
  onstart: ((event: Event) => void) | null
  onend: ((event: Event) => void) | null
  onerror: ((event: SpeechRecognitionErrorEventLike) => void) | null
  onresult: ((event: SpeechRecognitionResultEventLike) => void) | null
  start: () => void
  stop: () => void
  abort: () => void
}

function getSpeechRecognitionConstructor(): SpeechRecognitionConstructor | null {
  if (typeof window === 'undefined') {
    return null
  }
  const speechWindow = window as SpeechRecognitionWindow
  return speechWindow.SpeechRecognition ?? speechWindow.webkitSpeechRecognition ?? null
}

function appendDictationText(base: string, addition: string): string {
  const normalizedAddition = addition.replace(/\s+/g, ' ').trim()
  if (!normalizedAddition) {
    return base
  }
  const trimmedBaseEnd = base.replace(/[ \t]+$/g, '')
  if (!trimmedBaseEnd) {
    return normalizedAddition
  }
  const needsSpace = !/[\s\n]$/.test(trimmedBaseEnd) && !/^[,.;:!?]/.test(normalizedAddition)
  return `${trimmedBaseEnd}${needsSpace ? ' ' : ''}${normalizedAddition}`
}

export function isSilentSpeechRecognitionError(error: string, message = ''): boolean {
  const normalizedMessage = message.toLowerCase()
  return error === 'no-speech' || (
    normalizedMessage.includes('kafassistanterrordomain') && /\b(?:error|code)\s*=?\s*1107\b/.test(normalizedMessage)
  )
}

function speechRecognitionErrorMessage(error: string, message = ''): string {
  switch (error) {
    case 'not-allowed':
      return 'Microphone permission was denied.'
    case 'service-not-allowed':
      return 'Browser speech recognition is blocked in this context. Try Safari/Chrome over HTTPS.'
    case 'audio-capture':
      return 'No microphone was found for browser dictation.'
    case 'network':
      return 'Browser speech recognition hit a network error.'
    case 'no-speech':
      return 'No speech detected yet; still listening.'
    case 'language-not-supported':
      return 'This browser does not support speech recognition for the selected language.'
    default:
      return message.trim() || 'Browser speech recognition failed.'
  }
}

const EMPTY_AGENT_STATE: AgentStateRecord = {
  profiles: [],
  activePrimary: 'swarm',
  activeSubagent: {},
  version: 0,
  providerDefaultsPreview: null,
  toolInventory: null,
}

function metadataRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return null
  }
  return value as Record<string, unknown>
}

function metadataString(metadata: Record<string, unknown> | null, key: string): string {
  const value = metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

export function metadataTodoSummary(metadata: Record<string, unknown> | null): { openCount: number; inProgressCount: number; taskCount: number; activeText: string } | null {
  const raw = metadata?.agent_todo_summary
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    return null
  }
  const summary = raw as Record<string, unknown>
  const agent = summary.agent && typeof summary.agent === 'object' && !Array.isArray(summary.agent)
    ? summary.agent as Record<string, unknown>
    : summary
  const taskCount = typeof agent.task_count === 'number' ? agent.task_count : 0
  const openCount = typeof agent.open_count === 'number' ? agent.open_count : 0
  const inProgressCount = typeof agent.in_progress_count === 'number' ? agent.in_progress_count : 0
  if (taskCount <= 0 && openCount <= 0 && inProgressCount <= 0) {
    return null
  }
  const activeTodo = agent.active_todo && typeof agent.active_todo === 'object' && !Array.isArray(agent.active_todo)
    ? agent.active_todo as Record<string, unknown>
    : null
  const activeText = typeof activeTodo?.text === 'string' ? activeTodo.text.trim() : ''
  return { taskCount, openCount, inProgressCount, activeText }
}

export function formatAgentTodoBadge(summary: { openCount: number; inProgressCount: number; taskCount: number; activeText?: string } | null): string {
  if (!summary || summary.taskCount <= 0) {
    return ''
  }
  const total = Math.max(0, summary.taskCount)
  const open = Math.max(0, summary.openCount)
  const active = Math.max(0, summary.inProgressCount)
  const completed = Math.min(total, Math.max(0, total - open))
  const activeText = summary.activeText?.trim() ?? ''
  if (active > 0 && activeText) {
    return activeText
  }
  if (open === 0) {
    return `Complete · ${completed}/${total}`
  }
  if (active > 0) {
    return `${completed}/${total} complete • ${active} active`
  }
  return `${completed}/${total} complete`
}

export function formatMobileAgentTodoBadge(summary: { openCount: number; inProgressCount: number; taskCount: number; activeText?: string } | null): string {
  if (!summary || summary.taskCount <= 0) {
    return ''
  }
  const total = Math.max(0, summary.taskCount)
  const open = Math.max(0, summary.openCount)
  const active = Math.max(0, summary.inProgressCount)
  const completed = Math.min(total, Math.max(0, total - open))
  if (open === 0) {
    return 'Complete'
  }
  if (completed === 0 && active > 0) {
    return 'Active'
  }
  return `${completed}/${total}`
}

function lineageAgentName(label: string): string {
  const trimmed = label.trim()
  if (!trimmed) {
    return ''
  }
  const candidate = trimmed.startsWith('@') ? trimmed.slice(1).trim() : trimmed
  return candidate !== '' && !candidate.includes(' ') ? candidate : ''
}

function isFlowSessionMetadata(metadata: Record<string, unknown> | null): boolean {
  return metadataString(metadata, 'source').toLowerCase() === 'flow'
    || metadataString(metadata, 'lineage_kind').toLowerCase() === 'flow'
    || metadataString(metadata, 'flow_id') !== ''
}

function resolveFlowSessionAgentName(metadata: Record<string, unknown> | null): string {
  const flowAgentName = metadataString(metadata, 'flow_agent_name')
  if (flowAgentName) {
    return flowAgentName
  }
  const agentName = metadataString(metadata, 'agent_name')
  if (agentName) {
    return agentName
  }
  const lineageLabel = lineageAgentName(metadataString(metadata, 'lineage_label'))
  if (lineageLabel) {
    return lineageLabel
  }
  return ''
}

export function sessionUsesReadOnlyFlowIdentity(session: DesktopSessionRecord | null | undefined): boolean {
  return isFlowSessionMetadata(metadataRecord(session?.metadata))
}

export function resolveSessionEffectiveAgentName(session: DesktopSessionRecord | null | undefined, fallbackPrimary: string): string {
  const metadata = metadataRecord(session?.metadata)
  if (isFlowSessionMetadata(metadata)) {
    return resolveFlowSessionAgentName(metadata) || 'flow'
  }
  const explicitSubagent = metadataString(metadata, 'subagent')
  if (explicitSubagent) {
    return explicitSubagent
  }
  const requestedSubagent = metadataString(metadata, 'requested_subagent')
  if (requestedSubagent) {
    return requestedSubagent
  }
  const lineageLabel = lineageAgentName(metadataString(metadata, 'lineage_label'))
  if (lineageLabel) {
    return lineageLabel
  }
  const targetKind = metadataString(metadata, 'target_kind')
  const targetName = metadataString(metadata, 'target_name')
  if (targetKind && targetName) {
    return targetName
  }
  const agentName = metadataString(metadata, 'agent_name')
  if (agentName) {
    return agentName
  }
  return fallbackPrimary.trim() || 'swarm'
}

interface DesktopToastPayload {
  message: string
  tone: 'success' | 'error' | 'info'
}

interface DesktopChatPanelProps {
  hostSwarmName: string
  workspacePath: string
  workspaceName: string
  workspaceTopologyRoutes: WorkspaceOverviewTopologyRoute[]
  localWorkspaceBindingId?: string
  hostSwarmId?: string | null
  session: DesktopSessionRecord | null
  sessionCreateOverride?: (input: {
    title?: string
    mode: string
    agentName?: string
    preference: ResolvedSessionPreference['preference']
    metadata?: Record<string, unknown>
    worktreeMode?: string
    worktreeUseCurrentBranch?: boolean
    worktreeBaseBranch?: string
    worktreeBranchName?: string
  }) => Promise<DesktopSessionRecord>
  pendingWorktreeBranchName?: string
  onClearPendingWorktreeBranch?: () => void
  onSessionCreated: (session: DesktopSessionRecord) => void
  lockedAgentName?: string
  lockedAgentLabel?: string
  hideModeSelector?: boolean
  hideRouteSelector?: boolean
  hideWorkspaceActions?: boolean
  compactControls?: boolean
  newSessionLabel?: string
  onOpenSettingsTab: (tab: SettingsTabID) => void
  onOpenQuickSettings: (tab: QuickSettingsTabID) => void
  onOpenPermissions: () => void
  onOpenWorkspaceLauncher: () => void
  onOpenSidebarMenu: () => void
  onStartNewSession: (workspacePath: string, workspaceName: string) => void
  onToast?: (toast: DesktopToastPayload) => void
  compactHeader?: boolean
  emptyStateMessage?: ReactNode
}

type CommitMode = 'agent' | 'manual'

interface CommitModalState {
  open: boolean
  mode: CommitMode
  instructions: string
  status: 'idle' | 'starting' | 'running' | 'success' | 'error'
  error: string | null
  runId: string | null
  targetSessionId: string | null
}

interface PlanModalState {
  open: boolean
  loading: boolean
  historyLoading: boolean
  saving: boolean
  error: string | null
  hasActive: boolean
  plan: DesktopSessionPlanRecord | null
  revisions: DesktopSessionPlanRevisionRecord[]
}

type ExistingSessionStreamProbe = {
  sessionId: string
  clientRequestId: string
  submittedAt: number
  baselineMessageIds: Set<string>
  baselineAssistantIds: Set<string>
  baselineAssistantContent: string
  baselineLastEventSeq: number
  baselineRunId: string
  submitDiagnostics: ReturnType<typeof getDesktopV3RealtimeDiagnostics>
  emitted: boolean
}

function messageSort(left: ChatMessageRecord, right: ChatMessageRecord): number {
  return left.globalSeq - right.globalSeq
}

function assistantScreenText(messages: ChatMessageRecord[], liveDraft: string, retainedSegments: DesktopLiveAssistantSegment[]): string {
  return [
    ...messages.filter((message) => message.role === 'assistant').map((message) => message.content),
    ...retainedSegments.map((segment) => segment.content),
    liveDraft,
  ].filter((value) => value.trim() !== '').join('\n\n')
}

function newAssistantMessages(messages: ChatMessageRecord[], baselineIds: Set<string>): ChatMessageRecord[] {
  return messages.filter((message) => message.role === 'assistant' && !baselineIds.has(message.id))
}

export function dedupeMessages(messages: ChatMessageRecord[]): ChatMessageRecord[] {
  return messages.reduce<ChatMessageRecord[]>((merged, message) => mergeMessageIntoCache(merged, message), []).sort(messageSort)
}

function messageRoleLabel(role: string, assistantLabel = 'Swarm'): string {
  switch (role) {
    case 'user':
      return 'You'
    case 'assistant':
      return assistantLabel.trim() || 'Swarm'
    case 'reasoning':
      return 'Thinking'
    case 'tool':
      return 'Tool'
    case 'system':
      return 'System'
    default:
      return role
  }
}

export function resolveMessageAssistantLabel(message: ChatMessageRecord | null | undefined, fallbackAgent: string): string {
  const metadata = metadataRecord(message?.metadata)
  const agentName = metadataString(metadata, 'agent_name') || metadataString(metadata, 'resolved_agent_name')
  return agentName || fallbackAgent.trim() || 'swarm'
}

export function isDesktopCompactionCheckpointMessage(message: ChatMessageRecord | null | undefined): boolean {
  return message?.role.trim().toLowerCase() === 'system'
    && message.content.trim().startsWith('[context-compact]')
}

export function isDesktopManualCompactionAckMessage(message: ChatMessageRecord | null | undefined): boolean {
  const source = metadataString(metadataRecord(message?.metadata), 'source').toLowerCase()
  if (source === 'manual_context_compaction_ack') {
    return true
  }
  return message?.role.trim().toLowerCase() === 'assistant'
    && message.content.trim().startsWith('Manual context compact complete (Compact #')
    && !message.content.includes('Compacted recap:')
}

export function visibleDesktopChatMessages(messages: ChatMessageRecord[]): ChatMessageRecord[] {
  const hiddenIds = new Set<string>()
  for (let index = 0; index < messages.length; index += 1) {
    const message = messages[index]
    if (!isDesktopManualCompactionAckMessage(message)) {
      continue
    }
    const previous = index > 0 ? messages[index - 1] : null
    if (isDesktopCompactionCheckpointMessage(previous)) {
      hiddenIds.add(message.id)
    }
  }
  return messages.filter((message) => !hiddenIds.has(message.id))
}

function optionKey(provider: string, model: string, contextMode = ''): string {
  return `${provider}:${model}:${contextMode.trim().toLowerCase()}`
}

function modelOptionForPreference(
  options: ModelOptionRecord[],
  preference: ResolvedSessionPreference['preference'],
): ModelOptionRecord | null {
  const provider = preference.provider.trim()
  const model = preference.model.trim()
  if (!provider || !model) {
    return null
  }
  const exactKey = optionKey(provider, model, preference.contextMode)
  return options.find((option) => option.key === exactKey)
    ?? options.find((option) => option.provider === provider && option.model === model)
    ?? null
}

function agentPresetPreference(profile: AgentProfileRecord | null | undefined): ResolvedSessionPreference['preference'] | null {
  const provider = profile?.provider.trim() ?? ''
  const model = profile?.model.trim() ?? ''
  if (!provider || !model) {
    return null
  }
  return {
    provider,
    model,
    thinking: normalizeThinkingValue(profile?.thinking || defaultThinkingForProvider(provider)),
    serviceTier: '',
    contextMode: '',
    updatedAt: profile?.updatedAt ?? 0,
  }
}

function agentPolicyFromProfile(
  profile: AgentProfileRecord | null | undefined,
  options: ModelOptionRecord[],
): AgentModelPolicyRecord | null {
  const preference = agentPresetPreference(profile)
  if (!preference) {
    return null
  }
  const option = modelOptionForPreference(options, preference)
  const resolvedPreference = option
    ? {
        ...preference,
        provider: option.provider,
        model: option.model,
        contextMode: option.contextMode,
        thinking: normalizeThinkingValue(preference.thinking || option.thinking || defaultThinkingForProvider(option.provider)),
      }
    : preference
  const contextWindow = option?.contextWindow ?? 0
  return {
    agentName: profile?.name.trim() ?? '',
    resolvedAgentName: profile?.name.trim() ?? '',
    source: 'agent_preset',
    locked: true,
    reason: agentModelLockedMessage(profile?.name ?? ''),
    preference: resolvedPreference,
    contextWindow,
    maxOutputTokens: 0,
  }
}

function preferenceFromAgentPolicy(policy: AgentModelPolicyRecord | null): ResolvedSessionPreference | null {
  if (!policy?.locked) {
    return null
  }
  return {
    preference: {
      provider: policy.preference.provider,
      model: policy.preference.model,
      thinking: normalizeThinkingValue(policy.preference.thinking),
      serviceTier: policy.preference.serviceTier,
      contextMode: policy.preference.contextMode,
      updatedAt: policy.preference.updatedAt,
    },
    contextWindow: policy.contextWindow,
    maxOutputTokens: policy.maxOutputTokens,
  }
}

type ScrollMetrics = Pick<HTMLElement, 'scrollHeight' | 'scrollTop' | 'clientHeight'>

function scrollBottomGap(element: ScrollMetrics): number {
  return Math.max(0, element.scrollHeight - element.scrollTop - element.clientHeight)
}

function nearBottom(element: ScrollMetrics): boolean {
  return scrollBottomGap(element) < RETURN_TO_SCROLL_LOCK_MIN_DISTANCE_PX
}

export function shouldShowScrollLockReturnButton(element: ScrollMetrics): boolean {
  return scrollBottomGap(element) >= Math.max(RETURN_TO_SCROLL_LOCK_MIN_DISTANCE_PX, element.clientHeight / 2)
}

export type RenderItem =
  | { type: 'message'; message: ChatMessageRecord; virtualKey?: string }
  | { type: 'live-tool'; toolMessage: NonNullable<ChatMessageRecord['toolMessage']> }
  | { type: 'live-assistant'; id: string; content: string; timelineSeq?: number }
  | { type: 'live-reasoning'; id: string; text: string; summary: string; state: DesktopSessionRecord['live']['reasoningState']; startedAt: number | null; completedAt?: number | null; timelineSeq?: number }

function jsonStringValue(record: Record<string, unknown> | null | undefined, key: string): string {
  const value = record?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function jsonNumberValue(record: Record<string, unknown> | null | undefined, key: string): number | undefined {
  const value = record?.[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

export function imageSidebarStateFromToolMessage(toolMessage: NonNullable<ChatMessageRecord['toolMessage']> | null | undefined): ImageSessionSidebarState | null {
  if (!toolMessage) {
    return null
  }
  const toolName = toolMessage.tool.trim().toLowerCase()
  if (toolName !== 'manage-image' && toolName !== 'manage_image') {
    return null
  }
  const outputJson = toolMessage.output.trim()
    ? metadataRecord(safeParseJson(toolMessage.output))
    : metadataRecord(safeParseJson(toolMessage.completedOutput))
  const argsJson = metadataRecord(toolMessage.argumentsJson)
  const threadId = jsonStringValue(outputJson, 'thread_id') || jsonStringValue(argsJson, 'thread_id')
  if (!threadId) {
    return null
  }
  const status = jsonStringValue(outputJson, 'status') || (toolMessage.state === 'running' ? 'generating' : toolMessage.state)
  return {
    open: true,
    threadId,
    title: jsonStringValue(argsJson, 'title') || jsonStringValue(argsJson, 'purpose') || 'Image generation',
    provider: jsonStringValue(outputJson, 'provider') || jsonStringValue(argsJson, 'provider'),
    model: jsonStringValue(outputJson, 'model') || jsonStringValue(argsJson, 'model'),
    requestedCount: jsonNumberValue(outputJson, 'requested_count') ?? jsonNumberValue(argsJson, 'count'),
    savedCount: jsonNumberValue(outputJson, 'saved_count'),
    status,
  }
}

function safeParseJson(value: string): unknown {
  const trimmed = value.trim()
  if (!trimmed.startsWith('{') || !trimmed.endsWith('}')) {
    return null
  }
  try {
    return JSON.parse(trimmed) as unknown
  } catch {
    return null
  }
}

function renderItemKey(item: RenderItem | undefined, index: number): string {
  if (!item) {
    return `render-item:${index}`
  }
  switch (item.type) {
    case 'message':
      return item.virtualKey ?? item.message.id
    case 'live-tool':
      return `live-tool:${item.toolMessage.toolInstanceId || item.toolMessage.callId || item.toolMessage.tool || 'active'}`
    case 'live-assistant':
    case 'live-reasoning':
      return item.id
    default:
      return `render-item:${index}`
  }
}

function renderItemTimelineSeq(item: RenderItem): number {
  const seq = item.type === 'message'
    ? item.message.globalSeq
    : item.type === 'live-assistant' || item.type === 'live-reasoning'
      ? item.timelineSeq ?? 0
      : item.type === 'live-tool'
        ? item.toolMessage.timelineSeq ?? 0
        : 0
  return Number.isFinite(seq) && seq > 0 ? seq : 0
}

export function orderDesktopTimelineItems(items: RenderItem[]): RenderItem[] {
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

export function desktopChatThinkingTagsMeasurementKey(thinkingTagsEnabled: boolean): string {
  return thinkingTagsEnabled ? 'thinking-tags:on' : 'thinking-tags:off'
}

export function desktopChatVirtualItemKey(baseKey: string, thinkingTagsEnabled: boolean): string {
  return `${desktopChatThinkingTagsMeasurementKey(thinkingTagsEnabled)}:${baseKey}`
}

function estimateTaskToolSize(rowCount: number): number {
  if (rowCount <= 0) {
    return 104
  }
  const header = 94
  if (rowCount > 10) {
    const compactRowHeight = 40
    return Math.min(720, header + (rowCount * compactRowHeight))
  }
  const rowHeight = 52
  return Math.min(920, header + (rowCount * rowHeight))
}

function estimateRenderItemSize(item: RenderItem | undefined, thinkingTagsEnabled = true): number {
  if (!item) {
    return 96
  }
  switch (item.type) {
    case 'live-tool': {
      if (item.toolMessage.tool === 'task' && item.toolMessage.taskRows.length > 0) {
        return estimateTaskToolSize(item.toolMessage.taskRows.length)
      }
      const detailLines = item.toolMessage.previewLines.length
      return Math.min(520, 104 + (Math.max(detailLines - 1, 0) * 22))
    }
    case 'live-assistant':
      return Math.min(640, 88 + (Math.ceil(Math.max(item.content.length, 1) / 100) * 22))
    case 'live-reasoning': {
      const base = 72
      const reasoningLength = thinkingTagsEnabled ? (item.text || item.summary).length : 0
      return thinkingTagsEnabled
        ? Math.min(420, base + (Math.ceil(Math.max(reasoningLength, 1) / 100) * 22))
        : base
    }
    case 'message': {
      if (item.message.role === 'tool' && item.message.toolMessage) {
        const toolMessage = item.message.toolMessage
        if (toolMessage.tool === 'task' && toolMessage.taskRows.length > 0) {
          return estimateTaskToolSize(toolMessage.taskRows.length)
        }
        const base = toolMessage.searchData ? 180 : 104
        const detailLines = toolMessage.previewLines.length + (toolMessage.searchData ? Math.min(toolMessage.searchData.files.length, 6) * 2 : 0)
        return Math.min(520, base + (Math.max(detailLines - 1, 0) * 22))
      }
      if (item.message.role === 'reasoning') {
        const base = 72
        const reasoningLength = thinkingTagsEnabled ? item.message.content.length : 0
        return thinkingTagsEnabled
          ? Math.min(420, base + (Math.ceil(Math.max(reasoningLength, 1) / 100) * 22))
          : base
      }
      const base = item.message.role === 'user' ? 72 : 88
      return Math.min(640, base + (Math.ceil(Math.max(item.message.content.length, 1) / 100) * 22))
    }
    default:
      return 96
  }
}

function isLiveToolEventType(eventType: string): boolean {
  return [
    'tool.started',
    'tool.delta',
    'tool.completed',
    'run.tool.started',
    'run.tool.delta',
    'run.tool.completed',
    'session.tool.started',
    'session.tool.delta',
    'session.tool.completed',
  ].includes(eventType)
}

function hasRenderableToolSnapshot(snapshot: {
  toolName: string | null
  toolCallId: string | null
  toolInstanceId?: string | null
  toolArguments: string | null
  toolOutput: string
} | null | undefined): boolean {
  if (!snapshot) {
    return false
  }
  return Boolean(
    snapshot.toolName?.trim() ||
    snapshot.toolCallId?.trim() ||
    snapshot.toolArguments?.trim() ||
    snapshot.toolOutput?.trim(),
  )
}

type LiveToolSnapshot = {
  pathId?: DesktopLiveToolRecord['pathId']
  toolName: string | null
  toolCallId: string | null
  toolInstanceId?: string | null
  toolArguments: string | null
  toolOutput: string
  state: NonNullable<ChatMessageRecord['toolMessage']>['state']
  seq?: number | null
}

function liveToolRecordSnapshot(record: DesktopLiveToolRecord): LiveToolSnapshot | null {
  const toolName = record.toolName?.trim() ?? ''
  if (!toolName) {
    return null
  }
  return {
    pathId: record.pathId,
    toolName,
    toolCallId: record.callId,
    toolInstanceId: record.toolInstanceId,
    toolArguments: record.toolArguments,
    toolOutput: record.toolOutput,
    state: record.state,
    seq: record.seq ?? null,
  }
}

function buildLiveToolMessageFromSnapshot(snapshot: LiveToolSnapshot): NonNullable<ChatMessageRecord['toolMessage']> | null {
  const toolName = snapshot.toolName?.trim() ?? ''
  if (!toolName) {
    return null
  }

  const state = snapshot.state
  const toolMessage = buildStructuredToolMessage({
    pathId: snapshot.pathId,
    tool: toolName,
    callId: snapshot.toolCallId ?? '',
    toolInstanceId: snapshot.toolInstanceId ?? '',
    argumentsText: snapshot.toolArguments ?? '',
    outputText: snapshot.toolOutput ?? '',
    state,
  })

  if (!toolMessage) {
    return null
  }

  return {
    ...toolMessage,
    state,
    timelineSeq: snapshot.seq ?? undefined,
  }
}

export function buildLiveToolMessages(session: DesktopSessionRecord | null | undefined): NonNullable<ChatMessageRecord['toolMessage']>[] {
  const live = session?.live
  if (!live) {
    return []
  }

  const historyMessages = (live.toolHistory ?? [])
    .slice()
    .reverse()
    .map(liveToolRecordSnapshot)
    .filter((snapshot): snapshot is LiveToolSnapshot => hasRenderableToolSnapshot(snapshot))
    .map(buildLiveToolMessageFromSnapshot)
    .filter((message): message is NonNullable<ChatMessageRecord['toolMessage']> => message !== null)
  if (historyMessages.length > 0) {
    return historyMessages
  }

  const lastEventType = live.lastEventType?.trim() ?? ''
  const liveStatus = live.status ?? 'idle'
  const activeSnapshot = {
    toolName: live.toolName,
    toolCallId: live.toolCallId,
    toolArguments: live.toolArguments,
    toolOutput: live.toolOutput,
    state: 'running' as const,
  }
  const retainedSnapshot = {
    toolName: live.retainedToolName,
    toolCallId: live.retainedToolCallId,
    toolArguments: live.retainedToolArguments,
    toolOutput: live.retainedToolOutput,
    state: live.retainedToolState ?? 'done' as const,
  }

  const useActiveSnapshot = hasRenderableToolSnapshot(activeSnapshot)
    && (isLiveToolEventType(lastEventType) || ['starting', 'running', 'blocked'].includes(liveStatus))
  const snapshot = useActiveSnapshot ? activeSnapshot : hasRenderableToolSnapshot(retainedSnapshot) ? retainedSnapshot : null
  const message = snapshot ? buildLiveToolMessageFromSnapshot(snapshot) : null
  return message ? [message] : []
}

function toolMessageIdentity(toolMessage: NonNullable<ChatMessageRecord['toolMessage']>): string {
  return (toolMessage.toolInstanceId?.trim() || toolMessage.callId.trim())
}

function hasCanonicalLiveToolReplacement(
  messages: ChatMessageRecord[],
  liveToolMessage: NonNullable<ChatMessageRecord['toolMessage']> | null,
): boolean {
  if (!liveToolMessage) {
    return false
  }
  const liveIdentity = toolMessageIdentity(liveToolMessage)
  if (!liveIdentity) {
    return false
  }
  return messages.some((message) => {
    const toolMessage = message.toolMessage
    if (!toolMessage) {
      return false
    }
    return toolMessageIdentity(toolMessage) === liveIdentity
  })
}

function normalizeAssistantReplayContent(value: string): string {
  return value.replace(/\r\n/g, '\n').replace(/\r/g, '\n').trim()
}

function canonicalAssistantReplayContents(messages: ChatMessageRecord[]): Set<string> {
  return new Set(
    messages
      .filter((message) => message.role.trim().toLowerCase() === 'assistant')
      .map((message) => normalizeAssistantReplayContent(message.content))
      .filter((content) => content !== ''),
  )
}

export function liveAssistantDraftHasCanonicalReplay(
  draft: string,
  messages: ChatMessageRecord[],
): boolean {
  const content = normalizeAssistantReplayContent(draft)
  if (!content) {
    return false
  }
  return canonicalAssistantReplayContents(messages).has(content)
}

function canonicalReasoningReplayContents(messages: ChatMessageRecord[]): Set<string> {
  return new Set(
    messages
      .filter((message) => message.role.trim().toLowerCase() === 'reasoning')
      .map((message) => normalizeAssistantReplayContent(message.content))
      .filter((content) => content !== ''),
  )
}

function liveReasoningHasCanonicalReplay(
  text: string,
  summary: string,
  messages: ChatMessageRecord[],
): boolean {
  const canonicalReasoningContents = canonicalReasoningReplayContents(messages)
  if (canonicalReasoningContents.size === 0) {
    return false
  }
  const normalizedText = normalizeAssistantReplayContent(text)
  const normalizedSummary = normalizeAssistantReplayContent(summary)
  return Boolean(
    (normalizedText && canonicalReasoningContents.has(normalizedText))
    || (normalizedSummary && canonicalReasoningContents.has(normalizedSummary)),
  )
}

function liveReasoningRecordItem(record: DesktopLiveReasoningRecord, messages: ChatMessageRecord[]): Extract<RenderItem, { type: 'live-reasoning' }> | null {
  const text = record.text.trim()
  const summary = record.summary.trim()
  if (record.state === 'done' && liveReasoningHasCanonicalReplay(text, summary, messages)) {
    return null
  }
  if (!text && !summary && record.state !== 'running') {
    return null
  }
  return {
    type: 'live-reasoning',
    id: `live-reasoning:${record.key}`,
    text,
    summary,
    state: record.state,
    startedAt: record.startedAt,
    completedAt: record.completedAt,
    timelineSeq: record.timelineSeq,
  }
}

export function buildLiveReasoningItems(
  session: DesktopSessionRecord | null | undefined,
  messages: ChatMessageRecord[],
): Array<Extract<RenderItem, { type: 'live-reasoning' }>> {
  const history = session?.live.reasoningHistory ?? []
  return history
    .slice()
    .reverse()
    .map((record) => liveReasoningRecordItem(record, messages))
    .filter((item): item is Extract<RenderItem, { type: 'live-reasoning' }> => item !== null)
}

export function retainedAssistantSegmentsWithoutCanonicalReplay(
  segments: DesktopLiveAssistantSegment[],
  messages: ChatMessageRecord[],
): DesktopLiveAssistantSegment[] {
  if (segments.length === 0) {
    return segments
  }

  const canonicalAssistantContents = canonicalAssistantReplayContents(messages)

  if (canonicalAssistantContents.size === 0) {
    return segments
  }

  return segments.filter((segment) => {
    const content = normalizeAssistantReplayContent(segment.content)
    return content !== '' && !canonicalAssistantContents.has(content)
  })
}

function emptyPreference(): ResolvedSessionPreference {
  return {
    preference: {
      provider: '',
      model: '',
      thinking: '',
      serviceTier: '',
      contextMode: '',
      updatedAt: 0,
    },
    contextWindow: 0,
    maxOutputTokens: 0,
  }
}

function reasoningStateLabel(state: DesktopSessionRecord['live']['reasoningState']): string {
  switch (state) {
    case 'running':
      return 'Thinking'
    case 'error':
      return 'Thinking failed'
    default:
      return 'Thinking'
  }
}

function reasoningElapsedLabel(startedAt: number | null, timerNow: number, completedAt: number | null = null): string {
  if (typeof startedAt !== 'number' || startedAt <= 0) {
    return ''
  }
  const endAt = typeof completedAt === 'number' && completedAt > startedAt ? completedAt : timerNow
  return formatDurationCompact(endAt - startedAt)
}

export function savedRuleCountdownSeconds(expiresAt: number | null, now: number): number {
  if (!expiresAt) {
    return 0
  }
  return Math.max(0, Math.ceil((expiresAt - now) / 1000))
}

function reasoningHeadline(state: DesktopSessionRecord['live']['reasoningState'], startedAt: number | null, timerNow: number, completedAt: number | null = null): string {
  const label = reasoningStateLabel(state)
  const elapsed = reasoningElapsedLabel(startedAt, timerNow, state === 'running' ? null : completedAt)
  return elapsed ? `${label} · ${elapsed}` : label
}

function renderReasoningBody(text: string, summary: string, thinkingTagsEnabled: boolean): string {
  if (!thinkingTagsEnabled) {
    return ''
  }
  return text || summary || 'Thinking…'
}

function normalizeThinkingValue(value: string): string {
  const normalized = value.trim().toLowerCase()
  return normalized === '' ? 'off' : normalized
}

function hasExplicitPreference(preference: ResolvedSessionPreference['preference']): boolean {
  return preference.provider.trim() !== '' && preference.model.trim() !== '' && normalizeThinkingValue(preference.thinking).trim() !== ''
}

function agentModelLockedMessage(agentName: string): string {
  const label = agentName.trim() || 'this agent'
  return `${label} has a preset model. Set the agent model to Default in Agents settings before choosing another model.`
}

function defaultThinkingForProvider(provider: string): string {
  switch (provider.trim().toLowerCase()) {
    case 'copilot':
    case 'fireworks':
      return 'high'
    default:
      return 'xhigh'
  }
}

function normalizeFastToggle(value: string): 'on' | 'off' {
  return value.trim().toLowerCase() === 'on' ? 'on' : 'off'
}

function fastToggleFromPreference(preference: ResolvedSessionPreference['preference']): 'on' | 'off' {
  return preference.serviceTier.trim().toLowerCase() === 'fast' ? 'on' : 'off'
}

function buildFastPreference(
  preference: ResolvedSessionPreference['preference'],
  fast: 'on' | 'off',
): ResolvedSessionPreference['preference'] {
  return {
    ...preference,
    serviceTier: fast === 'on' ? 'fast' : '',
  }
}

type SessionMode = 'plan' | 'auto'

function normalizeSessionMode(mode: string): SessionMode {
  return mode.trim().toLowerCase() === 'auto' ? 'auto' : 'plan'
}

function executionSettingLabel(profile: AgentStateRecord['profiles'][number] | null): string {
  if (!profile || profile.exitPlanModeEnabled) {
    return ''
  }
  return profile.executionSetting === 'readwrite' ? 'readwrite' : 'read'
}

function desiredSessionModeForAgent(
  profile: AgentStateRecord['profiles'][number] | null,
  currentMode: string,
): SessionMode {
  if (!profile) {
    return normalizeSessionMode(currentMode)
  }
  if (!profile.exitPlanModeEnabled) {
    return 'auto'
  }
  return normalizeSessionMode(currentMode) === 'auto' ? 'auto' : 'plan'
}

function formatDurationCompact(durationMs: number): string {
  const safeDurationMs = Number.isFinite(durationMs) ? Math.max(0, durationMs) : 0
  if (safeDurationMs < 1000) {
    return `${Math.floor(safeDurationMs)}ms`
  }
  if (safeDurationMs < 60_000) {
    return `${(safeDurationMs / 1000).toFixed(1)}s`
  }
  const minutes = Math.floor(safeDurationMs / 60_000)
  const seconds = Math.floor((safeDurationMs % 60_000) / 1000)
  return `${minutes}m${String(seconds).padStart(2, '0')}s`
}

export interface DesktopChatRunControls {
  activeRunIntent: DesktopRunIntentRecord | null
  activeRunStartedAt: number
  durableRunActive: boolean
  resumableRunId: string
  reconnectingRun: boolean
  submitting: boolean
  canStop: boolean
  showRunTimer: boolean
  runTimerLabel: string
  composerDisabled: boolean
  runActive: boolean
}

function canonicalActiveRunIntent(runIntent: DesktopRunIntentRecord | null | undefined): DesktopRunIntentRecord | null {
  const status = runIntent?.status.trim().toLowerCase() ?? ''
  return status === 'pending_executor' || status === 'running' ? runIntent ?? null : null
}

export function deriveDesktopChatRunControls(
  runIntent: DesktopRunIntentRecord | null | undefined,
  options: { liveSummary?: string | null; timerNow: number },
): DesktopChatRunControls {
  const activeRunIntent = canonicalActiveRunIntent(runIntent)
  const activeRunStatus = activeRunIntent?.status.trim().toLowerCase() ?? ''
  const activeRunStartedAt = activeRunIntent && activeRunIntent.createdAt > 0 ? activeRunIntent.createdAt : 0
  const durableRunActive = activeRunIntent !== null
  const resumableRunId = activeRunIntent?.runId.trim() ?? ''
  const reconnectingRun = durableRunActive && options.liveSummary === 'Reconnecting…'
  const submitting = activeRunStatus === 'pending_executor' || reconnectingRun
  const canStop = durableRunActive && resumableRunId !== ''
  const showRunTimer = activeRunStartedAt > 0 && durableRunActive
  return {
    activeRunIntent,
    activeRunStartedAt,
    durableRunActive,
    resumableRunId,
    reconnectingRun,
    submitting,
    canStop,
    showRunTimer,
    runTimerLabel: showRunTimer ? formatDurationCompact(options.timerNow - activeRunStartedAt) : reconnectingRun ? 'Reconnecting…' : '',
    composerDisabled: durableRunActive,
    runActive: durableRunActive,
  }
}


function formatContextUsageBadgeLabel(usage: DesktopSessionRecord['usage'] | null): string {
  if (!usage || usage.contextWindow <= 0) {
    return ''
  }
  const window = usage.contextWindow
  const remaining = Math.max(0, Math.min(window, usage.remainingTokens))
  const leftPercent = Math.max(0, Math.min(100, Math.round((remaining * 100) / window)))
  return `${leftPercent}%`
}

function formatContextUsageTooltip(usage: DesktopSessionRecord['usage'] | null): string {
  if (!usage || usage.contextWindow <= 0) {
    return ''
  }
  const window = usage.contextWindow
  const remaining = Math.max(0, Math.min(window, usage.remainingTokens))
  const used = Math.max(0, window - remaining)
  return `${formatContextWindow(remaining)} left of ${formatContextWindow(window)} total · ${formatContextWindow(used)} used`
}

function buildCommitAgentInstructions(userInstructions: string): string {
  const instructions = [
    'You are Memory handling /commit from the web desktop as a background durable-artifact task.',
    'Inspect git status and diffs in the scoped current working directory before making changes.',
    'Understand the changed work, stage the appropriate files, and create one commit with a concise, accurate message.',
    'Use git add and git commit only when needed and only inside the granted workspace scope.',
    'Only run git push if the user explicitly requested push.',
    'If permissions are required, rely on the existing backend permission system and wait for approval.',
  ]
  if (userInstructions.trim() !== '') {
    instructions.push(`Additional user instructions: ${userInstructions.trim()}`)
  }
  return instructions.join('\n')
}


function commitExecutionContext(session: DesktopSessionRecord): NonNullable<Parameters<typeof startSessionRun>[0]['executionContext']> {
  const metadata = session.metadata && typeof session.metadata === 'object' ? session.metadata : null
  const executionContext = metadata && typeof metadata.execution_context === 'object'
    ? metadata.execution_context as Record<string, unknown>
    : null
  return {
    workspace_path: typeof executionContext?.workspace_path === 'string' && executionContext.workspace_path.trim() !== ''
      ? executionContext.workspace_path.trim()
      : (session.runtimeWorkspacePath || session.workspacePath || '').trim(),
    cwd: typeof executionContext?.cwd === 'string' && executionContext.cwd.trim() !== ''
      ? executionContext.cwd.trim()
      : (session.runtimeWorkspacePath || session.workspacePath || '').trim(),
    worktree_mode: typeof executionContext?.worktree_mode === 'string' && executionContext.worktree_mode.trim() !== ''
      ? executionContext.worktree_mode.trim()
      : 'inherit',
    worktree_root_path: typeof executionContext?.worktree_root_path === 'string' ? executionContext.worktree_root_path.trim() : (session.worktreeRootPath || '').trim(),
    worktree_branch: typeof executionContext?.worktree_branch === 'string' ? executionContext.worktree_branch.trim() : (session.worktreeBranch || '').trim(),
    worktree_base_branch: typeof executionContext?.worktree_base_branch === 'string' ? executionContext.worktree_base_branch.trim() : (session.worktreeBaseBranch || '').trim(),
  }
}

function childCommitSessionTitle(session: DesktopSessionRecord, instructions: string): string {
  const parentTitle = (session.title || '').trim()
  if (instructions.trim() === '') {
    return parentTitle ? `Commit · ${parentTitle}` : 'Commit'
  }
  return `Commit · ${parentTitle || 'Session'} · ${instructions.trim()}`.slice(0, 80)
}

const EMPTY_COMMIT_MODAL_STATE: CommitModalState = {
  open: false,
  mode: 'agent',
  instructions: '',
  status: 'idle',
  error: null,
  runId: null,
  targetSessionId: null,
}

const EMPTY_PLAN_MODAL_STATE: PlanModalState = {
  open: false,
  loading: false,
  historyLoading: false,
  saving: false,
  error: null,
  hasActive: false,
  plan: null,
  revisions: [],
}

function commitStatusLabel(state: CommitModalState): string {
  switch (state.status) {
    case 'starting':
      return 'Starting save…'
    case 'running':
      return state.mode === 'manual' ? 'Manual commit running…' : 'Memory commit running…'
    case 'success':
      return ''
    case 'error':
      return state.error || 'Save failed.'
    default:
      return ''
  }
}

export function DesktopChatPanel({
  hostSwarmName,
  workspacePath,
  workspaceName,
  workspaceTopologyRoutes,
  localWorkspaceBindingId,
  hostSwarmId,
  session,
  onSessionCreated,
  onOpenSettingsTab,
  onOpenQuickSettings,
  onOpenPermissions,
  onOpenWorkspaceLauncher,
  onOpenSidebarMenu,
  onStartNewSession,
  onToast,
  sessionCreateOverride,
  pendingWorktreeBranchName,
  onClearPendingWorktreeBranch,
  lockedAgentName,
  lockedAgentLabel,
  hideModeSelector = false,
  hideRouteSelector = false,
  hideWorkspaceActions = false,
  compactControls = false,
  newSessionLabel = 'New chat',
  compactHeader = false,
  emptyStateMessage,
}: DesktopChatPanelProps) {
  const queryClient = useQueryClient()
  const sessionId = session?.id ?? null
  const pendingWorktreeBranch = sessionId ? '' : (pendingWorktreeBranchName?.trim() ?? '')
  const ensureRunStream = useDesktopUiStore((state) => state.ensureRunStream)
  const submitPrompt = useDesktopUiStore((state) => state.submitPrompt)
  const stopRun = useDesktopUiStore((state) => state.stopRun)
  const setSessionDraft = useDesktopUiStore((state) => state.setSessionDraft)
  const setSessionDraftMode = useDesktopUiStore((state) => state.setSessionDraftMode)
  const [panelError, setPanelError] = useState<string | null>(null)
  const [messagesLoading, setMessagesLoading] = useState(false)
  const lastLoadedMessageTailSessionRef = useRef('')
  const [selectedPrimaryAgent, setSelectedPrimaryAgent] = useState('swarm')
  const [currentSessionAgent, setCurrentSessionAgent] = useState('swarm')
  const lastAutoModeSyncRef = useRef('')
  const [permissionError, setPermissionError] = useState<string | null>(null)
  const [resolvingPermissionIds, setResolvingPermissionIds] = useState<Set<string>>(() => new Set())
  const [lastSavedRulePreview, setLastSavedRulePreview] = useState<string | null>(null)
  const [lastSavedRuleExpiresAt, setLastSavedRuleExpiresAt] = useState<number | null>(null)
  const [savedRuleCountdownNow, setSavedRuleCountdownNow] = useState(() => Date.now())
  const [commitModal, setCommitModal] = useState<CommitModalState>(EMPTY_COMMIT_MODAL_STATE)
  const [planModal, setPlanModal] = useState<PlanModalState>(EMPTY_PLAN_MODAL_STATE)
  const [timerNow, setTimerNow] = useState(() => Date.now())
  const [thinkingTagsEnabled, setThinkingTagsEnabled] = useState(true)
  const [thinkingTagsSaving, setThinkingTagsSaving] = useState(false)
  const [defaultNewSessionMode, setDefaultNewSessionMode] = useState<'auto' | 'plan'>('auto')
  const [imageSidebar, setImageSidebar] = useState<ImageSessionSidebarState | null>(null)
  const scrollerRef = useRef<HTMLDivElement | null>(null)
  const composerTextareaRef = useRef<HTMLTextAreaElement | null>(null)
  const shouldStickToBottomRef = useRef(true)
  const scrollToLatestFrameRef = useRef<number | null>(null)
  const [showScrollLockReturnButton, setShowScrollLockReturnButton] = useState(false)
  const liveAssistantHandoffRef = useRef<{ sessionId: string; content: string; key: string } | null>(null)
  const existingSessionStreamProbeRef = useRef<ExistingSessionStreamProbe | null>(null)
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null)
  const dictationEnabledRef = useRef(false)
  const dictationCanRunRef = useRef(false)
  const dictationRestartTimerRef = useRef<number | null>(null)
  const dictationBaseDraftRef = useRef('')
  const dictationFinalTranscriptRef = useRef('')
  const dictationInterimTranscriptRef = useRef('')
  const dictationManualStopRef = useRef(false)
  const dictationAcceptLateResultRef = useRef(false)
  const dictationStartingRef = useRef(false)
  const [dictationSupported, setDictationSupported] = useState(false)
  const [dictationEnabled, setDictationEnabled] = useState(false)
  const [dictationListening, setDictationListening] = useState(false)
  const [dictationError, setDictationError] = useState<string | null>(null)

  const {
    data: agentState = EMPTY_AGENT_STATE,
  } = useQuery(agentStateQueryOptions())

  const { data: modelOptions = [] } = useQuery(modelOptionsQueryOptions())
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())

  const dbSession = useDesktopSession(sessionId)
  const liveSession = dbSession ?? session
  const trackedCommitSession = useDesktopSession(commitModal.targetSessionId)
  const trackedCommitActiveRun = useDesktopActiveRun(commitModal.targetSessionId)
  const draftSessionMode = useDesktopUiStore((state) => state.getSessionDraftMode(null, workspacePath))
  const draftSessionKey = `__workspace__:${workspacePath}`
  const routeOptions = useMemo(() => buildDesktopChatRouteOptions({
    hostSwarmName,
    workspacePath,
    workspaceName,
    topologyRoutes: workspaceTopologyRoutes,
    localWorkspaceBindingId,
    hostSwarmId,
  }), [hostSwarmName, hostSwarmId, localWorkspaceBindingId, workspacePath, workspaceName, workspaceTopologyRoutes])
  const defaultChatRoute = routeOptions[0]!
  const [selectedRouteId, setSelectedRouteId] = useState(() => defaultChatRoute?.id ?? 'host')
  const [draftRouteOverrideId, setDraftRouteOverrideId] = useState<string | null>(null)

  const dbMessages = useDesktopMessages(sessionId)
  const dbPreference = useDesktopPreference(sessionId)
  const dbActiveRun = useDesktopActiveRun(sessionId)
  const dbPlan = useDesktopPlan(sessionId)
  const dbPlanRevisions = useDesktopPlanRevisions(sessionId)

  const sessionPreferenceQuery = useQuery({
    ...sessionPreferenceQueryOptions(sessionId ?? '', queryClient),
    enabled: Boolean(sessionId),
    initialData: dbPreference ?? undefined,
  })

  useQuery({
    ...sessionUsageQueryOptions(sessionId ?? '', queryClient),
    enabled: Boolean(sessionId),
    initialData: liveSession?.usage ?? undefined,
  })

  useQuery({
    ...sessionPermissionsQueryOptions(sessionId ?? '', queryClient),
    enabled: Boolean(sessionId),
    initialData: liveSession?.permissionsHydrated ? liveSession.pendingPermissions : undefined,
  })

  const draftPreferenceQuery = useQuery({
    ...draftModelQueryOptions(),
    enabled: sessionId === null,
    initialData: queryClient.getQueryData<ResolvedSessionPreference>(draftModelQueryOptions().queryKey),
  })

  const sessionPreference = dbPreference ?? sessionPreferenceQuery.data ?? emptyPreference()
  const draftPreference = draftPreferenceQuery.data ?? emptyPreference()
  const selectedAgentProfileForModel = useMemo(
    () => agentState.profiles.find((profile) => profile.name === selectedPrimaryAgent) ?? null,
    [agentState.profiles, selectedPrimaryAgent],
  )
  const selectedAgentModelPolicy = useMemo(
    () => agentPolicyFromProfile(selectedAgentProfileForModel, modelOptions),
    [modelOptions, selectedAgentProfileForModel],
  )
  const activeAgentModelPolicy = selectedAgentModelPolicy
  const activeAgentModelLocked = Boolean(activeAgentModelPolicy?.locked)
  const activePreferenceRecord = preferenceFromAgentPolicy(activeAgentModelPolicy)
    ?? (sessionId ? sessionPreference : draftPreference)

  const composer = useDesktopUiStore((state) => state.getSessionDraft(sessionId, workspacePath))
  const composerDraftKey = sessionId ?? draftSessionKey
  const setComposerDraft = useCallback((value: string) => {
    setSessionDraft(composerDraftKey, value)
  }, [composerDraftKey, setSessionDraft])
  const slashPalette = useMemo(() => buildDesktopSlashPaletteState(composer), [composer])
  const [slashSelectionIndex, setSlashSelectionIndex] = useState(0)
  const [mentionSelectionIndex, setMentionSelectionIndex] = useState(0)
  const [modelPickerOpenSignal, setModelPickerOpenSignal] = useState(0)
  const [mobileSettingsOpen, setMobileSettingsOpen] = useState(false)
  const [mobileQuickCommandsOpen, setMobileQuickCommandsOpen] = useState(false)
  const mobileSettingsRef = useRef<HTMLDivElement>(null)
  const mobileSettingsTriggerRef = useRef<HTMLButtonElement>(null)
  const mobileQuickCommandsRef = useRef<HTMLDivElement>(null)
  const mobileQuickCommandsTriggerRef = useRef<HTMLButtonElement>(null)

  const clearDictationRestartTimer = useCallback(() => {
    if (dictationRestartTimerRef.current === null) {
      return
    }
    window.clearTimeout(dictationRestartTimerRef.current)
    dictationRestartTimerRef.current = null
  }, [])

  const commitDictationDraft = useCallback((includeInterim = true) => {
    const nextDraft = appendDictationText(
      appendDictationText(dictationBaseDraftRef.current, dictationFinalTranscriptRef.current),
      includeInterim ? dictationInterimTranscriptRef.current : '',
    )
    dictationBaseDraftRef.current = nextDraft
    dictationFinalTranscriptRef.current = ''
    dictationInterimTranscriptRef.current = ''
    setComposerDraft(nextDraft)
    return nextDraft
  }, [setComposerDraft])

  const stopDictation = useCallback((commitDraft = true, acceptLateResults = false) => {
    dictationEnabledRef.current = false
    dictationAcceptLateResultRef.current = acceptLateResults
    dictationManualStopRef.current = true
    dictationStartingRef.current = false
    setDictationEnabled(false)
    setDictationListening(false)
    clearDictationRestartTimer()
    if (commitDraft) {
      commitDictationDraft(true)
    }
    const recognition = recognitionRef.current
    if (!recognition) {
      return
    }
    try {
      recognition.stop()
    } catch {
      try {
        recognition.abort()
      } catch {
        // Browser implementations throw here when recognition is already stopped.
      }
    }
  }, [clearDictationRestartTimer, commitDictationDraft])

  const startDictationRecognition = useCallback((recognition: SpeechRecognitionLike) => {
    if (!dictationEnabledRef.current || !dictationCanRunRef.current || dictationStartingRef.current) {
      return
    }
    dictationStartingRef.current = true
    dictationManualStopRef.current = false
    try {
      recognition.start()
    } catch (error) {
      dictationStartingRef.current = false
      const message = error instanceof Error && error.message.trim()
        ? error.message
        : 'Browser speech recognition could not start.'
      setDictationError(message)
    }
  }, [])

  const handleDictationToggle = useCallback(() => {
    if (dictationEnabledRef.current) {
      stopDictation(true)
      return
    }

    const Recognition = getSpeechRecognitionConstructor()
    if (!Recognition) {
      setDictationSupported(false)
      setDictationError('Browser speech recognition is not available here.')
      return
    }

    setDictationSupported(true)
    setDictationError(null)
    const recognition = new Recognition()
    recognition.continuous = true
    recognition.interimResults = true
    recognition.maxAlternatives = 1
    recognition.lang = typeof navigator !== 'undefined' && navigator.language ? navigator.language : 'en-US'
    recognition.onstart = () => {
      dictationStartingRef.current = false
      setDictationListening(true)
      setDictationError(null)
    }
    recognition.onresult = (event) => {
      if (!dictationEnabledRef.current && !dictationAcceptLateResultRef.current) {
        return
      }
      let interimTranscript = ''
      let finalTranscript = ''
      for (let index = event.resultIndex; index < event.results.length; index += 1) {
        const result = event.results[index]
        const transcript = result?.[0]?.transcript ?? ''
        if (!transcript) {
          continue
        }
        if (result?.isFinal) {
          finalTranscript += transcript
        } else {
          interimTranscript += transcript
        }
      }
      if (finalTranscript) {
        dictationFinalTranscriptRef.current = appendDictationText(dictationFinalTranscriptRef.current, finalTranscript)
      }
      dictationInterimTranscriptRef.current = interimTranscript
      const nextDraft = appendDictationText(
        appendDictationText(dictationBaseDraftRef.current, dictationFinalTranscriptRef.current),
        dictationInterimTranscriptRef.current,
      )
      setComposerDraft(nextDraft)
    }
    recognition.onerror = (event) => {
      dictationStartingRef.current = false
      const error = event.error ?? ''
      if (isSilentSpeechRecognitionError(error, event.message)) {
        setDictationError(null)
        return
      }
      setDictationError(speechRecognitionErrorMessage(error, event.message))
      if (error === 'not-allowed' || error === 'service-not-allowed' || error === 'audio-capture' || error === 'language-not-supported') {
        dictationEnabledRef.current = false
        setDictationEnabled(false)
      }
    }
    recognition.onend = () => {
      dictationStartingRef.current = false
      setDictationListening(false)
      if (!dictationEnabledRef.current || dictationManualStopRef.current || !dictationCanRunRef.current) {
        return
      }
      clearDictationRestartTimer()
      dictationRestartTimerRef.current = window.setTimeout(() => {
        dictationRestartTimerRef.current = null
        if (recognitionRef.current && dictationEnabledRef.current && dictationCanRunRef.current) {
          startDictationRecognition(recognitionRef.current)
        }
      }, DICTATION_RESTART_DELAY_MS)
    }

    recognitionRef.current = recognition
    dictationCanRunRef.current = true
    dictationAcceptLateResultRef.current = false
    dictationBaseDraftRef.current = composer
    dictationFinalTranscriptRef.current = ''
    dictationInterimTranscriptRef.current = ''
    dictationEnabledRef.current = true
    setDictationEnabled(true)
    startDictationRecognition(recognition)
  }, [clearDictationRestartTimer, composer, setComposerDraft, startDictationRecognition, stopDictation])

  useEffect(() => {
    setDictationSupported(getSpeechRecognitionConstructor() !== null)
  }, [])

  useEffect(() => () => {
    stopDictation(false)
  }, [stopDictation])

  useEffect(() => {
    if (!mobileSettingsOpen && !mobileQuickCommandsOpen) return
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node
      if (
        mobileSettingsRef.current?.contains(target) ||
        mobileSettingsTriggerRef.current?.contains(target) ||
        mobileQuickCommandsRef.current?.contains(target) ||
        mobileQuickCommandsTriggerRef.current?.contains(target) ||
        !document.getElementById('root')?.contains(target)
      ) {
        return
      }
      setMobileSettingsOpen(false)
      setMobileQuickCommandsOpen(false)
    }
    const handleEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMobileSettingsOpen(false)
        setMobileQuickCommandsOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleEscape)
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [mobileSettingsOpen, mobileQuickCommandsOpen])

  const serverDefaultRouteId = defaultWorkspaceRouteId(uiSettingsQuery.data, workspacePath)
  const resolvedDefaultRouteId = resolveDesktopChatRouteById(routeOptions, serverDefaultRouteId, defaultChatRoute)?.id ?? defaultChatRoute?.id ?? 'host'

  useEffect(() => {
    setDraftRouteOverrideId(null)
  }, [workspacePath])

  useEffect(() => {
    if (sessionId) {
      setDraftRouteOverrideId(null)
    }
  }, [sessionId])

  useEffect(() => {
    const nextRoute = sessionId && !draftRouteOverrideId
      ? resolveDesktopChatRouteFromSession(liveSession ?? session, routeOptions, defaultChatRoute)
      : resolveDesktopChatRouteById(routeOptions, draftRouteOverrideId || serverDefaultRouteId, defaultChatRoute)
    const nextRouteId = nextRoute?.id ?? 'host'
    if (nextRouteId !== selectedRouteId) {
      setSelectedRouteId(nextRouteId)
    }
  }, [defaultChatRoute, draftRouteOverrideId, liveSession, routeOptions, selectedRouteId, serverDefaultRouteId, session, sessionId, workspacePath])

  const activeChatRoute = useMemo(
    () => routeOptions.find((entry) => entry.id === selectedRouteId) ?? defaultChatRoute,
    [defaultChatRoute, routeOptions, selectedRouteId],
  )
  const showRoutePicker = !hideRouteSelector && routeOptions.length > 1
  const resolvedLockedAgentName = lockedAgentName?.trim() ?? ''
  const resolvedLockedAgentLabel = lockedAgentLabel?.trim() || resolvedLockedAgentName

  const refreshUISettings = useCallback(async () => {
    try {
      const settings = await queryClient.fetchQuery({ ...uiSettingsQueryOptions(), staleTime: 0 })
      setThinkingTagsEnabled(normalizeThinkingTagsEnabled(settings))
      setDefaultNewSessionMode(normalizeDefaultNewSessionMode(settings.chat?.default_new_session_mode))
    } catch {
      setDefaultNewSessionMode('auto')
    }
  }, [queryClient])

  useEffect(() => {
    setSlashSelectionIndex(0)
  }, [slashPalette.query, slashPalette.hasArguments])

  useEffect(() => {
    void refreshUISettings()

    function handleVisibilityOrFocus() {
      if (document.visibilityState === 'hidden') {
        return
      }
      void refreshUISettings()
    }

    window.addEventListener('focus', handleVisibilityOrFocus)
    document.addEventListener('visibilitychange', handleVisibilityOrFocus)
    return () => {
      window.removeEventListener('focus', handleVisibilityOrFocus)
      document.removeEventListener('visibilitychange', handleVisibilityOrFocus)
    }
  }, [refreshUISettings])

  useEffect(() => {
    if (slashPalette.matches.length === 0) {
      if (slashSelectionIndex !== 0) {
        setSlashSelectionIndex(0)
      }
      return
    }
    if (slashSelectionIndex >= slashPalette.matches.length) {
      setSlashSelectionIndex(0)
    }
  }, [slashPalette.matches, slashSelectionIndex])

  useEffect(() => {
    if (!sessionId) {
      lastLoadedMessageTailSessionRef.current = ''
      setMessagesLoading(false)
      return
    }
    if (lastLoadedMessageTailSessionRef.current === sessionId) {
      return
    }
    lastLoadedMessageTailSessionRef.current = sessionId
    const controller = new AbortController()
    setMessagesLoading(true)
    fetchAndApplyDesktopV3SessionMessagesTail(sessionId, { signal: controller.signal })
      .catch((error) => {
        if (controller.signal.aborted) {
          return
        }
        lastLoadedMessageTailSessionRef.current = ''
        setPanelError(error instanceof Error ? error.message : 'Failed to load session messages')
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setMessagesLoading(false)
        }
      })
    return () => {
      controller.abort()
    }
  }, [sessionId])

  const messages = useMemo(() => dedupeMessages(dbMessages), [dbMessages])
  const displayedMessages = useMemo(() => visibleDesktopChatMessages(messages), [messages])
  const liveAssistantDraft = liveSession?.live.assistantDraft ?? ''
  const retainedAssistantSegments = liveSession?.live.retainedAssistantSegments ?? []
  const renderableRetainedAssistantSegments = useMemo(
    () => retainedAssistantSegmentsWithoutCanonicalReplay(retainedAssistantSegments, displayedMessages),
    [displayedMessages, retainedAssistantSegments],
  )
  const liveReasoningItems = useMemo(() => buildLiveReasoningItems(liveSession, displayedMessages), [displayedMessages, liveSession])
  const liveToolMessages = useMemo(() => buildLiveToolMessages(liveSession), [liveSession])
  const renderableLiveToolMessages = useMemo(
    () => liveToolMessages.filter((message) => !hasCanonicalLiveToolReplacement(displayedMessages, message)),
    [displayedMessages, liveToolMessages],
  )
  const shouldRenderLiveToolMessage = renderableLiveToolMessages.length > 0
  const shouldRenderLiveAssistantDraft =
    liveAssistantDraft !== ''
    && !liveAssistantDraftHasCanonicalReplay(liveAssistantDraft, displayedMessages)
    && !renderableLiveToolMessages.some((message) => message.tool.trim().toLowerCase() === 'task')
  const loadingMessages = messagesLoading
  const lifecycle = liveSession?.lifecycle ?? null
  const lifecyclePhase = lifecycle?.phase.trim().toLowerCase() ?? ''
  const lifecycleStopReason = lifecycle?.stopReason?.trim() ?? ''
  const liveRunId = liveSession?.live.runId?.trim() ?? ''
  const {
    activeRunIntent,
    durableRunActive,
    resumableRunId,
    reconnectingRun,
    submitting,
    canStop,
    showRunTimer,
    runTimerLabel,
    composerDisabled,
    runActive,
  } = deriveDesktopChatRunControls(dbActiveRun, { liveSummary: liveSession?.live.summary ?? null, timerNow })
  const liveAssistantDraftKey = `live-assistant:${activeRunIntent?.runId || liveRunId || 'draft'}`
  const savedRuleCountdown = savedRuleCountdownSeconds(lastSavedRuleExpiresAt, savedRuleCountdownNow)
  const terminalUserStopSummary = !durableRunActive && (lifecyclePhase === 'cancelled' || lifecyclePhase === 'canceled')
    ? (lifecycleStopReason || liveSession?.live.summary || 'Stream cancelled by user.')
    : !runActive && liveSession?.live.summary?.trim() === 'Run paused by user'
      ? 'Run paused by user'
      : ''
  useEffect(() => {
    const probe = existingSessionStreamProbeRef.current
    if (!probe || probe.emitted || probe.sessionId !== sessionId || runActive) {
      return
    }
    const newAssistant = newAssistantMessages(displayedMessages, probe.baselineAssistantIds)
    const cacheSession = liveSession ?? null
    const cacheUpdated = Boolean(
      cacheSession
      && (
        (cacheSession.lastEventSeq ?? 0) > probe.baselineLastEventSeq
        || displayedMessages.some((message) => !probe.baselineMessageIds.has(message.id))
        || newAssistant.length > 0
      ),
    )
    const finalAssistantText = newAssistant.map((message) => message.content).join('\n\n')
      || (assistantScreenText(displayedMessages, liveAssistantDraft, retainedAssistantSegments).startsWith(probe.baselineAssistantContent)
        ? assistantScreenText(displayedMessages, liveAssistantDraft, retainedAssistantSegments).slice(probe.baselineAssistantContent.length).trim()
        : assistantScreenText(displayedMessages, liveAssistantDraft, retainedAssistantSegments))
    if (!cacheUpdated && finalAssistantText.trim() === '') {
      return
    }
    probe.emitted = true
    existingSessionStreamProbeRef.current = null
    const completionDiagnostics = getDesktopV3RealtimeDiagnostics(probe.sessionId)
    const subscription = completionDiagnostics?.subscriptions[0] ?? null
    console.info('[existing-session-stream-complete]', {
      sessionId: probe.sessionId,
      clientRequestId: probe.clientRequestId,
      submittedAt: probe.submittedAt,
      completedAt: Date.now(),
      reconnect: {
        submit: probe.submitDiagnostics,
        completion: completionDiagnostics,
        socketState: completionDiagnostics?.socketState ?? 'none',
        subscribed: Boolean(subscription && subscription.subscribeSentAt >= probe.submittedAt),
        subscribeSentAt: subscription?.subscribeSentAt ?? 0,
        subscribeSentCount: subscription?.subscribeSentCount ?? 0,
        lastFrameKind: subscription?.lastFrameKind ?? '',
        lastEventType: subscription?.lastEventType ?? '',
        lastEventAt: subscription?.lastEventAt ?? 0,
        replayStartedAt: subscription?.lastReplayStartedAt ?? 0,
        replayCompleteAt: subscription?.lastReplayCompleteAt ?? 0,
        endpointCursorPresent: subscription?.endpointCursorPresent ?? false,
        lastEndpointCursorPresent: subscription?.lastEndpointCursorPresent ?? false,
      },
      streamedAssistantText: finalAssistantText,
      screenAssistantText: assistantScreenText(displayedMessages, liveAssistantDraft, retainedAssistantSegments),
      cache: {
        updated: cacheUpdated,
        source: 'v3-runtime-desktop-state',
        sessionPresent: cacheSession !== null,
        messageCount: displayedMessages.length,
        assistantMessageIds: displayedMessages.filter((message) => message.role === 'assistant').map((message) => message.id),
        newAssistantMessageIds: newAssistant.map((message) => message.id),
        lastEventSeqBefore: probe.baselineLastEventSeq,
        lastEventSeqAfter: cacheSession?.lastEventSeq ?? 0,
        runIdBefore: probe.baselineRunId,
        runIdAfter: liveRunId,
        liveDraftLength: liveAssistantDraft.length,
        retainedAssistantSegmentCount: retainedAssistantSegments.length,
      },
    })
  }, [displayedMessages, liveAssistantDraft, liveRunId, liveSession, retainedAssistantSegments, runActive, sessionId])
  const showDictationButton = !runActive && dictationSupported
  const dictationButtonDisabled = composerDisabled
  const dictationComposer = dictationEnabled
    ? appendDictationText(appendDictationText(dictationBaseDraftRef.current, dictationFinalTranscriptRef.current), dictationInterimTranscriptRef.current)
    : composer
  useEffect(() => {
    const textarea = composerTextareaRef.current
    if (!textarea) {
      return
    }
    textarea.style.height = '40px'
    if (dictationComposer.trim() === '') {
      return
    }
    const nextHeight = Math.min(Math.max(textarea.scrollHeight, 40), 112)
    textarea.style.height = `${nextHeight}px`
  }, [dictationComposer])
  useEffect(() => {
    dictationCanRunRef.current = showDictationButton && !composerDisabled
    if (!dictationCanRunRef.current && dictationEnabledRef.current) {
      stopDictation(true)
    }
  }, [composerDisabled, showDictationButton, stopDictation])

  useEffect(() => {
    for (let index = renderableLiveToolMessages.length - 1; index >= 0; index -= 1) {
      const sidebarState = imageSidebarStateFromToolMessage(renderableLiveToolMessages[index])
      if (sidebarState) {
        setImageSidebar((current) => ({
          ...sidebarState,
          workspacePath: current?.threadId === sidebarState.threadId ? current.workspacePath : workspacePath,
          workspaceName: current?.threadId === sidebarState.threadId ? current.workspaceName : workspaceName,
        }))
        return
      }
    }
  }, [renderableLiveToolMessages, workspaceName, workspacePath])

  useEffect(() => {
    for (let index = displayedMessages.length - 1; index >= 0; index -= 1) {
      const sidebarState = imageSidebarStateFromToolMessage(displayedMessages[index]?.toolMessage)
      if (sidebarState) {
        setImageSidebar((current) => current?.threadId === sidebarState.threadId && current.open
          ? { ...current, ...sidebarState, workspacePath: current.workspacePath || workspacePath, workspaceName: current.workspaceName || workspaceName }
          : { ...sidebarState, workspacePath, workspaceName })
        return
      }
    }
  }, [displayedMessages, workspaceName, workspacePath])

  const contextBadgeLabel = formatContextUsageBadgeLabel(liveSession?.usage ?? null)
  const contextBadgeTooltip = formatContextUsageTooltip(liveSession?.usage ?? null)
  const agentTodoSummary = metadataTodoSummary(metadataRecord(liveSession?.metadata))
  const agentTodoBadgeLabel = formatAgentTodoBadge(agentTodoSummary)
  const mobileAgentTodoBadgeLabel = formatMobileAgentTodoBadge(agentTodoSummary)
  const renderItems = useMemo(() => {
    const handoff = liveAssistantHandoffRef.current
    const lastDisplayedMessage = displayedMessages[displayedMessages.length - 1]
    const handoffMessageId = handoff
      && handoff.sessionId === sessionId
      && !shouldRenderLiveAssistantDraft
      && lastDisplayedMessage?.role === 'assistant'
      && lastDisplayedMessage.content === handoff.content
      ? lastDisplayedMessage.id
      : ''
    const items: RenderItem[] = displayedMessages.map((message) => ({
      type: 'message',
      message,
      virtualKey: message.id === handoffMessageId ? handoff?.key : undefined,
    }))
    for (const segment of renderableRetainedAssistantSegments) {
      items.push({ type: 'live-assistant', id: segment.id, content: segment.content, timelineSeq: segment.seq })
    }
    for (const liveReasoningItem of liveReasoningItems) {
      items.push(liveReasoningItem)
    }
    if (shouldRenderLiveToolMessage) {
      for (const liveToolMessage of renderableLiveToolMessages) {
        items.push({ type: 'live-tool', toolMessage: liveToolMessage })
      }
    }
    if (shouldRenderLiveAssistantDraft) {
      items.push({ type: 'live-assistant', id: liveAssistantDraftKey, content: liveAssistantDraft })
    }
    return orderDesktopTimelineItems(items)
  }, [displayedMessages, liveAssistantDraft, liveAssistantDraftKey, liveReasoningItems, renderableLiveToolMessages, renderableRetainedAssistantSegments, sessionId, shouldRenderLiveAssistantDraft, shouldRenderLiveToolMessage])
  const thinkingTagsMeasurementKey = desktopChatThinkingTagsMeasurementKey(thinkingTagsEnabled)
  const renderMeasurementKey = useMemo(
    () => [thinkingTagsMeasurementKey, ...renderItems.map((item) => {
      switch (item.type) {
        case 'message':
          return item.virtualKey
            ? `la:${item.message.content.length}`
            : `m:${item.message.id}:${item.message.content.length}`
        case 'live-tool':
          return `lt:${item.toolMessage.toolInstanceId || item.toolMessage.callId || item.toolMessage.tool}:${item.toolMessage.output.length}:${item.toolMessage.completedOutput.length}:${item.toolMessage.taskRows.length}`
        case 'live-assistant':
          return `la:${item.content.length}`
        case 'live-reasoning':
          return `lr:${item.id}:${item.text.length}:${item.summary.length}:${item.state}`
        default:
          return 'unknown'
      }
    })].join('|'),
    [renderItems, thinkingTagsMeasurementKey],
  )
  const rowVirtualizer = useVirtualizer({
    count: renderItems.length,
    getScrollElement: () => scrollerRef.current,
    estimateSize: (index) => estimateRenderItemSize(renderItems[index], thinkingTagsEnabled),
    getItemKey: (index) => desktopChatVirtualItemKey(renderItemKey(renderItems[index], index), thinkingTagsEnabled),
    overscan: 6,
  })
  rowVirtualizer.shouldAdjustScrollPositionOnItemSizeChange = (item, _delta, instance) => {
    if (shouldStickToBottomRef.current) {
      return true
    }
    return item.start < (instance.scrollOffset ?? 0) && instance.scrollDirection === 'backward'
  }
  const virtualItems = rowVirtualizer.getVirtualItems()

  const selectableAgents = useMemo(
    () => agentState.profiles.filter((profile) => profile.name.trim() !== ''),
    [agentState.profiles],
  )
  const mentionSubagents = useMemo(
    () => normalizeMentionSubagents(selectableAgents
      .filter((profile) => profile.enabled && profile.mode === 'subagent')
      .map((profile) => profile.name)),
    [selectableAgents],
  )
  const mentionPaletteIsActive = useMemo(() => mentionPaletteActive(composer, mentionSubagents), [composer, mentionSubagents])
  const mentionPaletteMatches = useMemo(
    () => mentionPaletteIsActive ? chatMentionCandidates(mentionPaletteQuery(composer), mentionSubagents) : [],
    [composer, mentionPaletteIsActive, mentionSubagents],
  )

  useEffect(() => {
    if (mentionPaletteMatches.length === 0) {
      if (mentionSelectionIndex !== 0) {
        setMentionSelectionIndex(0)
      }
      return
    }
    if (mentionSelectionIndex >= mentionPaletteMatches.length) {
      setMentionSelectionIndex(0)
    }
  }, [mentionPaletteMatches, mentionSelectionIndex])

  const resolvedSessionAgent = useMemo(
    () => resolveSessionEffectiveAgentName(liveSession ?? session, agentState.activePrimary),
    [agentState.activePrimary, liveSession, session],
  )
  const isFlowSession = useMemo(
    () => sessionUsesReadOnlyFlowIdentity(liveSession ?? session),
    [liveSession, session],
  )
  const selectedPrimaryAgentProfile = useMemo(
    () => selectableAgents.find((profile) => profile.name === selectedPrimaryAgent) ?? null,
    [selectableAgents, selectedPrimaryAgent],
  )
  const currentPrimaryAgentProfile = useMemo(
    () => selectableAgents.find((profile) => profile.name === agentState.activePrimary.trim()) ?? null,
    [agentState.activePrimary, selectableAgents],
  )
  const activeModeSourceProfile = isFlowSession ? null : (selectedPrimaryAgentProfile ?? currentPrimaryAgentProfile)
  const agentDerivedMode = useMemo(
    () => activeModeSourceProfile
      ? desiredSessionModeForAgent(activeModeSourceProfile, liveSession?.mode ?? session?.mode ?? draftSessionMode ?? 'plan')
      : normalizeSessionMode(liveSession?.mode ?? session?.mode ?? draftSessionMode ?? 'plan'),
    [activeModeSourceProfile, draftSessionMode, liveSession?.mode, session?.mode],
  )
  const sessionMode = sessionId
    ? normalizeSessionMode(liveSession?.mode ?? session?.mode ?? agentDerivedMode)
    : normalizeSessionMode(draftSessionMode || defaultNewSessionMode || agentDerivedMode)
  const effectiveSessionMode = selectedPrimaryAgentProfile?.exitPlanModeEnabled
    ? sessionMode
    : agentDerivedMode
  const selectedExecutionSettingLabel = executionSettingLabel(selectedPrimaryAgentProfile)
  const resolvedModelOptions = useMemo(() => modelOptions, [modelOptions])

  const selectedModelKey = activePreferenceRecord.preference.provider && activePreferenceRecord.preference.model
    ? optionKey(
        activePreferenceRecord.preference.provider,
        activePreferenceRecord.preference.model,
        activePreferenceRecord.preference.contextMode,
      )
    : ''
  const selectedModelOption = modelOptionForPreference(resolvedModelOptions, activePreferenceRecord.preference)
  const selectedModelAvailable = selectedModelKey !== '' && selectedModelOption !== null
  const selectedContextWindow = useMemo(
    () => effectiveContextWindow(
      activePreferenceRecord.preference.provider,
      activePreferenceRecord.preference.model,
      activePreferenceRecord.preference.contextMode,
      resolvedModelOptions.find((option) => option.key === selectedModelKey)?.contextWindow ?? activePreferenceRecord.contextWindow,
    ),
    [
      activePreferenceRecord.contextWindow,
      activePreferenceRecord.preference.contextMode,
      activePreferenceRecord.preference.model,
      activePreferenceRecord.preference.provider,
      resolvedModelOptions,
      selectedModelKey,
    ],
  )
  const normalizedThinking = normalizeThinkingValue(activePreferenceRecord.preference.thinking)
  const canSendWithSelectedPreference = hasExplicitPreference(activePreferenceRecord.preference) && selectedModelAvailable
  const fastSupported = supportsCodexFastMode(activePreferenceRecord.preference.provider, activePreferenceRecord.preference.model)
  const fastValue = fastToggleFromPreference(activePreferenceRecord.preference)
  const agentModelLockReason = activeAgentModelPolicy?.reason || agentModelLockedMessage(activeAgentModelPolicy?.agentName || currentSessionAgent)

  const actionablePendingPermissions = useMemo(
    () => (liveSession?.pendingPermissions ?? []).filter((permission) => {
      if (permission.status !== 'pending') {
        return false
      }
      if (resolvingPermissionIds.has(permission.id)) {
        return false
      }
      return permissionRequiresApproval(permission, liveSession?.mode ?? session?.mode ?? effectiveSessionMode)
    }),
    [effectiveSessionMode, liveSession?.mode, liveSession?.pendingPermissions, resolvingPermissionIds, session?.mode],
  )
  const activePermission = useMemo(
    () => (liveSession?.permissionsHydrated ? actionablePendingPermissions[0] ?? null : null),
    [actionablePendingPermissions, liveSession?.permissionsHydrated],
  )
  const pendingPermissionCount = actionablePendingPermissions.length
  const updateScrollLockReturnButton = useCallback(() => {
    const scroller = scrollerRef.current
    if (!scroller) {
      setShowScrollLockReturnButton(false)
      return
    }
    setShowScrollLockReturnButton(!shouldStickToBottomRef.current && shouldShowScrollLockReturnButton(scroller))
  }, [])

  const persistScrollState = useCallback(() => {
    const scroller = scrollerRef.current
    if (!scroller) {
      return
    }
    shouldStickToBottomRef.current = nearBottom(scroller)
    setShowScrollLockReturnButton(!shouldStickToBottomRef.current && shouldShowScrollLockReturnButton(scroller))
  }, [])

  const scrollToLatest = useCallback((attempts = 3) => {
    if (scrollToLatestFrameRef.current !== null) {
      window.cancelAnimationFrame(scrollToLatestFrameRef.current)
      scrollToLatestFrameRef.current = null
    }
    if (renderItems.length === 0) {
      return
    }
    const targetIndex = renderItems.length - 1
    const run = (remainingAttempts: number) => {
      rowVirtualizer.scrollToIndex(targetIndex, { align: 'end' })
      if (remainingAttempts <= 1) {
        scrollToLatestFrameRef.current = null
        return
      }
      scrollToLatestFrameRef.current = window.requestAnimationFrame(() => {
        run(remainingAttempts - 1)
      })
    }
    scrollToLatestFrameRef.current = window.requestAnimationFrame(() => {
      run(Math.max(1, attempts))
    })
  }, [renderItems.length, rowVirtualizer])

  const pinToLatest = useCallback(() => {
    shouldStickToBottomRef.current = true
    setShowScrollLockReturnButton(false)
    scrollToLatest()
  }, [scrollToLatest])

  useEffect(() => {
    if (isFlowSession || resolvedLockedAgentName) {
      return
    }
    if (selectableAgents.length === 0) {
      if (selectedPrimaryAgent !== 'swarm') {
        setSelectedPrimaryAgent('swarm')
      }
      return
    }
    if (!sessionId) {
      if (selectableAgents.some((profile) => profile.name === selectedPrimaryAgent)) {
        return
      }
      const nextSelectedAgent = agentState.activePrimary || selectableAgents[0].name || 'swarm'
      if (nextSelectedAgent !== selectedPrimaryAgent) {
        setSelectedPrimaryAgent(nextSelectedAgent)
      }
      return
    }
    const effectiveAgent = resolveSessionEffectiveAgentName(liveSession ?? session, agentState.activePrimary)
    if (selectableAgents.some((profile) => profile.name === effectiveAgent)) {
      if (effectiveAgent !== selectedPrimaryAgent) {
        setSelectedPrimaryAgent(effectiveAgent)
      }
      return
    }
    if (selectableAgents.some((profile) => profile.name === selectedPrimaryAgent)) {
      return
    }
    const nextSelectedAgent = agentState.activePrimary || selectableAgents[0].name || 'swarm'
    if (nextSelectedAgent !== selectedPrimaryAgent) {
      setSelectedPrimaryAgent(nextSelectedAgent)
    }
  }, [agentState.activePrimary, isFlowSession, liveSession, resolvedLockedAgentName, selectableAgents, selectedPrimaryAgent, session, sessionId])

  useEffect(() => {
    if (!sessionId) {
      return
    }
    if (resolvedLockedAgentName) {
      setCurrentSessionAgent(resolvedLockedAgentName)
      return
    }
    if (isFlowSession) {
      setCurrentSessionAgent(resolvedSessionAgent || 'flow')
      return
    }
    const nextAgent = selectableAgents.some((profile) => profile.name === resolvedSessionAgent)
      ? resolvedSessionAgent
      : agentState.activePrimary.trim() || selectableAgents[0]?.name || 'swarm'
    setCurrentSessionAgent(nextAgent)
  }, [agentState.activePrimary, isFlowSession, resolvedLockedAgentName, selectableAgents, resolvedSessionAgent, sessionId])

  useEffect(() => {
    if (sessionId) {
      return
    }
    setCurrentSessionAgent(resolvedLockedAgentName || selectedPrimaryAgent)
  }, [resolvedLockedAgentName, selectedPrimaryAgent, sessionId])

  useEffect(() => {
    if (isFlowSession || !activeModeSourceProfile) {
      return
    }
    const nextMode = desiredSessionModeForAgent(activeModeSourceProfile, liveSession?.mode ?? session?.mode ?? draftSessionMode)
    if (sessionId) {
      const currentMode = normalizeSessionMode(liveSession?.mode ?? session?.mode ?? nextMode)
      if (currentMode === nextMode) {
        return
      }
      if (activeModeSourceProfile.exitPlanModeEnabled) {
        if (currentMode === 'plan' || currentMode === 'auto') {
          return
        }
      }
      return
    }
    if (normalizeSessionMode(draftSessionMode) === nextMode) {
      return
    }
    if (activeModeSourceProfile.exitPlanModeEnabled) {
      const currentDraftMode = normalizeSessionMode(draftSessionMode)
      if (currentDraftMode === 'plan' || currentDraftMode === 'auto') {
        return
      }
    }
    setSessionDraftMode(draftSessionKey, nextMode)
  }, [activeModeSourceProfile, draftSessionKey, draftSessionMode, isFlowSession, liveSession?.mode, session?.mode, sessionId, setSessionDraftMode])

  useEffect(() => {
    if (!sessionId || isFlowSession || !activeModeSourceProfile) {
      lastAutoModeSyncRef.current = ''
      return
    }
    const nextMode = desiredSessionModeForAgent(activeModeSourceProfile, liveSession?.mode ?? session?.mode ?? draftSessionMode)
    const persistedMode = normalizeSessionMode(liveSession?.mode ?? session?.mode ?? nextMode)
    if (persistedMode === nextMode) {
      lastAutoModeSyncRef.current = ''
      return
    }
    const syncKey = `${sessionId}:${activeModeSourceProfile.name}:${persistedMode}->${nextMode}`
    if (lastAutoModeSyncRef.current === syncKey) {
      return
    }
    lastAutoModeSyncRef.current = syncKey
    void updateDesktopV3SessionMode(sessionId, nextMode).then(() => {
      lastAutoModeSyncRef.current = ''
    }).catch((error) => {
      lastAutoModeSyncRef.current = ''
      setPanelError(error instanceof Error ? error.message : 'Failed to update session mode')
    })
  }, [activeModeSourceProfile, draftSessionMode, isFlowSession, liveSession?.mode, queryClient, session?.mode, sessionId])

  const handleAgentSelect = useCallback(async (value: string) => {
    if (isFlowSession || resolvedLockedAgentName) {
      return
    }
    const nextAgent = value.trim() || 'swarm'
    const previousAgent = currentSessionAgent
    const previousSelectedAgent = selectedPrimaryAgent
    setPanelError(null)
    if (!sessionId) {
      setSelectedPrimaryAgent(nextAgent)
      setCurrentSessionAgent(nextAgent)
      return
    }
    setSelectedPrimaryAgent(nextAgent)
    try {
      const snapshot = await updateDesktopV3SessionAgent(sessionId, nextAgent)
      const serverAgent = resolveSessionEffectiveAgentName(snapshot.session, agentState.activePrimary)
      setSelectedPrimaryAgent(serverAgent)
      setCurrentSessionAgent(serverAgent)
    } catch (error) {
      setSelectedPrimaryAgent(previousSelectedAgent)
      setCurrentSessionAgent(previousAgent)
      setPanelError(error instanceof Error ? error.message : 'Failed to update session agent')
    }
  }, [agentState.activePrimary, currentSessionAgent, isFlowSession, queryClient, resolvedLockedAgentName, selectedPrimaryAgent, sessionId])

  useEffect(() => {
    setPanelError(null)
  }, [sessionId])

  useEffect(() => {
    if (!sessionId) {
      return
    }
    if (liveSession?.sessionApi?.trim().toLowerCase() === 'v3') {
      void ensureRunStream(sessionId, resumableRunId || null)
      return
    }
    if (!resumableRunId) {
      return
    }
    if (!durableRunActive && !reconnectingRun) {
      return
    }
    void ensureRunStream(sessionId, resumableRunId)
  }, [durableRunActive, ensureRunStream, liveSession?.sessionApi, reconnectingRun, resumableRunId, sessionId])

  useEffect(() => {
    if (!commitModal.targetSessionId || commitModal.status !== 'running') {
      return
    }
    const trackedSession = trackedCommitSession
    if (!trackedSession) {
      return
    }
    const liveStatus = trackedSession.live.status
    if (trackedCommitActiveRun) {
      return
    }
    setCommitModal((current) => {
      if (current.targetSessionId !== commitModal.targetSessionId || current.status !== 'running') {
        return current
      }
      if (liveStatus === 'error') {
        return {
          ...current,
          status: 'error',
          error: trackedSession.live.error || 'Save failed.',
        }
      }
      return {
        ...current,
        status: 'success',
        error: null,
      }
    })
  }, [commitModal.status, commitModal.targetSessionId, trackedCommitActiveRun, trackedCommitSession])

  useEffect(() => {
    if (sessionId && shouldRenderLiveAssistantDraft && liveAssistantDraft !== '') {
      liveAssistantHandoffRef.current = { sessionId, content: liveAssistantDraft, key: liveAssistantDraftKey }
    } else if (!sessionId) {
      liveAssistantHandoffRef.current = null
    }
  }, [liveAssistantDraft, liveAssistantDraftKey, sessionId, shouldRenderLiveAssistantDraft])

  useEffect(() => {
    if (shouldStickToBottomRef.current) {
      setShowScrollLockReturnButton(false)
      scrollToLatest()
      return
    }
    updateScrollLockReturnButton()
  }, [messages, renderMeasurementKey, scrollToLatest, sessionId, updateScrollLockReturnButton])

  useEffect(() => {
    const handleResize = () => updateScrollLockReturnButton()
    window.addEventListener('resize', handleResize)
    return () => window.removeEventListener('resize', handleResize)
  }, [updateScrollLockReturnButton])

  useEffect(() => {
    return () => {
      if (scrollToLatestFrameRef.current !== null) {
        window.cancelAnimationFrame(scrollToLatestFrameRef.current)
        scrollToLatestFrameRef.current = null
      }
    }
  }, [])

  useEffect(() => {
    if (!showRunTimer) {
      return
    }
    setTimerNow(Date.now())
    const intervalID = window.setInterval(() => setTimerNow(Date.now()), 100)
    return () => window.clearInterval(intervalID)
  }, [showRunTimer])

  useEffect(() => {
    if (!lastSavedRulePreview || !lastSavedRuleExpiresAt) {
      return
    }
    setSavedRuleCountdownNow(Date.now())
    const intervalID = window.setInterval(() => setSavedRuleCountdownNow(Date.now()), 250)
    const timeoutID = window.setTimeout(() => {
      setLastSavedRulePreview(null)
      setLastSavedRuleExpiresAt(null)
    }, Math.max(0, lastSavedRuleExpiresAt - Date.now()))
    return () => {
      window.clearInterval(intervalID)
      window.clearTimeout(timeoutID)
    }
  }, [lastSavedRuleExpiresAt, lastSavedRulePreview])

  useLayoutEffect(() => {
    rowVirtualizer.measure()
  }, [rowVirtualizer, thinkingTagsEnabled])

  const handlePreferenceChange = useCallback(async (next: ResolvedSessionPreference['preference']) => {
    setPanelError(null)
    if (activeAgentModelLocked) {
      setPanelError(activeAgentModelPolicy?.reason || agentModelLockedMessage(activeAgentModelPolicy?.agentName || currentSessionAgent))
      return
    }
    const normalizedNext = {
      ...next,
      thinking: normalizeThinkingValue(next.thinking),
    }
    if (!sessionId) {
      queryClient.setQueryData(draftModelQueryOptions().queryKey, (current: ResolvedSessionPreference | undefined) => ({
        ...(current ?? emptyPreference()),
        preference: {
          ...(current?.preference ?? emptyPreference().preference),
          ...normalizedNext,
        },
      }))
      if (!hasExplicitPreference(normalizedNext)) {
        return
      }
      try {
        const resolved = await updateDraftModelPreference(normalizedNext)
        queryClient.setQueryData(draftModelQueryOptions().queryKey, resolved)
      } catch (error) {
        setPanelError(error instanceof Error ? error.message : 'Failed to update draft model settings')
      }
      return
    }
    if (!hasExplicitPreference(normalizedNext)) {
      setPanelError('Session model and thinking are required')
      return
    }

    try {
      await updateDesktopV3SessionPreference(sessionId, normalizedNext)
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to update model settings')
    }
  }, [activeAgentModelLocked, activeAgentModelPolicy?.agentName, activeAgentModelPolicy?.reason, activePreferenceRecord, currentSessionAgent, queryClient, sessionId])

  const handleModelChange = useCallback((value: string) => {
    if (activeAgentModelLocked) {
      setPanelError(activeAgentModelPolicy?.reason || agentModelLockedMessage(activeAgentModelPolicy?.agentName || currentSessionAgent))
      return
    }
    const option = resolvedModelOptions.find((entry) => entry.key === value)
    const nextProvider = option?.provider ?? ''
    const next = {
      ...activePreferenceRecord.preference,
      provider: nextProvider,
      model: option?.model ?? '',
      contextMode: option?.contextMode ?? '',
      thinking: activePreferenceRecord.preference.thinking || option?.thinking || defaultThinkingForProvider(nextProvider),
    }
    void handlePreferenceChange(next)
  }, [activeAgentModelLocked, activeAgentModelPolicy?.agentName, activeAgentModelPolicy?.reason, activePreferenceRecord.preference, currentSessionAgent, handlePreferenceChange, resolvedModelOptions])

  const handleThinkingChange = useCallback((value: string) => {
    if (activeAgentModelLocked) {
      setPanelError(activeAgentModelPolicy?.reason || agentModelLockedMessage(activeAgentModelPolicy?.agentName || currentSessionAgent))
      return
    }
    void handlePreferenceChange({
      ...activePreferenceRecord.preference,
      thinking: value,
    })
  }, [activeAgentModelLocked, activeAgentModelPolicy?.agentName, activeAgentModelPolicy?.reason, activePreferenceRecord.preference, currentSessionAgent, handlePreferenceChange])

  const handleFastChange = useCallback((value: string) => {
    if (activeAgentModelLocked) {
      setPanelError(activeAgentModelPolicy?.reason || agentModelLockedMessage(activeAgentModelPolicy?.agentName || currentSessionAgent))
      return
    }
    const nextFast = normalizeFastToggle(value)
    void handlePreferenceChange(buildFastPreference(activePreferenceRecord.preference, nextFast))
  }, [activeAgentModelLocked, activeAgentModelPolicy?.agentName, activeAgentModelPolicy?.reason, activePreferenceRecord.preference, currentSessionAgent, handlePreferenceChange])

  const handleCompact = useCallback(async (rawInput = '') => {
    if (!sessionId || canStop || submitting) {
      return
    }
    setPanelError(null)
    try {
      const parsed = parseCompactCommandInput(rawInput)
      const liveSessionMetadata = metadataRecord(liveSession?.metadata)
      const currentMetadata: Record<string, unknown> = liveSessionMetadata
        ? { ...liveSessionMetadata }
        : {}
      if (parsed.hasThreshold) {
        if (parsed.thresholdPercent > 0) {
          currentMetadata[COMPACT_THRESHOLD_METADATA_KEY] = parsed.thresholdPercent
        } else {
          delete currentMetadata[COMPACT_THRESHOLD_METADATA_KEY]
        }
        await updateDesktopV3SessionMetadata(sessionId, currentMetadata)
      }
      await submitPrompt({
        sessionId,
        sessionApi: liveSession?.sessionApi,
        workspacePath,
        workspaceName,
        prompt: parsed.note,
        agentName: resolvedLockedAgentName || currentSessionAgent,
        compact: true,
      })
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to compact context')
    }
  }, [canStop, currentSessionAgent, liveSession?.metadata, queryClient, resolvedLockedAgentName, sessionId, submitPrompt, submitting, workspaceName, workspacePath])

  const openCommitModal = useCallback(() => {
    setCommitModal((current) => ({
      ...current,
      open: true,
      error: null,
      status: current.status === 'running' || current.status === 'starting' ? current.status : 'idle',
      runId: current.status === 'running' || current.status === 'starting' ? current.runId : null,
      targetSessionId: current.status === 'running' || current.status === 'starting' ? current.targetSessionId : null,
    }))
  }, [])

  const handleMobileQuickCommand = useCallback((command: 'new-session' | 'change-workspace' | 'save' | 'chats') => {
    setMobileSettingsOpen(false)
    setMobileQuickCommandsOpen(false)
    switch (command) {
      case 'new-session':
        onStartNewSession(workspacePath, workspaceName)
        return
      case 'change-workspace':
        onOpenWorkspaceLauncher()
        return
      case 'save':
        openCommitModal()
        return
      case 'chats':
        onOpenSidebarMenu()
        return
      default:
        return
    }
  }, [onOpenSidebarMenu, onOpenWorkspaceLauncher, onStartNewSession, openCommitModal, workspaceName, workspacePath])

  const openPlanModal = useCallback(async () => {
    if (!sessionId) {
      setPlanModal({
        open: true,
        loading: false,
        historyLoading: false,
        saving: false,
        error: 'Open or create a session before using /plan.',
        hasActive: false,
        plan: null,
        revisions: [],
      })
      return
    }
    const visiblePlan = dbPlan?.hasActivePlan && dbPlan.plan
      ? dbPlan.plan
      : {
          id: '',
          title: 'Current Plan',
          plan: '',
          document: null,
          status: 'draft',
          approvalState: '',
          updatedAt: 0,
        }
    setPanelError(null)
    setPlanModal({
      open: true,
      loading: true,
      historyLoading: false,
      saving: false,
      error: null,
      hasActive: Boolean(dbPlan?.hasActivePlan),
      plan: visiblePlan,
      revisions: dbPlanRevisions,
    })
    try {
      const snapshot = await fetchAndApplyDesktopV3PlanSnapshot(sessionId)
      if (!snapshot) {
        setPlanModal((current) => ({ ...current, loading: false }))
        return
      }
      setPlanModal({
        open: true,
        loading: false,
        historyLoading: false,
        saving: false,
        error: null,
        hasActive: Boolean(snapshot.hasActivePlan),
        plan: snapshot.activePlan ?? visiblePlan,
        revisions: snapshot.planRevisions,
      })
    } catch (error) {
      setPlanModal((current) => ({
        ...current,
        loading: false,
        error: error instanceof Error ? error.message : 'Failed to load current plan',
      }))
    }
  }, [dbPlan, dbPlanRevisions, sessionId])

  const handleThinkingTagsToggle = useCallback(async (enabled: boolean) => {
    if (thinkingTagsSaving) {
      return
    }
    const previous = thinkingTagsEnabled
    setThinkingTagsEnabled(enabled)
    setThinkingTagsSaving(true)
    setPanelError(null)
    try {
      const updated = await saveThinkingTagsSetting(enabled)
      queryClient.setQueryData(uiSettingsQueryKey(), updated)
      setThinkingTagsEnabled(normalizeThinkingTagsEnabled(updated))
    } catch (error) {
      setThinkingTagsEnabled(previous)
      setPanelError(error instanceof Error ? error.message : 'Failed to update thinking tags setting')
    } finally {
      setThinkingTagsSaving(false)
    }
  }, [queryClient, thinkingTagsEnabled, thinkingTagsSaving])

  const handleSlashSelect = useCallback((command: DesktopSlashCommand) => {
    setSlashSelectionIndex(0)
    if (command.state !== 'ready') {
      return
    }
    switch (command.action.kind) {
      case 'open-settings':
        onOpenSettingsTab(command.action.tab)
        setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        return
      case 'open-quick-settings':
        onOpenQuickSettings(command.action.tab)
        setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        return
      case 'open-permissions':
        onOpenPermissions()
        setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        return
      case 'open-workspace-launcher':
        onOpenWorkspaceLauncher()
        setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        return
      case 'open-model-picker':
        setModelPickerOpenSignal((current) => current + 1)
        setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        return
      case 'toggle-fast':
        if (supportsCodexFastMode(activePreferenceRecord.preference.provider, activePreferenceRecord.preference.model)) {
          const nextFast = fastToggleFromPreference(activePreferenceRecord.preference) === 'on' ? 'off' : 'on'
          void handlePreferenceChange(buildFastPreference(activePreferenceRecord.preference, nextFast))
          setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        } else {
          setPanelError('/fast is available on Codex gpt-5.4/gpt-5.5')
        }
        return
      case 'open-commit-modal':
        openCommitModal()
        setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        return
      case 'open-plan-modal':
        void openPlanModal()
        setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        return
      case 'compact-session':
        if (sessionId && !canStop && !submitting) {
          void handleCompact(composer)
          setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        }
        return
      case 'new-session':
        onStartNewSession(workspacePath, workspaceName)
        setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, '')
        return
      case 'show-help':
        setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, `${command.command} `)
        return
      default:
        return
    }
  }, [activePreferenceRecord.preference, canStop, composer, handleCompact, handlePreferenceChange, onOpenPermissions, onOpenQuickSettings, onOpenSettingsTab, onOpenWorkspaceLauncher, onStartNewSession, openCommitModal, openPlanModal, sessionId, setSessionDraft, submitting, workspaceName, workspacePath])

  const handleSlashInsert = useCallback((command: DesktopSlashCommand) => {
    setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, `${command.command} `)
    setSlashSelectionIndex(0)
  }, [sessionId, setSessionDraft, workspacePath])

  const handleRouteChange = useCallback((routeId: string) => {
    const nextRoute = routeOptions.find((entry) => entry.id === routeId)
    if (!nextRoute) {
      return
    }
    setDraftRouteOverrideId(nextRoute.id)
    setSelectedRouteId(nextRoute.id)
    if (!sessionId) {
      return
    }
    const currentSessionRoute = resolveDesktopChatRouteFromSession(liveSession ?? session, routeOptions, defaultChatRoute)
    if (desktopChatRoutesEqual(currentSessionRoute, nextRoute)) {
      return
    }
    setPanelError(null)
    setSessionDraft(draftSessionKey, composer)
    onStartNewSession(workspacePath, workspaceName)
  }, [composer, defaultChatRoute, draftSessionKey, liveSession, onStartNewSession, routeOptions, session, sessionId, setSessionDraft, workspaceName, workspacePath])

  const handleSetDefaultRoute = useCallback((routeId: string) => {
    const nextRoute = routeOptions.find((entry) => entry.id === routeId)
    if (!nextRoute) {
      return
    }
    if (!sessionId && selectedRouteId !== nextRoute.id) {
      setDraftRouteOverrideId(selectedRouteId)
    }
    void (async () => {
      try {
        const currentSettings = await queryClient.fetchQuery({ ...uiSettingsQueryOptions(), staleTime: 0 })
        const updated = await saveDefaultWorkspaceRoute({
          current: currentSettings,
          workspacePath,
          routeId: nextRoute.id,
        })
        queryClient.setQueryData(uiSettingsQueryKey(), updated)
        void queryClient.invalidateQueries({ queryKey: uiSettingsQueryKey() })
      } catch (error) {
        setPanelError(error instanceof Error ? error.message : 'Failed to update workspace default route')
      }
    })()
  }, [queryClient, routeOptions, selectedRouteId, sessionId, workspacePath])

  const handleSubmit = useCallback(async () => {
    if (submitting) {
      return
    }
    if (dictationEnabledRef.current) {
      stopDictation(false, true)
      await new Promise((resolve) => window.setTimeout(resolve, DICTATION_FINAL_FLUSH_MS))
      commitDictationDraft(true)
      dictationAcceptLateResultRef.current = false
    }
    const prompt = composer.trim()
    if (!prompt) {
      return
    }

    const targetedSubagent = parseTargetedSubagentPrompt(prompt, mentionSubagents)
    const runPrompt = targetedSubagent?.prompt ?? prompt
    const runTargetKind = targetedSubagent?.targetKind ?? ''
    const runTargetName = targetedSubagent?.targetName ?? ''

    shouldStickToBottomRef.current = true
    setPanelError(null)

    let targetSession = session
    try {
      if (!canSendWithSelectedPreference) {
        throw new Error('Select an authenticated model and thinking level before sending')
      }
      if (!targetSession) {
        const worktreeCreateFields = pendingWorktreeBranch
          ? {
            worktreeMode: 'on',
            worktreeUseCurrentBranch: true,
            worktreeBranchName: pendingWorktreeBranch,
          }
          : {}
        targetSession = sessionCreateOverride
          ? await sessionCreateOverride({
            mode: effectiveSessionMode,
            agentName: resolvedLockedAgentName || currentSessionAgent,
            preference: activePreferenceRecord.preference,
            ...worktreeCreateFields,
          })
          : await createSession({
            workspacePath,
            workspaceName,
            mode: effectiveSessionMode,
            agentName: resolvedLockedAgentName || currentSessionAgent,
            preference: activePreferenceRecord.preference,
            route: activeChatRoute,
            ...worktreeCreateFields,
          })
        onSessionCreated(targetSession)
        if (pendingWorktreeBranch) {
          onClearPendingWorktreeBranch?.()
        }
      }

      const clientRequestId = `desktop-v3-message:${targetSession.id}:${Date.now()}`
      if (session) {
        const existingSessionId = targetSession.id
        existingSessionStreamProbeRef.current = {
          sessionId: existingSessionId,
          clientRequestId,
          submittedAt: Date.now(),
          baselineMessageIds: new Set(displayedMessages.map((message) => message.id)),
          baselineAssistantIds: new Set(displayedMessages.filter((message) => message.role === 'assistant').map((message) => message.id)),
          baselineAssistantContent: assistantScreenText(displayedMessages, liveAssistantDraft, retainedAssistantSegments),
          baselineLastEventSeq: liveSession?.lastEventSeq ?? 0,
          baselineRunId: liveRunId,
          submitDiagnostics: getDesktopV3RealtimeDiagnostics(existingSessionId),
          emitted: false,
        }
      }
      await submitPrompt({
        sessionId: targetSession.id,
        route: activeChatRoute,
        sessionApi: targetSession.sessionApi,
        clientRequestId,
        workspacePath,
        workspaceName,
        prompt: runPrompt,
        agentName: resolvedLockedAgentName || currentSessionAgent,
        targetKind: runTargetKind,
        targetName: runTargetName,
      })
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to send prompt')
    }
  }, [activeChatRoute, activePreferenceRecord, canSendWithSelectedPreference, commitDictationDraft, composer, currentSessionAgent, displayedMessages, effectiveSessionMode, liveAssistantDraft, liveRunId, liveSession?.lastEventSeq, mentionSubagents, onClearPendingWorktreeBranch, onSessionCreated, pendingWorktreeBranch, resolvedLockedAgentName, retainedAssistantSegments, session, sessionCreateOverride, stopDictation, submitPrompt, submitting, workspaceName, workspacePath])

  const handleStop = useCallback(async () => {
    if (!sessionId) {
      return
    }
    setPanelError(null)
    try {
      await stopRun(sessionId, activeChatRoute, resumableRunId)
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to stop run')
    }
  }, [activeChatRoute, resumableRunId, sessionId, stopRun])

  const closeCommitModal = useCallback(() => {
    setCommitModal((current) => ({
      ...current,
      open: false,
      error: current.status === 'error' ? current.error : null,
    }))
  }, [])

  const closePlanModal = useCallback(() => {
    setPlanModal((current) => ({
      ...current,
      open: false,
      loading: false,
      saving: false,
      error: null,
    }))
  }, [])

  const handlePlanCopy = useCallback(async (text: string) => {
    const payload = text.trim()
    if (!payload) {
      return false
    }
    try {
      if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(payload)
      return true
    } catch {
      return false
    }
  }, [])

  const handlePlanSave = useCallback(async (planText: string, document?: Record<string, unknown>) => {
    if (!sessionId) {
      setPlanModal((current) => ({ ...current, error: 'Session id is unavailable.' }))
      return
    }
    const currentPlan = planModal.plan
    setPlanModal((current) => ({ ...current, saving: true, error: null }))
    setPanelError(null)
    try {
      const snapshot = await saveDesktopV3SessionPlan(sessionId, {
        id: currentPlan?.id,
        title: (currentPlan?.title?.trim() || 'Current Plan'),
        plan: planText,
        document,
        status: (currentPlan?.status?.trim() || 'draft'),
        approvalState: currentPlan?.approvalState,
      })
      setPlanModal({
        open: true,
        loading: false,
        historyLoading: false,
        saving: false,
        error: null,
        hasActive: Boolean(snapshot.hasActivePlan),
        plan: snapshot.activePlan,
        revisions: snapshot.planRevisions,
      })
    } catch (error) {
      setPlanModal((current) => ({
        ...current,
        saving: false,
        historyLoading: false,
        error: error instanceof Error ? error.message : 'Failed to save current plan',
      }))
    }
  }, [planModal.plan, queryClient, sessionId])

  const handleCommitModeChange = useCallback((mode: CommitMode) => {
    setCommitModal((current) => ({
      ...current,
      mode,
      error: null,
      status: current.status === 'running' || current.status === 'starting' ? current.status : 'idle',
    }))
  }, [])

  const handleCommitInstructionsChange = useCallback((instructions: string) => {
    setCommitModal((current) => ({
      ...current,
      instructions,
      error: current.status === 'error' ? null : current.error,
      status: current.status === 'success' ? 'idle' : current.status,
    }))
  }, [])

  const handleCommitSave = useCallback(async () => {
    if (!session) {
      setCommitModal((current) => ({
        ...current,
        status: 'error',
        error: 'Create or select a session before saving changes.',
      }))
      return
    }
    const instructions = commitModal.instructions.trim()
    const executionContext = commitExecutionContext(session)
    setCommitModal((current) => ({
      ...current,
      status: 'starting',
      error: null,
      runId: null,
    }))

    try {
      let targetSession = session
      let prompt = ''
      let agentName = ''
      let runInstructions = ''
      let targetKind = ''
      let targetName = ''

      if (commitModal.mode === 'agent') {
        const createdSession = await createSession({
          title: childCommitSessionTitle(session, instructions),
          workspacePath,
          workspaceName,
          mode: 'auto',
          agentName: selectedPrimaryAgent,
          metadata: {
            parent_session_id: session.id,
            parent_title: session.title,
            lineage_kind: 'background_agent',
            lineage_label: '@memory',
            launch_source: 'commit',
            commit_instructions: instructions,
            execution_context: executionContext,
            requested_background_agent: 'memory',
            background_agent: 'memory',
          },
          preference: activePreferenceRecord.preference,
          route: activeChatRoute,
        })
        targetSession = createdSession
        prompt = 'Review the git diff in scope, prepare the right staged set, and create the commit now.'
        runInstructions = buildCommitAgentInstructions(instructions)
        targetKind = 'background'
        targetName = 'memory'
      } else {
        if (!instructions) {
          setCommitModal((current) => ({
            ...current,
            status: 'error',
            error: 'Enter an exact commit message for manual commit.',
          }))
          return
        }
        const committed = await commitWorkspaceChanges({
          workspacePath: executionContext.workspace_path || activeChatRoute.runtimeWorkspacePath,
          cwd: executionContext.cwd || executionContext.workspace_path || activeChatRoute.runtimeWorkspacePath,
          message: instructions,
          all: true,
          endpoint: isManagedHostDesktopChatRoute(activeChatRoute)
            ? `/v1/swarm/managed-hosts/workspace/git/commit?swarm_id=${encodeURIComponent(activeChatRoute.swarmId || '')}`
            : undefined,
        })
        setCommitModal({
          ...EMPTY_COMMIT_MODAL_STATE,
          open: false,
        })
        setPanelError(null)
        const summary = committed.output?.trim() || committed.summary?.trim() || 'Manual git commit completed.'
        onToast?.({ message: summary, tone: 'success' })
        return
      }

      await startSessionRun({
        sessionId: targetSession.id,
        prompt,
        agentName,
        instructions: runInstructions,
        background: true,
        compact: false,
        targetKind,
        targetName,
        executionContext,
      })

      if (commitModal.mode === 'agent') {
        setCommitModal({
          ...EMPTY_COMMIT_MODAL_STATE,
          open: false,
        })
        onToast?.({ message: 'Memory commit started.', tone: 'info' })
        return
      }

    } catch (error) {
      setCommitModal((current) => ({
        ...current,
        status: 'error',
        error: error instanceof Error ? error.message : 'Failed to start save run.',
      }))
    }
  }, [activeChatRoute, activePreferenceRecord.preference, commitModal.instructions, commitModal.mode, onToast, selectedPrimaryAgent, session, workspaceName])


  const handleModeChange = useCallback(async (nextMode: 'plan' | 'auto') => {
    if (!selectedPrimaryAgentProfile?.exitPlanModeEnabled) {
      return
    }
    setPanelError(null)
    if (!sessionId) {
      setSessionDraftMode(draftSessionKey, nextMode)
      return
    }
    try {
      await updateDesktopV3SessionMode(sessionId, nextMode)
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to update session mode')
    }
  }, [draftSessionKey, queryClient, selectedPrimaryAgentProfile?.exitPlanModeEnabled, sessionId, sessionMode, setSessionDraftMode])

  const handleResolvePermission = useCallback(async (
    action: 'approve' | 'deny' | 'approve_always' | 'always_allow' | 'always_deny',
    reason: string,
    approvedArguments?: Record<string, unknown>,
  ) => {
    if (!sessionId || !activePermission) {
      return
    }
    const permissionId = activePermission.id
    setPermissionError(null)
    setResolvingPermissionIds((current) => new Set(current).add(permissionId))
    try {
      const resolved = await resolveSessionPermission(sessionId, permissionId, action, reason, approvedArguments, { sessionApi: liveSession?.sessionApi })
      const savedRulePreview = resolved.savedRule
        ? [resolved.savedRule.decision, resolved.savedRule.kind === 'bash_prefix' ? 'bash prefix:' : resolved.savedRule.kind === 'phrase' ? 'phrase:' : 'tool:', resolved.savedRule.kind === 'phrase' ? (resolved.savedRule.pattern || '') : resolved.savedRule.kind === 'bash_prefix' ? (resolved.savedRule.pattern || '') : (resolved.savedRule.tool || '')].filter(Boolean).join(' ')
        : null
      setLastSavedRulePreview(savedRulePreview)
      setLastSavedRuleExpiresAt(savedRulePreview ? Date.now() + ALWAYS_APPLY_SAVED_NOTICE_MS : null)
      setPermissionError(null)
    } catch (error) {
      setPermissionError(error instanceof Error ? error.message : 'Failed to resolve permission')
      throw error
    } finally {
      setResolvingPermissionIds((current) => {
        if (!current.has(permissionId)) {
          return current
        }
        const next = new Set(current)
        next.delete(permissionId)
        return next
      })
    }
  }, [activePermission, liveSession, session, sessionId])

  const handleMentionInsert = useCallback((name: string) => {
    const normalizedName = name.trim()
    if (!normalizedName) {
      return
    }
    const trimmedStart = composer.replace(/^[\s\t\r\n]+/, '')
    const leadingWhitespace = composer.slice(0, composer.length - trimmedStart.length)
    const nextDraft = `${leadingWhitespace}@${normalizedName} `
    setComposerDraft(nextDraft)
  }, [composer, setComposerDraft])

  const handleComposerKeyDown = useCallback((event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (mentionPaletteIsActive && mentionPaletteMatches.length > 0) {
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setMentionSelectionIndex((current) => (current + 1) % mentionPaletteMatches.length)
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setMentionSelectionIndex((current) => (current - 1 + mentionPaletteMatches.length) % mentionPaletteMatches.length)
        return
      }
      if (event.key === 'Tab') {
        event.preventDefault()
        const selected = mentionPaletteMatches[mentionSelectionIndex] ?? mentionPaletteMatches[0]
        if (selected) {
          handleMentionInsert(selected)
        }
        return
      }
      if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing && !mentionHasArgs(composer)) {
        const selected = mentionPaletteMatches[mentionSelectionIndex] ?? mentionPaletteMatches[0]
        if (selected) {
          event.preventDefault()
          handleMentionInsert(selected)
          return
        }
      }
    }

    if (slashPalette.active && slashPalette.matches.length > 0) {
      if (event.key === 'ArrowDown') {
        event.preventDefault()
        setSlashSelectionIndex((current) => (current + 1) % slashPalette.matches.length)
        return
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault()
        setSlashSelectionIndex((current) => (current - 1 + slashPalette.matches.length) % slashPalette.matches.length)
        return
      }
      if (event.key === 'Tab') {
        event.preventDefault()
        const command = slashPalette.matches[slashSelectionIndex] ?? slashPalette.matches[0]
        if (command) {
          handleSlashInsert(command)
        }
        return
      }
      if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
        event.preventDefault()
        const command = slashPalette.matches[slashSelectionIndex] ?? slashPalette.exactMatch ?? slashPalette.matches[0]
        if (command) {
          handleSlashSelect(command)
        }
        return
      }
    }

    if (event.key !== 'Enter' || event.shiftKey || event.nativeEvent.isComposing) {
      return
    }
    event.preventDefault()
    if (canStop || submitting || composer.trim() === '' || !canSendWithSelectedPreference) {
      return
    }
    pinToLatest()
    void handleSubmit()
  }, [canSendWithSelectedPreference, canStop, composer, handleMentionInsert, handleSlashInsert, handleSlashSelect, handleSubmit, mentionPaletteIsActive, mentionPaletteMatches, mentionSelectionIndex, pinToLatest, slashPalette.active, slashPalette.hasArguments, slashPalette.matches, slashSelectionIndex, submitting])

  useEffect(() => {
    setResolvingPermissionIds(new Set())
  }, [sessionId])

  const handleComposerDrop = useCallback((event: ReactDragEvent<HTMLTextAreaElement>) => {
    const todoText = event.dataTransfer.getData(TODO_DRAG_MIME).trim() || event.dataTransfer.getData('text/plain').trim()
    if (!todoText) {
      return
    }
    event.preventDefault()
    const nextDraft = composer.trim() === '' ? todoText : `${composer}${composer.endsWith('\n') ? '' : '\n'}${todoText}`
    setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, nextDraft)
  }, [composer, sessionId, setSessionDraft, workspacePath])

  return (
    <Card className="flex h-full w-full flex-1 min-h-0 min-w-0 flex-row overflow-hidden rounded-none border-0 bg-[var(--app-surface)]">
      <div className="flex h-full min-w-0 flex-1 flex-col overflow-hidden">
      <header className={`${compactHeader ? 'min-h-[48px] px-2.5 py-1.5' : 'min-h-[60px] px-2.5 pb-2 pt-[calc(var(--app-safe-area-top)_+_0.5rem)] sm:h-[60px] sm:px-4 sm:py-0'} shrink-0 flex items-center gap-1.5 border-b border-[var(--app-border)] sm:gap-2`}>
        <button
          type="button"
          className="inline-flex h-9 w-9 shrink-0 touch-manipulation items-center justify-center rounded-xl border border-transparent bg-transparent text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] sm:hidden"
          onClick={() => handleMobileQuickCommand('chats')}
          aria-label="Open chats"
          title="Chats"
        >
          <MessageSquareText size={18} />
        </button>
        <div className="min-w-0 flex-1">
          <div className="sm:hidden">
            <h1 className="truncate text-[13px] font-semibold leading-tight text-[var(--app-text)]" title={liveSession?.title || 'New conversation'}>
              {liveSession?.title || 'New conversation'}
            </h1>
            <div className="mt-1 grid min-w-0 grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-2 overflow-hidden">
              <div className="inline-flex min-w-0 items-center text-[10px] font-medium text-[var(--app-text-muted)]" title={workspaceName || workspacePath}>
                <span className="truncate text-left">{workspaceName || workspacePath}</span>
              </div>
              <div className="flex shrink-0 justify-center">
                {showRunTimer ? (
                  <div className="inline-flex h-[18px] items-center gap-1 rounded-full border border-transparent bg-transparent text-[10px] font-medium tabular-nums text-[var(--app-text-muted)]" title="Run time">
                    <Clock3 size={10} className="shrink-0" />
                    <span>{runTimerLabel}</span>
                  </div>
                ) : null}
              </div>
              <div className="flex min-w-0 items-center justify-end">
                {mobileAgentTodoBadgeLabel ? (
                  <div className="inline-flex max-w-[72px] items-center gap-1 text-[10px] font-medium text-[var(--app-text-muted)]" title={agentTodoSummary?.activeText || 'Agent checklist for this session'}>
                    <ListChecks size={10} className="shrink-0 text-[var(--app-text-subtle)]" />
                    <span className="truncate">{mobileAgentTodoBadgeLabel}</span>
                  </div>
                ) : null}
              </div>
            </div>
          </div>
          <div className="hidden min-w-0 sm:block">
            <h1 className="flex items-center gap-2 overflow-hidden text-sm font-semibold text-[var(--app-text)]">
              <span className="truncate" title={liveSession?.title || 'New conversation'}>{liveSession?.title || 'New conversation'}</span>
              <span className="shrink-0 text-[var(--app-text-subtle)] font-normal">/</span>
              <span className="truncate text-[var(--app-text-muted)] font-normal" title={workspaceName}>{workspaceName}</span>
            </h1>
            {agentTodoBadgeLabel ? (
              <div className="mt-1 flex max-w-full items-center gap-1.5 overflow-hidden text-[11px] font-medium text-[var(--app-text-muted)]" title={agentTodoSummary?.activeText || 'Agent checklist for this session'}>
                <ListChecks size={12} className="shrink-0 text-[var(--app-text-subtle)]" />
                <span className="truncate">{agentTodoBadgeLabel}</span>
              </div>
            ) : null}
          </div>
        </div>
        <div className="ml-auto flex min-w-0 items-center justify-end gap-1 sm:gap-2">
          {activePermission ? (
            <button
              type="button"
              className="flex w-fit cursor-default items-center gap-2 rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-2.5 py-1.5 text-left text-xs text-[var(--app-danger)] sm:px-3"
            >
              <ShieldAlert size={14} />
              <span>{pendingPermissionCount > 1 ? `${pendingPermissionCount} pending` : '1 pending'}</span>
            </button>
          ) : null}
          <button
            type="button"
            className="inline-flex h-9 w-9 shrink-0 touch-manipulation items-center justify-center rounded-xl border border-transparent bg-transparent text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] sm:hidden"
            onClick={() => handleMobileQuickCommand('new-session')}
            aria-label={newSessionLabel}
            title={newSessionLabel}
          >
            <Plus size={19} />
          </button>
          {showRunTimer ? (
            <div className="hidden h-9 shrink-0 items-center gap-1 rounded-xl border border-transparent bg-transparent px-1.5 text-[11px] font-medium tabular-nums text-[var(--app-text-muted)] sm:inline-flex" title="Run time">
              <Clock3 size={12} className="shrink-0" />
              <span>{runTimerLabel}</span>
            </div>
          ) : null}
          {!hideWorkspaceActions ? <div className="hidden h-9 shrink-0 items-center gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-1 text-[11px] font-medium text-[var(--app-text-muted)] shadow-sm sm:inline-flex">
            <button
              type="button"
              className="inline-flex h-7 w-7 items-center justify-center rounded-lg text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
              onClick={() => handleMobileQuickCommand('new-session')}
              aria-label={newSessionLabel}
              title={newSessionLabel}
            >
              <Plus size={13} className="shrink-0" />
            </button>
            <button
              type="button"
              className="inline-flex h-7 w-7 items-center justify-center rounded-lg text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
              onClick={() => handleMobileQuickCommand('change-workspace')}
              aria-label="Change workspace"
              title="Change workspace"
            >
              <Home size={13} className="shrink-0" />
            </button>
            <button
              type="button"
              className="inline-flex h-7 w-7 items-center justify-center rounded-lg text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-45 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
              onClick={() => handleMobileQuickCommand('save')}
              disabled={!sessionId}
              aria-label="Save changes"
              title={sessionId ? 'Save changes from this session' : 'Open a session before saving changes'}
            >
              <Save size={13} className="shrink-0" />
            </button>
          </div> : null}
          {!hideWorkspaceActions ? <div className="relative shrink-0 sm:hidden">
            <button
              ref={mobileQuickCommandsTriggerRef}
              type="button"
              className="inline-flex h-9 w-9 touch-manipulation select-none items-center justify-center rounded-xl border border-transparent bg-transparent text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] sm:h-10 sm:w-auto sm:min-w-10 sm:gap-2 sm:border-[var(--app-border)] sm:bg-[var(--app-bg-alt)] sm:px-2.5 sm:text-[var(--app-text)]"
              onClick={() => {
                setMobileSettingsOpen(false)
                setMobileQuickCommandsOpen((open) => !open)
              }}
              aria-expanded={mobileQuickCommandsOpen}
              aria-haspopup="menu"
              aria-label="Open quick actions"
              title="Quick actions"
            >
              <ListChecks size={18} />
              <ChevronDown size={11} className="-ml-1 text-[var(--app-text-subtle)]" />
            </button>
            {mobileQuickCommandsOpen ? (
              <div
                ref={mobileQuickCommandsRef}
                role="menu"
                aria-label="Quick actions"
                className="absolute right-0 top-[calc(100%+0.5rem)] z-50 w-56 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-1.5 text-sm shadow-[var(--shadow-panel)]"
              >
                <button
                  type="button"
                  role="menuitem"
                  className="hidden min-h-11 w-full items-center gap-3 rounded-xl px-3 text-left text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]"
                  onClick={() => handleMobileQuickCommand('chats')}
                >
                  <MessageSquareText size={17} className="shrink-0 text-[var(--app-text-subtle)]" />
                  <span className="min-w-0">
                    <span className="block font-medium">Chats</span>
                    <span className="block text-xs text-[var(--app-text-muted)]">Open conversations</span>
                  </span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="hidden min-h-11 w-full items-center gap-3 rounded-xl px-3 text-left text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50 sm:flex"
                  onClick={() => handleMobileQuickCommand('new-session')}
                >
                  <Plus size={17} className="shrink-0 text-[var(--app-text-subtle)]" />
                  <span className="min-w-0">
                    <span className="block font-medium">New session</span>
                    <span className="block text-xs text-[var(--app-text-muted)]">Start fresh here</span>
                  </span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="flex min-h-11 w-full items-center gap-3 rounded-xl px-3 text-left text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]"
                  onClick={() => handleMobileQuickCommand('change-workspace')}
                >
                  <Home size={17} className="shrink-0 text-[var(--app-text-subtle)]" />
                  <span className="min-w-0">
                    <span className="block font-medium">Change workspace</span>
                    <span className="block text-xs text-[var(--app-text-muted)]">Open workspace picker</span>
                  </span>
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="flex min-h-11 w-full items-center gap-3 rounded-xl px-3 text-left text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
                  onClick={() => handleMobileQuickCommand('save')}
                  disabled={!sessionId}
                >
                  <Save size={17} className="shrink-0 text-[var(--app-text-subtle)]" />
                  <span className="min-w-0">
                    <span className="block font-medium">Save changes</span>
                    <span className="block text-xs text-[var(--app-text-muted)]">Commit this session</span>
                  </span>
                </button>
              </div>
            ) : null}
          </div> : null}
        </div>
      </header>

      <div className="relative flex-1 min-h-0 min-w-0 bg-[var(--app-bg-alt)]">
        <div ref={scrollerRef} data-testid="desktop-chat-scroller" onScroll={persistScrollState} className="h-full min-h-0 min-w-0 overflow-y-auto overflow-x-hidden overscroll-contain py-4 [-webkit-overflow-scrolling:touch] sm:py-5">
        <div className="mx-auto min-w-0 w-full max-w-[1080px] px-4 sm:px-6">
        <div className="mx-auto min-w-0 w-full max-w-[980px]">
        {loadingMessages && messages.length === 0 ? (
          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-sm text-[var(--app-text-muted)]">Loading conversation…</div>
        ) : null}
        {!loadingMessages && messages.length === 0 && (!sessionId || emptyStateMessage) ? (
          <div className="flex items-center gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
            <Sparkles size={16} />
            {emptyStateMessage ?? `No conversation yet. Ask ${currentSessionAgent || 'swarm'} to do something in this workspace.`}
          </div>
        ) : null}
        <div
          className="relative min-w-0"
          style={{ height: rowVirtualizer.getTotalSize() > 0 ? `${rowVirtualizer.getTotalSize()}px` : undefined }}
        >
          {virtualItems.map((virtualItem) => {
            const item = renderItems[virtualItem.index]
            if (!item) {
              return null
            }

            if (item.type === 'live-tool') {
              return (
                <div
                  key={virtualItem.key}
                  ref={rowVirtualizer.measureElement}
                  data-index={virtualItem.index}
                  data-testid="desktop-chat-row"
                  data-render-item-type={item.type}
                  data-render-item-key={String(virtualItem.key)}
                  className="absolute left-0 top-0 w-full py-2 flex justify-center"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                >
                  <article className="w-full min-w-0">
                    <ChatMarkdown content="" toolMessage={item.toolMessage} thinkingTagsEnabled={thinkingTagsEnabled} />
                  </article>
                </div>
              )
            }

            if (item.type === 'live-assistant') {
              return (
                <div
                  key={virtualItem.key}
                  ref={rowVirtualizer.measureElement}
                  data-index={virtualItem.index}
                  data-testid="desktop-chat-row"
                  data-render-item-type={item.type}
                  data-render-item-key={String(virtualItem.key)}
                  className="absolute left-0 top-0 w-full py-2 flex justify-center"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                >
                  <article className="w-full min-w-0 max-w-[980px]">
                    <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                      {currentSessionAgent || 'swarm'}
                    </div>
                    <ChatMarkdown content={item.content} toolMessage={null} />
                  </article>
                </div>
              )
            }

            if (item.type === 'live-reasoning') {
              const reasoningLabel = reasoningHeadline(item.state, item.startedAt, timerNow, item.completedAt ?? null)
              const reasoningBody = renderReasoningBody(item.text, item.summary, thinkingTagsEnabled)
              return (
                <div
                  key={virtualItem.key}
                  ref={rowVirtualizer.measureElement}
                  data-index={virtualItem.index}
                  data-testid="desktop-chat-row"
                  data-render-item-type={item.type}
                  data-render-item-key={String(virtualItem.key)}
                  className="absolute left-0 top-0 w-full py-2 flex justify-center"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                >
                  <article className="w-full min-w-0 max-w-[980px] opacity-80">
                    <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                      {reasoningLabel}
                    </div>
                    {reasoningBody ? <ChatMarkdown content={reasoningBody} toolMessage={null} thinkingTagsEnabled={thinkingTagsEnabled} /> : null}
                  </article>
                </div>
              )
            }

            const message = item.message
            const isUser = message.role === 'user'
            const isReasoning = message.role === 'reasoning'

            if (isUser) {
              return (
                <div
                  key={virtualItem.key}
                  ref={rowVirtualizer.measureElement}
                  data-index={virtualItem.index}
                  data-testid="desktop-chat-row"
                  data-render-item-type={item.type}
                  data-render-item-key={String(virtualItem.key)}
                  className="absolute left-0 top-0 w-full py-2 flex justify-end"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                  data-global-seq={message.globalSeq}
                >
                  <article className="min-w-0 max-w-[80%] rounded-2xl bg-[var(--app-primary)] px-4 py-3 text-[var(--app-primary-text)] shadow-sm">
                    <ChatMarkdown content={message.content} toolMessage={message.toolMessage ?? null} className="!text-current" />
                  </article>
                </div>
              )
            }

            if (isReasoning) {
              const reasoningLabel = reasoningHeadline('done', null, timerNow)
              const reasoningBody = renderReasoningBody(message.content, message.content, thinkingTagsEnabled)
              return (
                <div
                  key={virtualItem.key}
                  ref={rowVirtualizer.measureElement}
                  data-index={virtualItem.index}
                  data-testid="desktop-chat-row"
                  data-render-item-type={item.type}
                  data-render-item-key={String(virtualItem.key)}
                  className="absolute left-0 top-0 w-full py-2 flex justify-center"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                  data-global-seq={message.globalSeq}
                >
                  <article className="w-full min-w-0 max-w-[980px] opacity-80">
                    <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                      {reasoningLabel}
                    </div>
                    {reasoningBody ? <ChatMarkdown content={reasoningBody} toolMessage={message.toolMessage ?? null} /> : null}
                  </article>
                </div>
              )
            }

            return (
              <div
                key={virtualItem.key}
                ref={rowVirtualizer.measureElement}
                data-index={virtualItem.index}
                data-testid="desktop-chat-row"
                data-render-item-type={item.type}
                data-render-item-key={String(virtualItem.key)}
                className="absolute left-0 top-0 w-full py-2 flex justify-center"
                style={{ transform: `translateY(${virtualItem.start}px)` }}
                data-global-seq={message.globalSeq}
              >
                <article className={message.role === 'tool' ? "w-full min-w-0" : "w-full min-w-0 max-w-[980px]"}>
                  {message.role !== 'tool' ? (
                    <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                      {messageRoleLabel(message.role, resolveMessageAssistantLabel(message, currentSessionAgent))}
                    </div>
                  ) : null}
                  <ChatMarkdown content={message.content} toolMessage={message.toolMessage ?? null} thinkingTagsEnabled={thinkingTagsEnabled} />
                </article>
              </div>
            )
          })}
        </div>
        </div>
        </div>
        </div>
        {showScrollLockReturnButton ? (
          <button
            type="button"
            onClick={pinToLatest}
            aria-label="Return to locked scroll position"
            title="Return to latest"
            className="absolute bottom-3 right-4 z-20 inline-flex h-9 w-9 items-center justify-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] shadow-sm transition hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] sm:right-6"
          >
            <ChevronDown size={18} aria-hidden="true" />
          </button>
        ) : null}
      </div>

      <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)]">
        <div className="mx-auto grid w-full max-w-[1080px] gap-3 px-4 pb-[calc(0.75rem+var(--app-safe-area-bottom))] pt-4 focus-within:pb-[calc(1rem+var(--app-safe-area-bottom))] sm:px-6 sm:pb-[calc(1.25rem+var(--app-safe-area-bottom))] sm:pt-5">
          {panelError ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{panelError}</div> : null}
          {permissionError ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{permissionError}</div> : null}
          {dictationError ? (
            <div className="flex items-start gap-3 rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm text-[var(--app-warning-text)]">
              <div className="min-w-0 flex-1">{dictationError}</div>
              <button
                type="button"
                className="-m-1 grid h-7 w-7 shrink-0 place-items-center rounded-full text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
                aria-label="Dismiss microphone warning"
                onClick={() => setDictationError(null)}
              >
                <X size={16} aria-hidden="true" />
              </button>
            </div>
          ) : null}
          {lastSavedRulePreview ? (
            <div className="flex items-center justify-between gap-3 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-3 py-2 text-sm text-[var(--app-success)]">
              <span className="min-w-0 truncate">Always apply saved: {lastSavedRulePreview}</span>
              <span className="shrink-0 text-xs text-[var(--app-text-muted)]">Disappears in {savedRuleCountdown}s</span>
            </div>
          ) : null}
          {terminalUserStopSummary ? (
            <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm text-[var(--app-warning-text)]">
              {terminalUserStopSummary}
            </div>
          ) : null}
          {pendingWorktreeBranch ? (
            <div className="flex items-center justify-between gap-3 rounded-xl border border-[var(--app-border-accent)] bg-[var(--app-bg-alt)] px-3 py-2 text-xs text-[var(--app-text-muted)]">
              <span className="min-w-0 truncate">
                New session will use worktree branch <span className="font-mono text-[var(--app-text)]">{pendingWorktreeBranch}</span>.
              </span>
              <button
                type="button"
                className="shrink-0 rounded-lg px-2 py-1 font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
                onClick={onClearPendingWorktreeBranch}
              >
                Clear
              </button>
            </div>
          ) : null}
          {mentionPaletteIsActive ? (
            <DesktopMentionPanel
              matches={mentionPaletteMatches}
              selectedIndex={mentionSelectionIndex}
              onHover={setMentionSelectionIndex}
              onSelect={handleMentionInsert}
            />
          ) : slashPalette.active ? (
            <DesktopSlashCommandPanel
              palette={slashPalette}
              selectedIndex={slashSelectionIndex}
              onHover={setSlashSelectionIndex}
              onSelect={handleSlashSelect}
            />
          ) : null}
          <div className="relative rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] transition-colors focus-within:border-[var(--app-border-accent)]">
            <div className="flex items-end gap-3 px-4 py-2 sm:py-3 lg:py-2.5">
              <div className="min-w-0 flex-1">
                <Textarea
                  ref={composerTextareaRef}
                  value={dictationComposer}
                  onChange={(event) => {
                    if (dictationEnabledRef.current) {
                      dictationBaseDraftRef.current = event.target.value
                      dictationFinalTranscriptRef.current = ''
                      dictationInterimTranscriptRef.current = ''
                    }
                    setComposerDraft(event.target.value)
                  }}
                  onKeyDown={handleComposerKeyDown}
                  onDragOver={(event) => {
                    const hasTodo = Array.from(event.dataTransfer.types).includes(TODO_DRAG_MIME) || Array.from(event.dataTransfer.types).includes('text/plain')
                    if (!hasTodo) {
                      return
                    }
                    event.preventDefault()
                    event.dataTransfer.dropEffect = 'copy'
                  }}
                  onDrop={handleComposerDrop}
                  placeholder=""
                  className={showDictationButton ? "!min-h-[40px] max-h-28 resize-none overflow-y-auto !rounded-none !border-0 !border-none bg-transparent px-0 py-0 pr-12 !shadow-none !outline-none !ring-0 focus:!ring-0 focus:!shadow-none focus:!border-0 focus-visible:!ring-0 focus-visible:!ring-offset-0 focus-visible:!shadow-none focus-visible:!border-0 hover:!border-0 disabled:bg-transparent sm:!min-h-[56px] lg:!min-h-[52px]" : "!min-h-[40px] max-h-28 resize-none overflow-y-auto !rounded-none !border-0 !border-none bg-transparent px-0 py-0 !shadow-none !outline-none !ring-0 focus:!ring-0 focus:!shadow-none focus:!border-0 focus-visible:!ring-0 focus-visible:!ring-offset-0 focus-visible:!shadow-none focus-visible:!border-0 hover:!border-0 disabled:bg-transparent sm:!min-h-[56px] lg:!min-h-[52px]"}
                  rows={1}
                  disabled={composerDisabled}
                />
                {showDictationButton ? (
                  <button
                    type="button"
                    onClick={handleDictationToggle}
                    disabled={dictationButtonDisabled}
                    aria-pressed={dictationEnabled}
                    aria-label={dictationEnabled ? 'Stop microphone dictation' : 'Start microphone dictation'}
                    title={dictationSupported ? (dictationEnabled ? 'Stop dictation' : 'Start dictation') : 'Speech recognition is not available in this browser'}
                    className={dictationEnabled
                      ? 'absolute right-3 top-3 inline-flex h-9 w-9 items-center justify-center rounded-full border border-[var(--app-border-accent)] bg-[var(--app-primary)] text-[var(--app-primary-text)] shadow-sm transition hover:bg-[var(--app-primary-hover)] disabled:cursor-not-allowed disabled:opacity-50'
                      : 'absolute right-3 top-3 inline-flex h-9 w-9 items-center justify-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] shadow-sm transition hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50'}
                  >
                    <Mic size={17} className={dictationListening ? 'animate-pulse' : undefined} />
                  </button>
                ) : null}
              </div>

            </div>

            {mentionPaletteIsActive ? (
              <div className="border-t border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-2 text-[11px] text-[var(--app-text-muted)]">
                Use ↑/↓ to choose a subagent, Tab or Enter to insert, then continue typing your task.
              </div>
            ) : null}

            <div className="border-t border-[var(--app-border)] px-4 py-2 text-[11px]">
              {/* DESKTOP LAYOUT (>= 1000px; thinking/fast collapse from 1000px to 1100px) */}
              <div className={`${compactControls ? 'hidden' : 'hidden min-[1000px]:flex'} min-w-0 items-center gap-2 justify-between`}>
                <div className="flex min-w-0 flex-1 items-center gap-3 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                  {isFlowSession ? (
                    <span className="inline-flex items-center gap-1 whitespace-nowrap font-medium text-[var(--app-text-muted)]">
                      <span className="text-[var(--app-text-subtle)]">
                        Flow agent:
                      </span>
                      <span className="font-semibold text-[var(--app-text)]">
                        {currentSessionAgent || 'flow'}
                      </span>
                    </span>
                  ) : resolvedLockedAgentLabel ? (
                    <span className="inline-flex items-center gap-1 whitespace-nowrap font-medium text-[var(--app-text-muted)]">
                      <span className="text-[var(--app-text-subtle)]">Agent:</span>
                      <span className="font-semibold text-[var(--app-text)]">{resolvedLockedAgentLabel}</span>
                    </span>
                  ) : !hideModeSelector && selectedPrimaryAgentProfile?.exitPlanModeEnabled ? (
                    <ModePicker
                      mode={sessionMode === 'auto' ? 'auto' : 'plan'}
                      onSelect={handleModeChange}
                    />
                  ) : selectedPrimaryAgentProfile ? (
                    <span className="inline-flex items-center gap-1 whitespace-nowrap font-medium text-[var(--app-text-muted)]">
                      <span className="text-[var(--app-text-subtle)]">
                        Execution:
                      </span>
                      <span className="font-semibold uppercase tracking-wider text-[var(--app-primary)]">
                        {selectedExecutionSettingLabel}
                      </span>
                    </span>
                  ) : null}

                  {!isFlowSession && !resolvedLockedAgentName ? (
                    <AgentPicker
                      currentAgent={currentSessionAgent}
                      selectedPrimaryAgent={selectedPrimaryAgent}
                      agents={selectableAgents}
                      onSelect={(value) => {
                        void handleAgentSelect(value)
                      }}
                    />
                  ) : null}

                  <ModelPicker
                    options={resolvedModelOptions}
                    selectedKey={selectedModelAvailable ? selectedModelKey : ''}
                    onSelect={handleModelChange}
                    openSignal={modelPickerOpenSignal}
                    disabled={activeAgentModelLocked}
                    disabledReason={agentModelLockReason}
                  />

                  <ThinkingPicker
                    value={normalizedThinking}
                    options={THINKING_OPTIONS}
                    onSelect={handleThinkingChange}
                    label="Thinking"
                    tagsEnabled={thinkingTagsEnabled}
                    onToggleTags={(enabled) => {
                      void handleThinkingTagsToggle(enabled)
                    }}
                    tagsBusy={thinkingTagsSaving}
                    disabled={activeAgentModelLocked}
                    disabledReason={agentModelLockReason}
                  />

                  {fastSupported ? (
                    <ThinkingPicker
                      value={fastValue}
                      options={FAST_ON_OFF_OPTIONS}
                      onSelect={handleFastChange}
                      label="Fast"
                      disabled={activeAgentModelLocked}
                      disabledReason={agentModelLockReason}
                    />
                  ) : null}

                  <button type="button" onClick={() => { void handleCompact(composer) }} disabled={!sessionId || canStop || submitting} title={contextBadgeTooltip ? `${contextBadgeTooltip} · Click to compact` : 'Compact conversation'} className="inline-flex min-h-6 items-center gap-1 rounded-full bg-[var(--app-bg-alt)] px-2 py-0.5 font-medium tabular-nums text-[var(--app-text)] transition hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50">
                    <span>{contextBadgeLabel || (selectedContextWindow > 0 ? `${formatContextWindow(selectedContextWindow)} ctx` : 'ctx')}</span>
                    <Minimize2 size={12} className="text-[var(--app-text-subtle)]" />
                  </button>
                </div>

                <div className="flex shrink-0 items-center gap-2">
                  {showRoutePicker ? (
                    <RoutePicker
                      currentRoute={activeChatRoute}
                      routes={routeOptions}
                      onSelect={handleRouteChange}
                      defaultRouteId={resolvedDefaultRouteId}
                      onSetDefault={handleSetDefaultRoute}
                      defaultDisabled={composerDisabled || canStop}
                      disabled={composerDisabled || canStop}
                      title={sessionId ? 'Changing the route starts a new session in this workspace.' : 'Route this chat through the host or a linked child swarm.'}
                    />
                  ) : null}

                  <Button
                    size="sm"
                    className="h-10 w-10 shrink-0 rounded-xl border border-transparent bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] hover:bg-[var(--app-primary-hover)] active:bg-[var(--app-primary-active)]"
                    onClick={() => {
                      if (canStop) {
                        void handleStop()
                        return
                      }
                      pinToLatest()
                      void handleSubmit()
                    }}
                    disabled={!canStop && (submitting || composer.trim() === '' || !canSendWithSelectedPreference)}
                    aria-label={canStop ? 'Stop run' : 'Send message'}
                  >
                    {canStop ? <Square size={18} /> : submitting ? <LoaderCircle size={18} className="animate-spin" /> : <Send size={20} />}
                  </Button>
                </div>
              </div>

              {/* MOBILE 1-ROW COMPACT LAYOUT (< 1000px) */}
              <div className={`${compactControls ? 'flex' : 'flex min-[1000px]:hidden'} w-full min-w-0 relative`}>
                {mobileSettingsOpen ? (
                  <div ref={mobileSettingsRef} className="absolute bottom-[100%] left-0 z-50 mb-2 flex w-[max(260px,100%)] flex-col gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-[var(--shadow-panel)]">
                    {!isFlowSession && !resolvedLockedAgentName && !hideModeSelector ? <ModePicker mode={sessionMode === 'auto' ? 'auto' : 'plan'} onSelect={handleModeChange} /> : null}
                    {!isFlowSession && !resolvedLockedAgentName ? <AgentPicker currentAgent={currentSessionAgent} selectedPrimaryAgent={selectedPrimaryAgent} agents={selectableAgents} onSelect={(value) => { void handleAgentSelect(value) }} dropdownAlign="left" /> : null}
                    <ModelPicker options={resolvedModelOptions} selectedKey={selectedModelAvailable ? selectedModelKey : ''} onSelect={handleModelChange} openSignal={modelPickerOpenSignal} disabled={activeAgentModelLocked} disabledReason={agentModelLockReason} />
                    <ThinkingPicker value={normalizedThinking} options={THINKING_OPTIONS} onSelect={handleThinkingChange} label="Thinking" tagsEnabled={thinkingTagsEnabled} onToggleTags={(enabled) => { void handleThinkingTagsToggle(enabled) }} tagsBusy={thinkingTagsSaving} disabled={activeAgentModelLocked} disabledReason={agentModelLockReason} />
                    {fastSupported ? <ThinkingPicker value={fastValue} options={FAST_ON_OFF_OPTIONS} onSelect={handleFastChange} label="Fast" disabled={activeAgentModelLocked} disabledReason={agentModelLockReason} /> : null}
                  </div>
                ) : null}

                <div className={showRoutePicker ? "grid w-full min-w-0 grid-cols-[minmax(0,1fr)_48px_minmax(0,max-content)_40px] items-center gap-1.5 sm:grid-cols-[minmax(0,1fr)_56px_minmax(0,0.7fr)_40px] sm:gap-2" : "grid w-full min-w-0 grid-cols-[minmax(0,1fr)_48px_40px] items-center gap-1.5 sm:grid-cols-[minmax(0,1fr)_56px_40px] sm:gap-2"}>
                  {/* The Summary/Settings Quick Toggle */}
                  <button 
                    ref={mobileSettingsTriggerRef}
                    type="button" 
                    onClick={() => setMobileSettingsOpen(!mobileSettingsOpen)} 
                    className="flex h-10 min-w-0 items-center gap-1.5 overflow-hidden rounded-xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] px-2 shadow-sm text-left hover:bg-[var(--app-surface-hover)] transition"
                    title={resolvedLockedAgentName ? 'Open model, thinking, and speed settings' : 'Open mode, agent, model, thinking, and speed settings'}
                  >
                    <Settings2 size={14} className="shrink-0 text-[var(--app-text-subtle)]" />
                    <span className="flex min-w-0 flex-col leading-tight">
                      <span className="truncate text-[11px] sm:text-[12px] font-medium text-[var(--app-text)]">{resolvedLockedAgentName ? 'Model settings' : isFlowSession ? 'Flow' : currentSessionAgent}</span>
                      <span className="truncate text-[10px] text-[var(--app-text-muted)]">
                        {!resolvedLockedAgentName && !hideModeSelector ? `${sessionMode === 'auto' ? 'auto' : 'plan'} · ` : ''}{selectedModelOption?.label || 'Model'} · {normalizedThinking}{fastSupported ? ` · fast ${fastValue}` : ''}
                      </span>
                    </span>
                  </button>

                  <button type="button" onClick={() => { void handleCompact(composer) }} disabled={!sessionId || canStop || submitting} title={contextBadgeTooltip ? `${contextBadgeTooltip} · Click to compact` : 'Compact conversation'} className="inline-flex h-10 min-w-0 items-center justify-center rounded-xl bg-[var(--app-surface-subtle)] px-1.5 sm:px-2.5 transition hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50 border border-[var(--app-border)] text-[var(--app-text-muted)] hover:text-[var(--app-text)] font-medium tabular-nums text-[10px] sm:text-[11px]">
                    <span className="truncate min-w-0 w-full text-center">{contextBadgeLabel || 'ctx'}</span>
                  </button>

                  {showRoutePicker ? (
                    <div className="flex min-w-0 overflow-hidden sm:[&>div]:w-full sm:[&>div>button]:w-full">
                      <RoutePicker
                        currentRoute={activeChatRoute}
                        routes={routeOptions}
                        onSelect={handleRouteChange}
                        defaultRouteId={resolvedDefaultRouteId}
                        onSetDefault={handleSetDefaultRoute}
                        defaultDisabled={composerDisabled || canStop}
                        disabled={composerDisabled || canStop}
                        title={sessionId ? 'Changing the route starts a new session in this workspace.' : 'Route this chat through the host or a linked child swarm.'}
                      />
                    </div>
                  ) : null}

                  <Button
                    size="sm"
                    className="h-10 w-10 shrink-0 rounded-xl border border-transparent bg-[var(--app-primary)] p-0 text-[var(--app-primary-text)] hover:bg-[var(--app-primary-hover)] active:bg-[var(--app-primary-active)]"
                    onClick={() => {
                      if (canStop) {
                        void handleStop()
                        return
                      }
                      pinToLatest()
                      void handleSubmit()
                    }}
                    disabled={!canStop && (submitting || composer.trim() === '' || !canSendWithSelectedPreference)}
                    aria-label={canStop ? 'Stop run' : 'Send message'}
                  >
                    {canStop ? <Square size={18} /> : submitting ? <LoaderCircle size={18} className="animate-spin" /> : <Send size={20} />}
                  </Button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      </div>
      <ImageSessionSidebar state={imageSidebar} onClose={() => setImageSidebar(null)} />

      <DesktopPermissionModal
        open={Boolean(activePermission)}
        permission={activePermission}
        pendingCount={pendingPermissionCount}
        sessionMode={sessionMode}
        onOpenChange={(open) => {
          if (open || !activePermission || resolvingPermissionIds.has(activePermission.id)) {
            return
          }
          void handleResolvePermission('deny', '')
        }}
        onResolve={handleResolvePermission}
      />
      <DesktopPlanModal
        open={planModal.open}
        plan={planModal.plan}
        revisions={planModal.revisions}
        historyLoading={planModal.historyLoading}
        saving={planModal.saving || planModal.loading}
        error={planModal.error}
        onOpenChange={(open) => {
          if (!open) {
            closePlanModal()
          }
        }}
        onCopy={handlePlanCopy}
        onSave={handlePlanSave}
      />
      {commitModal.open ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Save changes" className="z-[80] p-4 sm:p-6">
          <DialogBackdrop onClick={closeCommitModal} />
          <DialogPanel className="max-w-[min(680px,calc(100vw-24px))] rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:max-w-[min(720px,calc(100vw-48px))]">
            <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-6 py-5">
              <div className="min-w-0 flex-1">
                <h2 className="text-xl font-semibold tracking-tight text-[var(--app-text)]">Save changes</h2>
                <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                  Commit from the desktop header with Memory, or run an exact manual git commit directly.
                </p>
              </div>
              <ModalCloseButton onClick={closeCommitModal} aria-label="Close save dialog" />
            </div>
            <div className="grid gap-5 px-6 py-5">
              <div className="grid gap-2 sm:grid-cols-2">
                <button
                  type="button"
                  className={commitModal.mode === 'agent'
                    ? 'rounded-2xl border border-[var(--app-border-accent)] bg-[var(--app-bg-alt)] px-4 py-3 text-left'
                    : 'rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left hover:border-[var(--app-border-accent)]'}
                  onClick={() => handleCommitModeChange('agent')}
                >
                  <div className="text-sm font-semibold text-[var(--app-text)]">Memory agent</div>
                  <div className="mt-1 text-xs text-[var(--app-text-muted)]">Use Memory’s saved commit-capable tool contract.</div>
                </button>
                <button
                  type="button"
                  className={commitModal.mode === 'manual'
                    ? 'rounded-2xl border border-[var(--app-border-accent)] bg-[var(--app-bg-alt)] px-4 py-3 text-left'
                    : 'rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left hover:border-[var(--app-border-accent)]'}
                  onClick={() => handleCommitModeChange('manual')}
                >
                  <div className="text-sm font-semibold text-[var(--app-text)]">Manual commit</div>
                  <div className="mt-1 text-xs text-[var(--app-text-muted)]">Use the text below as the exact git commit message and run git commit directly.</div>
                </button>
              </div>
              <label className="grid gap-2">
                <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                  {commitModal.mode === 'manual' ? 'Exact commit message' : 'Extra commit instructions'}
                </span>
                <Textarea
                  value={commitModal.instructions}
                  onChange={(event) => handleCommitInstructionsChange(event.target.value)}
                  placeholder={commitModal.mode === 'manual'
                    ? 'Example: feat: add save modal'
                    : 'Optional: mention what should be emphasized in the commit message.'}
                  className="min-h-[140px] resize-y bg-[var(--app-bg-alt)]"
                />
              </label>
              <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
                {commitStatusLabel(commitModal) || (commitModal.mode === 'manual' ? 'Manual commit runs git commit --all directly in the workspace.' : 'Save runs in the background, including while the current session is still running.')}
              </div>
              {commitModal.error ? (
                <div className="rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">
                  {commitModal.error}
                </div>
              ) : null}
            </div>
            <div className="flex flex-wrap justify-end gap-2 border-t border-[var(--app-border)] px-6 py-4">
              <Button type="button" variant="ghost" onClick={closeCommitModal}>
                Close
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={() => {
                  void handleCommitSave()
                }}
                disabled={commitModal.status === 'starting' || commitModal.status === 'running' || !sessionId}
              >
                {commitModal.status === 'starting' || commitModal.status === 'running' ? <LoaderCircle size={16} className="animate-spin" /> : <Save size={16} />}
                {commitModal.mode === 'manual' ? 'Commit now' : 'Save now'}
              </Button>
            </div>
          </DialogPanel>
        </Dialog>
      ) : null}
    </Card>
  )
}
