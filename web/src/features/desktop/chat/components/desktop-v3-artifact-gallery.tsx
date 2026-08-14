import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import {
  AlertTriangle,
  ArrowLeft,
  Check,
  ChevronLeft,
  ChevronRight,
  Clock3,
  Download,
  FileText,
  GalleryHorizontal,
  Loader2,
  Maximize2,
  MessageSquarePlus,
  Minimize2,
  Search,
  Sparkles,
  X,
} from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import {
  buildDesktopV3ArtifactSandboxDocument,
  DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT,
  desktopV3ArtifactCatalogEntryForKey,
  desktopV3ArtifactCatalogEntryKey,
  desktopV3ArtifactSelection,
  fetchDesktopV3Artifact,
  fetchDesktopV3ArtifactBundle,
  fetchDesktopV3ArtifactCatalog,
  fetchDesktopV3ArtifactPreviewToken,
  formatDesktopV3ArtifactOutputRequirements,
  useDesktopV3Artifact,
  type DesktopV3ArtifactCatalogEntry,
  type DesktopV3ArtifactCollectionProgress,
  type DesktopV3ArtifactSelection,
} from '../../session-v3/artifact-api'
import { useDesktopV3OpenArtifactCatalogRefresh } from '../../session-v3/use-artifact-catalog-refresh'

export type DesktopV3ArtifactGalleryEntry = DesktopV3ArtifactCatalogEntry

/** A visible label paired with the opaque authority reference sent to chat. */
export interface DesktopV3ArtifactChatSelection {
  label: string
  selection: DesktopV3ArtifactSelection
}

export interface DesktopV3ArtifactGalleryProps {
  artifacts: DesktopV3ArtifactGalleryEntry[]
  open?: boolean
  onOpenChange?: (open: boolean) => void
  onAddToChat?: (artifacts: DesktopV3ArtifactChatSelection[]) => void | Promise<void>
  onUseThisDesign?: (artifact: DesktopV3ArtifactChatSelection) => void | Promise<void>
  onSelectionPersisted?: () => void | Promise<void>
  showTrigger?: boolean
  loading?: boolean
  error?: string
  title?: string
  initialArtifactKey?: string
  artifactHref?: (artifact: DesktopV3ArtifactGalleryEntry) => string
  collectionHref?: (artifact: DesktopV3ArtifactGalleryEntry) => string
  onArtifactNavigate?: (artifact: DesktopV3ArtifactGalleryEntry) => void
  onCollectionNavigate?: (artifact: DesktopV3ArtifactGalleryEntry) => void
}

type ArtifactCollectionGroup = {
  key: string
  entries: DesktopV3ArtifactGalleryEntry[]
  progress: DesktopV3ArtifactCollectionProgress
  sessionLabel: string
  workspaceLabel: string
}

function artifactBundleDownloadName(artifact: DesktopV3ArtifactGalleryEntry): string {
  const label = artifact.label.trim().replace(/[^a-z0-9._-]+/gi, '-').replace(/^-+|-+$/g, '') || 'artifact'
  const base = label.replace(/\.[a-z0-9]{1,8}$/i, '') || 'artifact'
  return `${base}.zip`
}

function artifactSelectionKey(artifact: DesktopV3ArtifactGalleryEntry): string {
  return desktopV3ArtifactCatalogEntryKey(artifact)
}

function artifactCollectionKey(artifact: DesktopV3ArtifactGalleryEntry): string {
  return artifact.collectionId
    ? `${artifact.sessionId}:${artifact.collectionId}`
    : `standalone:${artifact.sessionId}:${artifact.artifactId}`
}

function artifactTypeLabel(artifact: DesktopV3ArtifactGalleryEntry): string {
  return artifact.kind.trim() || artifact.mediaType.trim() || 'artifact'
}

function artifactWorkspaceLabel(artifact: DesktopV3ArtifactGalleryEntry): string {
  return artifact.workspaceName.trim() || artifact.workspacePath.trim() || 'Workspace'
}

function collectionProgress(entries: DesktopV3ArtifactGalleryEntry[]): DesktopV3ArtifactCollectionProgress {
  const reported = entries.find((entry) => entry.progress)?.progress
  if (reported) return reported
  return entries.reduce<DesktopV3ArtifactCollectionProgress>((progress, entry) => {
    progress.total += 1
    if (entry.status === 'staging') progress.staging += 1
    else if (entry.status === 'failed') progress.failed += 1
    else if (entry.status === 'unavailable') progress.unavailable += 1
    else progress.ready += 1
    return progress
  }, { total: 0, staging: 0, ready: 0, failed: 0, unavailable: 0 })
}

