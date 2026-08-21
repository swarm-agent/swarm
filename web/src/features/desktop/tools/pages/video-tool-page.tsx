import { type CSSProperties, type PointerEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Download, Eye, EyeOff, Film, FolderOpen, ListVideo, Loader2, MessageSquare, Moon, Pause, Play, RotateCcw, Sparkles } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { requestJson } from '../../../../app/api'
import { createSession, fetchDraftModelPreference } from '../../chat/queries/chat-queries'
import { uiSettingsQueryOptions, workspaceOverviewQueryOptions } from '../../../queries/query-options'
import { normalizeGlobalThemeSettings } from '../../settings/swarm/types/swarm-settings'
import { browseWorkspacePath } from '../../../workspaces/launcher/queries/browse-workspace-path'
import { buildWorkspaceRouteSlugMap, resolveWorkspaceBySlug, workspaceRouteSlugBase } from '../../../workspaces/launcher/services/workspace-route'
import { applyWorkspaceTheme, createWorkspaceThemeStyle } from '../../../workspaces/launcher/services/workspace-theme'
import { createDesktopV3ExistingMessageOperation, continueDesktopV3Conversation } from '../../session-v3/existing-session-flow'
import { buildDesktopChatRouteOptions, getDesktopSessionCreateTarget, type DesktopChatRoute } from '../../chat/services/chat-routing'
import type { WorkspaceBrowseResult, WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { WorkspaceOverviewSwarmTarget } from '../../../workspaces/launcher/types/workspace-overview'
import { SwarmToolSidebar } from '../components/swarm-tool-sidebar'
import { VIDEO_TRANSITION_KINDS, VideoProposalReview, VideoSessionAISidecar, renderedVideoArtifactUrl, requestVideoRenderCancellation, transitionLabel, videoPlanPartMessageSelection, videoProposalFocusClipId, videoStepEditRequest, type VideoEditProposalWire, type VideoPlanProposalWire, type VideoTransitionKind, type VideoTransitionWire } from '../video-studio/video-studio-surface'

export type VideoClip = {
  id: string
  name: string
  sourceRef: string
  extension: string
  sizeBytes: number
  modifiedAt: number
}

export type VideoTimelineClipWire = {
  id: string
  name?: string
  track?: number
  sequence?: number
  source_kind: string
  source_ref?: string
  source_start_ms?: number
  source_end_ms?: number
  timeline_start_ms?: number
  timeline_end_ms?: number
  duration_ms?: number
  visible?: boolean
  layer?: number
  volume?: number
  muted?: boolean
  artifact_ref?: { session_id?: string; collection_id: string; variant_id: string; event_seq?: number; media_type?: string }
  media_type?: string
  design_input?: { session_id?: string; collection_id: string; variant_id: string; event_seq?: number; media_type?: string }
  captions?: Array<{
    id: string
    text: string
    position?: string
    start_ms?: number
    end_ms?: number
  }>
}

export type VideoProjectTimelineWire = {
  schema_version?: number
  output_preset?: string
  width?: number
  height?: number
  fps?: number
  total_duration_ms?: number
  clips: VideoTimelineClipWire[]
  transitions?: VideoTransitionWire[]
  metadata?: Record<string, unknown>
}

export type VideoProjectSnapshotWire = {
  schema_version?: number
  id: string
  session_id: string
  title: string
  description?: string
  output_preset?: string
  current_revision_id?: string
  current_revision_number?: number
  revision_count?: number
  active_render_job_id?: string
  project_kind?: string
  metadata?: Record<string, unknown>
  created_at: number
  updated_at: number
}

export type VideoProjectRevisionSnapshotWire = {
  schema_version?: number
  id: string
  project_id: string
  revision_number: number
  session_id: string
  parent_revision_id?: string
  restored_from_revision_id?: string
  description?: string
  change_summary?: string
  timeline: VideoProjectTimelineWire
  author_principal?: string
  created_at: number
}

export type VideoProjectDetailWire = {
  project: VideoProjectSnapshotWire
  current_revision?: VideoProjectRevisionSnapshotWire
}

export function acceptedVideoPlan(timeline: VideoProjectTimelineWire): VideoPlanProposalWire | null {
  const candidate = timeline.metadata?.accepted_video_plan
  if (!candidate || typeof candidate !== 'object') return null
  const plan = candidate as Partial<VideoPlanProposalWire>
  if (!Array.isArray(plan.parts) || plan.parts.length === 0) return null
  const parts = plan.parts.filter((part) => part && typeof part.id === 'string' && typeof part.title === 'string' && typeof part.duration_ms === 'number' && Boolean(part.visual?.session_id && part.visual.collection_id && part.visual.variant_id && part.visual.event_seq))
  const kind = plan.kind === 'revision' ? 'revision' : 'initial'
  return parts.length > 0 ? { kind, summary: typeof plan.summary === 'string' ? plan.summary : undefined, parts } : null
}

export function preferredVisibleVideoProject(projects: VideoProjectSnapshotWire[]): VideoProjectSnapshotWire | undefined {
  return projects.find((project) => project.metadata?.reviewable_plan === true && Boolean(project.current_revision_id))
    ?? projects.find((project) => project.project_kind === 'video_tool' && Boolean(project.current_revision_id))
    ?? projects.find((project) => Boolean(project.current_revision_id))
    ?? projects.find((project) => project.project_kind === 'video_tool')
    ?? projects[0]
}

export function videoPlanClipDetails(clip: VideoTimelineClipWire): {
  timing: string
  title: string
  narration: string
  still: string
  onScreenText: string
} {
  const parts = String(clip.name ?? '').split(' | ').map((part) => part.trim())
  const heading = parts[0] ?? ''
  const headingParts = heading.split(' — ')
  return {
    timing: headingParts[0]?.trim() || `${formatTimelineTime((clip.timeline_start_ms ?? 0) / 1000)}–${formatTimelineTime((clip.timeline_end_ms ?? 0) / 1000)}`,
    title: headingParts.slice(1).join(' — ').trim() || `Section ${(clip.sequence ?? 0) + 1}`,
    narration: (parts.find((part) => part.startsWith('Narration:')) ?? '').replace(/^Narration:\s*/, ''),
    still: (parts.find((part) => part.startsWith('Planned still:')) ?? '').replace(/^Planned still:\s*/, ''),
    onScreenText: clip.captions?.map((caption) => caption.text.trim()).filter(Boolean).join(' · ') ?? '',
  }
}

export type VideoRenderJobSnapshotWire = {
  schema_version?: number
  id: string
  project_id: string
  revision_id: string
  revision_number: number
  session_id: string
  status: 'queued' | 'rendering' | 'ready' | 'failed' | 'cancelled' | 'stale'
  progress: number
  failure_code?: string
  failure_reason?: string
  output_preset?: string
  output_duration_ms?: number
  output_size_bytes?: number
  output_digest_sha256?: string
  output_artifact?: {
    collection_id: string
    variant_id: string
  }
  created_at: number
  updated_at: number
}

type VideoClipWire = {
  id?: string
  name?: string
  path?: string
  ref?: string
  source_ref?: string
  extension?: string
  size_bytes?: number
  sizeBytes?: number
  modified_at?: number
  modifiedAt?: number
}

type VideoClipRequest = {
  id: string
  name: string
  source_ref: string
  extension: string
  size_bytes: number
  modified_at: number
}

export type VideoThreadRecord = {
  id: string
  title: string
  workspacePath: string
  workspaceName: string
  videoFolders: string[]
  videoClips: VideoClip[]
  videoClipOrder: string[]
  metadata?: Record<string, unknown>
  createdAt: number
  updatedAt: number
}

type VideoScanResponse = {
  ok?: boolean
  workspace_path?: string
  folder_path?: string
  clips?: VideoClipWire[]
}

type VideoThreadWire = {
  id?: string
  title?: string
  workspace_path?: string
  workspace_name?: string
  video_folders?: string[]
  video_clips?: VideoClipWire[]
  video_clip_order?: string[]
  metadata?: Record<string, unknown>
  created_at?: number
  updated_at?: number
}

export type TimelineSegment = {
  id: string
  type: 'video' | 'image' | 'frame'
  clipId: string
  src: string
  sourceKind?: string
  title?: string
  onScreenText?: string
  frameDirection?: string
  track?: number
  sequence?: number
  layer?: number
  timelinePositioned?: boolean
  start: number
  sourceStart: number
  duration: number
  visible: boolean
  transitionIn?: VideoTransitionWire
}

type TimelineLayoutSegment = TimelineSegment & {
  timelineStart: number
  timelineEnd: number
}

const TIMELINE_METADATA_KEY = 'timelineSegments'
const VIDEO_TOOL_BLACK_MODE_STORAGE_KEY = 'swarm.videoTool.blackMode'
const VIDEO_STUDIO_LAST_SESSION_STORAGE_KEY = 'swarm.videoStudio.lastSession'
const DEFAULT_VIDEO_SESSION_TITLE = 'Swarm launch video'
export const VIDEO_STUDIO_AGENT_NAME = 'swarm'

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object'
}

function metadataStringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.map((entry) => String(entry ?? '').trim()).filter(Boolean)
    : []
}

function mapVideoClip(entry: unknown): VideoClip | null {
  if (!isRecord(entry)) {
    return null
  }
  const sourceRef = String(entry.source_ref ?? entry.ref ?? entry.id ?? '').trim()
  const id = String(entry.id ?? sourceRef).trim()
  const name = String(entry.name ?? '').trim()
  if (!id || !name || !sourceRef) {
    return null
  }
  return {
    id,
    name,
    sourceRef,
    extension: String(entry.extension ?? '').trim(),
    sizeBytes: typeof entry.size_bytes === 'number'
      ? entry.size_bytes
      : typeof entry.sizeBytes === 'number'
        ? entry.sizeBytes
        : 0,
    modifiedAt: typeof entry.modified_at === 'number'
      ? entry.modified_at
      : typeof entry.modifiedAt === 'number'
        ? entry.modifiedAt
        : 0,
  }
}

function metadataClips(value: unknown): VideoClip[] {
  if (!Array.isArray(value)) {
    return []
  }
  return value
    .map(mapVideoClip)
    .filter((entry): entry is VideoClip => Boolean(entry))
}

export function serializeVideoClipForRequest(clip: VideoClip): VideoClipRequest {
  return {
    id: clip.id,
    name: clip.name,
    source_ref: clip.sourceRef,
    extension: clip.extension,
    size_bytes: clip.sizeBytes,
    modified_at: clip.modifiedAt,
  }
}

function mapVideoThread(wire: VideoThreadWire): VideoThreadRecord | null {
  const id = String(wire.id ?? '').trim()
  const workspacePath = String(wire.workspace_path ?? '').trim()
  if (!id || !workspacePath) {
    return null
  }
  return {
    id,
    title: String(wire.title ?? '').trim(),
    workspacePath,
    workspaceName: String(wire.workspace_name ?? '').trim(),
    videoFolders: metadataStringArray(wire.video_folders),
    videoClips: metadataClips(wire.video_clips),
    videoClipOrder: metadataStringArray(wire.video_clip_order),
    metadata: isRecord(wire.metadata) ? wire.metadata : undefined,
    createdAt: typeof wire.created_at === 'number' ? wire.created_at : 0,
    updatedAt: typeof wire.updated_at === 'number' ? wire.updated_at : 0,
  }
}

function orderedClips(thread: VideoThreadRecord | null): VideoClip[] {
  if (!thread) {
    return []
  }
  const byId = new Map(thread.videoClips.map((clip) => [clip.id, clip]))
  const ordered: VideoClip[] = []
  for (const id of thread.videoClipOrder) {
    const clip = byId.get(id)
    if (clip) {
      ordered.push(clip)
      byId.delete(id)
    }
  }
  const remaining = Array.from(byId.values()).sort((left, right) => left.name.localeCompare(right.name))
  return [...ordered, ...remaining]
}

function formatFolderLabel(path: string): string {
  const normalized = path.replace(/[\\/]+$/, '')
  const parts = normalized.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || path
}

function videoSessionTitle(folderPath: string): string {
  const label = formatFolderLabel(folderPath)
  return label ? `Video: ${label}` : 'Video Session'
}

function threadFolderPath(thread: VideoThreadRecord | null): string {
  return thread?.videoFolders[0] || thread?.workspacePath || ''
}

function formatStartedAt(value: number): string {
  if (!value) {
    return 'Date unavailable'
  }
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  }).format(new Date(value))
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return 'Size unavailable'
  }
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let size = value
  let unitIndex = 0
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024
    unitIndex += 1
  }
  return `${size >= 10 || unitIndex === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[unitIndex]}`
}

function formatTimelineTime(value: number): string {
  const safe = Number.isFinite(value) && value > 0 ? value : 0
  const minutes = Math.floor(safe / 60)
  const seconds = Math.floor(safe % 60)
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
}

function finiteNonNegative(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : fallback
}

function clipDuration(clipDurations: Record<string, number>, clipId: string): number {
  const duration = clipDurations[clipId]
  return Number.isFinite(duration) && duration > 0 ? duration : 0
}

function clipMediaUrl(threadId: string, clipId: string): string {
  const search = new URLSearchParams({ clip_id: clipId })
  return `/v1/workspace/video/threads/${encodeURIComponent(threadId)}/clips/media?${search.toString()}`
}

function timelineSegmentId(clipId: string): string {
  return `segment-${clipId}`
}

