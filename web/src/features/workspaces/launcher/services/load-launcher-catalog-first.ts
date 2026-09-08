import type { WorkspaceOverviewResponse } from '../types/workspace-overview'
import type { WorkspaceDiscoverEntry } from '../types/workspace'

interface LauncherCatalogLoad {
  loadCatalog: () => Promise<WorkspaceOverviewResponse>
  publishCatalog: (overview: WorkspaceOverviewResponse) => void
  loadDetails: () => Promise<WorkspaceOverviewResponse>
  publishDetails: (overview: WorkspaceOverviewResponse) => void
  discover: () => Promise<WorkspaceDiscoverEntry[]>
  publishDiscovery: (entries: WorkspaceDiscoverEntry[], catalog: WorkspaceOverviewResponse) => void
  reportBackgroundError: (error: unknown) => void
  isCurrent: () => boolean
}

// Only the catalog belongs to the foreground promise. Start optional work in a
// later task so it cannot compete with the initial catalog request or state update.
export async function loadLauncherCatalogFirst(load: LauncherCatalogLoad): Promise<void> {
  const catalog = await load.loadCatalog()
  if (!load.isCurrent()) return
  load.publishCatalog(catalog)
  setTimeout(() => {
    if (!load.isCurrent()) return
    const report = (error: unknown) => {
      if (load.isCurrent()) load.reportBackgroundError(error)
    }
    void load.loadDetails().then((overview) => {
      if (load.isCurrent()) load.publishDetails(overview)
    }).catch(report)
    void load.discover().then((entries) => {
      if (load.isCurrent()) load.publishDiscovery(entries, catalog)
    }).catch(report)
  }, 0)
}
