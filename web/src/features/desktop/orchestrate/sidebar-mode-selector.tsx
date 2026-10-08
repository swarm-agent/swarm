import { Link } from '@tanstack/react-router'
import './swarm-section.css'

type SidebarModeSelectorProps = {
  mode: 'chat' | 'swarm'
  workspaceSlug?: string
  pendingReviews: number
  onNavigate?: () => void
  onNavigateChat?: () => void
}

// Retained history screens link back to the single Swarm product, not a Chat mode.
export function SidebarModeSelector({ pendingReviews, onNavigate }: SidebarModeSelectorProps) {
  return <nav aria-label="Swarm" className="swarm-sidebar-modes" onClick={onNavigate}>
    <Link to="/projects">Swarm projects{pendingReviews > 0 && <span className="swarm-mode-review-count">{pendingReviews}</span>}</Link>
  </nav>
}
