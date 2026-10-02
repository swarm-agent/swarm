import { useRouterState } from '@tanstack/react-router'
import { OrchestrateView } from './OrchestrateView'

export function OrchestratePage({
  workspaceSlug,
  onNavigateHome,
}: {
  workspaceSlug?: string
  onNavigateHome?: () => void
}) {
  const identity = useRouterState({ select: state => {
    const params = state.matches[state.matches.length - 1]?.params as { projectId?: string; sessionId?: string }
    return `${params?.projectId || ''}:${params?.sessionId || ''}`
  } })
  return (
    <OrchestrateView
      key={identity}
      workspaceSlug={workspaceSlug}
      onNavigateHome={onNavigateHome}
      initialThemeId="apple_peach"
    />
  )
}

export { OrchestrateView }
