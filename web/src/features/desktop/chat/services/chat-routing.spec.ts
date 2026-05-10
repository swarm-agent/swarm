import assert from 'node:assert/strict'
import test from 'node:test'

import { applyDesktopChatRouteToSession, desktopChatRouteFromSessionMetadata, isManagedHostDesktopChatRoute, resolveDesktopChatRouteById, resolveDesktopChatRouteFromSession, withDesktopChatRoute, type DesktopChatRoute } from './chat-routing'
import type { DesktopSessionRecord } from '../../types/realtime'

const remoteRoute: DesktopChatRoute = {
  id: 'swarm:child-swarm:/workspaces/swarm',
  label: 'child swarm',
  swarmId: 'child-swarm',
  targetKind: 'remote',
  targetRelationship: 'child',
  hostWorkspacePath: '/workspaces/host-swarm',
  hostWorkspaceName: 'host swarm',
  runtimeWorkspacePath: '/workspaces/swarm',
}

const managedHostRoute: DesktopChatRoute = {
  id: 'swarm:managed-swarm:/managed/workspace',
  label: 'managed host',
  swarmId: 'managed-swarm',
  targetKind: 'host',
  targetRelationship: 'managed',
  hostWorkspacePath: '/workspaces/host-swarm',
  hostWorkspaceName: 'host swarm',
  runtimeWorkspacePath: '/managed/workspace',
}

function sessionRecord(overrides: Partial<DesktopSessionRecord> = {}): DesktopSessionRecord {
  return {
    id: 'session-1',
    title: 'Remote child session',
    workspacePath: '/workspaces/swarm',
    workspaceName: 'child swarm',
    mode: 'auto',
    metadata: {},
    messageCount: 0,
    updatedAt: 0,
    createdAt: 0,
    permissionsHydrated: true,
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
    live: {
      status: 'idle',
      activeRunId: null,
      startedAt: null,
      completedAt: null,
      lastEventAt: null,
      lastError: null,
    },
    runtimeWorkspacePath: '/workspaces/swarm',
    worktreeEnabled: false,
    worktreeRootPath: '',
    worktreeBaseBranch: '',
    worktreeBranch: '',
    gitBranch: '',
    gitHasGit: false,
    gitClean: false,
    gitDirtyCount: 0,
    gitStagedCount: 0,
    gitModifiedCount: 0,
    gitUntrackedCount: 0,
    gitConflictCount: 0,
    gitAheadCount: 0,
    gitBehindCount: 0,
    gitCommittedFileCount: 0,
    gitCommittedAdditions: 0,
    gitCommittedDeletions: 0,
    lifecycle: null,
    ...overrides,
  }
}

test('routed session fetch URL includes swarm_id so backend can proxy to child', () => {
  assert.equal(
    withDesktopChatRoute('/v1/sessions/session-1', remoteRoute),
    '/v1/sessions/session-1?swarm_id=child-swarm',
  )
})

test('managed host routes are identified separately from child routes', () => {
  assert.equal(isManagedHostDesktopChatRoute(managedHostRoute), true)
  assert.equal(isManagedHostDesktopChatRoute(remoteRoute), false)
})

test('routed session metadata reconstructs route label from server metadata, not target metadata', () => {
  const route = desktopChatRouteFromSessionMetadata(sessionRecord({
    workspacePath: '/workspaces/host-swarm',
    workspaceName: 'host swarm',
    runtimeWorkspacePath: '/workspaces/swarm',
    metadata: {
      swarm_route_id: remoteRoute.id,
      swarm_route_label: 'Remote Swarm',
      swarm_route_target_kind: 'remote',
      swarm_routed_child_swarm_id: 'child-swarm',
      swarm_routed_host_workspace_path: '/workspaces/host-swarm',
      swarm_routed_runtime_workspace_path: '/workspaces/swarm',
      swarm_target_name: 'memory',
      target_display_name: 'memory',
    },
  }))

  assert.equal(route?.id, remoteRoute.id)
  assert.equal(route?.label, 'Remote Swarm')
  assert.equal(route?.targetKind, 'remote')
})

