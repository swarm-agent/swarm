import { requestJson } from '../../../app/api'
import type { AuthCredential, AuthCredentialWire, UpsertAuthCredentialInput } from '../settings/types/auth'
import { mapAuthCredential } from '../settings/types/auth'
import type {
  DesktopOnboardingStatusWire,
  SaveDesktopOnboardingInput,
} from './types'

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

export async function upgradeAccountToTeam(teamName: string): Promise<void> {
  await requestJson('/v1/account/team/upgrade', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ team_name: teamName }),
  })
}

