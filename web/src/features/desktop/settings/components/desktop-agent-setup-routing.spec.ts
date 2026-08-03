import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const settingsSource = readFileSync(new URL('./desktop-settings-page.tsx', import.meta.url), 'utf8')
const routerSource = readFileSync(new URL('../../../../app/router.tsx', import.meta.url), 'utf8')
const layoutSource = readFileSync(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')
const newSessionSource = readFileSync(new URL('../../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
const controlSource = readFileSync(new URL('../../chat/components/agent-model-control.tsx', import.meta.url), 'utf8')

assert.doesNotMatch(settingsSource, /AgentsSettingsPage|id: 'agents'/, 'standalone Agents settings page must be removed')
assert.match(settingsSource, /search\.tab === 'agents'[\s\S]*agentSetup: '1'/, 'legacy agents settings links must redirect to Agent Setup')
assert.match(routerSource, /path: '\/agents'[\s\S]*component: AgentSetupRedirect/, '/agents must route through Agent Setup redirect')
assert.match(layoutSource, /DesktopV3ExistingConversationPane[\s\S]*agentSettingsOpenSignal=\{agentSettingsOpenSignal\}/, 'existing Desktop conversations must open Agent Setup')
assert.match(layoutSource, /DesktopV3NewSessionPane[\s\S]*agentSettingsOpenSignal=\{agentSettingsOpenSignal\}/, 'pending Desktop router creators must open Agent Setup')
assert.match(newSessionSource, /agentSettingsOpenSignal=\{agentSettingsOpenSignal\}[\s\S]*agentSettingsInitialAgent=\{agentSettingsInitialAgent\}/, 'new-session composer must forward Agent Setup route state')
assert.doesNotMatch(controlSource, /provider \|\| 'Unassigned'/, 'Agent Setup cards must not show provider badges')
assert.match(controlSource, /agentModelSettingsQuery\.data\?\.swarm\.action[\s\S]*const model = assignment\?\.model\.trim\(\)[\s\S]*model \|\| 'Model not configured'/, 'Agent Setup cards must show canonical model details')
assert.match(controlSource, /const enabledDetails = \[thinking !== 'off' \? thinking : '', serviceTier\]\.filter\(Boolean\)[\s\S]*enabledDetails\.join\(' · '\)/, 'Agent Setup cards must show enabled thinking and priority values as an unlabeled compact line')
