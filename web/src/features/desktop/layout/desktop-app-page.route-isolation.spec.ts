import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

async function readDesktopAppPage(): Promise<string> {
  return readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
}

async function readNewSessionPane(): Promise<string> {
  return readFile(new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
}

async function readExistingConversationPane(): Promise<string> {
  return readFile(new URL('../chat/components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
}

test('Desktop V3 route panes are keyed by route identity', async () => {
  const source = await readDesktopAppPage()

  assert.match(
    source,
    /<DesktopV3ExistingConversationPane\s+key=\{`existing:\$\{routeSessionId\}`\}\s+sessionId=\{routeSessionId\}/,
  )
  assert.match(
    source,
    /<DesktopV3NewSessionPane\s+key=\{`new:\$\{topWorkspace\.path\}:\$\{newSessionEpoch\}`\}\s+workspace=\{topWorkspace\}\s+workspaceAuthority=\{activeWorkspaceAuthority\}/,
  )
})

test('Desktop V3 pane completions stay isolated from replacement route instances', async () => {
  const newPane = await readNewSessionPane()
  const existingPane = await readExistingConversationPane()

  // The routed pane owns only a subscribed local controller. Unmounting removes
  // the subscription, so its resolved-state effect cannot call into a replacement
  // route instance and does not need the legacy async mountedRef choreography.
  assert.match(
    newPane,
    /new DesktopV3RoutedNewSessionController\(async \(request\) => \{\s*const result = await postDesktopV3RoutedSessionStart\(request\)[\s\S]*?return result\s*\}\)/,
  )
  assert.match(newPane, /useEffect\(\(\) => controller\.subscribe\(setRoutedState\), \[controller\]\)/)
  assert.match(newPane, /if \(routedState\.phase !== 'resolved'\) return/)
  assert.match(newPane, /resolvedCallbackRef\.current\?\.\(routedState\.result\)/)
  assert.doesNotMatch(newPane, /const mountedRef = useRef\(true\)/)
  assert.doesNotMatch(newPane, /clearDesktopV3NewSessionOperation/)
  assert.doesNotMatch(newPane, /navigateToSession/)

  // Existing-conversation sends remain component-owned async work and retain
  // their explicit mounted guard.
  assert.match(existingPane, /const mountedRef = useRef\(true\)/)
  assert.match(existingPane, /return \(\) => \{\s*mountedRef\.current = false;\s*\}/)
  assert.match(
    existingPane,
    /clearDesktopV3ExistingMessageOperation\(\s*input\.sessionId,\s*input\.operation\.operationId,?\s*\);\s*if \(!input\.mountedRef\.current\) return;\s*input\.setOperation\(null\);\s*input\.setDraft\(["']{2}\);/s,
  )
  assert.match(existingPane, /catch \(error\) \{\s*if \(mountedRef\.current\) \{\s*setSendError/s)
  assert.match(existingPane, /finally \{\s*if \(mountedRef\.current\) \{\s*setSending\(false\)/s)
})

test('Desktop V3 routed drafts and durable conversations keep separate ownership boundaries', async () => {
  const appPage = await readDesktopAppPage()
  const newPane = await readNewSessionPane()
  const existingPane = await readExistingConversationPane()

  assert.match(appPage, /routeSessionId \? \(\s*<DesktopV3ExistingConversationPane/s)
  assert.match(appPage, /topWorkspace\?\.path && activeWorkspaceAuthority \? \(\s*<DesktopV3NewSessionPane/s)
  assert.doesNotMatch(appPage, /function DesktopV3ChatPane/)
  assert.doesNotMatch(appPage, /commitDesktopV3Message/)
  assert.doesNotMatch(appPage, /postDesktopV3AppendMessage/)
  assert.doesNotMatch(appPage, /postDesktopV3CreateSession/)

  // The new pane can request one routed start and report only a validated
  // resolved result. It does not activate cache, realtime, sidebar, or URL state.
  assert.match(newPane, /DesktopV3RoutedNewSessionController/)
  assert.match(newPane, /postDesktopV3RoutedSessionStart/)
  assert.match(newPane, /onRoutedSessionResolved/)
  assert.doesNotMatch(newPane, /existing-session-flow/)
  assert.doesNotMatch(newPane, /dispatchDesktopV3Cache/)
  assert.doesNotMatch(newPane, /activateDesktopV3RoutedSession/)
  assert.doesNotMatch(newPane, /selectAndHydrateDesktopV3Session/)

  // App ownership gates canonical activation against both workspace replacement
  // and activation generation before it changes selection or navigation.
  assert.match(appPage, /routedActivationWorkspaceRef\.current === expectedWorkspacePath/)
  assert.match(appPage, /activationGeneration === routedActivationGenerationRef\.current/)
  assert.match(appPage, /activateDesktopV3RoutedSession\(/)
  assert.match(appPage, /selectAndHydrateDesktopV3Session/)

  assert.match(existingPane, /from ["']\.\.\/\.\.\/session-v3\/existing-session-flow["']/)
  assert.doesNotMatch(existingPane, /DesktopV3RoutedNewSessionController/)
})

test('Desktop V3 ordinary route redirects after a hydrated system sidechat is classified', async () => {
  const source = await readDesktopAppPage()

  assert.match(source, /isDesktopV3NavigationHiddenRecord\(state\.sessionsById\[routeSessionId\]\)/)
  assert.match(source, /if \(!routeSessionNavigationHidden \|\| !routeWorkspaceSlug\) return/)
  assert.match(source, /navigate\(\{ to: '\/\$workspaceSlug', params: \{ workspaceSlug: routeWorkspaceSlug \} \}\)/)
  assert.match(source, /const routeSessionUnavailable = routeSessionNavigationHidden/)
})

test('Desktop V3 route selection stays isolated from runtime ownership', async () => {
  const source = await readDesktopAppPage()

  assert.doesNotMatch(source, /bootstrapDesktopV3SidebarMetadataOnly/)
  assert.doesNotMatch(source, /retainDesktopV3RealtimeController/)
  assert.match(
    source,
    /dispatchDesktopV3Cache\(selectSession\(undefined\)\)/,
  )
  assert.match(source, /requireDesktopV3RealtimeControllerReady/)
  assert.doesNotMatch(source, /selectedSessionId=\{routeSessionId \|\| selectedDesktopV3SessionId\}/)
  assert.doesNotMatch(source, /<DesktopV3ChatPane[\s\S]*selectedSessionId=\{selectedDesktopV3SessionId\}/)
})

test('Desktop V3 sidebar active timer replaces the relative-time metadata slot', async () => {
  const source = await readDesktopAppPage()

  assert.match(
    source,
    /sessionHasCanonicalActiveRun\(session\)[\s\S]*\? sessionTimerLabel\(session, now\)[\s\S]*: relativeActivityLabel/,
  )
  assert.ok(source.includes('<div className="mt-0.5 flex min-w-0 items-center justify-between gap-2 text-[10px] leading-4 text-[var(--app-text-subtle)]">'))
  assert.ok(source.includes('{rowTimerLabel ? <span>{rowTimerLabel}</span> : null}'))
  assert.doesNotMatch(source, /grid-cols-\[minmax\(0,1fr\)_5\.5rem\]/)
  assert.doesNotMatch(source, /ml-auto w-\[5\.5rem\] shrink-0 truncate text-right tabular-nums/)
})

test('Desktop V3 sidebar action menu is isolated from inactive row selection', async () => {
  const source = await readDesktopAppPage()

  assert.match(source, /onPointerDownCapture=\{\(event\) => \{\s*event\.preventDefault\(\)\s*event\.stopPropagation\(\)\s*\}\}/)
  assert.match(source, /onClick=\{\(event\) => \{\s*event\.preventDefault\(\)\s*event\.stopPropagation\(\)\s*setActionsOpen/s)
  assert.match(source, /actionsOpen \? 'z-30' : null/)
})
