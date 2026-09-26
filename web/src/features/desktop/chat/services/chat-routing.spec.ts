import test from 'node:test'
import assert from 'node:assert/strict'
import {
  desktopChatRouteFromSessionMetadata,
  resolveDesktopChatRouteFromSession,
  type DesktopChatRoute,
} from './chat-routing'
import type { DesktopSessionRecord } from '../../types/realtime'
import type { SessionSnapshot } from '../../state/desktop-v3-cache-types'

test('desktopChatRouteFromSessionMetadata resolves route from DesktopSessionRecord', () => {
  const session: DesktopSessionRecord = {
    id: 'session-record-1',
    title: 'Chat 1',
    workspacePath: '/workspaces/proj-a',
    workspaceName: 'proj-a',
    mode: 'auto',
    metadata: {
      swarm_v3_runtime_swarm_id: 'swarm-remote',
      swarm_v3_workspace_binding_id: 'wb-123',
      swarm_v3_source_workspace_name: 'proj-a',
    },
    messageCount: 5,
    updatedAt: 1000,
    createdAt: 1000,
    permissionsHydrated: true,
  }

  const route = desktopChatRouteFromSessionMetadata(session)
  assert.ok(route)
  assert.equal(route.swarmId, 'swarm-remote')
  assert.equal(route.workspaceBindingId, 'wb-123')
  assert.equal(route.workspaceName, 'proj-a')
  assert.equal(route.hostWorkspacePath, '/workspaces/proj-a')
})

test('desktopChatRouteFromSessionMetadata resolves route from SessionSnapshot', () => {
  const snapshot: SessionSnapshot = {
    id: 'session-snapshot-1',
    title: 'Snapshot Chat',
    workspace_path: '/workspaces/proj-b',
    workspace_name: 'proj-b',
    mode: 'auto',
    metadata: {
      swarm_v3_runtime_swarm_id: 'swarm-orchestrator',
      swarm_v3_workspace_binding_id: 'wb-456',
    },
    message_count: 2,
    created_at: 2000,
    updated_at: 2000,
    last_message_at: 2000,
  }

  const route = desktopChatRouteFromSessionMetadata(snapshot)
  assert.ok(route)
  assert.equal(route.swarmId, 'swarm-orchestrator')
  assert.equal(route.workspaceBindingId, 'wb-456')
  assert.equal(route.workspaceName, 'proj-b')
  assert.equal(route.hostWorkspacePath, '/workspaces/proj-b')
})

test('resolveDesktopChatRouteFromSession resolves matched route for SessionSnapshot', () => {
  const snapshot: SessionSnapshot = {
    id: 'session-snapshot-2',
    title: 'Snapshot Chat 2',
    workspace_path: '/workspaces/proj-c',
    workspace_name: 'proj-c',
    mode: 'auto',
    metadata: {
      swarm_v3_runtime_swarm_id: 'swarm-c',
      swarm_v3_workspace_binding_id: 'wb-789',
    },
    message_count: 0,
    created_at: 3000,
    updated_at: 3000,
    last_message_at: 3000,
  }

  const routeOption: DesktopChatRoute = {
    id: 'swarm:swarm-c:binding:wb-789',
    label: 'Swarm C',
    swarmId: 'swarm-c',
    targetKind: 'host',
    targetRelationship: 'self',
    hostSwarmId: 'swarm-c',
    hostSwarmName: 'Swarm C',
    hostWorkspacePath: '/workspaces/proj-c',
    hostWorkspaceName: 'proj-c',
    runtimeWorkspacePath: '/workspaces/proj-c',
    workspaceBindingId: 'wb-789',
    workspaceName: 'proj-c',
  }

  const resolved = resolveDesktopChatRouteFromSession(snapshot, [routeOption])
  assert.ok(resolved)
  assert.equal(resolved.id, 'swarm:swarm-c:binding:wb-789')
})
