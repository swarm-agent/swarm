import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import test from 'node:test'

const repoRoot = path.resolve(new URL('../../../../../', import.meta.url).pathname)
const desktopRoot = path.join(repoRoot, 'web/src/features/desktop')
const sessionV3Root = path.join(desktopRoot, 'session-v3')

const forbiddenDesktopV3TransportPatterns: Array<{ label: string; pattern: RegExp }> = [
  { label: 'legacy /ws socket helper', pattern: /\bopenDesktopWebSocket\b/g },
  { label: 'legacy /ws endpoint', pattern: /['"]\/ws['"]/g },
  { label: 'legacy run stream controller', pattern: /\bDesktopRunStreamController\b/g },
  { label: 'legacy run stream frame type', pattern: /\bRunStreamEventMessage\b/g },
  { label: 'legacy run stream owner singleton', pattern: /\brunStreamController\b/g },
  { label: 'legacy run stream owner factory', pattern: /\brequireRunStreamController\b/g },
  { label: 'per-session run stream opener', pattern: /\bopenRunStream\b/g },
  { label: 'legacy run-stream module import', pattern: /from\s+['"][^'"]*run-stream-controller['"]|from\s+['"][^'"]*run-stream['"]/g },
  { label: 'legacy run start helper', pattern: /\bstartSessionRun\b/g },
  { label: 'legacy run stop helper', pattern: /\bstopSessionRun\b/g },
  { label: 'legacy V3 realtime controller', pattern: /\bDesktopV3RealtimeController\b/g },
  { label: 'legacy V3 realtime owner singleton', pattern: /\bdesktopV3RealtimeController\b/g },
  { label: 'legacy V3 realtime owner factory', pattern: /\brequireV3RealtimeController\b/g },
  { label: 'legacy V3 realtime frame applier', pattern: /\bapplyDesktopV3RealtimeFrame\b/g },
  { label: 'legacy reconnect helper', pattern: /\bfetchAndApplyDesktopV3Reconnect\b|\bsyncV3RealtimeSessionsFromReconnect\b/g },
  { label: 'legacy reconnect API helper', pattern: /\breconnectSessionV3\b/g },
  { label: 'legacy reconnect endpoint', pattern: /['"`]\/v3\/sessions:reconnect['"`]/g },
  { label: 'legacy sessions workset endpoint', pattern: /['"`]\/v3\/sessions:workset['"`]/g },
  { label: 'legacy TUI sessions workset endpoint', pattern: /['"`]\/v3\/tui\/sessions:workset['"`]/g },
  { label: 'per-session V3 stream endpoint', pattern: /\/v3\/sessions\/(?:\$\{[^}]+\}|\{id\}|[^/`'"\s]+)\/stream/g },
  { label: 'legacy per-session full hydrate endpoint', pattern: /`\/v3\/sessions\/\$\{[^}]+\}`/g },
  { label: 'legacy run-stream endpoint', pattern: /\/run-stream\b|run-stream/g },
  { label: 'legacy V2 runtime/session stream endpoint', pattern: /\/v2\/[^`'"\s]*(?:runtime|sessions?|session)[^`'"\s]*\/stream|\/v2\/[^`'"\s]*(?:run-stream|stream)/g },
  { label: 'after_seq transport resume query', pattern: /url\.searchParams\.set\(['"]after_seq['"]/g },
  { label: 'afterSeq transport resume input', pattern: /\bafterSeq\b/g },
  { label: 'after_rev realtime transport resume input', pattern: /\bafter_rev\b/g },
  { label: 'afterRev realtime transport resume input', pattern: /\bafterRev\b/g },
  { label: 'lastEventSeq transport resume query', pattern: /url\.searchParams\.set\(['"]lastEventSeq['"]/g },
  { label: 'legacy desktop realtime socket owner', pattern: /\bdesktopRealtimeSocket\b/g },
  { label: 'V3 primary stream helper', pattern: /\bprimaryStream\b|primary-stream/g },
  { label: 'sessionV3StreamFrame helper', pattern: /\bsessionV3StreamFrame\b/g },
]

type Offender = {
  file: string
  label: string
  count: number
}

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

function isTestFile(filePath: string): boolean {
  return /(?:^|\.)(?:spec|test|e2e\.spec)\.tsx?$/.test(path.basename(filePath))
}

function desktopRelative(filePath: string): string {
  return path.relative(desktopRoot, filePath).split(path.sep).join('/')
}

function productionSessionV3Files(): string[] {
  return walkFiles(sessionV3Root).filter((filePath) => !isTestFile(filePath))
}

function activeDesktopV3TransportFiles(): string[] {
  return [
    'state/desktop-ui-store.ts',
    'state/desktop-v3-session-api.ts',
    'chat/components/desktop-chat-panel.tsx',
    ...productionSessionV3Files().map(desktopRelative),
  ].map((relative) => path.join(desktopRoot, relative))
}

function countPattern(source: string, pattern: RegExp): number {
  return Array.from(source.matchAll(new RegExp(pattern.source, pattern.flags.includes('g') ? pattern.flags : `${pattern.flags}g`))).length
}

function formatOffenders(offenders: Offender[]): string {
  return offenders.length === 0
    ? '(none)'
    : `\n${offenders.map((offender) => `- ${offender.file}: ${offender.label} (${offender.count})`).join('\n')}`
}

function forbiddenOffenders(files: string[]): Offender[] {
  return files.flatMap((filePath) => {
    const source = readText(filePath)
    const relative = desktopRelative(filePath)
    return forbiddenDesktopV3TransportPatterns.flatMap(({ label, pattern }) => {
      const count = countPattern(source, pattern)
      return count === 0 ? [] : [{ file: relative, label, count }]
    })
  }).sort((left, right) => `${left.file}:${left.label}`.localeCompare(`${right.file}:${right.label}`))
}

test('session-v3 production runtime has no legacy desktop transports or resume inputs', () => {
  const offenders = forbiddenOffenders(productionSessionV3Files())

  assert.deepEqual(
    offenders,
    [],
    `session-v3 production code must stay endpoint_cursor-only and must not use legacy reconnect/workset APIs, /ws, per-session streams, V2 streams, run-stream endpoints, or event-seq transport resume inputs.${formatOffenders(offenders)}`,
  )
})

test('active Desktop V3 session transport path has no legacy transport allowlist', () => {
  const offenders = forbiddenOffenders(activeDesktopV3TransportFiles())

  assert.deepEqual(
    offenders,
    [],
    `Desktop V3 session transport must be owned only by DesktopSessionV3Runtime and /v3/realtime/stream; no legacy reconnect/workset APIs, /ws, run-stream, DesktopV3RealtimeController, per-session stream, V2 stream, or event-seq transport inputs remain.${formatOffenders(offenders)}`,
  )
})

test('desktop-ui-store owns exactly one DesktopSessionV3Runtime instance', () => {
  const source = readText(path.join(desktopRoot, 'state/desktop-ui-store.ts'))
  assert.equal(
    countPattern(source, /\bnew\s+DesktopSessionV3Runtime\b/g),
    1,
    'desktop-ui-store.ts must construct exactly one store-owned DesktopSessionV3Runtime instance',
  )
  assert.equal(
    countPattern(source, /\bcreateDesktopSessionV3Runtime\b/g),
    0,
    'desktop-ui-store.ts must not hide extra runtime construction behind the factory helper',
  )
  assert.match(
    source,
    /let\s+desktopSessionV3Runtime:\s*DesktopSessionV3Runtime\s*\|\s*null\s*=\s*null/,
    'desktop-ui-store.ts must keep the runtime as a module singleton owned by the store',
  )
  assert.match(
    source,
    /function\s+ensureDesktopSessionV3Runtime\(\):\s*DesktopSessionV3Runtime/,
    'desktop-ui-store.ts must expose one narrow internal ensure function for the store-owned runtime',
  )
})

test('desktop-ui-store connect boots through DesktopSessionV3Runtime', () => {
  const source = readText(path.join(desktopRoot, 'state/desktop-ui-store.ts'))
  assert.match(
    source,
    /await\s+ensureDesktopSession\(true\)[\s\S]+const\s+runtime\s*=\s*ensureDesktopSessionV3Runtime\(\)[\s\S]+await\s+runtime\.boot\(\)/,
    'desktop connect must use ensureDesktopSession(true) for cookie/auth gating, then runtime.boot() for snapshot plus realtime startup',
  )
  assert.doesNotMatch(
    source,
    /openDesktopWebSocket|fetchAndApplyDesktopV3Reconnect|syncV3RealtimeSessionsFromReconnect|requireV3RealtimeController|DesktopV3RealtimeController/,
    'desktop connect must not use legacy /ws or legacy V3 controller reconnect helpers',
  )
})

test('external snapshot cursor does not reanimate stopped desktop V3 runtime', () => {
  const source = readText(path.join(desktopRoot, 'state/desktop-ui-store.ts'))
  assert.match(
    source,
    /function\s+disposeDesktopSessionV3Runtime\(\):\s*void\s*{[\s\S]*desktopSessionV3Runtime\s*=\s*null[\s\S]*runtime\?\.shutdown\(\)/,
    'desktop-ui-store must null and shutdown the store-owned runtime when disposing it',
  )
  assert.match(
    source,
    /function\s+clearDesktopRuntimeState\([^)]*\)[\s\S]*disposeDesktopSessionV3Runtime\(\)/,
    'clearing desktop runtime state must dispose/null the session-v3 runtime instead of leaving a stopped object alive',
  )
  assert.doesNotMatch(
    source,
    /refresh\(\{\s*reason:\s*['"]external snapshot cursor['"]\s*\}\)/,
    'desktop:v3-realtime-snapshot-cursor must not refresh/reconnect the session-v3 runtime',
  )
})

test('Desktop V3 realtime transport opens only /v3/realtime/stream with endpoint_cursor', () => {
  const source = readText(path.join(desktopRoot, 'session-v3/transport.ts'))
  assert.match(source, /SESSION_V3_REALTIME_STREAM_PATH/)
  assert.match(source, /url\.searchParams\.set\('endpoint_cursor',\s*endpointCursor\)/)
  assert.doesNotMatch(source, /after_seq|afterSeq|after_rev|afterRev|lastEventSeq|\/ws|\/v3\/sessions\/[^`'"\s]+\/stream/)
})
