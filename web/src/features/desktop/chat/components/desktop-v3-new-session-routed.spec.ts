import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const paneURL = new URL('./desktop-v3-new-session-pane.tsx', import.meta.url)
const composerURL = new URL('./desktop-v3-agentic-composer.tsx', import.meta.url)

test('new Desktop chat uses only the routed controller and endpoint before activation', async () => {
  const source = await readFile(paneURL, 'utf8')

  assert.match(source, /new DesktopV3RoutedNewSessionController\(postDesktopV3RoutedSessionStart\)/)
  assert.match(source, /controller\.submit\(\{/)
  assert.match(source, /controller\.retry\(\)/)
  assert.match(source, /onRoutedSessionResolved\?: \(result: DesktopV3RoutedStartResult\)/)
  assert.match(source, /resolvedCallbackRef\.current\?\.\(routedState\.result\)/)

  assert.doesNotMatch(source, /\bcreateDesktopV3NewSessionOperation\b/)
  assert.doesNotMatch(source, /\bstartNewDesktopV3Session\b/)
  assert.doesNotMatch(source, /\bappendFirstDesktopV3Message\b/)
  assert.doesNotMatch(source, /dispatchDesktopV3Cache|retainDesktopV3RealtimeController|bootstrapDesktopV3SidebarMetadataOnly/)
  assert.doesNotMatch(source, /useNavigate|navigate\(/)
})

test('routed pending and failure stay local and retry the persisted controller identity', async () => {
  const source = await readFile(paneURL, 'utf8')

  assert.match(source, /<DesktopV3RoutedPendingShell[\s\S]*state=\{pendingState\}[\s\S]*pendingPrompt=\{routedState\.prompt\}/)
  assert.match(source, /routedState\.phase === 'failed'[\s\S]*controller\.retry\(\)/)
  assert.match(source, /routedState\.phase === 'draft'/)
  assert.match(source, /showComposer \? \([\s\S]*<DesktopV3AgenticComposer/)
  assert.match(source, /routedNewSession/)
})

test('routed new-chat composer exposes no manual setup, model, or Plan mode authority', async () => {
  const source = await readFile(composerURL, 'utf8')

  assert.match(source, /routedNewSession\?: boolean/)
  assert.match(source, /!routedNewSession && showModePicker/)
  assert.match(source, /!routedNewSession && !resolvedSessionControls \? <AgentModelControl/)
  assert.match(source, /onAttach=\{routedNewSession \? undefined/)
  assert.match(source, /if \(!routedNewSession\) openAgentSetup\(currentAgent\)/)
})
