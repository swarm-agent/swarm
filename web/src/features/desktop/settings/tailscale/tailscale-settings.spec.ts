import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { mapTailscaleSettingsStatus, normalizeTailscaleOriginInput, verifiedRouteForInput } from './types'
import { TAILSCALE_SETTINGS_PATH } from './queries/get-tailscale-settings'
import { TAILSCALE_APPROVE_PATH } from './mutations/approve-tailscale-origin'
import { TAILSCALE_REVOKE_PATH } from './mutations/revoke-tailscale-origin'

const pageSource = readFileSync(new URL('./components/tailscale-settings-page.tsx', import.meta.url), 'utf8')
const settingsSource = readFileSync(new URL('../components/desktop-settings-page.tsx', import.meta.url), 'utf8')

test('Tailscale settings uses dedicated status and mutation endpoints', () => {
  assert.equal(TAILSCALE_SETTINGS_PATH, '/v1/settings/tailscale')
  assert.equal(TAILSCALE_APPROVE_PATH, '/v1/settings/tailscale/approve')
  assert.equal(TAILSCALE_REVOKE_PATH, '/v1/settings/tailscale/revoke')
  assert.doesNotMatch(pageSource, /\/v1\/onboarding|\/v1\/ui\/settings/)
})

test('manual origin selection accepts only an exact verified route', () => {
  const status = mapTailscaleSettingsStatus({
    state: 'verified_swarm_desktop',
    approvals: [],
    revision: 1,
    routes: [
      { origin: 'https://node.tailnet.ts.net', authority: 'node.tailnet.ts.net:443', classification: 'verified_swarm_desktop' },
      { origin: 'https://wrong.tailnet.ts.net', authority: 'wrong.tailnet.ts.net:443', classification: 'wrong_target' },
      { origin: 'https://funnel.tailnet.ts.net', authority: 'funnel.tailnet.ts.net:443', classification: 'funnel_enabled' },
    ],
  })
  assert.equal(normalizeTailscaleOriginInput('https://NODE.tailnet.ts.net/'), 'https://node.tailnet.ts.net')
  assert.equal(normalizeTailscaleOriginInput('https://node.tailnet.ts.net/not-root'), null)
  assert.equal(verifiedRouteForInput(status, 'https://node.tailnet.ts.net/')?.classification, 'verified_swarm_desktop')
  assert.equal(verifiedRouteForInput(status, 'https://wrong.tailnet.ts.net'), null)
  assert.equal(verifiedRouteForInput(status, 'https://funnel.tailnet.ts.net'), null)
})

test('desktop and mobile settings wiring includes the Tailscale page', () => {
  assert.match(settingsSource, /id: 'tailscale', label: 'Tailscale'/)
  assert.match(settingsSource, /activeTab === 'tailscale' \? <TailscaleSettingsPage \/>/)
  assert.match(settingsSource, /tabs\.map[\s\S]*<option key=\{tab\.id\} value=\{tab\.id\}>/)
})

test('surface keeps route diagnostics, verified-only approval, remediation, and revocation visible', () => {
  assert.match(pageSource, /route\.classification === 'verified_swarm_desktop'/)
  assert.match(pageSource, /wrong_target/)
  assert.match(pageSource, /funnel_enabled/)
  assert.match(pageSource, /status\.remediation/)
  assert.match(pageSource, /revokeTailscaleOrigin/)
})
