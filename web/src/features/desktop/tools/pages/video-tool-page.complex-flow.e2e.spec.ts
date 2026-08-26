import assert from 'node:assert/strict'
import test from 'node:test'

import { chromium, type Page } from 'playwright'

const ENABLED = process.env.SWARM_VIDEO_STUDIO_COMPLEX_E2E === '1'
const DESKTOP_URL = (process.env.SWARM_DESKTOP_URL || '').replace(/\/+$/, '')
const WORKSPACE_SLUG = (process.env.SWARM_VIDEO_STUDIO_WORKSPACE_SLUG || 'swarm-go').trim()
let SESSION_ID = (process.env.SWARM_VIDEO_STUDIO_SESSION_ID || '').trim()
const INITIAL_PROMPT = (process.env.SWARM_VIDEO_STUDIO_INITIAL_PROMPT || [
  'Treat this as broad multi-stage work: propose a complete executable plan with plan_manage request_new_plan before implementation, then continue automatically after approval.',
  'Build one mixed multi-clip Video Studio project using a real registered source video plus live HTML.',
  'Inspect the registered video sources and use a playable 4-second source-video excerpt as the final timeline clip from 12s to 16s.',
  'In the same turn, create exactly two consecutive 6-second live HTML clips from 0s to 12s in one visual plan.',
  'Give each HTML clip at least two selectable animation iterations.',
  'Keep the project and both HTML clips 16:9, use one stable part id per HTML clip, and preserve the source-video clip as auxiliary footage.',
].join(' ')).trim()
const CONTINUATION_PROMPT = (process.env.SWARM_VIDEO_STUDIO_CONTINUATION_PROMPT || [
  'Build on the current mixed three-clip timeline.',
  'Keep the selected first HTML clip and the source-video clip exactly unchanged.',
  'Replace only the second HTML clip with a new 16:9 live HTML treatment that has at least two selectable iterations.',
  'Return one pending Video Studio revision with the same stable HTML part ids and do not flatten or remove the source video.',
].join(' ')).trim()
const APPEND_PROMPT = (process.env.SWARM_VIDEO_STUDIO_APPEND_PROMPT || [
  'Build on the current working Video Studio revision without changing or reordering any existing clip.',
  'Append exactly one new 6-second 16:9 live HTML clip after the existing source-video clip.',
  'Give the new clip at least two selectable animation iterations and one new stable part id.',
  'Return one pending Video Studio revision containing the two existing HTML parts, the unchanged source-video clip, and the new HTML part.',
].join(' ')).trim()
const REPLACE_APPENDED_PROMPT = (process.env.SWARM_VIDEO_STUDIO_REPLACE_APPENDED_PROMPT || [
  'Build on the current four-clip working Video Studio revision.',
  'Replace only the newly appended HTML clip with a different 16:9 live HTML treatment and at least two selectable iterations.',
  'Keep both earlier HTML parts and the source-video clip byte-for-byte and identity-for-identity unchanged and in the same order.',
  'Keep the appended clip stable part id and return one pending Video Studio revision.',
].join(' ')).trim()
const TIMEOUT_MS = Number(process.env.SWARM_VIDEO_STUDIO_TIMEOUT_MS || '600000')

type JsonRecord = Record<string, unknown>

async function browserJSON<T>(page: Page, route: string): Promise<T> {
  return await page.evaluate(async (innerRoute) => {
    const response = await fetch(innerRoute, { credentials: 'include' })
    const text = await response.text()
    if (!response.ok) throw new Error(`${innerRoute} HTTP ${response.status}: ${text.slice(0, 1200)}`)
    return JSON.parse(text) as T
  }, route)
}

async function browserPostJSON<T>(page: Page, route: string, body: JsonRecord): Promise<T> {
  return await page.evaluate(async ({ innerRoute, innerBody }) => {
    const response = await fetch(innerRoute, {
      credentials: 'include',
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify(innerBody),
    })
    const text = await response.text()
    if (!response.ok) throw new Error(`${innerRoute} HTTP ${response.status}: ${text.slice(0, 1200)}`)
    return JSON.parse(text) as T
  }, { innerRoute: route, innerBody: body })
}

