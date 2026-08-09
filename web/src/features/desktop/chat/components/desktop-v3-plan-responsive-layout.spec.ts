import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('plan execution adapts its desktop sidebar and shifts above the composer below 1300px', async () => {
  const paneSource = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  const sidebarSource = await readFile(new URL('./desktop-plan-execution-sidebar.tsx', import.meta.url), 'utf8')
  const planAgentSource = await readFile(new URL('./desktop-plan-agent-sidecar.tsx', import.meta.url), 'utf8')

  assert.match(paneSource, /min-\[1300px\]:grid-cols-\[minmax\(0,1fr\)_360px\]/)
  assert.match(paneSource, /min-\[1300px\]:grid-cols-\[minmax\(0,1fr\)_280px\]/)
  assert.match(paneSource, /min-\[1300px\]:grid-cols-\[minmax\(0,1fr\)_56px\]/)
  assert.match(paneSource, /desktop-sidebar-display|planSidebarDisplayMode|data-plan-sidebar-mode/)
  assert.match(sidebarSource, /min-\[1300px\]:flex/)
  assert.match(sidebarSource, /displayMode|data-plan-thin-rail|w-\[280px\]|w-\[56px\]/)
  assert.match(planAgentSource, /displayMode|w-\[280px\]|w-\[56px\]/)
  assert.match(paneSource, /data-testid="desktop-plan-execution-composer-region"/)
  assert.match(paneSource, /min-\[1300px\]:hidden"\s+data-testid="desktop-plan-execution-composer-region"/)

  const compactRegionIndex = paneSource.indexOf('data-testid="desktop-plan-execution-composer-region"')
  const composerIndex = paneSource.indexOf('<DesktopV3AgenticComposer', compactRegionIndex)
  assert.ok(compactRegionIndex >= 0 && composerIndex > compactRegionIndex, 'compact plan surface should render immediately before the composer')

  const compactRegion = paneSource.slice(compactRegionIndex, composerIndex)
  assert.match(compactRegion, /<DesktopPlanExecutionSidebar/)
  assert.match(compactRegion, /embedded/)
  assert.match(compactRegion, /busyAction=\{planExecutionBusyAction\}/)
  assert.match(compactRegion, /onAction=\{stablePlanExecutionAction\}/)
  assert.match(compactRegion, /belowActions=\{planSidebarBelowActions\}/)
  assert.doesNotMatch(compactRegion, />\s*Automatic\s*</)
  assert.match(compactRegion, /policyMode !== "automatic"/)
  assert.match(compactRegion, />\s*Review each\s*</)
})
