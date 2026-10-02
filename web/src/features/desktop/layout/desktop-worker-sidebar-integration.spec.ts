import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

// Requirement: reviews live directly beneath the existing Workers navigation
// button, not in an additional session group; the authoring chat has an
// actionable card and preserves ordinary modal permission handling.
// Threat: a review hidden in a collapsed session group or stripped from chat.
// Layout wiring is the narrowest layer; selector/mutation have focused tests.
test('Workers navigation owns pending control while authoring chat owns its review card', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const navigation = source.slice(source.indexOf('aria-label="Open Workers"'), source.indexOf('aria-label="Open Environments"'))
  assert.match(navigation, /<PendingWorkerSidebarReviews onOpenChat={handleSelectSession} \/>/)
  const group = source.slice(source.indexOf('function renderSidebarSessionGroups('), source.indexOf('export function DesktopAppPage()'))
  assert.doesNotMatch(group, /PendingWorkerSidebarReviews/)
  const pane = await readFile(new URL('../chat/components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  assert.match(pane, /pendingWorkerPermissions\.map\(permission => \(/)
  assert.match(pane, /<DesktopPendingWorkerAlert/)
  assert.match(pane, /<DesktopAcceptedWorkerCard/)
  assert.match(pane, /!isAutomationPermission\(permission\)/)
})

// Requirement: DesktopAppPage must not drive the worker sidebar with a recurring
// clock. Regression: the one-second parent timer rebuilds every sidebar subtree.
// This source-level wiring guard complements the runtime's behavioral event tests;
// it does not claim browser-level rendering or transport verification.
test('worker sidebar has no recurring parent clock and retains V3 event repair', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /setInterval\s*\(/)
  assert.doesNotMatch(source, /setSidebarNow/)
  const realtime = await readFile(new URL('../realtime/v3-realtime-controller.ts', import.meta.url), 'utf8')
  assert.match(realtime, /desktopWorkers\.acceptFrame\(frame\)/)
  assert.match(realtime, /desktopWorkers\.invalidate\(\)/)
})
