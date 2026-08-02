import { requestJson } from '../../../../app/api'

interface UpdateWorkspaceWorktreesResponse {
  ok: boolean
  worktrees: {
    enabled: boolean
  }
}

export async function setWorkspaceWorktrees(path: string, enabled: boolean): Promise<boolean> {
  const response = await requestJson<UpdateWorkspaceWorktreesResponse>('/v1/worktrees', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      workspace_path: path,
      enabled,
    }),
  })

  return Boolean(response.worktrees?.enabled)
}
