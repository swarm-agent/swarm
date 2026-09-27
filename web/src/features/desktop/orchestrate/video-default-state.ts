import type { QueryClient } from '@tanstack/react-query'
import type { UISettingsWire } from '../settings/swarm/types/swarm-settings'
import { resolveQualifiedVideoModel, type TaskModalModelOption } from './videoTaskSettings'

// Qualified identities are exact; legacy bare IDs must be unambiguous.
export function resolveVideoDefault(options: TaskModalModelOption[], configured: string): string {
  if (!configured) return ''
  const matches = options.filter(option => configured.includes(':')
    ? resolveQualifiedVideoModel(option, option.id) === configured
    : option.id === configured || option.model === configured)
  return matches.length === 1 ? matches[0].id : ''
}

// One cache authority, success-only publication, and cancellation of stale reads.
export async function persistVideoDefault(
  client: QueryClient,
  persist: () => Promise<UISettingsWire>,
): Promise<void> {
  await client.cancelQueries({ queryKey: ['ui-settings'] })
  const saved = await persist()
  await client.cancelQueries({ queryKey: ['ui-settings'] })
  client.setQueryData(['ui-settings'], saved)
}
