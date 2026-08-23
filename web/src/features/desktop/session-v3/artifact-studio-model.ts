import {
  desktopV3ArtifactCatalogEntryKey,
  type DesktopV3ArtifactCatalogEntry,
} from './artifact-api'

export function desktopV3ArtifactStudioChainKey(entry: DesktopV3ArtifactCatalogEntry): string {
  return entry.artifactChainId?.trim() || entry.chain?.id.trim() || ''
}

function sourceKey(entry: DesktopV3ArtifactCatalogEntry): string {
  const lineage = entry.lineage
  if (!lineage?.sourceSessionId || !lineage.sourceCollectionId || !lineage.sourceVariantId || !lineage.sourceEventSeq) return ''
  return `${desktopV3ArtifactCatalogEntryKey({
    sessionId: lineage.sourceSessionId,
    collectionId: lineage.sourceCollectionId,
    artifactId: lineage.sourceVariantId,
  })}:${lineage.sourceEventSeq}`
}

function entryKey(entry: DesktopV3ArtifactCatalogEntry): string {
  return `${desktopV3ArtifactCatalogEntryKey(entry)}:${entry.eventSeq ?? 0}`
}

export function desktopV3ArtifactStudioEntries(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry[] {
  const chainKey = desktopV3ArtifactStudioChainKey(entry)
  if (chainKey) {
    return entries.filter((candidate) => desktopV3ArtifactStudioChainKey(candidate) === chainKey)
      .sort((left, right) => (left.revisionNumber ?? 0) - (right.revisionNumber ?? 0)
        || (left.candidateIndex ?? 0) - (right.candidateIndex ?? 0)
        || left.updatedAt - right.updatedAt)
  }
  const rootKey = desktopV3ArtifactStudioRootKey(entries, entry)
  return entries.filter((candidate) => desktopV3ArtifactStudioRootKey(entries, candidate) === rootKey)
}

export function desktopV3ArtifactStudioHead(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry {
  const chainEntries = desktopV3ArtifactStudioEntries(entries, entry)
  return chainEntries.find((candidate) => candidate.selected)
    ?? chainEntries.find((candidate) => candidate.chain?.head.sessionId === candidate.sessionId
      && candidate.chain.head.collectionId === candidate.collectionId
      && candidate.chain.head.variantId === candidate.artifactId)
    ?? chainEntries.filter((candidate) => candidate.status === 'ready').reduce<DesktopV3ArtifactCatalogEntry | undefined>((latest, candidate) => !latest || (candidate.revisionNumber ?? 0) > (latest.revisionNumber ?? 0) ? candidate : latest, undefined)
    ?? entry
}

export function desktopV3ArtifactStudioRounds(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): Array<{ id: string; revisionNumber: number; candidates: DesktopV3ArtifactCatalogEntry[] }> {
  const rounds = new Map<string, DesktopV3ArtifactCatalogEntry[]>()
  for (const candidate of desktopV3ArtifactStudioEntries(entries, entry)) {
    const id = candidate.revisionRoundId?.trim() || `revision-${candidate.revisionNumber ?? 0}`
    rounds.set(id, [...(rounds.get(id) ?? []), candidate])
  }
  return [...rounds.entries()].map(([id, candidates]) => ({ id, revisionNumber: candidates[0]?.revisionNumber ?? 0, candidates }))
    .sort((left, right) => left.revisionNumber - right.revisionNumber || left.id.localeCompare(right.id))
}

export function desktopV3ArtifactStudioParent(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry | undefined {
  const chainKey = desktopV3ArtifactStudioChainKey(entry)
  if (chainKey && (entry.revisionNumber ?? 0) > 1) {
    const previousRevision = (entry.revisionNumber ?? 0) - 1
    const candidates = entries.filter((candidate) => desktopV3ArtifactStudioChainKey(candidate) === chainKey && candidate.revisionNumber === previousRevision)
    return candidates.find((candidate) => candidate.selected) ?? candidates[candidates.length - 1]
  }
  const key = sourceKey(entry)
  if (!key) return undefined
  return entries.find((candidate) => entryKey(candidate) === key)
}

export function desktopV3ArtifactStudioRoot(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry {
  const visited = new Set<string>()
  let current = entry
  for (let index = 0; index < entries.length; index += 1) {
    const key = desktopV3ArtifactCatalogEntryKey(current)
    if (visited.has(key)) break
    visited.add(key)
    const parent = desktopV3ArtifactStudioParent(entries, current)
    if (!parent) break
    current = parent
  }
  return current
}

export function desktopV3ArtifactStudioRootKey(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): string {
  const visited = new Set<string>()
  let current = entry
  for (let index = 0; index < entries.length; index += 1) {
    const key = entryKey(current)
    if (visited.has(key)) return `artifact:${key}`
    visited.add(key)
    const parent = desktopV3ArtifactStudioParent(entries, current)
    if (parent) {
      current = parent
      continue
    }
    const unresolvedSource = sourceKey(current)
    return unresolvedSource ? `source:${unresolvedSource}` : `artifact:${key}`
  }
  return `artifact:${entryKey(current)}`
}

export function desktopV3ArtifactStudioSectionLineage(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry['lineage'] | undefined {
  const visited = new Set<string>()
  let current = entry
  for (let index = 0; index <= entries.length; index += 1) {
    const key = entryKey(current)
    if (visited.has(key)) break
    visited.add(key)
    const lineage = current.lineage
    if (lineage?.iterationSectionId && lineage.iterationSectionLabel
      && lineage.iterationSectionStartMs >= 0 && lineage.iterationSectionEndMs > lineage.iterationSectionStartMs) {
      return lineage
    }
    const parent = desktopV3ArtifactStudioParent(entries, current)
    if (!parent) break
    current = parent
  }
  return undefined
}

export function desktopV3ArtifactStudioBranchDepth(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): number {
  const visited = new Set<string>()
  let current = entry
  let depth = 0
  for (let index = 0; index < entries.length; index += 1) {
    const key = desktopV3ArtifactCatalogEntryKey(current)
    if (visited.has(key)) break
    visited.add(key)
    const parent = desktopV3ArtifactStudioParent(entries, current)
    if (!parent) break
    current = parent
    depth += 1
  }
  return depth
}

export function desktopV3ArtifactStudioSectionAlternatives(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  source: DesktopV3ArtifactCatalogEntry,
  sectionId: string,
): DesktopV3ArtifactCatalogEntry[] {
  const rootKey = desktopV3ArtifactStudioRootKey(entries, source)
  return entries
    .filter((entry) => desktopV3ArtifactStudioSectionLineage(entries, entry)?.iterationSectionId === sectionId
      && desktopV3ArtifactStudioRootKey(entries, entry) === rootKey)
    .sort((left, right) => desktopV3ArtifactStudioBranchDepth(entries, left) - desktopV3ArtifactStudioBranchDepth(entries, right)
      || left.updatedAt - right.updatedAt
      || desktopV3ArtifactCatalogEntryKey(left).localeCompare(desktopV3ArtifactCatalogEntryKey(right)))
}

export function desktopV3ArtifactStudioLockedAlternative(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
): DesktopV3ArtifactCatalogEntry | undefined {
  return entries.reduce<DesktopV3ArtifactCatalogEntry | undefined>((locked, entry) => {
    if (!entry.selected) return locked
    if (!locked || entry.updatedAt >= locked.updatedAt) return entry
    return locked
  }, undefined)
}
