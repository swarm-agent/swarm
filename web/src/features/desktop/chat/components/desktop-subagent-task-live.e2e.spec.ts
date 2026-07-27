import assert from 'node:assert/strict'
import { mkdirSync, writeFileSync } from 'node:fs'
import { mkdtemp } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { chromium, type Browser, type Page } from 'playwright'

const ENABLED = process.env.SWARM_DESKTOP_SUBAGENT_E2E === '1'
const DESKTOP_URL = (process.env.SWARM_DESKTOP_URL || process.env.SWARM_E2E_DESKTOP_URL || '').trim().replace(/\/+$/, '')
const PROMPT = process.env.SWARM_SUBAGENT_E2E_PROMPT || [
  'Use the task tool now. Launch exactly two saved subagents in one batched task call.',
  'Use subagent_type "parallel" for both launches.',
  'Shared task prompt: "Reply with exactly SUBAGENT_E2E_OK and do not use tools."',
  'Launch 1 meta_prompt: "child A". Launch 2 meta_prompt: "child B".',
  'Do not answer directly before the task tool call.',
].join(' ')
const TIMEOUT_MS = Number(process.env.SWARM_SUBAGENT_E2E_TIMEOUT_MS || process.env.SWARM_E2E_RUN_TIMEOUT_MS || 240_000)
const HEADFUL = process.env.SWARM_E2E_HEADFUL === '1'

type BrowserEvent = {
  epochMs: number
  kind: string
  detail?: string
  url?: string
  eventType?: string
  frameKind?: string
  sessionId?: string
  text?: string
}

type NetworkEvent = {
  epochMs: number
  method: string
  url: string
  status?: number
  requestPostData?: string | null
  responseText?: string
}

type Summary = {
  ok: boolean
  desktopURL: string
  evidenceDir: string
  prompt: string
  clickedApproval: boolean
  parentSessionIds: string[]
  childSessionIds: string[]
  observedEventTypes: string[]
  observedToolStarted: boolean
  observedToolDelta: boolean
  observedTaskText: boolean
  error?: string
}

function requireDesktopURL(): string {
  if (!DESKTOP_URL) {
    throw new Error('Set SWARM_DESKTOP_URL or pass a URL to scripts/run-desktop-subagent-task-e2e.mjs')
  }
  return DESKTOP_URL
}

function writeArtifact(evidenceDir: string, name: string, value: unknown): void {
  const body = typeof value === 'string' ? value : `${JSON.stringify(value, null, 2)}\n`
  writeFileSync(join(evidenceDir, name), body)
}

async function installBrowserInstrumentation(page: Page): Promise<void> {
  await page.addInitScript(`(() => {
    const events = [];
    const push = (event) => events.push({ epochMs: Date.now(), ...event });
    const parseFrame = (url, data) => {
      const text = typeof data === 'string' ? data : '';
      let parsed = null;
      try { parsed = text ? JSON.parse(text) : null; } catch {}
      const event = parsed && typeof parsed === 'object' ? parsed.event : null;
      const live = parsed && typeof parsed === 'object' ? parsed.live : null;
      const payload = event && typeof event === 'object' ? event.payload : null;
      const livePayload = live && typeof live === 'object' ? live.payload : null;
      const eventType = String(
        (event && event.event_type) ||
        (live && live.event_type) ||
        (payload && payload.event_type) ||
        (livePayload && livePayload.event_type) ||
        (parsed && parsed.event_type) ||
        ''
      );
      const sessionId = String(
        (event && event.session_id) ||
        (live && live.session_id) ||
        (parsed && parsed.session_id) ||
        (payload && payload.session_id) ||
        ''
      );
      push({
        kind: 'websocket.message',
        url: String(url),
        frameKind: String((parsed && (parsed.kind || parsed.type)) || ''),
        eventType,
        sessionId,
        text: text.slice(0, 20000),
      });
    };
    window.__swarmSubagentTaskE2E = {
      events,
      mark: (kind, detail) => push({ kind, detail: detail == null ? '' : String(detail) }),
      snapshot: () => events.slice(),
    };
    const NativeWebSocket = window.WebSocket;
    window.WebSocket = function(url, protocols) {
      const socket = protocols === undefined ? new NativeWebSocket(url) : new NativeWebSocket(url, protocols);
      push({ kind: 'websocket.constructed', url: String(url) });
      socket.addEventListener('open', () => push({ kind: 'websocket.open', url: String(url) }));
      socket.addEventListener('close', (event) => push({ kind: 'websocket.close', url: String(url), detail: String(event.code) }));
      socket.addEventListener('error', () => push({ kind: 'websocket.error', url: String(url) }));
      socket.addEventListener('message', (event) => parseFrame(url, event.data));
      return socket;
    };
    window.WebSocket.prototype = NativeWebSocket.prototype;
    Object.setPrototypeOf(window.WebSocket, NativeWebSocket);
  })()`)
}

