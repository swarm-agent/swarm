import assert from 'node:assert/strict'
import test from 'node:test'

import { applyDesktopChatRouteToSession, buildDesktopChatRouteOptions, desktopChatRouteFromSessionMetadata, desktopChatRouteID, getDesktopSessionCreateV2Target, isManagedHostDesktopChatRoute, resolveDesktopChatRouteById, resolveDesktopChatRouteFromSession, withDesktopChatRoute, type DesktopChatRoute } from './chat-routing'
import type { DesktopSessionRecord } from '../../types/realtime'

const remoteRoute: DesktopChatRoute = {
  id: 'swarm:child-swarm:binding:binding-remote',
  label: 'child swarm',
  swarmId: 'child-swarm',
  targetKind: 'remote',
  targetRelationship: 'child',
  hostSwarmId: 'host-swarm-id',
  hostSwarmName: 'Host Swarm',
  hostWorkspacePath: '/workspaces/host-swarm',
  hostWorkspaceName: 'host swarm',
  runtimeWorkspacePath: '/workspaces/swarm',
  workspaceBindingId: 'binding-remote',
}

const managedHostRoute: DesktopChatRoute = {
  id: 'swarm:managed-swarm:binding:binding-managed',
  label: 'managed host',
  swarmId: 'managed-swarm',
  targetKind: 'host',
  targetRelationship: 'managed',
  hostSwarmId: 'host-swarm-id',
  hostSwarmName: 'Host Swarm',
  hostWorkspacePath: '/workspaces/host-swarm',
  hostWorkspaceName: 'host swarm',
  runtimeWorkspacePath: '/managed/workspace',
  workspaceBindingId: 'binding-managed',
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

test('managed host routes are identified by managed relationship only', () => {
  assert.equal(isManagedHostDesktopChatRoute(managedHostRoute), true)
  assert.equal(isManagedHostDesktopChatRoute(remoteRoute), false)
  assert.equal(isManagedHostDesktopChatRoute({
    ...managedHostRoute,
    id: 'swarm:self-swarm:binding:binding-self',
    label: 'primary self',
    swarmId: 'self-swarm',
    targetKind: 'host',
    targetRelationship: 'self',
    workspaceBindingId: 'binding-self',
  }), false)
})

test('legacy routed session metadata is not accepted for native v2 route reconstruction', () => {
  const route = desktopChatRouteFromSessionMetadata(sessionRecord({
    workspacePath: '/workspaces/host-swarm',
    workspaceName: 'host swarm',
    runtimeWorkspacePath: '/workspaces/swarm',
    metadata: {
      swarm_route_id: remoteRoute.id,
      swarm_routed_workspace_binding_id: 'binding-remote',
      swarm_route_label: 'Remote Swarm',
      swarm_route_target_kind: 'remote',
      swarm_routed_child_swarm_id: 'child-swarm',
      swarm_routed_host_workspace_path: '/workspaces/host-swarm',
      swarm_routed_runtime_workspace_path: '/workspaces/swarm',
      swarm_target_name: 'memory',
      target_display_name: 'memory',
    },
  }))

  assert.equal(route, null)
})

test('v2 local-container mirrored session resolves to matching container route instead of primary', () => {
  const hostRoute: DesktopChatRoute = {
    id: 'swarm:primary-swarm:binding:binding-primary',
    label: 'primary',
    swarmId: 'primary-swarm',
    targetKind: 'host',
    targetRelationship: 'self',
    hostSwarmId: 'primary-swarm',
    hostSwarmName: 'primary',
    hostWorkspacePath: '/host/workspace',
    hostWorkspaceName: 'swarm-go',
    runtimeWorkspacePath: '/host/workspace',
    workspaceBindingId: 'binding-primary',
  }
  const containerRoute: DesktopChatRoute = {
    id: 'swarm:container-2-swarm:binding:binding-container-2',
    label: 'container 2',
    swarmId: 'container-2-swarm',
    targetKind: 'container',
    targetRelationship: 'child',
    hostSwarmId: 'primary-swarm',
    hostSwarmName: 'primary',
    hostWorkspacePath: '/host/workspace',
    hostWorkspaceName: 'swarm-go',
    runtimeWorkspacePath: '/container/workspace',
    workspaceBindingId: 'binding-container-2',
    workspaceName: 'swarm-go',
  }

  const route = resolveDesktopChatRouteFromSession(sessionRecord({
    workspacePath: '/host/workspace',
    workspaceName: 'swarm-go',
    runtimeWorkspacePath: '/container/workspace',
    metadata: {
      swarm_v2_execution_class: 'local_container',
      swarm_v2_runtime_swarm_id: 'container-2-swarm',
      swarm_v2_runtime_kind: 'container',
      swarm_v2_authority_host_swarm_id: 'primary-swarm',
      swarm_v2_workspace_binding_id: 'binding-container-2',
      local_workspace_binding_id: 'binding-container-2',
      swarm_v2_source_workspace_name: 'swarm-go',
      swarm_v2_source_workspace_path: '/host/workspace',
      swarm_v2_runtime_workspace_path: '/container/workspace',
    },
  }), [hostRoute, containerRoute], hostRoute)

  assert.equal(route?.id, containerRoute.id)
  assert.equal(route?.label, 'container 2')
  assert.equal(route?.swarmId, 'container-2-swarm')
})

test('v2 local-container mirrored session metadata provides fallback route before topology loads', () => {
  const route = desktopChatRouteFromSessionMetadata(sessionRecord({
    workspacePath: '/host/workspace',
    workspaceName: 'primary workspace',
    runtimeWorkspacePath: '',
    metadata: {
      swarm_v2_execution_class: 'local_container',
      swarm_v2_runtime_swarm_id: 'container-2-swarm',
      swarm_v2_runtime_kind: 'container',
      swarm_v2_authority_host_swarm_id: 'primary-swarm',
      local_workspace_binding_id: 'binding-container-2',
      swarm_v2_source_workspace_name: 'swarm-go',
      swarm_v2_source_workspace_path: '/host/workspace',
      swarm_v2_runtime_workspace_path: '/container/workspace',
    },
  }))

  assert.equal(route?.id, 'swarm:container-2-swarm:binding:binding-container-2')
  assert.equal(route?.label, 'container-2-swarm')
  assert.equal(route?.targetKind, 'container')
  assert.equal(route?.targetRelationship, 'child')
  assert.equal(route?.runtimeWorkspacePath, '/container/workspace')
  assert.equal(route?.workspaceName, 'swarm-go')
})

test('managed host session metadata reconstructs selected managed host route', () => {
  const route = resolveDesktopChatRouteFromSession(sessionRecord({
    workspacePath: '/host/workspace',
    workspaceName: 'host workspace',
    runtimeWorkspacePath: '/managed/workspace',
    metadata: {
      swarm_route_id: managedHostRoute.id,
      swarm_managed_host_workspace_binding_id: 'binding-managed',
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
    hostSwarmId: '',
    hostSwarmName: 'host',
    hostWorkspacePath: '/workspaces/host-swarm',
    hostWorkspaceName: 'host swarm',
    runtimeWorkspacePath: '/workspaces/host-swarm',
    workspaceBindingId: '',
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

import type { WorkspaceOverviewTopologyRoute } from '../../../workspaces/launcher/types/workspace-overview'

function topologyRoute(overrides: Partial<WorkspaceOverviewTopologyRoute> = {}): WorkspaceOverviewTopologyRoute {
  return {
    routeId: 'swarm:child-swarm:binding:binding-1',
    routeSource: 'topology/workspace_binding',
    workspaceBindingId: 'binding-1',
    runtimeSwarmId: 'child-swarm',
    runtimeSwarmName: 'Child Swarm',
    runtimeKind: 'remote',
    runtimeRelationship: 'child',
    authorityHostSwarmId: 'host-swarm',
    hostSwarmId: 'host-swarm',
    hostSwarmName: 'Host Swarm',
    hostWorkspacePath: '/host/workspace',
    hostWorkspaceName: 'Host Workspace',
    runtimeWorkspacePath: '/runtime/workspace',
    containerId: '',
    replicationMode: 'mirror',
    writable: true,
    sync: {
      enabled: true,
      mode: 'mirror',
      modules: [],
    },
    createdAt: 1,
    updatedAt: 2,
    ...overrides,
  }
}


test('route identity uses workspace binding id rather than runtime path', () => {
  const routeA = topologyRoute({
    routeId: 'legacy:path:/runtime/a',
    workspaceBindingId: 'binding-stable',
    runtimeWorkspacePath: '/runtime/a',
  })
  const routeB = topologyRoute({
    routeId: 'legacy:path:/runtime/b',
    workspaceBindingId: 'binding-stable',
    runtimeWorkspacePath: '/runtime/b',
  })

  assert.equal(desktopChatRouteID('child-swarm', 'Host Workspace', 'binding-stable'), 'swarm:child-swarm:binding:binding-stable')
  assert.equal(desktopChatRouteID('child-swarm', 'Host Workspace', 'binding-stable'), desktopChatRouteID('child-swarm', 'Other Host Workspace', 'binding-stable'))

  const routes = buildDesktopChatRouteOptions({
    hostSwarmName: 'Host Swarm',
    workspacePath: '/host/workspace',
    workspaceName: 'Host Workspace',
    topologyRoutes: [routeA, routeB],
  })

  assert.equal(routes.length, 2)
  assert.equal(routes[1]?.id, 'swarm:child-swarm:binding:binding-stable')
  assert.equal(routes[1]?.workspaceBindingId, 'binding-stable')
})

test('buildDesktopChatRouteOptions derives routes from topology route data', () => {
  const routes = buildDesktopChatRouteOptions({
    hostSwarmName: 'Host Swarm',
    workspacePath: '/host/workspace',
    workspaceName: 'Host Workspace',
    topologyRoutes: [topologyRoute()],
  })

  assert.equal(routes.length, 2)
  assert.deepEqual(routes[0], {
    id: 'host',
    label: 'Host Swarm',
    swarmId: null,
    targetKind: 'host',
    targetRelationship: 'self',
    hostSwarmId: '',
    hostSwarmName: 'Host Swarm',
    hostWorkspacePath: '/host/workspace',
    hostWorkspaceName: 'Host Workspace',
    runtimeWorkspacePath: '/host/workspace',
    workspaceBindingId: '',
    workspaceName: 'Host Workspace',
    targetSwarmName: 'Host Swarm',
  })
  assert.deepEqual(routes[1], {
    id: 'swarm:child-swarm:binding:binding-1',
    label: 'Child Swarm',
    swarmId: 'child-swarm',
    targetKind: 'remote',
    targetRelationship: 'child',
    hostSwarmId: 'host-swarm',
    hostSwarmName: 'Host Swarm',
    hostWorkspacePath: '/host/workspace',
    hostWorkspaceName: 'Host Workspace',
    runtimeWorkspacePath: '/runtime/workspace',
    workspaceBindingId: 'binding-1',
    workspaceName: 'Host Workspace',
    targetSwarmName: 'Child Swarm',
  })
})

test('buildDesktopChatRouteOptions does not require replication links or swarm target lookups', () => {
  const routes = buildDesktopChatRouteOptions({
    hostSwarmName: 'Host Swarm',
    workspacePath: '/host/workspace',
    workspaceName: 'Host Workspace',
    topologyRoutes: [topologyRoute({ routeId: '' })],
  })

  assert.equal(routes[1]?.id, desktopChatRouteID('child-swarm', 'Host Workspace', 'binding-1'))
  assert.equal(routes[1]?.label, 'Child Swarm')
})

test('buildDesktopChatRouteOptions skips workspace-backed routes without binding id', () => {
  const routes = buildDesktopChatRouteOptions({
    hostSwarmName: 'Host Swarm',
    workspacePath: '/host/workspace',
    workspaceName: 'Host Workspace',
    topologyRoutes: [topologyRoute({ routeId: 'swarm:child-swarm:workspace:Host Workspace', workspaceBindingId: '' })],
  })

  assert.equal(routes.length, 1)
  assert.equal(routes[0]?.id, 'host')
})

test('buildDesktopChatRouteOptions labels mirrored child routes with their runtime name and keeps host name separately', () => {
  const routes = buildDesktopChatRouteOptions({
    hostSwarmName: 'Primary Host',
    workspacePath: '/host/workspace',
    workspaceName: 'Host Workspace',
    topologyRoutes: [topologyRoute({
      runtimeSwarmId: 'container-swarm',
      runtimeSwarmName: 'SwarmTarget2',
      runtimeKind: 'mirrored',
      runtimeRelationship: 'child',
      hostSwarmId: 'managed-host-swarm',
      hostSwarmName: 'SwarmTarget2',
    })],
  })

  assert.equal(routes[1]?.label, 'SwarmTarget2')
  assert.equal(routes[1]?.hostSwarmName, 'SwarmTarget2')
  assert.equal(routes[1]?.targetKind, 'mirrored')
})

test('buildDesktopChatRouteOptions treats different workspace bindings as different routes', () => {
  const routes = buildDesktopChatRouteOptions({
    hostSwarmName: 'Host Swarm',
    workspacePath: '/host/workspace',
    workspaceName: 'Host Workspace',
    topologyRoutes: [topologyRoute(), topologyRoute({ workspaceBindingId: 'binding-duplicate' })],
  })

  assert.equal(routes.length, 3)
  assert.equal(routes[1]?.id, 'swarm:child-swarm:binding:binding-1')
  assert.equal(routes[2]?.id, 'swarm:child-swarm:binding:binding-duplicate')
})

test('groupDesktopChatRoutes keeps only managed-relationship host route outside Primary', async () => {
  const { groupDesktopChatRoutes } = await import('../components/route-picker')
  const routes = [
    {
      id: 'host',
      label: 'SwarmTarget1',
      swarmId: null,
      targetKind: 'host',
      targetRelationship: 'self',
      hostSwarmId: '',
      hostSwarmName: 'SwarmTarget1',
      hostWorkspacePath: '/srv/swarm/swarm-go',
      hostWorkspaceName: 'swarm-go',
      runtimeWorkspacePath: '/srv/swarm/swarm-go',
      workspaceBindingId: '',
    },
    {
      id: 'swarm:primary-swarm:binding:binding-primary-self',
      label: 'primary self binding',
      swarmId: 'primary-swarm',
      targetKind: 'host',
      targetRelationship: 'self',
      hostSwarmId: 'primary-swarm',
      hostSwarmName: 'SwarmTarget1',
      hostWorkspacePath: '/srv/swarm/swarm-go',
      hostWorkspaceName: 'swarm-go',
      runtimeWorkspacePath: '/srv/swarm/swarm-go',
      workspaceBindingId: 'binding-primary-self',
    },
    {
      id: 'swarm:localtest-swarm:binding:binding-localtest',
      label: 'localtest',
      swarmId: 'localtest-swarm',
      targetKind: 'local',
      targetRelationship: 'child',
      hostSwarmId: 'primary-swarm',
      hostSwarmName: 'SwarmTarget1',
      hostWorkspacePath: '/srv/swarm/swarm-go',
      hostWorkspaceName: 'swarm-go',
      runtimeWorkspacePath: '/workspaces/swarm-go',
      workspaceBindingId: 'binding-localtest',
    },
    {
      id: 'swarm:child-swarm:binding:binding-heytest',
      label: 'heytest',
      swarmId: 'child-swarm',
      targetKind: 'mirrored',
      targetRelationship: 'child',
      hostSwarmId: 'managed-swarm',
      hostSwarmName: 'SwarmTarget2',
      hostWorkspacePath: '/srv/swarm/swarm-go',
      hostWorkspaceName: 'swarm-go',
      runtimeWorkspacePath: '/workspaces/swarm-go',
      workspaceBindingId: 'binding-heytest',
    },
    {
      id: 'swarm:managed-swarm:binding:binding-managed-host',
      label: 'SwarmTarget2',
      swarmId: 'managed-swarm',
      targetKind: 'host',
      targetRelationship: 'managed',
      hostSwarmId: 'managed-swarm',
      hostSwarmName: 'SwarmTarget2',
      hostWorkspacePath: '/srv/swarm/swarm-go',
      hostWorkspaceName: 'swarm-go',
      runtimeWorkspacePath: '/srv/swarm/swarm-go',
      workspaceBindingId: 'binding-managed-host',
    },
  ]

  const groups = groupDesktopChatRoutes(routes)

  assert.deepEqual(groups.map((group) => group.label), ['Primary', 'Managed host'])
  assert.deepEqual(groups.map((group) => group.routes.map((route) => route.label)), [['SwarmTarget1', 'primary self binding', 'localtest'], ['SwarmTarget2', 'heytest']])
})


test('buildDesktopChatRouteOptions includes local self binding for host route when provided', () => {
  const routes = buildDesktopChatRouteOptions({
    hostSwarmName: 'Host Swarm',
    workspacePath: '/host/workspace',
    workspaceName: 'Host Workspace',
    topologyRoutes: [],
    localWorkspaceBindingId: 'binding-local-self',
    hostSwarmId: 'host-swarm',
  })

  assert.equal(routes[0]?.id, 'swarm:host-swarm:binding:binding-local-self')
  assert.equal(routes[0]?.swarmId, 'host-swarm')
  assert.equal(routes[0]?.workspaceBindingId, 'binding-local-self')
  assert.equal(routes[0]?.requiresWorkspaceBinding, true)
})


test('getDesktopSessionCreateV2Target maps primary and local-container routes without path inference', () => {
  assert.deepEqual(getDesktopSessionCreateV2Target({
    ...remoteRoute,
    swarmId: 'primary-swarm',
    targetKind: 'host',
    targetRelationship: 'self',
    workspaceBindingId: 'binding-primary',
    hostWorkspacePath: '/must/not/matter',
    runtimeWorkspacePath: '/must/not/matter-runtime',
    workspaceName: 'must-not-matter',
  }), {
    endpoint: '/v2/sessions/primary',
    swarmId: 'primary-swarm',
    workspaceBindingId: 'binding-primary',
  })

  for (const targetKind of ['container', 'local-container', 'local_container', 'mirrored_child', 'child']) {
    assert.deepEqual(getDesktopSessionCreateV2Target({
      ...remoteRoute,
      targetKind,
      targetRelationship: 'child',
      swarmId: 'child-swarm',
      workspaceBindingId: 'binding-child',
      hostWorkspacePath: '',
      runtimeWorkspacePath: '',
      workspaceName: '',
    }), {
      endpoint: '/v2/sessions/local-containers',
      swarmId: 'child-swarm',
      workspaceBindingId: 'binding-child',
    })
  }
})

test('getDesktopSessionCreateV2Target blocks managed and missing-binding routes', () => {
  assert.deepEqual(getDesktopSessionCreateV2Target(managedHostRoute), {
    endpoint: null,
    unsupportedReason: 'Managed-host v2 session create is not implemented yet.',
  })
  const missingBinding = getDesktopSessionCreateV2Target({ ...remoteRoute, targetKind: 'local-container', workspaceBindingId: '' })
  assert.equal(missingBinding.endpoint, null)
  assert.match(missingBinding.unsupportedReason, /workspace_binding_id/)
})
