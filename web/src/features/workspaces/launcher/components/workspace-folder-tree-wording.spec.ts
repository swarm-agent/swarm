import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./workspace-folder-tree.tsx', import.meta.url), 'utf8')
const sharedIterationSource = await readFile(new URL('../pages/iterations/shared.tsx', import.meta.url), 'utf8')
const iterationSource = await readFile(new URL('../pages/iterations/workspace-launcher-iteration-1.tsx', import.meta.url), 'utf8')

test('workspace explorer distinguishes chat-only folder use from creating a new workspace', () => {
  assert.match(source, /Use folder for this chat only/)
  assert.match(source, /Add folder as a new workspace/)
  assert.match(source, /Add folder \$\{entry\.name\} as a new workspace/)
  assert.doesNotMatch(source, /Use as temp|Add as workspace/)
  assert.match(sharedIterationSource, /Use for this chat only/)
  assert.match(sharedIterationSource, /Add folder as a new workspace/)
  assert.match(iterationSource, /use one for this chat only, or add it as a new workspace/)
})
