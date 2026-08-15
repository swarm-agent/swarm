import { useEffect, useMemo, useState, type MouseEvent } from 'react'
import { FileText, GalleryHorizontal, Loader2, Maximize2, MessageSquarePlus, TriangleAlert } from 'lucide-react'

import { cn } from '../../../../lib/cn'
import {
  buildDesktopV3ArtifactSandboxDocument,
  fetchDesktopV3Artifact,
  fetchDesktopV3ArtifactPreviewToken,
  formatDesktopV3ArtifactOutputRequirements,
  type DesktopV3ArtifactCatalogEntry,
  type DesktopV3ArtifactCollectionProgress,
} from '../../session-v3/artifact-api'
import type { DesktopSidebarDisplayMode } from './desktop-sidebar-display'
import { useDesktopV3ArtifactPreviewVisibility } from './desktop-v3-artifact-preview-thumbnail'

export type DesktopV3SessionSidebarView = 'plan' | 'artifacts'

export function desktopV3ArtifactsForSession(
  artifacts: readonly DesktopV3ArtifactCatalogEntry[],
  sessionId: string,
): DesktopV3ArtifactCatalogEntry[] {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) return []
  return artifacts.filter((artifact) =>
    artifact.sessionId === normalizedSessionId || artifact.lineage?.parentSessionId === normalizedSessionId
  )
}

export function desktopV3HasPendingVisualSwarm(
  artifacts: readonly DesktopV3ArtifactCatalogEntry[],
): boolean {
  return artifacts.some((artifact) =>
    artifact.status === 'staging' && Boolean(artifact.lineage?.iterationGroupId.trim())
  )
}

export function desktopV3MobileVisualSwarmArtifactToOpen(input: {
  artifacts: readonly DesktopV3ArtifactCatalogEntry[]
  sessionId: string
  sidebarViewport: boolean
  openedGroupKeys: ReadonlySet<string>
}): DesktopV3ArtifactCatalogEntry | undefined {
  const sessionId = input.sessionId.trim()
  if (!sessionId || input.sidebarViewport) return undefined
  return [...desktopV3ArtifactsForSession(input.artifacts, sessionId)].reverse().find((artifact) => {
    const iterationGroupId = artifact.lineage?.iterationGroupId.trim() ?? ''
    if (artifact.status !== 'staging' || !iterationGroupId) return false
    return !input.openedGroupKeys.has(`${sessionId}:${iterationGroupId}`)
  })
}

export function desktopV3NextSessionSidebarView(input: {
  current: DesktopV3SessionSidebarView
  previousArtifactCount: number
  artifactCount: number
  hasPlan: boolean
  prioritizePlan?: boolean
  hasPendingVisualSwarm?: boolean
}): DesktopV3SessionSidebarView {
  if (input.artifactCount === 0) return 'plan'
  if (input.hasPendingVisualSwarm) return 'artifacts'
  if (input.prioritizePlan) return 'plan'
  if (input.previousArtifactCount === 0 && !input.hasPlan) return 'artifacts'
  return input.current
}

export interface DesktopV3ArtifactSidebarProps {
  artifacts: DesktopV3ArtifactCatalogEntry[]
  displayMode?: DesktopSidebarDisplayMode
  loading?: boolean
  error?: string
  embedded?: boolean
  artifactHref: (artifact: DesktopV3ArtifactCatalogEntry) => string
  onOpenArtifact: (artifact: DesktopV3ArtifactCatalogEntry) => void
  onAddToChat?: (artifacts: DesktopV3ArtifactCatalogEntry[]) => void
}

function sidebarArtifactPreviewKey(artifact: DesktopV3ArtifactCatalogEntry): string {
  return `${artifact.sessionId}:${artifact.collectionId ?? ''}:${artifact.artifactId}`
}

function sidebarArtifactNeedsLivePreview(artifact: DesktopV3ArtifactCatalogEntry): boolean {
  return artifact.mediaType === 'image/svg+xml'
    || artifact.mediaType === 'image/gif'
    || artifact.mediaType === 'text/html'
    || artifact.mediaType === 'application/pdf'
    || artifact.mediaType.startsWith('video/')
    || artifact.kind === 'video'
}

