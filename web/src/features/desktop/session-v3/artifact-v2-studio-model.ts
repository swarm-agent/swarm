import type {
  DesktopV3ArtifactV2CatalogItem,
  DesktopV3ArtifactV2Composition,
  DesktopV3ArtifactV2CompositionPart,
  DesktopV3ArtifactV2Iteration,
  DesktopV3ArtifactV2IterationCandidate,
  DesktopV3ArtifactV2Part,
  DesktopV3ArtifactV2PartRevision,
  DesktopV3ArtifactV2Studio,
} from './artifact-v2-api'

export interface DesktopV3ArtifactV2SidebarGroup {
  key: string
  item: DesktopV3ArtifactV2CatalogItem
  section: 'working' | 'invalid' | 'iterations' | 'published' | 'storyboards' | 'ready'
  label: string
  detail: string
}

export function desktopV3ArtifactV2SidebarGroups(items: readonly DesktopV3ArtifactV2CatalogItem[]): DesktopV3ArtifactV2SidebarGroup[] {
  return [...items].map((item): DesktopV3ArtifactV2SidebarGroup => {
    const state = item.projection.state
    const section = item.working.kind === 'storyboard'
      ? 'storyboards'
      : state === 'invalid' ? 'invalid'
        : state === 'iterating' ? 'iterations'
          : state === 'published_view' ? 'published'
            : state === 'ready' ? 'ready' : 'working'
    const diagnostic = item.projection.latestDiagnostic?.safeMessage ?? ''
    const detail = diagnostic || (state === 'iterating'
      ? `${item.projection.partCount} parts · iteration ${item.projection.activeIterationId}`
      : `${item.projection.partCount} real part${item.projection.partCount === 1 ? '' : 's'} · revision ${item.projection.revision}`)
    return { key: item.working.id, item, section, label: item.working.intentReference || item.working.kind || 'Artifact', detail }
  }).sort((left, right) => right.item.projection.updatedAt - left.item.projection.updatedAt || left.key.localeCompare(right.key))
}

export interface DesktopV3ArtifactV2PartHistory {
  part: DesktopV3ArtifactV2Part
  current?: DesktopV3ArtifactV2CompositionPart
  revisions: DesktopV3ArtifactV2PartRevision[]
}

export interface DesktopV3ArtifactV2CandidateComparison {
  round: DesktopV3ArtifactV2Iteration
  candidate: DesktopV3ArtifactV2IterationCandidate
  composition?: DesktopV3ArtifactV2Composition
  changedPartIds: string[]
  preservesUnrelatedParts: boolean
  selected: boolean
}

export interface DesktopV3ArtifactV2StudioModel {
  head?: DesktopV3ArtifactV2Composition
  parts: DesktopV3ArtifactV2PartHistory[]
  candidates: DesktopV3ArtifactV2CandidateComparison[]
}

function sameRevision(left: DesktopV3ArtifactV2CompositionPart, right: DesktopV3ArtifactV2CompositionPart): boolean {
  return left.partId === right.partId && left.partRevisionId === right.partRevisionId && left.digestSha256 === right.digestSha256 && left.locked === right.locked
}

export function desktopV3ArtifactV2ChangedParts(base: DesktopV3ArtifactV2Composition, candidate: DesktopV3ArtifactV2Composition): string[] {
  const baseByPart = new Map(base.parts.map((part) => [part.partId, part]))
  if (baseByPart.size !== base.parts.length || candidate.parts.length !== base.parts.length) return []
  const changed: string[] = []
  for (const part of candidate.parts) {
    const previous = baseByPart.get(part.partId)
    if (!previous) return []
    if (!sameRevision(previous, part)) changed.push(part.partId)
  }
  return changed
}

export function desktopV3ArtifactV2StudioModel(studio: DesktopV3ArtifactV2Studio): DesktopV3ArtifactV2StudioModel {
  const compositions = new Map(studio.compositions.map((composition) => [composition.id, composition]))
  const head = studio.working.compositionHead ? compositions.get(studio.working.compositionHead.compositionId) : undefined
  const currentByPart = new Map((head?.parts ?? []).map((part) => [part.partId, part]))
  const parts = [...studio.parts].sort((left, right) => left.order - right.order || left.id.localeCompare(right.id)).map((part): DesktopV3ArtifactV2PartHistory => ({
    part,
    current: currentByPart.get(part.id),
    revisions: studio.partRevisions.filter((revision) => revision.partId === part.id).sort((left, right) => right.eventSeq - left.eventSeq || right.id.localeCompare(left.id)),
  }))
  const candidates = studio.iterations.flatMap((round): DesktopV3ArtifactV2CandidateComparison[] => {
    const base = compositions.get(round.baseCompositionId)
    return round.candidates.map((candidate) => {
      const composition = compositions.get(candidate.compositionId)
      const changedPartIds = base && composition ? desktopV3ArtifactV2ChangedParts(base, composition) : []
      const targetSet = new Set(round.targetPartIds)
      const preservesUnrelatedParts = Boolean(base && composition && changedPartIds.every((partId) => targetSet.has(partId)))
      return { round, candidate, composition, changedPartIds, preservesUnrelatedParts, selected: round.selectedSlotId === candidate.slotId }
    })
  })
  return { head, parts, candidates }
}