function permissionArguments(permission: JsonRecord): JsonRecord {
  const raw = permission.tool_arguments ?? permission.arguments ?? permission.payload
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) return raw as JsonRecord
  try {
    const decoded = JSON.parse(String(raw || '{}')) as unknown
    return decoded && typeof decoded === 'object' && !Array.isArray(decoded) ? decoded as JsonRecord : {}
  } catch {
    return {}
  }
}

async function approvePendingPermissions(page: Page, runIDs: string[]): Promise<{ plan: number; other: number }> {
  const response = await browserJSON<{ permissions?: JsonRecord[] }>(page, `/v3/sessions/${SESSION_ID}/permissions?status=pending&limit=50`)
  const pending = response.permissions || []
  const wantedRunIDs = new Set(runIDs.filter(Boolean))
  const forRun = pending.filter((permission) => wantedRunIDs.size === 0 || wantedRunIDs.has(String(permission.run_id || permission.runId || '')))
  const planPermission = forRun.find((permission) => {
    const tool = String(permission.tool_name || permission.toolName || '').trim().toLowerCase().replaceAll('-', '_')
    const requirement = String(permission.requirement || '').trim().toLowerCase()
    const action = String(permissionArguments(permission).action || '').trim().toLowerCase()
    return tool === 'plan_manage' && (requirement === 'plan_new_request' || action === 'request_new_plan')
  })
  const permission = planPermission || forRun[0]
  if (!permission) return { plan: 0, other: 0 }

  const permissionID = String(permission.id || '').trim()
  const permissionRunID = String(permission.run_id || permission.runId || '').trim()
  const toolName = String(permission.tool_name || permission.toolName || '').trim()
  const action = String(permissionArguments(permission).action || '').trim().toLowerCase()
  const isPlan = permission === planPermission
  assert(permissionID, `pending ${isPlan ? 'request_new_plan' : toolName || 'tool'} approval for run ${permissionRunID || 'unknown'} has no permission id`)
  if (isPlan) assert.equal(action, 'request_new_plan', `pending plan approval ${permissionID} for run ${permissionRunID} has unexpected action ${action || 'missing'}`)

  let resolved: { permission?: JsonRecord; status?: string }
  try {
    resolved = await browserPostJSON(page, `/v3/sessions/${SESSION_ID}/permissions/${encodeURIComponent(permissionID)}/resolve`, {
      action: 'allow_once',
      reason: isPlan
        ? 'Video Studio complex E2E explicitly approves the pending request_new_plan proposal'
        : 'Video Studio complex E2E approves this checked-in test action so the bounded run can continue',
    })
  } catch (error) {
    assert.fail(`failed to approve pending ${isPlan ? 'request_new_plan' : toolName || 'tool'} permission ${permissionID} for run ${permissionRunID}: ${error instanceof Error ? error.message : String(error)}`)
  }
  const status = String(resolved.permission?.status || resolved.status || '').trim().toLowerCase()
  assert.equal(status, 'approved', `${isPlan ? 'request_new_plan' : toolName || 'tool'} permission ${permissionID} for run ${permissionRunID} resolved as ${status || 'unknown'}, want approved`)
  console.log(`[video-studio-complex-e2e] session=${SESSION_ID} run=${permissionRunID} approved_${isPlan ? 'request_new_plan' : toolName || 'tool'}=${permissionID}`)
  return isPlan ? { plan: 1, other: 0 } : { plan: 0, other: 1 }
}

async function activePlan(page: Page): Promise<JsonRecord | undefined> {
  const response = await browserJSON<{ active_plan?: JsonRecord; plan?: JsonRecord }>(page, `/v3/sessions/${SESSION_ID}/plans/active`)
  return response.active_plan || response.plan
}

function activeCheckpoint(plan: JsonRecord | undefined): JsonRecord | undefined {
  const document = plan?.document as JsonRecord | undefined
  const checkpoints = (document?.checkpoints as JsonRecord[] | undefined) || []
  return checkpoints.find((checkpoint) => ['in_progress', 'blocked', 'failed', 'needs_review'].includes(String(checkpoint.status || '').toLowerCase()))
}

