import { validateEnvironmentsSearch } from './environments-search'

export function deploymentLocation(workspaceId: string, deploymentId: string) {
  return { to: '/environments' as const, search: validateEnvironmentsSearch({ tab: 'deployments', workspace_id: workspaceId, deployment_id: deploymentId }) }
}

export function navigateDeploymentClick(event: { button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; preventDefault(): void }, navigate: () => void): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  navigate()
}