async function browserEvents(page: Page): Promise<BrowserEvent[]> {
  return await page.evaluate(() => {
    const target = window as unknown as { __swarmSubagentTaskE2E?: { snapshot: () => BrowserEvent[] } }
    return target.__swarmSubagentTaskE2E?.snapshot() ?? []
  })
}

async function mark(page: Page, kind: string, detail = ''): Promise<void> {
  await page.evaluate(({ kind: eventKind, detail: eventDetail }) => {
    const target = window as unknown as { __swarmSubagentTaskE2E?: { mark: (kind: string, detail?: string) => void } }
    target.__swarmSubagentTaskE2E?.mark(eventKind, eventDetail)
  }, { kind, detail }).catch(() => undefined)
}

function collectObservedEventTypes(events: BrowserEvent[]): string[] {
  return [...new Set(events.map((event) => event.eventType || '').filter(Boolean))].sort()
}

function rawEventText(events: BrowserEvent[]): string {
  return events.map((event) => `${event.kind} ${event.eventType ?? ''} ${event.detail ?? ''} ${event.text ?? ''}`).join('\n')
}

function collectChildSessionIds(text: string): string[] {
  const ids = new Set<string>()
  const normalizedText = text.replace(/\\"/g, '"')
  const patterns = [
    /"child_session_id"\s*:\s*"([^"]+)"/g,
    /"childSessionId"\s*:\s*"([^"]+)"/g,
    /"session_id"\s*:\s*"([^"]+)"[^\n{}]*"lineage_kind"\s*:\s*"delegated_subagent"/g,
    /"lineage_kind"\s*:\s*"delegated_subagent"[^\n{}]*"session_id"\s*:\s*"([^"]+)"/g,
  ]
  for (const pattern of patterns) {
    let match: RegExpExecArray | null
    while ((match = pattern.exec(normalizedText)) !== null) {
      ids.add(match[1])
    }
  }
  return [...ids].sort()
}

function collectParentSessionIds(network: NetworkEvent[]): string[] {
  const ids = new Set<string>()
  for (const entry of network) {
    const match = entry.url.match(/\/v3\/sessions\/([^/?#]+)\/messages/)
    if (match) ids.add(decodeURIComponent(match[1]))
  }
  return [...ids].sort()
}

async function waitUntil(
  predicate: () => Promise<boolean>,
  description: string,
  timeoutMs: number,
  intervalMs = 500,
): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await predicate()) return
    await new Promise((resolve) => setTimeout(resolve, intervalMs))
  }
  throw new Error(`Timed out waiting for ${description}`)
}

async function waitForComposer(page: Page): Promise<void> {
  const composer = page.getByTestId('desktop-v3-agentic-composer').locator('textarea').first()
  await composer.waitFor({ state: 'visible', timeout: 45_000 })
  await waitUntil(async () => await composer.isEnabled(), 'Desktop composer to become enabled', 45_000)
}

async function fillAndSendPrompt(page: Page): Promise<void> {
  const composer = page.getByTestId('desktop-v3-agentic-composer').locator('textarea').first()
  await composer.fill(PROMPT)
  await mark(page, 'test.prompt.filled')
  await page.getByRole('button', { name: 'Send message' }).click()
  await mark(page, 'test.prompt.sent')
}

async function approveTaskLaunch(page: Page): Promise<boolean> {
  const approveButton = page.getByRole('button', { name: /Launch subagents|Accept|Approve|Allow/i }).first()
  await approveButton.waitFor({ state: 'visible', timeout: 90_000 })
  const label = await approveButton.textContent().catch(() => '')
  await mark(page, 'test.permission.visible', label ?? '')
  await approveButton.click()
  await mark(page, 'test.permission.clicked', label ?? '')
  return true
}

