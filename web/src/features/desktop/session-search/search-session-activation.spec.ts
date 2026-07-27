import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopSessionSearchItem } from './session-search-api'
import { activateSearchSession } from './search-session-activation'

function searchItem(archived: boolean): DesktopSessionSearchItem {
  return {
    id: 'session-1',
    workspace_path: '/workspace',
    workspace_name: 'Workspace',
    title: 'Archived chat',
    mode: 'auto',
    created_at: 1,
    updated_at: 42,
    message_count: 2,
    last_message_at: 41,
    archived,
  }
}

test('active search result opens without unarchive', async () => {
  const calls: string[] = []
  await activateSearchSession(searchItem(false), {
    unarchive: async () => { calls.push('unarchive') },
    openSession: () => { calls.push('open') },
  })

  assert.deepEqual(calls, ['open'])
})

test('archived search result opens only after successful unarchive', async () => {
  const calls: string[] = []
  await activateSearchSession(searchItem(true), {
    unarchive: async (versions) => {
      calls.push(`unarchive:${JSON.stringify(versions)}`)
    },
    openSession: () => { calls.push('open') },
  })

  assert.deepEqual(calls, ['unarchive:{"session-1":42}', 'open'])
})

test('unarchive failure rejects without opening the archived search result', async () => {
  const calls: string[] = []

  await assert.rejects(() => activateSearchSession(searchItem(true), {
    unarchive: async () => { calls.push('unarchive'); throw new Error('version conflict') },
    openSession: () => { calls.push('open') },
  }), /version conflict/)

  assert.deepEqual(calls, ['unarchive'])
})
