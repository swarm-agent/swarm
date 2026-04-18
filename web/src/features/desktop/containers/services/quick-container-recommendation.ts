import type { DesktopOnboardingStatus } from '../../onboarding/types'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import type { ContainerProfile, ContainerProfileDraft, ContainerProfileMount } from '../types/container-profiles'
import { containerMountTargetForPath, createEmptyContainerProfileDraft } from '../types/container-profiles'

interface BuildQuickContainerDraftOptions {
  name: string
  onboardingStatus: DesktopOnboardingStatus | null
  workspaces: WorkspaceEntry[]
}

export function suggestQuickContainerProfileName(
  profiles: ContainerProfile[],
  onboardingStatus: DesktopOnboardingStatus | null,
): string {
  const baseName = preferredQuickContainerBaseName(onboardingStatus)
  const usedNames = new Set(profiles.map((profile) => profile.name.trim().toLowerCase()).filter(Boolean))
  if (!usedNames.has(baseName.toLowerCase())) {
    return baseName
  }
  let suffix = 2
  while (usedNames.has(`${baseName} ${suffix}`.toLowerCase())) {
    suffix += 1
  }
  return `${baseName} ${suffix}`
}

export function buildQuickContainerProfileDraft({
  name,
  onboardingStatus,
  workspaces,
}: BuildQuickContainerDraftOptions): ContainerProfileDraft {
  const fallbackName = preferredQuickContainerBaseName(onboardingStatus)
  const trimmedName = name.trim() || fallbackName
  const roleHint = onboardingStatus?.config.child ? 'child' : 'workspace'

  return {
    ...createEmptyContainerProfileDraft(),
    name: trimmedName,
    description: buildQuickDescription(workspaces, onboardingStatus),
    roleHint,
    mounts: buildRecommendedMounts(workspaces),
  }
}

function preferredQuickContainerBaseName(onboardingStatus: DesktopOnboardingStatus | null): string {
  const swarmName = onboardingStatus?.config.swarmName.trim()
  if (swarmName) {
    return `${swarmName} swarm`
  }
  const dnsLabel = firstHostnameLabel(onboardingStatus?.network.tailscale.dnsName)
  if (dnsLabel) {
    return `${dnsLabel} swarm`
  }
  return 'Local swarm'
}

function buildRecommendedMounts(workspaces: WorkspaceEntry[]): ContainerProfileMount[] {
  const seenSourcePaths = new Set<string>()
  const seenTargets = new Set<string>()
  const mounts: ContainerProfileMount[] = []

  for (const workspace of workspaces) {
    const paths = Array.from(new Set([workspace.path, ...workspace.directories].map((value) => value.trim()).filter(Boolean)))
    for (const path of paths) {
      const normalizedPath = path.trim()
      const pathKey = normalizedPath.toLowerCase()
      if (!normalizedPath || seenSourcePaths.has(pathKey)) {
        continue
      }
      seenSourcePaths.add(pathKey)
      const targetPath = uniqueMountTarget(normalizedPath, seenTargets)
      mounts.push({
        sourcePath: normalizedPath,
        targetPath,
        mode: 'rw',
        workspacePath: workspace.path,
        workspaceName: workspace.workspaceName,
      })
    }
  }

  return mounts
}

function uniqueMountTarget(path: string, usedTargets: Set<string>): string {
  const baseTarget = containerMountTargetForPath(path)
  if (!usedTargets.has(baseTarget.toLowerCase())) {
    usedTargets.add(baseTarget.toLowerCase())
    return baseTarget
  }
  let suffix = 2
  while (usedTargets.has(`${baseTarget}-${suffix}`.toLowerCase())) {
    suffix += 1
  }
  const target = `${baseTarget}-${suffix}`
  usedTargets.add(target.toLowerCase())
  return target
}

function buildQuickDescription(
  workspaces: WorkspaceEntry[],
  onboardingStatus: DesktopOnboardingStatus | null,
): string {
  const workspaceCount = workspaces.length
  const workspaceLabel = workspaceCount === 1 ? 'workspace' : 'workspaces'
  const swarmName = onboardingStatus?.config.swarmName.trim()
  if (swarmName) {
    return `Recommended local swarm launcher for ${swarmName}, preloaded with ${workspaceCount} saved ${workspaceLabel}.`
  }
  return `Recommended local swarm launcher preloaded with ${workspaceCount} saved ${workspaceLabel}.`
}

function firstHostnameLabel(value: string | null | undefined): string {
  const trimmed = String(value ?? '').trim()
  if (!trimmed) {
    return ''
  }
  const withoutProtocol = trimmed.replace(/^[a-z]+:\/\//i, '')
  const hostname = withoutProtocol.split('/')[0].trim().replace(/\.+$/, '')
  if (!hostname) {
    return ''
  }
  return hostname.split('.')[0]?.trim() ?? ''
}
