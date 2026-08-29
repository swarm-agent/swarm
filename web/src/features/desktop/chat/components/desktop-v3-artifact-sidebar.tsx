import { memo, useEffect, useMemo, useState, type MouseEvent } from 'react'
import { FileText, GalleryHorizontal, Loader2, Maximize2, MessageSquarePlus, TriangleAlert } from 'lucide-react'

import { cn } from '../../../../lib/cn'
import {
  fetchDesktopV3ArtifactPreviewAccess,
  preflightDesktopV3ArtifactDirectContent,
  formatDesktopV3ArtifactAnimationProfile,
  formatDesktopV3ArtifactOutputRequirements,
  desktopV3ArtifactMessageSelection,
  desktopV3ArtifactPartIterationMessageSelection,
  desktopV3ArtifactPartMessageSelection,
  selectDesktopV3ArtifactPartRevisions,
  type DesktopV3ArtifactCatalogEntry,
  type DesktopV3ArtifactMessageSelection,
  type DesktopV3ArtifactCollectionProgress,
} from '../../session-v3/artifact-api'
import type { DesktopSidebarDisplayMode, DesktopV3SessionSidebarView } from './desktop-sidebar-display'
import { desktopV3ArtifactStudioPresentationGroupKey, desktopV3ArtifactStudioSamePartRevision, desktopV3ArtifactStudioStoryboard, desktopV3ArtifactStudioTurns } from '../../session-v3/artifact-studio-model'
import { refreshOpenDesktopV3ArtifactCatalogs } from '../../session-v3/artifact-catalog-refresh'
import { useDesktopV3ArtifactPreviewVisibility } from './desktop-v3-artifact-preview-thumbnail'

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
  onOpenArtifact: (artifact: DesktopV3ArtifactCatalogEntry, partId?: string, iterationCollectionId?: string) => void
  onAddToChat?: (selections: DesktopV3ArtifactMessageSelection[]) => void
}

function sidebarArtifactAnimationProfileKey(artifact: DesktopV3ArtifactCatalogEntry): string {
  const profile = artifact.animationProfile
  if (!profile) return ''
  const budgets = profile.budgets
  return [
    profile.profileId,
    profile.registryVersion,
    profile.runtimeKind,
    profile.runtimePackage,
    profile.runtimeVersion,
    profile.secondaryRuntimePackage,
    profile.secondaryRuntimeVersion,
    profile.heavy,
    profile.importedPlaybackOnly,
    profile.editableSourceRequired,
    budgets.maxSimultaneousLivePreviews,
    budgets.maxWebGLContexts,
    budgets.maxDevicePixelRatio,
    budgets.maxCanvasPixels,
    budgets.maxParticles,
    budgets.maxDrawCallsPerFrame,
    budgets.pauseWhenOffscreen,
    budgets.stopWhenDocumentHidden,
    budgets.reducedMotionBehavior,
    budgets.networkAllowed,
  ].join(':')
}

function sidebarArtifactThumbnailEqual(
  previous: Readonly<{ artifact: DesktopV3ArtifactCatalogEntry }>,
  next: Readonly<{ artifact: DesktopV3ArtifactCatalogEntry }>,
): boolean {
  const left = previous.artifact
  const right = next.artifact
  return left.sessionId === right.sessionId
    && left.artifactId === right.artifactId
    && left.label === right.label
    && left.mediaType === right.mediaType
    && left.kind === right.kind
    && left.status === right.status
    && left.previewable === right.previewable
    && left.eventSeq === right.eventSeq
    && left.updatedAt === right.updatedAt
    && sidebarArtifactAnimationProfileKey(left) === sidebarArtifactAnimationProfileKey(right)
}

export function desktopV3ArtifactSidebarPartChatSelection(
  artifact: DesktopV3ArtifactCatalogEntry,
  partId: string,
): DesktopV3ArtifactMessageSelection {
  return desktopV3ArtifactPartMessageSelection(artifact, partId, 'use')
}