function buildTimelineSegments(thread: VideoThreadRecord | null, clips: VideoClip[], clipDurations: Record<string, number>): TimelineSegment[] {
  if (!thread) {
    return []
  }
  const metadataSegments = Array.isArray(thread.metadata?.[TIMELINE_METADATA_KEY])
    ? thread.metadata?.[TIMELINE_METADATA_KEY] as unknown[]
    : []
  const clipsById = new Map(clips.map((clip) => [clip.id, clip]))
  const usedClipIds = new Set<string>()
  const segments: TimelineSegment[] = []

  for (const entry of metadataSegments) {
    if (!isRecord(entry)) {
      continue
    }
    const clipId = String(entry.clipId ?? entry.clip_id ?? '').trim()
    const clip = clipsById.get(clipId)
    if (!clip || usedClipIds.has(clipId)) {
      continue
    }
    const mediaDuration = clipDuration(clipDurations, clipId)
    const sourceStart = Math.min(finiteNonNegative(entry.sourceStart, 0), mediaDuration || Number.MAX_SAFE_INTEGER)
    segments.push({
      id: String(entry.id ?? '').trim() || timelineSegmentId(clipId),
      type: 'video',
      clipId,
      src: clipMediaUrl(thread.id, clipId),
      sourceKind: 'source_video',
      start: 0,
      sourceStart,
      duration: mediaDuration > 0 ? Math.max(0, mediaDuration - sourceStart) : 0,
      visible: entry.visible !== false,
    })
    usedClipIds.add(clipId)
  }

  for (const clip of clips) {
    if (usedClipIds.has(clip.id)) {
      continue
    }
    segments.push({
      id: timelineSegmentId(clip.id),
      type: 'video',
      clipId: clip.id,
      src: clipMediaUrl(thread.id, clip.id),
      sourceKind: 'source_video',
      start: 0,
      sourceStart: 0,
      duration: clipDuration(clipDurations, clip.id),
      visible: true,
    })
  }

  let start = 0
  return segments.map((segment) => {
    const next = { ...segment, start }
    if (segment.visible) {
      start += segment.duration
    }
    return next
  })
}

function transitionOverlapSeconds(transition: VideoTransitionWire | undefined, previousSegmentId: string): number {
  if (!transition || transition.kind === 'cut' || transition.from_clip_id !== previousSegmentId) return 0
  return Math.max(0, finiteNonNegative(transition.duration_ms, 0) / 1000)
}

export function layoutTimelineSegments(segments: TimelineSegment[]): TimelineLayoutSegment[] {
  const trackEnds = new Map<number, number>()
  const previousVisibleByTrack = new Map<number, TimelineSegment>()
  return segments.map((segment) => {
    const track = segment.track ?? 0
    const trackEnd = trackEnds.get(track) ?? 0
    if (!segment.visible || segment.duration <= 0) {
      const timelineStart = segment.timelinePositioned ? Math.max(0, segment.start) : trackEnd
      return { ...segment, start: timelineStart, timelineStart, timelineEnd: timelineStart }
    }
    const previousVisible = previousVisibleByTrack.get(track) ?? null
    const overlap = previousVisible ? Math.min(transitionOverlapSeconds(segment.transitionIn, previousVisible.id), previousVisible.duration, segment.duration) : 0
    const timelineStart = previousVisible
      ? Math.max(0, trackEnd - overlap)
      : segment.timelinePositioned
        ? Math.max(0, segment.start)
        : trackEnd
    const laidOut = { ...segment, start: timelineStart, timelineStart, timelineEnd: timelineStart + segment.duration }
    trackEnds.set(track, laidOut.timelineEnd)
    previousVisibleByTrack.set(track, segment)
    return laidOut
  })
}

function timelineDuration(layout: TimelineLayoutSegment[]): number {
  return layout.reduce((duration, segment) => segment.visible && segment.duration > 0 ? Math.max(duration, segment.timelineEnd) : duration, 0)
}

function timelineTrackWidth(duration: number): number {
  if (!Number.isFinite(duration) || duration <= 0) {
    return 720
  }
  return Math.max(720, Math.ceil(duration * 24))
}

function activeTimelineSegments(layout: TimelineLayoutSegment[], playhead: number): TimelineLayoutSegment[] {
  const visible = layout.filter((segment) => segment.visible && segment.duration > 0 && segment.timelineEnd > segment.timelineStart)
  if (visible.length === 0) return []
  const active = visible.filter((segment) => playhead >= segment.timelineStart && playhead < segment.timelineEnd)
  if (active.length > 0) return active.sort((left, right) => (left.layer ?? left.track ?? 0) - (right.layer ?? right.track ?? 0) || (left.track ?? 0) - (right.track ?? 0))
  const ended = visible.filter((segment) => playhead >= segment.timelineEnd).sort((left, right) => right.timelineEnd - left.timelineEnd)
  return ended.length > 0 ? [ended[0]] : []
}

function activeTimelineSegment(layout: TimelineLayoutSegment[], playhead: number): TimelineLayoutSegment | null {
  const active = activeTimelineSegments(layout, playhead)
  return active[active.length - 1] ?? null
}

function transitionPreviewOpacity(segments: TimelineLayoutSegment[], index: number, playhead: number): number {
  if (segments.length < 2) return 1
  const incoming = segments[segments.length - 1]
  const transition = incoming.transitionIn
  if (!transition || transition.kind === 'cut' || !transition.duration_ms) return 1
  const progress = Math.max(0, Math.min(1, (playhead - incoming.timelineStart) / (transition.duration_ms / 1000)))
  if (transition.kind === 'fade_through_black' || transition.kind === 'fade_to_black' || transition.kind === 'fade_from_black') {
    return index === 0 ? Math.max(0, 1 - progress * 2) : Math.max(0, (progress - 0.5) * 2)
  }
  return index === 0 ? 1 : progress
}

async function scanVideoFolder(workspacePath: string, folderPath: string): Promise<{ folderPath: string; clips: VideoClip[] }> {
  const response = await requestJson<VideoScanResponse>('/v1/workspace/video/scan', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      workspace_path: workspacePath,
      folder_path: folderPath,
    }),
  })
  return {
    folderPath: String(response.folder_path ?? folderPath).trim(),
    clips: metadataClips(response.clips),
  }
}

async function fetchVideoThreads(workspacePath: string): Promise<VideoThreadRecord[]> {
  const search = new URLSearchParams({ workspace_path: workspacePath })
  const response = await requestJson<{ threads?: VideoThreadWire[] }>(`/v1/workspace/video/threads?${search.toString()}`)
  return (Array.isArray(response.threads) ? response.threads : [])
    .map(mapVideoThread)
    .filter((thread): thread is VideoThreadRecord => Boolean(thread))
}

export function videoStudioSessionMetadata(): Record<string, unknown> {
  return {
    experience: 'video_studio',
    launch_source: 'video_tool',
    lineage_kind: 'video_project',
  }
}

export function resolveVideoStudioSessionRoute(
  workspace: WorkspaceEntry | null,
  swarmTarget: WorkspaceOverviewSwarmTarget | null,
): DesktopChatRoute | null {
  const swarmId = swarmTarget?.swarmId.trim() ?? ''
  const workspaceBindingId = workspace?.localWorkspaceBindingId.trim() ?? ''
  if (!workspace || !swarmId || !workspaceBindingId) return null
  const selfTopologyRoute = workspace.topologyRoutes.find((route) => (
    route.workspaceBindingId === workspaceBindingId
    && route.runtimeSwarmId === swarmId
    && route.runtimeRelationship.trim().toLowerCase() === 'self'
    && ['host', 'self'].includes(route.runtimeKind.trim().toLowerCase())
  ))
  if (!selfTopologyRoute) return null
  return buildDesktopChatRouteOptions({
    hostSwarmName: swarmTarget?.name || 'host',
    workspacePath: workspace.path,
    workspaceName: workspace.workspaceName,
    topologyRoutes: workspace.topologyRoutes,
    localWorkspaceBindingId: workspaceBindingId,
    hostSwarmId: swarmId,
  }).find((route) => {
    const target = getDesktopSessionCreateTarget(route)
    return target.endpoint === '/v3/sessions'
      && target.swarmId === swarmId
      && target.workspaceBindingId === workspaceBindingId
  }) ?? null
}

export async function createVideoThread(input: {
  title: string
  workspacePath: string
  workspaceName: string
  route: DesktopChatRoute
  folderPath?: string
  clips: VideoClip[]
}): Promise<VideoThreadRecord> {
  const preference = await fetchDraftModelPreference()
  const createdSession = await createSession({
    title: input.title,
    workspacePath: input.workspacePath,
    workspaceName: input.workspaceName,
    mode: 'auto',
    agentName: VIDEO_STUDIO_AGENT_NAME,
    preference: preference.preference,
    route: input.route,
    metadata: videoStudioSessionMetadata(),
  })
  const response = await requestJson<{ thread?: VideoThreadWire }>('/v1/workspace/video/threads', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      session_id: createdSession.id,
      title: input.title,
      workspace_path: input.workspacePath,
      workspace_name: input.workspaceName,
      video_folders: input.folderPath ? [input.folderPath] : [],
      video_clips: input.clips.map(serializeVideoClipForRequest),
      video_clip_order: input.clips.map((clip) => clip.id),
    }),
  })
  const thread = mapVideoThread(response.thread ?? {})
  if (!thread) {
    throw new Error('Video thread create returned no thread')
  }
  return thread
}

