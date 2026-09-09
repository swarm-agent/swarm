import type { DesktopV3NativeArtifactSummary, DesktopV3NativeGenerationGroup } from './artifact-v3-api'

export interface NativeArtifactSidebarGroup {
  key: string
  waveId: string
  count: number
  artifacts: DesktopV3NativeArtifactSummary[]
}

// The first persisted generation is the family identity. A later one-option
// correction must not pull an artifact out of its original sibling family.
// Never infer relationships from labels, timestamps, or prompt text.
export function groupNativeArtifactSidebar(artifacts: readonly DesktopV3NativeArtifactSummary[]): NativeArtifactSidebarGroup[] {
  const groups = new Map<string, NativeArtifactSidebarGroup>()
  for (const artifact of artifacts) {
    const generation = artifact.generations?.find((member) => member.waveId && member.count > 1)
    const key = JSON.stringify([artifact.ownerSessionId, generation ? 'wave' : 'artifact', generation?.waveId ?? artifact.artifactId])
    let group = groups.get(key)
    if (!group) {
      group = { key, waveId: generation?.waveId ?? '', count: generation?.count ?? 1, artifacts: [] }
      groups.set(key, group)
    }
    group.count = Math.max(group.count, generation?.count ?? 1)
    group.artifacts.push(artifact)
  }
  for (const group of groups.values()) {
    if (!group.waveId) continue
    group.artifacts.sort((a, b) => {
      const index = (artifact: DesktopV3NativeArtifactSummary) => artifact.generations?.find((member) => member.waveId === group.waveId)?.index ?? 0
      return index(a) - index(b) || a.artifactId.localeCompare(b.artifactId)
    })
  }
  return [...groups.values()]
}

export function defaultNativeGenerationGroup(groups: readonly DesktopV3NativeGenerationGroup[], waveId: string): DesktopV3NativeGenerationGroup | undefined {
  return groups.find((group) => group.waveId === waveId) ?? groups.find((group) => group.count > 1) ?? groups[0]
}