function DesktopV3ArtifactThumbnail({ artifact, live }: { artifact: DesktopV3ArtifactCatalogEntry; live: boolean }) {
  const [previewURL, setPreviewURL] = useState('')
  const [previewHTML, setPreviewHTML] = useState('')
  const [failed, setFailed] = useState(false)
  const previewEnabled = live || !sidebarArtifactNeedsLivePreview(artifact)
  const { previewRef, previewVisible } = useDesktopV3ArtifactPreviewVisibility<HTMLSpanElement>(previewEnabled)

  useEffect(() => {
    setPreviewURL('')
    setPreviewHTML('')
    setFailed(false)
    if (!previewVisible || !artifact.previewable || artifact.status !== 'ready') return undefined

    const controller = new AbortController()
    let objectURL = ''
    void fetchDesktopV3Artifact(artifact.sessionId, artifact.artifactId, controller.signal)
      .then(async (blob) => {
        if (controller.signal.aborted) return
        if (artifact.mediaType === 'text/html') {
          const [source, previewToken] = await Promise.all([
            blob.text(),
            fetchDesktopV3ArtifactPreviewToken(artifact.sessionId, artifact.artifactId, controller.signal),
          ])
          if (!controller.signal.aborted) {
            setPreviewHTML(buildDesktopV3ArtifactSandboxDocument(source, artifact.sessionId, artifact.artifactId, previewToken))
          }
          return
        }
        if (artifact.mediaType.startsWith('image/') || artifact.mediaType.startsWith('video/') || artifact.kind === 'video' || artifact.mediaType === 'application/pdf') {
          objectURL = URL.createObjectURL(blob)
          setPreviewURL(objectURL)
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true)
      })

    return () => {
      controller.abort()
      if (objectURL) URL.revokeObjectURL(objectURL)
    }
  }, [artifact.artifactId, artifact.mediaType, artifact.previewable, artifact.sessionId, artifact.status, previewVisible])

  let thumbnail = <FileText className="size-5 text-[var(--app-text-muted)]" aria-hidden="true" />
  if (artifact.status === 'staging') thumbnail = <Loader2 className="size-5 motion-safe:animate-spin motion-reduce:animate-none text-[var(--app-primary)]" aria-label="Generating artifact" />
  else if (artifact.status === 'failed' || artifact.status === 'unavailable' || failed) thumbnail = <TriangleAlert className="size-5 text-[var(--app-danger)]" aria-label="Artifact unavailable" />
  else if (previewVisible && artifact.mediaType.startsWith('image/') && previewURL) thumbnail = <img src={previewURL} alt="" className="size-full object-contain" />
  else if (previewVisible && (artifact.mediaType.startsWith('video/') || artifact.kind === 'video') && previewURL) thumbnail = <video src={previewURL} muted playsInline preload="metadata" className="size-full object-contain bg-black" />
  else if (previewVisible && artifact.mediaType === 'text/html' && previewHTML) thumbnail = <iframe title={`${artifact.label} thumbnail`} srcDoc={previewHTML} sandbox="allow-scripts" referrerPolicy="no-referrer" tabIndex={-1} className="pointer-events-none absolute left-0 top-0 size-[400%] origin-top-left scale-25 border-0 bg-white" />
  else if (previewVisible && artifact.mediaType === 'application/pdf' && previewURL) thumbnail = <iframe title={`${artifact.label} thumbnail`} src={previewURL} sandbox="" referrerPolicy="no-referrer" tabIndex={-1} className="pointer-events-none size-full border-0 bg-white" />

  return <span ref={previewRef} className="relative grid size-full place-items-center overflow-hidden" data-artifact-live-preview={live || undefined} data-artifact-preview-visible={previewVisible || undefined}>{thumbnail}</span>
}

export interface DesktopV3ArtifactSidebarGroup {
  key: string
  collectionId: string
  entries: DesktopV3ArtifactCatalogEntry[]
  progress: DesktopV3ArtifactCollectionProgress
  label: string
}

