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
    const params = state.matches[state.matches.length - 1]?.params as { projectId?: string }
    // Session navigation replaces the conversation, not the project shell.
    return params?.projectId || ''
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
