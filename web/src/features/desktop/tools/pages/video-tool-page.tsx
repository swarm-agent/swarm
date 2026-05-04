import { type CSSProperties, type PointerEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Eye, EyeOff, Film, FolderOpen, ListVideo, Loader2, Moon, Pause, Play, Sparkles } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { requestJson } from '../../../../app/api'
import { useDesktopStore } from '../../state/use-desktop-store'
import { createSession, fetchDraftModelPreference } from '../../chat/queries/chat-queries'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import { uiSettingsQueryOptions } from '../../../queries/query-options'
import { normalizeGlobalThemeSettings } from '../../settings/swarm/types/swarm-settings'
import { browseWorkspacePath } from '../../../workspaces/launcher/queries/browse-workspace-path'
import { resolveWorkspaceBySlug } from '../../../workspaces/launcher/services/workspace-route'
import { applyWorkspaceTheme, createWorkspaceThemeStyle } from '../../../workspaces/launcher/services/workspace-theme'
import type { WorkspaceBrowseResult, WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { SwarmToolSidebar } from '../components/swarm-tool-sidebar'

export type VideoClip = {
  id: string
  name: string
  path: string
  extension: string
  sizeBytes: number
  modifiedAt: number
}

type VideoClipWire = {
  id?: string
  name?: string
  path?: string
  extension?: string
  size_bytes?: number
  sizeBytes?: number
  modified_at?: number
  modifiedAt?: number
}

type VideoClipRequest = {
  id: string
  name: string
  path: string
  extension: string
  size_bytes: number
  modified_at: number
}

type VideoThreadRecord = {
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

type TimelineSegment = {
  id: string
  type: 'video'
  clipId: string
  src: string
  start: number
  sourceStart: number
  duration: number
  visible: boolean
}

type TimelineLayoutSegment = TimelineSegment & {
  timelineStart: number
  timelineEnd: number
}

const TIMELINE_METADATA_KEY = 'timelineSegments'
const VIDEO_TOOL_BLACK_MODE_STORAGE_KEY = 'swarm.videoTool.blackMode'
const DEFAULT_VIDEO_SESSION_TITLE = 'Swarm launch video'

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
  const id = String(entry.id ?? '').trim()
  const name = String(entry.name ?? '').trim()
  const path = String(entry.path ?? '').trim()
  if (!id || !name || !path) {
    return null
  }
  return {
    id,
    name,
    path,
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
    path: clip.path,
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

function roundTimelineTime(value: number): number {
  return Number.isFinite(value) && value > 0 ? Math.round(value * 1000) / 1000 : 0
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

function layoutTimelineSegments(segments: TimelineSegment[]): TimelineLayoutSegment[] {
  let timelineStart = 0
  return segments.map((segment) => {
    if (!segment.visible || segment.duration <= 0) {
      return { ...segment, start: timelineStart, timelineStart, timelineEnd: timelineStart }
    }
    const laidOut = { ...segment, start: timelineStart, timelineStart, timelineEnd: timelineStart + segment.duration }
    timelineStart = laidOut.timelineEnd
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

function activeTimelineSegment(layout: TimelineLayoutSegment[], playhead: number): TimelineLayoutSegment | null {
  const visible = layout.filter((segment) => segment.visible && segment.duration > 0 && segment.timelineEnd > segment.timelineStart)
  if (visible.length === 0) {
    return null
  }
  return visible.find((segment) => playhead >= segment.timelineStart && playhead < segment.timelineEnd) ?? visible[visible.length - 1] ?? null
}

function serializeTimelineSegments(segments: TimelineSegment[]): TimelineSegment[] {
  let start = 0
  return segments.map((segment) => {
    const serialized = {
      id: segment.id,
      type: 'video' as const,
      clipId: segment.clipId,
      src: segment.src,
      start: roundTimelineTime(start),
      sourceStart: roundTimelineTime(segment.sourceStart),
      duration: roundTimelineTime(segment.duration),
      visible: segment.visible,
    }
    if (segment.visible) {
      start += segment.duration
    }
    return serialized
  })
}

function timelineMetadataMatches(thread: VideoThreadRecord, segments: TimelineSegment[]): boolean {
  const existing = Array.isArray(thread.metadata?.[TIMELINE_METADATA_KEY])
    ? thread.metadata?.[TIMELINE_METADATA_KEY] as unknown[]
    : []
  const next = serializeTimelineSegments(segments)
  if (existing.length !== next.length) {
    return false
  }
  return next.every((segment, index) => {
    const current = existing[index]
    if (!isRecord(current)) {
      return false
    }
    return String(current.id ?? '').trim() === segment.id
      && String(current.clipId ?? current.clip_id ?? '').trim() === segment.clipId
      && Math.abs(finiteNonNegative(current.start, -1) - segment.start) < 0.001
      && Math.abs(finiteNonNegative(current.sourceStart, -1) - segment.sourceStart) < 0.001
      && Math.abs(finiteNonNegative(current.duration, -1) - segment.duration) < 0.001
      && (current.visible !== false) === segment.visible
  })
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

async function createVideoThread(input: {
  title: string
  workspacePath: string
  workspaceName: string
  folderPath?: string
  clips: VideoClip[]
}): Promise<VideoThreadRecord> {
  const response = await requestJson<{ thread?: VideoThreadWire }>('/v1/workspace/video/threads', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
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

async function updateVideoThreadTimeline(thread: VideoThreadRecord, segments: TimelineSegment[]): Promise<VideoThreadRecord> {
  return updateVideoThread({
    ...thread,
    videoClipOrder: segments.map((segment) => segment.clipId),
    metadata: {
      ...(thread.metadata ?? {}),
      [TIMELINE_METADATA_KEY]: serializeTimelineSegments(segments),
    },
  })
}

function moveItem<T>(items: T[], fromIndex: number, toIndex: number): T[] {
  if (fromIndex < 0 || toIndex < 0 || fromIndex >= items.length || toIndex >= items.length || fromIndex === toIndex) {
    return items
  }
  const next = [...items]
  const [moved] = next.splice(fromIndex, 1)
  next.splice(toIndex, 0, moved)
  return next
}

export function VideoToolPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const matchRoute = useMatchRoute()
  const workspaceVideoToolMatch = matchRoute({ to: '/$workspaceSlug/tools/video', fuzzy: false })
  const routeWorkspaceSlug = workspaceVideoToolMatch ? workspaceVideoToolMatch.workspaceSlug.trim() : ''
  const activeSessionId = useDesktopStore((state) => state.activeSessionId)
  const activeWorkspacePath = useDesktopStore((state) => state.activeWorkspacePath)
  const setActiveSession = useDesktopStore((state) => state.setActiveSession)
  const setActiveWorkspacePath = useDesktopStore((state) => state.setActiveWorkspacePath)
  const upsertSession = useDesktopStore((state) => state.upsertSession)

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
  const [selectedThreadId, setSelectedThreadId] = useState<string | null>(null)
  const [selectedClipId, setSelectedClipId] = useState<string | null>(null)
  const [reordering, setReordering] = useState(false)
  const [startingChat, setStartingChat] = useState(false)
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
  const playheadRef = useRef(0)
  const playbackStartRef = useRef(0)
  const playbackStartPlayheadRef = useRef(0)

  const workspacesQuery = useQuery({
    queryKey: ['video-tool-workspaces'],
    queryFn: () => listWorkspaces(200),
    staleTime: 30_000,
  })
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())
  const workspaces = workspacesQuery.data ?? []

  const selectedWorkspace = useMemo<WorkspaceEntry | null>(() => {
    if (routeWorkspaceSlug) {
      return resolveWorkspaceBySlug(workspaces, routeWorkspaceSlug)
    }
    if (activeWorkspacePath) {
      return workspaces.find((workspace) => workspace.path === activeWorkspacePath) ?? null
    }
    return workspaces[0] ?? null
  }, [activeWorkspacePath, routeWorkspaceSlug, workspaces])

  const selectedWorkspacePath = selectedWorkspace?.path ?? ''
  const selectedWorkspaceName = selectedWorkspace?.workspaceName ?? ''
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
    if (!selectedThreadId) {
      return
    }
    if (!videoThreads.some((thread) => thread.id === selectedThreadId)) {
      setSelectedThreadId(null)
    }
  }, [selectedThreadId, videoThreads])

  const selectedThread = useMemo(() => {
    if (!selectedThreadId) {
      return null
    }
    return videoThreads.find((thread) => thread.id === selectedThreadId) ?? null
  }, [selectedThreadId, videoThreads])

  const selectedClips = useMemo(() => orderedClips(selectedThread), [selectedThread])
  const selectedFolderPath = threadFolderPath(selectedThread)
  const timelineSegments = useMemo(() => buildTimelineSegments(selectedThread, selectedClips, clipDurations), [clipDurations, selectedClips, selectedThread])
  const timelineLayout = useMemo(() => layoutTimelineSegments(timelineSegments), [timelineSegments])
  const timelineLayoutByClipId = useMemo(() => new Map(timelineLayout.map((segment) => [segment.clipId, segment])), [timelineLayout])
  const visibleTimelineLayout = useMemo(() => timelineLayout.filter((segment) => segment.visible && segment.duration > 0), [timelineLayout])
  const hiddenTimelineLayout = useMemo(() => timelineLayout.filter((segment) => !segment.visible), [timelineLayout])
  const movieDuration = useMemo(() => timelineDuration(timelineLayout), [timelineLayout])
  const timelineTrackWidthPx = useMemo(() => timelineTrackWidth(movieDuration), [movieDuration])
  const playheadX = movieDuration > 0 ? Math.min(timelineTrackWidthPx, Math.max(0, (playhead / movieDuration) * timelineTrackWidthPx)) : 0
  const activeSegment = useMemo(() => activeTimelineSegment(timelineLayout, playhead), [playhead, timelineLayout])
  const selectedClip = selectedClips.find((clip) => clip.id === selectedClipId) ?? selectedClips[0] ?? null

  useEffect(() => {
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(VIDEO_TOOL_BLACK_MODE_STORAGE_KEY, blackModeEnabled ? 'true' : 'false')
    }
    applyWorkspaceTheme(blackModeEnabled ? 'black' : userThemeId)
  }, [blackModeEnabled, userThemeId])

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
    const activeClipIds = new Set(selectedClips.map((clip) => clip.id))
    for (const [clipId, video] of cache.entries()) {
      if (!activeClipIds.has(clipId)) {
        video.pause()
        video.removeAttribute('src')
        video.load()
        cache.delete(clipId)
      }
    }
    setClipDurations((current) => {
      const next = Object.fromEntries(Object.entries(current).filter(([clipId]) => activeClipIds.has(clipId)))
      return Object.keys(next).length === Object.keys(current).length ? current : next
    })
    for (const clip of selectedClips) {
      if (cache.has(clip.id) || !selectedThread) {
        continue
      }
      const video = document.createElement('video')
      video.src = clipMediaUrl(selectedThread.id, clip.id)
      video.preload = 'metadata'
      video.muted = true
      video.playsInline = true
      const updateDuration = () => {
        const duration = video.duration
        if (!Number.isFinite(duration) || duration <= 0) {
          return
        }
        setClipDurations((current) => {
          if (Math.abs((current[clip.id] ?? 0) - duration) < 0.001) {
            return current
          }
          return { ...current, [clip.id]: duration }
        })
      }
      video.addEventListener('loadedmetadata', updateDuration)
      video.addEventListener('durationchange', updateDuration)
      video.load()
      cache.set(clip.id, video)
    }
  }, [selectedClips, selectedThread])

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
      const segment = activeTimelineSegment(timelineLayout, nextPlayhead)
      if (!segment) {
        for (const cachedVideo of videoElementsRef.current.values()) {
          if (!cachedVideo.paused) {
            cachedVideo.pause()
          }
        }
        frame = window.requestAnimationFrame(render)
        return
      }
      for (const [clipId, cachedVideo] of videoElementsRef.current.entries()) {
        if (clipId !== segment.clipId && !cachedVideo.paused) {
          cachedVideo.pause()
        }
      }
      const video = videoElementsRef.current.get(segment.clipId)
      if (!video) {
        frame = window.requestAnimationFrame(render)
        return
      }
      const sourceTime = segment.sourceStart + Math.max(0, nextPlayhead - segment.timelineStart)
      if (Number.isFinite(sourceTime) && Math.abs(video.currentTime - sourceTime) > 0.08) {
        try {
          video.currentTime = sourceTime
        } catch {
          // Browser may reject seeks before metadata is ready; the next frame retries.
        }
      }
      if (isPlaying && video.paused) {
        void video.play().catch(() => undefined)
      }
      if (!isPlaying && !video.paused) {
        video.pause()
      }
      if (video.readyState >= HTMLMediaElement.HAVE_CURRENT_DATA) {
        const scale = Math.min(canvas.width / Math.max(1, video.videoWidth), canvas.height / Math.max(1, video.videoHeight))
        const drawWidth = Math.max(1, video.videoWidth * scale)
        const drawHeight = Math.max(1, video.videoHeight * scale)
        context.drawImage(video, (canvas.width - drawWidth) / 2, (canvas.height - drawHeight) / 2, drawWidth, drawHeight)
      }
      frame = window.requestAnimationFrame(render)
    }
    frame = window.requestAnimationFrame(render)
    return () => window.cancelAnimationFrame(frame)
  }, [isPlaying, timelineLayout])

  const handleBackToWorkspace = useMemo(() => {
    if (routeWorkspaceSlug) {
      if (activeSessionId) {
        return () => {
          void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug: routeWorkspaceSlug, sessionId: activeSessionId } })
        }
      }
      return () => {
        void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug } })
      }
    }
    return () => {
      void navigate({ to: '/' })
    }
  }, [activeSessionId, navigate, routeWorkspaceSlug])

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
    const title = newSessionTitle.trim() || DEFAULT_VIDEO_SESSION_TITLE
    setCreatingBlankSession(true)
    setCreateError(null)
    try {
      const createdThread = await createVideoThread({
        title,
        workspacePath: selectedWorkspacePath,
        workspaceName: selectedWorkspaceName,
        clips: [],
      })
      queryClient.setQueryData<VideoThreadRecord[]>(['video-tool-threads', selectedWorkspacePath], (current = []) => {
        const withoutCreated = current.filter((thread) => thread.id !== createdThread.id)
        return [createdThread, ...withoutCreated]
      })
      setSelectedThreadId(createdThread.id)
      setSelectedClipId(null)
      setNewSessionTitle('')
      await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setCreatingBlankSession(false)
    }
  }, [newSessionTitle, queryClient, selectedWorkspaceName, selectedWorkspacePath])

  const handleAddFolder = useCallback(async (folderPath: string) => {
    if (!selectedWorkspacePath || !selectedWorkspaceName) {
      setCreateError('Select a workspace before starting a video session.')
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
        const existingClipPaths = new Set(selectedThread.videoClips.map((clip) => clip.path))
        const clipsToAdd = scanned.clips.filter((clip) => !existingClipIds.has(clip.id) && !existingClipPaths.has(clip.path))
        const orderedExistingClipIds = orderedClips(selectedThread).map((clip) => clip.id)
        const updatedThread = await updateVideoThread({
          ...selectedThread,
          videoFolders: Array.from(folderSet),
          videoClips: [...selectedThread.videoClips, ...clipsToAdd],
          videoClipOrder: [...orderedExistingClipIds, ...clipsToAdd.map((clip) => clip.id)],
          metadata,
        })
        queryClient.setQueryData<VideoThreadRecord[]>(['video-tool-threads', selectedWorkspacePath], (current = []) => current.map((thread) => thread.id === updatedThread.id ? updatedThread : thread))
        setSelectedThreadId(updatedThread.id)
        setSelectedClipId(clipsToAdd[0]?.id ?? updatedThread.videoClipOrder[0] ?? updatedThread.videoClips[0]?.id ?? null)
        setPickerOpen(false)
        await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
        return
      }

      const createdThread = await createVideoThread({
        title: videoSessionTitle(scanned.folderPath),
        workspacePath: selectedWorkspacePath,
        workspaceName: selectedWorkspaceName,
        folderPath: scanned.folderPath,
        clips: scanned.clips,
      })
      queryClient.setQueryData<VideoThreadRecord[]>(['video-tool-threads', selectedWorkspacePath], (current = []) => {
        const withoutCreated = current.filter((thread) => thread.id !== createdThread.id)
        return [createdThread, ...withoutCreated]
      })
      setSelectedThreadId(createdThread.id)
      setSelectedClipId(createdThread.videoClipOrder[0] ?? createdThread.videoClips[0]?.id ?? null)
      setPickerOpen(false)
      await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setAddingFolderPath(null)
    }
  }, [queryClient, selectedThread, selectedWorkspaceName, selectedWorkspacePath])

  const persistTimelineSegments = useCallback(async (segments: TimelineSegment[], options?: { silent?: boolean }) => {
    if (!selectedThread) {
      return
    }
    if (!options?.silent) {
      setReordering(true)
    }
    try {
      const updatedThread = await updateVideoThreadTimeline(selectedThread, segments)
      queryClient.setQueryData<VideoThreadRecord[]>(['video-tool-threads', selectedWorkspacePath], (current = []) => current.map((thread) => thread.id === updatedThread.id ? updatedThread : thread))
      setSelectedThreadId(updatedThread.id)
      await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      if (!options?.silent) {
        setReordering(false)
      }
    }
  }, [queryClient, selectedThread, selectedWorkspacePath])

  useEffect(() => {
    if (!selectedThread || timelineSegments.length === 0) {
      return
    }
    if (selectedClips.some((clip) => clipDuration(clipDurations, clip.id) <= 0)) {
      return
    }
    if (timelineMetadataMatches(selectedThread, timelineSegments)) {
      return
    }
    void persistTimelineSegments(timelineSegments, { silent: true })
  }, [clipDurations, persistTimelineSegments, selectedClips, selectedThread, timelineSegments])

  const handleMoveClip = useCallback(async (direction: -1 | 1, clipId: string) => {
    const index = timelineSegments.findIndex((segment) => segment.clipId === clipId)
    const nextIndex = index + direction
    if (index < 0 || nextIndex < 0 || nextIndex >= timelineSegments.length) {
      return
    }
    const reordered = moveItem(timelineSegments, index, nextIndex)
    await persistTimelineSegments(reordered)
  }, [persistTimelineSegments, timelineSegments])

  const handleToggleSegment = useCallback(async (clipId: string) => {
    const next = timelineSegments.map((segment) => segment.clipId === clipId ? { ...segment, visible: !segment.visible } : segment)
    await persistTimelineSegments(next)
  }, [persistTimelineSegments, timelineSegments])

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

  const handleStartChat = useCallback(async () => {
    if (!selectedThread || !routeWorkspaceSlug) {
      return
    }
    setStartingChat(true)
    setCreateError(null)
    try {
      const preference = await fetchDraftModelPreference()
      const childSession = await createSession({
        title: `${selectedThread.title || 'Video'} chat`,
        workspacePath: selectedThread.workspacePath,
        workspaceName: selectedThread.workspaceName || selectedWorkspaceName,
        mode: 'auto',
        preference: preference.preference,
        metadata: {
          parent_video_thread_id: selectedThread.id,
          parent_title: selectedThread.title,
          parent_folder_path: selectedFolderPath,
          video_thread_id: selectedThread.id,
          video_folder_path: selectedFolderPath,
          video_clip_order: selectedClips.map((clip) => clip.id),
          video_clip_count: selectedClips.length,
          lineage_kind: 'video_child',
          launch_source: 'video_tool',
        },
      })
      upsertSession(childSession)
      setActiveSession(childSession.id)
      setActiveWorkspacePath(childSession.workspacePath || selectedThread.workspacePath)
      void navigate({
        to: '/$workspaceSlug/$sessionId',
        params: { workspaceSlug: routeWorkspaceSlug, sessionId: childSession.id },
      })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setStartingChat(false)
    }
  }, [navigate, routeWorkspaceSlug, selectedClips, selectedFolderPath, selectedThread, selectedWorkspaceName, setActiveSession, setActiveWorkspacePath, upsertSession])
  return (
    <div className="absolute inset-0 overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]">
      <div className="mx-auto flex h-full w-full max-w-none flex-col px-4 py-4 sm:px-5 sm:py-5">
        {createError ? (
          <div className="mb-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text)]">
            {createError}
          </div>
        ) : null}

        <main className="flex min-h-0 flex-1 overflow-hidden py-5">
            <SwarmToolSidebar
              backLabel={routeWorkspaceSlug ? (activeSessionId ? 'Back to chat' : 'Workspace') : 'Launcher'}
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
              onCreateTitleChange={setNewSessionTitle}
              createPlaceholder={DEFAULT_VIDEO_SESSION_TITLE}
              onCreate={() => void handleCreateBlankSession()}
              creating={creatingBlankSession}
              createDisabled={!selectedWorkspacePath}
              sessionsLabel="Video sessions"
              sessionsLoading={videoThreadsQuery.isLoading}
              sessions={videoThreads.map((thread) => ({
                id: thread.id,
                title: thread.title || 'Video Thread',
                subtitle: String(thread.videoClips.length) + ' clip' + (thread.videoClips.length === 1 ? '' : 's') + ' · ' + formatStartedAt(thread.createdAt),
              }))}
              selectedSessionId={selectedThread?.id ?? null}
              onSelectSession={setSelectedThreadId}
              emptySessionsMessage="No video sessions yet. Start session to get started."
              defaultSessionTitle="Video Thread"
              actions={[
                { id: 'add-folder', label: 'Add folder', icon: <FolderOpen size={14} />, suffix: 'source', onClick: handleOpenPicker, disabled: !selectedThread },
                { id: 'start-chat', label: 'Start chat', icon: <Sparkles size={14} />, suffix: 'child', onClick: () => void handleStartChat(), disabled: !selectedThread || startingChat || !routeWorkspaceSlug },
              ]}
            >
              {selectedThread ? (
                <div className="mt-4 min-h-0 flex-1 overflow-y-auto">
                  <p className="mb-2 px-2 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Current movie</p>
                  <div className="border border-[var(--app-border)] bg-[var(--app-bg)] p-3">
                    <h2 className="truncate text-sm font-semibold text-[var(--app-text)]">{selectedThread.title || 'Video thread'}</h2>
                    <p className="mt-2 break-all text-[11px] leading-5 text-[var(--app-text-subtle)]">{selectedFolderPath || 'No source folder yet'}</p>
                    <div className="mt-4 grid grid-cols-2 gap-2 text-[11px]">
                      <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Length</div><div className="mt-1 tabular-nums text-[var(--app-text)]">{formatTimelineTime(movieDuration)}</div></div>
                      <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-2"><div className="text-[10px] uppercase text-[var(--app-text-subtle)]">Clips</div><div className="mt-1 text-[var(--app-text)]">{visibleTimelineLayout.length}/{timelineSegments.length}</div></div>
                    </div>
                  </div>

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
                <Button variant="ghost" className="h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={handleBackToWorkspace}><ArrowLeft size={15} />{routeWorkspaceSlug ? (activeSessionId ? 'Back to chat' : 'Workspace') : 'Launcher'}</Button>
                <div className="flex items-center gap-2">
                  <Button variant="outline" style={darkOverrideButtonStyle} className={`h-8 w-8 rounded-xl px-0 ${blackModeEnabled ? 'border-[var(--video-tool-user-theme-accent)] bg-[var(--video-tool-user-theme-surface)] text-[var(--video-tool-user-theme-text)] hover:bg-[var(--video-tool-user-theme-surface-hover)]' : ''}`} onClick={() => setBlackModeEnabled((enabled) => !enabled)} aria-label="Toggle dark mode override for this page" aria-pressed={blackModeEnabled} title="Toggle dark mode override for this page"><Moon size={14} aria-hidden="true" /></Button>
                  <Button variant="ghost" className="h-8 rounded-xl px-2 text-xs text-[var(--app-text-muted)]" onClick={handleOpenPicker} disabled={!selectedThread}><FolderOpen size={14} />Add folder</Button>
                  <Button className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleStartChat()} disabled={!selectedThread || startingChat || !routeWorkspaceSlug}><Sparkles size={14} />{startingChat ? 'Starting…' : 'Start chat'}</Button>
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
                  <span className="text-xs text-[var(--app-text-muted)]">{visibleTimelineLayout.length} included · {hiddenTimelineLayout.length} hidden</span>
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
                            <button key={segment.id} type="button" onPointerDown={(event) => event.stopPropagation()} onClick={() => { setSelectedClipId(segment.clipId); handleSeek(segment.timelineStart) }} className={`absolute top-0 h-full overflow-hidden border-r border-black/30 bg-[var(--app-surface)] text-left transition hover:bg-[color-mix(in_srgb,var(--app-primary)_10%,var(--app-surface))] ${selectedClip?.id === segment.clipId ? 'outline outline-1 outline-[var(--app-primary)]' : ''}`} style={{ left: `${left}px`, width: `${Math.max(1, width)}px` }}>
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
                            <div key={`${segment.id}-controls`} className={`flex min-w-[190px] max-w-[280px] items-center gap-2 border px-2 py-2 text-xs ${segment.visible ? 'border-[var(--app-border)] bg-[var(--app-surface)]' : 'border-dashed border-[var(--app-border)] bg-transparent opacity-60'}`}>
                              <button type="button" onClick={() => { setSelectedClipId(segment.clipId); if (segment.visible) handleSeek(segment.timelineStart) }} className="min-w-0 flex-1 text-left"><span className="block truncate font-medium text-[var(--app-text)]">{clip?.name ?? segment.clipId}</span><span className="block text-[10px] text-[var(--app-text-muted)]">{segment.visible ? 'Included' : 'Hidden'} · {formatTimelineTime(segment.duration)}</span></button>
                              <Button variant={segment.visible ? 'outline' : 'ghost'} className="h-7 rounded-lg px-2 text-xs" onClick={() => void handleToggleSegment(segment.clipId)} disabled={reordering}>{segment.visible ? <Eye size={13} /> : <EyeOff size={13} />}</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-xs" onClick={() => void handleMoveClip(-1, segment.clipId)} disabled={reordering || index === 0}>←</Button>
                              <Button variant="outline" className="h-7 rounded-lg px-2 text-xs" onClick={() => void handleMoveClip(1, segment.clipId)} disabled={reordering || index === timelineSegments.length - 1}>→</Button>
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  </div>
                </div>
              </section>

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
                            <div className="truncate text-xs text-[var(--app-text-subtle)]">{clip.path}</div>
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