async function startSession(page: Page): Promise<string> {
  await page.goto(`${DESKTOP_URL}/${encodeURIComponent(WORKSPACE_SLUG)}`, { waitUntil: 'domcontentloaded', timeout: 30_000 })
  const pane = page.getByTestId('desktop-v3-new-session-pane')
  await pane.waitFor({ state: 'visible', timeout: 30_000 })
  const responsePromise = page.waitForResponse((response) => {
    if (response.request().method() !== 'POST') return false
    try { return new URL(response.url()).pathname === '/v3/sessions:routed' } catch { return false }
  }, { timeout: 30_000 })
  const composer = pane.locator('textarea').first()
  await composer.fill(`/new ${INITIAL_PROMPT}`)
  await composer.press('Enter')
  const response = await responsePromise
  const text = await response.text()
  assert(response.ok(), `routed session start failed: HTTP ${response.status()}: ${text.slice(0, 1200)}`)
  const decoded = JSON.parse(text) as { session_id?: string; run_intent?: { run_id?: string } }
  SESSION_ID = String(decoded.session_id || '').trim()
  assert(SESSION_ID, `routed session start returned no session_id: ${text.slice(0, 1200)}`)
  await page.waitForURL((url) => url.pathname.endsWith(`/${SESSION_ID}`), { timeout: 30_000 })
  await page.getByTestId('desktop-chat-scroller').waitFor({ state: 'visible', timeout: 30_000 })
  const responseRunID = String(decoded.run_intent?.run_id || '').trim()
  if (responseRunID) return responseRunID
  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    const hydrated = await hydrateRunIntents(page)
    const runID = String(hydrated.at(-1)?.run_id || '').trim()
    if (runID) return runID
    await page.waitForTimeout(250)
  }
  assert.fail(`routed session start produced no run intent: ${text.slice(0, 1200)}`)
}

async function sendMessage(page: Page, prompt: string): Promise<string> {
  const responsePromise = page.waitForResponse((response) => {
    if (response.request().method() !== 'POST') return false
    try { return new URL(response.url()).pathname === `/v3/sessions/${SESSION_ID}/messages` } catch { return false }
  }, { timeout: 30_000 })
  const composer = page.locator('textarea').last()
  await composer.fill(prompt)
  await page.getByRole('button', { name: 'Send message' }).last().click()
  const response = await responsePromise
  const text = await response.text()
  assert(response.ok(), `message send failed: HTTP ${response.status()}: ${text.slice(0, 1200)}`)
  const decoded = JSON.parse(text) as { run_intent?: { run_id?: string } }
  const runID = String(decoded.run_intent?.run_id || '').trim()
  assert(runID, `message response has no run_id: ${text.slice(0, 1200)}`)
  return runID
}

async function hydrateRunIntents(page: Page): Promise<JsonRecord[]> {
  const hydrated = await page.evaluate(async (sessionID) => {
    const response = await fetch('/v3/sync/hydrate', {
      credentials: 'include',
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({ surface: 'desktop', session_ids: [sessionID], history: { mode: 'none' }, resources: { run_intents: true, current_run_state: true }, include_active: true }),
    })
    return response.ok ? await response.json() : {}
  }, SESSION_ID) as { run_intents_by_session?: Record<string, JsonRecord[]> }
  return hydrated.run_intents_by_session?.[SESSION_ID] || []
}

async function waitForRun(page: Page, runID: string): Promise<void> {
  const deadline = Date.now() + TIMEOUT_MS
  let nextHeartbeat = Date.now()
  let status = ''
  let trackedRunID = runID
  let planApprovals = 0
  let otherApprovals = 0
  while (Date.now() < deadline) {
    const approvals = await approvePendingPermissions(page, [runID, trackedRunID])
    planApprovals += approvals.plan
    otherApprovals += approvals.other
    const intents = await hydrateRunIntents(page)
    const checkpointIntents = intents.filter((candidate) => String(candidate.checkpoint_id || '').trim())
    if (planApprovals > 0 && checkpointIntents.length > 0) {
      trackedRunID = String(checkpointIntents.at(-1)?.run_id || trackedRunID)
    }
    const intent = intents.find((candidate) => candidate.run_id === trackedRunID)
    status = String(intent?.status || '')
    if (['completed', 'needs_review'].includes(status)) {
      const checkpoint = activeCheckpoint(await activePlan(page))
      const checkpointStatus = String(checkpoint?.status || '').toLowerCase()
      if (checkpointStatus === 'blocked' || checkpointStatus === 'failed') {
        assert.fail(`run ${trackedRunID} completed with checkpoint ${String(checkpoint?.id || 'unknown')} ${checkpointStatus}: ${String(checkpoint?.result || checkpoint?.report || checkpoint?.notes || 'no terminal detail')}`)
      }
      return
    }
    if (/failed|cancelled|expired|interrupted/i.test(status)) assert.fail(`run ${trackedRunID} ended as ${status}`)
    if (Date.now() >= nextHeartbeat) {
      console.log(`[video-studio-complex-e2e] session=${SESSION_ID} run=${trackedRunID} initial_run=${runID} status=${status || 'pending'} plan_approvals=${planApprovals} other_approvals=${otherApprovals}`)
      nextHeartbeat = Date.now() + 15_000
    }
    await page.waitForTimeout(1_000)
  }
  assert.fail(`timed out waiting for run ${trackedRunID}; initial run=${runID}; last status=${status || 'unknown'}; approved request_new_plan permissions=${planApprovals}; other approvals=${otherApprovals}`)
}

