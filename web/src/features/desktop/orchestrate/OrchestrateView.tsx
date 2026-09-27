import { useState, useMemo, useEffect, useCallback, useRef } from 'react'
import { useVideoTaskDefault } from './use-video-task-default'
import { useQuery } from '@tanstack/react-query'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { isSwarmSection, swarmPageLink, type SwarmPage } from './swarm-navigation'
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
  Eye,
  ExternalLink,
  FileText,
  Film,
  Folder,
  FolderTree,
  FolderGit2,
  FolderPlus,
  GitBranch,
  GitPullRequest,
  Home,
  Image as ImageIcon,
  Layers,
  ListChecks,
  Loader2,
  MessageSquare,
  MoreHorizontal,
  Music,
  Palette,
  Paperclip,
  Play,
  Plus,
  Radio,
  RefreshCw,
  RotateCcw,
  Search,
  Settings,
  Settings2,
  Sparkles,
  Tag,
  Timer,
  Trash2,
  Upload,
  Volume2,
  X,
  Zap,
} from 'lucide-react'
import { formatContextWindow } from '../chat/services/model-options'
import { requestJson } from '../../../app/api'
import { useDesktopV3CacheSelector, getDesktopV3CacheSnapshot } from '../state/desktop-v3-cache-store'
import { selectPendingWorkerSidebarReviews } from '../state/desktop-automation-v2-state'
import { desktopAutomationV2 } from '../runtime/desktop-automation-v2'
import { decidePendingWorkerReview } from '../tools/automations/pending-worker-sidebar-reviews'
import type { AutomationV2Proposal } from '../state/desktop-automation-v2-api'
import { DesktopV3ExistingConversationPane } from '../chat/components/desktop-v3-existing-conversation-pane'
import {
  isDesktopV3SessionTailReady,
  selectRenderedSessionMessages,
  summarizeDesktopV3TaskToolActivity,
} from '../state/desktop-v3-cache-selectors'
import { selectAndHydrateDesktopV3Session, hydrateDesktopV3ChildCard } from '../state/desktop-v3-session-hydrator'
import {
  requireDesktopV3RealtimeControllerReady,
  type DesktopV3RealtimeSessionDemandLease,
} from '../realtime/v3-realtime-controller'
import { HistoricalMediaLibrary, MediaViewerModal, type MediaLibraryItem } from '../tools/media-library'
import type { MediaGenerationJob, MediaGenerationRequest, MediaGenerationSettings } from '../tools/media-library/media-generation'
import type { QuickRouteMode } from '../tools/media-library/media-viewer-modal'
import { ORCHESTRATE_THEMES } from './orchestrate-themes'
import { listStorageWorkers, activateStorageWorker } from '../storage/api'
import type { StorageDiscoveredWorker } from '../storage/types'
import { desktopProjects, useDesktopProject } from '../runtime/desktop-projects'
import {
  DeployedWorker,
  MediaDeliverable,
  MiddleCanvasVariant,
  OrchestrateThemeId,
  ProjectSummary,
  ProjectTaskMediaRef,
  RunningAutomation,
  RunningTask,
  RunningTaskPlanCheckpoint,
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
  getPrimarySystemAgentName,
  resolveDeployImpendingConfig,
  resolveOptimisticApprovedDeliverables,
  resolveTaskImpendingAgents,
} from './orchestrate-task-helpers'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { agentModelSettingsQueryOptions } from '../settings/swarm/queries/get-agent-model-settings'
import { AgentModelControl, type AgentModelControlTaskOverrideInput } from '../chat/components/agent-model-control'
import { modelOptionsQueryOptions } from '../../queries/query-options'
import type { ModelOptionRecord } from '../chat/types/chat'
import type { BackendTaskModelPreview } from './orchestrate-types'

