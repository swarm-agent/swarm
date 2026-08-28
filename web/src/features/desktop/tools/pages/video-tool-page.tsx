import { type CSSProperties, type PointerEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { Download, Eye, EyeOff, Film, FolderOpen, Library, ListVideo, Loader2, MessageSquare, Moon, Music, Pause, Play, RotateCcw, Search, Sparkles, Trash2, Volume2, VolumeX } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { ensureDesktopSession, requestJson } from '../../../../app/api'
import { createSession, fetchDraftModelPreference } from '../../chat/queries/chat-queries'
import { uiSettingsQueryOptions, workspaceOverviewQueryOptions } from '../../../queries/query-options'
import { normalizeGlobalThemeSettings } from '../../settings/swarm/types/swarm-settings'
import { browseWorkspacePath } from '../../../workspaces/launcher/queries/browse-workspace-path'
import { buildWorkspaceRouteSlugMap, resolveWorkspaceBySlug, workspaceRouteSlugBase } from '../../../workspaces/launcher/services/workspace-route'
import { applyWorkspaceTheme, createWorkspaceThemeStyle } from '../../../workspaces/launcher/services/workspace-theme'
import { buildDesktopChatRouteOptions, getDesktopSessionCreateTarget, type DesktopChatRoute } from '../../chat/services/chat-routing'
import type { WorkspaceBrowseResult, WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { WorkspaceOverviewSwarmTarget } from '../../../workspaces/launcher/types/workspace-overview'
import { SwarmToolSidebar } from '../components/swarm-tool-sidebar'
import { VIDEO_TRANSITION_KINDS, VideoIterationSidebar, VideoSessionAISidecar, acceptVideoEditProposal, createVideoEditProposal, rejectVideoEditProposal, renderedVideoArtifactUrl, requestVideoRenderCancellation, selectVideoAnimationCandidate, transitionLabel, updateVideoCompositionProposal, videoAnimationReadyForConfirmation, videoPlanPartMessageSelection, videoPlanPartStoryboardContext, videoPlanTransitionMessageSelection, videoProposalProjectionSequence, type VideoAnimationCandidateWire, type VideoEditProposalWire, type VideoIterationComposerContext, type VideoPlanProposalWire, type VideoStepEditAction, type VideoTransitionKind, type VideoTransitionWire } from '../video-studio/video-studio-surface'
import { fetchDesktopV3ArtifactPreviewAccess } from '../../session-v3/artifact-api'
import { desktopV3ArtifactIterationMessage } from '../../session-v3/artifact-iteration-protocol'
import { saveVideoSessionViewPreference } from '../video-studio/video-session-view-preference'
import { VideoCompositionEditor, VideoCompositionOverlay, resolveVideoComposition, type VideoCompositionCatalogWire, type VideoCompositionLinkWire } from '../video-studio/video-composition'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'

export type VideoClip = {
  id: string
  name: string
  sourceRef: string
  extension: string
  sizeBytes: number
  modifiedAt: number
}

export type AudioSourceWire = {
  ref: string
  name: string
  mime_type: string
  size_bytes: number
  source_fingerprint: string
  fingerprint_version: string
}

export type AudioClip = AudioSourceWire & {
  extension: string
  modified_at: number
}

export type VideoTimelineClipWire = {
  id: string
  name?: string
  track?: number
  sequence?: number
  source_kind: string
  source_ref?: string
  audio_source?: AudioSourceWire
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
  composition_part_id?: string
  composition?: VideoCompositionLinkWire
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
  composition_catalog?: VideoCompositionCatalogWire
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
  confirmed_revision_id?: string
  confirmed_revision_number?: number
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
  origin_proposal_id?: string
  description?: string
  change_summary?: string
  timeline: VideoProjectTimelineWire
  author_principal?: string
  created_at: number
}

export type VideoProjectDetailWire = {
  project: VideoProjectSnapshotWire
  current_revision?: VideoProjectRevisionSnapshotWire
  confirmed_revision?: VideoProjectRevisionSnapshotWire
}

export type WorkspaceVideoRelatedSessionWire = {
  session_id: string
  title: string
  archived?: boolean
}

export type WorkspaceVideoCatalogItemWire = {
  project: VideoProjectSnapshotWire
  revisions: VideoProjectRevisionSnapshotWire[]
  source_archived?: boolean
  source_session_id: string
  source_session_title?: string
  related_sessions: WorkspaceVideoRelatedSessionWire[]
}

export function filterWorkspaceVideoCatalog(items: WorkspaceVideoCatalogItemWire[], query: string): WorkspaceVideoCatalogItemWire[] {
  const normalized = query.trim().toLocaleLowerCase()
  if (!normalized) return items
  return items.filter((item) => [
    item.project.title,
    item.project.description,
    item.source_session_title,
    item.source_session_id,
    ...item.related_sessions.flatMap((session) => [session.title, session.session_id]),
  ].some((value) => String(value ?? '').toLocaleLowerCase().includes(normalized)))
}

export function workspaceVideosForSession(items: WorkspaceVideoCatalogItemWire[], sessionId: string): WorkspaceVideoCatalogItemWire[] {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) return []
  return items.filter((item) => item.source_session_id === normalizedSessionId
    || item.related_sessions.some((session) => session.session_id === normalizedSessionId))
}

export function selectWorkspaceVideoRevision(item: WorkspaceVideoCatalogItemWire, revisionId?: string | null): VideoProjectRevisionSnapshotWire | null {
  const exact = revisionId ? item.revisions.find((revision) => revision.id === revisionId) : undefined
  return exact ?? item.revisions.find((revision) => revision.id === item.project.current_revision_id)
    ?? [...item.revisions].sort((left, right) => right.revision_number - left.revision_number)[0]
    ?? null
}

export function workspaceVideoContextMetadata(item: WorkspaceVideoCatalogItemWire, revisionId: string): Record<string, unknown> {
  return {
    experience: 'video_studio',
    launch_source: 'video_library',
    lineage_kind: 'video_project',
    creative_mode: 'video',
    source_session_id: item.source_session_id,
    source_video_project_id: item.project.id,
    source_video_revision_id: revisionId,
    video_context: {
      source_session_id: item.source_session_id,
      source_project_id: item.project.id,
      source_revision_id: revisionId,
      title: item.project.title,
    },
  }
}

export type CachedVideoMedia = { src: string; element: HTMLVideoElement }
export type CachedImageMedia = { src: string; element: HTMLImageElement }

export function replaceCachedVideoMedia(
  cache: Map<string, CachedVideoMedia>,
  clipId: string,
  src: string,
  create: () => HTMLVideoElement,
): { entry: CachedVideoMedia; replaced: boolean } {
  const current = cache.get(clipId)
  if (current?.src === src) return { entry: current, replaced: false }
  if (current) {
    current.element.pause()
    current.element.removeAttribute('src')
    current.element.load()
  }
  const entry = { src, element: create() }
  cache.set(clipId, entry)
  return { entry, replaced: Boolean(current) }
}

export function replaceCachedImageMedia(
  cache: Map<string, CachedImageMedia>,
  clipId: string,
  src: string,
  create: () => HTMLImageElement,
): { entry: CachedImageMedia; replaced: boolean } {
  const current = cache.get(clipId)
  if (current?.src === src) return { entry: current, replaced: false }
  const entry = { src, element: create() }
  cache.set(clipId, entry)
  return { entry, replaced: Boolean(current) }
}

export function acceptedVideoPlan(timeline: VideoProjectTimelineWire): VideoPlanProposalWire | null {
  const candidate = timeline.metadata?.accepted_video_plan
  if (!candidate || typeof candidate !== 'object') return null
  const plan = candidate as Partial<VideoPlanProposalWire>
  if (!Array.isArray(plan.parts) || plan.parts.length === 0) return null
  const parts = plan.parts.filter((part) => part && typeof part.id === 'string' && typeof part.title === 'string' && typeof part.duration_ms === 'number' && Boolean(part.visual?.session_id && part.visual.collection_id && part.visual.variant_id && part.visual.event_seq))
  const kind = plan.kind === 'revision' ? 'revision' : 'initial'
  return parts.length > 0 ? {
    kind,
    summary: typeof plan.summary === 'string' ? plan.summary : undefined,
    ...(plan.composition_catalog ? { composition_catalog: plan.composition_catalog } : {}),
    parts,
  } : null
}

export function videoPlanForPlayback(
  proposal: VideoEditProposalWire | null,
  timeline: VideoProjectTimelineWire | null | undefined,
  interactive = true,
): VideoPlanProposalWire | null {
  const plan = proposal?.plan ?? (timeline ? acceptedVideoPlan(timeline) : null)
  if (!plan || interactive) return plan
  return {
    ...plan,
    parts: plan.parts.map((part) => {
      const candidates = part.animation_candidates
      const selected = candidates?.candidates.find((candidate) => candidate.id === candidates.selected_candidate_id)
      if (!candidates || !selected) {
        const { animation_candidates: _animationCandidates, ...staticPart } = part
        return staticPart
      }
      return {
        ...part,
        animation_candidates: { ...candidates, candidates: [selected] },
      }
    }),
  }
}

export function proposalOwnsAnimationPart(proposal: VideoEditProposalWire | null, partId: string): boolean {
  return proposal?.plan?.parts.some((part) => part.id === partId && Boolean(part.animation_candidates)) === true
}

export function replaceVideoEditProposal(
  proposals: VideoEditProposalWire[],
  updated: VideoEditProposalWire,
): VideoEditProposalWire[] {
  return proposals.some((proposal) => proposal.id === updated.id)
    ? proposals.map((proposal) => proposal.id === updated.id ? updated : proposal)
    : [...proposals, updated]
}

export function unresolvedVideoIterationLockPartIDs(
  proposal: VideoEditProposalWire | null,
  selectedChangeIds: string[] = [],
): string[] {
  if (!proposal?.plan) return []
  const selected = new Set(selectedChangeIds)
  return proposal.plan.parts
    .filter((part) => proposal.plan?.kind === 'initial' || selected.has(part.id))
    .filter((part) => (part.animation_candidates?.candidates.length ?? 0) > 1
      && !part.animation_candidates?.selected_candidate_id)
    .map((part) => part.id)
}

export function selectVideoAnimationCandidateLocally(
  proposal: VideoEditProposalWire,
  partId: string,
  candidate: VideoAnimationCandidateWire,
): VideoEditProposalWire | null {
  if (proposal.status !== 'pending' || !proposal.plan) return null
  const partIndex = proposal.plan.parts.findIndex((part) => part.id === partId)
  if (partIndex < 0) return null
  const part = proposal.plan.parts[partIndex]
  const candidates = part.animation_candidates
  const ownedCandidate = candidates?.candidates.find((item) => item.id === candidate.id)
  if (!candidates || !ownedCandidate || candidates.status === 'ready') return null
  if (ownedCandidate.source.session_id !== candidate.source.session_id
    || ownedCandidate.source.collection_id !== candidate.source.collection_id
    || ownedCandidate.source.variant_id !== candidate.source.variant_id
    || ownedCandidate.source.event_seq !== candidate.source.event_seq) return null

  const parts = [...proposal.plan.parts]
  parts[partIndex] = {
    ...part,
    animation_candidates: {
      ...candidates,
      selected_candidate_id: ownedCandidate.id,
      selected_source: ownedCandidate.source,
      derivative: undefined,
      failure_reason: undefined,
      status: 'awaiting_export',
    },
  }
  return { ...proposal, plan: { ...proposal.plan, parts } }
}

export function shouldScheduleVideoCanvasFrame(isPlaying: boolean, visibilityState: DocumentVisibilityState): boolean {
  return isPlaying && visibilityState !== 'hidden'
}

export function videoAnimationPartAtClip(plan: VideoPlanProposalWire | null, clipId: string | null | undefined): VideoPlanProposalWire['parts'][number] | null {
  if (!clipId) return null
  return plan?.parts.find((part) => part.id === clipId && Boolean(part.animation_candidates)) ?? null
}

export type VideoActivePreviewIdentity = {
  projectId: string
  proposalId: string
  baseRevisionId: string
  workingRevisionId: string
  timelineClipId: string
  planPartId: string
  candidateId: string
  sourceSessionId: string
  sourceCollectionId: string
  sourceVariantId: string
  sourceEventSeq: number
}

export function videoActivePreviewIdentity(input: {
  projectId: string
  proposal: VideoEditProposalWire | null
  revisionId: string
  timelineClipId: string
  part: VideoPlanProposalWire['parts'][number]
  candidate: VideoAnimationCandidateWire
}): VideoActivePreviewIdentity {
  return {
    projectId: input.projectId,
    proposalId: input.proposal?.id ?? '',
    baseRevisionId: input.proposal?.base_revision_id ?? input.revisionId,
    workingRevisionId: input.proposal?.working_revision_id ?? input.revisionId,
    timelineClipId: input.timelineClipId,
    planPartId: input.part.id,
    candidateId: input.candidate.id,
    sourceSessionId: input.candidate.source.session_id,
    sourceCollectionId: input.candidate.source.collection_id,
    sourceVariantId: input.candidate.source.variant_id,
    sourceEventSeq: input.candidate.source.event_seq,
  }
}

export function videoActivePreviewCandidate(input: {
  identity: VideoActivePreviewIdentity | null
  projectId: string
  proposal: VideoEditProposalWire | null
  revisionId: string
  timelineClipId: string
  part: VideoPlanProposalWire['parts'][number] | null
}): VideoAnimationCandidateWire | null {
  const { identity, part } = input
  if (!identity || !part) return null
  const proposalId = input.proposal?.id ?? ''
  const baseRevisionId = input.proposal?.base_revision_id ?? input.revisionId
  const workingRevisionId = input.proposal?.working_revision_id ?? input.revisionId
  if (identity.projectId !== input.projectId
    || identity.proposalId !== proposalId
    || identity.baseRevisionId !== baseRevisionId
    || identity.workingRevisionId !== workingRevisionId
    || identity.timelineClipId !== input.timelineClipId
    || identity.planPartId !== part.id) return null
  const candidate = part.animation_candidates?.candidates.find((item) => item.id === identity.candidateId)
  if (!candidate
    || candidate.source.session_id !== identity.sourceSessionId
    || candidate.source.collection_id !== identity.sourceCollectionId
    || candidate.source.variant_id !== identity.sourceVariantId
    || candidate.source.event_seq !== identity.sourceEventSeq) return null
  return candidate
}

export function videoClipReviewState(part: VideoPlanProposalWire['parts'][number] | undefined, mediaType: string, segmentType: TimelineSegment['type']): { mediaKind: string; state: string } {
  const storyboard = videoPlanPartStoryboardContext(part)
  if (storyboard) return storyboard.productionState === 'pending'
    ? { mediaKind: 'Storyboard still', state: 'Placeholder · filming needed' }
    : { mediaKind: 'Storyboard still', state: 'Production ready' }
  const animation = part?.animation_candidates
  if (animation) {
    if (animation.status === 'failed') return { mediaKind: 'Live HTML', state: 'Motion failed' }
    if (animation.status === 'ready') return { mediaKind: 'Motion', state: 'Motion ready' }
    if (videoAnimationReadyForConfirmation(part)) return { mediaKind: 'Live HTML', state: 'Motion selected · ready' }
    return { mediaKind: 'Live HTML', state: 'Choose motion' }
  }
  if (segmentType === 'video' || mediaType.startsWith('video/')) return { mediaKind: 'Video', state: 'Video' }
  return { mediaKind: 'Still', state: 'Still' }
}

export function defaultRenderedVideoExportPath(workspacePath: string, title: string, revisionNumber: number): string {
  const safeTitle = title.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'video'
  const root = workspacePath.replace(/\/+$/, '')
  return root ? `${root}/exports/${safeTitle}-r${Math.max(1, revisionNumber)}.mp4` : ''
}

export function preferredVisibleVideoProject(projects: VideoProjectSnapshotWire[]): VideoProjectSnapshotWire | undefined {
  const newestFirst = [...projects].sort((left, right) => right.updated_at - left.updated_at || right.created_at - left.created_at || right.id.localeCompare(left.id))
  return newestFirst.find((project) => project.metadata?.reviewable_plan === true && Boolean(project.current_revision_id))
    ?? newestFirst.find((project) => project.project_kind === 'video_tool' && Boolean(project.current_revision_id))
    ?? newestFirst.find((project) => Boolean(project.current_revision_id))
    ?? newestFirst.find((project) => project.project_kind === 'video_tool')
    ?? newestFirst[0]
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
  progress_stage?: string
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
  root_path?: string
  clips?: VideoClipWire[]
  audio_clips?: Array<Partial<AudioClip> & { ref?: string; name?: string; mime_type?: string; size_bytes?: number; modified_at?: number; source_fingerprint?: string; fingerprint_version?: string }>
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
  type: 'video' | 'audio' | 'image' | 'frame'
  clipId: string
  src: string
  artifactRef?: VideoTimelineClipWire['artifact_ref']
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
  volume?: number
  muted?: boolean
  audioSource?: AudioSourceWire
  transitionIn?: VideoTransitionWire
  captions?: NonNullable<VideoTimelineClipWire['captions']>
  compositionPartId?: string
  composition?: VideoCompositionLinkWire
}

type TimelineLayoutSegment = TimelineSegment & {
  timelineStart: number
  timelineEnd: number
}

const TIMELINE_METADATA_KEY = 'timelineSegments'
const VIDEO_TOOL_BLACK_MODE_STORAGE_KEY = 'swarm.videoTool.blackMode'
const VIDEO_STUDIO_UI_PLAYHEAD_INTERVAL_MS = 1000 / 30
const VIDEO_STUDIO_SIDECAR_PLAYHEAD_INTERVAL_MS = 250
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
    if ((segment.type !== 'audio' && !segment.visible) || segment.duration <= 0) {
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
  // Audio may share the playhead range, but it must never become the visual
  // focus used to resolve a part's live animation candidates.
  const visual = active.filter((segment) => segment.type !== 'audio')
  return visual[visual.length - 1] ?? active[active.length - 1] ?? null
}

export function syncTimelineAudioPlayback(
  layout: TimelineLayoutSegment[],
  audioElements: Map<string, HTMLAudioElement>,
  playhead: number,
  playing: boolean,
): void {
  const activeAudioSegments = layout.filter((segment) => segment.type === 'audio'
    && segment.duration > 0
    && segment.timelineEnd > segment.timelineStart
    && playhead >= segment.timelineStart
    && playhead < segment.timelineEnd)
  const activeAudioClipIds = new Set(activeAudioSegments.map((segment) => segment.clipId))
  for (const [clipId, audio] of audioElements.entries()) {
    if (!activeAudioClipIds.has(clipId) && !audio.paused) audio.pause()
  }
  for (const segment of activeAudioSegments) {
    const audio = audioElements.get(segment.clipId)
    if (!audio) continue
    audio.volume = Math.max(0, Math.min(1, segment.volume ?? 1))
    audio.muted = segment.muted === true
    const sourceTime = segment.sourceStart + Math.max(0, playhead - segment.timelineStart)
    if (Number.isFinite(sourceTime) && Math.abs(audio.currentTime - sourceTime) > 0.12) {
      try { audio.currentTime = sourceTime } catch { /* Retry after metadata loads. */ }
    }
    // Playback entry points call this synchronously from the user's click so
    // audible media receives browser activation. The render loop reuses the
    // same path to keep audio locked to the canonical visual playhead.
    if (playing && audio.paused) void audio.play().catch(() => undefined)
    if (!playing && !audio.paused) audio.pause()
  }
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

function drawActiveCaptions(context: CanvasRenderingContext2D, canvas: HTMLCanvasElement, segment: TimelineLayoutSegment, playheadSeconds: number): void {
  const playheadMs = Math.round(playheadSeconds * 1000)
  const active = (segment.captions ?? []).filter((caption) => {
    const startMs = caption.start_ms ?? Math.round(segment.timelineStart * 1000)
    const endMs = caption.end_ms ?? Math.round(segment.timelineEnd * 1000)
    return Boolean(caption.text.trim()) && playheadMs >= startMs && playheadMs < endMs
  })
  for (const caption of active) {
    const position = caption.position?.trim().toLowerCase() || 'bottom'
    const y = position === 'top' ? canvas.height * 0.13 : position === 'center' ? canvas.height * 0.5 : canvas.height * 0.86
    context.save()
    context.font = '600 54px system-ui, sans-serif'
    context.textAlign = 'center'
    context.textBaseline = 'middle'
    context.lineJoin = 'round'
    context.lineWidth = 12
    context.strokeStyle = 'rgba(0, 0, 0, 0.82)'
    context.strokeText(caption.text, canvas.width / 2, y, canvas.width * 0.82)
    context.fillStyle = '#ffffff'
    context.fillText(caption.text, canvas.width / 2, y, canvas.width * 0.82)
    context.restore()
  }
}

export function soundtrackTimelineClip(input: {
  audio: AudioClip
  durationMs: number
  existing?: VideoTimelineClipWire | null
}): VideoTimelineClipWire {
  const durationMs = Math.max(1, Math.round(input.durationMs))
  const sourceStartMs = Math.max(0, Math.round(input.existing?.source_start_ms ?? 0))
  return {
    id: input.existing?.id || `soundtrack-${input.audio.ref.slice(-12)}`,
    name: input.audio.name,
    track: input.existing?.track ?? 1,
    layer: input.existing?.layer ?? 1,
    sequence: input.existing?.sequence ?? 0,
    source_kind: 'source_audio',
    audio_source: {
      ref: input.audio.ref,
      name: input.audio.name,
      mime_type: input.audio.mime_type,
      size_bytes: input.audio.size_bytes,
      source_fingerprint: input.audio.source_fingerprint,
      fingerprint_version: input.audio.fingerprint_version,
    },
    media_type: input.audio.mime_type,
    source_start_ms: sourceStartMs,
    source_end_ms: sourceStartMs + durationMs,
    timeline_start_ms: Math.max(0, Math.round(input.existing?.timeline_start_ms ?? 0)),
    timeline_end_ms: Math.max(0, Math.round(input.existing?.timeline_start_ms ?? 0)) + durationMs,
    duration_ms: durationMs,
    visible: false,
    volume: typeof input.existing?.volume === 'number' ? input.existing.volume : 1,
    muted: input.existing?.muted === true,
  }
}

export async function scanVideoFolder(workspacePath: string, folderPath: string): Promise<{ folderPath: string; clips: VideoClip[]; audioClips: AudioClip[] }> {
  const response = await requestJson<VideoScanResponse>('/v1/workspace/video/scan', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      workspace_path: workspacePath,
      root_path: folderPath,
    }),
  })
  return {
    folderPath: String(response.root_path ?? response.folder_path ?? folderPath).trim(),
    clips: metadataClips(response.clips),
    audioClips: Array.isArray(response.audio_clips) ? response.audio_clips.flatMap((clip): AudioClip[] => clip?.ref && clip.name && clip.mime_type && clip.source_fingerprint && clip.fingerprint_version ? [{ ref: clip.ref, name: clip.name, mime_type: clip.mime_type, size_bytes: clip.size_bytes ?? 0, modified_at: clip.modified_at ?? 0, source_fingerprint: clip.source_fingerprint, fingerprint_version: clip.fingerprint_version, extension: clip.extension ?? '' }] : []) : [],
  }
}

