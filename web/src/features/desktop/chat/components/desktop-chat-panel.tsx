import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type DragEvent as ReactDragEvent } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Clock3, ListChecks, LoaderCircle, Menu, Mic, Minimize2, Save, Send, Settings2, ShieldAlert, Sparkles, Square } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Textarea } from '../../../../components/ui/textarea'
import { useDesktopStore } from '../../state/use-desktop-store'
import {
  agentStateQueryOptions,
  draftModelQueryOptions,
  ensureSessionRuntimeData,
  modelOptionsQueryOptions,
  sessionMessagesQueryKey,
  sessionMessagesQueryOptions,
  sessionPreferenceQueryKey,
  sessionPreferenceQueryOptions,
} from '../../../queries/query-options'
import {
  activatePrimaryAgent,
  createSession,
  fetchActiveSessionPlan,
  resolveSessionPermission,
  saveSessionPlan,
  startSessionRun,
  updateSessionMetadata,
  updateDraftModelPreference,
  updateSessionMode,
  updateSessionPreference,
} from '../queries/chat-queries'
import type { AgentStateRecord, ChatMessageRecord, ResolvedSessionPreference, DesktopSessionPlanRecord } from '../types/chat'
import type { DesktopSessionRecord } from '../../types/realtime'
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
import { getUISettings } from '../../settings/swarm/queries/get-ui-settings'
import { normalizeDefaultNewSessionMode, normalizeThinkingTagsEnabled } from '../../settings/swarm/types/swarm-settings'
import { countApprovalRequiredPermissions, permissionRequiresApproval } from '../../permissions/services/permission-payload'
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
  loadDesktopChatRouteForSession,
  loadDesktopChatRouteForWorkspace,
  saveDesktopChatRouteForWorkspace,
} from '../services/chat-routing'
import { buildDesktopSlashPaletteState, type DesktopSlashCommand } from '../services/slash-commands'
import { appendPendingUserMessage, createPendingUserMessage, removePendingUserMessage } from '../services/message-cache'
import type { SettingsTabID } from '../../settings/types/settings-tabs'
import type { WorkspaceReplicationLink } from '../../../workspaces/launcher/types/workspace'

const THINKING_OPTIONS = ['off', 'low', 'medium', 'high', 'xhigh']
const FAST_ON_OFF_OPTIONS = ['off', 'on']
const TODO_DRAG_MIME = 'application/x-swarm-workspace-todo'
const DICTATION_RESTART_DELAY_MS = 180
const DICTATION_FINAL_FLUSH_MS = 450

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

export function metadataTodoSummary(metadata: Record<string, unknown> | null): { openCount: number; inProgressCount: number; taskCount: number } | null {
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
  return { taskCount, openCount, inProgressCount }
}

export function formatAgentTodoBadge(summary: { openCount: number; inProgressCount: number; taskCount: number } | null): string {
  if (!summary || summary.taskCount <= 0) {
    return ''
  }
  const total = Math.max(0, summary.taskCount)
  const open = Math.max(0, summary.openCount)
  const active = Math.max(0, summary.inProgressCount)
  const completed = Math.min(total, Math.max(0, total - open))
  if (open === 0) {
    return `Complete · ${completed}/${total}`
  }
  if (active > 0) {
    return `${completed}/${total} complete • ${active} active`
  }
  return `${completed}/${total} complete`
}

function lineageAgentName(label: string): string {
  const trimmed = label.trim()
  if (!trimmed) {
    return ''
  }
  const candidate = trimmed.startsWith('@') ? trimmed.slice(1).trim() : trimmed
  return candidate !== '' && !candidate.includes(' ') ? candidate : ''
}

