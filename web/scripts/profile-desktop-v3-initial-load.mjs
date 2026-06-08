#!/usr/bin/env node
import { chromium } from 'playwright'
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { randomUUID } from 'node:crypto'

const LOADING_TEXTS = ['Loading session…', 'Loading session...', 'Loading conversation…', 'Loading conversation...']

function usage(code = 0) {
  const text = `Usage: node web/scripts/profile-desktop-v3-initial-load.mjs <desktop-session-url> [--out <json>] [--headed] [--chrome-path <path>] [--cdp <http://host:port>] [--timeout-ms <ms>]

Profiles direct-load latency for one Desktop session URL. No hosts or session IDs are built in; pass the target URL explicitly.
Use --cdp to connect to an already-running Chrome with remote debugging enabled.`
  ;(code === 0 ? console.log : console.error)(text)
  process.exit(code)
}

function parseArgs(argv) {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
  const args = {
    url: '',
    out: resolve(root, `.tmp/v3-initial-load-profile-${Date.now()}-${randomUUID().slice(0, 8)}.json`),
    headed: false,
    chromePath: process.env.CHROME_PATH || '',
    cdp: process.env.CHROME_CDP || '',
    timeoutMs: 60_000,
  }
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]
    if (arg === '-h' || arg === '--help') usage(0)
    if (arg === '--headed') { args.headed = true; continue }
    if (arg === '--out') { args.out = argv[++i] || args.out; continue }
    if (arg === '--chrome-path') { args.chromePath = argv[++i] || ''; continue }
    if (arg === '--cdp') { args.cdp = argv[++i] || ''; continue }
    if (arg === '--timeout-ms') { args.timeoutMs = Number(argv[++i] || args.timeoutMs); continue }
    if (!args.url) { args.url = arg; continue }
    throw new Error(`unexpected argument: ${arg}`)
  }
  if (!args.url) usage(2)
  if (!Number.isFinite(args.timeoutMs) || args.timeoutMs <= 0) args.timeoutMs = 60_000
  return args
}

function relUrl(raw) {
  try {
    const url = new URL(raw)
    return `${url.pathname}${url.search}`
  } catch {
    return raw
  }
}

function classifyUrl(raw) {
  const path = relUrl(raw)
  if (/\/assets\/.+\.js($|\?)/.test(path)) return 'js-chunk'
  if (/\/assets\/.+\.css($|\?)/.test(path)) return 'css'
  if (/\/api\/|\/v[123]\//.test(path)) return 'api'
  if (/\/events|\/stream|websocket/i.test(path)) return 'stream'
  if (/\.(png|svg|ico|webmanifest|json)($|\?)/.test(path)) return 'asset'
  return 'document-or-other'
}

function now() {
  return Math.round(performance.now())
}

async function installPageInstrumentation(page) {
  await page.addInitScript((loadingTexts) => {
    const start = performance.now()
    const events = []
    const seen = new Map()
    const mark = (name, data = {}) => {
      const t = Math.round(performance.now() - start)
      const key = `${name}:${JSON.stringify(data)}`
      const prev = seen.get(key)
      if (prev != null && t - prev < 25) return
      seen.set(key, t)
      events.push({ t, name, ...data })
    }
    window.__swarmInitialLoadProbe = { events, startEpochMs: Date.now() }
    window.__swarmLongTasks = []
    try {
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          window.__swarmLongTasks.push({
            startTime: Math.round(entry.startTime),
            duration: Math.round(entry.duration),
            name: entry.name,
          })
        }
      }).observe({ entryTypes: ['longtask'] })
    } catch {}

    let lastLoading = ''
    let lastRows = -1
    let lastSidebar = false
    let firstRowMarked = false
    let firstNotLoadingMarked = false
    let firstTitleMarked = false
    let raf = 0
    const sample = () => {
      const bodyText = document.body?.innerText || ''
      const loading = loadingTexts.filter((text) => bodyText.includes(text)).join('|')
      if (loading !== lastLoading) {
        mark(loading ? 'loading-visible' : 'loading-hidden', { text: loading })
        lastLoading = loading
      }
      const rows = document.querySelectorAll('[data-testid="desktop-chat-row"]').length
      if (rows !== lastRows) {
        mark('chat-row-count', { rows })
        lastRows = rows
      }
      if (!firstRowMarked && rows > 0) {
        firstRowMarked = true
        mark('first-chat-row-visible', { rows })
      }
      const sidebar = Boolean(document.querySelector('[data-testid="desktop-workspace-sidebar"]'))
      if (sidebar !== lastSidebar) {
        mark(sidebar ? 'sidebar-visible' : 'sidebar-hidden')
        lastSidebar = sidebar
      }
      const title = document.querySelector('h1')?.textContent?.replace(/\s+/g, ' ').trim() || ''
      if (!firstTitleMarked && title) {
        firstTitleMarked = true
        mark('title-visible', { title: title.slice(0, 120) })
      }
      if (!firstNotLoadingMarked && document.body && sidebar && !loading && (rows > 0 || !bodyText.includes('No workspace selected'))) {
        firstNotLoadingMarked = true
        mark('first-usable-session-paint', { rows })
      }
      raf = requestAnimationFrame(sample)
    }
    document.addEventListener('DOMContentLoaded', () => mark('domcontentloaded'))
    window.addEventListener('load', () => mark('window-load'))
    const observer = new MutationObserver(() => sample())
    document.addEventListener('DOMContentLoaded', () => {
      observer.observe(document.documentElement, { childList: true, subtree: true, characterData: true })
      sample()
      raf = requestAnimationFrame(sample)
    }, { once: true })
    window.addEventListener('beforeunload', () => cancelAnimationFrame(raf))
  }, LOADING_TEXTS)
}

