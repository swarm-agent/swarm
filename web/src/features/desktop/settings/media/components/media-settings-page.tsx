import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowUp, Check, ChevronRight, FileAudio, Folder, FolderOpen, Image, Sparkles, Video } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Card } from '../../../../../components/ui/card'
import { Input } from '../../../../../components/ui/input'
import { Select } from '../../../../../components/ui/select'
import { Textarea } from '../../../../../components/ui/textarea'
import { browseWorkspacePath } from '../../../../workspaces/launcher/queries/browse-workspace-path'
import { listWorkspaces } from '../../../../workspaces/launcher/queries/list-workspaces'
import type { WorkspaceBrowseResult } from '../../../../workspaces/launcher/types/workspace'
import { resolveWorkspaceBySlug } from '../../../../workspaces/launcher/services/workspace-route'
import { browseDesktopVideoSource, DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT } from '../../../chat/services/video-source-attachments'
import { saveImageDefaultModel } from '../../swarm/mutations/save-image-default-model'
import { saveMediaTranscriptionModel } from '../../swarm/mutations/save-media-transcription-model'
import { getUISettings } from '../../swarm/queries/get-ui-settings'
import { normalizeImageDefaultModel, normalizeMediaTranscriptionModel, type UISettingsWire } from '../../swarm/types/swarm-settings'
import {
  addSourceMediaDirectory,
  cancelVideoTranscription,
  getMediaSettingsCatalog,
  getSourceMediaDirectories,
  isTerminalVideoTranscriptionStatus,
  pollVideoTranscriptionJob,
  readVideoTranscript,
  removeSourceMediaDirectory,
  sourceMediaDirectoriesQueryKey,
  startVideoTranscription,
  truncateVideoFocusNotes,
  videoFocusNotesByteLength,
  VIDEO_FOCUS_NOTES_MAX_BYTES,
  type MediaCatalogModelOption,
  type VideoTranscript,
  type VideoTranscriptionJob,
} from '../queries/get-media-settings'
import { formatTimelineRange, transcriptSegmentDetails } from './video-transcript-presentation'

const uiSettingsQueryKey = ['ui-settings'] as const
const mediaCatalogQueryKey = ['media-settings-catalog'] as const

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback
}

function numberValue(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'string' && value.trim() && Number.isFinite(Number(value))) return Number(value)
  return null
}

function price(value: number): string {
  if (value === 0) return '$0'
  if (value >= 1) return `$${value.toFixed(value >= 10 ? 0 : 2).replace(/\.00$/, '')}`
  return `$${value.toFixed(4).replace(/0+$/, '').replace(/\.$/, '')}`
}

function imagePriceLabel(record: Record<string, unknown>): string {
  const direct = numberValue(record.per_image ?? record.image ?? record.output_image)
  if (direct !== null) return `${price(direct)}/image`

  const billing = record.billing
  if (billing && typeof billing === 'object' && !Array.isArray(billing)) {
    const lines = (billing as Record<string, unknown>).lines
    if (Array.isArray(lines)) {
      const equivalents = lines.flatMap((line) => {
        if (!line || typeof line !== 'object' || Array.isArray(line)) return []
        const item = line as Record<string, unknown>
        const conditions = item.conditions
        const serviceTier = conditions && typeof conditions === 'object' && !Array.isArray(conditions)
          ? String((conditions as Record<string, unknown>).service_tier ?? '')
          : ''
        const value = numberValue(item.price_usd)
        return item.kind === 'equivalent_cost' && item.billable === 'image_output' && item.unit === 'image' && serviceTier !== 'batch' && value !== null
          ? [value]
          : []
      })
      if (equivalents.length) {
        const low = Math.min(...equivalents)
        const high = Math.max(...equivalents)
        return low === high ? `${price(low)}/image` : `${price(low)}–${price(high)}/image`
      }
    }
  }

  const imageOutput = record.image_output_price
  if (imageOutput && typeof imageOutput === 'object' && !Array.isArray(imageOutput)) {
    const value = numberValue((imageOutput as Record<string, unknown>).amount)
    if (value !== null) return `${price(value)}/1M image tokens`
  }
  return ''
}

