import { Link } from '@tanstack/react-router'
import { Cpu, MessageSquare } from 'lucide-react'
import './swarm-section.css'

type SidebarModeSelectorProps = {
  mode: 'chat' | 'swarm'
  workspaceSlug?: string
  pendingReviews: number
  onNavigate?: () => void
  onNavigateChat?: () => void
}

export function SidebarModeSelector({ mode, workspaceSlug, pendingReviews, onNavigate, onNavigateChat }: SidebarModeSelectorProps) {
  // Link applies automatic aria-current AFTER user props. Exact matching prevents
  // the Chat workspace ancestor from becoming current on a Swarm subroute.
  // The owning shell supplies mode so nested routes keep their mode selected.
  return (
    <nav aria-label="Chat and Swarm mode" className="swarm-sidebar-modes" onClick={onNavigate}>
      <Link
        {...(workspaceSlug ? { to: '/$workspaceSlug' as const, params: { workspaceSlug } } : { to: '/' as const })}
        activeOptions={{ exact: true, includeSearch: false }}
        activeProps={{}}
        aria-current={mode === 'chat' ? 'page' : undefined}
        data-selected={mode === 'chat'}
        onClick={onNavigateChat}
        aria-label="Switch to Chat Mode"
      >
        <MessageSquare size={12} aria-hidden="true" />Chat
      </Link>
      <Link
        {...(workspaceSlug ? { to: '/$workspaceSlug/swarm' as const, params: { workspaceSlug } } : { to: '/swarm' as const })}
        activeOptions={{ exact: true, includeSearch: false }}
        activeProps={{}}
        aria-current={mode === 'swarm' ? 'page' : undefined}
        data-selected={mode === 'swarm'}
        aria-label="Switch to Swarm Orchestrate Mode"
      >
        <Cpu size={12} aria-hidden="true" />Swarm
        {pendingReviews > 0 && <span className="swarm-mode-review-count" aria-label={`${pendingReviews} worker reviews pending`}>{pendingReviews}</span>}
      </Link>
    </nav>
  )
}
