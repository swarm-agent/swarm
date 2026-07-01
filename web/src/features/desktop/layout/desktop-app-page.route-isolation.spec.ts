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
    /<DesktopV3NewSessionPane\s+key=\{`new:\$\{routeWorkspace\.path\}`\}\s+workspace=\{routeWorkspace\}/,
  )
})

test('Desktop V3 pane completions do not mutate replacement route instances after unmount', async () => {
  const newPane = await readNewSessionPane()
  const existingPane = await readExistingConversationPane()

  assert.match(newPane, /const mountedRef = useRef\(true\)/)
  assert.match(newPane, /return \(\) => \{\s*mountedRef\.current = false\s*\}/)
  assert.match(
    newPane,
    /clearDesktopV3NewSessionOperation\(input\.workspacePath, input\.operation\.operationId\)\s*if \(!input\.mountedRef\.current\) return\s*input\.setOperation\(null\)\s*input\.navigateToSession\(input\.operation\.sessionId\)/s,
  )
  assert.match(newPane, /catch \(error\) \{\s*if \(mountedRef\.current\) \{\s*setStartError/s)
  assert.match(newPane, /finally \{\s*if \(mountedRef\.current\) \{\s*setStarting\(false\)/s)

  assert.match(existingPane, /const mountedRef = useRef\(true\)/)
  assert.match(existingPane, /return \(\) => \{\s*mountedRef\.current = false\s*\}/)
  assert.match(
    existingPane,
    /clearDesktopV3ExistingMessageOperation\(input\.sessionId, input\.operation\.operationId\)\s*if \(!input\.mountedRef\.current\) return\s*input\.setOperation\(null\)\s*input\.setDraft\(''\)/s,
  )
  assert.match(existingPane, /catch \(error\) \{\s*if \(mountedRef\.current\) \{\s*setSendError/s)
  assert.match(existingPane, /finally \{\s*if \(mountedRef\.current\) \{\s*setSending\(false\)/s)
})

test('Desktop V3 route split keeps Path A and Path B source boundaries separate', async () => {
  const appPage = await readDesktopAppPage()
  const newPane = await readNewSessionPane()
  const existingPane = await readExistingConversationPane()

  assert.match(appPage, /routeSessionId \? \(\s*<DesktopV3ExistingConversationPane/s)
  assert.match(appPage, /routeWorkspace\?\.path \? \(\s*<DesktopV3NewSessionPane/s)
  assert.doesNotMatch(appPage, /function DesktopV3ChatPane/)
  assert.doesNotMatch(appPage, /commitDesktopV3Message/)
  assert.doesNotMatch(appPage, /postDesktopV3AppendMessage/)
  assert.doesNotMatch(appPage, /postDesktopV3CreateSession/)

  assert.match(newPane, /from '\.\.\/\.\.\/session-v3\/new-session-flow'/)
  assert.doesNotMatch(newPane, /existing-session-flow/)
  assert.match(existingPane, /from '\.\.\/\.\.\/session-v3\/existing-session-flow'/)
  assert.doesNotMatch(existingPane, /new-session-flow/)
})

test('Desktop V3 route selection stays isolated from runtime ownership', async () => {
  const source = await readDesktopAppPage()

  assert.doesNotMatch(source, /bootstrapDesktopV3SidebarMetadataOnly/)
  assert.doesNotMatch(source, /retainDesktopV3RealtimeController/)
  assert.match(
    source,
    /dispatchDesktopV3Cache\(selectSession\(undefined\)\)/,
  )
  assert.match(source, /requireDesktopV3RealtimeControllerReady\(\)/)
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
