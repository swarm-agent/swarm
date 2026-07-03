import {
  workspaceRoutePlacementLabel,
  workspaceRouteTargetName,
  workspaceRouteTargetType,
  workspacePlacementLinks,
} from './workspace-placement'
import type { SwarmTarget } from '../../../desktop/swarm/api/swarm-targets'
import type { WorkspaceOverviewTopologyRoute } from '../types/workspace-overview'

function assertEqual(actual: string, expected: string, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`)
  }
}

function route(overrides: Partial<WorkspaceOverviewTopologyRoute> = {}): WorkspaceOverviewTopologyRoute {
  return {
    routeId: 'target-swarm:/workspace',
    routeSource: 'topology/workspace_binding',
    workspaceBindingId: 'binding-1',
    runtimeSwarmId: 'target-swarm',
    runtimeSwarmName: 'Target Name',
    runtimeKind: 'host',
    runtimeRelationship: 'remote',
    authorityHostSwarmId: 'host-swarm',
    hostSwarmId: 'host-swarm',
    hostWorkspacePath: '/source',
    hostWorkspaceName: 'Source',
    runtimeWorkspacePath: '/workspace',
    containerId: '',
    replicationMode: 'bundle',
    writable: true,
    sync: { enabled: false, mode: '', modules: [] },
    createdAt: 0,
    updatedAt: 0,
    ...overrides,
  }
}

function target(overrides: Partial<SwarmTarget> = {}): SwarmTarget {
  return {
    swarm_id: 'target-swarm',
    name: 'Remote Laptop',
    role: '',
    relationship: '',
    kind: 'host',
    online: true,
    selectable: true,
    current: false,
    ...overrides,
  }
}

function testRemoteHostTopologyRouteIsVisibleFromTopologyBinding(): void {
  const remoteRoute = route({ runtimeSwarmName: 'Laptop' })
  assertEqual(workspaceRouteTargetType(remoteRoute, null), 'Remote Host', 'topology workspace bindings should be labeled as Remote Host')
  assertEqual(workspaceRoutePlacementLabel(remoteRoute, null), 'Laptop (Remote Host)', 'topology placement label should lead with the route name')
}

function testRuntimePathCanProvideVisibleRouteName(): void {
  const pathOnlyRoute = route({ runtimeSwarmName: '', runtimeWorkspacePath: '/srv/workspaces/project' })
  assertEqual(workspaceRouteTargetName(pathOnlyRoute, null), '/srv/workspaces/project', 'runtime path should be visible when no runtime name is stored')
}

function testNonTopologyRoutesAreRemovedFromVisibleLinks(): void {
  const links = workspacePlacementLinks([route({ routeSource: 'workspace/entry/deleted' })], [])
  assertEqual(String(links.length), '0', 'non-topology routes must not render as visible placement links')
}

function testRemoteRelationshipMapsToRemoteHost(): void {
  const remoteTarget = target({ kind: 'host', relationship: 'remote', role: 'remote' })
  assertEqual(workspaceRouteTargetType(route(), remoteTarget), 'Remote Host', 'remote relationship should map to Remote Host')
}

function testTopologyRoutesRenderWithoutTargetRecord(): void {
  const links = workspacePlacementLinks([route({ runtimeRelationship: '', runtimeKind: '' })], [])
  assertEqual(String(links.length), '1', 'topology workspace bindings should render even when the target list is unavailable')
  assertEqual(links[0]?.targetType ?? '', 'Remote Host', 'target-less topology workspace binding should still be a Remote Host route')
}

function main(): void {
  testRemoteHostTopologyRouteIsVisibleFromTopologyBinding()
  testRuntimePathCanProvideVisibleRouteName()
  testNonTopologyRoutesAreRemovedFromVisibleLinks()
  testRemoteRelationshipMapsToRemoteHost()
  testTopologyRoutesRenderWithoutTargetRecord()
  console.log('workspace-placement tests passed')
}

main()
