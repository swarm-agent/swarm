import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('Codex usage modal wires refresh, idempotent redemption outcomes, and accessible account controls', async () => {
  const source = await readFile(new URL('./desktop-codex-usage-modal.tsx', import.meta.url), 'utf8')

  assert.match(source, /Promise\.allSettled\(\[fetchCodexAccountUsage\(\), fetchCodexResetCredits\(\)\]\)/)
  assert.match(source, /redeemKeys\.current\.get\(credit\.id\)/)
  assert.match(source, /redeemKeys\.current\.set\(credit\.id, key\)/)
  assert.match(source, /result\.code === 'reset' \|\| result\.code === 'already_redeemed'/)
  assert.match(source, /result\.code === 'nothing_to_reset'/)
  assert.match(source, /Retry uses the same redemption key/)
  assert.match(source, /await refresh\(\)/)
  assert.match(source, /aria-label="Codex usage"/)
  assert.match(source, /Open Auth settings/)
})

test('Codex usage modal shows an accessible spoke spinner until initial account data settles', async () => {
  const source = await readFile(new URL('./desktop-codex-usage-modal.tsx', import.meta.url), 'utf8')

  assert.match(source, /const initialLoading = !usage && !credits && !usageError && !creditsError/)
  assert.match(source, /role="status" aria-label="Loading Codex account usage"/)
  assert.match(source, /<LoaderCircle[^>]+animate-spin/)
})

test('Desktop app preserves the dedicated Codex modal without the removed slash action', async () => {
  const source = await readFile(new URL('../layout/desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.match(source, /DesktopCodexUsageModal/)
  assert.doesNotMatch(source, /action\.kind === 'open-codex-usage'/)
})
