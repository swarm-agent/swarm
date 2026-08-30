import assert from 'node:assert/strict'
import test from 'node:test'

import { buildDesktopSlashPaletteState, parseDesktopTaskCommand } from './slash-commands'

test('/task defaults to Auto and preserves its request', () => {
  const parsed = parseDesktopTaskCommand('/task Fix the API routing')
  assert.deepEqual(parsed, { mode: 'auto', request: 'Fix the API routing' })
  assert.equal(buildDesktopSlashPaletteState('/task Fix the API routing').exactMatch?.id, 'task')
})

test('/task --workspace selects a saved workspace without changing Auto mode', () => {
  const parsed = parseDesktopTaskCommand('/task --workspace swarm-web Fix the deployment status page')
  assert.deepEqual(parsed, {
    mode: 'auto', request: 'Fix the deployment status page', workspaceSelector: 'swarm-web',
  })
})

test('/task plan --workspace preserves a quoted saved-workspace selector', () => {
  const parsed = parseDesktopTaskCommand('/task plan --workspace "Swarm Web" Audit deployment recovery')
  assert.deepEqual(parsed, {
    mode: 'plan', request: 'Audit deployment recovery', workspaceSelector: 'Swarm Web',
  })
  assert.equal(buildDesktopSlashPaletteState('/task plan Audit deployment recovery').exactMatch?.id, 'task-plan')
})

test('/task rejects a missing leading workspace selector without consuming request text later', () => {
  assert.deepEqual(parseDesktopTaskCommand('/task --workspace'), {
    mode: 'auto', request: '', workspaceSelectionInvalid: true,
  })
  assert.deepEqual(parseDesktopTaskCommand('/task Fix --workspace handling'), {
    mode: 'auto', request: 'Fix --workspace handling',
  })
})
