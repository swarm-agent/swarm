// Purpose: task browser access must never turn deployment existence into a URL
// grant. verifiedBrowserURL is the narrow final boundary before window.open;
// test literal loopback, mapped ports and negative credential/scheme cases.
import test from 'node:test'
import assert from 'node:assert/strict'
import { verifiedBrowserURL } from './task-browser-access'
import { validateEnvironmentsSearch } from '../pages/environments-search'

const endpoint = { id: 'web', name: 'Web', container_port: 3000, host_port: 49152, ready: true }
test('browser URLs require ready literal loopback and the actual mapped port', () => {
  for (const host of ['localhost', '127.0.0.1', '[::1]']) {
    assert.equal(verifiedBrowserURL({ ...endpoint, url: `http://${host}:49152/app` }), `http://${host}:49152/app`)
  }
  for (const url of ['javascript:alert(1)', 'file:///app', 'http://172.17.0.2:49152/',
    'http://127.1:49152/', 'http://2130706433:49152/', 'http://localhost.evil:49152/',
    'http://user:pass@localhost:49152/', 'http://localhost:49152/?secret=x',
    'http://localhost:49152/#token', 'http://localhost:3000/', 'http://localhost:49152\\evil']) {
    assert.equal(verifiedBrowserURL({ ...endpoint, url }), null, url)
  }
  assert.equal(verifiedBrowserURL({ ...endpoint, ready: false, url: 'http://localhost:49152/' }), null)
  assert.equal(verifiedBrowserURL({ ...endpoint, host_port: undefined, url: 'http://localhost:49152/' }), null)
})

// Purpose: navigation must preserve exact workspace/deployment identity. Test the
// exported route validator directly; route registration wiring is parent-owned.
test('deployment search preserves exact identities without accepting malformed values', () => {
  assert.deepEqual(validateEnvironmentsSearch({ tab: 'deployments', workspace_id: 'workspace', deployment_id: 'deployment' }),
    { tab: 'deployments', workspace_id: 'workspace', deployment_id: 'deployment' })
  assert.deepEqual(validateEnvironmentsSearch({ workspace_id: ' workspace', deployment_id: '\nwrong' }),
    { tab: undefined, workspace_id: undefined, deployment_id: undefined })
})
