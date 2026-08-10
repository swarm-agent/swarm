import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./behavior-settings-page.tsx', import.meta.url), 'utf8')
const guardSource = readFileSync(new URL('./plan-context-guard-settings.tsx', import.meta.url), 'utf8')
const swarmModeSource = readFileSync(new URL('./swarm-mode-settings.tsx', import.meta.url), 'utf8')
const settingsPageSource = readFileSync(new URL('../../components/desktop-settings-page.tsx', import.meta.url), 'utf8')

test('Behavior follows Actions in the visible settings hierarchy', () => {
  const behaviorIndex = settingsPageSource.indexOf("id: 'behavior'")
  const accountIndex = settingsPageSource.indexOf("id: 'account'")
  const actionsIndex = settingsPageSource.indexOf("id: 'actions'")
  assert.ok(accountIndex >= 0)
  assert.ok(actionsIndex > accountIndex)
  assert.ok(behaviorIndex > actionsIndex)
  assert.match(settingsPageSource, /activeTab === 'behavior' \? <BehaviorSettingsPage \/>/)
})

test('Behavior owns the Plan context guard controls and canonical save path', () => {
  assert.match(source, /<h2[^>]*>Behavior<\/h2>/)
  assert.match(source, /PlanContextGuardSettingsSection/)
  assert.match(source, /normalizePlanContextGuardSettings\(settingsQuery\.data\)/)
  assert.match(source, /savePlanContextGuardSettings/)
  assert.match(guardSource, /Used-context warning/)
  assert.match(guardSource, /\[50, 60, 70, 75, 80, 85, 90, 95\]/)
  assert.match(guardSource, /Maximum durable handoffs/)
  assert.match(guardSource, /None — finalize immediately/)
  assert.match(guardSource, /type="checkbox"/)
  assert.match(guardSource, /Save guard/)
})

test('Behavior exposes the bounded swarm mode maximum and policy explanation', () => {
  assert.match(source, /SwarmModeSettingsSection/)
  assert.match(source, /normalizeSwarmModeSettings\(settingsQuery\.data\)/)
  assert.match(source, /saveMaxSwarmAgents/)
  assert.match(swarmModeSource, /Maximum agents/)
  assert.match(swarmModeSource, /min=\{MIN_SWARM_AGENTS\}/)
  assert.match(swarmModeSource, /max=\{MAX_SWARM_AGENTS\}/)
  assert.match(swarmModeSource, /lower of this maximum and the current subagent concurrency policy/)
  assert.match(swarmModeSource, /expand themes, then refine each prompt/)
  assert.match(swarmModeSource, /not child sessions/)
  assert.match(swarmModeSource, /only final Designer or Coder agents/)
  assert.match(swarmModeSource, /Saving…/)
  assert.match(swarmModeSource, /role="alert"/)
})
