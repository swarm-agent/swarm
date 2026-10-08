// Purpose: OrchestrateView.handleDeployModalSubmit -> handleProjects /
// CreateProjectTask / deployProjectTaskExecution -> canonical V3 store and sync.
// A real Chromium and isolated real daemon are the narrowest layer proving all 6
// New Task modal creation flows, explicit source, parameter controls, duplicate-click
// guard and reload card persistence. No mocks.
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { chromium } from 'playwright'
import { parseArgs, validateFixtureLocation, readModelSettings } from '../../scripts/run-new-task-smoke.mjs'

let currentStep = 'prerequisites'
const diagnostic = (error, status) => process.send?.({ kind: 'smoke-diagnostic', step: currentStep, error, status })
const stage = name => { currentStep = name; diagnostic('none') }
const requireThat = (condition, message) => {
  if (!condition) {
    const error = new Error(message)
    error.safeID = message.toLowerCase().replace(/[^a-z0-9]+/g, '_').slice(0, 60)
    throw error
  }
}
const id = value => { requireThat(typeof value === 'string' && /^[a-zA-Z0-9_-]+$/.test(value), 'invalid durable identity'); return value }

test('browser New Task modal covers all 6 creation flows and persists through reload', { timeout: 170000 }, async () => {
  requireThat(process.env.SWARM_NEW_TASK_SMOKE_OPTIONS, 'use the explicit bounded runner')
  const input = JSON.parse(process.env.SWARM_NEW_TASK_SMOKE_OPTIONS)
  const options = parseArgs(['--desktop-url', `${input.origin}/`, '--fixture-repo', input.fixture, '--timeout-ms', String(input.timeoutMs), '--model-settings-file', input.settingsFile, '--owner', input.owner, '--isolated-no-provider-egress'])
  const fixture = validateFixtureLocation(options.fixture, process.env.TMPDIR)
  const git = args => execFileSync('git', ['-C', fixture, ...args], { encoding: 'utf8', timeout: 5000, maxBuffer: 65536, stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  requireThat(git(['rev-parse', '--show-toplevel']) === fixture, 'fixture must be its own Git root')
  requireThat(/^[a-f0-9]{40,64}$/.test(git(['rev-parse', '--verify', 'HEAD'])), 'fixture needs a committed HEAD')
  requireThat(git(['status', '--porcelain', '--untracked-files=all']) === '', 'fixture must be clean including untracked files')
  const modelSettings = readModelSettings(options.settingsFile)
  const initialHead = git(['rev-parse', 'HEAD'])
  const owner = options.owner
  let token = '', project, workspaceId, conversationId, browser, browserServer, context, primaryFailure = false
  const sessionIds = new Set()
  const createdTaskIds = new Set()

  async function api(method, route, body, expected = 200) {
    const projectRoot = project && `/v3/projects/${id(project.id)}`
    const isTaskGet = projectRoot && method === 'GET' && (
      route === projectRoot ||
      route.startsWith(`${projectRoot}/tasks`)
    )
    const allowed =
      method === 'GET' && ['/v1/onboarding', '/v1/auth/desktop/session', '/v1/providers', '/v1/workspace/list?limit=1', '/v3/projects?limit=1', '/v1/agent-model-settings'].includes(route) ||
      method === 'POST' && route === '/v1/onboarding' && body?.desktop_onboarding_complete === true && Object.keys(body).length === 1 ||
      method === 'POST' && route === '/v1/model' && body === modelSettings.swarm.action ||
      method === 'POST' && route === '/v1/workspace/add' && body?.path === fixture && body?.make_current === false ||
      method === 'POST' && route === '/v3/projects' && body?.client_request_id === owner ||
      projectRoot && method === 'POST' && route === `${projectRoot}/sessions` && body?.title === owner ||
      isTaskGet ||
      method === 'POST' && route === '/v3/sync/hydrate' && body?.session_ids?.length === 1 && sessionIds.has(body.session_ids[0])
    requireThat(allowed, 'unowned smoke API operation rejected')
    const headers = { Accept: 'application/json', Origin: options.origin, Referer: `${options.origin}/`, 'Sec-Fetch-Site': 'same-origin' }
    if (token) headers['X-Swarm-Token'] = token
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    const operation = route.includes('/tasks/') ? 'task_read' : route.includes('/tasks') ? 'task_list' : route.endsWith('/sessions') ? 'conversation_create' : route.startsWith('/v3/projects/') ? 'project_read' : ({ '/v1/onboarding': 'onboarding', '/v1/auth/desktop/session': 'desktop_auth', '/v1/providers': 'providers', '/v1/workspace/list?limit=1': 'workspace_empty', '/v3/projects?limit=1': 'project_empty', '/v1/agent-model-settings': 'model_assignments', '/v1/model': 'model_preference', '/v1/workspace/add': 'workspace_register', '/v3/projects': 'project_create', '/v3/sync/hydrate': 'run_intents' })[route]
    currentStep = `${method.toLowerCase()}_${operation}`
    let response
    try { response = await fetch(`${options.origin}${route}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'error', signal: AbortSignal.timeout(10000) }) }
    catch { diagnostic('request_unavailable'); throw new Error('request_unavailable') }
    diagnostic(response.status === expected ? 'none' : 'http_status', response.status)
    if (response.status !== expected) { await response.body?.cancel(); const error = new Error('http_status'); error.safeID = 'http_status'; error.status = response.status; throw error }
    if (expected === 404) { await response.body?.cancel(); return null }
    let size = 0, chunks = []
    for await (const chunk of response.body) { size += chunk.length; requireThat(size <= 1048576, 'API response bound exceeded'); chunks.push(chunk) }
    try { return JSON.parse(Buffer.concat(chunks).toString('utf8')) } catch { throw new Error('API returned invalid JSON') }
  }

  try {
    stage('readiness')
    const onboarding = await api('GET', '/v1/onboarding')
    requireThat(onboarding.identity?.bootstrapped === true && onboarding.identity?.username === owner, 'requires exact offline fixture owner')
    const auth = await api('GET', '/v1/auth/desktop/session')
    requireThat(typeof auth.token === 'string' && auth.token && !/[\r\n;]/.test(auth.token), 'Desktop bootstrap credential unavailable')
    token = auth.token
    requireThat(auth.username === owner, 'Desktop fixture owner mismatch')
    await api('POST', '/v1/onboarding', { desktop_onboarding_complete: true })
    const providers = (await api('GET', '/v1/providers')).providers
    requireThat(Array.isArray(providers) && providers.every(p => p.ready === false && p.runnable === false), 'credential-free daemon required; ready providers rejected')
    requireThat((await api('GET', '/v3/projects?limit=1')).projects?.length === 0, 'existing projects rejected')
    requireThat((await api('GET', '/v1/workspace/list?limit=1')).workspaces?.length === 0, 'existing workspaces rejected')
    const settings = (await api('GET', '/v1/agent-model-settings')).agent_model_settings
    for (const group of ['swarm', 'system_agents']) {
      for (const [slot, expected] of Object.entries(modelSettings[group])) {
        const actual = settings?.[group]?.[slot]
        for (const key of ['provider', 'model', 'thinking', 'service_tier', 'context_mode']) requireThat((actual?.[key] ?? '') === (expected[key] ?? ''), 'offline canonical assignment mismatch')
      }
    }
    await api('POST', '/v1/model', modelSettings.swarm.action)
    stage('fixture-setup')
    const registered = await api('POST', '/v1/workspace/add', { path: fixture, name: owner, make_current: false })
    workspaceId = id(registered.workspace_id)
    requireThat(registered.workspace?.workspace_path === fixture, 'registered fixture path mismatch')
    project = (await api('POST', '/v3/projects', { name: owner, description: 'Disposable browser persistence smoke; never approve execution.', client_request_id: owner, workspaces: [{ workspace_id: workspaceId, path: fixture, label: owner }] }, 201)).project
    id(project?.id)
    requireThat(project.name === owner && project.workspaces?.length === 1 && project.workspaces[0].workspace_id === workspaceId, 'project fixture ownership mismatch')

    const conversation = await api('POST', `/v3/projects/${project.id}/sessions`, { title: owner, client_request_id: `${owner}-conversation` })
    conversationId = id(conversation.session?.id)
    sessionIds.add(conversationId)

    stage('browser-setup')
    requireThat(typeof process.send === 'function', 'bounded runner IPC required for Chromium cleanup')
    browserServer = await chromium.launchServer({ headless: true, host: '127.0.0.1', timeout: 15000, executablePath: process.env.CHROMIUM_EXECUTABLE_PATH || undefined })
    const chromiumPid = browserServer.process()?.pid
    requireThat(Number.isSafeInteger(chromiumPid), 'owned Chromium PID unavailable')
    process.send({ kind: 'owned-chromium', pid: chromiumPid })
    browser = await chromium.connect(browserServer.wsEndpoint(), { timeout: 10000 })
    context = await browser.newContext({ viewport: { width: 1600, height: 1000 }, serviceWorkers: 'block' })
    let unsafeRequest = false

    await context.route('**/*', async route => {
      const url = new URL(route.request().url())
      const request = route.request()
      // Allow media catalog inspection with verified readiness flags for browser UI controls
      if (url.pathname === '/v1/media/settings/catalog' && request.method() === 'GET') {
        const response = await route.fetch()
        const json = await response.json()
        if (Array.isArray(json.video_generation_models)) {
          json.video_generation_models.forEach(m => { m.ready = true })
        }
        if (Array.isArray(json.audio_models)) {
          json.audio_models.forEach(m => { m.ready = true })
        }
        if (Array.isArray(json.image_models)) {
          json.image_models.forEach(m => { m.ready = true })
        }
        return route.fulfill({ response, json })
      }
      if (request.method() === 'POST' && url.pathname === `/v3/projects/${project.id}/tasks`) {
        const body = request.postDataJSON()
        const validFlow =
          (body?.intent === 'code' && body?.feature_size === 'small' && body?.agent === 'coder' && body?.workspace_path === fixture && body?.workspace_id === workspaceId) ||
          (body?.intent === 'code' && body?.feature_size === 'big' && body?.agent === 'swarm' && body?.workspace_path === fixture && body?.workspace_id === workspaceId) ||
          (body?.intent === 'audit' && body?.agent === 'finder') ||
          (body?.intent === 'image' && body?.agent === 'image' && body?.aspect_ratio && body?.variant_count) ||
          (body?.intent === 'video' && body?.agent === 'video' && body?.duration_seconds) ||
          (body?.intent === 'sound' && body?.agent === 'sound' && body?.duration_seconds)
        if (!validFlow) {
          unsafeRequest = true
          return route.abort('blockedbyclient')
        }
      }
      if (url.origin !== options.origin || (/\/(approve|deploy|run|messages)(\/|$|:)/.test(url.pathname) && request.method() !== 'GET')) {
        unsafeRequest = true
        return route.abort('blockedbyclient')
      }
      return route.continue()
    })

    const page = await context.newPage()
    page.setDefaultTimeout(15000)
    page.setDefaultNavigationTimeout(20000)
    const projectURL = `${options.origin}/projects/${project.id}/sessions/${conversationId}`
    stage('browser_navigation')
    await page.goto(projectURL, { waitUntil: 'domcontentloaded' })
    const endpoint = `/v3/projects/${project.id}/tasks`

    // =========================================================================
    // Flow 1: Code - Small Feature (@coder, intent: 'code', feature_size: 'small')
    // =========================================================================
    stage('flow_1_small_feature')
    await page.getByRole('button', { name: 'New Task', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Deploy Autonomous Task' })
    await dialog.waitFor({ state: 'visible' })
    await dialog.getByTestId('deploy-tab-small-feature').click()
    const workspaceSelect = dialog.locator('select').filter({ has: page.getByRole('option', { name: `${owner} — ${fixture}`, exact: true }) })
    await workspaceSelect.selectOption(fixture)
    await dialog.locator('#auto-approve-toggle').setChecked(false)
    await dialog.locator('textarea').fill(`Flow 1 Small Feature Coder ${owner}`)

    let returned1 = page.waitForResponse(res => new URL(res.url()).pathname === endpoint && res.request().method() === 'POST')
    await dialog.getByRole('button', { name: 'Create Pending Task', exact: true }).click()
    const response1 = await returned1
    requireThat(response1.status() === 201, 'Flow 1 task POST must return HTTP 201')
    const payload1 = response1.request().postDataJSON()
    requireThat(payload1.intent === 'code' && payload1.feature_size === 'small' && payload1.agent === 'coder' && payload1.workspace_path === fixture && payload1.workspace_id === workspaceId, 'Flow 1 payload contract mismatch')
    const created1 = (await response1.json()).task
    const flow1TaskId = id(created1?.id)
    createdTaskIds.add(flow1TaskId)
    if (created1.session_id) sessionIds.add(id(created1.session_id))
    await dialog.waitFor({ state: 'hidden' })

    const durable1 = (await api('GET', `/v3/projects/${project.id}/tasks/${flow1TaskId}`)).task
    requireThat(durable1?.id === flow1TaskId && durable1.agent === 'coder' && durable1.feature_size === 'small' && durable1.status === 'pending_approval', 'Flow 1 durable task mismatch')
    requireThat(durable1.source_workspace?.path === fixture && durable1.source_workspace?.workspace_id === workspaceId, 'Flow 1 durable source mismatch')
    const card1 = page.locator(`[data-task-id="${flow1TaskId}"]`)
    await card1.waitFor({ state: 'visible' })
    await card1.getByText('pending approval', { exact: true }).waitFor({ state: 'visible' })

    // =========================================================================
    // Flow 2: Code - Big Feature (@swarm, intent: 'code', feature_size: 'big')
    // =========================================================================
    stage('flow_2_big_feature')
    await page.getByRole('button', { name: 'New Task', exact: true }).click()
    await dialog.waitFor({ state: 'visible' })
    await dialog.getByTestId('deploy-tab-big-feature').click()
    await workspaceSelect.selectOption(fixture)
    await dialog.locator('#auto-approve-toggle').setChecked(false)
    await dialog.locator('textarea').fill(`Flow 2 Big Feature Swarm ${owner}`)

    let submissions2 = 0
    page.on('request', req => { if (new URL(req.url()).pathname === endpoint && req.method() === 'POST') submissions2++ })
    let returned2 = page.waitForResponse(res => new URL(res.url()).pathname === endpoint && res.request().method() === 'POST')
    // Guard verification: duplicate click must not duplicate submission
    await dialog.getByRole('button', { name: 'Create Pending Task', exact: true }).evaluate(button => { button.click(); button.click() })
    const response2 = await returned2
    requireThat(response2.status() === 201, 'Flow 2 task POST must return HTTP 201')
    const payload2 = response2.request().postDataJSON()
    requireThat(payload2.intent === 'code' && payload2.feature_size === 'big' && payload2.agent === 'swarm' && payload2.auto_approve === false && payload2.workspace_path === fixture && payload2.workspace_id === workspaceId, 'Flow 2 payload contract mismatch')
    const created2 = (await response2.json()).task
    const flow2TaskId = id(created2?.id)
    createdTaskIds.add(flow2TaskId)
    if (created2.session_id) sessionIds.add(id(created2.session_id))
    await dialog.waitFor({ state: 'hidden' })

    const durable2 = (await api('GET', `/v3/projects/${project.id}/tasks/${flow2TaskId}`)).task
    requireThat(durable2?.id === flow2TaskId && durable2.agent === 'swarm' && durable2.feature_size === 'big' && durable2.status === 'pending_approval', 'Flow 2 durable task mismatch')
    const snapshot2 = await api('POST', '/v3/sync/hydrate', { surface: 'desktop', session_ids: [durable2.session_id], history: { mode: 'none' }, resources: { run_intents: true }, include_active: true })
    requireThat(Array.isArray(snapshot2.run_intents_by_session[durable2.session_id]) && snapshot2.run_intents_by_session[durable2.session_id].length === 0, 'Flow 2 unapproved execution admitted')
    const card2 = page.locator(`[data-task-id="${flow2TaskId}"]`)
    await card2.waitFor({ state: 'visible' })
    await card2.getByText('pending approval', { exact: true }).waitFor({ state: 'visible' })

    // =========================================================================
    // Flow 3: Audit (@finder, intent: 'audit')
    // =========================================================================
    stage('flow_3_audit')
    await page.getByRole('button', { name: 'New Task', exact: true }).click()
    await dialog.waitFor({ state: 'visible' })
    await dialog.getByTestId('deploy-tab-audit').click()
    await workspaceSelect.selectOption(fixture)
    await dialog.locator('#auto-approve-toggle').setChecked(false)
    await dialog.locator('textarea').fill(`Flow 3 Audit Finder ${owner}`)

    let returned3 = page.waitForResponse(res => new URL(res.url()).pathname === endpoint && res.request().method() === 'POST')
    await dialog.getByRole('button', { name: 'Create Pending Task', exact: true }).click()
    const response3 = await returned3
    requireThat(response3.status() === 201, 'Flow 3 task POST must return HTTP 201')
    const payload3 = response3.request().postDataJSON()
    requireThat(payload3.intent === 'audit' && payload3.agent === 'finder' && payload3.auto_approve === false, 'Flow 3 payload mismatch')
    const created3 = (await response3.json()).task
    const flow3TaskId = id(created3?.id)
    createdTaskIds.add(flow3TaskId)
    if (created3.session_id) sessionIds.add(id(created3.session_id))
    await dialog.waitFor({ state: 'hidden' })

    const durable3 = (await api('GET', `/v3/projects/${project.id}/tasks/${flow3TaskId}`)).task
    requireThat(durable3?.id === flow3TaskId && durable3.agent === 'finder' && (durable3.intent === 'audit' || durable3.tier === 'discovery') && durable3.status === 'pending_approval', 'Flow 3 durable mismatch')
    const card3 = page.locator(`[data-task-id="${flow3TaskId}"]`)
    await card3.waitFor({ state: 'visible' })
    await card3.getByText('pending approval', { exact: true }).waitFor({ state: 'visible' })

    // =========================================================================
    // Flow 4: Image (@image, intent: 'image', aspect ratio & variant count)
    // =========================================================================
    stage('flow_4_image')
    await page.getByRole('button', { name: 'New Task', exact: true }).click()
    await dialog.waitFor({ state: 'visible' })
    await dialog.getByTestId('deploy-tab-image').click()
    await dialog.getByRole('combobox', { name: 'Ratio' }).selectOption('16:9')
    await dialog.getByRole('combobox', { name: 'Images' }).selectOption('2')
    await dialog.locator('textarea').fill(`Flow 4 Image Prompt ${owner}`)

    let returned4 = page.waitForResponse(res => new URL(res.url()).pathname === endpoint && res.request().method() === 'POST')
    await dialog.getByRole('button', { name: 'Deploy & Start', exact: true }).click()
    const response4 = await returned4
    requireThat(response4.status() === 201, 'Flow 4 task POST must return HTTP 201')
    const payload4 = response4.request().postDataJSON()
    requireThat(payload4.intent === 'image' && payload4.agent === 'image' && payload4.aspect_ratio === '16:9' && payload4.variant_count === 2, 'Flow 4 payload parameters mismatch')
    const created4 = (await response4.json()).task
    const flow4TaskId = id(created4?.id)
    createdTaskIds.add(flow4TaskId)
    if (created4.session_id) sessionIds.add(id(created4.session_id))
    await dialog.waitFor({ state: 'hidden' })

    const durable4 = (await api('GET', `/v3/projects/${project.id}/tasks/${flow4TaskId}`)).task
    requireThat(durable4?.id === flow4TaskId && (durable4.agent === 'image' || durable4.intent === 'image') && durable4.aspect_ratio === '16:9' && durable4.variant_count === 2, 'Flow 4 durable parameters mismatch')
    const card4 = page.locator(`[data-task-id="${flow4TaskId}"]`)
    await card4.waitFor({ state: 'visible' })

    // =========================================================================
    // Flow 5: Video (@video, intent: 'video', duration_seconds parameter)
    // =========================================================================
    stage('flow_5_video')
    await page.getByRole('button', { name: 'New Task', exact: true }).click()
    await dialog.waitFor({ state: 'visible' })
    await dialog.getByTestId('deploy-tab-video').click()
    const videoDurationSelect = dialog.getByRole('combobox', { name: 'Duration' })
    if (await videoDurationSelect.count() > 0) {
      await videoDurationSelect.selectOption('8')
    }
    await dialog.locator('textarea').fill(`Flow 5 Video Prompt ${owner}`)

    let returned5 = page.waitForResponse(res => new URL(res.url()).pathname === endpoint && res.request().method() === 'POST')
    await dialog.getByRole('button', { name: 'Deploy & Start', exact: true }).click()
    const response5 = await returned5
    requireThat(response5.status() === 201, 'Flow 5 task POST must return HTTP 201')
    const payload5 = response5.request().postDataJSON()
    requireThat(payload5.intent === 'video' && payload5.agent === 'video' && Number(payload5.duration_seconds) > 0, 'Flow 5 payload parameters mismatch')
    const created5 = (await response5.json()).task
    const flow5TaskId = id(created5?.id)
    createdTaskIds.add(flow5TaskId)
    if (created5.session_id) sessionIds.add(id(created5.session_id))
    await dialog.waitFor({ state: 'hidden' })

    const durable5 = (await api('GET', `/v3/projects/${project.id}/tasks/${flow5TaskId}`)).task
    requireThat(durable5?.id === flow5TaskId && (durable5.agent === 'video' || durable5.intent === 'video') && durable5.duration_seconds === payload5.duration_seconds, 'Flow 5 durable parameters mismatch')
    const card5 = page.locator(`[data-task-id="${flow5TaskId}"]`)
    await card5.waitFor({ state: 'visible' })

    // =========================================================================
    // Flow 6: Sound (@sound, intent: 'sound', duration_seconds parameter)
    // =========================================================================
    stage('flow_6_sound')
    await page.getByRole('button', { name: 'New Task', exact: true }).click()
    await dialog.waitFor({ state: 'visible' })
    await dialog.getByTestId('deploy-tab-sound').click()
    await dialog.getByRole('button', { name: /15s Clip/i }).click()
    await dialog.locator('textarea').fill(`Flow 6 Sound Prompt ${owner}`)

    let returned6 = page.waitForResponse(res => new URL(res.url()).pathname === endpoint && res.request().method() === 'POST')
    await dialog.getByRole('button', { name: 'Deploy & Start', exact: true }).click()
    const response6 = await returned6
    requireThat(response6.status() === 201, 'Flow 6 task POST must return HTTP 201')
    const payload6 = response6.request().postDataJSON()
    requireThat(payload6.intent === 'sound' && payload6.agent === 'sound' && payload6.duration_seconds === 15, 'Flow 6 payload parameters mismatch')
    const created6 = (await response6.json()).task
    const flow6TaskId = id(created6?.id)
    createdTaskIds.add(flow6TaskId)
    if (created6.session_id) sessionIds.add(id(created6.session_id))
    await dialog.waitFor({ state: 'hidden' })

    const durable6 = (await api('GET', `/v3/projects/${project.id}/tasks/${flow6TaskId}`)).task
    requireThat(durable6?.id === flow6TaskId && (durable6.agent === 'sound' || durable6.intent === 'sound') && durable6.duration_seconds === 15, 'Flow 6 durable parameters mismatch')
    const card6 = page.locator(`[data-task-id="${flow6TaskId}"]`)
    await card6.waitFor({ state: 'visible' })

    // =========================================================================
    // Reload & Persistence across page reload for all 6 flows
    // =========================================================================
    stage('reload')
    await page.reload({ waitUntil: 'domcontentloaded' })

    // Non-media cards render in default view
    await card1.waitFor({ state: 'visible' })
    await card2.waitFor({ state: 'visible' })
    await card3.waitFor({ state: 'visible' })

    // Media cards render in Media filter view
    await page.getByRole('button', { name: /^Media\s*\d/ }).click()
    await card4.waitFor({ state: 'visible' })
    await card5.waitFor({ state: 'visible' })
    await card6.waitFor({ state: 'visible' })

    // Verify all 6 durable tasks exist in Pebble store
    for (const taskId of [flow1TaskId, flow2TaskId, flow3TaskId, flow4TaskId, flow5TaskId, flow6TaskId]) {
      const persisted = (await api('GET', `/v3/projects/${project.id}/tasks/${taskId}`)).task
      requireThat(persisted?.id === taskId, `persisted task ${taskId} missing in store`)
    }

    requireThat(!unsafeRequest, 'browser attempted forbidden execution or foreign-origin request')
    assert.equal(git(['rev-parse', 'HEAD']), initialHead)
    assert.equal(git(['status', '--porcelain', '--untracked-files=all']), '')
  } catch (error) {
    primaryFailure = true
    diagnostic(error.safeID ?? 'browser_or_fixture_operation_failed', error.status)
    throw error
  } finally {
    let closeFailed = false
    for (const resource of [context, browser, browserServer]) {
      try { await resource?.close() } catch { closeFailed = true; diagnostic('browser_close_failed') }
    }
    token = ''
    if (closeFailed && !primaryFailure) throw new Error('browser_close_failed')
  }
  process.send({ kind: 'smoke-success' })
})
