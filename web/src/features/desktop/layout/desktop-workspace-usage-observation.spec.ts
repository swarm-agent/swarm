import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')

test('workspace usage projections remain observable cache inputs', () => {
  assert.match(source, /JSON\.stringify\(a\.workspace_grants \?\? \[\]\)/)
  assert.match(source, /JSON\.stringify\(a\.workspace_usage \?\? \[\]\)/)
  assert.match(source, /JSON\.stringify\(left\.workspace_usage \?\? \[\]\)/)
  assert.match(source, /workspaceUsage: row\.projection\?\.workspace_usage \?\? session\.workspace_usage \?\? \[\]/)
  assert.match(source, /primaryWorkspaceUsage\(session\.workspaceUsage\)/)
  assert.match(source, /workspacePathById\?\.get\(usageWorkspaceID\)\?\.trim\(\) \?\? ''/)
})
