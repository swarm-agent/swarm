import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./workspace-home-page.tsx', import.meta.url), 'utf8')

test('editing a workspace submits only newly added linked directories', () => {
  assert.match(
    source,
    /const alreadyLinkedDirectories = new Set\([\s\S]*modalState\.mode === 'edit'[\s\S]*editingWorkspace\?\.directories \?\? \[\][\s\S]*\)/,
  )
  assert.match(source, /alreadyLinkedDirectories\.has\(comparePath\)/)
  assert.match(source, /linkedDirectories,\s*\}\)/)
})
