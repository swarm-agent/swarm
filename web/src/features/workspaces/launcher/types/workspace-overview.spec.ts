import assert from 'node:assert/strict'
import test from 'node:test'

import { mapWorkspaceOverviewResponse, type WorkspaceOverviewResponseWire } from './workspace-overview'

function responseForTarget(kind: string, relationship: string): WorkspaceOverviewResponseWire {
  return {
    ok: true,
    swarm_target: {
      swarm_id: kind === 'self' ? 'host-swarm' : 'container-swarm',
      name: kind,
      kind,
      relationship,
      current: true,
    },
    workspaces: [{
      path: '/workspaces/swarm',
      workspace_name: 'swarm',
      directories: [],
      is_git_repo: true,
      sort_index: 0,
      added_at: 1,
      updated_at: 1,
      last_selected_at: 1,
      active: true,
      worktree_enabled: false,
      sessions: [{
        id: 'session-1',
        title: 'Target run',
        workspace_path: '/workspaces/swarm',
        workspace_name: 'swarm',
        mode: 'auto',
        created_at: 1,
        updated_at: 2,
        message_count: 1,
        metadata: {
          swarm_v2_source_workspace_path: '/workspaces/host-swarm',
          swarm_v2_runtime_workspace_path: '/workspaces/swarm',
        },
        session_status: 'idle',
      }],
      topology_routes: [{
        route_id: 'swarm:container-swarm:/workspaces/swarm',
        route_source: 'topology/workspace_binding',
        workspace_binding_id: 'binding-1',
        runtime_swarm_id: 'container-swarm',
        runtime_swarm_name: 'Container Swarm',
        runtime_kind: 'container',
        runtime_relationship: 'child',
        authority_host_swarm_id: 'host-swarm',
        host_swarm_id: 'host-swarm',
        host_workspace_path: '/workspaces/host-swarm',
        host_workspace_name: 'host-swarm',
        runtime_workspace_path: '/workspaces/swarm',
        writable: true,
        created_at: 1,
        updated_at: 2,
      }],
    }],
    directories: [],
  }
}

test('child target workspace overview groups sessions under runtime workspace path', () => {
  const overview = mapWorkspaceOverviewResponse(responseForTarget('container', 'child'))

  assert.equal(overview.swarmTarget?.kind, 'container')
  assert.equal(overview.workspaces[0]?.sessions[0]?.workspacePath, '/workspaces/swarm')
  assert.equal(overview.workspaces[0]?.sessions[0]?.runtimeWorkspacePath, '/workspaces/swarm')
  assert.equal(overview.workspaces[0]?.topologyRoutes[0]?.routeSource, 'topology/workspace_binding')
  assert.equal(overview.workspaces[0]?.topologyRoutes[0]?.runtimeSwarmId, 'container-swarm')
  assert.equal(overview.workspaces[0]?.topologyRoutes[0]?.workspaceBindingId, 'binding-1')
  assert.equal(overview.workspaces[0]?.topologyRoutes[0]?.authorityHostSwarmId, 'host-swarm')
})

test('self workspace overview keeps target sessions under source workspace path', () => {
  const overview = mapWorkspaceOverviewResponse(responseForTarget('self', 'self'))

  assert.equal(overview.swarmTarget?.kind, 'self')
  assert.equal(overview.workspaces[0]?.sessions[0]?.workspacePath, '/workspaces/host-swarm')
  assert.equal(overview.workspaces[0]?.sessions[0]?.runtimeWorkspacePath, '/workspaces/swarm')
})


test('workspace overview keeps topology routes by binding identity without requiring runtime path', () => {
  const response = responseForTarget('remote', 'child')
  const route = response.workspaces?.[0]?.topology_routes?.[0]
  if (route) {
    route.runtime_workspace_path = ''
  }

  const overview = mapWorkspaceOverviewResponse(response)

  assert.equal(overview.workspaces[0]?.topologyRoutes.length, 1)
  assert.equal(overview.workspaces[0]?.topologyRoutes[0]?.runtimeSwarmId, 'container-swarm')
  assert.equal(overview.workspaces[0]?.topologyRoutes[0]?.workspaceBindingId, 'binding-1')
  assert.equal(overview.workspaces[0]?.topologyRoutes[0]?.runtimeWorkspacePath, '')
})


test('workspace overview derives live run state and runIntent from canonical active_run only', () => {
  const response = responseForTarget('self', 'self')
  const session = response.workspaces?.[0]?.sessions?.[0]
  if (!session) {
    throw new Error('missing overview session fixture')
  }
  session.lifecycle = {
    session_id: 'session-1',
    run_id: 'run-lifecycle-stale',
    active: false,
    phase: 'completed',
    started_at: 10,
    ended_at: 20,
    updated_at: 20,
    generation: 1,
  }
  session.active_run = {
    run_id: 'run-canonical',
    status: 'running',
    created_at: 1_000,
    updated_at: 1_500,
    event_seq: 7,
    last_seq: 9,
  }
  session.session_status = 'idle'

  const mapped = mapWorkspaceOverviewResponse(response).workspaces[0]?.sessions[0]

  assert.equal(mapped?.runIntent?.runId, 'run-canonical')
  assert.equal(mapped?.runIntent?.status, 'running')
  assert.equal(mapped?.runIntent?.createdAt, 1_000)
  assert.equal(mapped?.live.runId, 'run-canonical')
  assert.equal(mapped?.live.startedAt, 1_000)
  assert.equal(mapped?.live.status, 'running')
  assert.equal(mapped?.live.seq, 9)
})

test('workspace overview ignores lifecycle active and session_status without canonical active_run', () => {
  const response = responseForTarget('self', 'self')
  const session = response.workspaces?.[0]?.sessions?.[0]
  if (!session) {
    throw new Error('missing overview session fixture')
  }
  session.lifecycle = {
    session_id: 'session-1',
    run_id: 'run-lifecycle-only',
    active: true,
    phase: 'running',
    started_at: 1_000,
    ended_at: 0,
    updated_at: 1_500,
    generation: 1,
  }
  session.active_run = null
  session.session_status = 'running'

  const mapped = mapWorkspaceOverviewResponse(response).workspaces[0]?.sessions[0]

  assert.equal(mapped?.runIntent, null)
  assert.equal(mapped?.live.runId, null)
  assert.equal(mapped?.live.startedAt, null)
  assert.equal(mapped?.live.status, 'idle')
})

test('workspace overview treats terminal canonical active_run as inactive even with running session_status', () => {
  const response = responseForTarget('self', 'self')
  const session = response.workspaces?.[0]?.sessions?.[0]
  if (!session) {
    throw new Error('missing overview session fixture')
  }
  session.active_run = {
    run_id: 'run-terminal',
    status: 'cancelled',
    created_at: 1_000,
    updated_at: 2_000,
    event_seq: 8,
  }
  session.session_status = 'running'

  const mapped = mapWorkspaceOverviewResponse(response).workspaces[0]?.sessions[0]

  assert.equal(mapped?.runIntent, null)
  assert.equal(mapped?.live.runId, null)
  assert.equal(mapped?.live.startedAt, null)
  assert.equal(mapped?.live.status, 'idle')
})
