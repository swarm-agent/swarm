export interface EnvironmentsSearch {
  tab?: string
  workspace_id?: string
  deployment_id?: string
}

// Both environment routes must use this validator; identities travel together.
export function validateEnvironmentsSearch(search: Record<string, unknown>): EnvironmentsSearch {
  const identity = (value: unknown) => typeof value === 'string' && value.length > 0 && value.length <= 256 && value === value.trim() && !/[\x00-\x1f]/.test(value) ? value : undefined
  return { tab: search.tab === 'deployments' || search.tab === 'connections' ? search.tab : undefined,
    workspace_id: identity(search.workspace_id), deployment_id: identity(search.deployment_id) }
}
