import { apiFetch, readErrorMessage, requestJson } from '../../../app/api'
import type { AuthCredential, AuthCredentialWire, UpsertAuthCredentialInput } from '../settings/types/auth'
import { mapAuthCredential } from '../settings/types/auth'
import type {
  DesktopOnboardingStatusWire,
  SaveDesktopOnboardingInput,
  WorkspaceOnboardingSessionStartResponse,
  WorkspaceOnboardingSessionStartResponseWire,
} from './types'
import { mapWorkspaceRepositoryState } from '../../workspaces/launcher/services/workspace-repository'

export function buildDesktopOnboardingPayload(input: SaveDesktopOnboardingInput): Record<string, unknown> {
  const payload: Record<string, unknown> = {}
  if (Object.prototype.hasOwnProperty.call(input, 'username')) {
    payload.username = input.username
  }
  if (Object.prototype.hasOwnProperty.call(input, 'swarmName')) {
    payload.swarm_name = input.swarmName
  }
  if (Object.prototype.hasOwnProperty.call(input, 'desktopOnboardingComplete')) {
    payload.desktop_onboarding_complete = input.desktopOnboardingComplete
  }
  return payload
}

export async function patchDesktopOnboarding(input: SaveDesktopOnboardingInput): Promise<DesktopOnboardingStatusWire> {
  return requestJson<DesktopOnboardingStatusWire>('/v1/onboarding', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(buildDesktopOnboardingPayload(input)),
  })
}

export async function acceptOnboardingProviderCredential(input: UpsertAuthCredentialInput): Promise<AuthCredential> {
  const response = await requestJson<AuthCredentialWire>('/v1/onboarding/provider/credential', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  })
  return mapAuthCredential(response)
}

export function buildWorkspaceOnboardingSessionPayload(input: {
  path: string
  expectedResolvedPath: string
  clientRequestId: string
  prompt?: string
}): Record<string, string> {
  const path = input.path.trim()
  const expectedResolvedPath = input.expectedResolvedPath.trim()
  const clientRequestId = input.clientRequestId.trim()
  if (!path || !expectedResolvedPath) {
    throw new Error('Onboarding Swarm requires the selected folder and its current canonical path.')
  }
  if (!clientRequestId) {
    throw new Error('Onboarding Swarm requires a stable request identity.')
  }
  return {
    path,
    expected_resolved_path: expectedResolvedPath,
    client_request_id: clientRequestId,
    ...(input.prompt?.trim() ? { input: input.prompt.trim() } : {}),
  }
}

export async function startWorkspaceOnboardingSession(input: {
  path: string
  expectedResolvedPath: string
  clientRequestId: string
  prompt?: string
}): Promise<WorkspaceOnboardingSessionStartResponse> {
  const request = buildWorkspaceOnboardingSessionPayload(input)
  const response = await apiFetch('/v3/sessions:workspace-onboarding', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Idempotency-Key': request.client_request_id,
    },
    body: JSON.stringify(request),
  })
  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }
  const payload = await response.json() as WorkspaceOnboardingSessionStartResponseWire
  const sessionId = String(payload.session_id ?? '').trim()
  if (payload.ok !== true || !sessionId || !payload.repository || !payload.session || !payload.first_message || !payload.projection || !payload.mutation) {
    throw new Error('Onboarding Swarm returned an incomplete session response.')
  }
  if (payload.session.id !== sessionId || payload.first_message.session_id !== sessionId || payload.projection.session_id !== sessionId || payload.mutation.session_id !== sessionId) {
    throw new Error('Onboarding Swarm returned inconsistent session identity.')
  }
  const repository = mapWorkspaceRepositoryState(payload.repository)
  if (repository.state !== 'needs_assisted_setup' || !repository.path) {
    throw new Error('Onboarding Swarm did not return an eligible existing-file folder.')
  }
  return {
    ok: true,
    sessionId,
    repository,
    session: payload.session,
    firstMessage: payload.first_message,
    projection: payload.projection,
    mutation: payload.mutation,
    replayed: Boolean(payload.replayed),
  }
}

export async function upgradeAccountToTeam(teamName: string): Promise<void> {
  await requestJson('/v1/account/team/upgrade', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ team_name: teamName }),
  })
}