const DesktopV3ArtifactThumbnail = memo(function DesktopV3ArtifactThumbnail({ artifact }: { artifact: DesktopV3ArtifactCatalogEntry }) {
  const [previewURL, setPreviewURL] = useState('')
  const [failed, setFailed] = useState(false)
  const { previewRef, previewVisible } = useDesktopV3ArtifactPreviewVisibility<HTMLSpanElement>()
  const previewEnabled = previewVisible

  useEffect(() => {
    setPreviewURL('')
    setFailed(false)
    if (!previewEnabled || !artifact.previewable || artifact.status !== 'ready') return undefined

    const controller = new AbortController()
    const resolveURL = artifact.mediaType === 'text/html'
      ? fetchDesktopV3ArtifactPreviewAccess(artifact.sessionId, artifact.artifactId, controller.signal).then((access) => access.url)
      : preflightDesktopV3ArtifactDirectContent(artifact, controller.signal)
    void resolveURL
      .then((url) => {
        if (!controller.signal.aborted) setPreviewURL(url)
      })
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true)
      })

    return () => controller.abort()
  }, [artifact.animationProfile, artifact.artifactId, artifact.mediaType, artifact.previewable, artifact.sessionId, artifact.status, previewEnabled])

  let thumbnail = <FileText className="size-5 text-[var(--app-text-muted)]" aria-hidden="true" />
  if (artifact.status === 'staging') thumbnail = <Loader2 className="size-5 motion-safe:animate-spin motion-reduce:animate-none text-[var(--app-primary)]" aria-label="Generating artifact" />
  else if (artifact.status === 'failed' || artifact.status === 'unavailable' || failed) thumbnail = <TriangleAlert className="size-5 text-[var(--app-danger)]" aria-label="Artifact unavailable" />
  else if (previewEnabled && artifact.mediaType.startsWith('image/') && previewURL) thumbnail = <img src={previewURL} alt="" className="size-full object-contain" onError={() => { setFailed(true); setPreviewURL('') }} />
  else if (previewEnabled && (!artifact.animationProfile || artifact.animationProfile.profileId === 'final_render') && (artifact.mediaType.startsWith('video/') || artifact.kind === 'video') && previewURL) thumbnail = <video src={previewURL} muted playsInline preload="metadata" className="size-full object-contain bg-black" onError={() => { setFailed(true); setPreviewURL('') }} />
  else if (previewEnabled && artifact.mediaType === 'text/html' && previewURL) thumbnail = <iframe title={`${artifact.label} thumbnail`} src={previewURL} sandbox="allow-scripts" referrerPolicy="no-referrer" tabIndex={-1} className="pointer-events-none absolute left-0 top-0 size-[400%] origin-top-left scale-25 border-0 bg-white" onError={() => { setFailed(true); setPreviewURL('') }} />
  else if (previewEnabled && artifact.mediaType === 'application/pdf' && previewURL) thumbnail = <iframe title={`${artifact.label} thumbnail`} src={previewURL} sandbox="" referrerPolicy="no-referrer" tabIndex={-1} className="pointer-events-none size-full border-0 bg-white" onError={() => { setFailed(true); setPreviewURL('') }} />

  return <span ref={previewRef} className="relative grid size-full place-items-center overflow-hidden" data-artifact-live-preview={previewEnabled && artifact.mediaType === 'text/html' ? true : undefined} data-artifact-preview-visible={previewEnabled || undefined} data-artifact-animation-profile={artifact.animationProfile?.profileId} data-artifact-animation-active={previewEnabled && Boolean(artifact.animationProfile) || undefined}>{thumbnail}</span>
}, sidebarArtifactThumbnailEqual)

export type DesktopV3ArtifactSidebarSection = 'active' | 'motion' | 'visual' | 'documents' | 'supporting'

export interface DesktopV3ArtifactSidebarGroup {
  key: string
  collectionId: string
  section: DesktopV3ArtifactSidebarSection
  entries: DesktopV3ArtifactCatalogEntry[]
  progress: DesktopV3ArtifactCollectionProgress
  label: string
}

export interface DesktopV3ArtifactSidebarIterationGroup {
  target: DesktopV3ArtifactCatalogEntry
  partId: string
  partLabel: string
  iterationCount: number
  collectionId: string
}

export function desktopV3ArtifactSidebarSection(entry: DesktopV3ArtifactCatalogEntry, activeChainIds: ReadonlySet<string>): DesktopV3ArtifactSidebarSection {
  if (entry.role === 'render_only') return 'supporting'
  if (entry.artifactChainId && activeChainIds.has(entry.artifactChainId)) return 'active'
  const mediaType = entry.mediaType.split(';', 1)[0]?.trim().toLowerCase() ?? ''
  const kind = entry.kind.trim().toLowerCase()
  if (mediaType === 'text/html' || kind === 'html' || Boolean(entry.animationProfile)) return 'motion'
  if (mediaType.startsWith('image/') || mediaType.startsWith('video/') || kind === 'image' || kind === 'video' || entry.category === 'visual') return 'visual'
  return 'documents'
}

