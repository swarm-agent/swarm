import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./use-workspace-launcher.ts', import.meta.url), 'utf8')
const desktopSource = await readFile(new URL('../../../desktop/layout/desktop-app-page.tsx', import.meta.url), 'utf8')

test('desktop route opts out of launcher auto refresh because it owns workspace overview query', () => {
  assert.match(desktopSource, /useWorkspaceLauncher\(\{ applyDocumentTheme: false, autoRefresh: false, browseDuringRefresh: false \}\)/)
  assert.match(desktopSource, /useQuery\(\{\s*\.\.\.workspaceOverviewQueryOptions\(\[\], 25\),/s)
  assert.match(desktopSource, /const workspacesLoading = launcherWorkspacesLoading \|\| overviewQuery\.isPending/)
})

test('launcher initial refresh can avoid duplicate overview and browse waterfalls', () => {
  assert.match(source, /autoRefresh = options\.autoRefresh \?\? true/)
  assert.match(source, /browseDuringRefresh = options\.browseDuringRefresh \?\? true/)
  assert.match(source, /if \(!autoRefresh\) \{\s*setLoading\(false\)\s*return\s*\}/s)
  assert.match(source, /if \(browseDuringRefresh\) \{\s*if \(roots\.length > 0\)/s)
  assert.match(source, /const discoveryPromise = discoverWorkspaces\(1000, roots\)/)
  assert.ok(source.indexOf('setLoading(false)') < source.indexOf('const discoveryResult = await discoveryPromise'))
})

test('launcher polls backend state only while a workspace definition is pending', () => {
  assert.match(source, /workspaces\.some\(\(workspace\) => workspace\.definitionStatus === 'pending'\)/)
  assert.match(source, /window\.setInterval\(\(\) => \{\s*void listWorkspaces\(\)/s)
  assert.match(source, /definitionStatus: updated\.definitionStatus/)
  assert.match(source, /\}, 2_000\)/)
  assert.match(source, /if \(!hasPendingWorkspaceDefinition\) \{\s*return\s*\}/s)
})

test('launcher query-cache subscription ignores observer churn and defers React state sync', () => {
  assert.match(source, /if \(event\.type !== 'updated'\) \{\s*return\s*\}/s)
  assert.match(source, /scheduleCacheSync\(syncFromOverviewCache\)/)
  assert.match(source, /scheduleCacheSync\(syncFromUISettingsCache\)/)
  assert.doesNotMatch(source, /const queryKey = event\?\.query\?\.queryKey/)
})
