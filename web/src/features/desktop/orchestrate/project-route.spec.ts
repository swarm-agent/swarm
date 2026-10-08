import test from 'node:test'
import assert from 'node:assert/strict'
import { projectNameSlug, projectRouteSegment, resolveProjectRoute } from './project-route'

// Purpose: project-route is the browser-name/internal-ID boundary. Pure tests are
// the narrowest proof that reload aliases and old IDs resolve identically, while
// duplicate names and ID/name collisions cannot silently select another project.
test('name URLs round-trip without changing opaque project identities', () => {
  const projects = [{ id: 'proj_alpha', name: 'Swarm Go' }, { id: 'proj_beta', name: 'Design Studio' }]
  assert.equal(projectRouteSegment(projects[0], projects), 'swarm-go')
  assert.equal(resolveProjectRoute('swarm-go', projects), projects[0])
  assert.equal(resolveProjectRoute('proj_alpha', projects), projects[0])
  assert.equal(resolveProjectRoute('missing', projects), undefined)
  assert.equal(resolveProjectRoute('swarm-go', []), undefined)
  assert.equal(projectNameSlug('  Café / 世界!  '), 'cafe-世界')
  assert.equal(projectNameSlug('!!!'), 'project')
  assert.deepEqual(projects.map(p => p.id), ['proj_alpha', 'proj_beta'])
})

test('duplicate and colliding names are deterministic and never guessed', () => {
  const projects = [{ id: 'proj_a', name: 'Same' }, { id: 'proj_b', name: 'Same!' }, { id: 'same', name: 'Third' }]
  for (const project of projects) {
    const segment = projectRouteSegment(project, projects)
    assert.equal(resolveProjectRoute(segment, projects), project)
    assert.equal(projectRouteSegment(project, [...projects].reverse()), segment)
    assert.equal(resolveProjectRoute(project.id, projects), project)
  }
  assert.equal(resolveProjectRoute('same', projects), projects[2])
  assert.equal(resolveProjectRoute('same', projects.slice(0, 2)), undefined)
  const renamed = [{ id: 'proj_a', name: 'Renamed' }]
  assert.equal(resolveProjectRoute('proj_a', renamed), renamed[0])
  assert.equal(resolveProjectRoute('same', renamed), undefined)
})