async function updateVideoThread(input: VideoThreadRecord): Promise<VideoThreadRecord> {
  const response = await requestJson<{ thread?: VideoThreadWire }>(`/v1/workspace/video/threads/${encodeURIComponent(input.id)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      title: input.title,
      video_folders: input.videoFolders,
      video_clips: input.videoClips.map(serializeVideoClipForRequest),
      video_clip_order: input.videoClipOrder,
      metadata: input.metadata,
    }),
  })
  const thread = mapVideoThread(response.thread ?? {})
  if (!thread) {
    throw new Error('Video thread update returned no thread')
  }
  return thread
}

export function timelineSegmentsToProjectTimeline(
  segments: TimelineSegment[],
  clips: VideoClip[],
  outputPreset: string = 'landscape_1080p',
): VideoProjectTimelineWire {
  const layout = layoutTimelineSegments(segments)
  const clipsById = new Map(clips.map((c) => [c.id, c]))
  const timelineClips: VideoTimelineClipWire[] = segments.map((seg, idx) => {
    const layoutSeg = layout.find((l) => l.id === seg.id)
    const clip = clipsById.get(seg.clipId)
    const dur = seg.duration > 0 ? seg.duration : (clip?.sizeBytes ? 5 : 0)
    return {
      id: seg.id,
      name: clip?.name ?? seg.clipId,
      track: 0,
      sequence: idx,
      source_kind: 'source_video',
      source_ref: clip?.sourceRef ?? seg.clipId,
      source_start_ms: Math.round(seg.sourceStart * 1000),
      source_end_ms: Math.round((seg.sourceStart + dur) * 1000),
      timeline_start_ms: layoutSeg ? Math.round(layoutSeg.timelineStart * 1000) : 0,
      timeline_end_ms: layoutSeg ? Math.round(layoutSeg.timelineEnd * 1000) : 0,
      duration_ms: Math.round(dur * 1000),
      visible: seg.visible,
      volume: 1.0,
    }
  })
  const [width, height] = outputPreset.startsWith('portrait') ? [1080, 1920] : outputPreset.startsWith('square') ? [1080, 1080] : [1920, 1080]
  let previousVisible: TimelineSegment | null = null
  const transitions = segments.flatMap((segment) => {
    if (!segment.visible) return []
    const transition = previousVisible && segment.transitionIn?.from_clip_id === previousVisible.id && segment.transitionIn.to_clip_id === segment.id
      ? [segment.transitionIn]
      : []
    previousVisible = segment
    return transition
  })
  return {
    schema_version: 1,
    output_preset: outputPreset,
    width,
    height,
    fps: 30,
    total_duration_ms: Math.round(timelineDuration(layout) * 1000),
    clips: timelineClips,
    transitions,
  }
}

function visualPlanTimeline(accepted: VideoProjectTimelineWire, plan: VideoPlanProposalWire, proposalId: string): VideoProjectTimelineWire {
  const currentPlan = acceptedVideoPlan(accepted)
  const parts = plan.kind === 'revision' && currentPlan
    ? currentPlan.parts.map((part) => plan.parts.find((replacement) => replacement.id === part.id) ?? part)
    : plan.parts
  let startMs = 0
  const planClips = parts.map((part, index): VideoTimelineClipWire => {
    const endMs = startMs + part.duration_ms
    const clip = {
      id: part.id,
      name: `${part.title} | Narration: ${part.narration ?? ''} | Planned still: ${part.visual_direction ?? ''}`,
      track: 0,
      sequence: index,
      source_kind: 'managed_artifact',
      artifact_ref: part.visual,
      media_type: part.visual_media_type,
      timeline_start_ms: startMs,
      timeline_end_ms: endMs,
      duration_ms: part.duration_ms,
      visible: true,
      captions: part.on_screen_text ? [{ id: `${part.id}-caption`, text: part.on_screen_text, position: 'center', start_ms: startMs, end_ms: endMs }] : [],
    }
    startMs = endMs
    return clip
  })
  const planPartIds = new Set(parts.map((part) => part.id))
  const auxiliaryClips = accepted.clips.filter((clip) => !planPartIds.has(clip.id))
  const clips = [...planClips, ...auxiliaryClips]
  const transitions = [
    ...parts.slice(1).map((part, index): VideoTransitionWire => ({
      id: `transition-${part.id}`,
      kind: 'crossfade',
      from_clip_id: parts[index].id,
      to_clip_id: part.id,
      duration_ms: 300,
    })),
    ...(accepted.transitions ?? []).filter((transition) => !planPartIds.has(transition.from_clip_id) || !planPartIds.has(transition.to_clip_id)),
  ]
  const totalDurationMs = auxiliaryClips.reduce((duration, clip) => Math.max(duration, clip.timeline_end_ms ?? ((clip.timeline_start_ms ?? 0) + (clip.duration_ms ?? 0))), startMs)
  const mergedPlan: VideoPlanProposalWire = { kind: 'initial', summary: plan.summary || currentPlan?.summary, parts }
  return {
    ...accepted,
    total_duration_ms: totalDurationMs,
    clips,
    transitions,
    metadata: { ...(accepted.metadata ?? {}), accepted_video_plan: mergedPlan, shadow_proposal_id: proposalId },
  }
}

export function applyPendingVideoProposal(
  accepted: VideoProjectTimelineWire,
  proposal: VideoEditProposalWire,
): VideoProjectTimelineWire {
  if (proposal.plan) return visualPlanTimeline(accepted, proposal.plan, proposal.id)
  const timeline: VideoProjectTimelineWire = {
    ...accepted,
    clips: accepted.clips.map((clip) => ({ ...clip, captions: clip.captions?.map((caption) => ({ ...caption })) })),
    transitions: (accepted.transitions ?? []).map((transition) => ({ ...transition })),
    metadata: { ...(accepted.metadata ?? {}), shadow_proposal_id: proposal.id },
  }
  for (const operation of proposal.operations) {
    switch (operation.type) {
      case 'add_clip':
        if (operation.clip) {
          const clip = operation.clip as VideoTimelineClipWire
          const existing = timeline.clips.findIndex((candidate) => candidate.id === clip.id)
          if (existing >= 0) timeline.clips[existing] = { ...timeline.clips[existing], ...clip }
          else timeline.clips.push({ ...clip })
        }
        break
      case 'update_clip':
        if (operation.clip) {
          const clip = operation.clip as VideoTimelineClipWire
          const existing = timeline.clips.findIndex((candidate) => candidate.id === clip.id)
          if (existing >= 0) timeline.clips[existing] = { ...timeline.clips[existing], ...clip }
        }
        break
      case 'remove_clip':
        timeline.clips = timeline.clips.filter((clip) => clip.id !== operation.clip_id)
        timeline.transitions = (timeline.transitions ?? []).filter((transition) => transition.from_clip_id !== operation.clip_id && transition.to_clip_id !== operation.clip_id)
        break
      case 'add_transition':
      case 'update_transition':
        if (operation.transition) {
          const existing = (timeline.transitions ?? []).findIndex((candidate) => candidate.id === operation.transition?.id)
          if (existing >= 0) timeline.transitions![existing] = { ...operation.transition }
          else timeline.transitions = [...(timeline.transitions ?? []), { ...operation.transition }]
        }
        break
      case 'remove_transition':
        timeline.transitions = (timeline.transitions ?? []).filter((transition) => transition.id !== operation.transition_id)
        break
    }
  }
  timeline.clips = timeline.clips
    .map((clip, index) => ({ ...clip, sequence: typeof clip.sequence === 'number' ? clip.sequence : index }))
    .sort((left, right) => (left.track ?? 0) - (right.track ?? 0) || (left.sequence ?? 0) - (right.sequence ?? 0))
  timeline.total_duration_ms = timeline.clips.reduce((duration, clip) => Math.max(duration, clip.timeline_end_ms ?? ((clip.timeline_start_ms ?? 0) + (clip.duration_ms ?? 0))), 0)
  return timeline
}

export function projectTimelineToTimelineSegments(
  timeline: VideoProjectTimelineWire,
  clipDurations: Record<string, number>,
  clips: VideoClip[] = [],
  threadId = '',
): TimelineSegment[] {
  if (!timeline || !Array.isArray(timeline.clips)) {
    return []
  }
  const clipsBySourceRef = new Map(clips.map((clip) => [clip.sourceRef, clip]))
  const clipsById = new Map(clips.map((clip) => [clip.id, clip]))
  const transitionsByDestination = new Map<string, VideoTransitionWire>()
  for (const transition of timeline.transitions ?? []) {
    if (!transitionsByDestination.has(transition.to_clip_id)) transitionsByDestination.set(transition.to_clip_id, transition)
  }
  let previewStartSec = 0
  return timeline.clips.map((clipWire, originalIndex): TimelineSegment => {
    const sourceClip = clipsBySourceRef.get(String(clipWire.source_ref ?? '')) ?? clipsById.get(clipWire.id)
    const clipId = sourceClip?.id ?? clipWire.id
    const sourceStartSec = (clipWire.source_start_ms ?? 0) / 1000
    const durationSec = (clipWire.duration_ms ?? 0) / 1000 || clipDuration(clipDurations, clipId)
    const transitionIn = transitionsByDestination.get(clipWire.id)
    const explicitStartMs = clipWire.timeline_start_ms
    const start = typeof explicitStartMs === 'number' && explicitStartMs >= 0
      ? explicitStartMs / 1000
      : Math.max(0, previewStartSec - ((transitionIn?.duration_ms ?? 0) / 1000))
    if (clipWire.visible !== false) previewStartSec = start + durationSec
    const details = videoPlanClipDetails(clipWire)
    const videoSource = clipWire.source_kind === 'source_video' && Boolean(clipWire.source_ref || sourceClip)
    const sourceRef = String(clipWire.source_ref ?? '').trim()
    const artifactRef = clipWire.artifact_ref ?? clipWire.design_input
    const artifactSessionId = String(artifactRef?.session_id || threadId).trim()
    const artifactId = String(artifactRef?.variant_id ?? '').trim()
    const artifactSource = clipWire.source_kind === 'managed_artifact' && Boolean(artifactSessionId && artifactId)
    const artifactMediaType = String(clipWire.media_type ?? artifactRef?.media_type ?? '').trim()
    const artifactVideo = artifactSource && artifactMediaType.startsWith('video/')
    return {
      id: clipWire.id,
      type: videoSource || artifactVideo ? 'video' : artifactSource ? 'image' : 'frame',
      clipId,
      src: videoSource ? (sourceClip && threadId ? clipMediaUrl(threadId, clipId) : threadId && sourceRef ? `/v3/sessions/${encodeURIComponent(threadId)}/video/sources/media?source_ref=${encodeURIComponent(sourceRef)}` : `/v1/workspace/video/threads/media?clip_id=${encodeURIComponent(clipId)}`) : artifactSource ? `/v3/sessions/${encodeURIComponent(artifactSessionId)}/artifacts/${encodeURIComponent(artifactId)}` : '',
      sourceKind: clipWire.source_kind,
      title: details.title,
      onScreenText: details.onScreenText,
      frameDirection: details.still,
      track: clipWire.track ?? 0,
      sequence: clipWire.sequence ?? originalIndex,
      layer: clipWire.layer ?? clipWire.track ?? 0,
      timelinePositioned: typeof explicitStartMs === 'number' && explicitStartMs >= 0,
      start,
      sourceStart: sourceStartSec,
      duration: durationSec,
      visible: clipWire.visible !== false,
      transitionIn,
    }
  }).sort((left, right) => left.start - right.start
    || (left.layer ?? left.track ?? 0) - (right.layer ?? right.track ?? 0)
    || (left.track ?? 0) - (right.track ?? 0)
    || (left.sequence ?? 0) - (right.sequence ?? 0))
}

export async function fetchPrimaryVideoProject(sessionId: string): Promise<VideoProjectDetailWire> {
  const response = await requestJson<{ project?: VideoProjectSnapshotWire; current_revision?: VideoProjectRevisionSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/primary`,
  )
  if (!response.project) throw new Error('Primary video project response returned no project')
  return { project: response.project, current_revision: response.current_revision }
}

export async function listVideoProjects(sessionId: string): Promise<VideoProjectSnapshotWire[]> {
  const response = await requestJson<{ projects?: VideoProjectSnapshotWire[] }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects?limit=32`,
  )
  return Array.isArray(response.projects) ? response.projects : []
}

export async function fetchVideoProject(sessionId: string, projectId: string): Promise<VideoProjectDetailWire> {
  const response = await requestJson<{ project?: VideoProjectSnapshotWire; current_revision?: VideoProjectRevisionSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}`,
  )
  if (!response.project) throw new Error('Video project response returned no project')
  return { project: response.project, current_revision: response.current_revision }
}

export async function createAdditionalVideoProject(sessionId: string, title: string, outputPreset = 'landscape_1080p'): Promise<VideoProjectDetailWire> {
  const response = await requestJson<{ project?: VideoProjectSnapshotWire; revision?: VideoProjectRevisionSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title,
        output_preset: outputPreset,
        initial_timeline: {
          schema_version: 1,
          output_preset: outputPreset,
          width: outputPreset.startsWith('portrait') ? 1080 : 1920,
          height: outputPreset.startsWith('portrait') ? 1920 : 1080,
          fps: 30,
          total_duration_ms: 0,
          clips: [],
          transitions: [],
        },
      }),
    },
  )
  if (!response.project) throw new Error('Video project create returned no project')
  return { project: response.project, current_revision: response.revision }
}

export async function ensurePrimaryVideoProject(sessionId: string, title: string, initialTimeline?: VideoProjectTimelineWire): Promise<VideoProjectDetailWire> {
  const timeline = initialTimeline ?? {
    schema_version: 1,
    output_preset: 'landscape_1080p',
    width: 1920,
    height: 1080,
    fps: 30,
    total_duration_ms: 0,
    clips: [],
    transitions: [],
  }
  const response = await requestJson<{ project?: VideoProjectSnapshotWire; revision?: VideoProjectRevisionSnapshotWire; current_revision?: VideoProjectRevisionSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/primary`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title, output_preset: timeline.output_preset, initial_timeline: timeline }),
    },
  )
  if (!response.project) throw new Error('Primary video project create returned no project')
  return { project: response.project, current_revision: response.current_revision ?? response.revision }
}

export async function createVideoProjectRevision(sessionId: string, projectId: string, timeline: VideoProjectTimelineWire, changeSummary: string): Promise<VideoProjectDetailWire> {
  const response = await requestJson<{ project?: VideoProjectSnapshotWire; revision?: VideoProjectRevisionSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}/revisions`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ timeline, change_summary: changeSummary }),
    },
  )
  if (!response.project || !response.revision) throw new Error('Video revision create returned incomplete state')
  return { project: response.project, current_revision: response.revision }
}

export async function listVideoProjectRevisions(sessionId: string, projectId: string): Promise<VideoProjectRevisionSnapshotWire[]> {
  const response = await requestJson<{ revisions?: VideoProjectRevisionSnapshotWire[] }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}/revisions?limit=100`,
  )
  return Array.isArray(response.revisions) ? response.revisions : []
}

export async function restoreVideoProjectRevision(sessionId: string, projectId: string, revisionId: string): Promise<VideoProjectDetailWire> {
  const response = await requestJson<{ project?: VideoProjectSnapshotWire; revision?: VideoProjectRevisionSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}/revisions/${encodeURIComponent(revisionId)}/restore`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ change_summary: 'Restored from Video Tool history' }),
    },
  )
  if (!response.project || !response.revision) throw new Error('Video revision restore returned incomplete state')
  return { project: response.project, current_revision: response.revision }
}

export function videoChildSessionMetadata(input: {
  thread: VideoThreadRecord
  projectId: string
  revisionId: string
  folderPath: string
  clips: VideoClip[]
}): Record<string, unknown> {
  return {
    parent_video_thread_id: input.thread.id,
    parent_session_id: input.thread.id,
    parent_video_project_id: input.projectId,
    parent_video_revision_id: input.revisionId,
    parent_title: input.thread.title,
    parent_folder_path: input.folderPath,
    video_thread_id: input.thread.id,
    video_folder_path: input.folderPath,
    video_clip_order: input.clips.map((clip) => clip.id),
    video_clip_count: input.clips.length,
    lineage_kind: 'video_child',
    launch_source: 'video_tool',
  }
}

export async function startVideoRender(sessionId: string, projectId: string, revisionId: string): Promise<VideoRenderJobSnapshotWire> {
  const response = await requestJson<{ ok?: boolean; render_job?: VideoRenderJobSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}/render`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ revision_id: revisionId }),
    },
  )
  if (!response.render_job) {
    throw new Error('Start video render returned no job')
  }
  return response.render_job
}

export async function getVideoRenderJob(sessionId: string, jobId: string): Promise<VideoRenderJobSnapshotWire> {
  const response = await requestJson<{ ok?: boolean; render_job?: VideoRenderJobSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/render-jobs/${encodeURIComponent(jobId)}`,
  )
  if (!response.render_job) {
    throw new Error('Get video render job returned no job')
  }
  return response.render_job
}

