import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

assert.match(
  source,
  /const hasExplicitSingleModel = Boolean\(profile\?\.provider\.trim\(\) \|\| profile\?\.model\.trim\(\)\)/,
  'single draft must distinguish default-mode agents from explicit single-model locks',
)

assert.match(
  source,
  /serviceTier: hasExplicitSingleModel \? normalizeDraftServiceTier\(provider, profile\?\.autoServiceTier \?\? ''\) : fallback\.serviceTier/,
  'default-mode draft must preserve the resolved default service tier instead of reading the cleared agent autoServiceTier field',
)
