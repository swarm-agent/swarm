#!/usr/bin/env node
// Read-only, production-bundle browser probe. No response bodies, titles, IDs,
// cookies, console messages or request payloads are retained in the evidence.
import { chromium } from 'playwright'
import { readFile, writeFile, stat } from 'node:fs/promises'
import { resolve, extname } from 'node:path'
import { execFileSync } from 'node:child_process'

const args = Object.fromEntries(process.argv.slice(2).map((arg, i, all) => arg.startsWith('--') ? [arg.slice(2), all[i + 1]] : []).filter(x => x.length))
if (!args.origin || !args.project || !args.bundle || !args.out) {
  console.error('Usage: node scripts/profile-project-refresh.mjs --origin <loopback Desktop URL> --project <id> --bundle <production dist> --out <private JSON> [--cards 13] [--runs 3]')
  process.exit(2)
}
const origin = new URL(args.origin)
if (!['127.0.0.1', 'localhost', '[::1]'].includes(origin.hostname) || origin.username || origin.password) throw new Error('Explicit loopback Desktop origin required')
const expected = Number(args.cards || 13)
const runs = Math.min(5, Math.max(1, Number(args.runs || 3)))
const bundle = resolve(args.bundle)
const browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : { channel: 'chrome' }), args: ['--disable-dev-shm-usage'] })
const evidence = { sourceHead: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), sourceDirty: Boolean(execFileSync('git', ['status', '--porcelain'], { encoding: 'utf8' }).trim()), conditions: { expectedCards: expected, productionAssets: true, backend: 'existing local daemon, unchanged', cold: 'new browser context; HTTP cache disabled; OS and daemon caches not flushed', warm: 'same authenticated context; fresh page hard navigation; routing disables HTTP cache in both conditions', writes: 'no probe mutations; normal application bootstrap/auth reads and catalog check run unchanged' }, runs: [] }
const sanitize = pathname => pathname.replace(/\/projects\/[^/]+/g, '/projects/:project').replace(/\/sessions\/[^/]+/g, '/sessions/:session').replace(/\/tasks\/[^/]+/g, '/tasks/:task').replace(/\/workspaces\/[^/]+/g, '/workspaces/:workspace')
try {
  for (let trial = 0; trial < runs; trial++) {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' })
    // Keep real same-origin admission, HTTP and realtime. Only static assets are
    // replaced with the exact local production build; never fixture API replies.
    await context.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.origin !== origin.origin) { await route.abort(); return }
      if (/^\/(v[123]|desktop|healthz|readyz)(\/|$)/.test(url.pathname)) { await route.continue(); return }
      const pathname = decodeURIComponent(url.pathname)
      const relative = pathname.startsWith('/assets/') || /\.[a-z0-9]+$/i.test(pathname) ? pathname.slice(1) : 'index.html'
      const file = resolve(bundle, relative)
      if (!file.startsWith(bundle + '/')) { await route.abort(); return }
      try {
        if (!(await stat(file)).isFile()) { await route.abort(); return }
        const type = ({ '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png' })[extname(file)] || 'application/octet-stream'
        await route.fulfill({ status: 200, contentType: type, body: await readFile(file) })
      } catch { await route.abort() }
    })
    let page = await context.newPage()
    await context.addInitScript(() => {
      const probe = { longTasks: [], transitions: [], tasksObserved: false, required: new Set(), hydrated: new Set(), hydration: [] }
      const fetchOriginal = window.fetch
      window.fetch = async (...input) => {
        const response = await fetchOriginal(...input)
        const url = new URL(typeof input[0] === 'string' ? input[0] : input[0] instanceof Request ? input[0].url : input[0], location.href)
        if (response.ok && (/^\/v3\/projects\/[^/]+\/tasks$/.test(url.pathname) || url.pathname === '/v3/sync/hydrate')) {
          void response.clone().json().then(body => {
            if (body.tasks) { for (const task of body.tasks) if (task.session_id) probe.required.add(task.session_id); probe.tasksObserved = true }
            if (body.session_views_by_id) for (const [id, view] of Object.entries(body.session_views_by_id)) if (view.pending_permissions !== undefined) probe.hydrated.add(id)
            probe.hydration.push({ ms: performance.now(), required: probe.required.size, ready: [...probe.required].filter(id => probe.hydrated.has(id)).length })
          }).catch(() => {})
        }
        return response
      }
      window.__projectRefreshProbe = probe
      try { new PerformanceObserver(list => { for (const item of list.getEntries()) probe.longTasks.push({ startMs: item.startTime, durationMs: item.duration }) }).observe({ entryTypes: ['longtask'] }) } catch {}
      let last = ''
      const sample = () => {
        const cards = [...document.querySelectorAll('[data-testid="orchestrate-task-card"]')]
        const visible = cards.filter(card => card.getClientRects().length > 0)
        const pending = [...document.querySelectorAll('[aria-label="Task needs your attention"]')].filter(el => /Loading pending requests/.test(el.textContent || '')).length
        const state = `${visible.length}:${pending}`
        if (state !== last) { probe.transitions.push({ ms: performance.now(), cards: visible.length, pendingDetails: pending }); last = state }
      }
      document.addEventListener('DOMContentLoaded', () => { new MutationObserver(sample).observe(document.documentElement, { childList: true, subtree: true, attributes: true, characterData: true }); sample() }, { once: true })
    })
    for (const mode of ['cold', 'warm']) {
      if (mode === 'warm') { await page.close(); page = await context.newPage() }
      const cdp = await context.newCDPSession(page)
      await cdp.send('Network.enable')
      await cdp.send('Network.setCacheDisabled', { cacheDisabled: mode === 'cold' })
      const requests = []
      const active = new Map()
      let maxConcurrent = 0
      let start = performance.now()
      let errors = 0
      const onError = () => { errors++ }
      const onRequest = request => {
        const url = new URL(request.url())
        if (!/^\/v[123]\//.test(url.pathname)) return
        const row = { path: sanitize(url.pathname), method: request.method(), startMs: performance.now() - start }
        if (url.pathname === '/v3/sync/hydrate') {
          try { const body = request.postDataJSON(); row.sessions = body.session_ids?.length || 0; row.history = body.history?.mode; row.plan = body.resources?.active_plan; row.permissions = body.resources?.permission_summaries; row.permissionDetails = body.resources?.permission_details } catch {}
        }
        requests.push(row); active.set(request, row); maxConcurrent = Math.max(maxConcurrent, active.size)
      }
      const onResponse = response => { const row = active.get(response.request()); if (row) { row.headersMs = performance.now() - start; row.status = response.status() } }
      const onEnd = request => { const row = active.get(request); if (row) { row.endMs = performance.now() - start; const timing = request.timing(); row.responseWaitMs = timing.responseStart >= 0 && timing.requestStart >= 0 ? timing.responseStart - timing.requestStart : null; row.networkQueueMs = timing.requestStart; row.failed = Boolean(request.failure()); active.delete(request) } }
      page.on('request', onRequest); page.on('response', onResponse); page.on('requestfinished', onEnd); page.on('requestfailed', onEnd); page.on('pageerror', onError)
      let failure = ''
      try {
        await page.goto(`${origin.origin}/projects/${encodeURIComponent(args.project)}`, { waitUntil: 'domcontentloaded', timeout: 60_000 })
        await page.waitForFunction(count => {
          const cards = [...document.querySelectorAll('[data-testid="orchestrate-task-card"]')].filter(el => el.getClientRects().length > 0)
          const probe = window.__projectRefreshProbe
          return probe.tasksObserved && cards.length === count && [...probe.required].every(id => probe.hydrated.has(id)) && cards.every(card => card.querySelector('button:not(:disabled)')) && ![...document.querySelectorAll('[aria-label="Task needs your attention"]')].some(el => /Loading pending requests/.test(el.textContent || ''))
        }, expected, { timeout: 45_000 })
      } catch { failure = 'Expected cards and pending controls not ready within probe timeout' }
      const usableMs = performance.now() - start
      const metrics = await page.evaluate(() => ({ longTasks: window.__projectRefreshProbe.longTasks, transitions: window.__projectRefreshProbe.transitions, hydration: window.__projectRefreshProbe.hydration, requiredSessions: window.__projectRefreshProbe.required.size, hydratedSessions: [...window.__projectRefreshProbe.required].filter(id => window.__projectRefreshProbe.hydrated.has(id)).length, cards: document.querySelectorAll('[data-testid="orchestrate-task-card"]').length, loading: Boolean(document.querySelector('#swarm-startup:not([hidden])')), navigation: performance.getEntriesByType('navigation').map(n => ({ responseEnd: n.responseEnd, domContentLoaded: n.domContentLoadedEventEnd })) }))
      evidence.runs.push({ trial: trial + 1, mode, usableMs, failure, errors, maxConcurrent, ...metrics, requests })
      console.log(JSON.stringify({ trial: trial + 1, mode, usableMs: Math.round(usableMs), cards: metrics.cards, errors, failure, requestCount: requests.length, maxConcurrent, requiredSessions: metrics.requiredSessions, hydratedSessions: metrics.hydratedSessions }))
      page.off('request', onRequest); page.off('response', onResponse); page.off('requestfinished', onEnd); page.off('requestfailed', onEnd); page.off('pageerror', onError)
      if (failure) break
    }
    await context.close()
    if (evidence.runs.at(-1)?.failure) break
  }
} finally {
  await browser.close()
  await writeFile(args.out, JSON.stringify(evidence, null, 2), { mode: 0o600 })
}
if (evidence.runs.some(run => run.failure)) process.exitCode = 1
