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

test('Desktop V3 refresh startup order and workspace route selection are guarded', async () => {
  const source = await readDesktopAppPage()

  assert.match(
    source,
    /const bootstrap = await bootstrapDesktopV3SidebarMetadataOnly[\s\S]*?const realtimeLease = retainDesktopV3RealtimeController[\s\S]*?await realtimeLease\.ready[\s\S]*?await hydrateDesktopV3InitialSessions/,
  )
  assert.match(
    source,
    /dispatchDesktopV3Cache\(selectSession\(undefined\)\)/,
  )
  assert.doesNotMatch(source, /selectedSessionId=\{routeSessionId \|\| selectedDesktopV3SessionId\}/)
  assert.doesNotMatch(source, /<DesktopV3ChatPane[\s\S]*selectedSessionId=\{selectedDesktopV3SessionId\}/)
})
