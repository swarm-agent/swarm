import {
  desktopV3ArtifactCatalogEntryKey,
  type DesktopV3ArtifactCatalogEntry,
} from './artifact-api'

function sourceKey(entry: DesktopV3ArtifactCatalogEntry): string {
  const lineage = entry.lineage
  if (!lineage?.sourceSessionId || !lineage.sourceCollectionId || !lineage.sourceVariantId || !lineage.sourceEventSeq) return ''
  return desktopV3ArtifactCatalogEntryKey({
    sessionId: lineage.sourceSessionId,
    collectionId: lineage.sourceCollectionId,
    artifactId: lineage.sourceVariantId,
  })
}

export function desktopV3ArtifactStudioParent(
  entries: readonly DesktopV3ArtifactCatalogEntry[],
  entry: DesktopV3ArtifactCatalogEntry,
): DesktopV3ArtifactCatalogEntry | undefined {
  const key = sourceKey(entry)
  if (!key) return undefined
  const eventSeq = entry.lineage?.sourceEventSeq ?? 0
  return entries.find((candidate) => desktopV3ArtifactCatalogEntryKey(candidate) === key && (candidate.eventSeq ?? 0) === eventSeq)
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
  const rootKey = desktopV3ArtifactCatalogEntryKey(desktopV3ArtifactStudioRoot(entries, source))
  return entries
    .filter((entry) => entry.lineage?.iterationSectionId === sectionId
      && desktopV3ArtifactCatalogEntryKey(desktopV3ArtifactStudioRoot(entries, entry)) === rootKey)
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
