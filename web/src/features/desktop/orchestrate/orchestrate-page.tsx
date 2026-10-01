import { OrchestrateView } from './OrchestrateView'

export function OrchestratePage({
  workspaceSlug,
  onNavigateHome,
}: {
  workspaceSlug?: string
  onNavigateHome?: () => void
}) {
  return (
    <OrchestrateView
      workspaceSlug={workspaceSlug}
      onNavigateHome={onNavigateHome}
      initialThemeId="apple_peach"
    />
  )
}

export { OrchestrateView }
