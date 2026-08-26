import assert from 'node:assert/strict'
import test from 'node:test'

import { chromium } from 'playwright'

const ENABLED = process.env.SWARM_VIDEO_STUDIO_E2E === '1'
const DESKTOP_URL = (process.env.SWARM_DESKTOP_URL || '').replace(/\/+$/, '')
const WORKSPACE_SLUG = (process.env.SWARM_VIDEO_STUDIO_WORKSPACE_SLUG || 'swarm-go').trim()
const SESSION_ID = (process.env.SWARM_VIDEO_STUDIO_SESSION_ID || '').trim()
const PROJECT_ID = (process.env.SWARM_VIDEO_STUDIO_PROJECT_ID || '').trim()
const EXPECTED_REVISIONS = Number(process.env.SWARM_VIDEO_STUDIO_EXPECTED_REVISIONS || '0')
const TIMEOUT_MS = Number(process.env.SWARM_VIDEO_STUDIO_TIMEOUT_MS || '120000')

type JsonRecord = Record<string, unknown>

async function browserJSON<T>(page: import('playwright').Page, route: string): Promise<T> {
  return await page.evaluate(async (innerRoute) => {
    const response = await fetch(innerRoute, { credentials: 'include' })
    const text = await response.text()
    if (!response.ok) throw new Error(`${innerRoute} HTTP ${response.status}: ${text.slice(0, 1200)}`)
    return JSON.parse(text) as T
  }, route)
}

test('testbench Video Studio preserves mixed HTML playback across revisions', { skip: !ENABLED, timeout: TIMEOUT_MS }, async () => {
  assert(DESKTOP_URL, 'SWARM_DESKTOP_URL is required')
  assert(SESSION_ID, 'SWARM_VIDEO_STUDIO_SESSION_ID is required')
  assert(PROJECT_ID, 'SWARM_VIDEO_STUDIO_PROJECT_ID is required')
  assert(Number.isFinite(EXPECTED_REVISIONS) && EXPECTED_REVISIONS >= 2, 'expected revisions must be at least 2')

  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || undefined,
  })
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } })
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()) })
  try {
    await page.goto(`${DESKTOP_URL}/${encodeURIComponent(WORKSPACE_SLUG)}/video/${encodeURIComponent(SESSION_ID)}`, { waitUntil: 'networkidle', timeout: 30_000 })
    await page.getByLabel('Selected video project').selectOption(PROJECT_ID)
    await page.getByText(`r${EXPECTED_REVISIONS}`, { exact: false }).first().waitFor({ state: 'visible', timeout: 30_000 })

    const project = await browserJSON<{ project?: JsonRecord; current_revision?: JsonRecord; confirmed_revision?: JsonRecord }>(page, `/v3/sessions/${encodeURIComponent(SESSION_ID)}/video/projects/${encodeURIComponent(PROJECT_ID)}`)
    const revisions = await browserJSON<{ revisions?: JsonRecord[] }>(page, `/v3/sessions/${encodeURIComponent(SESSION_ID)}/video/projects/${encodeURIComponent(PROJECT_ID)}/revisions`)
    const proposals = await browserJSON<{ proposals?: JsonRecord[] }>(page, `/v3/sessions/${encodeURIComponent(SESSION_ID)}/video/projects/${encodeURIComponent(PROJECT_ID)}/edit-proposals`)
    assert.equal(revisions.revisions?.length, EXPECTED_REVISIONS)
    assert.equal(String(project.project?.current_revision_id || ''), String((revisions.revisions || []).at(-1)?.id || ''))
    assert((proposals.proposals || []).some((proposal) => proposal.status === 'pending' && proposal.working_revision_id === project.project?.current_revision_id), 'current revision has no owning pending proposal')

    const pendingBadge = page.getByText(/Pending turn changes · working r/i)
    await pendingBadge.waitFor({ state: 'visible', timeout: 20_000 })
    const timelineLabels = await page.locator('button').filter({ hasText: /00:00 –|00:12 –/ }).allTextContents()
    assert(timelineLabels.length >= 2, `expected mixed timeline clips, got ${timelineLabels.join(' | ')}`)

    const firstVisual = page.getByRole('button', { name: /01\s+/ }).first()
    await firstVisual.click()
    const firstFrame = page.locator('[data-video-studio-live-animation]')
    await firstFrame.waitFor({ state: 'visible', timeout: 20_000 })
    const firstBox = await firstFrame.boundingBox()
    assert(firstBox && Math.abs(firstBox.width / firstBox.height - 16 / 9) < 0.02, `first HTML frame deformed: ${JSON.stringify(firstBox)}`)
    const firstStartPixels = await firstFrame.screenshot()
    await page.getByRole('button', { name: 'Play', exact: true }).click()
    await page.waitForTimeout(1_200)
    const firstPlayingPixels = await firstFrame.screenshot()
    assert.notDeepEqual(firstPlayingPixels, firstStartPixels, 'first HTML clip did not animate')

    const laterVisual = page.getByRole('button', { name: /02\s+/ }).first()
    await laterVisual.click()
    const iterations = page.getByLabel('Current turn iterations')
    await iterations.waitFor({ state: 'visible', timeout: 20_000 })
    const laterBox = await page.locator('[data-video-studio-live-animation]').boundingBox()
    assert(laterBox && Math.abs(laterBox.width / laterBox.height - 16 / 9) < 0.02, `pending HTML frame deformed: ${JSON.stringify(laterBox)}`)

    const earlier = page.getByRole('button', { name: '← Previous revision' })
    await earlier.click()
    await page.getByText(/Previewing r\d+;/).waitFor({ state: 'visible', timeout: 20_000 })
    await firstVisual.click()
    await page.locator('[data-video-studio-live-animation]').waitFor({ state: 'visible', timeout: 20_000 })
    const historicalStart = await page.locator('[data-video-studio-live-animation]').screenshot()
    await page.getByRole('button', { name: 'Play', exact: true }).click()
    await page.waitForTimeout(1_200)
    const historicalPlaying = await page.locator('[data-video-studio-live-animation]').screenshot()
    assert.notDeepEqual(historicalPlaying, historicalStart, 'historical HTML revision did not animate')
    assert.equal(errors.length, 0, `browser errors: ${errors.join(' | ')}`)
  } finally {
    await browser.close()
  }
})
