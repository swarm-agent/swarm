import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

// Requirement: automation navigation belongs to the existing Desktop shell, with
// the info row first and Automations immediately after Studio on desktop/mobile.
// Regression: a root-level automation route or prepended sidebar replaces that
// shell, and generic session matching can hydrate "automations" as a session.
// Authorities: router.tsx registration and DesktopAppPage route/sidebar rendering.
// This narrow structural test proves composition wiring only, not browser layout,
// runtime transitions, authentication, or visual quality.
const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
const router = await readFile(new URL('../../../app/router.tsx', import.meta.url), 'utf8')

test('automations is nested under the retained Desktop owner and rendered through its outlet', () => {
  const registration = router.slice(router.indexOf('const workspaceAutomationsRoute ='), router.indexOf('const workspaceTaskRoute ='))
  assert.match(registration, /getParentRoute: \(\) => conversationRoute/)
  assert.match(registration, /component: AutomationToolPage/)
  assert.match(router, /conversationRoute\.addChildren\(\[workspaceRoute, workspaceSessionRoute, workspaceAutomationsRoute\]\)/)
  assert.match(source, /const routeSessionId = mobileCreationPage \|\| workspaceAutomationsMatch\s*\? ''/)
  assert.match(source, /to: workspaceAutomationsMatch \? '\/\$workspaceSlug\/automations' : '\/\$workspaceSlug'/)
  assert.match(source, /workspaceAutomationsMatch \? \([\s\S]*?<Outlet \/>/)
})

test('shared sidebar keeps the info row first and Automations directly below Studio', () => {
  const sidebar = source.slice(source.indexOf('const sidebarContent = ('), source.indexOf('const sidebarContent = (') + 22000)
  assert.doesNotMatch(sidebar, /<AutomationSidebar/)
  const info = sidebar.indexOf('aria-label="Edit swarm name"')
  const studio = sidebar.indexOf('aria-label="Open Studio"')
  const automation = sidebar.indexOf('aria-label="Open Automations"')
  const quick = sidebar.indexOf('aria-label="Open Desktop quick actions"')
  assert.ok(info >= 0 && info < studio && studio < automation && automation < quick)
  const entry = sidebar.slice(sidebar.lastIndexOf('<button', automation), sidebar.indexOf('</button>', automation))
  assert.match(entry, /setMobileSidebarOpen\(false\)/)
  assert.match(entry, /disabled=\{!topWorkspaceSlug\}/)
  assert.match(entry, /workspaceSlug: topWorkspaceSlug/)
  assert.match(source, /onClick=\{\(\) => setMobileSidebarOpen\(true\)\} aria-label="Open sidebar"/)
  assert.match(source, /<div className="min-h-0 flex-1">\{sidebarContent\}<\/div>/)
})

test('automation content cannot restore a second workspace navigation shell', async () => {
  const content = await readFile(new URL('../tools/automations/automation-workspace.tsx', import.meta.url), 'utf8')
  assert.doesNotMatch(content, /Back to workspace|<nav aria-label="Automations"|min-h-dvh/)
  assert.match(content, /aria-label="Automation controls"/)
})
