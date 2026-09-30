import test from 'node:test'
import assert from 'node:assert/strict'
import { isSwarmSection, swarmPageLink, swarmWorkerHref, swarmWorkerLink, SWARM_SECTIONS } from './swarm-navigation'
import { composeDesktopDocumentTitle } from '../runtime/desktop-document-title'

// Requirement: every Swarm sidebar page has one global/workspace-scoped
// destination. swarmPageLink owns both Link props and imperative navigation.
// This narrow unit layer prevents scope loss and missing section destinations;
// it does not claim browser reload, history, or rendered anchor verification.
test('Swarm page destinations preserve workspace scope for every section', () => {
  assert.deepEqual(swarmPageLink(undefined, 'home'), { to: '/swarm' })
  assert.deepEqual(swarmPageLink('example-workspace', 'home'), {
    to: '/$workspaceSlug/swarm', params: { workspaceSlug: 'example-workspace' },
  })
  for (const page of SWARM_SECTIONS) {
    assert.deepEqual(swarmPageLink(undefined, page), {
      to: '/swarm/$swarmSection', params: { swarmSection: page },
    })
    assert.deepEqual(swarmPageLink('example-workspace', page), {
      to: '/$workspaceSlug/swarm/$swarmSection',
      params: { workspaceSlug: 'example-workspace', swarmSection: page },
    })
  }
})

// Requirement: the router's section guard accepts only registered pages;
// arbitrary strings must not silently render the home page.
test('Swarm section guard rejects unknown or malformed sections', () => {
  for (const section of SWARM_SECTIONS) assert.equal(isSwarmSection(section), true)
  for (const section of [undefined, null, {}, '', 'home', 'unknown', '../media', 'Media']) {
    assert.equal(isSwarmSection(section), false)
  }
})

// Requirement: bookmarks/browser tabs identify Swarm pages, not cached chat
// records whose IDs happen to match a reserved section or the root page.
// composeDesktopDocumentTitle owns this display boundary.
test('Swarm page titles cannot be replaced by colliding session titles', () => {
  const sessionsById = {
    swarm: { kind: 'full', session: { id: 'swarm', title: 'Unrelated session' } },
    media: { kind: 'full', session: { id: 'media', title: 'Unrelated media session' } },
  } as unknown as Parameters<typeof composeDesktopDocumentTitle>[0]['sessionsById']
  for (const pathname of ['/swarm', '/example-workspace/swarm']) {
    assert.equal(composeDesktopDocumentTitle({ pathname, unreadCount: 0, sessionsById }), 'Swarm')
  }
  for (const pathname of ['/swarm/media', '/example-workspace/swarm/media']) {
    assert.equal(composeDesktopDocumentTitle({ pathname, unreadCount: 2, sessionsById }), '(2) Swarm · Media')
  }
})

// Requirement: worker inspection stays under the persistent swarm-layout owner.
// Threat: moving to the conversation layout remounts Orchestrator and loses drafts.
// Boundary: swarmWorkerLink; narrow unit assertions prove destination/scope, not live chat preservation.
test('granular worker navigation preserves workspace scope', () => {
  assert.deepEqual(swarmWorkerLink(undefined, 'worker-1'), {
    to: '/swarm/$swarmSection',
    params: { swarmSection: 'workers' },
    search: { workerId: 'worker-1' },
  })
  assert.deepEqual(swarmWorkerLink('my-workspace', 'worker-1'), {
    to: '/$workspaceSlug/swarm/$swarmSection',
    params: { workspaceSlug: 'my-workspace', swarmSection: 'workers' },
    search: { workerId: 'worker-1' },
  })
  assert.equal(swarmWorkerHref(undefined, 'worker-1'), '/workers/worker-1')
  assert.equal(swarmWorkerHref('my-workspace', 'worker-1'), '/my-workspace/workers/worker-1')
})
