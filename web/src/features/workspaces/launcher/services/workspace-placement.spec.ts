import { workspaceLinkPlacementLabel, workspaceLinkTargetName, workspaceLinkTargetType, workspacePlacementLinks } from './workspace-placement'
import type { SwarmTarget } from '../../../desktop/swarm/api/swarm-targets'
import type { WorkspaceReplicationLink } from '../types/workspace'

function assertEqual(actual: string, expected: string, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`)
  }
}

function link(overrides: Partial<WorkspaceReplicationLink> = {}): WorkspaceReplicationLink {
  return {
    id: 'link-1',
    targetKind: '',
    targetSwarmId: 'target-swarm',
    targetSwarmName: 'Target Name',
    targetWorkspacePath: '/workspace',
    replicationMode: 'bundle',
    writable: true,
    sync: { enabled: false, mode: '' },
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

function testManagedHostLinkKindDoesNotFallBackToGenericTarget(): void {
  const managedLink = link({ targetKind: 'managed_host', targetSwarmName: 'Laptop' })
  assertEqual(workspaceLinkTargetType(managedLink, null), 'Managed Host', 'managed_host links should be labeled as Managed Host')
  assertEqual(workspaceLinkPlacementLabel(managedLink, null), 'Laptop (Managed Host)', 'managed_host placement label should lead with the link name')
}

function testTargetPathCanProvideVisibleLinkName(): void {
  const pathOnlyLink = link({ targetSwarmName: '', targetWorkspacePath: '/srv/workspaces/project' })
  assertEqual(workspaceLinkTargetName(pathOnlyLink, null), '/srv/workspaces/project', 'target path should be visible when no target name is stored')
}

function testDeadLegacyLinksAreRemovedFromVisibleLinks(): void {
  const links = workspacePlacementLinks([link({ targetKind: 'legacy_unused_target' })], [])
  assertEqual(String(links.length), '0', 'dead legacy links should not render as visible placement links')
}

function testManagedRelationshipMapsToManagedHost(): void {
  const managedTarget = target({ kind: 'host', relationship: 'managed', role: 'managed' })
  assertEqual(workspaceLinkTargetType(link({ targetKind: 'managed_host' }), managedTarget), 'Managed Host', 'managed relationship should map to Managed Host')
}

function testDeadLegacyLinksAreHidden(): void {
  assertEqual(workspaceLinkTargetType(link({ targetKind: 'legacy_unused_target' }), null), '', 'dead legacy target kind should not be displayed')
}

function main(): void {
  testManagedHostLinkKindDoesNotFallBackToGenericTarget()
  testTargetPathCanProvideVisibleLinkName()
  testDeadLegacyLinksAreRemovedFromVisibleLinks()
  testManagedRelationshipMapsToManagedHost()
  testDeadLegacyLinksAreHidden()
  console.log('workspace-placement tests passed')
}

main()