export function pricingLabel(pricing: unknown): string {
  if (!pricing || typeof pricing !== 'object' || Array.isArray(pricing)) return ''
  const record = pricing as Record<string, unknown>
  if (record.is_free === true) return 'Free'
  const input = numberValue(record.input_price_per_million_tokens ?? record.input_per_million ?? record.input_per_million_tokens ?? record.input)
  const output = numberValue(record.output_price_per_million_tokens ?? record.output_per_million ?? record.output_per_million_tokens ?? record.output)
  const cached = numberValue(record.cached_input_price_per_million_tokens ?? record.cached_input_per_million)
  const image = imagePriceLabel(record)
  const video = numberValue(record.per_minute ?? record.video_per_minute)
  const parts: string[] = []
  if (input !== null) parts.push(`${price(input)} in`)
  if (output !== null) parts.push(`${price(output)} out`)
  if (cached !== null) parts.push(`${price(cached)} cached`)
  if (image) parts.push(image)
  if (video !== null) parts.push(`${price(video)}/min`)
  return parts.length ? `${parts.join(' · ')}${input !== null || output !== null ? ' / 1M tokens' : ''}` : ''
}

function optionLabel(option: MediaCatalogModelOption): string {
  const pricing = pricingLabel(option.pricing)
  return pricing ? `${option.display_name} — ${pricing}` : option.display_name
}

function parentMediaRelativePath(path: string): string {
  const normalized = path.trim().replace(/\\/g, '/')
  if (!normalized || normalized === '.') return '.'
  const parts = normalized.split('/').filter((part) => part && part !== '.')
  parts.pop()
  return parts.length ? parts.join('/') : '.'
}

function providerLabel(provider: string): string {
  if (provider === 'google' || provider === 'google_gemini') return 'Google'
  if (provider === 'codex' || provider === 'codex_openai') return 'Codex'
  return provider.replace(/(^|[-_\s])([a-z])/g, (_match, prefix: string, char: string) => `${prefix}${char.toUpperCase()}`)
}

function ModelSelect({ models, value, disabled, onChange }: { models: MediaCatalogModelOption[]; value: string; disabled: boolean; onChange: (value: string) => void }) {
  const groups = useMemo(() => {
    const result = new Map<string, MediaCatalogModelOption[]>()
    for (const model of models) result.set(model.provider, [...(result.get(model.provider) ?? []), model])
    return Array.from(result.entries())
  }, [models])
  return (
    <Select value={value} disabled={disabled || models.length === 0} onChange={(event) => onChange(event.target.value)}>
      {!value ? <option value="" disabled>Choose a model</option> : null}
      {groups.map(([provider, options]) => (
        <optgroup key={provider} label={providerLabel(provider)}>
          {options.map((option) => <option key={option.id} value={option.id} disabled={!option.ready}>{optionLabel(option)}</option>)}
        </optgroup>
      ))}
    </Select>
  )
}

