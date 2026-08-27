import {
  desktopV3ArtifactCatalogEntryKey,
  type DesktopV3ArtifactCatalogEntry,
  type DesktopV3ArtifactChainReference,
  type DesktopV3ArtifactCompositionPart,
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

export function desktopV3ArtifactStudioSamePartRevision(left: DesktopV3ArtifactCompositionPart, right: DesktopV3ArtifactCompositionPart): boolean {
  return left.partId === right.partId
    && left.definitionOwnerSessionId === right.definitionOwnerSessionId
    && left.revision.partRevisionId === right.revision.partRevisionId
    && left.revision.ownerSessionId === right.revision.ownerSessionId
    && left.revision.digestSha256 === right.revision.digestSha256
    && left.revision.size === right.revision.size
    && left.revision.mediaType === right.revision.mediaType
}

export function desktopV3ArtifactStudioAuthoredEntry(entry: DesktopV3ArtifactCatalogEntry): boolean {
  return entry.role !== 'render_only'
}

function gitProjected(entry: DesktopV3ArtifactCatalogEntry): boolean {
  return desktopV3ArtifactStudioAuthoredEntry(entry)
    && entry.graphState === 'git_projection'
    && Boolean(entry.artifactChainId && entry.artifactStepId && entry.chain && entry.step)
    && entry.chain?.graphState === 'git_projection'
    && entry.chain.id === entry.artifactChainId
    && entry.step?.graphState === 'git_projection'
    && entry.step.artifactChainId === entry.artifactChainId
    && entry.step.id === entry.artifactStepId
    && (entry.step.candidates.some((candidate) => sameReference(entry, candidate))
      || sameReference(entry, entry.chain.head))
}

export function desktopV3ArtifactStudioChainKey(entry: DesktopV3ArtifactCatalogEntry): string {
  return gitProjected(entry) ? entry.artifactChainId! : ''
}

/**
 * Returns the user-visible artifact group for a session catalog. The durable
 * iteration-group identity is the generating AI turn and can span collections;
 * source-free legacy waves fall back to their shared collection. Once an
 * ungrouped chain gains a later revision, that chain becomes the stable artifact
 * whose turns should be browsed chronologically.
 */
export function desktopV3ArtifactStudioPresentationGroupKey(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): string {
  const chainKey = desktopV3ArtifactStudioChainKey(entry)
  if (!desktopV3ArtifactStudioAuthoredEntry(entry)) {
    return `supporting:${entry.sessionId}:${entry.collectionId || entry.artifactId}`
  }
  const owningSessionId = entry.lineage?.parentSessionId || entry.sessionId
  const chainHasMultipleTurns = Boolean(chainKey && entries.some((candidate) =>
    desktopV3ArtifactStudioChainKey(candidate) === chainKey
      && ((candidate.step?.revisionNumber ?? candidate.revisionNumber ?? 0) > 1
        || (candidate.chain?.revisionCount ?? 0) > 1)
  ))
  if (chainHasMultipleTurns) return `chain:${chainKey}`
  const iterationGroupId = entry.lineage?.iterationGroupId.trim() ?? ''
  if (iterationGroupId) return `turn:${owningSessionId}:${iterationGroupId}`
  return entry.collectionId
    ? `collection:${owningSessionId}:${entry.collectionId}`
    : `standalone:${entry.sessionId}:${entry.artifactId}`
}

export function desktopV3ArtifactStudioEntries(entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): DesktopV3ArtifactCatalogEntry[] {
  const chainKey = desktopV3ArtifactStudioChainKey(entry)
  if (!chainKey) return [entry]
  return entries.filter((candidate) => gitProjected(candidate) && candidate.artifactChainId === chainKey)
    .sort((left, right) => left.step!.revisionNumber - right.step!.revisionNumber
      || (left.candidateIndex ?? 0) - (right.candidateIndex ?? 0)
      || left.updatedAt - right.updatedAt)
}

export function desktopV3ArtifactStudioHead(entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): DesktopV3ArtifactCatalogEntry | undefined {
  if (!gitProjected(entry)) return undefined
  return desktopV3ArtifactStudioEntries(entries, entry).find((candidate) => sameReference(candidate, entry.chain?.head))
}

export function desktopV3ArtifactStudioRounds(entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): Array<{ id: string; revisionNumber: number; candidates: DesktopV3ArtifactCatalogEntry[]; accepted?: DesktopV3ArtifactCatalogEntry }> {
  if (!gitProjected(entry)) return []
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

export interface DesktopV3ArtifactStudioTurnCandidate {
  reference: DesktopV3ArtifactChainReference
  entry?: DesktopV3ArtifactCatalogEntry
  part?: DesktopV3ArtifactCompositionPart
}

export interface DesktopV3ArtifactStudioTurnPart {
  partId: string
  candidates: DesktopV3ArtifactStudioTurnCandidate[]
  accepted?: DesktopV3ArtifactStudioTurnCandidate
}

export interface DesktopV3ArtifactStudioTurnTarget {
  partId: string
  label: string
  kind: string
  startMs: number
  endMs: number
}

/** One authoritative artifact step, including roots, no-op steps, and single-candidate edits. */
export interface DesktopV3ArtifactStudioTurn {
  id: string
  groupId: string
  revisionNumber: number
  parent?: DesktopV3ArtifactChainReference
  changedPartIds: string[]
  relatedTargets: DesktopV3ArtifactStudioTurnTarget[]
  candidates: DesktopV3ArtifactStudioTurnCandidate[]
  parts: DesktopV3ArtifactStudioTurnPart[]
  accepted?: DesktopV3ArtifactStudioTurnCandidate
}

export interface DesktopV3ArtifactStudioPartTurn {
  id: string
  groupId: string
  revisionNumber: number
  changedPartIds: string[]
  candidates: DesktopV3ArtifactCatalogEntry[]
  accepted?: DesktopV3ArtifactCatalogEntry
}

export interface DesktopV3ArtifactStudioPartIteration {
  id: string
  label: string
  kind: string
  startMs: number
  endMs: number
  current?: DesktopV3ArtifactCompositionPart
  accepted?: DesktopV3ArtifactCompositionPart
  turns: DesktopV3ArtifactStudioPartTurn[]
}

export function desktopV3ArtifactStudioChangedPartIds(entries: readonly DesktopV3ArtifactCatalogEntry[], candidate: DesktopV3ArtifactCatalogEntry): string[] {
  const composition = candidate.composition
  const parent = desktopV3ArtifactStudioParent(entries, candidate)?.composition
  if (candidate.partGraphState !== 'git_projection' || !composition || !parent || composition.parts.length !== parent.parts.length) return []

  // Composition order is presentation metadata, not part identity. Compare slots by
  // their durable part IDs so a producer that emits the same composition in a
  // different order does not turn a real iteration into a misleading "0 parts" turn.
  const previousByPartId = new Map(parent.parts.map((part) => [part.partId, part]))
  if (previousByPartId.size !== parent.parts.length) return []
  const changed: string[] = []
  const visited = new Set<string>()
  for (const current of composition.parts) {
    if (visited.has(current.partId)) return []
    visited.add(current.partId)
    const previous = previousByPartId.get(current.partId)
    if (!previous || current.definitionOwnerSessionId !== previous.definitionOwnerSessionId) return []
    if (!desktopV3ArtifactStudioSamePartRevision(current, previous)) changed.push(current.partId)
  }
  return changed
}

function turnCandidate(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  reference: DesktopV3ArtifactChainReference,
): DesktopV3ArtifactStudioTurnCandidate {
  const entry = entries.find((candidate) => desktopV3ArtifactStudioAuthoredEntry(candidate) && sameReference(candidate, reference))
    ?? entries.find((candidate) => desktopV3ArtifactStudioAuthoredEntry(candidate) && candidate.sessionId === reference.sessionId
      && candidate.collectionId === reference.collectionId
      && candidate.artifactId === reference.variantId)
  return { reference, ...(entry ? { entry } : {}) }
}

/**
 * Projects the immutable chain into chronological user-visible turns. The step is
 * the authority: unresolved candidate references remain visible as references,
 * while legacy entries never gain inferred turns.
 */
export function desktopV3ArtifactStudioTurns(entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): DesktopV3ArtifactStudioTurn[] {
  if (!gitProjected(entry)) return []
  const chainEntries = desktopV3ArtifactStudioEntries(entries, entry)
  const steps = new Map<string, DesktopV3ArtifactCatalogEntry>()
  for (const candidate of chainEntries) {
    const current = steps.get(candidate.step!.id)
    if (!current || (candidate.candidateIndex ?? Number.MAX_SAFE_INTEGER) < (current.candidateIndex ?? Number.MAX_SAFE_INTEGER)) {
      steps.set(candidate.step!.id, candidate)
    }
  }
  return [...steps.values()].map((representative): DesktopV3ArtifactStudioTurn => {
    const step = representative.step!
    const candidates = step.candidates.map((reference) => turnCandidate(entries, reference))
    const changedByCandidate = candidates.map((candidate) => {
      if (!candidate.entry) return []
      if (!step.parent) return candidate.entry.partGraphState === 'git_projection'
        ? candidate.entry.composition?.parts.map((part) => part.partId) ?? []
        : []
      return desktopV3ArtifactStudioChangedPartIds(chainEntries, candidate.entry)
    })
    const declaredByCandidate = candidates.map((candidate) => {
      const composition = candidate.entry?.composition
      if (!candidate.entry || !composition?.iterationTurnId) return []
      const revisions = candidate.entry.partRevisions ?? []
      return composition.parts.flatMap((part) => revisions.some((revision) => revision.iterationTurnId === composition.iterationTurnId
        && revision.reference.partId === part.partId
        && revision.reference.partRevisionId === part.revision.partRevisionId
        && revision.reference.ownerSessionId === part.revision.ownerSessionId)
        ? [part.partId]
        : [])
    })
    const changedPartIds = [...new Set([...changedByCandidate.flat(), ...declaredByCandidate.flat()])]
    const relatedTargets = [...new Map(candidates.flatMap((candidate): Array<[string, DesktopV3ArtifactStudioTurnTarget]> => {
      const lineage = candidate.entry?.lineage
      const partId = lineage?.partId || lineage?.iterationSectionId || ''
      if (!partId) return []
      const definition = candidate.entry?.partDefinitions?.find((part) => part.id === partId)
      const reviewPart = candidate.entry?.parts?.find((part) => part.id === partId)
      const label = lineage?.partLabel || lineage?.iterationSectionLabel || definition?.label || reviewPart?.label || partId
      const kind = lineage?.partKind || definition?.locator?.kind || reviewPart?.kind || 'semantic'
      const startMs = lineage?.iterationSectionStartMs || (definition?.locator?.kind === 'temporal' ? definition.locator.startMs : reviewPart?.startMs) || 0
      const endMs = lineage?.iterationSectionEndMs || (definition?.locator?.kind === 'temporal' ? definition.locator.endMs : reviewPart?.endMs) || 0
      return [[partId, { partId, label, kind, startMs, endMs }]]
    })).values()]
    const accepted = step.accepted ? turnCandidate(entries, step.accepted) : undefined
    const parts = changedPartIds.map((partId): DesktopV3ArtifactStudioTurnPart => {
      const partCandidates = candidates.flatMap((candidate, index) => {
        if (!changedByCandidate[index]?.includes(partId) && !declaredByCandidate[index]?.includes(partId)) return []
        const part = candidate.entry?.composition?.parts.find((candidatePart) => candidatePart.partId === partId)
        return [{ ...candidate, ...(part ? { part } : {}) }]
      })
      const acceptedCandidate = accepted
        ? partCandidates.find((candidate) => candidate.reference.sessionId === accepted.reference.sessionId
          && candidate.reference.collectionId === accepted.reference.collectionId
          && candidate.reference.variantId === accepted.reference.variantId
          && candidate.reference.eventSeq === accepted.reference.eventSeq)
        : undefined
      return { partId, candidates: partCandidates, ...(acceptedCandidate ? { accepted: acceptedCandidate } : {}) }
    })
    return {
      id: step.id,
      groupId: representative.composition?.iterationGroupId || representative.revisionRoundId || step.id,
      revisionNumber: step.revisionNumber,
      ...(step.parent ? { parent: step.parent } : {}),
      changedPartIds,
      relatedTargets,
      candidates,
      parts,
      ...(accepted ? { accepted } : {}),
    }
  }).sort((left, right) => left.revisionNumber - right.revisionNumber || left.id.localeCompare(right.id))
}

export function desktopV3ArtifactStudioPartIterations(entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): DesktopV3ArtifactStudioPartIteration[] {
  if (entry.partGraphState !== 'git_projection' || !entry.composition) return []
  const head = desktopV3ArtifactStudioHead(entries, entry) ?? entry
  const definitions = new Map((head.partDefinitions ?? entry.partDefinitions ?? []).map((definition) => [definition.id, definition]))
  const currentComposition = head.composition ?? entry.composition
  const currentByPart = new Map(currentComposition.parts.map((part) => [part.partId, part]))
  const acceptedByPart = new Map((head.acceptedPartHeads ?? entry.acceptedPartHeads ?? []).map((part) => [part.partId, part]))
  const groups = new Map<string, DesktopV3ArtifactStudioPartIteration>()
  const definitionOrder = new Map((head.partDefinitions ?? entry.partDefinitions ?? []).map((definition, index) => [definition.id, index]))
  for (const part of currentComposition.parts) {
    const definition = definitions.get(part.partId)
    if (!definition) continue
    const locator = definition.locator
    groups.set(part.partId, { id: part.partId, label: definition.label, kind: locator?.kind ?? 'semantic', startMs: locator?.startMs ?? 0, endMs: locator?.endMs ?? 0, current: currentByPart.get(part.partId), accepted: acceptedByPart.get(part.partId), turns: [] })
  }
  for (const round of desktopV3ArtifactStudioRounds(entries, entry)) {
    const changedSets = round.candidates.map((candidate) => desktopV3ArtifactStudioChangedPartIds(entries, candidate))
    const first = changedSets[0] ?? []
    if (first.length === 0 || changedSets.some((ids) => ids.length !== first.length || ids.some((id, index) => id !== first[index]))) continue
    const turn: DesktopV3ArtifactStudioPartTurn = {
      ...round,
      groupId: round.candidates[0]?.composition?.iterationGroupId || round.candidates[0]?.revisionRoundId || round.id,
      changedPartIds: first,
    }
    for (const partId of first) groups.get(partId)?.turns.push(turn)
  }
  return [...groups.values()].filter((part) => part.turns.length > 0).sort((left, right) => {
    const leftRevision = left.turns[0]?.revisionNumber ?? 0
    const rightRevision = right.turns[0]?.revisionNumber ?? 0
    return leftRevision - rightRevision || (definitionOrder.get(left.id) ?? Number.MAX_SAFE_INTEGER) - (definitionOrder.get(right.id) ?? Number.MAX_SAFE_INTEGER) || left.label.localeCompare(right.label)
  })
}

export function desktopV3ArtifactStudioParent(entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): DesktopV3ArtifactCatalogEntry | undefined {
  if (!gitProjected(entry) || !entry.step?.parent) return undefined
  return entries.find((candidate) => gitProjected(candidate)
    && candidate.artifactChainId === entry.artifactChainId
    && sameReference(candidate, entry.step?.parent))
}

export function desktopV3ArtifactStudioRoot(entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): DesktopV3ArtifactCatalogEntry {
  if (!gitProjected(entry)) return entry
  return desktopV3ArtifactStudioEntries(entries, entry).find((candidate) => sameReference(candidate, entry.chain?.root)) ?? entry
}

export function desktopV3ArtifactStudioRootKey(_entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): string {
  const chainKey = desktopV3ArtifactStudioChainKey(entry)
  return chainKey ? `chain:${chainKey}` : `unstructured:${desktopV3ArtifactCatalogEntryKey(entry)}:${entry.eventSeq ?? 0}`
}

export function desktopV3ArtifactStudioSectionLineage(entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): DesktopV3ArtifactCatalogEntry['lineage'] | undefined {
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

export function desktopV3ArtifactStudioBranchDepth(_entries: readonly DesktopV3ArtifactCatalogEntry[], entry: DesktopV3ArtifactCatalogEntry): number {
  return gitProjected(entry) ? Math.max(0, entry.step!.revisionNumber - 1) : 0
}

export function desktopV3ArtifactStudioSectionAlternatives(entries: readonly DesktopV3ArtifactCatalogEntry[], source: DesktopV3ArtifactCatalogEntry, sectionId: string): DesktopV3ArtifactCatalogEntry[] {
  if (!gitProjected(source)) return []
  const chainKey = source.artifactChainId
  return entries.filter((entry) => gitProjected(entry)
      && entry.artifactChainId === chainKey
      && desktopV3ArtifactStudioSectionLineage(entries, entry)?.iterationSectionId === sectionId)
    .sort((left, right) => left.step!.revisionNumber - right.step!.revisionNumber
      || (left.candidateIndex ?? 0) - (right.candidateIndex ?? 0))
}

export function desktopV3ArtifactStudioLockedAlternative(entries: readonly DesktopV3ArtifactCatalogEntry[]): DesktopV3ArtifactCatalogEntry | undefined {
  return entries.find((entry) => gitProjected(entry) && sameReference(entry, entry.step?.accepted))
}
