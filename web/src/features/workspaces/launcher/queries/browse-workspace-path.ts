import { requestJson } from '../../../../app/api'
import type {
  WorkspaceBrowseResponse,
  WorkspaceBrowseResult,
  WorkspaceBrowseResultWire,
} from '../types/workspace'
import { mapWorkspaceBrowseResult } from '../types/workspace'

export async function browseWorkspacePath(path: string, includeFiles = false): Promise<WorkspaceBrowseResult> {
  const query = new URLSearchParams()
  if (includeFiles) query.set('include_files', 'true')
  if (path.trim() !== '') {
    query.set('path', path)
  }
  const response = await requestJson<WorkspaceBrowseResponse & { browser: WorkspaceBrowseResultWire }>(`/v1/workspace/browse?${query.toString()}`)
  return mapWorkspaceBrowseResult(response.browser)
}
