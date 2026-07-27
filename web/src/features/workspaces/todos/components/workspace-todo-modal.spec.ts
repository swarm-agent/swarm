import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

test('AI tasks display state and link to their managed session', async () => {
  const source = await readFile(new URL('./workspace-todo-modal.tsx', import.meta.url), 'utf8')
  assert.match(source, /item\.aiState/)
  assert.match(source, /AI · \$\{stateLabel\}/)
  assert.match(source, /item\.managedSessionId && onOpenManagedSession/)
  assert.match(source, /aria-label="Open managed session"/)
  assert.match(source, /const allowMutations = !aiTaskActive/)
  assert.match(source, /disabled=\{!allowMutations\}/)
  assert.match(source, /userActiveAITaskCount > 0/)
})
