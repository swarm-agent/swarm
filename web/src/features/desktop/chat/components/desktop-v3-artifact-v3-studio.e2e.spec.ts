/**
 * Requirement: Artifact Studio must let a user navigate actual V3 controls across current, candidate, and prior complete previews.
 * Regression threat: metadata-only rendering or source-string tests can pass while the browser controls, native routes, or exact history are broken.
 * Production boundary: DesktopV3ArtifactV3Sidebar, DesktopV3ArtifactV3Studio, and the authenticated /artifacts-v3 preview routes.
 * This optional Playwright journey is the narrowest layer that exercises the real component/browser interaction against a live native artifact.
 */
import assert from 'node:assert/strict'
import test from 'node:test'
import { chromium } from 'playwright'

const enabled = process.env.SWARM_ARTIFACT_V3_STUDIO_E2E === '1'
const desktopURL = (process.env.SWARM_DESKTOP_URL || '').replace(/\/+$/, '')
const sessionID = (process.env.SWARM_ARTIFACT_V3_SESSION_ID || '').trim()
const sessionURL = (process.env.SWARM_ARTIFACT_V3_SESSION_URL || '').trim()

async function apiJSON(page: import('playwright').Page, route: string): Promise<Record<string, unknown>> {
  return page.evaluate(async (path) => {
    const response = await fetch(path, { credentials: 'include', headers: { Accept: 'application/json' } })
    const text = await response.text()
    if (!response.ok) throw new Error(`${path} HTTP ${response.status}: ${text.slice(0, 800)}`)
    return JSON.parse(text) as Record<string, unknown>
  }, route)
}

test('Artifact V3 Studio browser journey opens current, candidate, and prior complete previews', { skip: !enabled, timeout: 120_000 }, async () => {
  assert(desktopURL, 'set SWARM_DESKTOP_URL')
  assert(sessionID, 'set SWARM_ARTIFACT_V3_SESSION_ID to a session with a multi-turn Artifact V3 project')
  assert(sessionURL, 'set SWARM_ARTIFACT_V3_SESSION_URL to that session’s Desktop URL')
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
    await page.goto(desktopURL, { waitUntil: 'domcontentloaded' })
    await apiJSON(page, '/v1/auth/desktop/session')
    const catalog = await apiJSON(page, `/v3/sessions/${encodeURIComponent(sessionID)}/artifacts-v3`)
    const artifacts = Array.isArray(catalog.artifacts) ? catalog.artifacts as Array<Record<string, unknown>> : []
    const artifact = artifacts.find((item) => Number(item.turn_count ?? 0) >= 2)
    assert(artifact, 'expected an Artifact V3 project with at least two turns')
    await page.goto(new URL(sessionURL, desktopURL).toString(), { waitUntil: 'domcontentloaded' })
    await page.getByTestId('desktop-session-artifact-v3-sidebar').waitFor({ state: 'visible' })
    await page.locator(`[data-artifact-v3-sidebar-id="${String(artifact.artifact_id)}"]`).click()
    const studio = page.locator('[data-artifact-v3-studio]')
    await studio.waitFor({ state: 'visible' })
    await studio.locator('[data-artifact-v3-complete-preview]').waitFor({ state: 'visible' })
    const candidates = studio.locator('[data-artifact-v3-candidate]')
    assert((await candidates.count()) >= 2, 'expected complete candidate choices')
    await candidates.last().getByRole('button').first().click()
    await studio.locator('[data-artifact-v3-complete-preview]').waitFor({ state: 'visible' })
    const revisions = studio.locator('[data-artifact-v3-revision-history] button')
    assert((await revisions.count()) >= 2, 'expected exact prior revision history')
    await revisions.first().click()
    await studio.getByText(/Viewing prior revision/).waitFor({ state: 'visible' })
    assert((await studio.locator('[data-artifact-v3-diff]').count()) === 1)
    assert((await studio.locator('[data-artifact-v3-part-navigator]').count()) === 1)
  } finally {
    await browser.close()
  }
})
