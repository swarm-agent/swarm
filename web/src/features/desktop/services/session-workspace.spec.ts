import assert from 'node:assert/strict'
import test from 'node:test'

import { canonicalSessionWorkspacePath, sessionWorkspaceFactsFromMetadata } from './session-workspace'

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

test('canonical session workspace uses v2 source workspace path from binding metadata', () => {
  const facts = sessionWorkspaceFactsFromMetadata({
    swarm_v2_workspace_binding_id: 'binding:replica:checkthis:/home/installer/swarm-go',
    swarm_v2_source_workspace_path: '/home/installer/swarm-go',
    swarm_v2_runtime_workspace_path: '/workspaces/swarm-go',
  })

  assert.equal(canonicalSessionWorkspacePath({
    workspacePath: '/workspaces/swarm-go',
    sourceWorkspacePath: facts.sourceWorkspacePath,
    runtimeWorkspacePath: facts.runtimeWorkspacePath,
  }), '/home/installer/swarm-go')
  assert.equal(facts.runtimeWorkspacePath, '/workspaces/swarm-go')
})