test('routed session route resolution falls back to server metadata when route option is unavailable', () => {
  const hostRoute: DesktopChatRoute = {
    id: 'host',
    label: 'Local Swarm',
    swarmId: null,
    targetKind: 'host',
    targetRelationship: 'self',
    hostWorkspacePath: '/workspaces/host-swarm',
    hostWorkspaceName: 'host swarm',
    runtimeWorkspacePath: '/workspaces/host-swarm',
  }
  const route = resolveDesktopChatRouteFromSession(sessionRecord({
    workspacePath: '/workspaces/host-swarm',
    workspaceName: 'host swarm',
    runtimeWorkspacePath: '/workspaces/swarm',
    metadata: {
      swarm_route_id: remoteRoute.id,
      swarm_route_label: 'Remote Swarm',
      swarm_routed_child_swarm_id: 'child-swarm',
      swarm_routed_host_workspace_path: '/workspaces/host-swarm',
      swarm_routed_runtime_workspace_path: '/workspaces/swarm',
      swarm_target_name: 'memory',
    },
  }), [hostRoute], hostRoute)

  assert.equal(route?.id, remoteRoute.id)
  assert.equal(route?.label, 'Remote Swarm')
})

test('routed session route resolution prefers current route option label when available', () => {
  const route = resolveDesktopChatRouteFromSession(sessionRecord({
    metadata: {
      swarm_route_id: remoteRoute.id,
      swarm_route_label: 'Stale Remote Swarm',
      swarm_routed_child_swarm_id: 'child-swarm',
      swarm_routed_runtime_workspace_path: '/workspaces/swarm',
    },
  }), [remoteRoute], null)

  assert.equal(route?.label, 'child swarm')
})

test('managed host session metadata reconstructs selected managed host route', () => {
  const route = resolveDesktopChatRouteFromSession(sessionRecord({
    workspacePath: '/host/workspace',
    workspaceName: 'host workspace',
    runtimeWorkspacePath: '/managed/workspace',
    metadata: {
      swarm_route_id: managedHostRoute.id,
      swarm_route_label: 'Managed Host',
      swarm_route_target_kind: 'host',
      swarm_route_target_relationship: 'managed',
      swarm_routed_session: true,
      swarm_routed_host_swarm_id: 'host-swarm-id',
      swarm_routed_host_workspace_path: '/host/workspace',
      swarm_routed_runtime_workspace_path: '/managed/workspace',
      swarm_routed_child_swarm_id: 'managed-swarm',
      swarm_managed_host_session: true,
    },
  }), [managedHostRoute], null)

  assert.equal(route?.id, managedHostRoute.id)
  assert.equal(route?.swarmId, 'managed-swarm')
  assert.equal(route?.targetKind, 'host')
  assert.equal(route?.targetRelationship, 'managed')
})

test('routed session hydration preserves remote child workspace identity', () => {
  const mapped = applyDesktopChatRouteToSession(sessionRecord(), remoteRoute)

  assert.equal(mapped.workspacePath, '/workspaces/swarm')
  assert.equal(mapped.workspaceName, 'child swarm')
  assert.equal(mapped.runtimeWorkspacePath, '/workspaces/swarm')
})

test('workspace defaults resolve by server-backed route id', () => {
  const hostRoute: DesktopChatRoute = {
    id: 'host',
    label: 'host',
    swarmId: null,
    targetKind: 'host',
    targetRelationship: 'self',
    hostWorkspacePath: '/workspaces/host-swarm',
    hostWorkspaceName: 'host swarm',
    runtimeWorkspacePath: '/workspaces/host-swarm',
  }

  assert.equal(resolveDesktopChatRouteById([hostRoute, remoteRoute], remoteRoute.id, hostRoute), remoteRoute)
})

test('routed local host mirror session remains grouped under host workspace', () => {
  const mapped = applyDesktopChatRouteToSession(sessionRecord({
    workspacePath: '/workspaces/host-swarm',
    workspaceName: 'host swarm',
  }), remoteRoute)

  assert.equal(mapped.workspacePath, '/workspaces/host-swarm')
  assert.equal(mapped.workspaceName, 'host swarm')
  assert.equal(mapped.runtimeWorkspacePath, '/workspaces/swarm')
})

test('flow sessions preserve their own workspace identity under routed child hydration', () => {
  const mapped = applyDesktopChatRouteToSession(sessionRecord({
    title: 'Memory sweep',
    workspacePath: '/workspaces/swarm',
    workspaceName: 'child swarm',
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-1',
    },
  }), remoteRoute)

  assert.equal(mapped.workspacePath, '/workspaces/swarm')
  assert.equal(mapped.workspaceName, 'child swarm')
  assert.equal(mapped.runtimeWorkspacePath, '/workspaces/swarm')
})
