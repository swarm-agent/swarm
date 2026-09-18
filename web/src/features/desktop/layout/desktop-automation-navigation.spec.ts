import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

// Requirement: automations are disabled for launch.
// Sidebar removes Automations/Workers link and icon; routes redirect safely.
// Authorities: router.tsx registration and DesktopAppPage route/sidebar rendering.
const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
const router = await readFile(new URL('../../../app/router.tsx', import.meta.url), 'utf8')
const chatPane = await readFile(new URL('../chat/components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

test('workers and automations routes redirect safely to workspace root', () => {
  const registration = router.slice(router.indexOf('const workspaceWorkersRoute ='), router.indexOf('const workspaceTaskRoute ='))
  assert.match(registration, /getParentRoute: \(\) => conversationRoute/)
  assert.match(registration, /path: '\/\$workspaceSlug\/workers'/)
  assert.match(registration, /path: '\/\$workspaceSlug\/automations'/)
  assert.match(registration, /redirect\({\s*to: '\/\$workspaceSlug'/)
  assert.doesNotMatch(registration, /component: AutomationToolPage/)
  assert.match(router, /conversationRoute\.addChildren\(\[workspaceRoute, workspaceSessionRoute, workspaceWorkersRoute,[\s\S]*?workspaceAutomationsRoute\]\)/)
  assert.match(router, /path: '\/\$workspaceSlug\/workers\/\$workerId'/)
  assert.match(router, /path: '\/\$workspaceSlug\/worker\/\$workerId'/)
})

test('shared sidebar removes Automations/Workers link and icon', () => {
  const sidebar = source.slice(source.indexOf('const sidebarContent = ('), source.indexOf('const sidebarContent = (') + 22000)
  assert.doesNotMatch(sidebar, /<AutomationSidebar/)
  assert.doesNotMatch(sidebar, /aria-label="Open Workers"/)
  assert.doesNotMatch(sidebar, /AutomationV2SidebarSummaryIndicator/)
  const info = sidebar.indexOf('aria-label="Edit swarm name"')
  const studio = sidebar.indexOf('aria-label="Open Studio"')
  const environments = sidebar.indexOf('aria-label="Open Environments"')
  const quick = sidebar.indexOf('aria-label="Open Desktop quick actions"')
  assert.ok(info >= 0 && info < studio && studio < environments && environments < quick)
  assert.match(source, /onClick=\{\(\) => setMobileSidebarOpen\(true\)\} aria-label="Open sidebar"/)
  assert.match(source, /<div className="min-h-0 flex-1">\{sidebarContent\}<\/div>/)
})

test('automation content cannot restore a second workspace navigation shell', async () => {
  const content = await readFile(new URL('../tools/automations/automation-workspace.tsx', import.meta.url), 'utf8')
  assert.doesNotMatch(content, /Back to workspace|<nav aria-label="Automations"|min-h-dvh/)
  assert.match(content, /aria-label="Worker controls"/)
})

test('session rows and conversation pane do not render automation badges or worker banner', () => {
  assert.doesNotMatch(source, /aria-label="Open Workers"/)
  assert.doesNotMatch(source, /<AutomationV2SidebarMetadata/)
  assert.doesNotMatch(source, /<AutomationProgressView/)
  assert.doesNotMatch(chatPane, /<WorkerSessionBanner/)
  assert.doesNotMatch(chatPane, /<AutomationSessionPanel/)
  assert.doesNotMatch(chatPane, /<AutomationV2Detail/)
})
