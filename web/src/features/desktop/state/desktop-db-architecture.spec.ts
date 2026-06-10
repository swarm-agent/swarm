import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import test from 'node:test'

const repoRoot = path.resolve(new URL('../../../../../', import.meta.url).pathname)
const webRoot = path.join(repoRoot, 'web')
const desktopRoot = path.join(webRoot, 'src/features/desktop')
const stateRoot = path.join(desktopRoot, 'state')
const packageJsonPath = path.join(webRoot, 'package.json')
const desktopDbPath = path.join(stateRoot, 'desktop-db.ts')
const legacyStorePath = path.join(stateRoot, 'use-desktop-store.ts')

const backendDerivedDesktopDataTerms = [
  'sessions',
  'session',
  'messages',
  'message',
  'permissions',
  'permission',
  'plans',
  'plan',
  'usage',
  'preference',
  'agentModelPolicy',
  'runIntent',
  'projection',
  'notificationCenter',
  'vault',
]

const allowedLegacyStoreFiles = new Set([
  // The file may remain only while later checkpoints delete it. No production reader may import it.
  'state/use-desktop-store.ts',
])

function readText(filePath: string): string {
  return readFileSync(filePath, 'utf8')
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

function importsLegacyDesktopStore(source: string): boolean {
  return /from\s+['"][^'"]*use-desktop-store['"]/.test(source)
}

function mentionsBackendDerivedDesktopData(source: string): boolean {
  return backendDerivedDesktopDataTerms.some((term) => new RegExp(`\\b${term}\\b`, 'i').test(source))
}

test('Desktop V3 declares TanStack DB, not Zustand, as the frontend state authority dependency', () => {
  const pkg = JSON.parse(readText(packageJsonPath)) as { dependencies?: Record<string, string> }
  const dependencies = pkg.dependencies ?? {}

  assert.ok(dependencies['@tanstack/db'], 'web/package.json must depend on @tanstack/db')
  assert.equal(dependencies.zustand, undefined, 'zustand must be removed from the Desktop V3 data path dependencies')
})

test('Desktop V3 has a canonical TanStack DB module for backend-derived state', () => {
  assert.equal(existsSync(desktopDbPath), true, 'missing web/src/features/desktop/state/desktop-db.ts')

  const source = existsSync(desktopDbPath) ? readText(desktopDbPath) : ''
  assert.match(source, /@tanstack\/db/, 'desktop-db.ts must import TanStack DB')
  assert.match(source, /\/v3\/sessions:workset|DesktopV3Workset/, 'desktop-db.ts must model the V3 workset bootstrap input')
})

test('the legacy Zustand desktop store is not an authoritative backend-derived store', () => {
  if (!existsSync(legacyStorePath)) {
    assert.ok(true)
    return
  }

  const source = readText(legacyStorePath)
  assert.doesNotMatch(source, /from\s+['"]zustand['"]/, 'use-desktop-store.ts must not create a Zustand store')
  assert.doesNotMatch(source, /create\s*</, 'use-desktop-store.ts must not create an authoritative store')
  assert.doesNotMatch(source, /sessions\s*:/, 'use-desktop-store.ts must not own backend-derived sessions')
})

test('production Desktop readers do not import useDesktopStore for backend-derived data', () => {
  const offenders = walkFiles(desktopRoot)
    .filter((filePath) => !filePath.endsWith('.spec.ts') && !filePath.endsWith('.spec.tsx') && !filePath.endsWith('.e2e.spec.ts') && !filePath.endsWith('.e2e.spec.tsx'))
    .filter((filePath) => !allowedLegacyStoreFiles.has(desktopRelative(filePath)))
    .filter((filePath) => {
      const source = readText(filePath)
      return importsLegacyDesktopStore(source) && mentionsBackendDerivedDesktopData(source)
    })
    .map(desktopRelative)
    .sort()

  assert.deepEqual(offenders, [], `backend-derived Desktop reads must come from TanStack DB, not useDesktopStore: ${offenders.join(', ')}`)
})

test('route readiness and cached session switching do not depend on the old workset/query cache authority', () => {
  const offenders = walkFiles(desktopRoot)
    .filter((filePath) => !filePath.endsWith('.spec.ts') && !filePath.endsWith('.spec.tsx') && !filePath.endsWith('.e2e.spec.ts') && !filePath.endsWith('.e2e.spec.tsx'))
    .filter((filePath) => {
      const relative = desktopRelative(filePath)
      if (relative === 'state/desktop-v3-cache.ts' || relative === 'state/desktop-v3-durable-reducer.ts') {
        return true
      }
      const source = readText(filePath)
      return /desktop-v3-cache|desktop-v3-durable-reducer|hydrateDesktopV3|readDesktopV3CachedSession|getCachedDesktopV3/.test(source)
    })
    .map(desktopRelative)
    .sort()

  assert.deepEqual(offenders, [], `cached-session readiness must be TanStack DB-derived, not old query/cache authority: ${offenders.join(', ')}`)
})
