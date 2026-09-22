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

test('workspace editor modal facilitates switching to repository root when in subdirectory', () => {
  // Verifies the modal detects subdirectories of Git repositories and provides quick actions
  assert.match(source, /isInsideSubdirectory/)
  assert.match(source, /Use repository root and add workspace/)
  assert.match(source, /Initialize as independent Git repository/)
  assert.match(source, /Select repository root path/)
  assert.match(source, /Switch to repository root/)
  assert.match(source, /onUseRepositoryRoot/)
})

test('workspace editor modal facilitates committed HEAD confirmation when uncommitted changes exist', () => {
  // Verifies the modal handles repositories with uncommitted files and offers one-click committed-only confirmation
  assert.match(source, /hasUncommittedContent/)
  assert.match(source, /Uncommitted changes detected/)
  assert.match(source, /Add workspace using committed HEAD/)
  assert.match(source, /onConfirmCommittedOnly/)
})
