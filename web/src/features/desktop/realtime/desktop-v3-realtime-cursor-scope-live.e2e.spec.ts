import assert from 'node:assert/strict'
import { mkdirSync, writeFileSync } from 'node:fs'
import { mkdtemp } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { chromium, type Page } from 'playwright'

const ENABLED = process.env.SWARM_V3_CURSOR_SCOPE_E2E === '1'
const TARGET_URL = (process.env.SWARM_V3_CURSOR_SCOPE_URL || '').trim()
const TIMEOUT_MS = Number(process.env.SWARM_V3_CURSOR_SCOPE_TIMEOUT_MS || 60_000)

type CursorEvidence = {
  source: string
  cursor: string
  url?: string
}

type CursorErrorEvidence = {
  url: string
  code: string
  message: string
  raw: unknown
}

type BrowserCursorDiagnostic = {
  websocketUrls: string[]
  realtimeStreamUrls: string[]
  snapshotCursors: CursorEvidence[]
  cursorErrors: CursorErrorEvidence[]
  websocketCloses: Array<{ url: string; code?: number; reason?: string }>
  websocketErrors: Array<{ url: string }>
  consoleErrors: string[]
}

type DiagnosticSummary = {
  ok: boolean
  targetURL: string
  evidenceDir: string
  realtimeStreamUrls: string[]
  realtimeEndpointCursors: string[]
  snapshotCursors: CursorEvidence[]
  matchingSnapshotCursorUses: CursorEvidence[]
  cursorErrors: CursorErrorEvidence[]
  websocketCloses: Array<{ url: string; code?: number; reason?: string }>
  websocketErrors: Array<{ url: string }>
  consoleErrors: string[]
}

function unique(values: string[]): string[] {
  return Array.from(new Set(values.filter((value) => value.trim())))
}

function endpointCursorFromRealtimeURL(value: string): string {
  try {
    return new URL(value).searchParams.get('endpoint_cursor')?.trim() ?? ''
  } catch {
    return ''
  }
}

function writeEvidence(evidenceDir: string, summary: DiagnosticSummary): void {
  mkdirSync(evidenceDir, { recursive: true })
  writeFileSync(join(evidenceDir, 'desktop-v3-realtime-cursor-scope-summary.json'), `${JSON.stringify(summary, null, 2)}\n`)
}

async function installCursorScopeInstrumentation(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const global = window as unknown as { __desktopV3CursorScopeDiagnostic?: BrowserCursorDiagnostic }
    const diagnostic: BrowserCursorDiagnostic = {
      websocketUrls: [],
      realtimeStreamUrls: [],
      snapshotCursors: [],
      cursorErrors: [],
      websocketCloses: [],
      websocketErrors: [],
      consoleErrors: [],
    }
    global.__desktopV3CursorScopeDiagnostic = diagnostic

    window.addEventListener('desktop:v3-realtime-snapshot-cursor', (event) => {
      const detail = (event as CustomEvent<{ endpointCursor?: string }>).detail
      const cursor = detail?.endpointCursor?.trim() ?? ''
      if (cursor) {
        diagnostic.snapshotCursors.push({ source: 'desktop:v3-realtime-snapshot-cursor', cursor })
      }
    })

    const nativeConsoleError = console.error.bind(console)
    console.error = (...args: unknown[]) => {
      diagnostic.consoleErrors.push(args.map((arg) => {
        if (typeof arg === 'string') return arg
        try {
          return JSON.stringify(arg)
        } catch {
          return String(arg)
        }
      }).join(' '))
      nativeConsoleError(...args)
    }

    const nativeFetch = window.fetch.bind(window)
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const response = await nativeFetch(input, init)
      const url = typeof input === 'string'
        ? input
        : input instanceof URL
          ? input.toString()
          : input.url
      const path = (() => {
        try {
          return new URL(url, window.location.href).pathname
        } catch {
          return url
        }
      })()
      if (path === '/v3/sync/bootstrap' || path === '/v3/sessions:reconnect' || path === '/v3/sessions:workset') {
        void response.clone().json()
          .then((body: { snapshot_endpoint_cursor?: unknown }) => {
            const cursor = String(body?.snapshot_endpoint_cursor ?? '').trim()
            if (cursor) {
              diagnostic.snapshotCursors.push({ source: `${path} snapshot_endpoint_cursor`, cursor, url: String(url) })
            }
          })
          .catch(() => undefined)
      }
      return response
    }

    const NativeWebSocket = window.WebSocket
    const WrappedWebSocket = function WebSocket(url: string | URL, protocols?: string | string[]) {
      const urlString = String(url)
      diagnostic.websocketUrls.push(urlString)
      const socket = protocols === undefined ? new NativeWebSocket(url) : new NativeWebSocket(url, protocols)
      if (urlString.includes('/v3/realtime/stream')) {
        diagnostic.realtimeStreamUrls.push(urlString)
        socket.addEventListener('message', (event) => {
          if (typeof event.data !== 'string') return
          try {
            const payload = JSON.parse(event.data) as { kind?: unknown; type?: unknown; error_code?: unknown; error?: unknown; message?: unknown }
            const kind = String(payload.kind ?? payload.type ?? '').trim()
            const code = String(payload.error_code ?? '').trim()
            const message = String(payload.error ?? payload.message ?? '').trim()
            if (kind === 'cursor.error' || code || message.includes('cursor scope mismatch') || message.includes('scope mismatch')) {
              diagnostic.cursorErrors.push({ url: urlString, code, message, raw: payload })
            }
          } catch {
            // Ignore non-JSON frames.
          }
        })
        socket.addEventListener('close', (event) => {
          diagnostic.websocketCloses.push({ url: urlString, code: event.code, reason: event.reason })
        })
        socket.addEventListener('error', () => {
          diagnostic.websocketErrors.push({ url: urlString })
        })
      }
      return socket
    } as typeof WebSocket
    WrappedWebSocket.prototype = NativeWebSocket.prototype
    Object.setPrototypeOf(WrappedWebSocket, NativeWebSocket)
    window.WebSocket = WrappedWebSocket
  })
}

