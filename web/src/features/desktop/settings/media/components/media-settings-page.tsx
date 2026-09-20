import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowUp,
  Check,
  ChevronDown,
  ChevronRight,
  FileAudio,
  Film,
  Folder,
  FolderOpen,
  Image,
  Info,
  Music,
  Sparkles,
  Video,
} from 'lucide-react'
import { cn } from '../../../../../lib/cn'
import { Button } from '../../../../../components/ui/button'
import { Card } from '../../../../../components/ui/card'
import { Input } from '../../../../../components/ui/input'
import { Textarea } from '../../../../../components/ui/textarea'
import { browseWorkspacePath } from '../../../../workspaces/launcher/queries/browse-workspace-path'
import { listWorkspaces } from '../../../../workspaces/launcher/queries/list-workspaces'
import type { WorkspaceBrowseResult } from '../../../../workspaces/launcher/types/workspace'
import { resolveWorkspaceBySlug } from '../../../../workspaces/launcher/services/workspace-route'
import { browseDesktopVideoSource, DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT } from '../../../chat/services/video-source-attachments'
import { saveAudioDefaultModel } from '../../swarm/mutations/save-audio-models'
import { saveImageDefaultModel } from '../../swarm/mutations/save-image-default-model'
import { saveMediaTranscriptionModel } from '../../swarm/mutations/save-media-transcription-model'
import { saveVideoDefaultModel, saveVideoIterationModel } from '../../swarm/mutations/save-video-models'
import { getUISettings } from '../../swarm/queries/get-ui-settings'
import {
  normalizeAudioDefaultModel,
  normalizeImageDefaultModel,
  normalizeMediaTranscriptionModel,
  normalizeVideoDefaultModel,
  normalizeVideoIterationModel,
  type UISettingsWire,
} from '../../swarm/types/swarm-settings'
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

