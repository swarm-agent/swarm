import assert from 'node:assert/strict'
import test from 'node:test'

import { resolveDesktopTaskWorkspace } from './task-workspace-selection'
import { parseDesktopTaskCommand } from './slash-commands'

// Purpose: resolveDesktopTaskWorkspace remains a legacy compatibility boundary,
// not Orchestrator launch coverage. This pure helper layer is the narrowest proof
// that saved catalog identity/binding is required and paths, ambiguous selectors
// and unavailable sources cannot silently retarget a background request.

const alpha = {
  path: '/saved/alpha', workspaceName: 'Alpha App', workspaceId: 'workspace-alpha',
  localWorkspaceBindingId: 'binding-alpha', state: 'ready',
}
const beta = {
  path: '/saved/beta', workspaceName: 'Beta API', workspaceId: 'workspace-beta',
  localWorkspaceBindingId: 'binding-beta', state: 'ready',
}

test('compatibility resolver retains the active canonical workspace', () => {
  assert.equal(resolveDesktopTaskWorkspace({ activeWorkspace: beta, savedWorkspaces: [alpha, beta] }), beta)
})

test('compatibility resolver accepts only saved workspace names and stable IDs', () => {
  assert.equal(resolveDesktopTaskWorkspace({ selector: 'alpha app', activeWorkspace: beta, savedWorkspaces: [alpha, beta] }), alpha)
  assert.equal(resolveDesktopTaskWorkspace({ selector: 'workspace-beta', activeWorkspace: alpha, savedWorkspaces: [alpha, beta] }), beta)
  assert.equal(resolveDesktopTaskWorkspace({ selector: 'binding-alpha', activeWorkspace: beta, savedWorkspaces: [alpha, beta] }), alpha)
})

test('compatibility resolver rejects unknown, ambiguous, unavailable, and filesystem selectors', () => {
  assert.throws(
    () => resolveDesktopTaskWorkspace({ selector: 'missing', activeWorkspace: alpha, savedWorkspaces: [alpha] }),
    /Unknown saved workspace/,
  )
  assert.throws(
    () => resolveDesktopTaskWorkspace({
      selector: 'duplicate', activeWorkspace: alpha,
      savedWorkspaces: [{ ...alpha, workspaceName: 'Duplicate' }, { ...beta, workspaceName: 'duplicate' }],
    }),
    /Ambiguous saved workspace/,
  )
  assert.throws(
    () => resolveDesktopTaskWorkspace({ selector: 'beta api', activeWorkspace: alpha, savedWorkspaces: [{ ...beta, state: 'unavailable' }] }),
    /unavailable/,
  )
  assert.throws(
    () => resolveDesktopTaskWorkspace({ selector: 'beta api', activeWorkspace: alpha, savedWorkspaces: [{ ...beta, localWorkspaceBindingId: '' }] }),
    /unavailable/,
  )
  assert.throws(
    () => resolveDesktopTaskWorkspace({ activeWorkspace: { ...alpha, localWorkspaceBindingId: '' }, savedWorkspaces: [alpha] }),
    /active workspace authority/,
  )
  for (const selector of ['/saved/alpha', '../alpha', './alpha', '~/alpha', String.raw`C:\alpha`]) {
    assert.throws(
      () => resolveDesktopTaskWorkspace({ selector, activeWorkspace: beta, savedWorkspaces: [alpha, beta] }),
      /not a filesystem path/,
    )
  }
})

// Purpose: preserve the existing negative parser assertion from the retired
// entrypoint suite. parseDesktopTaskCommand owns missing leading selector
// validation; this pure layer prevents treating malformed authority as a request.
// This is legacy compatibility, not a positive Orchestrator launch expectation.
test('compatibility parser rejects a missing leading workspace selector', () => {
  assert.deepEqual(parseDesktopTaskCommand('/task --workspace'), {
    mode: 'auto', request: '', workspaceSelectionInvalid: true,
  })
})
