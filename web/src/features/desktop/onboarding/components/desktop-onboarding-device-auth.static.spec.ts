import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('./desktop-onboarding-gate.tsx', import.meta.url), 'utf8')

test('onboarding recommends Codex setup by access locality and retains explicit OAuth fallbacks', () => {
  assert.match(source, /recommendedCodexSetup = codexSetupRecommendation\(\)/)
  assert.match(source, /handleStartOAuth\('device'\)/)
  assert.match(source, /Device Code/)
  assert.match(source, /Recommended for remote setup/)
  assert.match(source, /Local Setup/)
  assert.match(source, /Recommended on this device/)
  assert.match(source, /Manual callback fallback/)
  assert.match(source, /<CodexDeviceCode session=\{oauthSession\}/)
})

test('device start failures remain visible instead of silently selecting a fallback', () => {
  assert.match(source, /setError\(err instanceof Error \? err\.message : 'Failed to start Codex sign-in'\)/)
  assert.doesNotMatch(source, /catch[\s\S]{0,200}handleStartOAuth\('browser'\)/)
})
