import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./workspace-home-page.tsx', import.meta.url), 'utf8')
const editorSource = await readFile(new URL('../launcher/components/workspace-editor-modal.tsx', import.meta.url), 'utf8')

test('workspace home exposes a flat global catalog without linked-folder controls', () => {
  assert.match(source, /Pinned workspaces/)
  assert.match(source, /All workspaces/)
  assert.doesNotMatch(source, /linkedDirectories|addLinkedDirectories|removeLinkedDirectory/)
  assert.match(source, /Add folder as a new workspace/)
  assert.doesNotMatch(source, /Use folder for this chat only|Folder used for this chat only|Use current folder as temp|Make workspace/)
  assert.match(source, /Navigate folders and add a committed Git repository as a workspace/)
  assert.match(source, /WorkspaceRepositoryPrerequisiteError/)
  assert.match(source, /openCreateModal\(error\.repository\.path \|\| path/)
  assert.match(editorSource, /Initialize Git repository/)
  assert.match(editorSource, /Ask Swarm to help set up this repository/)
  assert.match(source, /postDesktopV3BackgroundRouterSessionStart/)
  assert.match(source, /launched\.session_id/)
})

test('workspace creation facilitates Git init and file commit review menu', () => {
  assert.match(source, /prepareBaselineForDraft/)
  assert.match(source, /onPrepareBaseline=\{prepareBaselineForDraft\}/)
  assert.match(source, /useRepositoryRootForDraft/)
  assert.match(source, /onUseRepositoryRoot=/)
  assert.match(editorSource, /Commit files to initial Git commit/)
  assert.match(editorSource, /Initialize Git repository and choose files to commit/)
  assert.match(editorSource, /Choose files to commit/)
  assert.match(editorSource, /Select all/)
  assert.match(editorSource, /Deselect all/)
  assert.match(editorSource, /I understand omitted files will not enter managed worktrees/)
  assert.match(editorSource, /handleCommitBaseline/)
})
