import { requestJson } from '../../../../../app/api'
import { mapTailscaleSettingsStatus, type TailscaleSettingsStatus } from '../types'

export const TAILSCALE_APPROVE_PATH = '/v1/settings/tailscale/approve'

export async function approveTailscaleOrigin(origin: string): Promise<TailscaleSettingsStatus> {
  const response = await requestJson<unknown>(TAILSCALE_APPROVE_PATH, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ origin }),
  })
  return mapTailscaleSettingsStatus(response)
}
