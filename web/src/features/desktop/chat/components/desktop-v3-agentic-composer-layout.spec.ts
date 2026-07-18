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

test('Desktop V3 composer hides compact controls unless the persisted preference is enabled', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

  assert.match(source, /showCompactButton = false/)
  assert.equal((source.match(/showCompactButton && onCompact \? compactButton\(\) : null/g) ?? []).length, 2)
  assert.doesNotMatch(source, /mobile.*plus|plus.*menu/i)
})

test('Desktop V3 composer uses the canonical joined plan and model control without iteration naming', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const control = await readFile(new URL('./composer-plan-model-control.tsx', import.meta.url), 'utf8')

  assert.equal((source.match(/<ComposerPlanModelControl/g) ?? []).length, 1)
  assert.equal((source.match(/renderComposerControl\(openPicker, open\)/g) ?? []).length, 2)
  assert.doesNotMatch(source, /iteration/i)
  assert.doesNotMatch(source, /<ModePicker/)
  assert.match(control, /data-composer-plan-model-control/)
  assert.doesNotMatch(control, /iteration/i)
  assert.match(control, /aria-pressed=\{planEnabled\}/)
  assert.match(control, /aria-haspopup="menu"/)
  assert.match(control, />\{modelLabel\}<\/span>/)
  assert.doesNotMatch(control, /profileName|profileLabel|primaryLabel|showSeparateModel|\bBot\b|agentName|Swarm/)
})
