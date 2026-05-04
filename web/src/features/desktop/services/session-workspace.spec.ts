import assert from 'node:assert/strict'
import test from 'node:test'

import { canonicalSessionWorkspacePath } from './session-workspace'

test('canonical session workspace defaults hosted routed sessions to host workspace path', () => {
  assert.equal(canonicalSessionWorkspacePath({
    workspacePath: '/workspaces/swarm',
    hostedHostWorkspacePath: '/workspaces/host-swarm',
    hostedRuntimeWorkspacePath: '/workspaces/swarm',
  }), '/workspaces/host-swarm')
})

test('canonical session workspace can prefer hosted runtime workspace path for remote child overviews', () => {
  assert.equal(canonicalSessionWorkspacePath({
    workspacePath: '/workspaces/swarm',
    hostedHostWorkspacePath: '/workspaces/host-swarm',
    hostedRuntimeWorkspacePath: '/workspaces/swarm',
    preferHostedRuntimeWorkspacePath: true,
  }), '/workspaces/swarm')
})

test('canonical session workspace preserves runtime path when no host mirror path is available', () => {
  assert.equal(canonicalSessionWorkspacePath({
    workspacePath: '/workspaces/swarm/',
    hostedRuntimeWorkspacePath: '/workspaces/swarm',
  }), '/workspaces/swarm')
})
