import { useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useWorkspaceLauncher } from '../../../workspaces/launcher/state/use-workspace-launcher'
import { resolveWorkspaceBySlug } from '../../../workspaces/launcher/services/workspace-route'
import { AutomationV2Workspace as AutomationWorkspace } from '../automations/automation-v2-workspace'

export function AutomationToolPage() {
  const params = useParams({ strict: false }) as { workspaceSlug?: string; workerId?: string }
  const search = useSearch({ strict: false }) as { sessionId?: string; automationId?: string; workerId?: string }
  const navigate = useNavigate()
  const { workspaces, loading } = useWorkspaceLauncher({ applyDocumentTheme: false, autoRefresh: false, browseDuringRefresh: false })
  if (loading) return <main role="status" className="p-6">Loading workspace…</main>
  const workspace = params.workspaceSlug ? resolveWorkspaceBySlug(workspaces, params.workspaceSlug) : undefined
  const targetWorkerId = (params.workerId || search?.workerId || search?.automationId || '').trim()
  return (
    <AutomationWorkspace
      key={workspace?.workspaceId ?? 'global-workers'}
      workspaceId={workspace?.workspaceId}
      workspacePath={workspace?.path}
      workspaceName={workspace?.workspaceName}
      workspaceBindingId={workspace?.localWorkspaceBindingId}
      workspaceSlug={params.workspaceSlug}
      workspaces={workspaces}
      initialSessionId={targetWorkerId || undefined}
      onOpenSession={(id, targetSlug) => {
        const slug = targetSlug || params.workspaceSlug
        if (slug) {
          void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug: slug, sessionId: id } })
        }
      }}
      onSelectWorker={(id, targetSlug) => {
        const slug = targetSlug || params.workspaceSlug
        if (slug) {
          if (id) {
            void navigate({ to: '/$workspaceSlug/workers/$workerId', params: { workspaceSlug: slug, workerId: id }, search: {} })
          } else {
            void navigate({ to: '/$workspaceSlug/workers', params: { workspaceSlug: slug }, search: {} })
          }
        } else {
          if (id) {
            void navigate({ to: '/workers/$workerId', params: { workerId: id } as any, search: {} })
          } else {
            void navigate({ to: '/workers', search: {} })
          }
        }
      }}
    />
  )
}