function sidebarCollectionProgress(entries: readonly DesktopV3ArtifactCatalogEntry[]): DesktopV3ArtifactCollectionProgress {
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

function sidebarCollectionLabel(entry: DesktopV3ArtifactCatalogEntry): string {
  return entry.collectionName
    || entry.lineage?.iterationGroup
    || (entry.lineage?.iterationGroupId ? 'Iteration Swarm' : '')
    || 'Artifact collection'
}

export function desktopV3ArtifactSidebarGroups(
  artifacts: readonly DesktopV3ArtifactCatalogEntry[],
): DesktopV3ArtifactSidebarGroup[] {
  const groups = new Map<string, DesktopV3ArtifactCatalogEntry[]>()
  for (const artifact of artifacts) {
    const collectionId = artifact.collectionId?.trim() ?? ''
    const owningSessionId = artifact.lineage?.parentSessionId || artifact.sessionId
    const key = collectionId
      ? `${owningSessionId}\u0000${collectionId}`
      : `${artifact.sessionId}\u0000artifact\u0000${artifact.artifactId}`
    groups.set(key, [...(groups.get(key) ?? []), artifact])
  }
  return [...groups.entries()].map(([key, entries]) => ({
    key,
    collectionId: entries[0]?.collectionId?.trim() ?? '',
    entries: [...entries].sort((left, right) => {
      const leftIndex = left.lineage?.iterationIndex ?? 0
      const rightIndex = right.lineage?.iterationIndex ?? 0
      return leftIndex && rightIndex ? leftIndex - rightIndex : leftIndex ? -1 : rightIndex ? 1 : 0
    }),
    progress: sidebarCollectionProgress(entries),
    label: entries[0] ? sidebarCollectionLabel(entries[0]) : 'Artifact collection',
  }))
}

function sidebarProgressLabel(group: DesktopV3ArtifactSidebarGroup): string {
  const terminal = group.progress.failed + group.progress.unavailable
  if (group.progress.staging > 0) return `${group.progress.ready}/${group.progress.total} ready · ${group.progress.staging} generating`
  if (terminal > 0) return `${group.progress.ready}/${group.progress.total} ready · ${terminal} failed`
  return `${group.progress.ready}/${group.progress.total} ready`
}

export function DesktopV3ArtifactSidebar({
  artifacts,
  displayMode = 'full',
  loading = false,
  error = '',
  embedded = false,
  artifactHref,
  onOpenArtifact,
  onAddToChat,
}: DesktopV3ArtifactSidebarProps) {
  const thin = displayMode === 'thin'
  const compact = displayMode === 'compact'
  const groups = useMemo(() => desktopV3ArtifactSidebarGroups(artifacts), [artifacts])
  const defaultLivePreviewKey = useMemo(() => {
    const liveArtifacts = groups.flatMap((group) => group.entries).filter((entry) => entry.status === 'ready' && sidebarArtifactNeedsLivePreview(entry))
    const artifact = liveArtifacts.find((entry) => entry.selected) ?? liveArtifacts[0]
    return artifact ? sidebarArtifactPreviewKey(artifact) : ''
  }, [groups])
  const [requestedLivePreviewKey, setRequestedLivePreviewKey] = useState('')
  const requestedLivePreviewAvailable = groups.some((group) => group.entries.some((entry) => sidebarArtifactPreviewKey(entry) === requestedLivePreviewKey))
  const livePreviewKey = requestedLivePreviewAvailable ? requestedLivePreviewKey : defaultLivePreviewKey
  const requestLivePreview = (artifact: DesktopV3ArtifactCatalogEntry) => {
    if (sidebarArtifactNeedsLivePreview(artifact)) setRequestedLivePreviewKey(sidebarArtifactPreviewKey(artifact))
  }

  return (
    <aside
      aria-label="Session artifact sidebar"
      data-testid="desktop-session-artifact-sidebar"
      data-display-mode={displayMode}
      className={cn(
        'min-h-0 min-w-0 bg-[var(--app-bg-alt)] text-[var(--app-text)]',
        embedded
          ? 'w-full'
          : 'hidden h-full flex-1 flex-col overflow-hidden border-l border-[var(--app-border)]/60 min-[1300px]:flex',
        !embedded && (thin ? 'w-[56px] max-w-[56px] px-2 py-3' : compact ? 'w-[292px] max-w-[292px] p-3' : 'w-[372px] max-w-[372px] p-4'),
      )}
    >
      <header className={cn('flex shrink-0 items-center', thin ? 'justify-center pb-2' : 'justify-between gap-3 pb-3')}>
        <div className={cn('flex min-w-0 items-center gap-2', thin && 'justify-center')}>
          <GalleryHorizontal className="size-4 shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
          {!thin ? <div className="min-w-0"><h2 className="truncate text-sm font-semibold">Artifacts</h2><p className="text-[10px] text-[var(--app-text-subtle)]">This session · {artifacts.length}</p></div> : null}
        </div>
      </header>

      {loading && artifacts.length === 0 ? <div className="grid flex-1 place-items-center"><Loader2 className="size-5 motion-safe:animate-spin motion-reduce:animate-none text-[var(--app-primary)]" aria-label="Loading session artifacts" /></div> : null}
      {error && artifacts.length === 0 ? <p className={cn('text-xs text-[var(--app-danger)]', thin ? 'sr-only' : 'rounded-lg border border-[var(--app-danger)]/40 bg-[var(--app-danger-bg)] p-3')}>{error}</p> : null}
      {!loading && !error && artifacts.length === 0 ? <p className={cn('text-xs text-[var(--app-text-muted)]', thin ? 'sr-only' : 'p-3 text-center')}>Artifacts created in this session will appear here.</p> : null}

      {artifacts.length > 0 ? (
        <div
          className={cn(
            'min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]',
            thin ? 'grid auto-rows-max gap-2' : embedded ? 'flex gap-2 overflow-x-auto overflow-y-hidden pb-1' : 'grid auto-rows-max gap-3',
          )}
          aria-label="Session artifact thumbnails"
          data-artifact-thumbnail-rail
        >
          {groups.map((group) => {
            const representative = group.entries.find((entry) => entry.selected)
              ?? group.entries.find((entry) => entry.status === 'ready')
              ?? group.entries[0]
            if (!representative) return null
            const grouped = Boolean(group.collectionId)
            const requirementLabel = formatDesktopV3ArtifactOutputRequirements(representative.outputRequirements)
            if (thin) {
              const representativeKey = sidebarArtifactPreviewKey(representative)
              return <a key={group.key} href={artifactHref(representative)} className="group relative grid size-10 place-items-center overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onPointerEnter={() => requestLivePreview(representative)} onFocus={() => requestLivePreview(representative)} onClick={(event: MouseEvent<HTMLAnchorElement>) => { if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); onOpenArtifact(representative) }} aria-label={`Open ${grouped ? group.label : representative.label} in full artifact view`}><DesktopV3ArtifactThumbnail artifact={representative} live={representativeKey === livePreviewKey} />{group.progress.staging > 0 ? <span className="absolute bottom-0 right-0 size-2 rounded-full bg-[var(--app-primary)]" aria-label={sidebarProgressLabel(group)} /> : null}</a>
            }
            return (
              <section key={group.key} className={cn('min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]', embedded ? 'w-64 shrink-0' : 'w-full')} data-artifact-collection-group={grouped ? group.collectionId : undefined}>
                <a href={artifactHref(representative)} className="flex min-w-0 items-center justify-between gap-2 border-b border-[var(--app-border)] px-3 py-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={(event: MouseEvent<HTMLAnchorElement>) => { if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); onOpenArtifact(representative) }}>
                  <span className="min-w-0"><span className="block truncate text-xs font-semibold">{grouped ? group.label : representative.label}</span><span className="mt-0.5 block text-[10px] text-[var(--app-text-subtle)]">{grouped ? sidebarProgressLabel(group) : representative.status === 'staging' ? 'Generating' : representative.kind || representative.mediaType}</span>{requirementLabel ? <span className="mt-0.5 block truncate text-[9px] text-[var(--app-text-subtle)]" data-artifact-output-requirements>{requirementLabel}</span> : null}</span>
                  {group.progress.staging > 0 ? <Loader2 className="size-4 shrink-0 motion-safe:animate-spin motion-reduce:animate-none text-[var(--app-primary)]" aria-label="Iteration Swarm generating" /> : <Maximize2 className="size-4 shrink-0 text-[var(--app-text-subtle)]" aria-hidden="true" />}
                </a>
                <div className={cn('grid gap-1 p-2', grouped && 'grid-cols-2')} aria-label={grouped ? `${group.label} iterations` : undefined}>
                  {group.entries.map((artifact, index) => (
                    <div key={`${artifact.sessionId}:${artifact.collectionId ?? ''}:${artifact.artifactId}`} className="group relative min-w-0 overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)]" onPointerEnter={() => requestLivePreview(artifact)} onFocusCapture={() => requestLivePreview(artifact)}>
                      <a href={artifactHref(artifact)} className="block min-w-0 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={(event: MouseEvent<HTMLAnchorElement>) => { if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); onOpenArtifact(artifact) }} aria-label={`Open ${artifact.label} in full artifact view`}>
                        <span className="relative grid h-20 place-items-center overflow-hidden"><DesktopV3ArtifactThumbnail artifact={artifact} live={sidebarArtifactPreviewKey(artifact) === livePreviewKey} /></span>
                        <span className="block min-w-0 px-2 py-1.5"><span className="block truncate text-[10px] font-semibold">{artifact.lineage?.iterationIndex ? `${artifact.lineage.iterationIndex}. ${artifact.lineage.iterationLabel || artifact.lineage.iterationTheme || artifact.label}` : artifact.label}</span><span className="block truncate text-[9px] text-[var(--app-text-subtle)]">{artifact.status === 'staging' ? 'Generating' : artifact.status === 'failed' || artifact.status === 'unavailable' ? 'Failed' : grouped ? `Iteration ${artifact.lineage?.iterationIndex || index + 1}` : artifact.kind || artifact.mediaType}</span></span>
                      </a>
                      {onAddToChat && artifact.status === 'ready' && artifact.collectionId && (artifact.eventSeq ?? 0) > 0 ? <button type="button" className="absolute right-1 top-1 grid size-7 place-items-center rounded-md bg-[var(--app-primary)] text-white opacity-0 shadow-md transition hover:opacity-90 group-hover:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white" aria-label={`Add ${artifact.label} to chat`} title="Add to chat" onClick={() => onAddToChat([artifact])}><MessageSquarePlus size={13} aria-hidden="true" /></button> : null}
                    </div>
                  ))}
                </div>
              </section>
            )
          })}
        </div>
      ) : null}
    </aside>
  )
}
