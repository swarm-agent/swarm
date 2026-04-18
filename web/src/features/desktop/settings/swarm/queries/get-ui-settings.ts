import { requestJson } from '../../../../../app/api'
import type { UISettingsWire } from '../types/swarm-settings'

export async function getUISettings(): Promise<UISettingsWire> {
  return requestJson<UISettingsWire>('/v1/ui/settings')
}

function normalizeRemoteSSHTarget(value: string): string {
  return value.trim()
}

export async function saveRemoteSSHTarget(input: { current: UISettingsWire; target: string }): Promise<UISettingsWire> {
  const nextTarget = normalizeRemoteSSHTarget(input.target)
  if (!nextTarget) {
    throw new Error('SSH alias or target is required.')
  }
  const existing = Array.isArray(input.current.swarm?.remote_ssh_targets)
    ? input.current.swarm?.remote_ssh_targets ?? []
    : []
  const deduped = [nextTarget, ...existing.map(normalizeRemoteSSHTarget)]
    .filter((value, index, array) => value && array.indexOf(value) === index)
    .slice(0, 8)

  return requestJson<UISettingsWire>('/v1/ui/settings', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      ...input.current,
      swarm: {
        ...(input.current.swarm ?? {}),
        remote_ssh_targets: deduped,
      },
    }),
  })
}
