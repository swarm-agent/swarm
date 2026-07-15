import assert from 'node:assert/strict'
import test from 'node:test'

import { canonicalSessionWorkspacePath, sessionWorkspaceFactsFromMetadata } from './session-workspace'

test('canonical session workspace uses the source workspace path from binding metadata', () => {
  assert.equal(canonicalSessionWorkspacePath({
    workspacePath: '/workspaces/runtime',
    sourceWorkspacePath: '/workspaces/source',
    runtimeWorkspacePath: '/workspaces/runtime',
  }), '/workspaces/source')
})

test('canonical session workspace can prefer the runtime workspace path for target overviews', () => {
  assert.equal(canonicalSessionWorkspacePath({
    workspacePath: '/workspaces/runtime',
    sourceWorkspacePath: '/workspaces/source',
    runtimeWorkspacePath: '/workspaces/runtime',
    preferRuntimeWorkspacePath: true,
  }), '/workspaces/runtime')
})

test('canonical session workspace preserves normalized runtime path when no source path is available', () => {
  assert.equal(canonicalSessionWorkspacePath({
    workspacePath: '/workspaces/runtime/',
    runtimeWorkspacePath: '/workspaces/runtime',
  }), '/workspaces/runtime')
})

test('session workspace facts use v2 source and runtime binding metadata', () => {
  const facts = sessionWorkspaceFactsFromMetadata({
    swarm_v2_workspace_binding_id: 'binding:container:checkthis:/workspaces/source',
    swarm_v2_source_workspace_path: '/workspaces/source',
    swarm_v2_runtime_workspace_path: '/workspaces/runtime',
  })

  assert.equal(canonicalSessionWorkspacePath({
    workspacePath: '/workspaces/runtime',
    sourceWorkspacePath: facts.sourceWorkspacePath,
    runtimeWorkspacePath: facts.runtimeWorkspacePath,
  }), '/workspaces/source')
  assert.equal(facts.runtimeWorkspacePath, '/workspaces/runtime')
})
