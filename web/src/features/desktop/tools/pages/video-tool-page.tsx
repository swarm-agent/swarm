import { useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Film, FolderOpen, GripVertical, Loader2 } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { requestJson } from '../../../../app/api'
import { useDesktopStore } from '../../state/use-desktop-store'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import { browseWorkspacePath } from '../../../workspaces/launcher/queries/browse-workspace-path'
import { resolveWorkspaceBySlug } from '../../../workspaces/launcher/services/workspace-route'
import type { WorkspaceBrowseResult, WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'

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
    if (selectedThreadId && videoThreads.some((thread) => thread.id === selectedThreadId)) {
      return
    }
    setSelectedThreadId(videoThreads[0]?.id ?? null)
  }, [selectedThreadId, videoThreads])

  const selectedThread = useMemo(() => {
    if (!selectedThreadId) {
      return null
    }
    return videoThreads.find((thread) => thread.id === selectedThreadId) ?? null
  }, [selectedThreadId, videoThreads])

  const selectedClips = useMemo(() => orderedClips(selectedThread), [selectedThread])

  const handleBack = useMemo(() => {
    if (routeWorkspaceSlug) {
      return () => {
        void navigate({ to: '/$workspaceSlug/tools', params: { workspaceSlug: routeWorkspaceSlug } })
      }
    }
    return () => {
      void navigate({ to: '/tools' })
    }
  }, [navigate, routeWorkspaceSlug])

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
      setSelectedThreadId(updatedThread.id)
      await queryClient.invalidateQueries({ queryKey: ['video-tool-threads', selectedWorkspacePath] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : String(error))
    } finally {
      setReordering(false)
    }
  }, [queryClient, selectedThread, selectedWorkspacePath])

  return (
    <div className="absolute inset-0 overflow-y-auto bg-[var(--app-bg)] text-[var(--app-text)]">
      <div className="mx-auto flex min-h-full w-full max-w-6xl flex-col px-6 py-6 sm:px-8 sm:py-8">
        <header className="flex flex-col gap-5 border-b border-[var(--app-border)] pb-6 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <Button variant="ghost" className="mb-5 h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={handleBack}>
              <ArrowLeft size={15} />
              Back to tools
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

        <main className="grid flex-1 gap-8 py-8 lg:grid-cols-[minmax(0,1fr)_320px]">
          <section className="flex flex-col gap-6">
            <button
              type="button"
              onClick={handleOpenPicker}
              className="flex min-h-[340px] w-full flex-col items-center justify-center border border-dashed border-[var(--app-border)] bg-transparent p-8 text-center transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface)]"
            >
              <FolderOpen size={34} strokeWidth={1.6} className="text-[var(--app-primary)]" />
              <h2 className="mt-5 text-xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">Add a folder with videos in it.</h2>
              <p className="mt-2 max-w-lg text-sm text-[var(--app-text-muted)]">
                Start a new folder-oriented video session from here, or reopen one from history.
              </p>
            </button>

            {createError ? (
              <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text)]">
                {createError}
              </div>
            ) : null}

            {selectedThread ? (
              <div className="rounded-3xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5">
                <div className="flex flex-col gap-2 border-b border-[var(--app-border)] pb-4 sm:flex-row sm:items-end sm:justify-between">
                  <div>
                    <p className="text-[11px] font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">Current video thread</p>
                    <h2 className="mt-1 text-2xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">{selectedThread.title || 'Video Thread'}</h2>
                    <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                      {selectedThread.videoFolders[0] ?? selectedThread.workspacePath}
                    </p>
                  </div>
                  <Button variant="outline" className="rounded-xl" disabled title="AI sessions will be created as children from this thread.">
                    Organization thread
                  </Button>
                </div>

                <div className="mt-5 flex flex-col gap-3">
                  {selectedClips.length === 0 ? (
                    <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-6 text-sm text-[var(--app-text-muted)]">
                      No accepted clips are stored in this video thread yet.
                    </div>
                  ) : (
                    selectedClips.map((clip, index) => (
                      <div
                        key={clip.id}
                        className="flex items-center gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-3"
                      >
                        <GripVertical size={16} className="text-[var(--app-text-subtle)]" />
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-medium text-[var(--app-text)]">{clip.name}</div>
                          <div className="truncate text-xs text-[var(--app-text-subtle)]">{clip.path}</div>
                        </div>
                        <div className="flex items-center gap-2">
                          <Button
                            variant="outline"
                            className="h-8 rounded-lg px-3"
                            onClick={() => void handleMoveClip(-1, clip.id)}
                            disabled={reordering || index === 0}
                          >
                            Up
                          </Button>
                          <Button
                            variant="outline"
                            className="h-8 rounded-lg px-3"
                            onClick={() => void handleMoveClip(1, clip.id)}
                            disabled={reordering || index === selectedClips.length - 1}
                          >
                            Down
                          </Button>
                        </div>
                      </div>
                    ))
                  )}
                </div>
              </div>
            ) : null}
          </section>

          <aside className="rounded-3xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5">
            <div className="border-b border-[var(--app-border)] pb-4">
              <p className="text-[11px] font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">History</p>
              <h2 className="mt-1 text-lg font-semibold tracking-[-0.03em] text-[var(--app-text)]">Prior video threads</h2>
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
                  const folder = thread.videoFolders[0] ?? thread.workspacePath
                  const active = thread.id === selectedThreadId
                  return (
                    <button
                      key={thread.id}
                      type="button"
                      onClick={() => setSelectedThreadId(thread.id)}
                      className={[
                        'rounded-2xl border px-4 py-3 text-left transition',
                        active
                          ? 'border-[var(--app-border-strong)] bg-[var(--app-bg)]'
                          : 'border-[var(--app-border)] bg-[var(--app-bg)] hover:border-[var(--app-border-strong)]',
                      ].join(' ')}
                    >
                      <div className="text-sm font-medium text-[var(--app-text)]">{thread.title || 'Video Thread'}</div>
                      <div className="mt-1 truncate text-xs text-[var(--app-text-subtle)]">{folder}</div>
                    </button>
                  )
                })
              )}
            </div>
          </aside>
        </main>
      </div>

      {pickerOpen ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Choose a video folder" className="z-[80] p-4 sm:p-6">
          <DialogBackdrop onClick={() => setPickerOpen(false)} />
          <DialogPanel className="mx-auto mt-[6vh] flex h-[min(84vh,860px)] w-[min(980px,calc(100vw-24px))] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1040px,calc(100vw-48px))]">
            <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-5 py-4 sm:px-6">
              <div>
                <p className="text-[11px] font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">New video thread</p>
                <h2 className="mt-1 text-xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">Choose a folder</h2>
                <p className="mt-2 text-sm text-[var(--app-text-muted)]">
                  Pick the folder that should define this DB-backed organization thread. AI work starts later as child sessions.
                </p>
              </div>
              <ModalCloseButton onClick={() => setPickerOpen(false)} aria-label="Close video folder picker" />
            </div>

            <div className="flex-1 overflow-y-auto px-5 py-5 sm:px-6">
              <div className="mb-4 flex items-center justify-between gap-3">
                <div className="text-sm text-[var(--app-text-muted)]">
                  Workspace: <span className="text-[var(--app-text)]">{selectedWorkspaceName || 'No workspace selected'}</span>
                </div>
                <div className="flex items-center gap-2">
                  <Button variant="outline" className="rounded-xl" onClick={() => void loadBrowser(browser?.parentPath ?? '')} disabled={!browser?.parentPath || browserLoading}>
                    Up
                  </Button>
                  <Button variant="outline" className="rounded-xl" onClick={() => void loadBrowser(browser?.resolvedPath ?? selectedWorkspacePath)} disabled={browserLoading}>
                    Refresh
                  </Button>
                </div>
              </div>

              {browserError ? (
                <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-4 text-sm text-[var(--app-text)]">
                  {browserError}
                </div>
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
                    Current: {browser.resolvedPath}
                  </div>

                  <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-4">
                    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                      <div>
                        <div className="text-sm font-medium text-[var(--app-text)]">Videos in this folder</div>
                        <div className="mt-1 text-xs text-[var(--app-text-subtle)]">
                          {browserScanLoading ? 'Scanning for accepted videos…' : `${browserClips.length} accepted video${browserClips.length === 1 ? '' : 's'} found`}
                        </div>
                      </div>
                      {browserClips.length > 0 ? (
                        <Button className="rounded-xl" onClick={() => void handleAddFolder(browser.resolvedPath)} disabled={addingFolderPath === browser.resolvedPath || !selectedWorkspacePath}>
                          {addingFolderPath === browser.resolvedPath ? 'Adding…' : 'Add folder'}
                        </Button>
                      ) : null}
                    </div>
                    {browserScanError ? (
                      <div className="mt-3 text-sm text-[var(--app-text)]">{browserScanError}</div>
                    ) : null}
                    {browserClips.length > 0 ? (
                      <div className="mt-4 grid gap-2">
                        {browserClips.map((clip) => (
                          <div key={clip.id} className="rounded-xl border border-[var(--app-border)] bg-transparent px-3 py-2">
                            <div className="truncate text-sm font-medium text-[var(--app-text)]">{clip.name}</div>
                            <div className="truncate text-xs text-[var(--app-text-subtle)]">{clip.path}</div>
                          </div>
                        ))}
                      </div>
                    ) : !browserScanLoading && !browserScanError ? (
                      <div className="mt-3 text-sm text-[var(--app-text-muted)]">No accepted video files in this folder.</div>
                    ) : null}
                  </div>

                  {browser.entries.length === 0 ? (
                    <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-5 text-sm text-[var(--app-text-muted)]">
                      No folders here.
                    </div>
                  ) : (
                    browser.entries.map((entry) => (
                      <div key={entry.path} className="flex flex-col gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-4 sm:flex-row sm:items-center sm:justify-between">
                        <button
                          type="button"
                          onClick={() => void loadBrowser(entry.path)}
                          className="min-w-0 text-left"
                        >
                          <div className="text-sm font-medium text-[var(--app-text)]">{entry.name}</div>
                          <div className="truncate text-xs text-[var(--app-text-subtle)]">{entry.path}</div>
                        </button>
                        <div className="flex shrink-0 items-center gap-2">
                          <Button variant="outline" className="rounded-xl" onClick={() => void loadBrowser(entry.path)}>
                            Browse
                          </Button>
                          <Button className="rounded-xl" onClick={() => void handleAddFolder(entry.path)} disabled={addingFolderPath === entry.path || !selectedWorkspacePath}>
                            {addingFolderPath === entry.path ? 'Adding…' : 'Add folder'}
                          </Button>
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
