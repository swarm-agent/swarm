import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import test from 'node:test'

const repoRoot = path.resolve(new URL('../../../../../', import.meta.url).pathname)
const webRoot = path.join(repoRoot, 'web')
const desktopRoot = path.join(webRoot, 'src/features/desktop')
const stateRoot = path.join(desktopRoot, 'state')
const packageJsonPath = path.join(webRoot, 'package.json')

const v3RuntimeStorePath = path.join(desktopRoot, 'v3-runtime/v3-store.ts')
const legacyExternalStoreFacadePath = path.join(stateRoot, 'desktop-state-store.ts')
const reducerPath = path.join(stateRoot, 'desktop-state.ts')
const reducerSpecPath = path.join(stateRoot, 'desktop-state.spec.ts')
const streamPath = path.join(stateRoot, 'desktop-state-stream.ts')
const realtimeClientPath = path.join(desktopRoot, 'realtime/client.ts')
const legacyDesktopDbPath = path.join(stateRoot, 'desktop-db.ts')

const forbiddenPackageNames = ['@tanstack/db', '@tanstack/react-db'] as const

const forbiddenDesktopDbPatterns: Array<{ label: string; pattern: RegExp }> = [
  { label: '@tanstack/db import', pattern: /(?:from\s+['"]@tanstack\/db['"]|import\s*\([^)]*['"]@tanstack\/db['"])/ },
  { label: '@tanstack/react-db import', pattern: /(?:from\s+['"]@tanstack\/react-db['"]|import\s*\([^)]*['"]@tanstack\/react-db['"])/ },
  { label: 'useLiveQuery', pattern: /\buseLiveQuery\b/ },
  { label: 'createCollection', pattern: /\bcreateCollection\s*(?:<|\()/ },
  { label: 'localOnlyCollectionOptions', pattern: /\blocalOnlyCollectionOptions\b/ },
  { label: 'BasicIndex', pattern: /\bBasicIndex\b/ },
  { label: 'TanStack Collection type', pattern: /\bCollection\s*</ },
  { label: 'Desktop DB collection symbol', pattern: /\bdesktop[A-Za-z0-9]*Collection\b/ },
]

const legacyCanonicalMutationHelpers = [
  'mergeDesktopDBDurablePatch',
  'applyDurableEventToDesktopDB',
  'applyOptimisticRunStartToDesktopDB',
  'applyRunIntentToDesktopDB',
  'desktopPlansCollection',
  'ensureDesktopDBRouteSession',
]

function readText(filePath: string): string {
  return readFileSync(filePath, 'utf8')
}

function readJson<T>(filePath: string): T {
  return JSON.parse(readText(filePath)) as T
}

function walkFiles(root: string): string[] {
  const entries = readdirSync(root)
  const files: string[] = []
  for (const entry of entries) {
    const fullPath = path.join(root, entry)
    const stat = statSync(fullPath)
    if (stat.isDirectory()) {
      files.push(...walkFiles(fullPath))
      continue
    }
    if (/\.(ts|tsx)$/.test(entry)) {
      files.push(fullPath)
    }
  }
  return files
}

function desktopRelative(filePath: string): string {
  return path.relative(desktopRoot, filePath).split(path.sep).join('/')
}

function stateRelative(filePath: string): string {
  return path.relative(stateRoot, filePath).split(path.sep).join('/')
}

function isTestFile(filePath: string): boolean {
  return /(?:^|\.)(?:spec|test|e2e\.spec)\.tsx?$/.test(path.basename(filePath))
}

function productionDesktopFiles(): string[] {
  return walkFiles(desktopRoot).filter((filePath) => !isTestFile(filePath))
}

function productionStateFiles(): string[] {
  return walkFiles(stateRoot).filter((filePath) => !isTestFile(filePath))
}

function matchingLineNumbers(source: string, pattern: RegExp): number[] {
  return source
    .split('\n')
    .map((line, index) => ({ line, number: index + 1 }))
    .filter(({ line }) => pattern.test(line))
    .map(({ number }) => number)
}

function formatOffenders(offenders: string[]): string {
  return offenders.length === 0 ? '(none)' : `\n${offenders.map((offender) => `- ${offender}`).join('\n')}`
}

function assertNoOffenders(message: string, offenders: string[]): void {
  assert.deepEqual(offenders, [], `${message}${formatOffenders(offenders)}`)
}

function assertFileExists(filePath: string, label: string): void {
  assert.equal(existsSync(filePath), true, `missing ${label}: ${path.relative(repoRoot, filePath)}`)
}

test('Desktop V3 package dependencies do not include TanStack DB authority packages', () => {
  const pkg = readJson<{ dependencies?: Record<string, string>; devDependencies?: Record<string, string> }>(packageJsonPath)
  const dependencyScopes = [pkg.dependencies ?? {}, pkg.devDependencies ?? {}]
  const offenders = forbiddenPackageNames.filter((packageName) => dependencyScopes.some((dependencies) => dependencies[packageName]))

  assertNoOffenders('Desktop V3 must not depend on TanStack DB authority packages.', offenders)
})

test('Desktop production source does not import or instantiate TanStack DB collections', () => {
  const offenders = productionDesktopFiles().flatMap((filePath) => {
    const source = readText(filePath)
    const relative = desktopRelative(filePath)
    return forbiddenDesktopDbPatterns.flatMap(({ label, pattern }) => {
      const lines = matchingLineNumbers(source, pattern)
      return lines.map((line) => `${relative}:${line} ${label}`)
    })
  }).sort()

  assertNoOffenders('Desktop V3 production source must not contain TanStack DB / React DB / live-query collection authority.', offenders)
})

test('the legacy desktop-db.ts authority module is gone instead of living beside the external store', () => {
  assert.equal(existsSync(legacyDesktopDbPath), false, 'delete web/src/features/desktop/state/desktop-db.ts after migrating its callers; do not keep a compatibility fork')
})

test('Desktop production source has no old DB mutation helpers or route hydration authority calls', () => {
  const patterns = legacyCanonicalMutationHelpers.map((name) => ({ name, pattern: new RegExp(`\\b${name}\\b`) }))
  const offenders = productionDesktopFiles().flatMap((filePath) => {
    const source = readText(filePath)
    const relative = desktopRelative(filePath)
    return patterns.flatMap(({ name, pattern }) => matchingLineNumbers(source, pattern).map((line) => `${relative}:${line} ${name}`))
  }).sort()

  assertNoOffenders('Canonical Desktop state must change only through snapshot replacement or daemon events, not old DB helper paths.', offenders)
})

test('React Query is not used by Desktop V3 runtime/core-state modules as a core-state authority', () => {
  const coreFiles = [
    path.join(stateRoot, 'desktop-state-store.ts'),
    path.join(stateRoot, 'desktop-state.ts'),
    path.join(stateRoot, 'desktop-state-snapshot.ts'),
    path.join(stateRoot, 'desktop-state-stream.ts'),
    ...walkFiles(path.join(desktopRoot, 'v3-runtime')).filter((filePath) => !isTestFile(filePath)),
  ]
  const offenders = coreFiles.flatMap((filePath) => {
    const source = readText(filePath)
    const relative = desktopRelative(filePath)
    const lines = matchingLineNumbers(source, /@tanstack\/react-query|\bQueryClient\b|\buseQueryClient\b|\buseQuery\b|queryClient\.(?:getQueryData|setQueryData|fetchQuery|ensureQueryData)/)
    return lines.map((line) => `${relative}:${line}`)
  }).sort()

  assertNoOffenders('Desktop V3 runtime/core state must not use React Query cache as canonical daemon state.', offenders)
})

test('Desktop V3 has exactly one canonical Zustand vanilla runtime store', () => {
  assertFileExists(v3RuntimeStorePath, 'Desktop V3 runtime store module')
  assertFileExists(legacyExternalStoreFacadePath, 'Desktop legacy external-store facade')

  const source = readText(v3RuntimeStorePath)
  assert.match(source, /from\s+['"]zustand\/vanilla['"]/, 'v3-store.ts must use Zustand vanilla')
  assert.match(source, /\bcreateStore\b/, 'v3-store.ts must create the runtime store')
  assert.match(source, /export\s+const\s+applyV3RuntimeEnvelope\b/, 'v3-store.ts must export the only runtime mutation gate')
  assert.match(source, /export\s+const\s+getV3RuntimeDesktopSnapshot\b/, 'v3-store.ts must expose desktop reads')
  assert.match(source, /export\s+const\s+subscribeV3Runtime\b/, 'v3-store.ts must expose subscriptions')
  assert.doesNotMatch(source, /@tanstack\/db|@tanstack\/react-db|@tanstack\/react-query/, 'v3-store.ts must not import TanStack DB, React DB, or React Query')

  const canonicalStoreOwners = productionDesktopFiles()
    .filter((filePath) => /\bcreateStore\s*</.test(readText(filePath)))
    .map(desktopRelative)
    .sort()

  assert.deepEqual(canonicalStoreOwners, ['v3-runtime/v3-store.ts'], `only v3-runtime/v3-store.ts may create the canonical V3 runtime store; found ${formatOffenders(canonicalStoreOwners)}`)

  const facadeSource = readText(legacyExternalStoreFacadePath)
  assert.match(facadeSource, /applyV3RuntimeEnvelope/, 'desktop-state-store.ts must be a compatibility facade over the V3 runtime')
  assert.doesNotMatch(facadeSource, /let\s+desktopState\s*=|createEmptyDesktopState\(/, 'desktop-state-store.ts must not retain a second mutable Desktop snapshot')
})

test('Desktop V3 has one reducer boundary for snapshot replacement and daemon events', () => {
  assertFileExists(reducerPath, 'Desktop reducer module')

  const source = readText(reducerPath)
  assert.match(source, /export\s+type\s+DesktopState\b|export\s+interface\s+DesktopState\b/, 'desktop-state.ts must export DesktopState')
  assert.match(source, /export\s+type\s+DesktopDaemonSnapshot\b|export\s+interface\s+DesktopDaemonSnapshot\b/, 'desktop-state.ts must export DesktopDaemonSnapshot')
  assert.match(source, /export\s+type\s+DesktopDaemonEvent\b|export\s+interface\s+DesktopDaemonEvent\b/, 'desktop-state.ts must export DesktopDaemonEvent')
  assert.match(source, /export\s+function\s+createEmptyDesktopState\b/, 'desktop-state.ts must export createEmptyDesktopState')
  assert.match(source, /export\s+function\s+desktopReducer\b/, 'desktop-state.ts must export desktopReducer')
  assert.match(source, /snapshot\/replace/, 'desktopReducer must have a snapshot/replace action')
  assert.match(source, /daemon\/event/, 'desktopReducer must have a daemon/event action')
  assert.match(source, /connection\/stale/, 'desktopReducer must have a connection/stale action')
  assert.match(source, /prevRev/, 'desktopReducer must check prevRev continuity before applying daemon payloads')
  assert.match(source, /Number\.isFinite|isFinite/, 'desktopReducer must reject missing or non-finite rev metadata')
  assert.doesNotMatch(source, /@tanstack\/db|@tanstack\/react-db|@tanstack\/react-query|useLiveQuery|createCollection|localOnlyCollectionOptions/, 'desktop-state.ts must be plain reducer code, not a DB/cache authority')
})

test('Desktop reducer tests encode revision guard behavior', () => {
  assertFileExists(reducerSpecPath, 'Desktop reducer unit test module')

  const source = readText(reducerSpecPath)
  assert.match(source, /snapshot\s+replacement|snapshot\/replace|replace.*snapshot/i, 'reducer tests must cover snapshot replacement')
  assert.match(source, /prevRev\s*={0,3}\s*state\.rev|matching\s+prevRev|continuous\s+rev/i, 'reducer tests must cover valid prevRev continuity')
  assert.match(source, /prevRev\s*!={0,2}\s*state\.rev|mismatch|stale/i, 'reducer tests must cover prevRev mismatch causing stale/resync')
  assert.match(source, /duplicate|old\s+event|rev\s*<=\s*state\.rev/i, 'reducer tests must cover duplicate/old events')
  assert.match(source, /non-finite|Number\.isFinite|missing.*rev|invalid.*rev/i, 'reducer tests must cover missing or non-finite revision metadata')
  assert.match(source, /resync|stale.*snapshot|clears\s+stale/i, 'reducer tests must cover resync snapshot clearing stale state')
})

test('Desktop stream lifecycle is a snapshot-first reducer feed, not a second store owner', () => {
  assertFileExists(streamPath, 'Desktop stream lifecycle module')

  const source = readText(streamPath)
  assert.match(source, /replaceDesktopFromSnapshot/, 'stream boot must replace store from snapshot before live patches')
  assert.match(source, /applyDesktopDaemonEvent/, 'stream must apply daemon events through the external-store reducer entrypoint')
  assert.match(source, /afterRev|prevRev|rev/, 'stream must subscribe/resume with revision metadata')
  assert.match(source, /slow_consumer|cursor_error|reconnect_required|resync|overflow/, 'stream must resync on slow consumer, cursor error, reconnect required, or queue overflow')
  assert.doesNotMatch(source, /new\s+Map\s*\([^)]*\)|createCollection|localOnlyCollectionOptions|useLiveQuery|@tanstack\/react-query/, 'stream module must not create a second canonical store/cache authority')
})

test('Desktop V3 final stack is React, TypeScript, WebSocket, one external store, and one reducer', () => {
  const pkg = readJson<{ dependencies?: Record<string, string>; devDependencies?: Record<string, string> }>(packageJsonPath)
  const dependencies = { ...(pkg.dependencies ?? {}), ...(pkg.devDependencies ?? {}) }

  assert.ok(dependencies.react, 'React must remain in the final stack')
  assert.ok(dependencies.typescript, 'TypeScript must remain in the final stack')
  assert.ok(dependencies.zustand, 'Zustand vanilla runtime must remain in the final stack')
  assertFileExists(realtimeClientPath, 'existing Desktop WebSocket client')
  assertFileExists(v3RuntimeStorePath, 'Zustand vanilla Desktop V3 runtime store')
  assertFileExists(reducerPath, 'Desktop reducer')
  assertFileExists(reducerSpecPath, 'Desktop reducer tests')

  const realtimeSource = readText(realtimeClientPath)
  assert.match(realtimeSource, /WebSocket/, 'Desktop realtime client must use the existing WebSocket transport')

  const forbiddenDependencyOffenders = forbiddenPackageNames.filter((packageName) => dependencies[packageName])
  assertNoOffenders('Final stack must not include TanStack DB packages.', forbiddenDependencyOffenders)
})
