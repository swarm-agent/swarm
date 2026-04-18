import { buildWorkspaceRouteSlugMap, resolveWorkspaceBySlug, workspaceRouteSlugBase } from './workspace-route'

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message)
  }
}

function testReservedSwarmSlugIsRemapped(): void {
  const slug = workspaceRouteSlugBase({
    path: '/workspaces/swarm',
    workspaceName: 'swarm',
  })
  assert(slug === 'swarm-workspace', `expected reserved slug to be remapped, got ${slug}`)
}

function testReservedSwarmSlugResolvesSavedWorkspace(): void {
  const workspaces = [
    { path: '/workspaces/swarm', workspaceName: 'swarm' },
  ]
  const slugByPath = buildWorkspaceRouteSlugMap(workspaces)
  assert(
    slugByPath.get('/workspaces/swarm') === 'swarm-workspace',
    `expected saved workspace slug to avoid /swarm, got ${slugByPath.get('/workspaces/swarm')}`,
  )

  const resolved = resolveWorkspaceBySlug(workspaces, 'swarm-workspace')
  assert(resolved?.path === '/workspaces/swarm', 'expected remapped slug to resolve the saved workspace')
}

function testReservedRemapStillDisambiguatesDuplicates(): void {
  const workspaces = [
    { path: '/workspaces/a', workspaceName: 'swarm' },
    { path: '/workspaces/b', workspaceName: 'swarm workspace' },
  ]
  const slugByPath = buildWorkspaceRouteSlugMap(workspaces)
  const first = slugByPath.get('/workspaces/a') ?? ''
  const second = slugByPath.get('/workspaces/b') ?? ''

  assert(first.startsWith('swarm-workspace-'), `expected duplicate reserved slug to be hashed, got ${first}`)
  assert(second.startsWith('swarm-workspace-'), `expected duplicate slug to be hashed, got ${second}`)
  assert(first !== second, 'expected duplicate workspace slugs to remain unique')
}

function main(): void {
  testReservedSwarmSlugIsRemapped()
  testReservedSwarmSlugResolvesSavedWorkspace()
  testReservedRemapStillDisambiguatesDuplicates()
  console.log('workspace-route tests passed')
}

main()
