import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

// Requirement: launcher wiring must use the catalog-first async boundary and a
// stable refresh callback so theme/browser updates cannot retrigger expensive work.
// These source assertions guard hook wiring only; load-launcher-catalog-first.spec
// exercises completion, failure, and stale-result behavior at the async boundary.
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
  assert.match(source, /if \(isCurrent\(\) && browseDuringRefresh\) \{\s*if \(roots\.length > 0\)/s)
  assert.match(source, /loadLauncherCatalogFirst\(\{/)
  assert.match(source, /workspaceOverviewQueryOptions\(roots, 25, false\)/)
  assert.match(source, /discover: \(\) => discoverWorkspaces\(1000, roots\)/)
  assert.match(source, /\}, \[browseDuringRefresh, browsePath, queryClient\]\)/)
  assert.match(source, /useState\(autoRefresh && !cachedOverview\)/)
})

// Requirement: pending definitions must not create background inventory polling.
// This source guard checks hook wiring only, not runtime freshness or security.
test('launcher uses cache updates rather than definition polling', () => {
  assert.doesNotMatch(source, /setInterval|listWorkspaces\(/)
  assert.match(source, /scheduleCacheSync\(syncFromOverviewCache\)/)
})

test('launcher query-cache subscription ignores observer churn and defers React state sync', () => {
  assert.match(source, /if \(event\.type !== 'updated' \|\| event\.action\.type !== 'success'\) \{\s*return\s*\}/s)
  assert.match(source, /scheduleCacheSync\(syncFromOverviewCache\)/)
  assert.match(source, /scheduleCacheSync\(syncFromUISettingsCache\)/)
  assert.doesNotMatch(source, /const queryKey = event\?\.query\?\.queryKey/)
})
