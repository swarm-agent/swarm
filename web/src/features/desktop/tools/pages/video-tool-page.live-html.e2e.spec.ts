import assert from 'node:assert/strict'
import test from 'node:test'

import { chromium } from 'playwright'

const ENABLED = process.env.SWARM_VIDEO_STUDIO_E2E === '1'
const DESKTOP_URL = (process.env.SWARM_DESKTOP_URL || '').replace(/\/+$/, '')
const WORKSPACE_SLUG = (process.env.SWARM_VIDEO_STUDIO_WORKSPACE_SLUG || 'swarm-go').trim()
const SESSION_ID = (process.env.SWARM_VIDEO_STUDIO_SESSION_ID || '').trim()
const PROJECT_ID = (process.env.SWARM_VIDEO_STUDIO_PROJECT_ID || '').trim()
const EXPECTED_INITIAL_PROPOSAL_ID = (process.env.SWARM_VIDEO_STUDIO_EXPECTED_INITIAL_PROPOSAL_ID || '').trim()
const EXPECTED_REVISIONS = Number(process.env.SWARM_VIDEO_STUDIO_EXPECTED_REVISIONS || '0')
const EXPECTED_FLOW = (process.env.SWARM_VIDEO_STUDIO_EXPECTED_FLOW || 'sequential').trim()
const EXPECTED_FIRST_PART_ID = (process.env.SWARM_VIDEO_STUDIO_EXPECTED_FIRST_PART_ID || '').trim()
const EXPECTED_SECOND_PART_ID = (process.env.SWARM_VIDEO_STUDIO_EXPECTED_SECOND_PART_ID || '').trim()
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

function planParts(proposal: JsonRecord): JsonRecord[] {
  const plan = proposal.plan as JsonRecord | undefined
  return (plan?.parts as JsonRecord[] | undefined) || []
}

function animationCandidateCount(part: JsonRecord): number {
  return (((part.animation_candidates as JsonRecord | undefined)?.candidates as JsonRecord[] | undefined) || []).length
}

function partIdentity(part: JsonRecord): string {
  return String(part.id || '').trim()
}

function visualIdentity(part: JsonRecord): string {
  const candidates = part.animation_candidates as JsonRecord | undefined
  const selectedSource = candidates?.selected_source as JsonRecord | undefined
  const visual = part.visual as JsonRecord | undefined
  const sources = ((candidates?.candidates as JsonRecord[] | undefined) || []).map((candidate) => {
    const source = candidate.source as JsonRecord | undefined
    return `${String(source?.collection_id || '')}:${String(source?.variant_id || '')}:${String(source?.event_seq || '')}`
  }).join('|')
  return [
    String(candidates?.selected_candidate_id || ''),
    String(selectedSource?.collection_id || ''),
    String(selectedSource?.variant_id || ''),
    String(selectedSource?.event_seq || ''),
    String(visual?.collection_id || ''),
    String(visual?.variant_id || ''),
    String(visual?.event_seq || ''),
    sources,
  ].join(':')
}

function assertSequentialFlow(proposals: JsonRecord[]): void {
  const candidateCounts = proposals.flatMap((proposal) => planParts(proposal).map(animationCandidateCount)).filter((count) => count > 0)
  assert(candidateCounts.includes(3), `expected a first clip with three iterations, got ${candidateCounts.join(', ')}`)
  assert(candidateCounts.some((count) => count >= 2), `expected a later clip with iterations, got ${candidateCounts.join(', ')}`)
}

