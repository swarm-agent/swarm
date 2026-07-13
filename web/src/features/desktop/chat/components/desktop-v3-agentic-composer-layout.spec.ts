import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('Desktop V3 composer keeps its frame height stable when the input receives focus', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const frameClass = source.match(/DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME = "([^"]+)"/)?.[1]

  assert.ok(frameClass, 'expected the canonical composer frame class')
  assert.doesNotMatch(frameClass, /focus-within:(?:p|m)[tblrxy]?-/)
  assert.match(frameClass, /pb-\[calc\(0\.75rem\+var\(--app-safe-area-bottom\)\)\]/)
})