function resolveSessionEffectiveAgentName(session: DesktopSessionRecord | null | undefined, fallbackPrimary: string): string {
  const metadata = metadataRecord(session?.metadata)
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

interface DesktopChatPanelProps {
  hostSwarmName: string
  workspacePath: string
  workspaceName: string
  workspaceWorktreeEnabled: boolean
  workspaceReplicationLinks: WorkspaceReplicationLink[]
  session: DesktopSessionRecord | null
  onSessionCreated: (session: DesktopSessionRecord) => void
  onOpenSettingsTab: (tab: SettingsTabID) => void
  onOpenPermissions: () => void
  onOpenWorkspaceLauncher: () => void
  onOpenSidebarMenu: () => void
  onStartNewSession: (workspacePath: string, workspaceName: string) => void
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
  saving: boolean
  error: string | null
  hasActive: boolean
  plan: DesktopSessionPlanRecord | null
}

function messageSort(left: ChatMessageRecord, right: ChatMessageRecord): number {
  return left.globalSeq - right.globalSeq
}

function dedupeMessages(messages: ChatMessageRecord[]): ChatMessageRecord[] {
  const map = new Map<number, ChatMessageRecord>()
  for (const message of messages) {
    map.set(message.globalSeq, message)
  }
  return Array.from(map.values()).sort(messageSort)
}

function messageRoleLabel(role: string): string {
  switch (role) {
    case 'user':
      return 'You'
    case 'assistant':
      return 'Swarm'
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

function optionKey(provider: string, model: string, contextMode = ''): string {
  return `${provider}:${model}:${contextMode.trim().toLowerCase()}`
}

function nearBottom(element: HTMLDivElement): boolean {
  return element.scrollHeight - element.scrollTop - element.clientHeight < 48
}

type RenderItem =
  | { type: 'message'; message: ChatMessageRecord; virtualKey?: string }
  | { type: 'live-tool'; toolMessage: NonNullable<ChatMessageRecord['toolMessage']> }
  | { type: 'live-assistant'; id: string; content: string }

function renderItemKey(item: RenderItem | undefined, index: number): string {
  if (!item) {
    return `render-item:${index}`
  }
  switch (item.type) {
    case 'message':
      return item.virtualKey ?? item.message.id
    case 'live-tool':
      return `live-tool:${item.toolMessage.callId || item.toolMessage.tool || 'active'}`
    case 'live-assistant':
      return item.id
    default:
      return `render-item:${index}`
  }
}

function estimateRenderItemSize(item: RenderItem | undefined, thinkingTagsEnabled = true): number {
  if (!item) {
    return 96
  }
  switch (item.type) {
    case 'live-tool': {
      const base = item.toolMessage.tool === 'task' && item.toolMessage.taskRows.length > 0 ? 168 : 104
      const detailLines = item.toolMessage.previewLines.length + item.toolMessage.taskRows.length
      return Math.min(520, base + (Math.max(detailLines - 1, 0) * 22))
    }
    case 'live-assistant':
      return Math.min(640, 88 + (Math.ceil(Math.max(item.content.length, 1) / 100) * 22))
    case 'message': {
      if (item.message.role === 'tool' && item.message.toolMessage) {
        const toolMessage = item.message.toolMessage
        const base = toolMessage.tool === 'task' && toolMessage.taskRows.length > 0
          ? 168
          : toolMessage.searchData
            ? 180
            : 104
        const detailLines = toolMessage.previewLines.length + toolMessage.taskRows.length + (toolMessage.searchData ? Math.min(toolMessage.searchData.files.length, 6) * 2 : 0)
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
  return ['tool.started', 'tool.delta', 'tool.completed', 'run.tool.started', 'run.tool.delta', 'run.tool.completed'].includes(eventType)
}

function hasRenderableToolSnapshot(snapshot: {
  toolName: string | null
  toolCallId: string | null
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

function buildLiveToolMessage(session: DesktopSessionRecord | null | undefined): NonNullable<ChatMessageRecord['toolMessage']> | null {
  const live = session?.live
  const lastEventType = live?.lastEventType?.trim() ?? ''
  const liveStatus = live?.status ?? 'idle'
  const activeSnapshot = live
    ? {
        toolName: live.toolName,
        toolCallId: live.toolCallId,
        toolArguments: live.toolArguments,
        toolOutput: live.toolOutput,
      }
    : null
  const retainedSnapshot = live
    ? {
        toolName: live.retainedToolName,
        toolCallId: live.retainedToolCallId,
        toolArguments: live.retainedToolArguments,
        toolOutput: live.retainedToolOutput,
      }
    : null

  const useActiveSnapshot = hasRenderableToolSnapshot(activeSnapshot)
    && (isLiveToolEventType(lastEventType) || ['starting', 'running', 'blocked'].includes(liveStatus))
  const snapshot = useActiveSnapshot ? activeSnapshot : hasRenderableToolSnapshot(retainedSnapshot) ? retainedSnapshot : null
  if (!snapshot) {
    return null
  }

  const toolName = snapshot.toolName?.trim() ?? ''
  if (!toolName) {
    return null
  }

  const state = useActiveSnapshot ? 'running' : (live?.retainedToolState ?? 'done')
  const toolMessage = buildStructuredToolMessage({
    tool: toolName,
    callId: snapshot.toolCallId ?? '',
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
  }
}

function hasCanonicalLiveToolReplacement(
  messages: ChatMessageRecord[],
  liveToolMessage: NonNullable<ChatMessageRecord['toolMessage']> | null,
): boolean {
  if (!liveToolMessage) {
    return false
  }
  const liveCallID = liveToolMessage.callId.trim()
  if (!liveCallID) {
    return false
  }
  return messages.some((message) => {
    const toolMessage = message.toolMessage
    if (!toolMessage) {
      return false
    }
    return toolMessage.callId.trim() === liveCallID
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

function reasoningElapsedLabel(startedAt: number | null, timerNow: number): string {
  if (typeof startedAt !== 'number' || startedAt <= 0) {
    return ''
  }
  return formatDurationCompact(timerNow - startedAt)
}

function reasoningHeadline(state: DesktopSessionRecord['live']['reasoningState'], startedAt: number | null, timerNow: number): string {
  const label = reasoningStateLabel(state)
  const elapsed = reasoningElapsedLabel(startedAt, timerNow)
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

function manualCommitToolScope(userInstructions: string): NonNullable<Parameters<typeof startSessionRun>[0]['toolScope']> {
  const prefixes = ['git status', 'git diff', 'git log', 'git show', 'git add', 'git commit']
  if (userInstructions.toLowerCase().includes('push')) {
    prefixes.push('git push')
  }
  return {
    preset: 'background_commit',
    bash_prefixes: prefixes,
  }
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
  saving: false,
  error: null,
  hasActive: false,
  plan: null,
}

function commitStatusLabel(state: CommitModalState): string {
  switch (state.status) {
    case 'starting':
      return 'Starting save…'
    case 'running':
      return state.mode === 'manual' ? 'Manual commit running…' : 'Memory commit running…'
    case 'success':
      return 'Save completed. You can save again.'
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
  workspaceWorktreeEnabled,
  workspaceReplicationLinks,
  session,
  onSessionCreated,
  onOpenSettingsTab,
  onOpenPermissions,
  onOpenWorkspaceLauncher,
  onOpenSidebarMenu,
  onStartNewSession,
}: DesktopChatPanelProps) {
  const queryClient = useQueryClient()
  const sessionId = session?.id ?? null
  const upsertSession = useDesktopStore((state) => state.upsertSession)
  const refreshSessionPermissions = useDesktopStore((state) => state.refreshSessionPermissions)
  const ensureRunStream = useDesktopStore((state) => state.ensureRunStream)
  const submitPrompt = useDesktopStore((state) => state.submitPrompt)
  const stopRun = useDesktopStore((state) => state.stopRun)
  const setSessionDraft = useDesktopStore((state) => state.setSessionDraft)
  const setSessionDraftMode = useDesktopStore((state) => state.setSessionDraftMode)
  const [panelError, setPanelError] = useState<string | null>(null)
  const [selectedPrimaryAgent, setSelectedPrimaryAgent] = useState('swarm')
  const [currentSessionAgent, setCurrentSessionAgent] = useState('swarm')
  const lastActivatedAgentRef = useRef('')
  const lastAutoModeSyncRef = useRef('')
  const [permissionError, setPermissionError] = useState<string | null>(null)
  const [resolvingPermissionIds, setResolvingPermissionIds] = useState<Set<string>>(() => new Set())
  const [lastSavedRulePreview, setLastSavedRulePreview] = useState<string | null>(null)
  const [commitModal, setCommitModal] = useState<CommitModalState>(EMPTY_COMMIT_MODAL_STATE)
  const [planModal, setPlanModal] = useState<PlanModalState>(EMPTY_PLAN_MODAL_STATE)
  const [timerNow, setTimerNow] = useState(() => Date.now())
  const [thinkingTagsEnabled, setThinkingTagsEnabled] = useState(true)
  const [thinkingTagsSaving, setThinkingTagsSaving] = useState(false)
  const [defaultNewSessionMode, setDefaultNewSessionMode] = useState<'auto' | 'plan'>('auto')

  const scrollerRef = useRef<HTMLDivElement | null>(null)
  const shouldStickToBottomRef = useRef(true)
  const scrollToLatestFrameRef = useRef<number | null>(null)
  const liveAssistantHandoffRef = useRef<{ sessionId: string; content: string; key: string } | null>(null)
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

  const storedSession = useDesktopStore((state) => (sessionId ? state.sessions[sessionId] ?? null : null))
  const liveSession = useDesktopStore((state) => (sessionId ? state.sessions[sessionId] ?? session : session))
  const trackedCommitSession = useDesktopStore((state) => (commitModal.targetSessionId ? state.sessions[commitModal.targetSessionId] ?? null : null))
  const draftSessionMode = useDesktopStore((state) => state.getSessionDraftMode(null, workspacePath))
  const draftSessionKey = `__workspace__:${workspacePath}`
  const routeOptions = useMemo(() => buildDesktopChatRouteOptions({
    hostSwarmName,
    workspacePath,
    workspaceName,
    replicationLinks: workspaceReplicationLinks,
  }), [hostSwarmName, workspacePath, workspaceName, workspaceReplicationLinks])
  const defaultChatRoute = routeOptions[0]!
  const [selectedRouteId, setSelectedRouteId] = useState(() => defaultChatRoute?.id ?? 'host')

  const cachedMessages = sessionId ? queryClient.getQueryData<ChatMessageRecord[]>(sessionMessagesQueryOptions(sessionId).queryKey) : undefined
  const cachedPreference = sessionId ? queryClient.getQueryData<ResolvedSessionPreference>(sessionPreferenceQueryOptions(sessionId).queryKey) : undefined

  const messagesQuery = useQuery({
    ...sessionMessagesQueryOptions(sessionId ?? ''),
    initialData: cachedMessages,
  })

  const sessionPreferenceQuery = useQuery({
    ...sessionPreferenceQueryOptions(sessionId ?? ''),
    initialData: cachedPreference,
  })

  const draftPreferenceQuery = useQuery({
    ...draftModelQueryOptions(),
    enabled: sessionId === null,
    initialData: queryClient.getQueryData<ResolvedSessionPreference>(draftModelQueryOptions().queryKey),
  })

  const sessionPreference = sessionPreferenceQuery.data ?? emptyPreference()
  const draftPreference = draftPreferenceQuery.data ?? emptyPreference()
  const activePreferenceRecord = sessionId ? sessionPreference : draftPreference

  const composer = useDesktopStore((state) => state.getSessionDraft(sessionId, workspacePath))
  const composerDraftKey = sessionId ?? draftSessionKey
  const setComposerDraft = useCallback((value: string) => {
    setSessionDraft(composerDraftKey, value)
  }, [composerDraftKey, setSessionDraft])
  const slashPalette = useMemo(() => buildDesktopSlashPaletteState(composer), [composer])
  const [slashSelectionIndex, setSlashSelectionIndex] = useState(0)
  const [mentionSelectionIndex, setMentionSelectionIndex] = useState(0)
  const [modelPickerOpenSignal, setModelPickerOpenSignal] = useState(0)
  const [mobileSettingsOpen, setMobileSettingsOpen] = useState(false)
  const [intermediateSettingsOpen, setIntermediateSettingsOpen] = useState(false)
  const mobileSettingsRef = useRef<HTMLDivElement>(null)
  const mobileSettingsTriggerRef = useRef<HTMLButtonElement>(null)
  const intermediateSettingsRef = useRef<HTMLDivElement>(null)
  const intermediateSettingsTriggerRef = useRef<HTMLButtonElement>(null)

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
      if (error === 'no-speech' && dictationEnabledRef.current) {
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
    if (!mobileSettingsOpen && !intermediateSettingsOpen) return
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node
      if (
        mobileSettingsRef.current?.contains(target) ||
        mobileSettingsTriggerRef.current?.contains(target) ||
        intermediateSettingsRef.current?.contains(target) ||
        intermediateSettingsTriggerRef.current?.contains(target) ||
        !document.getElementById('root')?.contains(target)
      ) {
        return
      }
      setMobileSettingsOpen(false)
      setIntermediateSettingsOpen(false)
    }
    const handleEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMobileSettingsOpen(false)
        setIntermediateSettingsOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleEscape)
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [mobileSettingsOpen, intermediateSettingsOpen])

  useEffect(() => {
    const storedRoute = sessionId
      ? loadDesktopChatRouteForSession(sessionId)
      : loadDesktopChatRouteForWorkspace(workspacePath)
    const nextRoute = routeOptions.find((entry) => desktopChatRoutesEqual(entry, storedRoute)) ?? defaultChatRoute
    const nextRouteId = nextRoute?.id ?? 'host'
    if (nextRouteId !== selectedRouteId) {
      setSelectedRouteId(nextRouteId)
    }
  }, [defaultChatRoute, routeOptions, selectedRouteId, sessionId, workspacePath])

  const activeChatRoute = useMemo(
    () => routeOptions.find((entry) => entry.id === selectedRouteId) ?? defaultChatRoute,
    [defaultChatRoute, routeOptions, selectedRouteId],
  )
  const showRoutePicker = routeOptions.length > 1

  const refreshUISettings = useCallback(async () => {
    try {
      const settings = await getUISettings()
      setThinkingTagsEnabled(normalizeThinkingTagsEnabled(settings))
      setDefaultNewSessionMode(normalizeDefaultNewSessionMode(settings.chat?.default_new_session_mode))
    } catch {
      setThinkingTagsEnabled(true)
      setDefaultNewSessionMode('auto')
    }
  }, [])

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

  const messages = useMemo(() => dedupeMessages(messagesQuery.data ?? []), [messagesQuery.data])
  const displayedMessages = messages
  const liveAssistantDraft = liveSession?.live.assistantDraft ?? ''
  const retainedAssistantSegments = liveSession?.live.retainedAssistantSegments ?? []
  const liveToolMessage = useMemo(() => buildLiveToolMessage(liveSession), [liveSession])
  const shouldRenderLiveToolMessage = useMemo(
    () => liveToolMessage !== null && !hasCanonicalLiveToolReplacement(displayedMessages, liveToolMessage),
    [displayedMessages, liveToolMessage],
  )
  const shouldRenderLiveAssistantDraft =
    liveAssistantDraft !== '' && !(shouldRenderLiveToolMessage && liveToolMessage?.tool.trim().toLowerCase() === 'task')
  const loadingMessages = sessionId !== null && messagesQuery.isLoading && messages.length === 0
  const lifecycle = liveSession?.lifecycle ?? null
  const lifecyclePhase = lifecycle?.phase.trim().toLowerCase() ?? ''
  const lifecycleStopReason = lifecycle?.stopReason?.trim() ?? ''
  const lifecycleActive = Boolean(lifecycle?.active)
  const lifecycleRunId = lifecycleActive ? lifecycle?.runId?.trim() ?? '' : ''
  const liveRunId = liveSession?.live.runId?.trim() ?? ''
  const liveAssistantDraftKey = `live-assistant:${liveRunId || 'draft'}`
  const reconnectingRun = !lifecycleActive
    && lifecyclePhase === ''
    && liveRunId !== ''
    && liveSession?.live.summary === 'Reconnecting…'
  const lifecycleStartedAt = lifecycleActive && typeof lifecycle?.startedAt === 'number' && lifecycle.startedAt > 0 ? lifecycle.startedAt : 0
  const awaitingLifecycleStart = !lifecycleActive && (Boolean(liveSession?.live.awaitingAck) || liveSession?.live.status === 'starting')
  const resumableRunId = lifecycleRunId || liveRunId
  const submitting = awaitingLifecycleStart || reconnectingRun || (lifecycleActive && lifecyclePhase === 'starting')
  const canStop = (lifecycleActive && lifecycleRunId !== '') || reconnectingRun || (awaitingLifecycleStart && liveRunId !== '')
  const showRunTimer = lifecycleActive && lifecycleStartedAt > 0
  const runTimerLabel = showRunTimer ? formatDurationCompact(timerNow - lifecycleStartedAt) : reconnectingRun ? 'Reconnecting…' : ''
  const composerDisabled = awaitingLifecycleStart || reconnectingRun || (lifecycleActive && lifecyclePhase === 'starting')
  const runActive = canStop || submitting || lifecycleActive
  const showDictationButton = !runActive && dictationSupported
  const dictationButtonDisabled = composerDisabled
  const dictationComposer = dictationEnabled
    ? appendDictationText(appendDictationText(dictationBaseDraftRef.current, dictationFinalTranscriptRef.current), dictationInterimTranscriptRef.current)
    : composer
  useEffect(() => {
    dictationCanRunRef.current = showDictationButton && !composerDisabled
    if (!dictationCanRunRef.current && dictationEnabledRef.current) {
      stopDictation(true)
    }
  }, [composerDisabled, showDictationButton, stopDictation])

  const contextBadgeLabel = formatContextUsageBadgeLabel(liveSession?.usage ?? null)
  const contextBadgeTooltip = formatContextUsageTooltip(liveSession?.usage ?? null)
  const agentTodoSummary = metadataTodoSummary(metadataRecord(liveSession?.metadata))
  const agentTodoBadgeLabel = formatAgentTodoBadge(agentTodoSummary)
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
    for (const segment of retainedAssistantSegments) {
      if (segment.content.trim() !== '') {
        items.push({ type: 'live-assistant', id: segment.id, content: segment.content })
      }
    }
    if (shouldRenderLiveToolMessage && liveToolMessage) {
      items.push({ type: 'live-tool', toolMessage: liveToolMessage })
    }
    if (shouldRenderLiveAssistantDraft) {
      items.push({ type: 'live-assistant', id: liveAssistantDraftKey, content: liveAssistantDraft })
    }
    return items
  }, [displayedMessages, liveAssistantDraft, liveAssistantDraftKey, liveToolMessage, retainedAssistantSegments, sessionId, shouldRenderLiveAssistantDraft, shouldRenderLiveToolMessage])
  const renderMeasurementKey = useMemo(
    () => renderItems.map((item) => {
      switch (item.type) {
        case 'message':
          return item.virtualKey
            ? `la:${item.message.content.length}`
            : `m:${item.message.id}:${item.message.content.length}`
        case 'live-tool':
          return `lt:${item.toolMessage.callId || item.toolMessage.tool}:${item.toolMessage.output.length}:${item.toolMessage.completedOutput.length}:${item.toolMessage.taskRows.length}`
        case 'live-assistant':
          return `la:${item.content.length}`
        default:
          return 'unknown'
      }
    }).join('|'),
    [renderItems],
  )
  const rowVirtualizer = useVirtualizer({
    count: renderItems.length,
    getScrollElement: () => scrollerRef.current,
    estimateSize: (index) => estimateRenderItemSize(renderItems[index], thinkingTagsEnabled),
    getItemKey: (index) => renderItemKey(renderItems[index], index),
    overscan: 6,
  })
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
  const selectedPrimaryAgentProfile = useMemo(
    () => selectableAgents.find((profile) => profile.name === selectedPrimaryAgent) ?? null,
    [selectableAgents, selectedPrimaryAgent],
  )
  const currentPrimaryAgentProfile = useMemo(
    () => selectableAgents.find((profile) => profile.name === agentState.activePrimary.trim()) ?? null,
    [agentState.activePrimary, selectableAgents],
  )
  const activeModeSourceProfile = selectedPrimaryAgentProfile ?? currentPrimaryAgentProfile
  const agentDerivedMode = useMemo(
    () => desiredSessionModeForAgent(activeModeSourceProfile, liveSession?.mode ?? session?.mode ?? draftSessionMode ?? 'plan'),
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
  const selectedModelAvailable = selectedModelKey !== '' && resolvedModelOptions.some((option) => option.key === selectedModelKey)
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
  const persistScrollState = useCallback(() => {
    const scroller = scrollerRef.current
    if (!scroller) {
      return
    }
    shouldStickToBottomRef.current = nearBottom(scroller)
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
    scrollToLatest()
  }, [scrollToLatest])

  useEffect(() => {
    if (selectableAgents.length === 0) {
      if (selectedPrimaryAgent !== 'swarm') {
        setSelectedPrimaryAgent('swarm')
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
  }, [agentState.activePrimary, liveSession, selectableAgents, selectedPrimaryAgent, session])

  useEffect(() => {
    if (!sessionId) {
      return
    }
    const nextAgent = selectableAgents.some((profile) => profile.name === resolvedSessionAgent)
      ? resolvedSessionAgent
      : agentState.activePrimary.trim() || selectableAgents[0]?.name || 'swarm'
    setCurrentSessionAgent(nextAgent)
  }, [agentState.activePrimary, selectableAgents, resolvedSessionAgent, sessionId])

  useEffect(() => {
    if (sessionId) {
      return
    }
    setCurrentSessionAgent(selectedPrimaryAgent)
  }, [selectedPrimaryAgent, sessionId])

  useEffect(() => {
    if (!activeModeSourceProfile) {
      return
    }
    const nextMode = desiredSessionModeForAgent(activeModeSourceProfile, liveSession?.mode ?? session?.mode ?? draftSessionMode)
    if (sessionId) {
      const currentMode = normalizeSessionMode(useDesktopStore.getState().sessions[sessionId]?.mode ?? liveSession?.mode ?? session?.mode ?? nextMode)
      if (currentMode === nextMode) {
        return
      }
      if (activeModeSourceProfile.exitPlanModeEnabled) {
        if (currentMode === 'plan' || currentMode === 'auto') {
          return
        }
      }
      useDesktopStore.setState((state) => {
        const current = state.sessions[sessionId]
        if (!current || normalizeSessionMode(current.mode) === nextMode) {
          return state
        }
        return {
          sessions: {
            ...state.sessions,
            [sessionId]: {
              ...current,
              mode: nextMode,
            },
          },
        }
      })
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
  }, [activeModeSourceProfile, draftSessionKey, draftSessionMode, liveSession?.mode, session?.mode, sessionId, setSessionDraftMode])

  useEffect(() => {
    if (!sessionId || !activeModeSourceProfile) {
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
    void updateSessionMode(sessionId, nextMode).then((resolvedModeRaw) => {
      const resolvedMode = normalizeSessionMode(resolvedModeRaw)
      useDesktopStore.setState((state) => {
        const current = state.sessions[sessionId]
        if (!current) {
          return state
        }
        return {
          sessions: {
            ...state.sessions,
            [sessionId]: {
              ...current,
              mode: resolvedMode,
            },
          },
        }
      })
      lastAutoModeSyncRef.current = ''
    }).catch((error) => {
      lastAutoModeSyncRef.current = ''
      setPanelError(error instanceof Error ? error.message : 'Failed to update session mode')
    })
  }, [activeModeSourceProfile, draftSessionMode, liveSession?.mode, session?.mode, sessionId])

  useEffect(() => {
    if (!selectedPrimaryAgentProfile) {
      return
    }
    const effectiveAgent = resolveSessionEffectiveAgentName(liveSession ?? session, agentState.activePrimary).trim()
    if (effectiveAgent && effectiveAgent === selectedPrimaryAgentProfile.name.trim()) {
      lastActivatedAgentRef.current = ''
      return
    }
    if (selectedPrimaryAgentProfile.mode !== 'primary') {
      lastActivatedAgentRef.current = ''
      return
    }
    const currentPrimary = agentState.activePrimary.trim()
    const nextSelectedAgent = selectedPrimaryAgentProfile.name.trim()
    if (nextSelectedAgent === '' || currentPrimary === '' || currentPrimary === nextSelectedAgent) {
      lastActivatedAgentRef.current = ''
      return
    }
    if (lastActivatedAgentRef.current === nextSelectedAgent) {
      return
    }
    lastActivatedAgentRef.current = nextSelectedAgent
    void activatePrimaryAgent(nextSelectedAgent).catch((error) => {
      console.error('[desktop-chat] activate primary agent failed', error)
      lastActivatedAgentRef.current = ''
      setPanelError(error instanceof Error ? error.message : 'Failed to activate agent')
    })
  }, [agentState.activePrimary, liveSession, selectedPrimaryAgentProfile, session])

  const handleAgentSelect = useCallback(async (value: string) => {
    const nextAgent = value.trim() || 'swarm'
    const previousAgent = currentSessionAgent
    setPanelError(null)
    setSelectedPrimaryAgent(nextAgent)
    setCurrentSessionAgent(nextAgent)
    if (!sessionId) {
      return
    }
    const liveSessionMetadata = metadataRecord(liveSession?.metadata)
    const currentMetadata = liveSessionMetadata
      ? { ...liveSessionMetadata }
      : {}
    currentMetadata.subagent = nextAgent
    try {
      const updatedSession = await updateSessionMetadata(sessionId, currentMetadata)
      upsertSession(updatedSession)
    } catch (error) {
      setCurrentSessionAgent(previousAgent)
      setPanelError(error instanceof Error ? error.message : 'Failed to update session agent')
    }
  }, [currentSessionAgent, liveSession?.metadata, sessionId, upsertSession])

  useEffect(() => {
    setPanelError(null)
    if (sessionId) {
      void ensureSessionRuntimeData(queryClient, sessionId)
      void refreshSessionPermissions(sessionId)
    }
  }, [queryClient, refreshSessionPermissions, sessionId])

  useEffect(() => {
    if (!sessionId || !resumableRunId) {
      return
    }
    if (!lifecycleActive && !reconnectingRun && !awaitingLifecycleStart) {
      return
    }
    void ensureRunStream(sessionId, resumableRunId)
  }, [awaitingLifecycleStart, ensureRunStream, lifecycleActive, reconnectingRun, resumableRunId, sessionId])

  useEffect(() => {
    if (!session || storedSession) {
      return
    }
    upsertSession(session)
  }, [session, storedSession, upsertSession])

  useEffect(() => {
    if (!messagesQuery.error) {
      return
    }
    setPanelError(messagesQuery.error instanceof Error ? messagesQuery.error.message : 'Failed to load conversation')
  }, [messagesQuery.error])

  useEffect(() => {
    if (!commitModal.targetSessionId || commitModal.status !== 'running') {
      return
    }
    const trackedSession = trackedCommitSession
    if (!trackedSession) {
      return
    }
    const lifecycleActive = Boolean(trackedSession.lifecycle?.active)
    const liveStatus = trackedSession.live.status
    if (lifecycleActive || liveStatus === 'starting' || liveStatus === 'running' || liveStatus === 'blocked') {
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
  }, [commitModal.status, commitModal.targetSessionId, trackedCommitSession])

  useEffect(() => {
    if (sessionId && shouldRenderLiveAssistantDraft && liveAssistantDraft !== '') {
      liveAssistantHandoffRef.current = { sessionId, content: liveAssistantDraft, key: liveAssistantDraftKey }
    } else if (!sessionId) {
      liveAssistantHandoffRef.current = null
    }
  }, [liveAssistantDraft, liveAssistantDraftKey, sessionId, shouldRenderLiveAssistantDraft])

  useEffect(() => {
    if (shouldStickToBottomRef.current) {
      scrollToLatest()
    }
  }, [messages, renderMeasurementKey, scrollToLatest, sessionId])

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
    rowVirtualizer.measure()
  }, [rowVirtualizer, thinkingTagsEnabled])

  const handlePreferenceChange = useCallback(async (next: ResolvedSessionPreference['preference']) => {
    setPanelError(null)
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

    queryClient.setQueryData(sessionPreferenceQueryOptions(sessionId).queryKey, (current: ResolvedSessionPreference | undefined) => ({
      ...(current ?? activePreferenceRecord),
      preference: {
        ...(current?.preference ?? activePreferenceRecord.preference),
        ...normalizedNext,
      },
    }))

    try {
      const resolved = await updateSessionPreference(sessionId, normalizedNext)
      queryClient.setQueryData(sessionPreferenceQueryOptions(sessionId).queryKey, resolved)
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to update model settings')
    }
  }, [activePreferenceRecord, queryClient, sessionId])

  const handleModelChange = useCallback((value: string) => {
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
  }, [activePreferenceRecord.preference, handlePreferenceChange, resolvedModelOptions])

  const handleThinkingChange = useCallback((value: string) => {
    void handlePreferenceChange({
      ...activePreferenceRecord.preference,
      thinking: value,
    })
  }, [activePreferenceRecord.preference, handlePreferenceChange])

  const handleFastChange = useCallback((value: string) => {
    const nextFast = normalizeFastToggle(value)
    void handlePreferenceChange(buildFastPreference(activePreferenceRecord.preference, nextFast))
  }, [activePreferenceRecord.preference, handlePreferenceChange])

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
        const updatedSession = await updateSessionMetadata(sessionId, currentMetadata)
        upsertSession(updatedSession)
      }
      await submitPrompt({
        sessionId,
        workspacePath,
        workspaceName,
        prompt: parsed.note,
        agentName: currentSessionAgent,
        compact: true,
      })
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to compact context')
    }
  }, [canStop, currentSessionAgent, liveSession?.metadata, sessionId, submitPrompt, submitting, upsertSession, workspaceName, workspacePath])

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

  const openPlanModal = useCallback(async () => {
    if (!sessionId) {
      setPlanModal({
        open: true,
        loading: false,
        saving: false,
        error: 'Open or create a session before using /plan.',
        hasActive: false,
        plan: null,
      })
      return
    }
    setPanelError(null)
    setPlanModal((current) => ({
      ...current,
      open: true,
      loading: true,
      saving: false,
      error: null,
    }))
    try {
      const result = await fetchActiveSessionPlan(sessionId)
      setPlanModal({
        open: true,
        loading: false,
        saving: false,
        error: null,
        hasActive: result.hasActive,
        plan: result.hasActive
          ? result.plan
          : {
              id: '',
              title: 'Current Plan',
              plan: '',
              status: 'draft',
              approvalState: '',
              updatedAt: 0,
            },
      })
    } catch (error) {
      setPlanModal({
        open: true,
        loading: false,
        saving: false,
        error: error instanceof Error ? error.message : 'Failed to load current plan',
        hasActive: false,
        plan: null,
      })
    }
  }, [sessionId])

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
      setThinkingTagsEnabled(normalizeThinkingTagsEnabled(updated))
    } catch (error) {
      setThinkingTagsEnabled(previous)
      setPanelError(error instanceof Error ? error.message : 'Failed to update thinking tags setting')
    } finally {
      setThinkingTagsSaving(false)
    }
  }, [thinkingTagsEnabled, thinkingTagsSaving])

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
  }, [activePreferenceRecord.preference, canStop, composer, handleCompact, handlePreferenceChange, onOpenPermissions, onOpenSettingsTab, onOpenWorkspaceLauncher, onStartNewSession, openCommitModal, openPlanModal, sessionId, setSessionDraft, submitting, workspaceName, workspacePath])

  const handleSlashInsert = useCallback((command: DesktopSlashCommand) => {
    setSessionDraft(sessionId ?? `__workspace__:${workspacePath}`, `${command.command} `)
    setSlashSelectionIndex(0)
  }, [sessionId, setSessionDraft, workspacePath])

  const handleRouteChange = useCallback((routeId: string) => {
    const nextRoute = routeOptions.find((entry) => entry.id === routeId)
    if (!nextRoute) {
      return
    }
    setSelectedRouteId(nextRoute.id)
    saveDesktopChatRouteForWorkspace(workspacePath, nextRoute)
    if (!sessionId) {
      return
    }
    const currentSessionRoute = routeOptions.find((entry) => desktopChatRoutesEqual(entry, loadDesktopChatRouteForSession(sessionId))) ?? defaultChatRoute
    if (desktopChatRoutesEqual(currentSessionRoute, nextRoute)) {
      return
    }
    setPanelError(null)
    setSessionDraft(draftSessionKey, composer)
    onStartNewSession(workspacePath, workspaceName)
  }, [composer, defaultChatRoute, draftSessionKey, onStartNewSession, routeOptions, sessionId, setSessionDraft, workspaceName, workspacePath])

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
    const prompt = useDesktopStore.getState().getSessionDraft(sessionId, workspacePath).trim()
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
    let pendingMessageId = ''
    try {
      if (!canSendWithSelectedPreference) {
        throw new Error('Select an authenticated model and thinking level before sending')
      }
      if (!targetSession) {
        targetSession = await createSession({
          workspacePath: activeChatRoute.runtimeWorkspacePath,
          workspaceName,
          mode: effectiveSessionMode,
          agentName: currentSessionAgent,
          preference: activePreferenceRecord.preference,
          route: activeChatRoute,
          worktreeMode: activeChatRoute.swarmId && workspaceWorktreeEnabled ? 'on' : undefined,
        })
        upsertSession(targetSession)
        queryClient.setQueryData(sessionPreferenceQueryKey(targetSession.id), {
          ...activePreferenceRecord,
          preference: {
            ...activePreferenceRecord.preference,
          },
        })
        onSessionCreated(targetSession)
      }

      const cachedTargetMessages = queryClient.getQueryData<ChatMessageRecord[]>(sessionMessagesQueryKey(targetSession.id)) ?? []
      const pendingMessage = createPendingUserMessage(
        targetSession.id,
        prompt,
        cachedTargetMessages[cachedTargetMessages.length - 1]?.globalSeq ?? 0,
      )
      pendingMessageId = pendingMessage.id
      queryClient.setQueryData(sessionMessagesQueryKey(targetSession.id), (current: ChatMessageRecord[] | undefined) => appendPendingUserMessage(current, pendingMessage))

      await submitPrompt({
        sessionId: targetSession.id,
        workspacePath,
        workspaceName,
        prompt: runPrompt,
        agentName: currentSessionAgent,
        targetKind: runTargetKind,
        targetName: runTargetName,
      })
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to send prompt')
      if (targetSession?.id) {
        if (pendingMessageId) {
          queryClient.setQueryData(sessionMessagesQueryKey(targetSession.id), (current: ChatMessageRecord[] | undefined) => removePendingUserMessage(current, pendingMessageId))
        }
        void queryClient.invalidateQueries({ queryKey: sessionMessagesQueryKey(targetSession.id) })
      }
    }
  }, [activeChatRoute, activePreferenceRecord, canSendWithSelectedPreference, commitDictationDraft, currentSessionAgent, mentionSubagents, onSessionCreated, queryClient, session, sessionId, stopDictation, submitPrompt, submitting, upsertSession, workspaceName, workspacePath, workspaceWorktreeEnabled])

  const handleStop = useCallback(async () => {
    if (!sessionId) {
      return
    }
    setPanelError(null)
    try {
      await stopRun(sessionId)
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to stop run')
    }
  }, [sessionId, stopRun])

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

  const handlePlanSave = useCallback(async (planText: string) => {
    if (!sessionId) {
      setPlanModal((current) => ({ ...current, error: 'Session id is unavailable.' }))
      return
    }
    const currentPlan = planModal.plan
    setPlanModal((current) => ({ ...current, saving: true, error: null }))
    setPanelError(null)
    try {
      const saved = await saveSessionPlan(sessionId, {
        id: currentPlan?.id,
        title: (currentPlan?.title?.trim() || 'Current Plan'),
        plan: planText,
        status: (currentPlan?.status?.trim() || 'draft'),
        approvalState: currentPlan?.approvalState,
      })
      setPlanModal({
        open: true,
        loading: false,
        saving: false,
        error: null,
        hasActive: true,
        plan: saved,
      })
    } catch (error) {
      setPlanModal((current) => ({
        ...current,
        saving: false,
        error: error instanceof Error ? error.message : 'Failed to save current plan',
      }))
    }
  }, [planModal.plan, sessionId])

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
    if (session.lifecycle?.active) {
      setCommitModal((current) => ({
        ...current,
        status: 'error',
        error: 'Wait for the current run to finish before saving again.',
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
      let toolScope: Parameters<typeof startSessionRun>[0]['toolScope'] | undefined

      if (commitModal.mode === 'agent') {
        const createdSession = await createSession({
          title: childCommitSessionTitle(session, instructions),
          workspacePath: activeChatRoute.runtimeWorkspacePath,
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
        upsertSession(createdSession)
        prompt = 'Review the git diff in scope, prepare the right staged set, and create the commit now.'
        runInstructions = buildCommitAgentInstructions(instructions)
        targetKind = 'background'
        targetName = 'memory'
      } else {
        prompt = instructions || 'Review the git diff in scope, stage the appropriate files, and create the commit now.'
        runInstructions = instructions
        agentName = 'swarm'
        toolScope = manualCommitToolScope(instructions)
      }

      const accepted = await startSessionRun({
        sessionId: targetSession.id,
        prompt,
        agentName,
        instructions: runInstructions,
        background: true,
        compact: false,
        targetKind,
        targetName,
        toolScope,
        executionContext,
      })

      if (commitModal.mode === 'agent') {
        setCommitModal({
          ...EMPTY_COMMIT_MODAL_STATE,
          open: false,
        })
        return
      }

      setCommitModal((current) => ({
        ...current,
        status: 'running',
        error: null,
        runId: accepted.run_id?.trim() || null,
        targetSessionId: targetSession.id,
        open: true,
      }))
      if (commitModal.mode === 'manual') {
        upsertSession({
          ...session,
          live: {
            ...session.live,
            status: 'starting',
            runId: accepted.run_id?.trim() || session.live.runId,
            awaitingAck: true,
            agentName: 'swarm',
            summary: 'Starting save…',
            error: null,
          },
        })
      }
    } catch (error) {
      setCommitModal((current) => ({
        ...current,
        status: 'error',
        error: error instanceof Error ? error.message : 'Failed to start save run.',
      }))
    }
  }, [activeChatRoute, activePreferenceRecord.preference, commitModal.instructions, commitModal.mode, selectedPrimaryAgent, session, upsertSession, workspaceName])


  const handleModeChange = useCallback(async (nextMode: 'plan' | 'auto') => {
    if (!selectedPrimaryAgentProfile?.exitPlanModeEnabled) {
      return
    }
    setPanelError(null)
    if (!sessionId) {
      setSessionDraftMode(draftSessionKey, nextMode)
      return
    }
    const previousMode = normalizeSessionMode(useDesktopStore.getState().sessions[sessionId]?.mode ?? sessionMode)
    useDesktopStore.setState((state) => {
      const current = state.sessions[sessionId]
      if (!current) {
        return state
      }
      return {
        sessions: {
          ...state.sessions,
          [sessionId]: {
            ...current,
            mode: nextMode,
          },
        },
      }
    })
    try {
      const resolvedMode = normalizeSessionMode(await updateSessionMode(sessionId, nextMode))
      useDesktopStore.setState((state) => {
        const current = state.sessions[sessionId]
        if (!current) {
          return state
        }
        return {
          sessions: {
            ...state.sessions,
            [sessionId]: {
              ...current,
              mode: resolvedMode,
            },
          },
        }
      })
    } catch (error) {
      setPanelError(error instanceof Error ? error.message : 'Failed to update session mode')
      useDesktopStore.setState((state) => {
        const current = state.sessions[sessionId]
        if (!current) {
          return state
        }
        return {
          sessions: {
            ...state.sessions,
            [sessionId]: {
              ...current,
              mode: previousMode,
            },
          },
        }
      })
    }
  }, [draftSessionKey, selectedPrimaryAgentProfile?.exitPlanModeEnabled, sessionId, sessionMode, setSessionDraftMode])

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
      const resolved = await resolveSessionPermission(sessionId, permissionId, action, reason, approvedArguments)
      useDesktopStore.setState((state) => {
        const existing = state.sessions[sessionId]
        if (!existing) {
          return state
        }
        const nextPermissions = existing.pendingPermissions
          .filter((item) => item.id !== resolved.id)
          .concat(resolved.status === 'pending' ? [resolved] : [])
        return {
          sessions: {
            ...state.sessions,
            [sessionId]: {
              ...existing,
              pendingPermissions: nextPermissions,
              pendingPermissionCount: countApprovalRequiredPermissions(nextPermissions, existing.mode),
            },
          },
        }
      })
      setLastSavedRulePreview(resolved.savedRule
        ? [resolved.savedRule.decision, resolved.savedRule.kind === 'bash_prefix' ? 'bash prefix:' : resolved.savedRule.kind === 'phrase' ? 'phrase:' : 'tool:', resolved.savedRule.kind === 'phrase' ? (resolved.savedRule.pattern || '') : resolved.savedRule.kind === 'bash_prefix' ? (resolved.savedRule.pattern || '') : (resolved.savedRule.tool || '')].filter(Boolean).join(' ')
        : null)
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
  }, [activePermission, sessionId])

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

  const placeholder = mentionPaletteIsActive
    ? 'Pick a subagent, then keep typing the task…'
    : sessionId
      ? 'Reply to this conversation…'
      : `Start a new conversation in ${workspaceName || workspacePath}`

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
    <Card className="flex h-full w-full flex-1 min-h-0 min-w-0 flex-col overflow-hidden rounded-none border-0 bg-[var(--app-surface)]">
      <header className="shrink-0 flex min-h-[60px] items-center gap-2 border-b border-[var(--app-border)] px-3 pb-2 pt-[calc(var(--app-safe-area-top)_+_0.5rem)] sm:h-[60px] sm:px-4 sm:py-0">
        <div className="min-w-0 flex-1">
          <div className="sm:hidden">
            <div className="truncate text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-muted)]" title={workspaceName || workspacePath}>
              {workspaceName || workspacePath}
            </div>
            <h1 className="truncate text-sm font-semibold text-[var(--app-text)]" title={liveSession?.title || 'New conversation'}>
              {liveSession?.title || 'New conversation'}
            </h1>
            {agentTodoBadgeLabel ? (
              <div className="mt-1 inline-flex max-w-full items-center gap-1 rounded-full border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2 py-0.5 text-[11px] font-medium text-[var(--app-text-muted)]" title="Agent checklist for this session">
                <ListChecks size={12} />
                <span className="truncate">{agentTodoBadgeLabel}</span>
              </div>
            ) : null}
          </div>
          <div className="hidden min-w-0 sm:block">
            <h1 className="flex items-center gap-2 overflow-hidden text-sm font-semibold text-[var(--app-text)]">
              <span className="truncate" title={liveSession?.title || 'New conversation'}>{liveSession?.title || 'New conversation'}</span>
              <span className="shrink-0 text-[var(--app-text-subtle)] font-normal">/</span>
              <span className="truncate text-[var(--app-text-muted)] font-normal" title={workspaceName}>{workspaceName}</span>
            </h1>
            {agentTodoBadgeLabel ? (
              <div className="mt-1 inline-flex max-w-full items-center gap-1 rounded-full border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2 py-0.5 text-[11px] font-medium text-[var(--app-text-muted)]" title="Agent checklist for this session">
                <ListChecks size={12} />
                <span className="truncate">{agentTodoBadgeLabel}</span>
              </div>
            ) : null}
          </div>
        </div>
        <div className="ml-auto flex min-w-0 items-center justify-end gap-1.5 sm:gap-2">
          {activePermission ? (
            <button
              type="button"
              className="flex w-fit cursor-default items-center gap-2 rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-2.5 py-1.5 text-left text-xs text-[var(--app-danger)] sm:px-3"
            >
              <ShieldAlert size={14} />
              <span>{pendingPermissionCount > 1 ? `${pendingPermissionCount} pending` : '1 pending'}</span>
            </button>
          ) : null}
          {showRunTimer ? (
            <div className="inline-flex h-9 shrink-0 items-center gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2.5 text-xs font-medium tabular-nums text-[var(--app-text-muted)] sm:h-10">
              <Clock3 size={14} />
              {runTimerLabel}
            </div>
          ) : null}
          <Button
            size="sm"
            variant="ghost"
            className="h-11 w-11 shrink-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-0 text-[var(--app-text)] hover:border-[var(--app-border-accent)] sm:h-10 sm:w-10"
            onClick={openCommitModal}
            disabled={!sessionId || submitting || canStop}
            aria-label="Save changes"
            title="Save / commit changes"
          >
            <Save size={22} />
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-11 w-11 shrink-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-0 text-[var(--app-text)] hover:border-[var(--app-border-accent)] sm:hidden"
            onClick={onOpenSidebarMenu}
            aria-label="Open sidebar"
            title="Open sidebar"
          >
            <Menu size={24} />
          </Button>
        </div>
      </header>

      <div ref={scrollerRef} data-testid="desktop-chat-scroller" onScroll={persistScrollState} className="flex-1 min-h-0 min-w-0 overflow-y-auto overflow-x-hidden overscroll-contain bg-[var(--app-bg-alt)] px-4 py-4 [-webkit-overflow-scrolling:touch] sm:px-6 sm:py-5">
        {loadingMessages && messages.length === 0 ? (
          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-sm text-[var(--app-text-muted)]">Loading conversation…</div>
        ) : null}
        {!loadingMessages && messages.length === 0 && !sessionId ? (
          <div className="flex items-center gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
            <Sparkles size={16} />
            No conversation yet. Ask Swarm to do something in this workspace.
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
                  className="absolute left-0 top-0 w-full py-2 flex justify-start"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                >
                  <article className="w-full min-w-0 max-w-[95%]">
                    <ChatMarkdown content="" toolMessage={item.toolMessage} nowMs={timerNow} />
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
                  className="absolute left-0 top-0 w-full py-2 flex justify-start"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                >
                  <article className="w-full min-w-0 max-w-[95%]">
                    <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                      Swarm
                    </div>
                    <ChatMarkdown content={item.content} toolMessage={null} />
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
                    <ChatMarkdown content={message.content} toolMessage={message.toolMessage ?? null} className="!text-current" nowMs={timerNow} />
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
                  className="absolute left-0 top-0 w-full py-2 flex justify-start"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                  data-global-seq={message.globalSeq}
                >
                  <article className="w-full min-w-0 max-w-[95%] border-l-2 border-[var(--app-border)] pl-4 opacity-80">
                    <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                      {reasoningLabel}
                    </div>
                    {reasoningBody ? <ChatMarkdown content={reasoningBody} toolMessage={message.toolMessage ?? null} nowMs={timerNow} /> : null}
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
                className="absolute left-0 top-0 w-full py-2 flex justify-start"
                style={{ transform: `translateY(${virtualItem.start}px)` }}
                data-global-seq={message.globalSeq}
              >
                <article className="w-full min-w-0 max-w-[95%]">
                  {message.role !== 'tool' ? (
                    <div className="mb-1 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                      {messageRoleLabel(message.role)}
                    </div>
                  ) : null}
                  <ChatMarkdown content={message.content} toolMessage={message.toolMessage ?? null} nowMs={timerNow} />
                </article>
              </div>
            )
          })}
        </div>
      </div>

      <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)]">
        <div className="grid gap-3 px-4 pb-[calc(0.75rem+var(--app-safe-area-bottom))] pt-4 focus-within:pb-[calc(1rem+var(--app-safe-area-bottom))] sm:px-6 sm:pb-[calc(1.25rem+var(--app-safe-area-bottom))] sm:pt-5">
          {panelError ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{panelError}</div> : null}
          {permissionError ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{permissionError}</div> : null}
          {dictationError ? <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm text-[var(--app-warning-text)]">{dictationError}</div> : null}
          {lastSavedRulePreview ? <div className="rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-3 py-2 text-sm text-[var(--app-success)]">Always apply saved: {lastSavedRulePreview}</div> : null}
          {!lifecycleActive && (lifecyclePhase === 'cancelled' || lifecyclePhase === 'canceled') ? (
            <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm text-[var(--app-warning-text)]">
              {lifecycleStopReason || liveSession?.live.summary || 'Stream cancelled by user.'}
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
            <div className="flex items-end gap-3 px-4 py-3 lg:py-2.5">
              <div className="min-w-0 flex-1">
                <Textarea
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
                  placeholder={placeholder}
                  className={showDictationButton ? "min-h-[56px] resize-none !rounded-none !border-0 !border-none bg-transparent px-0 py-0 pr-12 !shadow-none !outline-none !ring-0 focus:!ring-0 focus:!shadow-none focus:!border-0 focus-visible:!ring-0 focus-visible:!ring-offset-0 focus-visible:!shadow-none focus-visible:!border-0 hover:!border-0 disabled:bg-transparent lg:min-h-[52px]" : "min-h-[56px] resize-none !rounded-none !border-0 !border-none bg-transparent px-0 py-0 !shadow-none !outline-none !ring-0 focus:!ring-0 focus:!shadow-none focus:!border-0 focus-visible:!ring-0 focus-visible:!ring-offset-0 focus-visible:!shadow-none focus-visible:!border-0 hover:!border-0 disabled:bg-transparent lg:min-h-[52px]"}
                  rows={2}
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
              <div className="hidden min-[1000px]:flex min-w-0 items-center gap-2 justify-between">
                <div className="flex min-w-0 flex-1 items-center gap-3 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                  {selectedPrimaryAgentProfile?.exitPlanModeEnabled ? (
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

                  <AgentPicker
                    currentAgent={currentSessionAgent}
                    selectedPrimaryAgent={selectedPrimaryAgent}
                    agents={selectableAgents}
                    onSelect={(value) => {
                      void handleAgentSelect(value)
                    }}
                  />

                  <ModelPicker
                    options={resolvedModelOptions}
                    selectedKey={selectedModelAvailable ? selectedModelKey : ''}
                    onSelect={handleModelChange}
                    openSignal={modelPickerOpenSignal}
                  />

                  <div className="hidden min-[1100px]:contents">
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
                    />

                    {fastSupported ? (
                      <ThinkingPicker
                        value={fastValue}
                        options={FAST_ON_OFF_OPTIONS}
                        onSelect={handleFastChange}
                        label="Fast"
                      />
                    ) : null}
                  </div>

                  <div className="relative hidden min-[1000px]:block min-[1100px]:hidden">
                    <button
                      ref={intermediateSettingsTriggerRef}
                      type="button"
                      onClick={() => setIntermediateSettingsOpen(!intermediateSettingsOpen)}
                      title="Thinking and speed settings"
                      aria-haspopup="menu"
                      aria-expanded={intermediateSettingsOpen}
                      className="inline-flex items-center gap-1 text-[11px] font-medium text-[var(--app-text-muted)] transition hover:text-[var(--app-text)]"
                    >
                      <Sparkles size={13} className="shrink-0 text-[var(--app-text-subtle)]" />
                      <span className="max-w-[4.75rem] truncate">{normalizedThinking}</span>
                      <ChevronDown size={12} className={intermediateSettingsOpen ? 'shrink-0 rotate-180 transition-transform' : 'shrink-0 transition-transform'} />
                    </button>
                    {intermediateSettingsOpen ? (
                      <div ref={intermediateSettingsRef} className="absolute bottom-[100%] left-0 z-50 mb-2 flex w-[260px] flex-col gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-[var(--shadow-panel)]">
                        <ThinkingPicker value={normalizedThinking} options={THINKING_OPTIONS} onSelect={handleThinkingChange} label="Thinking" tagsEnabled={thinkingTagsEnabled} onToggleTags={(enabled) => { void handleThinkingTagsToggle(enabled) }} tagsBusy={thinkingTagsSaving} />
                        {fastSupported ? <ThinkingPicker value={fastValue} options={FAST_ON_OFF_OPTIONS} onSelect={handleFastChange} label="Fast" /> : null}
                      </div>
                    ) : null}
                  </div>

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
              <div className="flex w-full min-w-0 relative min-[1000px]:hidden">
                {mobileSettingsOpen ? (
                  <div ref={mobileSettingsRef} className="absolute bottom-[100%] left-0 z-50 mb-2 flex w-[max(260px,100%)] flex-col gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-[var(--shadow-panel)]">
                    <ModePicker mode={sessionMode === 'auto' ? 'auto' : 'plan'} onSelect={handleModeChange} />
                    <AgentPicker currentAgent={currentSessionAgent} selectedPrimaryAgent={selectedPrimaryAgent} agents={selectableAgents} onSelect={(value) => { void handleAgentSelect(value) }} dropdownAlign="left" />
                    <ModelPicker options={resolvedModelOptions} selectedKey={selectedModelAvailable ? selectedModelKey : ''} onSelect={handleModelChange} openSignal={modelPickerOpenSignal} />
                    <ThinkingPicker value={normalizedThinking} options={THINKING_OPTIONS} onSelect={handleThinkingChange} label="Thinking" tagsEnabled={thinkingTagsEnabled} onToggleTags={(enabled) => { void handleThinkingTagsToggle(enabled) }} tagsBusy={thinkingTagsSaving} />
                    {fastSupported ? <ThinkingPicker value={fastValue} options={FAST_ON_OFF_OPTIONS} onSelect={handleFastChange} label="Fast" /> : null}
                  </div>
                ) : null}

                <div className="grid w-full min-w-0 grid-cols-[minmax(0,1fr)_48px_minmax(0,0.7fr)_40px] items-center gap-1.5 sm:grid-cols-[minmax(0,1fr)_56px_minmax(0,0.7fr)_40px] sm:gap-2">
                  {/* The Summary/Settings Quick Toggle */}
                  <button 
                    ref={mobileSettingsTriggerRef}
                    type="button" 
                    onClick={() => setMobileSettingsOpen(!mobileSettingsOpen)} 
                    className="flex h-10 min-w-0 items-center gap-1.5 overflow-hidden rounded-xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] px-2 shadow-sm text-left hover:bg-[var(--app-surface-hover)] transition"
                    title="Open mode, agent, model, thinking, and speed settings"
                  >
                    <Settings2 size={14} className="shrink-0 text-[var(--app-text-subtle)]" />
                    <span className="flex min-w-0 flex-col leading-tight">
                      <span className="truncate text-[11px] sm:text-[12px] font-medium text-[var(--app-text)]">{currentSessionAgent}</span>
                      <span className="truncate text-[10px] text-[var(--app-text-muted)]">
                        {sessionMode === 'auto' ? 'auto' : 'plan'} · {resolvedModelOptions.find(o => o.key === selectedModelKey)?.label || 'Model'} · {normalizedThinking}{fastSupported ? ` · fast ${fastValue}` : ''}
                      </span>
                    </span>
                  </button>

                  <button type="button" onClick={() => { void handleCompact(composer) }} disabled={!sessionId || canStop || submitting} title={contextBadgeTooltip ? `${contextBadgeTooltip} · Click to compact` : 'Compact conversation'} className="inline-flex h-10 min-w-0 items-center justify-center rounded-xl bg-[var(--app-surface-subtle)] px-1.5 sm:px-2.5 transition hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50 border border-[var(--app-border)] text-[var(--app-text-muted)] hover:text-[var(--app-text)] font-medium tabular-nums text-[10px] sm:text-[11px]">
                    <span className="truncate min-w-0 w-full text-center">{contextBadgeLabel || 'ctx'}</span>
                  </button>

                  {showRoutePicker ? (
                    <div className="flex min-w-0 overflow-hidden [&>div]:w-full [&>div>button]:w-full">
                      <RoutePicker
                        currentRoute={activeChatRoute}
                        routes={routeOptions}
                        onSelect={handleRouteChange}
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
                  Commit from the desktop header with Memory or a manual git commit flow.
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
                  <div className="mt-1 text-xs text-[var(--app-text-muted)]">Provide commit instructions or exact commit text and run a restricted git commit flow.</div>
                </button>
              </div>
              <label className="grid gap-2">
                <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                  {commitModal.mode === 'manual' ? 'Commit instructions / message' : 'Extra commit instructions'}
                </span>
                <Textarea
                  value={commitModal.instructions}
                  onChange={(event) => handleCommitInstructionsChange(event.target.value)}
                  placeholder={commitModal.mode === 'manual'
                    ? 'Example: commit as "feat: add save modal" and include only the staged UI files.'
                    : 'Optional: mention what should be emphasized in the commit message.'}
                  className="min-h-[140px] resize-y bg-[var(--app-bg-alt)]"
                />
              </label>
              <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
                {commitStatusLabel(commitModal) || 'Save runs in the background and can be used again after completion.'}
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
                Save now
              </Button>
            </div>
          </DialogPanel>
        </Dialog>
      ) : null}
    </Card>
  )
}
