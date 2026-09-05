import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./workspace-folder-tree.tsx', import.meta.url), 'utf8')
const sharedIterationSource = await readFile(new URL('../pages/iterations/shared.tsx', import.meta.url), 'utf8')
const iterationSource = await readFile(new URL('../pages/iterations/workspace-launcher-iteration-1.tsx', import.meta.url), 'utf8')

test('workspace explorer requires repository setup instead of temporary non-Git use', () => {
  assert.match(source, /Add folder as a new workspace/)
  assert.match(source, /Add folder \$\{entry\.name\} as a new workspace/)
  assert.match(source, /Choose a committed Git repository/)
  assert.doesNotMatch(source, /Use folder for this chat only|Use as temp|Add as workspace/)
  assert.match(source, /Git required/)
  assert.match(source, /managed worktrees/)
  assert.match(source, /existing files are never staged or committed silently/)
  assert.doesNotMatch(source, /Git is optional|ready to use without Git/)
  assert.doesNotMatch(sharedIterationSource, /Use for this chat only/)
  assert.match(sharedIterationSource, /committed Git repository/)
  assert.match(sharedIterationSource, /Add folder as a new workspace/)
  assert.match(iterationSource, /Swarm accepts only a repository root with an initial commit/)
})
