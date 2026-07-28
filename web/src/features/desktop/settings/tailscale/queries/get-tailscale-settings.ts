import { requestJson } from '../../../../../app/api'
import { mapTailscaleSettingsStatus, type TailscaleSettingsStatus } from '../types'

export const TAILSCALE_SETTINGS_PATH = '/v1/settings/tailscale'

export async function getTailscaleSettings(): Promise<TailscaleSettingsStatus> {
  return mapTailscaleSettingsStatus(await requestJson<unknown>(TAILSCALE_SETTINGS_PATH))
}
