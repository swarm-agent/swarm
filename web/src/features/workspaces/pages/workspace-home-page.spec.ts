import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./workspace-home-page.tsx', import.meta.url), 'utf8')

test('workspace home exposes a flat global catalog without linked-folder controls', () => {
  assert.match(source, /Pinned workspaces/)
  assert.match(source, /All workspaces/)
  assert.doesNotMatch(source, /linkedDirectories|addLinkedDirectories|removeLinkedDirectory/)
  assert.match(source, /Add folder as a new workspace/)
  assert.doesNotMatch(source, /Use folder for this chat only|Folder used for this chat only|Use current folder as temp|Make workspace/)
  assert.match(source, /Navigate folders and add a committed Git repository as a workspace/)
  assert.match(source, /WorkspaceRepositoryPrerequisiteError/)
  assert.match(source, /openCreateModal\(error\.repository\.path \|\| path/)
  assert.match(source, /Initialize Git repository/)
  assert.match(source, /Ask Swarm to help set up this repository/)
  assert.match(source, /postDesktopV3BackgroundRouterSessionStart/)
  assert.match(source, /launched\.session_id/)
})
