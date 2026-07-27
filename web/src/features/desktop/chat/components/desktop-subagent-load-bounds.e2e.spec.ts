import assert from 'node:assert/strict'
import test from 'node:test'
import { chromium } from 'playwright'

const ENABLED = process.env.SWARM_DESKTOP_SUBAGENT_LOAD_E2E === '1'
const DESKTOP_URL = (process.env.SWARM_DESKTOP_SUBAGENT_LOAD_URL || '').trim()

interface SocketFrame { direction: 'in' | 'out'; socket: number; frame: Record<string, unknown> }

function sessionFrames(frames: SocketFrame[], kind: string): string[] {
  return frames
    .filter((entry) => entry.direction === 'out' && entry.frame.kind === kind)
    .map((entry) => String(entry.frame.session_id || ''))
    .filter(Boolean)
}

test('bounded subagent rows retain one socket and only selected or viewed child subscriptions', { skip: !ENABLED, timeout: 90_000 }, async () => {
  assert.ok(DESKTOP_URL, 'SWARM_DESKTOP_SUBAGENT_LOAD_URL must point at the bounded seeded task-card fixture')
  const browser = await chromium.launch()
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  await page.addInitScript(() => {
    const frames: SocketFrame[] = []
    const Native = window.WebSocket
    let socketCount = 0
    window.WebSocket = function (url: string | URL, protocols?: string | string[]) {
      const socket = protocols === undefined ? new Native(url) : new Native(url, protocols)
      const socketID = ++socketCount
      const nativeSend = socket.send.bind(socket)
      socket.send = (payload: string | ArrayBufferLike | Blob | ArrayBufferView) => {
        if (typeof payload === 'string') {
          try { frames.push({ direction: 'out', socket: socketID, frame: JSON.parse(payload) }) } catch { /* non-JSON is not V3 realtime */ }
        }
        nativeSend(payload)
      }
      socket.addEventListener('message', (event) => {
        if (typeof event.data === 'string') {
          try { frames.push({ direction: 'in', socket: socketID, frame: JSON.parse(event.data) }) } catch { /* ignore */ }
        }
      })
      return socket
    } as typeof WebSocket
    window.WebSocket.prototype = Native.prototype
    Object.setPrototypeOf(window.WebSocket, Native)
    Object.assign(window, { __swarmLoadProbe: { frames, socketCount: () => socketCount } })
  })

  try {
    await page.goto(DESKTOP_URL, { waitUntil: 'networkidle' })
    const rows = page.locator('[data-child-session-id]')
    assert.ok(await rows.count() >= 20, 'fixture must contain many child launch rows')
    const visibleIndexes = await rows.evaluateAll((nodes) => nodes.map((node, index) => ({ index, visible: (node as HTMLElement).getClientRects().length > 0 })).filter((entry) => entry.visible).map((entry) => entry.index))
    const visibleCount = visibleIndexes.length
    assert.ok(visibleCount > 0 && visibleCount < await rows.count(), 'only a bounded subset should be visible')

    await rows.nth(visibleIndexes[0]).hover()
    await page.waitForTimeout(250)
    await rows.nth(visibleIndexes[visibleIndexes.length - 1]).scrollIntoViewIfNeeded()
    await rows.nth(visibleIndexes[visibleIndexes.length - 1]).hover()
    await page.waitForTimeout(250)
    const stop = rows.nth(visibleIndexes[0]).locator('[data-task-stop]')
    if (await stop.isEnabled().catch(() => false)) await stop.click()

    const beforeNavigate = await page.evaluate(() => {
      const probe = (window as unknown as { __swarmLoadProbe: { frames: SocketFrame[]; socketCount: () => number } }).__swarmLoadProbe
      return { frames: probe.frames, socketCount: probe.socketCount() }
    })
    assert.equal(beforeNavigate.socketCount, 1, 'cards must share the retained V3 realtime socket')
    const subscribed = new Set(sessionFrames(beforeNavigate.frames, 'subscribe.session'))
    assert.ok(subscribed.size <= visibleCount + 2, `subscriptions ${subscribed.size} must remain bounded by selected/pending plus viewed rows ${visibleCount}`)

    await rows.nth(visibleIndexes[0]).press('Enter')
    await page.goBack({ waitUntil: 'networkidle' })
    await page.setViewportSize({ width: 760, height: 800 })
    await page.setViewportSize({ width: 420, height: 760 })
    await page.goto(DESKTOP_URL, { waitUntil: 'networkidle' })
    await page.waitForTimeout(250)

    const afterCleanup = await page.evaluate(() => {
      const probe = (window as unknown as { __swarmLoadProbe: { frames: SocketFrame[]; socketCount: () => number } }).__swarmLoadProbe
      return { frames: probe.frames, socketCount: probe.socketCount() }
    })
    const unsubscribed = new Set(sessionFrames(afterCleanup.frames, 'unsubscribe.session'))
    for (const child of subscribed) assert.ok(unsubscribed.has(child) || sessionFrames(afterCleanup.frames, 'subscribe.session').includes(child), `child ${child} must be released or remain the selected baseline`)
    assert.ok(afterCleanup.frames.some((entry) => entry.direction === 'in' && String(entry.frame.event_type || '').includes('usage')), 'fixture must deliver a live usage update')
  } finally {
    await browser.close()
  }
})
