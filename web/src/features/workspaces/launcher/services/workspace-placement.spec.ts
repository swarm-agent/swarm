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
    name: 'Worker target',
    role: '',
    relationship: '',
    kind: 'host',
    online: true,
    selectable: true,
    current: false,
    ...overrides,
  }
}

function testFormerContainerTargetTopologyRouteIsVisibleAsGenericTarget(): void {
  const targetRoute = route({ runtimeSwarmName: 'Worker' })
  const formerContainerTarget = target({ kind: 'container', relationship: 'child' })
  const links = workspacePlacementLinks([targetRoute], [formerContainerTarget])
  assertEqual(String(links.length), '1', 'former container-shaped topology bindings must remain visible')
  assertEqual(links[0]?.targetType ?? '', 'Target', 'former container-shaped targets should use the generic target label')
  assertEqual(workspaceRoutePlacementLabel(targetRoute, formerContainerTarget), 'Worker target (Target)', 'topology placement label should use the resolved generic target')
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
  testFormerContainerTargetTopologyRouteIsVisibleAsGenericTarget()
  testRuntimePathCanProvideVisibleRouteName()
  testNonTopologyRoutesAreRemovedFromVisibleLinks()
  testSelfHostMapsToHost()
  testTopologyRoutesRenderWithoutTargetRecord()
  console.log('workspace-placement tests passed')
}

main()
