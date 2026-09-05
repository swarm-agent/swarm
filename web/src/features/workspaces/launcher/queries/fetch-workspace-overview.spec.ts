import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./fetch-workspace-overview.ts', import.meta.url), 'utf8')

test('workspace overview fetches every catalog page instead of truncating the global list', () => {
  assert.match(source, /limit: '100'/)
  assert.match(source, /include_discovered: 'false'/)
  assert.match(source, /workspaces\.push\(\.\.\.\(response\.workspaces \?\? \[\]\)\)/)
  assert.match(source, /response\.has_more/)
  assert.match(source, /response\.next_cursor/)
  assert.match(source, /while \(cursor > 0\)/)
})
