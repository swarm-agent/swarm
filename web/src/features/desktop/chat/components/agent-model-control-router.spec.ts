import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('Desktop exposes Router through dedicated flat system-agent settings', () => {
  assert.match(source, /const ROUTER_AGENT_NAME = 'system-router'/)
  assert.match(source, /systemAgents\.router/)
  assert.match(source, /profile\.name === ROUTER_AGENT_NAME \? 'router'/)
  assert.match(source, /saveSystemAgentModelSettings/)
  assert.match(source, /routerProfile[\s\S]{0,900}provider: routerSettings\.provider[\s\S]*model: routerSettings\.model[\s\S]*thinking: routerSettings\.thinking/)
  assert.doesNotMatch(source, /routerProfile[\s\S]{0,900}modelMode|planProvider|autoProvider/)
})
