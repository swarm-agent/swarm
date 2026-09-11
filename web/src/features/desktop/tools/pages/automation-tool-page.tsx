import { useParams } from '@tanstack/react-router'
import { useWorkspaceLauncher } from '../../../workspaces/launcher/state/use-workspace-launcher'
import { resolveWorkspaceBySlug } from '../../../workspaces/launcher/services/workspace-route'
import { AutomationWorkspace } from '../automations/automation-workspace'

export function AutomationToolPage() {
  const params = useParams({ strict: false }) as { workspaceSlug?: string }
  const { workspaces, loading } = useWorkspaceLauncher({ applyDocumentTheme: false, autoRefresh: false, browseDuringRefresh: false })
  if (loading) return <main role="status" className="p-6">Loading workspace…</main>
  const workspace = resolveWorkspaceBySlug(workspaces, params.workspaceSlug ?? '')
  if (!workspace?.workspaceId) return <main className="p-6"><h1>Workspace unavailable</h1><p>Choose an accessible workspace before opening automations.</p><a href="/">Workspaces</a></main>
  return <AutomationWorkspace key={workspace.workspaceId} workspaceId={workspace.workspaceId} workspacePath={workspace.path} workspaceName={workspace.workspaceName} workspaceSlug={params.workspaceSlug!} />
}