export function ModelSelect({
  models,
  value,
  disabled,
  placeholder = 'Choose a model',
  onChange,
  ariaLabel,
}: {
  models: MediaCatalogModelOption[]
  value: string
  disabled: boolean
  placeholder?: string
  onChange: (value: string) => void
  ariaLabel?: string
}) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{
    top?: number
    bottom?: number
    left: number
    width: number
    maxHeight: number
  } | null>(null)

  const selectedModel = useMemo(
    () => models.find((model) => model.id === value) ?? null,
    [models, value],
  )

  const groups = useMemo(() => {
    const result = new Map<string, MediaCatalogModelOption[]>()
    for (const model of models) {
      result.set(model.provider, [...(result.get(model.provider) ?? []), model])
    }
    return Array.from(result.entries())
  }, [models])

  const updatePosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') {
      setPosition(null)
      return
    }
    const rect = triggerRef.current.getBoundingClientRect()
    const viewportHeight = window.innerHeight
    const viewportWidth = window.innerWidth
    const spaceBelow = viewportHeight - rect.bottom
    const spaceAbove = rect.top
    const openUpward = spaceBelow < 240 && spaceAbove > spaceBelow

    const width = rect.width
    const left = Math.max(8, Math.min(rect.left, viewportWidth - width - 8))
    const maxHeight = Math.max(140, Math.min(320, openUpward ? spaceAbove - 16 : spaceBelow - 16))

    if (openUpward) {
      setPosition({
        bottom: viewportHeight - rect.top + 6,
        left,
        width,
        maxHeight,
      })
    } else {
      setPosition({
        top: rect.bottom + 6,
        left,
        width,
        maxHeight,
      })
    }
  }, [])

  useEffect(() => {
    if (!open) {
      setPosition(null)
      return
    }
    updatePosition()
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
  }, [open, updatePosition])

  useEffect(() => {
    if (!open) return
    function handleClickOutside(e: MouseEvent) {
      const target = e.target as Node | null
      if (
        dropdownRef.current &&
        !dropdownRef.current.contains(target) &&
        !triggerRef.current?.contains(target)
      ) {
        setOpen(false)
      }
    }
    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') {
        e.preventDefault()
        setOpen(false)
        triggerRef.current?.focus()
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [open])

  const dropdownStyle: React.CSSProperties = position
    ? {
        left: position.left,
        width: position.width,
        maxHeight: position.maxHeight,
        ...(position.top !== undefined ? { top: position.top } : {}),
        ...(position.bottom !== undefined ? { bottom: position.bottom } : {}),
      }
    : {}

  return (
    <div className="relative w-full">
      <button
        ref={triggerRef}
        type="button"
        role="combobox"
        aria-expanded={open}
        aria-haspopup="listbox"
        aria-label={ariaLabel}
        disabled={disabled || models.length === 0}
        onClick={() => setOpen((current) => !current)}
        className={cn(
          'flex min-h-10 w-full items-center justify-between gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-left text-sm text-[var(--app-text)] outline-none transition',
          'hover:border-[var(--app-border-strong)] focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]',
          'disabled:cursor-not-allowed disabled:bg-[var(--app-bg-inset)] disabled:opacity-50',
          open && 'border-[var(--app-border-accent)] ring-1 ring-[var(--app-border-accent)]',
        )}
      >
        <span className={cn('truncate font-medium', !selectedModel && 'font-normal text-[var(--app-text-muted)]')}>
          {selectedModel ? selectedModel.display_name : placeholder}
        </span>
        <ChevronDown
          size={16}
          className={cn(
            'shrink-0 text-[var(--app-text-muted)] transition-transform duration-200',
            open && 'rotate-180',
          )}
        />
      </button>

      {open && position && typeof document !== 'undefined'
        ? createPortal(
            <div
              ref={dropdownRef}
              role="listbox"
              aria-label={ariaLabel}
              className="fixed z-[9999] flex flex-col overflow-hidden rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-elevated)] shadow-[var(--shadow-panel)] animate-in fade-in zoom-in-95 duration-150"
              style={dropdownStyle}
            >
              <div
                className="overflow-y-auto p-1.5 space-y-1"
                style={{ maxHeight: position.maxHeight }}
              >
                {groups.map(([provider, options]) => (
                  <div key={provider} className="space-y-0.5">
                    <div className="px-3 pt-2 pb-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                      {providerLabel(provider)}
                    </div>
                    {options.map((option) => {
                      const pricing = pricingLabel(option.pricing)
                      const isSelected = option.id === value
                      return (
                        <button
                          key={option.id}
                          type="button"
                          role="option"
                          aria-selected={isSelected}
                          disabled={!option.ready}
                          onClick={() => {
                            onChange(option.id)
                            setOpen(false)
                          }}
                          className={cn(
                            'group flex w-full items-center justify-between gap-3 rounded-xl px-3 py-2 text-left transition outline-none',
                            isSelected
                              ? 'border border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_12%,var(--app-surface-subtle))]'
                              : 'border border-transparent hover:bg-[var(--app-surface-hover)]',
                            !option.ready && 'cursor-not-allowed opacity-50',
                          )}
                        >
                          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                            {/* Top section: model name */}
                            <div className="flex items-center gap-2">
                              <span className="truncate text-sm font-medium text-[var(--app-text)]">
                                {option.display_name}
                              </span>
                              {!option.ready ? (
                                <span className="shrink-0 rounded bg-[var(--app-surface)] px-1.5 py-0.5 text-[10px] text-[var(--app-warning)]">
                                  Auth needed
                                </span>
                              ) : null}
                            </div>
                            {/* Bottom section: meta data for the info */}
                            <span
                              className="truncate text-xs text-[var(--app-text-muted)]"
                              title={pricing ? `${pricing}${option.model ? ` · ${option.model}` : ''}` : option.model}
                            >
                              {pricing || option.model || 'Standard tier'}
                            </span>
                          </div>
                          {isSelected ? (
                            <Check size={16} className="shrink-0 text-[var(--app-primary)]" />
                          ) : null}
                        </button>
                      )
                    })}
                  </div>
                ))}
              </div>
            </div>,
            document.body,
          )
        : null}
    </div>
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
  const videoDefaultSave = useMutation({
    mutationFn: (defaultModel: string) => saveVideoDefaultModel({ current: settingsQuery.data ?? {}, defaultModel }),
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })
  const videoIterationSave = useMutation({
    mutationFn: (iterationModel: string) => saveVideoIterationModel({ current: settingsQuery.data ?? {}, iterationModel }),
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })
  const audioDefaultSave = useMutation({
    mutationFn: (defaultModel: string) => saveAudioDefaultModel({ current: settingsQuery.data ?? {}, defaultModel }),
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
  const videoGenerationModels = catalogQuery.data?.video_generation_models ?? catalogQuery.data?.video_models ?? []
  const videoIterationModels = catalogQuery.data?.video_iteration_models ?? []
  const audioModels = catalogQuery.data?.audio_models ?? []
  const configuredImage = normalizeImageDefaultModel(settingsQuery.data)
  const configuredImageID = configuredImage === 'gpt-5.5' ? 'codex-image-gen' : configuredImage
  const configuredTranscription = normalizeMediaTranscriptionModel(settingsQuery.data)
  const configuredVideoDefault = normalizeVideoDefaultModel(settingsQuery.data)
  const configuredVideoIteration = normalizeVideoIterationModel(settingsQuery.data)
  const configuredAudioDefault = normalizeAudioDefaultModel(settingsQuery.data)
  const selectedImage = imageModels.some((model) => model.id === configuredImageID) ? configuredImageID : ''
  const selectedTranscription = transcriptionModels.some((model) => model.id === configuredTranscription) ? configuredTranscription : ''
  const selectedVideoDefault = videoGenerationModels.some((model) => model.id === configuredVideoDefault)
    ? configuredVideoDefault
    : videoGenerationModels.find((model) => model.id === 'veo-3.1-generate-preview' || model.id === 'gemini-omni-1.1-flash')?.id ?? videoGenerationModels[0]?.id ?? ''
  const selectedVideoIteration = videoIterationModels.some((model) => model.id === configuredVideoIteration)
    ? configuredVideoIteration
    : videoIterationModels.find((model) => model.id === 'gemini-omni-1.1-flash')?.id ?? videoIterationModels[0]?.id ?? ''
  const selectedAudioDefault = audioModels.some((model) => model.id === configuredAudioDefault)
    ? configuredAudioDefault
    : audioModels.find((model) => model.id === 'lyria-3.5' || model.id === 'lyria-3-clip-preview')?.id ?? audioModels[0]?.id ?? ''
  const selectedImageOption = imageModels.find((model) => model.id === selectedImage)
  const selectedTranscriptionOption = transcriptionModels.find((model) => model.id === selectedTranscription)
  const selectedVideoDefaultOption = videoGenerationModels.find((model) => model.id === selectedVideoDefault)
  const selectedVideoIterationOption = videoIterationModels.find((model) => model.id === selectedVideoIteration)
  const selectedAudioDefaultOption = audioModels.find((model) => model.id === selectedAudioDefault)

  const settingsError = imageSave.error || transcriptionSave.error || videoDefaultSave.error || videoIterationSave.error || audioDefaultSave.error || settingsQuery.error || catalogQuery.error
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
        <div className="grid h-10 w-10 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-primary)]">
          <Film size={20} />
        </div>
        <div>
          <h2 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">Media</h2>
          <p className="text-sm text-[var(--app-text-muted)]">Configure workspace source media folders and AI models for generation, editing, and understanding.</p>
        </div>
      </header>

      {/* 1. TOP SECTION: Source Media Folder */}
      <Card className="p-5 sm:p-6">
        <section aria-labelledby="source-media-title" className="space-y-4">
          <div className="flex items-start justify-between gap-3">
            <div className="flex items-start gap-3">
              <FolderOpen size={18} className="mt-1 text-[var(--app-primary)]" />
              <div>
                <h3 id="source-media-title" className="text-lg font-semibold text-[var(--app-text)]">Source media folders</h3>
                <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                  Designate folders where you store source videos for {workspaceName || 'this workspace'}. AI reads and transcribes these files with read-only access—Swarm never modifies, renames, or deletes your source files.
                </p>
              </div>
            </div>
            {workspaceName ? (
              <span className="hidden shrink-0 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-1 text-xs text-[var(--app-text-subtle)] sm:inline-block">
                Workspace scoped
              </span>
            ) : null}
          </div>

          {!workspaceSlug && !requestedWorkspacePath.trim() ? (
            <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4 text-sm text-[var(--app-warning)]">
              Source media folders are workspace-scoped. Open a workspace, then choose Settings → Media.
            </div>
          ) : workspaceLoading ? (
            <p className="text-sm text-[var(--app-text-muted)]">Loading workspace…</p>
          ) : workspacePath ? (
            <>
              {sourceQuery.isPending ? (
                <p className="text-sm text-[var(--app-text-muted)]">Loading designated folders…</p>
              ) : folders.length ? (
                <div className="grid gap-2">
                  {folders.map((folder) => (
                    <div key={folder} className="flex min-w-0 items-center justify-between gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3.5 py-2.5">
                      <div className="flex min-w-0 items-center gap-2.5">
                        <Folder size={15} className="shrink-0 text-[var(--app-text-muted)]" />
                        <span className="min-w-0 truncate font-mono text-xs text-[var(--app-text)]" title={folder}>{folder}</span>
                      </div>
                      <Button variant="ghost" size="sm" disabled={removeFolder.isPending} onClick={() => removeFolder.mutate(folder)} className="text-[var(--app-text-muted)] hover:text-[var(--app-danger)]">
                        Remove
                      </Button>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="flex items-center gap-3 rounded-xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
                  <FolderOpen size={18} className="shrink-0 text-[var(--app-text-subtle)]" />
                  <span>No source media folder designated yet. Add a folder below so AI can access and transcribe videos.</span>
                </div>
              )}

              <form className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_auto]" onSubmit={(event) => { event.preventDefault(); const path = folderDraft.trim(); if (path) addFolder.mutate(path) }}>
                <Input value={folderDraft} onChange={(event) => setFolderDraft(event.target.value)} placeholder="Choose or enter a folder path" aria-label="Source media folder path" />
                <Button type="button" variant="outline" onClick={() => { setFolderPickerOpen(true); folderBrowse.mutate(folderDraft.trim() || workspacePath) }}>
                  <FolderOpen size={14} className="mr-1.5" />
                  Browse
                </Button>
                <Button type="submit" variant="outline" disabled={!folderDraft.trim() || addFolder.isPending}>
                  {addFolder.isPending ? 'Adding…' : 'Add folder'}
                </Button>
              </form>

              {folderPickerOpen ? (
                <div className="space-y-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3.5">
                  <div className="flex items-center justify-between gap-2 border-b border-[var(--app-border)] pb-2.5">
                    <span className="min-w-0 truncate font-mono text-xs text-[var(--app-text)]" title={folderBrowser?.resolvedPath}>
                      {folderBrowser?.resolvedPath || 'Loading folders…'}
                    </span>
                    <div className="flex gap-2">
                      <Button type="button" variant="ghost" size="sm" disabled={!folderBrowser?.parentPath || folderBrowse.isPending} onClick={() => folderBrowser?.parentPath && folderBrowse.mutate(folderBrowser.parentPath)}>
                        <ArrowUp size={14} className="mr-1" /> Up
                      </Button>
                      <Button type="button" variant="ghost" size="sm" onClick={() => setFolderPickerOpen(false)}>
                        Close
                      </Button>
                    </div>
                  </div>
                  {folderBrowseError ? <p className="text-sm text-[var(--app-danger)]">{folderBrowseError}</p> : null}
                  <div className="grid max-h-64 gap-1 overflow-auto">
                    {folderBrowser?.entries.filter((entry) => entry.isDirectory).map((entry) => (
                      <div key={entry.path} className="flex items-center justify-between gap-2 rounded-lg px-2.5 py-1.5 hover:bg-[var(--app-surface-hover)]">
                        <button type="button" className="flex min-w-0 flex-1 items-center gap-2 text-left text-sm text-[var(--app-text)]" onClick={() => folderBrowse.mutate(entry.path)}>
                          <Folder size={14} className="shrink-0 text-[var(--app-primary)]" />
                          <span className="truncate">{entry.name}</span>
                        </button>
                        <Button type="button" variant="ghost" size="sm" disabled={addFolder.isPending} onClick={() => { setFolderDraft(entry.path); setFolderPickerOpen(false); addFolder.mutate(entry.path) }}>
                          Choose & save
                        </Button>
                      </div>
                    ))}
                  </div>
                  {folderBrowser?.resolvedPath ? (
                    <Button type="button" size="sm" disabled={addFolder.isPending} onClick={() => { setFolderDraft(folderBrowser.resolvedPath); setFolderPickerOpen(false); addFolder.mutate(folderBrowser.resolvedPath) }}>
                      Choose & save this folder
                    </Button>
                  ) : null}
                </div>
              ) : null}
            </>
          ) : null}

          {folderError ? <div role="alert" className="text-sm text-[var(--app-danger)]">{errorMessage(folderError, 'Source media folders are unavailable.')}</div> : null}
        </section>
      </Card>

      {/* 2. MIDDLE SECTION: Unified Media Models */}
      <Card className="p-5 sm:p-6">
        <section aria-labelledby="media-models-title" className="space-y-5">
          <div className="flex items-start justify-between gap-3">
            <div className="flex items-start gap-3">
              <Sparkles size={18} className="mt-1 text-[var(--app-primary)]" />
              <div>
                <h3 id="media-models-title" className="text-lg font-semibold text-[var(--app-text)]">Media models</h3>
                <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                  Configure default generative AI and understanding models for images, video creation, video iteration, and transcription.
                </p>
              </div>
            </div>
            <span className="shrink-0 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-1 text-xs text-[var(--app-text-subtle)]">
              Auto-saves
            </span>
          </div>

          <div className="grid gap-4 md:grid-cols-2">
            {/* Image Generation */}
            <div className="flex flex-col justify-between rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
              <section aria-labelledby="image-model-title" className="space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <Image size={16} className="text-[var(--app-primary)]" />
                    <h4 id="image-model-title" className="text-sm font-semibold text-[var(--app-text)]">Image generation</h4>
                  </div>
                  <span className="rounded bg-[var(--app-surface)] px-2 py-0.5 text-xs text-[var(--app-text-subtle)]">Image</span>
                </div>
                <p className="text-xs text-[var(--app-text-muted)]">
                  Used for managed image generation and multi-variant image swarms in chat.
                </p>
                {settingsQuery.isPending || catalogQuery.isPending ? (
                  <p className="text-sm text-[var(--app-text-muted)]">Loading image models…</p>
                ) : (
                  <div className="space-y-1.5">
                    <span className="text-xs font-medium text-[var(--app-text)]">Default image model</span>
                    <ModelSelect ariaLabel="Default image model" models={imageModels} value={selectedImage} disabled={imageSave.isPending} onChange={(value) => imageSave.mutate(value)} />
                  </div>
                )}
                {selectedImageOption && !selectedImageOption.ready ? (
                  <p className="text-xs text-[var(--app-warning)]">
                    {selectedImageOption.reason || `${providerLabel(selectedImageOption.provider)} needs authentication before it can generate images.`}
                  </p>
                ) : null}
                {imageSave.isSuccess ? <p className="text-xs text-[var(--app-success)]">Image model saved.</p> : null}
              </section>
            </div>

            {/* Base Video Generation */}
            <div className="flex flex-col justify-between rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
              <section aria-labelledby="video-generation-title" className="space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <Video size={16} className="text-[var(--app-primary)]" />
                    <h4 id="video-generation-title" className="text-sm font-semibold text-[var(--app-text)]">Base video generation</h4>
                  </div>
                  <span className="rounded bg-[var(--app-surface)] px-2 py-0.5 text-xs text-[var(--app-text-subtle)]">Initial scenes</span>
                </div>
                <p className="text-xs text-[var(--app-text-muted)]">
                  Used when generating new video clips from text prompts or still images (e.g. Veo 3.1).
                </p>
                <div className="space-y-1.5">
                  <span className="text-xs font-medium text-[var(--app-text)]">Base video generation model</span>
                  <ModelSelect ariaLabel="Base video generation model" models={videoGenerationModels} value={selectedVideoDefault} disabled={videoDefaultSave.isPending} onChange={(value) => videoDefaultSave.mutate(value)} />
                </div>
                {selectedVideoDefaultOption && !selectedVideoDefaultOption.ready ? (
                  <p className="text-xs text-[var(--app-warning)]">
                    {selectedVideoDefaultOption.reason || `${providerLabel(selectedVideoDefaultOption.provider)} needs authentication before it can generate videos.`}
                  </p>
                ) : null}
                {videoDefaultSave.isSuccess ? <p className="text-xs text-[var(--app-success)]">Generation model saved.</p> : null}
              </section>
            </div>

            {/* Video Iteration */}
            <div className="flex flex-col justify-between rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
              <section aria-labelledby="video-iteration-title" className="space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <Sparkles size={16} className="text-[var(--app-primary)]" />
                    <h4 id="video-iteration-title" className="text-sm font-semibold text-[var(--app-text)]">Video iteration & remixing</h4>
                  </div>
                  <span className="rounded bg-[var(--app-surface)] px-2 py-0.5 text-xs text-[var(--app-text-subtle)]">Conversational</span>
                </div>
                <p className="text-xs text-[var(--app-text-muted)]">
                  Used when modifying or refining existing video clips while preserving scene consistency.
                </p>
                <div className="space-y-1.5">
                  <span className="text-xs font-medium text-[var(--app-text)]">Video iteration model</span>
                  <ModelSelect ariaLabel="Video iteration model" models={videoIterationModels} value={selectedVideoIteration} disabled={videoIterationSave.isPending} onChange={(value) => videoIterationSave.mutate(value)} />
                </div>
                {selectedVideoIterationOption && !selectedVideoIterationOption.ready ? (
                  <p className="text-xs text-[var(--app-warning)]">
                    {selectedVideoIterationOption.reason || `${providerLabel(selectedVideoIterationOption.provider)} needs authentication before it can edit videos.`}
                  </p>
                ) : null}
                {videoIterationSave.isSuccess ? <p className="text-xs text-[var(--app-success)]">Iteration model saved.</p> : null}
                {selectedVideoIteration.toLowerCase().includes('omni') ? (
                  <div className="mt-2 flex items-start gap-2 rounded-lg border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-2.5 text-xs leading-5 text-[var(--app-warning)]">
                    <AlertTriangle size={15} className="mt-0.5 shrink-0" />
                    <div>
                      <span className="font-semibold">Regional Notice (EU / EEA / UK / Switzerland): </span>
                      Google restricts editing uploaded external video files in these regions. Set your primary Video Generation model to Gemini Omni Flash as well to generate and iterate natively in the same multi-turn conversation.
                    </div>
                  </div>
                ) : null}
              </section>
            </div>

            {/* Music & Audio Generation */}
            <div className="flex flex-col justify-between rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
              <section aria-labelledby="audio-generation-title" className="space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <Music size={16} className="text-[var(--app-primary)]" />
                    <h4 id="audio-generation-title" className="text-sm font-semibold text-[var(--app-text)]">Music & audio generation</h4>
                  </div>
                  <span className="rounded bg-[var(--app-surface)] px-2 py-0.5 text-xs text-[var(--app-text-subtle)]">Soundtrack</span>
                </div>
                <p className="text-xs text-[var(--app-text-muted)]">
                  Used when generating soundtracks, musical tracks, and sound clips (e.g. Lyria 3.5, Lyria 3 Clip Preview).
                </p>
                <div className="space-y-1.5">
                  <span className="text-xs font-medium text-[var(--app-text)]">Music & audio model</span>
                  <ModelSelect ariaLabel="Music & audio model" models={audioModels} value={selectedAudioDefault} disabled={audioDefaultSave.isPending} onChange={(value) => audioDefaultSave.mutate(value)} />
                </div>
                {selectedAudioDefaultOption && !selectedAudioDefaultOption.ready ? (
                  <p className="text-xs text-[var(--app-warning)]">
                    {selectedAudioDefaultOption.reason || `${providerLabel(selectedAudioDefaultOption.provider)} needs authentication before it can generate audio.`}
                  </p>
                ) : null}
                {audioDefaultSave.isSuccess ? <p className="text-xs text-[var(--app-success)]">Audio model saved.</p> : null}
              </section>
            </div>

            {/* Video Understanding & Transcription */}
            <div className="flex flex-col justify-between rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
              <section aria-labelledby="transcription-model-title" className="space-y-3">
                <div className="flex items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <FileAudio size={16} className="text-[var(--app-primary)]" />
                    <h4 id="transcription-model-title" className="text-sm font-semibold text-[var(--app-text)]">Video understanding</h4>
                  </div>
                  <span className="rounded bg-[var(--app-surface)] px-2 py-0.5 text-xs text-[var(--app-text-subtle)]">Transcription</span>
                </div>
                <p className="text-xs text-[var(--app-text-muted)]">
                  Analyzes a video’s visuals and embedded audio to produce timestamped multimodal transcripts.
                </p>
                {catalogQuery.isPending ? (
                  <p className="text-sm text-[var(--app-text-muted)]">Loading qualified Google models…</p>
                ) : transcriptionModels.length ? (
                  <div className="space-y-1.5">
                    <span className="text-xs font-medium text-[var(--app-text)]">Transcription model</span>
                    <ModelSelect ariaLabel="Transcription model" models={transcriptionModels} value={selectedTranscription} disabled={transcriptionSave.isPending} onChange={(value) => transcriptionSave.mutate(value)} />
                  </div>
                ) : (
                  <p className="text-sm text-[var(--app-text-muted)]">No catalog models currently qualify for video-to-text understanding.</p>
                )}
                {selectedTranscriptionOption && !selectedTranscriptionOption.ready ? (
                  <p className="text-xs text-[var(--app-warning)]">
                    {selectedTranscriptionOption.reason || 'Google authentication is required before this model can be used.'}
                  </p>
                ) : null}
                {transcriptionSave.isSuccess ? <p className="text-xs text-[var(--app-success)]">Transcription model saved.</p> : null}
              </section>
            </div>
          </div>

          <div className="flex items-start gap-2.5 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3.5 text-xs text-[var(--app-text-muted)]">
            <Info size={16} className="mt-0.5 shrink-0 text-[var(--app-primary)]" />
            <div className="space-y-1">
              <p className="font-medium text-[var(--app-text)]">How media models work:</p>
              <p>
                Selected default models are used automatically by the AI when generating images, producing soundtracks and music, creating video clips, or transcribing video sources. Video iteration requests automatically route to your configured iteration model to edit scenes with visual consistency.
              </p>
            </div>
          </div>
        </section>
      </Card>

      {/* 3. BOTTOM SECTION: Video Transcription & Analysis Tool */}
      <Card className="p-5 sm:p-6">
        <section aria-labelledby="transcribe-video-title" className="space-y-4">
          <div className="flex items-start gap-3">
            <FileAudio size={18} className="mt-1 text-[var(--app-primary)]" />
            <div>
              <h3 id="transcribe-video-title" className="text-lg font-semibold text-[var(--app-text)]">Transcribe videos</h3>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">
                Browse inside designated source folders, select up to {DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT} videos, and generate durable timestamped transcripts for AI inspection.
              </p>
            </div>
          </div>

          {!workspacePath ? (
            <p className="text-sm text-[var(--app-text-muted)]">Open a workspace to browse source videos.</p>
          ) : !folders.length ? (
            <p className="text-sm text-[var(--app-warning)]">Add a source media folder at the top of this page first.</p>
          ) : !selectedTranscription || selectedTranscriptionOption?.ready !== true ? (
            <p className="text-sm text-[var(--app-warning)]">Choose a ready transcription model in the Media Models section above before starting.</p>
          ) : (
            <>
              <div className="space-y-2">
                <span className="text-xs font-medium text-[var(--app-text-muted)]">Select designated folder</span>
                <div className="grid gap-2 sm:grid-cols-2">
                  {folders.map((folder) => (
                    <button
                      type="button"
                      key={folder}
                      className={`flex min-w-0 items-center gap-2 rounded-xl border px-3 py-2.5 text-left text-sm transition ${
                        transcriptionRoot === folder
                          ? 'border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]'
                          : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] hover:border-[var(--app-border-strong)]'
                      }`}
                      onClick={() => {
                        setTranscriptionRoot(folder)
                        setTranscriptionRelativePath('.')
                        browseVideos.mutate({ rootPath: folder, relativePath: '.' })
                      }}
                    >
                      <FolderOpen size={16} className="shrink-0 text-[var(--app-text-muted)]" />
                      <span className="min-w-0 truncate font-mono text-xs text-[var(--app-text)]" title={folder}>{folder}</span>
                      <ChevronRight size={14} className="ml-auto shrink-0 text-[var(--app-text-subtle)]" />
                    </button>
                  ))}
                </div>
              </div>

              {browseVideos.isPending ? (
                <p className="text-sm text-[var(--app-text-muted)]">Scanning videos…</p>
              ) : transcriptionRoot ? (
                <div className="space-y-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3.5">
                  <div className="flex items-center justify-between gap-2 border-b border-[var(--app-border)] pb-2">
                    <span className="min-w-0 truncate font-mono text-xs text-[var(--app-text)]" title={transcriptionRelativePath}>
                      {transcriptionRelativePath === '.' ? 'Folder root' : transcriptionRelativePath}
                    </span>
                    <Button type="button" variant="ghost" size="sm" disabled={transcriptionRelativePath === '.'} onClick={() => browseVideos.mutate({ rootPath: transcriptionRoot, relativePath: parentMediaRelativePath(transcriptionRelativePath) })}>
                      <ArrowUp size={14} className="mr-1" /> Up
                    </Button>
                  </div>
                  <div className="grid gap-1">
                    {videoDirectories.map((directory) => (
                      <button
                        type="button"
                        key={directory.relative_path}
                        className="flex items-center gap-2 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]"
                        onClick={() => browseVideos.mutate({ rootPath: transcriptionRoot, relativePath: directory.relative_path })}
                      >
                        <Folder size={15} className="shrink-0 text-[var(--app-primary)]" />
                        <span className="truncate">{directory.name}</span>
                        <ChevronRight size={14} className="ml-auto text-[var(--app-text-subtle)]" />
                      </button>
                    ))}
                    {videoOptions.map((video) => {
                      const selected = videoRefs.has(video.ref)
                      const disabled = !selected && videoRefs.size >= DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT
                      return (
                        <button
                          type="button"
                          key={video.ref}
                          disabled={disabled}
                          className="flex items-center gap-2 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:opacity-50"
                          onClick={() => setVideoRefs((current) => {
                            const next = new Set(current)
                            if (next.has(video.ref)) next.delete(video.ref)
                            else next.add(video.ref)
                            return next
                          })}
                        >
                          <span className={`flex size-4 shrink-0 items-center justify-center rounded border ${selected ? 'border-[var(--app-border-accent)] bg-[var(--app-primary)] text-[var(--app-bg)]' : 'border-[var(--app-border)] text-transparent'}`}>
                            <Check size={12} />
                          </span>
                          <Video size={15} className="shrink-0 text-[var(--app-text-muted)]" />
                          <span className="truncate">{video.name}</span>
                          {video.transcriptRef ? <span className="ml-auto rounded bg-[var(--app-success-bg)] px-2 py-0.5 text-xs text-[var(--app-success)]">Transcript ready</span> : null}
                        </button>
                      )
                    })}
                    {!videoDirectories.length && !videoOptions.length ? <p className="px-2 py-3 text-sm text-[var(--app-text-muted)]">No supported videos in this folder.</p> : null}
                  </div>
                  <p className="text-xs text-[var(--app-text-subtle)]">{videoRefs.size} selected · maximum {DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT}</p>
                </div>
              ) : null}

              <label className="block space-y-1.5">
                <span className="text-xs font-medium text-[var(--app-text)]">Optional focus notes</span>
                <Textarea value={focusNotes} rows={2} onChange={(event) => setFocusNotes(truncateVideoFocusNotes(event.target.value))} placeholder="For example: pay special attention to the UI steps shown in the demo." />
                <span className="text-xs text-[var(--app-text-subtle)]">{focusNotesBytes}/{VIDEO_FOCUS_NOTES_MAX_BYTES} bytes. Notes guide transcript emphasis without changing the schema.</span>
              </label>

              <div className="flex flex-wrap gap-2">
                <Button
                  disabled={!videoRefs.size || transcribeVideo.isPending || transcriptionJobs.some((job) => !isTerminalVideoTranscriptionStatus(job.status))}
                  onClick={() => transcribeVideo.mutate()}
                >
                  {transcribeVideo.isPending ? 'Starting…' : `Analyze and transcribe ${videoRefs.size || ''} ${videoRefs.size === 1 ? 'video' : 'videos'}`}
                </Button>
              </div>

              {transcriptionJobs.length ? (
                <div aria-live="polite" className="grid gap-2">
                  {transcriptionJobs.map((job, index) => (
                    <div key={job.ref} className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text-muted)]">
                      <span>Video {index + 1}: <span className="font-medium capitalize text-[var(--app-text)]">{job.status}</span>{job.status === 'ready' ? ' — saved and ready for AI read-back.' : job.status === 'processing' || job.status === 'partial' ? ' — analyzing audio and visuals.' : ''}</span>
                      {!isTerminalVideoTranscriptionStatus(job.status) ? (
                        <Button variant="ghost" size="sm" disabled={cancelTranscription.isPending} onClick={() => cancelTranscription.mutate(job.ref)}>Cancel</Button>
                      ) : null}
                    </div>
                  ))}
                </div>
              ) : null}

              {Object.values(transcripts).map((transcript, transcriptIndex) => (
                <div key={transcript.ref} className="space-y-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div>
                      <h4 className="font-semibold text-[var(--app-text)]">Saved transcript {Object.keys(transcripts).length > 1 ? transcriptIndex + 1 : ''}</h4>
                      <p className="mt-0.5 break-all font-mono text-xs text-[var(--app-text-subtle)]">{transcript.ref}</p>
                    </div>
                    <span className="rounded bg-[var(--app-success-bg)] px-2 py-0.5 text-xs text-[var(--app-success)]">{transcript.validation.state}</span>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button variant="outline" size="sm" onClick={() => { void copyTranscriptValue(transcript.text, 'Transcript copied.') }}>Copy transcript</Button>
                    <Button variant="outline" size="sm" onClick={() => { void copyTranscriptValue(transcript.ref, 'Transcript reference copied.') }}>Copy reference for AI</Button>
                  </div>
                  {transcriptCopyStatus ? <p aria-live="polite" className="text-xs text-[var(--app-text-muted)]">{transcriptCopyStatus}</p> : null}
                  {transcript.details_truncated ? <p className="text-sm text-[var(--app-warning)]">This preview is bounded. The full transcript remains durably saved and available by its transcript reference.</p> : null}
                  <pre className="max-h-80 overflow-auto rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3 whitespace-pre-wrap font-mono text-xs leading-5 text-[var(--app-text)]">{transcript.text}</pre>
                  {transcript.segments.length ? (
                    <div className="space-y-2">
                      <h5 className="text-xs font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Multimodal timeline</h5>
                      {transcript.segments.map((segment, index) => (
                        <div key={`${segment.start_ms}-${segment.end_ms}-${index}`} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                          <div className="text-xs font-medium text-[var(--app-text-subtle)]">{formatTimelineRange(segment.start_ms, segment.end_ms)}</div>
                          <div className="mt-1.5 grid gap-1 text-xs text-[var(--app-text)]">
                            {transcriptSegmentDetails(segment).map((detail) => (
                              <p key={detail.label}><strong>{detail.label}:</strong> {detail.value}</p>
                            ))}
                          </div>
                        </div>
                      ))}
                    </div>
                  ) : null}
                </div>
              ))}

              {transcriptionError || browseVideos.error || transcribeVideo.error || cancelTranscription.error ? (
                <div role="alert" className="text-sm text-[var(--app-danger)]">
                  {transcriptionError || errorMessage(browseVideos.error || transcribeVideo.error || cancelTranscription.error, 'Video transcription is unavailable.')}
                </div>
              ) : null}
            </>
          )}
        </section>
      </Card>

      {settingsError ? <div role="alert" className="text-sm text-[var(--app-danger)]">{errorMessage(settingsError, 'Media settings are unavailable.')}</div> : null}
      <p className="text-center text-xs text-[var(--app-text-subtle)]">Model changes save automatically and apply to future media operations.</p>
    </div>
  )
}
