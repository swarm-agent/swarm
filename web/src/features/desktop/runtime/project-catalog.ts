import { ensureDesktopSession, requestStartupJson, subscribeDesktopSessionReset } from '../../../app/api'
import { queryClient } from '../../../app/query-client'
import { desktopProjects } from './desktop-projects'
import type { ProjectContextGeneration } from '../state/project-creation'

export interface ProjectCatalogEntry {
  id: string
  name: string
  description?: string
  workspaces?: Array<{ workspace_id: string; path: string; role: string; label?: string }>
  project_context?: string
  theme_id?: string
  context_generation?: ProjectContextGeneration
  icon_png_data_url?: string
}

const key = ['project-catalog'] as const
subscribeDesktopSessionReset(() => {
  void queryClient.cancelQueries({ queryKey: key })
  queryClient.removeQueries({ queryKey: key })
})
desktopProjects.onProjectUpdate(() => { void queryClient.invalidateQueries({ queryKey: key }) })

/** Account-bound, shared navigation read. Never used as mutation authorization. */
export async function readProjectCatalog() {
  const identity = await ensureDesktopSession()
  return queryClient.fetchQuery({
    queryKey: [...key, identity.accountScopeId],
    queryFn: ({ signal }) => requestStartupJson<{ projects: ProjectCatalogEntry[] }>('/v3/projects', { signal }),
    staleTime: 30_000,
    retry: false,
  })
}

export function invalidateProjectCatalog() {
  return queryClient.invalidateQueries({ queryKey: key })
}
