import assert from 'node:assert/strict'
import test from 'node:test'
import { videoThreadFromSessionProject } from '../pages/video-tool-page'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'

// Requirement: a deep link must not synthesize a loaded session before canonical
// hydration. Threat: failed auth appears as an empty project and triggers ensure.
// The route-to-thread adapter is the narrowest guard; live auth remains separate.
test('deep-link adapter waits for hydrated session and retains a real session absent from list', () => {
  const workspace = { path: '/workspace', workspaceName: 'Demo' } as WorkspaceEntry
  assert.equal(videoThreadFromSessionProject('session', workspace), null)
  assert.equal(videoThreadFromSessionProject('session', null, { title: 'Film' }), null)
  assert.equal(videoThreadFromSessionProject('session', workspace, { title: 'Film' })?.title, 'Film')
  assert.equal(videoThreadFromSessionProject('session', workspace, { title: 'Film' })?.id, 'session')
})