async function fetchVideoThreads(workspacePath: string): Promise<VideoThreadRecord[]> {
  const search = new URLSearchParams({ workspace_path: workspacePath })
  const response = await requestJson<{ threads?: VideoThreadWire[] }>(`/v1/workspace/video/threads?${search.toString()}`)
  return (Array.isArray(response.threads) ? response.threads : [])
    .map(mapVideoThread)
    .filter((thread): thread is VideoThreadRecord => Boolean(thread))
}

export async function fetchWorkspaceVideoCatalog(workspacePath: string): Promise<WorkspaceVideoCatalogItemWire[]> {
  const search = new URLSearchParams({ workspace_path: workspacePath, limit: '200' })
  const response = await requestJson<{ videos?: WorkspaceVideoCatalogItemWire[] }>(`/v1/workspace/video/projects?${search.toString()}`)
  return Array.isArray(response.videos) ? response.videos.map((item) => ({
    ...item,
    revisions: Array.isArray(item.revisions) ? item.revisions : [],
    related_sessions: Array.isArray(item.related_sessions) ? item.related_sessions : [],
  })) : []
}

export async function forkWorkspaceVideoRevision(input: {
  workspacePath: string
  sourceSessionId: string
  sourceProjectId: string
  sourceRevisionId: string
  destinationSessionId: string
  attachToSession?: boolean
}): Promise<VideoProjectDetailWire> {
  const response = await requestJson<{ project?: VideoProjectSnapshotWire; revision?: VideoProjectRevisionSnapshotWire }>('/v1/workspace/video/projects/fork', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      workspace_path: input.workspacePath,
      source_session_id: input.sourceSessionId,
      source_project_id: input.sourceProjectId,
      source_revision_id: input.sourceRevisionId,
      destination_session_id: input.destinationSessionId,
      attach_to_session: input.attachToSession === true,
    }),
  })
  if (!response.project || !response.revision) throw new Error('Video fork returned incomplete state')
  return { project: response.project, current_revision: response.revision, confirmed_revision: response.revision }
}

function videoThreadFromSessionProject(
  sessionId: string,
  workspace: WorkspaceEntry | null,
  session?: { title?: string; created_at?: number; updated_at?: number },
): VideoThreadRecord | null {
  const id = sessionId.trim()
  if (!id || !workspace) return null
  return {
    id,
    title: String(session?.title ?? '').trim() || 'Video session',
    workspacePath: workspace.path,
    workspaceName: workspace.workspaceName,
    videoFolders: [],
    videoClips: [],
    videoClipOrder: [],
    createdAt: typeof session?.created_at === 'number' ? session.created_at : 0,
    updatedAt: typeof session?.updated_at === 'number' ? session.updated_at : 0,
  }
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
  metadata?: Record<string, unknown>
  beforeThreadCreate?: (sessionId: string) => Promise<void>
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
    metadata: input.metadata ?? videoStudioSessionMetadata(),
  })
  if (input.beforeThreadCreate) {
    await input.beforeThreadCreate(createdSession.id)
  }
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
      metadata: input.metadata,
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
      ...(seg.compositionPartId ? { composition_part_id: seg.compositionPartId } : {}),
      ...(seg.composition ? { composition: seg.composition } : {}),
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
    ? [
        ...currentPlan.parts.map((part) => {
          const replacement = plan.parts.find((candidate) => candidate.id === part.id)
          return replacement ? { ...part, ...replacement, composition: replacement.composition ?? part.composition } : part
        }),
        ...plan.parts.filter((part) => !currentPlan.parts.some((current) => current.id === part.id)),
      ]
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
      source_start_ms: part.source_start_ms ?? 0,
      source_end_ms: part.source_end_ms ?? ((part.visual_media_type || part.visual?.media_type) === 'video/mp4' ? undefined : part.duration_ms),
      captions: part.caption ? [{ ...part.caption, start_ms: startMs + (part.caption.start_ms ?? 0), end_ms: startMs + (part.caption.end_ms ?? part.duration_ms) }] : [],
      composition_part_id: part.composition ? part.id : undefined,
      composition: part.composition,
    }
    startMs = endMs
    return clip
  })
  const planPartIds = new Set(parts.map((part) => part.id))
  const auxiliaryClips = (accepted.clips ?? []).filter((clip) => !planPartIds.has(clip.id))
  const clips = [...planClips, ...auxiliaryClips]
  const transitions = [
    ...parts.flatMap((part) => part.transition ? [{ ...part.transition }] : []),
    ...(accepted.transitions ?? []).filter((transition) => !planPartIds.has(transition.from_clip_id) || !planPartIds.has(transition.to_clip_id)),
  ]
  const totalDurationMs = auxiliaryClips.reduce((duration, clip) => Math.max(duration, clip.timeline_end_ms ?? ((clip.timeline_start_ms ?? 0) + (clip.duration_ms ?? 0))), startMs)
  const mergedPlan: VideoPlanProposalWire = { kind: 'initial', summary: plan.summary || currentPlan?.summary, composition_catalog: plan.composition_catalog ?? currentPlan?.composition_catalog, parts }
  return {
    ...accepted,
    composition_catalog: mergedPlan.composition_catalog,
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
    clips: (accepted.clips ?? []).map((clip) => ({ ...clip, captions: clip.captions?.map((caption) => ({ ...caption })) })),
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
      case 'replace_clip':
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
    const audioSource = clipWire.source_kind === 'source_audio' && Boolean(clipWire.audio_source?.ref)
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
      type: audioSource ? 'audio' : videoSource || artifactVideo ? 'video' : artifactSource ? 'image' : 'frame',
      clipId,
      src: audioSource && threadId ? `/v3/sessions/${encodeURIComponent(threadId)}/video/sources/media?source_ref=${encodeURIComponent(clipWire.audio_source!.ref)}` : videoSource ? (sourceClip && threadId ? clipMediaUrl(threadId, clipId) : threadId && sourceRef ? `/v3/sessions/${encodeURIComponent(threadId)}/video/sources/media?source_ref=${encodeURIComponent(sourceRef)}` : `/v1/workspace/video/threads/media?clip_id=${encodeURIComponent(clipId)}`) : artifactSource ? `/v3/sessions/${encodeURIComponent(artifactSessionId)}/artifacts/${encodeURIComponent(artifactId)}` : '',
      artifactRef: artifactRef ? { ...artifactRef, session_id: artifactSessionId || undefined, media_type: artifactMediaType || undefined } : undefined,
      sourceKind: clipWire.source_kind,
      title: details.title,
      onScreenText: details.onScreenText,
      frameDirection: details.still,
      track: clipWire.track ?? (audioSource ? 1 : 0),
      sequence: clipWire.sequence ?? originalIndex,
      layer: clipWire.layer ?? clipWire.track ?? (audioSource ? 1 : 0),
      timelinePositioned: typeof explicitStartMs === 'number' && explicitStartMs >= 0,
      start,
      sourceStart: sourceStartSec,
      duration: durationSec,
      visible: audioSource ? true : clipWire.visible !== false,
      volume: typeof clipWire.volume === 'number' ? clipWire.volume : 1,
      muted: clipWire.muted === true,
      audioSource: clipWire.audio_source,
      transitionIn,
      captions: clipWire.captions?.map((caption) => ({ ...caption })),
      compositionPartId: clipWire.composition_part_id,
      composition: clipWire.composition,
    }
  }).sort((left, right) => left.start - right.start
    || (left.layer ?? left.track ?? 0) - (right.layer ?? right.track ?? 0)
    || (left.track ?? 0) - (right.track ?? 0)
    || (left.sequence ?? 0) - (right.sequence ?? 0))
}

export async function fetchPrimaryVideoProject(sessionId: string): Promise<VideoProjectDetailWire> {
  const response = await requestJson<{ project?: VideoProjectSnapshotWire; current_revision?: VideoProjectRevisionSnapshotWire; confirmed_revision?: VideoProjectRevisionSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/primary`,
  )
  if (!response.project) throw new Error('Primary video project response returned no project')
  return { project: response.project, current_revision: response.current_revision, confirmed_revision: response.confirmed_revision }
}

export async function listVideoProjects(sessionId: string): Promise<VideoProjectSnapshotWire[]> {
  const response = await requestJson<{ projects?: VideoProjectSnapshotWire[] }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects?limit=32`,
  )
  return Array.isArray(response.projects) ? response.projects : []
}

