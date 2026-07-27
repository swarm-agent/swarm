import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile, readdir } from 'node:fs/promises'
import { basename, join, relative, resolve, sep } from 'node:path'

const repoRoot = resolve(new URL('../../../../../', import.meta.url).pathname)

async function filesUnder(root: string): Promise<string[]> {
  const output: string[] = []

  for (const entry of await readdir(root, { withFileTypes: true })) {
    const path = join(root, entry.name)

    if (entry.isDirectory()) {
      output.push(...await filesUnder(path))
    } else if (/\.(ts|tsx|js|jsx|json|yaml|yml)$/.test(entry.name)) {
      output.push(path)
    }
  }

  return output
}

async function scanRoots(roots: string[]): Promise<string[]> {
  const files: string[] = []

  for (const root of roots) {
    const absolute = join(repoRoot, root)
    if (/\.(ts|tsx|js|jsx|json|yaml|yml)$/.test(root)) {
      files.push(absolute)
    } else {
      files.push(...await filesUnder(absolute))
    }
  }

  return files
}

function repoRelative(file: string): string {
  return relative(repoRoot, file).split(sep).join('/')
}

test('desktop session runtime has no browser database dependency', async () => {
  const forbidden = [
    'indexed' + 'DB',
    'IDB' + 'Database',
    'IDB' + 'Transaction',
    'idb-keyval',
    'dexie',
    'localforage',
  ]

  const offenders: string[] = []

  for (const file of await scanRoots(['web/src', 'web/package.json', 'web/pnpm-lock.yaml'])) {
    if (basename(file) === 'no-browser-session-db.static.spec.ts') continue
    const source = await readFile(file, 'utf8')

    for (const token of forbidden) {
      if (source.includes(token)) offenders.push(`${repoRelative(file)}: ${token}`)
    }
  }

  assert.deepEqual(offenders, [])
})

test('desktop state/runtime/realtime modules do not use browser storage as a session cache', async () => {
  const forbidden = [
    'local' + 'Storage',
    'session' + 'Storage',
  ]
  const offenders: string[] = []

  for (const file of await scanRoots([
    'web/src/features/desktop/state',
    'web/src/features/desktop/runtime',
    'web/src/features/desktop/realtime',
  ])) {
    if (basename(file) === 'no-browser-session-db.static.spec.ts') continue
    const source = await readFile(file, 'utf8')

    for (const token of forbidden) {
      if (source.includes(token)) offenders.push(`${repoRelative(file)}: ${token}`)
    }
  }

  assert.deepEqual(offenders, [])
})
