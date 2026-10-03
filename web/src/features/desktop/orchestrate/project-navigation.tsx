import { Link } from '@tanstack/react-router'
import { Bot, Folder, Home, Layers, Film, Settings } from 'lucide-react'
import type { ReactNode } from 'react'
import type { SwarmPage } from './swarm-navigation'
import { swarmPageLink } from './swarm-navigation'
import { projectConversationLink } from './project-conversations'

export function ProjectNavigation({ activePage, projectSegment, sessionId, workspaceSlug, projectCount, deliverableCount, mediaCount, workerCount, pendingReviews, onSelect }: {
  activePage: SwarmPage; projectSegment?: string; sessionId?: string; workspaceSlug?: string
  projectCount: number; deliverableCount: number; mediaCount: number; workerCount?: ReactNode; pendingReviews: number; onSelect: () => void
}) {
  const items = [
    { page: 'home', label: 'Tasks', icon: Home },
    { page: 'workers', label: 'Workers', icon: Bot, count: workerCount },
    { page: 'projects', label: 'Projects', icon: Folder, count: projectCount },
    { page: 'deliverables', label: 'Deliverables', icon: Layers, count: deliverableCount },
    { page: 'media', label: 'Media Studio and Library', text: 'Media', icon: Film, count: mediaCount },
    { page: 'agents', label: 'Agents', icon: Bot },
    { page: 'settings', label: 'Settings', icon: Settings },
  ] as const
  return <nav aria-label="Swarm destinations" className="swarm-route-navigation swarm-compact-navigation" onClick={onSelect}>
    {items.map(item => {
      const selected = activePage === item.page || (item.page === 'projects' && activePage === 'charter')
      const current = selected ? 'page' as const : undefined
      const link = projectSegment
        ? { ...projectConversationLink(projectSegment, sessionId), search: { section: item.page } }
        : swarmPageLink(workspaceSlug, item.page)
      return <Link key={item.page} {...link} aria-label={item.label} title={item.page === 'workers' && pendingReviews ? `Workers · ${pendingReviews} pending reviews` : item.label}
        activeOptions={{ exact: true, includeSearch: true }}
        aria-current={current} activeProps={{ 'aria-current': current }} inactiveProps={{ 'aria-current': current }}>
        <item.icon size={14} aria-hidden="true" />
        <span className="swarm-destination-label">{'text' in item ? item.text : item.label}</span>
        {'count' in item && <span className="swarm-destination-count">{item.count}</span>}
        {item.page === 'workers' && pendingReviews > 0 && <span aria-label={`${pendingReviews} pending reviews`} className="swarm-review-dot" />}
      </Link>
    })}
  </nav>
}