function collectionDisplayLabel(group: ArtifactCollectionGroup): string {
  const first = group.entries[0]
  if (!first?.collectionId) return first?.label || 'Artifact'
  if (first.collectionName) return first.collectionName
  if (first.lineage?.iterationGroup) return `${first.lineage.iterationGroup} iterations`
  if (first.lineage?.programId) return first.lineage.programId.replace(/[-_]+/g, ' ')
  if (first.lineage?.iterationGroupId || first.lineage?.taskCallId) return 'Designer iteration group'
  return first.sessionTitle ? `${first.sessionTitle} designs` : 'Design collection'
}

function iterationDisplayLabel(artifact: DesktopV3ArtifactGalleryEntry, index: number): string {
  const progression = artifact.lineage?.iterationIndex || index + 1
  const specialized = artifact.lineage?.iterationLabel || artifact.lineage?.iterationTheme
  return specialized ? `${progression}. ${specialized}` : `${progression}. ${artifact.label}`
}

function artifactStatusLabel(artifact: DesktopV3ArtifactGalleryEntry): string {
  if (artifact.status === 'staging') return 'Generating'
  if (artifact.status === 'failed') return 'Failed'
  if (artifact.status === 'unavailable') return 'Unavailable'
  if (artifact.status === 'ready') return 'Ready'
  return ''
}

function collectionGroups(entries: DesktopV3ArtifactGalleryEntry[]): ArtifactCollectionGroup[] {
  const grouped = new Map<string, DesktopV3ArtifactGalleryEntry[]>()
  for (const entry of entries) {
    const key = artifactCollectionKey(entry)
    grouped.set(key, [...(grouped.get(key) ?? []), entry])
  }
  return [...grouped.entries()].map(([key, collectionEntries]) => ({
    key,
    entries: collectionEntries,
    progress: collectionProgress(collectionEntries),
    sessionLabel: collectionEntries[0]?.sessionTitle || 'Session artifacts',
    workspaceLabel: collectionEntries[0] ? artifactWorkspaceLabel(collectionEntries[0]) : 'Workspace',
  }))
}

