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

function exportedFunctionSource(source: string, name: string): string {
  const start = source.indexOf(`export function ${name}`)
  assert.notEqual(start, -1, `missing exported function ${name}`)
  const bodyStart = source.indexOf('{', start)
  assert.notEqual(bodyStart, -1, `missing body for exported function ${name}`)
  let depth = 0
  for (let index = bodyStart; index < source.length; index += 1) {
    const char = source[index]
    if (char === '{') {
      depth += 1
    } else if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(start, index + 1)
      }
    }
  }
  assert.fail(`unterminated body for exported function ${name}`)
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
      return /desktop-v3-cache|desktop-v3-durable-reducer|hydrateDesktopV3|getCachedDesktopV3/.test(source)
    })
    .map(desktopRelative)
    .sort()

  assert.deepEqual(offenders, [], `cached-session readiness must be TanStack DB-derived, not old query/cache authority: ${offenders.join(', ')}`)
})

test('Desktop V3 hot message reads use scoped TanStack DB queries, not collection-wide subscriptions', () => {
  const source = readText(desktopDbPath)
  const hook = exportedFunctionSource(source, 'useDesktopMessages')
  const reader = exportedFunctionSource(source, 'readDesktopDbMessages')

  assert.match(hook, /useLiveQuery\(\s*\(query\)\s*=>/, 'useDesktopMessages must create a query-shaped live query')
  assert.match(hook, /from\(\{\s*message:\s*desktopMessagesCollection\s*\}\)/, 'useDesktopMessages must query from desktopMessagesCollection')
  assert.match(hook, /where\(\(\{\s*message\s*\}\)\s*=>\s*eq\(message\.sessionId,\s*normalizedSessionId\)\)/, 'useDesktopMessages must scope by sessionId in the query')
  assert.match(hook, /orderBy\(\(\{\s*message\s*\}\)\s*=>\s*message\.globalSeq\)/, 'useDesktopMessages must order in the query by globalSeq')
  assert.match(hook, /orderBy\(\(\{\s*message\s*\}\)\s*=>\s*message\.createdAt\)/, 'useDesktopMessages must order in the query by createdAt')
  assert.doesNotMatch(hook, /useLiveQuery\(\s*\(\)\s*=>\s*desktopMessagesCollection/, 'useDesktopMessages must not subscribe to the full message collection')
  assert.doesNotMatch(hook, /useDesktopCollectionData\(desktopMessagesCollection\)/, 'useDesktopMessages must not read all messages through a helper')
  assert.doesNotMatch(hook, /\.filter\(\s*\(?message\)?\s*=>\s*message\.sessionId\s*===/, 'useDesktopMessages must not filter session messages after reading all messages')

  assert.doesNotMatch(reader, /Array\.from\(desktopMessagesCollection\.values\(\)\)\s*\.filter/, 'per-session message snapshots must not scan all messages')
  assert.match(reader, /desktopMessagesBySessionIndex\.lookup\('eq',\s*normalizedSessionId\)/, 'per-session message snapshots must use the sessionId index')
})

test('Desktop V3 workspace, route readiness, and single-record hooks use scoped queries', () => {
  const source = readText(desktopDbPath)
  const workspaceSessions = exportedFunctionSource(source, 'useDesktopWorkspaceSessions')
  const routeReadiness = exportedFunctionSource(source, 'useDesktopRouteReadiness')
  const activeRun = exportedFunctionSource(source, 'useDesktopActiveRun')
  const preference = exportedFunctionSource(source, 'useDesktopPreference')

  assert.match(workspaceSessions, /from\(\{\s*session:\s*desktopSessionsCollection\s*\}\)/, 'workspace sessions must query from desktopSessionsCollection')
  assert.match(workspaceSessions, /where\(\(\{\s*session\s*\}\)\s*=>\s*inArray\(session\.workspacePath,\s*workspacePaths\)\)/, 'workspace sessions must scope by workspace in the query')
  assert.match(workspaceSessions, /orderBy\(\(\{\s*session\s*\}\)\s*=>\s*session\.updatedAt,\s*'desc'\)/, 'workspace sessions must order in the query')
  assert.doesNotMatch(workspaceSessions, /useDesktopCollectionData\(desktopSessionsCollection\)/, 'workspace sessions must not subscribe to all sessions through a helper')
  assert.doesNotMatch(workspaceSessions, /\.filter\(\s*\(?session\)?\s*=>/, 'workspace sessions must not filter sessions after collection-wide subscription')

  assert.match(routeReadiness, /from\(\{\s*readiness:\s*desktopSessionReadinessCollection\s*\}\)/, 'route readiness must query the readiness row')
  assert.match(routeReadiness, /where\(\(\{\s*readiness\s*\}\)\s*=>\s*eq\(readiness\.sessionId,\s*normalizedSessionId\)\)/, 'route readiness must scope by sessionId in the query')
  assert.doesNotMatch(routeReadiness, /useDesktopCollectionState\(/, 'route readiness must not depend on a broad collection state subscription')

  assert.match(activeRun, /from\(\{\s*runIntent:\s*desktopRunIntentsCollection\s*\}\)/, 'active run must query run intents by session')
  assert.match(activeRun, /findOne\(\)/, 'active run must be a single-row query')
  assert.match(preference, /from\(\{\s*preference:\s*desktopPreferencesCollection\s*\}\)/, 'preference must query preferences by session')
  assert.match(preference, /findOne\(\)/, 'preference must be a single-row query')
})

test('Desktop V3 scoped query indexes are declared for hot read predicates', () => {
  const source = readText(desktopDbPath)

  assert.match(source, /desktopMessagesCollection\.createIndex\(\(message\)\s*=>\s*message\.sessionId,\s*\{\s*name:\s*'desktop_messages_by_session_id',\s*indexType:\s*BasicIndex\s*\}\)/)
  assert.match(source, /desktopMessagesCollection\.createIndex\(\(message\)\s*=>\s*\[message\.sessionId,\s*message\.globalSeq\]/)
  assert.match(source, /desktopSessionsCollection\.createIndex\(\(session\)\s*=>\s*session\.workspacePath/)
  assert.match(source, /desktopPlanRevisionsCollection\.createIndex\(\(revision\)\s*=>\s*revision\.sessionId/)
  assert.match(source, /desktopRunIntentsCollection\.createIndex\(\(intent\)\s*=>\s*\[intent\.sessionId,\s*intent\.status\]/)
  assert.match(source, /desktopPermissionsCollection\.createIndex\(\(permission\)\s*=>\s*\[permission\.sessionId,\s*permission\.runId\]/)
})

test('Desktop V3 sidebar and route code do not subscribe to the full message collection for live status', () => {
  const desktopAppPagePath = path.join(desktopRoot, 'layout/desktop-app-page.tsx')
  const source = readText(desktopAppPagePath)

  assert.match(source, /useDesktopWorkspaceSessions\(\{ workspacePaths: mergedSidebarWorkspaceEntries\.map\(\(workspace\) => workspace\.path\) \}\)/)
  assert.match(source, /useDesktopRouteReadiness\(\{ workspacePath: selectedWorkspacePath \}, routeSessionId\)/)
  assert.doesNotMatch(source, /desktopMessagesCollection/, 'sidebar/route code must not read the full messages collection')
  assert.doesNotMatch(source, /useLiveQuery\(\s*\(\)\s*=>\s*desktopMessagesCollection/, 'sidebar/route code must not subscribe to all messages')
})
