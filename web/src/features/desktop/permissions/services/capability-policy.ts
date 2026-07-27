import { requestJson } from '../../../../app/api'

export type CapabilityPolicyMode = 'ask' | 'always_allow' | 'bounded'
export type SessionDeployOverLimitAction = 'ask' | 'deny'

export interface SessionDeployPolicy {
  mode: CapabilityPolicyMode
  automatic_deployments_per_parent_run: number
  over_limit_action: SessionDeployOverLimitAction
}

export interface PlanAcceptancePolicy {
  mode: 'ask' | 'always_allow'
}

export interface CapabilityPolicies {
  session_deploy: SessionDeployPolicy
  plan_acceptance: PlanAcceptancePolicy
}

interface CapabilityPolicyResponse {
  session_deploy?: Partial<SessionDeployPolicy>
  plan_acceptance?: Partial<PlanAcceptancePolicy>
}

export const DEFAULT_SESSION_DEPLOY_POLICY: SessionDeployPolicy = {
  mode: 'ask',
  automatic_deployments_per_parent_run: 0,
  over_limit_action: 'ask',
}

export const DEFAULT_PLAN_ACCEPTANCE_POLICY: PlanAcceptancePolicy = { mode: 'ask' }

export function normalizeCapabilityPolicies(response?: CapabilityPolicyResponse | null): CapabilityPolicies {
  const deploy = response?.session_deploy
  const acceptance = response?.plan_acceptance
  const deployMode = deploy?.mode === 'always_allow' || deploy?.mode === 'bounded' ? deploy.mode : 'ask'
  return {
    session_deploy: {
      mode: deployMode,
      automatic_deployments_per_parent_run: typeof deploy?.automatic_deployments_per_parent_run === 'number' && Number.isFinite(deploy.automatic_deployments_per_parent_run)
        ? Math.max(0, deploy.automatic_deployments_per_parent_run)
        : 0,
      over_limit_action: deploy?.over_limit_action === 'deny' ? 'deny' : 'ask',
    },
    plan_acceptance: {
      mode: acceptance?.mode === 'always_allow' ? 'always_allow' : 'ask',
    },
  }
}

export async function fetchCapabilityPolicies(): Promise<CapabilityPolicies> {
  return normalizeCapabilityPolicies(await requestJson<CapabilityPolicyResponse>('/v1/permissions/capabilities'))
}

export async function saveCapabilityPolicies(input: Partial<CapabilityPolicies>): Promise<CapabilityPolicies> {
  return normalizeCapabilityPolicies(await requestJson<CapabilityPolicyResponse>('/v1/permissions/capabilities', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  }))
}

export async function savePlanAcceptanceMode(mode: PlanAcceptancePolicy['mode']): Promise<PlanAcceptancePolicy> {
  return (await saveCapabilityPolicies({ plan_acceptance: { mode } })).plan_acceptance
}