export function DesktopV3ArtifactGallery({
  artifacts,
  open: controlledOpen,
  onOpenChange,
  onAddToChat,
  onUseThisDesign,
  onSelectionPersisted,
  showTrigger = true,
  loading: catalogLoading = false,
  error: catalogError = '',
  title = 'Artifact review',
  initialArtifactKey = '',
  artifactHref,
  collectionHref,
  onArtifactNavigate,
  onCollectionNavigate,
}: DesktopV3ArtifactGalleryProps) {
  const [internalOpen, setInternalOpen] = useState(false)
  const [selectedId, setSelectedId] = useState(artifacts[0] ? artifactSelectionKey(artifacts[0]) : '')
  const [chatSelectedIds, setChatSelectedIds] = useState<string[]>([])
  const [durableSelectedId, setDurableSelectedId] = useState('')
  const [previewURL, setPreviewURL] = useState('')
  const [previewText, setPreviewText] = useState('')
  const [previewError, setPreviewError] = useState('')
  const [previewLoading, setPreviewLoading] = useState(false)
  const [actionPending, setActionPending] = useState<'add' | 'use' | ''>('')
  const [actionError, setActionError] = useState('')
  const [query, setQuery] = useState('')
  const [organization, setOrganization] = useState<'collection' | 'workspace'>('collection')
  const galleryButtonRef = useRef<HTMLButtonElement>(null)
  const backButtonRef = useRef<HTMLButtonElement>(null)
  const previewSurfaceRef = useRef<HTMLDivElement>(null)
  const [previewFullscreen, setPreviewFullscreen] = useState(false)
  const open = controlledOpen ?? internalOpen
  const setOpen = (next: boolean) => {
    if (controlledOpen === undefined) setInternalOpen(next)
    onOpenChange?.(next)
  }

  const visibleArtifacts = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase()
    if (!normalizedQuery) return artifacts
    return artifacts.filter((artifact) => [
      artifact.label,
      artifact.description,
      artifact.filename,
      artifact.mediaType,
      artifact.kind,
      formatDesktopV3ArtifactOutputRequirements(artifact.outputRequirements),
      artifact.sessionTitle,
      artifact.workspaceName,
      artifact.workspacePath,
      artifact.collectionId ?? '',
      artifact.lineage?.programId ?? '',
      artifact.lineage?.taskCallId ?? '',
    ].some((value) => value.toLowerCase().includes(normalizedQuery)))
  }, [artifacts, query])

  const groups = useMemo(() => collectionGroups(visibleArtifacts), [visibleArtifacts])
  const selected = visibleArtifacts.find((artifact) => artifactSelectionKey(artifact) === selectedId) ?? visibleArtifacts[0]
  const selectedGroupKey = selected ? artifactCollectionKey(selected) : ''
  const selectedGroup = groups.find((group) => group.key === selectedGroupKey)
  const selectedVariants = selectedGroup?.entries ?? []
  const selectedVariantIndex = selected
    ? selectedVariants.findIndex((artifact) => artifactSelectionKey(artifact) === artifactSelectionKey(selected))
    : -1
  const persistedSelectedArtifact = artifacts.find((artifact) => artifact.selected)
  const canonicalSelectedId = durableSelectedId || (persistedSelectedArtifact ? artifactSelectionKey(persistedSelectedArtifact) : '')
  const selectedIsCanonical = Boolean(selected && artifactSelectionKey(selected) === canonicalSelectedId)
  const selectedCanAttach = selected?.status === 'ready' && Boolean(selected.collectionId) && (selected.eventSeq ?? 0) > 0
  const selectedIsQueuedForChat = Boolean(selected && chatSelectedIds.includes(artifactSelectionKey(selected)))
  const selectedRequirementLabel = formatDesktopV3ArtifactOutputRequirements(selected?.outputRequirements)
  const attachableSelectedArtifacts = artifacts.filter((artifact) => chatSelectedIds.includes(artifactSelectionKey(artifact))
    && artifact.status === 'ready'
    && Boolean(artifact.collectionId)
    && (artifact.eventSeq ?? 0) > 0)
  const pendingChatArtifacts = attachableSelectedArtifacts.length > 0
    ? attachableSelectedArtifacts
    : selectedCanAttach && selected ? [selected] : []

  useEffect(() => {
    const persisted = artifacts.find((artifact) => artifact.selected)
    setDurableSelectedId(persisted ? artifactSelectionKey(persisted) : '')
    const availableKeys = new Set(artifacts.map(artifactSelectionKey))
    setChatSelectedIds((current) => current.filter((id) => availableKeys.has(id)))
  }, [artifacts])

  useEffect(() => {
    if (open) return
    setChatSelectedIds([])
    setActionError('')
  }, [open])

  useEffect(() => {
    if (!open || !initialArtifactKey) return
    const requested = desktopV3ArtifactCatalogEntryForKey(artifacts, initialArtifactKey)
    if (requested) setSelectedId(artifactSelectionKey(requested))
  }, [artifacts, initialArtifactKey, open])

  useEffect(() => {
    if (!open) return undefined
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    backButtonRef.current?.focus()
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        if (!document.fullscreenElement) setOpen(false)
        return
      }
      if (event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement) return
      if ((event.key === 'ArrowLeft' || event.key === 'ArrowRight') && selectedVariants.length > 1 && selectedVariantIndex >= 0) {
        event.preventDefault()
        const offset = event.key === 'ArrowLeft' ? -1 : 1
        const nextIndex = (selectedVariantIndex + offset + selectedVariants.length) % selectedVariants.length
        const nextArtifact = selectedVariants[nextIndex]
        if (nextArtifact) {
          setSelectedId(artifactSelectionKey(nextArtifact))
          onArtifactNavigate?.(nextArtifact)
        }
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => {
      window.removeEventListener('keydown', handleKeyDown)
      document.body.style.overflow = previousOverflow
      galleryButtonRef.current?.focus()
    }
  }, [onArtifactNavigate, open, selectedVariantIndex, selectedVariants])

  useEffect(() => {
    const syncFullscreenState = () => setPreviewFullscreen(document.fullscreenElement === previewSurfaceRef.current)
    document.addEventListener('fullscreenchange', syncFullscreenState)
    return () => document.removeEventListener('fullscreenchange', syncFullscreenState)
  }, [])

  useEffect(() => {
    if (open) return
    setPreviewFullscreen(false)
  }, [open])

  useEffect(() => {
    if (!open || !selected) return undefined
    setSelectedId(artifactSelectionKey(selected))
    setPreviewURL('')
    setPreviewText('')
    setPreviewError('')
    setActionError('')
    if (selected.content !== undefined) {
      setPreviewText(selected.content)
      setPreviewLoading(false)
      return undefined
    }
    if (!selected.previewable || selected.status === 'staging' || selected.status === 'failed' || selected.status === 'unavailable') {
      setPreviewLoading(false)
      return undefined
    }
    const controller = new AbortController()
    let objectURL = ''
    setPreviewLoading(true)
    void fetchDesktopV3Artifact(selected.sessionId, selected.artifactId, controller.signal)
      .then(async (blob) => {
        if (controller.signal.aborted) return
        if (selected.mediaType === 'text/html') {
          const [source, previewToken] = await Promise.all([
            blob.text(),
            fetchDesktopV3ArtifactPreviewToken(selected.sessionId, selected.artifactId, controller.signal),
          ])
          if (!controller.signal.aborted) {
            setPreviewText(buildDesktopV3ArtifactSandboxDocument(source, selected.sessionId, selected.artifactId, previewToken))
          }
          return
        }
        if (selected.mediaType === 'text/markdown' || selected.mediaType === 'text/plain') {
          const text = await blob.text()
          if (!controller.signal.aborted) setPreviewText(text)
          return
        }
        objectURL = URL.createObjectURL(blob)
        setPreviewURL(objectURL)
      })
      .catch((error) => {
        if (!controller.signal.aborted) setPreviewError(error instanceof Error ? error.message : 'Artifact preview failed')
      })
      .finally(() => {
        if (!controller.signal.aborted) setPreviewLoading(false)
      })
    return () => {
      controller.abort()
      if (objectURL) URL.revokeObjectURL(objectURL)
    }
  }, [open, selected?.artifactId, selected?.content, selected?.mediaType, selected?.previewable, selected?.sessionId, selected?.status])

  const selectArtifact = (artifact: DesktopV3ArtifactGalleryEntry) => {
    setSelectedId(artifactSelectionKey(artifact))
    onArtifactNavigate?.(artifact)
  }

  const selectAdjacentVariant = (offset: -1 | 1) => {
    if (selectedVariants.length < 2 || selectedVariantIndex < 0) return
    const nextIndex = (selectedVariantIndex + offset + selectedVariants.length) % selectedVariants.length
    const nextArtifact = selectedVariants[nextIndex]
    if (nextArtifact) selectArtifact(nextArtifact)
  }

  const selectCollection = (group: ArtifactCollectionGroup) => {
    const next = group.entries.find((entry) => entry.selected)
      ?? group.entries.find((entry) => entry.status === 'ready')
      ?? group.entries[0]
    if (!next) return
    setSelectedId(artifactSelectionKey(next))
    onCollectionNavigate?.(next)
  }

  const togglePreviewFullscreen = async () => {
    const previewSurface = previewSurfaceRef.current
    if (!previewSurface) return
    try {
      setActionError('')
      if (document.fullscreenElement === previewSurface) await document.exitFullscreen()
      else await previewSurface.requestFullscreen()
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not open the artifact preview fullscreen')
    }
  }

  const downloadArtifact = async (artifact: DesktopV3ArtifactGalleryEntry) => {
    try {
      setPreviewError('')
      const blob = await fetchDesktopV3ArtifactBundle(artifact.sessionId, artifact.artifactId)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = artifactBundleDownloadName(artifact)
      anchor.click()
      window.setTimeout(() => URL.revokeObjectURL(url), 0)
    } catch (error) {
      setPreviewError(error instanceof Error ? error.message : 'Artifact download failed')
    }
  }

  const toggleChatSelection = (artifact: DesktopV3ArtifactGalleryEntry) => {
    if (artifact.status !== 'ready' || !artifact.collectionId || (artifact.eventSeq ?? 0) <= 0) return
    const key = artifactSelectionKey(artifact)
    setActionError('')
    setChatSelectedIds((current) => {
      if (current.includes(key)) return current.filter((id) => id !== key)
      if (current.length >= DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT) {
        setActionError(`Select at most ${DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT} artifacts per message.`)
        return current
      }
      return [...current, key]
    })
  }

  const emitAddToChat = async () => {
    if (pendingChatArtifacts.length === 0 || !onAddToChat) return false
    try {
      setActionPending('add')
      setActionError('')
      await onAddToChat(pendingChatArtifacts.map((artifact) => ({
        label: artifact.label,
        selection: desktopV3ArtifactSelection(artifact),
      })))
      setChatSelectedIds([])
      setOpen(false)
      return true
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not add the artifacts to chat')
      return false
    } finally {
      setActionPending('')
    }
  }

  const persistAndUseDesign = async () => {
    if (!selected || !onUseThisDesign) return
    try {
      setActionPending('use')
      setActionError('')
      const canonicalSelection = await useDesktopV3Artifact(desktopV3ArtifactSelection(selected))
      setDurableSelectedId(artifactSelectionKey(selected))
      await onSelectionPersisted?.()
      await onUseThisDesign({ label: selected.label, selection: canonicalSelection })
      setOpen(false)
    } catch (error) {
      setActionError(error instanceof Error ? error.message : 'Could not use this design')
    } finally {
      setActionPending('')
    }
  }

  const renderCollection = (group: ArtifactCollectionGroup) => {
    const active = group.key === selectedGroupKey
    const partialFailure = group.progress.failed + group.progress.unavailable
    const requirementLabel = formatDesktopV3ArtifactOutputRequirements(group.entries[0]?.outputRequirements)
    return (
      <div key={group.key} className="min-w-0">
        <div
          className={cn(
            'w-64 shrink-0 rounded-xl border px-3 py-2.5 text-left transition md:w-full',
            active
              ? 'border-[var(--app-primary)] bg-[color-mix(in_srgb,var(--app-primary)_8%,var(--app-surface))]'
              : 'border-transparent hover:border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]',
          )}
        >
          <div className="flex min-w-0 items-center justify-between gap-2"><button type="button" className="min-w-0 flex-1 text-left" aria-expanded={active} onClick={() => selectCollection(group)}><span className="block truncate text-xs font-semibold text-[var(--app-text)]">{collectionDisplayLabel(group)}</span><span className="mt-0.5 block truncate text-[10px] text-[var(--app-text-subtle)]">{group.sessionLabel} · {group.progress.total} variant{group.progress.total === 1 ? '' : 's'}</span>{requirementLabel ? <span className="mt-0.5 block truncate text-[9px] text-[var(--app-text-muted)]" data-artifact-output-requirements>{requirementLabel}</span> : null}</button>{collectionHref && group.entries[0]?.collectionId ? <a href={collectionHref(group.entries[0])} className="shrink-0 rounded px-1.5 py-1 text-[9px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-primary-soft)]" aria-label={`Open unique URL for ${collectionDisplayLabel(group)}`}>Group URL</a> : null}</div>
          <button type="button" className="mt-2 flex w-full flex-wrap gap-1 text-left" aria-label="Collection progress" onClick={() => selectCollection(group)}>
            {group.progress.staging > 0 ? <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-primary-soft)] px-2 py-0.5 text-[9px] font-semibold text-[var(--app-primary)]"><Loader2 className="size-2.5 animate-spin" />{group.progress.staging} generating</span> : null}
            {group.progress.ready > 0 ? <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-success-bg)] px-2 py-0.5 text-[9px] font-semibold text-[var(--app-success)]"><Check className="size-2.5" />{group.progress.ready} ready</span> : null}
            {partialFailure > 0 ? <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-danger-bg)] px-2 py-0.5 text-[9px] font-semibold text-[var(--app-danger)]"><AlertTriangle className="size-2.5" />{partialFailure} failed</span> : null}
          </button>
        </div>
        {active ? (
          <div className="mt-1 grid gap-1 border-l border-[var(--app-border)] pl-2" aria-label="Collection variants">
            {group.entries.map((artifact, index) => {
              const artifactActive = selected && artifactSelectionKey(artifact) === artifactSelectionKey(selected)
              const canonical = artifactSelectionKey(artifact) === canonicalSelectedId
              const chatSelected = chatSelectedIds.includes(artifactSelectionKey(artifact))
              const canSelectForChat = artifact.status === 'ready' && Boolean(artifact.collectionId) && (artifact.eventSeq ?? 0) > 0
              return (
                <div
                  key={artifactSelectionKey(artifact)}
                  className={cn('flex min-w-0 items-center gap-1 rounded-lg transition', artifactActive ? 'bg-[var(--app-surface-active)] text-[var(--app-text)]' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]')}
                >
                  <button
                    type="button"
                    className="flex min-w-0 flex-1 items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
                    aria-current={artifactActive ? 'page' : undefined}
                    onClick={() => selectArtifact(artifact)}
                  >
                    <span className="grid size-5 shrink-0 place-items-center rounded border border-[var(--app-border)] text-[9px] font-semibold">{artifact.lineage?.iterationIndex || index + 1}</span>
                    <span className="min-w-0 flex-1 truncate">{iterationDisplayLabel(artifact, index)}</span>
                    {artifact.status === 'staging' ? <Clock3 className="size-3 shrink-0 text-[var(--app-primary)]" aria-label="Generating" /> : null}
                    {artifact.status === 'failed' || artifact.status === 'unavailable' ? <AlertTriangle className="size-3 shrink-0 text-[var(--app-danger)]" aria-label={artifactStatusLabel(artifact)} /> : null}
                    {canonical ? <Check className="size-3.5 shrink-0 text-[var(--app-success)]" aria-label="Selected design" /> : null}
                  </button>
                  {artifactHref ? <a href={artifactHref(artifact)} className="shrink-0 rounded px-1.5 py-1 text-[9px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-primary-soft)]" aria-label={`Open unique URL for ${artifact.label}`}>URL</a> : null}
                  <button
                    type="button"
                    className={cn('mr-1 grid size-7 shrink-0 place-items-center rounded-md border transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]', chatSelected ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-subtle)]')}
                    aria-label={`${chatSelected ? 'Remove' : 'Select'} ${artifact.label} ${chatSelected ? 'from' : 'for'} chat`}
                    aria-pressed={chatSelected}
                    disabled={!canSelectForChat || (!chatSelected && chatSelectedIds.length >= DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT)}
                    onClick={() => toggleChatSelection(artifact)}
                    data-artifact-chat-selection
                  >
                    {chatSelected ? <Check className="size-3.5" aria-hidden="true" /> : <MessageSquarePlus className="size-3.5" aria-hidden="true" />}
                  </button>
                </div>
              )
            })}
          </div>
        ) : null}
      </div>
    )
  }

  const workspaceGroups = organization === 'workspace'
    ? Array.from(new Map(groups.map((group) => [group.workspaceLabel, group.workspaceLabel])).keys())
    : []

  if (!showTrigger && !open) return null
  if (showTrigger && artifacts.length === 0) return null

  return (
    <div className={showTrigger ? 'mt-3' : undefined} data-final-handoff-artifacts={showTrigger ? true : undefined}>
      {showTrigger ? <button ref={galleryButtonRef} type="button" className="inline-flex items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1.5 text-xs font-medium text-[var(--app-text)] transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={() => setOpen(true)}><GalleryHorizontal size={14} aria-hidden="true" />Open gallery ({artifacts.length})</button> : null}
      {open ? createPortal(
        <section aria-label="Artifact collection review" className="fixed inset-0 z-[85] flex h-[100dvh] w-[100dvw] min-h-0 min-w-0 flex-col overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]" data-artifact-gallery-page data-artifact-review-surface>
          <header className="flex min-h-14 items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 sm:px-5">
            <button ref={backButtonRef} type="button" className="inline-flex shrink-0 items-center gap-2 rounded-lg px-2.5 py-2 text-sm font-medium text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={() => setOpen(false)}><ArrowLeft size={16} aria-hidden="true" /><span className="hidden sm:inline">Back to conversation</span><span className="sm:hidden">Back</span></button>
            <div className="min-w-0 text-center"><div className="flex items-center justify-center gap-2 text-sm font-semibold"><GalleryHorizontal size={16} aria-hidden="true" /> {title}</div><div className="text-[10px] text-[var(--app-text-subtle)]">Live collections · {groups.length}</div></div>
            <button type="button" className="grid size-9 shrink-0 place-items-center rounded-lg text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" aria-label="Exit artifact gallery" onClick={() => setOpen(false)}><X size={17} /></button>
          </header>
          <div className="flex flex-wrap items-center gap-2 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 sm:px-5">
            <label className="relative min-w-[14rem] flex-1"><span className="sr-only">Search artifact collections</span><Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-[var(--app-text-subtle)]" /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search collections, variants, sessions, or types" className="h-9 w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] pl-8 pr-3 text-xs outline-none focus:border-[var(--app-border-active)]" /></label>
            <div className="inline-flex h-9 rounded-lg border border-[var(--app-border)] p-0.5" aria-label="Collection grouping"><button type="button" className={cn('rounded-md px-2.5 text-xs', organization === 'collection' && 'bg-[var(--app-surface-active)] font-semibold')} aria-pressed={organization === 'collection'} onClick={() => setOrganization('collection')}>Collections</button><button type="button" className={cn('rounded-md px-2.5 text-xs', organization === 'workspace' && 'bg-[var(--app-surface-active)] font-semibold')} aria-pressed={organization === 'workspace'} onClick={() => setOrganization('workspace')}>By workspace</button></div>
          </div>
          <div className="grid min-h-0 flex-1 grid-cols-1 grid-rows-[auto_minmax(0,1fr)] md:grid-cols-[310px_minmax(0,1fr)] md:grid-rows-1">
            <aside className="min-w-0 overflow-x-auto border-b border-[var(--app-border)] bg-[var(--app-surface)] p-2 md:min-h-0 md:overflow-x-hidden md:overflow-y-auto md:border-b-0 md:border-r md:p-3" aria-label="Artifact collections" data-artifact-collection-sidebar>
              {catalogLoading ? <div className="flex items-center gap-2 p-3 text-xs text-[var(--app-text-muted)]"><Loader2 className="size-4 animate-spin" />Loading live collections…</div> : null}
              {catalogError ? <div className="m-2 rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-3 text-xs text-[var(--app-danger)]">{catalogError}</div> : null}
              {!catalogLoading && !catalogError && groups.length === 0 ? <div className="p-4 text-center text-xs text-[var(--app-text-muted)]">No collections match this search.</div> : null}
              <div className="flex gap-2 md:grid">
                {organization === 'collection'
                  ? groups.map(renderCollection)
                  : workspaceGroups.map((workspace) => <section key={workspace} className="min-w-0"><h2 className="truncate px-2 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">{workspace}</h2><div className="flex gap-2 md:grid">{groups.filter((group) => group.workspaceLabel === workspace).map(renderCollection)}</div></section>)}
              </div>
            </aside>
            <main className="flex min-h-0 min-w-0 flex-col bg-[var(--app-bg-alt)]">
              {selected ? <>
                <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2.5 sm:px-4">
                  <div className="min-w-0 flex-1"><div className="flex min-w-0 flex-wrap items-center gap-2"><div className="truncate text-sm font-semibold">{selected.label}</div><span className="shrink-0 rounded border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-1.5 py-0.5 text-[9px] font-semibold uppercase">{artifactTypeLabel(selected)}</span>{selectedIsCanonical ? <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-[var(--app-success-bg)] px-2 py-0.5 text-[10px] font-semibold text-[var(--app-success)]" data-artifact-selected-design><Check className="size-3" />Selected design</span> : null}{selected.status && selected.status !== 'ready' ? <span className={cn('rounded-full px-2 py-0.5 text-[10px] font-semibold', selected.status === 'staging' ? 'bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'bg-[var(--app-danger-bg)] text-[var(--app-danger)]')}>{artifactStatusLabel(selected)}</span> : null}</div>{selected.description && selected.description !== selected.label ? <p className="truncate text-xs text-[var(--app-text-muted)]">{selected.description}</p> : null}<p className="truncate text-[10px] text-[var(--app-text-subtle)]">{selectedGroup ? collectionDisplayLabel(selectedGroup) : selected.sessionTitle}{selectedVariantIndex >= 0 ? ` · Variant ${selectedVariantIndex + 1} of ${selectedVariants.length}` : ''}</p>{selectedRequirementLabel ? <p className="truncate text-[10px] font-medium text-[var(--app-text-muted)]" data-artifact-output-requirements title="Requested output target; not measured binary dimensions">{selectedRequirementLabel}</p> : null}</div>
                  <div className="flex shrink-0 items-center gap-1.5">{artifactHref ? <a href={artifactHref(selected)} className="inline-flex h-8 items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-[10px] font-semibold hover:bg-[var(--app-surface-hover)]">Open iteration URL</a> : null}<button type="button" className="grid size-8 place-items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)] disabled:cursor-default disabled:opacity-40" disabled={selectedVariants.length < 2} aria-label="Previous artifact" onClick={() => selectAdjacentVariant(-1)}><ChevronLeft size={15} /></button><button type="button" className="grid size-8 place-items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)] disabled:cursor-default disabled:opacity-40" disabled={selectedVariants.length < 2} aria-label="Next artifact" onClick={() => selectAdjacentVariant(1)}><ChevronRight size={15} /></button><button type="button" className="grid size-8 place-items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)]" aria-label="View artifact fullscreen" onClick={() => void togglePreviewFullscreen()}><Maximize2 size={14} /></button>{selected.content === undefined && selected.status !== 'staging' && selected.status !== 'failed' && selected.status !== 'unavailable' ? <button type="button" className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs font-medium hover:bg-[var(--app-surface-hover)]" onClick={() => void downloadArtifact(selected)}><Download size={13} /> <span className="hidden sm:inline">Download bundle</span></button> : null}</div>
                </div>
                <div
                  ref={previewSurfaceRef}
                  className={cn(
                    'relative min-h-0 flex-1',
                    selected.mediaType.startsWith('image/') || selected.mediaType === 'text/html' || selected.mediaType === 'application/pdf' ? 'overflow-hidden' : 'overflow-auto',
                    selected.mediaType === 'text/html' || selected.mediaType === 'application/pdf' ? 'p-0' : 'p-3 sm:p-4',
                    previewFullscreen && 'h-[100dvh] w-[100dvw] flex-none bg-[var(--app-bg-alt)]',
                  )}
                  data-artifact-preview-surface
                >
                  {previewFullscreen ? <button type="button" className="absolute right-3 top-3 z-20 grid size-9 place-items-center rounded-full border border-white/20 bg-black/60 text-white shadow-lg hover:bg-black/75" aria-label="Exit fullscreen artifact preview" onClick={() => void togglePreviewFullscreen()}><Minimize2 size={16} /></button> : null}
                  {previewLoading ? <div className="grid h-full min-h-40 place-items-center text-sm text-[var(--app-text-muted)]"><span><Loader2 className="mr-2 inline size-4 animate-spin" />Loading preview…</span></div> : null}
                  {previewError ? <div className="mx-auto mt-8 max-w-lg rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">Preview unavailable: {previewError}</div> : null}
                  {!previewLoading && !previewError && selected.status === 'staging' ? <div className="grid h-full min-h-40 place-items-center text-center text-sm text-[var(--app-text-muted)]"><div><Loader2 className="mx-auto mb-3 size-6 animate-spin text-[var(--app-primary)]" /><p>This variant is still generating.</p><p className="mt-1 text-xs text-[var(--app-text-subtle)]">The live review surface will refresh when it is ready.</p></div></div> : null}
                  {!previewLoading && !previewError && (selected.status === 'failed' || selected.status === 'unavailable') ? <div className="grid h-full min-h-40 place-items-center text-center text-sm text-[var(--app-danger)]"><div><AlertTriangle className="mx-auto mb-3 size-6" /><p>This variant could not be generated.</p>{selected.failureCode ? <p className="mt-1 font-mono text-xs text-[var(--app-text-muted)]">{selected.failureCode}</p> : null}</div></div> : null}
                  {!previewLoading && !previewError && !selected.previewable && selected.content === undefined && selected.status !== 'staging' && selected.status !== 'failed' && selected.status !== 'unavailable' ? <div className="grid h-full min-h-40 place-items-center text-center text-sm text-[var(--app-text-muted)]"><div><FileText className="mx-auto mb-2 size-6" /><p>This artifact is available to download, but has no inline preview.</p></div></div> : null}
                  {!previewLoading && !previewError && selected.mediaType.startsWith('image/') && previewURL ? <div className="grid size-full min-h-0 place-items-center"><img src={previewURL} alt={selected.description || selected.label} className="size-full rounded-lg border border-[var(--app-border)] bg-white object-contain shadow-sm" /></div> : null}
                  {!previewLoading && !previewError && selected.mediaType === 'text/html' && previewText ? <iframe title={selected.label} srcDoc={previewText} sandbox="allow-scripts" referrerPolicy="no-referrer" className="h-full min-h-0 w-full border-0 bg-white" /> : null}
                  {!previewLoading && !previewError && selected.mediaType === 'application/pdf' && previewURL ? <iframe title={selected.label} src={previewURL} sandbox="" referrerPolicy="no-referrer" className="h-full min-h-0 w-full border-0 bg-white" /> : null}
                  {!previewLoading && !previewError && selected.mediaType === 'text/markdown' && previewText ? <div className="mx-auto max-w-4xl rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-5"><ChatMarkdown content={previewText} /></div> : null}
                  {!previewLoading && !previewError && selected.mediaType === 'text/plain' && previewText ? <pre className="mx-auto max-w-4xl whitespace-pre-wrap break-words rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-5 font-mono text-xs leading-5">{previewText}</pre> : null}
                </div>
                <footer className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-3 sm:px-4" aria-live="polite">
                  <div className="min-w-0 flex-1 text-xs">{actionError ? <span className="text-[var(--app-danger)]">{actionError}</span> : chatSelectedIds.length > 0 ? <span className="text-[var(--app-text-subtle)]">{chatSelectedIds.length} ready variant{chatSelectedIds.length === 1 ? '' : 's'} selected for chat. This does not change the durable selected design.</span> : selectedIsCanonical ? <span className="inline-flex items-center gap-1.5 text-[var(--app-success)]"><Check className="size-3.5" />This is the durable selected design for the collection.</span> : <span className="text-[var(--app-text-subtle)]">Add a reference without changing the selected design, or select and use it.</span>}</div>
                  <div className="flex shrink-0 items-center gap-2"><button type="button" className={cn('inline-flex h-9 items-center gap-2 rounded-lg border px-3 text-xs font-semibold transition disabled:cursor-not-allowed disabled:opacity-50', selectedIsQueuedForChat ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)]')} disabled={!selectedCanAttach || Boolean(actionPending)} aria-pressed={selectedIsQueuedForChat} onClick={() => selected && toggleChatSelection(selected)}>{selectedIsQueuedForChat ? <Check className="size-4" /> : <MessageSquarePlus className="size-4" />}{selectedIsQueuedForChat ? 'Queued for chat' : 'Select this iteration'}</button><button type="button" className="inline-flex h-9 items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 text-xs font-semibold hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50" disabled={pendingChatArtifacts.length === 0 || !onAddToChat || Boolean(actionPending)} title={pendingChatArtifacts.length === 0 ? 'Only ready managed variants can be attached' : undefined} onClick={() => void emitAddToChat()}>{actionPending === 'add' ? <Loader2 className="size-4 animate-spin" /> : <MessageSquarePlus className="size-4" />}Add {pendingChatArtifacts.length > 1 ? `${pendingChatArtifacts.length} ` : ''}to chat</button><button type="button" className="inline-flex h-9 items-center gap-2 rounded-lg bg-[var(--app-primary)] px-3 text-xs font-semibold text-white hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50" disabled={!selectedCanAttach || !onUseThisDesign || Boolean(actionPending)} title={!selectedCanAttach ? 'Only ready managed variants can be used' : undefined} onClick={() => void persistAndUseDesign()}>{actionPending === 'use' ? <Loader2 className="size-4 animate-spin" /> : <Sparkles className="size-4" />}Use this design</button></div>
                </footer>
              </> : null}
            </main>
          </div>
        </section>, document.body) : null}
    </div>
  )
}

export interface DesktopV3ArtifactCatalogGalleryProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onAddToChat?: DesktopV3ArtifactGalleryProps['onAddToChat']
  onUseThisDesign?: DesktopV3ArtifactGalleryProps['onUseThisDesign']
}

export function DesktopV3ArtifactCatalogGallery({ open, onOpenChange, onAddToChat, onUseThisDesign }: DesktopV3ArtifactCatalogGalleryProps) {
  const [artifacts, setArtifacts] = useState<DesktopV3ArtifactCatalogEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const refreshCatalog = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      setArtifacts(await fetchDesktopV3ArtifactCatalog())
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Artifact catalog failed to load')
    } finally {
      setLoading(false)
    }
  }, [])

  useDesktopV3OpenArtifactCatalogRefresh(open, refreshCatalog)

  useEffect(() => {
    if (!open) return
    void refreshCatalog()
  }, [open, refreshCatalog])

  return <DesktopV3ArtifactGallery artifacts={artifacts} open={open} onOpenChange={onOpenChange} onAddToChat={onAddToChat} onUseThisDesign={onUseThisDesign} onSelectionPersisted={refreshCatalog} showTrigger={false} loading={loading} error={error} title="Artifact review" />
}
