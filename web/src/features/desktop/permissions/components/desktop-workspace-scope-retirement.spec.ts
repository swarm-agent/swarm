import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const modalSource = await readFile(new URL('./desktop-permission-modal.tsx', import.meta.url), 'utf8')
const payloadSource = await readFile(new URL('../services/permission-payload.ts', import.meta.url), 'utf8')

test('workspace-scope permissions offer only session access after persistent add-dir retirement', () => {
  assert.doesNotMatch(modalSource, /payload\.addToWorkspace/)
  assert.doesNotMatch(payloadSource, /label: 'Add To Workspace'/)
  assert.doesNotMatch(payloadSource, /workspace_add_dir/)
  assert.match(payloadSource, /const normalizedDecision = 'session_allow'/)
  assert.match(payloadSource, /label: 'Allow temporarily for this chat'/)
  assert.match(payloadSource, /add it as a new workspace from Workspaces/)
  assert.match(modalSource, /Temporary access root/)
  assert.match(modalSource, /This chat only/)
  assert.doesNotMatch(modalSource, /Session scope root/)
})
