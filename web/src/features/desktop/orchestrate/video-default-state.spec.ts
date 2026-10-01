import assert from 'node:assert/strict'
import test from 'node:test'
import { QueryClient } from '@tanstack/react-query'
import { persistVideoDefault, resolveVideoDefault } from './video-default-state'
import type { UISettingsWire } from '../settings/swarm/types/swarm-settings'

// Requirement: modal and /media share persisted settings, never optimistic success or
// a catalog fallback. Authority: persistVideoDefault/useVideoTaskDefault and the
// canonical ui-settings query. The real QueryClient proves cancellation/publication
// without a browser or provider; rendered reopen/draft behavior needs browser proof.
const settings = (model: string): UISettingsWire => ({ tools: { video: { default_model: model } } })

test('failed save leaves the shared persisted default unchanged', async () => {
  const client = new QueryClient()
  client.setQueryData(['ui-settings'], settings('google:veo'))
  await assert.rejects(persistVideoDefault(client, async () => { throw new Error('save rejected') }), /save rejected/)
  assert.deepEqual(client.getQueryData(['ui-settings']), settings('google:veo'))
  client.clear()
})

test('successful save cancels stale reads and publishes the server response to every observer', async () => {
  const client = new QueryClient()
  client.setQueryData(['ui-settings'], settings('google:veo'))
  let finishRead!: (value: UISettingsWire) => void
  const staleRead = client.fetchQuery({ queryKey: ['ui-settings'], queryFn: () => new Promise<UISettingsWire>(resolve => { finishRead = resolve }) }).catch(() => undefined)
  await persistVideoDefault(client, async () => settings('google:omni'))
  finishRead(settings('google:veo'))
  await staleRead
  assert.deepEqual(client.getQueryData(['ui-settings']), settings('google:omni'))
  client.clear()
})

test('default matching preserves provider identity and rejects missing or ambiguous defaults', () => {
  const options = [
    { id: 'google:veo', model: 'veo', provider: 'google', label: 'Veo', ready: true },
    { id: 'openrouter:veo', model: 'veo', provider: 'openrouter', label: 'Veo elsewhere', ready: true },
  ]
  assert.equal(resolveVideoDefault(options, 'google:veo'), 'google:veo')
  assert.equal(resolveVideoDefault(options, 'openrouter:veo'), 'openrouter:veo')
  for (const configured of ['', 'veo', 'google:missing']) assert.equal(resolveVideoDefault(options, configured), '')
})
