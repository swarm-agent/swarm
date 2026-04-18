export type ContainerRoleHint = 'workspace' | 'child'
export type ContainerMountMode = 'rw' | 'ro'

export interface ContainerProfileMount {
  sourcePath: string
  targetPath: string
  mode: ContainerMountMode
  workspacePath: string
  workspaceName: string
}

export interface ContainerProfile {
  id: string
  name: string
  description: string
  roleHint: ContainerRoleHint
  mounts: ContainerProfileMount[]
  createdAt: number
  updatedAt: number
}

export interface ContainerProfileDraft {
  id: string
  name: string
  description: string
  roleHint: ContainerRoleHint
  mounts: ContainerProfileMount[]
}

export interface ContainerProfileMountWire {
  source_path: string
  target_path?: string
  mode?: string
  workspace_path?: string
  workspace_name?: string
}

export interface ContainerProfileWire {
  id: string
  name: string
  description?: string
  role_hint?: string
  mounts?: ContainerProfileMountWire[]
  created_at?: number
  updated_at?: number
}

export interface ListContainerProfilesResponse {
  ok: boolean
  profiles: ContainerProfileWire[]
}

export function normalizeContainerRoleHint(value: string | null | undefined): ContainerRoleHint {
  return String(value ?? '').trim().toLowerCase() === 'child' ? 'child' : 'workspace'
}

export function normalizeContainerMountMode(value: string | null | undefined): ContainerMountMode {
  return String(value ?? '').trim().toLowerCase() === 'ro' ? 'ro' : 'rw'
}

export function mapContainerProfileMount(mount: ContainerProfileMountWire): ContainerProfileMount {
  return {
    sourcePath: String(mount.source_path ?? '').trim(),
    targetPath: String(mount.target_path ?? '').trim(),
    mode: normalizeContainerMountMode(mount.mode),
    workspacePath: String(mount.workspace_path ?? '').trim(),
    workspaceName: String(mount.workspace_name ?? '').trim(),
  }
}

export function mapContainerProfile(profile: ContainerProfileWire): ContainerProfile {
  return {
    id: String(profile.id ?? '').trim(),
    name: String(profile.name ?? '').trim(),
    description: String(profile.description ?? '').trim(),
    roleHint: normalizeContainerRoleHint(profile.role_hint),
    mounts: Array.isArray(profile.mounts) ? profile.mounts.map(mapContainerProfileMount) : [],
    createdAt: typeof profile.created_at === 'number' && Number.isFinite(profile.created_at) ? profile.created_at : 0,
    updatedAt: typeof profile.updated_at === 'number' && Number.isFinite(profile.updated_at) ? profile.updated_at : 0,
  }
}

export function createEmptyContainerProfileDraft(): ContainerProfileDraft {
  return {
    id: '',
    name: '',
    description: '',
    roleHint: 'workspace',
    mounts: [],
  }
}

export function createContainerProfileDraft(profile: ContainerProfile | null | undefined): ContainerProfileDraft {
  if (!profile) {
    return createEmptyContainerProfileDraft()
  }
  return {
    id: profile.id,
    name: profile.name,
    description: profile.description,
    roleHint: profile.roleHint,
    mounts: profile.mounts.map((mount) => ({ ...mount })),
  }
}

export function mapDraftToUpsertPayload(draft: ContainerProfileDraft): ContainerProfileWire {
  return {
    id: draft.id,
    name: draft.name,
    description: draft.description,
    role_hint: draft.roleHint,
    mounts: draft.mounts.map((mount) => ({
      source_path: mount.sourcePath,
      target_path: mount.targetPath,
      mode: mount.mode,
      workspace_path: mount.workspacePath,
      workspace_name: mount.workspaceName,
    })),
  }
}

export function sortContainerProfiles(profiles: ContainerProfile[]): ContainerProfile[] {
  return [...profiles].sort((left, right) => {
    const leftKey = left.name.trim().toLowerCase()
    const rightKey = right.name.trim().toLowerCase()
    if (leftKey === rightKey) {
      return right.updatedAt - left.updatedAt
    }
    return leftKey.localeCompare(rightKey)
  })
}

export function suggestContainerSlug(value: string, fallback = 'container'): string {
  const normalized = value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '')
  return normalized || fallback
}

export function containerMountTargetForPath(path: string): string {
  const trimmed = path.trim().replace(/[\\/]+$/, '')
  const segments = trimmed.split(/[\\/]/).filter(Boolean)
  const lastSegment = suggestContainerSlug(segments[segments.length - 1] || 'workspace', 'workspace')
  return `/workspace/${lastSegment}`
}