async function main() {
  const args = parseArgs(process.argv.slice(2))
  const browser = args.cdp
    ? await chromium.connectOverCDP(args.cdp)
    : await chromium.launch({
      headless: !args.headed,
      executablePath: args.chromePath || undefined,
      args: ['--disable-dev-shm-usage'],
    })
  const context = args.cdp
    ? (browser.contexts()[0] || await browser.newContext({ viewport: { width: 1440, height: 1000 } }))
    : await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  const page = await context.newPage()
  page.setDefaultTimeout(args.timeoutMs)
  await installPageInstrumentation(page)

  const t0 = now()
  const requests = []
  const inFlight = new Map()

  page.on('request', (request) => {
    const entry = {
      id: `${requests.length + 1}`,
      method: request.method(),
      url: request.url(),
      path: relUrl(request.url()),
      kind: classifyUrl(request.url()),
      resourceType: request.resourceType(),
      startMs: now() - t0,
      responseMs: null,
      headerDurationMs: null,
      endMs: null,
      durationMs: null,
      status: null,
      failure: null,
      encodedBodySize: null,
    }
    requests.push(entry)
    inFlight.set(request, entry)
  })
  page.on('response', async (response) => {
    const entry = inFlight.get(response.request())
    if (!entry) return
    entry.status = response.status()
    entry.responseMs = now() - t0
    entry.headerDurationMs = entry.responseMs - entry.startMs
    try {
      const length = Number(response.headers()['content-length'] || 0)
      if (Number.isFinite(length) && length > 0) entry.encodedBodySize = length
    } catch {}
  })
  page.on('requestfinished', async (request) => {
    const entry = inFlight.get(request)
    if (!entry) return
    entry.endMs = now() - t0
    entry.durationMs = entry.endMs - entry.startMs
    try {
      const sizes = await request.sizes()
      entry.encodedBodySize = sizes.responseBodySize || entry.encodedBodySize
    } catch {}
    inFlight.delete(request)
  })
  page.on('requestfailed', (request) => {
    const entry = inFlight.get(request)
    if (!entry) return
    entry.endMs = now() - t0
    entry.durationMs = entry.endMs - entry.startMs
    entry.failure = request.failure()?.errorText || 'request failed'
    inFlight.delete(request)
  })

  const consoleMessages = []
  page.on('console', (message) => {
    const text = message.text()
    if (/desktop|v3|hydrate|cache|query|loading|session/i.test(text)) {
      consoleMessages.push({ t: now() - t0, type: message.type(), text })
    }
  })

  await page.goto(args.url, { waitUntil: 'domcontentloaded', timeout: args.timeoutMs })
  await page.waitForFunction((loadingTexts) => {
    const bodyText = document.body?.innerText || ''
    const loading = loadingTexts.some((text) => bodyText.includes(text))
    const rows = document.querySelectorAll('[data-testid="desktop-chat-row"]').length
    const sidebar = Boolean(document.querySelector('[data-testid="desktop-workspace-sidebar"]'))
    return sidebar && !loading && rows > 0
  }, LOADING_TEXTS, { timeout: args.timeoutMs })
  await page.waitForTimeout(1000)

  const pageData = await page.evaluate(() => {
    const nav = performance.getEntriesByType('navigation')[0]
    return {
      probe: window.__swarmInitialLoadProbe,
      longTasks: window.__swarmLongTasks || [],
      nav: nav ? {
        responseEnd: Math.round(nav.responseEnd),
        domContentLoadedEventEnd: Math.round(nav.domContentLoadedEventEnd),
        loadEventEnd: Math.round(nav.loadEventEnd),
        duration: Math.round(nav.duration),
      } : null,
      resources: performance.getEntriesByType('resource').map((entry) => ({
        name: entry.name,
        path: (() => { try { const url = new URL(entry.name); return `${url.pathname}${url.search}` } catch { return entry.name } })(),
        initiatorType: entry.initiatorType,
        startTime: Math.round(entry.startTime),
        duration: Math.round(entry.duration),
        transferSize: Math.round(entry.transferSize || 0),
        encodedBodySize: Math.round(entry.encodedBodySize || 0),
        decodedBodySize: Math.round(entry.decodedBodySize || 0),
      })),
      finalUrl: location.href,
      title: document.querySelector('h1')?.textContent?.replace(/\s+/g, ' ').trim() || '',
      rowCount: document.querySelectorAll('[data-testid="desktop-chat-row"]').length,
      bodyExcerpt: (document.body?.innerText || '').replace(/\s+/g, ' ').trim().slice(0, 500),
    }
  })

  const events = pageData.probe?.events || []
  const firstUsable = events.find((event) => event.name === 'first-usable-session-paint') || null
  const firstRow = events.find((event) => event.name === 'first-chat-row-visible') || null
  const loadingIntervals = []
  let open = null
  for (const event of events) {
    if (event.name === 'loading-visible' && !open) open = { startMs: event.t, text: event.text }
    if (event.name === 'loading-hidden' && open) {
      loadingIntervals.push({ ...open, endMs: event.t, durationMs: event.t - open.startMs })
      open = null
    }
  }
  if (open) loadingIntervals.push({ ...open, endMs: null, durationMs: null })

  const slowRequests = requests.filter((request) => request.durationMs != null).sort((a, b) => b.durationMs - a.durationMs).slice(0, 30)
  const criticalBeforeFirstRow = requests
    .filter((request) => firstRow && request.startMs <= firstRow.t && (request.endMs == null || request.endMs <= firstRow.t + 25))
    .filter((request) => ['api', 'js-chunk', 'css', 'document-or-other'].includes(request.kind))
    .sort((a, b) => (b.durationMs || 0) - (a.durationMs || 0))
    .slice(0, 30)
  const apiBeforeUsable = requests
    .filter((request) => request.kind === 'api' && firstUsable && request.startMs <= firstUsable.t)
    .sort((a, b) => (b.durationMs || 0) - (a.durationMs || 0))

  const result = {
    ok: true,
    inputUrl: args.url,
    finalUrl: pageData.finalUrl,
    summary: {
      firstChatRowMs: firstRow?.t ?? null,
      firstUsableSessionPaintMs: firstUsable?.t ?? null,
      totalLoadingMs: loadingIntervals.reduce((sum, interval) => sum + (interval.durationMs || 0), 0),
      loadingIntervals,
      rowCount: pageData.rowCount,
      nav: pageData.nav,
      slowestRequests: slowRequests.slice(0, 10).map((request) => ({ kind: request.kind, method: request.method, path: request.path, status: request.status, startMs: request.startMs, responseMs: request.responseMs, headerDurationMs: request.headerDurationMs, durationMs: request.durationMs, size: request.encodedBodySize })),
      apiBeforeUsable: apiBeforeUsable.map((request) => ({ path: request.path, status: request.status, startMs: request.startMs, responseMs: request.responseMs, headerDurationMs: request.headerDurationMs, durationMs: request.durationMs, size: request.encodedBodySize })),
      longTasksBeforeUsable: (pageData.longTasks || []).filter((task) => !firstUsable || task.startTime <= firstUsable.t),
    },
    events,
    requests,
    criticalBeforeFirstRow,
    resources: pageData.resources,
    longTasks: pageData.longTasks,
    consoleMessages,
    page: { title: pageData.title, rowCount: pageData.rowCount, bodyExcerpt: pageData.bodyExcerpt },
  }

  await mkdir(dirname(args.out), { recursive: true })
  await writeFile(args.out, JSON.stringify(result, null, 2))
  await page.close().catch(() => {})
  if (!args.cdp) await browser.close()

  console.log(JSON.stringify({ out: args.out, ...result.summary }, null, 2))
}

main().catch((error) => {
  console.error(error instanceof Error ? error.stack || error.message : String(error))
  process.exit(1)
})
