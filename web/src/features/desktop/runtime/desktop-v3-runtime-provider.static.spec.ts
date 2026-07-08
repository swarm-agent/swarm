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
  assert.match(source, /await runtime\.bootstrapReady[\s\S]*?void runtime\.realtimeLease\.ready\.catch/)
  assert.doesNotMatch(source, /await runtime\.realtimeLease\.ready/)
  assert.doesNotMatch(source, /desktopInitialHydrate\.update|hydrateDesktopV3InitialSessions|ensureSessionConnected|requireDesktopV3RealtimeControllerReady/)
})

test('warm restore does not force startup network hydrate', async () => {
  const source = await readRuntimeProvider()

  assert.doesNotMatch(source, /forceNetworkHydrate:\s*true/)
  assert.doesNotMatch(source, /await hydrateDesktopV3InitialSessions\(/)
})

test('DesktopV3RuntimeProvider wraps only the unlocked desktop shell after onboarding gates pass', async () => {
  const source = await readRouter()
  const runtimeSource = await readRuntimeProvider()
  const vaultSource = await readFile(new URL('../vault/components/desktop-vault-shell.tsx', import.meta.url), 'utf8')

  assert.match(source, /component: DesktopRootShell/)
  assert.match(source, /function DesktopRootShell\(\) \{[\s\S]*?<DesktopVaultShell initialPreferredSessionId=\{initialPreferredSessionId\} \/>/)
  assert.doesNotMatch(source, /<DesktopV3RuntimeProvider/)
  assert.match(vaultSource, /<DesktopV3RuntimeProvider initialPreferredSessionId=\{initialPreferredSessionId\}>[\s\S]*?<Outlet \/>[\s\S]*?<\/DesktopV3RuntimeProvider>/)
  assert.match(runtimeSource, /bootstrapDesktopV3SidebarMetadataOnly/)
  assert.match(source, /function initialDesktopV3PreferredSessionId\(\): string \| null \| undefined/)
  assert.match(source, /return route\.sessionId \|\| null/)
})