function assertTwoClipReplacementFlow(proposals: JsonRecord[]): void {
  assert(EXPECTED_FIRST_PART_ID, 'SWARM_VIDEO_STUDIO_EXPECTED_FIRST_PART_ID is required for two-clip-replacement')
  assert(EXPECTED_SECOND_PART_ID, 'SWARM_VIDEO_STUDIO_EXPECTED_SECOND_PART_ID is required for two-clip-replacement')
  const plans = proposals.map((proposal) => ({ proposal, parts: planParts(proposal) })).filter(({ parts }) => parts.length === 2)
  assert(plans.length >= 2, `expected at least two two-part proposals, got ${plans.length}`)
  assert(EXPECTED_INITIAL_PROPOSAL_ID, 'SWARM_VIDEO_STUDIO_EXPECTED_INITIAL_PROPOSAL_ID is required for two-clip-replacement')
  const initial = plans.find(({ proposal, parts }) => proposal.id === EXPECTED_INITIAL_PROPOSAL_ID && parts.some((part) => partIdentity(part) === EXPECTED_FIRST_PART_ID) && parts.some((part) => partIdentity(part) === EXPECTED_SECOND_PART_ID))
  assert(initial, `no proposal contains both expected parts ${EXPECTED_FIRST_PART_ID}, ${EXPECTED_SECOND_PART_ID}`)
  const initialFirst = initial.parts.find((part) => partIdentity(part) === EXPECTED_FIRST_PART_ID)!
  const initialSecond = initial.parts.find((part) => partIdentity(part) === EXPECTED_SECOND_PART_ID)!
  const replacement = plans.find(({ proposal, parts }) => proposal.id !== initial.proposal.id
    && String(proposal.status || '') === 'pending'
    && proposal.base_revision_id === initial.proposal.working_revision_id
    && parts.some((part) => partIdentity(part) === EXPECTED_FIRST_PART_ID)
    && parts.some((part) => partIdentity(part) === EXPECTED_SECOND_PART_ID)
    && visualIdentity(parts.find((part) => partIdentity(part) === EXPECTED_SECOND_PART_ID)!) !== visualIdentity(initialSecond))
  assert(replacement, `no later proposal replaces only the expected second part ${EXPECTED_SECOND_PART_ID}`)
  const replacementFirst = replacement.parts.find((part) => partIdentity(part) === EXPECTED_FIRST_PART_ID)!
  const replacementSecond = replacement.parts.find((part) => partIdentity(part) === EXPECTED_SECOND_PART_ID)!
  assert.equal(visualIdentity(replacementFirst), visualIdentity(initialFirst), 'the AI continuation changed the chosen first clip')
  assert.notEqual(visualIdentity(replacementSecond), visualIdentity(initialSecond), 'the AI continuation did not replace the second clip')
  assert(animationCandidateCount(initialFirst) >= 2, 'the initial first clip has no selectable iterations')
  assert(animationCandidateCount(initialSecond) >= 2, 'the initial second clip has no selectable iterations')
  assert(animationCandidateCount(replacementSecond) >= 2, 'the replacement second clip has no selectable iterations')
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
    const revisionNavigation = page.getByLabel('Video revision navigation')
    await revisionNavigation.waitFor({ state: 'visible', timeout: 30_000 })
    const projectSummary = revisionNavigation.locator('..')
    await projectSummary.getByText(new RegExp(`r${EXPECTED_REVISIONS}(?:\\s|$)`)).waitFor({ state: 'visible', timeout: 30_000 })

    const project = await browserJSON<{ project?: JsonRecord; current_revision?: JsonRecord; confirmed_revision?: JsonRecord }>(page, `/v3/sessions/${encodeURIComponent(SESSION_ID)}/video/projects/${encodeURIComponent(PROJECT_ID)}`)
    const revisions = await browserJSON<{ revisions?: JsonRecord[] }>(page, `/v3/sessions/${encodeURIComponent(SESSION_ID)}/video/projects/${encodeURIComponent(PROJECT_ID)}/revisions`)
    const proposals = await browserJSON<{ proposals?: JsonRecord[] }>(page, `/v3/sessions/${encodeURIComponent(SESSION_ID)}/video/projects/${encodeURIComponent(PROJECT_ID)}/edit-proposals`)
    assert.equal(revisions.revisions?.length, EXPECTED_REVISIONS)
    assert.equal(String(project.project?.current_revision_id || ''), String((revisions.revisions || []).at(-1)?.id || ''))
    assert((proposals.proposals || []).some((proposal) => proposal.status === 'pending' && proposal.working_revision_id === project.project?.current_revision_id), 'current revision has no owning pending proposal')
    const proposalList = proposals.proposals || []
    if (EXPECTED_FLOW === 'sequential') assertSequentialFlow(proposalList)
    else if (EXPECTED_FLOW === 'two-clip-replacement') assertTwoClipReplacementFlow(proposalList)
    else assert.fail(`unsupported SWARM_VIDEO_STUDIO_EXPECTED_FLOW: ${EXPECTED_FLOW}`)

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
    const iterations = page.getByLabel('Current video turn')
    await iterations.waitFor({ state: 'visible', timeout: 20_000 })
    const laterFrame = page.locator('[data-video-studio-live-animation]')
    const laterBox = await laterFrame.boundingBox()
    assert(laterBox && Math.abs(laterBox.width / laterBox.height - 16 / 9) < 0.02, `pending HTML frame deformed: ${JSON.stringify(laterBox)}`)
    const laterStartPixels = await laterFrame.screenshot()
    await page.getByRole('button', { name: 'Play', exact: true }).click()
    await page.waitForTimeout(1_200)
    const laterPlayingPixels = await laterFrame.screenshot()
    assert.notDeepEqual(laterPlayingPixels, laterStartPixels, 'later HTML clip did not animate')

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
    const workingCut = page.getByLabel('Current video turn')
    const workingCards = workingCut.locator('[aria-label^="Working clip "]')
    assert(await workingCards.count() >= 2, 'pending review does not show the complete multi-clip working cut')
    await workingCut.getByText(/Still image (proposed|locked in)|Video clip (proposed|locked in)/).first().waitFor({ state: 'visible', timeout: 20_000 }).catch(() => undefined)
    assert.equal(errors.length, 0, `browser errors: ${errors.join(' | ')}`)
  } finally {
    await browser.close()
  }
})
