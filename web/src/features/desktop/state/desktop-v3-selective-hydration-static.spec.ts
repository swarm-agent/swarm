import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const stateDir = __dirname

test('CP3 bootstrap does not directly all-session hydrate response.session_order', () => {
  const source = readFileSync(join(stateDir, 'desktop-v3-bootstrap-controller.ts'), 'utf8')
  assert.doesNotMatch(
    source,
    /hydrateDesktopV3InitialSessions\(\{\s*sessionIds:\s*response\.session_order\s*\?\?\s*\[\],\s*postHydrate/s,
  )
  assert.doesNotMatch(source, /hydrateDesktopV3InitialSessions/)
  assert.match(source, /desktopInitialHydrate\.update/)
  assert.match(source, /hydrateResponseCompletesSession/)
})

test('CP3 adds no production sync stream, reconnect, or realtime calls', () => {
  const productionSources = [
    'desktop-v3-bootstrap-controller.ts',
    'desktop-v3-initial-hydrate-controller.ts',
    'desktop-v3-sync-api.ts',
  ].map((file) => readFileSync(join(stateDir, file), 'utf8')).join('\n')

  assert.doesNotMatch(productionSources, /postDesktopV3SyncStream|postDesktopV3SessionsReconnect/)
  assert.doesNotMatch(productionSources, /fetch\([^)]*\/v3\/sync\/stream/)
  assert.doesNotMatch(productionSources, /fetch\([^)]*\/v3\/sessions:reconnect/)
  assert.doesNotMatch(productionSources, /fetch\([^)]*\/v3\/realtime\/stream/)
})
