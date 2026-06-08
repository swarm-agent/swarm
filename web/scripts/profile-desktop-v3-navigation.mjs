#!/usr/bin/env node
import { chromium } from 'playwright'
import { mkdir, writeFile } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { randomUUID } from 'node:crypto'

const DEFAULT_LOADING_TEXTS = ['Loading session…', 'Loading session...', 'Loading conversation…', 'Loading conversation...']
const DEFAULT_STABLE_TIMEOUT_MS = 45_000
const DEFAULT_POLL_MS = 50

function parseArgs(argv) {
  const args = {
    url: '',
    out: '',
    headed: false,
    slowMo: 0,
    timeoutMs: DEFAULT_STABLE_TIMEOUT_MS,
    pollMs: DEFAULT_POLL_MS,
    chromePath: process.env.CHROME_PATH || '',
  }
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i]
    if (arg === '--headed') {
      args.headed = true
      continue
    }
    if (arg === '--out') {
      args.out = argv[++i] ?? ''
      continue
    }
    if (arg === '--timeout-ms') {
      args.timeoutMs = Number(argv[++i] ?? args.timeoutMs)
      continue
    }
    if (arg === '--poll-ms') {
      args.pollMs = Number(argv[++i] ?? args.pollMs)
      continue
    }
    if (arg === '--slow-mo') {
      args.slowMo = Number(argv[++i] ?? 0)
      continue
    }
    if (arg === '--chrome-path') {
      args.chromePath = argv[++i] ?? ''
      continue
    }
    if (arg === '-h' || arg === '--help') {
      printUsage(0)
    }
    if (!args.url) {
      args.url = arg
      continue
    }
    throw new Error(`unexpected argument: ${arg}`)
  }
  if (!args.url) {
    printUsage(2)
  }
  if (!args.out) {
    const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
    args.out = resolve(root, `.tmp/desktop-v3-navigation-profile-${Date.now()}-${randomUUID().slice(0, 8)}.json`)
  }
  args.timeoutMs = Number.isFinite(args.timeoutMs) && args.timeoutMs > 0 ? args.timeoutMs : DEFAULT_STABLE_TIMEOUT_MS
  args.pollMs = Number.isFinite(args.pollMs) && args.pollMs > 0 ? args.pollMs : DEFAULT_POLL_MS
  args.slowMo = Number.isFinite(args.slowMo) && args.slowMo >= 0 ? args.slowMo : 0
  return args
}

function printUsage(code) {
  const usage = `Usage: node web/scripts/profile-desktop-v3-navigation.mjs <desktop-session-url> [--out <json>] [--headed] [--chrome-path <path>]

Loads the supplied Desktop session URL in local Chromium, records initial-load loading states and network timings, clicks the next sidebar session below the current one, and records navigation/loading/network timings.
`
  if (code === 0) {
    console.log(usage)
  } else {
    console.error(usage)
  }
  process.exit(code)
}

function nowMs() {
  return Math.round(performance.now())
}

function textSelector(text) {
  return `text=${text}`
}

async function visibleLoadingTexts(page) {
  const visible = []
  for (const text of DEFAULT_LOADING_TEXTS) {
    const count = await page.locator(textSelector(text)).count().catch(() => 0)
    if (count === 0) {
      continue
    }
    const first = page.locator(textSelector(text)).first()
    if (await first.isVisible().catch(() => false)) {
      visible.push(text)
    }
  }
  return [...new Set(visible)]
}

