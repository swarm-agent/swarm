import {
  desktopV3ArtifactCatalogEntryKey,
  type DesktopV3ArtifactCatalogEntry,
  type DesktopV3ArtifactChainReference,
} from './artifact-api'

function entryReference(entry: DesktopV3ArtifactCatalogEntry): DesktopV3ArtifactChainReference | undefined {
  if (!entry.collectionId || !entry.eventSeq) return undefined
  return { sessionId: entry.sessionId, collectionId: entry.collectionId, variantId: entry.artifactId, eventSeq: entry.eventSeq }
}

function sameReference(entry: DesktopV3ArtifactCatalogEntry, reference: DesktopV3ArtifactChainReference | null | undefined): boolean {
  return Boolean(reference
    && entry.sessionId === reference.sessionId
    && entry.collectionId === reference.collectionId
    && entry.artifactId === reference.variantId
    && entry.eventSeq === reference.eventSeq)
}

function authoritative(entry: DesktopV3ArtifactCatalogEntry): boolean {
  return entry.graphState === 'authoritative'
    && Boolean(entry.artifactChainId && entry.artifactStepId && entry.chain && entry.step)
    && entry.chain?.graphState === 'authoritative'
    && entry.chain.id === entry.artifactChainId
    && entry.step?.graphState === 'authoritative'
    && entry.step.artifactChainId === entry.artifactChainId
    && entry.step.id === entry.artifactStepId
    && entry.step.candidates.some((candidate) => sameReference(entry, candidate))
}

export function desktopV3ArtifactStudioChainKey(entry: DesktopV3ArtifactCatalogEntry): string {
  return authoritative(entry) ? entry.artifactChainId! : ''
}

