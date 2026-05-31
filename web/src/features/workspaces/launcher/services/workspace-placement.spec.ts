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
    runtimeRelationship: 'managed',
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
    name: 'Managed Laptop',
    role: '',
    relationship: '',
    kind: 'host',
    online: true,
    selectable: true,
    current: false,
    ...overrides,
  }
}

function testManagedHostTopologyRouteIsVisibleFromTopologyBinding(): void {
  const managedRoute = route({ runtimeSwarmName: 'Laptop' })
  assertEqual(workspaceRouteTargetType(managedRoute, null), 'Managed Host', 'topology workspace bindings should be labeled as Managed Host')
  assertEqual(workspaceRoutePlacementLabel(managedRoute, null), 'Laptop (Managed Host)', 'topology placement label should lead with the route name')
}

function testRuntimePathCanProvideVisibleRouteName(): void {
  const pathOnlyRoute = route({ runtimeSwarmName: '', runtimeWorkspacePath: '/srv/workspaces/project' })
  assertEqual(workspaceRouteTargetName(pathOnlyRoute, null), '/srv/workspaces/project', 'runtime path should be visible when no runtime name is stored')
}

function testNonTopologyRoutesAreRemovedFromVisibleLinks(): void {
  const links = workspacePlacementLinks([route({ routeSource: 'workspace/entry/deleted' })], [])
  assertEqual(String(links.length), '0', 'non-topology routes must not render as visible placement links')
}

function testManagedRelationshipMapsToManagedHost(): void {
  const managedTarget = target({ kind: 'host', relationship: 'managed', role: 'managed' })
  assertEqual(workspaceRouteTargetType(route(), managedTarget), 'Managed Host', 'managed relationship should map to Managed Host')
}

function testTopologyRoutesRenderWithoutTargetRecord(): void {
  const links = workspacePlacementLinks([route({ runtimeRelationship: '', runtimeKind: '' })], [])
  assertEqual(String(links.length), '1', 'topology workspace bindings should render even when the target list is unavailable')
  assertEqual(links[0]?.targetType ?? '', 'Managed Host', 'target-less topology workspace binding should still be a Managed Host route')
}

function main(): void {
  testManagedHostTopologyRouteIsVisibleFromTopologyBinding()
  testRuntimePathCanProvideVisibleRouteName()
  testNonTopologyRoutesAreRemovedFromVisibleLinks()
  testManagedRelationshipMapsToManagedHost()
  testTopologyRoutesRenderWithoutTargetRecord()
  console.log('workspace-placement tests passed')
}

main()
