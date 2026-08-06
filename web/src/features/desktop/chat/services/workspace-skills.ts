import { requestJson } from '../../../../app/api'

export interface WorkspaceSkill {
  name: string
  canonicalName: string
  description: string
  scope: string
}

interface WorkspaceSkillWire {
  name?: string
  canonical_name?: string
  description?: string
  scope?: string
  active?: boolean
}

interface WorkspaceSkillsResponseWire {
  report?: {
    skills?: WorkspaceSkillWire[]
  }
}

function mapWorkspaceSkill(skill: WorkspaceSkillWire): WorkspaceSkill | null {
  const name = skill.name?.trim() ?? ''
  const canonicalName = skill.canonical_name?.trim() || name
  if (!canonicalName || skill.active === false) return null
  return {
    name: name || canonicalName,
    canonicalName,
    description: skill.description?.trim() ?? '',
    scope: skill.scope?.trim() ?? '',
  }
}

export async function fetchWorkspaceSkills(workspacePath: string, signal?: AbortSignal): Promise<WorkspaceSkill[]> {
  const search = new URLSearchParams({ cwd: workspacePath })
  const response = await requestJson<WorkspaceSkillsResponseWire>(`/v1/context/sources?${search.toString()}`, { signal })
  const skills = Array.isArray(response.report?.skills) ? response.report.skills : []
  return skills
    .map(mapWorkspaceSkill)
    .filter((skill): skill is WorkspaceSkill => skill !== null)
    .sort((left, right) => left.name.localeCompare(right.name))
}

export async function deleteWorkspaceSkill(workspacePath: string, canonicalName: string): Promise<void> {
  await requestJson('/v1/workspace/skills/delete', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ workspace_path: workspacePath, canonical_name: canonicalName }),
  })
}