async function waitForProject(page: Page, excluded: Set<string>): Promise<JsonRecord> {
  const deadline = Date.now() + TIMEOUT_MS
  let latest: JsonRecord[] = []
  while (Date.now() < deadline) {
    latest = await projects(page)
    const created = latest.find((project) => !excluded.has(String(project.id || '')))
      || latest.sort((left, right) => Number(right.updated_at || 0) - Number(left.updated_at || 0))[0]
    if (created?.id) return created
    await page.waitForTimeout(1_000)
  }
  assert.fail(`timed out waiting for the first-turn Video Studio project; projects=${JSON.stringify(latest)}`)
}

async function projects(page: Page): Promise<JsonRecord[]> {
  const response = await browserJSON<{ projects?: JsonRecord[] }>(page, `/v3/sessions/${SESSION_ID}/video/projects?limit=32`)
  return response.projects || []
}

async function proposals(page: Page, projectID: string): Promise<JsonRecord[]> {
  const response = await browserJSON<{ proposals?: JsonRecord[] }>(page, `/v3/sessions/${SESSION_ID}/video/projects/${encodeURIComponent(projectID)}/edit-proposals`)
  return response.proposals || []
}

function parts(proposal: JsonRecord): JsonRecord[] {
  const plan = proposal.plan as JsonRecord | undefined
  return (plan?.parts as JsonRecord[] | undefined) || []
}

function partID(part: JsonRecord): string {
  return String(part.id || '').trim()
}

function candidateCount(part: JsonRecord): number {
  return (((part.animation_candidates as JsonRecord | undefined)?.candidates as JsonRecord[] | undefined) || []).length
}

function visualIdentity(part: JsonRecord): string {
  const candidates = part.animation_candidates as JsonRecord | undefined
  const selected = candidates?.selected_source as JsonRecord | undefined
  const visual = part.visual as JsonRecord | undefined
  const sources = ((candidates?.candidates as JsonRecord[] | undefined) || []).map((candidate) => {
    const source = candidate.source as JsonRecord | undefined
    return `${String(candidate.id || '')}:${String(source?.collection_id || '')}:${String(source?.variant_id || '')}:${String(source?.event_seq || '')}`
  }).join('|')
  return [
    candidates?.selected_candidate_id,
    selected?.collection_id,
    selected?.variant_id,
    selected?.event_seq,
    visual?.collection_id,
    visual?.variant_id,
    visual?.event_seq,
    sources,
  ].map((value) => String(value || '')).join(':')
}

function timelineClips(revision: JsonRecord | undefined): JsonRecord[] {
  const timeline = revision?.timeline as JsonRecord | undefined
  return (timeline?.clips as JsonRecord[] | undefined) || []
}

type IdentityLedger = {
  proposal: string
  baseRevision: string
  workingRevision: string
  partOrder: string[]
  partVisuals: Record<string, string>
  timelineOrder: string[]
  clipIdentities: Record<string, string>
}

function clipIdentity(clip: JsonRecord): string {
  const artifact = clip.artifact_ref as JsonRecord | undefined
  return [clip.id, clip.source_kind, clip.source_ref, artifact?.session_id, artifact?.collection_id, artifact?.variant_id, artifact?.event_seq, clip.source_start_ms, clip.source_end_ms, clip.timeline_start_ms, clip.timeline_end_ms, clip.duration_ms, clip.track, clip.sequence]
    .map((value) => String(value ?? '')).join(':')
}