const sidebarSectionOrder: Record<DesktopV3ArtifactSidebarSection, number> = { active: 0, motion: 1, visual: 2, documents: 3, supporting: 4 }
const sidebarSectionLabel: Record<DesktopV3ArtifactSidebarSection, string> = { active: 'Active artifact', motion: 'HTML & motion', visual: 'Visuals', documents: 'Documents', supporting: 'Supporting renders' }

function sidebarCollectionProgress(entries: readonly DesktopV3ArtifactCatalogEntry[]): DesktopV3ArtifactCollectionProgress {
  const collectionKeys = new Set(entries.map((entry) => `${entry.sessionId}:${entry.collectionId ?? ''}`))
  const reported = collectionKeys.size === 1 ? entries.find((entry) => entry.progress)?.progress : undefined
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
  const authoredChainEntries = artifacts.filter((artifact) => artifact.role !== 'render_only' && artifact.graphState === 'git_projection' && artifact.artifactChainId
    && ((artifact.chain?.revisionCount ?? 0) > 1 || (artifact.step?.revisionNumber ?? 0) > 1))
  const activeChainId = [...authoredChainEntries].sort((left, right) => {
    const leftHead = left.chain?.head
    const rightHead = right.chain?.head
    const leftIsHead = Boolean(leftHead && left.sessionId === leftHead.sessionId && left.collectionId === leftHead.collectionId && left.artifactId === leftHead.variantId && left.eventSeq === leftHead.eventSeq)
    const rightIsHead = Boolean(rightHead && right.sessionId === rightHead.sessionId && right.collectionId === rightHead.collectionId && right.artifactId === rightHead.variantId && right.eventSeq === rightHead.eventSeq)
    return Number(rightIsHead) - Number(leftIsHead) || right.updatedAt - left.updatedAt
  })[0]?.artifactChainId ?? ''
  const activeChainIds = new Set(activeChainId ? [activeChainId] : [])
  const groups = new Map<string, DesktopV3ArtifactCatalogEntry[]>()
  for (const artifact of artifacts) {
    const section = desktopV3ArtifactSidebarSection(artifact, activeChainIds)
    const presentationKey = desktopV3ArtifactStudioPresentationGroupKey(artifacts, artifact)
    const key = section === 'documents' || section === 'supporting' ? `media:${section}` : presentationKey
    groups.set(key, [...(groups.get(key) ?? []), artifact])
  }
  return [...groups.entries()].map(([key, entries]) => ({
    key,
    collectionId: entries[0]?.collectionId?.trim() ?? '',
    section: desktopV3ArtifactSidebarSection(entries[0]!, activeChainIds),
    entries: [...entries].sort((left, right) => (left.step?.revisionNumber ?? 0) - (right.step?.revisionNumber ?? 0)
      || (left.candidateIndex || left.lineage?.iterationIndex || 0) - (right.candidateIndex || right.lineage?.iterationIndex || 0)
      || left.updatedAt - right.updatedAt),
    progress: sidebarCollectionProgress(entries),
    label: key.startsWith('media:')
      ? sidebarSectionLabel[desktopV3ArtifactSidebarSection(entries[0]!, activeChainIds)]
      : key.startsWith('chain:')
        ? entries[0]?.chain?.name || (entries[0] ? sidebarCollectionLabel(entries[0]) : 'Artifact')
        : entries[0] ? sidebarCollectionLabel(entries[0]) : 'Artifact',
  })).sort((left, right) => sidebarSectionOrder[left.section] - sidebarSectionOrder[right.section]
    || Math.max(...right.entries.map((entry) => entry.updatedAt)) - Math.max(...left.entries.map((entry) => entry.updatedAt))
    || left.label.localeCompare(right.label))
}

/**
 * Collapses one Iteration Swarm wave into a single sidebar entry. Initial waves
 * use their durable turn lineage; later artifact-chain turns use the latest
 * authoritative step and retain an exact part target when the turn is focused.
 */
