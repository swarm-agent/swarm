import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./workspace-editor-modal.tsx', import.meta.url), 'utf8')

test('workspace editor modal facilitates Git init and file commit menu', () => {
  // Verifies the modal includes Git init facilitation and commit review menu elements
  assert.match(source, /Initialize Git repository and choose files to commit/)
  assert.match(source, /Choose files to commit/)
  assert.match(source, /Commit files to initial Git commit/)
  assert.match(source, /Select which files or folders to include in the initial Git commit/)
  assert.match(source, /Select all/)
  assert.match(source, /Deselect all/)
  assert.match(source, /I understand omitted files will not enter managed worktrees/)
  assert.match(source, /Initialize Git and create workspace/)
  assert.match(source, /handleCommitBaseline/)
  assert.match(source, /onPrepareBaseline/)
  assert.match(source, /reviewRepository/)
  assert.match(source, /prepareBaseline/)
})
