import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./workspace-home-page.tsx', import.meta.url), 'utf8')

test('workspace home exposes a flat global catalog without linked-folder controls', () => {
  assert.match(source, /Pinned workspaces/)
  assert.match(source, /All workspaces/)
  assert.doesNotMatch(source, /linkedDirectories|addLinkedDirectories|removeLinkedDirectory/)
  assert.match(source, /Use folder for this chat only/)
  assert.match(source, /Add folder as a new workspace/)
  assert.match(source, /Folder used for this chat only/)
  assert.doesNotMatch(source, /Use current folder as temp|Make workspace/)
})
