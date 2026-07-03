import test from 'node:test'
import assert from 'node:assert/strict'

import { composeDesktopDocumentTitle, desktopAreaLabel } from './desktop-document-title'
import type { DesktopV3CacheState, SessionSnapshot } from '../state/desktop-v3-cache-types'

function session(id: string, title: string): SessionSnapshot {
  return {
    id,
    workspace_path: '/tmp/workspace',
    workspace_name: 'Workspace',
    title,
    mode: 'auto',
    created_at: 1,
    updated_at: 2,
    message_count: 0,
    last_message_at: 0,
  }
}

test('composeDesktopDocumentTitle prefers route session title and unread prefix', () => {
  const sessionsById: DesktopV3CacheState['sessionsById'] = {
    'session-1': { kind: 'full', session: session('session-1', 'Build notifications'), needsHydrate: false },
  }

  assert.equal(composeDesktopDocumentTitle({
    pathname: '/workspace/session-1',
    unreadCount: 3,
    sessionsById,
  }), '(3) Build notifications')

  assert.equal(composeDesktopDocumentTitle({
    pathname: '/integrations/session-1',
    unreadCount: 0,
    sessionsById,
  }), 'Build notifications')
})

test('composeDesktopDocumentTitle falls back to contextual area labels', () => {
  assert.equal(desktopAreaLabel('/'), 'Workspace Launcher')
  assert.equal(desktopAreaLabel('/settings'), 'Settings')
  assert.equal(desktopAreaLabel('/workspace/settings'), 'Settings')
  assert.equal(desktopAreaLabel('/integrations'), 'Integrations')
  assert.equal(desktopAreaLabel('/tools/image/thread-1'), 'Image Tool')
  assert.equal(desktopAreaLabel('/workspace/tools/video'), 'Video Tool')
  assert.equal(desktopAreaLabel('/workspace'), 'Swarm Desktop')

  assert.equal(composeDesktopDocumentTitle({
    pathname: '/tools/video',
    unreadCount: 2,
    sessionsById: {},
  }), '(2) Video Tool')
})
