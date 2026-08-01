import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('Desktop exposes Router through the existing single-model system-agent settings control', () => {
  assert.match(source, /const ROUTER_AGENT_NAME = 'system-router'/)
  assert.match(source, /normalizeRouterAgentSettings\(uiSettings\)/)
  assert.match(source, /profile\.name === ROUTER_AGENT_NAME \? 'router'/)
  assert.match(source, /routerProfile[\s\S]{0,900}modelMode: 'single'/)
})