async function readDiagnostic(page: Page): Promise<BrowserCursorDiagnostic> {
  return await page.evaluate(() => (window as unknown as { __desktopV3CursorScopeDiagnostic?: BrowserCursorDiagnostic }).__desktopV3CursorScopeDiagnostic ?? {
    websocketUrls: [],
    realtimeStreamUrls: [],
    snapshotCursors: [],
    cursorErrors: [],
    websocketCloses: [],
    websocketErrors: [],
    consoleErrors: [],
  })
}

test('live Desktop V3 realtime stream does not open with a snapshot/sync cursor', { skip: !ENABLED, timeout: Math.max(90_000, TIMEOUT_MS + 30_000) }, async () => {
  assert.ok(TARGET_URL, 'Set SWARM_V3_CURSOR_SCOPE_URL to the live testbench conversation URL before enabling this diagnostic')

  const evidenceDir = process.env.SWARM_E2E_EVIDENCE_DIR || await mkdtemp(join(tmpdir(), 'swarm-v3-realtime-cursor-scope-'))
  const browser = await chromium.launch({ headless: process.env.SWARM_E2E_HEADFUL !== '1' })
  let page: Page | null = null
  let summary: DiagnosticSummary | null = null

  try {
    page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
    await installCursorScopeInstrumentation(page)
    await page.goto(TARGET_URL, { waitUntil: 'domcontentloaded', timeout: TIMEOUT_MS })
    await page.waitForFunction(() => {
      const diagnostic = (window as unknown as { __desktopV3CursorScopeDiagnostic?: BrowserCursorDiagnostic }).__desktopV3CursorScopeDiagnostic
      return Boolean(diagnostic && diagnostic.realtimeStreamUrls.length > 0)
    }, undefined, { timeout: TIMEOUT_MS })
    await page.waitForTimeout(3_000)

    const diagnostic = await readDiagnostic(page)
    const realtimeEndpointCursors = unique(diagnostic.realtimeStreamUrls.map(endpointCursorFromRealtimeURL))
    const snapshotCursorValues = new Set(diagnostic.snapshotCursors.map((entry) => entry.cursor))
    const matchingSnapshotCursorUses = realtimeEndpointCursors
      .filter((cursor) => snapshotCursorValues.has(cursor))
      .map((cursor) => diagnostic.snapshotCursors.find((entry) => entry.cursor === cursor)!)
    const scopeMismatchErrors = diagnostic.cursorErrors.filter((entry) => {
      const joined = `${entry.code} ${entry.message} ${JSON.stringify(entry.raw)}`
      return joined.includes('endpoint_cursor_scope_mismatch') || joined.includes('sync cursor scope mismatch') || joined.includes('cursor scope mismatch')
    })

    summary = {
      ok: matchingSnapshotCursorUses.length === 0 && scopeMismatchErrors.length === 0,
      targetURL: TARGET_URL,
      evidenceDir,
      realtimeStreamUrls: diagnostic.realtimeStreamUrls,
      realtimeEndpointCursors,
      snapshotCursors: diagnostic.snapshotCursors,
      matchingSnapshotCursorUses,
      cursorErrors: diagnostic.cursorErrors,
      websocketCloses: diagnostic.websocketCloses,
      websocketErrors: diagnostic.websocketErrors,
      consoleErrors: diagnostic.consoleErrors,
    }
    writeEvidence(evidenceDir, summary)

    const failures: string[] = []
    if (matchingSnapshotCursorUses.length > 0) {
      failures.push(`Desktop opened /v3/realtime/stream with snapshot/sync cursor(s): ${matchingSnapshotCursorUses.map((entry) => `${entry.cursor} from ${entry.source}`).join(', ')}`)
    }
    if (scopeMismatchErrors.length > 0) {
      failures.push(`Server reported realtime cursor scope mismatch: ${scopeMismatchErrors.map((entry) => entry.code || entry.message || JSON.stringify(entry.raw)).join(', ')}`)
    }

    assert.equal(failures.length, 0, `Desktop V3 realtime cursor scope diagnostic failed.\n${failures.join('\n')}\nEvidence: ${JSON.stringify(summary, null, 2)}`)
  } catch (error) {
    if (page) {
      await page.screenshot({ path: join(evidenceDir, 'desktop-v3-realtime-cursor-scope-failure.png'), fullPage: true }).catch(() => undefined)
      if (!summary) {
        const diagnostic = await readDiagnostic(page).catch(() => null)
        writeEvidence(evidenceDir, {
          ok: false,
          targetURL: TARGET_URL,
          evidenceDir,
          realtimeStreamUrls: diagnostic?.realtimeStreamUrls ?? [],
          realtimeEndpointCursors: diagnostic ? unique(diagnostic.realtimeStreamUrls.map(endpointCursorFromRealtimeURL)) : [],
          snapshotCursors: diagnostic?.snapshotCursors ?? [],
          matchingSnapshotCursorUses: [],
          cursorErrors: diagnostic?.cursorErrors ?? [],
          websocketCloses: diagnostic?.websocketCloses ?? [],
          websocketErrors: diagnostic?.websocketErrors ?? [],
          consoleErrors: diagnostic?.consoleErrors ?? [],
        })
      }
    }
    throw error
  } finally {
    await browser.close().catch(() => undefined)
  }
})
