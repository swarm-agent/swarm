export const SWARM_SECTIONS = ['projects', 'workers', 'deliverables', 'media', 'agents', 'settings', 'usage', 'charter', 'help'] as const
export type SwarmSection = typeof SWARM_SECTIONS[number]
export type SwarmPage = 'home' | SwarmSection

export function isSwarmSection(value: unknown): value is SwarmSection {
  return typeof value === 'string' && SWARM_SECTIONS.some((section) => section === value)
}

// Shared by sidebar links and programmatic navigation; never infer workspace
// identity from the selected project or persist a second active-page state.
export function swarmPageLink(workspaceSlug: string | undefined, page: SwarmPage) {
  if (workspaceSlug) {
    return page === 'home'
      ? { to: '/$workspaceSlug/swarm' as const, params: { workspaceSlug }, search: {} }
      : { to: '/$workspaceSlug/swarm/$swarmSection' as const, params: { workspaceSlug, swarmSection: page }, search: {} }
  }
  return page === 'home'
    ? { to: '/swarm' as const, search: {} }
    : { to: '/swarm/$swarmSection' as const, params: { swarmSection: page }, search: {} }
}

// Keep inspection under the persistent Swarm owner; legacy detail URLs redirect here.
export function swarmWorkerLink(workspaceSlug: string | undefined, workerId: string) {
  return { ...swarmPageLink(workspaceSlug, 'workers'), search: { workerId } }
}

export function swarmWorkerHref(workspaceSlug: string | undefined, workerId: string): string {
  return workspaceSlug
    ? `/${encodeURIComponent(workspaceSlug)}/workers/${encodeURIComponent(workerId)}`
    : `/workers/${encodeURIComponent(workerId)}`
}

/** Route-derived selection only: worker deep links never select Tasks. */
export function swarmActivePage(section: unknown, workerId?: string): SwarmPage {
  return workerId ? 'workers' : isSwarmSection(section) ? section : 'home'
}