export interface OrchestrateViewProps {
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
  const isVideo = d.type === 'video' || (d.previewUrl && !d.previewUrl.startsWith('data:image'))
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
    filename: `${d.title.replace(/[^a-zA-Z0-9_-]/g, '_')}.${kind === 'image' ? 'png' : 'mp4'}`,
    mediaType: kind === 'image' ? 'image/png' : 'video/mp4',
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
    directUrl: d.previewUrl || d.mediaUrl || '',
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
      mediaType: kind === 'image' ? 'image/png' : 'video/mp4',
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
  onPlay,
}: {
  type?: string
  duration?: string
  previewUrl?: string
  status?: string
  onPlay?: () => void
}) {
  if (status === 'generating') {
    return (
      <div className="relative aspect-video w-full rounded-lg border border-blue-500/40 bg-blue-950/20 flex flex-col items-center justify-center p-3 animate-pulse space-y-1.5">
        <Loader2 size={18} className="animate-spin text-blue-400" />
        <span className="text-[10px] font-mono text-blue-300 font-semibold tracking-wider uppercase">Generating Media...</span>
      </div>
    )
  }

  if (status === 'pending') {
    return (
      <div className="relative aspect-video w-full rounded-lg border-2 border-dashed border-slate-700/60 bg-slate-900/30 flex flex-col items-center justify-center p-3 space-y-1 text-center">
        <ImageIcon size={18} className="text-slate-500" />
        <span className="text-[10px] font-mono text-slate-400">Empty Slot • Pending</span>
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
    if (!start) return <span>{fallbackElapsed || 'Running...'}</span>
    const totalSec = Math.max(0, Math.floor((now - start) / 1000))
    const mins = Math.floor(totalSec / 60)
    const secs = totalSec % 60
    return <span>{`${mins}:${secs.toString().padStart(2, '0')}`}</span>
  }

  return <span>{fallbackElapsed || 'Just now'}</span>
}

/**
 * Minimal Task Card: Clean, technical, outcome-focused task card
 * Free of highlight gradients and pill badges. Displays "Action Needed",
 * worktree/unmerged git status, and "What did it do?" vs "What's not done yet?".
 */
function MinimalTaskCard({
  task,
  isSelected,
  onSelect,
  onOpenChat,
  onApprove,
  onIntegrate,
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
  defaultImageModel,
  defaultVideoModel,
  defaultAudioModel,
  imageModelOptions,
  videoModelOptions,
  audioModelOptions,
}: {
  task: RunningTask
  isSelected?: boolean
  onSelect?: () => void
  onOpenChat?: () => void
  onApprove?: () => void
  onIntegrate?: () => void
  onDelete?: () => void
  onRefine?: (feedback?: string, errorSummary?: string) => void
  onPreviewDeliverable?: (d: MediaDeliverable) => void
  onReopen?: (feedback?: string) => void
  onComplete?: () => void
  onRedeployJob?: (taskId: string, jobId: string, feedback?: string) => void
  onUpdateModel?: (taskId: string, model: string, scopeInput?: AgentModelControlTaskOverrideInput | null) => void | Promise<void>
  onOpenAgentSettings?: (agentName: string) => void
  onOpenTaskModelChanger?: (task: RunningTask) => void
  projectId?: string
  modelOptions?: ModelOptionRecord[]
  defaultImageModel?: string
  defaultVideoModel?: string
  defaultAudioModel?: string
  imageModelOptions?: TaskModalModelOption[]
  videoModelOptions?: TaskModalModelOption[]
  audioModelOptions?: TaskModalModelOption[]
}) {
  const agentModelSettingsQuery = useQuery(agentModelSettingsQueryOptions())
  const isPlanning = task.status === 'planning'
  const isPendingApproval = task.status === 'pending_approval' || task.status === 'queued'
  const [isFullPlanOpen, setIsFullPlanOpen] = useState(isPendingApproval)
  const [isModelChangerOpen, setIsModelChangerOpen] = useState(false)
  const [selectedTaskModel, setSelectedTaskModel] = useState(task.model || '')
  useEffect(() => {
    setSelectedTaskModel(task.model || '')
  }, [task.model])

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
  const isRunning = task.status === 'running' || task.status === 'in_progress'
  const isNeedsReview = task.status === 'needs_review'
  const isCompleted = task.status === 'completed'
  const hasUnintegrated = (task.unintegratedCommits ?? 0) > 0

  const taskProgramStatus = task.taskProgramStatus || (task as any).task_program_status
  const taskProgramDef = task.taskProgram || (task as any).task_program || taskProgramStatus?.definition
  const isTaskProgram = Boolean(
    (taskProgramDef?.jobs && taskProgramDef.jobs.length > 0) ||
    (taskProgramStatus?.jobs && taskProgramStatus.jobs.length > 0)
  )

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

  return (
    <div
      onClick={onSelect}
      className={`relative flex flex-col rounded-xl border transition-all p-3.5 space-y-3 cursor-pointer ${
        isSelected
          ? 'bg-[#0f1526] border-blue-500/60 shadow-[0_4px_20px_rgba(0,0,0,0.5)]'
          : 'bg-[#0a0f1d] border-slate-800/80 hover:border-slate-700/80 hover:bg-[#0c1222]'
      }`}
    >
      {/* 1. Header: Agent Tag + Title + Status + Actions */}
      <div className="flex items-start justify-between gap-3">
        <div className="flex flex-col gap-1 min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span
              className={`font-mono text-[9px] uppercase font-bold px-2 py-0.5 rounded border ${
                task.agentType === 'swarm'
                  ? 'bg-amber-500/10 text-amber-300 border-amber-500/30'
                  : task.agentType === 'designer' || task.agentType === 'video'
                  ? 'bg-blue-500/10 text-blue-400 border-blue-500/25'
                  : 'bg-indigo-500/10 text-indigo-300 border-indigo-500/25'
              }`}
            >
              {task.agentType.toUpperCase()} • {task.workspacePath ? task.workspacePath.split('/').filter(Boolean).pop() : 'WORKSPACE'}
            </span>
            {task.outcomeType && (
              <span className="font-mono text-[9px] uppercase px-1.5 py-0.5 rounded bg-slate-800 text-slate-400 border border-slate-700/50">
                {task.outcomeType === 'video_clip' ? 'SINGLE VIDEO' : task.outcomeType.replace('_', ' ')}
              </span>
            )}
            {/* Show worktree name right away for non-media tasks */}
            {!isMediaTask && (task.worktreeBranch || task.worktreeName) && (
              <span
                className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-indigo-950/70 text-indigo-300 border border-indigo-500/30 flex items-center gap-1 font-semibold"
                title={`Worktree: ${task.worktreeBranch || (task.worktreeName ? `agent/${task.worktreeName}` : 'agent/worktree')}`}
              >
                <GitBranch size={9} />
                <span>{task.worktreeBranch || (task.worktreeName ? `agent/${task.worktreeName}` : 'agent/worktree')}</span>
              </span>
            )}
            {/* Show integration status right away for non-media tasks */}
            {!isMediaTask && (
              task.isIntegrated ? (
                <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-emerald-950/80 text-emerald-300 border border-emerald-500/40 flex items-center gap-1 font-semibold">
                  <CheckCircle2 size={9} />
                  <span>Integrated</span>
                </span>
              ) : hasUnintegrated ? (
                <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-amber-950/80 text-amber-300 border border-amber-500/40 flex items-center gap-1 font-semibold">
                  <span>Not Integrated ({task.unintegratedCommits})</span>
                </span>
              ) : task.isDirty ? (
                <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-amber-950/50 text-amber-300 border border-amber-500/30 flex items-center gap-1 font-semibold">
                  <span>Changes Pending Commit</span>
                </span>
              ) : task.syncWarning || (task.behindCommits && task.behindCommits > 0) ? (
                <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-rose-950/80 text-rose-300 border border-rose-500/40 flex items-center gap-1 font-semibold">
                  <AlertTriangle size={9} />
                  <span>Out of Sync</span>
                </span>
              ) : null
            )}
            {(task.routerAlert || (task as any).router_alert) && (
              <span className="font-mono text-[9px] uppercase px-1.5 py-0.5 rounded bg-amber-950/80 text-amber-300 border border-amber-500/50 flex items-center gap-1 font-bold">
                <AlertTriangle size={9} />
                <span>Router Alert</span>
              </span>
            )}
          </div>
          <h3 className="text-xs font-bold text-white tracking-tight leading-snug truncate">
            {task.title}
          </h3>
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

        <div className="flex items-center gap-2 flex-shrink-0">
          <div
            className={`font-mono text-[10px] font-bold uppercase px-2 py-0.5 rounded border flex items-center gap-1.5 ${
              isPlanning
                ? 'bg-indigo-950/40 text-indigo-300 border-indigo-500/40'
                : isPendingApproval
                ? 'bg-amber-950/40 text-amber-300 border-amber-500/40'
                : isRunning
                ? 'bg-blue-950/40 text-blue-400 border-blue-500/40'
                : isNeedsReview
                ? 'bg-amber-950/40 text-amber-300 border-amber-500/40'
                : isCompleted
                ? 'bg-emerald-950/40 text-emerald-300 border-emerald-500/40'
                : 'bg-slate-800 text-slate-400 border-slate-700'
            }`}
          >
            <span
              className={`h-1.5 w-1.5 rounded-sm ${
                isPlanning
                  ? 'bg-indigo-400 animate-spin'
                  : isPendingApproval
                  ? 'bg-amber-400'
                  : isRunning
                  ? 'bg-blue-400 animate-pulse'
                  : isNeedsReview
                  ? 'bg-amber-400'
                  : isCompleted
                  ? 'bg-emerald-400'
                  : 'bg-slate-500'
              }`}
            />
            <span>{task.status === 'in_progress' ? 'in progress' : task.status.replace('_', ' ')}</span>
          </div>

          <span className="font-mono text-[10px] text-slate-400 flex items-center gap-1">
            {isRunning && <Timer size={10} className="text-blue-400 animate-spin" />}
            <TaskElapsedTimer
              isRunning={isRunning}
              startedAt={task.startedAt}
              createdAt={task.createdAt}
              fallbackElapsed={task.elapsed}
            />
          </span>

          {task.sessionId && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onOpenChat?.()
              }}
              className="flex items-center gap-1 px-2 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white text-[10px] font-medium transition-colors border border-slate-700/60"
              title="Open session chat with this worker"
            >
              <MessageSquare size={11} />
              <span>Chat</span>
            </button>
          )}
        </div>
      </div>

      {/* ROUTER AGENT FAILURE ALERT BANNER */}
      {(task.routerAlert || (task as any).router_alert) && (
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
      {isPlanning && (
        <div className="flex flex-col p-3 rounded-lg bg-indigo-950/30 border border-indigo-500/40 space-y-2 text-xs">
          <div className="flex items-center gap-2">
            <span className="font-bold text-indigo-400 flex items-center gap-1.5 font-mono text-[10px] uppercase">
              <Sparkles size={12} className="animate-spin text-indigo-300" />
              <span>AI Task Router Planning...</span>
            </span>
            <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-indigo-900/40 text-indigo-300 border border-indigo-500/30 animate-pulse">
              Analyzing Workspaces
            </span>
          </div>
          <p className="text-slate-300 text-xs leading-relaxed">
            The AI Router is analyzing project boundaries, detecting target workspaces, and formulating a multi-tier mission plan...
          </p>
        </div>
      )}

      {/* 2. PENDING APPROVAL MISSION PROPOSAL BANNER */}
      {isPendingApproval && (
        <div className="flex flex-col p-3 rounded-lg bg-blue-950/20 border border-blue-500/40 space-y-2.5 text-xs">
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <div className="flex items-center gap-2">
              <span className="font-bold text-blue-400 flex items-center gap-1.5 font-mono text-[10px] uppercase">
                <Sparkles size={12} />
                <span>AI Mission Proposal</span>
              </span>
              <span className="font-mono text-[9px] uppercase px-1.5 py-0.5 rounded bg-blue-900/40 text-blue-300 border border-blue-500/30">
                {task.tier === 'discovery' ? 'Tier 2: Discovery' : task.tier === 'complex' ? 'Tier 3: Complex Plan' : 'Tier 1: Direct'}
              </span>
              {task.revision && task.revision > 1 && (
                <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-indigo-900/50 text-indigo-300 border border-indigo-500/30 font-bold">
                  Rev {task.revision}
                </span>
              )}
              {task.autoApprove && (
                <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-emerald-950/60 text-emerald-300 border border-emerald-500/30 flex items-center gap-1 font-bold">
                  <Zap size={9} />
                  <span>Auto-Approved</span>
                </span>
              )}
            </div>
            {!isMediaTask && (task.worktreeBranch || task.worktreeName) && (
              <span className="text-[10px] font-mono text-slate-400 flex items-center gap-1.5">
                <span>Worktree:</span>
                <span className="text-indigo-300 font-semibold flex items-center gap-1">
                  <GitBranch size={10} />
                  <span>{task.worktreeBranch || (task.worktreeName ? `agent/${task.worktreeName}` : 'agent/worktree')}</span>
                </span>
              </span>
            )}
          </div>

          {/* Impending Execution Agents & Resolved Models */}
          <div className="flex flex-col gap-2 p-2.5 rounded bg-slate-900/90 border border-slate-800 text-[11px] font-mono" data-testid="task-impending-agents">
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
          </div>

          <p className="text-slate-200 text-xs leading-relaxed">
            {task.subtitle || task.title}
          </p>

          {/* Plan Summary */}
          {task.planSummary && (
            <div className="p-2.5 rounded bg-slate-900/80 border border-slate-800 text-[11px] font-mono text-slate-300 whitespace-pre-line leading-relaxed">
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
              <span>Target Worktree: <strong className="text-indigo-300 font-semibold">{task.worktreeBranch || (task.worktreeName ? `agent/${task.worktreeName}` : 'agent/worktree')}</strong></span>
              <span>•</span>
              <span className="text-emerald-400 font-semibold">Verified Local Tests</span>
              <span>•</span>
              <span className="text-slate-400">Target Integration: <strong className="text-white">{task.baseBranch || 'main'}</strong></span>
            </div>
          )}
          {/* Plan Spec - for big features / plan mode tasks */}
          {(task.agentType === 'plan' || task.outcomeType === 'plan_spec') && (
            <div className="flex items-center gap-2 p-2 rounded bg-purple-950/30 border border-purple-500/40 text-[10px] font-mono text-purple-200 flex-wrap">
              <Sparkles size={11} className="text-purple-400 flex-shrink-0" />
              <span>Execution Mode: <strong className="text-purple-300 font-semibold">Plan-Mode Orchestration</strong></span>
              <span>•</span>
              <span className="text-purple-300">Requires User Plan Review Before Launch</span>
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

          {/* Visual Deliverable Blueprint / Placeholders (Empty boxes before acceptance) */}
          {(task.agentType === 'image' || task.agentType === 'video' || task.outcomeType === 'media_bundle' || task.outcomeType === 'video_story' || task.outcomeType === 'video_clip') && (
            <div className="flex flex-col space-y-2 pt-2 pb-1 border-t border-blue-500/20">
              <div className="flex items-center justify-between text-[10px] font-mono text-slate-400">
                <span className="font-bold uppercase tracking-wider text-blue-400 flex items-center gap-1.5">
                  <Sparkles size={11} />
                  <span>Deliverable Blueprint ({variantSlots.length} {variantSlots.length === 1 ? 'Slot' : 'Slots'})</span>
                </span>
                <span className="text-slate-500">Empty placeholder until accepted</span>
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-2.5">
                {variantSlots.map((slotNum) => (
                  <div
                    key={slotNum}
                    className={`relative flex flex-col items-center justify-center p-4 rounded-xl border-2 border-dashed border-blue-500/30 bg-blue-950/10 hover:border-blue-500/50 transition-colors ${
                      task.aspectRatio === '1:1' ? 'aspect-square' : task.aspectRatio === '9:16' ? 'aspect-[9/16]' : 'aspect-video'
                    }`}
                  >
                    <div className="flex flex-col items-center text-center space-y-1.5">
                      <div className="w-8 h-8 rounded-lg bg-blue-500/10 border border-blue-500/20 flex items-center justify-center text-blue-400">
                        {task.agentType === 'video' ? <Film size={16} /> : <ImageIcon size={16} />}
                      </div>
                      <span className="text-[11px] font-mono font-bold text-slate-200">
                        Slot {slotNum}: {task.aspectRatio || (task.agentType === 'video' ? '16:9' : '1:1')} {task.agentType === 'video' ? (isSingleVideo ? 'Single Video' : 'Video Story') : 'Image'}
                      </span>
                      <span className="text-[9px] font-mono text-blue-400/80 bg-blue-950/40 px-2 py-0.5 rounded border border-blue-500/20">
                        Pending Acceptance
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Expandable Full Plan */}
          {task.fullPlanMarkdown && (
            <div className="border border-slate-800/80 rounded bg-[#070b14]/90 overflow-hidden">
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  setIsFullPlanOpen(!isFullPlanOpen)
                }}
                className="w-full flex items-center justify-between px-2.5 py-1.5 text-[10px] font-mono text-slate-400 hover:text-slate-200 transition-colors"
              >
                <span className="flex items-center gap-1.5 font-semibold">
                  <FileText size={11} className="text-blue-400" />
                  <span>{isFullPlanOpen ? 'Hide Full Plan Spec' : 'Read Full Plan Spec & Criteria'}</span>
                </span>
                {isFullPlanOpen ? <ChevronUp size={11} /> : <ChevronDown size={11} />}
              </button>
              {isFullPlanOpen && (
                <div className="p-3 border-t border-slate-800 text-[11px] text-slate-300 font-mono whitespace-pre-wrap leading-relaxed max-h-60 overflow-y-auto bg-slate-950/60">
                  {task.fullPlanMarkdown}
                </div>
              )}
            </div>
          )}

          {/* Refine / Actions Bar */}
          <div className="flex flex-col gap-2 pt-1 border-t border-blue-500/20">
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
                      : '1 Branch PR + Test Suite'}
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
                    title="Send instructions back to the Router / Plan Agent to adjust workspaces or plan"
                  >
                    <Sparkles size={10} className="text-indigo-400" />
                    <span>{isRefineOpen ? 'Cancel' : 'Refine Plan'}</span>
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
                    onClick={(e) => {
                      e.stopPropagation()
                      onApprove()
                    }}
                    className="flex items-center gap-1.5 px-3 py-1.5 rounded bg-blue-600 hover:bg-blue-500 text-white font-bold text-xs shadow-md transition-all active:scale-95"
                  >
                    <Sparkles size={11} />
                    <span>
                      {task.agentType === 'image' || task.agentType === 'video' || task.outcomeType === 'media_bundle' || task.outcomeType === 'video_story' || task.outcomeType === 'video_clip'
                        ? `Approve & Generate (${variantSlots.length} ${variantSlots.length === 1 ? (task.agentType === 'video' ? 'Clip' : 'Variant') : 'Variants'})`
                        : 'Approve & Start Session'}
                    </span>
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
                  placeholder="e.g. Keep in web workspace only, don't touch daemon API..."
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
                  Send to Router
                </button>
              </div>
            )}
          </div>
        </div>
      )}

      {/* 2b. IN PROGRESS / RUNNING LIVE EXECUTION SECTION */}
      {isRunning && (
        <div className="flex flex-col p-3 rounded-lg bg-[#070d1e]/90 border border-blue-500/40 space-y-2.5 text-xs">
          {/* Current Focus Banner */}
          <div className="flex items-center justify-between p-2 rounded-lg bg-blue-950/50 border border-blue-500/30 gap-2">
            <div className="flex items-center gap-2 min-w-0">
              <span className="flex h-2 w-2 relative flex-shrink-0">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75"></span>
                <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500"></span>
              </span>
              <div className="flex flex-col min-w-0">
                <div className="flex items-center gap-1.5 flex-wrap">
                  <span className="font-mono text-[9px] uppercase font-bold text-blue-300 tracking-wider">
                    🎯 Current Focus
                  </span>
                  {task.currentTool && (
                    <span className="font-mono text-[8px] uppercase px-1.5 py-0.2 rounded bg-indigo-900/60 text-indigo-300 border border-indigo-500/30">
                      tool: {task.currentTool}
                    </span>
                  )}
                </div>
                <span className="font-semibold text-white truncate text-[11px]">
                  {task.currentFocus || 'Executing autonomous mission...'}
                </span>
              </div>
            </div>
            {task.planProgressPercent !== undefined && task.planProgressPercent > 0 && (
              <div className="flex items-center gap-1 font-mono text-[10px] text-blue-300 font-bold flex-shrink-0 bg-blue-900/40 px-2 py-0.5 rounded border border-blue-500/30">
                <span>{task.planProgressPercent}%</span>
              </div>
            )}
          </div>

          {/* Agent's Created Execution Plan with Subtasks Checklist OR Compact Task Program Multi-Coder Grid */}
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
                              <span>Integrated</span>
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

                        {job.child_session_id && onOpenChat && !isConflict && (
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

          {/* Live Streaming Activity Box */}
          {(task.liveAssistantText || task.toolActivitySummary) && (
            <div className="flex flex-col p-2.5 rounded-lg bg-[#050811] border border-blue-500/25 space-y-1.5 font-mono text-[10px]">
              <div className="flex items-center justify-between text-slate-400">
                <span className="flex items-center gap-1.5 text-blue-400 font-bold uppercase tracking-wider text-[9px]">
                  <Radio size={10} className="animate-pulse text-emerald-400" />
                  <span>Live Streaming Activity</span>
                </span>
                {task.toolActivitySummary && (
                  <span className="text-slate-400 truncate max-w-[200px]" title={task.toolActivitySummary}>
                    {task.toolActivitySummary}
                  </span>
                )}
              </div>
              {task.liveAssistantText && (
                <div className="p-2 rounded bg-black/70 border border-slate-900 text-slate-300 whitespace-pre-wrap leading-relaxed max-h-24 overflow-y-auto">
                  {task.liveAssistantText.slice(-300)}
                  <span className="inline-block w-1.5 h-3 bg-blue-400 ml-0.5 animate-pulse" />
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {/* 2c. NEEDS REVIEW COMPLETION BANNER & ACTIONS */}
      {isNeedsReview && (
        <div className="flex flex-col p-3 rounded-lg bg-amber-950/20 border border-amber-500/40 space-y-2.5 text-xs">
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <div className="flex items-center gap-2">
              <CheckCircle2 size={14} className="text-amber-400 flex-shrink-0" />
              <div className="flex flex-col">
                <span className="font-bold text-amber-300 font-mono text-[10px] uppercase">
                  Mission Execution Completed — Awaiting Review
                </span>
                <span className="text-[11px] text-slate-300">
                  Agent finished execution. Verify changes and integrate or reopen for revisions.
                </span>
              </div>
            </div>
            <span className="font-mono text-[9px] px-1.5 py-0.5 rounded bg-amber-900/40 text-amber-300 border border-amber-500/30 font-bold uppercase">
              Needs Review
            </span>
          </div>

          {/* Plan Summary if available */}
          {task.planSummary && (
            <div className="p-2 rounded bg-slate-900/80 border border-slate-800 text-[11px] font-mono text-slate-300 leading-relaxed">
              <span className="text-amber-400 font-bold block text-[9px] uppercase tracking-wider mb-0.5">Execution Summary</span>
              {task.planSummary}
            </div>
          )}

          {/* Actions: Integrate, Complete, Reopen */}
          <div className="flex items-center justify-between pt-1 border-t border-amber-500/20 gap-2 flex-wrap">
            <div className="flex items-center gap-2">
              {onReopen && (
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation()
                    setIsReopenOpen(!isReopenOpen)
                  }}
                  className="flex items-center gap-1 px-2.5 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white text-[10px] font-medium transition-colors border border-slate-700/60"
                  title="Reopen task to add instructions and continue work"
                >
                  <RotateCcw size={10} className="text-amber-400" />
                  <span>{isReopenOpen ? 'Cancel' : 'Reopen Task'}</span>
                </button>
              )}
            </div>

            <div className="flex items-center gap-2">
              {onComplete && !hasUnintegrated && (
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation()
                    onComplete()
                  }}
                  className="flex items-center gap-1.5 px-3 py-1 rounded bg-emerald-600 hover:bg-emerald-500 text-white font-bold text-xs shadow transition-all active:scale-95"
                >
                  <Check size={11} />
                  <span>Accept & Complete</span>
                </button>
              )}
              {hasUnintegrated && onIntegrate && (
                <button
                  type="button"
                  onClick={(e) => {
                    e.stopPropagation()
                    onIntegrate()
                  }}
                  className="flex items-center gap-1.5 px-3 py-1 rounded bg-amber-500 hover:bg-amber-400 text-slate-950 font-bold text-xs shadow transition-all active:scale-95"
                >
                  <GitPullRequest size={11} />
                  <span>Integrate into {task.baseBranch || 'main'}</span>
                </button>
              )}
            </div>
          </div>

          {/* Inline Reopen Feedback Input */}
          {isReopenOpen && onReopen && (
            <div className="flex items-center gap-2 p-2 rounded bg-slate-900 border border-slate-800 animate-in fade-in duration-200">
              <input
                type="text"
                value={reopenFeedback}
                onClick={(e) => e.stopPropagation()}
                onChange={(e) => setReopenFeedback(e.target.value)}
                placeholder="Instructions for the agent to resume work..."
                className="flex-1 bg-slate-950 border border-slate-800 rounded px-2.5 py-1 text-xs text-white placeholder-slate-500 focus:outline-none focus:border-amber-500 font-mono"
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.stopPropagation()
                    onReopen(reopenFeedback.trim() || undefined)
                    setReopenFeedback('')
                    setIsReopenOpen(false)
                  }
                }}
              />
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onReopen(reopenFeedback.trim() || undefined)
                  setReopenFeedback('')
                  setIsReopenOpen(false)
                }}
                className="px-3 py-1 rounded bg-amber-600 hover:bg-amber-500 text-white font-bold text-[10px] transition-colors flex-shrink-0"
              >
                Resume Run
              </button>
            </div>
          )}
        </div>
      )}

      {/* Execution Error Recovery Banner */}
      {task.lastError && (
        <div className="flex items-start justify-between p-2.5 rounded-lg bg-rose-950/30 border border-rose-500/40 text-[11px] gap-2">
          <div className="flex items-start gap-2 min-w-0">
            <AlertTriangle size={13} className="text-rose-400 flex-shrink-0 mt-0.5" />
            <div className="space-y-0.5 min-w-0">
              <span className="font-bold text-rose-300 block">Execution Error / Test Failure:</span>
              <span className="font-mono text-slate-300 text-[10px] break-all block">{task.lastError}</span>
            </div>
          </div>
          {onRefine && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onRefine(undefined, task.lastError)
              }}
              className="flex-shrink-0 px-2.5 py-1 rounded bg-rose-600 hover:bg-rose-500 text-white font-bold text-[10px] transition-colors flex items-center gap-1 shadow"
              title="Send this failure log to Plan Agent to generate an error recovery fix strategy"
            >
              <Sparkles size={10} />
              <span>Re-Plan with Plan Agent</span>
            </button>
          )}
        </div>
      )}

      {/* 3. Out of Sync Warning Banner */}
      {!isPendingApproval && (task.syncWarning || (task.behindCommits && task.behindCommits > 0)) && (
        <div className="flex items-center justify-between p-2 rounded-lg bg-rose-950/30 border border-rose-500/50 text-rose-200 text-[11px] gap-2">
          <div className="flex items-center gap-2 min-w-0">
            <AlertTriangle size={12} className="text-rose-400 flex-shrink-0" />
            <span className="font-bold text-rose-400 flex-shrink-0">Out of Sync Warning:</span>
            <span className="truncate">
              {task.syncWarning || `${task.baseBranch || 'main'} branch could be out of sync (${task.behindCommits} commits behind). Rebase or synchronization recommended.`}
            </span>
          </div>
        </div>
      )}

      {/* 4. Action Needed / Not Integrated Banner (if unintegrated commits ready to land) */}
      {!isPendingApproval && !isMediaTask && hasUnintegrated && (
        <div className="flex items-center justify-between p-2 rounded-lg bg-amber-950/25 border border-amber-500/40 text-amber-200 text-[11px] gap-2">
          <div className="flex items-center gap-2 min-w-0">
            <GitPullRequest size={12} className="text-amber-400 flex-shrink-0" />
            <span className="font-bold text-amber-400 flex-shrink-0">Not Integrated:</span>
            <span className="truncate">
              {task.unintegratedCommits} commit(s) on {task.worktreeBranch || 'worktree'} ready to integrate into {task.baseBranch || 'main'}.
            </span>
          </div>
          {onIntegrate && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onIntegrate()
              }}
              className="flex-shrink-0 px-2.5 py-0.5 rounded bg-amber-500 hover:bg-amber-400 text-slate-950 font-bold text-[10px] transition-colors"
            >
              Integrate into {task.baseBranch || 'main'}
            </button>
          )}
        </div>
      )}

      {/* 5. Already Integrated Banner */}
      {!isPendingApproval && !isMediaTask && task.isIntegrated && (
        <div className="flex items-center justify-between p-2 rounded-lg bg-emerald-950/20 border border-emerald-500/30 text-emerald-200 text-[11px] gap-2">
          <div className="flex items-center gap-2 min-w-0">
            <CheckCircle2 size={12} className="text-emerald-400 flex-shrink-0" />
            <span className="font-bold text-emerald-400 flex-shrink-0">Integrated:</span>
            <span className="truncate">
              Changes on {task.worktreeBranch || 'worktree'} have been successfully integrated into {task.baseBranch || 'main'}.
            </span>
          </div>
          <div className="flex items-center gap-2 flex-shrink-0">
            <span className="px-2 py-0.5 rounded bg-emerald-500/20 text-emerald-300 font-mono text-[9px] font-bold border border-emerald-500/30">
              Up-to-date
            </span>
            {onReopen && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  setIsReopenOpen(!isReopenOpen)
                }}
                className="flex items-center gap-1 px-2 py-0.5 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white text-[10px] font-medium transition-colors border border-slate-700"
                title="Reopen task"
              >
                <RotateCcw size={10} className="text-indigo-400" />
                <span>{isReopenOpen ? 'Cancel' : 'Reopen Task'}</span>
              </button>
            )}
          </div>
        </div>
      )}
      {!isPendingApproval && task.isIntegrated && isReopenOpen && onReopen && (
        <div className="flex items-center gap-2 p-2 rounded bg-slate-900 border border-slate-800 animate-in fade-in duration-200">
          <input
            type="text"
            value={reopenFeedback}
            onClick={(e) => e.stopPropagation()}
            onChange={(e) => setReopenFeedback(e.target.value)}
            placeholder="Instructions for reopening..."
            className="flex-1 bg-slate-950 border border-slate-800 rounded px-2.5 py-1 text-xs text-white placeholder-slate-500 focus:outline-none focus:border-indigo-500 font-mono"
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.stopPropagation()
                onReopen(reopenFeedback.trim() || undefined)
                setReopenFeedback('')
                setIsReopenOpen(false)
              }
            }}
          />
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation()
              onReopen(reopenFeedback.trim() || undefined)
              setReopenFeedback('')
              setIsReopenOpen(false)
            }}
            className="px-3 py-1 rounded bg-indigo-600 hover:bg-indigo-500 text-white font-bold text-[10px] transition-colors flex-shrink-0"
          >
            Reopen
          </button>
        </div>
      )}

      {/* 6. Worktree & Git Changes Bar (ONLY show when there are changes waiting to be committed or unintegrated commits) */}
      {!isPendingApproval && !isMediaTask && (task.isDirty || hasUnintegrated) && (
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
                  Not integrated ({task.unintegratedCommits} commit(s))
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
              : ['Verified scope and authored changes']
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
              : ['Awaiting code review and merge']
            ).map((item, idx) => (
              <div key={idx} className="flex items-start gap-1.5 text-slate-400 leading-tight">
                <span className="text-amber-400">⋯</span>
                <span className="truncate">{item}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 6. Expected Deliverables Slot Rendering */}
      {task.deliverables && task.deliverables.length > 0 && (
        <div className="space-y-1.5">
          <div className="flex items-center justify-between text-[10px] font-semibold text-slate-500">
            <span className="uppercase font-mono tracking-wider">
              Deliverables ({task.deliverables.length})
            </span>
            <span className="font-mono text-[9px]">Contract: {task.outcomeType || 'media'}</span>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-2">
            {task.deliverables.map((d) => (
              <div
                key={d.id}
                onClick={(e) => {
                  e.stopPropagation()
                  if (d.status === 'ready' || d.status === 'accepted') {
                    onPreviewDeliverable?.(d)
                  }
                }}
                className={`p-2 rounded-xl border transition-all ${
                  d.status === 'generating'
                    ? 'border-blue-500/40 bg-blue-950/20 animate-pulse'
                    : d.status === 'pending'
                    ? 'border-dashed border-slate-800 bg-slate-950/40'
                    : 'border-slate-800 bg-[#070b14] hover:border-blue-500/50 cursor-pointer shadow-sm hover:shadow-md'
                }`}
              >
                {d.status === 'generating' ? (
                  <div className="flex flex-col items-center justify-center p-3 text-center space-y-1.5 min-h-[90px]">
                    <Loader2 size={16} className="animate-spin text-blue-400" />
                    <span className="text-[11px] font-mono font-semibold text-blue-200 truncate w-full px-1">{d.title}</span>
                    <span className="text-[9px] font-mono text-blue-400/80 uppercase tracking-wider">Generating media...</span>
                  </div>
                ) : d.status === 'pending' ? (
                  <div className="flex flex-col items-center justify-center p-3 text-center space-y-1 min-h-[90px]">
                    {d.type === 'video' ? (
                      <Film size={16} className="text-indigo-400" />
                    ) : d.type === 'pr' || d.type === 'code' ? (
                      <GitPullRequest size={16} className="text-emerald-400" />
                    ) : d.type === 'report' ? (
                      <FileText size={16} className="text-amber-400" />
                    ) : d.type === 'artifact' ? (
                      <Palette size={16} className="text-purple-400" />
                    ) : (
                      <ImageIcon size={16} className="text-slate-500" />
                    )}
                    <span className="text-[11px] font-mono font-semibold text-slate-300 truncate w-full px-1">{d.title}</span>
                    <span className="text-[9px] font-mono text-slate-500">
                      {d.type === 'pr' || d.type === 'code' ? 'Pending Acceptance • Code PR' : 'Pending Acceptance'}
                    </span>
                  </div>
                ) : (
                  <div className="space-y-1.5">
                    <div className="relative aspect-video w-full overflow-hidden rounded-lg bg-black/80 flex items-center justify-center">
                      {d.previewUrl || d.mediaUrl || (d.thumbnailType && d.thumbnailType.startsWith('data:')) ? (
                        <img
                          src={d.previewUrl || d.mediaUrl || d.thumbnailType}
                          alt={d.title}
                          className="w-full h-full object-cover transition-transform group-hover:scale-105 duration-300"
                        />
                      ) : (
                        <DeliverableThumbnail type={d.thumbnailType} duration={d.duration} />
                      )}
                      <div className="absolute inset-0 bg-black/40 opacity-0 hover:opacity-100 flex items-center justify-center transition-opacity">
                        <span className="flex items-center gap-1 px-2 py-0.5 rounded bg-black/80 text-[10px] font-mono font-bold text-white border border-white/20">
                          <Eye size={10} /> Preview
                        </span>
                      </div>
                      <span className="absolute top-1 right-1 z-10 px-1 py-0.5 rounded bg-emerald-950/80 border border-emerald-500/40 text-emerald-300 text-[8px] font-mono uppercase font-bold">
                        Ready
                      </span>
                    </div>
                    <div className="text-[11px] font-semibold text-white truncate px-0.5" title={d.title}>{d.title}</div>
                    <div className="flex items-center justify-between text-[9px] text-slate-400 font-mono px-0.5">
                      <span className="text-blue-400 uppercase">{d.type}</span>
                      <span className="text-slate-500 group-hover:text-slate-300">Click to view</span>
                    </div>
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

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

        {task.sessionId && (
          <span
            onClick={(e) => {
              e.stopPropagation()
              onOpenChat?.()
            }}
            className="text-blue-400 hover:text-blue-300 hover:underline cursor-pointer flex-shrink-0"
          >
            sess_{task.sessionId.slice(0, 8)}
          </span>
        )}
      </div>
    </div>
  )
}

/**
 * Right AI Chat Panel: Supports switching between Executive Project Orchestrator
 * and individual Task Worker sessions with a back-button navigation bar.
 */
function OrchestratorChatSidebar({
  sessionId,
  project,
  activeTask,
  onBackToOrchestrator,
  onOrchestratorSessionReset,
}: {
  sessionId: string
  project?: ProjectSummary
  activeTask?: RunningTask
  onBackToOrchestrator?: () => void
  onOrchestratorSessionReset?: (newSessionId: string) => void
}) {
  const [error, setError] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [clearingContext, setClearingContext] = useState(false)
  const [clearSuccess, setClearSuccess] = useState(false)

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

  const contextStats = useMemo(() => {
    if (!sessionUsage) {
      return { totalTokens: 0, contextWindow: 0, percent: 0, label: '0 tokens used' }
    }
    const total = Number(sessionUsage.total_tokens ?? sessionUsage.totalTokens ?? 0)
    const windowSize = Number(sessionUsage.context_window ?? sessionUsage.contextWindow ?? 0)
    const percent = windowSize > 0 && total > 0 ? Math.min(100, Math.round((total / windowSize) * 100)) : 0
    let label = `${total.toLocaleString()} tokens used`
    if (windowSize > 0) {
      label = `${total.toLocaleString()} / ${formatContextWindow(windowSize)} ctx (${percent}%)`
    }
    return { totalTokens: total, contextWindow: windowSize, percent, label }
  }, [sessionUsage])

  const handleClearContext = async () => {
    if (!project?.id || clearingContext) return
    setClearingContext(true)
    setClearSuccess(false)
    try {
      const res = await requestJson<{ ok: boolean; session_id: string }>(
        `/v3/projects/${project.id}/orchestrator:clear-context`,
        { method: 'POST' }
      )
      if (res?.session_id) {
        onOrchestratorSessionReset?.(res.session_id)
        setClearSuccess(true)
        setTimeout(() => setClearSuccess(false), 2500)
      } else {
        const clientRequestId = `desktop-v3-create:${crypto.randomUUID()}`
        const sessRes = await requestJson<{ session: { id: string } }>('/v3/sessions', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            client_request_id: clientRequestId,
            title: `Project Orchestrator: ${project.name}`,
            workspace_path: project.repoPath || '.',
            agent_name: 'system-orchestrator',
            metadata: {
              project_id: project.id,
              role: 'project_orchestrator',
            },
          }),
        })
        if (sessRes?.session?.id) {
          const sid = sessRes.session.id
          await requestJson(`/v3/projects/${project.id}`, {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ primary_session_id: sid }),
          }).catch(() => {})
          onOrchestratorSessionReset?.(sid)
          setClearSuccess(true)
          setTimeout(() => setClearSuccess(false), 2500)
        }
      }
    } catch (e) {
      console.warn('Failed to clear orchestrator context:', e)
    } finally {
      setClearingContext(false)
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
      className="relative flex w-[440px] flex-shrink-0 flex-col overflow-hidden rounded-3xl border border-slate-800/80 bg-[#0d121f] shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]"
      style={{
        '--app-bg': '#0d121f',
        '--app-bg-alt': '#090d16',
        '--app-surface': '#111728',
        '--app-surface-subtle': '#0a0f1d',
        '--app-border': 'rgba(255, 255, 255, 0.08)',
        '--app-border-muted': 'rgba(255, 255, 255, 0.05)',
      } as React.CSSProperties}
    >
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
              <span>Orchestrator</span>
            </button>
            <div className="flex flex-col min-w-0">
              <span className="font-bold text-white truncate">{activeTask.title}</span>
              <span className="text-[10px] text-slate-400 font-mono">
                {activeTask.workerName || '@Worker'} • {activeTask.status} • {activeTask.elapsed}
              </span>
            </div>
          </div>
          <span className="text-[9px] font-mono uppercase px-2 py-0.5 rounded bg-blue-500/10 text-blue-400 border border-blue-500/20 font-bold">
            {activeTask.agentType}
          </span>
        </div>
      ) : (
        <div className="flex flex-col border-b border-slate-800 bg-[#0a0f1d] text-xs">
          <div className="flex items-center justify-between p-3 pb-2">
            <div className="flex items-center gap-2">
              <div className="h-2 w-2 rounded-full bg-emerald-400" />
              <span className="font-bold text-white">Project Orchestrator</span>
              <span className="text-[10px] text-slate-400 font-mono">({project?.name})</span>
            </div>
            <span className="text-[9px] font-mono uppercase px-2 py-0.5 rounded bg-slate-800 text-slate-300 font-bold">
              Executive
            </span>
          </div>
          {/* Orchestrator Context Status & Clear Context Action */}
          <div className="flex items-center justify-between px-3 py-1.5 bg-[#080d19]/90 border-t border-slate-800/60 text-[11px]">
            <div className="flex items-center gap-2 min-w-0">
              <span className="text-slate-400 text-[10px] uppercase font-semibold tracking-wider">Context:</span>
              <span className="font-mono text-slate-200 text-[11px] truncate" data-testid="orchestrator-context-label">
                {contextStats.label}
              </span>
              {contextStats.percent > 0 && (
                <div className="w-12 h-1.5 rounded-full bg-slate-800 overflow-hidden flex-shrink-0">
                  <div
                    className={`h-full rounded-full transition-all duration-300 ${
                      contextStats.percent > 80 ? 'bg-amber-400' : 'bg-blue-400'
                    }`}
                    style={{ width: `${contextStats.percent}%` }}
                  />
                </div>
              )}
            </div>
            <button
              onClick={handleClearContext}
              disabled={clearingContext}
              title="Clear orchestrator conversation context and start fresh"
              data-testid="clear-orchestrator-context-btn"
              className="flex items-center gap-1.5 px-2 py-0.5 rounded bg-slate-800 hover:bg-red-950/70 hover:text-red-200 hover:border-red-500/50 text-slate-300 transition-colors border border-slate-700/80 font-medium text-[10px] flex-shrink-0 disabled:opacity-50"
            >
              <RotateCcw size={10} className={clearingContext ? 'animate-spin text-amber-400' : 'text-slate-400'} />
              <span>{clearingContext ? 'Clearing...' : clearSuccess ? 'Cleared!' : 'Clear Context'}</span>
            </button>
          </div>
        </div>
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
        presentation="sidebar"
        sessionId={sessionId}
        session={currentSession}
        initialHydrateStatus={error ? 'error' : hydrating ? 'loading' : ready ? 'ready' : 'loading'}
        renderedMessages={messages}
        messagesLoaded={ready}
        loadedMessageCount={count}
        contextChip={
          activeTask
            ? {
                id: activeTask.id,
                label: activeTask.title,
                kind: 'task',
                description: `Task Session (${activeTask.agentType}) for ${activeTask.title}`,
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
        metadata={{
          orchestrate_view: true,
          ...(project ? { project_id: project.id } : {}),
          ...(activeTask ? { task_id: activeTask.id } : {}),
        }}
      />
    </aside>
  )
}

export function OrchestrateView({
  workspaceSlug: workspaceSlugProp,
  onNavigateHome,
  initialThemeId = 'modern_navy',
}: OrchestrateViewProps) {
  const [currentThemeId] = useState<OrchestrateThemeId>(initialThemeId)
  const theme = ORCHESTRATE_THEMES[currentThemeId] || ORCHESTRATE_THEMES.modern_navy

  // Real user profile from /v1/auth/desktop/session
  const [userProfile, setUserProfile] = useState<{ id: string; name: string; email: string }>({
    id: '',
    name: 'Operator',
    email: 'local operator',
  })

  // Projects State
  const [projects, setProjects] = useState<ProjectSummary[]>([])
  const [selectedProjectId, setSelectedProjectId] = useState<string>('')
  const [, setIsLoadingProjects] = useState<boolean>(true)
  const selectedProject = projects.find((p) => p.id === selectedProjectId) ?? projects[0]

  // Project Onboarding & Creation State
  const [isOnboardingActive, setIsOnboardingActive] = useState<boolean>(false)
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

  const taskSessionIdsKey = useMemo(() => {
    const set = new Set<string>()
    for (const t of tasks) {
      if (t.sessionId) set.add(t.sessionId)
    }
    return Array.from(set).sort().join(',')
  }, [tasks])

  const liveTaskSessionsData = useDesktopV3CacheSelector(
    (state) => {
      const ids = taskSessionIdsKey ? taskSessionIdsKey.split(',').filter(Boolean) : []
      const result: Record<
        string,
        {
          sessionRecord?: (typeof state.sessionsById)[string]
          view?: (typeof state.sessionViewsById)[string]
          intent?: (typeof state.currentRunIntentBySession)[string]
          liveRun?: any
          planRecord?: (typeof state.plansBySession)[string]
        }
      > = {}
      for (const sid of ids) {
        const intent = state.currentRunIntentBySession?.[sid]
        result[sid] = {
          sessionRecord: state.sessionsById[sid],
          view: state.sessionViewsById?.[sid],
          intent,
          liveRun: intent?.run_id ? state.liveRunsBySession?.[sid]?.[intent.run_id] : undefined,
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

  // Automations State
  const [automations, setAutomations] = useState<RunningAutomation[]>([])

  // Middle canvas layout variant state
  const [middleVariant] = useState<MiddleCanvasVariant>('matrix')

  // Search & Filters
  const [searchQuery, setSearchQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'running' | 'needs_review' | 'queued' | 'completed'>('all')
  const [selectedTag] = useState<string>('all')

  // Matrix expandable drawer state
  const [expandedTaskId, setExpandedTaskId] = useState<string | null>(null)

  // Split Studio selected task state
  const [selectedTaskId, setSelectedTaskId] = useState<string>('')

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
    select: (state) => state.matches[state.matches.length - 1]?.params as { workspaceSlug?: string; swarmSection?: string } | undefined,
  }) ?? {}
  const navigate = useNavigate()
  const workspaceSlug = routeParams.workspaceSlug ?? workspaceSlugProp
  const activeNavTab: SwarmPage = isSwarmSection(routeParams.swarmSection) ? routeParams.swarmSection : 'home'
  const showFullMediaCenter = activeNavTab === 'media'
  const setActiveNavTab = (page: SwarmPage) => { void navigate(swarmPageLink(workspaceSlug, page)) }
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
  const [reviewError, setReviewError] = useState<{ id: string; message: string } | null>(null)
  const [editingScopePermissionId, setEditingScopePermissionId] = useState<string | null>(null)
  const [editedWorkspaceIds, setEditedWorkspaceIds] = useState<string[]>([])
  const [savingScope, setSavingScope] = useState(false)

  const handleUpdateWorkerWorkspaceScope = async (permissionId: string, proposal: AutomationV2Proposal, newWorkspaceIds: string[]) => {
    if (savingScope || newWorkspaceIds.length === 0) return
    setSavingScope(true)
    setReviewError(null)
    try {
      const primaryWs = newWorkspaceIds[0] || proposal.workspace_id
      const currentDoc = proposal.document
      const baseSettings = ((currentDoc as any)?.worker_v2 || currentDoc.automation_v2 || {}) as Record<string, unknown>
      const updatedDoc = {
        ...currentDoc,
        worker_v2: {
          ...baseSettings,
          workspace_id: primaryWs,
          workspace_ids: newWorkspaceIds,
        },
        automation_v2: {
          ...baseSettings,
          workspace_id: primaryWs,
          workspace_ids: newWorkspaceIds,
        },
      }
      await desktopAutomationV2.mutate({
        action: 'propose_automation',
        workspace_id: proposal.workspace_id,
        session_id: proposal.session_id,
        document: updatedDoc as any,
        review: {
          proposal_id: proposal.proposal_id,
          revision: proposal.revision,
          digest: proposal.digest,
        },
      })
      setEditingScopePermissionId(null)
    } catch (cause) {
      setReviewError({ id: permissionId, message: cause instanceof Error ? cause.message : 'Failed to update workspace scope.' })
    } finally {
      setSavingScope(false)
    }
  }

  const handleDecideReview = async (id: string, revision: number, digest: string, action: 'accept_automation' | 'decline_automation') => {
    if (reviewBusyId) return
    setReviewBusyId(id)
    setReviewError(null)
    try {
      await decidePendingWorkerReview(getDesktopV3CacheSnapshot(), id, revision, digest, action, (input) => desktopAutomationV2.mutate(input))
    } catch (cause) {
      setReviewError({ id, message: cause instanceof Error ? cause.message : 'Worker decision failed.' })
    } finally {
      setReviewBusyId(null)
    }
  }

  const [cloudWorkers, setCloudWorkers] = useState<StorageDiscoveredWorker[]>([])
  const [cloudWorkersError, setCloudWorkersError] = useState<string | null>(null)
  const [activatingWorkerId, setActivatingWorkerId] = useState<string | null>(null)

  const fetchCloudWorkers = useCallback(async () => {
    try {
      setCloudWorkersError(null)
      const workers = await listStorageWorkers()
      setCloudWorkers(workers)
    } catch (err) {
      setCloudWorkersError(err instanceof Error ? err.message : 'Failed to discover storage workers')
    }
  }, [])

  useEffect(() => {
    void fetchCloudWorkers()
  }, [fetchCloudWorkers])

  const handleActivateCloudWorker = async (workerId: string) => {
    setActivatingWorkerId(workerId)
    try {
      await activateStorageWorker(workerId)
      await fetchCloudWorkers()
    } catch (err) {
      console.error('Failed to activate cloud worker', err)
      setCloudWorkersError(err instanceof Error ? err.message : 'Failed to activate cloud worker')
    } finally {
      setActivatingWorkerId(null)
    }
  }

  // Deploy Task Modal State
  const queryClient = useQueryClient()
  const [isDeployModalOpen, setIsDeployModalOpen] = useState(false)
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
  const [newTaskPrompt, setNewTaskPrompt] = useState('')
  const [newTaskWorkspace, setNewTaskWorkspace] = useState('')
  const [imageAspectRatio, setImageAspectRatio] = useState<'1:1' | '16:9' | '9:16' | '4:3'>('16:9')
  const [imageVariants, setImageVariants] = useState<number>(1)
  const [imageResolution, setImageResolution] = useState<'1k' | '2k' | '4k'>('1k')
  const [soundDuration, setSoundDuration] = useState<number>(30)
  const [autoApproveTask, setAutoApproveTask] = useState<boolean>(false)
  const [isDeployingTask, setIsDeployingTask] = useState(false)
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
  const [defaultAudioModel, setDefaultAudioModel] = useState<string>('')
  const [saveImageAsDefault, setSaveImageAsDefault] = useState<boolean>(false)
  const [saveVideoAsDefault, setSaveVideoAsDefault] = useState<boolean>(false)
  const [localGenerationJobs, setLocalGenerationJobs] = useState<MediaGenerationJob[]>([])
  const [mediaViewerInitialMode, setMediaViewerInitialMode] = useState<QuickRouteMode | null>(null)
  const [mediaCatalogLoaded, setMediaCatalogLoaded] = useState<boolean>(false)
  const [, setIsSavingModelChoice] = useState<boolean>(false)

  const deployPreviewQuery = useQuery({
    queryKey: ['projects', selectedProject?.id, 'tasks:preview', {
      prompt: newTaskPrompt.trim() || 'New task',
      intent: taskIntent,
      featureSize: taskIntent === 'code' ? featureSize : undefined,
      model: newTaskModelOverride || undefined,
      agent: taskIntent === 'code' ? (featureSize === 'big' ? 'plan' : 'coder') : (taskIntent === 'audit' ? 'finder' : taskIntent),
      tier: taskIntent === 'code' ? (featureSize === 'big' ? 'complex' : 'direct') : (taskIntent === 'audit' ? 'discovery' : 'direct'),
    }],
    queryFn: async () => {
      const targetAgent = taskIntent === 'code'
        ? (featureSize === 'big' ? 'plan' : 'coder')
        : taskIntent === 'audit'
          ? 'finder'
          : taskIntent === 'image'
            ? 'image'
            : taskIntent === 'video'
              ? 'video'
              : 'sound'
      const targetOutcomeType = taskIntent === 'code'
        ? (featureSize === 'big' ? 'plan_spec' : 'code_pr')
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
      const res = await requestJson<{ task_plan: any; model_preview: BackendTaskModelPreview }>(
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
            workspace_path: newTaskWorkspace || selectedProject?.repoPath || '.',
          }),
        }
      )
      return res.model_preview
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
  const defaultImageOption = useMemo(
    () => imageModelOptions.find((opt) => opt.id === defaultImageModel),
    [imageModelOptions, defaultImageModel]
  )
  const defaultVideoOption = useMemo(
    () => videoModelOptions.find((opt) => opt.id === defaultVideoModel),
    [videoModelOptions, defaultVideoModel]
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

  // Active task object derived from activeTaskId
  const activeTask = useMemo(() => tasks.find((t) => t.id === activeTaskId), [tasks, activeTaskId])

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
            email: auth.account_scope_id || 'authenticated session',
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
            selected: true,
          }))
          setOnboardingWorkspaces(detected)
        }
      } catch {}

      // 3. Automations
      try {
        const autoRes = await requestJson<{ records?: any[] }>('/v3/automations/v2')
        if (autoRes?.records && !cancelled) {
          const mapped: RunningAutomation[] = autoRes.records.map((r: any) => ({
            id: r.id || 'automation',
            name: r.worker_v2?.name || r.name || 'Worker Automation',
            kind: (r.worker_v2?.kind || r.schedule?.kind || 'trigger') as any,
            status: (r.status || (r.paused ? 'idle' : 'running')) as any,
            nextRun: r.schedule?.cron || r.schedule?.interval || 'On demand',
            lastRun: r.last_run_at && !isNaN(new Date(r.last_run_at).getTime()) ? new Date(r.last_run_at).toLocaleTimeString() : 'Never',
            outputSummary: r.description || r.worker_v2?.definition?.goal || 'Automated background task',
            totalRuns: r.run_count || 0,
          }))
          setAutomations(mapped)
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
            primarySessionId: p.primary_session_id,
          }))
          setProjects(loaded)
          setSelectedProjectId(loaded[0].id)
          if (loaded[0].primarySessionId) {
            setActiveSessionId(loaded[0].primarySessionId)
          }
        } else {
          if (!cancelled) {
            setProjects([])
            setSelectedProjectId('')
            setActiveSessionId('')
            setIsOnboardingActive(true)
          }
        }
      } catch (err) {
        console.warn('Failed to load projects from Pebble:', err)
      }

      // 5. Image, Video & Audio Models Catalog and UI Defaults
      try {
        const [settingsRes, catalogRes] = await Promise.all([
          getUISettings(),
          requestJson<{ image_models?: any[]; video_generation_models?: any[]; video_models?: any[]; audio_models?: any[]; default_image_model?: string; default_video_model?: string; default_audio_model?: string }>('/v1/media/settings/catalog').catch(() => null),
        ])
        if (!cancelled) {
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

  // Auto-select first task if none selected or if selected task no longer exists
  useEffect(() => {
    if (tasks.length > 0 && (!selectedTaskId || !tasks.some((t) => t.id === selectedTaskId))) {
      setSelectedTaskId(tasks[0].id)
    }
  }, [tasks, selectedTaskId])

  const [mediaSyncError, setMediaSyncError] = useState<string | null>(null)
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

  const activeLeasesRef = useRef<Map<string, DesktopV3RealtimeSessionDemandLease>>(new Map())
  const hydratedSessionsRef = useRef<Set<string>>(new Set())

  const activeTaskSessionIdsKey = useMemo(() => {
    const set = new Set<string>()
    for (const t of tasks) {
      if (t.sessionId && (t.status === 'running' || t.status === 'in_progress' || t.id === selectedTaskId)) {
        set.add(t.sessionId)
      }
    }
    return Array.from(set).sort().join(',')
  }, [tasks, selectedTaskId])

  // Realtime session demand and plan hydration for active tasks (incremental, deduplicated, sorted)
  useEffect(() => {
    const currentSessionIds = new Set(
      activeTaskSessionIdsKey ? activeTaskSessionIdsKey.split(',').filter(Boolean) : []
    )
    let cancelled = false

    // 1. Remove leases for sessions no longer active
    for (const [sid, lease] of activeLeasesRef.current.entries()) {
      if (!currentSessionIds.has(sid)) {
        lease.release()
        activeLeasesRef.current.delete(sid)
      }
    }

    // 2. Identify new sessions that need hydration and leases
    const newSessionIds: string[] = []
    for (const sid of currentSessionIds) {
      if (!activeLeasesRef.current.has(sid)) {
        newSessionIds.push(sid)
      }
    }

    if (newSessionIds.length === 0) return

    // 3. Hydrate NEW sessions only
    newSessionIds.forEach((sid) => {
      if (!hydratedSessionsRef.current.has(sid)) {
        hydratedSessionsRef.current.add(sid)
        void hydrateDesktopV3ChildCard(sid, { activePlan: true, permissionSummary: true }).catch(() => undefined)
      }
    })

    // 4. Acquire realtime demand leases so live events stream for NEW sessions only
    void requireDesktopV3RealtimeControllerReady()
      .then((controller) => {
        if (cancelled) return
        newSessionIds.forEach((sid) => {
          if (!activeLeasesRef.current.has(sid) && currentSessionIds.has(sid)) {
            const ownerKey = `orchestrate-task:${sid}`
            try {
              activeLeasesRef.current.set(sid, controller.acquireSessionDemand(ownerKey, sid))
            } catch {
              // ignore
            }
          }
        })
      })
      .catch(() => undefined)

    return () => {
      cancelled = true
    }
  }, [activeTaskSessionIdsKey])

  // Cleanup all leases on unmount
  useEffect(() => {
    return () => {
      for (const lease of activeLeasesRef.current.values()) {
        lease.release()
      }
      activeLeasesRef.current.clear()
      hydratedSessionsRef.current.clear()
    }
  }, [])

  // Helper to ensure an active orchestrator session exists for a project
  const ensureOrchestratorSession = useCallback(async (project: ProjectSummary): Promise<string | null> => {
    if (project.primarySessionId) {
      setActiveSessionId(project.primarySessionId)
      return project.primarySessionId
    }
    const clientRequestId = `desktop-v3-create:${crypto.randomUUID()}`
    try {
      const sessRes = await requestJson<{ session: { id: string } }>('/v3/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          client_request_id: clientRequestId,
          title: `Project Orchestrator: ${project.name}`,
          workspace_id: project.primaryWorkspaceId || project.workspaces?.[0]?.workspace_id || undefined,
          workspace_path: project.repoPath || '.',
          agent_name: 'system-orchestrator',
          metadata: {
            project_id: project.id,
            role: 'project_orchestrator',
          },
        }),
      })
      if (sessRes?.session?.id) {
        const sid = sessRes.session.id
        setActiveSessionId(sid)
        await requestJson(`/v3/projects/${project.id}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ primary_session_id: sid }),
        }).catch(() => {})
        setProjects((prev) =>
          prev.map((p) => (p.id === project.id ? { ...p, primarySessionId: sid } : p))
        )
        return sid
      }
    } catch (e) {
      console.warn('Failed to ensure orchestrator session:', e)
    }
    return null
  }, [])

  // Synchronize active orchestrator session with selected project
  useEffect(() => {
    if (!selectedProject || isOnboardingActive) return
    void ensureOrchestratorSession(selectedProject)
  }, [selectedProject?.id, isOnboardingActive, ensureOrchestratorSession])

  // Task selection & Per-Task Session Switching
  const handleSelectTask = (task: RunningTask) => {
    setSelectedTaskId(task.id)
    if (task.sessionId) {
      setActiveSessionId(task.sessionId)
      setActiveTaskId(task.id)
    }
  }

  const handleBackToOrchestrator = () => {
    setActiveTaskId(null)
    if (selectedProject?.primarySessionId) {
      setActiveSessionId(selectedProject.primarySessionId)
    }
  }

  const handleOrchestratorSessionReset = useCallback((newSessionId: string) => {
    setActiveSessionId(newSessionId)
    if (selectedProject) {
      setProjects((prev) =>
        prev.map((p) => (p.id === selectedProject.id ? { ...p, primarySessionId: newSessionId } : p))
      )
    }
    void selectAndHydrateDesktopV3Session(newSessionId)
  }, [selectedProject?.id])

  // Media handling: file uploads, pasted docs, tagging, and studio library integration
  const handleFileUpload = async (files: FileList | null) => {
    if (!files || !selectedProject?.id) return
    setIsUploadingMedia(true)
    try {
      for (const file of Array.from(files)) {
        const isImage = file.type.startsWith('image/')
        const isVideo = file.type.startsWith('video/')
        const isAudio = file.type.startsWith('audio/')
        const kind = isImage ? 'image' : isVideo ? 'video' : isAudio ? 'audio' : 'doc'

        let dataUrl = ''
        let textData = ''
        if (kind === 'doc' || file.type.includes('text') || file.name.endsWith('.md') || file.name.endsWith('.txt')) {
          textData = await file.text()
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
          dataUrl = await new Promise<string>((resolve) => {
            const reader = new FileReader()
            reader.onload = () => resolve(reader.result as string)
            reader.readAsDataURL(file)
          })
        }

        const newMedia: ProjectTaskMediaRef = {
          id: `med_${Date.now()}_${Math.random().toString(36).slice(2, 7)}`,
          title: file.name,
          filename: file.name,
          kind,
          mediaType: file.type || (kind === 'doc' ? 'text/plain' : 'application/octet-stream'),
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

        const savedMedia = res?.media || newMedia
        desktopProjects.setOptimisticMedia(selectedProject.id, (prev) => [...prev, savedMedia])
        if (taskIntent === 'video') {
          // Exactly one initial image reference for video generation
          setTaggedMedia([savedMedia])
        } else {
          setTaggedMedia((prev) => [...prev, savedMedia])
        }
        desktopProjects.invalidate(selectedProject.id)
      }
    } catch (err) {
      console.warn('Failed to upload media:', err)
    } finally {
      setIsUploadingMedia(false)
    }
  }

  const handleDeleteUploadedMedia = async (mediaId: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/media/${mediaId}`, {
        method: 'DELETE',
      })
      desktopProjects.setOptimisticMedia(selectedProject.id, (prev) => prev.filter((m) => m.id !== mediaId))
      setTaggedMedia((prev) => prev.filter((m) => m.id !== mediaId))
      desktopProjects.invalidate(selectedProject.id)
    } catch (err) {
      console.warn('Failed to delete uploaded media:', err)
    }
  }

  const handleAddPastedDoc = async () => {
    if (!selectedProject?.id || !pastedDocContent.trim()) return
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
      const savedMedia = res?.media || newMedia
      desktopProjects.setOptimisticMedia(selectedProject.id, (prev) => [...prev, savedMedia])
      setTaggedMedia((prev) => [...prev, savedMedia])
      setIsPasteDocOpen(false)
      setPastedDocTitle('')
      setPastedDocContent('')
      desktopProjects.invalidate(selectedProject.id)
    } catch (err) {
      console.warn('Failed to save pasted document:', err)
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

  const allMediaLibraryItems = useMemo<MediaLibraryItem[]>(() => {
    const items: MediaLibraryItem[] = []
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
  }, [tasks, uploadedMedia, selectedProject])

  const handleOpenDeliverableInMediaCenter = (d: MediaDeliverable, parentTask?: RunningTask, mode?: QuickRouteMode) => {
    const item = deliverableToMediaItem(d, parentTask, selectedProject)
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
        const finalVariantCount = action === 'iterate' ? (variantCount || 1) : 1
        const finalScenesCount = targetIntent === 'video' ? (scenesCount || 1) : undefined
        const finalSoundtrack = targetIntent === 'video' ? (soundtrack || undefined) : undefined

        const res = await requestJson<{ task: any }>(`/v3/projects/${selectedProject.id}/tasks`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            prompt: composedPrompt,
            workspace_path: selectedProject.repoPath || '.',
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
          if (!activeMediaViewerItem && !showFullMediaCenter && res.task.session_id) {
            setActiveSessionId(res.task.session_id)
            setActiveTaskId(res.task.id)
          }
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
      if (!t.attachedMedia || t.attachedMedia.length === 0) continue
      seenTaskIds.add(t.id)

      for (const am of t.attachedMedia) {
        const localJob = localGenerationJobs.find((job) => job.taskId === t.id && job.sourceId === am.id)
        let status: string = t.status
        if (t.status === 'failed') {
          status = 'failed'
        } else if (t.status === 'in_progress' || t.status === 'running') {
          const hasDelivs = t.deliverables && t.deliverables.length > 0
          const allReady = hasDelivs && t.deliverables?.every((d) => d.status === 'ready' || d.status === 'accepted')
          if (allReady) status = 'completed'
          else status = 'in_progress'
        } else if (t.status === 'completed' || t.status === 'needs_review') {
          status = t.lastError ? 'partial_failure' : 'completed'
        } else if (t.status === 'queued' || t.status === 'pending') {
          status = 'queued'
        }

        jobs.push({
          id: localJob?.id || `${t.id}_${am.id}`,
          taskId: t.id,
          prompt: localJob?.prompt || t.description || t.title,
          createdAt: localJob?.createdAt || t.createdAt,
          outputIds: t.deliverables?.filter((d) => (d.status === 'ready' || d.status === 'accepted') && (d.mediaUrl || d.previewUrl)).map((d) => d.id) || [],
          sourceId: am.id,
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
    setDefaultImageModel(newModel)
    setIsSavingModelChoice(true)
    try {
      await requestJson('/v1/desktop/ui-settings', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tools: { image: { default_model: newModel } } }),
      })
    } catch (err) {
      console.warn('Failed to save default image model:', err)
    } finally {
      setIsSavingModelChoice(false)
    }
  }

  const handleImageModelChange = async (newModel: string, alsoSetDefault = false) => {
    setSelectedImageModel(newModel)
    if (alsoSetDefault || saveImageAsDefault) {
      await handleSetImageAsDefault(newModel)
    }
  }

  const handleSetVideoAsDefault = videoDefaults.save

  const handleVideoModelChange = async (newModel: string, alsoSetDefault = false) => {
    videoDefaults.select(newModel)
    if (alsoSetDefault || saveVideoAsDefault) {
      await handleSetVideoAsDefault(newModel)
    }
  }

  const handleAudioModelChange = async (newModel: string) => {
    setSelectedAudioModel(newModel)
    setIsSavingModelChoice(true)
    try {
      await requestJson('/v1/desktop/ui-settings', {
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
    const prompt = newTaskPrompt.trim()
    if (!prompt || !selectedProject?.id) return
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

    setIsDeployingTask(true)
    try {
      const qualifiedModel = taskIntent === 'video'
        ? (resolveQualifiedVideoModel(selectedVideoOption, selectedVideoModel) || undefined)
        : taskIntent === 'image'
        ? (selectedImageModel || undefined)
        : taskIntent === 'sound'
        ? (selectedAudioModel || undefined)
        : (newTaskModelOverride.trim() || undefined)

      const targetAgent = taskIntent === 'code'
        ? (featureSize === 'big' ? 'plan' : 'coder')
        : taskIntent === 'audit'
          ? 'finder'
          : taskIntent === 'image'
            ? 'image'
            : taskIntent === 'video'
              ? 'video'
              : 'sound'

      const attachedMediaForTask = taggedMedia

      const res = await requestJson<{ task: any }>(`/v3/projects/${selectedProject.id}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt,
          workspace_path: newTaskWorkspace || selectedProject.repoPath || '.',
          intent: taskIntent,
          feature_size: taskIntent === 'code' ? featureSize : undefined,
          agent: targetAgent,
          video_type: taskIntent === 'video' ? (scenePrompts.length ? 'multipart' : 'single') : undefined,
          operation: taskIntent === 'video' ? 'create' : undefined,
          scenes: scenePrompts.length ? scenePrompts.map((scenePrompt, index) => ({ scene_number: index + 1, title: `Scene ${index + 1}`, prompt: scenePrompt })) : undefined,
          scenes_count: scenePrompts.length || undefined,
          enhance_prompt: taskIntent === 'video' ? enhanceVideoPrompt : undefined,
          aspect_ratio: taskIntent === 'image' ? imageAspectRatio : taskIntent === 'video' ? (videoAspectRatio && supportedVideoAspectRatios.includes(videoAspectRatio) ? videoAspectRatio : undefined) : undefined,
          resolution: taskIntent === 'image' ? imageResolution : taskIntent === 'video' ? (videoResolution && supportedVideoResolutions.includes(videoResolution) ? videoResolution : undefined) : undefined,
          variant_count: taskIntent === 'image' ? imageVariants : taskIntent === 'video' ? videoClipCount : undefined,
          duration_seconds: taskIntent === 'sound' ? soundDuration : taskIntent === 'video' ? (videoDuration > 0 && supportedVideoDurations.includes(videoDuration) ? videoDuration : undefined) : undefined,
          model: qualifiedModel,
          auto_approve: autoApproveTask,
          deploy_session: true,
          attached_media: attachedMediaForTask,
        }),
      })
      if (res?.task) {
        desktopProjects.invalidate(selectedProject.id)
        if (res.task.session_id) {
          setActiveSessionId(res.task.session_id)
          setActiveTaskId(res.task.id)
        }
      }
      setIsDeployModalOpen(false)
      setNewTaskPrompt('')
      setNewTaskModelOverride('')
      setTaggedMedia([])
    } finally {
      setIsDeployingTask(false)
    }
  }

  // Approve pending task and start execution run
  const handleApproveTask = async (taskId: string) => {
    if (!selectedProject?.id) return
    // Optimistic loading state: immediately mark task as in_progress
    desktopProjects.setOptimisticTasks(selectedProject.id, (prev) =>
      prev.map((t) =>
        t.id === taskId
          ? {
              ...t,
              status: 'in_progress' as const,
              deliverables: resolveOptimisticApprovedDeliverables(t),
            }
          : t
      )
    )
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/approve`, {
        method: 'POST',
      })
      desktopProjects.invalidate(selectedProject.id)
      const approvedTask = tasks.find((t) => t.id === taskId)
      if (approvedTask?.sessionId) {
        setActiveSessionId(approvedTask.sessionId)
        setActiveTaskId(approvedTask.id)
      }
    } catch (err) {
      console.warn('Approve task failed:', err)
      desktopProjects.invalidate(selectedProject.id)
    }
  }

  // Delete/discard task
  const handleDeleteTask = async (taskId: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}`, {
        method: 'DELETE',
      })
      desktopProjects.invalidate(selectedProject.id)
      if (activeTaskId === taskId) {
        handleBackToOrchestrator()
      }
    } catch (err) {
      console.warn('Delete task failed:', err)
    }
  }

  // Integrate / Promote task commits into target branch
  const handleIntegrateTask = async (taskId: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/integrate`, {
        method: 'POST',
      })
      desktopProjects.invalidate(selectedProject.id)
    } catch (err) {
      console.warn('Integrate task failed:', err)
    }
  }

  // Refine task with router or re-plan error
  const handleRefineTask = async (taskId: string, feedback?: string, errorSummary?: string) => {
    if (!selectedProject?.id) return
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/refine`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          feedback: feedback?.trim() || undefined,
          error_summary: errorSummary?.trim() || undefined,
        }),
      })
      desktopProjects.invalidate(selectedProject.id)
    } catch (err) {
      console.warn('Refine task failed:', err)
    }
  }

  // Reopen task back to in_progress
  const handleReopenTask = async (taskId: string, feedback?: string) => {
    if (!selectedProject?.id) return
    desktopProjects.setOptimisticTasks(selectedProject.id, (prev) =>
      prev.map((t) =>
        t.id === taskId
          ? {
              ...t,
              status: 'in_progress' as const,
              isIntegrated: false,
              startedAt: Date.now(),
            }
          : t
      )
    )
    try {
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/reopen`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ feedback }),
      })
      desktopProjects.invalidate(selectedProject.id)
      const reopenedTask = tasks.find((t) => t.id === taskId)
      if (reopenedTask?.sessionId) {
        setActiveSessionId(reopenedTask.sessionId)
        setActiveTaskId(reopenedTask.id)
        void hydrateDesktopV3ChildCard(reopenedTask.sessionId, { activePlan: true, permissionSummary: true }).catch(() => undefined)
      }
    } catch (err) {
      console.warn('Reopen task failed:', err)
      desktopProjects.invalidate(selectedProject.id)
    }
  }

  // Complete task explicitly
  const handleCompleteTask = async (taskId: string) => {
    if (!selectedProject?.id) return
    desktopProjects.setOptimisticTasks(selectedProject.id, (prev) =>
      prev.map((t) =>
        t.id === taskId
          ? { ...t, status: 'completed' as const }
          : t
      )
    )
    try {
      const task = tasks.find(item => item.id === taskId)
      const action = task?.deliverables?.some(item => item.type === 'video' || item.type === 'image') ? 'accept' : 'complete'
      await requestJson(`/v3/projects/${selectedProject.id}/tasks/${taskId}/${action}`, {
        method: 'POST',
      })
      desktopProjects.invalidate(selectedProject.id)
    } catch (err) {
      console.warn('Complete task failed:', err)
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

  // Real-time status transitions linked to V3 session lifecycles
  const liveTasks = useMemo(() => {
    return tasks.map((task) => {
      if (!task.sessionId) {
        return task
      }
      const data = liveTaskSessionsData[task.sessionId]
      const record = data?.sessionRecord
      const sess = record?.kind === 'full' ? record.session : undefined
      const view = data?.view
      const intent = data?.intent
      const liveRun = data?.liveRun
      const planRecord = data?.planRecord as any
      const planDoc = planRecord?.document

      const lifecycle = sess?.lifecycle as any

      // 1. Status synchronization:
      let status = task.status
      const isLifecycleActive = Boolean(
        lifecycle?.active === true ||
        intent?.status === 'running' ||
        view?.current_run_state?.status === 'running'
      )
      const hasReviewRequired = Boolean(
        planRecord?.status === 'waiting_review' ||
        lifecycle?.phase === 'needs_review' ||
        planDoc?.executionState?.status === 'waiting_review' ||
        planDoc?.checkpoints?.some((cp: any) => cp.status === 'needs_review')
      )

      if (task.status === 'pending_approval' || task.status === 'planning' || task.status === 'queued') {
        // Keep pending approval until explicitly approved
        status = task.status
      } else if (isLifecycleActive) {
        status = 'running'
      } else if (hasReviewRequired || (!isLifecycleActive && sess && (sess.message_count ?? 0) > 1)) {
        // Agent finished execution! Transition to needs_review, never directly to completed!
        if (task.isIntegrated) {
          status = 'completed'
        } else if (task.status === 'completed') {
          status = 'completed'
        } else {
          status = 'needs_review'
        }
      }

      // 2. Extract agent's live plan checkpoints & subtasks:
      let activePlanCheckpoints: RunningTaskPlanCheckpoint[] | undefined
      let totalSubtasks = 0
      let completedSubtasks = 0
      let activeSubtaskId = ''
      let activeCheckpointTitle = ''
      let activeSubtaskTitle = ''

      if (planDoc?.checkpoints && Array.isArray(planDoc.checkpoints)) {
        const activeCheckpointId = planDoc.activeCheckpointId || planDoc.executionState?.lastCheckpointId || ''
        activePlanCheckpoints = planDoc.checkpoints.map((cp: any) => {
          const isCpActive = cp.id === activeCheckpointId
          if (isCpActive && cp.title) {
            activeCheckpointTitle = cp.title
          }
          const subs = (cp.subtasks || []).map((st: any) => {
            const isDone = st.status === 'completed' || st.completed === true
            if (isDone) completedSubtasks++
            totalSubtasks++
            if (cp.activeSubtaskId === st.id || st.status === 'in_progress') {
              activeSubtaskId = st.id
              activeSubtaskTitle = st.title
            }
            return {
              id: st.id,
              title: st.title,
              status: st.status || (st.completed ? 'completed' : 'pending'),
              completed: isDone,
            }
          })
          if (!cp.subtasks || cp.subtasks.length === 0) {
            totalSubtasks++
            if (cp.status === 'completed') completedSubtasks++
          }
          return {
            id: cp.id,
            title: cp.title,
            status: cp.status,
            subtasks: subs,
          }
        })
      }

      // 3. Extract tool calls & live streaming text:
      const toolCalls = liveRun ? Object.values(liveRun.toolCallsByCallId) : []
      const currentTool = [...toolCalls]
        .sort((a, b) => b.updatedAt - a.updatedAt)
        .map((t) => t.toolDisplay?.trim() || t.toolName?.trim() || '')
        .find(Boolean) || ''
      const toolActivitySummary = summarizeDesktopV3TaskToolActivity(toolCalls)
      const liveAssistantText = [
        ...(liveRun?.assistantSegments ?? []),
        ...(liveRun?.assistantDraft ? [liveRun.assistantDraft] : []),
      ]
        .sort((a, b) => (a.timelineSeq ?? 0) - (b.timelineSeq ?? 0) || a.updatedAt - b.updatedAt)
        .map((s) => s.content)
        .join('')
        .trim()
      const liveToolCalls = [...toolCalls]
        .sort((a, b) => a.updatedAt - b.updatedAt)
        .map((t) => t.toolDisplay?.trim() || t.toolName?.trim() || '')
        .filter(Boolean)
        .join('\n')

      // 4. Current focus:
      const currentFocus = activeSubtaskTitle || activeCheckpointTitle || toolActivitySummary || currentTool || (isLifecycleActive ? 'Executing autonomous mission plan...' : '')

      // 5. Plan progress percent:
      const planProgressPercent = totalSubtasks > 0 ? Math.round((completedSubtasks / totalSubtasks) * 100) : undefined

      // 6. Elapsed timer:
      const startedAt = intent?.started_at || lifecycle?.started_at || task.createdAt || (sess ? sess.created_at : undefined)
      const elapsedMs = intent?.duration_ms || (startedAt ? Date.now() - startedAt : 0)

      return {
        ...task,
        status,
        currentFocus: currentFocus || task.currentFocus,
        currentTool: currentTool || task.currentTool,
        liveAssistantText: liveAssistantText || task.liveAssistantText,
        liveToolCalls: liveToolCalls || task.liveToolCalls,
        toolActivitySummary: toolActivitySummary || task.toolActivitySummary,
        activePlanCheckpoints: activePlanCheckpoints && activePlanCheckpoints.length > 0 ? activePlanCheckpoints : task.activePlanCheckpoints,
        activeSubtaskId: activeSubtaskId || task.activeSubtaskId,
        planProgressPercent: planProgressPercent !== undefined ? planProgressPercent : task.planProgressPercent,
        subtasksCount: totalSubtasks > 0 ? { completed: completedSubtasks, total: totalSubtasks } : task.subtasksCount,
        startedAt,
        elapsedMs,
      }
    })
  }, [tasks, liveTaskSessionsData])

  // Filtered tasks
  const filteredTasks = useMemo(() => {
    return liveTasks.filter((task) => {
      const matchesSearch =
        searchQuery === '' ||
        task.title.toLowerCase().includes(searchQuery.toLowerCase()) ||
        task.subtitle?.toLowerCase().includes(searchQuery.toLowerCase()) ||
        task.id.toLowerCase().includes(searchQuery.toLowerCase())

      const matchesStatus = statusFilter === 'all' || task.status === statusFilter
      const matchesTag = selectedTag === 'all' || task.tags?.includes(selectedTag)

      return matchesSearch && matchesStatus && matchesTag
    })
  }, [liveTasks, searchQuery, statusFilter, selectedTag])

  const selectedTaskForSplit = useMemo(() => {
    return liveTasks.find((t) => t.id === selectedTaskId) || liveTasks[0]
  }, [liveTasks, selectedTaskId])

  const handleDeleteProject = async (projectId: string, e?: React.MouseEvent) => {
    e?.stopPropagation()
    try {
      await requestJson(`/v3/projects/${projectId}`, { method: 'DELETE' })
      desktopProjects.invalidate(projectId)
      setProjects((prev) => {
        const next = prev.filter((p) => p.id !== projectId)
        if (selectedProjectId === projectId) {
          if (next.length > 0) {
            setSelectedProjectId(next[0].id)
            if (next[0].primarySessionId) {
              setActiveSessionId(next[0].primarySessionId)
            }
          } else {
            setSelectedProjectId('')
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

    let newId = `proj_${Date.now()}`
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
      console.warn('Backend /v3/projects save failed:', err)
    }

    // Spawn primary orchestrator session with valid client_request_id and model preference
    let orchSessionId = ''
    const clientRequestId = `desktop-v3-create:${crypto.randomUUID()}`
    try {
      const sessRes = await requestJson<{ session: { id: string } }>('/v3/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          client_request_id: clientRequestId,
          title: `Project Orchestrator: ${payload.name}`,
          workspace_id: selectedWs[0]?.workspace_id || undefined,
          workspace_path: selectedWs[0]?.path || '.',
          agent_name: 'system-orchestrator',
          metadata: {
            project_id: newId,
            role: 'project_orchestrator',
          },
        }),
      })
      if (sessRes?.session?.id) {
        orchSessionId = sessRes.session.id
        await requestJson(`/v3/projects/${newId}`, {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ primary_session_id: orchSessionId }),
        }).catch(() => {})
      }
    } catch (e) {
      console.warn('Failed to spawn initial orchestrator session:', e)
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
      primarySessionId: orchSessionId,
    }

    setProjects((prev) => [newProject, ...prev])
    setSelectedProjectId(newProject.id)
    if (orchSessionId) {
      setActiveSessionId(orchSessionId)
      setActiveTaskId(null)
    }
    setIsOnboardingActive(false)
    setIsActivating(false)
  }

  // Count summaries
  const runningCount = liveTasks.filter((t) => t.status === 'running').length
  const reviewCount = liveTasks.filter((t) => t.status === 'needs_review').length
  const queuedCount = liveTasks.filter((t) => t.status === 'queued' || t.status === 'pending_approval').length
  const completedCount = liveTasks.filter((t) => t.status === 'completed').length

  return (
    <div
      className={`relative flex h-screen w-screen overflow-hidden p-3 gap-3 ${theme.bgClass} ${theme.textPrimaryClass} font-sans select-none`}
      style={theme.customVars as React.CSSProperties}
    >
      {/* ─────────────────────────────────────────────────────────────
          PANEL 1: LEFT SIDEBAR (NAVIGATION, PROJECTS & USER HUD)
         ───────────────────────────────────────────────────────────── */}
      <aside className="relative order-first flex w-72 flex-shrink-0 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
        {/* App Branding & Header */}
        <div className="p-3.5 border-b border-slate-800/80">
          <div className="flex items-center justify-between pb-2">
            <div className="flex items-center gap-2.5">
              <div className="flex h-7 w-7 items-center justify-center rounded-xl bg-blue-600/20 text-blue-400 border border-blue-500/30 shadow-[0_2px_8px_rgba(37,99,235,0.3)]">
                <Sparkles size={14} />
              </div>
              <div>
                <div className="text-xs font-bold tracking-tight text-white">Swarm Orchestrate</div>
                <div className="text-[10px] text-slate-400">Autonomous Project Coordination</div>
              </div>
            </div>

            <Link
              {...(workspaceSlug ? { to: '/$workspaceSlug' as const, params: { workspaceSlug } } : { to: '/' as const })}
              onClick={onNavigateHome}
              className="rounded-lg p-1.5 text-slate-400 hover:bg-slate-800 hover:text-slate-200 transition-all border border-slate-700/60"
              title="Back to Sessions"
              aria-label="Back to Sessions"
            >
              <X size={13} />
            </Link>
          </div>

          {/* Search Bar */}
          <div className="mt-2.5 flex items-center justify-between px-3 py-1.5 rounded-xl bg-[#090d16] border border-slate-800 text-xs text-slate-400">
            <div className="flex items-center gap-2">
              <Search size={13} className="text-slate-500" />
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search tasks & projects..."
                className="bg-transparent border-none text-[11px] text-slate-200 placeholder-slate-500 focus:outline-none w-full"
              />
            </div>
            {searchQuery && (
              <button onClick={() => setSearchQuery('')} className="text-slate-500 hover:text-slate-300">
                <X size={11} />
              </button>
            )}
          </div>
        </div>

        {/* Navigation Menu Links */}
        <div className="p-3 border-b border-slate-800/80 space-y-1" onClick={() => setIsOnboardingActive(false)}>
          <Link
            {...swarmPageLink(workspaceSlug, 'home')}
            aria-current={activeNavTab === 'home' ? 'page' : undefined}
            className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'home'
                ? 'bg-white/[0.08] text-white shadow-sm font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <Home size={15} />
            <span>Tasks & Canvas</span>
          </Link>
          <Link
            {...swarmPageLink(workspaceSlug, 'projects')}
            aria-current={activeNavTab === 'projects' ? 'page' : undefined}
            className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'projects'
                ? 'bg-white/[0.08] text-white shadow-sm font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <Folder size={15} />
            <span>Projects ({projects.length})</span>
          </Link>
          <Link
            {...swarmPageLink(workspaceSlug, 'workers')}
            aria-current={activeNavTab === 'workers' ? 'page' : undefined}
            className={`w-full flex items-center justify-between px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'workers'
                ? 'bg-blue-600/15 text-blue-400 border border-blue-500/30 font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <div className="flex items-center gap-2.5">
              <Bot size={15} />
              <span>Workers</span>
            </div>
            <div className="flex items-center gap-1.5">
              {pendingReviews.length > 0 ? (
                <span className="rounded-full bg-amber-500/20 text-amber-400 border border-amber-500/40 px-1.5 py-0.5 text-[10px] font-bold animate-pulse">
                  {pendingReviews.length} pending
                </span>
              ) : (
                <span className="text-[10px] font-mono text-slate-500">{automations.length}</span>
              )}
            </div>
          </Link>
          <Link
            {...swarmPageLink(workspaceSlug, 'deliverables')}
            aria-current={activeNavTab === 'deliverables' ? 'page' : undefined}
            className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'deliverables'
                ? 'bg-white/[0.08] text-white shadow-sm font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <Layers size={15} />
            <span>Deliverables ({liveTasks.flatMap((t) => t.deliverables || []).length})</span>
          </Link>
          <Link
            {...swarmPageLink(workspaceSlug, 'media')}
            aria-current={activeNavTab === 'media' ? 'page' : undefined}
            className={`w-full flex items-center justify-between px-3 py-2 rounded-xl text-xs font-medium transition-all ${activeNavTab === 'media' ? 'bg-white/[0.08] text-white shadow-sm font-semibold' : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'}`}
          >
            <div className="flex items-center gap-2.5">
              <Film size={15} className="text-blue-400" />
              <span>Media Studio & Library</span>
            </div>
            {allMediaLibraryItems.length > 0 && (
              <span className="font-mono text-[10px] px-1.5 py-0.5 rounded bg-blue-950/60 border border-blue-500/30 text-blue-300">
                {allMediaLibraryItems.length}
              </span>
            )}
          </Link>
          <Link
            {...swarmPageLink(workspaceSlug, 'settings')}
            aria-current={activeNavTab === 'settings' ? 'page' : undefined}
            className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-xl text-xs font-medium transition-all ${
              activeNavTab === 'settings'
                ? 'bg-white/[0.08] text-white shadow-sm font-semibold'
                : 'text-slate-400 hover:text-slate-200 hover:bg-white/[0.04]'
            }`}
          >
            <Settings size={15} />
            <span>Project Charter</span>
          </Link>
        </div>

        {/* Projects Switcher Section */}
        <div className="p-3 border-b border-slate-800/80 flex-1 overflow-y-auto">
          <div className="flex items-center justify-between pb-2">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-500">
              Projects
            </span>
            <button
              onClick={() => {
                setIsOnboardingActive(true)
                setActiveNavTab('home')
              }}
              className={`flex h-5 w-5 items-center justify-center rounded-lg transition-all border ${
                isOnboardingActive
                  ? 'bg-blue-600 text-white border-blue-500 shadow-[0_0_8px_rgba(59,130,246,0.5)]'
                  : 'bg-slate-800/80 text-slate-400 hover:text-slate-200 hover:bg-slate-700 border-slate-700/60'
              }`}
              title="Add New Project"
            >
              <Plus size={12} />
            </button>
          </div>

          <div className="flex flex-col gap-1.5">
            {isOnboardingActive && (
              <div className="flex items-center justify-between p-2 text-left rounded-xl bg-blue-950/40 border border-blue-500/40 text-blue-200 shadow-sm">
                <div className="flex items-center gap-2.5 min-w-0">
                  <div className="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-lg bg-blue-600/30 text-blue-300 border border-blue-400/40">
                    <FolderPlus size={12} />
                  </div>
                  <span className="text-xs font-semibold truncate">{onboardingName || 'New Project'}</span>
                </div>
                <span className="text-[9px] uppercase tracking-wider font-mono px-1.5 py-0.5 rounded bg-blue-600/30 text-blue-300">
                  Draft
                </span>
              </div>
            )}
            {projects.map((proj) => {
              const isSelected = !isOnboardingActive && proj.id === selectedProjectId
              return (
                <div
                  key={proj.id}
                  onClick={() => {
                    setSelectedProjectId(proj.id)
                    setIsOnboardingActive(false)
                    setActiveTaskId(null)
                  }}
                  className={`group flex items-center justify-between p-2 text-left rounded-xl transition-all cursor-pointer ${
                    isSelected
                      ? 'bg-slate-800/90 border border-slate-700/80 text-white shadow-sm'
                      : 'border border-transparent hover:bg-slate-800/40 text-slate-400 hover:text-slate-200'
                  }`}
                >
                  <div className="flex items-center gap-2.5 min-w-0 flex-1">
                    <div
                      className={`flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-lg border transition-all ${
                        isSelected
                          ? 'bg-blue-600/20 text-blue-400 border-blue-500/30 shadow-[0_1px_6px_rgba(37,99,235,0.2)]'
                          : 'bg-slate-800/60 text-slate-500 border-slate-700/50 group-hover:text-slate-300'
                      }`}
                    >
                      <Folder size={12} />
                    </div>
                    <div className="flex flex-col min-w-0">
                      <span className="text-xs font-semibold truncate">{proj.name}</span>
                      <span className="text-[10px] text-slate-500 truncate font-mono">
                        {proj.linkedWorkspaces.length} workspace{proj.linkedWorkspaces.length === 1 ? '' : 's'}
                      </span>
                    </div>
                  </div>
                  <div className="flex items-center gap-1 flex-shrink-0">
                    {isSelected && (
                      <span className="h-2 w-2 rounded-full bg-blue-500 shadow-[0_0_6px_rgba(59,130,246,0.6)]" />
                    )}
                    <button
                      type="button"
                      onClick={(e) => handleDeleteProject(proj.id, e)}
                      className="opacity-0 group-hover:opacity-100 p-1 text-slate-400 hover:text-rose-400 rounded-md transition-all hover:bg-rose-500/10"
                      title="Delete Project"
                    >
                      <Trash2 size={12} />
                    </button>
                  </div>
                </div>
              )
            })}
            {projects.length === 0 && !isOnboardingActive && (
              <div className="p-3 text-center text-xs text-slate-500 border border-dashed border-slate-800 rounded-xl">
                No projects saved.
              </div>
            )}
          </div>
        </div>

        {/* Uploaded Media & Documents Shelf */}
        <div className="p-3 border-b border-slate-800/80">
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
              <label
                className="flex h-5 items-center gap-1 px-1.5 rounded-lg bg-blue-600/20 border border-blue-500/30 text-blue-400 hover:bg-blue-600/30 text-[10px] font-semibold cursor-pointer transition"
                title="Upload image, video, audio, or document"
              >
                <Upload size={10} />
                <span>Upload</span>
                <input
                  type="file"
                  multiple
                  className="hidden"
                  onChange={(e) => void handleFileUpload(e.target.files)}
                />
              </label>
              <button
                type="button"
                onClick={() => setIsPasteDocOpen(true)}
                className="flex h-5 items-center gap-1 px-1.5 rounded-lg bg-slate-800 border border-slate-700/60 text-slate-300 hover:text-white text-[10px] font-semibold transition"
                title="Paste Markdown / Text Document"
              >
                <FileText size={10} />
                <span>Paste</span>
              </button>
            </div>
          </div>

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
        <div className="p-3 border-t border-slate-800/80 flex items-center justify-between bg-[#0a0f1d]/50">
          <div className="flex items-center gap-2.5 min-w-0">
            <div className="flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full bg-blue-600/20 text-blue-400 font-bold text-[10px] border border-blue-500/30">
              {userProfile.name.slice(0, 2).toUpperCase()}
            </div>
            <div className="flex flex-col min-w-0">
              <span className="text-xs font-semibold text-slate-200 truncate">{userProfile.name}</span>
              <span className="text-[10px] text-slate-500 truncate">{userProfile.email}</span>
            </div>
          </div>
          <button className="text-slate-400 hover:text-slate-200 p-1 rounded-lg hover:bg-slate-800/60 transition-colors">
            <MoreHorizontal size={14} />
          </button>
        </div>
      </aside>

      {/* ─────────────────────────────────────────────────────────────
          PANEL 2: MIDDLE SECTION (CANVAS / TASKS / VIEWS)
         ───────────────────────────────────────────────────────────── */}
      {!showFullMediaCenter && <main className="relative flex flex-1 flex-col overflow-hidden rounded-3xl border bg-[#0d121f]/95 border-slate-800/80 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
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
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
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
                disabled={isActivating || onboardingWorkspaces.filter((w) => w.selected).length === 0}
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
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              {projects.map((p) => (
                <div
                  key={p.id}
                  onClick={() => {
                    setSelectedProjectId(p.id)
                    setActiveNavTab('home')
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
          /* WORKERS HUB TAB */
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-6 max-w-5xl mx-auto w-full">
            <div className="flex items-center justify-between pb-4 border-b border-slate-800">
              <div className="flex items-center gap-3">
                <div className="h-9 w-9 rounded-xl bg-blue-600/20 text-blue-400 flex items-center justify-center border border-blue-500/30">
                  <Bot size={18} />
                </div>
                <div>
                  <h2 className="text-base font-bold text-white tracking-tight">Workers & Autonomous Fleet</h2>
                  <p className="text-xs text-slate-400 mt-0.5">
                    Autonomous workers, on-demand trigger endpoints, and cloud deployments
                  </p>
                </div>
              </div>
              <div className="flex items-center gap-2">
                {pendingReviews.length > 0 && (
                  <span className="rounded-full bg-amber-500/15 text-amber-400 border border-amber-500/30 px-2.5 py-1 text-xs font-semibold flex items-center gap-1.5 animate-pulse">
                    <span className="h-1.5 w-1.5 rounded-full bg-amber-400" />
                    {pendingReviews.length} Proposal Pending Review
                  </span>
                )}
                <span className="rounded-lg bg-slate-900 border border-slate-800 px-2.5 py-1 text-xs text-slate-400 font-mono">
                  {automations.length} Registered
                </span>
              </div>
            </div>

            {/* PENDING WORKER PROPOSALS SECTION */}
            {pendingReviews.length > 0 && (
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <span className="text-[11px] font-bold uppercase tracking-wider text-amber-400 flex items-center gap-1.5">
                    <Sparkles size={13} />
                    Pending Worker Proposals Awaiting Acceptance
                  </span>
                </div>

                {pendingReviews.map(({ permission, proposal }) => {
                  const doc = proposal.document
                  const schedule = doc?.automation_v2?.schedule
                  const isTrigger = schedule?.kind === 'trigger'
                  const isInterval = schedule?.kind === 'interval'
                  const isCron = schedule?.kind === 'cron'
                  const scheduleText = isTrigger
                    ? 'On-Demand Trigger'
                    : isInterval
                      ? `Every ${Math.round((schedule?.interval_seconds || 60) / 60)}m`
                      : isCron
                        ? `Cron (${schedule?.cron})`
                        : 'On-Demand'

                  const goalLower = (doc?.info?.goal || '').toLowerCase()
                  const titleLower = (doc?.title || '').toLowerCase()
                  const isCloudTarget =
                    goalLower.includes('cloud') ||
                    goalLower.includes('gcs') ||
                    goalLower.includes('gcp') ||
                    titleLower.includes('cloud')
                  const hasTwitter =
                    goalLower.includes('tweet') ||
                    goalLower.includes('twitter') ||
                    goalLower.includes('social') ||
                    titleLower.includes('social')

                  return (
                    <div
                      key={permission.id}
                      className="rounded-2xl border border-amber-500/30 bg-gradient-to-b from-amber-500/[0.06] to-slate-950/80 p-5 shadow-xl space-y-4"
                    >
                      <div className="flex items-start justify-between gap-4">
                        <div className="flex items-start gap-3">
                          <div className="h-10 w-10 rounded-xl bg-amber-500/20 text-amber-400 flex items-center justify-center border border-amber-500/40 shrink-0 font-bold text-base">
                            🤖
                          </div>
                          <div>
                            <div className="flex items-center gap-2">
                              <h3 className="text-base font-bold text-white">{doc?.title || 'Autonomous Worker'}</h3>
                              <span className="rounded-md bg-amber-500/20 px-2 py-0.5 text-[10px] font-semibold text-amber-300 border border-amber-500/30">
                                Revision {proposal.revision}
                              </span>
                            </div>
                            <p className="text-xs text-slate-300 mt-1 leading-relaxed">
                              {doc?.info?.goal || 'No description provided.'}
                            </p>
                          </div>
                        </div>
                      </div>

                      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-2.5 pt-1">
                        <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-3">
                          <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 block mb-1">
                            Execution Mode
                          </span>
                          <div className="flex items-center gap-1.5 text-xs font-semibold text-blue-400">
                            <Zap size={13} />
                            <span>{scheduleText}</span>
                          </div>
                        </div>

                        <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-3">
                          <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 block mb-1">
                            Target Environment
                          </span>
                          <div className="flex items-center gap-1.5 text-xs font-semibold text-emerald-400">
                            <span>{isCloudTarget ? '☁️ Cloud Run (GCP)' : '💻 Local Workstation'}</span>
                          </div>
                        </div>

                        <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-3">
                          <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 block mb-1">
                            Required Secrets
                          </span>
                          <div
                            className="flex items-center gap-1.5 text-xs font-mono text-purple-400 truncate"
                            title={hasTwitter ? 'twitter-api-key, twitter-access-token' : 'Standard environment'}
                          >
                            <span>{hasTwitter ? '🔑 twitter-api-key...' : '🔒 Default machine identity'}</span>
                          </div>
                        </div>

                        <div className="rounded-xl border border-slate-800 bg-slate-900/60 p-3">
                          <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 block mb-1">
                            Bound Project / Repo
                          </span>
                          <div
                            className="flex items-center gap-1.5 text-xs font-mono text-slate-300 truncate"
                            title={selectedProject?.name || proposal.workspace_id}
                          >
                            <Folder size={12} className="text-slate-500 shrink-0" />
                            <span className="truncate">{selectedProject?.name || 'Assigned workspace'}</span>
                          </div>
                        </div>
                      </div>

                      {/* SCOPED PROJECT WORKSPACES & WORKSPACE SCOPE REVIEW */}
                      {(() => {
                        const projectWorkspaces = selectedProject?.workspaces || []
                        const scopedWsIds = proposal.workspace_ids && proposal.workspace_ids.length > 0
                          ? proposal.workspace_ids
                          : (proposal.workspace_id ? [proposal.workspace_id] : [])
                        const isEditingScope = editingScopePermissionId === permission.id

                        return (
                          <div className="rounded-xl border border-slate-800 bg-slate-900/70 p-3.5 space-y-3">
                            <div className="flex items-center justify-between">
                              <div className="flex items-center gap-2">
                                <FolderTree size={14} className="text-blue-400" />
                                <span className="text-xs font-bold text-slate-200">
                                  Scoped Project Workspaces ({scopedWsIds.length} of {Math.max(projectWorkspaces.length, scopedWsIds.length)} Active)
                                </span>
                              </div>
                              <button
                                type="button"
                                onClick={() => {
                                  if (isEditingScope) {
                                    setEditingScopePermissionId(null)
                                  } else {
                                    setEditingScopePermissionId(permission.id)
                                    setEditedWorkspaceIds(scopedWsIds)
                                  }
                                }}
                                className="px-2.5 py-1 rounded-lg border border-slate-700 bg-slate-800 hover:bg-slate-700 text-[11px] font-semibold text-slate-300 hover:text-white transition-all flex items-center gap-1.5"
                              >
                                <Edit3 size={11} />
                                <span>{isEditingScope ? 'Cancel Scoping' : 'Suggest Changes / Edit Workspaces'}</span>
                              </button>
                            </div>

                            <p className="text-[11px] text-slate-400 leading-relaxed">
                              The orchestrator scoped this worker to specific directories within project{' '}
                              <strong className="text-slate-200">{selectedProject?.name || 'current project'}</strong>.
                              Not all workspaces are required. You can customize the included workspaces below before registering.
                            </p>

                            {isEditingScope ? (
                              <div className="space-y-2.5 pt-1">
                                <div className="space-y-1.5">
                                  {projectWorkspaces.length > 0 ? (
                                    projectWorkspaces.map((ws) => {
                                      const wsId = ws.workspace_id || ws.path
                                      const isChecked = editedWorkspaceIds.includes(wsId)
                                      return (
                                        <label
                                          key={wsId}
                                          className={`flex items-center justify-between p-2.5 rounded-xl border text-xs cursor-pointer transition-all ${
                                            isChecked
                                              ? 'border-blue-500/50 bg-blue-500/10 text-white'
                                              : 'border-slate-800 bg-slate-950/60 text-slate-400 hover:border-slate-700'
                                          }`}
                                        >
                                          <div className="flex items-center gap-2.5">
                                            <input
                                              type="checkbox"
                                              checked={isChecked}
                                              onChange={(e) => {
                                                if (e.target.checked) {
                                                  setEditedWorkspaceIds((prev) => [...prev, wsId])
                                                } else {
                                                  setEditedWorkspaceIds((prev) => prev.filter((id) => id !== wsId))
                                                }
                                              }}
                                              className="rounded border-slate-700 bg-slate-900 text-blue-600 focus:ring-0"
                                            />
                                            <span className="font-mono text-xs">{ws.path}</span>
                                            {ws.role && (
                                              <span className="px-1.5 py-0.5 rounded text-[10px] uppercase font-mono bg-slate-800 text-slate-400">
                                                {ws.role}
                                              </span>
                                            )}
                                          </div>
                                          <span className="text-[10px] font-semibold">
                                            {isChecked ? (
                                              <span className="text-emerald-400">Included</span>
                                            ) : (
                                              <span className="text-slate-500">Excluded</span>
                                            )}
                                          </span>
                                        </label>
                                      )
                                    })
                                  ) : (
                                    <div className="text-xs text-slate-500">No additional project workspaces discovered.</div>
                                  )}
                                </div>
                                <div className="flex items-center justify-end gap-2 pt-1">
                                  <button
                                    type="button"
                                    disabled={savingScope || editedWorkspaceIds.length === 0}
                                    onClick={() => handleUpdateWorkerWorkspaceScope(permission.id, proposal, editedWorkspaceIds)}
                                    className="px-3 py-1.5 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-xs font-bold transition-all disabled:opacity-50 flex items-center gap-1.5"
                                  >
                                    {savingScope ? <Loader2 size={12} className="animate-spin" /> : <Check size={12} />}
                                    <span>Apply Workspace Scope</span>
                                  </button>
                                </div>
                              </div>
                            ) : (
                              <div className="flex flex-wrap gap-2 pt-1">
                                {projectWorkspaces.length > 0 ? (
                                  projectWorkspaces.map((ws) => {
                                    const wsId = ws.workspace_id || ws.path
                                    const isIncluded = scopedWsIds.includes(wsId) || (!proposal.workspace_ids && ws.workspace_id === proposal.workspace_id)
                                    return (
                                      <div
                                        key={wsId}
                                        className={`flex items-center gap-2 px-2.5 py-1.5 rounded-xl border text-xs font-mono transition-all ${
                                          isIncluded
                                            ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
                                            : 'border-slate-800 bg-slate-900/40 text-slate-500 line-through opacity-60'
                                        }`}
                                        title={ws.path}
                                      >
                                        <Folder size={12} className={isIncluded ? 'text-emerald-400' : 'text-slate-600'} />
                                        <span className="truncate max-w-[220px]">{ws.label || ws.path.split('/').pop() || ws.path}</span>
                                        <span className={`text-[10px] px-1.5 py-0.5 rounded font-sans font-semibold ${
                                          isIncluded ? 'bg-emerald-500/20 text-emerald-300' : 'bg-slate-800 text-slate-500'
                                        }`}>
                                          {isIncluded ? 'Scoped' : 'Excluded'}
                                        </span>
                                      </div>
                                    )
                                  })
                                ) : (
                                  <div className="flex items-center gap-2 px-2.5 py-1.5 rounded-xl border border-emerald-500/40 bg-emerald-500/10 text-emerald-300 text-xs font-mono">
                                    <Folder size={12} className="text-emerald-400" />
                                    <span>{proposal.workspace_id}</span>
                                    <span className="text-[10px] px-1.5 py-0.5 rounded font-sans font-semibold bg-emerald-500/20 text-emerald-300">
                                      Scoped
                                    </span>
                                  </div>
                                )}
                              </div>
                            )}
                          </div>
                        )
                      })()}

                      {/* PLANNED PIPELINE TASKS / LONG TERM TASK CONTRACT */}
                      {doc?.checkpoints && doc.checkpoints.length > 0 && (
                        <div className="rounded-xl border border-slate-800 bg-slate-900/50 p-3 text-xs space-y-2">
                          <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 block">
                            📌 Pre-Configured Worker Pipeline ({doc.checkpoints.length} Step{doc.checkpoints.length === 1 ? '' : 's'})
                          </span>
                          <div className="space-y-1.5">
                            {doc.checkpoints.map((cp, idx) => (
                              <div key={cp.id || idx} className="rounded-lg border border-slate-800/80 bg-slate-950/60 p-2 text-xs">
                                <div className="font-semibold text-slate-200">
                                  {idx + 1}. {cp.title}
                                </div>
                                {cp.tasks && cp.tasks.length > 0 && (
                                  <ul className="mt-1 space-y-0.5 text-[11px] text-slate-400 list-disc list-inside">
                                    {cp.tasks.map((task, tIdx) => (
                                      <li key={tIdx}>{task}</li>
                                    ))}
                                  </ul>
                                )}
                              </div>
                            ))}
                          </div>
                        </div>
                      )}

                      <div className="rounded-xl border border-slate-800/80 bg-[#070b14] p-3 text-xs space-y-1.5">
                        <span className="text-[10px] font-semibold uppercase tracking-wider text-slate-400 block">
                          📋 Execution Pipeline Upon Acceptance
                        </span>
                        <ul className="space-y-1 text-slate-300 text-[11px] list-disc list-inside">
                          <li>Worker registers permanently to your project fleet.</li>
                          {isTrigger && (
                            <li>
                              Swarm automatically mints an isolated trigger key and writes it securely to{' '}
                              <code className="text-amber-300 bg-amber-500/10 px-1 py-0.5 rounded">
                                ~/.config/swarm/secrets.env
                              </code>{' '}
                              (mode 0600).
                            </li>
                          )}
                          {hasTwitter && (
                            <li>
                              Worker will authenticate with Twitter API v2 using your configured GCP Secret Manager credentials.
                            </li>
                          )}
                          <li>
                            Future worker tasks will pend deliverables with full tweet previews and interactive approval before publishing.
                          </li>
                        </ul>
                      </div>

                      {reviewError?.id === permission.id && (
                        <div className="rounded-lg bg-red-500/10 border border-red-500/30 p-2.5 text-xs text-red-400">
                          {reviewError.message}
                        </div>
                      )}

                      <div className="flex items-center justify-end gap-3 pt-2">
                        <button
                          type="button"
                          disabled={!!reviewBusyId}
                          onClick={() =>
                            handleDecideReview(permission.id, proposal.revision, proposal.digest, 'decline_automation')
                          }
                          className="px-3.5 py-1.5 rounded-xl border border-slate-700 bg-slate-800 text-slate-300 hover:text-white hover:bg-slate-700 text-xs font-semibold transition-all disabled:opacity-50"
                        >
                          Decline Proposal
                        </button>
                        <button
                          type="button"
                          disabled={!!reviewBusyId}
                          onClick={() =>
                            handleDecideReview(permission.id, proposal.revision, proposal.digest, 'accept_automation')
                          }
                          className="px-4 py-1.5 rounded-xl bg-gradient-to-r from-blue-600 to-indigo-600 hover:from-blue-500 hover:to-indigo-500 text-white text-xs font-bold shadow-lg shadow-blue-500/20 transition-all flex items-center gap-1.5 disabled:opacity-50"
                        >
                          {reviewBusyId === permission.id ? (
                            <>
                              <Loader2 size={13} className="animate-spin" />
                              <span>Registering...</span>
                            </>
                          ) : (
                            <>
                              <Check size={14} />
                              <span>Accept & Register Worker</span>
                            </>
                          )}
                        </button>
                      </div>
                    </div>
                  )
                })}
              </div>
            )}

            {/* ACTIVE REGISTERED WORKERS SECTION */}
            <div className="space-y-3">
              {cloudWorkersError && (
                <div className="p-3 bg-red-950/60 border border-red-500/40 rounded-xl text-xs text-red-300 flex items-center justify-between">
                  <span>Storage workers error: {cloudWorkersError}</span>
                  <button
                    type="button"
                    onClick={() => void fetchCloudWorkers()}
                    className="px-2.5 py-1 bg-red-800/60 hover:bg-red-700/60 rounded text-red-100 font-medium transition-colors"
                  >
                    Retry
                  </button>
                </div>
              )}
              <span className="text-[11px] font-bold uppercase tracking-wider text-slate-400">
                Registered Workers Fleet ({automations.length + cloudWorkers.length})
              </span>

              {automations.length > 0 || cloudWorkers.length > 0 ? (
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  {automations.map((a) => {
                    const isTrigger = a.kind === 'trigger'
                    return (
                      <div
                        key={a.id}
                        className="rounded-2xl border border-slate-800 bg-slate-900/40 p-4 hover:border-slate-700/80 transition-all space-y-3"
                      >
                        <div className="flex items-start justify-between">
                          <div className="flex items-center gap-2.5">
                            <div className="h-8 w-8 rounded-xl bg-blue-600/15 text-blue-400 flex items-center justify-center border border-blue-500/20 font-bold text-xs">
                              🤖
                            </div>
                            <div>
                              <h3 className="text-xs font-bold text-white">{a.name}</h3>
                              <span className="text-[10px] text-slate-400 font-mono">ID: {a.id}</span>
                            </div>
                          </div>
                          <span className="rounded-full bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 px-2 py-0.5 text-[10px] font-semibold">
                            ● {a.status}
                          </span>
                        </div>

                        <div className="grid grid-cols-2 gap-2 text-[11px]">
                          <div className="rounded-lg bg-slate-950/60 p-2 border border-slate-800/60">
                            <span className="text-[10px] text-slate-500 block">Schedule</span>
                            <span className="text-slate-300 font-mono text-[10px]">{a.kind}</span>
                          </div>
                          <div className="rounded-lg bg-slate-950/60 p-2 border border-slate-800/60">
                            <span className="text-[10px] text-slate-500 block">Trigger Mode</span>
                            <span className="text-blue-400 font-semibold">
                              {isTrigger ? 'On-Demand API' : 'Background Run'}
                            </span>
                          </div>
                        </div>

                        <div className="flex items-center justify-between pt-1 border-t border-slate-800/60 text-[10px]">
                          <span className="text-slate-500">
                            {a.lastRun ? `Last run: ${new Date(a.lastRun).toLocaleTimeString()}` : 'Never executed'}
                          </span>
                          <span className="text-slate-400 font-mono">
                            {a.totalRuns !== undefined ? `${a.totalRuns} total runs` : `Target: ${a.id.slice(0, 14)}...`}
                          </span>
                        </div>
                      </div>
                    )
                  })}
                  {cloudWorkers.map((cw) => {
                    const isPending = cw.status === 'pending_approval'
                    const spendFormatted = cw.total_spend_usd !== undefined ? `$${cw.total_spend_usd.toFixed(4)}` : '$0.0000'
                    const tokensFormatted = cw.total_tokens ? `${(cw.total_tokens / 1000).toFixed(1)}k` : '0k'

                    return (
                      <div
                        key={cw.worker_id}
                        className={`rounded-2xl border p-4 transition-all space-y-3 ${
                          isPending
                            ? 'border-amber-500/40 bg-gradient-to-b from-amber-500/[0.08] to-slate-900/60'
                            : 'border-slate-800 bg-slate-900/40 hover:border-slate-700/80'
                        }`}
                      >
                        <div className="flex items-start justify-between">
                          <div className="flex items-center gap-2.5">
                            <div
                              className={`h-8 w-8 rounded-xl flex items-center justify-center border font-bold text-xs ${
                                isPending
                                  ? 'bg-amber-500/20 text-amber-400 border-amber-500/30'
                                  : 'bg-blue-600/15 text-blue-400 border-blue-500/20'
                              }`}
                            >
                              ☁️
                            </div>
                            <div>
                              <div className="flex items-center gap-2">
                                <h3 className="text-xs font-bold text-white">{cw.name}</h3>
                                <span className="rounded bg-blue-900/40 text-blue-300 border border-blue-500/30 px-1.5 py-0.2 text-[9px] font-mono">
                                  GCP Cloud Run
                                </span>
                              </div>
                              <span className="text-[10px] text-slate-400 font-mono">ID: {cw.worker_id}</span>
                            </div>
                          </div>
                          <span
                            className={`rounded-full px-2 py-0.5 text-[10px] font-semibold border ${
                              isPending
                                ? 'bg-amber-500/20 text-amber-300 border-amber-500/30 animate-pulse'
                                : 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                            }`}
                          >
                            ● {cw.status || 'active'}
                          </span>
                        </div>

                        {/* Granular Telemetry Strip */}
                        <div className="grid grid-cols-3 gap-2 text-[11px]">
                          <div className="rounded-lg bg-slate-950/60 p-2 border border-slate-800/60">
                            <span className="text-[10px] text-slate-500 block">Total Spend</span>
                            <span className="text-emerald-400 font-mono font-semibold text-[11px]">{spendFormatted}</span>
                          </div>
                          <div className="rounded-lg bg-slate-950/60 p-2 border border-slate-800/60">
                            <span className="text-[10px] text-slate-500 block">Total Tokens</span>
                            <span className="text-purple-400 font-mono font-semibold text-[11px]">{tokensFormatted}</span>
                          </div>
                          <div className="rounded-lg bg-slate-950/60 p-2 border border-slate-800/60">
                            <span className="text-[10px] text-slate-500 block">Jobs Executed</span>
                            <span className="text-blue-400 font-mono font-semibold text-[11px]">{cw.total_jobs_count || 0}</span>
                          </div>
                        </div>

                        {/* Action Bar for Cloud Worker */}
                        {isPending && (
                          <div className="flex items-center justify-between pt-2 border-t border-slate-800/60">
                            <span className="text-[10px] text-amber-300 font-medium">Awaiting operator acceptance</span>
                            <button
                              type="button"
                              disabled={activatingWorkerId === cw.worker_id}
                              onClick={() => handleActivateCloudWorker(cw.worker_id)}
                              className="px-3 py-1 rounded-xl bg-gradient-to-r from-emerald-600 to-teal-600 hover:from-emerald-500 hover:to-teal-500 text-white text-xs font-bold shadow-md transition-all flex items-center gap-1.5"
                            >
                              <CheckCircle2 size={12} />
                              <span>{activatingWorkerId === cw.worker_id ? 'Activating...' : 'Activate Worker'}</span>
                            </button>
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              ) : (
                <div className="rounded-2xl border border-slate-800/80 bg-slate-900/20 p-8 text-center space-y-2">
                  <Bot className="h-8 w-8 text-slate-600 mx-auto" />
                  <h4 className="text-xs font-bold text-slate-300">No active workers registered yet</h4>
                  <p className="text-[11px] text-slate-500 max-w-sm mx-auto">
                    Propose a worker or ask Swarm in the AI sidebar to configure a dedicated specialist for this project.
                  </p>
                </div>
              )}
            </div>
          </div>
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
              <div className="grid grid-cols-2 md:grid-cols-3 gap-3">
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
        ) : activeNavTab === 'settings' ? (
          /* PROJECT CHARTER / SETTINGS TAB */
          <div className="flex-1 flex flex-col min-h-0 overflow-y-auto p-6 space-y-4">
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
            {/* TOP TOOLBAR: AUTOMATION OVERVIEW & 5-VARIANT SWITCHER DOCK */}
            <div className="flex flex-col border-b border-slate-800/80 bg-[#0a0f1d]/60">
              <div className="flex items-center justify-between p-3.5 pb-2.5">
                <div className="flex items-center gap-3">
                  <div>
                    <div className="flex items-center gap-2">
                      <h1 className="text-base font-bold tracking-tight text-white">
                        {selectedProject?.name || 'Project Overview'}
                      </h1>
                      <span className="rounded bg-blue-500/10 border border-blue-500/30 px-2 py-0.5 text-[10px] font-semibold text-blue-400 font-mono">
                        {liveTasks.length} {liveTasks.length === 1 ? 'Task' : 'Tasks'}
                      </span>
                    </div>
                    <p className="text-[11px] text-slate-400 mt-0.5">
                      Autonomous worker sessions executing across {selectedProject?.name || 'workspace'}
                    </p>
                  </div>
                </div>

                {/* Quick Action buttons */}
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => setIsDeployModalOpen(true)}
                    className="flex items-center gap-1.5 rounded-lg bg-blue-600 hover:bg-blue-500 text-white font-medium text-xs px-3 py-1.5 shadow-[0_2px_10px_rgba(37,99,235,0.3)] transition-all active:scale-95"
                  >
                    <Plus size={13} />
                    <span>+ New Task</span>
                  </button>
                  <button
                    onClick={() => {
                      setNewTaskPrompt('Run local testbench suite: verify critical test gates and check repository stability')
                      setIsDeployModalOpen(true)
                    }}
                    className="flex items-center gap-1.5 rounded-lg bg-slate-800 hover:bg-slate-700 border border-slate-700 text-slate-200 text-xs px-3 py-1.5 transition-all active:scale-95"
                  >
                    <Play size={11} fill="currentColor" />
                    <span>Run Testbench</span>
                  </button>
                </div>
              </div>

              {/* Clean Status Pipeline Header */}
              <div className="px-4 py-2.5 flex items-center justify-between border-t border-slate-800/50 text-xs">
                <div className="flex items-center gap-2">
                  <span className="text-xs font-semibold text-white">Project Pipeline</span>
                  <span className="text-[11px] text-slate-400">({liveTasks.length} tasks)</span>
                </div>
                {/* Status counter summary */}
                <div className="flex items-center gap-2 font-mono text-[10px]">
                  <span className="text-blue-400">● {runningCount} Running</span>
                  <span className="text-amber-400">● {reviewCount} Review</span>
                  <span className="text-slate-500">● {queuedCount} Queued</span>
                  <span className="text-emerald-400">● {completedCount} Done</span>
                </div>
              </div>
            </div>

            {/* STATUS / AUTOMATION TICKER */}
            <div className="px-4 py-2 border-b border-slate-800/80 bg-[#080d19]/80 flex items-center justify-between gap-4 text-xs">
              <div className="flex items-center gap-2.5 min-w-0">
                <span className="flex h-2 w-2 rounded-sm bg-emerald-400 flex-shrink-0" />
                <div className="flex items-center gap-2 min-w-0">
                  <span className="font-semibold text-white truncate text-[11px]">
                    {selectedProject?.name || 'Project Orchestrator'}
                  </span>
                  <span className="text-[10px] text-slate-400 truncate">
                    • System Orchestrator Active • {selectedProject?.linkedWorkspaces?.length || 0} Bound Workspace{selectedProject?.linkedWorkspaces?.length === 1 ? '' : 's'}
                  </span>
                </div>
              </div>
              <div className="flex items-center gap-2 text-[10px] font-mono text-slate-400">
                <span>Branch: {selectedProject?.branch || 'dev'}</span>
                <span>•</span>
                <span className="text-emerald-400">Pebble DB Synced</span>
              </div>
            </div>

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
              </div>
            )}

            {/* ACTIVE RUNNING AUTOMATIONS TICKER / CARDS AT TOP OF PROJECT */}
            {automations.length > 0 ? (
              <div className="mx-3.5 mt-2 mb-1.5 p-3 rounded-2xl border border-slate-800/90 bg-slate-900/60 shadow-lg">
                <div className="flex items-center justify-between pb-2 mb-2 border-b border-slate-800/60">
                  <div className="flex items-center gap-2">
                    <div className="h-2 w-2 rounded-full bg-emerald-400 animate-pulse" />
                    <span className="text-xs font-bold text-white tracking-tight">Active Project Automations ({automations.length})</span>
                    <span className="text-[10px] text-slate-400">Autonomous workers running for {selectedProject?.name || 'this project'}</span>
                  </div>
                  <button
                    type="button"
                    onClick={() => setActiveNavTab('workers')}
                    className="flex items-center gap-1 text-[11px] font-medium text-blue-400 hover:text-blue-300 transition-colors"
                  >
                    <span>Manage Fleet in Workers Hub</span>
                    <ArrowRight size={12} />
                  </button>
                </div>

                <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2.5">
                  {automations.map((a) => {
                    const isTrigger = a.kind === 'trigger'
                    return (
                      <div
                        key={a.id}
                        onClick={() => setActiveNavTab('workers')}
                        className="group cursor-pointer rounded-xl border border-slate-800 bg-[#090e1a]/80 p-2.5 hover:border-blue-500/50 hover:bg-slate-800/60 transition-all flex items-start justify-between gap-2 shadow-sm"
                        title="Click to view in Workers Hub"
                      >
                        <div className="flex items-start gap-2 min-w-0">
                          <div className="h-7 w-7 rounded-lg bg-blue-600/15 text-blue-400 border border-blue-500/25 flex items-center justify-center shrink-0 text-xs">
                            🤖
                          </div>
                          <div className="min-w-0">
                            <h4 className="text-xs font-semibold text-white truncate group-hover:text-blue-300 transition-colors">
                              {a.name}
                            </h4>
                            <p className="text-[10px] text-slate-400 truncate mt-0.5">
                              {isTrigger ? '⚡ On-Demand Trigger' : `🕒 ${a.nextRun || a.kind}`}
                            </p>
                          </div>
                        </div>
                        <div className="flex flex-col items-end shrink-0 gap-1">
                          <span className="rounded-full bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 px-1.5 py-0.2 text-[9px] font-semibold">
                            ● {a.status}
                          </span>
                          <span className="text-[9px] font-mono text-slate-500">
                            {a.totalRuns !== undefined ? `${a.totalRuns} run${a.totalRuns === 1 ? '' : 's'}` : ''}
                          </span>
                        </div>
                      </div>
                    )
                  })}
                </div>
              </div>
            ) : (
              <div className="mx-3.5 mt-2 mb-1 px-3.5 py-2 rounded-xl border border-dashed border-slate-800 bg-slate-900/30 flex items-center justify-between gap-3 text-xs">
                <div className="flex items-center gap-2 text-slate-400">
                  <Bot size={14} className="text-slate-500" />
                  <span className="text-[11px]">No background automations running for this project yet.</span>
                </div>
                <button
                  type="button"
                  onClick={() => setActiveNavTab('workers')}
                  className="text-[11px] font-semibold text-blue-400 hover:text-blue-300 flex items-center gap-1"
                >
                  <span>Explore Workers Hub</span>
                  <ArrowRight size={11} />
                </button>
              </div>
            )}

            {/* MAIN BODY: 5 DISTINCT VARIANTS */}
            <div className="flex-1 overflow-hidden flex flex-col">
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
              {/* ─────────────────────────────────────────────────────────────
                  VARIANT 1: COMPACT MATRIX & EXPANDABLE DRAWER
                 ───────────────────────────────────────────────────────────── */}
              {middleVariant === 'matrix' && (
                <div className="flex-1 flex flex-col overflow-hidden p-3.5 space-y-3">
                  {/* Search & Filter bar */}
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex-1 relative">
                      <Search size={13} className="absolute left-3 top-2.5 text-slate-500" />
                      <input
                        type="text"
                        value={searchQuery}
                        onChange={(e) => setSearchQuery(e.target.value)}
                        placeholder="Search tasks by title, worker, or tag..."
                        className="w-full bg-[#080c16] border border-slate-800 rounded-lg pl-8 pr-3 py-1.5 text-xs text-white placeholder-slate-500 focus:outline-none focus:border-blue-500/40"
                      />
                    </div>

                    <div className="flex items-center gap-1.5">
                      {(['all', 'running', 'needs_review', 'queued', 'completed'] as const).map((st) => (
                        <button
                          key={st}
                          onClick={() => setStatusFilter(st)}
                          className={`px-2.5 py-1 rounded text-[10px] font-semibold uppercase tracking-wider transition-all ${
                            statusFilter === st
                              ? 'bg-slate-700 text-white shadow-sm'
                              : 'bg-slate-800/60 text-slate-400 hover:text-slate-200'
                          }`}
                        >
                          {st === 'all'
                            ? `All (${liveTasks.length})`
                            : st === 'running'
                            ? `Running (${runningCount})`
                            : st === 'needs_review'
                            ? `Review (${reviewCount})`
                            : st === 'queued'
                            ? `Queued (${queuedCount})`
                            : `Done (${completedCount})`}
                        </button>
                      ))}
                    </div>
                  </div>

                  {/* Task rows */}
                  <div className="flex-1 overflow-y-auto space-y-2 pr-1">
                    {filteredTasks.length > 0 ? (
                      filteredTasks.map((t) => {
                        const isExpanded = expandedTaskId === t.id
                        return (
                          <div
                            key={t.id}
                            className="rounded-xl border border-slate-800/80 bg-[#0a0f1d]/70 hover:border-slate-700/80 transition-all overflow-hidden"
                          >
                            <div
                              onClick={() => setExpandedTaskId(isExpanded ? null : t.id)}
                              className="flex items-center justify-between p-3 cursor-pointer hover:bg-white/[0.02]"
                            >
                              <div className="flex items-center gap-2.5 min-w-0 flex-1">
                                <span
                                  className={`h-2 w-2 rounded-sm flex-shrink-0 ${
                                    t.status === 'pending_approval'
                                      ? 'bg-amber-400'
                                      : t.status === 'running'
                                      ? 'bg-blue-400 animate-pulse'
                                      : t.status === 'needs_review'
                                      ? 'bg-amber-400'
                                      : t.status === 'completed'
                                      ? 'bg-emerald-400'
                                      : 'bg-slate-600'
                                  }`}
                                />
                                <span className="font-mono text-[10px] text-slate-500 w-16">{t.id.slice(0, 8)}</span>
                                <span className="text-xs font-bold text-slate-200 truncate">{t.title}</span>
                                {t.outcomeType && (
                                  <span className="font-mono text-[9px] uppercase px-1.5 py-0.5 rounded bg-slate-800 text-slate-400">
                                    {t.outcomeType.replace('_', ' ')}
                                  </span>
                                )}
                              </div>

                              <div className="flex items-center gap-3 flex-shrink-0">
                                {(t.unintegratedCommits ?? 0) > 0 && (
                                  <span className="text-[10px] font-mono px-2 py-0.5 rounded bg-amber-950/40 text-amber-300 border border-amber-500/30">
                                    {t.unintegratedCommits} unmerged
                                  </span>
                                )}
                                <span className="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-800 text-slate-300">
                                  {t.workerName}
                                </span>
                                <span className="text-[10px] text-slate-500 font-mono">{t.elapsed}</span>
                                <button
                                  type="button"
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    handleSelectTask(t)
                                  }}
                                  className="flex items-center gap-1 px-2 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 hover:text-white text-[10px] font-medium"
                                  title="Open Chat"
                                >
                                  <MessageSquare size={11} />
                                  <span>Chat</span>
                                </button>
                                <ChevronDown
                                  size={13}
                                  className={`text-slate-400 transition-transform ${isExpanded ? 'rotate-180' : ''}`}
                                />
                              </div>
                            </div>

                            {/* Drawer Content */}
                            {isExpanded && (
                              <div className="p-3 border-t border-slate-800/60 bg-[#070b14]">
                                <MinimalTaskCard
                                  task={t}
                                  isSelected={selectedTaskId === t.id}
                                  onSelect={() => handleSelectTask(t)}
                                  onOpenChat={() => handleSelectTask(t)}
                                  onApprove={() => handleApproveTask(t.id)}
                                  onIntegrate={() => handleIntegrateTask(t.id)}
                                  onDelete={() => handleDeleteTask(t.id)}
                                  onRefine={(fb, err) => handleRefineTask(t.id, fb, err)}
                                  onPreviewDeliverable={(d) => handleOpenDeliverableInMediaCenter(d)}
                                  onReopen={(fb) => handleReopenTask(t.id, fb)}
                                  onComplete={() => handleCompleteTask(t.id)}
                                  onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                                  onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                                  onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                                  onOpenTaskModelChanger={handleOpenTaskModelChanger}
                                  projectId={selectedProject?.id}
                                  modelOptions={modelOptions}
                                  defaultImageModel={defaultImageModel}
                                  defaultVideoModel={defaultVideoModel}
                                  defaultAudioModel={defaultAudioModel}
                                  imageModelOptions={imageModelOptions}
                                  videoModelOptions={videoModelOptions}
                                  audioModelOptions={audioModelOptions}
                                />
                              </div>
                            )}
                          </div>
                        )
                      })
                    ) : (
                      <div className="flex-1 flex flex-col items-center justify-center p-8 text-center border border-dashed border-slate-800 rounded-xl bg-[#080c16]/50">
                        <Code size={20} className="text-slate-600 mb-2" />
                        <h4 className="text-xs font-bold text-slate-300">No tasks active</h4>
                        <p className="text-[11px] text-slate-500 mt-1 mb-4">
                          Propose a task in plain English to have the AI organize the mission and branch for approval.
                        </p>
                        <button
                          onClick={() => setIsDeployModalOpen(true)}
                          className="px-4 py-2 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-xs font-semibold shadow-md shadow-blue-600/20 transition-all flex items-center gap-1.5"
                        >
                          <Plus size={13} />
                          <span>+ New Task</span>
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
                  {(
                    [
                      { key: 'queued', label: 'Pending Approval', color: 'amber' },
                      { key: 'running', label: 'In Progress', color: 'blue' },
                      { key: 'needs_review', label: 'Needs Review', color: 'amber' },
                      { key: 'completed', label: 'Completed', color: 'emerald' },
                    ] as const
                  ).map((col) => {
                    const colTasks = liveTasks.filter((t) => {
                      if (col.key === 'queued') return t.status === 'queued' || t.status === 'pending_approval'
                      if (col.key === 'running') return t.status === 'running' || t.status === 'in_progress'
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
                              onSelect={() => handleSelectTask(t)}
                              onOpenChat={() => handleSelectTask(t)}
                              onApprove={() => handleApproveTask(t.id)}
                              onIntegrate={() => handleIntegrateTask(t.id)}
                              onDelete={() => handleDeleteTask(t.id)}
                              onRefine={(fb, err) => handleRefineTask(t.id, fb, err)}
                              onPreviewDeliverable={(d) => handleOpenDeliverableInMediaCenter(d)}
                              onReopen={(fb) => handleReopenTask(t.id, fb)}
                              onComplete={() => handleCompleteTask(t.id)}
                              onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                              onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                              onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                              onOpenTaskModelChanger={handleOpenTaskModelChanger}
                              projectId={selectedProject?.id}
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
                      className="flex items-center gap-1.5 px-3 py-1 rounded-lg bg-blue-600 hover:bg-blue-500 text-white text-xs font-medium"
                    >
                      <Plus size={12} />
                      <span>+ New Task</span>
                    </button>
                  </div>

                  {deployedWorkers.length > 0 ? (
                    <div className="grid grid-cols-2 gap-3">
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
                    <h4 className="text-xs font-bold text-white">Project Tasks ({liveTasks.length})</h4>
                    <div className="space-y-2.5">
                      {liveTasks.map((task) => (
                        <MinimalTaskCard
                          key={task.id}
                          task={task}
                          isSelected={selectedTaskId === task.id}
                          onSelect={() => handleSelectTask(task)}
                          onOpenChat={() => handleSelectTask(task)}
                          onApprove={() => handleApproveTask(task.id)}
                          onIntegrate={() => handleIntegrateTask(task.id)}
                          onDelete={() => handleDeleteTask(task.id)}
                          onRefine={(fb, err) => handleRefineTask(task.id, fb, err)}
                          onPreviewDeliverable={(d) => handleOpenDeliverableInMediaCenter(d)}
                          onReopen={(fb) => handleReopenTask(task.id, fb)}
                          onComplete={() => handleCompleteTask(task.id)}
                          onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                          onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                          onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                          onOpenTaskModelChanger={handleOpenTaskModelChanger}
                          projectId={selectedProject?.id}
                          modelOptions={modelOptions}
                          defaultImageModel={defaultImageModel}
                          defaultVideoModel={defaultVideoModel}
                          defaultAudioModel={defaultAudioModel}
                          imageModelOptions={imageModelOptions}
                          videoModelOptions={videoModelOptions}
                          audioModelOptions={audioModelOptions}
                        />
                      ))}
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
                    <div className="text-xs font-bold text-white pb-1">Tasks ({liveTasks.length})</div>
                    {liveTasks.map((t) => {
                      const isSel = selectedTaskId === t.id
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
                          <div className="text-xs font-semibold leading-snug">{t.title}</div>
                          <div className="flex items-center justify-between text-[10px] font-mono text-slate-500 mt-2">
                            <span>{t.workerName}</span>
                            <span className="text-blue-400 font-bold">{t.status}</span>
                          </div>
                        </div>
                      )
                    })}
                    {liveTasks.length === 0 && (
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
                        onSelect={() => handleSelectTask(selectedTaskForSplit)}
                        onOpenChat={() => handleSelectTask(selectedTaskForSplit)}
                        onApprove={() => handleApproveTask(selectedTaskForSplit.id)}
                        onIntegrate={() => handleIntegrateTask(selectedTaskForSplit.id)}
                        onDelete={() => handleDeleteTask(selectedTaskForSplit.id)}
                        onRefine={(fb, err) => handleRefineTask(selectedTaskForSplit.id, fb, err)}
                        onPreviewDeliverable={(d) => handleOpenDeliverableInMediaCenter(d)}
                        onReopen={(fb) => handleReopenTask(selectedTaskForSplit.id, fb)}
                        onComplete={() => handleCompleteTask(selectedTaskForSplit.id)}
                        onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                        onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                        onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                        onOpenTaskModelChanger={handleOpenTaskModelChanger}
                        projectId={selectedProject?.id}
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
                  <div className="text-xs font-bold text-white pb-1">Activity Stream</div>
                  {liveTasks.map((t) => (
                    <MinimalTaskCard
                      key={t.id}
                      task={t}
                      isSelected={selectedTaskId === t.id}
                      onSelect={() => handleSelectTask(t)}
                      onOpenChat={() => handleSelectTask(t)}
                      onApprove={() => handleApproveTask(t.id)}
                      onIntegrate={() => handleIntegrateTask(t.id)}
                      onDelete={() => handleDeleteTask(t.id)}
                      onRefine={(fb, err) => handleRefineTask(t.id, fb, err)}
                      onPreviewDeliverable={(d) => handleOpenDeliverableInMediaCenter(d)}
                      onReopen={(fb) => handleReopenTask(t.id, fb)}
                      onComplete={() => handleCompleteTask(t.id)}
                      onRedeployJob={(tId, jId, fb) => handleRedeployJob(tId, jId, fb)}
                      onUpdateModel={(taskId, model) => handleUpdateTaskModel(taskId, model)}
                      onOpenAgentSettings={(agentName) => handleOpenAgentSettings(agentName)}
                      onOpenTaskModelChanger={handleOpenTaskModelChanger}
                      projectId={selectedProject?.id}
                      modelOptions={modelOptions}
                      defaultImageModel={defaultImageModel}
                      defaultVideoModel={defaultVideoModel}
                      defaultAudioModel={defaultAudioModel}
                      imageModelOptions={imageModelOptions}
                      videoModelOptions={videoModelOptions}
                      audioModelOptions={audioModelOptions}
                    />
                  ))}
                  {liveTasks.length === 0 && (
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
      {activeSessionId ? (
        <OrchestratorChatSidebar
          key={activeSessionId}
          sessionId={activeSessionId}
          project={selectedProject}
          activeTask={activeTask}
          onBackToOrchestrator={handleBackToOrchestrator}
          onOrchestratorSessionReset={handleOrchestratorSessionReset}
        />
      ) : (
        <aside className="relative flex w-[440px] flex-shrink-0 flex-col items-center justify-center p-6 text-center rounded-3xl border border-slate-800/80 bg-[#0d121f] text-xs text-slate-400 shadow-[inset_0_1px_1px_rgba(255,255,255,0.06),0_18px_40px_rgba(0,0,0,0.65)]">
          <div className="h-12 w-12 rounded-2xl bg-blue-600/20 border border-blue-500/30 flex items-center justify-center text-blue-400 mb-3 shadow-lg shadow-blue-600/10">
            <Bot size={22} />
          </div>
          <h3 className="font-bold text-sm text-white mb-1">Swarm Project Orchestrator</h3>
          <p className="text-slate-400 text-xs max-w-xs leading-relaxed">
            {selectedProject ? 'Connecting executive orchestrator session...' : 'Select or create a project to activate the executive AI orchestrator.'}
          </p>
        </aside>
      )}

      {/* ─────────────────────────────────────────────────────────────
          MODAL: NEW TASK (VISUAL INTENT, MEDIA & AUTO-APPROVE)
         ───────────────────────────────────────────────────────────── */}
      {isDeployModalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6 backdrop-blur-md">
          <div className="relative flex max-w-xl w-full flex-col p-6 rounded-2xl border border-slate-800 bg-[#0d121f] shadow-2xl space-y-4">
            {/* Header */}
            <div className="flex items-center justify-between pb-3 border-b border-slate-800">
              <div className="flex items-center gap-2">
                <Sparkles size={16} className="text-blue-400" />
                <h3 className="text-sm font-bold text-white">Deploy Autonomous Task</h3>
              </div>
              <button
                onClick={() => setIsDeployModalOpen(false)}
                className="rounded-full p-1 text-slate-400 hover:bg-slate-800 hover:text-white transition-colors"
              >
                <X size={16} />
              </button>
            </div>

            {/* Intent Category Tabs */}
            <div className="grid grid-cols-6 gap-1 p-1 bg-slate-950 rounded-xl border border-slate-800 text-[11px] font-mono">
              <button
                type="button"
                onClick={() => {
                  setTaskIntent('code')
                  setFeatureSize('small')
                }}
                className={`flex items-center justify-center gap-1.5 py-1.5 rounded-lg font-bold transition-all ${
                  taskIntent === 'code' && featureSize === 'small'
                    ? 'bg-blue-600 text-white shadow'
                    : 'text-slate-400 hover:text-white hover:bg-slate-900'
                }`}
                data-testid="deploy-tab-small-feature"
              >
                <Code size={12} />
                <span>Small Feature/Fix</span>
              </button>
              <button
                type="button"
                onClick={() => {
                  setTaskIntent('code')
                  setFeatureSize('big')
                }}
                className={`flex items-center justify-center gap-1.5 py-1.5 rounded-lg font-bold transition-all ${
                  taskIntent === 'code' && featureSize === 'big'
                    ? 'bg-purple-600 text-white shadow'
                    : 'text-slate-400 hover:text-white hover:bg-slate-900'
                }`}
                data-testid="deploy-tab-big-feature"
              >
                <Sparkles size={12} />
                <span>Big Feature</span>
              </button>
              <button
                type="button"
                onClick={() => setTaskIntent('image')}
                className={`flex items-center justify-center gap-1.5 py-1.5 rounded-lg font-bold transition-all ${
                  taskIntent === 'image'
                    ? 'bg-blue-600 text-white shadow'
                    : 'text-slate-400 hover:text-white hover:bg-slate-900'
                }`}
                data-testid="deploy-tab-image"
              >
                <ImageIcon size={12} />
                <span>Image</span>
              </button>
              <button
                type="button"
                onClick={() => setTaskIntent('video')}
                className={`flex items-center justify-center gap-1.5 py-1.5 rounded-lg font-bold transition-all ${
                  taskIntent === 'video'
                    ? 'bg-blue-600 text-white shadow'
                    : 'text-slate-400 hover:text-white hover:bg-slate-900'
                }`}
                data-testid="deploy-tab-video"
              >
                <Film size={12} />
                <span>Video</span>
              </button>
              <button
                type="button"
                onClick={() => setTaskIntent('sound')}
                className={`flex items-center justify-center gap-1.5 py-1.5 rounded-lg font-bold transition-all ${
                  taskIntent === 'sound'
                    ? 'bg-blue-600 text-white shadow'
                    : 'text-slate-400 hover:text-white hover:bg-slate-900'
                }`}
                data-testid="deploy-tab-sound"
              >
                <Volume2 size={12} />
                <span>Sounds</span>
              </button>
              <button
                type="button"
                onClick={() => setTaskIntent('audit')}
                className={`flex items-center justify-center gap-1.5 py-1.5 rounded-lg font-bold transition-all ${
                  taskIntent === 'audit'
                    ? 'bg-blue-600 text-white shadow'
                    : 'text-slate-400 hover:text-white hover:bg-slate-900'
                }`}
                data-testid="deploy-tab-audit"
              >
                <Search size={12} />
                <span>Audit</span>
              </button>
            </div>

            {/* Impending Execution & Model Banner */}
            <div className="flex flex-col gap-2 p-3 rounded-xl bg-slate-950/80 border border-slate-800 text-[11px] font-mono" data-testid="deploy-modal-impending-preview">
              <div className="flex items-center justify-between gap-2 flex-wrap">
                <div className="flex items-center gap-2">
                  <span className="text-[10px] uppercase font-bold text-slate-400 tracking-wider flex items-center gap-1">
                    <Bot size={12} className="text-blue-400" />
                    <span>Impending Agent:</span>
                  </span>
                  <span className="font-bold text-white px-2 py-0.5 rounded bg-slate-900 border border-slate-700">
                    {taskIntent === 'code'
                      ? (featureSize === 'big' ? '@plan (Orchestrator Plan Mode)' : '@coder (Coder)')
                      : taskIntent === 'audit'
                        ? '@finder (Finder)'
                        : taskIntent === 'image'
                          ? '@image (Designer)'
                          : taskIntent === 'video'
                            ? '@video (Video)'
                            : '@sound (Audio)'}
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
                {taskIntent === 'code' && featureSize === 'small' && 'Direct autonomous code generation and tests on an isolated worktree branch via @coder.'}
                {taskIntent === 'code' && featureSize === 'big' && 'Plan-mode orchestration: builds a structured proposed plan and criteria awaiting user approval before launching worker agents.'}
                {taskIntent === 'audit' && 'Read-only architectural audit and code exploration via @finder, producing an audit report.'}
                {taskIntent === 'image' && 'Image media generation with selected media model.'}
                {taskIntent === 'video' && 'Video shot/storyline generation with selected video model.'}
                {taskIntent === 'sound' && 'Audio sound clip synthesis with selected audio model.'}
              </p>
            </div>

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
                  {taskIntent === 'image' && 'Visual Concept & Composition Details'}
                  {taskIntent === 'video' && 'Single Video Shot Concept (1 Prompt · Visuals & Audio)'}
                  {taskIntent === 'sound' && 'Audio Soundtrack / Mood Prompt'}
                  {taskIntent === 'audit' && 'Investigation Objective & Target Questions'}
                </label>
                <textarea
                  value={newTaskPrompt}
                  onChange={(e) => setNewTaskPrompt(e.target.value)}
                  rows={3}
                  autoFocus
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
                <div className="mt-1.5 flex items-center justify-between text-[11px]">
                  <div className="flex items-center gap-1.5 text-slate-400">
                    <Paperclip size={12} className="text-blue-400" />
                    <span>Attach media from project shelf or upload:</span>
                  </div>
                  <div className="flex items-center gap-1.5">
                    <label className="flex items-center gap-1 px-2 py-0.5 rounded bg-blue-600/20 text-blue-300 hover:bg-blue-600/30 border border-blue-500/30 cursor-pointer font-semibold transition">
                      <Upload size={10} />
                      <span>{isUploadingMedia ? 'Uploading...' : 'Upload'}</span>
                      <input
                        type="file"
                        multiple
                        accept={taskIntent === 'video' ? 'image/png,image/jpeg,.png,.jpg,.jpeg' : undefined}
                        className="hidden"
                        onChange={(e) => void handleFileUpload(e.target.files)}
                      />
                    </label>
                    <button
                      type="button"
                      onClick={() => setIsPasteDocOpen(true)}
                      className="flex items-center gap-1 px-2 py-0.5 rounded bg-slate-800 text-slate-300 hover:text-white border border-slate-700 font-semibold transition"
                    >
                      <FileText size={10} />
                      <span>Paste Doc</span>
                    </button>
                    <button
                      type="button"
                      onClick={() => setShowFullMediaCenter(true)}
                      className="flex items-center gap-1 px-2 py-0.5 rounded bg-slate-800 text-slate-300 hover:text-white border border-slate-700 font-semibold transition"
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
                      <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold">Image Model</label>
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
                        className="w-full rounded bg-slate-900 border border-slate-800 px-2.5 py-1.5 text-white text-[11px] font-mono focus:outline-none focus:border-blue-500"
                      >
                        {imageModelOptions.map((opt) => (
                          <option key={opt.id} value={opt.id} disabled={!opt.ready}>
                            {opt.label}{opt.id === defaultImageModel ? ' (Default)' : ''}{!opt.ready ? ` (Unavailable${opt.reason ? `: ${opt.reason}` : ''})` : ''}
                          </option>
                        ))}
                      </select>
                    )}

                    {/* Override vs Default Controls */}
                    {selectedImageModel && defaultImageModel && (
                      <div className="mt-1.5 pt-1.5 border-t border-slate-800/40 space-y-1 text-[10px] font-mono">
                        <div className="flex flex-wrap items-center justify-between gap-1">
                          {selectedImageModel !== defaultImageModel ? (
                            <>
                              <span className="text-amber-400 flex items-center gap-1">
                                <span>Override for this task</span>
                                <span className="text-slate-500">(Default: {defaultImageOption?.label || defaultImageModel})</span>
                              </span>
                              <button
                                type="button"
                                onClick={() => handleSetImageAsDefault(selectedImageModel)}
                                className="text-blue-400 hover:text-blue-300 font-semibold underline underline-offset-2 transition"
                              >
                                Set as default for next time
                              </button>
                            </>
                          ) : (
                            <span className="text-slate-400 flex items-center gap-1">
                              <span className="text-emerald-400 font-bold">✓ Default image model</span>
                              <span className="text-slate-500">· Saved for future tasks</span>
                            </span>
                          )}
                        </div>
                        {selectedImageModel !== defaultImageModel && (
                          <label className="flex items-center gap-1.5 text-slate-400 cursor-pointer pt-0.5">
                            <input
                              type="checkbox"
                              checked={saveImageAsDefault}
                              onChange={(e) => {
                                setSaveImageAsDefault(e.target.checked)
                                if (e.target.checked) {
                                  void handleSetImageAsDefault(selectedImageModel)
                                }
                              }}
                              className="rounded border-slate-700 bg-slate-900 text-blue-500 focus:ring-0 w-3 h-3"
                            />
                            <span className="font-sans text-[10px]">Change default in this menu for next time</span>
                          </label>
                        )}
                      </div>
                    )}
                  </div>

                  {/* Resolution Selector (4K, 2K, 1K) */}
                  <div className="pt-2 border-t border-slate-800/60">
                    <div className="flex items-center justify-between mb-1.5">
                      <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold">Resolution & Quality</label>
                      <span className="text-[10px] text-slate-500 font-mono">
                        {imageResolution === '1k' ? '1024×1024 (Standard)' : imageResolution === '2k' ? '2048×2048 (HD)' : '4096×4096 (Ultra HD)'}
                      </span>
                    </div>
                    <div className="grid grid-cols-3 gap-1.5 font-mono text-[10px]">
                      {(['1k', '2k', '4k'] as const).map((res) => {
                        const unitRate = imagePricingInfo.unitRatesByResolution?.[res] ?? (res === '1k' ? 0.03 : res === '2k' ? 0.06 : 0.12)
                        const totalForRes = imagePricingInfo.totalsByResolution?.[res] ?? (unitRate * imageVariants)
                        const isSelected = imageResolution === res
                        return (
                          <button
                            key={res}
                            type="button"
                            onClick={() => setImageResolution(res)}
                            className={`py-1.5 px-2 rounded border text-center transition-all flex flex-col items-center justify-center ${
                              isSelected
                                ? 'bg-blue-600 border-blue-400 text-white shadow-sm'
                                : 'bg-slate-900 border-slate-800 text-slate-400 hover:text-white'
                            }`}
                          >
                            <div className="font-bold uppercase leading-tight">{res}</div>
                            <div className={`text-[10px] font-semibold mt-0.5 leading-tight ${isSelected ? 'text-white' : 'text-slate-200'}`}>
                              ${totalForRes.toFixed(2)}
                            </div>
                            <div className={`text-[8px] leading-tight ${isSelected ? 'text-blue-100 opacity-90' : 'text-slate-500'}`}>
                              {imageVariants > 1 ? `${imageVariants}x at $${unitRate.toFixed(2)}/ea` : `$${unitRate.toFixed(2)}/img`}
                            </div>
                          </button>
                        )
                      })}
                    </div>
                  </div>

                  {/* Aspect Ratio with visual cues & Variants Count */}
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-2 border-t border-slate-800/60">
                    <div>
                      <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold mb-1.5">Aspect Ratio</label>
                      <div className="grid grid-cols-4 gap-1 font-mono text-[10px]">
                        {IMAGE_ASPECT_RATIOS.map((ar) => (
                          <button
                            key={ar.ratio}
                            type="button"
                            onClick={() => setImageAspectRatio(ar.ratio)}
                            className={`py-1.5 px-0.5 rounded border text-center font-bold flex flex-col items-center justify-center gap-1 transition-all ${
                              imageAspectRatio === ar.ratio
                                ? 'bg-blue-600 border-blue-400 text-white shadow'
                                : 'bg-slate-900 border-slate-800 text-slate-400 hover:text-white'
                            }`}
                          >
                            <div
                              className={`border rounded-[1.5px] transition-colors ${ar.widthClass} ${ar.heightClass} ${
                                imageAspectRatio === ar.ratio ? 'border-white bg-white/20' : 'border-slate-500 bg-slate-800/40'
                              }`}
                            />
                            <span className="leading-none">{ar.ratio}</span>
                            <span className="text-[8px] font-sans font-normal opacity-75 leading-none">{ar.label}</span>
                          </button>
                        ))}
                      </div>
                    </div>
                    <div>
                      <div className="flex items-center justify-between mb-1.5">
                        <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold">Variants Count</label>
                        <span className="text-[9px] text-slate-500 font-mono">
                          {imageVariants} {imageVariants === 1 ? 'image' : 'images'}
                        </span>
                      </div>
                      <div className="grid grid-cols-6 gap-1 font-mono text-[10px]">
                        {[1, 2, 4, 5, 10, 25].map((v) => {
                          const costForV = (imagePricingInfo.ratePerImage * v).toFixed(2)
                          const isSelected = imageVariants === v
                          return (
                            <button
                              key={v}
                              type="button"
                              onClick={() => setImageVariants(v)}
                              className={`py-1 rounded border text-center font-bold flex flex-col items-center justify-center transition-all ${
                                isSelected
                                  ? 'bg-blue-600 border-blue-400 text-white'
                                  : 'bg-slate-900 border-slate-800 text-slate-400 hover:text-white'
                              }`}
                            >
                              <span className="leading-tight">{v}{v >= 5 ? 'x' : ''}</span>
                              <span className={`text-[8px] font-normal leading-tight mt-0.5 ${isSelected ? 'text-blue-100 font-semibold' : 'text-slate-400'}`}>
                                ${costForV}
                              </span>
                            </button>
                          )
                        })}
                      </div>
                    </div>
                  </div>

                  {/* Estimated Model Cost Banner for Images */}
                  <div className="rounded-lg border border-slate-800 bg-slate-900/60 p-2.5 flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <Sparkles size={13} className="text-blue-400 shrink-0" />
                      <span className="text-[11px] font-mono text-slate-300">
                        Estimated Model Cost: <strong className="text-white font-semibold">{imagePricingInfo.formattedSummary}</strong>
                      </span>
                    </div>
                    <span className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-slate-800 text-blue-300 border border-slate-700 font-semibold shrink-0">
                      ${imagePricingInfo.totalPrice.toFixed(2)} Total · {imageVariants} {imageVariants === 1 ? 'image' : 'images'} · {imageResolution.toUpperCase()}
                    </span>
                  </div>
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
                      <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold">Video Model</label>
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
                        className="w-full rounded bg-slate-900 border border-slate-800 px-2.5 py-1.5 text-white text-[11px] font-mono focus:outline-none focus:border-blue-500"
                      >
                        <option value="">Select a video model</option>
                        {videoModelOptions.map((opt) => (
                          <option key={opt.id} value={opt.id} disabled={!opt.ready}>
                            {opt.label}{opt.id === defaultVideoModel ? ' (Default)' : ''}{!opt.ready ? ` (Unavailable${opt.reason ? `: ${opt.reason}` : ''})` : ''}
                          </option>
                        ))}
                      </select>
                    )}

                    {/* Override vs Default Controls for Video */}
                    {selectedVideoModel && (
                      <div className="mt-1.5 pt-1.5 border-t border-slate-800/40 space-y-1 text-[10px] font-mono">
                        <div className="flex flex-wrap items-center justify-between gap-1">
                          {selectedVideoModel !== defaultVideoModel ? (
                            <>
                              <span className="text-amber-400 flex items-center gap-1">
                                <span>Override for this task</span>
                                <span className="text-slate-500">(Default: {defaultVideoOption?.label || defaultVideoModel})</span>
                              </span>
                              <button
                                type="button"
                                disabled={videoDefaults.saving}
                                onClick={() => handleSetVideoAsDefault(selectedVideoModel)}
                                className="text-blue-400 hover:text-blue-300 font-semibold underline underline-offset-2 transition"
                              >
                                Set as default for next time
                              </button>
                            </>
                          ) : (
                            <span className="text-slate-400 flex items-center gap-1">
                              <span className="text-emerald-400 font-bold">✓ Default video model</span>
                              <span className="text-slate-500">· Saved for future tasks</span>
                            </span>
                          )}
                        </div>
                        {selectedVideoModel !== defaultVideoModel && (
                          <label className="flex items-center gap-1.5 text-slate-400 cursor-pointer pt-0.5">
                            <input
                              type="checkbox"
                              checked={saveVideoAsDefault}
                              disabled={videoDefaults.saving}
                              onChange={(e) => {
                                setSaveVideoAsDefault(e.target.checked)
                                if (e.target.checked) {
                                  void handleSetVideoAsDefault(selectedVideoModel)
                                }
                              }}
                              className="rounded border-slate-700 bg-slate-900 text-blue-500 focus:ring-0 w-3 h-3"
                            />
                            <span className="font-sans text-[10px]">Change default in this menu for next time</span>
                          </label>
                        )}
                      </div>
                    )}
                  </div>

                  {/* Direct Single Video Execution Banner */}
                  <div className="p-2.5 rounded-lg bg-blue-950/20 border border-blue-500/20 text-[11px] text-slate-300 space-y-1 font-sans leading-relaxed">
                    <div className="flex items-center gap-1.5 font-bold text-blue-400 text-[10px] font-mono uppercase">
                      <Sparkles size={11} />
                      <span>Single Video Shot (1 Prompt · Direct Model Execution)</span>
                    </div>
                    <p className="text-[11px] text-slate-300">
                      Generates video directly with the selected model; audio availability depends on its catalog capabilities. Optional scene prompts create an ordered assembled cut. Timeline editing and audio mixing are handled in Video Studio.
                    </p>
                  </div>

                  {/* AI Prompt Enhancement Toggle */}
                  <div className="pt-2 border-t border-slate-800/60">
                    <label className="flex items-center justify-between cursor-pointer select-none">
                      <div className="flex items-center gap-2">
                        <input
                          type="checkbox"
                          checked={enhanceVideoPrompt}
                          onChange={(e) => setEnhanceVideoPrompt(e.target.checked)}
                          className="rounded border-slate-700 bg-slate-900 text-blue-600 focus:ring-0 focus:ring-offset-0 h-3.5 w-3.5"
                        />
                        <span className="font-mono text-[10px] uppercase font-bold text-slate-300 flex items-center gap-1.5">
                          <Sparkles size={11} className={enhanceVideoPrompt ? 'text-indigo-400' : 'text-slate-500'} />
                          <span>Enhance prompt with AI</span>
                        </span>
                      </div>
                      <span className="text-[10px] font-mono text-slate-500">
                        {enhanceVideoPrompt ? 'Uses Router to polish prompt' : 'Direct to Video Model (No router rewrite)'}
                      </span>
                    </label>
                    {enhanceVideoPrompt ? (
                      <p className="mt-1 text-[10px] text-slate-400 pl-5 leading-relaxed font-sans">
                        The AI router will refine your prompt for visual lighting and camera motion without decomposing your single shot into multiple scenes.
                      </p>
                    ) : (
                      <p className="mt-1 text-[10px] text-slate-500 pl-5 leading-relaxed font-sans">
                        Your prompt is sent directly to the video model without AI prompt modification or router planning.
                      </p>
                    )}
                  </div>

                  {/* Resolution Selector & Aspect Ratio */}
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-2 border-t border-slate-800/60">
                    <div>
                      <div className="flex items-center justify-between mb-1.5">
                        <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold">Resolution & Quality</label>
                        <span className="text-[9px] text-slate-500 font-mono">
                          {videoDuration > 0 ? `${videoDuration}s · ` : ''}{videoClipCount} {videoClipCount === 1 ? 'clip' : 'clips'}
                        </span>
                      </div>
                      {supportedVideoResolutions.length > 0 ? (
                        <div className="grid grid-cols-[repeat(auto-fit,minmax(5rem,1fr))] auto-rows-fr gap-1 font-mono text-[10px]">
                          {supportedVideoResolutions.map((res) => {
                            const durations = resolveAllowedVideoDurations(selectedVideoGenOptions, res)
                            const estimateDuration = durations.includes(videoDuration) ? videoDuration : (durations[durations.length - 1] || 0)
                            const estimate = resolveVideoPricing(selectedVideoOption, res, estimateDuration, videoClipCount)
                            const totalForRes = estimate.totalPrice
                            const isSelected = videoResolution === res
                            return (
                              <button
                                key={res}
                                type="button"
                                onClick={() => setVideoResolution(res)}
                                className={`py-1.5 px-1 rounded border text-center font-bold flex flex-col items-center justify-center transition-all ${
                                  isSelected
                                    ? 'bg-blue-600 border-blue-400 text-white shadow'
                                    : 'bg-slate-900 border-slate-800 text-slate-400 hover:text-white'
                                }`}
                              >
                                <span className="leading-tight">{res}</span>
                                <span className={`text-[9px] font-semibold mt-0.5 leading-tight ${isSelected ? 'text-white' : 'text-slate-200'}`}>
                                  {totalForRes !== undefined
                                    ? `${estimate.approximate ? '≈' : ''}$${totalForRes.toFixed(2)}`
                                    : estimate.ratesByResolution?.[normalizeVideoResKey(res)] || 'No pricing'}
                                </span>
                                <span className={`text-[8px] font-normal leading-tight ${isSelected ? 'text-blue-100 opacity-90' : 'text-slate-500'}`}>
                                  {totalForRes !== undefined ? estimate.ratesByResolution?.[normalizeVideoResKey(res)] || 'No pricing' : estimate.approximate ? 'Approx. video output' : 'Catalog rate'}
                                  {estimateDuration > 0 ? ` · ${estimateDuration}s` : ''}
                                </span>
                              </button>
                            )
                          })}
                        </div>
                      ) : (
                        <div className="p-2 rounded bg-slate-900/60 border border-slate-800 text-[10px] font-mono text-slate-500">
                          Resolution not configurable for this model
                        </div>
                      )}
                    </div>

                    <div>
                      <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold mb-1.5">Aspect Ratio</label>
                      {supportedVideoAspectRatios.length > 0 ? (
                        <div className="grid grid-cols-[repeat(auto-fit,minmax(5rem,1fr))] auto-rows-fr gap-1 font-mono text-[10px]">
                          {VIDEO_ASPECT_RATIOS.filter((ar) => supportedVideoAspectRatios.includes(ar.ratio)).map((ar) => {
                            const isSelected = videoAspectRatio === ar.ratio
                            return (
                              <button
                                key={ar.ratio}
                                type="button"
                                onClick={() => setVideoAspectRatio(ar.ratio)}
                                className={`py-1.5 px-1 rounded border text-center font-bold flex flex-col items-center justify-center gap-1 transition-all ${
                                  isSelected
                                    ? 'bg-blue-600 border-blue-400 text-white shadow'
                                    : 'bg-slate-900 border-slate-800 text-slate-400 hover:text-white'
                                }`}
                              >
                                <div
                                  className={`border rounded-[1.5px] transition-colors ${ar.widthClass} ${ar.heightClass} ${
                                    isSelected ? 'border-white bg-white/20' : 'border-slate-500 bg-slate-800/40'
                                  }`}
                                />
                                <span className="leading-none">{ar.ratio}</span>
                                <span className="text-[8px] font-sans font-normal opacity-75 leading-none">{ar.label}</span>
                              </button>
                            )
                          })}
                        </div>
                      ) : (
                        <div className="p-2 rounded bg-slate-900/60 border border-slate-800 text-[10px] font-mono text-slate-500">
                          Aspect ratio not configurable for this model
                        </div>
                      )}
                    </div>
                  </div>

                  <div>
                    <label htmlFor="video-scene-prompts" className="block text-[10px] text-slate-400 font-mono uppercase font-bold mb-1.5">Multipart scene prompts (optional)</label>
                    <textarea id="video-scene-prompts" value={videoScenePrompts} onChange={event => setVideoScenePrompts(event.target.value)} rows={3} className="w-full rounded border border-slate-700 bg-slate-900 p-2 text-sm text-slate-100" placeholder="One scene per line, 2–8 scenes. Leave empty for a single shot." />
                    <p className="text-[10px] text-slate-400">Scenes generate sequentially and assemble in order. Select one clip. Each scene is billed separately; generated scene audio is retained.</p>
                  </div>
                  {/* Duration & Clip Count Selectors */}
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-2 border-t border-slate-800/60">
                    <div>
                      <div className="flex items-center justify-between mb-1.5">
                        <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold">Duration (Seconds)</label>
                        <span className="text-[9px] text-slate-500 font-mono">
                          {supportedVideoDurations.length === 1
                            ? `Fixed for ${videoResolution || 'model'}`
                            : supportedVideoDurations.length > 0
                            ? 'Model constrained'
                            : 'Not configurable'}
                        </span>
                      </div>
                      {supportedVideoDurations.length > 0 ? (
                        <div className="grid grid-cols-[repeat(auto-fit,minmax(5rem,1fr))] auto-rows-fr gap-1 font-mono text-[10px]">
                          {supportedVideoDurations.map((dur) => {
                            const isSelected = videoDuration === dur
                            return (
                              <button
                                key={dur}
                                type="button"
                                onClick={() => setVideoDuration(dur)}
                                className={`py-1.5 px-1 rounded border text-center font-bold flex flex-col items-center justify-center transition-all ${
                                  isSelected
                                    ? 'bg-blue-600 border-blue-400 text-white shadow'
                                    : 'bg-slate-900 border-slate-800 text-slate-400 hover:text-white'
                                }`}
                              >
                                <span className="leading-tight">{dur}s</span>
                                <span className={`text-[8px] font-normal leading-tight mt-0.5 ${isSelected ? 'text-blue-100 font-semibold' : 'text-slate-500'}`}>
                                  {videoPricingInfo.ratePerSec !== undefined
                                    ? `$${(videoPricingInfo.ratePerSec * dur).toFixed(2)}/clip`
                                    : `${dur} Seconds`}
                                </span>
                              </button>
                            )
                          })}
                        </div>
                      ) : (
                        <div className="p-2 rounded bg-slate-900/60 border border-slate-800 text-[10px] font-mono text-slate-500">
                          Duration not configurable for this model
                        </div>
                      )}
                    </div>

                    <div>
                      <div className="flex items-center justify-between mb-1.5">
                        <label className="block text-[10px] text-slate-400 font-mono uppercase font-bold">Clip Count (1..8)</label>
                        <span className="text-[9px] text-slate-500 font-mono">
                          Application limit (max 8)
                        </span>
                      </div>
                      <div className="grid grid-cols-4 gap-1 font-mono text-[10px]">
                        {[1, 2, 4, 8].map((c) => {
                          const isSelected = videoClipCount === c
                          const costForC = videoPricingInfo.rateForClip !== undefined
                            ? `$${(videoPricingInfo.rateForClip * c).toFixed(2)}`
                            : 'Unavailable'
                          return (
                            <button
                              key={c}
                              type="button"
                              onClick={() => setVideoClipCount(c)}
                              className={`py-1.5 px-1 rounded border text-center font-bold flex flex-col items-center justify-center transition-all ${
                                isSelected
                                  ? 'bg-blue-600 border-blue-400 text-white shadow'
                                  : 'bg-slate-900 border-slate-800 text-slate-400 hover:text-white'
                              }`}
                            >
                              <span className="leading-tight">{c}{c === 1 ? ' Clip' : 'x'}</span>
                              <span className={`text-[8px] font-normal leading-tight mt-0.5 ${isSelected ? 'text-blue-100 font-semibold' : 'text-slate-400'}`}>
                                {costForC}
                              </span>
                            </button>
                          )
                        })}
                      </div>
                    </div>
                  </div>

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
                    <div className="p-2 rounded bg-slate-900/60 border border-slate-800 text-[10px] font-mono text-slate-400 flex items-center justify-between">
                      <span className="flex items-center gap-1.5">
                        <Paperclip size={11} className="text-blue-400" />
                        <span>Optional Starting Frame (1 Image): Drop or tag an image reference. Text stays in prompt.</span>
                      </span>
                      {taggedMedia.length > 0 && (
                        <span className="text-emerald-400 font-semibold">1 image attached</span>
                      )}
                    </div>
                  )}

                  {/* Pricing Transparency Summary */}
                  <div className="p-2.5 rounded-lg bg-slate-900/90 border border-slate-800 text-[11px] font-mono flex flex-wrap items-center justify-between gap-2">
                    <span className="text-slate-300 flex items-center gap-1.5">
                      <Tag size={12} className="text-blue-400" />
                      <span>Estimated Model Cost: <strong className="text-white font-semibold">{videoPricingInfo.formattedSummary}</strong></span>
                    </span>
                    <span className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-slate-800 text-blue-300 border border-slate-700 font-semibold shrink-0">
                      {videoPricingInfo.totalPrice !== undefined
                        ? `$${videoPricingInfo.totalPrice.toFixed(2)} Total${videoResolution ? ` · ${videoResolution.toUpperCase()}` : ''}${videoDuration > 0 ? ` · ${videoDuration}s` : ''} · ${videoClipCount} ${videoClipCount === 1 ? 'clip' : 'clips'}`
                        : `Pricing Unavailable${videoResolution ? ` · ${videoResolution.toUpperCase()}` : ''}${videoDuration > 0 ? ` · ${videoDuration}s` : ''}`}
                    </span>
                  </div>
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

              {/* Workspace Selector for Code Intent */}
              {taskIntent === 'code' && selectedProject?.linkedWorkspaces && selectedProject.linkedWorkspaces.length > 1 && (
                <div>
                  <div className="flex items-center justify-between mb-1">
                    <label className="text-[11px] text-slate-400 font-medium">Hero Workspace</label>
                    <span className="text-[10px] text-blue-400 font-mono">Auto-detected if left empty</span>
                  </div>
                  <select
                    value={newTaskWorkspace}
                    onChange={(e) => setNewTaskWorkspace(e.target.value)}
                    className="w-full rounded-lg bg-slate-950 border border-slate-800 px-3 py-2 text-white focus:outline-none focus:border-blue-500/60 text-xs"
                  >
                    <option value="">✨ Auto-detect from prompt & project context</option>
                    {selectedProject.linkedWorkspaces.map((ws) => (
                      <option key={ws} value={ws}>
                        {ws}
                      </option>
                    ))}
                  </select>
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
          <div className="relative flex max-w-lg w-full flex-col p-6 rounded-2xl border border-slate-800 bg-[#0d121f] shadow-2xl space-y-4">
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
          key={activeMediaViewerItem.id}
          item={activeMediaViewerItem}
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
        <main className="relative order-[-1] flex min-w-0 flex-1 flex-col overflow-hidden rounded-3xl border border-slate-800/80 bg-[#0d121f]/95">
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
              title="Back to Tasks & Canvas"
            >
              <X size={16} />
            </button>
          </div>
          {mediaSyncError && <p role="alert" className="px-5 py-2 text-xs text-amber-300">Live updates interrupted: {mediaSyncError}. Retrying automatically; do not resubmit.</p>}
          <div className="flex-1 min-h-0">
            <HistoricalMediaLibrary
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
