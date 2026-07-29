import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('./desktop-onboarding-gate.tsx', import.meta.url), 'utf8')

test('onboarding prefers device-code auth and retains explicit OAuth fallbacks', () => {
  assert.match(source, /handleStartOAuth\('device'\)/)
  assert.match(source, /Sign in with device code/)
  assert.match(source, /Preferred · remote-friendly/)
  assert.match(source, /Local browser fallback/)
  assert.match(source, /Manual callback fallback/)
  assert.match(source, /<CodexDeviceCode session=\{oauthSession\}/)
})

test('device start failures remain visible instead of silently selecting a fallback', () => {
  assert.match(source, /setError\(err instanceof Error \? err\.message : 'Failed to start Codex sign-in'\)/)
  assert.doesNotMatch(source, /catch[\s\S]{0,200}handleStartOAuth\('browser'\)/)
})
