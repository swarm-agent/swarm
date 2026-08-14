import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('./image-tool-page.tsx', import.meta.url), 'utf8')

const codexSlowWarning = 'OAuth only. Codex image generation is really slow and may not work well for image swarms.'

test('Codex default image model warns about slow generation and image swarm suitability', () => {
  assert.equal(source.includes(`helper: '${codexSlowWarning}'`), true)
  assert.match(source, /\{option\.helper\}/)
  assert.match(source, /activeImageDefaultOption\.kind === 'codex-image-gen' \? 'text-\[var\(--app-warning\)\]'/)
})

test('artifact viewer copies the canonical URL for the selected image iteration', () => {
  assert.match(source, /function canonicalImageIterationURL\(threadId: string, assetId: string\)/)
  assert.match(source, /new URL\(imageAssetURL\(threadId, assetId\), window\.location\.origin\)\.toString\(\)/)
  assert.match(source, /selectedIterationURL = selectedThread && selectedImageAsset/)
  assert.match(source, /copyTextToClipboard\(selectedIterationURL\)/)
  assert.doesNotMatch(source, /copyTextToClipboard\(selectedSessionURL\)/)
})
