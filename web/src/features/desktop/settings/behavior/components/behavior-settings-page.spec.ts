import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./behavior-settings-page.tsx', import.meta.url), 'utf8')
const guardSource = readFileSync(new URL('./plan-context-guard-settings.tsx', import.meta.url), 'utf8')
const taskSource = readFileSync(new URL('./task-context-settings.tsx', import.meta.url), 'utf8')
const artifactLibrarySource = readFileSync(new URL('./artifact-library-settings.tsx', import.meta.url), 'utf8')
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

test('Behavior owns the artifact library control and saves only on submit', () => {
  assert.match(source, /ArtifactLibrarySettingsSection/)
  assert.match(source, /normalizeArtifactLibrarySettings\(settingsQuery\.data\)/)
  assert.match(source, /saveArtifactLibrarySettings/)
  assert.match(artifactLibrarySource, /Show in folder/)
  assert.match(artifactLibrarySource, /Leave blank to use Swarm’s portable system default/)
  assert.match(artifactLibrarySource, /type="submit"/)
})

test('Behavior owns the Task context compaction controls and canonical save path', () => {
  assert.match(source, /TaskContextSettingsSection/)
  assert.match(source, /normalizeTaskContextSettings\(settingsQuery\.data\)/)
  assert.match(source, /saveTaskContextSettings/)
  assert.match(taskSource, /Task context compaction/)
  assert.match(taskSource, /Maximum compactions per Task/)
  assert.match(taskSource, /same durable session/)
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
