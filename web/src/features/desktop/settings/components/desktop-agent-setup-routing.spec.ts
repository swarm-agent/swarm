import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const settingsSource = readFileSync(new URL('./desktop-settings-page.tsx', import.meta.url), 'utf8')
const routerSource = readFileSync(new URL('../../../../app/router.tsx', import.meta.url), 'utf8')
const layoutSource = readFileSync(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')

assert.doesNotMatch(settingsSource, /AgentsSettingsPage|id: 'agents'/, 'standalone Agents settings page must be removed')
assert.match(settingsSource, /search\.tab === 'agents'[\s\S]*agentSetup: '1'/, 'legacy agents settings links must redirect to Agent Setup')
assert.match(routerSource, /path: '\/agents'[\s\S]*component: AgentSetupRedirect/, '/agents must route through Agent Setup redirect')
assert.match(layoutSource, /agentSettingsOpenSignal=\{agentSettingsOpenSignal\}/, 'Desktop routes must open the Agent Setup modal')