export function desktopV3ArtifactSidebarIterationGroup(
  artifacts: readonly DesktopV3ArtifactCatalogEntry[],
  group: DesktopV3ArtifactSidebarGroup,
): DesktopV3ArtifactSidebarIterationGroup | undefined {
  const hasIterationLineage = group.entries.some((entry) => Boolean(entry.lineage?.iterationGroupId?.trim()
    || entry.composition?.iterationGroupId?.trim()))
  const representative = group.entries[0]
  if (!representative) return undefined

  if (group.key.startsWith('chain:')) {
    const turns = desktopV3ArtifactStudioTurns(artifacts, representative)
    const turn = [...turns].reverse().find((candidate) => candidate.candidates.length > 1
      && candidate.candidates.some((option) => Boolean(option.entry?.lineage?.iterationGroupId?.trim()
        || option.entry?.composition?.iterationGroupId?.trim())))
    if (turn) {
      const target = turn.accepted?.entry ?? turn.candidates.find((candidate) => candidate.entry)?.entry
      if (!target) return undefined
      const singlePartId = turn.parts.length === 1
        ? turn.parts[0]!.partId
        : turn.relatedTargets.length === 1 ? turn.relatedTargets[0]!.partId : ''
      const definition = target.partDefinitions?.find((part) => part.id === singlePartId)
        ?? representative.partDefinitions?.find((part) => part.id === singlePartId)
      const relatedTarget = turn.relatedTargets.find((part) => part.partId === singlePartId)
      return {
        target,
        partId: singlePartId,
        partLabel: definition?.label || relatedTarget?.label || singlePartId,
        iterationCount: turn.candidates.length,
        collectionId: '',
      }
    }
  }

  if (!hasIterationLineage) return undefined
  const lineagePartIds = [...new Set(group.entries.map((entry) => entry.lineage?.partId || entry.lineage?.iterationSectionId || '').filter(Boolean))]
  const partId = lineagePartIds.length === 1 ? lineagePartIds[0]! : ''
  const partEntry = group.entries.find((entry) => (entry.lineage?.partId || entry.lineage?.iterationSectionId) === partId)
  return {
    target: partEntry ?? representative,
    partId,
    partLabel: partEntry?.lineage?.partLabel || partEntry?.lineage?.iterationSectionLabel || partId,
    iterationCount: group.entries.length,
    collectionId: partEntry?.collectionId ?? representative.collectionId ?? '',
  }
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
  const [partSelectionPending, setPartSelectionPending] = useState('')
  const [partSelectionError, setPartSelectionError] = useState('')

  const applyPartChoice = async (source: DesktopV3ArtifactCatalogEntry, candidate: DesktopV3ArtifactCatalogEntry, partId: string, locked: boolean) => {
    const slot = candidate.composition?.parts.find((part) => part.partId === partId)
    const revision = slot ? candidate.partRevisions?.find((partRevision) => partRevision.reference.partId === partId
      && partRevision.reference.partRevisionId === slot.revision.partRevisionId
      && partRevision.reference.ownerSessionId === slot.revision.ownerSessionId
      && partRevision.reference.digestSha256 === slot.revision.digestSha256) : undefined
    if (!slot || !revision?.eventSeq) {
      setPartSelectionError(`Exact ${partId} revision unavailable; refresh and try again.`)
      return
    }
    const pendingKey = `${partId}:${slot.revision.ownerSessionId}:${slot.revision.partRevisionId}:${locked}`
    try {
      setPartSelectionPending(pendingKey)
      setPartSelectionError('')
      await selectDesktopV3ArtifactPartRevisions(source, [{ partId, revision: slot.revision, revisionEventSeq: revision.eventSeq, locked }])
      await refreshOpenDesktopV3ArtifactCatalogs()
    } catch (error) {
      await refreshOpenDesktopV3ArtifactCatalogs()
      setPartSelectionError(error instanceof Error ? `${error.message} Catalog refreshed.` : 'Part selection conflicted; catalog refreshed.')
    } finally {
      setPartSelectionPending('')
    }
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

      {partSelectionError ? <p className={cn('mb-2 text-[10px] text-[var(--app-danger)]', thin && 'sr-only')} role="alert">{partSelectionError}</p> : null}
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
            const iterationGroup = desktopV3ArtifactSidebarIterationGroup(artifacts, group)
            const projected = group.entries.find((entry) => entry.role !== 'render_only' && entry.graphState === 'git_projection' && entry.chain?.head)
            const head = projected?.chain?.head
            const representative = head
              ? group.entries.find((entry) => entry.sessionId === head.sessionId
                && entry.collectionId === head.collectionId
                && entry.artifactId === head.variantId
                && entry.eventSeq === head.eventSeq)
              : group.entries[0]
            if (!representative) return null
            const grouped = group.entries.length > 1 || Boolean(iterationGroup)
            const turnBased = group.key.startsWith('chain:') && group.section === 'active'
            const compactRows = group.section === 'documents' || group.section === 'supporting'
            const artifactTurns = turnBased ? desktopV3ArtifactStudioTurns(artifacts, representative) : []
            const storyboard = turnBased ? desktopV3ArtifactStudioStoryboard(artifacts, representative) : undefined
            const initialStoryboardPart = storyboard?.parts[0]
            const openTarget = iterationGroup?.target ?? initialStoryboardPart?.still ?? storyboard?.source ?? representative
            const openPartId = iterationGroup?.partId ?? initialStoryboardPart?.id ?? ''
            const authoritativeHead = storyboard?.source ?? (head
              ? artifacts.find((entry) => entry.role !== 'render_only' && entry.sessionId === head.sessionId && entry.collectionId === head.collectionId && entry.artifactId === head.variantId && entry.eventSeq === head.eventSeq)
              : undefined)
            const currentComposition = authoritativeHead?.composition ?? representative.composition
            const currentPartDefinitions = authoritativeHead?.partDefinitions ?? representative.partDefinitions ?? []
            const latestTurnId = artifactTurns[artifactTurns.length - 1]?.id ?? ''
            const partDefinitionsById = new Map(currentPartDefinitions.map((part) => [part.id, part]))
            const requirementLabel = formatDesktopV3ArtifactOutputRequirements(representative.outputRequirements)
            const animationLabel = formatDesktopV3ArtifactAnimationProfile(representative.animationProfile)
            if (thin) {
              return <a key={group.key} href={artifactHref(openTarget)} className="group relative grid size-10 place-items-center overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={(event: MouseEvent<HTMLAnchorElement>) => { if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); onOpenArtifact(openTarget, openPartId, iterationGroup?.collectionId) }} aria-label={`Open ${grouped ? group.label : representative.label} in full artifact view`}><DesktopV3ArtifactThumbnail artifact={representative} />{group.progress.staging > 0 ? <span className="absolute bottom-0 right-0 size-2 rounded-full bg-[var(--app-primary)]" aria-label={sidebarProgressLabel(group)} /> : null}</a>
            }
            return (
              <section key={group.key} className={cn('min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]', embedded ? 'w-64 shrink-0' : 'w-full')} data-artifact-collection-group={grouped ? group.collectionId : undefined} data-artifact-sidebar-section={group.section}>
                <a href={artifactHref(openTarget)} className="flex min-w-0 items-center justify-between gap-2 border-b border-[var(--app-border)] px-3 py-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={(event: MouseEvent<HTMLAnchorElement>) => { if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); onOpenArtifact(openTarget, openPartId, iterationGroup?.collectionId) }}>
                  <span className="min-w-0"><span className="block truncate text-xs font-semibold">{turnBased ? (representative.chain?.name || representative.label) : compactRows ? group.label : grouped ? group.label : representative.label}</span><span className="mt-0.5 block text-[10px] text-[var(--app-text-subtle)]">{storyboard ? `One initial proposal · ${storyboard.parts.length} ordered parts · Current proposal` : iterationGroup ? `${iterationGroup.partLabel ? `${iterationGroup.partLabel} · ` : 'Overall · '}${iterationGroup.iterationCount} iteration${iterationGroup.iterationCount === 1 ? '' : 's'} · Click to open group` : turnBased ? `${artifactTurns.length} authored turn${artifactTurns.length === 1 ? '' : 's'} · Current head · Turn ${authoritativeHead?.step?.revisionNumber || representative.step?.revisionNumber || 1}` : compactRows ? `${group.entries.length} ${group.section === 'supporting' ? 'render asset' : 'document'}${group.entries.length === 1 ? '' : 's'}` : grouped ? `Overall iterations · ${sidebarProgressLabel(group)}` : `Unstructured · ${representative.status === 'staging' ? 'Generating' : representative.kind || representative.mediaType}`}</span>{requirementLabel ? <span className="mt-0.5 block truncate text-[9px] text-[var(--app-text-subtle)]" data-artifact-output-requirements>{requirementLabel}</span> : null}{animationLabel ? <span className="mt-0.5 block truncate text-[9px] text-[var(--app-text-subtle)]" data-artifact-animation-profile-label>{animationLabel}</span> : null}</span>
                  {group.progress.staging > 0 ? <Loader2 className="size-4 shrink-0 motion-safe:animate-spin motion-reduce:animate-none text-[var(--app-primary)]" aria-label="Iteration Swarm generating" /> : <Maximize2 className="size-4 shrink-0 text-[var(--app-text-subtle)]" aria-hidden="true" />}
                </a>
                {iterationGroup ? <button type="button" className="flex w-full items-center justify-between gap-2 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 text-left text-[10px] hover:bg-[var(--app-surface-hover)]" onClick={() => onOpenArtifact(iterationGroup.target, iterationGroup.partId, iterationGroup.collectionId)} data-artifact-sidebar-iteration-group={group.key}><span className="min-w-0 truncate font-semibold">{iterationGroup.partLabel ? `${iterationGroup.partLabel} iterations` : 'Initial iterations'}</span><span className="shrink-0 text-[var(--app-text-subtle)]">{iterationGroup.iterationCount} grouped</span></button> : compactRows ? <details className="border-b border-[var(--app-border)]" open={group.section === 'documents'} data-artifact-sidebar-compact-group={group.section}>
                  <summary className="cursor-pointer px-3 py-1.5 text-[10px] font-semibold text-[var(--app-text-muted)]">{group.section === 'supporting' ? 'Show supporting render assets' : 'Session documents'}</summary>
                  <div className="grid divide-y divide-[var(--app-border)]">{group.entries.map((artifact) => <a key={`${artifact.sessionId}:${artifact.collectionId ?? ''}:${artifact.artifactId}`} href={artifactHref(artifact)} className="flex min-w-0 items-center gap-2 px-3 py-2 text-left hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={(event: MouseEvent<HTMLAnchorElement>) => { if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); onOpenArtifact(artifact) }} data-artifact-sidebar-document-row={group.section === 'documents' || undefined} data-artifact-sidebar-supporting-row={group.section === 'supporting' || undefined}><FileText className="size-3.5 shrink-0 text-[var(--app-text-muted)]" aria-hidden="true" /><span className="min-w-0 flex-1"><span className="block truncate text-[10px] font-semibold">{artifact.filename || artifact.label}</span><span className="block truncate text-[9px] text-[var(--app-text-subtle)]">{artifact.label !== artifact.filename ? artifact.label : artifact.mediaType}</span></span></a>)}</div>
                </details> : iterationGroup ? null : storyboard ? <div className="grid gap-2 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] p-2" aria-label="Storyboard proposal" data-artifact-sidebar-storyboard>
                  <section className="overflow-hidden rounded-lg border border-[var(--app-primary)] bg-[var(--app-surface)]">
                    <button type="button" className="flex w-full items-center justify-between gap-2 px-2 py-1.5 text-left hover:bg-[var(--app-surface-hover)]" onClick={() => onOpenArtifact(openTarget, openPartId, iterationGroup?.collectionId)}>
                      <span className="text-[10px] font-semibold">Initial proposal</span>
                      <span className="text-[9px] text-[var(--app-text-subtle)]">Current · {storyboard.parts.length} parts</span>
                    </button>
                    <div className="grid gap-1 border-t border-[var(--app-border)] p-1.5" aria-label="Storyboard parts">
                      {storyboard.parts.map((part, partIndex) => {
                        const target = part.still ?? part.source
                        const status = part.still?.status === 'staging' ? 'Generating' : part.still?.status === 'failed' || part.still?.status === 'unavailable' ? 'Failed' : part.still ? 'Ready' : 'Source'
                        return <button key={part.id} type="button" className="flex min-w-0 items-center gap-2 rounded-md border border-[var(--app-border)] bg-[var(--app-bg)] px-2 py-1.5 text-left hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)]" onClick={() => onOpenArtifact(target, part.id)} data-artifact-sidebar-storyboard-part={part.id}>
                          <span className="grid size-5 shrink-0 place-items-center rounded-full bg-[var(--app-surface-active)] font-mono text-[9px]">{partIndex + 1}</span>
                          <span className="min-w-0 flex-1 truncate text-[10px] font-semibold">{part.label}</span>
                          <span className="shrink-0 text-[9px] text-[var(--app-text-subtle)]">{status}</span>
                        </button>
                      })}
                    </div>
                  </section>
                </div> : artifactTurns.length > 0 ? <div className="grid gap-2 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] p-2" aria-label="Artifact turn progression" data-artifact-sidebar-turn-progression>
                  {artifactTurns.map((turn, turnIndex) => {
                    const turnAccepted = turn.accepted?.entry
                    const turnTarget = turnAccepted ?? turn.candidates.find((candidate) => candidate.entry)?.entry
                    const currentTurn = turn.id === latestTurnId
                    const headTurn = turn.candidates.some((candidate) => Boolean(authoritativeHead && candidate.entry
                      && candidate.entry.sessionId === authoritativeHead.sessionId
                      && candidate.entry.collectionId === authoritativeHead.collectionId
                      && candidate.entry.artifactId === authoritativeHead.artifactId
                      && candidate.entry.eventSeq === authoritativeHead.eventSeq))
                    return <section key={turn.id} className="overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)]" data-artifact-sidebar-turn={turn.id}>
                      <button type="button" className="flex w-full items-center justify-between gap-2 px-2 py-1.5 text-left hover:bg-[var(--app-surface-hover)] disabled:opacity-50" disabled={!turnTarget} onClick={() => { if (turnTarget) onOpenArtifact(turnTarget) }}>
                        <span className="text-[10px] font-semibold">Turn {turn.revisionNumber}</span>
                        <span className="text-[9px] text-[var(--app-text-subtle)]">{currentTurn ? 'Current authored turn' : 'Authored turn history'} · {headTurn ? 'Authoritative head' : turn.accepted ? 'Decision recorded' : 'No decision'} · {turn.candidates.length} option{turn.candidates.length === 1 ? '' : 's'}</span>
                      </button>
                      {turn.parts.length === 1 ? <div className="grid gap-1 border-t border-[var(--app-border)] p-1.5"><p className="px-0.5 text-[9px] text-[var(--app-text-subtle)]">1 changed part</p>
                        {turn.parts.map((turnPart) => {
                          const definition = partDefinitionsById.get(turnPart.partId)
                          const currentSlot = currentComposition?.parts.find((part) => part.partId === turnPart.partId)
                          const acceptedSlot = authoritativeHead?.acceptedPartHeads?.find((part) => part.partId === turnPart.partId)
                          const acceptedCurrent = Boolean(currentSlot && acceptedSlot && desktopV3ArtifactStudioSamePartRevision(currentSlot, acceptedSlot))
                          const partStatus = turnPart.accepted ? 'Decision recorded' : currentTurn ? 'Choose an option' : acceptedCurrent ? 'Accepted head' : currentSlot?.locked ? 'Locked pending' : 'History'
                          return <section key={turnPart.partId} className="rounded-md border border-[var(--app-border)] bg-[var(--app-bg)] p-1.5" data-artifact-sidebar-turn-part={turnPart.partId}>
                            <div className="flex items-center justify-between gap-2 px-0.5 pb-1">
                              <button type="button" className="truncate text-left text-[10px] font-semibold hover:text-[var(--app-primary)]" onClick={() => { const target = turnPart.accepted?.entry ?? turnPart.candidates.find((candidate) => candidate.entry)?.entry; if (target) onOpenArtifact(target, turnPart.partId) }}>{definition?.label || turnPart.partId}</button>
                              <span className="flex shrink-0 items-center gap-1"><span className="text-[9px] text-[var(--app-text-subtle)]">{partStatus}</span>{onAddToChat && authoritativeHead ? <><button type="button" className="grid size-5 place-items-center rounded border border-[var(--app-border)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-active)]" aria-label={`Attach ${definition?.label || turnPart.partId} for chat changes`} title={`Attach ${definition?.label || turnPart.partId} as the exact change target`} onClick={() => onAddToChat([desktopV3ArtifactSidebarPartChatSelection(authoritativeHead, turnPart.partId)])}><MessageSquarePlus size={10} aria-hidden="true" /></button><button type="button" className="rounded border border-[var(--app-border)] px-1 py-0.5 text-[9px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-primary-soft)]" onClick={() => onAddToChat([desktopV3ArtifactPartIterationMessageSelection(authoritativeHead, turnPart.partId, 3)])} data-artifact-sidebar-iterate-part>Iterate</button></> : null}</span>
                            </div>
                            <div className="grid gap-1" data-artifact-sidebar-part-choices={turnPart.partId}>{turnPart.candidates.map((candidate, candidateIndex) => {
                              const artifact = candidate.entry
                              const candidateSlot = candidate.part
                              const acceptedOption = Boolean(turnPart.accepted && candidate.reference.sessionId === turnPart.accepted.reference.sessionId && candidate.reference.collectionId === turnPart.accepted.reference.collectionId && candidate.reference.variantId === turnPart.accepted.reference.variantId && candidate.reference.eventSeq === turnPart.accepted.reference.eventSeq)
                              const pendingKey = candidateSlot ? `${turnPart.partId}:${candidateSlot.revision.ownerSessionId}:${candidateSlot.revision.partRevisionId}:true` : ''
                              return <div key={`${candidate.reference.sessionId}:${candidate.reference.variantId}:${turnPart.partId}`} className="flex min-w-0 items-center gap-1">
                                <button type="button" className="flex min-w-0 flex-1 items-center gap-1.5 rounded border border-[var(--app-border)] px-1.5 py-1 text-left text-[9px] hover:bg-[var(--app-surface-active)] disabled:opacity-50" disabled={!artifact} onClick={() => { if (artifact) onOpenArtifact(artifact, turnPart.partId) }}><span className="grid size-4 shrink-0 place-items-center rounded-full bg-[var(--app-surface-active)] font-mono">{artifact?.candidateIndex || candidateIndex + 1}</span><span className="truncate font-semibold">Option {artifact?.candidateIndex || candidateIndex + 1}</span><span className="ml-auto shrink-0 text-[var(--app-text-subtle)]">{!artifact ? 'Pending' : artifact.status === 'staging' ? 'Generating' : artifact.status === 'failed' || artifact.status === 'unavailable' ? 'Failed' : acceptedOption ? 'Committed' : currentTurn ? 'Available' : 'History'}</span></button>
                                {authoritativeHead && artifact?.status === 'ready' ? <button type="button" className="shrink-0 rounded border border-[var(--app-border)] px-1.5 py-1 text-[9px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-active)] disabled:opacity-50" disabled={!candidateSlot || Boolean(partSelectionPending)} onClick={() => void applyPartChoice(authoritativeHead, artifact, turnPart.partId, true)}>{partSelectionPending === pendingKey ? <Loader2 className="mr-1 inline size-2.5 animate-spin" /> : null}Lock</button> : null}
                              </div>
                            })}</div>
                          </section>
                        })}
                      </div> : <div className="border-t border-[var(--app-border)] px-2 py-1.5 text-[9px] text-[var(--app-text-subtle)]">{turnIndex === 0 ? 'Original composition' : turn.parts.length > 1 ? `${turn.parts.length} parts changed together` : 'Composition-only update'}{turnTarget ? <button type="button" className="ml-2 font-semibold text-[var(--app-primary)]" onClick={() => onOpenArtifact(turnTarget)}>Open iteration</button> : null}</div>}
                    </section>
                  })}
                  {authoritativeHead && currentComposition?.parts.some((part) => part.locked) ? <div className="flex flex-wrap gap-1">{currentComposition.parts.filter((part) => part.locked).map((part) => <button key={part.partId} type="button" className="rounded border border-[var(--app-border)] px-1.5 py-0.5 text-[9px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-active)] disabled:opacity-50" disabled={Boolean(partSelectionPending)} onClick={() => void applyPartChoice(authoritativeHead, authoritativeHead, part.partId, false)}>Unlock {partDefinitionsById.get(part.partId)?.label || part.partId}</button>)}</div> : null}
                </div> : <div className={cn('grid gap-1 p-2', grouped && 'grid-cols-2')} aria-label={grouped ? `${group.label} iterations` : undefined}>
                  {group.entries.map((artifact, index) => (
                    <div key={`${artifact.sessionId}:${artifact.collectionId ?? ''}:${artifact.artifactId}`} className="group relative min-w-0 overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)]">
                      <a href={artifactHref(artifact)} className="block min-w-0 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" onClick={(event: MouseEvent<HTMLAnchorElement>) => { if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); onOpenArtifact(artifact) }} aria-label={`Open ${artifact.label} in full artifact view`}>
                        <span className="relative grid h-20 place-items-center overflow-hidden"><DesktopV3ArtifactThumbnail artifact={artifact} /></span>
                        <span className="block min-w-0 px-2 py-1.5"><span className="block truncate text-[10px] font-semibold">{artifact.lineage?.iterationIndex ? `${artifact.lineage.iterationIndex}. ${artifact.lineage.iterationLabel || artifact.lineage.iterationTheme || artifact.label}` : artifact.label}</span><span className="block truncate text-[9px] text-[var(--app-text-subtle)]">{artifact.status === 'staging' ? 'Generating' : artifact.status === 'failed' || artifact.status === 'unavailable' ? 'Failed' : grouped ? `Iteration ${artifact.lineage?.iterationIndex || index + 1}` : artifact.kind || artifact.mediaType}</span></span>
                      </a>
                      {onAddToChat && artifact.status === 'ready' && artifact.collectionId && (artifact.eventSeq ?? 0) > 0 ? <button type="button" className="absolute right-1 top-1 grid size-7 place-items-center rounded-md bg-[var(--app-primary)] text-white opacity-0 shadow-md transition hover:opacity-90 group-hover:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white" aria-label={`Attach ${artifact.label} for chat changes`} title={artifact.mediaType.startsWith('image/') ? 'Attach to chat for remixing' : 'Attach to chat'} onClick={() => onAddToChat([desktopV3ArtifactMessageSelection(artifact, 'select')])}><MessageSquarePlus size={13} aria-hidden="true" /></button> : null}
                    </div>
                  ))}
                </div>}
              </section>
            )
          })}
        </div>
      ) : null}
    </aside>
  )
}