async function waitForTaskEvidence(page: Page): Promise<void> {
  await page.waitForFunction(() => {
    const target = window as unknown as { __swarmSubagentTaskE2E?: { snapshot: () => BrowserEvent[] } }
    const events = target.__swarmSubagentTaskE2E?.snapshot() ?? []
    const text = events.map((event) => `${event.eventType ?? ''} ${event.text ?? ''}`).join('\n')
    const normalizedText = text.replace(/\\"/g, '"')
    const childIds = [...normalizedText.matchAll(/"child_session_id"\s*:\s*"([^"]+)"/g)].map((match) => match[1])
    return text.includes('session.tool.started') && text.includes('session.tool.delta') && new Set(childIds).size >= 2
  }, undefined, { timeout: TIMEOUT_MS })
}

async function domSnapshot(page: Page): Promise<string> {
  return await page.evaluate(() => document.body.innerText.slice(0, 40000))
}

async function runScenario(page: Page, desktopURL: string, network: NetworkEvent[]): Promise<Summary> {
  await page.goto(desktopURL, { waitUntil: 'domcontentloaded', timeout: 60_000 })
  await mark(page, 'test.page.loaded', page.url())
  await waitForComposer(page)
  await fillAndSendPrompt(page)
  const clickedApproval = await approveTaskLaunch(page)
  await waitForTaskEvidence(page)

  const events = await browserEvents(page)
  const text = rawEventText(events)
  const bodyText = await domSnapshot(page)
  const observedEventTypes = collectObservedEventTypes(events)
  const childSessionIds = collectChildSessionIds(text)
  const parentSessionIds = collectParentSessionIds(network)

  return {
    ok: true,
    desktopURL,
    evidenceDir: '',
    prompt: PROMPT,
    clickedApproval,
    parentSessionIds,
    childSessionIds,
    observedEventTypes,
    observedToolStarted: text.includes('session.tool.started'),
    observedToolDelta: text.includes('session.tool.delta'),
    observedTaskText: /\btask\b|subagent|Launch subagents/i.test(bodyText),
  }
}

test('Desktop live URL can launch two subagents from the UI and stream task tool logs', { skip: !ENABLED, timeout: Math.max(120_000, TIMEOUT_MS + 60_000) }, async () => {
  const desktopURL = requireDesktopURL()
  const evidenceDir = process.env.SWARM_E2E_EVIDENCE_DIR || await mkdtemp(join(tmpdir(), 'swarm-desktop-subagent-task-e2e-'))
  mkdirSync(evidenceDir, { recursive: true })

  const network: NetworkEvent[] = []
  const consoleEvents: BrowserEvent[] = []
  const browser: Browser = await chromium.launch({ headless: !HEADFUL })
  let summary: Summary = {
    ok: false,
    desktopURL,
    evidenceDir,
    prompt: PROMPT,
    clickedApproval: false,
    parentSessionIds: [],
    childSessionIds: [],
    observedEventTypes: [],
    observedToolStarted: false,
    observedToolDelta: false,
    observedTaskText: false,
  }

  let page: Page | null = null
  try {
    page = await browser.newPage({ viewport: { width: 1440, height: 980 } })
    page.on('console', (message) => {
      consoleEvents.push({ epochMs: Date.now(), kind: `console.${message.type()}`, text: message.text() })
    })
    page.on('request', (request) => {
      const url = request.url()
      if (!/\/v[123]\//.test(url) && !url.includes('/v3/realtime/stream')) return
      network.push({ epochMs: Date.now(), method: request.method(), url, requestPostData: request.postData() })
    })
    page.on('response', async (response) => {
      const url = response.url()
      if (!/\/v[123]\//.test(url) && !url.includes('/v3/realtime/stream')) return
      network.push({
        epochMs: Date.now(),
        method: response.request().method(),
        url,
        status: response.status(),
        responseText: await response.text().catch(() => ''),
      })
    })
    await installBrowserInstrumentation(page)

    summary = { ...await runScenario(page, desktopURL, network), evidenceDir }
    const events = await browserEvents(page)
    const bodyText = await domSnapshot(page)

    writeArtifact(evidenceDir, 'summary.json', summary)
    writeArtifact(evidenceDir, 'browser-events.json', events)
    writeArtifact(evidenceDir, 'browser-console.json', consoleEvents)
    writeArtifact(evidenceDir, 'network.json', network)
    writeArtifact(evidenceDir, 'dom-snapshot.txt', bodyText)
    await page.screenshot({ path: join(evidenceDir, 'final.png'), fullPage: true }).catch(() => undefined)

    console.log(`desktop subagent task Playwright E2E evidence\n${JSON.stringify(summary, null, 2)}`)

    assert.equal(summary.clickedApproval, true, 'task launch approval was not clicked')
    assert.equal(summary.observedToolStarted, true, 'did not observe session.tool.started in websocket logs')
    assert.equal(summary.observedToolDelta, true, 'did not observe session.tool.delta in websocket logs')
    assert.equal(summary.childSessionIds.length, 2, `expected two child session ids in task logs, got ${summary.childSessionIds.length}: ${summary.childSessionIds.join(', ')}`)
    assert.equal(summary.observedTaskText, true, 'page did not render task/subagent text after launch')
  } catch (error) {
    const events = page ? await browserEvents(page).catch(() => []) : []
    const bodyText = page ? await domSnapshot(page).catch(() => '') : ''
    summary = { ...summary, ok: false, error: error instanceof Error ? error.message : String(error) }
    writeArtifact(evidenceDir, 'summary.json', summary)
    writeArtifact(evidenceDir, 'browser-events.json', events)
    writeArtifact(evidenceDir, 'browser-console.json', consoleEvents)
    writeArtifact(evidenceDir, 'network.json', network)
    writeArtifact(evidenceDir, 'dom-snapshot.txt', bodyText)
    if (page) await page.screenshot({ path: join(evidenceDir, 'failure.png'), fullPage: true }).catch(() => undefined)
    throw error
  } finally {
    await browser.close().catch(() => undefined)
  }
})