async function monitorLoadingPhase(page, label, run, options) {
  const start = nowMs()
  const samples = []
  let lastKey = ''
  let currentInterval = null
  const intervals = []
  let stopped = false

  const poll = async () => {
    if (stopped) {
      return
    }
    const elapsedMs = nowMs() - start
    const texts = await visibleLoadingTexts(page)
    const url = page.url()
    const key = texts.join('|')
    if (key || key !== lastKey) {
      samples.push({ elapsedMs, texts, url })
    }
    if (key !== lastKey) {
      if (currentInterval) {
        currentInterval.endMs = elapsedMs
        currentInterval.durationMs = currentInterval.endMs - currentInterval.startMs
        intervals.push(currentInterval)
        currentInterval = null
      }
      if (key) {
        currentInterval = { texts, startMs: elapsedMs, endMs: elapsedMs, durationMs: 0 }
      }
      lastKey = key
    }
  }

  const timer = setInterval(() => {
    void poll().catch(() => undefined)
  }, options.pollMs)
  await poll()
  let error = null
  try {
    await run()
  } catch (err) {
    error = err instanceof Error ? err.message : String(err)
  } finally {
    stopped = true
    clearInterval(timer)
    await poll().catch(() => undefined)
    const end = nowMs() - start
    if (currentInterval) {
      currentInterval.endMs = end
      currentInterval.durationMs = currentInterval.endMs - currentInterval.startMs
      intervals.push(currentInterval)
    }
  }

  const totalLoadingMs = intervals.reduce((sum, interval) => sum + interval.durationMs, 0)
  const longestLoadingInterval = intervals.slice().sort((a, b) => b.durationMs - a.durationMs)[0] ?? null
  return {
    label,
    startMs: start,
    endMs: nowMs(),
    durationMs: nowMs() - start,
    totalLoadingMs,
    longestLoadingInterval,
    intervals,
    samples,
    error,
  }
}

function isApiUrl(url) {
  return /\/v[123]\//.test(url) || url.includes('/workspace-overview') || url.includes('/api/')
}

function summarizeNetwork(entries, phaseStartMs, phaseEndMs) {
  return entries
    .filter((entry) => entry.startMs >= phaseStartMs && entry.startMs <= phaseEndMs)
    .map((entry) => ({
      method: entry.method,
      url: entry.url,
      status: entry.status,
      resourceType: entry.resourceType,
      startOffsetMs: entry.startMs - phaseStartMs,
      endOffsetMs: entry.endMs == null ? null : entry.endMs - phaseStartMs,
      durationMs: entry.durationMs,
      failure: entry.failure,
      encodedBodySize: entry.encodedBodySize,
    }))
    .sort((a, b) => (b.durationMs ?? -1) - (a.durationMs ?? -1))
}

async function waitForInitialConversation(page, timeoutMs) {
  await page.waitForFunction(
    (loadingTexts) => {
      const bodyText = document.body?.innerText || ''
      const hasLoading = loadingTexts.some((text) => bodyText.includes(text))
      const hasSidebar = Boolean(document.querySelector('[data-testid="desktop-workspace-sidebar"]'))
      return hasSidebar && !hasLoading && !bodyText.includes('No workspace selected') && !bodyText.includes('Workspace not found')
    },
    DEFAULT_LOADING_TEXTS,
    { timeout: timeoutMs },
  )
}

async function sidebarSessionLinks(page) {
  return page.evaluate(() => {
    const aside = document.querySelector('[data-testid="desktop-workspace-sidebar"]')
    if (!aside) {
      return []
    }
    return Array.from(aside.querySelectorAll('a[href]'))
      .map((anchor, index) => {
        const href = anchor.getAttribute('href') || ''
        const text = anchor.textContent?.replace(/\s+/g, ' ').trim() || ''
        const rect = anchor.getBoundingClientRect()
        return { href, text, index, top: rect.top, left: rect.left, width: rect.width, height: rect.height }
      })
      .filter((entry) => entry.width > 0 && entry.height > 0)
  })
}

function sessionIdFromUrl(rawUrl) {
  const url = new URL(rawUrl)
  const parts = url.pathname.split('/').filter(Boolean)
  return decodeURIComponent(parts[parts.length - 1] || '')
}

