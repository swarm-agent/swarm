import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ArrowLeft, ChevronLeft, ChevronRight, Download, FileText, GalleryHorizontal, Loader2, Search, X } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import {
  buildDesktopV3ArtifactSandboxDocument,
  fetchDesktopV3Artifact,
  fetchDesktopV3ArtifactBundle,
  fetchDesktopV3ArtifactCatalog,
  fetchDesktopV3ArtifactPreviewToken,
  type DesktopV3ArtifactCatalogEntry,
} from '../../session-v3/artifact-api'

export type DesktopV3ArtifactGalleryEntry = DesktopV3ArtifactCatalogEntry

export interface DesktopV3ArtifactGalleryProps {
  artifacts: DesktopV3ArtifactGalleryEntry[]
  open?: boolean
  onOpenChange?: (open: boolean) => void
  showTrigger?: boolean
  loading?: boolean
  error?: string
  title?: string
}

function artifactBundleDownloadName(artifact: DesktopV3ArtifactGalleryEntry): string {
  const label = artifact.label.trim().replace(/[^a-z0-9._-]+/gi, '-').replace(/^-+|-+$/g, '') || 'artifact'
  const base = label.replace(/\.[a-z0-9]{1,8}$/i, '') || 'artifact'
  return `${base}.zip`
}

function artifactSelectionKey(artifact: DesktopV3ArtifactGalleryEntry): string {
  return `${artifact.sessionId}:${artifact.artifactId}`
}

function artifactTypeLabel(artifact: DesktopV3ArtifactGalleryEntry): string {
  return artifact.kind.trim() || artifact.mediaType.trim() || 'artifact'
}

function workspaceLabel(artifact: DesktopV3ArtifactGalleryEntry): string {
  return artifact.workspaceName.trim() || artifact.workspacePath.trim() || 'Workspace'
}

function categoryLabel(category: DesktopV3ArtifactGalleryEntry['category']): string {
  if (category === 'plan') return 'Plans'
  if (category === 'visual') return 'Visual artifacts'
  return 'Documents'
}

