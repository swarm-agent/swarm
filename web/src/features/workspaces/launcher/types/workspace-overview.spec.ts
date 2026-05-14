import assert from 'node:assert/strict'
import test from 'node:test'

import { mapWorkspaceOverviewResponse, type WorkspaceOverviewResponseWire } from './workspace-overview'

function responseForTarget(kind: string, relationship: string): WorkspaceOverviewResponseWire {
  return {
    ok: true,
    swarm_target: {
      swarm_id: kind === 'self' ? 'host-swarm' : 'child-swarm',
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
        title: 'Remote run',
        workspace_path: '/workspaces/swarm',
        workspace_name: 'swarm',
        mode: 'auto',
        created_at: 1,
        updated_at: 2,
        message_count: 1,
        metadata: {
          swarm_routed_session: true,
          swarm_routed_host_workspace_path: '/workspaces/host-swarm',
          swarm_routed_runtime_workspace_path: '/workspaces/swarm',
        },
        session_status: 'idle',
      }],
      topology_routes: [{
        route_id: 'swarm:child-swarm:/workspaces/swarm',
        route_source: 'topology/workspace_binding',
        workspace_binding_id: 'binding-1',
        runtime_swarm_id: 'child-swarm',
        runtime_swarm_name: 'Child Swarm',
        runtime_kind: 'remote',
        runtime_relationship: 'child',
        runtime_backend_url: 'https://child.example.test',
        host_swarm_id: 'host-swarm',
        host_workspace_path: '/workspaces/host-swarm',
        host_workspace_name: 'host-swarm',
        runtime_workspace_path: '/workspaces/swarm',
        container_id: 'container-1',
        replication_mode: 'mirror',
        writable: true,
        sync: {
          enabled: true,
          mode: 'mirror',
          modules: ['sessions'],
        },
        created_at: 1,
        updated_at: 2,
      }],
    }],
    directories: [],
  }
}

test('remote child workspace overview groups routed sessions under runtime workspace path', () => {
  const overview = mapWorkspaceOverviewResponse(responseForTarget('remote', 'child'))

  assert.equal(overview.swarmTarget?.kind, 'remote')
  assert.equal(overview.workspaces[0]?.sessions[0]?.workspacePath, '/workspaces/swarm')
  assert.equal(overview.workspaces[0]?.sessions[0]?.runtimeWorkspacePath, '/workspaces/swarm')
  assert.equal(overview.workspaces[0]?.topologyRoutes[0]?.routeSource, 'topology/workspace_binding')
  assert.equal(overview.workspaces[0]?.topologyRoutes[0]?.runtimeSwarmId, 'child-swarm')
  assert.deepEqual(overview.workspaces[0]?.topologyRoutes[0]?.sync.modules, ['sessions'])
})

test('self workspace overview keeps routed mirror sessions under host workspace path', () => {
  const overview = mapWorkspaceOverviewResponse(responseForTarget('self', 'self'))

  assert.equal(overview.swarmTarget?.kind, 'self')
  assert.equal(overview.workspaces[0]?.sessions[0]?.workspacePath, '/workspaces/host-swarm')
  assert.equal(overview.workspaces[0]?.sessions[0]?.runtimeWorkspacePath, '/workspaces/swarm')
})