export function desktopV3ArtifactStudioEntries(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry[] {
  const chainKey = desktopV3ArtifactStudioChainKey(entry)
  if (!chainKey) return [entry]
  return entries.filter((candidate) => authoritative(candidate) && candidate.artifactChainId === chainKey)
    .sort((left, right) => left.step!.revisionNumber - right.step!.revisionNumber
      || (left.candidateIndex ?? 0) - (right.candidateIndex ?? 0)
      || left.updatedAt - right.updatedAt)
}

export function desktopV3ArtifactStudioHead(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry | undefined {
  if (!authoritative(entry)) return undefined
  return desktopV3ArtifactStudioEntries(entries, entry).find((candidate) => sameReference(candidate, entry.chain?.head))
}

export function desktopV3ArtifactStudioRounds(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): Array<{ id: string; revisionNumber: number; candidates: DesktopV3ArtifactCatalogEntry[]; accepted?: DesktopV3ArtifactCatalogEntry }> {
  if (!authoritative(entry)) return []
  const rounds = new Map<string, DesktopV3ArtifactCatalogEntry[]>()
  for (const candidate of desktopV3ArtifactStudioEntries(entries, entry)) {
    const id = candidate.step!.id
    rounds.set(id, [...(rounds.get(id) ?? []), candidate])
  }
  return [...rounds.entries()].map(([id, candidates]) => ({
    id,
    revisionNumber: candidates[0]!.step!.revisionNumber,
    candidates,
    accepted: candidates.find((candidate) => sameReference(candidate, candidates[0]!.step!.accepted)),
  })).sort((left, right) => left.revisionNumber - right.revisionNumber || left.id.localeCompare(right.id))
}

export interface DesktopV3ArtifactStudioPartTurn {
  id: string
  revisionNumber: number
  candidates: DesktopV3ArtifactCatalogEntry[]
  accepted?: DesktopV3ArtifactCatalogEntry
}

export interface DesktopV3ArtifactStudioPartIteration {
  id: string
  label: string
  kind: string
  startMs: number
  endMs: number
  turns: DesktopV3ArtifactStudioPartTurn[]
}

function exactPartCandidate(entries: readonly DesktopV3ArtifactCatalogEntry[], candidate: DesktopV3ArtifactCatalogEntry): boolean {
  const partId = candidate.targetedPartId?.trim() ?? ''
  const composition = candidate.composition
  const parent = desktopV3ArtifactStudioParent(entries, candidate)?.composition
  if (candidate.partGraphState !== 'authoritative' || !partId || !composition || !parent || composition.parts.length !== parent.parts.length) return false
  let changes = 0
  for (let index = 0; index < composition.parts.length; index += 1) {
    const current = composition.parts[index]!
    const previous = parent.parts[index]!
    if (current.partId !== previous.partId || current.definitionOwnerSessionId !== previous.definitionOwnerSessionId) return false
    const changed = current.revision.partRevisionId !== previous.revision.partRevisionId
      || current.revision.ownerSessionId !== previous.revision.ownerSessionId
      || current.revision.digestSha256 !== previous.revision.digestSha256
      || current.revision.size !== previous.revision.size
      || current.revision.mediaType !== previous.revision.mediaType
    if (changed) {
      if (current.partId !== partId) return false
      changes += 1
    }
  }
  return changes === 1
}

export function desktopV3ArtifactStudioPartIterations(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactStudioPartIteration[] {
  const groups = new Map<string, DesktopV3ArtifactStudioPartIteration>()
  for (const round of desktopV3ArtifactStudioRounds(entries, entry)) {
    const candidates = round.candidates.filter((candidate) => exactPartCandidate(entries, candidate))
    const id = candidates[0]?.targetedPartId?.trim() ?? ''
    if (!id || candidates.length !== round.candidates.length || candidates.some((candidate) => candidate.targetedPartId !== id)) continue
    const definition = candidates[0]?.partDefinitions?.find((part) => part.id === id)
    if (!definition || candidates.some((candidate) => !candidate.composition?.parts.some((part) => part.partId === id))) continue
    const locator = definition.locator
    const accepted = round.accepted && candidates.includes(round.accepted) ? round.accepted : undefined
    const turn = { ...round, candidates, accepted }
    const group = groups.get(id) ?? { id, label: definition.label, kind: locator?.kind ?? 'semantic', startMs: locator?.startMs ?? 0, endMs: locator?.endMs ?? 0, turns: [] }
    group.turns.push(turn)
    groups.set(id, group)
  }
  return [...groups.values()].sort((left, right) => {
    const leftRevision = left.turns[0]?.revisionNumber ?? 0
    const rightRevision = right.turns[0]?.revisionNumber ?? 0
    return leftRevision - rightRevision || left.label.localeCompare(right.label)
  })
}

export function desktopV3ArtifactStudioParent(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry | undefined {
  if (!authoritative(entry) || !entry.step?.parent) return undefined
  return entries.find((candidate) => authoritative(candidate)
    && candidate.artifactChainId === entry.artifactChainId
    && sameReference(candidate, entry.step?.parent))
}

export function desktopV3ArtifactStudioRoot(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry {
  if (!authoritative(entry)) return entry
  return desktopV3ArtifactStudioEntries(entries, entry).find((candidate) => sameReference(candidate, entry.chain?.root)) ?? entry
}

export function desktopV3ArtifactStudioRootKey(
  _entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): string {
  const chainKey = desktopV3ArtifactStudioChainKey(entry)
  return chainKey ? `chain:${chainKey}` : `unstructured:${desktopV3ArtifactCatalogEntryKey(entry)}:${entry.eventSeq ?? 0}`
}

export function desktopV3ArtifactStudioSectionLineage(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry['lineage'] | undefined {
  const visited = new Set<string>()
  let current: DesktopV3ArtifactCatalogEntry | undefined = entry
  while (current) {
    const reference = entryReference(current)
    const key = reference ? `${reference.sessionId}:${reference.collectionId}:${reference.variantId}:${reference.eventSeq}` : desktopV3ArtifactCatalogEntryKey(current)
    if (visited.has(key)) break
    visited.add(key)
    const lineage = current.lineage
    if (lineage?.iterationSectionId && lineage.iterationSectionLabel
      && lineage.iterationSectionStartMs >= 0 && lineage.iterationSectionEndMs > lineage.iterationSectionStartMs) return lineage
    current = desktopV3ArtifactStudioParent(entries, current)
  }
  return undefined
}

export function desktopV3ArtifactStudioBranchDepth(
  _entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): number {
  return authoritative(entry) ? Math.max(0, entry.step!.revisionNumber - 1) : 0
}

export function desktopV3ArtifactStudioSectionAlternatives(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  source: DesktopV3ArtifactCatalogEntry,
  sectionId: string,
): DesktopV3ArtifactCatalogEntry[] {
  if (!authoritative(source)) return []
  const chainKey = source.artifactChainId
  return entries.filter((entry) => authoritative(entry)
      && entry.artifactChainId === chainKey
      && desktopV3ArtifactStudioSectionLineage(entries, entry)?.iterationSectionId === sectionId)
    .sort((left, right) => left.step!.revisionNumber - right.step!.revisionNumber
      || (left.candidateIndex ?? 0) - (right.candidateIndex ?? 0))
}

export function desktopV3ArtifactStudioLockedAlternative(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
): DesktopV3ArtifactCatalogEntry | undefined {
  return entries.find((entry) => authoritative(entry) && sameReference(entry, entry.step?.accepted))
}
