import { requestJson } from '../../../../../app/api'
import { mapTailscaleSettingsStatus, type TailscaleSettingsStatus } from '../types'

export const TAILSCALE_REVOKE_PATH = '/v1/settings/tailscale/revoke'

export async function revokeTailscaleOrigin(origin: string): Promise<TailscaleSettingsStatus> {
  const response = await requestJson<unknown>(TAILSCALE_REVOKE_PATH, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ origin }),
  })
  return mapTailscaleSettingsStatus(response)
}