function chooseNextSessionLink(links, currentSessionId) {
  const sessionLinks = links.filter((entry) => {
    const parts = entry.href.split('?')[0].split('#')[0].split('/').filter(Boolean)
    return parts.length >= 2 && !['settings', 'flow', 'tools', 'integrations'].includes(parts[parts.length - 1] || '')
  })
  const currentIndex = sessionLinks.findIndex((entry) => {
    const parts = entry.href.split('?')[0].split('#')[0].split('/').filter(Boolean)
    return decodeURIComponent(parts[parts.length - 1] || '') === currentSessionId
  })
  if (currentIndex >= 0 && currentIndex + 1 < sessionLinks.length) {
    return sessionLinks[currentIndex + 1]
  }
  return sessionLinks.find((entry) => {
    const parts = entry.href.split('?')[0].split('#')[0].split('/').filter(Boolean)
    return decodeURIComponent(parts[parts.length - 1] || '') !== currentSessionId
  }) ?? null
}

async function collectBrowserPerformance(page) {
  return page.evaluate(() => {
    const resources = performance.getEntriesByType('resource').map((entry) => ({
      name: entry.name,
      initiatorType: entry.initiatorType,
      startTime: Math.round(entry.startTime),
      duration: Math.round(entry.duration),
      transferSize: 'transferSize' in entry ? entry.transferSize : 0,
      encodedBodySize: 'encodedBodySize' in entry ? entry.encodedBodySize : 0,
      decodedBodySize: 'decodedBodySize' in entry ? entry.decodedBodySize : 0,
    }))
    const nav = performance.getEntriesByType('navigation').map((entry) => ({
      name: entry.name,
      startTime: Math.round(entry.startTime),
      duration: Math.round(entry.duration),
      domContentLoadedEventEnd: Math.round(entry.domContentLoadedEventEnd),
      loadEventEnd: Math.round(entry.loadEventEnd),
      responseEnd: Math.round(entry.responseEnd),
    }))
    return { resources, navigation: nav, longTasks: window.__swarmLongTasks || [] }
  })
}