function identityLedger(proposal: JsonRecord, revision: JsonRecord | undefined): IdentityLedger {
  const proposalParts = parts(proposal)
  const clips = timelineClips(revision)
  return {
    proposal: String(proposal.id || ''),
    baseRevision: String(proposal.base_revision_id || ''),
    workingRevision: String(proposal.working_revision_id || ''),
    partOrder: proposalParts.map(partID),
    partVisuals: Object.fromEntries(proposalParts.map((part) => [partID(part), visualIdentity(part)])),
    timelineOrder: clips.map((clip) => String(clip.id || '')),
    clipIdentities: Object.fromEntries(clips.map((clip) => [String(clip.id || ''), clipIdentity(clip)])),
  }
}

function assertPreservedIdentities(before: IdentityLedger, after: IdentityLedger, preservedPartIDs: string[], preservedClipIDs: string[]): void {
  for (const id of preservedPartIDs) assert.equal(after.partVisuals[id], before.partVisuals[id], `non-target HTML part ${id} changed`)
  for (const id of preservedClipIDs) assert.equal(after.clipIdentities[id], before.clipIdentities[id], `non-target timeline clip ${id} changed`)
  assert.deepEqual(after.timelineOrder.filter((id) => preservedClipIDs.includes(id)), before.timelineOrder.filter((id) => preservedClipIDs.includes(id)), 'preserved timeline clips were reordered')
}

function sourceVideoIdentity(clip: JsonRecord): string {
  return [
    clip.id,
    clip.source_kind,
    clip.source_ref,
    clip.source_start_ms,
    clip.source_end_ms,
    clip.timeline_start_ms,
    clip.timeline_end_ms,
    clip.duration_ms,
    clip.track,
    clip.sequence,
  ].map((value) => String(value ?? '')).join(':')
}

async function projectDetail(page: Page, projectID: string): Promise<{ current_revision?: JsonRecord }> {
  return await browserJSON<{ current_revision?: JsonRecord }>(page, `/v3/sessions/${SESSION_ID}/video/projects/${encodeURIComponent(projectID)}`)
}

async function waitForProposal(page: Page, projectID: string, predicate: (proposal: JsonRecord) => boolean, label: string): Promise<JsonRecord> {
  const deadline = Date.now() + TIMEOUT_MS
  let latest: JsonRecord[] = []
  while (Date.now() < deadline) {
    latest = await proposals(page, projectID)
    const match = latest.find(predicate)
    if (match) return match
    await page.waitForTimeout(1_000)
  }
  assert.fail(`timed out waiting for ${label}; proposals=${JSON.stringify(latest)}`)
}

async function assertClipAnimates(page: Page, part: JsonRecord): Promise<void> {
  const button = page.getByRole('button', { name: new RegExp(partID(part), 'i') }).first()
  await button.click()
  const frame = page.locator('[data-video-studio-live-animation]')
  await frame.waitFor({ state: 'visible', timeout: 20_000 })
  const box = await frame.boundingBox()
  assert(box && Math.abs(box.width / box.height - 16 / 9) < 0.02, `${partID(part)} HTML frame deformed: ${JSON.stringify(box)}`)
  const start = await frame.screenshot()
  await page.getByRole('button', { name: 'Play', exact: true }).click()
  await page.waitForTimeout(1_200)
  assert.notDeepEqual(await frame.screenshot(), start, `${partID(part)} did not animate`)
  await page.getByRole('button', { name: 'Pause', exact: true }).click()
}

async function assertSourceVideoPlays(page: Page, clip: JsonRecord): Promise<void> {
  const clipID = String(clip.id || '').trim()
  assert(clipID, 'source-video clip has no stable id')
  await page.getByRole('button', { name: new RegExp(clipID, 'i') }).first().click()
  await page.locator('[data-video-studio-live-animation]').waitFor({ state: 'hidden', timeout: 20_000 })
  const canvas = page.locator('canvas[width="1920"][height="1080"]').first()
  const box = await canvas.boundingBox()
  assert(box && Math.abs(box.width / box.height - 16 / 9) < 0.02, `source-video canvas deformed: ${JSON.stringify(box)}`)
  const start = await canvas.screenshot()
  await page.getByRole('button', { name: 'Play', exact: true }).click()
  await page.waitForTimeout(1_500)
  assert.notDeepEqual(await canvas.screenshot(), start, `source-video clip ${clipID} did not visibly play`)
  await page.getByRole('button', { name: 'Pause', exact: true }).click()
}