export function DesktopV3ArtifactGallery({
  artifacts,
  open: controlledOpen,
  onOpenChange,
  showTrigger = true,
  loading: catalogLoading = false,
  error: catalogError = '',
  title = 'Artifacts',
}: DesktopV3ArtifactGalleryProps) {
  const [internalOpen, setInternalOpen] = useState(false)
  const [selectedId, setSelectedId] = useState(artifacts[0] ? artifactSelectionKey(artifacts[0]) : '')
  const [previewURL, setPreviewURL] = useState('')
  const [previewText, setPreviewText] = useState('')
  const [previewError, setPreviewError] = useState('')
  const [previewLoading, setPreviewLoading] = useState(false)
  const [query, setQuery] = useState('')
  const [showPlans, setShowPlans] = useState(true)
  const [organization, setOrganization] = useState<'flat' | 'workspace'>('flat')
  const galleryButtonRef = useRef<HTMLButtonElement>(null)
  const backButtonRef = useRef<HTMLButtonElement>(null)
  const open = controlledOpen ?? internalOpen
  const setOpen = (next: boolean) => {
    if (controlledOpen === undefined) setInternalOpen(next)
    onOpenChange?.(next)
  }

  const visibleArtifacts = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase()
    return artifacts.filter((artifact) => {
      if (!showPlans && artifact.category === 'plan') return false
      if (!normalizedQuery) return true
      return [
        artifact.label,
        artifact.description,
        artifact.filename,
        artifact.mediaType,
        artifact.kind,
        artifact.category,
        artifact.sessionTitle,
        artifact.workspaceName,
        artifact.workspacePath,
        artifact.planTitle,
        artifact.checkpointTitle,
      ].some((value) => value.toLowerCase().includes(normalizedQuery))
    })
  }, [artifacts, query, showPlans])

  const selected = visibleArtifacts.find((artifact) => artifactSelectionKey(artifact) === selectedId) ?? visibleArtifacts[0]
  const selectedIndex = selected ? visibleArtifacts.findIndex((artifact) => artifactSelectionKey(artifact) === artifactSelectionKey(selected)) : -1

  useEffect(() => {
    if (!open) return undefined
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    backButtonRef.current?.focus()
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => {
      window.removeEventListener('keydown', handleKeyDown)
      document.body.style.overflow = previousOverflow
      galleryButtonRef.current?.focus()
    }
  }, [open])

  useEffect(() => {
    if (!open || !selected) return undefined
    setSelectedId(artifactSelectionKey(selected))
    setPreviewURL('')
    setPreviewText('')
    setPreviewError('')
    if (selected.content !== undefined) {
      setPreviewText(selected.content)
      setPreviewLoading(false)
      return undefined
    }
    if (!selected.previewable) {
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
  }, [open, selected?.artifactId, selected?.content, selected?.mediaType, selected?.previewable, selected?.sessionId])

  const selectAdjacentArtifact = (offset: -1 | 1) => {
    if (visibleArtifacts.length < 2 || selectedIndex < 0) return
    const nextIndex = (selectedIndex + offset + visibleArtifacts.length) % visibleArtifacts.length
    const nextArtifact = visibleArtifacts[nextIndex]
    if (nextArtifact) setSelectedId(artifactSelectionKey(nextArtifact))
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
      URL.revokeObjectURL(url)
    } catch (error) {
      setPreviewError(error instanceof Error ? error.message : 'Artifact download failed')
    }
  }

  const renderArtifactButton = (artifact: DesktopV3ArtifactGalleryEntry) => (
    <button
      key={`${artifact.sessionId}:${artifact.artifactId}`}
      type="button"
      className={cn('w-52 shrink-0 rounded-lg border px-3 py-2 text-left transition md:w-auto md:min-w-0', artifactSelectionKey(artifact) === (selected ? artifactSelectionKey(selected) : '') ? 'border-[var(--app-primary)] bg-[color-mix(in_srgb,var(--app-primary)_8%,var(--app-surface))]' : 'border-transparent hover:border-[var(--app-border)] hover:bg-[var(--app-surface-hover)]')}
      aria-current={artifactSelectionKey(artifact) === (selected ? artifactSelectionKey(selected) : '') ? 'page' : undefined}
      onClick={() => setSelectedId(artifactSelectionKey(artifact))}
    >
      <span className="flex items-center gap-2 text-xs font-medium text-[var(--app-text)]"><FileText size={13} className="shrink-0" /> <span className="truncate">{artifact.label}</span></span>
      <span className="mt-1 flex min-w-0 items-center gap-1.5">
        <span className="rounded border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wide text-[var(--app-text-muted)]">{artifactTypeLabel(artifact)}</span>
        <span className="truncate text-[10px] text-[var(--app-text-subtle)]">{artifact.sessionTitle || categoryLabel(artifact.category)}</span>
      </span>
    </button>
  )

  const renderCategoryGroups = (entries: DesktopV3ArtifactGalleryEntry[]) => (
    (['plan', 'visual', 'document'] as const).map((category) => {
      const categoryEntries = entries.filter((artifact) => artifact.category === category)
      if (categoryEntries.length === 0) return null
      return <section key={category} className="min-w-0"><h3 className="px-2 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">{categoryLabel(category)} · {categoryEntries.length}</h3><div className="flex gap-1.5 md:grid">{categoryEntries.map(renderArtifactButton)}</div></section>
    })
  )

  const workspaceGroups = organization === 'workspace'
    ? Array.from(new Map(visibleArtifacts.map((artifact) => [artifact.workspacePath || artifact.workspaceName, workspaceLabel(artifact)])).entries())
    : []

  if (!showTrigger && !open) return null
  if (showTrigger && artifacts.length === 0) return null

  return (
    <div className={showTrigger ? 'mt-3' : undefined} data-final-handoff-artifacts={showTrigger ? true : undefined}>
      {showTrigger ? <button ref={galleryButtonRef} type="button" className="inline-flex items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1.5 text-xs font-medium text-[var(--app-text)] transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={() => setOpen(true)}><GalleryHorizontal size={14} aria-hidden="true" />Open gallery ({artifacts.length})</button> : null}
      {open ? createPortal(
        <section aria-label="Artifact gallery" className="fixed inset-0 z-[85] flex h-[100dvh] w-[100dvw] min-h-0 min-w-0 flex-col overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]" data-artifact-gallery-page>
          <header className="flex min-h-14 items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 sm:px-5">
            <button ref={backButtonRef} type="button" className="inline-flex shrink-0 items-center gap-2 rounded-lg px-2.5 py-2 text-sm font-medium text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={() => setOpen(false)}><ArrowLeft size={16} aria-hidden="true" /><span className="hidden sm:inline">Back to conversation</span><span className="sm:hidden">Back</span></button>
            <div className="min-w-0 text-center"><div className="flex items-center justify-center gap-2 text-sm font-semibold"><GalleryHorizontal size={16} aria-hidden="true" /> {title}</div><div className="text-[10px] text-[var(--app-text-subtle)]">{selectedIndex >= 0 ? selectedIndex + 1 : 0} of {visibleArtifacts.length}</div></div>
            <button type="button" className="grid size-9 shrink-0 place-items-center rounded-lg text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" aria-label="Exit artifact gallery" onClick={() => setOpen(false)}><X size={17} /></button>
          </header>
          <div className="flex flex-wrap items-center gap-2 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 sm:px-5">
            <label className="relative min-w-[14rem] flex-1"><Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-[var(--app-text-subtle)]" /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search artifacts, workspaces, plans, or types" className="h-9 w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] pl-8 pr-3 text-xs outline-none focus:border-[var(--app-border-active)]" /></label>
            <label className="inline-flex h-9 items-center gap-2 rounded-lg border border-[var(--app-border)] px-3 text-xs font-medium"><input type="checkbox" checked={showPlans} onChange={(event) => setShowPlans(event.target.checked)} />Show plans</label>
            <div className="inline-flex h-9 rounded-lg border border-[var(--app-border)] p-0.5" aria-label="Artifact organization"><button type="button" className={cn('rounded-md px-2.5 text-xs', organization === 'flat' && 'bg-[var(--app-surface-active)] font-semibold')} onClick={() => setOrganization('flat')}>Flat</button><button type="button" className={cn('rounded-md px-2.5 text-xs', organization === 'workspace' && 'bg-[var(--app-surface-active)] font-semibold')} onClick={() => setOrganization('workspace')}>By workspace</button></div>
          </div>
          <div className="grid min-h-0 flex-1 grid-cols-1 grid-rows-[auto_minmax(0,1fr)] md:grid-cols-[290px_minmax(0,1fr)] md:grid-rows-1">
            <aside className="min-w-0 overflow-x-auto border-b border-[var(--app-border)] bg-[var(--app-surface)] p-2 md:min-h-0 md:overflow-x-hidden md:overflow-y-auto md:border-b-0 md:border-r md:p-3" aria-label="Available artifacts">
              {catalogLoading ? <div className="flex items-center gap-2 p-3 text-xs text-[var(--app-text-muted)]"><Loader2 className="size-4 animate-spin" />Loading artifact catalog…</div> : null}
              {catalogError ? <div className="m-2 rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-3 text-xs text-[var(--app-danger)]">{catalogError}</div> : null}
              {!catalogLoading && !catalogError && visibleArtifacts.length === 0 ? <div className="p-4 text-center text-xs text-[var(--app-text-muted)]">No artifacts match these filters.</div> : null}
              {organization === 'flat' ? renderCategoryGroups(visibleArtifacts) : workspaceGroups.map(([workspaceKey, label]) => <section key={workspaceKey || label} className="min-w-0 border-b border-[var(--app-border)] pb-2 last:border-0"><h2 className="truncate px-2 pb-1 pt-3 text-xs font-semibold text-[var(--app-text)]">{label}</h2>{renderCategoryGroups(visibleArtifacts.filter((artifact) => (artifact.workspacePath || artifact.workspaceName) === workspaceKey))}</section>)}
            </aside>
            <main className="flex min-h-0 min-w-0 flex-col bg-[var(--app-bg-alt)]">
              {selected ? <><div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2.5 sm:px-4"><div className="min-w-0 flex-1"><div className="flex min-w-0 items-center gap-2"><div className="truncate text-sm font-semibold">{selected.label}</div><span className="shrink-0 rounded border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-1.5 py-0.5 text-[9px] font-semibold uppercase">{artifactTypeLabel(selected)}</span></div>{selected.description && selected.description !== selected.label ? <p className="truncate text-xs text-[var(--app-text-muted)]">{selected.description}</p> : null}<p className="truncate text-[10px] text-[var(--app-text-subtle)]">{workspaceLabel(selected)}{selected.planTitle ? ` · ${selected.planTitle}` : ''}</p></div><div className="flex shrink-0 items-center gap-1.5"><button type="button" className="grid size-8 place-items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)] disabled:cursor-default disabled:opacity-40" disabled={visibleArtifacts.length < 2} aria-label="Previous artifact" onClick={() => selectAdjacentArtifact(-1)}><ChevronLeft size={15} /></button><button type="button" className="grid size-8 place-items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] hover:bg-[var(--app-surface-hover)] disabled:cursor-default disabled:opacity-40" disabled={visibleArtifacts.length < 2} aria-label="Next artifact" onClick={() => selectAdjacentArtifact(1)}><ChevronRight size={15} /></button>{selected.content === undefined ? <button type="button" className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs font-medium hover:bg-[var(--app-surface-hover)]" onClick={() => void downloadArtifact(selected)}><Download size={13} /> <span className="hidden sm:inline">Download bundle</span></button> : null}</div></div>
                <div className={cn('relative min-h-0 flex-1 overflow-auto', selected.mediaType === 'text/html' || selected.mediaType === 'application/pdf' ? 'p-0' : 'p-3 sm:p-4')}>
                  {previewLoading ? <div className="grid h-full min-h-40 place-items-center text-sm text-[var(--app-text-muted)]"><span><Loader2 className="mr-2 inline size-4 animate-spin" />Loading preview…</span></div> : null}
                  {previewError ? <div className="mx-auto mt-8 max-w-lg rounded-lg border border-[var(--app-danger)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">Preview unavailable: {previewError}</div> : null}
                  {!previewLoading && !previewError && !selected.previewable && selected.content === undefined ? <div className="grid h-full min-h-40 place-items-center text-center text-sm text-[var(--app-text-muted)]"><div><FileText className="mx-auto mb-2 size-6" /><p>This artifact is available to download, but has no inline preview.</p></div></div> : null}
                  {!previewLoading && !previewError && selected.mediaType.startsWith('image/') && previewURL ? <div className="grid min-h-full place-items-center"><img src={previewURL} alt={selected.description || selected.label} className="max-h-full max-w-full rounded-lg border border-[var(--app-border)] bg-white object-contain shadow-sm" /></div> : null}
                  {!previewLoading && !previewError && selected.mediaType === 'text/html' && previewText ? <iframe title={selected.label} srcDoc={previewText} sandbox="allow-scripts" referrerPolicy="no-referrer" className="h-full min-h-0 w-full border-0 bg-white" /> : null}
                  {!previewLoading && !previewError && selected.mediaType === 'application/pdf' && previewURL ? <iframe title={selected.label} src={previewURL} sandbox="" referrerPolicy="no-referrer" className="h-full min-h-0 w-full border-0 bg-white" /> : null}
                  {!previewLoading && !previewError && selected.mediaType === 'text/markdown' && previewText ? <div className="mx-auto max-w-4xl rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-5"><ChatMarkdown content={previewText} /></div> : null}
                  {!previewLoading && !previewError && selected.mediaType === 'text/plain' && previewText ? <pre className="mx-auto max-w-4xl whitespace-pre-wrap break-words rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-5 font-mono text-xs leading-5">{previewText}</pre> : null}
                </div></> : null}
            </main>
          </div>
        </section>, document.body) : null}
    </div>
  )
}

export function DesktopV3ArtifactCatalogGallery({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [artifacts, setArtifacts] = useState<DesktopV3ArtifactCatalogEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return undefined
    const controller = new AbortController()
    setLoading(true)
    setError('')
    void fetchDesktopV3ArtifactCatalog(controller.signal)
      .then(setArtifacts)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Artifact catalog failed to load')
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [open])

  return <DesktopV3ArtifactGallery artifacts={artifacts} open={open} onOpenChange={onOpenChange} showTrigger={false} loading={loading} error={error} title="Workspace artifacts" />
}