export async function exportRenderedVideo(
  sessionId: string,
  projectId: string,
  destinationPath: string,
  jobId?: string,
): Promise<{ destination_path: string }> {
  const response = await requestJson<{ ok?: boolean; destination_path?: string }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}/export`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ destination_path: destinationPath, job_id: jobId }),
    },
  )
  if (!response.destination_path) {
    throw new Error('Export video returned no destination path')
  }
  return { destination_path: response.destination_path }
}

export function VideoToolPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const matchRoute = useMatchRoute()
  const workspaceStudioSessionMatch = matchRoute({ to: '/$workspaceSlug/studio/$videoSessionId', fuzzy: false })
  const workspaceStudioMatch = matchRoute({ to: '/$workspaceSlug/studio', fuzzy: false })
  const workspaceVideoToolMatch = matchRoute({ to: '/$workspaceSlug/tools/video', fuzzy: false })
  const workspaceVideoSessionMatch = matchRoute({ to: '/$workspaceSlug/video/$videoSessionId', fuzzy: false })
  const routeWorkspaceSlug = (workspaceStudioSessionMatch
    ? workspaceStudioSessionMatch.workspaceSlug
    : workspaceVideoSessionMatch
      ? workspaceVideoSessionMatch.workspaceSlug
      : workspaceStudioMatch
        ? workspaceStudioMatch.workspaceSlug
        : workspaceVideoToolMatch
          ? workspaceVideoToolMatch.workspaceSlug
          : '').trim()
  const routeVideoSessionId = (workspaceStudioSessionMatch
    ? workspaceStudioSessionMatch.videoSessionId
    : workspaceVideoSessionMatch
      ? workspaceVideoSessionMatch.videoSessionId
      : '').trim()
  const [pickerOpen, setPickerOpen] = useState(false)
  const [browser, setBrowser] = useState<WorkspaceBrowseResult | null>(null)
  const [browserLoading, setBrowserLoading] = useState(false)
  const [browserError, setBrowserError] = useState<string | null>(null)
  const [browserClips, setBrowserClips] = useState<VideoClip[]>([])
  const [browserScanLoading, setBrowserScanLoading] = useState(false)
  const [browserScanError, setBrowserScanError] = useState<string | null>(null)
  const [addingFolderPath, setAddingFolderPath] = useState<string | null>(null)
  const [createError, setCreateError] = useState<string | null>(null)
  const [newSessionTitle, setNewSessionTitle] = useState('')
  const [creatingBlankSession, setCreatingBlankSession] = useState(false)
  const [selectedThreadId, setSelectedThreadId] = useState<string | null>(() => {
    if (routeVideoSessionId) return routeVideoSessionId
    if (typeof window === 'undefined') return null
    return window.localStorage.getItem(`${VIDEO_STUDIO_LAST_SESSION_STORAGE_KEY}:${routeWorkspaceSlug}`)?.trim() || null
  })
  const [selectedClipId, setSelectedClipId] = useState<string | null>(null)
  const [reordering, setReordering] = useState(false)
  const [videoProjects, setVideoProjects] = useState<VideoProjectSnapshotWire[]>([])
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null)
  const [videoProject, setVideoProject] = useState<VideoProjectSnapshotWire | null>(null)
  const [currentRevision, setCurrentRevision] = useState<VideoProjectRevisionSnapshotWire | null>(null)
  const [projectRevisions, setProjectRevisions] = useState<VideoProjectRevisionSnapshotWire[]>([])
  const [projectLoading, setProjectLoading] = useState(false)
  const [creatingProject, setCreatingProject] = useState(false)
  const [restoringRevisionId, setRestoringRevisionId] = useState<string | null>(null)
  const [transitionKind, setTransitionKind] = useState<VideoTransitionKind>('cut')
  const [aiRefreshKey, setAIRefreshKey] = useState(0)
  const [pendingProposal, setPendingProposal] = useState<VideoEditProposalWire | null>(null)
  const [composerDraftRequest, setComposerDraftRequest] = useState<{ id: number; draft: string } | undefined>()
  const [stepRequestBusyId, setStepRequestBusyId] = useState('')
  const [revealingStorage, setRevealingStorage] = useState(false)
  const [rendering, setRendering] = useState(false)
  const [renderJob, setRenderJob] = useState<VideoRenderJobSnapshotWire | null>(null)
  const [renderProgress, setRenderProgress] = useState(0)
  const [renderError, setRenderError] = useState<string | null>(null)
  const [exportPath, setExportPath] = useState('')
  const [exporting, setExporting] = useState(false)
  const [blackModeEnabled, setBlackModeEnabled] = useState(() => {
    if (typeof window === 'undefined') {
      return false
    }
    return window.localStorage.getItem(VIDEO_TOOL_BLACK_MODE_STORAGE_KEY) === 'true'
  })
  const [isPlaying, setIsPlaying] = useState(false)
  const [playhead, setPlayhead] = useState(0)
  const [clipDurations, setClipDurations] = useState<Record<string, number>>({})
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const timelineScrollRef = useRef<HTMLDivElement | null>(null)
  const videoElementsRef = useRef<Map<string, HTMLVideoElement>>(new Map())
  const imageElementsRef = useRef<Map<string, HTMLImageElement>>(new Map())
  const playheadRef = useRef(0)
  const playbackStartRef = useRef(0)
  const playbackStartPlayheadRef = useRef(0)

  const workspaceOverviewQuery = useQuery(workspaceOverviewQueryOptions([], 25))
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())
  const workspaces = workspaceOverviewQuery.data?.workspaces ?? []
  const workspaceSlugByPath = useMemo(() => buildWorkspaceRouteSlugMap(
    workspaces.map((workspace) => ({ path: workspace.path, workspaceName: workspace.workspaceName })),
  ), [workspaces])

  const selectedWorkspace = useMemo<WorkspaceEntry | null>(() => {
    if (routeWorkspaceSlug) {
      return resolveWorkspaceBySlug(workspaces, routeWorkspaceSlug)
    }
    return workspaces[0] ?? null
  }, [routeWorkspaceSlug, workspaces])

  const selectedWorkspacePath = selectedWorkspace?.path ?? ''
  const selectedWorkspaceName = selectedWorkspace?.workspaceName ?? ''
  const selectedSessionRoute = useMemo(
    () => resolveVideoStudioSessionRoute(selectedWorkspace, workspaceOverviewQuery.data?.swarmTarget ?? null),
    [selectedWorkspace, workspaceOverviewQuery.data?.swarmTarget],
  )
  const userThemeId = selectedWorkspace?.themeId?.trim() || normalizeGlobalThemeSettings(uiSettingsQuery.data).activeId
  const darkOverrideButtonStyle = useMemo(() => createWorkspaceThemeStyle(userThemeId, '--video-tool-user-theme') as CSSProperties, [userThemeId])

  const videoThreadsQuery = useQuery({
    queryKey: ['video-tool-threads', selectedWorkspacePath],
    queryFn: () => fetchVideoThreads(selectedWorkspacePath),
    enabled: selectedWorkspacePath.trim() !== '',
    staleTime: 15_000,
  })
  const videoThreads = videoThreadsQuery.data ?? []

  useEffect(() => {
    if (routeVideoSessionId && routeVideoSessionId !== selectedThreadId) {
      setSelectedThreadId(routeVideoSessionId)
      return
    }
    if (!selectedThreadId || videoThreadsQuery.isLoading || !videoThreadsQuery.isFetched) return
    if (!videoThreads.some((thread) => thread.id === selectedThreadId)) setSelectedThreadId(null)
  }, [routeVideoSessionId, selectedThreadId, videoThreads, videoThreadsQuery.isFetched, videoThreadsQuery.isLoading])

  useEffect(() => {
    if (!selectedThreadId || typeof window === 'undefined') return
    window.localStorage.setItem(`${VIDEO_STUDIO_LAST_SESSION_STORAGE_KEY}:${routeWorkspaceSlug}`, selectedThreadId)
  }, [routeWorkspaceSlug, selectedThreadId])

  const selectedThread = useMemo(() => {
    if (!selectedThreadId) {
      return null
    }
    return videoThreads.find((thread) => thread.id === selectedThreadId) ?? null
  }, [selectedThreadId, videoThreads])

  const selectedClips = useMemo(() => orderedClips(selectedThread), [selectedThread])
  const selectedFolderPath = threadFolderPath(selectedThread)
  const legacyTimelineSegments = useMemo(() => buildTimelineSegments(selectedThread, selectedClips, clipDurations), [clipDurations, selectedClips, selectedThread])
  const acceptedTimelineSegments = useMemo(() => currentRevision
    ? projectTimelineToTimelineSegments(currentRevision.timeline, clipDurations, selectedClips, selectedThread?.id ?? '')
    : legacyTimelineSegments, [clipDurations, currentRevision, legacyTimelineSegments, selectedClips, selectedThread?.id])
  const shadowTimeline = useMemo(() => pendingProposal && currentRevision && pendingProposal.base_revision_id === currentRevision.id
    ? applyPendingVideoProposal(currentRevision.timeline, pendingProposal)
    : null, [currentRevision, pendingProposal])
  const timelineSegments = useMemo(() => shadowTimeline
    ? projectTimelineToTimelineSegments(shadowTimeline, clipDurations, selectedClips, selectedThread?.id ?? '')
    : acceptedTimelineSegments, [acceptedTimelineSegments, clipDurations, selectedClips, selectedThread?.id, shadowTimeline])
  const timelineLayout = useMemo(() => layoutTimelineSegments(timelineSegments), [timelineSegments])
  const timelineLayoutByClipId = useMemo(() => new Map(timelineLayout.map((segment) => [segment.clipId, segment])), [timelineLayout])
  const visibleTimelineLayout = useMemo(() => timelineLayout.filter((segment) => segment.visible && segment.duration > 0), [timelineLayout])
  const hiddenTimelineLayout = useMemo(() => timelineLayout.filter((segment) => !segment.visible), [timelineLayout])
  const movieDuration = useMemo(() => timelineDuration(timelineLayout), [timelineLayout])
  const timelineTrackWidthPx = useMemo(() => timelineTrackWidth(movieDuration), [movieDuration])
  const playheadX = movieDuration > 0 ? Math.min(timelineTrackWidthPx, Math.max(0, (playhead / movieDuration) * timelineTrackWidthPx)) : 0
  const activeSegment = useMemo(() => activeTimelineSegment(timelineLayout, playhead), [playhead, timelineLayout])
  const acceptedPlan = useMemo(() => currentRevision ? acceptedVideoPlan(currentRevision.timeline) : null, [currentRevision])
  const hasUnresolvedPlanFrames = timelineSegments.some((segment) => segment.sourceKind === 'text' || (segment.sourceKind === 'managed_artifact' && !segment.src))
  const selectedClip = selectedClips.find((clip) => clip.id === selectedClipId) ?? selectedClips[0] ?? null

  useEffect(() => {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(VIDEO_TOOL_BLACK_MODE_STORAGE_KEY, blackModeEnabled ? 'true' : 'false')
    }
    if (routeWorkspaceSlug && workspaceOverviewQuery.isPending && !selectedWorkspace) {
      return
    }
    applyWorkspaceTheme(blackModeEnabled ? 'black' : userThemeId)
  }, [blackModeEnabled, routeWorkspaceSlug, selectedWorkspace, userThemeId, workspaceOverviewQuery.isPending])

  useEffect(() => {
    let cancelled = false
    setVideoProjects([])
    setSelectedProjectId(null)
    setVideoProject(null)
    setCurrentRevision(null)
    setProjectRevisions([])
    setPendingProposal(null)
    setRenderJob(null)
    if (!selectedThread) return
    setProjectLoading(true)
    void (async () => {
      try {
        let projects = await listVideoProjects(selectedThread.id)
        let preferred = preferredVisibleVideoProject(projects)
        if (projects.length === 0 || (preferred?.project_kind === 'video_tool' && !preferred.current_revision_id)) {
          const ensured = await ensurePrimaryVideoProject(selectedThread.id, selectedThread.title || 'Video Tool project')
          projects = await listVideoProjects(selectedThread.id)
          if (projects.length === 0) projects = [ensured.project]
          preferred = preferredVisibleVideoProject(projects)
        }
        if (cancelled) return
        setVideoProjects(projects)
        setSelectedProjectId(preferred?.id ?? null)
      } catch (error) {
        if (!cancelled) setCreateError(error instanceof Error ? error.message : String(error))
      } finally {
        if (!cancelled) setProjectLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [selectedThread?.id])

  useEffect(() => {
    let cancelled = false
    setVideoProject(null)
    setCurrentRevision(null)
    setProjectRevisions([])
    setPendingProposal(null)
    setRenderJob(null)
    if (!selectedThread || !selectedProjectId) return
    setProjectLoading(true)
    void (async () => {
      try {
        const detail = await fetchVideoProject(selectedThread.id, selectedProjectId)
        let selectedRevision = detail.current_revision ?? null
        if (!selectedRevision && detail.project.current_revision_id) {
          const current = await requestJson<{ revision?: VideoProjectRevisionSnapshotWire }>(
            `/v3/sessions/${encodeURIComponent(selectedThread.id)}/video/projects/${encodeURIComponent(detail.project.id)}/revisions/${encodeURIComponent(detail.project.current_revision_id)}`,
          )
          selectedRevision = current.revision ?? null
        }
        if (cancelled) return
        setVideoProject(detail.project)
        setCurrentRevision(selectedRevision)
        setProjectRevisions(await listVideoProjectRevisions(selectedThread.id, detail.project.id))
      } catch (error) {
        if (!cancelled) setCreateError(error instanceof Error ? error.message : String(error))
      } finally {
        if (!cancelled) setProjectLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [selectedProjectId, selectedThread?.id])

  const refreshSelectedVideoProject = useCallback(async () => {
    if (!selectedThread) return
    const projects = await listVideoProjects(selectedThread.id)
    const preferred = preferredVisibleVideoProject(projects)
    const projectId = preferred?.id ?? selectedProjectId
    setVideoProjects(projects)
    if (!projectId) return
    const [detail, revisions] = await Promise.all([
      fetchVideoProject(selectedThread.id, projectId),
      listVideoProjectRevisions(selectedThread.id, projectId),
    ])
    setSelectedProjectId(projectId)
    setVideoProject(detail.project)
    setCurrentRevision(detail.current_revision ?? null)
    setProjectRevisions(revisions)
  }, [selectedProjectId, selectedThread])

  const handleCreateAdditionalProject = useCallback(async () => {
    if (!selectedThread || creatingProject) return
    setCreatingProject(true)
    setCreateError(null)
    try {
      const title = `Video ${videoProjects.length + 1}`
      const detail = await createAdditionalVideoProject(selectedThread.id, title, videoProject?.output_preset)
      setVideoProjects(await listVideoProjects(selectedThread.id))
      setSelectedProjectId(detail.project.id)
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setCreatingProject(false)
    }
  }, [creatingProject, selectedThread, videoProject?.output_preset, videoProjects.length])

  useEffect(() => {
    if (!selectedThread) {
      setSelectedClipId(null)
      setIsPlaying(false)
      setPlayhead(0)
      setClipDurations({})
      return
    }
    if (selectedClipId && selectedClips.some((clip) => clip.id === selectedClipId)) {
      return
    }
    setSelectedClipId(selectedClips[0]?.id ?? null)
  }, [selectedClipId, selectedClips, selectedThread])

  useEffect(() => {
    if (movieDuration <= 0 && playhead !== 0) {
      setPlayhead(0)
      return
    }
    if (movieDuration > 0 && playhead > movieDuration) {
      setPlayhead(movieDuration)
    }
  }, [movieDuration, playhead])

  useEffect(() => {
    if (activeSegment) {
      setSelectedClipId(activeSegment.clipId)
    }
  }, [activeSegment])

  useEffect(() => {
    playheadRef.current = playhead
  }, [playhead])

  useEffect(() => {
    const cache = videoElementsRef.current
    const mediaByClipId = new Map(timelineSegments.filter((segment) => segment.type === 'video' && segment.src).map((segment) => [segment.clipId, segment.src]))
    for (const [clipId, video] of cache.entries()) {
      if (!mediaByClipId.has(clipId)) {
        video.pause()
        video.removeAttribute('src')
        video.load()
        cache.delete(clipId)
      }
    }
    setClipDurations((current) => {
      const next = Object.fromEntries(Object.entries(current).filter(([clipId]) => mediaByClipId.has(clipId)))
      return Object.keys(next).length === Object.keys(current).length ? current : next
    })
    for (const [clipId, src] of mediaByClipId) {
      if (cache.has(clipId)) continue
      const video = document.createElement('video')
      video.src = src
      video.preload = 'metadata'
      video.muted = true
      video.playsInline = true
      const updateDuration = () => {
        const duration = video.duration
        if (!Number.isFinite(duration) || duration <= 0) return
        setClipDurations((current) => Math.abs((current[clipId] ?? 0) - duration) < 0.001 ? current : { ...current, [clipId]: duration })
      }
      video.addEventListener('loadedmetadata', updateDuration)
      video.addEventListener('durationchange', updateDuration)
      video.load()
      cache.set(clipId, video)
    }
  }, [timelineSegments])

  useEffect(() => {
    const cache = imageElementsRef.current
    const mediaByClipId = new Map(timelineSegments.filter((segment) => segment.type === 'image' && segment.src).map((segment) => [segment.clipId, segment.src]))
    for (const clipId of cache.keys()) if (!mediaByClipId.has(clipId)) cache.delete(clipId)
    for (const [clipId, src] of mediaByClipId) {
      if (cache.has(clipId)) continue
      const image = new Image()
      image.decoding = 'async'
      image.src = src
      cache.set(clipId, image)
    }
  }, [timelineSegments])

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) {
      return
    }
    const context = canvas.getContext('2d')
    if (!context) {
      return
    }
    let frame = 0
    const render = () => {
      const duration = timelineDuration(timelineLayout)
      let nextPlayhead = playheadRef.current
      if (isPlaying && duration > 0) {
        nextPlayhead = Math.min(duration, playbackStartPlayheadRef.current + (performance.now() - playbackStartRef.current) / 1000)
        if (nextPlayhead >= duration) {
          setIsPlaying(false)
        }
        playheadRef.current = nextPlayhead
        setPlayhead(nextPlayhead)
      }

      context.fillStyle = 'black'
      context.fillRect(0, 0, canvas.width, canvas.height)
      const activeSegments = activeTimelineSegments(timelineLayout, nextPlayhead)
      if (activeSegments.length === 0) {
        for (const cachedVideo of videoElementsRef.current.values()) {
          if (!cachedVideo.paused) cachedVideo.pause()
        }
        frame = window.requestAnimationFrame(render)
        return
      }
      const activeClipIds = new Set(activeSegments.map((segment) => segment.clipId))
      for (const [clipId, cachedVideo] of videoElementsRef.current.entries()) {
        if (!activeClipIds.has(clipId) && !cachedVideo.paused) cachedVideo.pause()
      }
      for (const [segmentIndex, segment] of activeSegments.entries()) {
        const opacity = transitionPreviewOpacity(activeSegments, segmentIndex, nextPlayhead)
        if (segment.type === 'image') {
          const image = imageElementsRef.current.get(segment.clipId)
          if (!image?.complete || image.naturalWidth <= 0) continue
          const scale = Math.min(canvas.width / image.naturalWidth, canvas.height / image.naturalHeight)
          const drawWidth = Math.max(1, image.naturalWidth * scale)
          const drawHeight = Math.max(1, image.naturalHeight * scale)
          context.save()
          context.globalAlpha = opacity
          context.drawImage(image, (canvas.width - drawWidth) / 2, (canvas.height - drawHeight) / 2, drawWidth, drawHeight)
          context.restore()
          continue
        }
        if (segment.type === 'frame') {
          context.save()
          context.globalAlpha = opacity
          context.fillStyle = segment.sourceKind === 'text' ? '#0b1020' : '#161616'
          context.fillRect(0, 0, canvas.width, canvas.height)
          context.textAlign = 'center'
          context.textBaseline = 'middle'
          context.fillStyle = '#f8fafc'
          context.font = '600 78px system-ui, sans-serif'
          context.fillText(segment.onScreenText || segment.title || 'Planned frame', canvas.width / 2, canvas.height / 2 - 32, canvas.width * 0.78)
          context.fillStyle = '#94a3b8'
          context.font = '34px system-ui, sans-serif'
          context.fillText(segment.frameDirection || `${(segment.sourceKind || 'frame').replace(/_/g, ' ')} · pending preview`, canvas.width / 2, canvas.height / 2 + 70, canvas.width * 0.72)
          context.restore()
          continue
        }
        const video = videoElementsRef.current.get(segment.clipId)
        if (!video) continue
        const sourceTime = segment.sourceStart + Math.max(0, nextPlayhead - segment.timelineStart)
        if (Number.isFinite(sourceTime) && Math.abs(video.currentTime - sourceTime) > 0.08) {
          try {
            video.currentTime = sourceTime
          } catch {
            // Browser may reject seeks before metadata is ready; the next frame retries.
          }
        }
        if (isPlaying && video.paused) void video.play().catch(() => undefined)
        if (!isPlaying && !video.paused) video.pause()
        if (video.readyState < HTMLMediaElement.HAVE_CURRENT_DATA) continue
        const scale = Math.min(canvas.width / Math.max(1, video.videoWidth), canvas.height / Math.max(1, video.videoHeight))
        const drawWidth = Math.max(1, video.videoWidth * scale)
        const drawHeight = Math.max(1, video.videoHeight * scale)
        context.save()
        context.globalAlpha = opacity
        context.drawImage(video, (canvas.width - drawWidth) / 2, (canvas.height - drawHeight) / 2, drawWidth, drawHeight)
        context.restore()
      }
      frame = window.requestAnimationFrame(render)
    }
    frame = window.requestAnimationFrame(render)
    return () => window.cancelAnimationFrame(frame)
  }, [isPlaying, timelineLayout])

  const handleBackToWorkspace = useMemo(() => {
    if (routeWorkspaceSlug) {
      return () => {
        void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug } })
      }
    }
    return () => {
      void navigate({ to: '/' })
    }
  }, [navigate, routeWorkspaceSlug])

  const handleBackToTools = useCallback(() => {
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/tools', params: { workspaceSlug: routeWorkspaceSlug } })
      return
    }
    void navigate({ to: '/tools' })
  }, [navigate, routeWorkspaceSlug])

  const handleSelectVideoSession = useCallback((sessionId: string) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) return
    setSelectedThreadId(normalizedSessionId)
    const workspaceSlug = routeWorkspaceSlug
      || workspaceSlugByPath.get(selectedWorkspacePath)
      || workspaceRouteSlugBase({ path: selectedWorkspacePath, workspaceName: selectedWorkspaceName })
    if (!workspaceSlug) return
    void navigate({ to: '/$workspaceSlug/studio/$videoSessionId', params: { workspaceSlug, videoSessionId: normalizedSessionId } })
  }, [navigate, routeWorkspaceSlug, selectedWorkspaceName, selectedWorkspacePath, workspaceSlugByPath])

  const handleOpenSessionMode = useCallback(() => {
    if (!selectedThread || !routeWorkspaceSlug) return
    void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug: routeWorkspaceSlug, sessionId: selectedThread.id } })
  }, [navigate, routeWorkspaceSlug, selectedThread])

  const handleSelectWorkspace = useCallback((workspacePath: string) => {
    const workspace = workspaces.find((candidate) => candidate.path === workspacePath)
    if (!workspace) return
    const workspaceSlug = workspaceSlugByPath.get(workspace.path)
      ?? workspaceRouteSlugBase({ path: workspace.path, workspaceName: workspace.workspaceName })
    setSelectedThreadId(null)
    setSelectedClipId(null)
    void navigate({ to: '/$workspaceSlug/studio', params: { workspaceSlug } })
  }, [navigate, workspaceSlugByPath, workspaces])

  const loadBrowser = useCallback(async (path: string) => {
    setBrowserLoading(true)
    setBrowserError(null)
    setBrowserClips([])
    setBrowserScanError(null)
    try {
      const next = await browseWorkspacePath(path)
      setBrowser(next)
      if (selectedWorkspacePath) {
        setBrowserScanLoading(true)
        try {
          const scanned = await scanVideoFolder(selectedWorkspacePath, next.resolvedPath)
          setBrowserClips(scanned.clips)
        } catch (scanError) {
          setBrowserScanError(scanError instanceof Error ? scanError.message : String(scanError))
        } finally {
          setBrowserScanLoading(false)
        }
      }
    } catch (error) {
      setBrowserError(error instanceof Error ? error.message : String(error))
    } finally {
      setBrowserLoading(false)
    }
  }, [selectedWorkspacePath])

  useEffect(() => {
    if (!pickerOpen) {
      return
    }
    if (browser || browserLoading) {
      return
    }
    void loadBrowser(selectedWorkspacePath || '')
  }, [browser, browserLoading, loadBrowser, pickerOpen, selectedWorkspacePath])

  const handleRevealVideoStorage = useCallback(async (clipId?: string) => {
    if (!selectedThread) return
    setRevealingStorage(true)
    setCreateError(null)
    try {
      const search = new URLSearchParams({ thread_id: selectedThread.id })
      if (clipId) search.set('clip_id', clipId)
      await requestJson<{ ok?: boolean; path?: string; method?: string }>(`/v1/workspace/video/storage/reveal?${search.toString()}`, { method: 'POST' })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setRevealingStorage(false)
    }
  }, [selectedThread])

  const handleCancelRender = useCallback(async () => {
    if (!selectedThread || !renderJob) return
    setRenderError(null)
    try {
      await requestVideoRenderCancellation(selectedThread.id, renderJob.id)
      const cancelled = await getVideoRenderJob(selectedThread.id, renderJob.id)
      setRenderJob(cancelled)
      setRendering(false)
    } catch (error) {
      setRenderError(error instanceof Error ? error.message : String(error))
    }
  }, [renderJob, selectedThread])

  const handleExportRender = useCallback(async () => {
    if (!selectedThread || !videoProject || !renderJob || renderJob.status !== 'ready' || !exportPath.trim()) return
    setExporting(true)
    setRenderError(null)
    try {
      await exportRenderedVideo(selectedThread.id, videoProject.id, exportPath.trim(), renderJob.id)
    } catch (error) {
      setRenderError(error instanceof Error ? error.message : String(error))
    } finally {
      setExporting(false)
    }
  }, [exportPath, renderJob, selectedThread, videoProject])

  const handleStartRender = useCallback(async () => {
    if (!selectedThread) return
    setRendering(true)
    setRenderError(null)
    try {
      if (!videoProject?.current_revision_id) throw new Error('Save a timeline revision before rendering')
      const job = await startVideoRender(selectedThread.id, videoProject.id, videoProject.current_revision_id)
      setRenderJob(job)
      const pollInterval = window.setInterval(async () => {
        try {
          const updated = await getVideoRenderJob(selectedThread.id, job.id)
          setRenderJob(updated)
          setRenderProgress(updated.progress)
          if (updated.status === 'ready' || updated.status === 'failed' || updated.status === 'cancelled') {
            window.clearInterval(pollInterval)
            setRendering(false)
            if (updated.status === 'failed') {
              setRenderError(updated.failure_reason || updated.failure_code || 'Render failed')
            }
          }
        } catch {
          window.clearInterval(pollInterval)
          setRendering(false)
        }
      }, 1000)
    } catch (error) {
      setRenderError(error instanceof Error ? error.message : String(error))
      setRendering(false)
    }
  }, [selectedThread, videoProject])

  const handleOpenPicker = useCallback(() => {
    setCreateError(null)
    setBrowser(null)
    setBrowserError(null)
    setBrowserClips([])
    setBrowserScanError(null)
    setPickerOpen(true)
  }, [])

  const handleCreateBlankSession = useCallback(async () => {
    if (!selectedWorkspacePath || !selectedWorkspaceName) {
      setCreateError('Select a workspace before starting a video session.')
      return
    }
    if (workspaceOverviewQuery.isPending) {
      setCreateError('The selected workspace session route is still loading.')
      return
    }
    if (workspaceOverviewQuery.isError) {
      setCreateError(workspaceOverviewQuery.error instanceof Error ? workspaceOverviewQuery.error.message : 'Could not load the selected workspace session route.')
      return
    }
    if (!selectedSessionRoute) {
      setCreateError('The selected workspace has no available V3 Swarm session route.')
      return
    }
    const title = newSessionTitle.trim() || DEFAULT_VIDEO_SESSION_TITLE
    setCreatingBlankSession(true)
    setCreateError(null)
    try {
      const createdThread = await createVideoThread({
        title,
        workspacePath: selectedWorkspacePath,
        workspaceName: selectedWorkspaceName,
        route: selectedSessionRoute,
        clips: [],
      })
      queryClient.setQueryData<VideoThreadRecord[]>(['video-tool-threads', selectedWorkspacePath], (current = []) => {
        const withoutCreated = current.filter((thread) => thread.id !== createdThread.id)
        return [createdThread, ...withoutCreated]
      })
      handleSelectVideoSession(createdThread.id)
      setSelectedClipId(null)
      setNewSessionTitle('')
      await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setCreatingBlankSession(false)
    }
  }, [handleSelectVideoSession, newSessionTitle, queryClient, selectedSessionRoute, selectedWorkspaceName, selectedWorkspacePath, workspaceOverviewQuery.error, workspaceOverviewQuery.isError, workspaceOverviewQuery.isPending])

  const handleAddFolder = useCallback(async (folderPath: string) => {
    if (!selectedWorkspacePath || !selectedWorkspaceName) {
      setCreateError('Select a workspace before starting a video session.')
      return
    }
    if (workspaceOverviewQuery.isPending) {
      setCreateError('The selected workspace session route is still loading.')
      return
    }
    if (workspaceOverviewQuery.isError) {
      setCreateError(workspaceOverviewQuery.error instanceof Error ? workspaceOverviewQuery.error.message : 'Could not load the selected workspace session route.')
      return
    }
    if (!selectedSessionRoute) {
      setCreateError('The selected workspace has no available V3 Swarm session route.')
      return
    }
    setAddingFolderPath(folderPath)
    setCreateError(null)
    try {
      const scanned = await scanVideoFolder(selectedWorkspacePath, folderPath)
      if (scanned.clips.length === 0) {
        setCreateError('That folder has no accepted video files yet.')
        return
      }

      if (selectedThread) {
        const metadata = { ...(selectedThread.metadata ?? {}) }
        delete metadata[TIMELINE_METADATA_KEY]
        const folderSet = new Set(selectedThread.videoFolders)
        folderSet.add(scanned.folderPath)
        const existingClipIds = new Set(selectedThread.videoClips.map((clip) => clip.id))
        const existingSourceRefs = new Set(selectedThread.videoClips.map((clip) => clip.sourceRef))
        const clipsToAdd = scanned.clips.filter((clip) => !existingClipIds.has(clip.id) && !existingSourceRefs.has(clip.sourceRef))
        const orderedExistingClipIds = orderedClips(selectedThread).map((clip) => clip.id)
        const updatedThread = await updateVideoThread({
          ...selectedThread,
          videoFolders: Array.from(folderSet),
          videoClips: [...selectedThread.videoClips, ...clipsToAdd],
          videoClipOrder: [...orderedExistingClipIds, ...clipsToAdd.map((clip) => clip.id)],
          metadata,
        })
        queryClient.setQueryData<VideoThreadRecord[]>(['video-tool-threads', selectedWorkspacePath], (current = []) => current.map((thread) => thread.id === updatedThread.id ? updatedThread : thread))
        handleSelectVideoSession(updatedThread.id)
        setSelectedClipId(clipsToAdd[0]?.id ?? updatedThread.videoClipOrder[0] ?? updatedThread.videoClips[0]?.id ?? null)
        setPickerOpen(false)
        await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
        return
      }

      const createdThread = await createVideoThread({
        title: videoSessionTitle(scanned.folderPath),
        workspacePath: selectedWorkspacePath,
        workspaceName: selectedWorkspaceName,
        route: selectedSessionRoute,
        folderPath: scanned.folderPath,
        clips: scanned.clips,
      })
      queryClient.setQueryData<VideoThreadRecord[]>(['video-tool-threads', selectedWorkspacePath], (current = []) => {
        const withoutCreated = current.filter((thread) => thread.id !== createdThread.id)
        return [createdThread, ...withoutCreated]
      })
      handleSelectVideoSession(createdThread.id)
      setSelectedClipId(createdThread.videoClipOrder[0] ?? createdThread.videoClips[0]?.id ?? null)
      setPickerOpen(false)
      await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setAddingFolderPath(null)
    }
  }, [handleSelectVideoSession, queryClient, selectedSessionRoute, selectedThread, selectedWorkspaceName, selectedWorkspacePath, workspaceOverviewQuery.error, workspaceOverviewQuery.isError, workspaceOverviewQuery.isPending])

  const persistTimelineSegments = useCallback(async (segments: TimelineSegment[], options?: { migration?: boolean; transitionKind?: VideoTransitionKind }) => {
    if (!selectedThread) return
    setReordering(true)
    try {
      let project = videoProject
      let revision = currentRevision
      const nextSegments = options?.transitionKind ? (() => {
        let previousVisible: TimelineSegment | null = null
        return segments.map((segment) => {
          if (!segment.visible) return { ...segment, transitionIn: undefined }
          const next = previousVisible ? {
            ...segment,
            transitionIn: {
              id: `transition-${previousVisible.id}-${segment.id}`,
              kind: options.transitionKind as VideoTransitionKind,
              from_clip_id: previousVisible.id,
              to_clip_id: segment.id,
              duration_ms: options.transitionKind === 'cut' ? 0 : 300,
            },
          } : { ...segment, transitionIn: undefined }
          previousVisible = segment
          return next
        })
      })() : segments
      const timeline = timelineSegmentsToProjectTimeline(nextSegments, selectedClips, project?.output_preset)
      if (!project) {
        const created = await ensurePrimaryVideoProject(selectedThread.id, selectedThread.title || 'Video Tool project', timeline)
        project = created.project
        revision = created.current_revision ?? null
      } else {
        const updated = await createVideoProjectRevision(selectedThread.id, project.id, timeline, options?.migration ? 'Migrated legacy Video Tool timeline' : 'Edited in Video Tool')
        project = updated.project
        revision = updated.current_revision ?? null
      }
      setVideoProject(project)
      setCurrentRevision(revision)
      setProjectRevisions(await listVideoProjectRevisions(selectedThread.id, project.id))
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setReordering(false)
    }
  }, [currentRevision, selectedClips, selectedThread, videoProject])

  useEffect(() => {
    if (!selectedThread || projectLoading || currentRevision || legacyTimelineSegments.length === 0) return
    if (selectedClips.some((clip) => clipDuration(clipDurations, clip.id) <= 0)) return
    void persistTimelineSegments(legacyTimelineSegments, { migration: true })
  }, [clipDurations, currentRevision, legacyTimelineSegments, persistTimelineSegments, projectLoading, selectedClips, selectedThread])

  useEffect(() => {
    if (!selectedThread || projectLoading || !currentRevision || selectedClips.length === 0) return
    const timelineClipIds = new Set(timelineSegments.map((segment) => segment.clipId))
    const addedClips = selectedClips.filter((clip) => !timelineClipIds.has(clip.id))
    if (addedClips.length === 0 || addedClips.some((clip) => clipDuration(clipDurations, clip.id) <= 0)) return
    const appended = [...timelineSegments, ...addedClips.map((clip) => ({
      id: timelineSegmentId(clip.id), type: 'video' as const, clipId: clip.id,
      src: clipMediaUrl(selectedThread.id, clip.id), sourceKind: 'source_video', start: 0, sourceStart: 0,
      duration: clipDuration(clipDurations, clip.id), visible: true,
    }))]
    void persistTimelineSegments(appended)
  }, [clipDurations, currentRevision, persistTimelineSegments, projectLoading, selectedClips, selectedThread, timelineSegments])

  const handleToggleSegment = useCallback(async (clipId: string) => {
    const next = timelineSegments.map((segment) => segment.clipId === clipId ? { ...segment, visible: !segment.visible } : segment)
    await persistTimelineSegments(next)
  }, [persistTimelineSegments, timelineSegments])

  const handleRestoreRevision = useCallback(async (revisionId: string) => {
    if (!selectedThread || !videoProject || revisionId === currentRevision?.id) return
    setRestoringRevisionId(revisionId)
    setCreateError(null)
    try {
      const restored = await restoreVideoProjectRevision(selectedThread.id, videoProject.id, revisionId)
      setVideoProject(restored.project)
      setCurrentRevision(restored.current_revision ?? null)
      setProjectRevisions(await listVideoProjectRevisions(selectedThread.id, videoProject.id))
      setPlayhead(0)
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setRestoringRevisionId(null)
    }
  }, [currentRevision?.id, selectedThread, videoProject])

  const handleTogglePlayback = useCallback(() => {
    if (movieDuration <= 0) {
      return
    }
    if (isPlaying) {
      setIsPlaying(false)
      return
    }
    const startAt = playhead >= movieDuration ? 0 : playhead
    playheadRef.current = startAt
    setPlayhead(startAt)
    playbackStartPlayheadRef.current = startAt
    playbackStartRef.current = performance.now()
    setIsPlaying(true)
  }, [isPlaying, movieDuration, playhead])

  const handleSeek = useCallback((value: number) => {
    const next = Math.max(0, Math.min(movieDuration, value))
    playheadRef.current = next
    setPlayhead(next)
    playbackStartPlayheadRef.current = next
    playbackStartRef.current = performance.now()
  }, [movieDuration])

  const handleFocusStep = useCallback((clipId: string, playheadMs: number) => {
    const segment = timelineLayout.find((candidate) => candidate.id === clipId || candidate.clipId === clipId)
    setSelectedClipId(segment?.clipId ?? clipId)
    handleSeek(segment?.timelineStart ?? playheadMs / 1000)
  }, [handleSeek, timelineLayout])

  const handlePendingProposalChange = useCallback((proposal: VideoEditProposalWire | null) => {
    setPendingProposal(proposal)
    if (!proposal) return
    const clipId = videoProposalFocusClipId(proposal)
    const operation = [...proposal.operations].reverse().find((candidate) => String(candidate.clip?.id ?? candidate.clip_id ?? candidate.transition?.to_clip_id ?? '').trim() === clipId)
    const playheadMs = typeof operation?.clip?.timeline_start_ms === 'number' ? operation.clip.timeline_start_ms : proposal.affected_ranges?.[0]?.start_ms ?? 0
    if (clipId) handleFocusStep(clipId, playheadMs)
  }, [handleFocusStep])

  const handleRequestStepEdit = useCallback(async (action: 'visual' | 'transition' | 'source' | 'move_earlier' | 'move_later', segment: TimelineLayoutSegment) => {
    if (!selectedThread || !videoProject || !currentRevision || stepRequestBusyId) return
    setStepRequestBusyId(`${segment.id}:${action}`)
    setCreateError(null)
    try {
      const acceptedPart = acceptedPlan?.parts.find((part) => part.id === segment.id || part.id === segment.clipId)
      const prompt = videoStepEditRequest({ action, clipId: segment.id, playheadMs: segment.timelineStart * 1000, visual: acceptedPart?.visual })
      await continueDesktopV3Conversation(createDesktopV3ExistingMessageOperation({
        sessionId: selectedThread.id,
        prompt,
        artifactSelections: action === 'visual' && acceptedPart ? [videoPlanPartMessageSelection(acceptedPart)] : undefined,
        metadata: {
          creative_mode: 'video',
          video_project_id: videoProject.id,
          video_revision_id: currentRevision.id,
          video_anchor_clip_id: segment.id,
          video_playhead_ms: Math.round(segment.timelineStart * 1000),
        },
      }))
      handleFocusStep(segment.id, segment.timelineStart * 1000)
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setStepRequestBusyId('')
    }
  }, [acceptedPlan, currentRevision, handleFocusStep, selectedThread, stepRequestBusyId, videoProject])

  const handleTimelinePointer = useCallback((event: PointerEvent<HTMLDivElement>) => {
    if (movieDuration <= 0) {
      return
    }
    const bounds = event.currentTarget.getBoundingClientRect()
    const x = Math.max(0, Math.min(timelineTrackWidthPx, event.clientX - bounds.left))
    handleSeek((x / timelineTrackWidthPx) * movieDuration)
  }, [handleSeek, movieDuration, timelineTrackWidthPx])

  useEffect(() => {
    const scroller = timelineScrollRef.current
    if (!scroller || movieDuration <= 0) {
      return
    }
    const leftPadding = 96
    const rightPadding = 160
    const playheadPosition = playheadX
    if (playheadPosition < scroller.scrollLeft + leftPadding) {
      scroller.scrollLeft = Math.max(0, playheadPosition - leftPadding)
      return
    }
    if (playheadPosition > scroller.scrollLeft + scroller.clientWidth - rightPadding) {
      scroller.scrollLeft = Math.min(scroller.scrollWidth - scroller.clientWidth, playheadPosition - scroller.clientWidth + rightPadding)
    }
  }, [movieDuration, playheadX])

  return (
    <div className="absolute inset-0 overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]">
      <div className="flex min-h-dvh flex-col px-5 pt-[calc(var(--app-safe-area-top)+24px)] pb-[calc(var(--app-safe-area-bottom)+24px)] lg:hidden">
        <button type="button" onClick={handleBackToTools} className="mb-6 inline-flex h-10 items-center gap-2 self-start rounded-xl px-2 text-sm text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
          <ArrowLeft size={16} />
          Tools
        </button>
        <div className="grid flex-1 place-items-center text-center">
          <div className="max-w-sm rounded-[2rem] border border-[var(--app-border)] bg-[var(--app-surface)] px-6 py-8 shadow-[var(--shadow-panel)]">
            <span className="mx-auto grid h-14 w-14 place-items-center rounded-2xl border border-[color-mix(in_srgb,var(--app-primary)_38%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-primary)_12%,transparent)] text-[var(--app-primary)]">
              <Film size={26} strokeWidth={1.7} />
            </span>
            <p className="mt-6 text-[11px] font-medium uppercase tracking-[0.24em] text-[var(--app-text-subtle)]">Video Tool</p>
            <h1 className="mt-2 text-2xl font-semibold tracking-[-0.05em] text-[var(--app-text)]">Coming soon for mobile</h1>
            <p className="mt-3 text-sm leading-6 text-[var(--app-text-muted)]">Video editing is desktop only for now.</p>
          </div>
        </div>
      </div>
      <div className="mx-auto hidden h-full w-full max-w-none flex-col px-4 py-4 sm:px-5 sm:py-5 lg:flex">
        {createError ? (
          <div className="mb-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text)]">
            {createError}
          </div>
        ) : null}

        <main className="flex min-h-0 flex-1 overflow-hidden py-5">
            <SwarmToolSidebar
              backLabel={routeWorkspaceSlug ? 'Workspace' : 'Launcher'}
              onBack={handleBackToWorkspace}
              darkModeEnabled={blackModeEnabled}
              onToggleDarkMode={() => setBlackModeEnabled((enabled) => !enabled)}
              darkModeStyle={darkOverrideButtonStyle}
              darkModeActiveClassName="border-[var(--video-tool-user-theme-accent)] bg-[var(--video-tool-user-theme-surface)] text-[var(--video-tool-user-theme-text)] hover:bg-[var(--video-tool-user-theme-surface-hover)]"
              toolIcon={<Film size={16} strokeWidth={1.8} />}
              toolTitle="Video"
              toolDescription="Video sessions are DB-backed movie threads. Originals stay untouched; generated tool files use Swarm’s private app-managed workspace bucket."
              createLabel="Start new video session"
              createTitle={newSessionTitle}
              createPrefix={(
                <label className="block">
                  <span className="text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Workspace</span>
                  <select
                    value={selectedWorkspacePath}
                    onChange={(event) => handleSelectWorkspace(event.currentTarget.value)}
                    disabled={workspaceOverviewQuery.isPending || workspaces.length === 0}
                    className="mt-2 h-9 w-full border border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-[12px] text-[var(--app-text)] outline-none focus:border-[var(--app-primary)] disabled:opacity-50"
                    aria-label="Video Studio workspace"
                  >
                    {workspaces.map((workspace) => (
                      <option key={workspace.path} value={workspace.path}>{workspace.workspaceName}</option>
                    ))}
                  </select>
                </label>
              )}
              onCreateTitleChange={setNewSessionTitle}
              createPlaceholder={DEFAULT_VIDEO_SESSION_TITLE}
              onCreate={() => void handleCreateBlankSession()}
              creating={creatingBlankSession}
              createDisabled={!selectedWorkspacePath || workspaceOverviewQuery.isPending || !selectedSessionRoute}
              sessionsLabel="Video sessions"
              sessionsLoading={videoThreadsQuery.isLoading}
              sessions={videoThreads.map((thread) => ({
                id: thread.id,
                title: thread.title || 'Video Thread',
                subtitle: String(thread.videoClips.length) + ' clip' + (thread.videoClips.length === 1 ? '' : 's') + ' · ' + formatStartedAt(thread.createdAt),
              }))}
              selectedSessionId={selectedThread?.id ?? null}
              onSelectSession={handleSelectVideoSession}
              emptySessionsMessage="No video sessions yet. Start session to get started."
              defaultSessionTitle="Video Thread"
              actions={[
                { id: 'add-folder', label: 'Add folder', icon: <FolderOpen size={14} />, suffix: 'source', onClick: handleOpenPicker, disabled: !selectedThread },
                { id: 'show-files', label: revealingStorage ? 'Opening…' : 'Show files', icon: <FolderOpen size={14} />, suffix: 'local', onClick: () => void handleRevealVideoStorage(), disabled: !selectedThread || revealingStorage },
                { id: 'session-mode', label: 'Open session mode', icon: <MessageSquare size={14} />, suffix: 'chat', onClick: handleOpenSessionMode, disabled: !selectedThread || !routeWorkspaceSlug },
              ]}
            >
              {selectedThread ? (
                <div className="mt-4 min-h-0 flex-1 overflow-y-auto">
                  <p className="mb-2 px-2 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Videos in this session</p>
                  <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-2 px-2">
                    <select
                      value={selectedProjectId ?? ''}
                      onChange={(event) => setSelectedProjectId(event.currentTarget.value || null)}
                      disabled={projectLoading || videoProjects.length === 0}
                      className="h-8 min-w-0 border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-xs text-[var(--app-text)]"
                      aria-label="Selected video project"
                    >
                      {videoProjects.map((project) => <option key={project.id} value={project.id}>{project.title || 'Untitled video'} · r{project.current_revision_number ?? 0}</option>)}
                    </select>
                    <Button variant="outline" className="h-8 px-2 text-xs" onClick={() => void handleCreateAdditionalProject()} disabled={creatingProject || projectLoading}>
                      {creatingProject ? <Loader2 size={12} className="animate-spin" /> : '+'} Video
                    </Button>
                  </div>
                  <p className="mb-2 mt-4 px-2 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Current movie</p>
                  <div className="border border-[var(--app-border)] bg-[var(--app-bg)] p-3">
                    <h2 className="truncate text-sm font-semibold text-[var(--app-text)]">{videoProject?.title || selectedThread.title || 'Video project'}</h2>
                    <p className="mt-2 break-all text-[11px] leading-5 text-[var(--app-text-subtle)]">{selectedFolderPath || 'No source folder yet'}</p>
                    <div className="mt-4 grid grid-cols-2 gap-2 text-[11px]">
                      <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Length</div><div className="mt-1 tabular-nums text-[var(--app-text)]">{formatTimelineTime(movieDuration)}</div></div>
                      <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Revision</div><div className="mt-1 text-[var(--app-text)]">{projectLoading ? 'Loading…' : currentRevision ? `r${currentRevision.revision_number}` : 'Unsaved'}</div></div>
                    </div>
                    <Button variant="outline" className="mt-3 h-8 w-full rounded-xl px-3 text-xs" onClick={() => void handleRevealVideoStorage()} disabled={revealingStorage}>
                      <FolderOpen size={13} />{revealingStorage ? 'Opening…' : 'Show stored files'}
                    </Button>
                  </div>

                  {projectRevisions.length > 0 ? (
                    <>
                      <p className="mb-2 mt-4 px-2 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Revision history</p>
                      <div className="flex max-h-40 flex-col gap-1 overflow-y-auto">
                        {projectRevisions.map((revision) => {
                          const active = revision.id === currentRevision?.id
                          return (
                            <button key={revision.id} type="button" onClick={() => void handleRestoreRevision(revision.id)} disabled={active || Boolean(restoringRevisionId)} className={`flex items-center gap-2 px-2 py-1.5 text-left text-[11px] hover:bg-[var(--app-surface-hover)] disabled:cursor-default ${active ? 'bg-[var(--app-surface-active)] text-[var(--app-text)]' : 'text-[var(--app-text-muted)]'}`}>
                              {restoringRevisionId === revision.id ? <Loader2 size={12} className="animate-spin" /> : <RotateCcw size={12} />}
                              <span className="min-w-0 flex-1 truncate">r{revision.revision_number} · {revision.change_summary || 'Timeline revision'}</span>
                              {active ? <span className="text-[10px] text-[var(--app-primary)]">current</span> : null}
                            </button>
                          )
                        })}
                      </div>
                      {currentRevision?.restored_from_revision_id ? <p className="mt-2 px-2 text-[10px] text-[var(--app-primary)]">Rollback active · restored from an earlier revision</p> : null}
                    </>
                  ) : null}

                  <p className="mb-2 mt-4 px-2 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Sources</p>
                  <div className="flex flex-col gap-1">
                    {timelineSegments.length === 0 ? <div className="px-2 py-3 text-[11px] text-[var(--app-text-subtle)]">No clips yet.</div> : timelineSegments.map((segment, index) => {
                      const clip = selectedClips.find((candidate) => candidate.id === segment.clipId)
                      const layoutSegment = timelineLayoutByClipId.get(segment.clipId)
                      return (
                        <button key={segment.id + '-sidebar'} type="button" onClick={() => { setSelectedClipId(segment.clipId); if (layoutSegment?.visible) handleSeek(layoutSegment.timelineStart) }} className={['grid grid-cols-[24px_minmax(0,1fr)_18px] items-center gap-2 px-2 py-1.5 text-left hover:bg-[var(--app-surface-hover)]', selectedClip?.id === segment.clipId ? 'bg-[var(--app-surface-active)] text-[var(--app-text)]' : '', segment.visible ? '' : 'opacity-55'].filter(Boolean).join(' ')}>
                          <span className="text-[10px] text-[var(--app-text-subtle)]">{String(index + 1).padStart(2, '0')}</span>
                          <span className="min-w-0 truncate">{clip?.name ?? segment.clipId}</span>
                          {segment.visible ? <Eye size={13} className="text-[var(--app-primary)]" /> : <EyeOff size={13} className="text-[var(--app-text-subtle)]" />}
                        </button>
                      )
                    })}
                  </div>
                </div>
              ) : null}
            </SwarmToolSidebar>

            <section className="flex min-w-0 flex-1 flex-col overflow-y-auto">
              <div className="mb-4 flex items-center justify-between gap-3 lg:hidden">
                <Button variant="ghost" className="h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={handleBackToWorkspace}><ArrowLeft size={15} />{routeWorkspaceSlug ? 'Workspace' : 'Launcher'}</Button>
                <div className="flex items-center gap-2">
                  <Button variant="outline" style={darkOverrideButtonStyle} className={`h-8 w-8 rounded-xl px-0 ${blackModeEnabled ? 'border-[var(--video-tool-user-theme-accent)] bg-[var(--video-tool-user-theme-surface)] text-[var(--video-tool-user-theme-text)] hover:bg-[var(--video-tool-user-theme-surface-hover)]' : ''}`} onClick={() => setBlackModeEnabled((enabled) => !enabled)} aria-label="Toggle dark mode override for this page" aria-pressed={blackModeEnabled} title="Toggle dark mode override for this page"><Moon size={14} aria-hidden="true" /></Button>
                  <Button variant="ghost" className="h-8 rounded-xl px-2 text-xs text-[var(--app-text-muted)]" onClick={handleOpenPicker} disabled={!selectedThread}><FolderOpen size={14} />Add folder</Button>
                  <span className="text-xs text-[var(--app-text-subtle)]">Video Studio</span>
                </div>
              </div>

              {!selectedThread ? (
                <div className="grid min-h-full place-items-center border border-dashed border-[var(--app-border)] bg-[var(--app-surface)] px-6 py-16 text-center">
                  <div className="max-w-sm">
                    <Film className="mx-auto text-[var(--app-primary)]" size={42} strokeWidth={1.5} />
                    <h2 className="mt-5 text-2xl font-semibold tracking-[-0.05em] text-[var(--app-text)]">Start session to get started</h2>
                    <p className="mt-3 text-sm leading-6 text-[var(--app-text-muted)]">
                      Name a video session in the sidebar, then add folders and clips inside that session.
                    </p>
                  </div>
                </div>
              ) : (
                <>
              <div className="relative aspect-video min-h-[360px] overflow-hidden border border-[var(--app-border)] bg-black lg:min-h-[480px]">
                <canvas ref={canvasRef} width={1920} height={1080} className="h-full w-full bg-black object-contain" />
                {pendingProposal ? <div className="pointer-events-none absolute right-4 top-4 border border-amber-300/50 bg-amber-950/80 px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.16em] text-amber-200">Shadow cut · pending · accepted r{pendingProposal.base_revision_number} unchanged</div> : null}
                {timelineSegments.length === 0 ? (
                  <div className="absolute inset-0 grid place-items-center text-center"><div><Film className="mx-auto text-white/45" size={42} strokeWidth={1.5} /><p className="mt-3 text-sm font-medium text-white/80">No clips in this timeline</p></div></div>
                ) : null}
                <div className="pointer-events-none absolute left-4 top-4 rounded bg-black/55 px-2 py-1 text-xs text-white/70">
                  {activeSegment ? `${selectedClip?.name ?? activeSegment.clipId} · ${formatTimelineTime(playhead)} / ${formatTimelineTime(movieDuration)}` : 'Timeline player'}
                </div>
              </div>

              <section className="mt-5">
                <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
                  <div className="flex items-center gap-3">
                    <Button className="h-9 rounded-xl px-3" onClick={handleTogglePlayback} disabled={movieDuration <= 0}>{isPlaying ? <Pause size={15} /> : <Play size={15} />}{isPlaying ? 'Pause' : 'Play'}</Button>
                    <div className="text-xs tabular-nums text-[var(--app-text-muted)]">{formatTimelineTime(playhead)} / {formatTimelineTime(movieDuration)}</div>
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-[var(--app-text-muted)]">{visibleTimelineLayout.length} included · {hiddenTimelineLayout.length} hidden</span>
                    {rendering ? (
                      <Button variant="outline" className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleCancelRender()}>
                        <Loader2 size={13} className="animate-spin" /> Rendering {Math.round(renderProgress * 100)}% · Cancel
                      </Button>
                    ) : renderJob?.status === 'ready' ? (
                      <span className="text-xs text-green-500"><Film size={13} className="inline" /> Render ready for playback and export</span>
                    ) : (
                      <Button variant="outline" className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleStartRender()} disabled={movieDuration <= 0 || rendering || projectLoading || !currentRevision || Boolean(pendingProposal) || hasUnresolvedPlanFrames}>
                        <Sparkles size={13} /> {pendingProposal ? 'Accept or reject shadow cut first' : hasUnresolvedPlanFrames ? 'Replace planned frames with sources' : 'Render Video'}
                      </Button>
                    )}
                  </div>
                </div>
                {renderJob?.status === 'ready' ? (
                  <div className="mb-3 grid gap-2 border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                    {renderedVideoArtifactUrl(selectedThread.id, renderJob) ? <video controls className="max-h-72 w-full bg-black" src={renderedVideoArtifactUrl(selectedThread.id, renderJob)} /> : <p className="text-xs text-[var(--app-text-muted)]">Rendered artifact is ready; use Export MP4 to copy it to a workspace path.</p>}
                    <div className="flex gap-2"><input className="h-8 min-w-0 flex-1 border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-xs" value={exportPath} onChange={(event) => setExportPath(event.target.value)} placeholder="Absolute destination path for MP4" /><Button variant="outline" className="h-8 px-3 text-xs" disabled={exporting || !exportPath.trim()} onClick={() => void handleExportRender()}>{exporting ? <Loader2 size={13} className="animate-spin" /> : <Download size={13} />}Export MP4</Button></div>
                  </div>
                ) : null}
                {renderError ? (
                  <div className="mb-3 rounded-xl border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
                    Render error: {renderError}
                  </div>
                ) : null}

                <div className="mb-3 flex items-center gap-2 border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
                  <span className="text-xs text-[var(--app-text-muted)]">Transition between clips</span>
                  <select className="h-8 border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-xs" value={transitionKind} onChange={(event) => setTransitionKind(event.target.value as VideoTransitionKind)}>
                    {VIDEO_TRANSITION_KINDS.map((kind) => <option key={kind} value={kind}>{transitionLabel(kind)}</option>)}
                  </select>
                  <Button variant="outline" className="h-8 px-3 text-xs" disabled={timelineSegments.length < 2 || reordering || Boolean(pendingProposal)} onClick={() => void persistTimelineSegments(timelineSegments.map((segment) => ({ ...segment })), { transitionKind })}>Apply separately</Button>
                </div>
                <div className="border-y border-[var(--app-border)] py-4">
                  <div className="mb-3 flex justify-between text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]"><span>00:00</span><span>{formatTimelineTime(movieDuration)}</span></div>
                  <div ref={timelineScrollRef} className="overflow-x-auto pb-2">
                    <div className="relative min-h-48 min-w-full" style={{ width: `${timelineTrackWidthPx}px` }}>
                      <div role="slider" tabIndex={0} aria-label="Scaled movie timeline" aria-valuemin={0} aria-valuemax={movieDuration} aria-valuenow={Math.min(playhead, Math.max(0, movieDuration))} onPointerDown={handleTimelinePointer} onPointerMove={(event) => { if (event.buttons === 1) handleTimelinePointer(event) }} onKeyDown={(event) => { if (event.key === 'ArrowLeft') { event.preventDefault(); handleSeek(playhead - 1) } if (event.key === 'ArrowRight') { event.preventDefault(); handleSeek(playhead + 1) } }} className="absolute inset-x-0 top-0 h-28 cursor-pointer overflow-hidden border border-[var(--app-border)] bg-[var(--app-bg)]">
                        {visibleTimelineLayout.length === 0 ? <div className="grid h-full place-items-center text-sm text-[var(--app-text-muted)]">No included clips with loaded duration yet.</div> : visibleTimelineLayout.map((segment, visibleIndex) => {
                          const clip = selectedClips.find((candidate) => candidate.id === segment.clipId)
                          const left = movieDuration > 0 ? (segment.timelineStart / movieDuration) * timelineTrackWidthPx : 0
                          const width = movieDuration > 0 ? (segment.duration / movieDuration) * timelineTrackWidthPx : 0
                          return (
                            <button key={segment.id} type="button" onPointerDown={(event) => event.stopPropagation()} onClick={() => { setSelectedClipId(segment.clipId); handleSeek(segment.timelineStart) }} className={`absolute top-0 h-full overflow-hidden border-r border-black/30 text-left transition hover:bg-[color-mix(in_srgb,var(--app-primary)_10%,var(--app-surface))] ${pendingProposal ? 'bg-amber-950/30' : 'bg-[var(--app-surface)]'} ${selectedClip?.id === segment.clipId ? 'outline outline-1 outline-[var(--app-primary)]' : ''}`} style={{ left: `${left}px`, width: `${Math.max(1, width)}px` }}>
                              <div className="flex h-full flex-col justify-between px-2 py-2"><div className="flex items-center justify-between gap-2 text-[10px] text-[var(--app-text-subtle)]"><span>{String(visibleIndex + 1).padStart(2, '0')}</span><span className="tabular-nums">{formatTimelineTime(segment.duration)}</span></div><div className="min-w-0"><p className="truncate text-xs font-medium text-[var(--app-text)]">{clip?.name ?? segment.clipId}</p><p className="truncate text-[10px] text-[var(--app-text-muted)]">{formatTimelineTime(segment.timelineStart)} – {formatTimelineTime(segment.timelineEnd)}</p></div></div>
                            </button>
                          )
                        })}
                        <div className="pointer-events-none absolute top-0 h-full w-0.5 bg-[var(--app-primary)] shadow-[0_0_0_1px_rgba(0,0,0,0.35)]" style={{ left: `${playheadX}px` }} />
                      </div>
                      <div className="absolute inset-x-0 top-36 flex items-center gap-2">
                        {timelineLayout.map((segment, index) => {
                          const clip = selectedClips.find((candidate) => candidate.id === segment.clipId)
                          return (
                            <div key={`${segment.id}-controls`} className={`flex min-w-[320px] max-w-[440px] flex-wrap items-center gap-2 border px-2 py-2 text-xs ${segment.visible ? 'border-[var(--app-border)] bg-[var(--app-surface)]' : 'border-dashed border-[var(--app-border)] bg-transparent opacity-60'}`}>
                              <button type="button" onClick={() => handleFocusStep(segment.id, segment.timelineStart * 1000)} className="min-w-[160px] flex-1 text-left"><span className="block truncate font-medium text-[var(--app-text)]">{segment.title || clip?.name || segment.clipId}</span><span className="block truncate font-mono text-[10px] text-[var(--app-text-muted)]">{pendingProposal ? 'Pending shadow' : segment.visible ? 'Included' : 'Hidden'} · {segment.id} · {formatTimelineTime(segment.duration)}</span></button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => void handleRequestStepEdit('visual', segment)} disabled={Boolean(stepRequestBusyId)}>Visual</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => void handleRequestStepEdit('transition', segment)} disabled={Boolean(stepRequestBusyId)}>Transition</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => void handleRequestStepEdit('source', segment)} disabled={Boolean(stepRequestBusyId)}>Source</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => void handleRequestStepEdit('move_earlier', segment)} disabled={index === 0}>AI ←</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => void handleRequestStepEdit('move_later', segment)} disabled={index === timelineSegments.length - 1}>AI →</Button>
                              <Button variant={segment.visible ? 'outline' : 'ghost'} className="h-7 rounded-lg px-2 text-xs" onClick={() => void handleToggleSegment(segment.clipId)} disabled={reordering || Boolean(pendingProposal)}>{segment.visible ? <Eye size={13} /> : <EyeOff size={13} />}</Button>
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  </div>
                </div>
              </section>

              {acceptedPlan ? (
                <section className="mt-6 border border-[var(--app-border)] bg-[var(--app-surface)] p-4" aria-label="Accepted video plan">
                  <div className="mb-4 flex items-start justify-between gap-4"><div><div className="flex items-center gap-2"><Sparkles size={16} className="text-[var(--app-primary)]" /><h2 className="text-sm font-semibold text-[var(--app-text)]">Accepted video plan</h2></div><p className="mt-1 text-xs text-[var(--app-text-muted)]">This structure is the source of truth for the next AI proposals. Timing and source media can be filled in later.</p></div><span className="shrink-0 border border-[var(--app-primary)] px-2 py-1 text-[10px] uppercase tracking-[0.14em] text-[var(--app-primary)]">Accepted</span></div>
                  {acceptedPlan.summary ? <p className="mb-3 text-xs leading-5 text-[var(--app-text-muted)]">{acceptedPlan.summary}</p> : null}
                  <ol className="grid gap-3 xl:grid-cols-2">{acceptedPlan.parts.map((part, index) => <li key={part.id} className="border border-[var(--app-border)] bg-[var(--app-bg)] p-4"><div className="flex items-center justify-between"><span className="text-[10px] uppercase tracking-[0.14em] text-[var(--app-primary)]">Part {index + 1}</span><span className="font-mono text-[10px] text-[var(--app-text-subtle)]">{part.id}</span></div><h3 className="mt-2 text-sm font-semibold text-[var(--app-text)]">{part.title}</h3><p className="mt-2 text-xs leading-5 text-[var(--app-text-muted)]">{part.narration || part.on_screen_text || part.visual_direction || 'Ready for source and timing work.'}</p></li>)}</ol>
                </section>
              ) : null}

              {currentRevision?.timeline.clips.some((clip) => clip.source_kind === 'text') ? (
                <section className="mt-6 border border-[var(--app-border)] bg-[var(--app-surface)] p-4" aria-label="Still production plan">
                  <div className="mb-4 flex items-start justify-between gap-4">
                    <div>
                      <div className="flex items-center gap-2"><Sparkles size={16} className="text-[var(--app-primary)]" /><h2 className="text-sm font-semibold text-[var(--app-text)]">Still production plan</h2></div>
                      <p className="mt-1 text-xs text-[var(--app-text-muted)]">Review timing, narration, on-screen text, and frame direction before generating any stills.</p>
                    </div>
                    <span className="shrink-0 border border-[var(--app-border)] bg-[var(--app-bg)] px-2 py-1 text-[10px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Pre-production</span>
                  </div>
                  <div className="grid gap-3 xl:grid-cols-2">
                    {currentRevision.timeline.clips.filter((clip) => clip.source_kind === 'text').map((clip) => {
                      const details = videoPlanClipDetails(clip)
                      return (
                        <article key={`${clip.id}-plan`} className="border border-[var(--app-border)] bg-[var(--app-bg)] p-4">
                          <div className="flex items-baseline justify-between gap-3"><h3 className="text-sm font-semibold text-[var(--app-text)]">{details.title}</h3><span className="shrink-0 text-xs tabular-nums text-[var(--app-primary)]">{details.timing}</span></div>
                          <dl className="mt-3 grid gap-3 text-xs leading-5">
                            <div><dt className="font-medium uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">Narration</dt><dd className="mt-1 text-[var(--app-text-muted)]">{details.narration || 'No narration specified.'}</dd></div>
                            <div><dt className="font-medium uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">On-screen text</dt><dd className="mt-1 text-[var(--app-text-muted)]">{details.onScreenText || 'No on-screen text specified.'}</dd></div>
                            <div><dt className="font-medium uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">Proposed still / frame</dt><dd className="mt-1 text-[var(--app-text-muted)]">{details.still || 'No frame direction specified.'}</dd></div>
                          </dl>
                        </article>
                      )
                    })}
                  </div>
                </section>
              ) : null}

              {videoProject && currentRevision ? <VideoProposalReview key={`${currentRevision.id}:${aiRefreshKey}`} sessionId={selectedThread.id} projectId={videoProject.id} currentRevisionId={currentRevision.id} acceptedPlan={acceptedPlan} acceptedClips={currentRevision.timeline.clips} onAccepted={refreshSelectedVideoProject} onPendingChange={handlePendingProposalChange} onFocusStep={handleFocusStep} onFeedback={(message) => { setComposerDraftRequest({ id: Date.now(), draft: message }) }} /> : null}

              <section className="mt-6">
                <div className="mb-3 flex items-center justify-between gap-3"><div className="flex items-center gap-2"><ListVideo size={16} className="text-[var(--app-primary)]" /><h2 className="text-sm font-semibold text-[var(--app-text)]">Playlist sources</h2></div>{reordering ? <span className="text-xs text-[var(--app-text-subtle)]">Saving…</span> : null}</div>
                <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
                  {timelineSegments.length === 0 ? <div className="border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-4 text-sm text-[var(--app-text-muted)]">No accepted clips are stored in this video thread yet.</div> : timelineSegments.map((segment, index) => {
                    const clip = selectedClips.find((candidate) => candidate.id === segment.clipId)
                    const layoutSegment = timelineLayoutByClipId.get(segment.clipId)
                    return (
                      <button key={`${segment.id}-source`} type="button" onClick={() => { setSelectedClipId(segment.clipId); if (layoutSegment?.visible) handleSeek(layoutSegment.timelineStart) }} className={`flex min-w-0 items-center gap-3 border px-3 py-3 text-left transition hover:border-[var(--app-border-strong)] ${selectedClip?.id === segment.clipId ? 'border-[var(--app-primary)] bg-[color-mix(in_srgb,var(--app-primary)_8%,transparent)]' : 'border-[var(--app-border)] bg-[var(--app-surface)]'} ${segment.visible ? '' : 'opacity-60'}`}>
                        <span className="w-8 shrink-0 text-xs font-semibold text-[var(--app-primary)]">{String(index + 1).padStart(2, '0')}</span>
                        <div className="min-w-0 flex-1"><p className="truncate text-sm text-[var(--app-text)]">{clip?.name ?? segment.clipId}</p><p className="mt-1 text-xs text-[var(--app-text-muted)]">{clip ? formatBytes(clip.sizeBytes) : 'source'} · {segment.visible ? 'Included' : 'Hidden'} · {formatTimelineTime(segment.duration)}</p></div>
                        {segment.visible ? <Eye size={14} className="text-[var(--app-primary)]" /> : <EyeOff size={14} className="text-[var(--app-text-subtle)]" />}
                      </button>
                    )
                  })}
                </div>
              </section>
              </>
              )}
            </section>
            {selectedThread ? <VideoSessionAISidecar key={selectedThread.id} sessionId={selectedThread.id} projectId={videoProject?.id} revisionId={currentRevision?.id} anchorClipId={activeSegment?.id} playheadMs={playhead * 1000} routeOptions={selectedSessionRoute ? [selectedSessionRoute] : []} draftRequest={composerDraftRequest} onActivity={() => { setAIRefreshKey((value) => value + 1); void refreshSelectedVideoProject().catch(() => undefined) }} /> : null}
          </main>
      </div>

      {pickerOpen ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Choose a video folder" className="z-[80] p-4 sm:p-6">
          <DialogBackdrop onClick={() => setPickerOpen(false)} />
          <DialogPanel className="mx-auto mt-[6vh] flex h-[min(84vh,860px)] w-[min(980px,calc(100vw-24px))] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1040px,calc(100vw-48px))]">
            <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4 sm:px-6">
              <div>
                <p className="text-[11px] font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">Video session source</p>
                <h2 className="mt-1 text-xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">Choose a folder</h2>
                <p className="mt-2 text-sm text-[var(--app-text-muted)]">Pick a folder to add source clips to the selected video session.</p>
              </div>
              <ModalCloseButton onClick={() => setPickerOpen(false)} aria-label="Close video folder picker" />
            </div>

            <div className="flex-1 overflow-y-auto px-5 py-5 sm:px-6">
              <div className="mb-4 flex items-center justify-between gap-3">
                <div className="text-sm text-[var(--app-text-muted)]">
                  Workspace: <span className="text-[var(--app-text)]">{selectedWorkspaceName || 'No workspace selected'}</span>
                </div>
                <div className="flex items-center gap-2">
                  <Button variant="outline" className="rounded-xl" onClick={() => void loadBrowser(browser?.parentPath ?? '')} disabled={!browser?.parentPath || browserLoading}>Up</Button>
                  <Button variant="outline" className="rounded-xl" onClick={() => void loadBrowser(browser?.resolvedPath ?? selectedWorkspacePath)} disabled={browserLoading}>Refresh</Button>
                </div>
              </div>

              {browserError ? (
                <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-4 text-sm text-[var(--app-text)]">{browserError}</div>
              ) : null}

              {browserLoading && !browser ? (
                <div className="flex items-center gap-2 text-sm text-[var(--app-text-muted)]">
                  <Loader2 size={14} className="animate-spin" />
                  Loading folders…
                </div>
              ) : null}

              {browser ? (
                <div className="grid gap-3">
                  <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-3 text-xs text-[var(--app-text-subtle)]">
                    Selected folder: <span className="break-all text-[var(--app-text)]">{browser.resolvedPath}</span>
                  </div>

                  <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-4">
                    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                      <div>
                        <div className="text-sm font-medium text-[var(--app-text)]">Videos in selected folder</div>
                        <div className="mt-1 text-xs text-[var(--app-text-subtle)]">
                          {browserScanLoading ? 'Scanning the selected folder…' : `${browserClips.length} accepted video${browserClips.length === 1 ? '' : 's'} found in this exact folder`}
                        </div>
                      </div>
                      {browserClips.length > 0 ? (
                        <Button className="rounded-xl" onClick={() => void handleAddFolder(browser.resolvedPath)} disabled={addingFolderPath === browser.resolvedPath || !selectedWorkspacePath}>
                          {addingFolderPath === browser.resolvedPath ? (selectedThread ? 'Adding…' : 'Creating…') : (selectedThread ? 'Add selected folder to session' : 'Create video session from selected folder')}
                        </Button>
                      ) : null}
                    </div>
                    {browserScanError ? <div className="mt-3 text-sm text-[var(--app-text)]">{browserScanError}</div> : null}
                    {browserClips.length > 0 ? (
                      <div className="mt-4 grid gap-2">
                        {browserClips.map((clip) => (
                          <div key={clip.id} className="rounded-xl border border-[var(--app-border)] bg-transparent px-3 py-2">
                            <div className="truncate text-sm font-medium text-[var(--app-text)]">{clip.name}</div>
                            <div className="truncate text-xs text-[var(--app-text-subtle)]">{clip.sourceRef}</div>
                          </div>
                        ))}
                      </div>
                    ) : !browserScanLoading && !browserScanError ? <div className="mt-3 text-sm text-[var(--app-text-muted)]">No accepted video files in this folder.</div> : null}
                  </div>

                  {browser.entries.length === 0 ? (
                    <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-5 text-sm text-[var(--app-text-muted)]">No folders here.</div>
                  ) : (
                    browser.entries.map((entry) => (
                      <div key={entry.path} className="flex flex-col gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-4 sm:flex-row sm:items-center sm:justify-between">
                        <button type="button" onClick={() => void loadBrowser(entry.path)} className="min-w-0 text-left">
                          <div className="text-sm font-medium text-[var(--app-text)]">{entry.name}</div>
                          <div className="truncate text-xs text-[var(--app-text-subtle)]">{entry.path}</div>
                        </button>
                        <div className="flex shrink-0 items-center gap-2">
                          <Button variant="outline" className="rounded-xl" onClick={() => void loadBrowser(entry.path)}>Open folder</Button>
                        </div>
                      </div>
                    ))
                  )}
                </div>
              ) : null}
            </div>
          </DialogPanel>
        </Dialog>
      ) : null}
    </div>
  )
}
