import { requestJson } from '../../../../app/api'

export interface CreateWorkspaceFolderResult {
  path: string
  name: string
  parentPath: string
  requiresSudo: boolean
  permissionErrorMessage: string
}

interface CreateWorkspaceFolderResultWire {
  path: string
  name: string
  parent_path: string
  requires_sudo: boolean
  permission_error_message?: string
}

function mapCreateWorkspaceFolderResult(entry: CreateWorkspaceFolderResultWire): CreateWorkspaceFolderResult {
  return {
    path: entry.path,
    name: entry.name,
    parentPath: entry.parent_path,
    requiresSudo: Boolean(entry.requires_sudo),
    permissionErrorMessage: String(entry.permission_error_message ?? '').trim(),
  }
}

export async function createWorkspaceFolder(parentPath: string, name: string): Promise<CreateWorkspaceFolderResult> {
  const response = await requestJson<{ ok: boolean; folder: CreateWorkspaceFolderResultWire }>('/v1/workspace/folders/create', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      parent_path: parentPath,
      name,
    }),
  })

  return mapCreateWorkspaceFolderResult(response.folder)
}
