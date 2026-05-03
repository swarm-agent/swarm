import { useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Film, FolderOpen, GripVertical, ListVideo, Loader2, Music2, Play, Sparkles } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { requestJson } from '../../../../app/api'
import { useDesktopStore } from '../../state/use-desktop-store'
import { createSession, fetchDraftModelPreference } from '../../chat/queries/chat-queries'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import { browseWorkspacePath } from '../../../workspaces/launcher/queries/browse-workspace-path'
import { resolveWorkspaceBySlug } from '../../../workspaces/launcher/services/workspace-route'
import type { WorkspaceBrowseResult, WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'

const waveformBars = [28, 44, 36, 58, 42, 68, 52, 74, 46, 62, 38, 55, 70, 48, 32, 60, 45, 66, 40, 54, 34, 50, 64, 42]

type VideoClip = {
  id: string
  name: string
  path: string
  extension: string
  sizeBytes: number
  modifiedAt: number
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
  clips?: VideoClip[]
}

type VideoThreadWire = {
  id?: string
  title?: string
  workspace_path?: string
  workspace_name?: string
  video_folders?: string[]
  video_clips?: VideoClip[]
  video_clip_order?: string[]
  metadata?: Record<string, unknown>
  created_at?: number
  updated_at?: number
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object'
}

function metadataStringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.map((entry) => String(entry ?? '').trim()).filter(Boolean)
    : []
}

function metadataClips(value: unknown): VideoClip[] {
  if (!Array.isArray(value)) {
    return []
  }
  return value
    .map((entry) => {
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
      } satisfies VideoClip
    })
    .filter((entry): entry is VideoClip => Boolean(entry))
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

function clipDurationLabel(clip: VideoClip): string {
  if (clip.sizeBytes <= 0) {
    return 'clip'
  }
  const pseudoSeconds = Math.max(6, Math.min(40, Math.round(clip.sizeBytes / 1024 / 1024)))
  return `${pseudoSeconds}s`
}

function clipWidth(clip: VideoClip): string {
  if (clip.sizeBytes <= 0) {
    return '24%'
  }
  const width = Math.max(18, Math.min(48, Math.round(clip.sizeBytes / 1024 / 1024)))
  return `${width}%`
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
    clips: Array.isArray(response.clips) ? response.clips : [],
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
  folderPath: string
  clips: VideoClip[]
}): Promise<VideoThreadRecord> {
  const response = await requestJson<{ thread?: VideoThreadWire }>('/v1/workspace/video/threads', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      title: input.title,
      workspace_path: input.workspacePath,
      workspace_name: input.workspaceName,
      video_folders: [input.folderPath],
      video_clips: input.clips,
      video_clip_order: input.clips.map((clip) => clip.id),
    }),
  })
  const thread = mapVideoThread(response.thread ?? {})
  if (!thread) {
    throw new Error('Video thread create returned no thread')
  }
  return thread
}

