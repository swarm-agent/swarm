import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
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
  { label: 'per-session V3 stream endpoint', pattern: /\/v3\/sessions\/(?:\$\{[^}]+\}|\{id\}|[^/`'"\s]+)\/stream/g },
  { label: 'after_seq transport resume query', pattern: /url\.searchParams\.set\(['"]after_seq['"]/g },
  { label: 'afterSeq transport resume input', pattern: /\bafterSeq\b/g },
  { label: 'afterRev realtime transport resume input', pattern: /\bafterRev\b/g },
  { label: 'legacy desktop realtime socket owner', pattern: /\bdesktopRealtimeSocket\b/g },
  { label: 'V3 primary stream helper', pattern: /\bprimaryStream\b|primary-stream/g },
  { label: 'sessionV3StreamFrame helper', pattern: /\bsessionV3StreamFrame\b/g },
]

// CP-1 freezes the current mixed-transport architecture while preventing the
// forbidden desktop V3 transport symbols from spreading to new production files.
// Later checkpoints should shrink this allowlist to zero as each owner moves to
// web/src/features/desktop/session-v3/*.
const allowedCurrentForbiddenTransportFiles: Record<string, readonly string[]> = {
  'legacy /ws socket helper': ['realtime/client.ts', 'state/desktop-ui-store.ts'],
  'legacy /ws endpoint': ['realtime/client.ts'],
  'legacy run stream controller': ['state/desktop-ui-store.ts', 'state/run-stream-controller.ts'],
  'legacy run stream frame type': [
    'realtime/v3-realtime-controller.ts',
    'state/desktop-ui-store.ts',
    'state/run-stream-controller.ts',
  ],
  'legacy run stream owner singleton': ['state/desktop-ui-store.ts'],
  'legacy run stream owner factory': ['state/desktop-ui-store.ts'],
  'per-session run stream opener': ['chat/queries/chat-queries.ts', 'state/run-stream-controller.ts'],
  'legacy run-stream module import': ['realtime/v3-realtime-controller.ts', 'state/desktop-ui-store.ts'],
  'legacy run start helper': [
    'chat/components/desktop-chat-panel.tsx',
    'chat/queries/chat-queries.ts',
    'state/run-stream-controller.ts',
  ],
  'legacy run stop helper': ['chat/queries/chat-queries.ts', 'state/run-stream-controller.ts'],
  'per-session V3 stream endpoint': ['chat/queries/chat-queries.ts'],
  'after_seq transport resume query': ['chat/queries/chat-queries.ts'],
  'afterSeq transport resume input': ['chat/queries/chat-queries.ts', 'state/desktop-ui-store.ts', 'state/run-stream-controller.ts'],
  'afterRev realtime transport resume input': ['state/desktop-state-stream.ts'],
  'legacy desktop realtime socket owner': ['state/desktop-ui-store.ts'],
  'V3 primary stream helper': [],
  'sessionV3StreamFrame helper': [],
}

const legacyMixedTransportTestsToReplace: Array<{ file: string; marker: RegExp; replacement: string }> = [
  {
    file: 'state/run-stream-controller-v3.spec.ts',
    marker: /__testApplyRunStreamFrame|run-stream-controller/,
    replacement: 'Replace V3 run-stream frame assertions with session-v3 reducer/runtime realtime frame tests.',
  },
  {
    file: 'state/run-stream-controller.lifecycle.spec.ts',
    marker: /DesktopRunStreamController/,
    replacement: 'Replace per-run WebSocket lifecycle assertions with one /v3/realtime/stream lifecycle assertions.',
  },
  {
    file: 'realtime/v3-realtime-controller.spec.ts',
    marker: /subscribe\.session|RunStreamEventMessage|endpoint_cursor/,
    replacement: 'Replace session-only subscribe controller assertions with one resume frame containing worksets plus session subscriptions.',
  },
  {
    file: 'realtime/local-session-auth.spec.ts',
    marker: /openDesktopWebSocket|openRunStream|\/ws/,
    replacement: 'Replace legacy /ws and per-session stream auth expectations with /v3/realtime/stream cookie-only expectations.',
  },
  {
    file: 'chat/queries/session-v3-subresource-guard.spec.ts',
    marker: /startSessionRun|stopSessionRun|sessionV3StreamFrame/,
    replacement: 'Replace legacy lifecycle-helper V3 assertions with session-v3 HTTP mutation API assertions.',
  },
  {
    file: 'state/desktop-db-architecture.spec.ts',
    marker: /afterRev|Desktop stream lifecycle/,
    replacement: 'Replace afterRev desktop stream assertions with endpoint_cursor-only session-v3 transport assertions.',
  },
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

function productionDesktopFiles(): string[] {
  return walkFiles(desktopRoot).filter((filePath) => !isTestFile(filePath))
}

function productionSessionV3Files(): string[] {
  return walkFiles(sessionV3Root).filter((filePath) => !isTestFile(filePath))
}

function countPattern(source: string, pattern: RegExp): number {
  return Array.from(source.matchAll(new RegExp(pattern.source, pattern.flags.includes('g') ? pattern.flags : `${pattern.flags}g`))).length
}

function formatOffenders(offenders: Offender[]): string {
  return offenders.length === 0
    ? '(none)'
    : `\n${offenders.map((offender) => `- ${offender.file}: ${offender.label} (${offender.count})`).join('\n')}`
}

test('session-v3 production runtime has no legacy desktop transports or resume inputs', () => {
  const offenders = productionSessionV3Files().flatMap((filePath) => {
    const source = readText(filePath)
    const relative = desktopRelative(filePath)
    return forbiddenDesktopV3TransportPatterns.flatMap(({ label, pattern }) => {
      const count = countPattern(source, pattern)
      return count === 0 ? [] : [{ file: relative, label, count }]
    })
  }).sort((left, right) => `${left.file}:${left.label}`.localeCompare(`${right.file}:${right.label}`))

  assert.deepEqual(
    offenders,
    [],
    `New session-v3 production code must stay endpoint_cursor-only and must not use /ws, per-session streams, after_seq/afterSeq/afterRev, or run-stream imports.${formatOffenders(offenders)}`,
  )
})

test('Desktop V3 forbidden transport symbols remain confined to the known mixed-architecture files', () => {
  const offenders = productionDesktopFiles().flatMap((filePath) => {
    const source = readText(filePath)
    const relative = desktopRelative(filePath)
    return forbiddenDesktopV3TransportPatterns.flatMap(({ label, pattern }) => {
      const count = countPattern(source, pattern)
      if (count === 0) {
        return []
      }
      const allowedFiles = allowedCurrentForbiddenTransportFiles[label] ?? []
      return allowedFiles.includes(relative) ? [] : [{ file: relative, label, count }]
    })
  }).sort((left, right) => `${left.file}:${left.label}`.localeCompare(`${right.file}:${right.label}`))

  assert.deepEqual(
    offenders,
    [],
    `Desktop V3 code must not introduce new legacy /ws, run-stream, per-session stream, or after_seq/afterRev transport owners.${formatOffenders(offenders)}`,
  )
})

test('Desktop V3 transport migration has an explicit inventory of old mixed-shape tests to replace', () => {
  const missing = legacyMixedTransportTestsToReplace.flatMap(({ file, marker, replacement }) => {
    const filePath = path.join(desktopRoot, file)
    if (!existsSync(filePath)) {
      return [`${file}: missing; ${replacement}`]
    }
    const source = readText(filePath)
    return marker.test(source) ? [] : [`${file}: marker ${marker} not found; ${replacement}`]
  })

  assert.deepEqual(missing, [], `CP-1 legacy transport test inventory is stale:\n${missing.join('\n')}`)
})