export async function fetchVideoProject(sessionId: string, projectId: string): Promise<VideoProjectDetailWire> {
  const response = await requestJson<{ project?: VideoProjectSnapshotWire; current_revision?: VideoProjectRevisionSnapshotWire; confirmed_revision?: VideoProjectRevisionSnapshotWire }>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/video/projects/${encodeURIComponent(projectId)}`,
  )
  if (!response.project) throw new Error('Video project response returned no project')
  return { project: response.project, current_revision: response.current_revision, confirmed_revision: response.confirmed_revision }
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
  const [browserAudioClips, setBrowserAudioClips] = useState<AudioClip[]>([])
  const [browserScanLoading, setBrowserScanLoading] = useState(false)
  const [browserScanError, setBrowserScanError] = useState<string | null>(null)
  const [addingFolderPath, setAddingFolderPath] = useState<string | null>(null)
  const [createError, setCreateError] = useState<string | null>(null)
  const [newSessionTitle, setNewSessionTitle] = useState('')
  const [creatingBlankSession, setCreatingBlankSession] = useState(false)
  const [videoLibraryQuery, setVideoLibraryQuery] = useState('')
  const [selectedLibraryVideoId, setSelectedLibraryVideoId] = useState<string | null>(null)
  const [startingFromLibrary, setStartingFromLibrary] = useState(false)
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
  const [confirmedRevision, setConfirmedRevision] = useState<VideoProjectRevisionSnapshotWire | null>(null)
  const [projectRevisions, setProjectRevisions] = useState<VideoProjectRevisionSnapshotWire[]>([])
  const [previewRevisionId, setPreviewRevisionId] = useState<string | null>(null)
  const [projectLoading, setProjectLoading] = useState(false)
  const [creatingProject, setCreatingProject] = useState(false)
  const [restoringRevisionId, setRestoringRevisionId] = useState<string | null>(null)
  const [transitionKind, setTransitionKind] = useState<VideoTransitionKind>('cut')
  const [aiRefreshKey, setAIRefreshKey] = useState(0)
  const [pendingProposal, setPendingProposal] = useState<VideoEditProposalWire | null>(null)
  const [projectProposals, setProjectProposals] = useState<VideoEditProposalWire[]>([])
  const [pendingSelectedChangeIds, setPendingSelectedChangeIds] = useState<string[]>([])
  const [compositionEditing, setCompositionEditing] = useState(false)
  const [compositionProposalBusy, setCompositionProposalBusy] = useState(false)
  const [soundtrackPickerOpen, setSoundtrackPickerOpen] = useState(false)
  const [soundtrackDraft, setSoundtrackDraft] = useState<VideoTimelineClipWire | null>(null)
  const [soundtrackProposalBusy, setSoundtrackProposalBusy] = useState(false)
  const [workingCutReviewBusy, setWorkingCutReviewBusy] = useState(false)
  const [composerDraftRequest, setComposerDraftRequest] = useState<{ id: number; draft: string } | undefined>()
  const [studioArtifactSelectionRequest, setStudioArtifactSelectionRequest] = useState<ReturnType<typeof videoPlanPartMessageSelection> | null>(null)
  const [studioArtifactReviewPortalTarget, setStudioArtifactReviewPortalTarget] = useState<HTMLDivElement | null>(null)
  const [studioComposerContext, setStudioComposerContext] = useState<{ revisionId: string; anchorClipId: string; label: string; playheadMs: number; selectionKind: 'visual' | 'transition' | 'iteration'; transition: VideoTransitionWire | null; iteration?: VideoIterationComposerContext; storyboard?: ReturnType<typeof videoPlanPartStoryboardContext> } | null>(null)
  const [revealingStorage, setRevealingStorage] = useState(false)
  const [rendering, setRendering] = useState(false)
  const [renderJob, setRenderJob] = useState<VideoRenderJobSnapshotWire | null>(null)
  const [renderProgress, setRenderProgress] = useState(0)
  const [renderError, setRenderError] = useState<string | null>(null)
  const [exportPath, setExportPath] = useState('')
  const [exportedPath, setExportedPath] = useState('')
  const [exporting, setExporting] = useState(false)
  const [blackModeEnabled, setBlackModeEnabled] = useState(() => {
    if (typeof window === 'undefined') {
      return false
    }
    return window.localStorage.getItem(VIDEO_TOOL_BLACK_MODE_STORAGE_KEY) === 'true'
  })
  const [isPlaying, setIsPlaying] = useState(false)
  const [activePreviewIdentity, setActivePreviewIdentity] = useState<VideoActivePreviewIdentity | null>(null)
  const [animationSelectionBusyPartId, setAnimationSelectionBusyPartId] = useState<string | null>(null)
  const [liveAnimationURL, setLiveAnimationURL] = useState('')
  const [liveAnimationError, setLiveAnimationError] = useState<string | null>(null)
  const [iterationCardURLs, setIterationCardURLs] = useState<Record<string, string>>({})
  const liveAnimationFrameRef = useRef<HTMLIFrameElement | null>(null)
  const [playhead, setPlayhead] = useState(0)
  const [clipDurations, setClipDurations] = useState<Record<string, number>>({})
  const [canvasRenderVersion, setCanvasRenderVersion] = useState(0)
  const requestCanvasRender = useCallback(() => setCanvasRenderVersion((version) => version + 1), [])
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const timelineScrollRef = useRef<HTMLDivElement | null>(null)
  const videoElementsRef = useRef<Map<string, CachedVideoMedia>>(new Map())
  const imageElementsRef = useRef<Map<string, CachedImageMedia>>(new Map())
  const audioElementsRef = useRef<Map<string, HTMLAudioElement>>(new Map())
  const playheadRef = useRef(0)
  const playbackStartRef = useRef(0)
  const playbackStartPlayheadRef = useRef(0)
  const lastPublishedPlayheadRef = useRef(0)
  const refreshRequestSequenceRef = useRef(0)
  const animationSelectionRequestSequenceRef = useRef(0)

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
  const videoLibraryQueryResult = useQuery({
    queryKey: ['video-library', selectedWorkspacePath],
    queryFn: () => fetchWorkspaceVideoCatalog(selectedWorkspacePath),
    enabled: selectedWorkspacePath.trim() !== '',
    staleTime: 15_000,
  })
  const videoLibrary = videoLibraryQueryResult.data ?? []
  const filteredVideoLibrary = useMemo(() => filterWorkspaceVideoCatalog(videoLibrary, videoLibraryQuery), [videoLibrary, videoLibraryQuery])
  const selectedLibraryVideo = useMemo(() => videoLibrary.find((item) => item.project.id === selectedLibraryVideoId) ?? null, [selectedLibraryVideoId, videoLibrary])
  const selectedLibraryRevision = useMemo(() => selectedLibraryVideo ? selectWorkspaceVideoRevision(selectedLibraryVideo, previewRevisionId) : null, [previewRevisionId, selectedLibraryVideo])
  const libraryReadOnly = selectedLibraryVideo?.source_archived === true
  const selectedSessionVideos = useMemo(() => workspaceVideosForSession(videoLibrary, selectedThreadId ?? ''), [selectedThreadId, videoLibrary])
  const routeVideoSession = useDesktopV3CacheSelector((state) => {
    const record = routeVideoSessionId ? state.sessionsById[routeVideoSessionId] : undefined
    return record?.kind === 'full' ? record.session : undefined
  })
  const videoThreads = useMemo(() => {
    const threads = videoThreadsQuery.data ?? []
    if (!routeVideoSessionId || threads.some((thread) => thread.id === routeVideoSessionId)) return threads
    const routed = videoThreadFromSessionProject(routeVideoSessionId, selectedWorkspace, routeVideoSession)
    return routed ? [routed, ...threads] : threads
  }, [routeVideoSession, routeVideoSessionId, selectedWorkspace, videoThreadsQuery.data])

  useEffect(() => {
    if (routeVideoSessionId && !selectedLibraryVideo && routeVideoSessionId !== selectedThreadId) {
      setSelectedThreadId(routeVideoSessionId)
      return
    }
    if (!selectedThreadId || selectedLibraryVideo || videoThreadsQuery.isLoading || !videoThreadsQuery.isFetched) return
    if (!videoThreads.some((thread) => thread.id === selectedThreadId)) setSelectedThreadId(null)
  }, [routeVideoSessionId, selectedLibraryVideo, selectedThreadId, videoThreads, videoThreadsQuery.isFetched, videoThreadsQuery.isLoading])

  useEffect(() => {
    if (!selectedThreadId || typeof window === 'undefined') return
    window.localStorage.setItem(`${VIDEO_STUDIO_LAST_SESSION_STORAGE_KEY}:${routeWorkspaceSlug}`, selectedThreadId)
    saveVideoSessionViewPreference(selectedThreadId, 'studio')
  }, [routeWorkspaceSlug, selectedThreadId])

  const videoProjectProjectionSequence = useDesktopV3CacheSelector(
    useCallback((state) => selectedThreadId ? videoProposalProjectionSequence(state, selectedThreadId) : 0, [selectedThreadId]),
  )

  const selectedThread = useMemo(() => {
    if (!selectedThreadId) return null
    const thread = videoThreads.find((candidate) => candidate.id === selectedThreadId)
    if (thread) return thread
    if (selectedLibraryVideo && (selectedLibraryVideo.source_session_id === selectedThreadId || selectedLibraryVideo.related_sessions.some((session) => session.session_id === selectedThreadId))) return {
      id: selectedThreadId,
      title: selectedLibraryVideo.source_session_title || selectedLibraryVideo.project.title || 'Video source session',
      workspacePath: selectedWorkspacePath,
      workspaceName: selectedWorkspaceName,
      videoFolders: [], videoClips: [], videoClipOrder: [],
      createdAt: selectedLibraryVideo.project.created_at,
      updatedAt: selectedLibraryVideo.project.updated_at,
    }
    return null
  }, [selectedLibraryVideo, selectedThreadId, selectedWorkspaceName, selectedWorkspacePath, videoThreads])

  const selectedClips = useMemo(() => orderedClips(selectedThread), [selectedThread])
  const legacyTimelineSegments = useMemo(() => buildTimelineSegments(selectedThread, selectedClips, clipDurations), [clipDurations, selectedClips, selectedThread])
  const previewRevision = useMemo(() => projectRevisions.find((revision) => revision.id === previewRevisionId) ?? null, [previewRevisionId, projectRevisions])
  const previewRevisionIndex = previewRevision ? projectRevisions.findIndex((revision) => revision.id === previewRevision.id) : -1
  const activeTurnRevision = previewRevision ?? currentRevision
  const activeTurnRevisionIndex = activeTurnRevision ? projectRevisions.findIndex((revision) => revision.id === activeTurnRevision.id) : -1
  const playerRevision = previewRevision ?? currentRevision
  const keptRevision = confirmedRevision ?? currentRevision
  const acceptedTimelineSegments = useMemo(() => playerRevision
    ? projectTimelineToTimelineSegments(playerRevision.timeline, clipDurations, selectedClips, selectedThread?.id ?? '')
    : legacyTimelineSegments, [clipDurations, legacyTimelineSegments, playerRevision, selectedClips, selectedThread?.id])
  const shadowTimeline = useMemo(() => {
    if (previewRevision || !pendingProposal || !currentRevision || pendingProposal.working_revision_id !== currentRevision.id) return null
    const base = projectRevisions.find((revision) => revision.id === pendingProposal.base_revision_id)
    if (!base) return currentRevision.timeline
    const selected = new Set(pendingSelectedChangeIds)
    const selectableCount = pendingProposal.plan?.kind === 'revision' ? pendingProposal.plan.parts.length : pendingProposal.plan ? 0 : pendingProposal.operations.length
    if (selected.size === selectableCount) return currentRevision.timeline
    const selectedProposal = pendingProposal.plan?.kind === 'revision'
      ? { ...pendingProposal, plan: { ...pendingProposal.plan, parts: pendingProposal.plan.parts.filter((part) => selected.has(part.id)) } }
      : pendingProposal.plan
        ? pendingProposal
        : { ...pendingProposal, operations: pendingProposal.operations.filter((operation) => selected.has(operation.id)) }
    return applyPendingVideoProposal(base.timeline, selectedProposal)
  }, [currentRevision, pendingProposal, pendingSelectedChangeIds, previewRevision, projectRevisions])
  const timelineSegments = useMemo(() => shadowTimeline
    ? projectTimelineToTimelineSegments(shadowTimeline, clipDurations, selectedClips, selectedThread?.id ?? '')
    : acceptedTimelineSegments, [acceptedTimelineSegments, clipDurations, selectedClips, selectedThread?.id, shadowTimeline])
  const timelineLayout = useMemo(() => layoutTimelineSegments(timelineSegments), [timelineSegments])
  const timelineLayoutByClipId = useMemo(() => new Map(timelineLayout.map((segment) => [segment.clipId, segment])), [timelineLayout])
  const visualTimelineLayout = useMemo(() => timelineLayout.filter((segment) => segment.type !== 'audio'), [timelineLayout])
  const audioTimelineLayout = useMemo(() => timelineLayout.filter((segment) => segment.type === 'audio'), [timelineLayout])
  const visibleTimelineLayout = useMemo(() => visualTimelineLayout.filter((segment) => segment.visible && segment.duration > 0), [visualTimelineLayout])
  const hiddenTimelineLayout = useMemo(() => visualTimelineLayout.filter((segment) => !segment.visible), [visualTimelineLayout])
  const movieDuration = useMemo(() => timelineDuration(timelineLayout), [timelineLayout])
  const timelineTrackWidthPx = useMemo(() => timelineTrackWidth(movieDuration), [movieDuration])
  const playheadX = movieDuration > 0 ? Math.min(timelineTrackWidthPx, Math.max(0, (playhead / movieDuration) * timelineTrackWidthPx)) : 0
  const activeSegment = useMemo(() => activeTimelineSegment(timelineLayout, playhead), [playhead, timelineLayout])
  const currentWorkingProposal = useMemo(
    () => projectProposals.find((proposal) => proposal.status === 'pending' && proposal.working_revision_id === currentRevision?.id) ?? null,
    [currentRevision?.id, projectProposals],
  )
  const playbackPlan = useMemo(
    () => videoPlanForPlayback(null, shadowTimeline ?? playerRevision?.timeline),
    [playerRevision, shadowTimeline],
  )
  const liveAnimationPart = useMemo(() => videoAnimationPartAtClip(playbackPlan, activeSegment?.clipId), [activeSegment?.clipId, playbackPlan])
  const activePreviewCandidate = useMemo(() => videoActivePreviewCandidate({
    identity: activePreviewIdentity,
    projectId: videoProject?.id ?? '',
    proposal: currentWorkingProposal,
    revisionId: playerRevision?.id ?? '',
    timelineClipId: activeSegment?.clipId ?? '',
    part: liveAnimationPart,
  }), [activePreviewIdentity, activeSegment?.clipId, currentWorkingProposal, liveAnimationPart, playerRevision?.id, videoProject?.id])
  const activeCandidate = activePreviewCandidate
    ?? liveAnimationPart?.animation_candidates?.candidates.find((candidate) => candidate.id === liveAnimationPart.animation_candidates?.selected_candidate_id)
    ?? liveAnimationPart?.animation_candidates?.candidates[0]
    ?? null
  const activePreviewRequestIdentity = useMemo(() => videoProject && playerRevision && liveAnimationPart && activeCandidate
    ? videoActivePreviewIdentity({
      projectId: videoProject.id,
      proposal: currentWorkingProposal,
      revisionId: playerRevision.id,
      timelineClipId: liveAnimationPart.id,
      part: liveAnimationPart,
      candidate: activeCandidate,
    })
    : null, [activeCandidate, currentWorkingProposal, liveAnimationPart, playerRevision, videoProject])
  const activeClipReviewState = videoClipReviewState(liveAnimationPart ?? undefined, activeSegment?.artifactRef?.media_type ?? '', activeSegment?.type ?? 'frame')
  const activeCompositionPart = useMemo(() => playbackPlan?.parts.find((part) => part.id === activeSegment?.compositionPartId || part.id === activeSegment?.clipId) ?? null, [activeSegment?.clipId, activeSegment?.compositionPartId, playbackPlan])
  const activeCompositionCatalog = playbackPlan?.composition_catalog ?? (shadowTimeline ?? playerRevision?.timeline)?.composition_catalog
  const activeCompositionSlots = useMemo(() => resolveVideoComposition(activeCompositionCatalog, activeCompositionPart?.composition ?? activeSegment?.composition, (shadowTimeline ?? playerRevision?.timeline)?.width ?? 1920, (shadowTimeline ?? playerRevision?.timeline)?.height ?? 1080), [activeCompositionCatalog, activeCompositionPart?.composition, activeSegment?.composition, playerRevision?.timeline, shadowTimeline])
  const currentTurnParts = useMemo(() => currentWorkingProposal?.plan?.parts ?? [], [currentWorkingProposal])
  const currentTurnVariantPartCount = currentTurnParts.filter((part) => (part.animation_candidates?.candidates.length ?? 0) > 0).length
  const currentTurnStaticFallbackCount = currentTurnParts.filter((part) => (part.visual_media_type || part.visual?.media_type || '').startsWith('image/')).length
  const currentTurnSummary = currentWorkingProposal?.title || currentWorkingProposal?.plan?.summary || currentWorkingProposal?.rationale || 'Review the clips created by this pending Video Studio change.'
  const currentWorkingTimelineSegments = useMemo(() => currentRevision
    ? projectTimelineToTimelineSegments(currentRevision.timeline, clipDurations, selectedClips, selectedThread?.id ?? '')
    : [], [clipDurations, currentRevision, selectedClips, selectedThread?.id])
  const currentWorkingVisualLayout = useMemo(() => layoutTimelineSegments(currentWorkingTimelineSegments).filter((segment) => segment.type !== 'audio' && segment.visible && segment.duration > 0), [currentWorkingTimelineSegments])
  const currentTurnPartByID = useMemo(() => new Map(currentTurnParts.map((part) => [part.id, part])), [currentTurnParts])
  const currentTurnConfirmIDs = currentWorkingProposal?.plan?.kind === 'initial' ? [] : pendingSelectedChangeIds
  const currentTurnUnreadyAnimations = currentTurnParts.filter((part) => !videoAnimationReadyForConfirmation(part)
    && (currentWorkingProposal?.plan?.kind === 'initial' || currentTurnConfirmIDs.includes(part.id)))
  const selectedTurnParts = currentWorkingProposal?.plan?.kind === 'revision'
    ? currentTurnParts.filter((part) => currentTurnConfirmIDs.includes(part.id))
    : currentTurnParts
  const unresolvedTurnCompositionPartIDs = selectedTurnParts.filter((part) => part.composition && !part.composition.disabled && resolveVideoComposition(currentWorkingProposal?.plan?.composition_catalog, part.composition, currentRevision?.timeline.width ?? 1920, currentRevision?.timeline.height ?? 1080).some((slot) => !slot.source)).map((part) => part.id)
  const pendingTurnProductionPartIDs = selectedTurnParts.filter((part) => part.production_state === 'pending').map((part) => part.id)
  const currentTurnCanConfirm = Boolean(currentWorkingProposal) && currentTurnUnreadyAnimations.length === 0 && unresolvedTurnCompositionPartIDs.length === 0 && pendingTurnProductionPartIDs.length === 0 && (currentWorkingProposal?.plan?.kind === 'initial' || currentTurnConfirmIDs.length > 0)
  const unresolvedIterationLockPartIDs = useMemo(
    () => unresolvedVideoIterationLockPartIDs(currentWorkingProposal, currentTurnConfirmIDs),
    [currentTurnConfirmIDs, currentWorkingProposal],
  )
  const acceptedPlan = useMemo(() => confirmedRevision ? acceptedVideoPlan(confirmedRevision.timeline) : null, [confirmedRevision])
  const visibleStoryboardPartByID = useMemo(() => new Map((playbackPlan?.parts ?? []).filter((part) => videoPlanPartStoryboardContext(part)).map((part) => [part.id, part])), [playbackPlan])
  const pendingStoryboardPartIDs = useMemo(() => (playbackPlan?.parts ?? []).filter((part) => videoPlanPartStoryboardContext(part)?.productionState === 'pending').map((part) => part.id), [playbackPlan])
  const hasUnresolvedPlanFrames = timelineSegments.some((segment) => segment.sourceKind === 'text' || (segment.sourceKind === 'managed_artifact' && !segment.src))
  const renderRevision = playerRevision
  const renderBlockedByPendingProposal = Boolean(currentWorkingProposal || pendingProposal?.status === 'pending')
  const renderBlockedByIterations = !previewRevision && unresolvedIterationLockPartIDs.length > 0
  const renderBlockedByStoryboard = pendingStoryboardPartIDs.length > 0
  const renderBlockedByComposition = (playbackPlan?.parts ?? []).some((part) => part.composition && !part.composition.disabled && resolveVideoComposition(playbackPlan?.composition_catalog, part.composition, playerRevision?.timeline.width ?? 1920, playerRevision?.timeline.height ?? 1080).some((slot) => !slot.source))
  const selectedClip = selectedClips.find((clip) => clip.id === selectedClipId) ?? selectedClips[0] ?? null
  const acceptedSoundtrack = useMemo(() => (keptRevision?.timeline.clips ?? []).find((clip) => clip.source_kind === 'source_audio') ?? null, [keptRevision])
  const playbackSoundtrack = audioTimelineLayout[0] ?? null

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
    if (selectedLibraryVideo) return
    setVideoProjects([])
    setSelectedProjectId(null)
    setVideoProject(null)
    setCurrentRevision(null)
    setConfirmedRevision(null)
    setProjectRevisions([])
    setPreviewRevisionId(null)
    setPendingProposal(null)
    setProjectProposals([])
    setPendingSelectedChangeIds([])
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
  }, [selectedLibraryVideo, selectedThread?.id])

  useEffect(() => {
    let cancelled = false
    if (selectedLibraryVideo) return
    setVideoProject(null)
    setCurrentRevision(null)
    setConfirmedRevision(null)
    setProjectRevisions([])
    setPreviewRevisionId(null)
    setPendingProposal(null)
    setProjectProposals([])
    setPendingSelectedChangeIds([])
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
        setConfirmedRevision(detail.confirmed_revision ?? selectedRevision)
        setProjectRevisions(await listVideoProjectRevisions(selectedThread.id, detail.project.id))
      } catch (error) {
        if (!cancelled) setCreateError(error instanceof Error ? error.message : String(error))
      } finally {
        if (!cancelled) setProjectLoading(false)
      }
    })()
    return () => { cancelled = true }
  }, [selectedLibraryVideo, selectedProjectId, selectedThread?.id])

  const refreshSelectedVideoProject = useCallback(async () => {
    if (!selectedThread) return
    const requestSequence = ++refreshRequestSequenceRef.current
    const projects = await listVideoProjects(selectedThread.id)
    const selected = projects.find((project) => project.id === selectedProjectId)
    const preferred = selected ?? preferredVisibleVideoProject(projects)
    const projectId = preferred?.id
    setVideoProjects(projects)
    if (!projectId) return
    const [detail, revisions] = await Promise.all([
      fetchVideoProject(selectedThread.id, projectId),
      listVideoProjectRevisions(selectedThread.id, projectId),
    ])
    if (requestSequence !== refreshRequestSequenceRef.current) return
    setSelectedProjectId(projectId)
    setVideoProject(detail.project)
    setCurrentRevision(detail.current_revision ?? null)
    setConfirmedRevision(detail.confirmed_revision ?? detail.current_revision ?? null)
    setProjectRevisions(revisions)
    setPreviewRevisionId((current) => current && revisions.some((revision) => revision.id === current) ? current : null)
  }, [selectedProjectId, selectedThread])

  useEffect(() => {
    if (!videoProjectProjectionSequence || !selectedThread || !selectedProjectId) return
    void refreshSelectedVideoProject().catch((error) => setCreateError(error instanceof Error ? error.message : String(error)))
  }, [refreshSelectedVideoProject, selectedProjectId, selectedThread, videoProjectProjectionSequence])

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

  const beginTimelinePlayback = useCallback((startAt: number) => {
    playheadRef.current = startAt
    setPlayhead(startAt)
    playbackStartPlayheadRef.current = startAt
    playbackStartRef.current = performance.now()
    // Prime audible media while this callback still has the browser's user
    // activation. Starting it later from requestAnimationFrame is commonly
    // rejected by autoplay policy and leaves the visual playing silently.
    syncTimelineAudioPlayback(timelineLayout, audioElementsRef.current, startAt, true)
    setIsPlaying(true)
  }, [timelineLayout])

  const previewCurrentTurnPart = useCallback((part: NonNullable<VideoPlanProposalWire['parts'][number]>, candidate?: VideoAnimationCandidateWire) => {
    const layout = timelineLayoutByClipId.get(part.id)
    const startAt = layout?.timelineStart ?? 0
    setSelectedClipId(part.id)
    if (candidate && videoProject && playerRevision) setActivePreviewIdentity(videoActivePreviewIdentity({
      projectId: videoProject.id,
      proposal: currentWorkingProposal,
      revisionId: playerRevision.id,
      timelineClipId: part.id,
      part,
      candidate,
    }))
    beginTimelinePlayback(startAt)
  }, [beginTimelinePlayback, currentWorkingProposal, playerRevision, timelineLayoutByClipId, videoProject])

  const selectLiveAnimationCandidate = useCallback((part: NonNullable<VideoPlanProposalWire['parts'][number]>, candidate: VideoAnimationCandidateWire) => {
    if (!currentWorkingProposal || !selectedThread || !videoProject || animationSelectionBusyPartId) return
    if (currentWorkingProposal.plan?.kind === 'revision' && !pendingSelectedChangeIds.includes(part.id)) {
      setCreateError(`Enable ${part.title} before choosing one of its variants.`)
      return
    }
    const optimistic = selectVideoAnimationCandidateLocally(currentWorkingProposal, part.id, candidate)
    if (!optimistic) {
      setCreateError(part.animation_candidates?.status === 'ready'
        ? `${part.title} already has a finalized motion source. Start a new revision to change it.`
        : `This variant does not belong to ${part.title}.`)
      return
    }

    previewCurrentTurnPart(part, candidate)
    const layout = timelineLayoutByClipId.get(part.id)
    const startMs = Math.round((layout?.timelineStart ?? 0) * 1000)
    const endMs = startMs + part.duration_ms
    const label = `${part.title} · ${candidate.label || candidate.id}`
    setCreateError(null)
    setAnimationSelectionBusyPartId(part.id)
    const selectionRequestId = ++animationSelectionRequestSequenceRef.current
    const selectionProposalId = currentWorkingProposal.id
    const selectionWorkingRevisionId = currentWorkingProposal.working_revision_id || ''
    setPendingProposal(optimistic)
    setProjectProposals((current) => replaceVideoEditProposal(current, optimistic))
    setStudioComposerContext({
      revisionId: currentWorkingProposal.working_revision_id || currentWorkingProposal.base_revision_id,
      anchorClipId: part.id,
      label,
      playheadMs: startMs,
      selectionKind: 'iteration',
      transition: null,
      iteration: { proposalId: currentWorkingProposal.id, parentRevisionId: currentWorkingProposal.base_revision_id, candidateRevisionId: currentWorkingProposal.working_revision_id || '', changeId: candidate.id, anchorClipId: part.id, startMs, endMs, label, artifact: candidate.source },
    })
    setStudioArtifactSelectionRequest({ ...candidate.source, label: candidate.label || candidate.id, description: `Selected for ${part.title} on the current Video Studio turn.`, action: 'select' })
    void selectVideoAnimationCandidate({ sessionId: selectedThread.id, projectId: videoProject.id, proposalId: currentWorkingProposal.id, partId: part.id, candidate })
      .then((proposal) => {
        if (selectionRequestId !== animationSelectionRequestSequenceRef.current
          || proposal.id !== selectionProposalId
          || (proposal.working_revision_id || '') !== selectionWorkingRevisionId) return
        setPendingProposal(proposal)
        setProjectProposals((current) => replaceVideoEditProposal(current, proposal))
      })
      .catch((error) => {
        if (selectionRequestId !== animationSelectionRequestSequenceRef.current) return
        setPendingProposal(currentWorkingProposal)
        setProjectProposals((current) => replaceVideoEditProposal(current, currentWorkingProposal))
        setCreateError(error instanceof Error ? error.message : String(error))
      })
      .finally(() => {
        if (selectionRequestId === animationSelectionRequestSequenceRef.current) setAnimationSelectionBusyPartId(null)
      })
  }, [animationSelectionBusyPartId, currentWorkingProposal, pendingSelectedChangeIds, previewCurrentTurnPart, selectedThread, timelineLayoutByClipId, videoProject])

  useEffect(() => {
    if (!videoProject || !playerRevision || !liveAnimationPart || !activePreviewRequestIdentity) {
      setActivePreviewIdentity(null)
      setLiveAnimationURL('')
      setLiveAnimationError(null)
      return
    }
    setActivePreviewIdentity((current) => current && videoActivePreviewCandidate({
      identity: current,
      projectId: videoProject.id,
      proposal: currentWorkingProposal,
      revisionId: playerRevision.id,
      timelineClipId: liveAnimationPart.id,
      part: liveAnimationPart,
    }) ? current : activePreviewRequestIdentity)
  }, [activePreviewRequestIdentity, currentWorkingProposal, liveAnimationPart, playerRevision, videoProject])

  useEffect(() => {
    let cancelled = false
    const candidates = currentTurnParts.flatMap((part) => (part.animation_candidates?.candidates ?? []).map((candidate) => ({ partId: part.id, candidate })))
    setIterationCardURLs({})
    if (candidates.length === 0) return
    void Promise.all(candidates.map(async ({ partId, candidate }) => {
      const access = await fetchDesktopV3ArtifactPreviewAccess(candidate.source.session_id, candidate.source.variant_id)
      return [`${partId}:${candidate.id}`, access.url] as const
    })).then((entries) => { if (!cancelled) setIterationCardURLs(Object.fromEntries(entries)) }).catch(() => { if (!cancelled) setIterationCardURLs({}) })
    return () => { cancelled = true }
  }, [currentTurnParts])

  useEffect(() => {
    let cancelled = false
    setLiveAnimationURL('')
    setLiveAnimationError(null)
    if (!activeCandidate || !activePreviewRequestIdentity) return
    void fetchDesktopV3ArtifactPreviewAccess(activeCandidate.source.session_id, activeCandidate.source.variant_id).then((access) => {
      if (!cancelled) setLiveAnimationURL(access.url)
    }).catch((error) => {
      if (!cancelled) setLiveAnimationError(error instanceof Error ? error.message : String(error))
    })
    return () => { cancelled = true }
  }, [activeCandidate, activePreviewRequestIdentity])

  useEffect(() => {
    const frame = liveAnimationFrameRef.current?.contentWindow
    if (!frame || !liveAnimationPart) return
    const localTimeMs = Math.max(0, Math.min(liveAnimationPart.duration_ms, Math.round(playhead * 1000 - (activeSegment?.timelineStart ?? 0) * 1000)))
    frame.postMessage(desktopV3ArtifactIterationMessage(`video-studio-seek-${localTimeMs}`, 'seek', localTimeMs), '*')
    if (!isPlaying) frame.postMessage(desktopV3ArtifactIterationMessage('video-studio-stop', 'stop'), '*')
  }, [activeSegment?.timelineStart, isPlaying, liveAnimationPart, playhead])

  useEffect(() => {
    const cache = videoElementsRef.current
    const mediaByClipId = new Map(timelineSegments.filter((segment) => segment.type === 'video' && segment.src).map((segment) => [segment.clipId, segment.src]))
    for (const [clipId, cached] of cache.entries()) {
      if (!mediaByClipId.has(clipId)) {
        cached.element.pause()
        cached.element.removeAttribute('src')
        cached.element.load()
        cache.delete(clipId)
      }
    }
    setClipDurations((current) => {
      const next = Object.fromEntries(Object.entries(current).filter(([clipId]) => mediaByClipId.has(clipId)))
      return Object.keys(next).length === Object.keys(current).length ? current : next
    })
    for (const [clipId, src] of mediaByClipId) {
      const { entry, replaced } = replaceCachedVideoMedia(cache, clipId, src, () => document.createElement('video'))
      if (!replaced && entry.element.src) continue
      if (replaced) setClipDurations((current) => {
        if (!(clipId in current)) return current
        const next = { ...current }
        delete next[clipId]
        return next
      })
      const video = entry.element
      video.src = src
      video.preload = 'metadata'
      video.muted = true
      video.playsInline = true
      const updateDuration = () => {
        const duration = video.duration
        if (!Number.isFinite(duration) || duration <= 0) return
        setClipDurations((current) => Math.abs((current[clipId] ?? 0) - duration) < 0.001 ? current : { ...current, [clipId]: duration })
      }
      let authRetryPending = false
      let authRetryAttempted = false
      const retryAfterAuth = () => {
        if (authRetryPending || authRetryAttempted) return
        authRetryPending = true
        authRetryAttempted = true
        void ensureDesktopSession(true).then(() => {
          video.src = src
          video.load()
        }).catch(() => undefined).finally(() => { authRetryPending = false })
      }
      video.addEventListener('loadedmetadata', updateDuration)
      video.addEventListener('durationchange', updateDuration)
      video.addEventListener('loadeddata', requestCanvasRender)
      video.addEventListener('seeked', requestCanvasRender)
      video.addEventListener('error', retryAfterAuth)
      video.load()
    }
  }, [requestCanvasRender, timelineSegments])

  useEffect(() => {
    const cache = audioElementsRef.current
    const mediaByClipId = new Map(timelineSegments.filter((segment) => segment.type === 'audio' && segment.src).map((segment) => [segment.clipId, segment]))
    for (const [clipId, audio] of cache.entries()) {
      if (mediaByClipId.has(clipId)) continue
      audio.pause()
      audio.removeAttribute('src')
      audio.load()
      cache.delete(clipId)
    }
    for (const [clipId, segment] of mediaByClipId) {
      let audio = cache.get(clipId)
      if (!audio) {
        audio = document.createElement('audio')
        audio.preload = 'auto'
        let authRetryPending = false
        let authRetryAttempted = false
        audio.addEventListener('error', () => {
          if (authRetryPending || authRetryAttempted) return
          authRetryPending = true
          authRetryAttempted = true
          void ensureDesktopSession(true).then(() => {
            audio!.src = segment.src
            audio!.load()
          }).catch(() => undefined).finally(() => { authRetryPending = false })
        })
        audio.src = segment.src
        audio.load()
        cache.set(clipId, audio)
      } else if (audio.src !== new URL(segment.src, window.location.href).href) {
        audio.pause()
        audio.src = segment.src
        audio.load()
      }
      audio.volume = Math.max(0, Math.min(1, segment.volume ?? 1))
      audio.muted = segment.muted === true
    }
  }, [timelineSegments])

  useEffect(() => {
    const cache = imageElementsRef.current
    const mediaByClipId = new Map(timelineSegments.filter((segment) => segment.type === 'image' && segment.src).map((segment) => [segment.clipId, segment.src]))
    for (const clipId of cache.keys()) if (!mediaByClipId.has(clipId)) cache.delete(clipId)
    for (const [clipId, src] of mediaByClipId) {
      const { entry, replaced } = replaceCachedImageMedia(cache, clipId, src, () => new Image())
      if (!replaced && entry.element.src) continue
      entry.element.decoding = 'async'
      let authRetryPending = false
      let authRetryAttempted = false
      entry.element.addEventListener('error', () => {
        if (authRetryPending || authRetryAttempted) return
        authRetryPending = true
        authRetryAttempted = true
        void ensureDesktopSession(true).then(() => { entry.element.src = src }).catch(() => undefined).finally(() => { authRetryPending = false })
      })
      entry.element.addEventListener('load', requestCanvasRender)
      entry.element.src = src
    }
  }, [requestCanvasRender, timelineSegments])

  useEffect(() => {
    const canvasElement = canvasRef.current
    if (!canvasElement) return
    const renderingContext = canvasElement.getContext('2d')
    if (!renderingContext) return
    const canvas = canvasElement
    const context = renderingContext

    let frame = 0
    let disposed = false
    const stopMedia = () => {
      for (const cachedVideo of videoElementsRef.current.values()) {
        if (!cachedVideo.element.paused) cachedVideo.element.pause()
      }
      syncTimelineAudioPlayback(timelineLayout, audioElementsRef.current, playheadRef.current, false)
    }
    function schedule() {
      if (disposed || frame || !shouldScheduleVideoCanvasFrame(isPlaying, document.visibilityState)) return
      frame = window.requestAnimationFrame(render)
    }
    function render() {
      frame = 0
      if (document.visibilityState === 'hidden') {
        stopMedia()
        return
      }
      const duration = timelineDuration(timelineLayout)
      let nextPlayhead = playheadRef.current
      if (isPlaying && duration > 0) {
        const now = performance.now()
        nextPlayhead = Math.min(duration, playbackStartPlayheadRef.current + (now - playbackStartRef.current) / 1000)
        const playbackEnded = nextPlayhead >= duration
        if (playbackEnded) setIsPlaying(false)
        playheadRef.current = nextPlayhead
        if (playbackEnded || now - lastPublishedPlayheadRef.current >= VIDEO_STUDIO_UI_PLAYHEAD_INTERVAL_MS) {
          lastPublishedPlayheadRef.current = now
          setPlayhead(nextPlayhead)
        }
      }

      context.fillStyle = 'black'
      context.fillRect(0, 0, canvas.width, canvas.height)
      const activeSegments = activeTimelineSegments(timelineLayout, nextPlayhead)
      const activeVisualSegments = activeSegments.filter((segment) => segment.type !== 'audio')
      syncTimelineAudioPlayback(timelineLayout, audioElementsRef.current, nextPlayhead, isPlaying)
      const activeLiveAnimation = activeVisualSegments.some((segment) => segment.clipId === liveAnimationPart?.id && Boolean(liveAnimationURL))
      if (activeVisualSegments.length === 0) {
        for (const cachedVideo of videoElementsRef.current.values()) {
          if (!cachedVideo.element.paused) cachedVideo.element.pause()
        }
        schedule()
        return
      }
      const activeClipIds = new Set(activeVisualSegments.map((segment) => segment.clipId))
      for (const [clipId, cachedVideo] of videoElementsRef.current.entries()) {
        if (!activeClipIds.has(clipId) && !cachedVideo.element.paused) cachedVideo.element.pause()
      }
      for (const [segmentIndex, segment] of activeVisualSegments.entries()) {
        const opacity = transitionPreviewOpacity(activeVisualSegments, segmentIndex, nextPlayhead)
        if (segment.type === 'image') {
          const image = imageElementsRef.current.get(segment.clipId)?.element
          if (!image?.complete || image.naturalWidth <= 0) continue
          const scale = Math.min(canvas.width / image.naturalWidth, canvas.height / image.naturalHeight)
          const drawWidth = Math.max(1, image.naturalWidth * scale)
          const drawHeight = Math.max(1, image.naturalHeight * scale)
          context.save()
          context.globalAlpha = opacity
          context.drawImage(image, (canvas.width - drawWidth) / 2, (canvas.height - drawHeight) / 2, drawWidth, drawHeight)
          context.restore()
          drawActiveCaptions(context, canvas, segment, nextPlayhead)
          continue
        }
        if (activeLiveAnimation && segment.clipId === liveAnimationPart?.id) {
          // The sandboxed iframe is composited above this canvas. Audio remains
          // synchronized here from the same Video Studio playhead.
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
        const video = videoElementsRef.current.get(segment.clipId)?.element
        if (!video) continue
        const sourceTime = segment.sourceStart + Math.max(0, nextPlayhead - segment.timelineStart)
        if (Number.isFinite(sourceTime) && Math.abs(video.currentTime - sourceTime) > 0.08) {
          try {
            video.currentTime = sourceTime
          } catch {
            // Browser may reject seeks before metadata is ready; a media event requests another draw.
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
        drawActiveCaptions(context, canvas, segment, nextPlayhead)
      }
      schedule()
    }
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'hidden') {
        if (frame) window.cancelAnimationFrame(frame)
        frame = 0
        stopMedia()
        return
      }
      playbackStartPlayheadRef.current = playheadRef.current
      playbackStartRef.current = performance.now()
      render()
    }

    document.addEventListener('visibilitychange', handleVisibilityChange)
    render()
    return () => {
      disposed = true
      if (frame) window.cancelAnimationFrame(frame)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  }, [canvasRenderVersion, isPlaying, liveAnimationPart?.id, liveAnimationURL, timelineLayout])

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

  const applyLibraryVideoSelection = useCallback((item: WorkspaceVideoCatalogItemWire, sessionId = item.source_session_id) => {
    const revision = selectWorkspaceVideoRevision(item)
    setSelectedLibraryVideoId(item.project.id)
    setSelectedThreadId(sessionId)
    setVideoProjects([item.project])
    setSelectedProjectId(item.project.id)
    setVideoProject(item.project)
    setCurrentRevision(revision)
    setConfirmedRevision(revision)
    setProjectRevisions(item.revisions)
    setPreviewRevisionId(null)
    setPlayhead(0)
  }, [])

  const handleSelectVideoSession = useCallback((sessionId: string) => {
    const normalizedSessionId = sessionId.trim()
    if (!normalizedSessionId) return
    const sessionVideos = workspaceVideosForSession(videoLibrary, normalizedSessionId)
    if (sessionVideos.length === 1) applyLibraryVideoSelection(sessionVideos[0], normalizedSessionId)
    else {
      setSelectedLibraryVideoId(null)
      setSelectedThreadId(normalizedSessionId)
    }
    const workspaceSlug = routeWorkspaceSlug
      || workspaceSlugByPath.get(selectedWorkspacePath)
      || workspaceRouteSlugBase({ path: selectedWorkspacePath, workspaceName: selectedWorkspaceName })
    if (!workspaceSlug) return
    void navigate({ to: '/$workspaceSlug/studio/$videoSessionId', params: { workspaceSlug, videoSessionId: normalizedSessionId } })
  }, [applyLibraryVideoSelection, navigate, routeWorkspaceSlug, selectedWorkspaceName, selectedWorkspacePath, videoLibrary, workspaceSlugByPath])

  const handleSelectLibraryVideo = useCallback((item: WorkspaceVideoCatalogItemWire) => {
    applyLibraryVideoSelection(item)
  }, [applyLibraryVideoSelection])

  const handleStartSessionFromLibrary = useCallback(async () => {
    if (!selectedLibraryVideo || !selectedLibraryRevision || !selectedSessionRoute || !selectedWorkspacePath || !selectedWorkspaceName) return
    setStartingFromLibrary(true)
    setCreateError(null)
    try {
      const createdThread = await createVideoThread({
        title: `${selectedLibraryVideo.project.title || 'Video'} continuation`,
        workspacePath: selectedWorkspacePath,
        workspaceName: selectedWorkspaceName,
        route: selectedSessionRoute,
        clips: [],
        metadata: workspaceVideoContextMetadata(selectedLibraryVideo, selectedLibraryRevision.id),
        beforeThreadCreate: async (destinationSessionId) => {
          await forkWorkspaceVideoRevision({
            workspacePath: selectedWorkspacePath,
            sourceSessionId: selectedLibraryVideo.source_session_id,
            sourceProjectId: selectedLibraryVideo.project.id,
            sourceRevisionId: selectedLibraryRevision.id,
            destinationSessionId,
            attachToSession: true,
          })
        },
      })
      queryClient.setQueryData<VideoThreadRecord[]>(['video-tool-threads', selectedWorkspacePath], (current = []) => [createdThread, ...current.filter((thread) => thread.id !== createdThread.id)])
      setSelectedLibraryVideoId(null)
      setSelectedThreadId(createdThread.id)
      handleSelectVideoSession(createdThread.id)
      void queryClient.invalidateQueries({ queryKey: ['video-library', selectedWorkspacePath] })
      void queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setStartingFromLibrary(false)
    }
  }, [handleSelectVideoSession, queryClient, selectedLibraryRevision, selectedLibraryVideo, selectedSessionRoute, selectedWorkspaceName, selectedWorkspacePath])

  const handleOpenSessionMode = useCallback(() => {
    if (!selectedThread || !routeWorkspaceSlug) return
    saveVideoSessionViewPreference(selectedThread.id, 'chat')
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
    setBrowserAudioClips([])
    setBrowserScanError(null)
    try {
      const next = await browseWorkspacePath(path)
      setBrowser(next)
      if (selectedWorkspacePath) {
        setBrowserScanLoading(true)
        try {
          const scanned = await scanVideoFolder(selectedWorkspacePath, next.resolvedPath)
          setBrowserClips(scanned.clips)
          setBrowserAudioClips(scanned.audioClips)
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
    setExportedPath('')
    try {
      const exported = await exportRenderedVideo(selectedThread.id, videoProject.id, exportPath.trim(), renderJob.id)
      setExportPath(exported.destination_path)
      setExportedPath(exported.destination_path)
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
      if (!videoProject || !renderRevision?.id) throw new Error('Save a timeline revision before rendering')
      if (renderBlockedByPendingProposal) throw new Error('Confirm or revise the pending Video Studio changes before final rendering')
      if (renderBlockedByIterations) throw new Error(`Lock in one variant for each clip with multiple iterations before rendering (${unresolvedIterationLockPartIDs.length} remaining)`)
      const job = await startVideoRender(selectedThread.id, videoProject.id, renderRevision.id)
      setRenderJob(job)
      const pollInterval = window.setInterval(async () => {
        try {
          const updated = await getVideoRenderJob(selectedThread.id, job.id)
          setRenderJob(updated)
          setRenderProgress(updated.progress)
          if (updated.status === 'ready' || updated.status === 'failed' || updated.status === 'cancelled') {
            window.clearInterval(pollInterval)
            setRendering(false)
            if (updated.status === 'ready') {
              setExportPath((current) => current.trim() || defaultRenderedVideoExportPath(selectedWorkspacePath, videoProject.title || selectedThread.title, updated.revision_number))
              setExportedPath('')
            }
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
  }, [renderBlockedByIterations, renderBlockedByPendingProposal, renderRevision, selectedThread, selectedWorkspacePath, unresolvedIterationLockPartIDs.length, videoProject])

  const handleOpenPicker = useCallback(() => {
    setCreateError(null)
    setBrowser(null)
    setBrowserError(null)
    setBrowserClips([])
    setBrowserAudioClips([])
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
    if (!selectedThread || selectedLibraryVideo) return
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
      setConfirmedRevision(revision)
      setProjectRevisions(await listVideoProjectRevisions(selectedThread.id, project.id))
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setReordering(false)
    }
  }, [currentRevision, selectedClips, selectedLibraryVideo, selectedThread, videoProject])

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

  const handlePreviewRevision = useCallback((revisionId: string | null) => {
    animationSelectionRequestSequenceRef.current += 1
    setAnimationSelectionBusyPartId(null)
    setActivePreviewIdentity(null)
    setPreviewRevisionId(revisionId)
    setPlayhead(0)
  }, [])

  const handleRestoreRevision = useCallback(async (revisionId: string) => {
    if (!selectedThread || !videoProject || revisionId === currentRevision?.id) return
    setRestoringRevisionId(revisionId)
    setCreateError(null)
    try {
      const restored = await restoreVideoProjectRevision(selectedThread.id, videoProject.id, revisionId)
      setVideoProject(restored.project)
      setCurrentRevision(restored.current_revision ?? null)
      setConfirmedRevision(restored.current_revision ?? null)
      setProjectRevisions(await listVideoProjectRevisions(selectedThread.id, videoProject.id))
      setPreviewRevisionId(null)
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
    beginTimelinePlayback(startAt)
  }, [beginTimelinePlayback, isPlaying, movieDuration, playhead])

  const handleSeek = useCallback((value: number) => {
    const next = Math.max(0, Math.min(movieDuration, value))
    playheadRef.current = next
    setPlayhead(next)
    playbackStartPlayheadRef.current = next
    playbackStartRef.current = performance.now()
    requestCanvasRender()
  }, [movieDuration, requestCanvasRender])

  const handleFocusStep = useCallback((clipId: string, playheadMs: number) => {
    const segment = timelineLayout.find((candidate) => candidate.id === clipId || candidate.clipId === clipId)
    setSelectedClipId(segment?.clipId ?? clipId)
    handleSeek(segment?.timelineStart ?? playheadMs / 1000)
  }, [handleSeek, timelineLayout])

  const handlePendingProposalChange = useCallback((proposal: VideoEditProposalWire | null, selectedChangeIds: string[]) => {
    setCompositionEditing(false)
    animationSelectionRequestSequenceRef.current += 1
    setAnimationSelectionBusyPartId(null)
    setActivePreviewIdentity(null)
    setPendingProposal(proposal)
    setPendingSelectedChangeIds(selectedChangeIds)
    if (proposal) setPreviewRevisionId(null)
  }, [])
  const handleConfirmWorkingCut = useCallback(async () => {
    if (!selectedThread || !videoProject || !currentWorkingProposal || !currentTurnCanConfirm || workingCutReviewBusy) return
    setWorkingCutReviewBusy(true)
    setCreateError(null)
    try {
      await acceptVideoEditProposal({
        sessionId: selectedThread.id,
        projectId: videoProject.id,
        proposalId: currentWorkingProposal.id,
        selectedOperationIds: currentTurnConfirmIDs,
        changeSummary: currentWorkingProposal.title || currentWorkingProposal.plan?.summary || currentWorkingProposal.rationale,
      })
      await refreshSelectedVideoProject()
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setWorkingCutReviewBusy(false)
    }
  }, [currentTurnCanConfirm, currentTurnConfirmIDs, currentWorkingProposal, refreshSelectedVideoProject, selectedThread, videoProject, workingCutReviewBusy])
  const handleReviseWorkingCut = useCallback(async () => {
    if (!selectedThread || !videoProject || !currentWorkingProposal || workingCutReviewBusy) return
    const feedback = `Restore the accepted parent of iteration ${currentWorkingProposal.id} and revise only the clips I describe: `
    setWorkingCutReviewBusy(true)
    setCreateError(null)
    try {
      await rejectVideoEditProposal(selectedThread.id, videoProject.id, currentWorkingProposal.id, feedback)
      setComposerDraftRequest({ id: Date.now(), draft: feedback })
      await refreshSelectedVideoProject()
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setWorkingCutReviewBusy(false)
    }
  }, [currentWorkingProposal, refreshSelectedVideoProject, selectedThread, videoProject, workingCutReviewBusy])
  const handleIterationFeedback = useCallback((message: string) => {
    setComposerDraftRequest({ id: Date.now(), draft: message })
  }, [])
  const handleAttachIterationChange = useCallback((context: VideoIterationComposerContext) => {
    setStudioComposerContext({
      revisionId: context.candidateRevisionId || context.parentRevisionId || currentRevision?.id || '',
      anchorClipId: context.anchorClipId,
      label: context.label,
      playheadMs: context.startMs,
      selectionKind: 'iteration',
      transition: null,
      iteration: context,
    })
    setStudioArtifactSelectionRequest(context.artifact ? {
      session_id: context.artifact.session_id,
      collection_id: context.artifact.collection_id,
      variant_id: context.artifact.variant_id,
      event_seq: context.artifact.event_seq,
      label: context.artifact.label || context.label,
      description: context.artifact.description,
      action: 'select',
    } : null)
  }, [currentRevision?.id])
  const handleStudioContextRemove = useCallback(() => setStudioComposerContext(null), [])
  const handleStudioArtifactSelectionHandled = useCallback(() => setStudioArtifactSelectionRequest(null), [])
  const handleStudioMessageSent = useCallback(() => {
    setStudioComposerContext(null)
  }, [])
  const studioRouteOptions = useMemo(() => selectedSessionRoute ? [selectedSessionRoute] : [], [selectedSessionRoute])
  const studioSidecarPlayheadMs = studioComposerContext?.playheadMs
    ?? Math.round((playhead * 1000) / VIDEO_STUDIO_SIDECAR_PLAYHEAD_INTERVAL_MS) * VIDEO_STUDIO_SIDECAR_PLAYHEAD_INTERVAL_MS
  const studioContextChip = useMemo(() => studioComposerContext ? {
    id: `${studioComposerContext.selectionKind}:${studioComposerContext.revisionId}:${studioComposerContext.anchorClipId}:${studioComposerContext.iteration?.changeId ?? studioComposerContext.transition?.id ?? ''}`,
    label: studioComposerContext.selectionKind === 'transition' ? `Transition · ${studioComposerContext.label}` : studioComposerContext.label,
    kind: studioComposerContext.selectionKind,
    description: studioComposerContext.iteration
      ? `Iteration ${studioComposerContext.iteration.proposalId} · change ${studioComposerContext.iteration.changeId} · ${studioComposerContext.iteration.startMs}–${studioComposerContext.iteration.endMs}ms${studioComposerContext.iteration.storyboard ? ` · storyboard ${studioComposerContext.iteration.storyboard.captureStateId} · ${studioComposerContext.iteration.storyboard.productionState}` : ''}`
      : studioComposerContext.storyboard
        ? `Stable storyboard part ${studioComposerContext.storyboard.partId} · capture ${studioComposerContext.storyboard.captureStateId} · ${studioComposerContext.storyboard.productionState}`
      : studioComposerContext.transition
        ? `${studioComposerContext.transition.kind}; ${studioComposerContext.transition.duration_ms ?? 0}ms`
        : `Stable part ${studioComposerContext.anchorClipId}`,
  } : null, [studioComposerContext])

  const submitSoundtrackProposal = useCallback(async (type: 'add_clip' | 'update_clip' | 'replace_clip' | 'remove_clip', clip?: VideoTimelineClipWire) => {
    if (!selectedThread || !videoProject || !currentRevision || pendingProposal || selectedLibraryVideo) return
    const target = clip ?? acceptedSoundtrack
    if (!target) return
    setSoundtrackProposalBusy(true)
    setCreateError(null)
    try {
      const rangeStart = Math.max(0, target.timeline_start_ms ?? 0)
      const rangeEnd = Math.max(rangeStart + 1, target.timeline_end_ms ?? (rangeStart + (target.duration_ms ?? 1)))
      await createVideoEditProposal({
        sessionId: selectedThread.id,
        projectId: videoProject.id,
        baseRevisionId: currentRevision.id,
        title: type === 'add_clip' ? `Add soundtrack · ${target.name || 'audio'}` : type === 'replace_clip' ? `Replace soundtrack · ${target.name || 'audio'}` : type === 'remove_clip' ? 'Remove soundtrack' : `Adjust soundtrack · ${target.name || 'audio'}`,
        rationale: 'Requested in Video Studio. This remains pending until explicitly confirmed.',
        operations: [{
          id: `${type}-${target.id}-${Date.now()}`,
          type,
          ...(type === 'remove_clip' ? { clip_id: target.id } : type === 'replace_clip' ? { clip_id: acceptedSoundtrack?.id ?? target.id, clip: target as unknown as Record<string, unknown> } : { clip: target as unknown as Record<string, unknown> }),
        }],
        affectedRanges: [{ start_ms: rangeStart, end_ms: rangeEnd }],
      })
      setSoundtrackDraft(null)
      setSoundtrackPickerOpen(false)
      setAIRefreshKey((value) => value + 1)
      await refreshSelectedVideoProject()
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setSoundtrackProposalBusy(false)
    }
  }, [acceptedSoundtrack, currentRevision, pendingProposal, refreshSelectedVideoProject, selectedLibraryVideo, selectedThread, videoProject])

  const handleSelectSoundtrack = useCallback((audio: AudioClip) => {
    const clip = soundtrackTimelineClip({ audio, durationMs: Math.max(1, Math.round(movieDuration * 1000)), existing: acceptedSoundtrack })
    setSoundtrackDraft(clip)
  }, [acceptedSoundtrack, movieDuration])

  const proposeCompositionChange = useCallback(async (catalog: VideoCompositionCatalogWire, link: VideoCompositionLinkWire, summary: string, productionState: 'pending' | 'ready') => {
    if (!selectedThread || !videoProject || !currentRevision || !activeCompositionPart || selectedLibraryVideo) return
    setCompositionProposalBusy(true)
    setCreateError(null)
    try {
      const workingProposal = currentWorkingProposal
      if (workingProposal?.plan && workingProposal.working_revision_id === currentRevision.id) {
        const plan: VideoPlanProposalWire = {
          ...workingProposal.plan,
          composition_catalog: catalog,
          parts: workingProposal.plan.parts.map((part) => part.id === activeCompositionPart.id ? { ...part, composition: link, production_state: productionState } : part),
        }
        const updated = await updateVideoCompositionProposal({ sessionId: selectedThread.id, projectId: videoProject.id, proposalId: workingProposal.id, expectedRevisionId: currentRevision.id, plan })
        setPendingProposal(updated)
        setProjectProposals((current) => current.map((proposal) => proposal.id === updated.id ? updated : proposal))
        setPendingSelectedChangeIds((current) => updated.plan?.kind === 'revision' ? current.filter((id) => updated.plan!.parts.some((part) => part.id === id)) : current)
      } else {
        const accepted = acceptedVideoPlan(currentRevision.timeline)
        const part = (accepted?.parts ?? playbackPlan?.parts ?? []).find((candidate) => candidate.id === activeCompositionPart.id) ?? activeCompositionPart
        const proposalPart = { ...part, composition: link, production_state: productionState }
        await createVideoEditProposal({
          sessionId: selectedThread.id,
          projectId: videoProject.id,
          baseRevisionId: currentRevision.id,
          title: summary,
          rationale: 'Spatial composition edited in Video Studio. This remains pending until explicitly confirmed.',
          plan: accepted
            ? { kind: 'revision', summary, composition_catalog: catalog, parts: accepted.parts.map((candidate) => candidate.id === proposalPart.id ? proposalPart : candidate) }
            : { kind: 'initial', summary, composition_catalog: catalog, parts: [proposalPart] },
          operations: [],
          affectedRanges: [{ start_ms: Math.round((activeSegment?.timelineStart ?? 0) * 1000), end_ms: Math.round((activeSegment?.timelineEnd ?? 0) * 1000) }],
        })
      }
      setCompositionEditing(false)
      setAIRefreshKey((value) => value + 1)
      await refreshSelectedVideoProject()
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setCompositionProposalBusy(false)
    }
  }, [activeCompositionPart, activeSegment?.timelineEnd, activeSegment?.timelineStart, currentRevision, currentWorkingProposal, playbackPlan?.parts, refreshSelectedVideoProject, selectedLibraryVideo, selectedThread, videoProject])

  const handleRequestStepEdit = useCallback((action: VideoStepEditAction, segment: TimelineLayoutSegment) => {
    if (!videoProject || !currentRevision) return
    setCreateError(null)
    const acceptedPart = acceptedPlan?.parts.find((part) => part.id === segment.id || part.id === segment.clipId)
    const acceptedTransition = keptRevision?.timeline.transitions?.find((transition) => transition.to_clip_id === segment.id || transition.to_clip_id === segment.clipId)
    const visualPart = acceptedPart ?? (segment.artifactRef?.collection_id && segment.artifactRef.variant_id && segment.artifactRef.event_seq
      ? {
          id: segment.id,
          title: segment.title || segment.id,
          duration_ms: Math.round(segment.duration * 1000),
          visual_media_type: segment.artifactRef.media_type,
          visual: {
            session_id: segment.artifactRef.session_id || selectedThread?.id || '',
            collection_id: segment.artifactRef.collection_id,
            variant_id: segment.artifactRef.variant_id,
            event_seq: segment.artifactRef.event_seq,
          },
        }
      : null)
    try {
      if (action === 'visual' || action === 'transition') {
        const storyboard = videoPlanPartStoryboardContext(acceptedPart)
        setStudioComposerContext({
          revisionId: confirmedRevision?.id ?? currentRevision.id,
          anchorClipId: segment.id,
          label: segment.title || segment.id,
          playheadMs: Math.round(segment.timelineStart * 1000),
          selectionKind: action,
          transition: action === 'transition' ? acceptedTransition ?? null : null,
          storyboard,
        })
        setStudioArtifactSelectionRequest(visualPart
          ? action === 'transition'
            ? videoPlanTransitionMessageSelection(visualPart, acceptedTransition)
            : videoPlanPartMessageSelection(visualPart)
          : null)
      } else {
        setStudioComposerContext(null)
        const draft = action === 'source' ? 'Change this source'
          : action === 'move_earlier' ? 'Move this step earlier'
            : 'Move this step later'
        setComposerDraftRequest({ id: Date.now(), draft })
      }
      handleFocusStep(segment.id, segment.timelineStart * 1000)
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    }
  }, [acceptedPlan, confirmedRevision, currentRevision, handleFocusStep, keptRevision, selectedThread?.id, videoProject])

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

  const videoLibraryNavigation = (
    <section className="border border-[color-mix(in_srgb,var(--app-primary)_38%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-primary)_6%,var(--app-surface))] p-3" aria-label="Standalone video library">
      <div className="flex items-start gap-2">
        <span className="grid h-8 w-8 shrink-0 place-items-center bg-[var(--app-primary)] text-[var(--app-primary-foreground)]"><Library size={15} /></span>
        <div className="min-w-0">
          <h2 className="text-[12px] font-semibold text-[var(--app-text)]">Browse video library</h2>
          <p className="mt-1 text-[10px] leading-4 text-[var(--app-text-muted)]">Choose any retained video and iteration. Then start a session with that exact cut already attached for AI.</p>
        </div>
        {videoLibraryQueryResult.isLoading ? <Loader2 size={12} className="ml-auto shrink-0 animate-spin" /> : null}
      </div>
      <label className="relative mt-3 block">
        <Search size={12} className="pointer-events-none absolute left-2.5 top-2.5 text-[var(--app-text-subtle)]" />
        <input value={videoLibraryQuery} onChange={(event) => setVideoLibraryQuery(event.target.value)} className="h-8 w-full border border-[var(--app-border)] bg-[var(--app-bg)] pl-7 pr-2 text-[11px]" placeholder="Filter videos or related sessions" aria-label="Filter video library" />
      </label>
      {videoLibraryQueryResult.isError ? <p className="py-2 text-[10px] text-red-400">{videoLibraryQueryResult.error instanceof Error ? videoLibraryQueryResult.error.message : 'Could not load video library.'}</p> : null}
      <select aria-label="Selected library video" className="mt-2 h-9 w-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-[11px] font-medium text-[var(--app-text)]" value={selectedLibraryVideoId ?? ''} onChange={(event) => { const item = videoLibrary.find((candidate) => candidate.project.id === event.currentTarget.value); if (item) handleSelectLibraryVideo(item) }}>
        <option value="">{filteredVideoLibrary.length === 0 ? (videoLibrary.length === 0 ? 'No retained videos' : 'No matching videos') : 'Select a video to open…'}</option>
        {filteredVideoLibrary.map((item) => <option key={`${item.source_session_id}:${item.project.id}`} value={item.project.id}>{item.project.title || 'Untitled video'}{item.source_archived ? ' · archived source' : ''}</option>)}
      </select>
      {!selectedLibraryVideo && selectedSessionVideos.length > 1 ? <div className="mt-3"><p className="text-[9px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Videos in this session</p><div className="mt-1 grid gap-1">{selectedSessionVideos.map((item) => <button key={item.project.id} type="button" className="truncate px-2 py-1 text-left text-[10px] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" onClick={() => handleSelectLibraryVideo(item)}>{item.project.title || 'Untitled video'} · {item.revisions.length} iteration{item.revisions.length === 1 ? '' : 's'}</button>)}</div></div> : null}
      {selectedLibraryVideo ? <div className="mt-3 border-t border-[var(--app-border)] pt-3">
        <p className="truncate text-[11px] font-semibold text-[var(--app-text)]">{selectedLibraryVideo.project.title || 'Untitled video'}</p>
        <p className="mt-1 text-[9px] text-[var(--app-text-subtle)]">{libraryReadOnly ? 'Archived source · remains read-only' : 'Retained video · source stays unchanged'}</p>
        <label className="mt-3 block text-[9px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Exact iteration
          <select aria-label="Selected video iteration" className="mt-1 h-8 w-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-[10px] normal-case tracking-normal text-[var(--app-text)]" value={selectedLibraryRevision?.id ?? ''} onChange={(event) => handlePreviewRevision(event.currentTarget.value || null)}>{[...selectedLibraryVideo.revisions].sort((left, right) => left.revision_number - right.revision_number).map((revision) => <option key={revision.id} value={revision.id}>r{revision.revision_number} · {revision.change_summary || revision.id}</option>)}</select>
        </label>
        <Button className="mt-3 h-auto min-h-9 w-full justify-center px-3 py-2 text-[11px]" onClick={() => void handleStartSessionFromLibrary()} disabled={!selectedLibraryRevision || !selectedSessionRoute || startingFromLibrary}>{startingFromLibrary ? <Loader2 size={12} className="animate-spin" /> : <MessageSquare size={12} />}Start new session with r{selectedLibraryRevision?.revision_number ?? 0} attached</Button>
        <p className="mt-2 text-[9px] leading-4 text-[var(--app-text-muted)]">The new session opens immediately, is placed first in Video sessions, and keeps this exact revision in durable AI context.</p>
        <div className="mt-3"><p className="text-[9px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Related sessions</p><div className="mt-1 grid gap-1">{selectedLibraryVideo.related_sessions.map((session) => <button key={session.session_id} type="button" className={`truncate px-2 py-1 text-left text-[10px] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] ${selectedThreadId === session.session_id ? 'bg-[var(--app-surface-active)] text-[var(--app-text)]' : ''}`} onClick={() => handleSelectVideoSession(session.session_id)}>{session.title || 'Video session'}{session.archived ? ' · archived' : ''}</button>)}</div></div>
      </div> : null}
    </section>
  )

  return (
    <div className="absolute inset-0 flex min-h-0 flex-col overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]">
        <header className="flex min-h-[60px] shrink-0 items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 pb-2 pt-[calc(var(--app-safe-area-top)+0.5rem)] sm:h-[60px] sm:px-4 sm:py-0">
          <div className="inline-flex min-w-0 items-center gap-2">
            <Film size={17} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
            <span className="truncate text-sm font-semibold">Video Studio</span>
            {selectedThread ? <span className="truncate text-xs text-[var(--app-text-muted)]">/ {selectedThread.title || 'Video session'}</span> : null}
          </div>
          {selectedThread && routeWorkspaceSlug ? (
            <button
              type="button"
              className="inline-flex h-9 shrink-0 items-center gap-1.5 rounded-xl border border-transparent bg-transparent px-2 text-xs font-medium text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
              onClick={handleOpenSessionMode}
              aria-label="Switch to session mode"
              aria-pressed="true"
              title="Video Studio is on. Switch back to the session chat."
              data-testid="video-studio-session-toggle"
            >
              <MessageSquare size={15} aria-hidden="true" />
              <span>Chat</span>
            </button>
          ) : null}
        </header>
        {createError ? (
          <div className="mb-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text)]">
            {createError}
          </div>
        ) : null}

        <main className="flex min-h-0 flex-1 flex-col overflow-y-auto overscroll-contain lg:flex-row lg:overflow-hidden">
          <div className="contents">
            <SwarmToolSidebar
              layoutClassName="flex max-h-[48dvh] min-h-0 w-full shrink-0 flex-col overflow-hidden border-b border-[var(--app-border)] px-3 py-2 font-mono text-[12px] text-[var(--app-text-muted)] lg:mr-5 lg:max-h-none lg:w-[300px] lg:flex-none lg:border-b-0 lg:border-r lg:px-0 lg:py-5 lg:pl-3 lg:pr-4"
              childrenClassName="mt-3 min-h-0 flex-1 overflow-y-auto"
              compactSelectedSession={Boolean(selectedThread && !selectedLibraryVideo)}
              prioritizeChildren={Boolean(selectedThread && !selectedLibraryVideo)}
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
                { id: 'add-folder', label: 'Add folder', icon: <FolderOpen size={14} />, suffix: 'source', onClick: handleOpenPicker, disabled: !selectedThread || Boolean(selectedLibraryVideo) },
                { id: 'show-files', label: revealingStorage ? 'Opening…' : 'Show files', icon: <FolderOpen size={14} />, suffix: 'local', onClick: () => void handleRevealVideoStorage(), disabled: !selectedThread || revealingStorage || Boolean(selectedLibraryVideo) },
                { id: 'session-mode', label: 'Open session mode', icon: <MessageSquare size={14} />, suffix: 'chat', onClick: handleOpenSessionMode, disabled: !selectedThread || !routeWorkspaceSlug || Boolean(selectedLibraryVideo) },
              ]}
              beforeSessions={videoLibraryNavigation}
            >
              {selectedThread ? (
                <div className="min-h-0">
                  <p className="mb-2 px-2 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Video workspace</p>
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
                  <div className="mt-3 border border-[var(--app-border)] bg-[var(--app-bg)] p-2">
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <p className="truncate text-[11px] font-medium text-[var(--app-text)]">{videoProject?.title || selectedThread.title || 'Video project'}</p>
                        <p className="mt-1 truncate text-[9px] text-[var(--app-text-subtle)]">{formatTimelineTime(movieDuration)} · {projectLoading ? 'loading revision' : activeTurnRevision ? `r${activeTurnRevision.revision_number}` : 'unsaved'}{currentWorkingProposal ? ' · pending turn changes' : ''}</p>
                      </div>
                      <button type="button" className="shrink-0 p-1 text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" onClick={() => void handleRevealVideoStorage()} disabled={revealingStorage} aria-label="Show stored files" title="Show stored files"><FolderOpen size={13} /></button>
                    </div>
                    <div className="mt-2 grid grid-cols-2 gap-2" aria-label="Video revision navigation">
                      <Button variant="outline" className="h-7 px-2 text-[10px]" disabled={activeTurnRevisionIndex <= 0} onClick={() => handlePreviewRevision(projectRevisions[activeTurnRevisionIndex - 1]?.id ?? null)}>← Previous revision</Button>
                      <Button variant="outline" className="h-7 px-2 text-[10px]" disabled={activeTurnRevisionIndex < 0 || activeTurnRevisionIndex >= projectRevisions.length - 1} onClick={() => handlePreviewRevision(projectRevisions[activeTurnRevisionIndex + 1]?.id ?? null)}>Next revision →</Button>
                    </div>
                  </div>

                  {videoProject && currentRevision && !selectedLibraryVideo ? <div className="hidden"><VideoIterationSidebar key={`${videoProject.id}:${aiRefreshKey}`} sessionId={selectedThread.id} projectId={videoProject.id} currentRevisionId={currentRevision.id} revisions={projectRevisions} onProposalsLoaded={setProjectProposals} onAccepted={refreshSelectedVideoProject} onFeedback={handleIterationFeedback} onPreviewProposal={handlePendingProposalChange} onPreviewRevision={handlePreviewRevision} onFocusChange={handleFocusStep} onAttachChange={handleAttachIterationChange} /></div> : null}
                  {previewRevision ? <div className="mt-2 grid gap-2 px-2"><p className="text-[10px] text-amber-300">Previewing r{previewRevision.revision_number}; kept r{keptRevision?.revision_number} is unchanged.</p><div className="grid grid-cols-2 gap-2"><Button variant="outline" className="h-7 px-2 text-[10px]" disabled={previewRevisionIndex <= 0} onClick={() => handlePreviewRevision(projectRevisions[previewRevisionIndex - 1].id)}>Previous</Button><Button variant="outline" className="h-7 px-2 text-[10px]" disabled={previewRevisionIndex < 0 || previewRevisionIndex >= projectRevisions.length - 1} onClick={() => handlePreviewRevision(projectRevisions[previewRevisionIndex + 1].id)}>Next</Button></div><Button variant="outline" className="h-7 px-2 text-[10px]" onClick={() => setPreviewRevisionId(null)}>Return to kept version</Button><Button className="h-7 px-2 text-[10px]" disabled={Boolean(restoringRevisionId) || Boolean(selectedLibraryVideo)} onClick={() => void handleRestoreRevision(previewRevision.id)}>{restoringRevisionId === previewRevision.id ? <Loader2 size={11} className="animate-spin" /> : <RotateCcw size={11} />}Restore as new version</Button></div> : null}

                  <p className="mb-2 mt-3 px-2 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Sources</p>
                  <div className="flex flex-col gap-1">
                    {timelineSegments.length === 0 ? <div className="px-2 py-3 text-[11px] text-[var(--app-text-subtle)]">No clips yet.</div> : timelineSegments.filter((segment) => segment.type !== 'audio').map((segment, index) => {
                      const clip = selectedClips.find((candidate) => candidate.id === segment.clipId)
                      const layoutSegment = timelineLayoutByClipId.get(segment.clipId)
                      const planPart = playbackPlan?.parts.find((part) => part.id === segment.clipId)
                      const reviewState = videoClipReviewState(planPart, segment.artifactRef?.media_type ?? '', segment.type)
                      return (
                        <button key={segment.id + '-sidebar'} type="button" onClick={() => { setSelectedClipId(segment.clipId); if (layoutSegment?.visible) handleSeek(layoutSegment.timelineStart) }} className={['grid w-full grid-cols-[24px_minmax(0,1fr)] gap-2 border-l-2 px-2 py-2 text-left hover:bg-[var(--app-surface-hover)]', selectedClipId === segment.clipId ? 'border-[var(--app-primary)] bg-[var(--app-surface-active)]' : 'border-transparent', segment.visible ? '' : 'opacity-55'].filter(Boolean).join(' ')}>
                          <span className="pt-0.5 text-[10px] text-[var(--app-text-subtle)]">{String(index + 1).padStart(2, '0')}</span>
                          <span className="min-w-0">
                            <span className="block truncate text-[11px] text-[var(--app-text)]">{planPart?.title || clip?.name || segment.clipId}</span>
                            <span className="mt-0.5 block truncate text-[9px] text-[var(--app-text-subtle)]">{formatTimelineTime(segment.duration)} · {reviewState.mediaKind}</span>
                            <span className={`mt-0.5 block truncate text-[9px] ${reviewState.state === 'Motion failed' ? 'text-red-400' : reviewState.state.startsWith('Motion') || reviewState.state === 'Choose motion' || reviewState.state.includes('filming') ? 'text-amber-300' : reviewState.state === 'Production ready' ? 'text-green-400' : 'text-[var(--app-text-muted)]'}`}>{reviewState.state}</span>
                            {planPart?.filming_requirements?.length ? <span className="mt-0.5 block truncate text-[9px] text-[var(--app-text-subtle)]">Film: {planPart.filming_requirements.join(' · ')}</span> : null}
                          </span>
                        </button>
                      )
                    })}
                  </div>
                </div>
              ) : null}
            </SwarmToolSidebar>
          </div>

            <section className="relative flex min-w-0 shrink-0 flex-col px-3 py-4 sm:px-4 lg:min-h-0 lg:flex-1 lg:shrink lg:overflow-y-auto lg:px-0 lg:py-0" aria-label="Video player and timeline">
              <div ref={setStudioArtifactReviewPortalTarget} className="absolute inset-0 z-20 min-h-0 min-w-0 empty:hidden" data-studio-artifact-review-host />
              <div className="mb-4 flex items-center justify-end gap-2 lg:hidden">
                <Button variant="outline" style={darkOverrideButtonStyle} className={`h-9 w-9 rounded-xl px-0 ${blackModeEnabled ? 'border-[var(--video-tool-user-theme-accent)] bg-[var(--video-tool-user-theme-surface)] text-[var(--video-tool-user-theme-text)] hover:bg-[var(--video-tool-user-theme-surface-hover)]' : ''}`} onClick={() => setBlackModeEnabled((enabled) => !enabled)} aria-label="Toggle dark mode override for this page" aria-pressed={blackModeEnabled} title="Toggle dark mode override for this page"><Moon size={14} aria-hidden="true" /></Button>
                <Button variant="outline" className="h-9 rounded-xl px-3 text-xs" onClick={handleOpenPicker} disabled={!selectedThread || Boolean(selectedLibraryVideo)}><FolderOpen size={14} />Add source</Button>
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
              <div className="relative w-full shrink-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-black lg:rounded-none" data-video-studio-player-viewport style={{ aspectRatio: `${(shadowTimeline ?? playerRevision?.timeline)?.width ?? 1920} / ${(shadowTimeline ?? playerRevision?.timeline)?.height ?? 1080}` }}>
                <canvas ref={canvasRef} width={(shadowTimeline ?? playerRevision?.timeline)?.width ?? 1920} height={(shadowTimeline ?? playerRevision?.timeline)?.height ?? 1080} className="absolute inset-0 h-full w-full bg-black object-contain" />
                {activeCompositionSlots.length > 0 && activeSegment ? <VideoCompositionOverlay slots={activeCompositionSlots} outputWidth={(shadowTimeline ?? playerRevision?.timeline)?.width ?? 1920} outputHeight={(shadowTimeline ?? playerRevision?.timeline)?.height ?? 1080} playheadMs={Math.round(playhead * 1000)} partStartMs={Math.round(activeSegment.timelineStart * 1000)} playing={isPlaying} sourceURL={(sourceRef) => selectedThread ? `/v3/sessions/${encodeURIComponent(selectedThread.id)}/video/sources/media?source_ref=${encodeURIComponent(sourceRef)}` : ''} editing={compositionEditing} /> : null}
                {liveAnimationPart && liveAnimationURL && !liveAnimationError ? <div className="absolute inset-0 overflow-hidden bg-black"><iframe ref={liveAnimationFrameRef} title={activeCandidate?.label || liveAnimationPart.title} src={liveAnimationURL} sandbox="allow-scripts" referrerPolicy="no-referrer" className="absolute inset-0 h-full w-full border-0 bg-black" data-video-studio-live-animation onError={() => setLiveAnimationError(`Could not play ${activeCandidate?.label || activeCandidate?.id || liveAnimationPart.title}.`)} /></div> : null}
                {liveAnimationPart && liveAnimationError ? <div className="absolute inset-0 z-10 grid place-items-center bg-black px-8 text-center"><div><p className="text-sm font-semibold text-red-300">Live HTML preview failed</p><p className="mt-2 max-w-xl text-xs leading-5 text-red-200/80">{liveAnimationError}</p><p className="mt-2 text-[10px] text-white/50">The still fallback is not substituted for the selected motion source.</p></div></div> : null}
                {previewRevision ? <div className="pointer-events-none absolute right-4 top-4 border border-sky-300/50 bg-sky-950/80 px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.16em] text-sky-200">History preview · r{previewRevision.revision_number} · kept r{confirmedRevision?.revision_number} unchanged</div> : currentWorkingProposal ? <div className="pointer-events-none absolute right-4 top-4 border border-amber-300/50 bg-amber-950/80 px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.16em] text-amber-200">Pending turn changes · working r{currentWorkingProposal.working_revision_number ?? currentRevision?.revision_number} · confirm when ready</div> : null}
                {timelineSegments.length === 0 ? (
                  <div className="absolute inset-0 grid place-items-center text-center"><div><Film className="mx-auto text-white/45" size={42} strokeWidth={1.5} /><p className="mt-3 text-sm font-medium text-white/80">No clips in this timeline</p></div></div>
                ) : null}
                <div className="pointer-events-none absolute left-4 top-4 rounded bg-black/55 px-2 py-1 text-xs text-white/70">
                  {activeSegment ? `Clip ${Math.max(1, visualTimelineLayout.findIndex((segment) => segment.clipId === activeSegment.clipId) + 1)} · ${liveAnimationPart?.title || activeSegment.title || selectedClip?.name || activeSegment.clipId} · ${activeCandidate?.label || activeClipReviewState.mediaKind} · ${liveAnimationPart ? 'Live HTML' : activeClipReviewState.mediaKind} · ${formatTimelineTime(playhead)} / ${formatTimelineTime(movieDuration)}` : 'Timeline player'}
                </div>
              </div>

              {currentWorkingProposal ? <div className="sticky top-0 z-10 mt-2 flex flex-wrap items-center justify-between gap-3 border border-amber-300/45 bg-amber-950/95 px-3 py-2 shadow-lg backdrop-blur" role="status" aria-label="Pending video confirmation">
                <div className="min-w-0"><p className="text-[10px] font-semibold uppercase tracking-[0.14em] text-amber-200">Pending changes are not final</p><p className="mt-0.5 text-[10px] text-amber-100/75">Review the working cut with its soundtrack, then confirm it before final rendering.</p></div>
                <Button className="h-8 shrink-0 px-3 text-[10px]" disabled={!currentTurnCanConfirm || workingCutReviewBusy} onClick={() => void handleConfirmWorkingCut()}>{workingCutReviewBusy ? <Loader2 size={12} className="animate-spin" /> : null}{currentTurnUnreadyAnimations.length > 0 ? 'Choose HTML motion first' : unresolvedTurnCompositionPartIDs.length > 0 ? 'Assign composition sources first' : pendingTurnProductionPartIDs.length > 0 ? 'Finish storyboard production first' : currentWorkingProposal.plan?.kind === 'initial' ? `Confirm all ${currentWorkingVisualLayout.length} clips` : `Confirm ${currentTurnConfirmIDs.length} selected change${currentTurnConfirmIDs.length === 1 ? '' : 's'}`}</Button>
              </div> : null}

              {activeCompositionPart?.composition && activeCompositionCatalog ? <><div className="mt-2 flex items-center justify-between border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2"><div><p className="text-[10px] font-medium">Spatial composition · {activeCompositionSlots.length} slot{activeCompositionSlots.length === 1 ? '' : 's'}</p><p className="text-[9px] text-[var(--app-text-muted)]">{activeCompositionPart.composition.detached ? 'Detached shot override' : `Linked layout ${activeCompositionPart.composition.layout_id}`} · {activeCompositionSlots.filter((slot) => !slot.source).length} unassigned · {currentWorkingProposal ? 'pending working cut' : 'accepted cut'}</p></div><Button variant="outline" className="h-7 px-2 text-[10px]" disabled={Boolean(selectedLibraryVideo) || Boolean(pendingProposal && !currentWorkingProposal?.plan)} onClick={() => setCompositionEditing((value) => !value)}>{compositionEditing ? 'Close composition editor' : 'Edit boxes'}</Button></div>{compositionEditing ? <VideoCompositionEditor catalog={activeCompositionCatalog} link={activeCompositionPart.composition} partTitle={activeCompositionPart.title} durationMs={activeCompositionPart.duration_ms} sources={selectedClips.map((clip) => ({ sourceRef: clip.sourceRef, name: clip.name, mediaType: 'video/mp4', durationMs: Math.round((clipDurations[clip.id] ?? activeCompositionPart.duration_ms / 1000) * 1000) }))} pending={Boolean(currentWorkingProposal)} productionState={activeCompositionPart.production_state} disabled={compositionProposalBusy || Boolean(pendingProposal && !currentWorkingProposal?.plan)} onPropose={proposeCompositionChange} /> : null}</> : null}

              {currentWorkingProposal ? <section className="order-2 mt-4 border border-[var(--app-border)] bg-[var(--app-surface)] p-3" aria-label="Current video turn">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0"><div className="flex items-center gap-2"><span className="rounded-full bg-[var(--app-primary)]/15 px-2 py-1 text-[10px] font-semibold uppercase tracking-[0.14em] text-[var(--app-primary)]">Working cut r{activeTurnRevision?.revision_number ?? 1}</span><span className="text-[10px] uppercase tracking-[0.12em] text-amber-300">Pending review</span></div><p className="mt-2 text-xs font-semibold text-[var(--app-text)]">What changed in this turn</p><p className="mt-1 max-w-4xl text-[11px] leading-5 text-[var(--app-text-muted)]">{currentTurnSummary}</p><p className="mt-1 text-[10px] text-[var(--app-text-subtle)]">{currentTurnParts.length} stable clips · {currentTurnVariantPartCount} with live variants · {currentTurnStaticFallbackCount} render-ready image fallbacks</p></div>
                  <div className="flex items-center gap-1"><Button variant="outline" className="h-7 px-2 text-[10px]" disabled={activeTurnRevisionIndex <= 0} onClick={() => handlePreviewRevision(projectRevisions[activeTurnRevisionIndex - 1]?.id ?? null)}>← Previous revision</Button><Button variant="outline" className="h-7 px-2 text-[10px]" disabled={activeTurnRevisionIndex < 0 || activeTurnRevisionIndex >= projectRevisions.length - 1} onClick={() => handlePreviewRevision(projectRevisions[activeTurnRevisionIndex + 1]?.id ?? null)}>Next revision →</Button></div>
                </div>
                {currentWorkingProposal.plan ? <div className="mt-3 grid gap-2 md:grid-cols-2" data-video-turn-parts>
                  {currentWorkingVisualLayout.map((segment, clipIndex) => {
                    const part = currentTurnPartByID.get(segment.clipId)
                    const candidates = part?.animation_candidates?.candidates ?? []
                    const selectedCandidate = candidates.find((candidate) => candidate.id === part?.animation_candidates?.selected_candidate_id)
                    const exactPreviewURL = segment.artifactRef?.session_id && segment.artifactRef.variant_id
                      ? `/v3/sessions/${encodeURIComponent(segment.artifactRef.session_id)}/artifacts/${encodeURIComponent(segment.artifactRef.variant_id)}`
                      : segment.src
                    const isProposed = Boolean(part)
                    const storyboard = videoPlanPartStoryboardContext(part)
                    const mediaStatus = storyboard
                      ? storyboard.productionState === 'pending' ? 'Storyboard placeholder · filming needed' : 'Storyboard section · production ready'
                      : part?.animation_candidates
                      ? part.animation_candidates.status === 'ready' ? 'Motion derivative ready' : videoAnimationReadyForConfirmation(part) ? 'Live HTML selected · ready to confirm' : part.animation_candidates.status === 'failed' ? `Animation failed · ${part.animation_candidates.failure_reason || 'fix before confirmation'}` : 'Choose one HTML motion variant'
                      : segment.type === 'image' ? isProposed ? 'Still image proposed' : 'Still image locked in'
                        : segment.type === 'video' ? isProposed ? 'Video clip proposed' : 'Video clip locked in'
                          : isProposed ? 'Clip proposed' : 'Clip locked in'
                    const preview = <div className="relative aspect-video overflow-hidden bg-black">{segment.type === 'video' && exactPreviewURL ? <video src={exactPreviewURL} muted playsInline preload="metadata" className="h-full w-full object-contain" /> : segment.type === 'image' && exactPreviewURL ? <img src={exactPreviewURL} alt="" className="h-full w-full object-contain" /> : <div className="grid h-full place-items-center px-4 text-center text-[10px] text-white/55">{mediaStatus}</div>}</div>
                    return <article key={segment.id} className={`overflow-hidden border ${activeSegment?.clipId === segment.clipId ? 'border-[var(--app-primary)] bg-[var(--app-primary)]/5' : 'border-[var(--app-border)] bg-[var(--app-bg)]'}`} aria-label={`Working clip ${clipIndex + 1}: ${part?.title || segment.title || segment.clipId}`}>
                      <button type="button" className="block w-full text-left" onClick={() => part ? selectedCandidate ? previewCurrentTurnPart(part, selectedCandidate) : previewCurrentTurnPart(part) : handleFocusStep(segment.clipId, Math.round(segment.timelineStart * 1000))}>{preview}</button>
                      <div className="p-3"><div className="flex flex-wrap items-start justify-between gap-2"><button type="button" className="min-w-0 flex-1 text-left" onClick={() => part ? selectedCandidate ? previewCurrentTurnPart(part, selectedCandidate) : previewCurrentTurnPart(part) : handleFocusStep(segment.clipId, Math.round(segment.timelineStart * 1000))}><span className="text-[9px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Clip {clipIndex + 1} · {formatTimelineTime(segment.timelineStart)}–{formatTimelineTime(segment.timelineEnd)} · {isProposed ? 'pending' : 'locked'}</span><span className="mt-1 block text-xs font-semibold text-[var(--app-text)]">{part?.title || segment.title || segment.clipId}</span><span className="mt-1 block text-[10px] leading-4 text-[var(--app-text-muted)]">{part?.visual_direction || part?.on_screen_text || mediaStatus}</span></button><Button variant="outline" className="h-7 shrink-0 px-2 text-[10px]" onClick={() => part ? selectedCandidate ? previewCurrentTurnPart(part, selectedCandidate) : previewCurrentTurnPart(part) : handleFocusStep(segment.clipId, Math.round(segment.timelineStart * 1000))}>Play clip + soundtrack</Button></div>
                      <p className={`mt-2 text-[9px] uppercase tracking-[0.12em] ${(part?.animation_candidates && part.animation_candidates.status !== 'ready') || storyboard?.productionState === 'pending' ? 'text-amber-300' : storyboard?.productionState === 'ready' ? 'text-green-400' : 'text-[var(--app-text-subtle)]'}`}>{mediaStatus}</p>
                      {storyboard ? <div className="mt-2 border-l-2 border-amber-300/60 pl-2 text-[10px] leading-4 text-[var(--app-text-muted)]"><p className="font-medium text-[var(--app-text)]">Filming guide</p><p>{storyboard.filmingRequirements.join(' · ')}</p><p className="mt-1 font-mono text-[9px] text-[var(--app-text-subtle)]">Stable part {storyboard.partId} · capture {storyboard.captureStateId}</p></div> : null}
                      {candidates.length > 0 ? <div className="mt-3"><p className="mb-2 text-[9px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">{candidates.length} variants for this clip · choose one to keep</p><div className="grid gap-2 sm:grid-cols-2">{candidates.map((candidate) => { const selected = part?.animation_candidates?.selected_candidate_id === candidate.id; const previewURL = iterationCardURLs[`${part?.id}:${candidate.id}`]; return <div key={candidate.id} className={`overflow-hidden rounded-lg border ${selected ? 'border-[var(--app-primary)] ring-1 ring-[var(--app-primary)]' : 'border-[var(--app-border)]'}`}><button type="button" className="block w-full text-left" onClick={() => part && previewCurrentTurnPart(part, candidate)}><div className="relative aspect-video overflow-hidden bg-black">{previewURL ? <iframe title={`${candidate.label || candidate.id} preview`} src={previewURL} sandbox="allow-scripts" referrerPolicy="no-referrer" tabIndex={-1} className="pointer-events-none absolute inset-0 h-full w-full border-0" /> : <div className="grid h-full place-items-center text-[10px] text-white/50">Loading preview…</div>}</div><div className="px-3 py-2"><p className="truncate text-[11px] font-semibold text-[var(--app-text)]">{candidate.label || candidate.id}</p><p className="mt-1 text-[9px] text-[var(--app-text-muted)]">Preview at this clip’s soundtrack position</p></div></button><Button className="h-8 w-full rounded-none text-[10px]" variant={selected ? 'primary' : 'outline'} disabled={selected || Boolean(animationSelectionBusyPartId) || part?.animation_candidates?.status === 'ready' || (currentWorkingProposal.plan?.kind === 'revision' && !pendingSelectedChangeIds.includes(part?.id ?? ''))} onClick={() => part && selectLiveAnimationCandidate(part, candidate)}>{selected ? '✓ Locked in for this clip' : animationSelectionBusyPartId === part?.id ? 'Locking in…' : currentWorkingProposal.plan?.kind === 'revision' && !pendingSelectedChangeIds.includes(part?.id ?? '') ? 'Enable clip to choose' : 'Lock in this variant'}</Button></div> })}</div></div> : null}
                      {currentWorkingProposal.plan?.kind === 'revision' && part ? <label className="mt-3 flex items-center gap-2 text-[10px] text-[var(--app-text-muted)]"><input type="checkbox" checked={pendingSelectedChangeIds.includes(part.id)} onChange={(event) => setPendingSelectedChangeIds((current) => event.target.checked ? Array.from(new Set([...current, part.id])) : current.filter((id) => id !== part.id))} />Keep this proposed clip in the confirmed cut</label> : null}
                    </div></article>
                  })}
                </div> : <div className="mt-3 grid gap-2" data-video-turn-operations>
                  {currentWorkingProposal.operations.map((operation) => <label key={operation.id} className="flex items-start gap-2 border border-[var(--app-border)] bg-[var(--app-bg)] p-2 text-[10px] text-[var(--app-text-muted)]"><input className="mt-0.5" type="checkbox" checked={pendingSelectedChangeIds.includes(operation.id)} onChange={(event) => setPendingSelectedChangeIds((current) => event.target.checked ? Array.from(new Set([...current, operation.id])) : current.filter((id) => id !== operation.id))} /><span><span className="block font-medium text-[var(--app-text)]">{operation.type.replace(/_/g, ' ')}</span><span className="mt-1 block">{String(operation.clip?.name || operation.clip?.id || operation.clip_id || operation.transition_id || 'Timeline change')}</span></span></label>)}
                </div>}
                <div className="mt-3 flex flex-wrap items-center justify-between gap-3 border-t border-[var(--app-border)] pt-3"><p className="max-w-3xl text-[10px] text-[var(--app-text-muted)]">{currentWorkingProposal.plan?.kind === 'initial' ? `First round: review all ${currentWorkingVisualLayout.length} clips, then confirm them once as the initial kept cut.` : currentWorkingProposal.plan ? `Review the complete ${currentWorkingVisualLayout.length}-clip working cut. Only checked pending clips are confirmed.` : `Review ${currentWorkingProposal.operations.length} pending timeline change${currentWorkingProposal.operations.length === 1 ? '' : 's'}. Confirmation updates the kept cut; rendering remains unavailable until then.`}{currentTurnUnreadyAnimations.length > 0 ? ` ${currentTurnUnreadyAnimations.length} HTML clip${currentTurnUnreadyAnimations.length === 1 ? '' : 's'} still require a selected live motion source before confirmation.` : ''}{unresolvedTurnCompositionPartIDs.length > 0 ? ` ${unresolvedTurnCompositionPartIDs.length} selected composition${unresolvedTurnCompositionPartIDs.length === 1 ? '' : 's'} still need exact video sources.` : ''}{pendingTurnProductionPartIDs.length > 0 ? ` ${pendingTurnProductionPartIDs.length} selected storyboard part${pendingTurnProductionPartIDs.length === 1 ? '' : 's'} remain pending production.` : ''}</p><div className="flex items-center gap-2"><Button variant="ghost" className="h-8 px-3 text-[10px]" disabled={workingCutReviewBusy} onClick={() => void handleReviseWorkingCut()}><RotateCcw size={12} />Revise this turn</Button><Button className="h-8 px-3 text-[10px]" disabled={!currentTurnCanConfirm || workingCutReviewBusy} onClick={() => void handleConfirmWorkingCut()}>{workingCutReviewBusy ? <Loader2 size={12} className="animate-spin" /> : null}{currentTurnUnreadyAnimations.length > 0 ? 'Choose HTML motion first' : unresolvedTurnCompositionPartIDs.length > 0 ? 'Assign composition sources first' : pendingTurnProductionPartIDs.length > 0 ? 'Finish storyboard production first' : currentWorkingProposal.plan?.kind === 'initial' ? `Confirm all ${currentWorkingVisualLayout.length} clips` : `Confirm ${currentTurnConfirmIDs.length} selected change${currentTurnConfirmIDs.length === 1 ? '' : 's'}`}</Button></div></div>
              </section> : null}

              <section className="order-1 mt-4" aria-label="Video timeline">
                <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
                  <div className="flex items-center gap-3">
                    <Button className="h-9 rounded-xl px-3" onClick={handleTogglePlayback} disabled={movieDuration <= 0}>{isPlaying ? <Pause size={15} /> : <Play size={15} />}{isPlaying ? 'Pause' : 'Play'}</Button>
                    <div className="text-xs tabular-nums text-[var(--app-text-muted)]">{formatTimelineTime(playhead)} / {formatTimelineTime(movieDuration)}</div>
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-[var(--app-text-muted)]">{visibleTimelineLayout.length} included · {hiddenTimelineLayout.length} hidden</span>
                    {rendering ? (
                      <Button variant="outline" className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleCancelRender()}>
                        <Loader2 size={13} className="animate-spin" /> {renderJob?.progress_stage || 'Rendering'} · {Math.round(renderProgress * 100)}% · Cancel
                      </Button>
                    ) : renderJob?.status === 'ready' ? (
                      <span className="text-xs text-green-500"><Film size={13} className="inline" /> Render ready for playback and export</span>
                    ) : (
                      <Button variant="outline" className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleStartRender()} disabled={movieDuration <= 0 || rendering || projectLoading || !renderRevision || renderBlockedByPendingProposal || renderBlockedByIterations || renderBlockedByStoryboard || renderBlockedByComposition || hasUnresolvedPlanFrames || Boolean(selectedLibraryVideo)}>
                        <Sparkles size={13} /> {renderBlockedByPendingProposal ? 'Confirm pending changes to render' : renderBlockedByIterations ? `Lock ${unresolvedIterationLockPartIDs.length} clip variant${unresolvedIterationLockPartIDs.length === 1 ? '' : 's'} to render` : renderBlockedByStoryboard ? `Replace ${pendingStoryboardPartIDs.length} storyboard placeholder${pendingStoryboardPartIDs.length === 1 ? '' : 's'} to render` : renderBlockedByComposition ? 'Assign all composition sources to render' : hasUnresolvedPlanFrames ? 'Replace planned frames with sources' : `Render r${renderRevision?.revision_number ?? ''}`}
                      </Button>
                    )}
                  </div>
                </div>
                {renderJob?.status === 'ready' ? (
                  <div className="mb-3 grid gap-2 border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                    {renderedVideoArtifactUrl(selectedThread.id, renderJob) ? <video controls preload="metadata" className="max-h-72 w-full bg-black" src={renderedVideoArtifactUrl(selectedThread.id, renderJob)} /> : <p className="text-xs text-[var(--app-text-muted)]">Rendered artifact is ready, but its exact output reference is unavailable.</p>}
                    <p className="text-[11px] text-[var(--app-text-muted)]">The finished render is already stored as a managed video artifact. Export copies it into this workspace:</p>
                    <div className="flex gap-2"><input aria-label="MP4 export destination" className="h-8 min-w-0 flex-1 border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-xs" value={exportPath} onChange={(event) => { setExportPath(event.target.value); setExportedPath('') }} placeholder="Workspace destination for MP4" /><Button className="h-8 px-3 text-xs" disabled={exporting || !exportPath.trim()} onClick={() => void handleExportRender()}>{exporting ? <Loader2 size={13} className="animate-spin" /> : <Download size={13} />}Export MP4</Button></div>
                    {exportedPath ? <p className="break-all text-[11px] text-emerald-400">Exported MP4 to {exportedPath}</p> : null}
                  </div>
                ) : null}
                {renderError ? (
                  <div className="mb-3 rounded-xl border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
                    Render error: {renderError}
                  </div>
                ) : null}

                <section className="mb-3 border border-[var(--app-border)] bg-[var(--app-surface)] p-3" aria-label="Soundtrack controls">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div className="flex min-w-0 items-center gap-2"><Music size={15} className="text-[var(--app-primary)]" /><div className="min-w-0"><p className="truncate text-xs font-medium text-[var(--app-text)]">{playbackSoundtrack?.title || playbackSoundtrack?.audioSource?.name || 'No soundtrack in this player revision'}</p><p className="text-[10px] text-[var(--app-text-muted)]">{playbackSoundtrack ? `${pendingProposal ? 'Playing pending working audio' : 'Playing confirmed audio'} · ${formatTimelineTime(playbackSoundtrack.timelineStart)} · ${formatTimelineTime(playbackSoundtrack.duration)} · ${Math.round((playbackSoundtrack.volume ?? 1) * 100)}%${playbackSoundtrack.muted ? ' · muted' : ''}` : 'Choose trusted local audio to create a pending proposal.'}</p></div></div>
                    <Button variant="outline" className="h-8 px-3 text-xs" disabled={!currentRevision || Boolean(pendingProposal) || soundtrackProposalBusy} onClick={() => { setSoundtrackDraft(acceptedSoundtrack); setSoundtrackPickerOpen(true); if (!browser && !browserLoading) void loadBrowser(selectedWorkspacePath || '') }}>{acceptedSoundtrack ? 'Replace' : 'Choose audio'}</Button>
                  </div>
                  {acceptedSoundtrack ? <div className="mt-3 grid gap-3 sm:grid-cols-[1fr_1fr_1.4fr_auto_auto] sm:items-end">
                    <label className="text-[10px] uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">Start (s)<input aria-label="Soundtrack timeline start seconds" type="number" min="0" step="0.1" className="mt-1 h-8 w-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-xs" value={(soundtrackDraft ?? acceptedSoundtrack).timeline_start_ms! / 1000} onChange={(event) => { const draft = { ...(soundtrackDraft ?? acceptedSoundtrack) }; const start = Math.max(0, Number(event.target.value) || 0) * 1000; draft.timeline_start_ms = Math.round(start); draft.timeline_end_ms = Math.round(start + (draft.duration_ms ?? 1)); setSoundtrackDraft(draft) }} /></label>
                    <label className="text-[10px] uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">Trim in (s)<input aria-label="Soundtrack source trim seconds" type="number" min="0" step="0.1" className="mt-1 h-8 w-full border border-[var(--app-border)] bg-[var(--app-bg)] px-2 text-xs" value={(soundtrackDraft ?? acceptedSoundtrack).source_start_ms! / 1000} onChange={(event) => { const draft = { ...(soundtrackDraft ?? acceptedSoundtrack) }; const start = Math.max(0, Number(event.target.value) || 0) * 1000; draft.source_start_ms = Math.round(start); draft.source_end_ms = Math.round(start + (draft.duration_ms ?? 1)); setSoundtrackDraft(draft) }} /></label>
                    <label className="text-[10px] uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">Volume <input aria-label="Soundtrack volume" type="range" min="0" max="2" step="0.05" className="mt-2 w-full" value={(soundtrackDraft ?? acceptedSoundtrack).volume ?? 1} onChange={(event) => setSoundtrackDraft({ ...(soundtrackDraft ?? acceptedSoundtrack), volume: Number(event.target.value) })} /></label>
                    <Button variant="outline" className="h-8 px-2 text-xs" disabled={Boolean(pendingProposal) || soundtrackProposalBusy} onClick={() => setSoundtrackDraft({ ...(soundtrackDraft ?? acceptedSoundtrack), muted: !(soundtrackDraft ?? acceptedSoundtrack).muted })}>{(soundtrackDraft ?? acceptedSoundtrack).muted ? <VolumeX size={13} /> : <Volume2 size={13} />}{(soundtrackDraft ?? acceptedSoundtrack).muted ? 'Muted' : 'Mute'}</Button>
                    <Button variant="ghost" className="h-8 px-2 text-xs text-red-400" disabled={Boolean(pendingProposal) || soundtrackProposalBusy} onClick={() => void submitSoundtrackProposal('remove_clip')}><Trash2 size={13} />Remove</Button>
                    {soundtrackDraft && JSON.stringify(soundtrackDraft) !== JSON.stringify(acceptedSoundtrack) ? <Button className="h-8 px-3 text-xs sm:col-span-5" disabled={Boolean(pendingProposal) || soundtrackProposalBusy} onClick={() => void submitSoundtrackProposal('update_clip', soundtrackDraft)}>Propose placement / trim / volume change</Button> : null}
                  </div> : null}
                </section>
                <div className="mb-3 flex flex-wrap items-center gap-2 border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
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
                          const storyboardPart = visibleStoryboardPartByID.get(segment.clipId)
                          const left = movieDuration > 0 ? (segment.timelineStart / movieDuration) * timelineTrackWidthPx : 0
                          const width = movieDuration > 0 ? (segment.duration / movieDuration) * timelineTrackWidthPx : 0
                          return (
                            <button key={segment.id} type="button" onClick={(event) => { setSelectedClipId(segment.clipId); if (event.detail === 0) handleSeek(segment.timelineStart) }} className={`absolute top-0 h-full overflow-hidden border-r border-black/30 text-left transition hover:bg-[color-mix(in_srgb,var(--app-primary)_10%,var(--app-surface))] ${currentWorkingProposal ? 'bg-amber-950/30' : 'bg-[var(--app-surface)]'} ${selectedClipId === segment.clipId ? 'outline outline-1 outline-[var(--app-primary)]' : ''}`} style={{ left: `${left}px`, width: `${Math.max(1, width)}px` }}>
                              <div className="flex h-full flex-col justify-between px-2 py-2"><div className="flex items-center justify-between gap-2 text-[10px] text-[var(--app-text-subtle)]"><span>{String(visibleIndex + 1).padStart(2, '0')}</span><span className="tabular-nums">{formatTimelineTime(segment.duration)}</span></div><div className="min-w-0"><p className="truncate text-xs font-medium text-[var(--app-text)]">{storyboardPart?.title || clip?.name || segment.clipId}</p><p className={`truncate text-[10px] ${storyboardPart?.production_state === 'pending' ? 'text-amber-300' : 'text-[var(--app-text-muted)]'}`}>{storyboardPart ? storyboardPart.production_state === 'pending' ? 'Storyboard · film this section' : 'Storyboard · ready' : `${formatTimelineTime(segment.timelineStart)} – ${formatTimelineTime(segment.timelineEnd)}`}</p></div></div>
                            </button>
                          )
                        })}
                        <div className="pointer-events-none absolute top-0 h-full w-0.5 bg-[var(--app-primary)] shadow-[0_0_0_1px_rgba(0,0,0,0.35)]" style={{ left: `${playheadX}px` }} />
                      </div>
                      <div className="absolute inset-x-0 top-32 h-10 border border-violet-400/30 bg-violet-950/20" aria-label="Soundtrack lane">
                        {audioTimelineLayout.length === 0 ? <div className="flex h-full items-center px-3 text-[10px] uppercase tracking-[0.14em] text-violet-300/70"><Music size={12} className="mr-2" />Soundtrack lane · empty</div> : audioTimelineLayout.map((segment) => {
                          const left = movieDuration > 0 ? (segment.timelineStart / movieDuration) * timelineTrackWidthPx : 0
                          const width = movieDuration > 0 ? (segment.duration / movieDuration) * timelineTrackWidthPx : 0
                          return <div key={`${segment.id}-audio-lane`} className={`absolute inset-y-0 overflow-hidden border-r border-violet-300/30 px-2 py-1 text-violet-100 ${currentWorkingProposal ? 'bg-amber-900/45' : 'bg-violet-700/40'}`} style={{ left: `${left}px`, width: `${Math.max(2, width)}px` }}><p className="truncate text-[10px] font-medium">{segment.title || segment.audioSource?.name || 'Soundtrack'}</p><p className="truncate text-[9px] opacity-70">{segment.muted ? 'muted' : `${Math.round((segment.volume ?? 1) * 100)}%`} · {formatTimelineTime(segment.duration)}</p></div>
                        })}
                      </div>
                      <div className="absolute inset-x-0 top-44 flex items-center gap-2">
                        {visualTimelineLayout.map((segment, index) => {
                          const clip = selectedClips.find((candidate) => candidate.id === segment.clipId)
                          const storyboardPart = visibleStoryboardPartByID.get(segment.clipId)
                          return (
                            <div key={`${segment.id}-controls`} className={`flex min-w-[280px] max-w-[440px] flex-wrap items-center gap-2 border px-2 py-2 text-xs sm:min-w-[320px] ${segment.visible ? 'border-[var(--app-border)] bg-[var(--app-surface)]' : 'border-dashed border-[var(--app-border)] bg-transparent opacity-60'}`}>
                              <button type="button" onClick={() => handleFocusStep(segment.id, segment.timelineStart * 1000)} className="min-w-[160px] flex-1 text-left"><span className="block truncate font-medium text-[var(--app-text)]">{segment.title || clip?.name || segment.clipId}</span><span className="block truncate font-mono text-[10px] text-[var(--app-text-muted)]">{storyboardPart ? storyboardPart.production_state === 'pending' ? 'Storyboard placeholder · filming needed' : 'Production ready' : currentWorkingProposal ? 'Pending turn change' : segment.visible ? 'Included' : 'Hidden'} · {segment.id} · {formatTimelineTime(segment.duration)}</span>{storyboardPart?.filming_requirements?.length ? <span className="block max-w-[320px] truncate text-[10px] text-amber-300">Film: {storyboardPart.filming_requirements.join(' · ')}</span> : null}</button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => handleRequestStepEdit('visual', segment)}>Visual</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => handleRequestStepEdit('transition', segment)}>Transition</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => handleRequestStepEdit('source', segment)}>Source</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => void handleRequestStepEdit('move_earlier', segment)} disabled={index === 0}>AI ←</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-[10px]" onClick={() => void handleRequestStepEdit('move_later', segment)} disabled={index === visualTimelineLayout.length - 1}>AI →</Button>
                              <Button variant={segment.visible ? 'outline' : 'ghost'} className="h-7 rounded-lg px-2 text-xs" onClick={() => void handleToggleSegment(segment.clipId)} disabled={reordering || Boolean(pendingProposal) || Boolean(selectedLibraryVideo)}>{segment.visible ? <Eye size={13} /> : <EyeOff size={13} />}</Button>
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  </div>
                </div>
              </section>

              {currentRevision?.timeline.clips?.some((clip) => clip.source_kind === 'text') ? (
                <section className="order-3 mt-6 border border-[var(--app-border)] bg-[var(--app-surface)] p-4" aria-label="Still production plan">
                  <div className="mb-4 flex items-start justify-between gap-4">
                    <div>
                      <div className="flex items-center gap-2"><Sparkles size={16} className="text-[var(--app-primary)]" /><h2 className="text-sm font-semibold text-[var(--app-text)]">Still production plan</h2></div>
                      <p className="mt-1 text-xs text-[var(--app-text-muted)]">Review timing, narration, on-screen text, and frame direction before generating any stills.</p>
                    </div>
                    <span className="shrink-0 border border-[var(--app-border)] bg-[var(--app-bg)] px-2 py-1 text-[10px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">Pre-production</span>
                  </div>
                  <div className="grid gap-3 xl:grid-cols-2">
                    {(currentRevision.timeline.clips ?? []).filter((clip) => clip.source_kind === 'text').map((clip) => {
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

              <section className="order-3 mt-6">
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
            {selectedThread && !selectedLibraryVideo ? <VideoSessionAISidecar key={selectedThread.id} sessionId={selectedThread.id} projectId={videoProject?.id} revisionId={studioComposerContext?.revisionId ?? currentRevision?.id} anchorClipId={studioComposerContext?.anchorClipId ?? activeSegment?.id} playheadMs={studioSidecarPlayheadMs} selectionKind={studioComposerContext?.selectionKind} transition={studioComposerContext?.transition} iterationContext={studioComposerContext?.iteration} storyboardContext={studioComposerContext?.storyboard} routeOptions={studioRouteOptions} draftRequest={composerDraftRequest} artifactSelectionRequest={studioArtifactSelectionRequest} artifactReviewPortalTarget={studioArtifactReviewPortalTarget} contextChip={studioContextChip} onContextChipRemove={handleStudioContextRemove} onArtifactSelectionRequestHandled={handleStudioArtifactSelectionHandled} onMessageSent={handleStudioMessageSent} /> : null}
          </main>

      {soundtrackPickerOpen ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Choose trusted soundtrack" className="z-[85] p-4 sm:p-6">
          <DialogBackdrop onClick={() => setSoundtrackPickerOpen(false)} />
          <DialogPanel className="mx-auto mt-[8vh] flex max-h-[80vh] w-[min(760px,calc(100vw-24px))] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] shadow-[var(--shadow-panel)]">
            <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4"><div><p className="text-[11px] uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">Trusted local audio</p><h2 className="mt-1 text-xl font-semibold text-[var(--app-text)]">Choose soundtrack</h2><p className="mt-2 text-sm text-[var(--app-text-muted)]">Audio is discovered only from registered source-media folders and stays an exact authenticated reference.</p></div><ModalCloseButton onClick={() => setSoundtrackPickerOpen(false)} aria-label="Close soundtrack picker" /></div>
            <div className="overflow-y-auto p-5">
              <Button variant="outline" className="mb-4 h-8 px-3 text-xs" onClick={() => void loadBrowser(browser?.resolvedPath ?? selectedWorkspacePath)} disabled={browserLoading}>{browserLoading ? <Loader2 size={13} className="animate-spin" /> : <FolderOpen size={13} />}Scan selected folder</Button>
              {browserScanError ? <p className="mb-3 text-sm text-red-400">{browserScanError}</p> : null}
              {browserAudioClips.length === 0 ? <p className="border border-dashed border-[var(--app-border)] p-5 text-sm text-[var(--app-text-muted)]">No supported audio in the selected folder. Use Add folder to browse another registered source-media folder.</p> : <div className="grid gap-2">{browserAudioClips.map((audio) => <button key={audio.ref} type="button" onClick={() => handleSelectSoundtrack(audio)} className={`flex items-center gap-3 border px-3 py-3 text-left ${soundtrackDraft?.audio_source?.ref === audio.ref ? 'border-[var(--app-primary)] bg-[color-mix(in_srgb,var(--app-primary)_8%,transparent)]' : 'border-[var(--app-border)] bg-[var(--app-bg)]'}`}><Music size={16} className="text-violet-300" /><span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium">{audio.name}</span><span className="block truncate text-xs text-[var(--app-text-muted)]">{audio.mime_type} · {formatBytes(audio.size_bytes)}</span></span></button>)}</div>}
              {soundtrackDraft?.audio_source ? <Button className="mt-4 w-full" disabled={soundtrackProposalBusy || Boolean(pendingProposal) || movieDuration <= 0} onClick={() => void submitSoundtrackProposal(acceptedSoundtrack ? 'replace_clip' : 'add_clip', soundtrackDraft)}>{soundtrackProposalBusy ? <Loader2 size={13} className="animate-spin" /> : <Music size={13} />}Create pending {acceptedSoundtrack ? 'replacement' : 'soundtrack'} proposal</Button> : null}
            </div>
          </DialogPanel>
        </Dialog>
      ) : null}

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