async function updateVideoThreadOrder(thread: VideoThreadRecord, clips: VideoClip[]): Promise<VideoThreadRecord> {
  const response = await requestJson<{ thread?: VideoThreadWire }>(`/v1/workspace/video/threads/${encodeURIComponent(thread.id)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      video_folders: thread.videoFolders,
      video_clips: clips,
      video_clip_order: clips.map((clip) => clip.id),
      metadata: thread.metadata,
    }),
  })
  const updated = mapVideoThread(response.thread ?? {})
  if (!updated) {
    throw new Error('Video thread update returned no thread')
  }
  return updated
}

function moveClip(items: VideoClip[], fromIndex: number, toIndex: number): VideoClip[] {
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
  const [selectedThreadId, setSelectedThreadId] = useState<string | null>(null)
  const [reordering, setReordering] = useState(false)
  const [startingChat, setStartingChat] = useState(false)

  const workspacesQuery = useQuery({
    queryKey: ['video-tool-workspaces'],
    queryFn: () => listWorkspaces(200),
    staleTime: 30_000,
  })
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
  const primaryClip = selectedClips[0] ?? null

  const handleBack = useMemo(() => {
    if (selectedThread) {
      return () => setSelectedThreadId(null)
    }
    if (routeWorkspaceSlug) {
      return () => {
        void navigate({ to: '/$workspaceSlug/tools', params: { workspaceSlug: routeWorkspaceSlug } })
      }
    }
    return () => {
      void navigate({ to: '/tools' })
    }
  }, [navigate, routeWorkspaceSlug, selectedThread])

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
      setPickerOpen(false)
      await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setAddingFolderPath(null)
    }
  }, [queryClient, selectedWorkspaceName, selectedWorkspacePath])

  const handleMoveClip = useCallback(async (direction: -1 | 1, clipId: string) => {
    if (!selectedThread) {
      return
    }
    const current = orderedClips(selectedThread)
    const index = current.findIndex((clip) => clip.id === clipId)
    const nextIndex = index + direction
    if (index < 0 || nextIndex < 0 || nextIndex >= current.length) {
      return
    }
    const reordered = moveClip(current, index, nextIndex)
    setReordering(true)
    try {
      const updatedThread = await updateVideoThreadOrder(selectedThread, reordered)
      queryClient.setQueryData<VideoThreadRecord[]>(['video-tool-threads', selectedWorkspacePath], (current = []) => current.map((thread) => thread.id === updatedThread.id ? updatedThread : thread))
      setSelectedThreadId(updatedThread.id)
      await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setReordering(false)
    }
  }, [queryClient, selectedThread, selectedWorkspacePath])

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
    <div className="absolute inset-0 overflow-y-auto bg-[var(--app-bg)] text-[var(--app-text)]">
      <div className="mx-auto flex min-h-full w-full max-w-7xl flex-col px-6 py-6 sm:px-8 sm:py-8">
        <header className="flex flex-col gap-5 border-b border-[var(--app-border)] pb-6 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <Button variant="ghost" className="mb-5 h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={handleBack}>
              <ArrowLeft size={15} />
              {selectedThread ? 'Back to add folder' : 'Back to tools'}
            </Button>
            <div className="flex items-center gap-3">
              <span className="grid h-11 w-11 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)] shadow-sm">
                <Film size={20} strokeWidth={1.8} />
              </span>
              <div>
                <p className="text-[11px] font-medium uppercase tracking-[0.28em] text-[var(--app-text-subtle)]">Swarm Tools</p>
                <h1 className="mt-1 text-3xl font-semibold tracking-[-0.055em] text-[var(--app-text)]">Video Tool</h1>
              </div>
            </div>
          </div>
          <Button variant="outline" className="h-10 rounded-xl" onClick={handleBackToWorkspace}>
            {routeWorkspaceSlug ? (activeSessionId ? 'Back to chat' : 'Back to workspace') : 'Back to launcher'}
          </Button>
        </header>

        {createError ? (
          <div className="mt-6 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text)]">
            {createError}
          </div>
        ) : null}

        {selectedThread ? (
          <main className="flex flex-1 flex-col gap-8 py-6">
            <section className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_300px]">
              <div className="min-w-0">
                <div className="mb-3 flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <h2 className="truncate text-sm font-semibold text-[var(--app-text)]">{selectedThread.title || 'Video thread'}</h2>
                    <p className="mt-1 truncate text-xs text-[var(--app-text-muted)]">{selectedFolderPath}</p>
                  </div>
                  <div className="flex shrink-0 items-center gap-2">
                    <Button variant="ghost" className="h-8 rounded-xl px-2 text-xs text-[var(--app-text-muted)]" onClick={handleOpenPicker}>
                      <FolderOpen size={14} />
                      Add folder
                    </Button>
                    <Button className="h-8 rounded-xl px-3 text-xs" onClick={() => void handleStartChat()} disabled={startingChat || !routeWorkspaceSlug}>
                      <Sparkles size={14} />
                      {startingChat ? 'Starting…' : 'Start chat'}
                    </Button>
                  </div>
                </div>

                <div className="relative grid aspect-video min-h-[340px] place-items-center overflow-hidden border border-[var(--app-border)] bg-[linear-gradient(135deg,color-mix(in_srgb,var(--app-surface)_88%,black),color-mix(in_srgb,var(--app-bg)_92%,black))]">
                  <div className="absolute left-4 top-4 text-xs text-white/55">16:9 · preview</div>
                  <div className="text-center">
                    <Film className="mx-auto text-white/45" size={42} strokeWidth={1.5} />
                    <p className="mt-3 text-sm font-medium text-white/80">{primaryClip?.name || 'Video sits here'}</p>
                    {primaryClip ? <p className="mt-1 text-xs text-white/45">{formatBytes(primaryClip.sizeBytes)}</p> : null}
                  </div>
                  <div className="absolute bottom-0 left-0 right-0 flex items-center gap-3 border-t border-white/10 bg-black/35 px-4 py-3 text-white">
                    <button className="grid h-8 w-8 place-items-center rounded-full bg-white text-black" type="button" aria-label="Play preview">
                      <Play size={14} fill="currentColor" />
                    </button>
                    <div className="h-1 flex-1 bg-white/20">
                      <div className="h-full w-[38%] bg-white" />
                    </div>
                    <span className="text-xs tabular-nums text-white/65">00:14 / 00:38</span>
                  </div>
                </div>
              </div>

              <aside className="min-w-0 border-t border-[var(--app-border)] pt-5 xl:border-l xl:border-t-0 xl:pl-6 xl:pt-0">
                <div className="mb-4 flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <ListVideo size={16} className="text-[var(--app-primary)]" />
                    <h2 className="text-sm font-semibold text-[var(--app-text)]">Clip order</h2>
                  </div>
                  {reordering ? <span className="text-xs text-[var(--app-text-subtle)]">Saving…</span> : null}
                </div>
                <div className="divide-y divide-[var(--app-border)]">
                  {selectedClips.length === 0 ? (
                    <div className="py-4 text-sm text-[var(--app-text-muted)]">No accepted clips are stored in this video thread yet.</div>
                  ) : selectedClips.map((clip, index) => (
                    <div key={clip.id} className="flex items-center gap-3 py-3">
                      <span className="w-8 shrink-0 text-xs font-semibold text-[var(--app-primary)]">{String(index + 1).padStart(2, '0')}</span>
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-sm text-[var(--app-text)]">{clip.name}</p>
                        <p className="mt-1 text-xs text-[var(--app-text-muted)]">{formatBytes(clip.sizeBytes)} · Modified {formatStartedAt(clip.modifiedAt)}</p>
                      </div>
                      <GripVertical size={14} className="text-[var(--app-text-subtle)]" />
                    </div>
                  ))}
                </div>
              </aside>
            </section>

            <section>
              <div className="mb-3 flex items-center justify-between gap-3">
                <h2 className="text-sm font-semibold text-[var(--app-text)]">Timeline</h2>
                <span className="text-xs text-[var(--app-text-muted)]">{selectedClips.length} clip{selectedClips.length === 1 ? '' : 's'}</span>
              </div>

              <div className="border-y border-[var(--app-border)] py-4">
                <div className="mb-3 grid grid-cols-5 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">
                  <span>00:00</span>
                  <span>00:10</span>
                  <span>00:20</span>
                  <span>00:30</span>
                  <span className="text-right">00:40</span>
                </div>

                <div className="space-y-2 overflow-x-auto pb-2">
                  <div className="flex min-w-[720px] gap-2">
                    {selectedClips.map((clip, index) => (
                      <div
                        key={clip.id}
                        className="min-w-[120px] border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-3"
                        style={{ width: clipWidth(clip) }}
                      >
                        <div className="flex items-center justify-between gap-2 text-[11px] text-[var(--app-text-subtle)]">
                          <span>{String(index + 1).padStart(2, '0')}</span>
                          <span>{clipDurationLabel(clip)}</span>
                        </div>
                        <p className="mt-3 truncate text-sm font-medium text-[var(--app-text)]">{clip.name}</p>
                        <p className="mt-1 truncate text-xs text-[var(--app-text-muted)]">{clip.path}</p>
                        <div className="mt-3 flex items-center gap-2">
                          <Button variant="outline" className="h-7 rounded-lg px-2 text-xs" onClick={() => void handleMoveClip(-1, clip.id)} disabled={reordering || index === 0}>Up</Button>
                          <Button variant="outline" className="h-7 rounded-lg px-2 text-xs" onClick={() => void handleMoveClip(1, clip.id)} disabled={reordering || index === selectedClips.length - 1}>Down</Button>
                        </div>
                      </div>
                    ))}
                  </div>

                  <div className="relative min-w-[720px] border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-3">
                    <div className="mb-2 flex items-center gap-2 text-xs text-[var(--app-text-muted)]">
                      <Music2 size={14} className="text-[var(--app-primary)]" />
                      Sound clip · mock track
                    </div>
                    <div className="ml-[8%] flex h-10 w-[84%] items-center gap-1 bg-[color-mix(in_srgb,var(--app-primary)_10%,var(--app-surface))] px-2">
                      {waveformBars.map((height, index) => (
                        <span
                          key={`${height}-${index}`}
                          className="w-full bg-[color-mix(in_srgb,var(--app-primary)_42%,var(--app-border))]"
                          style={{ height: `${height}%` }}
                        />
                      ))}
                    </div>
                  </div>
                </div>
              </div>
            </section>
          </main>
        ) : (
          <main className="grid flex-1 gap-8 py-8 lg:grid-cols-[minmax(0,1fr)_320px]">
            <section className="grid place-items-center">
              <button
                type="button"
                onClick={handleOpenPicker}
                className="flex aspect-square w-full max-w-sm flex-col items-center justify-center border border-dashed border-[var(--app-border)] bg-transparent p-8 text-center transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface)]"
              >
                <FolderOpen size={34} strokeWidth={1.6} className="text-[var(--app-primary)]" />
                <h2 className="mt-5 text-xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">Add a folder with videos in it.</h2>
                <p className="mt-2 text-sm text-[var(--app-text-muted)]">Swarm will make a swarm-video folder and leave the originals untouched.</p>
              </button>
            </section>

            <aside className="rounded-3xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5">
              <div className="flex items-start justify-between gap-3 border-b border-[var(--app-border)] pb-4">
                <div>
                  <p className="text-[11px] font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">History</p>
                  <h2 className="mt-1 text-lg font-semibold tracking-[-0.03em] text-[var(--app-text)]">Prior video threads</h2>
                </div>
                <Button variant="outline" className="h-8 rounded-lg px-3" onClick={handleOpenPicker}>New</Button>
              </div>
              <div className="mt-4 flex flex-col gap-3">
                {videoThreadsQuery.isLoading ? (
                  <div className="flex items-center gap-2 text-sm text-[var(--app-text-muted)]">
                    <Loader2 size={14} className="animate-spin" />
                    Loading video threads…
                  </div>
                ) : videoThreads.length === 0 ? (
                  <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-5 text-sm text-[var(--app-text-muted)]">
                    No prior video threads in this workspace yet.
                  </div>
                ) : (
                  videoThreads.map((thread) => {
                    const folder = threadFolderPath(thread)
                    return (
                      <button
                        key={thread.id}
                        type="button"
                        onClick={() => setSelectedThreadId(thread.id)}
                        className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-3 text-left transition hover:border-[var(--app-border-strong)]"
                      >
                        <div className="min-w-0 truncate text-sm font-medium text-[var(--app-text)]">{thread.title || 'Video Thread'}</div>
                        <div className="mt-1 truncate text-xs text-[var(--app-text-subtle)]">{folder}</div>
                        <div className="mt-2 text-[11px] text-[var(--app-text-subtle)]">Started {formatStartedAt(thread.createdAt)} · {thread.videoClips.length} clip{thread.videoClips.length === 1 ? '' : 's'}</div>
                      </button>
                    )
                  })
                )}
              </div>
            </aside>
          </main>
        )}
      </div>

      {pickerOpen ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Choose a video folder" className="z-[80] p-4 sm:p-6">
          <DialogBackdrop onClick={() => setPickerOpen(false)} />
          <DialogPanel className="mx-auto mt-[6vh] flex h-[min(84vh,860px)] w-[min(980px,calc(100vw-24px))] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1040px,calc(100vw-48px))]">
            <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4 sm:px-6">
              <div>
                <p className="text-[11px] font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">New video thread</p>
                <h2 className="mt-1 text-xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">Choose a folder</h2>
                <p className="mt-2 text-sm text-[var(--app-text-muted)]">Pick the folder that should define this DB-backed video page.</p>
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
                          {addingFolderPath === browser.resolvedPath ? 'Creating…' : 'Create video page from selected folder'}
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
