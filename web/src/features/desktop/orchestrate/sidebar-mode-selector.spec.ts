import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// Purpose: both production shells must use the shared selector with their own
// mode authority and existing review/navigation inputs. This narrow wiring test
// prevents a second divergent selector; the browser test proves actual behavior.
test('both sidebar owners delegate mode rendering without losing navigation or reviews', () => {
  const swarm = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const chat = readFileSync(new URL('../layout/desktop-app-page.tsx', import.meta.url), 'utf8')
  assert.match(swarm, /<SidebarModeSelector mode="swarm" workspaceSlug=\{workspaceSlug\} pendingReviews=\{pendingReviews.length\}/)
  assert.match(swarm, /onNavigateChat=\{onNavigateHome\} onNavigate=\{\(\) => responsiveLayout.setNavigationOpen\(false\)\}/)
  assert.ok(swarm.indexOf('<SidebarModeSelector') < swarm.indexOf('<nav aria-label="Swarm destinations"'))
  assert.match(chat, /<SidebarModeSelector mode=\{isOrchestrateRoute \? 'swarm' : 'chat'\}/)
  assert.match(chat, /workspaceSlug=\{topWorkspaceSlug \|\| undefined\}/)
  assert.match(chat, /pendingReviews=\{pendingWorkerCount\} onNavigate=\{\(\) => setMobileSidebarOpen\(false\)\}/)
  for (const source of [swarm, chat]) assert.equal(source.split('<SidebarModeSelector').length - 1, 1)
})
