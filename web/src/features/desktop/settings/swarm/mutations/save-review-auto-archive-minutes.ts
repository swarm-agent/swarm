import { patchUISettings } from '../queries/get-ui-settings'
import { type UISettingsWire, withReviewAutoArchiveMinutes } from '../types/swarm-settings'

export async function saveReviewAutoArchiveMinutes(input: { current: UISettingsWire; minutes: number }): Promise<UISettingsWire> {
  const next = withReviewAutoArchiveMinutes(input.current, input.minutes)
  return patchUISettings({ chat: { review_auto_archive_minutes: next.chat?.review_auto_archive_minutes } })
}
