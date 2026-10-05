// Purpose: deployment navigation must preserve exact workspace/deployment IDs
// and use SPA navigation on ordinary clicks. The click controller and shared
// route validator prove the contract without loading the application/browser.
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { deploymentLocation, navigateDeploymentClick } from './deployment-navigation'
import { validateEnvironmentsSearch } from './environments-search'

test('deployment click prevents reload and preserves identity; modified clicks retain native behavior', () => {
  let prevented = 0, navigated = 0
  const event = { button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, preventDefault() { prevented++ } }
  navigateDeploymentClick(event, () => { navigated++ })
  assert.equal(prevented, 1)
  assert.equal(navigated, 1)
  navigateDeploymentClick({ ...event, ctrlKey: true }, () => { navigated++ })
  assert.equal(prevented, 1)
  assert.equal(navigated, 1)
  assert.deepEqual(deploymentLocation('workspace', 'deployment'), { to: '/environments', search: { tab: 'deployments', workspace_id: 'workspace', deployment_id: 'deployment' } })
  assert.equal(validateEnvironmentsSearch({ workspace_id: ' bad', deployment_id: '\u0000' }).workspace_id, undefined)
  // Registration guard supplements (not substitutes for) behavioral assertions.
  const source = readFileSync(new URL('../../../../app/router.tsx', import.meta.url), 'utf8')
  for (const name of ['environmentsRoute', 'workspaceEnvironmentsRoute']) {
    const block = source.slice(source.indexOf(`const ${name} = createRoute(`)).split('\n})')[0]
    assert.match(block, /validateSearch: validateEnvironmentsSearch/)
  }
})
