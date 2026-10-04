import { OrchestrateView } from './OrchestrateView'

export function OrchestratePage({
  workspaceSlug,
  onNavigateHome,
}: {
  workspaceSlug?: string
  onNavigateHome?: () => void
}) {
  // OrchestrateView derives readiness and runtime demand from the resolved project
  // ID. Keying by the URL alias remounts on ID-to-name canonicalization, aborting
  // and duplicating the same initial collection request.
  return (
    <OrchestrateView
      workspaceSlug={workspaceSlug}
      onNavigateHome={onNavigateHome}
      initialThemeId="apple_peach"
    />
  )
}

export { OrchestrateView }
