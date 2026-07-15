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
    runtimeKind: 'container',
    runtimeRelationship: 'child',
    authorityHostSwarmId: 'host-swarm',
    hostSwarmId: 'host-swarm',
    hostWorkspacePath: '/source',
    hostWorkspaceName: 'Source',
    runtimeWorkspacePath: '/workspace',
    writable: true,
    createdAt: 0,
    updatedAt: 0,
    ...overrides,
  }
}

function target(overrides: Partial<SwarmTarget> = {}): SwarmTarget {
  return {
    swarm_id: 'target-swarm',
    name: 'Container target',
    role: '',
    relationship: '',
    kind: 'host',
    online: true,
    selectable: true,
    current: false,
    ...overrides,
  }
}

function testContainerTargetTopologyRouteIsVisibleFromTopologyBinding(): void {
  const targetRoute = route({ runtimeSwarmName: 'Worker' })
  assertEqual(workspaceRouteTargetType(targetRoute, null), 'Container', 'container topology bindings should use the container label')
  assertEqual(workspaceRoutePlacementLabel(targetRoute, null), 'Worker (Container)', 'topology placement label should lead with the route name')
}

function testRuntimePathCanProvideVisibleRouteName(): void {
  const pathOnlyRoute = route({ runtimeSwarmName: '', runtimeWorkspacePath: '/srv/workspaces/project' })
  assertEqual(workspaceRouteTargetName(pathOnlyRoute, null), '/srv/workspaces/project', 'runtime path should be visible when no runtime name is stored')
}

function testNonTopologyRoutesAreRemovedFromVisibleLinks(): void {
  const links = workspacePlacementLinks([route({ routeSource: 'workspace/entry/deleted' })], [])
  assertEqual(String(links.length), '0', 'non-topology routes must not render as visible placement links')
}

function testSelfHostMapsToHost(): void {
  const selfTarget = target({ kind: 'host', relationship: 'self', role: 'self' })
  assertEqual(workspaceRouteTargetType(route({ runtimeRelationship: 'self' }), selfTarget), 'Host', 'self host target should map to Host')
}

function testTopologyRoutesRenderWithoutTargetRecord(): void {
  const links = workspacePlacementLinks([route({ runtimeRelationship: '', runtimeKind: '' })], [])
  assertEqual(String(links.length), '1', 'topology workspace bindings should render even when the target list is unavailable')
  assertEqual(links[0]?.targetType ?? '', 'Target', 'target-less topology workspace binding should remain a generic target route')
}

function main(): void {
  testContainerTargetTopologyRouteIsVisibleFromTopologyBinding()
  testRuntimePathCanProvideVisibleRouteName()
  testNonTopologyRoutesAreRemovedFromVisibleLinks()
  testSelfHostMapsToHost()
  testTopologyRoutesRenderWithoutTargetRecord()
  console.log('workspace-placement tests passed')
}

main()