test('Video Studio preserves exact identities through four mixed-media iteration turns and rejects stale actions', { skip: !ENABLED, timeout: TIMEOUT_MS }, async () => {
  assert(DESKTOP_URL, 'SWARM_DESKTOP_URL is required')
  assert(Number.isFinite(TIMEOUT_MS) && TIMEOUT_MS >= 60_000, 'SWARM_VIDEO_STUDIO_TIMEOUT_MS must be at least 60000')

  const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || undefined })
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } })
  const browserErrors: string[] = []
  page.on('pageerror', (error) => browserErrors.push(error.message))
  page.on('console', (message) => { if (message.type() === 'error') browserErrors.push(message.text()) })
  try {
    const existingSession = Boolean(SESSION_ID)
    let initialRunID = ''
    let beforeProjectIDs = new Set<string>()
    if (existingSession) {
      await page.goto(`${DESKTOP_URL}/${encodeURIComponent(WORKSPACE_SLUG)}/${encodeURIComponent(SESSION_ID)}`, { waitUntil: 'networkidle', timeout: 30_000 })
      await page.getByTestId('desktop-chat-scroller').waitFor({ state: 'visible', timeout: 30_000 })
      beforeProjectIDs = new Set((await projects(page)).map((project) => String(project.id || '')))
      initialRunID = await sendMessage(page, INITIAL_PROMPT)
    } else {
      initialRunID = await startSession(page)
    }
    await waitForRun(page, initialRunID)
    const createdProject = await waitForProject(page, beforeProjectIDs)
    const projectID = String(createdProject?.id || '').trim()
    assert(projectID, 'first AI turn created no Video Studio project')
    await page.goto(`${DESKTOP_URL}/${encodeURIComponent(WORKSPACE_SLUG)}/video/${encodeURIComponent(SESSION_ID)}`, { waitUntil: 'networkidle', timeout: 30_000 })
    await page.getByLabel('Selected video project').selectOption(projectID)

    const initial = await waitForProposal(page, projectID, (proposal) => proposal.status === 'pending' && parts(proposal).length === 2 && parts(proposal).every((part) => candidateCount(part) >= 2), 'initial two-clip iterated proposal')
    const initialParts = parts(initial)
    const [firstPart, secondPart] = initialParts
    assert(partID(firstPart) && partID(secondPart) && partID(firstPart) !== partID(secondPart), 'initial proposal lacks two stable distinct HTML part ids')
    const initialDetail = await projectDetail(page, projectID)
    const initialTimelineClips = timelineClips(initialDetail.current_revision)
    const initialSourceVideos = initialTimelineClips.filter((clip) => clip.source_kind === 'source_video')
    assert.equal(initialSourceVideos.length, 1, `initial mixed timeline must contain exactly one source-video clip: ${JSON.stringify(initialTimelineClips)}`)
    const initialSourceVideo = initialSourceVideos[0]
    assert.equal(initialTimelineClips.filter((clip) => clip.source_kind === 'managed_artifact').length, 2, `initial mixed timeline must contain two managed HTML clips: ${JSON.stringify(initialTimelineClips)}`)
    assert(Number(initialSourceVideo.timeline_start_ms) >= 12_000, `source video must follow the two HTML clips: ${JSON.stringify(initialSourceVideo)}`)
    assert(Number(initialSourceVideo.duration_ms) >= 3_000, `source-video excerpt is too short to prove playback: ${JSON.stringify(initialSourceVideo)}`)

    const selectionResponse = page.waitForResponse((response) => {
      if (response.request().method() !== 'POST') return false
      try { return new URL(response.url()).pathname === `/v3/sessions/${SESSION_ID}/video/projects/${projectID}/edit-proposals/${initial.id}/animation-candidate-select` } catch { return false }
    }, { timeout: 30_000 })
    const secondCandidates = page.getByLabel(`${String(secondPart.title || partID(secondPart))} animation sources`).getByRole('button')
    assert(await secondCandidates.count() >= 2, 'second clip exposes fewer than two candidate controls')
    await secondCandidates.nth(1).click()
    const selectedProposalResponse = await selectionResponse
    const selectedProposalBody = await selectedProposalResponse.json() as { proposal?: JsonRecord }
    assert(selectedProposalResponse.ok() && selectedProposalBody.proposal, 'second clip candidate selection did not persist')
    const selectedFirstPart = parts(selectedProposalBody.proposal).find((part) => partID(part) === partID(firstPart))!
    const selectedSecondPart = parts(selectedProposalBody.proposal).find((part) => partID(part) === partID(secondPart))!
    const initialLedger = identityLedger(selectedProposalBody.proposal, initialDetail.current_revision)
    // Candidate selection itself anchors this exact clip/iteration in the AI composer.
    const secondRun = await sendMessage(page, CONTINUATION_PROMPT)
    await waitForRun(page, secondRun)

    const replacement = await waitForProposal(page, projectID, (proposal) => proposal.status === 'pending'
      && proposal.id !== initial.id
      && proposal.base_revision_id === initial.working_revision_id
      && parts(proposal).length === 2
      && parts(proposal).some((part) => partID(part) === partID(firstPart))
      && parts(proposal).some((part) => partID(part) === partID(secondPart)), 'second-clip replacement proposal')
    const replacementFirst = parts(replacement).find((part) => partID(part) === partID(firstPart))!
    const replacementSecond = parts(replacement).find((part) => partID(part) === partID(secondPart))!
    assert.equal(visualIdentity(replacementFirst), visualIdentity(selectedFirstPart), 'AI continuation changed the preserved first HTML clip')
    assert.notEqual(visualIdentity(replacementSecond), visualIdentity(selectedSecondPart), 'AI continuation did not replace the targeted second HTML clip')
    assert(candidateCount(replacementFirst) >= 2, 'preserved first HTML clip lost its iterations')
    assert(candidateCount(replacementSecond) >= 2, 'replacement second HTML clip has fewer than two iterations')

    const replacementDetail = await projectDetail(page, projectID)
    const replacementTimelineClips = timelineClips(replacementDetail.current_revision)
    const replacementSourceVideos = replacementTimelineClips.filter((clip) => clip.source_kind === 'source_video')
    assert.equal(replacementSourceVideos.length, 1, `replacement mixed timeline must retain exactly one source-video clip: ${JSON.stringify(replacementTimelineClips)}`)
    assert.equal(sourceVideoIdentity(replacementSourceVideos[0]), sourceVideoIdentity(initialSourceVideo), 'AI continuation changed or repositioned the preserved source-video clip')
    assert.equal(replacementTimelineClips.filter((clip) => clip.source_kind === 'managed_artifact').length, 2, `replacement mixed timeline must retain two managed HTML clips: ${JSON.stringify(replacementTimelineClips)}`)
    const replacementLedger = identityLedger(replacement, replacementDetail.current_revision)
    assert.equal(replacementLedger.baseRevision, initialLedger.workingRevision, 'second turn did not chain from the first working revision')
    assertPreservedIdentities(initialLedger, replacementLedger, [partID(firstPart)], [String(initialSourceVideo.id || '')])

    const staleCandidate = ((selectedSecondPart.animation_candidates as JsonRecord).candidates as JsonRecord[])[0]
    assert(staleCandidate, 'initial second HTML part has no candidate for the stale-action proof')
    const staleResponse = await page.evaluate(async ({ route, partId, candidate }) => {
      const response = await fetch(route, { credentials: 'include', method: 'POST', headers: { Accept: 'application/json', 'Content-Type': 'application/json' }, body: JSON.stringify({ part_id: partId, candidate }) })
      return { status: response.status, body: await response.text() }
    }, { route: `/v3/sessions/${SESSION_ID}/video/projects/${projectID}/edit-proposals/${initial.id}/animation-candidate-select`, partId: partID(secondPart), candidate: staleCandidate })
    assert(staleResponse.status === 409 || staleResponse.status === 412, `stale selection from proposal ${initial.id} was not rejected after ${replacement.id}: HTTP ${staleResponse.status} ${staleResponse.body.slice(0, 600)}`)

    const appendRun = await sendMessage(page, APPEND_PROMPT)
    await waitForRun(page, appendRun)
    const appended = await waitForProposal(page, projectID, (proposal) => proposal.status === 'pending'
      && proposal.id !== replacement.id
      && proposal.base_revision_id === replacement.working_revision_id
      && parts(proposal).length === 3
      && parts(proposal).filter((part) => !replacementLedger.partOrder.includes(partID(part))).length === 1, 'appended third HTML part proposal')
    const appendedPart = parts(appended).find((part) => !replacementLedger.partOrder.includes(partID(part)))!
    assert(candidateCount(appendedPart) >= 2, 'appended HTML part has fewer than two iterations')
    const appendedDetail = await projectDetail(page, projectID)
    const appendedLedger = identityLedger(appended, appendedDetail.current_revision)
    const sourceVideoID = String(initialSourceVideo.id || '')
    assertPreservedIdentities(replacementLedger, appendedLedger, replacementLedger.partOrder, [...replacementLedger.timelineOrder])
    assert.deepEqual(appendedLedger.partOrder, [...replacementLedger.partOrder, partID(appendedPart)], 'new HTML part was not appended after the existing stable parts')
    assert.equal(timelineClips(appendedDetail.current_revision).filter((clip) => clip.source_kind === 'source_video').length, 1, 'append turn duplicated or removed the source video')

    const replaceAppendedRun = await sendMessage(page, REPLACE_APPENDED_PROMPT)
    await waitForRun(page, replaceAppendedRun)
    const finalProposal = await waitForProposal(page, projectID, (proposal) => proposal.status === 'pending'
      && proposal.id !== appended.id
      && proposal.base_revision_id === appended.working_revision_id
      && parts(proposal).length === 3
      && parts(proposal).some((part) => partID(part) === partID(appendedPart)), 'targeted appended-part replacement proposal')
    const finalDetail = await projectDetail(page, projectID)
    const finalLedger = identityLedger(finalProposal, finalDetail.current_revision)
    assertPreservedIdentities(appendedLedger, finalLedger, replacementLedger.partOrder, appendedLedger.timelineOrder.filter((id) => id !== partID(appendedPart)))
    assert.notEqual(finalLedger.partVisuals[partID(appendedPart)], appendedLedger.partVisuals[partID(appendedPart)], 'fourth turn did not replace the targeted appended HTML part')
    assert.deepEqual(finalLedger.partOrder, appendedLedger.partOrder, 'fourth turn changed stable part order')
    assert.equal(finalLedger.clipIdentities[sourceVideoID], appendedLedger.clipIdentities[sourceVideoID], 'fourth turn changed the retained source video')

    await page.reload({ waitUntil: 'networkidle', timeout: 30_000 })
    await page.getByLabel('Selected video project').selectOption(projectID)
    await page.getByText(new RegExp(`Pending turn changes · working r${String(finalProposal.working_revision_number || '')}`, 'i')).waitFor({ state: 'visible', timeout: 30_000 })
    await assertClipAnimates(page, parts(finalProposal).find((part) => partID(part) === partID(firstPart))!)
    await assertClipAnimates(page, parts(finalProposal).find((part) => partID(part) === partID(secondPart))!)
    await assertClipAnimates(page, parts(finalProposal).find((part) => partID(part) === partID(appendedPart))!)
    await assertSourceVideoPlays(page, timelineClips(finalDetail.current_revision).find((clip) => clip.source_kind === 'source_video')!)
    const activeFrame = page.locator('[data-video-studio-live-animation]')
    assert.equal(await activeFrame.count(), 1, 'player mounted duplicate live animation frames')
    const activeTitle = await activeFrame.getAttribute('title')
    assert(activeTitle, 'active HTML player does not expose its candidate identity')
    assert.equal(browserErrors.length, 0, `browser errors: ${browserErrors.join(' | ')}`)
    console.log(JSON.stringify({ result: 'PASS', flow: 'four-turn-mixed-source-video-html-iteration', session_id: SESSION_ID, project_id: projectID, proposal_ids: [initial.id, replacement.id, appended.id, finalProposal.id], part_ids: finalLedger.partOrder, source_video_clip_id: sourceVideoID }, null, 2))
  } finally {
    await browser.close()
  }
})
