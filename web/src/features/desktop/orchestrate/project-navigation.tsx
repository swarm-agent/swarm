import { Link } from '@tanstack/react-router'
import { Bot, ChartNoAxesCombined, Layers, Film, Settings } from 'lucide-react'
import type { SwarmPage } from './swarm-navigation'
import { swarmPageLink } from './swarm-navigation'
import { projectConversationLink } from './project-conversations'

export function ProjectNavigation({ activePage, projectSegment, sessionId, workspaceSlug, deliverableCount, mediaCount, onSelect }: {
  activePage: SwarmPage; projectSegment?: string; sessionId?: string; workspaceSlug?: string
  deliverableCount: number; mediaCount: number; onSelect: () => void
}) {
  const items = [
    { page: 'deliverables', label: 'Deliverables', icon: Layers, count: deliverableCount },
    { page: 'media', label: 'Media', icon: Film, count: mediaCount },
    { page: 'settings', label: 'Settings', icon: Settings },
    { page: 'agents', label: 'Agents', icon: Bot },
    { page: 'usage', label: 'Usage', icon: ChartNoAxesCombined },
  ] as const
  return <nav aria-label="Swarm destinations" className="swarm-route-navigation swarm-compact-navigation" onClick={onSelect}>
    {items.map(item => {
      const current = activePage === item.page ? 'page' as const : undefined
      const link = projectSegment
        ? { ...projectConversationLink(projectSegment, sessionId), search: { section: item.page } }
        : swarmPageLink(workspaceSlug, item.page)
      return <Link key={item.page} {...link} aria-label={item.label} title={item.label}
        activeOptions={{ exact: true, includeSearch: true }}
        aria-current={current} activeProps={{ 'aria-current': current }} inactiveProps={{ 'aria-current': current }}>
        <item.icon size={14} aria-hidden="true" />
        <span className="swarm-destination-label">{item.label}</span>
        {'count' in item && <span className="swarm-destination-count">{item.count}</span>}
      </Link>
    })}
  </nav>
}
