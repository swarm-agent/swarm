import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

async function readRuntimeProvider(): Promise<string> {
  return readFile(new URL('./desktop-v3-runtime-provider.tsx', import.meta.url), 'utf8')
}

async function readRouter(): Promise<string> {
  return readFile(new URL('../../../app/router.tsx', import.meta.url), 'utf8')
}

test('DesktopV3RuntimeProvider synchronously retains one realtime lease with delayed bootstrap', async () => {
  const source = await readRuntimeProvider()

  assert.match(source, /export function DesktopV3RuntimeProvider/)
  assert.match(source, /const ensureRuntime = \(\) => \{\s*if \(!runtimeRef\.current\) \{\s*runtimeRef\.current = retainDesktopV3Runtime\(initialPreferredSessionId\)/s)
  assert.match(source, /ensureRuntime\(\)[\s\S]*?useEffect\(\(\) => \{\s*const runtime = ensureRuntime\(\)/)
  assert.match(source, /const bootstrapReady = bootstrapDesktopV3SidebarMetadataOnly\(\{\s*preferredSessionId: normalizedPreferredSessionId,\s*\}\)/s)
  assert.match(source, /retainDesktopV3RealtimeController\(\{\s*ownerKey: DESKTOP_V3_RUNTIME_PROVIDER_OWNER_KEY,\s*preferredSessionId: normalizedPreferredSessionId,\s*bootstrap: bootstrapReady,/s)
  assert.match(source, /await runtime\.realtimeLease\.ready[\s\S]*?await hydrateDesktopV3InitialSessions/)
})

test('DesktopV3RuntimeProvider wraps the root desktop shell so route changes keep one socket', async () => {
  const source = await readRouter()

  assert.match(source, /component: DesktopRootShell/)
  assert.match(source, /function DesktopRootShell\(\) \{[\s\S]*?<DesktopV3RuntimeProvider initialPreferredSessionId=\{initialPreferredSessionId\}>[\s\S]*?<DesktopVaultShell \/>[\s\S]*?<\/DesktopV3RuntimeProvider>/)
  assert.match(source, /function initialDesktopV3PreferredSessionId\(\): string \| null \| undefined/)
  assert.match(source, /return route\.sessionId \|\| null/)
})