export function MediaSettingsPage({ workspaceSlug = '', workspacePath: requestedWorkspacePath = '' }: { workspaceSlug?: string; workspacePath?: string }) {
  const queryClient = useQueryClient()
  const settingsQuery = useQuery({ queryKey: uiSettingsQueryKey, queryFn: getUISettings, staleTime: 30_000 })
  const catalogQuery = useQuery({ queryKey: mediaCatalogQueryKey, queryFn: ({ signal }) => getMediaSettingsCatalog(signal), staleTime: 30_000 })
  const [workspacePath, setWorkspacePath] = useState('')
  const [workspaceName, setWorkspaceName] = useState('')
  const [workspaceLoading, setWorkspaceLoading] = useState(Boolean(workspaceSlug))
  const [workspaceError, setWorkspaceError] = useState('')
  const [folderDraft, setFolderDraft] = useState('')
  const [folderPickerOpen, setFolderPickerOpen] = useState(false)
  const [folderBrowser, setFolderBrowser] = useState<WorkspaceBrowseResult | null>(null)
  const [folderBrowseError, setFolderBrowseError] = useState('')
  const [transcriptionRoot, setTranscriptionRoot] = useState('')
  const [transcriptionRelativePath, setTranscriptionRelativePath] = useState('.')
  const [videoRefs, setVideoRefs] = useState<Set<string>>(() => new Set())
  const [videoOptions, setVideoOptions] = useState<Array<{ ref: string; name: string; transcriptRef?: string }>>([])
  const [videoDirectories, setVideoDirectories] = useState<Array<{ name: string; relative_path: string }>>([])
  const [focusNotes, setFocusNotes] = useState('')
  const [transcriptionSession, setTranscriptionSession] = useState('')
  const [transcriptionJobs, setTranscriptionJobs] = useState<VideoTranscriptionJob[]>([])
  const [transcripts, setTranscripts] = useState<Record<string, VideoTranscript>>({})
  const [transcriptionError, setTranscriptionError] = useState('')
  const [transcriptCopyStatus, setTranscriptCopyStatus] = useState('')
  const focusNotesBytes = videoFocusNotesByteLength(focusNotes)

  const copyTranscriptValue = async (value: string, successMessage: string) => {
    try {
      await navigator.clipboard.writeText(value)
      setTranscriptCopyStatus(successMessage)
    } catch {
      setTranscriptCopyStatus('Copy failed.')
    }
  }

  useEffect(() => {
    let cancelled = false
    const directWorkspacePath = requestedWorkspacePath.trim()
    if (directWorkspacePath) {
      setWorkspacePath(directWorkspacePath)
      setWorkspaceName(directWorkspacePath)
      setWorkspaceLoading(false)
      setWorkspaceError('')
      return () => { cancelled = true }
    }
    if (!workspaceSlug) {
      setWorkspacePath('')
      setWorkspaceName('')
      setWorkspaceLoading(false)
      return () => { cancelled = true }
    }
    setWorkspaceLoading(true)
    setWorkspaceError('')
    void listWorkspaces().then((workspaces) => {
      if (cancelled) return
      const workspace = resolveWorkspaceBySlug(workspaces, workspaceSlug)
      if (!workspace) throw new Error('The workspace for this settings route could not be found.')
      setWorkspacePath(workspace.path)
      setWorkspaceName(workspace.workspaceName || workspace.path)
    }).catch((error) => {
      if (!cancelled) setWorkspaceError(errorMessage(error, 'Workspace details are unavailable.'))
    }).finally(() => {
      if (!cancelled) setWorkspaceLoading(false)
    })
    return () => { cancelled = true }
  }, [requestedWorkspacePath, workspaceSlug])

  const sourceQueryKey = sourceMediaDirectoriesQueryKey(workspacePath)
  const sourceQuery = useQuery({
    queryKey: sourceQueryKey,
    queryFn: ({ signal }) => getSourceMediaDirectories(workspacePath, signal),
    enabled: Boolean(workspacePath),
  })
  const imageSave = useMutation({
    mutationFn: (defaultModel: string) => saveImageDefaultModel({ current: settingsQuery.data ?? {}, defaultModel }),
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })
  const transcriptionSave = useMutation({
    mutationFn: (transcriptionModel: string) => saveMediaTranscriptionModel({ current: settingsQuery.data ?? {}, transcriptionModel }),
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })
  const addFolder = useMutation({
    mutationFn: (directoryPath: string) => addSourceMediaDirectory(workspacePath, directoryPath),
    onSuccess: (directories) => {
      queryClient.setQueryData(sourceQueryKey, directories)
      setFolderDraft('')
    },
  })
  const folderBrowse = useMutation({
    mutationFn: (path: string) => browseWorkspacePath(path),
    onSuccess: (result) => {
      setFolderBrowser(result)
      setFolderBrowseError('')
    },
    onError: (error) => setFolderBrowseError(errorMessage(error, 'Folders are unavailable.')),
  })
  const browseVideos = useMutation({
    mutationFn: ({ rootPath, relativePath }: { rootPath: string; relativePath: string }) => browseDesktopVideoSource(workspacePath, rootPath, relativePath),
    onSuccess: (result) => {
      setTranscriptionRelativePath(result.relativePath)
      setVideoDirectories(result.directories)
      setVideoOptions(result.clips.map((clip) => ({ ref: clip.ref, name: clip.name, transcriptRef: clip.transcript_ref })))
      setVideoRefs(new Set())
      setTranscripts({})
      setTranscriptionJobs([])
      setTranscriptionError('')
      setTranscriptCopyStatus('')
    },
  })
  const transcribeVideo = useMutation({
    mutationFn: () => startVideoTranscription(workspacePath, Array.from(videoRefs), focusNotes),
    onMutate: () => {
      setTranscriptionError('')
      setTranscripts({})
      setTranscriptionJobs([])
      setTranscriptCopyStatus('')
    },
    onSuccess: ({ session_id, jobs }) => {
      setTranscriptionSession(session_id)
      setTranscriptionJobs(jobs)
    },
  })
  const cancelTranscription = useMutation({
    mutationFn: (jobRef: string) => {
      if (!transcriptionSession) throw new Error('There is no active transcription job to cancel.')
      return cancelVideoTranscription(workspacePath, transcriptionSession, jobRef)
    },
    onSuccess: (job) => setTranscriptionJobs((current) => current.map((candidate) => candidate.ref === job.ref ? job : candidate)),
  })
  const removeFolder = useMutation({
    mutationFn: (directoryPath: string) => removeSourceMediaDirectory(workspacePath, directoryPath),
    onSuccess: (directories) => queryClient.setQueryData(sourceQueryKey, directories),
  })

  const imageModels = catalogQuery.data?.image_models ?? []
  const transcriptionModels = catalogQuery.data?.transcription_models ?? []
  const configuredImage = normalizeImageDefaultModel(settingsQuery.data)
  const configuredImageID = configuredImage === 'gpt-5.5' ? 'codex-image-gen' : configuredImage
  const configuredTranscription = normalizeMediaTranscriptionModel(settingsQuery.data)
  const selectedImage = imageModels.some((model) => model.id === configuredImageID) ? configuredImageID : ''
  const selectedTranscription = transcriptionModels.some((model) => model.id === configuredTranscription) ? configuredTranscription : ''
  const selectedImageOption = imageModels.find((model) => model.id === selectedImage)
  const selectedTranscriptionOption = transcriptionModels.find((model) => model.id === selectedTranscription)
  const settingsError = imageSave.error || transcriptionSave.error || settingsQuery.error || catalogQuery.error
  const folders = sourceQuery.data ?? []

  const activeTranscriptionJobRefs = transcriptionJobs.filter((job) => !isTerminalVideoTranscriptionStatus(job.status)).map((job) => job.ref).join(',')
  useEffect(() => {
    const refs = activeTranscriptionJobRefs.split(',').filter(Boolean)
    if (!refs.length || !transcriptionSession) return
    const controller = new AbortController()
    void Promise.all(refs.map((jobRef) => pollVideoTranscriptionJob({
      workspacePath,
      sessionID: transcriptionSession,
      jobRef,
      signal: controller.signal,
      onUpdate: (job) => setTranscriptionJobs((current) => current.map((candidate) => candidate.ref === job.ref ? job : candidate)),
    }))).then((jobs) => {
      const failed = jobs.find((job) => job.status === 'failed')
      if (failed) setTranscriptionError(failed.failure_reason || 'Video transcription failed.')
    }).catch((error) => {
      if (!controller.signal.aborted) setTranscriptionError(errorMessage(error, 'Transcription status is unavailable.'))
    })
    return () => controller.abort()
  }, [activeTranscriptionJobRefs, transcriptionSession, workspacePath])

  const readyTranscriptRefs = transcriptionJobs.filter((job) => job.status === 'ready' && !transcripts[job.transcript_ref]).map((job) => job.transcript_ref).join(',')
  useEffect(() => {
    const refs = readyTranscriptRefs.split(',').filter(Boolean)
    if (!refs.length || !transcriptionSession) return
    let cancelled = false
    void Promise.all(refs.map((ref) => readVideoTranscript(workspacePath, transcriptionSession, ref))).then((savedTranscripts) => {
      if (!cancelled) setTranscripts((current) => Object.fromEntries([...Object.entries(current), ...savedTranscripts.map((saved) => [saved.ref, saved])]))
    }).catch((error) => {
      if (!cancelled) setTranscriptionError(errorMessage(error, 'A saved transcript could not be read.'))
    })
    return () => { cancelled = true }
  }, [readyTranscriptRefs, transcriptionSession, workspacePath])
  const folderError = addFolder.error || removeFolder.error || sourceQuery.error || (workspaceError ? new Error(workspaceError) : null)

  return (
    <div className="grid gap-6">
      <header className="flex items-center gap-3">
        <div className="grid h-10 w-10 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text)]"><Sparkles size={18} /></div>
        <div>
          <h2 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">Media</h2>
          <p className="text-sm text-[var(--app-text-muted)]">Configure media models and workspace-scoped source access.</p>
        </div>
      </header>

      <Card className="p-5">
        <section aria-labelledby="source-media-title" className="space-y-4">
          <div className="flex items-start gap-3"><FolderOpen size={18} className="mt-1 text-[var(--app-text-muted)]" /><div><h3 id="source-media-title" className="text-lg font-semibold text-[var(--app-text)]">Source media folder</h3><p className="mt-1 text-sm text-[var(--app-text-muted)]">Designate one or more folders where you put source videos for {workspaceName || 'this workspace'}. AI can view and transcribe supported videos through the media tools, but source access is read-only: Swarm never edits, moves, renames, or deletes these files, so they remain unmodified.</p></div></div>
          {!workspaceSlug && !requestedWorkspacePath.trim() ? <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4 text-sm text-[var(--app-warning)]">Source media folders are workspace-scoped. Open a workspace, then choose Settings → Media.</div> : workspaceLoading ? <p className="text-sm text-[var(--app-text-muted)]">Loading workspace…</p> : workspacePath ? <>
            {sourceQuery.isPending ? <p className="text-sm text-[var(--app-text-muted)]">Loading designated folders…</p> : folders.length ? <div className="grid gap-2">{folders.map((folder) => <div key={folder} className="flex min-w-0 items-center justify-between gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2"><span className="min-w-0 truncate font-mono text-xs text-[var(--app-text)]" title={folder}>{folder}</span><Button variant="ghost" size="sm" disabled={removeFolder.isPending} onClick={() => removeFolder.mutate(folder)}>Remove</Button></div>)}</div> : <p className="text-sm text-[var(--app-text-muted)]">No source media folder has been designated yet.</p>}
            <form className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_auto]" onSubmit={(event) => { event.preventDefault(); const path = folderDraft.trim(); if (path) addFolder.mutate(path) }}><Input value={folderDraft} onChange={(event) => setFolderDraft(event.target.value)} placeholder="Choose or enter a folder" aria-label="Source media folder path" /><Button type="button" variant="outline" onClick={() => { setFolderPickerOpen(true); folderBrowse.mutate(folderDraft.trim() || workspacePath) }}>Browse</Button><Button type="submit" variant="outline" disabled={!folderDraft.trim() || addFolder.isPending}>{addFolder.isPending ? 'Adding…' : 'Add folder'}</Button></form>
            {folderPickerOpen ? <div className="space-y-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3"><div className="flex items-center justify-between gap-2"><span className="min-w-0 truncate font-mono text-xs text-[var(--app-text)]" title={folderBrowser?.resolvedPath}>{folderBrowser?.resolvedPath || 'Loading folders…'}</span><div className="flex gap-2"><Button type="button" variant="ghost" size="sm" disabled={!folderBrowser?.parentPath || folderBrowse.isPending} onClick={() => folderBrowser?.parentPath && folderBrowse.mutate(folderBrowser.parentPath)}><ArrowUp size={14} /> Up</Button><Button type="button" variant="ghost" size="sm" onClick={() => setFolderPickerOpen(false)}>Close</Button></div></div>{folderBrowseError ? <p className="text-sm text-[var(--app-danger)]">{folderBrowseError}</p> : null}<div className="grid max-h-64 gap-1 overflow-auto">{folderBrowser?.entries.filter((entry) => entry.isDirectory).map((entry) => <div key={entry.path} className="flex items-center justify-between gap-2 rounded-lg px-2 py-1 hover:bg-[var(--app-surface-hover)]"><button type="button" className="flex min-w-0 flex-1 items-center gap-2 text-left text-sm text-[var(--app-text)]" onClick={() => folderBrowse.mutate(entry.path)}><Folder size={14} /><span className="truncate">{entry.name}</span></button><Button type="button" variant="ghost" size="sm" disabled={addFolder.isPending} onClick={() => { setFolderDraft(entry.path); setFolderPickerOpen(false); addFolder.mutate(entry.path) }}>Choose & save</Button></div>)}</div>{folderBrowser?.resolvedPath ? <Button type="button" size="sm" disabled={addFolder.isPending} onClick={() => { setFolderDraft(folderBrowser.resolvedPath); setFolderPickerOpen(false); addFolder.mutate(folderBrowser.resolvedPath) }}>Choose & save this folder</Button> : null}</div> : null}
          </> : null}
          {folderError ? <div role="alert" className="text-sm text-[var(--app-danger)]">{errorMessage(folderError, 'Source media folders are unavailable.')}</div> : null}
        </section>
      </Card>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card className="p-5">
          <section aria-labelledby="image-model-title" className="space-y-4">
            <div className="flex items-start gap-3"><Image size={18} className="mt-1 text-[var(--app-text-muted)]" /><div><h3 id="image-model-title" className="text-lg font-semibold text-[var(--app-text)]">Image generation</h3><p className="mt-1 text-sm text-[var(--app-text-muted)]">Used by managed image generation and image Iteration Swarms.</p></div></div>
            {settingsQuery.isPending || catalogQuery.isPending ? <p className="text-sm text-[var(--app-text-muted)]">Loading image models…</p> : (
              <label className="block space-y-2"><span className="text-sm font-medium text-[var(--app-text)]">Default image model</span><ModelSelect models={imageModels} value={selectedImage} disabled={imageSave.isPending} onChange={(value) => imageSave.mutate(value)} /></label>
            )}
            {selectedImageOption && !selectedImageOption.ready ? <p className="text-sm text-[var(--app-warning)]">{selectedImageOption.reason || `${providerLabel(selectedImageOption.provider)} needs authentication before it can generate images.`}</p> : null}
            {imageSave.isSuccess ? <p className="text-sm text-[var(--app-success)]">Image model saved.</p> : null}
          </section>
        </Card>

        <Card className="p-5">
          <section aria-labelledby="transcription-model-title" className="space-y-4">
            <div className="flex items-start gap-3"><FileAudio size={18} className="mt-1 text-[var(--app-text-muted)]" /><div><h3 id="transcription-model-title" className="text-lg font-semibold text-[var(--app-text)]">Video understanding model</h3><p className="mt-1 text-sm text-[var(--app-text-muted)]">Choose the Google model Swarm uses to analyze a video’s visuals and embedded audio and produce one timestamped multimodal transcript.</p></div></div>
            {catalogQuery.isPending ? <p className="text-sm text-[var(--app-text-muted)]">Loading qualified Google models…</p> : transcriptionModels.length ? (
              <label className="block space-y-2"><span className="text-sm font-medium text-[var(--app-text)]">Transcription model</span><ModelSelect models={transcriptionModels} value={selectedTranscription} disabled={transcriptionSave.isPending} onChange={(value) => transcriptionSave.mutate(value)} /></label>
            ) : <p className="text-sm text-[var(--app-text-muted)]">No catalog models currently qualify for video-to-text understanding.</p>}
            {selectedTranscriptionOption && !selectedTranscriptionOption.ready ? <p className="text-sm text-[var(--app-warning)]">{selectedTranscriptionOption.reason || 'Google authentication is required before this model can be used.'}</p> : null}
            {transcriptionSave.isSuccess ? <p className="text-sm text-[var(--app-success)]">Transcription model saved.</p> : null}
            <p className="text-xs text-[var(--app-text-subtle)]">The same durable pipeline serves agent-triggered and direct transcription.</p>
          </section>
        </Card>
      </div>

      <Card className="p-5">
        <section aria-labelledby="transcribe-video-title" className="space-y-4">
          <div className="flex items-start gap-3"><FileAudio size={18} className="mt-1 text-[var(--app-text-muted)]" /><div><h3 id="transcribe-video-title" className="text-lg font-semibold text-[var(--app-text)]">Transcribe videos</h3><p className="mt-1 text-sm text-[var(--app-text-muted)]">Browse inside a designated source folder, then select one video or up to {DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT} videos. Each saved transcript remains tied to the unchanged source fingerprint for later authenticated AI read-back.</p></div></div>
          {!workspacePath ? <p className="text-sm text-[var(--app-text-muted)]">Open a workspace to browse source videos.</p> : !folders.length ? <p className="text-sm text-[var(--app-warning)]">Add a source media folder at the top of this page first.</p> : !selectedTranscription || selectedTranscriptionOption?.ready !== true ? <p className="text-sm text-[var(--app-warning)]">Choose a ready transcription model above before starting.</p> : <>
            <div className="space-y-2"><span className="text-sm font-medium text-[var(--app-text)]">Designated folders</span><div className="grid gap-2 sm:grid-cols-2">{folders.map((folder) => <button type="button" key={folder} className={`flex min-w-0 items-center gap-2 rounded-xl border px-3 py-3 text-left text-sm transition ${transcriptionRoot === folder ? 'border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] hover:border-[var(--app-border-strong)]'}`} onClick={() => { setTranscriptionRoot(folder); setTranscriptionRelativePath('.'); browseVideos.mutate({ rootPath: folder, relativePath: '.' }) }}><FolderOpen size={16} className="shrink-0 text-[var(--app-text-muted)]" /><span className="min-w-0 truncate font-mono text-xs text-[var(--app-text)]" title={folder}>{folder}</span><ChevronRight size={14} className="ml-auto shrink-0 text-[var(--app-text-subtle)]" /></button>)}</div></div>
            {browseVideos.isPending ? <p className="text-sm text-[var(--app-text-muted)]">Scanning videos…</p> : transcriptionRoot ? <div className="space-y-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3"><div className="flex items-center justify-between gap-2"><span className="min-w-0 truncate font-mono text-xs text-[var(--app-text)]" title={transcriptionRelativePath}>{transcriptionRelativePath === '.' ? 'Folder root' : transcriptionRelativePath}</span><Button type="button" variant="ghost" size="sm" disabled={transcriptionRelativePath === '.'} onClick={() => browseVideos.mutate({ rootPath: transcriptionRoot, relativePath: parentMediaRelativePath(transcriptionRelativePath) })}><ArrowUp size={14} /> Up</Button></div><div className="grid gap-1">{videoDirectories.map((directory) => <button type="button" key={directory.relative_path} className="flex items-center gap-2 rounded-lg px-2 py-2 text-left text-sm text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]" onClick={() => browseVideos.mutate({ rootPath: transcriptionRoot, relativePath: directory.relative_path })}><Folder size={15} /><span className="truncate">{directory.name}</span><ChevronRight size={14} className="ml-auto text-[var(--app-text-subtle)]" /></button>)}{videoOptions.map((video) => { const selected = videoRefs.has(video.ref); const disabled = !selected && videoRefs.size >= DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT; return <button type="button" key={video.ref} disabled={disabled} className="flex items-center gap-2 rounded-lg px-2 py-2 text-left text-sm text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:opacity-50" onClick={() => setVideoRefs((current) => { const next = new Set(current); if (next.has(video.ref)) next.delete(video.ref); else next.add(video.ref); return next })}><span className={`flex size-4 shrink-0 items-center justify-center rounded border ${selected ? 'border-[var(--app-border-accent)] bg-[var(--app-primary)] text-[var(--app-bg)]' : 'border-[var(--app-border)] text-transparent'}`}><Check size={12} /></span><Video size={15} /><span className="truncate">{video.name}</span>{video.transcriptRef ? <span className="ml-auto text-xs text-[var(--app-success)]">Transcript ready</span> : null}</button>})}{!videoDirectories.length && !videoOptions.length ? <p className="px-2 py-3 text-sm text-[var(--app-text-muted)]">No supported videos in this folder.</p> : null}</div><p className="text-xs text-[var(--app-text-subtle)]">{videoRefs.size} selected · maximum {DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT}</p></div> : null}
            <label className="block space-y-2"><span className="text-sm font-medium text-[var(--app-text)]">Optional focus notes</span><Textarea value={focusNotes} rows={3} onChange={(event) => setFocusNotes(truncateVideoFocusNotes(event.target.value))} placeholder="For example: pay special attention to the steps shown in the settings panel." /><span className="text-xs text-[var(--app-text-subtle)]">{focusNotesBytes}/{VIDEO_FOCUS_NOTES_MAX_BYTES} bytes. Notes guide emphasis only; they cannot change the transcript format.</span></label>
            <p className="text-sm text-[var(--app-text-muted)]">Swarm sends the complete video to the configured model, which automatically analyzes embedded audio and sampled visuals. You do not need to identify whether the video has audio.</p>
            <div className="flex flex-wrap gap-2">
              <Button disabled={!videoRefs.size || transcribeVideo.isPending || transcriptionJobs.some((job) => !isTerminalVideoTranscriptionStatus(job.status))} onClick={() => transcribeVideo.mutate()}>{transcribeVideo.isPending ? 'Starting…' : `Analyze and transcribe ${videoRefs.size || ''} ${videoRefs.size === 1 ? 'video' : 'videos'}`}</Button>
            </div>
            {transcriptionJobs.length ? <div aria-live="polite" className="grid gap-2">{transcriptionJobs.map((job, index) => <div key={job.ref} className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text-muted)]"><span>Video {index + 1}: <span className="font-medium capitalize text-[var(--app-text)]">{job.status}</span>{job.status === 'ready' ? ' — saved and ready for AI read-back.' : job.status === 'processing' || job.status === 'partial' ? ' — analyzing audio and visuals.' : ''}</span>{!isTerminalVideoTranscriptionStatus(job.status) ? <Button variant="ghost" size="sm" disabled={cancelTranscription.isPending} onClick={() => cancelTranscription.mutate(job.ref)}>Cancel</Button> : null}</div>)}</div> : null}
            {Object.values(transcripts).map((transcript, transcriptIndex) => <div key={transcript.ref} className="space-y-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4"><div className="flex flex-wrap items-center justify-between gap-3"><div><h4 className="font-semibold text-[var(--app-text)]">Saved transcript {Object.keys(transcripts).length > 1 ? transcriptIndex + 1 : ''}</h4><p className="mt-1 break-all font-mono text-xs text-[var(--app-text-subtle)]">{transcript.ref}</p></div><span className="text-xs text-[var(--app-success)]">{transcript.validation.state}</span></div><div className="flex flex-wrap gap-2"><Button variant="outline" size="sm" onClick={() => { void copyTranscriptValue(transcript.text, 'Transcript copied.') }}>Copy transcript</Button><Button variant="outline" size="sm" onClick={() => { void copyTranscriptValue(transcript.ref, 'Transcript reference copied.') }}>Copy reference for AI</Button></div>{transcriptCopyStatus ? <p aria-live="polite" className="text-xs text-[var(--app-text-muted)]">{transcriptCopyStatus}</p> : null}{transcript.details_truncated ? <p className="text-sm text-[var(--app-warning)]">This preview is bounded. The full transcript remains durably saved and available by its transcript reference.</p> : null}<pre className="max-h-96 overflow-auto whitespace-pre-wrap text-sm text-[var(--app-text)]">{transcript.text}</pre>{transcript.segments.length ? <div className="space-y-3"><h5 className="text-sm font-semibold text-[var(--app-text)]">Multimodal timeline</h5>{transcript.segments.map((segment, index) => <div key={`${segment.start_ms}-${segment.end_ms}-${index}`} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3"><div className="text-xs font-medium text-[var(--app-text-subtle)]">{formatTimelineRange(segment.start_ms, segment.end_ms)}</div><div className="mt-2 grid gap-1 text-sm text-[var(--app-text)]">{transcriptSegmentDetails(segment).map((detail) => <p key={detail.label}><strong>{detail.label}:</strong> {detail.value}</p>)}</div></div>)}</div> : null}</div>)}
            {transcriptionError || browseVideos.error || transcribeVideo.error || cancelTranscription.error ? <div role="alert" className="text-sm text-[var(--app-danger)]">{transcriptionError || errorMessage(browseVideos.error || transcribeVideo.error || cancelTranscription.error, 'Video transcription is unavailable.')}</div> : null}
          </>}
        </section>
      </Card>

      <Card className="overflow-hidden p-5">
        <section aria-labelledby="video-generation-title" className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3"><Video size={18} className="mt-1 text-[var(--app-text-muted)]" /><div><h3 id="video-generation-title" className="text-lg font-semibold text-[var(--app-text)]">Video generation</h3><p className="mt-1 text-sm text-[var(--app-text-muted)]">Generate managed video alternatives from Swarm briefs.</p></div></div>
          <span className="shrink-0 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1 text-xs font-semibold uppercase tracking-wide text-[var(--app-text-muted)]">Coming soon</span>
        </section>
      </Card>

      {settingsError ? <div role="alert" className="text-sm text-[var(--app-danger)]">{errorMessage(settingsError, 'Media settings are unavailable.')}</div> : null}
      <p className="text-xs text-[var(--app-text-subtle)]">Model changes save automatically and apply to future media operations.</p>
    </div>
  )
}
