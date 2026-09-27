export const SWARM_SECTIONS = ['projects', 'workers', 'deliverables', 'media', 'settings'] as const
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
      ? { to: '/$workspaceSlug/swarm' as const, params: { workspaceSlug } }
      : { to: '/$workspaceSlug/swarm/$swarmSection' as const, params: { workspaceSlug, swarmSection: page } }
  }
  return page === 'home'
    ? { to: '/swarm' as const }
    : { to: '/swarm/$swarmSection' as const, params: { swarmSection: page } }
}
