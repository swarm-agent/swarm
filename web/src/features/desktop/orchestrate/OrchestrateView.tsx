import { PersonalAvatar } from './personal-avatar'
import { ContextRemaining } from './orchestrator-composer-surface'
import { ProjectHeaderIdentity, ProjectImageSettings } from './project-header-identity'
import { useLayoutEffect, useState, useReducer, useMemo, useEffect, useCallback, useRef, useSyncExternalStore, useId } from 'react'
import { MediaTaskSelect, MediaTaskDefault, MediaTaskHelp, MediaTaskScenes, MediaTaskCost } from './media-task-controls'
import { ImagePromptControls, imagePromptReducer, initialImagePromptState, imagePromptEnhancement } from './image-task-prompt'
import { DurableWorkerReviews } from '../chat/components/durable-worker-reviews'
import { taskReopenOperations, taskReopenKey, type TaskReopenOutcome } from './task-reopen-operation'
import { taskIntegrationBatches, integrationSkipReason, MAX_INTEGRATION_BATCH } from './task-integration-batch'
import { integrationLanePending, taskIntegrationRequest, taskIntegrationOperations, taskIntegrationKey, taskIntegrationFailureIdentity, taskIntegrationPhase, type TaskIntegrationOperation, type TaskIntegrationResult } from './task-integration-operation'
import { taskDelivery, taskOutcome } from './task-outcome'
import { TaskOutcomeDetails, ProjectTaskAttention } from './task-outcome-view'
import { TaskAttemptHistory } from './task-attempt-history'
import { TaskUsageFooter, TaskWorkerBudgetMetadata } from './task-usage-metadata'
import { useOrchestratorDictation } from './use-orchestrator-dictation'
import { TaskSessionErrors } from './task-session-error'
import { admittedConversationId } from './project-entry-policy'
import { createProjectConversation, projectConversationLink, projectConversationMessageMetadata, requireProjectConversation } from './project-conversations'
import { useProjectConversations } from '../runtime/project-conversations'
import { ProjectNavigation } from './project-navigation'
import { UsagePage } from '../usage/pages/usage-page'
import { ProjectConversationSidebar } from './project-conversation-sidebar'
import { clearSessionContext, ContextClearRejected } from '../session-v3/context-clear-api'
import { projectRouteSegment, resolveProjectRoute } from './project-route'
import { resolveDesktopChatRouteFromSession } from '../chat/services/chat-routing'
import { integrationFailure, repairUnavailable, redactIntegrationDiagnostic, orchestratorDrafts, type IntegrationFailure } from './integration-recovery'
import { useVideoTaskDefault } from './use-video-task-default'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { swarmPageLink, type SwarmPage } from './swarm-navigation'
import { filterOrchestrateCommands, parseOrchestrateCommand, ORCHESTRATE_TIPS, type OrchestrateCommand } from './orchestrate-commands'
import { SwarmLayoutControls, useSwarmResponsiveLayout, useSwarmModalFocus } from './swarm-responsive-layout'
import { OrchestrateSettings } from './orchestrate-settings'
import { OrchestrateAgents } from './orchestrate-agents'
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Bot,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Code,
  Code2,
  Edit3,
  ExternalLink,
  FileText,
  Film,
  Folder,
  FolderGit2,
  GitBranch,
  GitPullRequest,
  Image as ImageIcon,
  Layers,
  ListChecks,
  Loader2,
  MessageSquare,
  Mic,
  MicOff,
  Music,
  Paperclip,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Settings2,
  Sparkles,
  Tag,
  Trash2,
  Upload,
  Volume2,
  Send,
  Square,
  X,
  Zap,
} from 'lucide-react'
import { requestJson, getDesktopSessionIdentitySnapshot, updateDesktopSessionUsername } from '../../../app/api'
import { WorkerHub, type SelectedWorker } from './worker-hub'
import { submitWithWorkerSelection } from './worker-message-context'
import { ProjectWorkerSidebar } from '../layout/project-worker-sidebar'
import { OrchestratorNotifications } from '../notifications/components/orchestrator-notifications'
import { swarmWorkerHref, swarmActivePage } from './swarm-navigation'
import { useDesktopV3CacheSelector, getDesktopV3CacheSnapshot } from '../state/desktop-v3-cache-store'
import { selectPendingWorkerSidebarReviews } from '../state/desktop-automation-v2-state'
import { desktopAutomationV2 } from '../runtime/desktop-automation-v2'
import { decidePendingWorkerReview } from '../tools/automations/pending-worker-sidebar-reviews'
import { DesktopV3ExistingConversationPane, resolveDesktopV3StopRunRequest } from '../chat/components/desktop-v3-existing-conversation-pane'
import {
  isDesktopV3SessionTailReady,
  selectRenderedSessionMessages,
} from '../state/desktop-v3-cache-selectors'
import { selectAndHydrateDesktopV3Session, hydrateDesktopV3ChildCard } from '../state/desktop-v3-session-hydrator'
import { requireDesktopV3RealtimeControllerReady } from '../realtime/v3-realtime-controller'
import { HistoricalMediaLibrary, MediaViewerModal, type MediaLibraryItem } from '../tools/media-library'
import { mediaJobIdentity } from '../tools/media-library/media-job-identity'
import { toMediaLibraryItem } from '../tools/media-library/media-classifier'
import type { DesktopV3ArtifactCatalogEntry } from '../session-v3/artifact-api'
import type { MediaGenerationJob, MediaGenerationRequest, MediaGenerationSettings } from '../tools/media-library/media-generation'
import type { QuickRouteMode } from '../tools/media-library/media-viewer-modal'
import { MediaTaskCard, MediaTaskThreads, isCreativeMediaTask } from './media-task-card'
import { archiveProjectTask, projectTaskArchiveQueue } from '../runtime/project-task-archive'
import { DesktopCodexUsageModal } from '../codex/desktop-codex-usage-modal'
import { subscribeDesktopSessionReset } from '../../../app/api'
import { DesignMediaTasks, useProjectDesigns } from '../tools/media-library/design-media'
import { ORCHESTRATE_THEMES } from './orchestrate-themes'
import { TaskCardHandoff, TaskCardAgents, TaskCardOutputs, TaskExpectedOutputs } from './task-card-details'
import { TaskCardSummary, formatElapsedString, formatElapsedSeconds } from './task-card-summary'
import { taskWithCurrentSessions } from './task-card-sessions'
import { TaskCardActionButtons } from './task-card-action-buttons'
import { TaskCardActivity } from './task-card-activity'
import { SessionPermissionAttention, TaskAttention, useTaskAttention } from './task-attention'
import { TaskListHeader, TaskListToolbar } from './task-list-toolbar'
import { useQuery } from '@tanstack/react-query'
import { fetchGitStatus, gitStatusQueryKey } from '../git/api'
import { inheritedSwarmThemeStyle, projectThemePatch, resolveSwarmProjectTheme } from './swarm-section-theme'
import { createProjectThemeRefresh } from './project-theme-refresh'
import { WORKSPACE_THEME_OPTIONS, setWorkspaceThemeCatalog, formatWorkspaceThemeLabel } from '../../workspaces/launcher/services/workspace-theme'
import {
  desktopProjects,
  useDesktopProject,
  computeActiveTaskSessionIds,
  extractTaskSessionIds,
  type TaskSessionCandidate,
  TaskSessionLeaseManager,
} from '../runtime/desktop-projects'
import { mapBackendTask, mapBackendTasks } from '../state/desktop-projects-state'
import {
  DeployedWorker,
  MediaDeliverable,
  MiddleCanvasVariant,
  OrchestrateThemeId,
  ProjectSummary,
  ProjectTaskMediaRef,
  RunningTask,
} from './orchestrate-types'
import {
  resolveVideoPricing,
  normalizeVideoResKey,
  resolveAllowedVideoDurations,
  resolveAllowedVideoResolutions,
  resolveAllowedVideoAspectRatios,
  validateVideoAttachment,
  resolveQualifiedVideoModel,
  type TaskModalModelOption,
} from './videoTaskSettings'

export { resolveVideoPricing, normalizeVideoResKey, type TaskModalModelOption }
import {
  createDesktopV3ExistingMessageOperation,
  continueDesktopV3Conversation,
} from '../session-v3/existing-session-flow'
import { getDesktopV3MediaCapability, uploadDesktopV3MediaAsset } from '../session-v3/write-api'
import { admitComposerFile } from '../chat/services/composer-attachments'
import { stopSessionV3Run } from '../session-v3/api'
import type { DesktopV3MediaReference } from '../state/desktop-v3-cache-types'
import {
  aggregateTaskLiveState,
  buildTaskAcceptancePayload,
  buildSelectedTaskMessageEnvelope,
  buildSelectedTaskMessageMetadata,
  reconcileSelectedTaskId,
  validateSelectedTaskForContext,
  getPrimarySystemAgentName,
  resolveDeployImpendingConfig,
  resolveTaskImpendingAgents,
  resolveTaskWorkspace,
  taskWorkspaceSelection,
  taskDeployRequestIdentity,
  isTaskRunning,
} from './orchestrate-task-helpers'
import type { DesktopSessionRecord } from '../types/realtime'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import { useQueryClient } from '@tanstack/react-query'
import { agentModelSettingsQueryOptions } from '../settings/swarm/queries/get-agent-model-settings'
import { AgentModelControl, type AgentModelControlTaskOverrideInput } from '../chat/components/agent-model-control'
import { modelOptionsQueryOptions } from '../../queries/query-options'
import type { ModelOptionRecord } from '../chat/types/chat'
import type { BackendTaskModelPreview } from './orchestrate-types'


export interface OrchestrateViewProps {
  workerDetailId?: string
  workspaceSlug?: string
  onNavigateHome?: () => void
  initialThemeId?: OrchestrateThemeId
}

/**
 * Helper to safely parse any date/time representation into a valid Date object.
 * Prevents "RangeError: Invalid time value" on Date.prototype.toISOString() or toLocaleDateString().
 */
function parseSafeDate(val: unknown): Date {
  if (val instanceof Date && !isNaN(val.getTime())) {
    return val
  }
  if (typeof val === 'number' && !isNaN(val) && isFinite(val) && val > 0) {
    const d = new Date(val)
    if (!isNaN(d.getTime())) return d
  }
  if (typeof val === 'string' && val.trim().length > 0) {
    const parsed = Date.parse(val)
    if (!isNaN(parsed)) {
      return new Date(parsed)
    }
  }
  return new Date()
}

function safeIsoDayKey(date: Date): string {
  try {
    return date.toISOString().slice(0, 10)
  } catch {
    const now = new Date()
    return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
  }
}



export interface AspectRatioOption<T extends string = string> {
  ratio: T
  label: string
  widthClass: string
  heightClass: string
}

export const IMAGE_ASPECT_RATIOS: AspectRatioOption<'1:1' | '16:9' | '9:16' | '4:3'>[] = [
  { ratio: '16:9', label: 'Landscape', widthClass: 'w-5', heightClass: 'h-3' },
  { ratio: '1:1', label: 'Square', widthClass: 'w-3.5', heightClass: 'h-3.5' },
  { ratio: '9:16', label: 'Portrait', widthClass: 'w-3', heightClass: 'h-5' },
  { ratio: '4:3', label: 'Standard', widthClass: 'w-4', heightClass: 'h-3' },
]

export const VIDEO_ASPECT_RATIOS: AspectRatioOption<'16:9' | '9:16' | '1:1'>[] = [
  { ratio: '16:9', label: 'Landscape', widthClass: 'w-5', heightClass: 'h-3' },
  { ratio: '9:16', label: 'Portrait', widthClass: 'w-3', heightClass: 'h-5' },
  { ratio: '1:1', label: 'Square', widthClass: 'w-3.5', heightClass: 'h-3.5' },
]

/**
 * Helper to resolve estimated pricing for image generation based on model catalog and resolution
 */
function normalizeImageResKey(cond: string): '1k' | '2k' | '4k' | string {
  const c = cond.toLowerCase().trim()
  if (c === '1k' || c === '1024x1024' || c === '1024×1024' || c === 'standard' || c === '1024') return '1k'
  if (c === '2k' || c === '2048x2048' || c === '2048×2048' || c === 'hd' || c === '2048') return '2k'
  if (c === '4k' || c === '4096x4096' || c === '4096×4096' || c === 'ultra_hd' || c === 'uhd' || c === '4096') return '4k'
  return c
}

export function resolveImagePricing(
  option: TaskModalModelOption | undefined,
  resolution: string,
  variantCount: number
): {
  ratePerImage: number
  totalPrice: number
  formattedSummary: string
  ratesByResolution: Record<string, string>
  unitRatesByResolution: Record<string, number>
  totalsByResolution: Record<string, number>
  isVerified: boolean
} {
  const defaultRateValues: Record<'1k' | '2k' | '4k', number> = {
    '1k': 0.03,
    '2k': 0.06,
    '4k': 0.12,
  }
  const unitRates: Record<string, number> = { ...defaultRateValues }
  let isVerified = false

  if (option && option.pricing) {
    const p = option.pricing as Record<string, any>
    if (p.billing?.lines && Array.isArray(p.billing.lines)) {
      for (const line of p.billing.lines) {
        if (!line || typeof line !== 'object') continue
        const rawCond = line.conditions?.resolution || line.conditions?.image_size || ''
        const condRes = normalizeImageResKey(String(rawCond))
        const priceVal = typeof line.price_usd === 'number' ? line.price_usd : parseFloat(line.price_usd)
        if (!isNaN(priceVal) && priceVal > 0) {
          isVerified = true
          if (condRes in unitRates) {
            unitRates[condRes] = priceVal
          } else if (!unitRates['1k']) {
            unitRates['1k'] = priceVal
          }
        }
      }
    }
    const directPrice = typeof p.per_image === 'number' ? p.per_image : typeof p.image === 'number' ? p.image : undefined
    if (typeof directPrice === 'number' && directPrice > 0) {
      isVerified = true
      unitRates['1k'] = directPrice
      if (unitRates['2k'] === defaultRateValues['2k']) unitRates['2k'] = directPrice * 2
      if (unitRates['4k'] === defaultRateValues['4k']) unitRates['4k'] = directPrice * 4
    }
  }

  const normalizedRes = (normalizeImageResKey(resolution) as '1k' | '2k' | '4k') || '1k'
  const finalRate = unitRates[normalizedRes] ?? (defaultRateValues[normalizedRes] || 0.03)
  const total = finalRate * variantCount

  const totalsByResolution: Record<string, number> = {}
  const ratesByResolution: Record<string, string> = {}

  for (const res of ['1k', '2k', '4k'] as const) {
    const uRate = unitRates[res] ?? defaultRateValues[res]
    const resTotal = uRate * variantCount
    totalsByResolution[res] = resTotal
    ratesByResolution[res] = variantCount > 1
      ? `$${resTotal.toFixed(2)} ($${uRate.toFixed(2)}/ea)`
      : `$${uRate.toFixed(2)}/img`
  }

  const formattedSummary = variantCount > 1
    ? `$${total.toFixed(2)} Total ($${finalRate.toFixed(2)}/image × ${variantCount} images) · ${isVerified ? 'Verified catalog' : 'Standard estimate'}`
    : `$${total.toFixed(2)} Total ($${finalRate.toFixed(2)}/image) · ${isVerified ? 'Verified catalog' : 'Standard estimate'}`

  return {
    ratePerImage: finalRate,
    totalPrice: total,
    formattedSummary,
    ratesByResolution,
    unitRatesByResolution: unitRates,
    totalsByResolution,
    isVerified,
  }
}

/**
 * Helper to resolve estimated pricing for audio generation based on model catalog and duration
 */
export function resolveAudioPricing(
  option: TaskModalModelOption | undefined,
  durationSeconds: number
): {
  cost: number
  formattedSummary: string
  isVerified: boolean
} {
  let rate = 0.04
  let isVerified = false
  if (option && option.pricing) {
    const p = option.pricing as Record<string, any>
    if (typeof p.audio_output === 'number' && p.audio_output > 0) {
      rate = p.audio_output
      isVerified = true
    } else if (typeof p.per_audio === 'number' && p.per_audio > 0) {
      rate = p.per_audio
      isVerified = true
    }
  }
  const total = durationSeconds >= 60 ? rate * 2 : rate
  return {
    cost: total,
    formattedSummary: `$${total.toFixed(2)} Total (${durationSeconds}s audio track) · ${isVerified ? 'Verified catalog' : 'Standard estimate'}`,
    isVerified,
  }
}
function deliverableToMediaItem(
  d: MediaDeliverable,
  parentTask?: RunningTask,
  project?: ProjectSummary | null,
): MediaLibraryItem {
  const isVideo = d.type === 'video'
  const isImage = d.type === 'image' || (d.previewUrl && d.previewUrl.startsWith('data:image'))
  const kind: 'image' | 'video' | 'audio' | 'animation' = isVideo ? 'video' : isImage ? 'image' : d.type === 'audio' ? 'audio' : 'image'
  const createdDate = parseSafeDate(d.createdAt)
  const isJustNow = typeof d.createdAt === 'string' && d.createdAt.toLowerCase().includes('just now')
  const formattedDate = isJustNow ? 'Just now' : createdDate.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' })
  const formattedTime = createdDate.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const dayKey = safeIsoDayKey(createdDate)

    const prov = (d as any).videoProvenance || (d as any).video_provenance || null
    const model = prov?.model || (d as any).model || undefined
    const aspectRatio = prov?.aspect_ratio || (d as any).aspectRatio || (d as any).aspect_ratio || d.videoAspect || undefined // Deliverable actual aspect ratio only; parentTask?.aspectRatio is legacy requested settings
    const resolution = prov?.resolution || (d as any).resolution || undefined // Deliverable actual resolution only; parentTask?.resolution is legacy requested settings
    let durationSeconds: number | undefined = undefined
    if (typeof (d as any).durationSeconds === 'number' && (d as any).durationSeconds > 0) {
      durationSeconds = (d as any).durationSeconds
    } else if (typeof (d as any).duration_seconds === 'number' && (d as any).duration_seconds > 0) {
      durationSeconds = (d as any).duration_seconds
    } else if (typeof prov?.duration_seconds === 'number' && prov.duration_seconds > 0) {
      durationSeconds = prov.duration_seconds
    } // Deliverable actual durationSeconds only; parentTask?.durationSeconds is legacy requested settings

    const durationMs = prov?.observed_duration_ms && prov.observed_duration_ms > 0
      ? prov.observed_duration_ms
      : (typeof (d as any).durationMs === 'number' && (d as any).durationMs > 0
      ? (d as any).durationMs
      : (durationSeconds ? durationSeconds * 1000 : undefined))

    return {
    id: d.id,
    title: d.title,
    filename: `${d.title.replace(/[^a-zA-Z0-9_-]/g, '_')}.${kind === 'image' ? 'png' : kind === 'audio' ? 'wav' : 'mp4'}`,
    mediaType: kind === 'image' ? 'image/png' : kind === 'audio' ? 'audio/wav' : 'video/mp4',
    kind,
    createdAt: createdDate.getTime(),
    formattedDate,
    formattedTime,
    dayKey,
    dayLabel: formattedDate,
    sessionId: parentTask?.sessionId || project?.primarySessionId || '',
    sessionTitle: parentTask?.title || project?.name || 'Orchestrate Studio',
    workspacePath: parentTask?.workspacePath || project?.repoPath || '',
    workspaceName: project?.name || 'Project Canvas',
    iterationGroupId: parentTask?.id,
    iterationGroupTitle: parentTask?.title,
    dimensions: d.videoAspect || aspectRatio || undefined,
    durationMs,
    directUrl: d.mediaUrl || d.previewUrl || '',
    parentId: d.parentDeliverableId || (d as any).parent_deliverable_id,
    sourceMediaRef: d.sourceMediaRef || (d as any).source_media_ref,
    model,
    aspectRatio,
    resolution,
    durationSeconds,
    videoProvenance: prov,
    legacyRequestedSettings: parentTask ? {
      model: parentTask.model,
      aspectRatio: parentTask.aspectRatio,
      resolution: parentTask.resolution,
      durationSeconds: parentTask.durationSeconds,
    } : undefined,
    artifact: {
      artifactId: d.id,
      sessionId: parentTask?.sessionId || '',
      label: d.title,
      filename: d.title,
      kind,
      mediaType: kind === 'image' ? 'image/png' : kind === 'audio' ? 'audio/wav' : 'video/mp4',
      description: d.prompt || d.title,
      model,
      aspectRatio,
      resolution,
      durationSeconds,
      videoProvenance: prov,
      createdAt: createdDate.getTime(),
      updatedAt: createdDate.getTime(),
    } as any,
  }
}

/**
 * Adapter to convert a ProjectTaskMediaRef into a MediaLibraryItem
 */
function uploadedToMediaItem(
  m: ProjectTaskMediaRef,
  project?: ProjectSummary | null,
): MediaLibraryItem {
  const isVideo = m.kind === 'video' || (m.mediaType && m.mediaType.startsWith('video/'))
  const isAudio = m.kind === 'audio' || (m.mediaType && m.mediaType.startsWith('audio/'))
  const kind: 'image' | 'video' | 'audio' | 'animation' = isVideo ? 'video' : isAudio ? 'audio' : 'image'
  const createdDate = parseSafeDate(m.createdAt)
  const formattedDate = createdDate.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' })
  const formattedTime = createdDate.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const dayKey = safeIsoDayKey(createdDate)

  return {
    id: m.id,
    title: m.title || m.filename || 'Uploaded Media',
    filename: m.filename || m.title || `${m.id}.${kind === 'image' ? 'png' : 'mp4'}`,
    mediaType: m.mediaType || (kind === 'image' ? 'image/png' : 'video/mp4'),
    kind,
    createdAt: createdDate.getTime(),
    formattedDate,
    formattedTime,
    dayKey,
    dayLabel: formattedDate,
    sessionId: project?.primarySessionId || '',
    sessionTitle: project?.name || 'Uploaded Media',
    workspacePath: project?.repoPath || '',
    workspaceName: project?.name || 'Project Shelf',
    directUrl: m.url || '',
    model: (m as any).model,
    aspectRatio: (m as any).aspectRatio,
    resolution: (m as any).resolution,
    durationSeconds: (m as any).durationSeconds,
    artifact: {
      artifactId: m.id,
      sessionId: project?.primarySessionId || '',
      label: m.title || m.filename,
      filename: m.filename || m.title,
      kind,
      mediaType: m.mediaType || 'image/png',
      description: m.data ? m.data.slice(0, 200) : m.title,
      createdAt: createdDate.getTime(),
      updatedAt: createdDate.getTime(),
    } as any,
  }
}

/**
 * Thumbnail graphic renderer for video and media deliverables
 */
function DeliverableThumbnail({
  type,
  duration,
  previewUrl,
  status,
  deliverableType,
  onPlay,
}: {
  type?: string
  duration?: string
  previewUrl?: string
  status?: string
  deliverableType?: string
  onPlay?: () => void
}) {
  const isCode = deliverableType === 'code' || deliverableType === 'pr' || type === 'code' || type === 'pr'

  if (status === 'generating') {
    return (
      <div className="relative aspect-video w-full rounded-lg border border-blue-500/40 bg-blue-950/20 flex flex-col items-center justify-center p-3 animate-pulse space-y-1.5">
        <Loader2 size={18} className="animate-spin text-blue-400" />
        <span className="text-[10px] font-mono text-blue-300 font-semibold tracking-wider uppercase">
          {isCode ? 'Executing Coder...' : 'Generating Media...'}
        </span>
      </div>
    )
  }

  if (status === 'pending') {
    return (
      <div className="relative aspect-video w-full rounded-lg border-2 border-dashed border-slate-700/60 bg-slate-900/30 flex flex-col items-center justify-center p-3 space-y-1 text-center">
        {isCode ? <GitPullRequest size={18} className="text-emerald-400" /> : <ImageIcon size={18} className="text-slate-500" />}
        <span className="text-[10px] font-mono text-slate-400">
          {isCode ? 'Code PR • Pending Acceptance' : 'Empty Slot • Pending'}
        </span>
      </div>
    )
  }

  if (isCode) {
    return (
      <div
        onClick={onPlay}
        className="group/thumb relative aspect-video w-full cursor-pointer overflow-hidden rounded-lg border border-emerald-500/30 bg-[#091512] transition-all hover:border-emerald-500/60 p-2.5 flex flex-col justify-between"
      >
        <div className="flex items-center justify-between">
          <GitPullRequest size={16} className="text-emerald-400" />
          <span className="px-1.5 py-0.5 rounded bg-emerald-950 text-emerald-300 border border-emerald-500/30 font-mono text-[9px] font-bold">
            Branch PR
          </span>
        </div>
        <div className="text-[10px] font-mono text-slate-300 truncate">
          Code Branch Deliverable
        </div>
      </div>
    )
  }

  const src = previewUrl || (type && (type.startsWith('data:') || type.startsWith('http') || type.startsWith('/')) ? type : undefined)
  if (src) {
    return (
      <div
        onClick={onPlay}
        className="group/thumb relative aspect-video w-full cursor-pointer overflow-hidden rounded-lg border border-slate-800/80 bg-[#090d16] transition-all hover:border-blue-500/40"
      >
        <img src={src} alt="Deliverable" className="w-full h-full object-cover transition-transform group-hover/thumb:scale-105 duration-300" />
        {duration && (
          <span className="absolute bottom-1 right-1 z-20 rounded bg-black/80 px-1 py-0.5 font-mono text-[9px] font-semibold text-slate-300 backdrop-blur-sm border border-white/10">
            {duration}
          </span>
        )}
      </div>
    )
  }

  return (
    <div
      onClick={onPlay}
      className="group/thumb relative aspect-video w-full cursor-pointer overflow-hidden rounded-lg border border-slate-800/80 bg-[#090d16] transition-all hover:border-blue-500/40"
    >
      {type === 'cyber_lattice' && (
        <div className="absolute inset-0 bg-gradient-to-br from-[#0c162d] via-[#091024] to-[#040814] flex items-center justify-center">
          <svg className="absolute inset-0 h-full w-full opacity-60" viewBox="0 0 200 112">
            <defs>
              <linearGradient id="cyber-grad" x1="0%" y1="0%" x2="100%" y2="100%">
                <stop offset="0%" stopColor="#3b82f6" stopOpacity="0.8" />
                <stop offset="100%" stopColor="#06b6d4" stopOpacity="0.3" />
              </linearGradient>
            </defs>
            <path
              d="M 100 20 L 0 112 M 100 20 L 50 112 M 100 20 L 100 112 M 100 20 L 150 112 M 100 20 L 200 112"
              stroke="#38bdf8"
              strokeWidth="0.5"
              strokeOpacity="0.4"
            />
            <path
              d="M 20 100 L 180 100 M 40 85 L 160 85 M 60 70 L 140 70 M 80 55 L 120 55"
              stroke="#60a5fa"
              strokeWidth="0.5"
              strokeOpacity="0.3"
            />
            <rect x="25" y="45" width="12" height="60" fill="url(#cyber-grad)" rx="1" opacity="0.7" />
            <rect x="55" y="30" width="16" height="75" fill="url(#cyber-grad)" rx="1" opacity="0.9" />
            <rect x="135" y="35" width="15" height="70" fill="url(#cyber-grad)" rx="1" opacity="0.8" />
            <rect x="165" y="50" width="14" height="55" fill="url(#cyber-grad)" rx="1" opacity="0.7" />
            <circle cx="100" cy="30" r="3" fill="#60a5fa" />
          </svg>
        </div>
      )}

      {type === 'neural_core' && (
        <div className="absolute inset-0 bg-gradient-to-br from-[#0a1226] via-[#070d1e] to-[#040711] flex items-center justify-center">
          <svg className="absolute inset-0 h-full w-full opacity-70" viewBox="0 0 200 112">
            <circle cx="100" cy="56" r="40" fill="#3b82f6" opacity="0.15" filter="blur(8px)" />
            <path d="M 100 32 L 128 48 L 100 64 L 72 48 Z" fill="#1e293b" stroke="#818cf8" strokeWidth="0.8" />
            <path d="M 72 48 L 100 64 L 100 94 L 72 78 Z" fill="#0f172a" stroke="#6366f1" strokeWidth="0.8" />
            <path d="M 128 48 L 100 64 L 100 94 L 128 78 Z" fill="#1e1b4b" stroke="#3b82f6" strokeWidth="0.8" />
          </svg>
        </div>
      )}

      {type !== 'cyber_lattice' && type !== 'neural_core' && (
        <div className="absolute inset-0 bg-gradient-to-br from-slate-900 to-slate-950 flex items-center justify-center">
          <Film size={18} className="text-slate-600" />
        </div>
      )}

      <div className="relative z-10 flex h-full w-full items-center justify-center">
        <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-slate-900/80 text-white backdrop-blur-md border border-white/20 shadow-xl transition-transform group-hover/thumb:scale-105">
          <Play size={12} fill="currentColor" className="ml-0.5 text-white" />
        </div>
      </div>

      {duration && (
        <span className="absolute bottom-1 right-1 z-20 rounded bg-black/80 px-1 py-0.5 font-mono text-[9px] font-semibold text-slate-300 backdrop-blur-sm border border-white/10">
          {duration}
        </span>
      )}
    </div>
  )
}

/**
 * Leaf component isolating the 1-second elapsed timer updater
 * to prevent whole MinimalTaskCard re-rendering every second.
 */
function TaskElapsedTimer({
  isRunning,
  startedAt,
  createdAt,
  fallbackElapsed,
}: {
  isRunning: boolean
  startedAt?: number
  createdAt?: number
  fallbackElapsed?: string
}) {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    if (!isRunning) return
    const interval = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(interval)
  }, [isRunning])

  if (isRunning) {
    const start = startedAt || createdAt
    if (!start) return <span>{formatElapsedString(fallbackElapsed) || 'Running...'}</span>
    const totalSec = Math.max(0, Math.floor((now - start) / 1000))
    return <span>{formatElapsedSeconds(totalSec)}</span>
  }

  return <span>{formatElapsedString(fallbackElapsed) || 'Just now'}</span>
}

/**
 * Minimal Task Card: Clean, technical, outcome-focused task card
 * Free of highlight gradients and pill badges. Displays "Action Needed",
 * worktree/unmerged git status, and "What did it do?" vs "What's not done yet?".
 */
import { TaskRequirements, TaskPlanChecklist, TaskProposalChecklist, isTaskPlanReviewable } from './task-requirements'

export function MinimalTaskCard({
  task,
  isSelected,
  isMarked,
  onToggleMarked,
  isExpanded,
  onToggleExpand,
  onSelect,
  onOpenChat,
  onAskOrchestrator,
  onArchiveTask,
  onInvestigateSession,
  onApprove,
  onIntegrate,
  integrationOperation,
  onDelete,
  onRefine,
  onPreviewDeliverable,
  onReopen,
  onComplete,
  onRedeployJob,
  onUpdateModel,
  onOpenAgentSettings,
  onOpenTaskModelChanger,
  projectId,
  workspaceSlug,
  onOpenWorkerDetail,
  defaultImageModel,
  defaultVideoModel,
  defaultAudioModel,
  imageModelOptions,
  videoModelOptions,
  audioModelOptions,
  isApproving,
  taskError,
  integrationRecovery,
  previousRuns,
  onClearError,
}: {
  task: RunningTask
  isSelected?: boolean
  isMarked?: boolean
  onToggleMarked?: () => void
  isExpanded?: boolean
  onToggleExpand?: () => void
  onSelect?: () => void
  onOpenChat?: () => void
  onArchiveTask?: () => void
  onAskOrchestrator?: () => void
  onInvestigateSession?: (sessionId: string) => void
  onApprove?: () => void
  onIntegrate?: () => void
  integrationOperation?: TaskIntegrationOperation
  onDelete?: () => void
  onRefine?: (feedback?: string, errorSummary?: string) => void
  onPreviewDeliverable?: (d: MediaDeliverable, mode?: QuickRouteMode) => void
  onReopen?: (feedback?: string) => Promise<TaskReopenOutcome>
  onComplete?: () => void
  onRedeployJob?: (taskId: string, jobId: string, feedback?: string) => void
  onUpdateModel?: (taskId: string, model: string, scopeInput?: AgentModelControlTaskOverrideInput | null) => void | Promise<void>
  onOpenAgentSettings?: (agentName: string) => void
  onOpenTaskModelChanger?: (task: RunningTask) => void
  projectId?: string
  workspaceSlug?: string
  onOpenWorkerDetail?: (workerId: string) => void
  modelOptions?: ModelOptionRecord[]
  defaultImageModel?: string
  defaultVideoModel?: string
  defaultAudioModel?: string
  imageModelOptions?: TaskModalModelOption[]
  videoModelOptions?: TaskModalModelOption[]
  audioModelOptions?: TaskModalModelOption[]
  isApproving?: boolean
  previousRuns?: React.ReactNode
  integrationRecovery?: React.ReactNode
  taskError?: string
  onClearError?: () => void
}) {
  const [internalExpanded, setInternalExpanded] = useState(false)
  const expanded = isExpanded !== undefined ? isExpanded : internalExpanded
  const detailsToggleRef = useRef<HTMLButtonElement>(null)
  const detailsId = useId()
  const handleToggleExpand = () => {
    if (expanded) {
      requestAnimationFrame(() => {
        detailsToggleRef.current?.focus({ preventScroll: true })
        detailsToggleRef.current?.scrollIntoView({ block: 'nearest', behavior: 'instant' })
      })
    }
    if (onToggleExpand) {
      onToggleExpand()
    } else {
      setInternalExpanded(!internalExpanded)
    }
  }
  const agentModelSettingsQuery = useQuery(agentModelSettingsQueryOptions())
  const isPlanning = task.status === 'planning'
  const isPendingApproval = task.status === 'pending_approval'
  const [isFullPlanOpen, setIsFullPlanOpen] = useState(false)
  const [isModelChangerOpen, setIsModelChangerOpen] = useState(false)
  const [selectedTaskModel, setSelectedTaskModel] = useState(task.model || '')
  useEffect(() => {
    setSelectedTaskModel(task.model || '')
  }, [task.model])
  const disclosureBinding = task.planBinding || task.plan_binding
  const disclosurePlanId = disclosureBinding?.planId || disclosureBinding?.plan_id
  const disclosureRevision = disclosureBinding?.definitionRevision ?? disclosureBinding?.definition_revision
  // New tasks and durable definition revisions start with titles only. Status
  // chatter must not reopen details or discard the user's current expansion.
  useEffect(() => {
    setIsFullPlanOpen(false)
  }, [task.id, disclosurePlanId, disclosureRevision])

  const modelPreviewQuery = useQuery({
    queryKey: ['projects', projectId, 'tasks', task.id, 'model-preview'],
    queryFn: async () => {
      const res = await requestJson<{ task: any; model_preview: BackendTaskModelPreview }>(
        `/v3/projects/${projectId}/tasks/${task.id}/model-preview`
      )
      return res.model_preview
    },
    enabled: Boolean(projectId && task.id && isPendingApproval),
    staleTime: 60_000,
  })
  const [isRefineOpen, setIsRefineOpen] = useState(false)
  const [refineFeedback, setRefineFeedback] = useState('')
  const [isReopenOpen, setIsReopenOpen] = useState(false)
  const [reopenFeedback, setReopenFeedback] = useState('')
  const reopenFlightRef = useRef(false)
  const [localReopenPending, setLocalReopenPending] = useState(false)
  const [localReopenError, setLocalReopenError] = useState<string>()
  useSyncExternalStore(taskReopenOperations.subscribe, taskReopenOperations.getSnapshot, taskReopenOperations.getSnapshot)
  const reopenOperation = taskReopenOperations.get(taskReopenKey(projectId || '', task.id))
  const previousReopenPending = useRef(reopenOperation.pending)
  useEffect(() => {
    if (previousReopenPending.current && !reopenOperation.pending && !reopenOperation.error) {
      setReopenFeedback(''); setIsReopenOpen(false); setLocalReopenError(undefined)
    }
    previousReopenPending.current = reopenOperation.pending
  }, [reopenOperation])
  const isReopening = localReopenPending || reopenOperation.pending
  const actionError = localReopenError || reopenOperation.error || taskError
  const cardIdentityRef = useRef('')
  cardIdentityRef.current = taskReopenKey(projectId || '', task.id)
  useEffect(() => {
    const retained = taskReopenOperations.get(taskReopenKey(projectId || '', task.id))
    setLocalReopenPending(false); setLocalReopenError(undefined)
    setIsReopenOpen(retained.draft !== undefined); setReopenFeedback(retained.draft ?? '')
  }, [projectId, task.id])
  const isRunning = isTaskRunning(task)
  const isNeedsReview = task.status === 'needs_review'
  const isCompleted = task.status === 'completed'
  const isFailed = task.status === 'failed'
  const isRejected = task.status === 'rejected'
  const integrationPhase = taskIntegrationPhase(task, integrationOperation)
  const isIntegrating = integrationPhase === 'pending'
  const hasIntegrationReceipt = integrationPhase !== 'ready'
  const delivery = taskDelivery(task)
  const hasUnintegrated = delivery ? (delivery.actionable || delivery.recoverable) : task.gitStatus === 'diverged' && !task.isIntegrated && (task.unintegratedCommits ?? 0) > 0
  const deliveryNeedsReview = Boolean(delivery && !delivery.actionable && !delivery.recoverable && !delivery.integrated && !delivery.recovered)
  const canReopen = !isPendingApproval && !isRejected && !isRunning && Boolean(onReopen) &&
    (isNeedsReview || isCompleted || isFailed || task.isIntegrated || integrationPhase === 'success')
  const showComplete = !deliveryNeedsReview && isNeedsReview && !hasUnintegrated && !hasIntegrationReceipt && !task.isIntegrated && Boolean(onComplete)
  const isIntegratedAction = integrationPhase === 'success' || Boolean((delivery?.integrated ?? task.isIntegrated) && !hasIntegrationReceipt)
  const reopenButtonRef = useRef<HTMLButtonElement>(null)
  const reopenFormId = useId()
  const closeReopen = () => {
    setIsReopenOpen(false)
    reopenButtonRef.current?.focus()
  }

  const isWorker = Boolean(task.workerId?.trim() || task.worker_id?.trim())
  const workerTargetId = (task.workerId?.trim() || task.worker_id?.trim()) || ''
  const workerDisplayName = task.worker_name || (task.workerName && !task.workerName.startsWith('@') ? task.workerName : undefined) || workerTargetId || 'Worker'
  const handleWorkerClick = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (onOpenWorkerDetail && workerTargetId) {
      e.preventDefault()
      onOpenWorkerDetail(workerTargetId)
    }
  }

  const taskProgramStatus = task.taskProgramStatus || (task as any).task_program_status
  const taskProgramDef = task.taskProgram || (task as any).task_program || taskProgramStatus?.definition
  const isTaskProgram = Boolean(
    (taskProgramDef?.jobs && taskProgramDef.jobs.length > 0) ||
    (taskProgramStatus?.jobs && taskProgramStatus.jobs.length > 0)
  )

  const rawPlanDoc = task.planDocument || (task as any).plan_document || (task as any).document
  const planDoc = useMemo(() => {
    if (!rawPlanDoc) return null
    if (typeof rawPlanDoc === 'string') {
      try {
        return JSON.parse(rawPlanDoc)
      } catch {
        return null
      }
    }
    return rawPlanDoc?.document || rawPlanDoc
  }, [rawPlanDoc])
  const planDocTitle = planDoc?.title || task.planSummary || ''
  const planDocGoal = planDoc?.info?.goal || planDoc?.goal || planDoc?.objective || ''
  const planCheckpointsToRender = useMemo(() => {
    if (planDoc?.checkpoints && Array.isArray(planDoc.checkpoints) && planDoc.checkpoints.length > 0) {
      return planDoc.checkpoints
    }
    if (task.activePlanCheckpoints && task.activePlanCheckpoints.length > 0) {
      return task.activePlanCheckpoints
    }
    return []
  }, [planDoc, task.activePlanCheckpoints])
  const hasTaskProgramSpec = Boolean(taskProgramDef && ((taskProgramDef.stages && taskProgramDef.stages.length > 0) || (taskProgramDef.jobs && taskProgramDef.jobs.length > 0)))
  const hasStructuredPlan = planCheckpointsToRender.length > 0 || hasTaskProgramSpec
  const isPlanRejected = Boolean(
    planDoc?.status === 'rejected' ||
    planDoc?.approval_state === 'rejected' ||
    planDoc?.approvalState === 'rejected'
  )
  const isPlanTaskWithoutStructuredPlan = Boolean(
    (task.agentType === 'plan' || task.outcomeType === 'plan_spec' || Boolean(task.planBinding || (task as any).plan_binding || rawPlanDoc)) && !isTaskPlanReviewable(planDoc)
  )
  const bindingRevision =
    task.planBinding?.definitionRevision ??
    task.planBinding?.definition_revision ??
    (task as any).plan_binding?.definitionRevision ??
    (task as any).plan_binding?.definition_revision
  const hasPlanBinding = Boolean(
    task.planBinding?.planId ||
    task.planBinding?.plan_id ||
    (task as any).plan_binding?.planId ||
    (task as any).plan_binding?.plan_id
  )
  const isPlanCard = Boolean(hasPlanBinding || rawPlanDoc || task.agentType === 'plan' || task.outcomeType === 'plan_spec')
  const isPlanBindingMissingRevision = Boolean(
    hasPlanBinding && (typeof bindingRevision !== 'number' || bindingRevision <= 0)
  )
  const taskSessionId = task.planBinding?.sessionId || task.planBinding?.session_id || (task as any).plan_binding?.sessionId || (task as any).plan_binding?.session_id || task.sessionId

  const programJobs = useMemo(() => {
    if (!isTaskProgram) return []
    if (taskProgramStatus?.jobs && taskProgramStatus.jobs.length > 0) {
      return taskProgramStatus.jobs
    }
    if (taskProgramDef?.jobs) {
      return taskProgramDef.jobs.map((j: any) => ({
        job_id: j.id,
        stage_id: j.stage_id,
        state: 'declared',
        attempt_number: 1,
      }))
    }
    return []
  }, [isTaskProgram, taskProgramStatus, taskProgramDef])

  const findProgramJobDef = (jobId: string) => {
    return taskProgramDef?.jobs?.find((j: any) => j.id === jobId)
  }

  const activeStageId = taskProgramStatus?.active_stage_id || taskProgramDef?.stages?.[0]?.id || ''
  const completedJobsCount = programJobs.filter((j: any) => j.state === 'integrated' || j.state === 'completed' || j.state === 'handoff_ready').length
  const runningJobsCount = programJobs.filter((j: any) => j.state === 'running').length
  const conflictJobsCount = programJobs.filter((j: any) => j.state === 'conflict').length
  const totalJobsCount = programJobs.length

  const variantSlots = useMemo(() => {
    const count =
      task.variantCount && task.variantCount > 0
        ? task.variantCount
        : task.deliverables && task.deliverables.length > 0
        ? task.deliverables.length
        : 1
    return Array.from({ length: count }, (_, i) => i + 1)
  }, [task.variantCount, task.deliverables])

  const isMediaTask =
    task.agentType === 'image' ||
    task.agentType === 'video' ||
    task.agentType === 'sound' ||
    task.agentType === 'audio' ||
    task.outcomeType === 'media_bundle' ||
    task.outcomeType === 'video_story' ||
    task.outcomeType === 'video_clip'
  const showIntegration = !isPendingApproval && !isMediaTask && (deliveryNeedsReview || hasUnintegrated || hasIntegrationReceipt || task.isIntegrated)

  const isSingleVideo =
    task.agentType === 'video' &&
    (task.outcomeType === 'video_clip' || !task.scenes || task.scenes.length <= 1)

  const impendingAgents = useMemo(
    () =>
      resolveTaskImpendingAgents(
        task,
        programJobs,
        findProgramJobDef,
        agentModelSettingsQuery.data,
        {
          image: defaultImageModel,
          video: defaultVideoModel,
          audio: defaultAudioModel,
        },
        modelPreviewQuery.data,
        modelPreviewQuery.isError ? ((modelPreviewQuery.error as Error)?.message || 'Failed to load model preview') : null
      ),
    [
      task,
      programJobs,
      findProgramJobDef,
      agentModelSettingsQuery.data,
      defaultImageModel,
      defaultVideoModel,
      defaultAudioModel,
      modelPreviewQuery.data,
      modelPreviewQuery.isError,
      modelPreviewQuery.error,
    ]
  )

  const primaryAgentName = useMemo(
    () => getPrimarySystemAgentName(task.agentType),
    [task.agentType]
  )

  const attention = useTaskAttention(task)
  const attentionPending = attention.unresolvedCount > 0 || attention.permissions.length > 0

  if (['image', 'video', 'audio', 'sound'].includes(task.agentType)) {
    return <MediaTaskCard task={task} onPreview={onPreviewDeliverable} onApprove={onApprove} onArchive={onArchiveTask} onDelete={onDelete} isApproving={isApproving} error={taskError} attention={<TaskAttention attention={attention} />} />
  }

  return (
    <div
      onClick={() => { onSelect?.(); if (!expanded) handleToggleExpand() }}
      onKeyDown={event => { if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); onSelect?.(); if (!expanded) handleToggleExpand() } }}
      tabIndex={0}
      aria-label={`Task details ${task.title}`}
      data-testid="orchestrate-task-card"
      data-task-id={task.id}
      data-task-state={attentionPending ? 'waiting_for_input' : task.status}
      data-expanded={expanded}
      className={`swarm-task-card relative flex min-w-0 flex-col transition-colors cursor-pointer ${isSelected ? 'swarm-task-card-selected' : ''}`}
    >
      <TaskCardSummary
        projectId={projectId}
        expanded={expanded}
        // The dedicated row below owns live activity; the summary owns task facts.
        task={isRunning ? { ...task, toolActivitySummary: undefined } : task}
        onPreview={onPreviewDeliverable}
        onOpenSession={onInvestigateSession}
        statusBadge={
          <div
            className={`swarm-task-state ${isPendingApproval ? 'swarm-task-approval-pill' : ''} text-[11px] flex items-center gap-1.5 shrink-0 ${
              isPlanning
                ? 'bg-indigo-500/10 text-indigo-300 border-indigo-500/25'
                : isPendingApproval
                ? 'bg-amber-500/10 text-amber-300 border-amber-500/25'
                : isRunning
                ? 'bg-sky-500/10 text-sky-300 border-sky-500/25'
                : isNeedsReview
                ? 'bg-amber-500/[0.08] text-amber-200 border-amber-500/20'
                : isCompleted
                ? 'bg-emerald-500/10 text-emerald-300 border-emerald-500/25'
                : isFailed || isRejected
                ? 'bg-rose-500/10 text-rose-300 border-rose-500/25'
                : 'bg-slate-800/80 text-slate-300 border-slate-700/60'
            }`}
          >
            <span
              className={`h-1.5 w-1.5 rounded-full shrink-0 ${
                isPlanning
                  ? 'bg-indigo-400'
                  : isPendingApproval
                  ? 'bg-amber-400'
                  : isRunning
                  ? 'bg-sky-400'
                  : isNeedsReview
                  ? 'bg-amber-400/80'
                  : isCompleted
                  ? 'bg-emerald-400'
                  : isFailed || isRejected
                  ? 'bg-rose-400'
                  : 'bg-slate-500'
              }`}
            />
            <span>{attentionPending ? `Waiting for you (${attention.unresolvedCount})` : task.status === 'needs_review' ? 'Needs review' : task.status === 'in_progress' ? 'In progress' : task.status.replace(/_/g, ' ')}</span>
          </div>
        }
        timer={
          <span className="text-[11px] text-slate-400 flex items-center gap-1 shrink-0 font-normal">
            <TaskElapsedTimer
              isRunning={isRunning}
              startedAt={task.startedAt}
              createdAt={task.createdAt}
              fallbackElapsed={task.elapsed}
            />
          </span>
        }
        actions={
          <div className="flex items-center gap-1.5">
            <TaskCardActionButtons onArchiveTask={onArchiveTask} onAskOrchestrator={onAskOrchestrator} />
            {onToggleMarked && (
              <label
                className={`group flex items-center justify-center w-5 h-5 rounded-md border transition-all cursor-pointer shrink-0 ${
                  isMarked
                    ? 'bg-blue-600 border-blue-500 text-white shadow-sm'
                    : 'border-slate-700/60 bg-slate-900/40 hover:border-slate-500 hover:bg-slate-800/80 text-transparent hover:text-slate-400'
                }`}
                title={`Select task ${task.title}`}
                onClick={(e) => e.stopPropagation()}
              >
                <input
                  type="checkbox"
                  checked={Boolean(isMarked)}
                  onChange={onToggleMarked}
                  aria-label={`Select task ${task.title}`}
                  className="sr-only"
                />
                <Check size={12} strokeWidth={2.5} className={isMarked ? 'opacity-100' : 'opacity-0 group-hover:opacity-40'} />
              </label>
            )}
          </div>
        }
        extraBadges={
          <>
            {task.outcomeType && task.outcomeType !== 'code_pr' && !(isPendingApproval && task.outcomeType === 'plan_spec') && (
              <span className="text-[11px] capitalize px-2 py-0.5 rounded-md bg-slate-800/70 text-slate-300 border border-slate-700/50 font-normal">
                {task.outcomeType === 'video_clip' ? 'Single Video' : task.outcomeType.replace(/_/g, ' ')}
              </span>
            )}
            {isWorker && (
              <a
                href={swarmWorkerHref(workspaceSlug, workerTargetId)}
                onClick={handleWorkerClick}
                className="text-[11px] px-2 py-0.5 rounded-md bg-indigo-500/10 text-indigo-300 border border-indigo-500/25 hover:bg-indigo-500/20 hover:text-white flex items-center gap-1 font-medium transition-colors cursor-pointer"
                title={`Worker: ${workerDisplayName}`}
                data-testid="worker-tag"
              >
                <Bot size={11} className="text-indigo-400" />
                <span>Worker: {workerDisplayName}</span>
              </a>
            )}
            {(task.routerAlert || (task as any).router_alert) && (
              <span className="text-[11px] px-2 py-0.5 rounded-md bg-amber-500/10 text-amber-300 border border-amber-500/25 flex items-center gap-1 font-medium">
                <AlertTriangle size={11} className="text-amber-400" />
                <span>Router Alert</span>
              </span>
            )}
          </>
        }
      />
      {isRunning && <TaskCardActivity task={task} />}
      {(showIntegration || canReopen || showComplete) && (
        <div className="swarm-task-action-row flex items-center flex-wrap gap-2 text-xs" data-testid="task-primary-actions">
          {showIntegration && <div className="flex items-center flex-wrap gap-2" data-testid="task-pending-worktree-bar">
            <span role="status" aria-live="polite">
              {isIntegrating ? 'Integrating worktree' : integrationPhase === 'error' ? 'Integration needs attention' : isIntegratedAction ? (delivery?.recovered ? 'Task delta delivered:' : 'Integrated:') : deliveryNeedsReview ? delivery?.summary : task.baseBranch ? 'Ready to integrate' : 'Target unavailable'}
            </span>
            {deliveryNeedsReview && <button type="button" onClick={event => { event.stopPropagation(); handleToggleExpand() }}>Review task</button>}
            {onIntegrate && !deliveryNeedsReview && <button type="button"
              disabled={isIntegrating || isReopening || isIntegratedAction || !task.baseBranch}
              aria-busy={isIntegrating}
              aria-label={isIntegrating ? 'Integrating…' : isIntegratedAction ? (delivery?.recovered ? 'Task delta delivered' : 'Integrated') : delivery?.recoverable ? 'Recover & integrate' : task.baseBranch ? `${integrationPhase === 'error' ? 'Retry integrate' : 'Integrate'} into ${task.baseBranch}` : 'Target unavailable'}
              onClick={event => { event.stopPropagation(); onIntegrate() }}
              className="swarm-outline-action inline-grid min-h-7 items-center rounded-lg border border-amber-500/50 px-3 py-1 font-medium disabled:opacity-70 disabled:cursor-not-allowed"
              title={task.baseBranch ? `Integrate changes into ${task.baseBranch}` : 'Target branch unavailable; refresh task lineage'}>
              <span aria-hidden="true" className="invisible col-start-1 row-start-1 flex items-center gap-1.5"><GitPullRequest size={12} />{task.baseBranch ? `Retry integrate into ${task.baseBranch}` : 'Target unavailable'}</span>
              <span className="col-start-1 row-start-1 flex items-center justify-center gap-1.5">
                {isIntegrating ? <Loader2 size={12} className="animate-spin motion-reduce:animate-none" /> : isIntegratedAction ? <Check size={12} /> : <GitPullRequest size={12} />}
                {isIntegrating ? 'Integrating…' : isIntegratedAction ? (delivery?.recovered ? 'Task delta delivered' : 'Integrated') : delivery?.recoverable ? 'Recover & integrate' : task.baseBranch ? `${integrationPhase === 'error' ? 'Retry integrate' : 'Integrate'} into ${task.baseBranch}` : 'Target unavailable'}
              </span>
            </button>}
          </div>}
          {canReopen && <button ref={reopenButtonRef} type="button" disabled={isIntegrating || isReopening} aria-busy={isReopening}
            aria-expanded={isReopenOpen} aria-controls={reopenFormId}
            onClick={event => { event.stopPropagation(); if (isReopenOpen) closeReopen(); else setIsReopenOpen(true) }}
            className="swarm-outline-action flex items-center gap-1 rounded border border-slate-700 px-2.5 py-1 disabled:opacity-70">
            <RotateCcw size={11} />Reopen task
          </button>}
          {showComplete && <button type="button" onClick={event => { event.stopPropagation(); onComplete?.() }}
            className="swarm-outline-action flex items-center gap-1 rounded border border-emerald-500/50 px-3 py-1 text-emerald-400"><Check size={12} />Complete</button>}
        </div>
      )}
      {isReopenOpen && (canReopen || isReopening || Boolean(actionError)) && <form id={reopenFormId} aria-label="Reopen task instructions"
        className="flex items-center flex-wrap gap-2 text-xs" onClick={event => event.stopPropagation()}
        onSubmit={event => {
          event.preventDefault(); event.stopPropagation()
          if (isIntegrating || isReopening || reopenFlightRef.current || !onReopen) return
          const identity = cardIdentityRef.current
          reopenFlightRef.current = true
          setLocalReopenPending(true); setLocalReopenError(undefined)
          void onReopen(reopenFeedback).then(outcome => {
            if (cardIdentityRef.current !== identity) return
            if (outcome.ok) { setReopenFeedback(''); closeReopen() }
            else setLocalReopenError(outcome.error)
          }).catch(error => {
            if (cardIdentityRef.current === identity) setLocalReopenError(error instanceof Error ? error.message : String(error))
          }).finally(() => {
            reopenFlightRef.current = false
            if (cardIdentityRef.current === identity) setLocalReopenPending(false)
          })
        }}>
        <input autoFocus type="text" aria-label="Instructions for the agent to resume work" disabled={isIntegrating || isReopening}
          value={reopenFeedback} onChange={event => setReopenFeedback(event.target.value)}
          placeholder="Instructions for the agent to resume work..."
          className="min-w-0 flex-1 rounded border border-slate-700 bg-transparent px-2.5 py-1"
          onKeyDown={event => { event.stopPropagation(); if (event.key === 'Escape') { event.preventDefault(); if (!isReopening) closeReopen() } }} />
        <button type="submit" disabled={isIntegrating || isReopening} aria-busy={isReopening} className="swarm-outline-action rounded border border-amber-500/50 px-3 py-1">{isReopening ? 'Reopening…' : 'Resume Run'}</button>
        <button type="button" disabled={isReopening} onClick={closeReopen} className="swarm-outline-action rounded border border-slate-700 px-3 py-1">Cancel</button>
      </form>}
      {actionError && <div role="alert" data-testid="task-error-banner" className="flex flex-wrap items-center gap-2 rounded border border-rose-500/50 bg-rose-950/40 p-2.5 text-xs text-rose-200" onClick={event => event.stopPropagation()}>
        <AlertTriangle size={14} className="shrink-0" /><span className="min-w-0 break-words">Action Failed: {actionError}</span>
        {onApprove && isPendingApproval && <button type="button" disabled={isApproving} data-testid="retry-approve-btn" onClick={onApprove}>Retry</button>}
        {onClearError && !reopenOperation.error && !localReopenError && <button type="button" onClick={onClearError}>Dismiss</button>}
      </div>}
      {/* Retained plan data is not approval authority. The accepted task response
          updates status immediately; only a fresh pending revision restores this preview. */}
      {isPendingApproval && (isPlanCard
        ? <TaskPlanChecklist document={planDoc} />
        : task.agentType === 'coder' && <TaskProposalChecklist description={task.description} />)}
      <TaskUsageFooter task={task} projectId={projectId}>
      <button ref={detailsToggleRef} type="button" className="swarm-task-details-toggle shrink-0"
        aria-expanded={expanded} aria-controls={`${detailsId} ${detailsId}-continued`} data-testid="toggle-task-details-btn"
        onClick={event => { event.stopPropagation(); handleToggleExpand() }}>
        {expanded ? 'Hide details' : 'Show details'}
      </button>
      </TaskUsageFooter>
      <div id={detailsId} hidden={!expanded} className="swarm-task-details" onClick={event => event.stopPropagation()}>
      {expanded && <>
      {!isPlanCard && task.agentType === 'coder' && task.fullPlanMarkdown && <section aria-label="Full proposed task" className="min-w-0 space-y-2 p-3 text-sm [overflow-wrap:anywhere]">
        <h4 className="font-semibold">Proposed task details</h4>
        <p className="whitespace-pre-wrap">{task.fullPlanMarkdown}</p>
      </section>}
      {isPlanCard && <section aria-label="Full current plan" className="min-w-0 space-y-3">
        <TaskRequirements document={planDoc} />
        {planDoc && <section aria-label="Complete plan definition">
          <h4>Complete plan definition · technical details</h4>
          <pre className="whitespace-pre-wrap text-xs [overflow-wrap:anywhere]">{JSON.stringify(planDoc, null, 2)}</pre>
        </section>}
        {taskProgramDef && <section aria-label="Plan execution program">
          <h4>Execution program</h4>
          <pre className="whitespace-pre-wrap text-xs [overflow-wrap:anywhere]">{JSON.stringify(taskProgramDef, null, 2)}</pre>
        </section>}
      </section>}
      <TaskWorkerBudgetMetadata task={task} />
      <h4>Result / current work</h4>
      {showIntegration && <div className="text-xs font-mono" data-testid="task-integration-lineage">
        {task.worktreeBranch || 'Source unavailable'} → {task.baseBranch || 'Target unavailable'}
        {delivery ? ` · ${delivery.summary}` : task.unintegratedCommits ? ` (${task.unintegratedCommits} ${task.unintegratedCommits === 1 ? 'commit' : 'commits'})` : ''}
      </div>}
      <TaskCardHandoff task={task} />
      {expanded && onInvestigateSession && <TaskSessionErrors task={task} onInvestigate={onInvestigateSession} />}
      {expanded && (Boolean(task.workspacesInvolved?.length) || Boolean(task.contextPoolSummary)) && (
        <div className="flex flex-col gap-1 pt-1.5 border-t border-slate-800/60 min-w-0">
          {!isMediaTask && task.workspacesInvolved && task.workspacesInvolved.length > 0 && (
            <div className="flex items-center gap-1.5 flex-wrap pt-0.5">
              <span className="font-mono text-[9px] uppercase tracking-wider text-slate-500 font-semibold">Workspaces:</span>
              {task.workspacesInvolved.map((ws, i) => {
                const wsLabel = ws.split('/').filter(Boolean).pop() || ws
                const isHero = i === 0
                return (
                  <span
                    key={i}
                    className={`font-mono text-[9px] px-1.5 py-0.5 rounded flex items-center gap-1 border ${
                      isHero
                        ? 'bg-blue-950/80 text-blue-200 border-blue-400/50 font-bold'
                        : 'bg-slate-900/60 text-slate-300 border-slate-700/50'
                    }`}
                    title={isHero ? `Primary Hero Workspace: ${ws}` : `Involved Context Workspace: ${ws}`}
                  >
                    <FolderGit2 size={9} />
                    <span>{wsLabel}</span>
                    {isHero && <span className="text-[8px] uppercase tracking-wider text-blue-400 ml-0.5 font-normal">[Hero]</span>}
                  </span>
                )
              })}
            </div>
          )}
          {task.contextPoolSummary && (
            <div className="flex items-center gap-1.5 pt-0.5 text-[9px] font-mono text-slate-400">
              <span className="text-emerald-400 font-semibold flex items-center gap-1">
                <span>✓ Context Pool:</span>
              </span>
              <span className="truncate text-slate-300" title={task.contextPoolSummary}>
                {task.contextPoolSummary}
              </span>
            </div>
          )}
        </div>
      )}
      <TaskAttention attention={attention} />
      {/* ROUTER AGENT FAILURE ALERT BANNER */}
      {expanded && (task.routerAlert || (task as any).router_alert) && (
        <div className="flex items-start gap-2.5 p-3 rounded-lg bg-amber-950/40 border border-amber-500/60 text-amber-200 text-xs">
          <AlertTriangle size={15} className="text-amber-400 flex-shrink-0 mt-0.5" />
          <div className="flex flex-col gap-1 min-w-0 flex-1">
            <div className="flex items-center justify-between gap-2 flex-wrap">
              <span className="font-bold text-[10px] uppercase tracking-wider text-amber-300 font-mono">
                ⚠️ Router Agent Failure Alert
              </span>
              <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-amber-900/60 text-amber-300 border border-amber-500/40 font-bold">
                Swarm Default Agent
              </span>
            </div>
            <p className="text-[11px] text-amber-200/90 leading-relaxed font-mono">
              {task.routerAlert || (task as any).router_alert}
            </p>
          </div>
        </div>
      )}

      {/* PLANNING STATE BANNER */}
      {expanded && isPlanning && (
        <div className="flex flex-col p-3 rounded-lg bg-indigo-950/30 border border-indigo-500/40 space-y-2 text-xs" data-testid="task-planning-banner">
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <div className="flex items-center gap-2">
              <span className="font-bold text-indigo-400 flex items-center gap-1.5 font-mono text-[10px] uppercase">
                <Sparkles size={12} className="animate-spin text-indigo-300" />
                <span>Plan Agent Investigating...</span>
              </span>
              <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-indigo-900/40 text-indigo-300 border border-indigo-500/30 animate-pulse">
                Plan Mode (Read-Only)
              </span>
            </div>
            {expanded && taskSessionId && onOpenChat && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onOpenChat()
                }}
                className="flex items-center gap-1 px-2 py-0.5 rounded bg-indigo-900/50 hover:bg-indigo-800 text-indigo-200 text-[10px] font-mono border border-indigo-500/40 transition-colors"
                data-testid="view-planning-session-btn"
              >
                <span>Open execution session</span>
                <ExternalLink size={9} />
              </button>
            )}
          </div>
          <p className="text-slate-300 text-xs leading-relaxed">
            The Plan agent is investigating the codebase in plan mode to author an executable structured plan. Implementation is locked until your explicit plan review and acceptance.
          </p>
        </div>
      )}

      {/* Actionable recovery is independent of previous-run history. */}
      {integrationRecovery}
      </>}
      </div>
      {/* Pending review stays on the card, independently of task details. */}
      {isPendingApproval && (
        <div className="swarm-task-proposal flex flex-col space-y-2.5" onClick={event => event.stopPropagation()}>
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <div className="flex items-center gap-2">
              <span className="text-[13px] text-slate-400">
                {task.tier === 'complex' || task.agentType === 'plan' || task.outcomeType === 'plan_spec'
                  ? 'Tier 3 plan · review before launch'
                  : task.tier === 'discovery' ? 'Tier 2 discovery · review before launch' : 'Tier 1 direct · review before launch'}
              </span>
              {task.revision && task.revision > 1 && (
                <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-indigo-900/50 text-indigo-300 border border-indigo-500/30 font-bold">
                  Rev {task.revision}
                </span>
              )}
              {task.autoApprove && !hasPlanBinding && task.agentType !== 'plan' && task.outcomeType !== 'plan_spec' && (
                <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-emerald-950/60 text-emerald-300 border border-emerald-500/30 flex items-center gap-1 font-bold">
                  <Zap size={9} />
                  <span>Auto-Approved</span>
                </span>
              )}
            </div>

          </div>

          {expanded && <>
          {/* Impending Execution Agents & Resolved Models */}
          {expanded && <div className="flex flex-col gap-2 p-2.5 rounded bg-slate-900/90 border border-slate-800 text-[11px] font-mono" data-testid="task-impending-agents">
            <div className="flex items-center justify-between gap-2 flex-wrap">
              <span className="text-[10px] uppercase tracking-wider text-slate-400 font-bold flex items-center gap-1.5">
                <Bot size={12} className="text-blue-400" />
                <span>Impending Execution</span>
              </span>
              <div className="flex items-center gap-2">
                {!isMediaTask && onOpenAgentSettings && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      onOpenAgentSettings(primaryAgentName)
                    }}
                    className="text-[10px] text-slate-400 hover:text-slate-300 font-semibold flex items-center gap-1"
                    data-testid="task-model-open-agents-btn"
                    title="Configure Default in /agents"
                  >
                    <span>(/agents)</span>
                  </button>
                )}
                {task.model && onUpdateModel && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      setSelectedTaskModel('')
                      void onUpdateModel(task.id, '')
                    }}
                    className="text-[10px] text-amber-400 hover:text-amber-300 font-semibold underline decoration-dotted"
                    data-testid="task-model-reset-default-btn"
                    title="Reset to account default"
                  >
                    Reset
                  </button>
                )}
                {onUpdateModel && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      if (isMediaTask) {
                        setIsModelChangerOpen(!isModelChangerOpen)
                      } else if (onOpenTaskModelChanger) {
                        onOpenTaskModelChanger(task)
                      } else if (onOpenAgentSettings) {
                        onOpenAgentSettings(primaryAgentName)
                      }
                    }}
                    className="text-[10px] text-blue-400 hover:text-blue-300 flex items-center gap-1 font-semibold underline decoration-dotted"
                    data-testid="task-card-change-model-btn"
                  >
                    <Settings2 size={11} />
                    <span>{isMediaTask && isModelChangerOpen ? 'Close Settings' : 'Change Model'}</span>
                  </button>
                )}
              </div>
            </div>

            {modelPreviewQuery.isError && (
              <div className="p-2 rounded bg-amber-950/40 border border-amber-500/30 text-amber-300 text-[10px] font-mono flex items-center gap-1.5" data-testid="task-model-preview-error">
                <AlertTriangle size={12} className="text-amber-400 shrink-0" />
                <span>Model preview unavailable: {(modelPreviewQuery.error as Error)?.message || 'Failed to load model preview'}</span>
              </div>
            )}

            <div className="flex flex-wrap items-center gap-2 pt-0.5">
              {impendingAgents.map((ag, idx) => (
                <div key={idx} className="flex items-center gap-1.5 px-2 py-1 rounded bg-slate-950 border border-slate-800 text-xs">
                  <span className="text-indigo-300 font-bold">{ag.label}</span>
                  <span className="text-slate-500">•</span>
                  <span className="text-slate-300 flex items-center gap-1">
                    <span className="text-slate-500">Model:</span>
                    <strong className="text-white font-mono">{ag.model}</strong>
                  </span>
                  <span className={`text-[9px] px-1 py-0.2 rounded border font-semibold ${
                    ag.isOverride
                      ? 'bg-amber-950/60 text-amber-300 border-amber-500/40'
                      : 'bg-slate-800 text-slate-400 border-slate-700'
                  }`}>
                    {ag.isOverride ? 'Task Override' : 'Default'}
                  </span>
                </div>
              ))}
            </div>

            {/* Media Model Changer Drawer (media tools only, not Swarm agent settings) */}
            {isMediaTask && isModelChangerOpen && onUpdateModel && (
              <div
                className="mt-2 p-3 rounded-lg bg-slate-950 border border-slate-750 flex flex-col gap-2.5"
                onClick={(e) => e.stopPropagation()}
              >
                <div className="flex items-center justify-between text-[11px] gap-2 flex-wrap">
                  <span className="font-semibold text-slate-300">Media Model Selection:</span>
                  <span className="text-[10px] text-slate-400 font-mono">Media Settings Authority</span>
                </div>

                <div className="flex items-center gap-2 flex-wrap">
                  <select
                    value={selectedTaskModel}
                    onChange={(e) => setSelectedTaskModel(e.target.value)}
                    className="flex-1 min-w-[200px] bg-slate-900 border border-slate-700 rounded px-2.5 py-1.5 text-xs text-white"
                    aria-label="Media Task Model"
                  >
                    <option value="">Account Default ({impendingAgents[0]?.model || 'Default'})</option>
                    {(task.agentType === 'video'
                      ? (videoModelOptions || [])
                      : task.agentType === 'sound' || task.agentType === 'audio'
                        ? (audioModelOptions || [])
                        : (imageModelOptions || [])
                    ).map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.label || m.id}
                      </option>
                    ))}
                  </select>

                  <button
                    type="button"
                    onClick={() => {
                      void onUpdateModel(task.id, selectedTaskModel)
                      setIsModelChangerOpen(false)
                    }}
                    className="px-3 py-1.5 rounded bg-blue-600 hover:bg-blue-500 text-white font-bold text-xs"
                    data-testid="task-model-apply-override-btn"
                  >
                    Apply to Task
                  </button>

                  {task.model && (
                    <button
                      type="button"
                      onClick={() => {
                        setSelectedTaskModel('')
                        void onUpdateModel(task.id, '')
                        setIsModelChangerOpen(false)
                      }}
                      className="px-2.5 py-1.5 rounded bg-slate-800 text-slate-300 hover:text-white text-xs border border-slate-700"
                      data-testid="task-model-reset-default-btn"
                    >
                      Reset to Default
                    </button>
                  )}
                </div>
              </div>
            )}
          </div>}

          {expanded && <p className="text-slate-200 text-xs leading-relaxed">
            {task.subtitle || task.title}
          </p>}

          {/* Plan Summary */}
          {expanded && task.planSummary && (
            <div data-testid="task-execution-overview" className="min-w-0 max-w-full [overflow-wrap:anywhere] p-2.5 rounded bg-slate-900/80 border border-slate-800 text-[11px] font-mono text-slate-300 whitespace-pre-line leading-relaxed">
              <div className="text-[9px] uppercase tracking-wider text-blue-400 font-bold mb-1">
                Execution Overview
              </div>
              {task.planSummary}
            </div>
          )}

          {/* Multi-Scene Video Production Script */}
          {!isSingleVideo && task.scenes && task.scenes.length > 0 && (
            <div className="flex flex-col p-2.5 rounded bg-slate-900/90 border border-slate-800 space-y-2 text-xs">
              <div className="flex items-center justify-between text-[10px] font-mono">
                <span className="font-bold text-blue-400 flex items-center gap-1.5 uppercase">
                  <Film size={11} />
                  <span>Multi-Scene Video Blueprint ({task.aspectRatio || '16:9'})</span>
                </span>
                <span className="text-slate-400">{task.scenes.length} Scenes</span>
              </div>
              <div className="space-y-1.5">
                {task.scenes.map((sc, idx) => (
                  <div key={idx} className="p-2 rounded bg-slate-950/80 border border-slate-800/80 text-[11px] font-mono text-slate-300">
                    <div className="flex items-center justify-between font-bold text-slate-200 mb-0.5">
                      <span className="text-indigo-300">{sc.title}</span>
                      <span className="text-[10px] text-slate-500 font-normal">{sc.duration_sec}s</span>
                    </div>
                    <p className="text-slate-400 text-[10px] leading-relaxed">{sc.prompt}</p>
                    {sc.visual_notes && (
                      <p className="text-[9px] text-blue-400/80 italic mt-0.5">Camera: {sc.visual_notes}</p>
                    )}
                  </div>
                ))}
              </div>
              {task.soundtrack && (
                <div className="flex items-center gap-1.5 text-[10px] font-mono text-slate-400 pt-1 border-t border-slate-800">
                  <Volume2 size={11} className="text-emerald-400 flex-shrink-0" />
                  <span className="text-slate-300 truncate" title={task.soundtrack}>Soundtrack: {task.soundtrack}</span>
                </div>
              )}
            </div>
          )}

          {/* Single Video Shot Spec */}
          {isSingleVideo && (
            <div className="flex flex-col p-2.5 rounded bg-slate-900/90 border border-slate-800 space-y-2 text-xs">
              <div className="flex items-center justify-between text-[10px] font-mono">
                <span className="font-bold text-blue-400 flex items-center gap-1.5 uppercase">
                  <Film size={11} />
                  <span>Single Video Shot Spec ({task.aspectRatio || '16:9'} · 8s)</span>
                </span>
                <span className="px-1.5 py-0.5 rounded bg-blue-950/60 text-blue-300 border border-blue-500/30 text-[9px] font-mono font-semibold">
                  Direct Model Execution
                </span>
              </div>
              <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-[10px] font-mono">
                <div className="p-1.5 rounded bg-slate-950/70 border border-slate-800/80">
                  <span className="text-slate-500 block text-[9px]">Duration</span>
                  <span className="text-white font-bold">8 Seconds</span>
                </div>
                <div className="p-1.5 rounded bg-slate-950/70 border border-slate-800/80">
                  <span className="text-slate-500 block text-[9px]">Resolution</span>
                  <span className="text-white font-bold">{task.resolution || '1080p'}</span>
                </div>
                <div className="p-1.5 rounded bg-slate-950/70 border border-slate-800/80">
                  <span className="text-slate-500 block text-[9px]">Video Model</span>
                  <span className="text-white font-bold truncate block" title={task.model || defaultVideoModel || 'Video Model (Default)'}>{task.model || defaultVideoModel || 'Video Model (Default)'}</span>
                </div>
                <div className="p-1.5 rounded bg-slate-950/70 border border-slate-800/80">
                  <span className="text-slate-500 block text-[9px]">Audio Synthesis</span>
                  <span className="text-emerald-400 font-bold">Model Native</span>
                </div>
              </div>
              <div className="p-2 rounded bg-slate-950/80 border border-slate-800/80 text-[11px] font-mono text-slate-300">
                <div className="text-[9px] text-blue-400 font-bold uppercase mb-0.5">Prompt Concept</div>
                <p className="text-slate-300 text-[10px] leading-relaxed">{task.description || task.title}</p>
                {task.scenes && task.scenes[0]?.visual_notes && (
                  <p className="text-[9px] text-blue-400/80 italic mt-1 font-mono">Camera: {task.scenes[0].visual_notes}</p>
                )}
              </div>
            </div>
          )}

          {/* Image Spec - ONLY for image/media tasks */}
          {(task.agentType === 'image' || task.outcomeType === 'media_bundle') && task.aspectRatio && (!task.scenes || task.scenes.length === 0) && (
            <div className="flex items-center gap-2 p-2 rounded bg-slate-900/80 border border-slate-800 text-[10px] font-mono text-slate-300">
              <ImageIcon size={11} className="text-blue-400" />
              <span>Aspect Ratio: <strong className="text-white">{task.aspectRatio}</strong></span>
              {task.variantCount ? (
                <>
                  <span>•</span>
                  <span>Iterations: <strong className="text-white">{task.variantCount} {task.variantCount === 1 ? 'variant' : 'variants'}</strong></span>
                </>
              ) : null}
            </div>
          )}

          {/* Code PR Spec - for code tasks */}
          {(task.agentType === 'coder' || task.outcomeType === 'code_pr' || task.outcomeType === 'bug_patch') && (
            <div className="flex items-center gap-2 p-2 rounded bg-slate-900/80 border border-slate-800 text-[10px] font-mono text-slate-300 flex-wrap">
              <Code2 size={11} className="text-indigo-400 flex-shrink-0" />
              <span className="text-emerald-400 font-semibold">Verified Local Tests</span>
              <span>•</span>
              <span className="text-slate-400">Target Integration: <strong className="text-white">{task.baseBranch || 'Target unavailable'}</strong></span>
            </div>
          )}
          {/* Audit Spec - for finder audit tasks */}
          {(task.agentType === 'finder' || task.outcomeType === 'audit_report') && (
            <div className="flex items-center gap-2 p-2 rounded bg-cyan-950/30 border border-cyan-500/40 text-[10px] font-mono text-cyan-200 flex-wrap">
              <Search size={11} className="text-cyan-400 flex-shrink-0" />
              <span>Execution Mode: <strong className="text-cyan-300 font-semibold">Read-Only Architectural Audit (@finder)</strong></span>
              <span>•</span>
              <span className="text-cyan-300">Delivers Findings Ledger Report</span>
            </div>
          )}
          {/* Worker Spec - for worker-generated tasks */}
          {isWorker && (
            <div className="flex items-center gap-2 p-2 rounded bg-indigo-950/30 border border-indigo-500/40 text-[10px] font-mono text-indigo-200 flex-wrap" data-testid="worker-task-spec">
              <Bot size={11} className="text-indigo-400 flex-shrink-0" />
              <span>Automated Worker: <strong className="text-indigo-300 font-semibold">{workerDisplayName}</strong></span>
              <span>•</span>
              <span className="text-slate-400">Worker ID: <span className="text-slate-300 font-semibold">{workerTargetId}</span></span>
              {(task.workerRunId || task.worker_run_id) && (
                <>
                  <span>•</span>
                  <span className="text-slate-400">Run: <span className="text-slate-300">{task.workerRunId || task.worker_run_id}</span></span>
                </>
              )}
              {(task.automationId || task.automation_id) && (
                <>
                  <span>•</span>
                  <span className="text-slate-400">Automation: <span className="text-slate-300">{task.automationId || task.automation_id}</span></span>
                </>
              )}
              <a
                href={swarmWorkerHref(workspaceSlug, workerTargetId)}
                onClick={handleWorkerClick}
                className="ml-auto inline-flex items-center gap-1 text-[10px] text-blue-400 hover:text-blue-300 underline font-sans cursor-pointer"
              >
                <span>View Worker in Hub</span>
                <ArrowRight size={10} />
              </a>
            </div>
          )}

          {isMediaTask && <p>Planned: {variantSlots.length} {task.agentType} output(s) · {task.aspectRatio || 'aspect ratio unspecified'}</p>}

          </>}
          {/* Technical execution details remain available on demand. */}
          {(!isPlanCard || expanded) && (hasStructuredPlan || task.fullPlanMarkdown) && (
            <div className="min-w-0 max-w-full whitespace-normal [overflow-wrap:anywhere] border border-slate-800/80 rounded bg-[#070b14]/90" data-testid="task-plan-spec">
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  setIsFullPlanOpen(!isFullPlanOpen)
                }}
                className="w-full flex items-center justify-between px-2.5 py-1.5 text-[10px] font-mono text-slate-400 hover:text-slate-200 transition-colors"
                aria-expanded={isFullPlanOpen}
                aria-controls={`${detailsId}-plan`}
                data-testid="toggle-plan-spec-btn"
              >
                <span className="flex items-center gap-1.5 font-semibold">
                  <FileText size={11} className="text-blue-400" />
                  <span>
                    {hasStructuredPlan
                      ? (isFullPlanOpen ? 'Hide execution details' : 'Show execution details')
                      : (isFullPlanOpen ? 'Hide Full Plan Spec' : 'Read Full Plan Spec & Criteria')}
                  </span>
                </span>
                {isFullPlanOpen ? <ChevronUp size={11} /> : <ChevronDown size={11} />}
              </button>
              <div id={`${detailsId}-plan`}>
              {isFullPlanOpen && (
                <div data-testid="task-plan-reader" className="min-w-0 p-3 border-t border-slate-800 text-[11px] text-slate-300 font-mono leading-relaxed whitespace-normal [overflow-wrap:anywhere] space-y-3">
                  {/* Render Structured Plan Document Checkpoints */}
                  {planCheckpointsToRender.length > 0 && (
                    <div className="space-y-2">
                      {isFullPlanOpen && (planDocTitle || planDocGoal) && (
                        <div className="min-w-0 text-xs font-bold text-white space-y-1 border-b border-slate-800/80 pb-1">
                          {planDocTitle && <div>{planDocTitle}</div>}
                          {planDocGoal && (
                            <div className="text-[10px] text-slate-400 font-normal">{planDocGoal}</div>
                          )}
                        </div>
                      )}
                      <div className="space-y-2">
                        {planCheckpointsToRender.map((cp: any, idx: number) => {
                          const tasksList = (cp.tasks && Array.isArray(cp.tasks) && cp.tasks.length > 0)
                            ? cp.tasks
                            : (cp.subtasks && Array.isArray(cp.subtasks) ? cp.subtasks : [])
                          const criteriaList = (cp.acceptanceCriteria && Array.isArray(cp.acceptanceCriteria) && cp.acceptanceCriteria.length > 0)
                            ? cp.acceptanceCriteria
                            : (cp.acceptance_criteria && Array.isArray(cp.acceptance_criteria) && cp.acceptance_criteria.length > 0)
                            ? cp.acceptance_criteria
                            : (cp.criteria && Array.isArray(cp.criteria) ? cp.criteria : [])
                          return (
                            <section key={cp.id || idx} className="swarm-plan-step space-y-1.5" data-testid={`plan-checkpoint-${cp.id || idx}`}>
                              <div className="swarm-plan-step-title min-w-0 flex items-start gap-1.5 font-semibold">
                                <span className="text-blue-400 font-mono shrink-0">{idx + 1}.</span>
                                <span className={isFullPlanOpen ? 'min-w-0' : 'swarm-plan-step-title-collapsed min-w-0'} title={cp.title}>{cp.title}</span>
                              </div>
                              {isFullPlanOpen && <>
                              {cp.objective && (
                                <p className="text-[10px] text-slate-400 leading-snug">{cp.objective}</p>
                              )}
                              {tasksList.length > 0 && (
                                <div className="space-y-0.5 pt-0.5">
                                  <span className="text-[9px] font-semibold text-slate-400 uppercase tracking-wider">Tasks:</span>
                                  <ul className="list-disc list-inside space-y-0.5 text-[10px] text-slate-300 pl-1">
                                    {tasksList.map((tText: any, tIdx: number) => {
                                      const label = typeof tText === 'string' ? tText : (tText?.title || tText?.text || JSON.stringify(tText))
                                      return (
                                        <li key={tIdx}>{label}</li>
                                      )
                                    })}
                                  </ul>
                                </div>
                              )}
                              {criteriaList.length > 0 && (
                                <div className="space-y-0.5 pt-0.5">
                                  <span className="text-[9px] font-semibold text-emerald-400/90 uppercase tracking-wider">Acceptance Criteria:</span>
                                  <ul className="space-y-0.5 text-[10px] text-slate-300 pl-1">
                                    {criteriaList.map((cText: any, cIdx: number) => {
                                      const label = typeof cText === 'string' ? cText : (cText?.title || cText?.text || cText?.criterion || JSON.stringify(cText))
                                      return (
                                        <li key={cIdx} className="flex items-start gap-1">
                                          <span className="text-emerald-400 font-bold">✓</span>
                                          <span className="min-w-0">{label}</span>
                                        </li>
                                      )
                                    })}
                                  </ul>
                                </div>
                              )}
                              {cp.notes && (
                                <div className="text-[9px] text-slate-500 italic pt-0.5">Note: {cp.notes}</div>
                              )}
                              </>}
                            </section>
                          )
                        })}
                      </div>
                    </div>
                  )}

                  {/* Render Structured Task Program Spec */}
                  {isFullPlanOpen && taskProgramDef && (taskProgramDef.stages?.length > 0 || taskProgramDef.jobs?.length > 0) && (
                    <div className="space-y-2 border-t border-slate-800/80 pt-2" data-testid="task-program-spec">
                      <div className="text-xs font-bold text-indigo-300 flex flex-wrap items-start gap-1.5 justify-between">
                        <span className="flex items-center gap-1.5">
                          <Layers size={12} className="text-indigo-400" />
                          <span>Task Program Specification ({taskProgramDef.jobs?.length || 0} Jobs across {taskProgramDef.stages?.length || 1} Stages)</span>
                        </span>
                        {taskProgramDef.id && (
                          <span className="font-mono text-[9px] text-slate-400">{taskProgramDef.id}</span>
                        )}
                      </div>
                      <div className="space-y-2">
                        {(taskProgramDef.stages || []).map((stage: any, sIdx: number) => {
                          const stageJobs = (taskProgramDef.jobs || []).filter((j: any) => j.stage_id === stage.id || (!j.stage_id && sIdx === 0))
                          return (
                            <div key={stage.id || sIdx} className="p-2 rounded bg-slate-900/60 border border-slate-800/80 space-y-1.5" data-testid={`program-stage-${stage.id || sIdx}`}>
                              <div className="flex flex-wrap items-start gap-1.5 justify-between font-mono text-[10px]">
                                <span className="font-bold text-slate-200">
                                  Stage {sIdx + 1}: {stage.id}
                                </span>
                                {stage.depends_on && stage.depends_on.length > 0 && (
                                  <span className="text-slate-400 text-[9px]">depends on: {stage.depends_on.join(', ')}</span>
                                )}
                              </div>
                              {stage.dependency_evidence && (
                                <p className="text-[10px] text-slate-400 italic">{stage.dependency_evidence}</p>
                              )}
                              <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 pt-1 font-mono text-[10px]">
                                {stageJobs.map((job: any) => (
                                  <div key={job.id} className="min-w-0 p-1.5 rounded bg-slate-950/80 border border-slate-800 space-y-1">
                                    <div className="flex flex-wrap items-start gap-1.5 justify-between">
                                      <span className="text-[9px] uppercase font-bold px-1 py-0.2 rounded bg-indigo-900/60 text-indigo-300 border border-indigo-500/30">
                                        @{job.agent_type || 'coder'}
                                      </span>
                                      <span className="min-w-0 font-bold text-white text-[10px]">{job.title || job.id}</span>
                                    </div>
                                    {job.owned_scope && job.owned_scope.length > 0 && (
                                      <div className="text-[9px] text-slate-400">
                                        <span className="text-slate-500">scope:</span> {job.owned_scope.join(', ')}
                                      </div>
                                    )}
                                    {job.deliverable && (
                                      <div className="text-[9px] text-slate-400">
                                        <span className="text-slate-500">deliverable:</span> {job.deliverable}
                                      </div>
                                    )}
                                    {job.acceptance_criteria && job.acceptance_criteria.length > 0 && (
                                      <div className="text-[9px] text-emerald-400/90 pt-0.5">
                                        <span>Acceptance Criteria:</span>
                                        <ul className="list-disc list-inside space-y-0.5">
                                          {job.acceptance_criteria.map((criterion: string, criterionIdx: number) => (
                                            <li key={criterionIdx}>{criterion}</li>
                                          ))}
                                        </ul>
                                      </div>
                                    )}
                                  </div>
                                ))}
                              </div>
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  )}

                  {/* Legacy prose is not an executable structured plan. */}
                  {planCheckpointsToRender.length === 0 && (!taskProgramDef || (!taskProgramDef.stages?.length && !taskProgramDef.jobs?.length)) && task.fullPlanMarkdown && (
                    <p role="status">Structured execution details have not been authored yet. Request changes to author a plan on this card.</p>
                  )}
                </div>
              )}
              </div>
            </div>
          )}

          {/* Refine / Actions Bar */}
          <div className="flex flex-col gap-2 pt-1 border-t border-blue-500/20">

            {isPlanRejected && (
              <div className="p-2.5 rounded bg-rose-950/40 border border-rose-500/50 text-rose-200 text-xs flex items-center gap-2" data-testid="task-plan-rejected-banner">
                <AlertTriangle size={13} className="text-rose-400 shrink-0" />
                <span className="font-mono text-[11px]">
                  <strong>Plan Rejected:</strong> This plan definition was rejected. Use &quot;Refine Plan&quot; below to adjust instructions and generate a new plan revision.
                </span>
              </div>
            )}

            <div className="flex items-center justify-between text-[11px] gap-2 flex-wrap">
              <div className="flex items-center gap-2">
                <span className="text-slate-400 font-mono text-[10px]">
                  Expected:{' '}
                  <strong className="text-white">
                    {task.agentType === 'image'
                      ? `${task.variantCount || 1} Image ${task.variantCount === 1 ? 'Variant' : 'Variants'} (${task.aspectRatio || '1:1'})`
                      : task.agentType === 'video'
                      ? (isSingleVideo
                          ? `Single Video Clip (${task.aspectRatio || '16:9'} · 8s)`
                          : `${task.scenes?.length || 2}-Scene Video Story (${task.aspectRatio || '16:9'})`)
                      : task.outcomeType === 'media_bundle'
                      ? `${task.variantCount || 1} Media Asset(s)`
                      : task.outcomeType === 'bug_patch'
                      ? 'Regression Test & Fix Diff'
                      : task.outcomeType === 'audit_report'
                      ? 'Findings Ledger Report'
                      : '1 branch PR + test suite'}
                  </strong>
                </span>
                {onRefine && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      setIsRefineOpen(!isRefineOpen)
                    }}
                    className="flex items-center gap-1 px-2 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white text-[10px] font-medium transition-colors border border-slate-700/60"
                    title="Ask Orchestrator to change only the affected requirements"
                  >
                    <Sparkles size={10} className="text-indigo-400" />
                    <span>{isRefineOpen ? 'Cancel' : 'Request changes'}</span>
                  </button>
                )}
              </div>

              <div className="flex items-center gap-2">
                {onDelete && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      onDelete()
                    }}
                    className="px-2.5 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-400 hover:text-rose-400 text-[10px] font-medium transition-colors"
                  >
                    Discard
                  </button>
                )}
                {onApprove && (
                  <button
                    type="button"
                    disabled={isApproving || isPlanRejected || isPlanTaskWithoutStructuredPlan || isPlanBindingMissingRevision}
                    onClick={(e) => {
                      e.stopPropagation()
                      if (!isApproving && !isPlanRejected && !isPlanTaskWithoutStructuredPlan && !isPlanBindingMissingRevision) {
                        onApprove()
                      }
                    }}
                    className="swarm-outline-action flex items-center gap-1.5 px-3 py-1.5 rounded font-medium text-xs"
                    data-testid="approve-task-btn"
                    title={
                      isPlanRejected
                        ? 'Plan definition was rejected. Click Refine Plan to author a revised plan.'
                        : isPlanTaskWithoutStructuredPlan
                        ? 'Waiting for structured plan to be authored before approval.'
                        : isPlanBindingMissingRevision
                        ? 'Plan definition revision guard is missing or unverified.'
                        : undefined
                    }
                  >
                    {isApproving ? (
                      <>
                        <Loader2 size={11} className="animate-spin" />
                        <span>Approving & Starting...</span>
                      </>
                    ) : (
                      <>
                        <Sparkles size={11} />
                        <span>
                          {task.agentType === 'image' || task.agentType === 'video' || task.outcomeType === 'media_bundle' || task.outcomeType === 'video_story' || task.outcomeType === 'video_clip'
                            ? `Approve & Generate (${variantSlots.length} ${variantSlots.length === 1 ? (task.agentType === 'video' ? 'Clip' : 'Variant') : 'Variants'})`
                            : 'Approve and start session'}
                        </span>
                      </>
                    )}
                  </button>
                )}
              </div>
            </div>

            {/* Inline Refine Input */}
            {isRefineOpen && onRefine && (
              <div className="flex items-center gap-2 p-2 rounded bg-slate-900 border border-slate-800 animate-in fade-in duration-200">
                <input
                  type="text"
                  value={refineFeedback}
                  onClick={(e) => e.stopPropagation()}
                  onChange={(e) => setRefineFeedback(e.target.value)}
                  aria-label="Requested requirement changes"
                  placeholder="What should change? e.g. Remove email notifications"
                  className="flex-1 bg-slate-950 border border-slate-800 rounded px-2.5 py-1 text-xs text-white placeholder-slate-500 focus:outline-none focus:border-blue-500 font-mono"
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && refineFeedback.trim()) {
                      e.stopPropagation()
                      onRefine(refineFeedback)
                      setRefineFeedback('')
                      setIsRefineOpen(false)
                    }
                  }}
                />
                <button
                  type="button"
                  disabled={!refineFeedback.trim()}
                  onClick={(e) => {
                    e.stopPropagation()
                    if (refineFeedback.trim()) {
                      onRefine(refineFeedback)
                      setRefineFeedback('')
                      setIsRefineOpen(false)
                    }
                  }}
                  className="px-3 py-1 rounded bg-indigo-600 hover:bg-indigo-500 disabled:opacity-40 text-white font-bold text-[10px] transition-colors flex-shrink-0"
                >
                  Request changes
                </button>
              </div>
            )}
          </div>
        </div>
      )}

      <div id={`${detailsId}-continued`} hidden={!expanded} className="swarm-task-details" onClick={event => event.stopPropagation()}>
      {expanded && <>
      <h4>Agents &amp; plan</h4>
      <TaskCardAgents task={task} onOpen={onInvestigateSession} />
      <TaskExpectedOutputs task={task} />
      {/* IN PROGRESS / RUNNING LIVE EXECUTION SECTION */}
      {isRunning && (
        <div className="swarm-task-running flex min-w-0 flex-col space-y-2.5 text-xs">


          {/* Agent's Created Execution Plan with Subtasks Checklist OR Compact Task Program Multi-Coder Grid */}
          {expanded && (
            <>
              {isTaskProgram ? (
            <div className="flex flex-col p-3 rounded-lg bg-slate-900/90 border border-slate-800 space-y-2.5">
              <div className="flex items-center justify-between text-[10px] font-mono">
                <span className="font-bold uppercase tracking-wider text-slate-300 flex items-center gap-1.5">
                  <Layers size={12} className="text-blue-400" />
                  <span>Parallel Multi-Agent Cohort • Stage: {activeStageId || 'Active'}</span>
                </span>
                <span className="text-slate-400 font-mono text-[9px]">
                  {completedJobsCount}/{totalJobsCount} Coders Finished
                </span>
              </div>

              {/* High-density Segmented Progress Bar */}
              <div className="w-full h-1.5 rounded-full bg-slate-800 overflow-hidden flex">
                <div
                  className="h-full bg-emerald-500 transition-all duration-300"
                  style={{ width: `${(completedJobsCount / Math.max(1, totalJobsCount)) * 100}%` }}
                />
                <div
                  className="h-full bg-blue-500 transition-all duration-300"
                  style={{ width: `${(runningJobsCount / Math.max(1, totalJobsCount)) * 100}%` }}
                />
                <div
                  className="h-full bg-amber-500 transition-all duration-300"
                  style={{ width: `${(conflictJobsCount / Math.max(1, totalJobsCount)) * 100}%` }}
                />
              </div>

              {/* Side-by-side Compact Coder Tiles */}
              <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2 pt-1 font-mono text-[10px]">
                {programJobs.map((job: any) => {
                  const jobDef = findProgramJobDef(job.job_id)
                  const isJobRunning = job.state === 'running'
                  const isDone = job.state === 'integrated' || job.state === 'completed'
                  const isHandoffReady = job.state === 'handoff_ready'
                  const isConflict = job.state === 'conflict'

                  return (
                    <div
                      key={job.job_id}
                      className={`flex flex-col p-2 rounded-lg border transition-all ${
                        isConflict
                          ? 'bg-rose-950/40 border-rose-500/50 shadow-[0_0_10px_rgba(244,63,94,0.15)]'
                          : isJobRunning
                            ? 'bg-blue-950/30 border-blue-500/40 shadow-sm'
                            : isDone
                              ? 'bg-emerald-950/20 border-emerald-500/30'
                              : isHandoffReady
                                ? 'bg-indigo-950/30 border-indigo-500/30'
                                : 'bg-slate-950/40 border-slate-800/80 text-slate-400'
                      }`}
                    >
                      <div className="flex items-center justify-between gap-1 mb-1">
                        <span className="text-[8px] uppercase font-bold px-1 py-0.2 rounded bg-indigo-900/60 text-indigo-300 border border-indigo-500/30 flex-shrink-0">
                          {jobDef?.agent_type?.toUpperCase() || 'CODER'}
                        </span>
                        <span className="font-bold text-white text-[11px] truncate flex-1" title={jobDef?.title || job.job_id}>
                          {jobDef?.title || job.job_id}
                        </span>
                        {job.attempt_number && job.attempt_number > 1 && (
                          <span className="text-[8px] px-1 py-0.2 rounded bg-amber-950/80 text-amber-300 border border-amber-500/30 font-bold flex-shrink-0">
                            Att {job.attempt_number}
                          </span>
                        )}
                      </div>

                      {jobDef?.owned_scope && jobDef.owned_scope.length > 0 && (
                        <div className="text-[9px] text-slate-400 truncate mb-1.5" title={jobDef.owned_scope.join(', ')}>
                          <span className="text-slate-500">scope:</span> {jobDef.owned_scope[0]}
                        </div>
                      )}

                      <div className="flex items-center justify-between gap-1 mt-auto pt-1 border-t border-slate-800/60 text-[9px]">
                        <span className="flex items-center gap-1">
                          {isJobRunning ? (
                            <span className="text-blue-400 font-bold flex items-center gap-1">
                              <span className="h-1.5 w-1.5 rounded-full bg-blue-400 animate-ping inline-block" />
                              <span>Running</span>
                            </span>
                          ) : isDone ? (
                            <span className="text-emerald-400 font-bold flex items-center gap-1">
                              <CheckCircle2 size={10} />
                              <span>{job.state === 'integrated' ? 'Integrated' : 'Completed'}</span>
                            </span>
                          ) : isHandoffReady ? (
                            <span className="text-indigo-300 font-semibold flex items-center gap-1">
                              <Check size={10} />
                              <span>Handoff Ready</span>
                            </span>
                          ) : isConflict ? (
                            <span className="text-rose-400 font-bold flex items-center gap-1">
                              <AlertTriangle size={10} />
                              <span>Conflict</span>
                            </span>
                          ) : (
                            <span className="text-slate-500 flex items-center gap-1">
                              <span>○ Declared</span>
                            </span>
                          )}
                        </span>

                        {isConflict && onRedeployJob && (
                          <button
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation()
                              onRedeployJob(task.id, job.job_id)
                            }}
                            className="flex items-center gap-1 px-1.5 py-0.5 rounded bg-rose-600 hover:bg-rose-500 text-white font-bold text-[9px] transition-colors shadow"
                            title="Redeploy this coder with updated base and conflict resolution instructions"
                          >
                            <RotateCcw size={9} />
                            <span>Redeploy</span>
                          </button>
                        )}

                        {expanded && job.child_session_id && onOpenChat && !isConflict && (
                          <button
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation()
                              onOpenChat()
                            }}
                            className="text-slate-400 hover:text-white text-[9px] flex items-center gap-0.5"
                            title="Inspect child session"
                          >
                            <span>Session</span>
                            <ExternalLink size={8} />
                          </button>
                        )}
                      </div>
                    </div>
                  )
                })}
              </div>
            </div>
          ) : task.activePlanCheckpoints && task.activePlanCheckpoints.length > 0 ? (
            <div className="flex flex-col p-2.5 rounded-lg bg-slate-900/80 border border-slate-800 space-y-2">
              <div className="flex items-center justify-between text-[10px] font-mono text-slate-400">
                <span className="font-bold uppercase tracking-wider text-slate-300 flex items-center gap-1.5">
                  <ListChecks size={12} className="text-blue-400" />
                  <span>Agent Execution Plan</span>
                </span>
                {task.subtasksCount && (
                  <span className="text-slate-400 font-mono text-[9px]">
                    {task.subtasksCount.completed}/{task.subtasksCount.total} complete
                  </span>
                )}
              </div>
              {task.planProgressPercent !== undefined && (
                <div className="w-full h-1.5 rounded-full bg-slate-800 overflow-hidden">
                  <div
                    className="h-full bg-blue-500 transition-all duration-300 ease-out"
                    style={{ width: `${task.planProgressPercent}%` }}
                  />
                </div>
              )}
              <div className="space-y-1.5 max-h-44 overflow-y-auto font-mono text-[10px] pr-1">
                {task.activePlanCheckpoints.map((cp) => (
                  <div key={cp.id} className="space-y-1">
                    <div className="flex items-center justify-between font-bold text-slate-200">
                      <div className="flex items-center gap-1.5">
                        <span className={cp.status === 'completed' ? 'text-emerald-400' : cp.status === 'in_progress' ? 'text-blue-400' : 'text-slate-500'}>
                          {cp.status === 'completed' ? '✓' : cp.status === 'in_progress' ? '●' : '○'}
                        </span>
                        <span>{cp.title}</span>
                      </div>
                      <span className="text-[9px] uppercase text-slate-500 font-normal">{cp.status}</span>
                    </div>
                    {cp.subtasks?.map((st) => {
                      const isActive = st.id === task.activeSubtaskId || st.status === 'in_progress'
                      const isDone = st.completed || st.status === 'completed'
                      return (
                        <div
                          key={st.id}
                          className={`flex items-start gap-1.5 pl-3 py-0.5 rounded transition-colors ${
                            isActive
                              ? 'bg-blue-950/60 text-blue-200 border-l-2 border-blue-400 font-semibold'
                              : isDone
                              ? 'text-slate-400 line-through'
                              : 'text-slate-400'
                          }`}
                        >
                          <span className={isDone ? 'text-emerald-400 font-bold' : isActive ? 'text-blue-400 font-bold animate-pulse' : 'text-slate-600'}>
                            {isDone ? '✓' : isActive ? '▶' : '·'}
                          </span>
                          <span className="truncate">{st.title}</span>
                        </div>
                      )
                    })}
                  </div>
                ))}
              </div>
            </div>
          ) : null}

            </>
          )}
        </div>
      )}

      {/* Execution Summary if available (visible when expanded) */}
      {expanded && task.planSummary && (
        <div className="p-2.5 rounded-xl bg-slate-900/60 border border-slate-800 text-[11px] font-mono text-slate-300 leading-relaxed">
          <span className="text-amber-400 font-bold block text-[9px] uppercase tracking-wider mb-0.5">Execution Summary</span>
          {task.planSummary}
        </div>
      )}

      {/* 2d. FAILED / REJECTED BANNER */}
      {expanded && task.status === 'blocked' && (
        <div role="status" className="rounded-lg border border-amber-500/30 bg-amber-950/20 p-3 text-xs text-amber-200">
          {redactIntegrationDiagnostic(task.actionNeeded || task.lastError || 'Execution is blocked or paused. Open the session to inspect the required action.')}
        </div>
      )}
      {(isFailed || isRejected) && (
        <div className="flex flex-col p-3 rounded-lg bg-rose-950/20 border border-rose-500/40 space-y-2.5 text-xs" data-testid="task-failed-banner">
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <div className="flex items-center gap-2">
              <AlertTriangle size={14} className="text-rose-400 flex-shrink-0" />
              <div className="flex flex-col">
                <span className="font-bold text-rose-300 font-mono text-[10px] uppercase">
                  {isRejected ? 'Task Rejected' : 'Task Failed'}
                </span>
                <span className="text-[11px] text-slate-300">
                  {expanded ? redactIntegrationDiagnostic(task.lastError || (isRejected ? 'This task proposal was rejected.' : 'Execution failed. Review session logs or reopen with new instructions.')) : 'Open details to inspect the error.'}
                </span>
              </div>
            </div>
            <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-rose-900/40 text-rose-300 border border-rose-500/30 font-bold uppercase">
              {isRejected ? 'Rejected' : 'Failed'}
            </span>
          </div>
        </div>
      )}

      {/* Execution Error Recovery Banner */}
      {expanded && task.lastError && ['failed', 'blocked', 'paused'].includes(task.status) && !task.sessionSummary?.sessionStates.some(state => state.lastError) && (
        <div className="flex items-start justify-between p-2.5 rounded-lg bg-rose-950/30 border border-rose-500/40 text-[11px] gap-2">
          <div className="flex items-start gap-2 min-w-0">
            <AlertTriangle size={13} className="text-rose-400 flex-shrink-0 mt-0.5" />
            <div className="space-y-0.5 min-w-0">
              <span className="font-bold text-rose-300 block">Execution Error / Test Failure:</span>
              <span className="font-mono text-slate-300 text-[10px] break-all block">{redactIntegrationDiagnostic(task.lastError)}</span>
            </div>
          </div>
          {onOpenChat && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onOpenChat()
              }}
              className="flex-shrink-0 px-2.5 py-1 rounded bg-rose-600 hover:bg-rose-500 text-white font-bold text-[10px] transition-colors flex items-center gap-1 shadow"
              title="Investigate execution session"
            >
              <Sparkles size={10} />
              <span>Investigate session</span>
            </button>
          )}
        </div>
      )}

      {/* 3. Out of Sync Warning Banner */}
      {expanded && !isPendingApproval && (Boolean(task.syncWarning) || (task.behindCommits ?? 0) > 0) && (
        <div className="flex items-center justify-between p-2 rounded-lg bg-rose-950/30 border border-rose-500/50 text-rose-200 text-[11px] gap-2">
          <div className="flex items-center gap-2 min-w-0">
            <AlertTriangle size={12} className="text-rose-400 flex-shrink-0" />
            <span className="font-bold text-rose-400 flex-shrink-0">Out of Sync Warning:</span>
            <span className="truncate">
              {task.syncWarning || `${task.baseBranch || 'Target unavailable'} branch could be out of sync (${task.behindCommits} commits behind). Rebase or synchronization recommended.`}
            </span>
          </div>
        </div>
      )}



      {expanded && (
        <>
          {/* Header toolbar inside expanded full plan, subtasks & activity logs */}
          <div className="flex items-center justify-between px-2.5 py-1.5 rounded-lg bg-slate-900/60 border border-slate-800/80 text-[11px]">
            <span className="text-slate-400 font-medium flex items-center gap-1.5">
              <MessageSquare size={12} className="text-blue-400" />
              <span>Full Plan, Subtasks & Activity Logs</span>
            </span>
            {taskSessionId && onOpenChat && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onOpenChat()
                }}
                className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-slate-800/90 hover:bg-slate-700 text-slate-200 hover:text-white text-[11px] font-medium transition-colors border border-slate-700/80 cursor-pointer shadow-sm"
                title="Open execution session"
                data-testid="task-card-chat-btn"
              >
                <MessageSquare size={12} className="text-blue-400" />
                <span>Open execution session</span>
              </button>
            )}
          </div>
      {/* 6. Worktree & Git Changes Bar (ONLY show when there are changes waiting to be committed or unintegrated commits) */}
      {!isPendingApproval && !isMediaTask && task.gitStatus !== 'unknown' && task.gitStatus !== 'stale' && (task.isDirty || hasUnintegrated) && (
        <div className="flex items-center justify-between p-2 rounded-lg bg-[#070b14] border border-slate-800/80 text-[10px] font-mono text-slate-400">
          <div className="flex items-center gap-2 truncate">
            <span className="text-indigo-400 flex items-center gap-1">
              <GitBranch size={10} />
              <span>{task.worktreeBranch || (task.worktreeName ? `agent/${task.worktreeName}` : 'agent/worktree')}</span>
            </span>
            {task.diffSummary && <span>• {task.diffSummary}</span>}
          </div>
          <div className="flex items-center gap-2 flex-shrink-0">
            {task.isDirty ? (
              <span className="text-amber-400 flex items-center gap-1 font-semibold">
                <span>●</span>
                <span>{task.dirtyCount ? `${task.dirtyCount} dirty file(s) waiting to commit` : 'Changes waiting to commit'}</span>
              </span>
            ) : (
              <span className="text-emerald-400">clean</span>
            )}
            {hasUnintegrated && (
              <>
                <span>•</span>
                <span className="text-amber-400 font-semibold">
                  {delivery?.summary || `Not integrated (${task.unintegratedCommits} commit(s))`}
                </span>
              </>
            )}
          </div>
        </div>
      )}

      {/* 5. Two-Column Ledger: "What did it do?" vs "What's not done yet?" */}
      {!isPendingApproval && ((task.whatDidDo && task.whatDidDo.length > 0) || (task.whatNotDone && task.whatNotDone.length > 0)) && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-2 text-[11px] p-2 rounded-lg bg-[#070b14]/70 border border-slate-800/60">
          <div className="space-y-1">
            <span className="font-mono text-[9px] uppercase font-bold text-slate-500 block">
              What did it do?
            </span>
            {(task.whatDidDo && task.whatDidDo.length > 0
              ? task.whatDidDo
              : []
            ).map((item, idx) => (
              <div key={idx} className="flex items-start gap-1.5 text-slate-300 leading-tight">
                <span className="text-emerald-400 font-bold">✓</span>
                <span className="truncate">{item}</span>
              </div>
            ))}
          </div>
          <div className="space-y-1">
            <span className="font-mono text-[9px] uppercase font-bold text-slate-500 block">
              What's not done yet?
            </span>
            {(task.whatNotDone && task.whatNotDone.length > 0
              ? task.whatNotDone
              : []
            ).map((item, idx) => (
              <div key={idx} className="flex items-start gap-1.5 text-slate-400 leading-tight">
                <span className="text-amber-400">⋯</span>
                <span className="truncate">{item}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      <TaskOutcomeDetails task={task} />
      <TaskCardOutputs task={task} onPreview={onPreviewDeliverable} />

      {/* 7. Pipeline Stepper & Footer */}
      <div className="flex items-center justify-between pt-1 border-t border-slate-800/60 text-[10px] font-mono text-slate-500">
        <div className="flex items-center gap-1.5 truncate">
          {task.stepTimeline?.map((st, idx) => (
            <span
              key={st.step}
              className={`flex items-center gap-1 ${
                st.status === 'complete'
                  ? 'text-emerald-400'
                  : st.status === 'processing'
                  ? 'text-blue-400 font-bold'
                  : 'text-slate-600'
              }`}
            >
              {idx > 0 && <span className="text-slate-700">→</span>}
              <span>{st.label}</span>
            </span>
          ))}
        </div>

        {taskSessionId && (
          <span
            onClick={(e) => {
              e.stopPropagation()
              onOpenChat?.()
            }}
            className="text-blue-400 hover:text-blue-300 hover:underline cursor-pointer flex-shrink-0"
          >
            sess_{taskSessionId.slice(0, 8)}
          </span>
        )}
      </div>
        </>
      )}
      {expanded && previousRuns}
      <button type="button" className="swarm-task-details-toggle" data-testid="collapse-task-details-btn"
        aria-expanded={expanded} aria-controls={`${detailsId} ${detailsId}-continued`}
        onClick={event => { event.stopPropagation(); handleToggleExpand() }}>Hide details · return to summary</button>
      </>}
      </div>
    </div>
  )
}

/**
 * Bottom Composer for Orchestrator Chat with safe selected-task context forwarding.
 * Supports explicit user-message envelope snapshotting on send, exact project/task/revision
 * validation, and rejects stale, missing, or cross-project tasks.
 * Includes attachments, running states (active run stop/busy), and pending permissions.
 */
export function OrchestratorChatComposer({
  contextControls,
  onCommandNavigate,
  sessionId,
  session,
  project,
  targetTask,
  selectedTaskId,
  attachedTasks = [],
  onRemoveAttachedTask,
  allTasks: _allTasks,
  onDeselectTask,
  selectedWorker,
  currentSelectedWorker,
  onDeselectWorker,
  creatingWorker = false,
  onWorkerCreationSent,
  submitMessage = continueDesktopV3Conversation,
}: {
  sessionId: string
  creatingWorker?: boolean
  onWorkerCreationSent?: () => void
  onCommandNavigate?: (page: SwarmPage) => void
  contextControls?: React.ReactNode
  /** Replace only the network submission boundary in rendered tests. */
  submitMessage?: typeof continueDesktopV3Conversation
  selectedWorker?: SelectedWorker | null
  currentSelectedWorker?: () => SelectedWorker | null
  onDeselectWorker?: (consumed: SelectedWorker | null) => void
  session?: DesktopSessionRecord | SessionSnapshot | null
  project?: ProjectSummary
  targetTask?: RunningTask | null
  selectedTaskId?: string
  attachedTasks?: RunningTask[]
  onRemoveAttachedTask?: (id: string) => void
  allTasks?: RunningTask[]
  onDeselectTask?: () => void
}) {
  const draftKey = `${project?.id || ''}:${sessionId}`
  const draftState = useSyncExternalStore(orchestratorDrafts.subscribe, () => orchestratorDrafts.get(draftKey), () => orchestratorDrafts.get(draftKey))
  const composerScope = useRef(draftKey)
  composerScope.current = draftKey
  useEffect(() => { composerScope.current = draftKey; return () => { composerScope.current = '' } }, [draftKey])
  const draft = draftState.text
  const setDraft = (value: string | ((previous: string) => string)) => orchestratorDrafts.set(draftKey, typeof value === 'function' ? value(orchestratorDrafts.get(draftKey).text) : value)
  const composerRef = useRef<HTMLTextAreaElement>(null)
  useLayoutEffect(() => {
    const input = composerRef.current
    if (!input) return
    const resize = () => {
      input.style.height = 'auto'
      input.style.height = `${Math.min(input.scrollHeight, window.innerHeight * 0.32)}px`
      if (input.selectionEnd === input.value.length) input.scrollTop = input.scrollHeight
    }
    resize()
    const observer = new ResizeObserver(resize)
    if (input.parentElement) observer.observe(input.parentElement)
    window.addEventListener('resize', resize)
    return () => { observer.disconnect(); window.removeEventListener('resize', resize) }
  }, [draft])
  const [commandsOpen, setCommandsOpen] = useState(false)
  const [codexUsageOpen, setCodexUsageOpen] = useState(false)
  const [commandQuery, setCommandQuery] = useState('')
  const [commandIndex, setCommandIndex] = useState(0)
  const composingRef = useRef(false)
  const commands = filterOrchestrateCommands(commandQuery)
  const selectCommand = (command: OrchestrateCommand) => {
    setCommandsOpen(false)
    setSendError(null)
    if ('action' in command) setCodexUsageOpen(true)
    else if (onCommandNavigate) onCommandNavigate(command.page)
    else setSendError('Navigation is unavailable. Return to the Orchestrate workspace to use commands.')
    composerRef.current?.focus()
  }
  useEffect(() => { if (draftState.focus) composerRef.current?.focus() }, [draftKey, draftState.focus])
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<string | null>(null)
  const [attachments, setAttachments] = useState<DesktopV3MediaReference[]>([])
  const [uploadingAttachment, setUploadingAttachment] = useState(false)
  const [attachmentFailedFiles, setAttachmentFailedFiles] = useState<File[]>([])
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  const activeRun = useDesktopV3CacheSelector(
    useCallback((state) => {
      const runs = Object.values(state.liveRunsBySession[sessionId] ?? {})
      return runs.find((r) => r.status === 'running' || r.status === 'pending_executor') ?? null
    }, [sessionId])
  )
  const isRunning = Boolean(activeRun && activeRun.runId)
  const dictation = useOrchestratorDictation(draftKey, sending || isRunning, setDraft)
  const cacheSession = useDesktopV3CacheSelector(
    useCallback((state) => {
      const record = state.sessionsById[sessionId]
      return record?.kind === 'full' ? record.session : null
    }, [sessionId])
  )

  const handleStopRun = async () => {
    if (!activeRun?.runId) return
    try {
      const activeSession = session ?? cacheSession ?? null
      const route = resolveDesktopChatRouteFromSession(activeSession, [])
      const stopRequest = resolveDesktopV3StopRunRequest({
        route,
        runId: activeRun.runId,
        session: activeSession,
      })
      await stopSessionV3Run(sessionId, stopRequest)
    } catch (err: any) {
      setSendError(err?.message || 'Failed to stop agent execution')
    }
  }

  const handleProcessComposerFiles = async (files: FileList | File[] | null) => {
    if (!files || files.length === 0 || !sessionId) return
    const fileArray = Array.from(files)
    setUploadingAttachment(true)
    setSendError(null)
    setAttachmentFailedFiles([])
    const failed: File[] = []
    const capability = await getDesktopV3MediaCapability(sessionId).catch(() => null)
    for (const file of fileArray) {
      try {
        const admission = admitComposerFile(file, capability)
        if (admission.kind === 'rejected') {
          throw new Error(admission.reason)
        }
        if (admission.kind === 'media') {
          const uploaded = await uploadDesktopV3MediaAsset({
            sessionId,
            file,
            mimeType: admission.mimeType || file.type || 'application/octet-stream',
            modality: admission.capability.modality,
            fileType: admission.fileType,
            contractToken: capability?.contract_token,
          })
          if (composerScope.current !== draftKey) return
          setAttachments((prev) => {
            if (prev.some((p) => p.asset_id === uploaded.asset_id)) return prev
            return [...prev, uploaded]
          })
          continue
        }
        if (admission.kind === 'text') {
          const text = await file.text()
          if (composerScope.current !== draftKey) return
          setDraft((prev) => (prev.trim() ? `${prev}\n\n[File: ${file.name}]\n${text}` : `[File: ${file.name}]\n${text}`))
          continue
        }
      } catch (err: any) {
        failed.push(file)
        setSendError(err?.message || `Failed to attach "${file.name}"`)
      }
    }
    if (failed.length > 0) {
      setAttachmentFailedFiles(failed)
    }
    setUploadingAttachment(false)
    if (fileInputRef.current) {
      fileInputRef.current.value = ''
    }
  }

  const handleFileSelected = (e: React.ChangeEvent<HTMLInputElement>) => {
    void handleProcessComposerFiles(e.target.files)
  }

  const handleSend = async () => {
    if (composingRef.current) return
    if (dictation.active) {
      dictation.toggle()
      setSendError('Finishing dictation. Review the final text, then send.')
      return
    }
    const text = draft.trim()
    const parsed = parseOrchestrateCommand(text)
    if (parsed.kind === 'command') { selectCommand(parsed.command); return }
    if (parsed.kind === 'unsupported') {
      setSendError(`Unsupported Orchestrate command ${parsed.token}. Type / for available commands; command arguments are not supported.`)
      setCommandsOpen(false)
      return
    }
    if (text === '/') { setCommandsOpen(true); setCommandQuery(''); return }
    if ((!text && attachments.length === 0) || sending || isRunning || uploadingAttachment) return

    // Invalidate capture synchronously before submission snapshots can be reused.
    dictation.cancel()
    setSending(true)
    setSendError(null)

    try {
      let finalContent = creatingWorker ? `Please propose a new worker for human approval. Here is the job I want it to do:\n\n${text}` : text
      let finalMetadata = projectConversationMessageMetadata()

      // Check if task context was intended (either targetTask or selectedTaskId is present)
      const contextTasks = targetTask ? [targetTask] : attachedTasks
      const effectiveTaskIds = contextTasks.length ? contextTasks.map(task => task.id) : selectedTaskId ? [selectedTaskId] : []
      const effectiveTaskId = effectiveTaskIds[0]
      const envelopes: string[] = []
      const contextMetadata: Record<string, unknown>[] = []
      for (const effectiveTaskId of effectiveTaskIds) {
        if (!project || !project.id || !project.id.trim()) {
          throw new Error('No active project selected for task context forwarding')
        }

        // Canonical API fetch before send: never rely solely on stale local cache
        let authoritativeTask: RunningTask | null = null
        try {
          const res = await requestJson<{ task?: any }>(
            `/v3/projects/${encodeURIComponent(project.id)}/tasks/${encodeURIComponent(effectiveTaskId)}`
          )
          if (res?.task) {
            authoritativeTask = mapBackendTasks([res.task])[0] || null
          }
        } catch (fetchErr: any) {
          throw new Error(
            `Selected task "${effectiveTaskId}" was not found in project "${project.id}": ${fetchErr?.message || 'task lookup failed'}`
          )
        }

        if (!authoritativeTask) {
          // Fail closed when missing selected ID rather than silently general message
          throw new Error(
            `Selected task "${effectiveTaskId}" was not found in project "${project.id}". Context cannot be attached.`
          )
        }

        // Validate selected task context against authoritative task:
        // compares revision, project, session, and plan
        const candidateTask = contextTasks.find(task => task.id === effectiveTaskId) || authoritativeTask
        const validation = validateSelectedTaskForContext(project, candidateTask, authoritativeTask)
        if (!validation.valid) {
          throw new Error(validation.error || 'Selected task context is invalid or stale')
        }

        // Snapshot on send: capture immutable record at the moment of send from authoritative task
        const snapshot = validation.snapshot!

        // Format explicit user-message envelope in content (no system prompt/schema changes)
        envelopes.push(buildSelectedTaskMessageEnvelope(snapshot, effectiveTaskIds.length === 1 ? text : ''))

        // Set metadata for tracking without relying on invented backend ignored metadata
        contextMetadata.push(buildSelectedTaskMessageMetadata(snapshot, {}))
      }
      if (envelopes.length) {
        finalContent = envelopes.length === 1 ? envelopes[0] : `${envelopes.join('\n\n')}\n\n${text}`
        finalMetadata = { ...finalMetadata, ...contextMetadata[0], ...(contextMetadata.length > 1 ? { selected_task_contexts: contextMetadata } : {}) }
      }

      const operation = createDesktopV3ExistingMessageOperation({
        sessionId,
        prompt: finalContent,
        metadata: finalMetadata,
        media: attachments.length > 0 ? attachments : undefined,
      })

      if (composerScope.current !== draftKey) return
      await submitWithWorkerSelection(!effectiveTaskId ? selectedWorker || null : null, async (workerMetadata) => {
        operation.request.metadata = { ...operation.request.metadata, ...workerMetadata }
        if (submitMessage) {
          await submitMessage(operation)
        } else {
          await continueDesktopV3Conversation(operation)
        }
      }, currentSelectedWorker || (() => selectedWorker || null), () => onDeselectWorker?.(selectedWorker || null))
      // Do not erase recovery context appended while an earlier message was sending.
      if (orchestratorDrafts.get(draftKey).text === draft) setDraft('')
      setAttachments([])
      if (creatingWorker && composerScope.current === draftKey) onWorkerCreationSent?.()
    } catch (err: any) {
      setSendError(err?.message || String(err))
    } finally {
      setSending(false)
    }
  }

  const effectiveTask = targetTask || (selectedTaskId ? { id: selectedTaskId, title: selectedTaskId, revision: 1 } : null)

  return (
    <div
      className="swarm-chat-composer-lane border-t border-slate-800 bg-[#0a0f1d] text-xs space-y-2 flex-shrink-0"
      data-testid="orchestrator-chat-composer"
    >
      <DesktopCodexUsageModal open={codexUsageOpen} onOpenChange={open => {
        setCodexUsageOpen(open)
        if (!open) composerRef.current?.focus()
      }} onOpenAuthSettings={() => onCommandNavigate?.('settings')} />
      {contextControls && <div className="swarm-composer-context">{contextControls}</div>}
      {commandsOpen && <div id="orchestrate-command-list" onKeyDown={(event) => { if (event.key === 'Escape') { event.preventDefault(); setCommandsOpen(false); composerRef.current?.focus() } }} role="listbox" aria-label="Orchestrate commands" className="max-h-64 overflow-y-auto rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-1">
        {commands.map((command, index) => <button key={command.name} id={`orchestrate-command-${command.name}`} type="button" role="option" aria-selected={index === commandIndex} onClick={() => selectCommand(command)} className={`block w-full rounded-lg p-2 text-left text-[var(--app-text)] ${index === commandIndex ? 'bg-[var(--app-surface-hover)]' : ''}`}>
          <strong>/{command.name}</strong> <span className="text-[var(--app-text-muted)]">{command.description}</span>
        </button>)}
        {!commands.length && <p className="p-2 text-[var(--app-text-muted)]">No matching commands. Escape returns to your draft.</p>}
      </div>}
      <div data-testid="composer-pending-permissions-notice">
        <SessionPermissionAttention key={sessionId} sessionId={sessionId} />
      </div>

      {attachedTasks.map(task => <div key={task.id} data-testid="composer-attached-task" className="flex items-center justify-between text-blue-200"><span>{task.title} · r{task.revision || 1}</span><button type="button" aria-label={`Remove task context ${task.title}`} onClick={() => onRemoveAttachedTask?.(task.id)}><X size={12} /></button></div>)}
      {/* Task Context Badge */}
      {effectiveTask && (
        <div
          className="flex items-center justify-between px-2.5 py-1 rounded-lg bg-blue-950/40 border border-blue-500/30 text-[11px] text-blue-200"
          data-testid="composer-task-context-badge"
        >
          <div className="flex items-center gap-1.5 min-w-0">
            <span className="font-semibold text-blue-300">Context:</span>
            <span className="font-medium text-white truncate max-w-[200px]" title={effectiveTask.title}>
              {effectiveTask.title}
            </span>
            <span className="font-mono text-[9px] px-1 py-0.2 rounded bg-blue-900/60 text-blue-300 border border-blue-500/30 font-bold">
              r{effectiveTask.revision || 1}
            </span>
          </div>
          {onDeselectTask && (
            <button
              type="button"
              onClick={onDeselectTask}
              className="text-slate-400 hover:text-white p-0.5 rounded hover:bg-slate-800 transition-colors"
              title="Clear selected task context"
              aria-label="Clear selected task context"
              data-testid="composer-clear-task-context-btn"
            >
              <X size={10} />
            </button>
          )}
        </div>
      )}

      {selectedWorker && !targetTask && !selectedTaskId && attachedTasks.length === 0 && (
        <div className="flex items-center justify-between rounded-lg border border-indigo-500/30 bg-indigo-950/40 px-2.5 py-1 text-indigo-200" data-testid="composer-worker-context-chip">
          <span>Next message only: {selectedWorker.name} (r{selectedWorker.revision}) · no task dispatched</span>
          <button type="button" aria-label="Remove selected worker" onClick={() => onDeselectWorker?.(selectedWorker)}><X size={12} /></button>
        </div>
      )}
      <DurableWorkerReviews />
      {/* Attachments preview list */}
      {attachments.length > 0 && (
        <div className="flex flex-wrap gap-1.5 pb-1">
          {attachments.map((att, idx) => (
            <div
              key={att.asset_id || idx}
              className="flex items-center gap-1.5 px-2 py-0.5 rounded-lg bg-slate-800 text-[11px] text-slate-200 border border-slate-700 shadow-sm"
            >
              {att.modality === 'image' ? (
                <img
                  src={`/v3/sessions/${encodeURIComponent(sessionId)}/media/${encodeURIComponent(att.asset_id)}`}
                  alt={att.file_name || 'image'}
                  className="w-4 h-4 rounded object-cover border border-slate-600 shrink-0"
                />
              ) : att.modality === 'video' ? (
                <Film size={11} className="text-purple-400 shrink-0" />
              ) : att.modality === 'audio' ? (
                <Music size={11} className="text-amber-400 shrink-0" />
              ) : (
                <Paperclip size={11} className="text-blue-400 shrink-0" />
              )}
              <span className="truncate max-w-[130px]" title={att.file_name || `${att.modality} attachment ${idx + 1}`}>
                {att.file_name || `${att.modality} attachment ${idx + 1}`}
              </span>
              <span className="text-[9px] text-slate-400 font-mono">
                {att.size > 1024 * 1024 ? `${(att.size / (1024 * 1024)).toFixed(1)} MB` : `${Math.ceil(att.size / 1024)} KB`}
              </span>
              <button
                type="button"
                onClick={() => setAttachments((prev) => prev.filter((_, i) => i !== idx))}
                className="text-slate-400 hover:text-red-300 p-0.5 transition"
                title="Remove attachment from composer (preserves retained media)"
                aria-label="Remove attachment"
              >
                <X size={10} />
              </button>
            </div>
          ))}
        </div>
      )}

      {sendError && (
        <div
          className="p-2 rounded bg-red-950/40 border border-red-500/30 text-red-200 text-[11px] flex items-center justify-between gap-2"
          data-testid="chat-send-error"
        >
          <span className="break-words flex-1">{sendError}</span>
          {attachmentFailedFiles.length > 0 && (
            <button
              type="button"
              onClick={() => void handleProcessComposerFiles(attachmentFailedFiles)}
              className="px-2 py-0.5 rounded bg-red-600/30 hover:bg-red-600/50 text-red-100 border border-red-500/40 font-semibold text-[10px] shrink-0 transition"
            >
              Retry
            </button>
          )}
        </div>
      )}

      {dictation.error && (
        <div role="alert" className="p-2 rounded bg-red-950/40 border border-red-500/30 text-red-200 text-[11px]">
          {dictation.error}
        </div>
      )}
      {dictation.active && (
        <div role="status" className="text-[11px] text-blue-300">
          {dictation.interimText || (dictation.listening ? 'Listening…' : 'Starting microphone…')}
        </div>
      )}

      <div className="swarm-composer-inputs">
        <input
          ref={fileInputRef}
          type="file"
          multiple
          hidden
          className="hidden"
          onChange={handleFileSelected}
        />
        <textarea
          ref={composerRef}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value)
            const slash = /^\s*\/([a-z-]*)$/i.exec(e.target.value)
            setCommandsOpen(Boolean(slash))
            setCommandQuery(slash?.[1] || '')
            setCommandIndex(0)
            if (sendError) setSendError(null)
          }}
          onCompositionStart={() => { composingRef.current = true }}
          onCompositionEnd={() => { composingRef.current = false }}
          aria-controls={commandsOpen ? 'orchestrate-command-list' : undefined}
          aria-activedescendant={commandsOpen && commands[commandIndex] ? `orchestrate-command-${commands[commandIndex].name}` : undefined}
          onKeyDown={(e) => {
            if (composingRef.current || e.nativeEvent.isComposing || e.keyCode === 229) return
            if (commandsOpen && e.key === 'Escape') { e.preventDefault(); setCommandsOpen(false); return }
            if (commandsOpen && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
              e.preventDefault()
              setCommandIndex((index) => commands.length ? (index + (e.key === 'ArrowDown' ? 1 : commands.length - 1)) % commands.length : 0)
              return
            }
            if (commandsOpen && e.key === 'Enter' && !e.shiftKey && commands[commandIndex]) {
              e.preventDefault(); selectCommand(commands[commandIndex]); return
            }
            if (e.key === 'Enter' && !e.shiftKey) {
              if (uploadingAttachment) return
              e.preventDefault()
              void handleSend()
            }
          }}
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault()
            if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
              void handleProcessComposerFiles(e.dataTransfer.files)
            }
          }}
          onPaste={(e) => {
            if (e.clipboardData.files && e.clipboardData.files.length > 0) {
              e.preventDefault()
              void handleProcessComposerFiles(e.clipboardData.files)
            }
          }}
          disabled={sending || isRunning}
          rows={2}
          placeholder={
            uploadingAttachment
              ? 'Uploading attachment...'
              : isRunning
              ? 'Agent is actively executing...'
              : effectiveTask
              ? `Message about "${effectiveTask.title}"...`
              : 'Message Swarm Orchestrator...'
          }
          className="swarm-expanding-input text-xs font-sans leading-relaxed disabled:opacity-60"
          data-testid="orchestrator-chat-input"
        />

        {/* Attachment button */}
        <button
          type="button"
          onClick={() => fileInputRef.current?.click()}
          disabled={sending || isRunning || uploadingAttachment}
          className="flex items-center justify-center p-2.5 rounded-xl bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white disabled:opacity-40 disabled:cursor-not-allowed transition-colors flex-shrink-0"
          title="Attach file or image"
          aria-label="Attach file or image"
          data-testid="orchestrator-chat-attach-btn"
        >
          <Paperclip size={14} className={uploadingAttachment ? 'animate-pulse text-blue-400' : ''} />
        </button>

        <button
          type="button"
          onClick={() => { dictation.toggle(); composerRef.current?.focus() }}
          disabled={sending || isRunning}
          aria-pressed={dictation.active}
          aria-label={dictation.active ? 'Stop dictation' : 'Start dictation'}
          title={dictation.active ? 'Stop dictation' : 'Start dictation'}
          className={`flex items-center justify-center p-2.5 rounded-xl disabled:opacity-40 disabled:cursor-not-allowed transition-colors flex-shrink-0 ${dictation.active ? 'bg-red-600/80 hover:bg-red-500 text-white' : 'bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white'}`}
        >
          {dictation.active ? <MicOff size={14} /> : <Mic size={14} />}
        </button>

        {/* Stop button when agent is running */}
        {isRunning && (
          <button
            type="button"
            onClick={() => void handleStopRun()}
            className="flex items-center justify-center p-2.5 rounded-xl bg-red-600/80 hover:bg-red-500 text-white transition-colors flex-shrink-0"
            title="Stop agent run"
            aria-label="Stop agent run"
            data-testid="orchestrator-chat-stop-btn"
          >
            <Square size={13} className="fill-current" />
          </button>
        )}

        {/* Send and stop occupy the same control slot. */}
        {!isRunning && <button
          type="button"
          onClick={() => void handleSend()}
          disabled={sending || isRunning || uploadingAttachment || (!dictation.active && !draft.trim() && attachments.length === 0)}
          className="flex items-center justify-center p-2.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white disabled:opacity-40 disabled:cursor-not-allowed transition-colors flex-shrink-0"
          title="Send message"
          aria-label="Send message"
          data-testid="orchestrator-chat-send-btn"
        >
          <Send size={14} className={sending ? 'animate-pulse' : ''} />
        </button>}
      </div>
    </div>
  )
}

/**
 * Right AI Chat Panel: Supports switching between Executive Project Orchestrator
 * and individual Task Worker sessions with a back-button navigation bar.
 */
function OrchestratorChatSidebar({
  onOpenMediaArtifact,
  workspaceSlug,
  repairSession = false,
  creatingWorker,
  onWorkerCreationSent,
  sessionId,
  project,
  projectSegment,
  activeTask,
  selectedTask,
  selectedTaskId,
  attachedTasks,
  onRemoveAttachedTask,
  allTasks,
  onBackToOrchestrator,
  onDeselectTask,
  selectedWorker,
  currentSelectedWorker,
  onDeselectWorker,
}: {
  sessionId: string
  workspaceSlug?: string
  repairSession?: boolean
  onOpenMediaArtifact?: (artifact: DesktopV3ArtifactCatalogEntry) => boolean
  creatingWorker?: boolean
  onWorkerCreationSent?: () => void
  selectedWorker?: SelectedWorker | null
  currentSelectedWorker?: () => SelectedWorker | null
  onDeselectWorker?: (consumed: SelectedWorker | null) => void
  project?: ProjectSummary
  projectSegment?: string
  activeTask?: RunningTask
  selectedTask?: RunningTask | null
  selectedTaskId?: string
  attachedTasks?: RunningTask[]
  onRemoveAttachedTask?: (id: string) => void
  allTasks?: RunningTask[]
  onBackToOrchestrator?: () => void
  onDeselectTask?: () => void
}) {
  const [error, setError] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const navigate = useNavigate()
  const projectPageLink = (page: SwarmPage) => project?.id ? { ...projectConversationLink(projectSegment || project.id, project.primarySessionId), search: { section: page } } : swarmPageLink(workspaceSlug, page)
  const [clearingContext, setClearingContext] = useState(false)
  const [clearSuccess, setClearSuccess] = useState(false)

  // Target task is activeTask if viewing task child session, or selectedTask if viewing orchestrator session
  const targetTask = activeTask || selectedTask || null

  const messages = useDesktopV3CacheSelector(
    useCallback((state) => selectRenderedSessionMessages(state, sessionId), [sessionId]),
    (left, right) =>
      left.committed === right.committed &&
      left.pendingUser === right.pendingUser &&
      left.liveRuns === right.liveRuns &&
      left.runIntents === right.runIntents &&
      left.currentRunIntent === right.currentRunIntent &&
      left.latestRunIntent === right.latestRunIntent
  )
  const ready = useDesktopV3CacheSelector(
    useCallback((state) => isDesktopV3SessionTailReady(state, sessionId), [sessionId])
  )
  const count = useDesktopV3CacheSelector(
    useCallback((state) => state.messagesBySession[sessionId]?.items.length ?? 0, [sessionId])
  )
  const hydrating = useDesktopV3CacheSelector(
    useCallback((state) => (state.hydrateInFlightBySession[sessionId] ?? 0) > 0, [sessionId])
  )

  const currentSession = useDesktopV3CacheSelector(
    useCallback(
      (state) => {
        const record = state.sessionsById[sessionId]
        return record?.kind === 'full' ? record.session : null
      },
      [sessionId]
    )
  )

  const sessionUsage = useDesktopV3CacheSelector(
    useCallback(
      (state) => {
        const usage = state.usageBySession[sessionId] ?? state.sessionViewsById[sessionId]?.usage_summary
        return usage && typeof usage === 'object' ? (usage as Record<string, unknown>) : null
      },
      [sessionId]
    )
  )

  const [clearError, setClearError] = useState('')
  const clearRequest = useRef<{ id: string; seq: number } | null>(null)
  const clearInFlight = useRef(false)
  const contextSequence = useDesktopV3CacheSelector(useCallback(state => state.projectionsBySession[sessionId]?.last_event_seq, [sessionId]))
  const clearScope = useRef(`${project?.id}:${sessionId}`)
  clearScope.current = `${project?.id}:${sessionId}`
  useEffect(() => { clearScope.current = `${project?.id}:${sessionId}`; return () => { clearScope.current = '' } }, [project?.id, sessionId])
  useEffect(() => { clearRequest.current = null; setClearError(''); setClearSuccess(false); setClearingContext(false) }, [project?.id, sessionId])

  const handleClearContext = async () => {
    if (!project?.id || clearInFlight.current || repairSession || activeTask || contextSequence === undefined) return
    if (!window.confirm('Clear AI context for this session? Saved messages remain in history. No new session will be created. Active work or plans must be finished first.')) return
    clearInFlight.current = true
    const scope = clearScope.current
    setClearError('')
    setClearingContext(true)
    setClearSuccess(false)
    try {
      clearRequest.current ||= { id: `desktop-v3-clear:${crypto.randomUUID()}`, seq: contextSequence }
      await clearSessionContext(sessionId, clearRequest.current.id, clearRequest.current.seq)
      if (clearScope.current !== scope) return
      clearRequest.current = null
      onDeselectTask?.()
      setClearSuccess(true)
    } catch (e) {
      if (clearScope.current === scope) {
        // Keep request identity on uncertain failures so retry cannot clear twice.
        if (e instanceof ContextClearRejected) { clearRequest.current = null; setAttempt(value => value + 1) }
        setClearError(e instanceof Error ? e.message : 'Failed to clear orchestrator context.')
      }
    } finally {
      clearInFlight.current = false
      if (clearScope.current === scope) setClearingContext(false)
    }
  }

  useEffect(() => {
    let active = true
    setError(false)
    void selectAndHydrateDesktopV3Session(sessionId).catch(() => {
      if (active) setError(true)
    })
    return () => {
      active = false
    }
  }, [sessionId, attempt])

  return (
    <aside
      aria-label="Swarm Orchestrator AI Chat"
      data-swarm-transcript-lane="sidebar"
      className="swarm-ai-sidebar relative flex min-h-0 w-[440px] flex-1 flex-col overflow-hidden rounded-3xl border border-slate-800/80 shadow-[var(--shadow-panel)]"
    >
      <header className="p-3 border-b border-slate-800"><h2>{currentSession?.title || 'New conversation'}</h2><p className="text-xs text-slate-400">{project?.name}</p></header>
      {/* Top Header: Task Navigation vs Orchestrator Header */}
      {activeTask ? (
        <div className="flex items-center justify-between p-3 border-b border-slate-800 bg-[#0a0f1d] text-xs">
          <div className="flex items-center gap-2.5 min-w-0">
            <button
              onClick={onBackToOrchestrator}
              className="flex items-center gap-1.5 px-2.5 py-1 rounded-lg bg-slate-800 hover:bg-slate-700 text-slate-200 font-semibold transition-colors border border-slate-700"
              title="Return to Executive Project Orchestrator"
            >
              <ArrowLeft size={12} />
              <span>Back to Orchestrator</span>
            </button>
            <div className="flex flex-col min-w-0">
              <span className="font-bold text-white truncate">{activeTask.title}</span>
              <span className="text-[10px] text-slate-400 font-mono flex items-center gap-1.5 flex-wrap">
                {Boolean(activeTask.workerId?.trim() || activeTask.worker_id?.trim()) ? (
                  <span className="inline-flex items-center gap-1 text-indigo-300 font-semibold" data-testid="active-task-worker-tag">
                    <Bot size={10} className="text-indigo-400" />
                    <span>{activeTask.worker_name || (activeTask.workerName && !activeTask.workerName.startsWith('@') ? activeTask.workerName : undefined) || activeTask.workerId || activeTask.worker_id}</span>
                  </span>
                ) : (
                  <span>{activeTask.workerName || '@Worker'}</span>
                )}
                <span>•</span>
                <span>{activeTask.status}</span>
                <span>•</span>
                <span>{activeTask.elapsed}</span>
              </span>
            </div>
          </div>
          <span className="text-[9px] font-mono uppercase px-2 py-0.5 rounded bg-blue-500/10 text-blue-400 border border-blue-500/20 font-bold">
            {activeTask.agentType}
          </span>
        </div>
      ) : (
        repairSession ? <div className="p-2 text-xs">
          <span>Integration repair session</span>
          <button type="button" className="p-2 underline" onClick={onBackToOrchestrator}>Back to Project Orchestrator</button>
        </div> : null
      )}

      {error && (
        <div className="flex items-center justify-between p-3 bg-red-950/40 border-b border-red-500/30 text-xs text-red-200">
          <span>Failed to load session.</span>
          <button
            onClick={() => setAttempt((a) => a + 1)}
            className="px-2 py-0.5 rounded bg-red-800 text-white font-medium hover:bg-red-700"
          >
            Retry
          </button>
        </div>
      )}

      <DesktopV3ExistingConversationPane
        onOpenMediaArtifact={onOpenMediaArtifact}
        presentation="sidebar"
        sessionId={sessionId}
        session={currentSession}
        initialHydrateStatus={error ? 'error' : hydrating ? 'loading' : ready ? 'ready' : 'loading'}
        renderedMessages={messages}
        messagesLoaded={ready}
        loadedMessageCount={count}
        composerOverride={
          <OrchestratorChatComposer
            key={`${project?.id}:${sessionId}`}
            contextControls={<>
              <ContextRemaining usage={sessionUsage} />
              {!activeTask && <button type="button" onClick={() => void handleClearContext()} disabled={clearingContext || repairSession || !ready || contextSequence === undefined} data-testid="clear-orchestrator-context-btn">{clearingContext ? 'Clearing…' : 'Clear context'}</button>}
              {clearSuccess && <span role="status">Context cleared; history preserved.</span>}
              {clearError && <span role="alert">{clearError}</span>}
            </>}
            onCommandNavigate={(page) => { void navigate(projectPageLink(page)) }}
            sessionId={sessionId}
            session={currentSession}
            project={project}
            targetTask={targetTask}
            selectedTaskId={selectedTaskId}
            attachedTasks={activeTask ? [] : attachedTasks}
            onRemoveAttachedTask={onRemoveAttachedTask}
            allTasks={allTasks}
            onDeselectTask={onDeselectTask}
            selectedWorker={activeTask ? null : selectedWorker}
            currentSelectedWorker={currentSelectedWorker}
            onDeselectWorker={onDeselectWorker}
            creatingWorker={creatingWorker}
            onWorkerCreationSent={onWorkerCreationSent}
          />
        }
        contextChip={
          activeTask
            ? {
                id: activeTask.id,
                label: activeTask.title,
                kind: 'task',
                description: `Task Session (${activeTask.agentType}) for ${activeTask.title}`,
              }
            : selectedTask
            ? {
                id: selectedTask.id,
                label: selectedTask.title,
                kind: 'selected_task',
                description: `Selected Task Context: ${selectedTask.title} (r${selectedTask.revision || 1} · ${selectedTask.agentType})`,
              }
            : project
            ? {
                id: project.id,
                label: project.name,
                kind: 'project',
                description: `Executive Orchestrator for ${project.name} (${project.linkedWorkspaces.length} workspace(s) bound)`,
              }
            : null
        }
        onContextChipRemove={selectedTask && !activeTask ? onDeselectTask : undefined}
        metadata={
          targetTask
            ? buildSelectedTaskMessageMetadata(
                {
                  projectId: project?.id || '',
                  projectName: project?.name,
                  taskId: targetTask.id,
                  taskTitle: targetTask.title,
                  agentType: targetTask.agentType,
                  status: targetTask.status,
                  sessionId: targetTask.sessionId,
                  taskRevision: targetTask.revision || 1,
                  planId: targetTask.planBinding?.planId || (targetTask as any).plan_binding?.plan_id,
                  planDefinitionRevision: targetTask.planBinding?.definitionRevision ?? (targetTask as any).plan_binding?.definition_revision,
                  snapshotTimestamp: Date.now(),
                },
                projectConversationMessageMetadata()
              )
            : projectConversationMessageMetadata()
        }
      />
    </aside>
  )
}

export function OrchestrateView({
  workerDetailId,
  workspaceSlug: workspaceSlugProp,
  initialThemeId = 'modern_navy',
}: OrchestrateViewProps) {
  const theme = ORCHESTRATE_THEMES[initialThemeId] || ORCHESTRATE_THEMES.modern_navy

  // Real user profile from /v1/auth/desktop/session
  const [userProfile, setUserProfile] = useState<{ id: string; name: string }>({
    id: '',
    name: 'Operator',
  })
  const [isEditingAccountName, setIsEditingAccountName] = useState(false)
  const [editingAccountName, setEditingAccountName] = useState('')
  const [isUpdatingAccountName, setIsUpdatingAccountName] = useState(false)
  const [accountNameError, setAccountNameError] = useState<string | null>(null)

  const projectRouteParams = useRouterState({ select: state => state.matches[state.matches.length - 1]?.params as { projectId?: string; sessionId?: string } }) ?? {}
  const routeConversationId = projectRouteParams.sessionId || ''
  const [conversationAdmission, setConversationAdmission] = useState<{ projectId: string; sessionId: string } | null>(null)
  const createProjectIntent = useRouterState({ select: state => (state.location.search as { createProject?: boolean }).createProject === true })
  // Projects State
  const [projects, setProjects] = useState<ProjectSummary[]>([])
  const [projectsLoaded, setProjectsLoaded] = useState(false)
  const routeProjectSegment = projectRouteParams.projectId || ''
  const resolvedProject = resolveProjectRoute(routeProjectSegment, projects)
  const selectedProjectId = resolvedProject?.id || ''
  const selectedProjectSegment = resolvedProject ? projectRouteSegment(resolvedProject, projects) : ''
  const projectRouteError = projectsLoaded && routeProjectSegment && !resolvedProject ? 'Project not found or name is ambiguous. Choose a project.' : ''
  const admittedParentId = admittedConversationId(conversationAdmission, selectedProjectId, routeConversationId)
  const conversationRouteScope = `${selectedProjectId}:${routeConversationId}`
  const appliedConversationRouteScope = useRef(conversationRouteScope)
  const currentConversationRouteScope = useRef(conversationRouteScope)
  currentConversationRouteScope.current = conversationRouteScope
  const projectLink = (projectId: string, sessionId?: string) => {
    const project = projects.find(item => item.id === projectId)
    return projectConversationLink(project ? projectRouteSegment(project, projects) : projectId, sessionId)
  }
  const setSelectedProjectId = (projectId: string) => { void navigate(projectId ? projectLink(projectId) : { to: '/projects' }) }
  const conversations = useProjectConversations(selectedProjectId)
  const [conversationError, setConversationError] = useState('')
  const [creatingConversation, setCreatingConversation] = useState(false)
  const conversationRequest = useRef('')
  const conversationCreating = useRef(false)
  const [admissionAttempt, setAdmissionAttempt] = useState(0)
  const [, setIsLoadingProjects] = useState<boolean>(true)
  const selectedProject = useMemo(() => {
    const project = projects.find(p => p.id === selectedProjectId)
    return project ? { ...project, primarySessionId: admittedParentId || undefined } : undefined
  }, [projects, selectedProjectId, admittedParentId])
  const selectedProjectRef = useRef(selectedProject?.id)
  selectedProjectRef.current = selectedProject?.id
  const [themeRoot, setThemeRoot] = useState<HTMLDivElement | null>(null)
  const responsiveLayout = useSwarmResponsiveLayout(themeRoot)
  const [themeCatalogRevision, setThemeCatalogRevision] = useState(0)
  const [themeSaving, setThemeSaving] = useState(false)
  const [themeError, setThemeError] = useState('')
  const projectTheme = resolveSwarmProjectTheme(selectedProject?.themeId)
  const themeOptions = useMemo(() => [...WORKSPACE_THEME_OPTIONS], [themeCatalogRevision])
  useEffect(() => {
    let active = true
    if (!selectedProjectId) return
    const refresh = createProjectThemeRefresh(selectedProjectId, async () => {
      // Theme creation and assignment may arrive together. Refresh the account catalog
      // before resolving the saved project reference; in-flight invalidations coalesce.
      try {
        const [{ project }, settings] = await Promise.all([
          requestJson<{ project: { id: string; theme_id?: string; icon_png_data_url?: string } }>(`/v3/projects/${encodeURIComponent(selectedProjectId)}`),
          getUISettings(),
        ])
        if (!active) return
        setWorkspaceThemeCatalog(settings.theme)
        setThemeCatalogRevision((revision) => revision + 1)
        setProjects((prev) => prev.map((item) => item.id === project.id ? { ...item, themeId: project.theme_id || '', iconPNGDataURL: project.icon_png_data_url || '' } : item))
        setThemeError('')
      } catch (error) {
        if (active) setThemeError(error instanceof Error ? error.message : 'Unable to refresh project theme')
      }
    })
    const unsubscribe = desktopProjects.onProjectUpdate(refresh.invalidate)
    return () => { active = false; refresh.dispose(); unsubscribe() }
  }, [selectedProjectId])
  const handleProjectThemeChange = async (themeId: string) => {
    if (!selectedProject || themeSaving) return
    setThemeSaving(true)
    setThemeError('')
    try {
      const response = await requestJson<{ project: { theme_id?: string } }>(`/v3/projects/${encodeURIComponent(selectedProject.id)}`, {
        method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(projectThemePatch(themeId)),
      })
      setProjects((prev) => prev.map((project) => project.id === selectedProject.id ? { ...project, themeId: response.project.theme_id || '' } : project))
      desktopProjects.invalidate(selectedProject.id)
    } catch (error) {
      if (selectedProjectRef.current === selectedProject.id) setThemeError(error instanceof Error ? error.message : 'Unable to save project theme')
    } finally { setThemeSaving(false) }
  }

  const handleProjectImageChange = async (id: string, image: string) => {
    const { project } = await requestJson<{ project: { id: string; icon_png_data_url?: string } }>(`/v3/projects/${encodeURIComponent(id)}`, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ icon_png_data_url: image }),
    })
    if (project?.id !== id || (project.icon_png_data_url || '') !== image) throw new Error('Project image was not saved.')
    setProjects(previous => previous.map(item => item.id === id ? { ...item, iconPNGDataURL: project.icon_png_data_url || '' } : item))
    desktopProjects.invalidate(id)
  }

  // Project Onboarding & Creation State
  const [isOnboardingActive, setIsOnboardingActive] = useState<boolean>(createProjectIntent)
  useEffect(() => { if (createProjectIntent) setIsOnboardingActive(true) }, [createProjectIntent])
  const [onboardingName, setOnboardingName] = useState('Swarm Platform')
  const [onboardingDescription, setOnboardingDescription] = useState('Core daemon, desktop client, and multi-workspace initiative')
  const [onboardingWorkspaces, setOnboardingWorkspaces] = useState<Array<{ id?: string; path: string; label: string; role: 'primary_code' | 'auxiliary'; selected: boolean }>>([])
  const [customFolderPath, setCustomFolderPath] = useState('')
  const [isEditingContext, setIsEditingContext] = useState(false)
  const [isSynthesizing, setIsSynthesizing] = useState(false)
  const [isActivating, setIsActivating] = useState(false)
  const [onboardingContext, setOnboardingContext] = useState('')

  // Canonical Desktop Projects runtime state
  const projectState = useDesktopProject(selectedProjectId)
  const tasks = projectState?.tasks ?? []
  const uploadedMedia = projectState?.media ?? []
  const projectTasksError = projectState?.error
  const projectGitPath = selectedProject?.repoPath || ''
  const projectGit = useQuery({
    queryKey: gitStatusQueryKey(projectGitPath),
    queryFn: ({ signal }) => fetchGitStatus(projectGitPath, 0, '', signal),
    enabled: Boolean(projectGitPath && projectGitPath !== '.'),
    refetchOnWindowFocus: false,
    refetchInterval: false,
  })
  const orchestratorState = useDesktopV3CacheSelector(state => {
    const id = routeConversationId
    if (!id) return 'inactive' as const
    const run = state.sessionViewsById[id]?.current_run_state
    const intent = state.currentRunIntentBySession[id]
    if (intent) return intent.status === 'running' ? 'active' as const : 'inactive' as const
    return run ? run.active ? 'active' as const : 'inactive' as const : 'unknown' as const
  })

  const taskSessionCohort = useDesktopV3CacheSelector(
    state => ({ source: tasks, tasks: tasks.map(task => taskWithCurrentSessions(task, state)) }),
    (prev, next) => prev.source === next.source && prev.tasks.every((task, index) =>
      task.sessionIds?.join(',') === next.tasks[index]?.sessionIds?.join(',')),
  )
  const tasksWithSessions = taskSessionCohort.tasks

  const taskSessionIdsKey = useMemo(() => {
    const set = new Set<string>()
    for (const t of tasksWithSessions) {
      for (const sid of extractTaskSessionIds(t as TaskSessionCandidate)) {
        set.add(sid)
      }
    }
    return Array.from(set).sort().join(',')
  }, [tasksWithSessions])

  const liveTaskSessionsData = useDesktopV3CacheSelector(
    (state) => {
      const ids = taskSessionIdsKey ? taskSessionIdsKey.split(',').filter(Boolean) : []
      const result: Record<
        string,
        {
          sessionRecord?: (typeof state.sessionsById)[string]
          view?: (typeof state.sessionViewsById)[string]
          intent?: (typeof state.currentRunIntentBySession)[string]
          liveRun?: (typeof state.liveRunsBySession)[string][string]
          planRecord?: (typeof state.plansBySession)[string]
        }
      > = {}
      for (const sid of ids) {
        const intent = state.currentRunIntentBySession?.[sid]
        const view = state.sessionViewsById?.[sid]
        const runState = view?.current_run_state
        const runId = intent?.status === 'running' && intent.run_id !== runState?.run_id
          ? intent.run_id : (runState?.run_id || intent?.run_id)
        result[sid] = {
          sessionRecord: state.sessionsById[sid],
          view,
          intent,
          liveRun: runId ? state.liveRunsBySession?.[sid]?.[runId] : undefined,
          planRecord: state.plansBySession?.[sid],
        }
      }
      return result
    },
    (prev, next) => {
      const prevKeys = Object.keys(prev)
      const nextKeys = Object.keys(next)
      if (prevKeys.length !== nextKeys.length) return false
      for (const k of nextKeys) {
        const p = prev[k]
        const n = next[k]
        if (!p || !n) return false
        if (p.sessionRecord !== n.sessionRecord) return false
        if (p.view !== n.view) return false
        if (p.intent !== n.intent) return false
        if (p.liveRun !== n.liveRun) return false
        if (p.planRecord !== n.planRecord) return false
      }
      return true
    }
  )

  // Active Chat Session state (can be executive orchestrator OR a task session)
  const [activeSessionId, setActiveSessionId] = useState<string>('')
  const [activeTaskId, setActiveTaskId] = useState<string | null>(null)

  // Middle canvas layout variant state
  const [middleVariant] = useState<MiddleCanvasVariant>('matrix')

  // Search & Filters
  const shelfFileInputRef = useRef<HTMLInputElement>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [taskSourceFilter, setTaskSourceFilter] = useState<'all' | 'worker'>('all')
  const [statusFilter, setStatusFilter] = useState<'all' | 'running' | 'needs_review' | 'queued' | 'completed'>('all')
  const [selectedTag] = useState<string>('all')
  const [markedTaskIds, setMarkedTaskIds] = useState<Set<string>>(() => new Set())
  const [managementBusy, setManagementBusy] = useState(false)
  const managementPending = useRef(new Set<string>())
  const managementEpoch = useRef(0)
  const [managementOwner] = useState(() => crypto.randomUUID())
  useEffect(() => () => { managementEpoch.current++ }, [])
  useEffect(() => { managementEpoch.current++; managementPending.current.clear(); setManagementBusy(false); setManagementMessage(''); setTaskActionErrors({}); setArchivedTasks([]); setArchivedError(''); setArchivedLoading(false); setArchivedOpen(false) }, [selectedProject?.id])
  useEffect(() => subscribeDesktopSessionReset(() => { managementEpoch.current++; managementPending.current.clear(); setManagementBusy(false); setManagementMessage(''); setTaskActionErrors({}); setArchivedTasks([]); setArchivedError(''); setArchivedLoading(false); setArchivedOpen(false) }), [])
  const [managementMessage, setManagementMessage] = useState('')
  const [archivedOpen, setArchivedOpen] = useState(false)
  const [archivedTasks, setArchivedTasks] = useState<RunningTask[]>([])
  const [archivedLoading, setArchivedLoading] = useState(false)
  const [archivedError, setArchivedError] = useState('')
  const archivedCloseRef = useRef<HTMLButtonElement>(null)
  const archivedTriggerRef = useRef<HTMLButtonElement>(null)

  // Matrix expandable drawer state
  const [expandedTaskId, setExpandedTaskId] = useState<string | null>(null)

  // Split Studio selected task state
  const [selectedTaskId, setSelectedTaskId] = useState<string>('')
  const [attachedTaskIds, setAttachedTaskIds] = useState<string[]>([])
  const [selectedWorker, setSelectedWorker] = useState<SelectedWorker | null>(null)
  const [workerChatOpen, setWorkerChatOpen] = useState(false)
  const [workerCreationRequested, setWorkerCreationRequested] = useState(false)
  const [workerChatError, setWorkerChatError] = useState('')
  const workerChatStarting = useRef(false)
  const workerChatRequestId = useRef<string | null>(null)
  const selectedWorkerRef = useRef<SelectedWorker | null>(null)
  const clearConsumedWorker = (worker: SelectedWorker | null) => {
    if (selectedWorkerRef.current === worker) { selectedWorkerRef.current = null; setSelectedWorker(null) }
  }

  // Only the asset viewer is transient; the library itself is a routed page.
  const [activeMediaViewerItem, setActiveMediaViewerItem] = useState<MediaLibraryItem | null>(null)

  // Uploaded & Tagged Media State
  const [taggedMedia, setTaggedMedia] = useState<ProjectTaskMediaRef[]>([])
  const [isUploadingMedia, setIsUploadingMedia] = useState<boolean>(false)
  const [isUploadedMediaExpanded, setIsUploadedMediaExpanded] = useState<boolean>(true)
  const [isPasteDocOpen, setIsPasteDocOpen] = useState<boolean>(false)
  const [pastedDocTitle, setPastedDocTitle] = useState<string>('')
  const [pastedDocContent, setPastedDocContent] = useState<string>('')

  // The router is the sole authority for section selection, including reloads
  // and browser back/forward. The shared layout retains project and chat state.
  const routeParams = useRouterState({
    select: (state) => state.matches[state.matches.length - 1]?.params as { workspaceSlug?: string; swarmSection?: string; workerId?: string } | undefined,
  }) ?? {}
  const navigate = useNavigate()
  const projectLocation = useRouterState({ select: state => state.location })
  useEffect(() => {
    if (!selectedProjectSegment || selectedProjectSegment === routeProjectSegment) return
    const parts = projectLocation.pathname.split('/')
    if (parts[1] !== 'projects') return
    parts[2] = encodeURIComponent(selectedProjectSegment)
    void navigate({ to: parts.join('/'), search: true, hash: true, replace: true })
  }, [selectedProjectSegment, routeProjectSegment, projectLocation.pathname, navigate])
  const routeWorkerId = useRouterState({ select: state => {
    const search = state.location.search as { workerId?: unknown }
    return typeof search?.workerId === 'string' && (search.workerId.startsWith('worker_') || search.workerId.startsWith('worker-')) ? search.workerId : undefined
  } })
  const workspaceSlug = routeParams.workspaceSlug ?? workspaceSlugProp
  const accountScopeId = getDesktopSessionIdentitySnapshot()?.accountScopeId
  useEffect(() => { setAttachedTaskIds([]) }, [selectedProjectId, accountScopeId])
  // Worker context belongs to one account, workspace, project and session only.
  const workerContextScope = `${accountScopeId || ''}:${workspaceSlug || ''}:${selectedProject?.id || ''}:${activeSessionId}`
  const previousWorkerContextScope = useRef(workerContextScope)
  const workerConversationScope = `${accountScopeId || ''}:${workspaceSlug || ''}:${selectedProject?.id || ''}`
  const workerConversationScopeRef = useRef(workerConversationScope)
  workerConversationScopeRef.current = workerConversationScope
  useEffect(() => { workerChatRequestId.current = null; setWorkerCreationRequested(false); setWorkerChatOpen(false); setWorkerChatError('') }, [workerConversationScope])
  useEffect(() => {
    if (previousWorkerContextScope.current !== workerContextScope) {
      previousWorkerContextScope.current = workerContextScope
      selectedWorkerRef.current = null
      setSelectedWorker(null)
    }
  }, [workerContextScope])
  const inspectedWorkerId = routeParams.workerId || workerDetailId || routeWorkerId
  const routeSection = useRouterState({ select: state => (state.location.search as { section?: string }).section })
  const activeNavTab: SwarmPage = swarmActivePage(routeSection || routeParams.swarmSection || (!selectedProjectId ? 'projects' : undefined), inspectedWorkerId)
  const projectPageLink = (page: SwarmPage) => selectedProjectId ? { ...projectLink(selectedProjectId, routeConversationId || undefined), search: { section: page } } : swarmPageLink(workspaceSlug, page)
  const showFullMediaCenter = activeNavTab === 'media'
  const setActiveNavTab = (page: SwarmPage) => { void navigate(projectPageLink(page)) }
  const setShowFullMediaCenter = (open: boolean) => {
    if (open) setActiveNavTab('media')
    else if (showFullMediaCenter) setActiveNavTab('home')
  }

  // Pending worker reviews awaiting acceptance
  const pendingReviews = useDesktopV3CacheSelector(selectPendingWorkerSidebarReviews, (a, b) =>
    a.length === b.length && a.every((item, index) =>
      item.permission.id === b[index].permission.id && item.permission.toolArguments === b[index].permission.toolArguments
    )
  )
  const [reviewBusyId, setReviewBusyId] = useState<string | null>(null)
  const [reviewError, setReviewError] = useState<string | null>(null)

  const handleDecideReview = async (id: string, revision: number, digest: string, action: 'accept_automation' | 'decline_automation') => {
    if (reviewBusyId) return
    setReviewBusyId(id)
    setReviewError(null)
    try {
      await decidePendingWorkerReview(getDesktopV3CacheSnapshot(), id, revision, digest, action, (input) => desktopAutomationV2.mutate(input))
    } catch (cause) {
      setReviewError(cause instanceof Error ? cause.message : 'Worker decision failed.')
    } finally {
      setReviewBusyId(null)
    }
  }

  // Deploy Task Modal State
  const queryClient = useQueryClient()

  const handleSaveAccountName = async () => {
    const trimmed = editingAccountName.trim()
    if (!trimmed || trimmed === userProfile.name || isUpdatingAccountName) {
      setIsEditingAccountName(false)
      setAccountNameError(null)
      return
    }
    setIsUpdatingAccountName(true)
    setAccountNameError(null)
    try {
      const updated = await requestJson<{ userID?: string; user_id?: string; username?: string }>('/v1/account/username', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: trimmed }),
      })
      const nextName = String(updated?.username ?? trimmed).trim()
      if (nextName) {
        updateDesktopSessionUsername(nextName)
        setUserProfile((prev) => ({ ...prev, name: nextName }))
        queryClient.setQueryData<any>(['desktop-account-context'], (current: any) =>
          current ? { ...current, username: nextName } : current
        )
      }
      setIsEditingAccountName(false)
    } catch (err: any) {
      setAccountNameError(err?.message || 'Failed to update username')
    } finally {
      setIsUpdatingAccountName(false)
    }
  }
  const [isDeployModalOpen, setIsDeployModalOpen] = useState(false)
  const deployDialogRef = useRef<HTMLDivElement>(null)
  useSwarmModalFocus(deployDialogRef, isDeployModalOpen, () => setIsDeployModalOpen(false))
  const [taskIntent, setTaskIntent] = useState<'code' | 'image' | 'video' | 'sound' | 'audit'>('code')
  const [videoResolution, setVideoResolution] = useState<string>('')
  const [videoDuration, setVideoDuration] = useState<number>(0)
  const [videoScenePrompts, setVideoScenePrompts] = useState('')
  const [videoClipCount, setVideoClipCount] = useState<number>(1)
  const [videoAspectRatio, setVideoAspectRatio] = useState<string>('')
  const [videoAttachmentError, setVideoAttachmentError] = useState<string | null>(null)
  const [featureSize, setFeatureSize] = useState<'small' | 'big'>('small')
  const [newTaskModelOverride, setNewTaskModelOverride] = useState<string>('')
  const [, setNewTaskModelScope] = useState<AgentModelControlTaskOverrideInput | null>(null)
  const [, setTaskModelOverrides] = useState<Record<string, AgentModelControlTaskOverrideInput>>({})
  const [agentSettingsOpenSignal, setAgentSettingsOpenSignal] = useState(0)
  const [agentSettingsInitialAgent, setAgentSettingsInitialAgent] = useState('system-coder')
  const [agentSettingsTaskContext, setAgentSettingsTaskContext] = useState<{
    taskId: string
    label: string
    hasOverride: boolean
    isDeployModal: boolean
    currentModel?: string
  } | null>(null)
  const modelOptionsQuery = useQuery(modelOptionsQueryOptions())
  const modelOptions = modelOptionsQuery.data ?? []
  const agentModelSettingsQuery = useQuery(agentModelSettingsQueryOptions())

  const handleOpenAgentSettings = useCallback((agentName: string) => {
    setAgentSettingsInitialAgent(agentName)
    setAgentSettingsTaskContext(null)
    setAgentSettingsOpenSignal((s) => s + 1)
  }, [])

  const [enhanceVideoPrompt, setEnhanceVideoPrompt] = useState<boolean>(false)
  const taskDraftKey = `task:${selectedProject?.id || ''}`
  const taskDraft = useSyncExternalStore(orchestratorDrafts.subscribe, () => orchestratorDrafts.get(taskDraftKey), () => orchestratorDrafts.get(taskDraftKey))
  const newTaskPrompt = taskDraft.text
  const setNewTaskPrompt = (value: string | ((previous: string) => string)) => orchestratorDrafts.set(taskDraftKey, typeof value === 'function' ? value(orchestratorDrafts.get(taskDraftKey).text) : value)
  const [newTaskWorkspace, setNewTaskWorkspace] = useState('')
  const [imageAspectRatio, setImageAspectRatio] = useState<'1:1' | '16:9' | '9:16' | '4:3'>('16:9')
  const [imagePromptState, dispatchImagePrompt] = useReducer(imagePromptReducer, initialImagePromptState)
  const imageVariants = imagePromptState.count
  const setImageVariants = (count: number) => dispatchImagePrompt({ type: 'count', count })
  useEffect(() => {
    dispatchImagePrompt({ type: 'reset' })
  }, [taskIntent, isDeployModalOpen])
  const [imageResolution, setImageResolution] = useState<'1k' | '2k' | '4k'>('1k')
  const [soundDuration, setSoundDuration] = useState<number>(30)
  const [autoApproveTask, setAutoApproveTask] = useState<boolean>(false)
  const [isDeployingTask, setIsDeployingTask] = useState(false)
  const [deployError, setDeployError] = useState<string | null>(null)
  const [imageModelOptions, setImageModelOptions] = useState<TaskModalModelOption[]>([])
  const videoCatalog = useQuery({
    queryKey: ['video-task-catalog', isDeployModalOpen],
    queryFn: () => requestJson<{ video_generation_models?: any[] }>('/v1/media/settings/catalog'),
    enabled: isDeployModalOpen,
    staleTime: 0,
  })
  const videoModelOptions = useMemo<TaskModalModelOption[]>(() =>
    (videoCatalog.data?.video_generation_models || []).map(m => ({
      id: m.id || m.model, label: m.display_name || m.model || m.id,
      ready: m.ready !== false, reason: m.reason || '', pricing: m.pricing,
      provider: m.provider, model: m.model, generationOptions: m.generation_options,
    })), [videoCatalog.data])
  const [audioModelOptions, setAudioModelOptions] = useState<TaskModalModelOption[]>([])
  const [selectedImageModel, setSelectedImageModel] = useState<string>('')
  const videoDefaults = useVideoTaskDefault(isDeployModalOpen, videoModelOptions)
  const selectedVideoModel = videoDefaults.selected
  const defaultVideoModel = videoDefaults.defaultModel
  const [selectedAudioModel, setSelectedAudioModel] = useState<string>('')
  const [defaultImageModel, setDefaultImageModel] = useState<string>('')
  const [imageDefaultError, setImageDefaultError] = useState<string | null>(null)
  const [defaultAudioModel, setDefaultAudioModel] = useState<string>('')

  const [localGenerationJobs, setLocalGenerationJobs] = useState<MediaGenerationJob[]>([])
  const [mediaViewerInitialMode, setMediaViewerInitialMode] = useState<QuickRouteMode | null>(null)
  const [mediaCatalogLoaded, setMediaCatalogLoaded] = useState<boolean>(false)
  const [isSavingModelChoice, setIsSavingModelChoice] = useState<boolean>(false)

  const deployPreviewQuery = useQuery({
    queryKey: ['projects', selectedProject?.id, 'tasks:preview', {
      prompt: newTaskPrompt.trim() || 'New task',
      intent: taskIntent,
      featureSize: taskIntent === 'code' ? featureSize : undefined,
      model: newTaskModelOverride || undefined,
      workspace: newTaskWorkspace.trim(),
      workspaceCatalog: selectedProject?.workspaces,
      agent: taskIntent === 'code' ? (featureSize === 'big' ? 'swarm' : 'coder') : (taskIntent === 'audit' ? 'finder' : taskIntent),
      tier: taskIntent === 'code' ? (featureSize === 'big' ? 'complex' : 'direct') : (taskIntent === 'audit' ? 'discovery' : 'direct'),
    }],
    queryFn: async () => {
      const targetAgent = taskIntent === 'code'
        ? (featureSize === 'big' ? 'swarm' : 'coder')
        : taskIntent === 'audit'
          ? 'finder'
          : taskIntent === 'image'
            ? 'image'
            : taskIntent === 'video'
              ? 'video'
              : 'sound'
      const targetOutcomeType = taskIntent === 'code'
        ? 'code_pr'
        : taskIntent === 'audit'
          ? 'audit_report'
          : taskIntent === 'image'
            ? 'media_bundle'
            : taskIntent === 'video'
              ? 'video_clip'
              : 'audio_clip'
      const targetTier = taskIntent === 'code'
        ? (featureSize === 'big' ? 'complex' : 'direct')
        : taskIntent === 'audit'
          ? 'discovery'
          : 'direct'
      const res = await requestJson<{ task_plan: any; model_preview: BackendTaskModelPreview; workspace_diagnostic?: string }>(
        `/v3/projects/${selectedProject!.id}/tasks:preview`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            prompt: newTaskPrompt.trim() || 'New task',
            intent: taskIntent,
            feature_size: taskIntent === 'code' ? featureSize : undefined,
            agent: targetAgent,
            outcome_type: targetOutcomeType,
            tier: targetTier,
            model: newTaskModelOverride || undefined,
            ...taskWorkspaceSelection(newTaskWorkspace, selectedProject),
          }),
        }
      )
      return { ...res.model_preview, workspaceDiagnostic: res.workspace_diagnostic }
    },
    enabled: Boolean(selectedProject?.id && isDeployModalOpen && (taskIntent === 'code' || taskIntent === 'audit')),
    staleTime: 30_000,
  })

  const deployConfig = useMemo(
    () =>
      resolveDeployImpendingConfig(
        taskIntent,
        featureSize,
        newTaskModelOverride,
        agentModelSettingsQuery.data,
        {
          selectedImageModel,
          defaultImageModel,
          selectedVideoModel,
          defaultVideoModel,
          selectedAudioModel,
          defaultAudioModel,
        },
        deployPreviewQuery.data,
        deployPreviewQuery.isError ? ((deployPreviewQuery.error as Error)?.message || 'Failed to load model preview') : null
      ),
    [
      taskIntent,
      featureSize,
      newTaskModelOverride,
      agentModelSettingsQuery.data,
      selectedImageModel,
      defaultImageModel,
      selectedVideoModel,
      defaultVideoModel,
      selectedAudioModel,
      defaultAudioModel,
      deployPreviewQuery.data,
      deployPreviewQuery.isError,
      deployPreviewQuery.error,
    ]
  )
  const resolvedDeployModel = deployConfig.resolvedModel
  const isDeployModelOverridden = deployConfig.isOverridden

  const selectedImageOption = useMemo(
    () => imageModelOptions.find((opt) => opt.id === selectedImageModel),
    [imageModelOptions, selectedImageModel]
  )
  const selectedVideoOption = useMemo(
    () => videoModelOptions.find((opt) => opt.id === selectedVideoModel),
    [videoModelOptions, selectedVideoModel]
  )
  const selectedAudioOption = useMemo(
    () => audioModelOptions.find((opt) => opt.id === selectedAudioModel),
    [audioModelOptions, selectedAudioModel]
  )

  const selectedVideoGenOptions = useMemo(
    () => selectedVideoOption?.generationOptions,
    [selectedVideoOption]
  )
  const supportedVideoAspectRatios = useMemo(
    () => resolveAllowedVideoAspectRatios(selectedVideoGenOptions),
    [selectedVideoGenOptions]
  )
  const supportedVideoResolutions = useMemo(
    () => resolveAllowedVideoResolutions(selectedVideoGenOptions),
    [selectedVideoGenOptions]
  )
  const supportedVideoDurations = useMemo(
    () => resolveAllowedVideoDurations(selectedVideoGenOptions, videoResolution),
    [selectedVideoGenOptions, videoResolution]
  )

  const videoPricingInfo = useMemo(() => {
    return resolveVideoPricing(selectedVideoOption, videoResolution, videoDuration, videoClipCount)
  }, [selectedVideoOption, videoResolution, videoDuration, videoClipCount])

  useEffect(() => {
    if (selectedVideoOption) {
      const gOpts = selectedVideoOption.generationOptions
      const allowedRatios = resolveAllowedVideoAspectRatios(gOpts)
      if (allowedRatios.length > 0) {
        if (!allowedRatios.includes(videoAspectRatio)) {
          setVideoAspectRatio(
            gOpts?.default_ratio && allowedRatios.includes(gOpts.default_ratio)
              ? gOpts.default_ratio
              : allowedRatios[0]
          )
        }
      } else {
        setVideoAspectRatio('')
      }

      const allowedRes = resolveAllowedVideoResolutions(gOpts)
      let currentRes = videoResolution
      if (allowedRes.length > 0) {
        if (!allowedRes.includes(videoResolution)) {
          currentRes =
            gOpts?.default_resolution && allowedRes.includes(gOpts.default_resolution)
              ? gOpts.default_resolution
              : allowedRes[0]
          setVideoResolution(currentRes)
        }
      } else {
        currentRes = ''
        setVideoResolution('')
      }

      const allowedDurs = resolveAllowedVideoDurations(gOpts, currentRes)
      if (allowedDurs.length > 0) {
        if (!allowedDurs.includes(videoDuration)) {
          const preferredDur =
            gOpts?.default_duration && allowedDurs.includes(gOpts.default_duration)
              ? gOpts.default_duration
              : allowedDurs[allowedDurs.length - 1] || allowedDurs[0]
          setVideoDuration(preferredDur)
        }
      } else {
        setVideoDuration(0)
      }
    } else {
      setVideoAspectRatio('')
      setVideoResolution('')
      setVideoDuration(0)
    }
  }, [selectedVideoOption])

  useEffect(() => {
    if (selectedVideoOption) {
      const allowedDurs = resolveAllowedVideoDurations(selectedVideoOption.generationOptions, videoResolution)
      if (allowedDurs.length > 0) {
        if (!allowedDurs.includes(videoDuration)) {
          setVideoDuration(allowedDurs[allowedDurs.length - 1] || allowedDurs[0])
        }
      } else {
        setVideoDuration(0)
      }
    }
  }, [videoResolution, selectedVideoOption])

  const imagePricingInfo = useMemo(() => {
    return resolveImagePricing(selectedImageOption, imageResolution, imageVariants)
  }, [selectedImageOption, imageResolution, imageVariants])

  const audioPricingInfo = useMemo(() => {
    return resolveAudioPricing(selectedAudioOption, soundDuration)
  }, [selectedAudioOption, soundDuration])

  // Active task object derived from activeTaskId (task whose child session is open in chat)
  const activeTask = useMemo(() => tasks.find((t) => t.id === activeTaskId), [tasks, activeTaskId])


  const handleDeselectTask = useCallback(() => {
    setSelectedTaskId('')
  }, [])

  // Clear task selection on project switch to prevent cross-project context leaks
  useEffect(() => {
    setSelectedTaskId('')
    setActiveTaskId(null)
    setAttachedTaskIds([])
    selectedWorkerRef.current = null
    setSelectedWorker(null)
    setTaggedMedia([])
    setThemeError('')
    setShelfUploadError(null)
    setShelfFailedFiles([])
    setIsUploadingMedia(false)
    setIsPasteDocOpen(false)
    setPastedDocTitle('')
    setPastedDocContent('')
    setActiveSessionId('')
  }, [selectedProjectId])

  // 1. Fetch User Auth, Workspaces, Automations, and Projects on mount
  useEffect(() => {
    let cancelled = false

    async function bootstrap() {
      // 1. User Session
      try {
        const auth = await requestJson<{ ok: boolean; user_id?: string; account_scope_id?: string; username?: string }>('/v1/auth/desktop/session')
        if (auth?.user_id && !cancelled) {
          const rawId = auth.user_id
          const displayName = auth.username || (rawId.startsWith('user_') ? `Operator (${rawId.slice(5, 11)})` : rawId)
          setUserProfile({
            id: rawId,
            name: displayName,
          })
        }
      } catch {}
      try {
        const me = await requestJson<{ userID?: string; username?: string }>('/v1/me')
        if (me?.username && !cancelled) {
          setUserProfile((prev) => ({
            ...prev,
            name: me.username!,
          }))
        }
      } catch {}

      // 2. Discover Registered Workspaces
      try {
        const wsRes = await requestJson<{ workspaces?: Array<{ path: string; name?: string; id?: string }> }>('/v1/workspace/list?limit=200')
        if (wsRes?.workspaces && wsRes.workspaces.length > 0 && !cancelled) {
          const detected = wsRes.workspaces.map((w, idx) => ({
            id: w.id || '',
            path: w.path,
            label: w.name || w.path.split('/').filter(Boolean).pop() || 'Workspace',
            role: (idx === 0 ? ('primary_code' as const) : ('auxiliary' as const)),
            selected: false,
          }))
          setOnboardingWorkspaces(detected)
        }
      } catch {}

      // 4. Projects from Pebble
      try {
        const res = await requestJson<{ projects?: any[] }>('/v3/projects')
        if (cancelled) return

        if (res?.projects && res.projects.length > 0) {
          const loaded: ProjectSummary[] = res.projects.map((p) => ({
            id: p.id,
            name: p.name,
            slug: p.name.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
            description: p.description || '',
            repoPath: p.workspaces?.[0]?.path || '.',
            primaryWorkspaceId: p.workspaces?.[0]?.workspace_id,
            workspaces: p.workspaces,
            branch: 'dev',
            gitStatus: 'clean',
            linkedWorkspaces: p.workspaces?.map((w: any) => w.path) || [],
            activeWorkersCount: 0,
            pendingDeliverablesCount: 0,
            runningTasksCount: 0,
            projectContext: p.project_context,
            themeId: p.theme_id || '',
            iconPNGDataURL: p.icon_png_data_url || '',
            primarySessionId: undefined,
          }))
          setProjects(loaded)
          // Route identity selects the project, never implicit workspace membership.
        } else {
          if (!cancelled) {
            setProjects([])
            setActiveSessionId('')
            setIsOnboardingActive(true)
          }
        }
      } catch (err) {
        if (!cancelled) setConversationError(err instanceof Error ? err.message : 'Unable to load projects')
      } finally {
        if (!cancelled) setProjectsLoaded(true)
      }

      // 5. Image, Video & Audio Models Catalog and UI Defaults
      try {
        const [settingsRes, catalogRes] = await Promise.all([
          getUISettings(),
          requestJson<{ image_models?: any[]; video_generation_models?: any[]; video_models?: any[]; audio_models?: any[]; default_image_model?: string; default_video_model?: string; default_audio_model?: string }>('/v1/media/settings/catalog').catch(() => null),
        ])
        if (!cancelled) {
          setWorkspaceThemeCatalog(settingsRes?.theme)
          setThemeCatalogRevision((revision) => revision + 1)
          const configuredImage = settingsRes?.tools?.image?.default_model || ''
          const configuredAudio = settingsRes?.tools?.audio?.default_model || ''

          if (catalogRes?.image_models && Array.isArray(catalogRes.image_models)) {
            const imgOpts: TaskModalModelOption[] = catalogRes.image_models.map((m: any) => ({
              id: m.id || m.model,
              label: m.display_name || m.model || m.id,
              ready: m.ready !== false,
              reason: m.reason || '',
              pricing: m.pricing,
              provider: m.provider,
              model: m.model,
              generationOptions: m.generation_options,
            }))
            setImageModelOptions(imgOpts)
            const match = imgOpts.find((m) => m.id === configuredImage)
            const catalogDefault = imgOpts.find((m) => m.id === catalogRes?.default_image_model)
            const firstReady = imgOpts.find((m) => m.ready)
            const resolvedDefaultImage = match?.id || catalogDefault?.id || firstReady?.id || (imgOpts[0]?.id ?? '')
            setDefaultImageModel(resolvedDefaultImage)
            setSelectedImageModel(resolvedDefaultImage)
          }

          if (catalogRes?.audio_models && Array.isArray(catalogRes.audio_models)) {
            const audOpts: TaskModalModelOption[] = catalogRes.audio_models.map((m: any) => ({
              id: m.id || m.model,
              label: m.display_name || m.model || m.id,
              ready: m.ready !== false,
              reason: m.reason || '',
              pricing: m.pricing,
            }))
            setAudioModelOptions(audOpts)
            const match = audOpts.find((m) => m.id === configuredAudio)
            const catalogDefault = audOpts.find((m) => m.id === catalogRes?.default_audio_model)
            const firstReady = audOpts.find((m) => m.ready)
            const resolvedDefaultAudio = match?.id || catalogDefault?.id || firstReady?.id || (audOpts[0]?.id ?? '')
            setDefaultAudioModel(resolvedDefaultAudio)
            setSelectedAudioModel(resolvedDefaultAudio)
          }
          setMediaCatalogLoaded(true)
        }
      } catch (err) {
        console.warn('Failed to load media catalog in OrchestrateView:', err)
      } finally {
        if (!cancelled) {
          setIsLoadingProjects(false)
        }
      }
    }

    void bootstrap()
    return () => {
      cancelled = true
    }
  }, [])

  // Reconcile task selection against live project tasks:
  // Invariant: Never implicitly auto-selects tasks. Prunes stale selections if task was deleted.
  useEffect(() => {
    const reconciled = reconcileSelectedTaskId(selectedTaskId, tasks)
    if (reconciled !== selectedTaskId) {
      setSelectedTaskId(reconciled)
    }
  }, [tasks, selectedTaskId])

  const mediaSyncError = projectState?.mediaError
  const handleUpdateTaskModel = useCallback(
    async (taskId: string, newModel: string, scopeInput?: AgentModelControlTaskOverrideInput | null) => {
      if (!selectedProject?.id) return
      try {
        if (scopeInput) {
          setTaskModelOverrides((prev) => ({ ...prev, [taskId]: scopeInput }))
        } else if (newModel === '') {
          setTaskModelOverrides((prev) => {
            const next = { ...prev }
            delete next[taskId]
            return next
          })
        }
        await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ model: newModel }),
        })
        queryClient.invalidateQueries({ queryKey: ['projects', selectedProject.id, 'tasks', taskId, 'model-preview'] })
        queryClient.invalidateQueries({ queryKey: ['projects', selectedProject.id, 'tasks'] })
        desktopProjects.invalidate(selectedProject.id)
      } catch (err) {
        console.error('Failed to update task model:', err)
      }
    },
    [selectedProject?.id, queryClient]
  )

  const handleApplyTaskModelFromControl = useCallback(
    async (input: AgentModelControlTaskOverrideInput) => {
      if (!agentSettingsTaskContext) return
      if (agentSettingsTaskContext.isDeployModal) {
        setNewTaskModelOverride(input.model)
        setNewTaskModelScope(input)
      } else if (agentSettingsTaskContext.taskId) {
        await handleUpdateTaskModel(agentSettingsTaskContext.taskId, input.model, input)
      }
    },
    [agentSettingsTaskContext, handleUpdateTaskModel]
  )

  const handleResetTaskModelFromControl = useCallback(async () => {
    if (!agentSettingsTaskContext) return
    if (agentSettingsTaskContext.isDeployModal) {
      setNewTaskModelOverride('')
      setNewTaskModelScope(null)
    } else if (agentSettingsTaskContext.taskId) {
      await handleUpdateTaskModel(agentSettingsTaskContext.taskId, '', null)
    }
  }, [agentSettingsTaskContext, handleUpdateTaskModel])

  const handleOpenTaskModelChanger = useCallback((t: RunningTask) => {
    const agentName = getPrimarySystemAgentName(t.agentType)
    setAgentSettingsInitialAgent(agentName)
    setAgentSettingsTaskContext({
      taskId: t.id,
      label: t.title,
      hasOverride: Boolean(t.model),
      isDeployModal: false,
      currentModel: t.model,
    })
    setAgentSettingsOpenSignal((s) => s + 1)
  }, [])

  const handleOpenDeployModelChanger = useCallback(() => {
    const agentName = taskIntent === 'code'
      ? (featureSize === 'big' ? 'swarm' : 'system-coder')
      : 'system-finder'
    setAgentSettingsInitialAgent(agentName)
    setAgentSettingsTaskContext({
      taskId: '',
      label: 'New Task',
      hasOverride: Boolean(newTaskModelOverride),
      isDeployModal: true,
      currentModel: newTaskModelOverride,
    })
    setAgentSettingsOpenSignal((s) => s + 1)
  }, [taskIntent, featureSize, newTaskModelOverride])

  const leaseManagerRef = useRef<TaskSessionLeaseManager | null>(null)
  const isDeployingTaskRef = useRef(false)
  const approvingTaskIdsRef = useRef<Set<string>>(new Set())
  const pendingDeployRequestRef = useRef<{
    payloadKey: string
    clientTaskId: string
  } | null>(null)
  const pendingApproveRequestIdsRef = useRef<Map<string, string>>(new Map())
  if (!leaseManagerRef.current) {
    leaseManagerRef.current = new TaskSessionLeaseManager({
      getControllerReady: requireDesktopV3RealtimeControllerReady,
      hydrate: (sid) => hydrateDesktopV3ChildCard(sid, { activePlan: true, permissionSummary: true }),
      ownerKeyPrefix: 'orchestrate-task',
    })
  }

  const activeTaskSessionIds = useMemo(() => {
    return computeActiveTaskSessionIds(tasksWithSessions, selectedTaskId)
  }, [tasksWithSessions, selectedTaskId])

  const activeTaskSessionIdsKey = useMemo(() => activeTaskSessionIds.join(','), [activeTaskSessionIds])

  // OrchestrateView acquires realtime session demand leases via TaskSessionLeaseManager (acquireSessionDemand)
  // Realtime session demand and plan hydration for active tasks (incremental, deduplicated, sorted)
  useEffect(() => {
    const handle = leaseManagerRef.current!.reconcile(activeTaskSessionIds)
    return () => {
      handle.cancel()
    }
  }, [activeTaskSessionIdsKey])

  // Cleanup all leases on unmount
  useEffect(() => {
    return () => {
      leaseManagerRef.current?.cleanup()
    }
  }, [])

  // Helper to ensure an active orchestrator session exists for a project
  const newConversation = async (): Promise<string | null> => {
    if (!selectedProject || conversationCreating.current) return null
    conversationCreating.current = true
    setCreatingConversation(true); setConversationError('')
    const projectId = selectedProject.id
    const routeScope = currentConversationRouteScope.current
    conversationRequest.current ||= `desktop-v3-create:${crypto.randomUUID()}`
    try {
      const sid = await createProjectConversation(projectId, conversationRequest.current)
      conversationRequest.current = ''
      if (selectedProjectRef.current === projectId && currentConversationRouteScope.current === routeScope) {
        responsiveLayout.setPanel('chat'); responsiveLayout.setNavigationOpen(false)
        void navigate(projectLink(projectId, sid))
      }
      return sid
    } catch (cause) {
      if (selectedProjectRef.current === projectId && currentConversationRouteScope.current === routeScope) setConversationError(cause instanceof Error ? cause.message : 'Unable to create conversation')
      return null
    } finally { conversationCreating.current = false; setCreatingConversation(false) }
  }
  const ensureOrchestratorSession = async (_project: ProjectSummary) => activeSessionId && !activeTaskId ? activeSessionId : newConversation()

  const openWorkerConversation = async (worker: SelectedWorker | null) => {
    if (workerChatStarting.current) return
    workerChatStarting.current = true
    const scope = workerConversationScopeRef.current
    // Keep the mounted chat and its draft while attaching worker context.
    selectedWorkerRef.current = null
    setSelectedWorker(null)
    setWorkerChatError('')
    setWorkerChatOpen(true)
    setWorkerCreationRequested(!worker)
    setActiveTaskId(null)
    setSelectedTaskId('')
    try {
      let sessionId: string | null = !activeTaskId ? activeSessionId || null : null
      if (!sessionId && selectedProject) {
        sessionId = await ensureOrchestratorSession(selectedProject)
      } else if (!sessionId) {
        throw new Error('Choose a project before opening a worker conversation.')
      }
      if (workerConversationScopeRef.current !== scope) return
      if (!sessionId) throw new Error('Could not open Orchestrator. Check your workspace connection and try again.')
      previousWorkerContextScope.current = `${accountScopeId || ''}:${workspaceSlug || ''}:${selectedProject?.id || ''}:${sessionId}`
      setActiveSessionId(sessionId)
      selectedWorkerRef.current = worker
      setSelectedWorker(worker)
    } catch (cause) {
      if (workerConversationScopeRef.current === scope) setWorkerChatError(cause instanceof Error ? cause.message : 'Could not open Orchestrator')
    } finally { workerChatStarting.current = false }
  }

  // Verify provenance before mounting a transcript, composer or permission prompt.
  useEffect(() => {
    let active = true
    appliedConversationRouteScope.current = conversationRouteScope
    setConversationAdmission(null)
    setActiveSessionId(''); setActiveTaskId(null); setSelectedTaskId(''); setAttachedTaskIds([])
    setConversationError(''); conversationRequest.current = ''
    if (selectedProject) {
      try { localStorage.setItem(`swarm:last-project:${accountScopeId || ''}`, selectedProject.id) } catch { /* Optional navigation preference; route remains authoritative. */ }
    }
    if (selectedProject && routeConversationId) {
      void requestJson<{ session: SessionSnapshot }>(`/v3/sessions/${encodeURIComponent(routeConversationId)}`).then(({ session }) => {
        requireProjectConversation(selectedProject.id, session)
        if (session.id !== routeConversationId) throw new Error('Session identity mismatch')
        if (active) {
          setConversationAdmission({ projectId: selectedProject.id, sessionId: session.id })
          setActiveSessionId(session.id)
        }
      }).catch(cause => { if (active) setConversationError(cause instanceof Error ? cause.message : 'Unable to open conversation') })
    }
    return () => { active = false }
  }, [selectedProject?.id, routeConversationId, accountScopeId, admissionAttempt])

  // Selecting task details is not consent to leave the current conversation.
  const handleSelectTask = (task: RunningTask) => {
    setSelectedTaskId(task.id)
  }

  // Only the secondary action inside task details opens execution chat.
  const handleOpenTaskSession = (task: RunningTask) => {
    const currentTask = tasks.find(row => row.id === task.id)
    const sessionId = currentTask?.sessionId || currentTask?.planBinding?.sessionId || currentTask?.planBinding?.session_id
    if (!currentTask || !sessionId) return
    handleSelectTask(currentTask)
    setActiveTaskId(currentTask.id)
    setActiveSessionId(sessionId)
  }

  const handleBackToOrchestrator = () => {
    setActiveTaskId(null)
    setSelectedTaskId('')
    setWorkerCreationRequested(false)
    setActiveSessionId(admittedParentId)
  }

  // Media handling: file uploads, pasted docs, tagging, and studio library integration
  const [shelfFailedFiles, setShelfFailedFiles] = useState<File[]>([])
  const [shelfUploadError, setShelfUploadError] = useState<string | null>(null)

  const handleFileUpload = async (files: FileList | File[] | null) => {
    if (!files || files.length === 0 || !selectedProject?.id) return
    setIsUploadingMedia(true)
    setShelfUploadError(null)
    setShelfFailedFiles([])
    const fileArray = Array.from(files)
    const failed: File[] = []

    try {
      for (const file of fileArray) {
        try {
          const isImage = file.type.startsWith('image/') || ['png', 'jpg', 'jpeg', 'webp', 'gif', 'heic', 'heif'].some((ext) => file.name.toLowerCase().endsWith('.' + ext))
          const isVideo = file.type.startsWith('video/') || ['mp4', 'webm', 'mov', 'm4v'].some((ext) => file.name.toLowerCase().endsWith('.' + ext))
          const isAudio = file.type.startsWith('audio/') || ['mp3', 'wav', 'ogg', 'm4a', 'aac'].some((ext) => file.name.toLowerCase().endsWith('.' + ext))
          const isDoc = file.type.includes('text') || file.name.endsWith('.md') || file.name.endsWith('.txt') || file.name.endsWith('.json')
          const kind = isImage ? 'image' : isVideo ? 'video' : isAudio ? 'audio' : 'doc'

          if (!isImage && !isVideo && !isAudio && !isDoc) {
            throw new Error(`File ${file.name} is not a supported media or document file.`)
          }

          let dataUrl = ''
          let textData = ''
          if (kind === 'doc' && isDoc) {
            textData = await file.text()
            if (selectedProjectRef.current !== selectedProject.id) return
            if (taskIntent === 'video') {
              // Text stays in prompt for video task flow
              setNewTaskPrompt((prev) => (prev.trim() ? `${prev.trim()}\n\n${textData.trim()}` : textData.trim()))
              continue
            }
          } else {
            if (taskIntent === 'video') {
              const validation = validateVideoAttachment(file, selectedVideoOption?.generationOptions)
              if (!validation.valid) {
                setVideoAttachmentError(validation.error || 'Invalid video reference image')
                continue
              }
              setVideoAttachmentError(null)
            }
            dataUrl = await new Promise<string>((resolve, reject) => {
              const reader = new FileReader()
              reader.onload = () => typeof reader.result === 'string' ? resolve(reader.result) : reject(new Error('Unable to read file.'))
              reader.onerror = reader.onabort = () => reject(new Error(`Unable to read ${file.name}.`))
              reader.readAsDataURL(file)
            })
          }

          if (selectedProjectRef.current !== selectedProject.id) return
          const newMedia: ProjectTaskMediaRef = {
            id: `med_${Date.now()}_${Math.random().toString(36).slice(2, 7)}`,
            title: file.name,
            filename: file.name,
            kind,
            mediaType: file.type || (kind === 'doc' ? 'text/plain' : isImage ? 'image/png' : isVideo ? 'video/mp4' : isAudio ? 'audio/mpeg' : 'application/octet-stream'),
            url: dataUrl,
            data: textData,
            sizeBytes: file.size,
            createdAt: Date.now(),
          }

          const res = await requestJson<{ media: ProjectTaskMediaRef; uploaded_media: ProjectTaskMediaRef[] }>(
            `/v3/projects/${selectedProject.id}/media`,
            {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify(newMedia),
            }
          )

          if (!res?.media?.id || !res.media.kind || (!res.media.url && !res.media.data)) throw new Error('Upload returned no persisted media. Please retry.')
          if (selectedProjectRef.current !== selectedProject.id) return
          const savedMedia = res.media
          desktopProjects.setOptimisticMedia(selectedProject.id, (prev) => {
            if (prev.some((m) => m.id === savedMedia.id)) return prev
            return [...prev, savedMedia]
          })
          if (taskIntent === 'video') {
            // Exactly one initial image reference for video generation
            setTaggedMedia([savedMedia])
          } else {
            setTaggedMedia((prev) => {
              if (prev.some((m) => m.id === savedMedia.id)) return prev
              return [...prev, savedMedia]
            })
          }
          desktopProjects.invalidate(selectedProject.id)
        } catch (fileErr: any) {
          if (selectedProjectRef.current !== selectedProject.id) return
          failed.push(file)
          setShelfUploadError(fileErr?.message || `Failed to upload "${file.name}"`)
        }
      }
    } catch (err: any) {
      if (selectedProjectRef.current !== selectedProject.id) return
      setShelfUploadError(err?.message || 'Failed to upload media')
    } finally {
      if (selectedProjectRef.current === selectedProject.id) {
        if (failed.length > 0) setShelfFailedFiles(failed)
        setIsUploadingMedia(false)
      }
    }
  }

  const handleDeleteUploadedMedia = async (mediaId: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/media/${mediaId}`, {
        method: 'DELETE',
      })
      desktopProjects.setOptimisticMedia(selectedProject.id, (prev) => prev.filter((m) => m.id !== mediaId))
      if (selectedProjectRef.current === selectedProject.id) setTaggedMedia((prev) => prev.filter((m) => m.id !== mediaId))
      desktopProjects.invalidate(selectedProject.id)
    } catch (err) {
      if (selectedProjectRef.current === selectedProject.id) setShelfUploadError(err instanceof Error ? err.message : 'Failed to delete uploaded media.')
    }
  }

  const handleAddPastedDoc = async () => {
    if (!selectedProject?.id || !pastedDocContent.trim()) return
    setShelfUploadError(null)
    if (taskIntent === 'video') {
      const docText = pastedDocContent.trim()
      setNewTaskPrompt((prev) => (prev.trim() ? `${prev.trim()}\n\n${docText}` : docText))
      setIsPasteDocOpen(false)
      setPastedDocTitle('')
      setPastedDocContent('')
      return
    }
    const docTitle = pastedDocTitle.trim() || 'Pasted Document'
    const newMedia: ProjectTaskMediaRef = {
      id: `med_doc_${Date.now()}`,
      title: docTitle,
      filename: `${docTitle.replace(/[^a-zA-Z0-9_-]/g, '_')}.md`,
      kind: 'doc',
      mediaType: 'text/markdown',
      data: pastedDocContent.trim(),
      sizeBytes: new Blob([pastedDocContent]).size,
      createdAt: Date.now(),
    }
    try {
      const res = await requestJson<{ media: ProjectTaskMediaRef; uploaded_media: ProjectTaskMediaRef[] }>(
        `/v3/projects/${selectedProject.id}/media`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(newMedia),
        }
      )
      if (!res?.media?.id || res.media.kind !== 'doc' || (!res.media.url && !res.media.data)) throw new Error('Save returned no persisted document. Please retry.')
      if (selectedProjectRef.current !== selectedProject.id) return
      const savedMedia = res.media
      desktopProjects.setOptimisticMedia(selectedProject.id, (prev) => [...prev, savedMedia])
      setTaggedMedia((prev) => [...prev, savedMedia])
      setIsPasteDocOpen(false)
      setPastedDocTitle('')
      setPastedDocContent('')
      desktopProjects.invalidate(selectedProject.id)
    } catch (err) {
      if (selectedProjectRef.current === selectedProject.id) setShelfUploadError(err instanceof Error ? err.message : 'Failed to save pasted document.')
    }
  }

  const toggleTagDeliverable = (d: MediaDeliverable) => {
    if (taskIntent === 'video') {
      const validation = validateVideoAttachment(
        { name: d.title, type: d.type === 'video' ? 'video/mp4' : 'image/png', kind: d.type === 'video' ? 'video' : 'image' },
        selectedVideoOption?.generationOptions
      )
      if (!validation.valid) {
        setVideoAttachmentError(validation.error || 'Invalid video reference image')
        return
      }
      setVideoAttachmentError(null)
      setTaggedMedia((prev) => {
        const exists = prev.some((t) => t.id === d.id)
        if (exists) return []
        return [
          {
            id: d.id,
            title: d.title,
            url: d.previewUrl || d.mediaUrl,
            kind: 'image',
            mediaType: 'image/png',
            filename: d.title,
            createdAt: Date.now(),
          },
        ]
      })
      return
    }
    setTaggedMedia((prev) => {
      const exists = prev.some((t) => t.id === d.id)
      if (exists) {
        return prev.filter((t) => t.id !== d.id)
      }
      return [
        ...prev,
        {
          id: d.id,
          title: d.title,
          url: d.previewUrl || d.mediaUrl,
          kind: (d.type === 'video' ? 'video' : 'image') as any,
          mediaType: d.type === 'video' ? 'video/mp4' : 'image/png',
          filename: d.title,
          createdAt: Date.now(),
        },
      ]
    })
  }

  const toggleTagMediaRef = (m: ProjectTaskMediaRef) => {
    if (taskIntent === 'video') {
      const validation = validateVideoAttachment(
        { name: m.filename || m.title, type: m.mediaType, kind: m.kind },
        selectedVideoOption?.generationOptions
      )
      if (!validation.valid) {
        setVideoAttachmentError(validation.error || 'Invalid video reference image')
        return
      }
      setVideoAttachmentError(null)
      setTaggedMedia((prev) => {
        const exists = prev.some((t) => t.id === m.id)
        if (exists) return []
        return [m]
      })
      return
    }
    setTaggedMedia((prev) => {
      const exists = prev.some((t) => t.id === m.id)
      if (exists) {
        return prev.filter((t) => t.id !== m.id)
      }
      return [...prev, m]
    })
  }

  const projectDesigns = useProjectDesigns(selectedProject?.id ?? '')
  useEffect(() => { setActiveMediaViewerItem(null) }, [selectedProject?.id])
  const allMediaLibraryItems = useMemo<MediaLibraryItem[]>(() => {
    const items: MediaLibraryItem[] = [...projectDesigns.items]
    const seenIds = new Set<string>()

    for (const task of tasks) {
      for (const d of task.deliverables || []) {
        if ((d.mediaUrl || d.previewUrl || d.status === 'ready' || d.status === 'accepted') && !seenIds.has(d.id)) {
          seenIds.add(d.id)
          items.push(deliverableToMediaItem(d, task, selectedProject))
        }
      }
    }

    for (const m of uploadedMedia) {
      if (!seenIds.has(m.id) && m.kind !== 'doc') {
        seenIds.add(m.id)
        items.push(uploadedToMediaItem(m, selectedProject))
      }
    }

    return items
  }, [tasks, uploadedMedia, selectedProject, projectDesigns.items])

  const handleOpenDeliverableInMediaCenter = (d: MediaDeliverable, parentTask?: RunningTask, mode?: QuickRouteMode) => {
    const owner = tasks.find(task => task.deliverables?.some(output => output.id === d.id)) ?? parentTask
    const item = deliverableToMediaItem(d, owner, selectedProject)
    setActiveMediaViewerItem(item)
    setMediaViewerInitialMode(mode ?? null)
  }

  const handleOpenUploadedInMediaCenter = (m: ProjectTaskMediaRef, mode?: QuickRouteMode) => {
    const item = uploadedToMediaItem(m, selectedProject)
    setActiveMediaViewerItem(item)
    setMediaViewerInitialMode(mode ?? null)
  }

  // Quick Route Media: Fine-Tune, Iterations, Video Story, or Continuation
  const handleQuickRouteMedia = async (options: {
    item: MediaLibraryItem | ProjectTaskMediaRef | MediaDeliverable
    action: 'fine_tune' | 'iterate' | 'to_video' | 'next_scene'
    deltaPrompt?: string
    variantCount?: number
    scenesCount?: number
    soundtrack?: string
    autoDeploy?: boolean
    requestId?: string
    model?: string
    settings?: MediaGenerationSettings
  }) => {
    if (!selectedProject?.id) throw new Error('Select a project before generating media.')
    const { item, action, deltaPrompt, variantCount, scenesCount, soundtrack, autoDeploy = true, model, settings } = options
    if ('source' in item && item.source === 'independent-design') throw new Error('Use the exact design revision edit controls.')

    const rawKind = (item as any).kind || (item as any).type || 'image'
    const isVideo = rawKind === 'video' || (item as any).mediaType?.startsWith('video/')
    const itemTitle = item.title || (item as any).filename || 'Media Item'
    let directUrl = (item as any).directUrl || (item as any).url || (item as any).previewUrl || (item as any).mediaUrl || ''
    const art = (item as any).artifact
    const isLegacyExactRef = Boolean(art?.collectionId && art?.eventSeq && !art?.revision_ref && !art?.revisionRef)
    if (isLegacyExactRef && directUrl && !directUrl.includes('revision=') && !directUrl.includes('event_seq=') && !directUrl.startsWith('data:')) {
      const sep = directUrl.includes('?') ? '&' : '?'
      directUrl = `${directUrl}${sep}revision=${art.eventSeq}`
    }
    const mediaType = (item as any).mediaType || (isVideo ? 'video/mp4' : 'image/png')

    const mediaRef: ProjectTaskMediaRef = {
      id: item.id,
      title: itemTitle,
      filename: (item as any).filename || itemTitle,
      kind: isVideo ? 'video' : 'image',
      mediaType,
      url: directUrl,
      createdAt: Date.now(),
    }

    // Determine target intent
    let targetIntent: 'image' | 'video' | 'code' | 'audit' = 'image'
    if (action === 'to_video' || action === 'next_scene' || isVideo) {
      targetIntent = 'video'
    } else {
      targetIntent = 'image'
    }

    // Compose high-context prompt tailored to the intent & action
    let composedPrompt = ''
    if (action === 'fine_tune') {
      if (isVideo) {
        composedPrompt = deltaPrompt
          ? `Change this video: ${deltaPrompt}`
          : `Fine-tune and modify this video with stylistic adjustments`
      } else {
        composedPrompt = deltaPrompt
          ? `Change this image: ${deltaPrompt}`
          : `Fine-tune and modify this image with refined lighting and details`
      }
    } else if (action === 'iterate') {
      if (isVideo) {
        composedPrompt = deltaPrompt
          ? `Create alternative video takes: ${deltaPrompt}`
          : `Generate alternative video variations and scenes based on this video`
      } else {
        const count = variantCount || 5
        composedPrompt = deltaPrompt
          ? `Create ${count} variations of this image: ${deltaPrompt}`
          : `Generate ${count} creative iterations of this image in diverse styles`
      }
    } else if (action === 'to_video') {
      composedPrompt = deltaPrompt
        ? `Create a cinematic video story based on this image keyframe: ${deltaPrompt}`
        : `Transform this image into a multi-scene cinematic video story with synchronized soundtrack`
    } else if (action === 'next_scene') {
      composedPrompt = deltaPrompt
        ? `Continue this video with next scene: ${deltaPrompt}`
        : `Continue this video story with the next sequence and matching cinematic soundtrack`
    }

    if (autoDeploy) {
      // 1-Click Fast Autonomous Execution: route and deploy immediately!
      setIsDeployingTask(true)

      const tempJobId = options.requestId || crypto.randomUUID()
      const jobCount = targetIntent === 'image' ? (action === 'iterate' ? (variantCount || 5) : 1) : (variantCount || 1)
      const optimisticJob: MediaGenerationJob = {
        id: tempJobId,
        sourceId: item.id,
        prompt: deltaPrompt || composedPrompt,
        createdAt: Date.now(),
        title: `${action.replace('_', ' ')} (${jobCount}x)`,
        count: jobCount,
        status: 'submitting',
      }
      setLocalGenerationJobs((prev) => [optimisticJob, ...prev])

      try {
        const finalVariantCount = jobCount
        const finalScenesCount = targetIntent === 'video' ? (scenesCount || 1) : undefined
        const finalSoundtrack = targetIntent === 'video' ? (soundtrack || undefined) : undefined

        const res = await requestJson<{ task: any }>(`/v3/projects/${selectedProject.id}/tasks`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            prompt: composedPrompt,
            intent: targetIntent,
            operation: targetIntent === 'video' ? (isVideo ? (action === 'next_scene' ? 'extend' : 'edit') : 'create') : undefined,
            aspect_ratio: settings ? settings.aspectRatio : (targetIntent === 'image' ? imageAspectRatio : undefined),
            resolution: settings?.resolution || undefined,
            duration_seconds: settings?.durationSeconds || undefined,
            variant_count: finalVariantCount,
            scenes_count: finalScenesCount,
            soundtrack: finalSoundtrack,
            model: model || (targetIntent === 'image' ? (selectedImageModel || undefined) : targetIntent === 'video' ? (selectedVideoModel || undefined) : undefined),
            auto_approve: true,
            deploy_session: true,
            attached_media: [mediaRef],
          }),
        })
        if (!res?.task?.id) throw new Error('Generation response did not include a task.')
        if (res.task) {
          setLocalGenerationJobs((prev) =>
            prev.map((j) =>
              j.id === tempJobId
                ? {
                    ...j,
                    taskId: res.task.id,
                    status: res.task.status === 'in_progress' ? 'in_progress' : res.task.status || 'queued',
                    title: res.task.title || j.title,
                  }
                : j
            )
          )
          desktopProjects.invalidate(selectedProject.id)
        }
      } catch (err: any) {
        const errMsg = err instanceof Error ? err.message : 'Generation request failed'
        setLocalGenerationJobs((prev) =>
          prev.map((j) => (j.id === tempJobId ? { ...j, status: 'failed', error: errMsg } : j))
        )
        throw err
      } finally {
        setIsDeployingTask(false)
      }
    } else {
      // Open in planner for review
      setActiveMediaViewerItem(null)
      setShowFullMediaCenter(false)
      setTaskIntent(targetIntent)
      setTaggedMedia([mediaRef])
      setNewTaskPrompt(composedPrompt)
      if (targetIntent === 'image' && variantCount) {
        setImageVariants(variantCount)
      }
      setIsDeployModalOpen(true)
    }
  }

  // Typed Media Generation handler for MediaViewerModal and HistoricalMediaLibrary
  const handleMediaGenerate = useCallback(
    async (request: MediaGenerationRequest): Promise<void> => {
      await handleQuickRouteMedia({
        item: request.item,
        requestId: request.requestId,
        action: request.action,
        deltaPrompt: request.deltaPrompt,
        variantCount: request.variantCount,
        model: request.model,
        settings: request.settings,
        autoDeploy: true,
      })
    },
    [handleQuickRouteMedia]
  )

  // Derive generation jobs combining backend durable tasks with attached media and optimistic/in-flight local jobs
  const allGenerationJobs = useMemo<MediaGenerationJob[]>(() => {
    const jobs: MediaGenerationJob[] = []
    const seenTaskIds = new Set<string>()

    for (const t of tasks) {
      if (!isCreativeMediaTask(t)) continue
      seenTaskIds.add(t.id)

      // One durable task is one turn, regardless of candidate or attachment count.
      const parentIds = [...new Set((t.deliverables ?? []).flatMap(output => [output.parentDeliverableId, output.sourceMediaRef].filter((id): id is string => Boolean(id))))]
      const sourceId = parentIds.length === 1 ? parentIds[0]! : t.attachedMedia?.length === 1 ? t.attachedMedia[0].id : t.deliverables?.[0]?.id ?? ''
      for (const am of [{ id: sourceId }]) {
        const localJob = localGenerationJobs.find((job) => job.taskId === t.id)
        let status: string = t.status
        if (t.status === 'failed') {
          status = 'failed'
        } else if (t.status === 'in_progress' || t.status === 'running') {
          // Successful prior outputs do not complete a newly running request.
          status = 'in_progress'
        } else if (t.status === 'completed' || t.status === 'needs_review') {
          status = t.lastError ? 'partial_failure' : 'completed'
        } else if (t.status === 'queued' || t.status === 'pending') {
          status = 'queued'
        }

        jobs.push({
          ...mediaJobIdentity(t.id, am.id, localGenerationJobs),
          taskId: t.id,
          prompt: localJob?.prompt || t.description || t.title,
          createdAt: localJob?.createdAt || t.createdAt,
          outputIds: t.deliverables?.filter((d) => d.status === 'ready' || d.status === 'accepted').map((d) => d.id) || [],
          title: t.title,
          count: t.variantCount || t.deliverables?.length || 1,
          status,
          error: t.lastError,
          sessionId: t.sessionId,
        })
      }
    }

    for (const lj of localGenerationJobs) {
      if (!lj.taskId || !seenTaskIds.has(lj.taskId)) {
        jobs.push(lj)
      }
    }

    return jobs
  }, [tasks, localGenerationJobs])

  // Derive real active workers from V3 sessions with equality check to prevent re-render churn
  const deployedWorkers = useDesktopV3CacheSelector(
    (state) => {
      const list: DeployedWorker[] = []
      const allRecords = Object.values(state.sessionsById)
      for (const rec of allRecords) {
        if (rec.kind !== 'full' || !rec.session) continue
        const sess = rec.session
        const isActive = !!(sess.lifecycle as any)?.active
        const agent = (sess as any).agent_name || 'swarm'
        if (isActive || agent !== 'swarm' || (sess.message_count ?? 0) > 1) {
          list.push({
            id: sess.id,
            name: sess.title || `@${agent}`,
            role: `${agent.toUpperCase()} Agent • ${sess.workspace_name || 'Workspace'}`,
            triggerKind: 'trigger',
            scheduleLabel: isActive ? 'Executing Live Run' : 'Idle / Ready',
            activeJobsCount: isActive ? 1 : 0,
            completedJobsCount: (sess.message_count ?? 0) > 2 ? 1 : 0,
            status: isActive ? 'active' : 'idle',
            currentJobTitle: sess.title,
            assignedTaskIds: [],
          })
        }
      }
      return list.slice(0, 12)
    },
    (prev, next) => {
      if (prev.length !== next.length) return false
      return prev.every((p, i) => {
        const n = next[i]
        return (
          p.id === n.id &&
          p.status === n.status &&
          p.name === n.name &&
          p.scheduleLabel === n.scheduleLabel &&
          p.activeJobsCount === n.activeJobsCount &&
          p.completedJobsCount === n.completedJobsCount
        )
      })
    }
  )

  // Model Change Handlers with UI Settings Persistence & Task Override
  const handleSetImageAsDefault = async (newModel: string) => {
    setImageDefaultError(null)
    setIsSavingModelChoice(true)
    try {
      await requestJson('/v1/ui/settings', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tools: { image: { default_model: newModel } } }),
      })
      setDefaultImageModel(newModel)
    } catch (err) {
      setImageDefaultError(err instanceof Error ? err.message : 'Unable to save image default. Try Set default again.')
    } finally {
      setIsSavingModelChoice(false)
    }
  }

  const handleImageModelChange = (newModel: string) => {
    setSelectedImageModel(newModel)
  }

  const handleSetVideoAsDefault = videoDefaults.save

  const handleVideoModelChange = (newModel: string) => {
    videoDefaults.select(newModel)
  }

  const handleAudioModelChange = async (newModel: string) => {
    setSelectedAudioModel(newModel)
    setIsSavingModelChoice(true)
    try {
      await requestJson('/v1/ui/settings', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tools: { audio: { default_model: newModel } } }),
      })
    } catch (err) {
      console.warn('Failed to save default audio model:', err)
    } finally {
      setIsSavingModelChoice(false)
    }
  }

  // Submit Task Proposal with Intent, Visual Controls & Auto-Approve Policy
  const handleDeployModalSubmit = async () => {
    if (isDeployingTaskRef.current) return
    const prompt = taskIntent === 'image' ? newTaskPrompt : newTaskPrompt.trim()
    if (!prompt.trim() || !selectedProject?.id) return
    const scenePrompts = taskIntent === 'video' ? videoScenePrompts.split('\n').map(value => value.trim()).filter(Boolean) : []

    if (taskIntent === 'video') {
      if (scenePrompts.length > 0 && (scenePrompts.length < 2 || scenePrompts.length > 8 || videoClipCount !== 1)) {
        setVideoAttachmentError('Multipart requires 2–8 scene prompts and one assembled clip.')
        return
      }
      if (videoDefaults.loading || videoDefaults.loadFailed || videoCatalog.isFetching || videoCatalog.isError) {
        setVideoAttachmentError('Load video defaults before submitting')
        return
      }
      if (!selectedVideoOption?.ready) {
        setVideoAttachmentError('Select an available video model before submitting')
        return
      }
      if ((supportedVideoAspectRatios.length > 0 && !supportedVideoAspectRatios.includes(videoAspectRatio)) ||
          (supportedVideoResolutions.length > 0 && !supportedVideoResolutions.includes(videoResolution)) ||
          (supportedVideoDurations.length > 0 && !supportedVideoDurations.includes(videoDuration))) {
        setVideoAttachmentError('Selected video options are no longer supported. Review the catalog controls.')
        return
      }
      if (taggedMedia.length > 1) {
        setVideoAttachmentError('Selected video flow supports at most 1 initial image reference')
        return
      }
      if (taggedMedia.length === 1) {
        const m = taggedMedia[0]
        const validation = validateVideoAttachment(
          { name: m.filename || m.title, type: m.mediaType, kind: m.kind },
          selectedVideoOption?.generationOptions
        )
        if (!validation.valid) {
          setVideoAttachmentError(validation.error || 'Invalid video reference image')
          return
        }
      }
      setVideoAttachmentError(null)
    }

    isDeployingTaskRef.current = true
    setIsDeployingTask(true)
    setDeployError(null)
    try {
      const qualifiedModel = taskIntent === 'video'
        ? (resolveQualifiedVideoModel(selectedVideoOption, selectedVideoModel) || undefined)
        : taskIntent === 'image'
        ? (selectedImageModel || undefined)
        : taskIntent === 'sound'
        ? (selectedAudioModel || undefined)
        : (newTaskModelOverride.trim() || undefined)

      const targetAgent = taskIntent === 'code'
        ? (featureSize === 'big' ? 'swarm' : 'coder')
        : taskIntent === 'audit'
          ? 'finder'
          : taskIntent === 'image'
            ? 'image'
            : taskIntent === 'video'
              ? 'video'
              : 'sound'

      const attachedMediaForTask = taggedMedia

      // Omitted Auto-detect is resolved by the backend only if there is exactly one
      // authorized project repository. Explicit choices must match the project catalog.
      const workspaceSelection = taskIntent === 'code' || taskIntent === 'audit'
        ? taskWorkspaceSelection(newTaskWorkspace, selectedProject)
        : {}

      const deployPayload = {
        prompt,
        ...workspaceSelection,
        intent: taskIntent,
        feature_size: taskIntent === 'code' ? featureSize : undefined,
        agent: targetAgent,
        video_type: taskIntent === 'video' ? (scenePrompts.length ? 'multipart' : 'single') : undefined,
        operation: taskIntent === 'video' ? 'create' : undefined,
        scenes: scenePrompts.length ? scenePrompts.map((scenePrompt, index) => ({ scene_number: index + 1, title: `Scene ${index + 1}`, prompt: scenePrompt })) : undefined,
        scenes_count: scenePrompts.length || undefined,
        enhance_prompt: taskIntent === 'image'
          ? imagePromptEnhancement(imageVariants, imagePromptState.aiVariants)
          : taskIntent === 'video' ? enhanceVideoPrompt : undefined,
        aspect_ratio: taskIntent === 'image' ? imageAspectRatio : taskIntent === 'video' ? (videoAspectRatio && supportedVideoAspectRatios.includes(videoAspectRatio) ? videoAspectRatio : undefined) : undefined,
        resolution: taskIntent === 'image' ? imageResolution : taskIntent === 'video' ? (videoResolution && supportedVideoResolutions.includes(videoResolution) ? videoResolution : undefined) : undefined,
        variant_count: taskIntent === 'image' ? imageVariants : taskIntent === 'video' ? videoClipCount : undefined,
        duration_seconds: taskIntent === 'sound' ? soundDuration : taskIntent === 'video' ? (videoDuration > 0 && supportedVideoDurations.includes(videoDuration) ? videoDuration : undefined) : undefined,
        model: qualifiedModel,
        auto_approve: autoApproveTask,
        deploy_session: true,
        attached_media: attachedMediaForTask,
      }
      pendingDeployRequestRef.current = taskDeployRequestIdentity(
        pendingDeployRequestRef.current,
        selectedProject.id,
        deployPayload,
        () => `task_${Date.now()}_${Math.random().toString(36).slice(2, 7)}`
      )
      const { clientTaskId } = pendingDeployRequestRef.current

      const res = await requestJson<{ task: any }>(`/v3/projects/${selectedProject.id}/tasks`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Request-ID': clientTaskId,
        },
        body: JSON.stringify({
          id: clientTaskId,
          ...deployPayload,
        }),
      })
      if (!res?.task) {
        throw new Error('Task creation succeeded without returned task authority')
      }
      // Reset stable request identity only upon successful response
      pendingDeployRequestRef.current = null
      // Use the returned durable slots immediately; hydration is a repair, not
      // a prerequisite for displaying the requested output count.
      desktopProjects.setOptimisticTasks(selectedProject.id, previous => [
        ...previous.filter(task => task.id !== res.task.id), mapBackendTask(res.task),
      ])
      desktopProjects.invalidate(selectedProject.id)
      setIsDeployModalOpen(false)
      setNewTaskPrompt('')
      dispatchImagePrompt({ type: 'reset' })
      setNewTaskModelOverride('')
      setTaggedMedia([])
      setDeployError(null)
    } catch (err: any) {
      console.warn('Deploy task failed:', err)
      setDeployError(err?.message || 'Failed to deploy task. Please verify workspace and parameters.')
    } finally {
      isDeployingTaskRef.current = false
      setIsDeployingTask(false)
    }
  }

  // Track in-flight task approvals to prevent duplicate clicks and premature execution
  const [approvingTaskIds, setApprovingTaskIds] = useState<Set<string>>(new Set())
  const [taskActionErrors, setTaskActionErrors] = useState<Record<string, string>>({})
  useSyncExternalStore(taskIntegrationOperations.subscribe, taskIntegrationOperations.getSnapshot, taskIntegrationOperations.getSnapshot)
  const integrationForTask = (task: RunningTask) => taskIntegrationOperations.get(taskIntegrationKey(selectedProject?.id || '', task))
  const recoveryProjectRef = useRef(selectedProject?.id)
  recoveryProjectRef.current = selectedProject?.id
  const batchSourceRef = useRef<{ projectId?: string; tasks: RunningTask[] }>({ tasks: [] })
  const batchNavigation = useRef({ projectId: selectedProject?.id, generation: 0 })
  if (batchNavigation.current.projectId !== selectedProject?.id) batchNavigation.current = { projectId: selectedProject?.id, generation: batchNavigation.current.generation + 1 }
  batchSourceRef.current = { projectId: selectedProject?.id, tasks }
  useEffect(() => () => { batchNavigation.current.generation++ }, [])
  const integrationBatches = useSyncExternalStore(taskIntegrationBatches.subscribe, taskIntegrationBatches.getSnapshot, taskIntegrationBatches.getSnapshot)
  const integrationBatch = integrationBatches.get(selectedProject?.id || '')
  useSyncExternalStore(taskReopenOperations.subscribe, taskReopenOperations.getSnapshot, taskReopenOperations.getSnapshot)

  const handleClearTaskError = useCallback((taskId: string) => {
    setTaskActionErrors((prev) => {
      if (!prev[taskId]) return prev
      const next = { ...prev }
      delete next[taskId]
      return next
    })
  }, [])

  // Approve pending task and start execution run
  const handleApproveTask = async (taskId: string) => {
    if (!selectedProject?.id) return
    if (approvingTaskIdsRef.current.has(taskId) || approvingTaskIds.has(taskId)) return

    const targetTask = tasks.find((t) => t.id === taskId) || liveTasks.find((t) => t.id === taskId)
    if (!targetTask) return

    // Pre-flight validation against illegal or premature approval
    if (
      targetTask.status === 'planning' &&
      (!targetTask.planBinding?.planId &&
        !targetTask.planBinding?.plan_id &&
        !(targetTask as any).plan_binding?.planId &&
        !(targetTask as any).plan_binding?.plan_id)
    ) {
      setTaskActionErrors((prev) => ({
        ...prev,
        [taskId]: 'Cannot approve task while Plan agent is still investigating. Please wait for the structured plan to be submitted.',
      }))
      return
    }

    if (targetTask.status === 'rejected') {
      setTaskActionErrors((prev) => ({
        ...prev,
        [taskId]: 'Cannot approve a rejected task.',
      }))
      return
    }

    const planDoc = targetTask.planDocument || (targetTask as any).plan_document
    if (planDoc?.status === 'rejected' || planDoc?.approval_state === 'rejected' || planDoc?.approvalState === 'rejected') {
      setTaskActionErrors((prev) => ({
        ...prev,
        [taskId]: 'Cannot approve a rejected plan definition. Please refine or re-plan.',
      }))
      return
    }

    const acceptanceBody = buildTaskAcceptancePayload(targetTask)
    const isPlanTask = Boolean(
      targetTask.agentType === 'plan' ||
      targetTask.outcomeType === 'plan_spec' ||
      targetTask.planBinding?.planId ||
      targetTask.planBinding?.plan_id ||
      (targetTask as any).plan_binding?.planId ||
      (targetTask as any).plan_binding?.plan_id ||
      targetTask.planDocument ||
      (targetTask as any).plan_document
    )
    const hasBinding = Boolean(
      targetTask.planBinding?.planId ||
      targetTask.planBinding?.plan_id ||
      (targetTask as any).plan_binding?.planId ||
      (targetTask as any).plan_binding?.plan_id
    )
    if ((isPlanTask || hasBinding) && (!acceptanceBody.session_id || (targetTask.sessionId && acceptanceBody.session_id !== targetTask.sessionId) || !acceptanceBody.plan_id || acceptanceBody.definition_revision == null || acceptanceBody.definition_revision <= 0)) {
      setTaskActionErrors((prev) => ({
        ...prev,
        [taskId]: 'Plan definition revision guard is missing or stale. Cannot execute without verified plan revision.',
      }))
      return
    }

    // Duplicate-click prevention & in-flight guard: synchronous ref lock + React state
    approvingTaskIdsRef.current.add(taskId)
    setApprovingTaskIds((prev) => new Set(prev).add(taskId))
    handleClearTaskError(taskId)

    // Acceptance request identity: exact binding-derived or retained across retry
    let approveRequestId: string
    if (acceptanceBody.plan_id && typeof acceptanceBody.definition_revision === 'number') {
      // Deterministic exact binding-derived request identity
      approveRequestId = `approve:${taskId}:${acceptanceBody.session_id}:${acceptanceBody.plan_id}:r${acceptanceBody.definition_revision}`
    } else {
      // Retained across retry
      const retainedId = pendingApproveRequestIdsRef.current.get(taskId)
      if (retainedId) {
        approveRequestId = retainedId
      } else {
        approveRequestId = `approve:${taskId}:${targetTask.revision || 1}:${Math.random().toString(36).slice(2, 9)}`
        pendingApproveRequestIdsRef.current.set(taskId, approveRequestId)
      }
    }

    try {
      const res = await requestJson<{ status: string; task: any; message?: string }>(
        `/v3/projects/${selectedProject.id}/tasks/${taskId}/approve`,
        {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'X-Request-ID': approveRequestId,
          },
          body: JSON.stringify(acceptanceBody),
        }
      )

      if (!res?.task) {
        throw new Error(res?.message || 'Approve succeeded without returned task authority')
      }
      pendingApproveRequestIdsRef.current.delete(taskId)

      const mapped = mapBackendTask(res.task)
      desktopProjects.setOptimisticTasks(selectedProject.id, (prev) =>
        prev.map((t) => (t.id === taskId ? mapped : t))
      )
      desktopProjects.invalidate(selectedProject.id)

      // Hydrate task details without selecting/attaching the execution conversation.
      const linkedSessionId = res.task.session_id || res.task.sessionId || res.task.plan_binding?.session_id || res.task.planBinding?.session_id
      if (linkedSessionId) {
        setSelectedTaskId(taskId)
        void hydrateDesktopV3ChildCard(linkedSessionId, { activePlan: true, permissionSummary: true }).catch(() => undefined)
      }
    } catch (err: any) {
      const errMsg = err?.message || String(err)
      console.warn('Approve task failed:', err)
      setTaskActionErrors((prev) => ({ ...prev, [taskId]: errMsg }))
      desktopProjects.invalidate(selectedProject.id)
    } finally {
      approvingTaskIdsRef.current.delete(taskId)
      setApprovingTaskIds((prev) => {
        const next = new Set(prev)
        next.delete(taskId)
        return next
      })
    }
  }

  const managementNavigation = useRef({ activeTaskId, selectedTaskId })
  managementNavigation.current = { activeTaskId, selectedTaskId }
  // Every management mutation is guarded by the stored revision. Never remove a task optimistically.
  const manageTasks = async (rows: RunningTask[], action: 'archive' | 'delete') => {
    if (taskIntegrationBatches.getSnapshot().get(selectedProject?.id || '')?.pending) return
    const projectId = selectedProject?.id
    if (!projectId || rows.length === 0) return
    const epoch = managementEpoch.current
    const current = () => epoch === managementEpoch.current && selectedProjectRef.current === projectId
    rows = rows.filter(row => !managementPending.current.has(row.id))
    if (!rows.length) return
    if (action === 'delete' && !window.confirm(`Permanently delete ${rows.length} selected task${rows.length === 1 ? '' : 's'}? This cannot be undone. Only archived, unlaunched tasks can be deleted; launched tasks and their work are retained.`)) return
    rows.forEach(row => managementPending.current.add(row.id))
    setTaskActionErrors(prev => ({ ...prev, ...Object.fromEntries(rows.map(row => [row.id, action === 'archive' ? 'Archiving…' : 'Deleting…'])) }))
    setManagementBusy(true)
    setManagementMessage('')
    const failures: string[] = []
    const succeeded: string[] = []
    await Promise.all(rows.map(async row => {
      try {
        await projectTaskArchiveQueue.run(JSON.stringify(['task', managementOwner, epoch, projectId, row.id]), async () => {
        if (!current()) throw new Error('Project or account changed')
        let revision = row.revision
        if (!Number.isSafeInteger(revision) || (revision ?? 0) <= 0) throw new Error('Missing task revision; refresh and retry')
        if (action === 'delete') {
          if (row.sessionId || row.taskProgramId || row.planBinding) {
            throw new Error('Launched task has retained execution; archive instead of deleting')
          }
          const receipt = await archiveProjectTask(projectId, row, current)
          if (!receipt) return
          desktopProjects.archiveReceipt(projectId, receipt)
          revision = receipt.revision
          await requestJson(`/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(row.id)}?revision=${revision}`, { method: 'DELETE' })
        } else {
          const receipt = await archiveProjectTask(projectId, row, current)
          if (!receipt) return
          desktopProjects.archiveReceipt(projectId, receipt)
        }
        })
        if (!current()) return
        succeeded.push(row.id)
        setTaskActionErrors(prev => ({ ...prev, [row.id]: '' }))
        setMarkedTaskIds(prev => new Set([...prev].filter(id => id !== row.id)))
        if (managementNavigation.current.activeTaskId === row.id) handleBackToOrchestrator()
        if (managementNavigation.current.selectedTaskId === row.id) setSelectedTaskId('')
      } catch (err) {
        if (!current()) return
        const message = err instanceof Error ? err.message : String(err)
        failures.push(`${row.title}: ${message}`)
        setTaskActionErrors(prev => ({ ...prev, [row.id]: message }))
      } finally {
        if (current()) { managementPending.current.delete(row.id); setManagementBusy(managementPending.current.size > 0) }
      }
    }))
    if (!current()) return
    setManagementMessage(`${succeeded.length} ${action === 'archive' ? 'archived' : 'deleted'}, ${failures.length} failed.${failures.length ? ` Retry after refresh: ${failures.join('; ')}` : ''}`)
    setMarkedTaskIds(prev => new Set([...prev].filter(id => !succeeded.includes(id))))
    if (archivedOpen) void loadArchivedTasks(projectId, true)

  }

  const handleDeleteTask = async (taskId: string) => {
    const task = tasks.find(row => row.id === taskId)
    if (task) await manageTasks([task], 'delete')
  }

  const renderPreviousRuns = (task: RunningTask) => selectedProject ? <TaskAttemptHistory
    key={`${selectedProject.id}:${task.id}:${task.activeAttemptId || 'initial'}`}
    projectId={selectedProject.id} taskId={task.id} onOpen={sessionId => {
      setSelectedTaskId(task.id); setActiveTaskId(task.id); setActiveSessionId(sessionId); setWorkerChatOpen(true)
      void hydrateDesktopV3ChildCard(sessionId, { activePlan: true, permissionSummary: true }).catch(() => undefined)
    }} /> : null

  const renderIntegrationRecovery = (task: RunningTask) => {
    const operation = integrationForTask(task)
    const outcome = taskOutcome(task)
    const reopen = taskReopenOperations.get(taskReopenKey(selectedProject?.id || '', task.id))
    if (outcome.repairSessionId && !outcome.integrationFailed && selectedProject) return <div className="integration-recovery">
      <p>Repair attempt retained · {outcome.delivery}. {outcome.verification}.</p>
      <button type="button" onClick={event => { event.stopPropagation(); setActiveTaskId(task.id); setSelectedTaskId(task.id); setActiveSessionId(outcome.repairSessionId!); setWorkerChatOpen(true) }}>Open repair session</button>
    </div>
    const retryAttempt = task.attempts?.find(attempt => attempt.id === task.activeAttemptId && attempt.launch_state === 'launch_failed')
    const failure = (retryAttempt?.recovery && selectedProject
      ? integrationFailure(selectedProject, task, new Error(retryAttempt.last_error || 'Repair launch incomplete; retry retained request')) : undefined) || (operation.phase === 'error' ? { ...operation.failure, task } : undefined) || (selectedProject && outcome.integrationFailed && task.integration
      ? integrationFailure(selectedProject, task, new Error(task.integration.error || 'Integration failed; retained backend receipt')) : undefined)
    if (!selectedProject) return null
    if (operation.phase === 'success' && operation.refreshError) return <><p role="status">{operation.refreshError}</p></>
    const failureKey = taskIntegrationKey(selectedProject.id, task)
    const failureIdentity = taskIntegrationFailureIdentity(task, operation)
    if (!failure || failure.projectId !== selectedProject.id) return <>{retryAttempt?.request && <button type="button" onClick={event => { event.stopPropagation(); void handleReopenTask(task.id, retryAttempt.request) }}>Retry incomplete follow-up</button>}</>
    const unavailable = repairUnavailable(failure.task)
    return <div className="integration-recovery" role="alert" onClick={event => event.stopPropagation()}>
      <strong>Integration failed</strong>
      <p>{outcome.delivery || 'Delivery not verified'}. {outcome.verification}.</p>
      {!taskIntegrationOperations.isDismissed(failureKey, failureIdentity) && <pre>{failure.error}</pre>}
      <div className="flex flex-wrap gap-2">
        <button type="button" disabled={Boolean(unavailable) || reopen.pending} onClick={() => void launchIntegrationRepair(failure)}>
          {reopen.pending ? 'Launching…' : 'Launch repair session'}
        </button>
        <button type="button" disabled={reopen.pending || taskIntegrationPhase(task, operation) === 'pending'} onClick={() => void handleIntegrateTask(task.id)}>Retry integration to refresh verified receipt</button>
        <button type="button" aria-label="Dismiss integration error" onClick={event => { event.stopPropagation(); taskIntegrationOperations.dismiss(failureKey, failureIdentity) }}>Dismiss</button>
      </div>
      {unavailable && <p>{unavailable}</p>}
      {reopen.error && <p>{redactIntegrationDiagnostic(reopen.error)}</p>}
    </div>
  }

  const launchIntegrationRepair = async (failure: IntegrationFailure) => {
    const task = failure.task
    if (repairUnavailable(task)) return
    const active = task.attempts?.find(attempt => attempt.id === task.activeAttemptId && attempt.launch_state && attempt.launch_state !== 'launched')
    const feedback = active?.request || 'Repair the failed integration for this task. Preserve the captured target, inspect the retained integration receipt, and coordinate the repair without automatic promotion.'
    const outcome = await taskReopenOperations.run(failure.projectId, task.id, task, feedback,
      body => requestJson<{ status: string; task: any }>(`/v3/projects/${encodeURIComponent(failure.projectId)}/tasks/${encodeURIComponent(task.id)}/reopen`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
      }),
      returnedTask => {
        const mapped = mapBackendTask(returnedTask)
        desktopProjects.setOptimisticTasks(failure.projectId, previous => previous.map(row => row.id === task.id ? mapped : row))
        desktopProjects.invalidate(failure.projectId)
        if (recoveryProjectRef.current !== failure.projectId) return
        const sessionId = returnedTask.session_id || returnedTask.sessionId
        setActiveTaskId(task.id); setSelectedTaskId(task.id); setActiveSessionId(sessionId); setWorkerChatOpen(true)
        selectedWorkerRef.current = null
        setSelectedWorker(null)
      }, true)
    if (!outcome.ok) desktopProjects.invalidate(failure.projectId)
  }

  const handleIntegrateSelected = async (rows: RunningTask[]) => {
    const project = selectedProject
    if (!project || managementBusy) return
    const generation = batchNavigation.current.generation
    await taskIntegrationBatches.run(project, rows,
      id => batchNavigation.current.generation === generation && batchSourceRef.current.projectId === project.id ? batchSourceRef.current.tasks.find(task => task.id === id) : undefined,
      (task, token) => {
        const request = taskIntegrationRequest(project.id, task)
        return taskIntegrationOperations.run(project, task, () =>
          requestJson<TaskIntegrationResult>(request.url, {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(request.body),
          }), () => {}, token)
      },
      () => desktopProjects.invalidate(project.id))
  }

  // Integrate / Promote task commits into target branch
  const handleIntegrateTask = async (taskId: string) => {
    const project = selectedProject
    const task = tasks.find(row => row.id === taskId)
    if (!project || !task) return
    const projectId = project.id
    const request = taskIntegrationRequest(projectId, task)
    await taskIntegrationOperations.run(project, task, () =>
      requestJson<TaskIntegrationResult>(request.url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(request.body),
      }),
      () => desktopProjects.invalidate(projectId),
    )
  }

  // Refine task with router or re-plan error
  const handleRefineTask = async (taskId: string, feedback?: string, errorSummary?: string) => {
    if (!selectedProject?.id) return
    const targetTask = tasks.find(task => task.id === taskId)
    if (!targetTask) return
    const guards = buildTaskAcceptancePayload(targetTask)
    handleClearTaskError(taskId)
    try {
      if (targetTask.status === 'pending_approval') {
        if (!activeSessionId) throw new Error('Open an Orchestrator conversation before requesting changes.')
        await continueDesktopV3Conversation(createDesktopV3ExistingMessageOperation({
          sessionId: activeSessionId,
          prompt: !targetTask.planBinding
            ? `Author the first structured plan for pending task ${taskId} in project ${selectedProject.id} on the same card. Read get_task first; submit refine_task with plan_document and expected_revision ${targetTask.revision}. Include authored requirements with stable IDs, each bound to an exact checkpoint acceptance criterion. Do not create a replacement task or approve it. User request: ${feedback?.trim() || errorSummary?.trim() || ''}`
            : `Request changes to the requirements on task ${taskId} in project ${selectedProject.id}. Reviewed binding: ${JSON.stringify(guards)}. User request: ${feedback?.trim() || errorSummary?.trim() || ''}\nRead the current bound plan; apply only targeted requirement edits with edit_requirements. Preserve unrelated requirements and execution details. Do not delegate to Plan or regenerate the plan. Summarize the changed requirements on the same card for fresh approval.`,
        }))
        return
      }
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/refine`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ...guards,
          feedback: feedback?.trim() || undefined,
          error_summary: errorSummary?.trim() || undefined,
        }),
      })
      desktopProjects.invalidate(selectedProject.id)
    } catch (err) {
      setTaskActionErrors(prev => ({...prev, [taskId]: err instanceof Error ? err.message : 'Plan refinement failed'}))
    }
  }

  // Reopen task back to in_progress
  const handleReopenTask = async (taskId: string, feedback?: string): Promise<TaskReopenOutcome> => {
    const projectId = selectedProject?.id
    if (!projectId) return { ok: false, error: 'Select the task project before reopening.' }
    const targetTask = tasks.find(task => task.id === taskId)
    const outcome = await taskReopenOperations.run(projectId, taskId, targetTask, feedback,
      body => requestJson<{ status: string; task: any }>(
        `/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/reopen`,
        { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) },
      ),
      returnedTask => {
        const mapped = mapBackendTask(returnedTask)
        desktopProjects.setOptimisticTasks(projectId, prev => prev.map(task => task.id === taskId ? mapped : task))
        desktopProjects.invalidate(projectId)
        if (recoveryProjectRef.current !== projectId) return
        handleClearTaskError(taskId)
        setSelectedTaskId(taskId)
        const linkedSessionId = returnedTask.session_id || returnedTask.sessionId
        void hydrateDesktopV3ChildCard(linkedSessionId, { activePlan: true, permissionSummary: true }).catch(() => undefined)
      },
    )
    if (!outcome.ok) desktopProjects.invalidate(projectId)
    return outcome
  }

  // Complete task explicitly
  const handleCompleteTask = async (taskId: string) => {
    if (!selectedProject?.id) return
    handleClearTaskError(taskId)
    try {
      const task = tasks.find(item => item.id === taskId)
      const endpoint = task?.deliverables?.some(item => item.type === 'video' || item.type === 'image')
        ? `/v3/projects/${selectedProject.id}/tasks/${taskId}/accept`
        : `/v3/projects/${selectedProject.id}/tasks/${taskId}/complete`
      const res = await requestJson<{ status?: string; task?: any }>(
        endpoint,
        {
          method: 'POST',
        }
      )
      if (res?.task) {
        const mapped = mapBackendTask(res.task)
        desktopProjects.setOptimisticTasks(selectedProject.id, (prev) =>
          prev.map((t) => (t.id === taskId ? mapped : t))
        )
      }
      desktopProjects.invalidate(selectedProject.id)
    } catch (err: any) {
      console.warn('Complete task failed:', err)
      setTaskActionErrors((prev) => ({ ...prev, [taskId]: err?.message || String(err) }))
      desktopProjects.invalidate(selectedProject.id)
    }
  }

  // Redeploy task program job on conflict or failure
  const handleRedeployJob = async (taskId: string, jobId: string, feedback?: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/program:redeploy-job`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ job_id: jobId, feedback }),
      })
      desktopProjects.invalidate(selectedProject.id)
    } catch (err) {
      console.warn('Redeploy job failed:', err)
      desktopProjects.invalidate(selectedProject.id)
    }
  }

  // Real-time status transitions linked to V3 session lifecycles: status = 'needs_review' when execution completes
  const liveTasks = useMemo(() => {
    return tasksWithSessions.map((task) => aggregateTaskLiveState(task, liveTaskSessionsData))
  }, [tasksWithSessions, liveTaskSessionsData])

  // Task counts by source and status
  const filteredBySourceTasks = useMemo(() => {
    if (taskSourceFilter === 'all') return liveTasks
    return liveTasks.filter((t) => Boolean(t.workerId?.trim() || t.worker_id?.trim()))
  }, [liveTasks, taskSourceFilter])

  const filteredBySourceRunningCount = useMemo(() => {
    return filteredBySourceTasks.filter((t) => t.status === 'running' || t.status === 'in_progress').length
  }, [filteredBySourceTasks])

  const filteredBySourceReviewCount = useMemo(() => {
    return filteredBySourceTasks.filter((t) => t.status === 'needs_review').length
  }, [filteredBySourceTasks])

  const filteredBySourceQueuedCount = useMemo(() => {
    return filteredBySourceTasks.filter((t) => t.status === 'queued' || t.status === 'pending_approval' || t.status === 'planning').length
  }, [filteredBySourceTasks])

  const filteredBySourceCompletedCount = useMemo(() => {
    return filteredBySourceTasks.filter((t) => t.status === 'completed').length
  }, [filteredBySourceTasks])

  // Filtered tasks for all layouts, search, and status
  const filteredTasks = useMemo(() => {
    const q = searchQuery.trim().toLowerCase()
    return filteredBySourceTasks.filter((task) => {
      if (q) {
        const matchesSearch =
          task.title.toLowerCase().includes(q) ||
          Boolean(task.subtitle?.toLowerCase().includes(q)) ||
          task.id.toLowerCase().includes(q) ||
          Boolean(task.workerId?.toLowerCase().includes(q)) ||
          Boolean(task.worker_id?.toLowerCase().includes(q)) ||
          Boolean(task.workerName?.toLowerCase().includes(q)) ||
          Boolean(task.worker_name?.toLowerCase().includes(q)) ||
          Boolean(task.tags?.some(tag => tag.toLowerCase().includes(q)))
        if (!matchesSearch) return false
      }

      if (statusFilter !== 'all') {
        const match = statusFilter === 'running' ? isTaskRunning(task)
          : statusFilter === 'queued' ? ['queued', 'pending_approval', 'planning'].includes(task.status)
          : task.status === statusFilter
        if (!match) return false
      }

      if (selectedTag !== 'all' && !task.tags?.includes(selectedTag)) {
        return false
      }

      return true
    })
  }, [filteredBySourceTasks, searchQuery, statusFilter, selectedTag])

  const visibleCreativeTaskIds = new Set(filteredTasks.filter(isCreativeMediaTask).map(task => task.id))
  const renderCreativeThreads = (column?: string) => <MediaTaskThreads
    tasks={liveTasks} visibleTaskIds={visibleCreativeTaskIds} column={column}
    actions={task => ({
      onPreview: (output, mode) => handleOpenDeliverableInMediaCenter(output, task, mode),
      onApprove: () => handleApproveTask(task.id),
      onArchive: selectionProps(task).onArchiveTask,
      onDelete: () => handleDeleteTask(task.id),
      isApproving: approvingTaskIds.has(task.id), error: taskActionErrors[task.id],
    })}
  />
  const markedRows = filteredTasks.filter(row => markedTaskIds.has(row.id))
  useEffect(() => { setMarkedTaskIds(new Set()); setManagementMessage(''); setArchivedOpen(false) }, [selectedProjectId, searchQuery, taskSourceFilter, statusFilter])
  useEffect(() => {
    setMarkedTaskIds(prev => {
      const valid = new Set(filteredTasks.map(row => row.id))
      return [...prev].every(id => valid.has(id)) ? prev : new Set([...prev].filter(id => valid.has(id)))
    })
  }, [filteredTasks])
  const archivedRequest = useRef<{ projectId: string; epoch: number; promise: Promise<void> } | null>(null)
  const loadArchivedTasks = (projectId: string, force = false): Promise<void> => {
    const epoch = managementEpoch.current
    const pending = archivedRequest.current
    if (!force && pending?.projectId === projectId && pending.epoch === epoch) return pending.promise
    setArchivedLoading(true)
    setArchivedError('')
    const current = () => managementEpoch.current === epoch && selectedProjectRef.current === projectId && archivedRequest.current === entry
    const entry = { projectId, epoch, promise: Promise.resolve() }
    entry.promise = requestJson<{ tasks: any[] }>(`/v3/projects/${encodeURIComponent(projectId)}/tasks?view=archived`)
      .then(result => { if (current()) setArchivedTasks(mapBackendTasks(result.tasks || [])) })
      .catch(err => { if (current()) setArchivedError(err instanceof Error ? err.message : 'Failed to load archived tasks') })
      .finally(() => { if (current()) setArchivedLoading(false); if (archivedRequest.current === entry) archivedRequest.current = null })
    archivedRequest.current = entry
    return entry.promise
  }
  useEffect(() => {
    if (!archivedOpen || !selectedProjectId) return
    archivedCloseRef.current?.focus()
    void loadArchivedTasks(selectedProjectId)
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { setArchivedOpen(false); archivedTriggerRef.current?.focus() }
      if (event.key === 'Tab') {
        const dialog = archivedCloseRef.current?.closest('[role="dialog"]')
        const controls = dialog?.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], input:not(:disabled)')
        if (!controls?.length) return
        const first = controls[0], last = controls[controls.length - 1]
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus() }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [archivedOpen, selectedProjectId])

  const toggleMarked = (id: string) => setMarkedTaskIds(prev => {
    const next = new Set(prev)
    if (next.has(id)) next.delete(id); else next.add(id)
    return next
  })
  const selectionProps = (row: RunningTask) => ({
    isMarked: markedTaskIds.has(row.id), onToggleMarked: () => toggleMarked(row.id),
    onArchiveTask: () => { void manageTasks([row], 'archive') },
    onInvestigateSession: (sessionId: string) => {
      const current = liveTasks.find(task => task.id === row.id)
      if (!current || !extractTaskSessionIds(current as TaskSessionCandidate).includes(sessionId)) return
      setActiveTaskId(current.id)
      setActiveSessionId(sessionId)
    },
    onAskOrchestrator: () => {
      if (!tasks.some(task => task.id === row.id)) return
      selectedWorkerRef.current = null
      setSelectedWorker(null)
      setWorkerCreationRequested(false)
      setAttachedTaskIds(ids => ids.includes(row.id) ? ids : [...ids, row.id])
    },
  })

  const selectedTaskForSplit = useMemo(() => {
    return selectedTaskId ? liveTasks.find((t) => t.id === selectedTaskId) || null : null
  }, [liveTasks, selectedTaskId])

  const handleDeleteProject = async (projectId: string, e?: React.MouseEvent) => {
    e?.stopPropagation()
    if (!window.confirm(`Delete project "${projects.find(item => item.id === projectId)?.name || projectId}"? This cannot be undone.`)) return
    try {
      await requestJson(`/v3/projects/${projectId}`, { method: 'DELETE' })
      desktopProjects.evict(projectId)
      setProjects((prev) => {
        const next = prev.filter((p) => p.id !== projectId)
        if (selectedProjectId === projectId) {
          if (next.length > 0) {
            setSelectedProjectId(next[0].id)
            if (next[0].primarySessionId) {
              setActiveSessionId(next[0].primarySessionId)
            }
          } else {
            setActiveSessionId('')
            setIsOnboardingActive(true)
          }
        }
        return next
      })
    } catch (err) {
      console.warn('Delete project failed:', err)
    }
  }

  // Workspaces toggling for project onboarding
  const handleToggleWorkspace = (path: string) => {
    setOnboardingWorkspaces((prev) =>
      prev.map((w) => (w.path === path ? { ...w, selected: !w.selected } : w))
    )
  }

  const handleAddCustomFolder = () => {
    const trimmed = customFolderPath.trim()
    if (!trimmed) return
    if (onboardingWorkspaces.some((w) => w.path === trimmed)) {
      setOnboardingWorkspaces((prev) =>
        prev.map((w) => (w.path === trimmed ? { ...w, selected: true } : w))
      )
    } else {
      const base = trimmed.split('/').filter(Boolean).pop() || 'Workspace'
      setOnboardingWorkspaces((prev) => [
        ...prev,
        { path: trimmed, label: base, role: 'auxiliary', selected: true },
      ])
    }
    setCustomFolderPath('')
  }

  const handleSynthesizeContext = async () => {
    setIsSynthesizing(true)
    try {
      const selectedWs = onboardingWorkspaces.filter((w) => w.selected).map((w) => w.path)
      const res = await requestJson<{ project_context: string }>('/v3/projects/synthesize-context', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: onboardingName.trim() || 'Project Architecture',
          workspaces: selectedWs,
        }),
      })
      if (res?.project_context) {
        setOnboardingContext(res.project_context)
      }
    } catch (err) {
      console.warn('Context synthesis failed, generating local fallback:', err)
      const selectedWs = onboardingWorkspaces.filter((w) => w.selected)
      const synthesized = `# ${onboardingName.trim() || 'Project Architecture'}

## Overview
${onboardingDescription.trim() || 'Multi-workspace software initiative managed by Swarm Orchestrator.'}

## Subsystems & Workspaces
${selectedWs.map((w) => `- \`${w.path}\`: ${w.label} (${w.role})`).join('\n')}

## Operational Directives
- Local-first architecture; session records persist to Pebble database.
- Subagents execute inside isolated Git worktrees.
- Autonomous project tasks deliver verified outcomes (code_pr, media_bundle, bug_patch, audit_report).
- Verification gate: all pull requests and deliverables require review before promotion.
`
      setOnboardingContext(synthesized)
    } finally {
      setIsSynthesizing(false)
    }
  }

  const handleCreateAndActivateProject = async () => {
    setIsActivating(true)
    const selectedWs = onboardingWorkspaces.filter((w) => w.selected).map((w) => ({
      workspace_id: w.id || '',
      path: w.path,
      role: w.role,
      label: w.label,
    }))

    const payload = {
      name: onboardingName.trim() || 'My Project',
      description: onboardingDescription.trim(),
      workspaces: selectedWs,
      project_context: onboardingContext,
    }

    let newId = ''
    try {
      const res = await requestJson<{ project: { id: string } }>('/v3/projects', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      if (res?.project?.id) {
        newId = res.project.id
      }
    } catch (err) {
      setConversationError(err instanceof Error ? err.message : 'Unable to create project')
      setIsActivating(false)
      return
    }
    if (!newId) { setConversationError('Project creation returned no identity'); setIsActivating(false); return }

    // Spawn primary orchestrator session with valid client_request_id and model preference
    let orchSessionId = ''
    const clientRequestId = `desktop-v3-create:${crypto.randomUUID()}`
    try {
      orchSessionId = await createProjectConversation(newId, clientRequestId)
    } catch (e) {
      setConversationError(e instanceof Error ? e.message : 'Project saved, but conversation creation failed. Use New session to retry.')
    }

    const newProject: ProjectSummary = {
      id: newId,
      name: payload.name,
      slug: payload.name.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
      description: payload.description,
      repoPath: selectedWs[0]?.path || '.',
      branch: 'dev',
      gitStatus: 'clean',
      linkedWorkspaces: selectedWs.map((w) => w.path),
      activeWorkersCount: 0,
      pendingDeliverablesCount: 0,
      runningTasksCount: 0,
      projectContext: payload.project_context,
      primarySessionId: undefined,
    }

    setProjects((prev) => [newProject, ...prev])
    void navigate(projectConversationLink(projectRouteSegment(newProject, [newProject, ...projects]), orchSessionId || undefined))
    if (orchSessionId) {
      setActiveSessionId(orchSessionId)
      setActiveTaskId(null)
    }
    setIsOnboardingActive(false)
    setIsActivating(false)
  }

  useEffect(() => {
    if (activeTask || workerChatOpen) responsiveLayout.setPanel('chat')
  }, [activeTask?.id, workerChatOpen])

  return (
    <div
      ref={setThemeRoot}
      className="swarm-section swarm-responsive-shell relative font-sans"
      data-layout={responsiveLayout.mode}
      data-split={responsiveLayout.split}
      data-panel={responsiveLayout.panel}
      data-navigation-open={responsiveLayout.navigationOpen}
      data-project-theme={projectTheme.state}
      style={{ ...theme.customVars, ...inheritedSwarmThemeStyle(initialThemeId), ...projectTheme.style, ...(projectTheme.colorScheme ? { colorScheme: projectTheme.colorScheme } : {}) } as React.CSSProperties}
    >
      <SwarmLayoutControls layout={responsiveLayout} />
      {/* ─────────────────────────────────────────────────────────────
          PANEL 1: LEFT SIDEBAR (NAVIGATION, PROJECTS & USER HUD)
         ───────────────────────────────────────────────────────────── */}
      <aside id={responsiveLayout.navigationId} aria-label="Swarm navigation" role={responsiveLayout.navigationOpen ? 'dialog' : undefined} aria-modal={responsiveLayout.navigationOpen ? true : undefined} tabIndex={-1} className="swarm-navigation-sidebar relative order-first flex w-72 flex-shrink-0 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
        <button type="button" className="swarm-navigation-close" aria-label="Close Swarm navigation" onClick={() => responsiveLayout.setNavigationOpen(false)}>Close navigation</button>
        {/* App Branding & Header */}
        <header className="swarm-unified-header">
          <div className="swarm-header-row">
            <div className="flex min-w-0 flex-1 items-center gap-2.5">
              <ProjectHeaderIdentity project={selectedProject} projects={projects}
                onSelect={id => { setSelectedProjectId(id); setIsOnboardingActive(false); setActiveTaskId(null) }}
                onCreate={() => { setIsOnboardingActive(true); setActiveNavTab('home') }}
                onManage={() => { setIsOnboardingActive(false); setActiveNavTab('projects') }} />
            </div>

            {accountScopeId && <OrchestratorNotifications accountScopeId={accountScopeId} workspaceSlug={workspaceSlug} />}
          </div>
        </header>
        <ProjectNavigation activePage={activeNavTab} projectSegment={selectedProjectSegment} sessionId={routeConversationId || undefined}
          workspaceSlug={workspaceSlug} deliverableCount={liveTasks.flatMap(task => task.deliverables || []).length}
          mediaCount={allMediaLibraryItems.length}
          onSelect={() => { setIsOnboardingActive(projects.length === 0); responsiveLayout.setPanel('main'); responsiveLayout.setNavigationOpen(false) }} />
        <ProjectTaskAttention tasks={liveTasks} onOpen={task => {
          setSelectedTaskId(task.id); setExpandedTaskId(task.id); setStatusFilter('all'); setActiveNavTab('home')
          responsiveLayout.setPanel('main'); responsiveLayout.setNavigationOpen(false)
          if (task.sessionId) { setActiveTaskId(task.id); setActiveSessionId(task.sessionId); setWorkerChatOpen(true) }
        }} />

        <ProjectConversationSidebar projectId={selectedProjectSegment} projectName={selectedProject?.name}
          selectedId={routeConversationId} sessions={conversations.sessions} rows={conversations.rows} loading={conversations.loading}
          creating={creatingConversation} error={projectRouteError || conversationError || conversations.error}
          onCreate={() => void newConversation()}
          onRetry={() => { conversations.refresh(); if (routeConversationId && !admittedParentId) setAdmissionAttempt(value => value + 1) }}
          onSelect={() => { setActiveTaskId(null); setActiveSessionId(admittedParentId); responsiveLayout.setPanel('chat'); responsiveLayout.setNavigationOpen(false) }} />

        {/* Workers for the selected project only. Project management lives in the header. */}
        <div className="p-3 border-b border-slate-800/80 flex-1 overflow-y-auto">
          {accountScopeId && selectedProject && <ProjectWorkerSidebar key={selectedProject.id} accountScopeId={accountScopeId} projectId={selectedProject.id} showActivity onBrowse={() => setActiveNavTab('workers')} onInspect={id => { void navigate({ ...projectPageLink('workers'), search: { section: 'workers', workerId: id } }) }} />}

        </div>

        {/* Uploaded Media & Documents Shelf */}
        <div
          className="p-3 border-b border-slate-800/80"
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault()
            if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
              void handleFileUpload(e.dataTransfer.files)
            }
          }}
        >
          <div className="flex items-center justify-between pb-1.5">
            <div className="flex items-center gap-1.5">
              <button
                type="button"
                onClick={() => setIsUploadedMediaExpanded(!isUploadedMediaExpanded)}
                className="text-slate-400 hover:text-slate-200"
                title={isUploadedMediaExpanded ? 'Collapse shelf' : 'Expand shelf'}
              >
                {isUploadedMediaExpanded ? <ChevronUp size={12} /> : <ChevronDown size={12} />}
              </button>
              <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-500">
                Uploaded Media ({uploadedMedia.length})
              </span>
            </div>
            <div className="flex items-center gap-1">
              <button type="button" className="swarm-shelf-icon-button"
                aria-label="Upload image, video, audio, or document"
                title="Upload image, video, audio, or document"
                onClick={() => shelfFileInputRef.current?.click()}>
                <Upload size={14} aria-hidden="true" />
              </button>
              <input ref={shelfFileInputRef} type="file" multiple hidden
                onChange={(e) => { const files = Array.from(e.target.files || []); e.target.value = ''; void handleFileUpload(files) }} />
              <button
                type="button"
                onClick={() => { setShelfUploadError(null); setIsPasteDocOpen(true) }}
                className="swarm-shelf-icon-button"
                aria-label="Paste Markdown / Text Document"
                title="Paste Markdown / Text Document"
              >
                <FileText size={14} aria-hidden="true" />
              </button>
            </div>
          </div>

          <p className="text-[10px] text-slate-400">Project files for task attachments. To attach to chat, use the composer paperclip.</p>
          {shelfUploadError && (
            <div role="alert" className="mb-1.5 p-1.5 rounded bg-red-950/40 border border-red-500/30 text-red-200 text-[10px] flex items-center justify-between gap-1.5">
              <span className="flex-1 break-words">{shelfUploadError}</span>
              {shelfFailedFiles.length > 0 && (
                <button
                  type="button"
                  onClick={() => void handleFileUpload(shelfFailedFiles)}
                  className="px-1.5 py-0.5 rounded bg-red-600/30 hover:bg-red-600/50 text-red-100 border border-red-500/40 font-semibold text-[9px] shrink-0 transition"
                >
                  Retry
                </button>
              )}
            </div>
          )}

          {isUploadedMediaExpanded && (uploadedMedia.length > 0 ? (
            <div className="max-h-36 overflow-y-auto space-y-1.5 pt-1">
              {uploadedMedia.map((m) => {
                const isTagged = taggedMedia.some((t) => t.id === m.id)
                return (
                  <div
                    key={m.id}
                    className="flex items-center justify-between p-1.5 rounded-lg bg-slate-900/60 border border-slate-800/80 hover:border-slate-700 text-xs transition"
                  >
                    <div
                      onClick={() => handleOpenUploadedInMediaCenter(m)}
                      className="flex items-center gap-2 min-w-0 flex-1 cursor-pointer hover:opacity-80"
                      title="Click to view & edit in studio"
                    >
                      {m.kind === 'image' && <ImageIcon size={13} className="text-blue-400 shrink-0" />}
                      {m.kind === 'video' && <Film size={13} className="text-purple-400 shrink-0" />}
                      {m.kind === 'audio' && <Music size={13} className="text-amber-400 shrink-0" />}
                      {m.kind === 'doc' && <FileText size={13} className="text-emerald-400 shrink-0" />}
                      <span className="truncate text-[11px] text-slate-300 font-medium" title={m.title || m.filename}>
                        {m.title || m.filename}
                      </span>
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      {(m.kind === 'image' || m.kind === 'video') && (
                        <>
                          <button
                            type="button"
                            onClick={() => handleOpenUploadedInMediaCenter(m, 'fine_tune')}
                            className="px-1.5 py-0.5 rounded text-[9px] font-mono text-amber-300 hover:text-white bg-slate-800 hover:bg-slate-700 transition"
                            title={m.kind === 'video' ? 'Fine-tune / modify video' : 'Fine-tune image ("change this to...")'}
                          >
                            Edit
                          </button>
                          <button
                            type="button"
                            onClick={() => handleOpenUploadedInMediaCenter(m, 'iterate')}
                            className="px-1.5 py-0.5 rounded text-[9px] font-mono text-emerald-300 hover:text-white bg-slate-800 hover:bg-slate-700 transition"
                            title="Iterate variations"
                          >
                            Iterate
                          </button>
                        </>
                      )}
                      <button
                        type="button"
                        onClick={() => toggleTagMediaRef(m)}
                        className={`px-1.5 py-0.5 rounded text-[9px] font-mono font-bold transition ${
                          isTagged
                            ? 'bg-blue-600 text-white'
                            : 'bg-slate-800 text-slate-400 hover:text-white'
                        }`}
                        title={isTagged ? 'Tagged for Task' : 'Tag for Task'}
                      >
                        {isTagged ? 'Tagged' : 'Tag'}
                      </button>
                      <button
                        type="button"
                        onClick={() => void handleDeleteUploadedMedia(m.id)}
                        className="p-1 text-slate-500 hover:text-rose-400 rounded transition"
                        title="Remove uploaded media"
                      >
                        <Trash2 size={11} />
                      </button>
                    </div>
                  </div>
                )
              })}
            </div>
          ) : (
            <p className="text-[10px] text-slate-500 pt-1">
              No files uploaded. Upload images/videos or paste docs to tag for tasks.
            </p>
          ))}
        </div>



        {/* Real User HUD */}
        <div className="p-3 border-t border-slate-800/80 flex items-center bg-[#0a0f1d]/50">
          <div className="flex items-center gap-2.5 min-w-0 w-full">
            <PersonalAvatar key={`${accountScopeId}:${userProfile.id}`} userId={userProfile.id} accountScopeId={accountScopeId || ''} name={userProfile.name} />
            {isEditingAccountName ? (
              <form
                onSubmit={(e) => {
                  e.preventDefault()
                  void handleSaveAccountName()
                }}
                className="flex items-center gap-1.5 min-w-0 flex-1"
              >
                <input
                  type="text"
                  value={editingAccountName}
                  onChange={(e) => {
                    setEditingAccountName(e.target.value)
                    if (accountNameError) setAccountNameError(null)
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Escape') {
                      setIsEditingAccountName(false)
                      setEditingAccountName(userProfile.name)
                      setAccountNameError(null)
                    }
                  }}
                  disabled={isUpdatingAccountName}
                  autoFocus
                  placeholder="Username"
                  aria-label="Account username"
                  className="h-6 w-full min-w-0 rounded bg-slate-900 px-1.5 text-xs font-medium text-slate-200 border border-blue-500/50 focus:outline-none focus:ring-1 focus:ring-blue-500"
                  data-testid="account-name-input"
                />
                <button
                  type="submit"
                  disabled={isUpdatingAccountName || !editingAccountName.trim() || editingAccountName.trim() === userProfile.name}
                  className="p-1 rounded text-slate-400 hover:text-emerald-400 disabled:opacity-30 transition-colors shrink-0"
                  title="Save name"
                  aria-label="Save name"
                  data-testid="account-name-save-btn"
                >
                  {isUpdatingAccountName ? <Loader2 size={12} className="animate-spin text-blue-400" /> : <Check size={12} />}
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setIsEditingAccountName(false)
                    setEditingAccountName(userProfile.name)
                    setAccountNameError(null)
                  }}
                  disabled={isUpdatingAccountName}
                  className="p-1 rounded text-slate-400 hover:text-rose-400 disabled:opacity-30 transition-colors shrink-0"
                  title="Cancel"
                  aria-label="Cancel"
                  data-testid="account-name-cancel-btn"
                >
                  <X size={12} />
                </button>
              </form>
            ) : (
              <div className="flex items-center min-w-0 flex-1">
                <button
                  type="button"
                  onClick={() => {
                    setEditingAccountName(userProfile.name)
                    setIsEditingAccountName(true)
                    setAccountNameError(null)
                  }}
                  className="group/acc flex items-center gap-1.5 min-w-0 text-left rounded px-1.5 py-1 -mx-1.5 hover:bg-slate-800/60 transition-colors"
                  title="Click to change account name"
                  aria-label={`Change account name (current: ${userProfile.name})`}
                  data-testid="account-name-button"
                >
                  <span className="text-xs font-semibold text-slate-200 truncate group-hover/acc:text-blue-300">
                    {userProfile.name}
                  </span>
                  <Edit3 size={11} className="text-slate-500 opacity-0 group-hover/acc:opacity-100 transition-opacity shrink-0" />
                </button>
              </div>
            )}
          </div>
        </div>
      </aside>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 2: MIDDLE SECTION (CANVAS / TASKS / VIEWS)
         ───────────────────────────────────────────────────────────── */}
      {!showFullMediaCenter && <main className="swarm-main-panel relative min-w-0 flex flex-1 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
        <nav aria-label="Tasks and Workers views" className="flex shrink-0 gap-2 border-b border-slate-800 p-3 text-xs">
          <Link {...projectPageLink('home')} activeOptions={{ exact: true, includeSearch: true }} aria-current={activeNavTab === 'home' ? 'page' : undefined} className="rounded-lg border border-slate-700 px-3 py-2">Tasks</Link>
          <Link {...projectPageLink('workers')} activeOptions={{ exact: true, includeSearch: true }} aria-current={activeNavTab === 'workers' ? 'page' : undefined} className="rounded-lg border border-slate-700 px-3 py-2">Workers</Link>
        </nav>
        {isOnboardingActive && (activeNavTab === 'home' || activeNavTab === 'projects') ? (
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-6">
            {/* Onboarding Header */}
            <div className="flex items-start justify-between border-b border-slate-800/80 pb-5">
              <div>
                <div className="flex items-center gap-2 text-blue-400 font-semibold text-xs uppercase tracking-wider mb-1">
                  <Sparkles size={14} className="text-blue-400 animate-pulse" />
                  <span>Project Setup & Architecture Synthesis</span>
                </div>
                <h1 className="text-xl font-bold text-white tracking-tight">Create Your Project</h1>
                <p className="text-xs text-slate-400 mt-1 max-w-xl">
                  A Project elevates your local workspaces into an executive space coordinated by the Swarm Orchestrator.
                </p>
              </div>
              {projects.length > 0 && (
                <button
                  onClick={() => setIsOnboardingActive(false)}
                  className="text-xs text-slate-400 hover:text-slate-200 px-3 py-1.5 rounded-xl border border-slate-800 hover:bg-slate-800 transition-colors"
                >
                  Cancel
                </button>
              )}
            </div>

            {/* Project Identity Inputs */}
            <div className="rounded-2xl border border-slate-800/80 bg-slate-900/60 p-4 space-y-3">
              <div className="text-xs font-semibold text-slate-300">Project Identity</div>
              <div className="swarm-content-grid grid grid-cols-1 md:grid-cols-2 gap-3">
                <div>
                  <label className="text-[11px] text-slate-400 mb-1 block">Project Name</label>
                  <input
                    type="text"
                    value={onboardingName}
                    onChange={(e) => setOnboardingName(e.target.value)}
                    placeholder="e.g. Swarm Platform"
                    className="w-full text-xs rounded-xl bg-slate-950/80 border border-slate-800 px-3 py-2 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/60 transition-all font-medium"
                  />
                </div>
                <div>
                  <label className="text-[11px] text-slate-400 mb-1 block">Description</label>
                  <input
                    type="text"
                    value={onboardingDescription}
                    onChange={(e) => setOnboardingDescription(e.target.value)}
                    placeholder="e.g. Core Go daemon, desktop client, and video production pipeline"
                    className="w-full text-xs rounded-xl bg-slate-950/80 border border-slate-800 px-3 py-2 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/60 transition-all font-medium"
                  />
                </div>
              </div>
            </div>

            {/* Section 1: Workspace Selection */}
            <div className="rounded-2xl border border-slate-800/80 bg-slate-900/60 p-4 space-y-3">
              <div className="flex items-center justify-between">
                <div>
                  <div className="text-xs font-semibold text-slate-300">1. Select Bound Workspaces</div>
                  <p className="text-[11px] text-slate-500">Choose existing repositories on your machine to bind into this project.</p>
                </div>
                <span className="text-[11px] text-blue-400 font-medium">
                  {onboardingWorkspaces.filter((w) => w.selected).length} selected
                </span>
              </div>

              <div className="grid grid-cols-1 gap-2 pt-1">
                {onboardingWorkspaces.map((ws) => (
                  <div
                    key={ws.path}
                    onClick={() => handleToggleWorkspace(ws.path)}
                    className={`flex items-center justify-between p-3 rounded-xl border transition-all cursor-pointer ${
                      ws.selected
                        ? 'border-blue-500/40 bg-blue-950/20 text-white shadow-sm'
                        : 'border-slate-800/80 bg-slate-950/40 text-slate-400 hover:border-slate-700/80 hover:text-slate-200'
                    }`}
                  >
                    <div className="flex items-center gap-3">
                      <div className={`h-4 w-4 rounded flex items-center justify-center border transition-all ${
                        ws.selected ? 'bg-blue-600 border-blue-500 text-white' : 'border-slate-700 bg-slate-900'
                      }`}>
                        {ws.selected && <Check size={11} strokeWidth={3} />}
                      </div>
                      <div>
                        <div className="text-xs font-semibold flex items-center gap-2">
                          <span>{ws.label}</span>
                          <span className={`text-[10px] px-1.5 py-0.5 rounded font-mono ${
                            ws.role === 'primary_code' ? 'bg-blue-500/10 text-blue-400 border border-blue-500/20' : 'bg-slate-800 text-slate-400'
                          }`}>
                            {ws.role}
                          </span>
                        </div>
                        <div className="text-[11px] text-slate-500 font-mono mt-0.5">{ws.path}</div>
                      </div>
                    </div>
                  </div>
                ))}
              </div>

              {/* Add folder inline */}
              <div className="pt-2 flex items-center gap-2">
                <input
                  type="text"
                  value={customFolderPath}
                  onChange={(e) => setCustomFolderPath(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && handleAddCustomFolder()}
                  placeholder="Enter path to another workspace folder on this machine..."
                  className="flex-1 text-xs rounded-xl bg-slate-950/80 border border-slate-800 px-3 py-2 text-slate-200 placeholder-slate-600 focus:outline-none focus:border-blue-500/60 font-mono"
                />
                <button
                  type="button"
                  onClick={handleAddCustomFolder}
                  className="px-3 py-2 rounded-xl bg-slate-800 hover:bg-slate-700 text-slate-200 text-xs font-medium transition-all"
                >
                  Add Folder
                </button>
              </div>
            </div>

            {/* Section 2: AI Context Synthesis */}
            <div className="rounded-2xl border border-slate-800/80 bg-slate-900/60 p-4 space-y-3">
              <div className="flex items-center justify-between">
                <div>
                  <div className="text-xs font-semibold text-slate-300">2. Authoritative Project Context (project.md)</div>
                  <p className="text-[11px] text-slate-500">
                    Synthesizes architectural boundaries from selected workspaces to inject into Swarm Orchestrator.
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <button
                    type="button"
                    onClick={handleSynthesizeContext}
                    disabled={isSynthesizing || onboardingWorkspaces.filter((w) => w.selected).length === 0}
                    className="flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-blue-600/20 hover:bg-blue-600/30 text-blue-400 border border-blue-500/30 text-xs font-medium transition-all disabled:opacity-50"
                  >
                    <Sparkles size={12} className={isSynthesizing ? 'animate-spin' : ''} />
                    <span>{isSynthesizing ? 'Synthesizing...' : 'Generate with AI'}</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => setIsEditingContext(!isEditingContext)}
                    className="flex items-center gap-1 px-2.5 py-1.5 rounded-xl bg-slate-800 hover:bg-slate-700 text-slate-300 text-xs transition-all"
                  >
                    <Edit3 size={12} />
                    <span>{isEditingContext ? 'Preview' : 'Edit'}</span>
                  </button>
                </div>
              </div>

              {isEditingContext ? (
                <textarea
                  value={onboardingContext}
                  onChange={(e) => setOnboardingContext(e.target.value)}
                  rows={10}
                  className="w-full text-xs font-mono rounded-xl bg-slate-950/80 border border-slate-800 p-3 text-slate-200 focus:outline-none focus:border-blue-500/60 transition-all leading-relaxed"
                />
              ) : (
                <div className="rounded-xl bg-slate-950/80 border border-slate-800 p-3 max-h-56 overflow-y-auto font-mono text-[11px] text-slate-300 whitespace-pre-wrap leading-relaxed">
                  {onboardingContext || 'Click "Generate with AI" to synthesize architecture from selected workspaces.'}
                </div>
              )}
            </div>

            {/* Submit Action */}
            <div className="flex items-center justify-between pt-2">
              <div className="text-[11px] text-slate-500">
                Local-first • Saved securely in Swarm Pebble database
              </div>
              <button
                type="button"
                onClick={handleCreateAndActivateProject}
                disabled={isActivating || !onboardingName.trim()}
                className="px-5 py-2.5 rounded-xl bg-gradient-to-r from-blue-600 to-indigo-600 hover:from-blue-500 hover:to-indigo-500 text-white font-semibold text-xs shadow-lg shadow-blue-600/20 transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <span>{isActivating ? 'Activating...' : 'Create & Activate Project'}</span>
                <ArrowRight size={14} />
              </button>
            </div>
          </div>
        ) : activeNavTab === 'projects' ? (
          /* PROJECTS OVERVIEW TAB */
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div>
                <h2 className="text-base font-bold text-white">Registered Projects</h2>
                <p className="text-xs text-slate-400 mt-0.5">Projects managed by Swarm Orchestrator with Pebble persistence</p>
              </div>
              <button
                onClick={() => setIsOnboardingActive(true)}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white text-xs font-semibold"
              >
                <Plus size={13} />
                <span>+ New Project</span>
              </button>
            </div>
            {selectedProject && <Link {...projectPageLink('charter')} aria-label="Project Charter" className="swarm-project-charter-link">
              <FileText size={15} aria-hidden="true" /><span>Project Charter · {selectedProject.name}</span>
            </Link>}
            <div className="swarm-content-grid grid grid-cols-1 md:grid-cols-2 gap-4">
              {projects.map((p) => (
                <div
                  key={p.id}
                  onClick={() => {
                    setSelectedProjectId(p.id)
                    setActiveTaskId(null)
                  }}
                  className={`p-4 rounded-2xl border transition-all cursor-pointer ${
                    p.id === selectedProjectId
                      ? 'bg-slate-800/80 border-blue-500/50 shadow-md'
                      : 'bg-[#0a0f1d] border-slate-800 hover:border-slate-700'
                  }`}
                >
                  <div className="flex items-center justify-between mb-2">
                    <h3 className="text-sm font-bold text-white flex items-center gap-2">
                      <Folder size={15} className="text-blue-400" />
                      <span>{p.name}</span>
                    </h3>
                    <div className="flex items-center gap-2">
                      <span className="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-800 text-slate-300">
                        {p.branch || 'dev'}
                      </span>
                      <button
                        onClick={(e) => handleDeleteProject(p.id, e)}
                        className="text-slate-500 hover:text-rose-400 p-1 rounded transition-colors"
                        title="Delete project"
                      >
                        <Trash2 size={13} />
                      </button>
                    </div>
                  </div>
                  <p className="text-xs text-slate-400 mb-3">{p.description || 'No description'}</p>
                  <div className="space-y-1">
                    <span className="text-[10px] text-slate-500 font-semibold uppercase tracking-wider block">
                      Bound Workspaces:
                    </span>
                    <div className="flex flex-wrap gap-1">
                      {p.linkedWorkspaces.map((ws) => (
                        <span key={ws} className="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-900 border border-slate-800 text-slate-300">
                          {ws}
                        </span>
                      ))}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        ) : activeNavTab === 'workers' ? (
          <WorkerHub workspaceSlug={workspaceSlug} initialWorkerId={inspectedWorkerId} onInspectWorker={id => { if (id) void navigate({ ...projectPageLink('workers'), search: { section: 'workers', workerId: id } }); else setActiveNavTab('workers') }} onSelectWorker={worker => { void openWorkerConversation(worker) }} onAddWorker={() => { void openWorkerConversation(null) }} />
        ) : activeNavTab === 'deliverables' ? (
          /* DELIVERABLES TAB */
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div>
                <h2 className="text-base font-bold text-white">Project Deliverables</h2>
                <p className="text-xs text-slate-400 mt-0.5">Media assets, reports, and code deliverables produced by autonomous workers</p>
              </div>
              <button
                type="button"
                onClick={() => setShowFullMediaCenter(true)}
                className="flex items-center gap-2 px-3 py-1.5 rounded-xl bg-blue-600 hover:bg-blue-500 text-white text-xs font-semibold shadow-md transition"
              >
                <Film size={14} />
                <span>Open Media Studio</span>
              </button>
            </div>
            {liveTasks.flatMap((t) => t.deliverables || []).length > 0 ? (
              <div className="swarm-content-grid grid grid-cols-2 md:grid-cols-3 gap-3">
                {liveTasks.flatMap((t) => t.deliverables || []).map((d) => (
                  <div
                    key={d.id}
                    onClick={() => {
                      if (d.status === 'ready' || d.status === 'accepted') {
                        handleOpenDeliverableInMediaCenter(d)
                      }
                    }}
                    className={`rounded-2xl border p-3 space-y-2 transition-all ${
                      d.status === 'generating'
                        ? 'border-blue-500/40 bg-blue-950/20 animate-pulse'
                        : d.status === 'pending'
                        ? 'border-dashed border-slate-800 bg-[#0a0f1d]'
                        : 'border-slate-800 bg-[#0a0f1d] hover:border-blue-500/50 cursor-pointer shadow-sm'
                    }`}
                  >
                    <DeliverableThumbnail
                      type={d.thumbnailType}
                      deliverableType={d.type}
                      previewUrl={d.previewUrl || d.mediaUrl}
                      duration={d.duration}
                      status={d.status}
                      onPlay={() => {
                        if (d.status === 'ready' || d.status === 'accepted') {
                          handleOpenDeliverableInMediaCenter(d)
                        }
                      }}
                    />
                    <div className="text-xs font-semibold text-white truncate" title={d.title}>{d.title}</div>
                    <div className="flex items-center justify-between text-[10px] text-slate-400 font-mono">
                      <span className="text-blue-400 uppercase">{d.type}</span>
                      <span className={`px-1.5 py-0.5 rounded text-[9px] uppercase font-bold ${
                        d.status === 'ready' || d.status === 'accepted'
                          ? 'bg-emerald-950/60 text-emerald-300 border border-emerald-500/30'
                          : d.status === 'generating'
                          ? 'bg-blue-950/60 text-blue-300 border border-blue-500/30'
                          : 'bg-slate-900 text-slate-500 border border-slate-800'
                      }`}>
                        {d.status}
                      </span>
                    </div>
                    {(d.status === 'ready' || d.status === 'accepted') && (
                      <div className="flex items-center justify-between pt-1 border-t border-slate-800/60">
                        <button
                          type="button"
                          onClick={(e) => {
                            e.stopPropagation()
                            toggleTagDeliverable(d)
                          }}
                          className={`flex items-center gap-1 px-2 py-0.5 rounded text-[10px] font-semibold transition ${
                            taggedMedia.some((t) => t.id === d.id)
                              ? 'bg-blue-600 text-white shadow-sm'
                              : 'bg-slate-800 text-slate-300 hover:text-white hover:bg-slate-700'
                          }`}
                          title={taggedMedia.some((t) => t.id === d.id) ? 'Tagged for Task' : 'Tag for Task'}
                        >
                          <Tag size={10} className={taggedMedia.some((t) => t.id === d.id) ? 'fill-current' : ''} />
                          <span>{taggedMedia.some((t) => t.id === d.id) ? 'Tagged' : 'Tag'}</span>
                        </button>
                        <div className="flex items-center gap-1">
                          <button
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation()
                              handleOpenDeliverableInMediaCenter(d, tasks.find((t) => t.deliverables?.some((entry) => entry.id === d.id)), 'fine_tune')
                            }}
                            className="flex items-center gap-0.5 px-1.5 py-0.5 rounded bg-slate-800 hover:bg-slate-700 text-[10px] text-amber-300 hover:text-white transition font-medium"
                            title="Fine-tune / modify this media"
                          >
                            <Edit3 size={10} />
                            <span>Edit</span>
                          </button>
                          <button
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation()
                              handleOpenDeliverableInMediaCenter(d, tasks.find((t) => t.deliverables?.some((entry) => entry.id === d.id)), 'iterate')
                            }}
                            className="flex items-center gap-0.5 px-1.5 py-0.5 rounded bg-slate-800 hover:bg-slate-700 text-[10px] text-emerald-300 hover:text-white transition font-medium"
                            title="Configure variations"
                          >
                            <Sparkles size={10} />
                            <span>Iterate</span>
                          </button>
                          <button
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation()
                              handleOpenDeliverableInMediaCenter(d)
                            }}
                            className="text-[10px] text-blue-400 hover:text-blue-300 font-medium ml-1"
                          >
                            Studio →
                          </button>
                        </div>
                      </div>
                    )}
                  </div>
                ))}
              </div>
            ) : (
              <div className="p-8 text-center border border-dashed border-slate-800 rounded-2xl">
                <Layers size={24} className="mx-auto text-slate-600 mb-2" />
                <h4 className="text-xs font-bold text-slate-300">No deliverables yet</h4>
                <p className="text-[11px] text-slate-500 mt-1">Deploy tasks generating video or designs to view deliverables here.</p>
              </div>
            )}
          </div>
        ) : activeNavTab === 'usage' ? (
          <UsagePage embedded />
        ) : activeNavTab === 'agents' ? (
          <OrchestrateAgents />
        ) : activeNavTab === 'settings' ? (
          <div className="min-h-0 overflow-y-auto">
            <section className="p-6 space-y-3" aria-label="Project appearance">
              <div className="swarm-theme-picker">
                <label htmlFor="swarm-project-theme">Project theme</label>
                <select id="swarm-project-theme" aria-label="Project theme" disabled={!selectedProject || themeSaving}
                  value={selectedProject?.themeId || ''} onChange={event => void handleProjectThemeChange(event.target.value)}>
                  <option value="">Inherit Swarm default</option>
                  {selectedProject?.themeId && projectTheme.state === 'missing' && <option value={selectedProject.themeId}>Missing theme: {selectedProject.themeId}</option>}
                  {themeOptions.map(option => <option key={option.id} value={option.id}>{option.label}</option>)}
                </select>
                {projectTheme.state === 'missing' && <p role="status">{formatWorkspaceThemeLabel(selectedProject!.themeId!)} was deleted or is unavailable. Reset or select a saved theme.</p>}
                {themeError && <p role="alert">{themeError}</p>}
              </div>
              {selectedProject && <ProjectImageSettings key={selectedProject.id} project={selectedProject} onSave={handleProjectImageChange} />}
            </section>
            <OrchestrateSettings workspaceSlug={workspaceSlug} />
          </div>
        ) : activeNavTab === 'help' ? (
          <section className="min-h-0 overflow-y-auto p-6 space-y-4 text-[var(--app-text)]"><h1 className="text-xl font-semibold">Orchestrate tips</h1><ul className="list-disc space-y-3 pl-5">{ORCHESTRATE_TIPS.map((tip) => <li key={tip}>{tip}</li>)}</ul></section>
        ) : activeNavTab === 'charter' ? (
          /* Project-scoped editor; preserve the existing charter deep link. */
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-4">
            <Link {...projectPageLink('projects')} className="swarm-project-charter-link">← Projects</Link>
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div>
                <h2 className="text-base font-bold text-white">Project Charter: {selectedProject?.name}</h2>
                <p className="text-xs text-slate-400 mt-0.5">
                  Authoritative project.md context injected into Swarm Orchestrator prompt
                </p>
              </div>
              <div className="flex items-center gap-2">
                {selectedProject && (
                  <button
                    onClick={(e) => handleDeleteProject(selectedProject.id, e)}
                    className="flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-rose-600/20 hover:bg-rose-600/30 text-rose-400 border border-rose-500/30 text-xs font-semibold transition-all"
                  >
                    <Trash2 size={12} />
                    <span>Delete Project</span>
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => {
                    if (selectedProject?.linkedWorkspaces) {
                      setIsSynthesizing(true)
                      requestJson<{ project_context: string }>('/v3/projects/synthesize-context', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                          name: selectedProject.name,
                          workspaces: selectedProject.linkedWorkspaces,
                        }),
                      })
                        .then((res) => {
                          if (res?.project_context) {
                            requestJson(`/v3/projects/${selectedProject.id}`, {
                              method: 'PATCH',
                              headers: { 'Content-Type': 'application/json' },
                              body: JSON.stringify({ project_context: res.project_context }),
                            }).then(() => {
                              setProjects((prev) =>
                                prev.map((p) => (p.id === selectedProject.id ? { ...p, projectContext: res.project_context } : p))
                              )
                            })
                          }
                        })
                        .finally(() => setIsSynthesizing(false))
                    }
                  }}
                  disabled={isSynthesizing}
                  className="flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-blue-600/20 hover:bg-blue-600/30 text-blue-400 border border-blue-500/30 text-xs font-semibold transition-all disabled:opacity-50"
                >
                  <RefreshCw size={12} className={isSynthesizing ? 'animate-spin' : ''} />
                  <span>{isSynthesizing ? 'Synthesizing...' : 'Re-synthesize Context'}</span>
                </button>
              </div>
            </div>
            <div className="rounded-2xl border border-slate-800 bg-slate-950 p-4">
              <pre className="font-mono text-xs text-slate-300 whitespace-pre-wrap leading-relaxed overflow-x-auto">
                {selectedProject?.projectContext || 'No synthesized context available. Click "Re-synthesize Context" to scan bound repositories.'}
              </pre>
            </div>
          </div>
        ) : (
          /* HOME: 5-VARIANT TASK MANAGEMENT CANVAS */
          <>
            <TaskListHeader title={selectedProject?.name || 'Swarm Local'} branch={projectGit.data?.status.branch} workspaceCount={selectedProject?.linkedWorkspaces?.length ?? 0} orchestratorState={orchestratorState} onNewTask={() => setIsDeployModalOpen(true)} />

            {/* PROJECT-TOP PENDING WORKER BANNER (SURFACES PROPOSALS FOR ASSIGNED PROJECT) */}
            {pendingReviews.length > 0 && (
              <div className="mx-3.5 mt-3 mb-1 rounded-2xl border border-amber-500/40 bg-gradient-to-r from-amber-500/15 via-slate-900/95 to-amber-500/10 p-4 shadow-xl flex items-center justify-between gap-4">
                <div className="flex items-center gap-3.5 min-w-0">
                  <div className="h-10 w-10 rounded-xl bg-amber-500/20 text-amber-400 flex items-center justify-center border border-amber-500/40 shrink-0 font-bold text-lg shadow-sm">
                    🤖
                  </div>
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-xs font-bold text-white uppercase tracking-wider">
                        Pending Worker Proposal for {selectedProject?.name || 'Project'}
                      </span>
                      <span className="rounded-full bg-amber-500/20 text-amber-300 border border-amber-500/30 px-2 py-0.5 text-[10px] font-semibold animate-pulse">
                        Awaiting Acceptance
                      </span>
                    </div>
                    <p className="text-xs text-slate-300 truncate mt-0.5">
                      {pendingReviews[0]?.proposal.document?.title || 'Autonomous Worker'} •{' '}
                      {pendingReviews[0]?.proposal.document?.info?.goal || 'Scheduled & on-demand task execution with Swarm engine.'}
                    </p>
                  </div>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  <button
                    onClick={() => setActiveNavTab('workers')}
                    className="px-3 py-1.5 rounded-xl border border-slate-700 bg-slate-800/80 text-xs font-medium text-slate-300 hover:text-white hover:bg-slate-700 transition-all"
                  >
                    View Details
                  </button>
                  <button
                    onClick={() =>
                      handleDecideReview(
                        pendingReviews[0].permission.id,
                        pendingReviews[0].proposal.revision,
                        pendingReviews[0].proposal.digest,
                        'accept_automation'
                      )
                    }
                    disabled={reviewBusyId === pendingReviews[0].permission.id}
                    className="px-4 py-1.5 rounded-xl bg-gradient-to-r from-emerald-600 to-teal-600 hover:from-emerald-500 hover:to-teal-500 text-white text-xs font-bold shadow-lg shadow-emerald-900/30 transition-all flex items-center gap-1.5"
                  >
                    <CheckCircle2 size={13} />
                    <span>
                      {reviewBusyId === pendingReviews[0].permission.id ? 'Registering...' : 'Accept & Register Worker'}
                    </span>
                  </button>
                  <button
                    onClick={() =>
                      handleDecideReview(
                        pendingReviews[0].permission.id,
                        pendingReviews[0].proposal.revision,
                        pendingReviews[0].proposal.digest,
                        'decline_automation'
                      )
                    }
                    disabled={reviewBusyId === pendingReviews[0].permission.id}
                    className="px-3 py-1.5 rounded-xl border border-rose-500/30 bg-rose-500/10 hover:bg-rose-500/20 text-rose-300 text-xs font-medium transition-all"
                  >
                    Decline
                  </button>
                </div>
                {reviewError && <div role="alert" className="text-xs text-red-300">{reviewError}</div>}
              </div>
            )}

            {/* MAIN BODY: 5 DISTINCT VARIANTS */}
            <div className="flex-1 overflow-hidden flex flex-col">
              {projectState?.mediaError && <div role="alert">Failed to load project media: {projectState.mediaError} <button type="button" onClick={() => void desktopProjects.refresh(selectedProjectId, false)}>Retry media</button></div>}
              {projectTasksError && (
                <div className="mx-3.5 mt-2 p-3 bg-red-950/60 border border-red-500/40 rounded-xl text-xs text-red-300 flex items-center justify-between">
                  <span>Failed to load project tasks: {projectTasksError}</span>
                  <button
                    type="button"
                    onClick={() => {
                      if (selectedProjectId) desktopProjects.invalidate(selectedProjectId)
                    }}
                    className="px-2.5 py-1 bg-red-800/60 hover:bg-red-700/60 rounded text-red-100 font-medium transition-colors"
                  >
                    Retry
                  </button>
                </div>
              )}

              <TaskListToolbar
                search={searchQuery} onSearch={setSearchQuery} source={taskSourceFilter} onSource={setTaskSourceFilter}
                status={statusFilter} onStatus={setStatusFilter}
                counts={{ all: filteredBySourceTasks.length, running: filteredBySourceRunningCount, needs_review: filteredBySourceReviewCount, queued: filteredBySourceQueuedCount, completed: filteredBySourceCompletedCount }}
                total={filteredTasks.length} selected={markedRows.length} busy={Boolean(integrationBatch?.pending) || (markedRows.length > 0 && markedRows.every(row => managementPending.current.has(row.id)))}
                integrationEligible={markedRows.filter(row => !integrationSkipReason(selectedProjectId, row)).length}
                integrationDisabled={managementBusy || integrationLanePending() || markedRows.length > MAX_INTEGRATION_BATCH}
                onIntegrate={() => void handleIntegrateSelected(markedRows)}
                onSelectAll={() => setMarkedTaskIds(new Set(filteredTasks.map(row => row.id)))}
                onClear={() => setMarkedTaskIds(new Set())}
                onArchive={() => void manageTasks(markedRows, 'archive')} onDelete={() => void manageTasks(markedRows, 'delete')}
                onArchived={() => setArchivedOpen(true)} archivedRef={archivedTriggerRef}
              />
              {markedRows.length > 0 && <details className="px-4 text-xs text-slate-400"><summary>Integration eligibility</summary><ul>{markedRows.map(row => <li key={row.id}>{row.title}: {integrationSkipReason(selectedProjectId, row) || 'Ready to integrate'}</li>)}</ul>{markedRows.length > MAX_INTEGRATION_BATCH && <p>Select at most {MAX_INTEGRATION_BATCH} tasks.</p>}</details>}
              {integrationBatch && <section aria-label="Selected integration results" className="px-4 text-xs text-slate-400">
                <p role="status">{integrationBatch.pending ? 'Integrating selected tasks… ' : 'Integration batch finished. '}{(['integrated', 'already_integrated', 'recovered', 'equivalent', 'skipped', 'failed', 'not_attempted', 'pending', 'queued'] as const).map(status => `${integrationBatch.entries.filter(entry => entry.status === status).length} ${status.replace('_', ' ')}`).join(' · ')}</p>
                {integrationBatch.refreshError && <p role="alert">{integrationBatch.refreshError}</p>}
                <details><summary>Per-task results (successful integrations are not rolled back)</summary><ul>{integrationBatch.entries.map(entry => <li key={entry.id}>{entry.title}: {entry.status.replace('_', ' ')}{entry.reason ? ` — ${entry.reason}` : ''}</li>)}</ul></details>
              </section>}
              {managementBusy && <span role="status" className="px-4 text-slate-400 text-[11px]">Updating tasks…</span>}
              {managementMessage && <span role="status" className="px-4 text-amber-300 text-[11px]">{managementMessage}</span>}
              {archivedOpen && <div className="fixed inset-0 z-[100] bg-black/70 flex items-center justify-center p-4" onMouseDown={e => { if (e.target === e.currentTarget) { setArchivedOpen(false); archivedTriggerRef.current?.focus() } }}>
                <section role="dialog" aria-modal="true" aria-labelledby="archived-tasks-title" className="swarm-local-dialog w-full max-w-xl max-h-[85vh] overflow-hidden flex flex-col rounded-xl border border-slate-700 bg-[#0a101e] p-4 text-white shadow-2xl">
                  <div className="flex items-center justify-between gap-3"><h2 id="archived-tasks-title" className="text-base font-semibold">Archived tasks</h2><button type="button" ref={archivedCloseRef} onClick={() => { setArchivedOpen(false); archivedTriggerRef.current?.focus() }} aria-label="Close archived tasks">Close</button></div>
                  {selectedProject && <div className="overflow-y-auto min-h-0"><DesignMediaTasks projectId={selectedProject.id} archived onPreview={item => { setArchivedOpen(false); setActiveMediaViewerItem(item) }} /></div>}
                  <p className="text-xs text-slate-400 my-2">Archived tasks in this project are read-only. Their sessions, branches and code remain untouched.</p>
                  {archivedLoading ? <p role="status">Loading archived tasks…</p> : archivedError ? <div role="alert">{archivedError} <button type="button" onClick={() => selectedProjectId && void loadArchivedTasks(selectedProjectId)}>Retry</button></div> : archivedTasks.length === 0 ? <p>No archived tasks.</p> : <ul className="overflow-y-auto min-h-0 space-y-2">{archivedTasks.map(row => <li key={row.id} className="p-3 rounded border border-slate-700"><strong className="block text-sm">{row.title}</strong><span className="text-xs text-slate-400">{row.status} · {row.workerName || 'Task'}</span></li>)}</ul>}
                </section>
              </div>}

              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 1: COMPACT MATRIX & EXPANDABLE DRAWER
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'matrix' && (
                <div className="flex-1 flex flex-col overflow-hidden p-3.5 space-y-3">

                  {/* Task rows */}
                  <div className="flex-1 overflow-y-auto space-y-3 pr-1" data-testid="orchestrate-task-list">
                    {selectedProject && <DesignMediaTasks projectId={selectedProject.id} onPreview={setActiveMediaViewerItem} />}
                    {renderCreativeThreads()}
                    {filteredTasks.length > 0 ? (
                      filteredTasks.filter(task => !isCreativeMediaTask(task)).map((t) => (
                        <MinimalTaskCard
                          key={t.id}
                          task={t}
                          isSelected={selectedTaskId === t.id}
                          {...selectionProps(t)}
                          isExpanded={expandedTaskId === t.id}
                          workspaceSlug={workspaceSlug}
                          onOpenWorkerDetail={(workerId) => {
                            void navigate({ ...projectPageLink('workers'), search: { section: 'workers', workerId } })
                          }}
                          onToggleExpand={() => setExpandedTaskId(expandedTaskId === t.id ? null : t.id)}
                          onSelect={() => handleSelectTask(t)}
                          onOpenChat={() => handleOpenTaskSession(t)}
                          onApprove={() => handleApproveTask(t.id)}
                          onIntegrate={() => handleIntegrateTask(t.id)}
                          integrationOperation={integrationForTask(t)}
                          onDelete={() => handleDeleteTask(t.id)}
                          onRefine={(fb, err) => handleRefineTask(t.id, fb, err)}
                          onPreviewDeliverable={(d, mode) => handleOpenDeliverableInMediaCenter(d, t, mode)}
                          onReopen={(fb) => handleReopenTask(t.id, fb)}
                          onComplete={() => handleCompleteTask(t.id)}
                          onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                          onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                          onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                          onOpenTaskModelChanger={handleOpenTaskModelChanger}
                          projectId={selectedProject?.id}
                          isApproving={approvingTaskIds.has(t.id)}
                          previousRuns={renderPreviousRuns(t)} integrationRecovery={renderIntegrationRecovery(t)}
                          taskError={taskActionErrors[t.id]}
                          onClearError={() => handleClearTaskError(t.id)}
                          modelOptions={modelOptions}
                          defaultImageModel={defaultImageModel}
                          defaultVideoModel={defaultVideoModel}
                          defaultAudioModel={defaultAudioModel}
                          imageModelOptions={imageModelOptions}
                          videoModelOptions={videoModelOptions}
                          audioModelOptions={audioModelOptions}
                        />
                      ))
                    ) : (
                      <div className="flex-1 flex flex-col items-center justify-center p-8 text-center border border-dashed border-slate-800 rounded-xl bg-[#080c16]/50">
                        <Code size={20} className="text-slate-600 mb-2" />
                        <h4 className="text-xs font-bold text-slate-300">No tasks active</h4>
                        <p className="text-[11px] text-slate-500 mt-1 mb-4">
                          Propose a task in plain English to have the AI organize the mission and branch for approval.
                        </p>
                        <button
                          onClick={() => setIsDeployModalOpen(true)}
                          className="swarm-new-task-cta px-4 py-2 rounded-lg text-xs font-semibold flex items-center gap-1.5"
                        >
                          <Plus size={13} aria-hidden="true" />
                          <span>New Task</span>
                        </button>
                      </div>
                    )}
                  </div>
                </div>
              )}

              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 2: MISSION PIPELINE KANBAN
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'kanban' && (
                <div className="flex-1 flex overflow-x-auto p-4 gap-3">
                  <section aria-label="Creative threads" className="w-80 shrink-0 min-w-0 overflow-y-auto space-y-3">
                    {selectedProject && <DesignMediaTasks projectId={selectedProject.id} onPreview={setActiveMediaViewerItem} />}
                    {renderCreativeThreads()}
                  </section>
                  {(
                    [
                      { key: 'queued', label: 'Pending Approval', color: 'amber' },
                      { key: 'running', label: 'In Progress', color: 'blue' },
                      { key: 'needs_review', label: 'Needs Review', color: 'amber' },
                      { key: 'blocked', label: 'Blocked', color: 'amber' },
                      { key: 'completed', label: 'Completed', color: 'emerald' },
                      { key: 'failed', label: 'Failed', color: 'rose' },
                    ] as const
                  ).map((col) => {
                    const colTasks = filteredTasks.filter((t) => {
                      if (isCreativeMediaTask(t)) return false
                      if (col.key === 'queued') return t.status === 'queued' || t.status === 'pending_approval' || t.status === 'planning'
                      if (col.key === 'running') return t.status === 'running' || t.status === 'in_progress'
                      if (col.key === 'failed') return t.status === 'failed' || t.status === 'rejected'
                      return t.status === col.key
                    })
                    return (
                      <div
                        key={col.key}
                        className="w-80 flex-shrink-0 flex flex-col rounded-xl border border-slate-800/80 bg-[#090d16]/70 p-3 space-y-2.5 overflow-hidden"
                      >
                        <div className="flex items-center justify-between pb-1.5 border-b border-slate-800/60">
                          <span className="text-xs font-bold text-white flex items-center gap-1.5">
                            <span
                              className={`h-2 w-2 rounded-sm ${
                                col.color === 'blue'
                                  ? 'bg-blue-400'
                                  : col.color === 'amber'
                                  ? 'bg-amber-400'
                                  : col.color === 'emerald'
                                  ? 'bg-emerald-400'
                                  : col.color === 'rose'
                                  ? 'bg-rose-400'
                                  : 'bg-slate-500'
                              }`}
                            />
                            <span>{col.label}</span>
                          </span>
                          <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-slate-800 text-slate-400">
                            {colTasks.length}
                          </span>
                        </div>

                        <div className="flex-1 overflow-y-auto space-y-2.5 pr-0.5">
                          {colTasks.map((t) => (
                            <MinimalTaskCard
                              key={t.id}
                              task={t}
                              isSelected={selectedTaskId === t.id}
                          {...selectionProps(t)}
                              workspaceSlug={workspaceSlug}
                              onOpenWorkerDetail={(workerId) => {
                                void navigate({ ...projectPageLink('workers'), search: { section: 'workers', workerId } })
                              }}
                              onSelect={() => handleSelectTask(t)}
                              onOpenChat={() => handleOpenTaskSession(t)}
                              onApprove={() => handleApproveTask(t.id)}
                              onIntegrate={() => handleIntegrateTask(t.id)}
                              integrationOperation={integrationForTask(t)}
                              onDelete={() => handleDeleteTask(t.id)}
                              onRefine={(fb, err) => handleRefineTask(t.id, fb, err)}
                              onPreviewDeliverable={(d, mode) => handleOpenDeliverableInMediaCenter(d, t, mode)}
                              onReopen={(fb) => handleReopenTask(t.id, fb)}
                              onComplete={() => handleCompleteTask(t.id)}
                              onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                              onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                              onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                              onOpenTaskModelChanger={handleOpenTaskModelChanger}
                              projectId={selectedProject?.id}
                              isApproving={approvingTaskIds.has(t.id)}
                              previousRuns={renderPreviousRuns(t)} integrationRecovery={renderIntegrationRecovery(t)}
                              taskError={taskActionErrors[t.id]}
                              onClearError={() => handleClearTaskError(t.id)}
                              modelOptions={modelOptions}
                              defaultImageModel={defaultImageModel}
                              defaultVideoModel={defaultVideoModel}
                              defaultAudioModel={defaultAudioModel}
                              imageModelOptions={imageModelOptions}
                              videoModelOptions={videoModelOptions}
                              audioModelOptions={audioModelOptions}
                            />
                          ))}
                          {colTasks.length === 0 && (
                            <div className="p-4 text-center text-[10px] text-slate-600 border border-dashed border-slate-850 rounded-lg">
                              No {col.label.toLowerCase()} tasks
                            </div>
                          )}
                        </div>
                      </div>
                    )
                  })}
                </div>
              )}

              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 3: AUTONOMOUS WORKER FLEET
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'fleet' && (
                <div className="flex-1 overflow-y-auto p-4 space-y-4">
                  <div className="flex items-center justify-between">
                    <div>
                      <h3 className="text-sm font-bold text-white">Active Worker Sessions</h3>
                      <p className="text-[11px] text-slate-400">
                        Autonomous worker sessions executing across your repositories
                      </p>
                    </div>
                    <button
                      onClick={() => setIsDeployModalOpen(true)}
                      className="swarm-new-task-cta flex items-center gap-1.5 px-3 py-1 rounded-lg text-xs font-medium"
                    >
                      <Plus size={12} aria-hidden="true" />
                      <span>New Task</span>
                    </button>
                  </div>

                  {deployedWorkers.length > 0 ? (
                    <div className="swarm-content-grid grid grid-cols-2 gap-3">
                      {deployedWorkers.map((worker) => (
                        <div
                          key={worker.id}
                          className="p-3 rounded-xl border border-slate-800/80 bg-[#0a0f1d] space-y-2.5"
                        >
                          <div className="flex items-center justify-between">
                            <div className="flex items-center gap-2.5">
                              <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-blue-500/10 text-blue-400 border border-blue-500/20">
                                <Bot size={15} />
                              </div>
                              <div>
                                <h4 className="text-xs font-bold text-white truncate max-w-[180px]">{worker.name}</h4>
                                <p className="text-[10px] text-slate-400 truncate max-w-[180px]">{worker.role}</p>
                              </div>
                            </div>
                            <span
                              className={`px-2 py-0.5 rounded text-[9px] font-mono font-semibold uppercase ${
                                worker.status === 'active'
                                  ? 'bg-blue-500/20 text-blue-400 border border-blue-500/30'
                                  : 'bg-slate-800 text-slate-400'
                              }`}
                            >
                              {worker.status}
                            </span>
                          </div>

                          <div className="flex items-center justify-between text-[11px] border-t border-slate-800/80 pt-2 text-slate-400 font-mono">
                            <span>{worker.scheduleLabel}</span>
                            <span className="text-blue-400 font-bold">{worker.activeJobsCount} active</span>
                          </div>
                        </div>
                      ))}
                    </div>
                  ) : (
                    <div className="p-6 text-center border border-dashed border-slate-800 rounded-xl">
                      <Bot size={22} className="mx-auto text-slate-600 mb-2" />
                      <h4 className="text-xs font-bold text-slate-300">No active worker sessions</h4>
                      <p className="text-[11px] text-slate-500 mt-1">Propose a task to launch an autonomous worker session.</p>
                    </div>
                  )}

                  {/* Tasks List in Fleet */}
                  <div className="pt-2 space-y-3">
                    <h4 className="text-xs font-bold text-white">Project Tasks ({filteredTasks.length})</h4>
                    {selectedProject && <DesignMediaTasks projectId={selectedProject.id} onPreview={setActiveMediaViewerItem} />}
                    {renderCreativeThreads()}
                    <div className="space-y-2.5">
                      {filteredTasks.filter(task => !isCreativeMediaTask(task)).map((task) => (
                        <MinimalTaskCard
                          key={task.id}
                          task={task}
                          isSelected={selectedTaskId === task.id}
                          {...selectionProps(task)}
                          workspaceSlug={workspaceSlug}
                          onOpenWorkerDetail={(workerId) => {
                            void navigate({ ...projectPageLink('workers'), search: { section: 'workers', workerId } })
                          }}
                          onSelect={() => handleSelectTask(task)}
                          onOpenChat={() => handleOpenTaskSession(task)}
                          onApprove={() => handleApproveTask(task.id)}
                          onIntegrate={() => handleIntegrateTask(task.id)}
                          integrationOperation={integrationForTask(task)}
                          onDelete={() => handleDeleteTask(task.id)}
                          onRefine={(fb, err) => handleRefineTask(task.id, fb, err)}
                          onPreviewDeliverable={(d, mode) => handleOpenDeliverableInMediaCenter(d, task, mode)}
                          onReopen={(fb) => handleReopenTask(task.id, fb)}
                          onComplete={() => handleCompleteTask(task.id)}
                          onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                          onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                          onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                          onOpenTaskModelChanger={handleOpenTaskModelChanger}
                          projectId={selectedProject?.id}
                          isApproving={approvingTaskIds.has(task.id)}
                          previousRuns={renderPreviousRuns(task)} integrationRecovery={renderIntegrationRecovery(task)}
                          taskError={taskActionErrors[task.id]}
                          onClearError={() => handleClearTaskError(task.id)}
                          modelOptions={modelOptions}
                          defaultImageModel={defaultImageModel}
                          defaultVideoModel={defaultVideoModel}
                          defaultAudioModel={defaultAudioModel}
                          imageModelOptions={imageModelOptions}
                          videoModelOptions={videoModelOptions}
                          audioModelOptions={audioModelOptions}
                        />
                      ))}
                      {filteredTasks.length === 0 && (
                        <div className="p-6 text-center text-xs text-slate-500 border border-dashed border-slate-800 rounded-lg">
                          No tasks found.
                        </div>
                      )}
                    </div>
                  </div>
                </div>
              )}

              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 4: SPLIT STUDIO CONSOLE
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'split' && (
                <div className="flex-1 flex overflow-hidden">
                  {/* Left Column: Tasks List */}
                  <div className="w-1/2 border-r border-slate-800/80 flex flex-col p-3 overflow-y-auto space-y-2">
                    <div className="text-xs font-bold text-white pb-1">Tasks ({filteredTasks.length})</div>
                    {selectedProject && <DesignMediaTasks projectId={selectedProject.id} onPreview={setActiveMediaViewerItem} />}
                    {renderCreativeThreads()}
                    {filteredTasks.filter(task => !isCreativeMediaTask(task)).map((t) => {
                      const isSel = selectedTaskId === t.id
                      const isWorker = Boolean(t.workerId?.trim() || t.worker_id?.trim())
                      return (
                        <div
                          key={t.id}
                          onClick={() => handleSelectTask(t)}
                          className={`p-3 rounded-lg border transition-all cursor-pointer ${
                            isSel
                              ? 'bg-blue-950/20 border-blue-500/50 text-white shadow-sm'
                              : 'bg-[#0a0f1d] border-slate-800/80 hover:border-slate-700 text-slate-300'
                          }`}
                        >
                          <div className="flex items-center justify-between gap-2">
                            <label className="flex items-center gap-1" onClick={e => e.stopPropagation()}><input type="checkbox" checked={markedTaskIds.has(t.id)} onChange={() => toggleMarked(t.id)} aria-label={`Select task ${t.title}`} /></label>
                            <span className="text-xs font-semibold leading-snug truncate">{t.title}</span>
                            {isWorker && (
                              <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-indigo-950/70 text-indigo-300 border border-indigo-500/30 font-semibold shrink-0" data-testid="worker-tag">
                                Worker
                              </span>
                            )}
                          </div>
                          <div className="flex items-center justify-between text-[10px] font-mono text-slate-500 mt-2">
                            {isWorker ? (
                              <a
                                href={swarmWorkerHref(workspaceSlug, (t.workerId?.trim() || t.worker_id?.trim())!)}
                                onClick={(e) => {
                                  e.stopPropagation()
                                  void navigate({ ...projectPageLink('workers'), search: { section: 'workers', workerId: (t.workerId?.trim() || t.worker_id?.trim())! } })
                                }}
                                className="text-indigo-400 hover:text-indigo-300 underline flex items-center gap-1 cursor-pointer"
                                data-testid="worker-link"
                              >
                                <Bot size={10} />
                                <span>{t.worker_name || (t.workerName && !t.workerName.startsWith('@') ? t.workerName : undefined) || t.workerId || t.worker_id}</span>
                              </a>
                            ) : (
                              <span>{t.workerName}</span>
                            )}
                            <span className="text-blue-400 font-bold">{t.status}</span>
                          </div>
                        </div>
                      )
                    })}
                    {filteredTasks.length === 0 && (
                      <div className="p-6 text-center text-xs text-slate-500 border border-dashed border-slate-800 rounded-lg">
                        No tasks found.
                      </div>
                    )}
                  </div>

                  {/* Right Column: Live Inspector */}
                  <div className="w-1/2 flex flex-col p-4 overflow-y-auto space-y-3">
                    {selectedTaskForSplit ? (
                      <MinimalTaskCard
                        task={selectedTaskForSplit}
                        isSelected={true}
                        {...selectionProps(selectedTaskForSplit)}
                        workspaceSlug={workspaceSlug}
                        onOpenWorkerDetail={(workerId) => {
                          void navigate({ ...projectPageLink('workers'), search: { section: 'workers', workerId } })
                        }}
                        onSelect={() => handleSelectTask(selectedTaskForSplit)}
                        onOpenChat={() => handleOpenTaskSession(selectedTaskForSplit)}
                        onApprove={() => handleApproveTask(selectedTaskForSplit.id)}
                        onIntegrate={() => handleIntegrateTask(selectedTaskForSplit.id)}
                        integrationOperation={integrationForTask(selectedTaskForSplit)}
                        onDelete={() => handleDeleteTask(selectedTaskForSplit.id)}
                        onRefine={(fb, err) => handleRefineTask(selectedTaskForSplit.id, fb, err)}
                        onPreviewDeliverable={(d, mode) => handleOpenDeliverableInMediaCenter(d, selectedTaskForSplit, mode)}
                        onReopen={(fb) => handleReopenTask(selectedTaskForSplit.id, fb)}
                        onComplete={() => handleCompleteTask(selectedTaskForSplit.id)}
                        onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                        onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                        onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                        onOpenTaskModelChanger={handleOpenTaskModelChanger}
                        projectId={selectedProject?.id}
                        isApproving={approvingTaskIds.has(selectedTaskForSplit.id)}
                        previousRuns={renderPreviousRuns(selectedTaskForSplit)} integrationRecovery={renderIntegrationRecovery(selectedTaskForSplit)}
                        taskError={taskActionErrors[selectedTaskForSplit.id]}
                        onClearError={() => handleClearTaskError(selectedTaskForSplit.id)}
                        modelOptions={modelOptions}
                        defaultImageModel={defaultImageModel}
                        defaultVideoModel={defaultVideoModel}
                        defaultAudioModel={defaultAudioModel}
                        imageModelOptions={imageModelOptions}
                        videoModelOptions={videoModelOptions}
                        audioModelOptions={audioModelOptions}
                      />
                    ) : (
                      <div className="flex-1 flex items-center justify-center text-xs text-slate-500">
                        Select a task to inspect details
                      </div>
                    )}
                  </div>
                </div>
              )}

              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 5: TIMELINE ACTIVITY STREAM
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'timeline' && (
                <div className="flex-1 overflow-y-auto p-4 space-y-3">
                  <div className="text-xs font-bold text-white pb-1">Activity Stream ({filteredTasks.length})</div>
                  {selectedProject && <DesignMediaTasks projectId={selectedProject.id} onPreview={setActiveMediaViewerItem} />}
                  {renderCreativeThreads()}
                  {filteredTasks.filter(task => !isCreativeMediaTask(task)).map((t) => (
                    <MinimalTaskCard
                      key={t.id}
                      task={t}
                      isSelected={selectedTaskId === t.id}
                      workspaceSlug={workspaceSlug}
                      onOpenWorkerDetail={(workerId) => {
                        void navigate({ ...projectPageLink('workers'), search: { section: 'workers', workerId } })
                      }}
                      onSelect={() => handleSelectTask(t)}
                      onOpenChat={() => handleOpenTaskSession(t)}
                      onApprove={() => handleApproveTask(t.id)}
                      onIntegrate={() => handleIntegrateTask(t.id)}
                      integrationOperation={integrationForTask(t)}
                      onDelete={() => handleDeleteTask(t.id)}
                      onRefine={(fb, err) => handleRefineTask(t.id, fb, err)}
                      onPreviewDeliverable={(d, mode) => handleOpenDeliverableInMediaCenter(d, t, mode)}
                      onReopen={(fb) => handleReopenTask(t.id, fb)}
                      onComplete={() => handleCompleteTask(t.id)}
                      onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                      onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                      onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                      onOpenTaskModelChanger={handleOpenTaskModelChanger}
                      projectId={selectedProject?.id}
                      isApproving={approvingTaskIds.has(t.id)}
                      previousRuns={renderPreviousRuns(t)} integrationRecovery={renderIntegrationRecovery(t)}
                      taskError={taskActionErrors[t.id]}
                      onClearError={() => handleClearTaskError(t.id)}
                      modelOptions={modelOptions}
                      defaultImageModel={defaultImageModel}
                      defaultVideoModel={defaultVideoModel}
                      defaultAudioModel={defaultAudioModel}
                      imageModelOptions={imageModelOptions}
                      videoModelOptions={videoModelOptions}
                      audioModelOptions={audioModelOptions}
                    />
                  ))}
                  {filteredTasks.length === 0 && (
                    <div className="p-8 text-center border border-dashed border-slate-800 rounded-xl text-xs text-slate-500">
                      No task activity recorded yet.
                    </div>
                  )}
                </div>
              )}
            </div>
          </>
        )}
      </main>}

      {/* ─────────────────────────────────────────────────────────────
          PANEL 3: RIGHT PANEL (CANONICAL DESKTOP V3 AI CHAT SIDEBAR)
         ───────────────────────────────────────────────────────────── */}
      {workerChatOpen && workerChatError && <div role="alert" className="max-w-sm p-4 text-sm text-red-300">{workerChatError}<button className="ml-2 underline" onClick={() => setWorkerChatError('')}>Dismiss</button></div>}
      {appliedConversationRouteScope.current === conversationRouteScope && activeSessionId && selectedProject && ((activeTaskId && tasks.some(task => task.id === activeTaskId && (extractTaskSessionIds(task).includes(activeSessionId) || taskOutcome(task).repairSessionId === activeSessionId))) || (admittedParentId && activeSessionId === admittedParentId)) ? (
        // Keep the chat bounded to the page, below the optional worker header.
        <div className="swarm-conversation-panel flex min-h-0 shrink-0 flex-col">
        {activeNavTab === 'workers' && workerChatOpen && <div className="flex max-w-[440px] shrink-0 items-center justify-between gap-3 p-3 text-xs text-slate-300"><span>{workerCreationRequested ? 'Add worker: describe its job to Orchestrator below. Nothing runs until you approve.' : 'Discuss this worker with Orchestrator'}</span><button onClick={() => { setWorkerChatOpen(false); setWorkerCreationRequested(false) }}>Close</button></div>}
        <OrchestratorChatSidebar
        onOpenMediaArtifact={(artifact) => {
          const selected = toMediaLibraryItem(artifact)
          if (!selected) return false
          setActiveMediaViewerItem(selected)
          setMediaViewerInitialMode(null)
          return true
        }}
          workspaceSlug={workspaceSlug}
          key={activeSessionId}
          repairSession={tasks.some(task => taskOutcome(task).repairSessionId === activeSessionId)}
          sessionId={activeSessionId}
          project={selectedProject}
          projectSegment={selectedProjectSegment}
          activeTask={activeTask}
          selectedTask={null}
          attachedTasks={tasks.filter(task => attachedTaskIds.includes(task.id))}
          onRemoveAttachedTask={id => setAttachedTaskIds(ids => ids.filter(value => value !== id))}
          allTasks={tasks}
          onBackToOrchestrator={handleBackToOrchestrator}
          onDeselectTask={handleDeselectTask}
          selectedWorker={selectedWorker}
          currentSelectedWorker={() => selectedWorkerRef.current}
          onDeselectWorker={clearConsumedWorker}
          creatingWorker={workerCreationRequested}
          onWorkerCreationSent={() => setWorkerCreationRequested(false)}
        />
        </div>
      ) : (
        <aside className="swarm-conversation-panel swarm-empty-conversation relative flex w-[440px] flex-shrink-0 flex-col items-center justify-center p-6 text-center rounded-3xl border border-slate-800/80 bg-[#0d121f] text-xs text-slate-400 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
          <div className="h-12 w-12 rounded-2xl bg-blue-600/20 border border-blue-500/30 flex items-center justify-center text-blue-400 mb-3 shadow-lg shadow-blue-600/10">
            <Bot size={22} />
          </div>
          <h3 className="font-bold text-sm text-white mb-1">{selectedProject?.name || 'Welcome to Swarm'}</h3>
          <p className="text-slate-400 text-xs max-w-xs leading-relaxed">
            {conversationError ? 'Unable to open this conversation. Retry sessions in the sidebar.' : routeConversationId && selectedProject ? 'Opening conversation…' : workerChatOpen ? 'Opening Worker Orchestrator… If the connection fails, retry Add worker or Ask Orchestrator.' : selectedProject ? 'Choose a conversation or start a new session.' : 'Create your first project. Workspaces can be added later.'}
          </p>
          {selectedProject && !routeConversationId && <ul aria-label="Project conversations">{conversations.sessions.map(session => <li key={session.id}><Link {...projectLink(selectedProject.id, session.id)}>{session.title || 'New conversation'}</Link></li>)}</ul>}
          {selectedProject ? <button type="button" disabled={creatingConversation} onClick={() => { void newConversation() }}>New session</button> : <button type="button" onClick={() => { setIsOnboardingActive(true); setActiveNavTab('projects') }}>Create a project</button>}
        </aside>
      )}

      {/* ─────────────────────────────────────────────────────────────
          MODAL: NEW TASK (VISUAL INTENT, MEDIA & AUTO-APPROVE)
         ───────────────────────────────────────────────────────────── */}
      {isDeployModalOpen && (
        <div className="swarm-deploy-backdrop fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-3 backdrop-blur-md" onClick={event => { if (event.target === event.currentTarget) setIsDeployModalOpen(false) }}>
          <div ref={deployDialogRef} tabIndex={-1} role="dialog" aria-modal="true" aria-label="Deploy Autonomous Task" className="swarm-local-dialog relative flex max-w-xl w-full flex-col p-6 rounded-2xl border border-slate-800 bg-[#0d121f] shadow-2xl space-y-4">
            {/* Header */}
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div className="flex items-center gap-2">
                <Sparkles size={16} className="text-blue-400" />
                <h3 className="text-sm font-bold text-white">Deploy Autonomous Task</h3>
              </div>
              <button
                type="button"
                aria-label="Close deploy task dialog"
                onClick={() => setIsDeployModalOpen(false)}
                className="rounded-full p-1 text-slate-400 hover:bg-slate-800 hover:text-white transition-colors"
              >
                <X size={16} />
              </button>
            </div>

            {/* Keep coding work distinct from media generation. */}
            <div className="space-y-3">
              <fieldset className="min-w-0 rounded-xl border border-slate-800 bg-slate-950 p-2">
                <legend className="px-2 text-[11px] font-semibold text-slate-300">Coding</legend>
                <div className="swarm-dialog-intents grid grid-cols-3 gap-1 text-[11px]">
                  <button
                    type="button"
                    aria-pressed={taskIntent === 'code' && featureSize === 'small'}
                    onClick={() => {
                      setTaskIntent('code')
                      setFeatureSize('small')
                    }}
                    className={`flex min-w-0 items-center justify-center px-2 py-2 rounded-lg font-semibold transition-colors ${
                      taskIntent === 'code' && featureSize === 'small'
                        ? 'bg-blue-600 text-white shadow'
                        : 'text-slate-400 hover:text-white hover:bg-slate-900'
                    }`}
                    data-testid="deploy-tab-small-feature"
                  >
                    <span>Small Feature/Fix</span>
                  </button>
                  <button
                    type="button"
                    aria-pressed={taskIntent === 'code' && featureSize === 'big'}
                    onClick={() => {
                      setTaskIntent('code')
                      setFeatureSize('big')
                    }}
                    className={`flex min-w-0 items-center justify-center px-2 py-2 rounded-lg font-semibold transition-colors ${
                      taskIntent === 'code' && featureSize === 'big'
                        ? 'bg-blue-600 text-white shadow'
                        : 'text-slate-400 hover:text-white hover:bg-slate-900'
                    }`}
                    data-testid="deploy-tab-big-feature"
                  >
                    <span>Big Feature</span>
                  </button>
                  <button
                    type="button"
                    aria-pressed={taskIntent === 'audit'}
                    onClick={() => setTaskIntent('audit')}
                    className={`flex min-w-0 items-center justify-center gap-1.5 px-2 py-2 rounded-lg font-semibold transition-colors ${
                      taskIntent === 'audit'
                        ? 'bg-blue-600 text-white shadow'
                        : 'text-slate-400 hover:text-white hover:bg-slate-900'
                    }`}
                    data-testid="deploy-tab-audit"
                  >
                    <Search size={12} aria-hidden="true" className="shrink-0" />
                    <span>Audit</span>
                  </button>
                </div>
              </fieldset>
              <fieldset className="min-w-0 rounded-xl border border-slate-800 bg-slate-950 p-2">
                <legend className="px-2 text-[11px] font-semibold text-slate-300">Media</legend>
                <div className="swarm-dialog-intents grid grid-cols-3 gap-1 text-[11px]">
                  <button
                    type="button"
                    aria-pressed={taskIntent === 'image'}
                    onClick={() => setTaskIntent('image')}
                    className={`flex min-w-0 items-center justify-center gap-1.5 px-2 py-2 rounded-lg font-semibold transition-colors ${
                      taskIntent === 'image'
                        ? 'bg-blue-600 text-white shadow'
                        : 'text-slate-400 hover:text-white hover:bg-slate-900'
                    }`}
                    data-testid="deploy-tab-image"
                  >
                    <ImageIcon size={12} aria-hidden="true" className="shrink-0" />
                    <span>Image</span>
                  </button>
                  <button
                    type="button"
                    aria-pressed={taskIntent === 'video'}
                    onClick={() => setTaskIntent('video')}
                    className={`flex min-w-0 items-center justify-center gap-1.5 px-2 py-2 rounded-lg font-semibold transition-colors ${
                      taskIntent === 'video'
                        ? 'bg-blue-600 text-white shadow'
                        : 'text-slate-400 hover:text-white hover:bg-slate-900'
                    }`}
                    data-testid="deploy-tab-video"
                  >
                    <Film size={12} aria-hidden="true" className="shrink-0" />
                    <span>Video</span>
                  </button>
                  <button
                    type="button"
                    aria-pressed={taskIntent === 'sound'}
                    onClick={() => setTaskIntent('sound')}
                    className={`flex min-w-0 items-center justify-center gap-1.5 px-2 py-2 rounded-lg font-semibold transition-colors ${
                      taskIntent === 'sound'
                        ? 'bg-blue-600 text-white shadow'
                        : 'text-slate-400 hover:text-white hover:bg-slate-900'
                    }`}
                    data-testid="deploy-tab-sound"
                  >
                    <Volume2 size={12} aria-hidden="true" className="shrink-0" />
                    <span>Sounds</span>
                  </button>
                </div>
              </fieldset>
            </div>

            {/* Agent preview belongs to code/audit only; media settings own their model selection. */}
            {(taskIntent === 'code' || taskIntent === 'audit') && (
            <div className="flex flex-col gap-2 p-3 rounded-xl bg-slate-950/80 border border-slate-800 text-[11px] font-mono" data-testid="deploy-modal-impending-preview">
              <div className="flex items-center justify-between gap-2 flex-wrap">
                <div className="flex items-center gap-2">
                  <span className="text-[10px] uppercase font-bold text-slate-400 tracking-wider flex items-center gap-1">
                    <Bot size={12} className="text-blue-400" />
                    <span>Impending Agent:</span>
                  </span>
                  <span className="font-bold text-white px-2 py-0.5 rounded bg-slate-900 border border-slate-700">
                    {taskIntent === 'code'
                      ? (featureSize === 'big' ? '@swarm (Swarm)' : '@coder (Coder)')
                      : '@finder (Finder)'}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <span className="text-slate-400">Model:</span>
                  <strong className="text-white font-bold">{resolvedDeployModel}</strong>
                  <span className={`text-[9px] px-1.5 py-0.5 rounded border font-semibold ${
                    isDeployModelOverridden
                      ? 'bg-amber-950/60 text-amber-300 border-amber-500/40'
                      : 'bg-slate-800 text-slate-400 border-slate-700'
                  }`}>
                    {isDeployModelOverridden ? 'Task Override' : 'Account Default'}
                  </span>
                  {(taskIntent === 'code' || taskIntent === 'audit') && (
                    <div className="flex items-center gap-1.5">
                      <button
                        type="button"
                        onClick={handleOpenDeployModelChanger}
                        className="text-[10px] text-blue-400 hover:text-blue-300 font-semibold underline decoration-dotted flex items-center gap-1"
                        data-testid="deploy-modal-change-model-btn"
                      >
                        <Settings2 size={11} />
                        <span>Change</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          const targetAgentName = taskIntent === 'code'
                            ? (featureSize === 'big' ? 'swarm' : 'system-coder')
                            : 'system-finder'
                          handleOpenAgentSettings(targetAgentName)
                        }}
                        className="text-[10px] text-slate-400 hover:text-slate-300 font-semibold flex items-center gap-1"
                        data-testid="modal-open-agents-btn"
                        title="Configure Default in /agents"
                      >
                        <span>(/agents)</span>
                      </button>
                      {newTaskModelOverride && (
                        <button
                          type="button"
                          onClick={() => {
                            setNewTaskModelOverride('')
                            setNewTaskModelScope(null)
                          }}
                          className="text-[10px] text-amber-400 hover:text-amber-300 font-semibold underline decoration-dotted"
                          data-testid="deploy-modal-reset-override-btn"
                        >
                          Reset
                        </button>
                      )}
                    </div>
                  )}
                </div>
              </div>

              {deployPreviewQuery.isError && (
                <div className="p-2 rounded bg-amber-950/40 border border-amber-500/30 text-amber-300 text-[10px] font-mono flex items-center gap-1.5" data-testid="deploy-modal-preview-error">
                  <AlertTriangle size={12} className="text-amber-400 shrink-0" />
                  <span>Model preview unavailable: {(deployPreviewQuery.error as Error)?.message || 'Failed to load model preview'}</span>
                </div>
              )}

              <p className="text-[10px] text-slate-400 leading-normal">
                {taskIntent === 'code' && featureSize === 'small' && 'Code and test authoring on an isolated worktree via @coder; parent-run validation.'}
                {taskIntent === 'code' && featureSize === 'big' && <>
                  Swarm handles big features directly, including Bash and test execution subject to configured tools and permissions.{' '}
                  <Link {...projectPageLink('settings')} hash="permissions" onClick={() => setIsDeployModalOpen(false)} className="underline hover:text-slate-200">/permissions</Link> (no permissions are changed automatically).
                </>}
                {taskIntent === 'audit' && 'Read-only architectural audit and code exploration via @finder, producing an audit report.'}
              </p>
            </div>
            )}

            {/* Tagged / Attached Media Bar */}
            {taggedMedia.length > 0 && (
              <div className="flex flex-wrap items-center gap-1.5 p-2 rounded-xl bg-blue-950/40 border border-blue-500/30">
                <div className="flex items-center gap-1 text-[11px] font-bold text-blue-400 mr-1">
                  <Tag size={12} className="fill-current" />
                  <span>Attached Media ({taggedMedia.length}):</span>
                </div>
                {taggedMedia.map((m) => (
                  <div key={m.id} className="flex items-center gap-1.5 px-2 py-0.5 rounded-lg bg-blue-900/60 border border-blue-400/40 text-blue-200 text-[11px]">
                    {m.kind === 'image' && <ImageIcon size={11} />}
                    {m.kind === 'video' && <Film size={11} />}
                    {m.kind === 'audio' && <Music size={11} />}
                    {m.kind === 'doc' && <FileText size={11} />}
                    <span className="truncate max-w-[120px]">{m.title || m.filename}</span>
                    <button
                      type="button"
                      onClick={() => toggleTagMediaRef(m)}
                      className="text-blue-300 hover:text-white"
                      title="Remove attachment"
                    >
                      <X size={10} />
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  onClick={() => setTaggedMedia([])}
                  className="text-[10px] text-blue-400/80 hover:text-blue-200 underline ml-auto"
                >
                  Clear all
                </button>
              </div>
            )}

            {/* Form inputs */}
            <div className="space-y-3 text-xs">
              <div>
                <label className="block text-[11px] text-slate-400 font-medium mb-1">
                  {taskIntent === 'code' && (featureSize === 'big' ? 'Big Feature Architecture & Requirements' : 'Small Feature / Fix Instructions')}
                  {taskIntent === 'image' && 'Image prompt'}
                  {taskIntent === 'video' && 'Video prompt'}
                  {taskIntent === 'sound' && 'Audio Soundtrack / Mood Prompt'}
                  {taskIntent === 'audit' && 'Investigation Objective & Target Questions'}
                </label>
                <textarea
                  value={newTaskPrompt}
                  onChange={(e) => setNewTaskPrompt(e.target.value)}
                  rows={3}
                  placeholder={
                    taskIntent === 'code'
                      ? (featureSize === 'big'
                          ? 'e.g. overhaul the project architecture, multi-workspace routing and plan orchestration...'
                          : 'e.g. fix the sidebar layout and theme colors, or add OAuth login with test coverage...')
                      : taskIntent === 'image'
                      ? 'e.g. futuristic neon AI developer workstation in isometric pixel art with dark mood lighting...'
                      : taskIntent === 'video'
                      ? 'e.g. dramatic drone shot flying over a futuristic neon city at dusk with volumetric fog, ambient engine drone and synth pads...'
                      : taskIntent === 'sound'
                      ? 'e.g. energetic techno synthwave soundtrack at 128 BPM with driving bass and hi-hats...'
                      : 'e.g. investigate why pebble database locks on restart and audit connection pool handling...'
                  }
                  className="w-full rounded-lg bg-slate-950 border border-slate-800 p-3 text-white placeholder-slate-600 focus:outline-none focus:border-blue-500/60 resize-none text-xs leading-relaxed font-sans"
                  onDragOver={(e) => e.preventDefault()}
                  onDrop={(e) => {
                    e.preventDefault()
                    if (e.dataTransfer.files && e.dataTransfer.files.length > 0) {
                      void handleFileUpload(e.dataTransfer.files)
                    }
                  }}
                  onPaste={(e) => {
                    if (e.clipboardData.files && e.clipboardData.files.length > 0) {
                      void handleFileUpload(e.clipboardData.files)
                    }
                  }}
                />
                <div className={taskIntent === 'image' || taskIntent === 'video' ? 'mt-1.5 flex flex-wrap items-center justify-between gap-2 text-xs' : 'mt-1.5 flex items-center justify-between text-[11px]'}>
                  <div className="flex items-center gap-1.5 text-slate-400">
                    <Paperclip size={12} className="text-blue-400" />
                    <span>{taskIntent === 'image' || taskIntent === 'video' ? 'Attach' : 'Attach media from project shelf or upload:'}</span>
                  </div>
                  <div className="flex items-center gap-1.5">
                    <label className={`flex items-center gap-1 px-2 rounded bg-blue-600/20 text-blue-300 hover:bg-blue-600/30 border border-blue-500/30 cursor-pointer font-semibold transition focus-within:outline focus-within:outline-blue-400 ${taskIntent === 'image' || taskIntent === 'video' ? 'min-h-9' : 'py-0.5'}`}>
                      <Upload size={10} />
                      <span>{isUploadingMedia ? 'Uploading...' : 'Upload'}</span>
                      <input
                        type="file"
                        multiple
                        accept={taskIntent === 'video' ? 'image/png,image/jpeg,.png,.jpg,.jpeg' : undefined}
                        aria-label="Upload attachment"
                        className={taskIntent === 'image' || taskIntent === 'video' ? 'sr-only' : 'hidden'}
                        onChange={(e) => void handleFileUpload(e.target.files)}
                      />
                    </label>
                    <button
                      type="button"
                      onClick={() => { setShelfUploadError(null); setIsPasteDocOpen(true) }}
                      className={`flex items-center gap-1 px-2 rounded bg-slate-800 text-slate-300 hover:text-white border border-slate-700 font-semibold transition ${taskIntent === 'image' || taskIntent === 'video' ? 'min-h-9' : 'py-0.5'}`}
                    >
                      <FileText size={10} />
                      <span>Paste Doc</span>
                    </button>
                    <button
                      type="button"
                      onClick={() => setShowFullMediaCenter(true)}
                      className={`flex items-center gap-1 px-2 rounded bg-slate-800 text-slate-300 hover:text-white border border-slate-700 font-semibold transition ${taskIntent === 'image' || taskIntent === 'video' ? 'min-h-9' : 'py-0.5'}`}
                    >
                      <Film size={10} />
                      <span>Browse Media</span>
                    </button>
                  </div>
                </div>
              </div>

              {/* Visual Selectors for Image Intent */}
              {taskIntent === 'image' && (
                <div className="space-y-3 p-3 rounded-lg bg-slate-950/70 border border-slate-800">
                  {/* Image Model Selector */}
                  <div>
                    <div className="flex items-center justify-between mb-1">
                      <label className="block text-xs text-slate-400">Model</label>
                      <div className="flex items-center gap-2">
                        {selectedImageOption && !selectedImageOption.ready ? (
                          <span className="text-[10px] text-amber-400 flex items-center gap-1 font-mono">
                            <AlertTriangle size={10} /> {selectedImageOption.reason || 'Not configured'}
                          </span>
                        ) : (
                          <span className="text-[10px] text-emerald-400 flex items-center gap-1 font-mono">
                            ✓ Connected
                          </span>
                        )}
                      </div>
                    </div>
                    {imageModelOptions.length === 0 && mediaCatalogLoaded ? (
                      <div className="rounded bg-slate-900 border border-amber-500/30 p-2 text-amber-300 text-[11px] font-mono flex items-center gap-2">
                        <AlertTriangle size={13} className="shrink-0 text-amber-400" />
                        <span>No image models connected. Add Google or OpenAI key in Settings.</span>
                      </div>
                    ) : (
                      <select
                        aria-label="Image Model"
                        value={selectedImageModel}
                        onChange={(e) => handleImageModelChange(e.target.value)}
                        className="min-h-9 w-full min-w-0 rounded bg-slate-900 border border-slate-700 px-2.5 py-1.5 text-white text-sm focus:outline-none focus:border-blue-500"
                      >
                        {imageModelOptions.map((opt) => (
                          <option key={opt.id} value={opt.id} disabled={!opt.ready}>
                            {opt.label}{opt.id === defaultImageModel ? ' (Default)' : ''}{!opt.ready ? ` (Unavailable${opt.reason ? `: ${opt.reason}` : ''})` : ''}
                          </option>
                        ))}
                      </select>
                    )}

                    {imageDefaultError && <p role="alert" className="text-xs text-amber-300">{imageDefaultError}</p>}
                    {selectedImageModel && <MediaTaskDefault isDefault={selectedImageModel === defaultImageModel} disabled={isSavingModelChoice || !selectedImageOption?.ready} saving={isSavingModelChoice} onSave={() => void handleSetImageAsDefault(selectedImageModel)} />}
                  </div>

                  <div className="grid grid-cols-2 sm:grid-cols-3 gap-2">
                    <MediaTaskSelect label="Size" value={imageResolution} values={['1k', '2k', '4k']} onChange={value => setImageResolution(value as '1k' | '2k' | '4k')} />
                    <MediaTaskSelect label="Ratio" value={imageAspectRatio} values={IMAGE_ASPECT_RATIOS.map(option => option.ratio)} onChange={value => setImageAspectRatio(value as typeof imageAspectRatio)} />
                    <MediaTaskSelect label="Images" value={imageVariants} values={[1, 2, 4, 5, 10, 25]} onChange={value => setImageVariants(Number(value))} />
                  </div>

                  <ImagePromptControls
                    count={imageVariants}
                    aiVariants={imagePromptState.aiVariants}
                    onChange={(aiVariants) => dispatchImagePrompt({ type: 'choice', aiVariants })}
                  />

                  <MediaTaskCost total={imagePricingInfo.totalPrice} approximate={!imagePricingInfo.isVerified} details={imagePricingInfo.formattedSummary} />
                </div>
              )}

              {/* Visual Selectors for Video Intent */}
              {taskIntent === 'video' && (
                <div className="space-y-3 p-3 rounded-lg bg-slate-950/70 border border-slate-800">
                  {videoCatalog.isError && <p role="alert" className="text-xs text-amber-300">Unable to load video catalog. <button type="button" onClick={() => void videoCatalog.refetch()}>Retry catalog</button></p>}
                  {(videoDefaults.loading || videoCatalog.isFetching) && <p role="status" className="text-xs text-slate-400">Loading video defaults…</p>}
                  {videoDefaults.error && <p role="alert" className="text-xs text-amber-300">{videoDefaults.error} <button type="button" onClick={() => void videoDefaults.retry()}>Retry</button></p>}
                  {!videoDefaults.loading && !selectedVideoModel && <p className="text-xs text-amber-300">{videoDefaults.configured ? 'Configured default is unavailable in this catalog. Select a model explicitly.' : 'No video default configured. Select a model explicitly.'}</p>}
                  {/* Video Model Selector */}
                  <div>
                    <div className="flex items-center justify-between mb-1">
                      <label className="block text-xs text-slate-400">Model</label>
                      {selectedVideoOption && !selectedVideoOption.ready && (
                        <span className="text-[10px] text-amber-400 flex items-center gap-1 font-mono">
                          <AlertTriangle size={10} /> {selectedVideoOption.reason || 'Not configured'}
                        </span>
                      )}
                    </div>
                    {videoModelOptions.length === 0 && mediaCatalogLoaded ? (
                      <div className="rounded bg-slate-900 border border-amber-500/30 p-2 text-amber-300 text-[11px] font-mono flex items-center gap-2">
                        <AlertTriangle size={13} className="shrink-0 text-amber-400" />
                        <span>No video models connected. Connect Google or OpenRouter key in Settings.</span>
                      </div>
                    ) : (
                      <select
                        aria-label="Video Model"
                        disabled={videoDefaults.loading || videoDefaults.saving || videoDefaults.loadFailed || videoCatalog.isFetching || videoCatalog.isError}
                        value={selectedVideoModel}
                        onChange={(e) => handleVideoModelChange(e.target.value)}
                        className="min-h-9 w-full min-w-0 rounded bg-slate-900 border border-slate-700 px-2.5 py-1.5 text-white text-sm focus:outline-none focus:border-blue-500"
                      >
                        <option value="">Select a video model</option>
                        {videoModelOptions.map((opt) => (
                          <option key={opt.id} value={opt.id} disabled={!opt.ready}>
                            {opt.label}{opt.id === defaultVideoModel ? ' (Default)' : ''}{!opt.ready ? ` (Unavailable${opt.reason ? `: ${opt.reason}` : ''})` : ''}
                          </option>
                        ))}
                      </select>
                    )}

                    {selectedVideoModel && <MediaTaskDefault isDefault={selectedVideoModel === defaultVideoModel} disabled={videoDefaults.loading || videoDefaults.saving || videoDefaults.loadFailed || videoCatalog.isFetching || videoCatalog.isError || !selectedVideoOption?.ready} saving={videoDefaults.saving} onSave={() => void handleSetVideoAsDefault(selectedVideoModel)} />}
                  </div>

                  <div className="flex flex-wrap items-start gap-2">
                    <label className="flex min-h-9 items-center gap-2 text-sm text-slate-300 cursor-pointer">
                      <input type="checkbox" checked={enhanceVideoPrompt} onChange={event => setEnhanceVideoPrompt(event.target.checked)} />
                      Enhance prompt
                    </label>
                    <MediaTaskHelp label="About video generation">Without enhancement, your prompt goes directly to the selected model. Enhancement uses Router to refine lighting and camera motion, not to split scenes. Audio depends on model capabilities.</MediaTaskHelp>
                  </div>

                  <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
                    <MediaTaskSelect label="Size" value={videoResolution} values={supportedVideoResolutions} onChange={setVideoResolution} />
                    <MediaTaskSelect label="Ratio" value={videoAspectRatio} values={supportedVideoAspectRatios} onChange={setVideoAspectRatio} />
                    <MediaTaskSelect label="Duration" value={videoDuration} values={supportedVideoDurations} suffix="s" onChange={value => setVideoDuration(Number(value))} />
                    <MediaTaskSelect label="Clips" value={videoClipCount} values={[1, 2, 4, 8]} onChange={value => setVideoClipCount(Number(value))} />
                  </div>
                  <MediaTaskScenes value={videoScenePrompts} onChange={setVideoScenePrompts} />

                  {/* Video Attachment Error Banner */}
                  {videoAttachmentError && (
                    <div className="p-2 rounded bg-red-950/40 border border-red-500/40 text-[11px] font-mono text-red-300 flex items-center justify-between gap-1.5">
                      <div className="flex items-center gap-1.5">
                        <AlertTriangle size={12} className="shrink-0 text-red-400" />
                        <span>{videoAttachmentError}</span>
                      </div>
                      <button
                        type="button"
                        onClick={() => setVideoAttachmentError(null)}
                        className="text-red-400 hover:text-white text-xs font-bold px-1"
                      >
                        ×
                      </button>
                    </div>
                  )}

                  {/* Initial Image Reference Guidance */}
                  {!selectedVideoGenOptions?.initial_image?.supported ? (
                    <div className="p-2 rounded bg-amber-950/20 border border-amber-500/30 text-[10px] font-mono text-amber-300 flex items-center gap-1.5">
                      <AlertTriangle size={12} className="shrink-0 text-amber-400" />
                      <span>Initial image support is unavailable or unverified for this model. Use a text prompt.</span>
                    </div>
                  ) : (
                    <div className="flex flex-wrap items-start gap-1 text-xs text-slate-400">
                      <span className="py-1">{taggedMedia.length > 0 ? '1 image attached' : 'Starting frame optional'}</span>
                      <MediaTaskHelp label="About starting frames">Attach, upload, browse, drop or paste one PNG/JPEG image. Text stays in the prompt. Model restrictions still apply.</MediaTaskHelp>
                    </div>
                  )}

                  <MediaTaskCost total={videoPricingInfo.totalPrice} approximate={videoPricingInfo.approximate} details={videoPricingInfo.formattedSummary} scenes={Boolean(videoScenePrompts.trim())} />
                </div>
              )}

              {/* Audio Selectors for Sounds Intent */}
              {taskIntent === 'sound' && (
                <div className="space-y-3 p-3 rounded-lg bg-slate-950/70 border border-slate-800">
                  {/* Audio Model Selector */}
                  <div>
                    <div className="flex items-center justify-between mb-1">
                      <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold">Audio / Sound Model</label>
                      {selectedAudioOption && !selectedAudioOption.ready && (
                        <span className="text-[10px] text-amber-400 flex items-center gap-1 font-mono">
                          <AlertTriangle size={10} /> {selectedAudioOption.reason || 'Not configured'}
                        </span>
                      )}
                    </div>
                    {audioModelOptions.length === 0 && mediaCatalogLoaded ? (
                      <div className="rounded bg-slate-900 border border-amber-500/30 p-2 text-amber-300 text-[11px] font-mono flex items-center gap-2">
                        <AlertTriangle size={13} className="shrink-0 text-amber-400" />
                        <span>No sound models connected. Add Google (Lyria) or OpenRouter key in Settings.</span>
                      </div>
                    ) : (
                      <select
                        aria-label="Audio Model"
                        value={selectedAudioModel}
                        onChange={(e) => handleAudioModelChange(e.target.value)}
                        className="w-full rounded bg-slate-900 border border-slate-800 px-2.5 py-1.5 text-white text-[11px] font-mono focus:outline-none focus:border-blue-500"
                      >
                        {audioModelOptions.map((opt) => (
                          <option key={opt.id} value={opt.id} disabled={!opt.ready}>
                            {opt.label}{opt.id === defaultAudioModel ? ' (Default)' : ''}{!opt.ready ? ` (Unavailable${opt.reason ? `: ${opt.reason}` : ''})` : ''}
                          </option>
                        ))}
                      </select>
                    )}
                  </div>

                  {/* Sound Clip Duration */}
                  <div className="pt-2 border-t border-slate-800/60">
                    <div className="flex items-center justify-between mb-1.5">
                      <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold">Audio Duration</label>
                      <span className="text-[10px] text-blue-400 font-mono">{soundDuration} seconds</span>
                    </div>
                    <div className="grid grid-cols-4 gap-1.5 font-mono text-[10px]">
                      {[15, 30, 60, 120].map((d) => {
                        const dCost = (d >= 60 ? 0.08 : 0.04).toFixed(2)
                        const isSelected = soundDuration === d
                        return (
                          <button
                            key={d}
                            type="button"
                            onClick={() => setSoundDuration(d)}
                            className={`py-1.5 rounded border text-center font-bold flex flex-col items-center justify-center transition-all ${
                              isSelected
                                ? 'bg-blue-600 border-blue-400 text-white'
                                : 'bg-slate-900 border-slate-800 text-slate-400 hover:text-white'
                            }`}
                          >
                            <span className="leading-tight">{d >= 60 ? `${d / 60}m Track` : `${d}s Clip`}</span>
                            <span className={`text-[8px] font-normal leading-tight mt-0.5 ${isSelected ? 'text-blue-100 font-semibold' : 'text-slate-400'}`}>
                              ${dCost}
                            </span>
                          </button>
                        )
                      })}
                    </div>
                  </div>

                  {/* Pricing Transparency Summary for Sound */}
                  <div className="p-2.5 rounded-lg bg-slate-900/90 border border-slate-800 text-[11px] font-mono flex items-center justify-between">
                    <span className="text-slate-300 flex items-center gap-1.5">
                      <Tag size={12} className="text-blue-400" />
                      <span>Estimated Model Cost: <strong className="text-white font-semibold">{audioPricingInfo.formattedSummary}</strong></span>
                    </span>
                    <span className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-slate-800 text-blue-300 border border-slate-700 font-semibold shrink-0">
                      ${audioPricingInfo.cost.toFixed(2)} Total · {soundDuration}s
                    </span>
                  </div>

                  {/* Audio Usage Tip */}
                  <div className="p-2.5 rounded-lg bg-blue-950/20 border border-blue-500/20 text-[11px] text-slate-300 space-y-1 font-sans">
                    <span className="font-semibold text-blue-400 flex items-center gap-1.5 text-[10px] font-mono uppercase">
                      <Music size={11} />
                      <span>Soundtracks & Media Pairing</span>
                    </span>
                    <p className="text-[10px] text-slate-400 leading-relaxed">
                      Generated sound clips can be previewed in the Media Center and used as soundtrack clips for your single and multi-part video tasks to give them cohesive audio.
                    </p>
                  </div>
                </div>
              )}

              {/* Workspace Selector for Code/Audit Intent */}
              {(taskIntent === 'code' || taskIntent === 'audit') && (
                <div>
                  <div className="flex items-center justify-between mb-1">
                    <label className="text-[11px] text-slate-400 font-medium">Target Workspace</label>
                    <span className="text-[10px] text-blue-400 font-mono">
                      {resolveTaskWorkspace(newTaskWorkspace) || 'Let Router Decide'}
                    </span>
                  </div>
                  {selectedProject?.workspaces && selectedProject.workspaces.length > 0 ? (
                    <select
                      value={newTaskWorkspace}
                      onChange={(e) => {
                        setNewTaskWorkspace(e.target.value)
                        setDeployError(null)
                      }}
                      className="w-full rounded-lg bg-slate-950 border border-slate-800 px-3 py-2 text-white focus:outline-none focus:border-blue-500/60 text-xs"
                    >
                      <option value="">✨ Let Router Decide (automatic)</option>
                      {selectedProject.workspaces.map((ws) => (
                        <option key={ws.workspace_id || ws.path} value={ws.path}>
                          {ws.label ? `${ws.label} — ` : ''}{ws.path}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <div className="text-[11px] font-mono text-slate-400 px-3 py-1.5 rounded-lg bg-slate-950/60 border border-slate-800/80 truncate">
                      {resolveTaskWorkspace(newTaskWorkspace) || 'Let Router Decide — link a project workspace to enable routing'}
                    </div>
                  )}
                  <p className="mt-1 text-[10px] text-slate-400">Router selects an authorized source and read-only context on submission. Your approval setting still controls launch.</p>
                  {deployPreviewQuery.data?.workspaceDiagnostic && (
                    <p className="mt-1 text-[10px] text-amber-400" role="status">Workspace: {deployPreviewQuery.data.workspaceDiagnostic}</p>
                  )}
                </div>
              )}

              {/* Deploy Error Banner */}
              {deployError && (
                <div className="p-2.5 rounded-lg bg-rose-950/40 border border-rose-500/50 text-rose-300 text-xs font-mono flex items-center justify-between gap-2" data-testid="deploy-modal-error">
                  <span>{deployError}</span>
                  <button
                    type="button"
                    onClick={() => setDeployError(null)}
                    className="text-rose-400 hover:text-white text-[10px]"
                  >
                    Dismiss
                  </button>
                </div>
              )}

              {/* Auto-Approve Permission Policy Toggle */}
              <div className="flex items-start gap-2.5 p-2.5 rounded-lg bg-slate-950/80 border border-slate-800/80">
                <input
                  type="checkbox"
                  id="auto-approve-toggle"
                  checked={autoApproveTask}
                  onChange={(e) => setAutoApproveTask(e.target.checked)}
                  className="mt-0.5 rounded border-slate-700 bg-slate-900 text-blue-600 focus:ring-0 cursor-pointer"
                />
                <label htmlFor="auto-approve-toggle" className="flex flex-col cursor-pointer select-none">
                  <span className="text-[11px] font-bold text-slate-200 flex items-center gap-1.5">
                    <Zap size={11} className={autoApproveTask ? "text-amber-400" : "text-slate-500"} />
                    <span>Auto-approve & launch immediately upon routing</span>
                  </span>
                  <span className="text-[10px] text-slate-500 leading-snug">
                    {autoApproveTask
                      ? '⚡ Skips pending review: session and media swarms start executing immediately.'
                      : '🔒 Pending Review (Default): The router stages the blueprint for your inspection and approval before launching.'}
                  </span>
                </label>
              </div>
            </div>

            {/* Actions */}
            <div className="flex items-center justify-end gap-2 pt-3 border-t border-slate-800">
              <button
                type="button"
                onClick={() => setIsDeployModalOpen(false)}
                className="px-4 py-2 rounded-lg text-xs font-medium text-slate-400 hover:text-slate-200 hover:bg-slate-800 transition-colors"
              >
                Cancel
              </button>
              <button
                type="button"
                disabled={!newTaskPrompt.trim() || isDeployingTask}
                onClick={handleDeployModalSubmit}
                className="flex items-center gap-1.5 px-5 py-2 rounded-lg text-xs font-bold text-white bg-blue-600 hover:bg-blue-500 shadow-md transition-all disabled:opacity-40 disabled:cursor-not-allowed"
              >
                <Plus size={13} />
                <span>
                  {isDeployingTask
                    ? 'Routing & Compiling...'
                    : autoApproveTask
                    ? 'Deploy & Start'
                    : 'Create Pending Task'}
                </span>
              </button>
            </div>
          </div>
        </div>
      )}

      {/* ─────────────────────────────────────────────────────────────
          MODAL: PASTE DOCUMENT / TEXT SPEC (ATTACH FOR TASKS)
         ───────────────────────────────────────────────────────────── */}
      {isPasteDocOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6 backdrop-blur-md">
          <div className="swarm-local-dialog relative flex max-w-lg w-full flex-col p-6 rounded-2xl border border-slate-800 bg-[#0d121f] shadow-2xl space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div className="flex items-center gap-2">
                <FileText size={16} className="text-emerald-400" />
                <h3 className="text-sm font-bold text-white">Paste Document / Markdown Spec</h3>
              </div>
              <button
                onClick={() => setIsPasteDocOpen(false)}
                className="rounded-full p-1 text-slate-400 hover:bg-slate-800 hover:text-white transition"
              >
                <X size={16} />
              </button>
            </div>
            <div className="space-y-3 text-xs">
              <div>
                <label className="block text-[11px] text-slate-400 font-medium mb-1">Document Title / Filename</label>
                <input
                  type="text"
                  value={pastedDocTitle}
                  onChange={(e) => setPastedDocTitle(e.target.value)}
                  placeholder="e.g. system_architecture.md or bug_report.txt"
                  className="w-full rounded-lg bg-slate-950 border border-slate-800 p-2.5 text-white placeholder-slate-600 focus:outline-none focus:border-blue-500 font-mono text-xs"
                />
              </div>
              <div>
                <label className="block text-[11px] text-slate-400 font-medium mb-1">Document Content</label>
                <textarea
                  value={pastedDocContent}
                  onChange={(e) => setPastedDocContent(e.target.value)}
                  rows={8}
                  placeholder="Paste Markdown spec, architecture guidelines, or text here..."
                  className="w-full rounded-lg bg-slate-950 border border-slate-800 p-3 text-white placeholder-slate-600 focus:outline-none focus:border-blue-500 font-mono text-xs leading-relaxed resize-none"
                />
              </div>
            </div>
            {shelfUploadError && <p role="alert">{shelfUploadError}</p>}
            <div className="flex items-center justify-end gap-2 pt-2 border-t border-slate-800">
              <button
                type="button"
                onClick={() => setIsPasteDocOpen(false)}
                className="px-3 py-1.5 rounded-lg text-slate-400 hover:text-white text-xs"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={() => void handleAddPastedDoc()}
                disabled={!pastedDocContent.trim()}
                className="flex items-center gap-1.5 px-4 py-2 rounded-lg bg-emerald-600 hover:bg-emerald-500 text-white text-xs font-bold transition disabled:opacity-50"
              >
                <Check size={14} />
                <span>Save & Tag Document</span>
              </button>
            </div>
          </div>
        </div>
      )}

      {/* ─────────────────────────────────────────────────────────────
          STUDIO MEDIA VIEWER MODAL (PAN/ZOOM, METADATA, TAGGING, SWARM ITERATIONS)
         ───────────────────────────────────────────────────────────── */}
      {activeMediaViewerItem && (
        <MediaViewerModal
          item={allMediaLibraryItems.find(item => item.id === activeMediaViewerItem.id) ?? activeMediaViewerItem}
          items={allMediaLibraryItems}
          isGenerating={isDeployingTask}
          onClose={() => {
            setActiveMediaViewerItem(null)
            setMediaViewerInitialMode(null)
          }}
          onSelect={(item) => setActiveMediaViewerItem(item)}
          onOpenSession={(sessionId) => {
            setActiveSessionId(sessionId)
            setActiveMediaViewerItem(null)
          }}
          isTagged={taggedMedia.some((t) => t.id === activeMediaViewerItem.id)}
          onToggleTag={(item) => {
            const isTagged = taggedMedia.some((t) => t.id === item.id)
            if (isTagged) {
              setTaggedMedia((prev) => prev.filter((t) => t.id !== item.id))
            } else {
              setTaggedMedia((prev) => [
                ...prev,
                {
                  id: item.id,
                  title: item.title,
                  filename: item.filename,
                  kind: item.kind as any,
                  mediaType: item.mediaType,
                  url: item.directUrl,
                  createdAt: item.createdAt,
                },
              ])
            }
          }}
          onGenerate={handleMediaGenerate}
          generationJobs={allGenerationJobs}
          initialQuickRouteMode={mediaViewerInitialMode}
        />
      )}

      {/* ─────────────────────────────────────────────────────────────
          ROUTED HISTORICAL MEDIA LIBRARY / STUDIO CENTER
         ───────────────────────────────────────────────────────────── */}
      {showFullMediaCenter && (
        <main className="swarm-main-panel swarm-media-panel relative flex min-w-0 flex-1 flex-col overflow-hidden rounded-3xl border border-slate-800/80 bg-[#0d121f]/95">
          <div className="flex h-14 items-center justify-between border-b border-slate-800 bg-[#0d121f] px-5">
            <div className="flex items-center gap-3">
              <div className="flex size-8 items-center justify-center rounded-xl bg-blue-600/20 text-blue-400 border border-blue-500/30">
                <Film size={16} />
              </div>
              <div>
                <h3 className="text-sm font-bold text-white">Media Studio & Historical Library</h3>
                <p className="text-[11px] text-slate-400">Browse, inspect metadata, pan/zoom, and tag assets for autonomous tasks</p>
              </div>
            </div>
            <button
              onClick={() => setShowFullMediaCenter(false)}
              className="p-2 rounded-xl bg-slate-800 text-slate-400 hover:text-white hover:bg-slate-700 transition"
              title="Back to Tasks"
            >
              <X size={16} />
            </button>
          </div>
          {mediaSyncError && <p role="alert" className="px-5 py-2 text-xs text-amber-300">Live updates interrupted: {mediaSyncError}. Refresh project state before resubmitting.</p>}
          <div className="flex-1 min-h-0">
            <HistoricalMediaLibrary
              key={selectedProject?.id ?? ''}
              projectId={selectedProject?.id}
              onClose={() => setShowFullMediaCenter(false)}
              onTagMedia={(item) => {
                setTaggedMedia((prev) => {
                  if (prev.some((t) => t.id === item.id)) {
                    return prev.filter((t) => t.id !== item.id)
                  }
                  return [
                    ...prev,
                    {
                      id: item.id,
                      title: item.title,
                      filename: item.filename,
                      kind: item.kind as any,
                      mediaType: item.mediaType,
                      url: item.directUrl,
                      createdAt: item.createdAt,
                    },
                  ]
                })
              }}
              taggedMediaIds={new Set(taggedMedia.map((t) => t.id))}
              onGenerate={handleMediaGenerate}
              generationJobs={allGenerationJobs}
              isGenerating={isDeployingTask}
              extraItems={allMediaLibraryItems}
            />
          </div>
        </main>
      )}

      {/* Reused Agent Setup & Model Control (/agents authority) */}
      <AgentModelControl
        portalContainer={themeRoot}
        currentAgent="swarm"
        selectedPrimaryAgent="swarm"
        agents={[]}
        selectedModel={null}
        modelOptions={modelOptions}
        setupOpenSignal={agentSettingsOpenSignal}
        initialAgentName={agentSettingsInitialAgent}
        showTrigger={false}
        taskScoped={agentSettingsTaskContext !== null}
        taskLabel={agentSettingsTaskContext?.label}
        taskModelOverride={agentSettingsTaskContext?.currentModel}
        hasTaskOverride={Boolean(agentSettingsTaskContext?.hasOverride)}
        onApplyTaskModel={handleApplyTaskModelFromControl}
        onResetTaskModel={handleResetTaskModelFromControl}
      />
    </div>
  )
}