async function main() {
  const args = parseArgs(process.argv.slice(2))
  const launchOptions = {
    headless: !args.headed,
    slowMo: args.slowMo,
    args: ['--disable-dev-shm-usage'],
  }
  if (args.chromePath) {
    launchOptions.executablePath = args.chromePath
  }

  const browser = await chromium.launch(launchOptions)
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  const page = await context.newPage()
  page.setDefaultTimeout(args.timeoutMs)

  await page.addInitScript(() => {
    window.__swarmLongTasks = []
    try {
      const observer = new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          window.__swarmLongTasks.push({
            name: entry.name,
            startTime: Math.round(entry.startTime),
            duration: Math.round(entry.duration),
          })
        }
      })
      observer.observe({ entryTypes: ['longtask'] })
    } catch {
      // Long-task performance entries are best-effort profiling data.
    }
  })

  const network = []
  page.on('request', (request) => {
    network.push({
      id: `${network.length + 1}`,
      method: request.method(),
      url: request.url(),
      resourceType: request.resourceType(),
      startMs: nowMs(),
      endMs: null,
      durationMs: null,
      status: null,
      failure: null,
      encodedBodySize: null,
    })
  })
  page.on('response', async (response) => {
    const request = response.request()
    const entry = [...network].reverse().find((item) => item.url === request.url() && item.method === request.method() && item.status == null)
    if (!entry) {
      return
    }
    entry.status = response.status()
    try {
      const headers = response.headers()
      const length = Number(headers['content-length'] || 0)
      if (Number.isFinite(length) && length > 0) {
        entry.encodedBodySize = length
      }
    } catch {
      // ignore header read failures
    }
  })
  page.on('requestfinished', async (request) => {
    const entry = [...network].reverse().find((item) => item.url === request.url() && item.method === request.method() && item.endMs == null)
    if (!entry) {
      return
    }
    entry.endMs = nowMs()
    entry.durationMs = entry.endMs - entry.startMs
    try {
      const sizes = await request.sizes()
      entry.encodedBodySize = sizes.responseBodySize || entry.encodedBodySize
    } catch {
      // ignore
    }
  })
  page.on('requestfailed', (request) => {
    const entry = [...network].reverse().find((item) => item.url === request.url() && item.method === request.method() && item.endMs == null)
    if (!entry) {
      return
    }
    entry.endMs = nowMs()
    entry.durationMs = entry.endMs - entry.startMs
    entry.failure = request.failure()?.errorText ?? 'request failed'
  })

  const consoleMessages = []
  page.on('console', (message) => {
    const text = message.text()
    if (/desktop|v3|Loading|hydrate|navigation/i.test(text)) {
      consoleMessages.push({ atMs: nowMs(), type: message.type(), text })
    }
  })

  const startedAt = new Date().toISOString()
  const initialPhase = await monitorLoadingPhase(page, 'initial-load', async () => {
    await page.goto(args.url, { waitUntil: 'domcontentloaded', timeout: args.timeoutMs })
    await waitForInitialConversation(page, args.timeoutMs)
  }, args)

  const currentSessionId = sessionIdFromUrl(page.url())
  const linksBeforeClick = await sidebarSessionLinks(page)
  const target = chooseNextSessionLink(linksBeforeClick, currentSessionId)
  if (!target) {
    throw new Error(`could not find sidebar session below current session ${currentSessionId}`)
  }
  const targetUrl = new URL(target.href, page.url()).toString()
  const targetSessionId = sessionIdFromUrl(targetUrl)

  const clickPhase = await monitorLoadingPhase(page, 'sidebar-click-next-session', async () => {
    await page.locator(`[href="${target.href}"]`).first().click()
    await page.waitForURL((url) => sessionIdFromUrl(url.toString()) === targetSessionId, { timeout: args.timeoutMs })
    await waitForInitialConversation(page, args.timeoutMs)
  }, args)

  await page.waitForTimeout(500)
  const browserPerformance = await collectBrowserPerformance(page)
  const apiNetwork = network.filter((entry) => isApiUrl(entry.url))
  const initialApi = summarizeNetwork(apiNetwork, initialPhase.startMs, initialPhase.endMs)
  const clickApi = summarizeNetwork(apiNetwork, clickPhase.startMs, clickPhase.endMs)
  const allNetworkByDuration = network
    .filter((entry) => entry.durationMs != null)
    .sort((a, b) => (b.durationMs ?? 0) - (a.durationMs ?? 0))
    .slice(0, 40)

  const result = {
    ok: !initialPhase.error && !clickPhase.error,
    startedAt,
    inputUrl: args.url,
    finalUrl: page.url(),
    currentSessionId,
    targetSessionId,
    targetSidebarLink: target,
    phases: {
      initialLoad: {
        ...initialPhase,
        slowestApi: initialApi.slice(0, 10),
      },
      sidebarClick: {
        ...clickPhase,
        slowestApi: clickApi.slice(0, 10),
      },
    },
    loadingSummary: {
      initialLoadTotalLoadingMs: initialPhase.totalLoadingMs,
      initialLoadLongestLoading: initialPhase.longestLoadingInterval,
      sidebarClickTotalLoadingMs: clickPhase.totalLoadingMs,
      sidebarClickLongestLoading: clickPhase.longestLoadingInterval,
    },
    network: {
      slowestOverall: allNetworkByDuration,
      api: apiNetwork,
    },
    browserPerformance,
    consoleMessages,
    sidebarLinksBeforeClick: linksBeforeClick,
  }

  await mkdir(dirname(args.out), { recursive: true })
  await writeFile(args.out, JSON.stringify(result, null, 2))
  await browser.close()

  console.log(JSON.stringify({
    ok: result.ok,
    out: args.out,
    currentSessionId,
    targetSessionId,
    initialLoadTotalLoadingMs: result.loadingSummary.initialLoadTotalLoadingMs,
    sidebarClickTotalLoadingMs: result.loadingSummary.sidebarClickTotalLoadingMs,
    initialSlowestApi: initialApi.slice(0, 5),
    clickSlowestApi: clickApi.slice(0, 5),
  }, null, 2))

  if (!result.ok) {
    process.exitCode = 1
  }
}

main().catch((err) => {
  console.error(err instanceof Error ? err.stack || err.message : String(err))
  process.exit(1)
})
