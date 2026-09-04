import type { ProviderStatus } from '../settings/types/auth'
import type { VaultStatus } from '../vault/types'
import type {
  MessageSnapshot,
  SessionMutationResult,
  SessionSnapshot,
  V3SessionProjection,
} from '../state/desktop-v3-cache-types'
import type { WorkspaceRepositoryState, WorkspaceRepositoryStateWire } from '../../workspaces/launcher/services/workspace-repository'

export interface WorkspaceOnboardingSessionStartResponseWire {
  ok?: boolean
  session_id?: string
  repository?: WorkspaceRepositoryStateWire
  session?: SessionSnapshot
  first_message?: MessageSnapshot
  projection?: V3SessionProjection
  mutation?: SessionMutationResult
  replayed?: boolean
}

export interface WorkspaceOnboardingSessionStartResponse {
  ok: true
  sessionId: string
  repository: WorkspaceRepositoryState
  session: SessionSnapshot
  firstMessage: MessageSnapshot
  projection: V3SessionProjection
  mutation: SessionMutationResult
  replayed: boolean
}

export interface DesktopOnboardingConfigWire {
  swarm_name?: string
  desktop_onboarding_complete?: boolean
}

export interface DesktopOnboardingHeuristicsWire {
  missing_swarm_name?: boolean
  credential_count?: number
  agent_count?: number
  saved_workspace_count?: number
  vault_configured?: boolean
}

export interface DesktopOnboardingAuthWire {
  credential_count?: number
  active_providers?: string[]
  providers?: ProviderStatus[]
}

export interface DesktopOnboardingWorkspaceWire {
  saved_count?: number
}

export interface DesktopOnboardingIdentityWire {
  bootstrapped?: boolean
  user_id?: string
  account_scope_id?: string
  username?: string
  team_id?: string
  team_display_name?: string
  team_default?: boolean
  membership_role?: string
}

export interface DesktopOnboardingStatusWire {
  ok?: boolean
  needs_onboarding?: boolean
  identity?: DesktopOnboardingIdentityWire
  config?: DesktopOnboardingConfigWire
  heuristics?: DesktopOnboardingHeuristicsWire
  vault?: VaultStatus
  auth?: DesktopOnboardingAuthWire
  workspace?: DesktopOnboardingWorkspaceWire
}

export interface DesktopOnboardingConfig {
  swarmName: string
  desktopOnboardingComplete: boolean
}

export interface DesktopOnboardingHeuristics {
  missingSwarmName: boolean
  credentialCount: number
  agentCount: number
  savedWorkspaceCount: number
  vaultConfigured: boolean
}

export interface DesktopOnboardingAuth {
  credentialCount: number
  activeProviders: string[]
  providers: ProviderStatus[]
}

export interface DesktopOnboardingWorkspace {
  savedCount: number
}

export interface DesktopOnboardingIdentity {
  bootstrapped: boolean
  userID: string
  accountScopeID: string
  username: string
  teamID: string
  teamDisplayName: string
  teamDefault: boolean
  membershipRole: string
}

export interface DesktopOnboardingStatus {
  ok: boolean
  needsOnboarding: boolean
  identity: DesktopOnboardingIdentity
  config: DesktopOnboardingConfig
  heuristics: DesktopOnboardingHeuristics
  vault: VaultStatus
  auth: DesktopOnboardingAuth
  workspace: DesktopOnboardingWorkspace
}

export interface SaveDesktopOnboardingInput {
  username?: string
  swarmName?: string
  desktopOnboardingComplete?: boolean
}
