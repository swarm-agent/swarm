import assert from 'node:assert/strict'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { mkdtemp } from 'node:fs/promises'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'
import test from 'node:test'

import { chromium, type Page } from 'playwright'

const ENABLED = process.env.SWARM_MANAGED_CONTAINER_WORKTREE_TWO_MESSAGE_E2E === '1'
const BACKEND_URL = (process.env.SWARM_BACKEND_URL || 'http://127.0.0.1:7781').replace(/\/+$/, '')
const SOURCE_WORKSPACE_PATH = resolve(process.env.SWARM_E2E_SOURCE_WORKSPACE_PATH || process.env.SWARM_E2E_WORKSPACE_PATH || process.cwd())
const SOURCE_WORKSPACE_NAME = process.env.SWARM_E2E_WORKSPACE_NAME || workspaceNameFromPath(SOURCE_WORKSPACE_PATH)
const PROVIDER = process.env.SWARM_PROVIDER || 'codex'
const MODEL = process.env.SWARM_MODEL || 'gpt-5.5'
const THINKING = process.env.SWARM_THINKING || 'low'
const AGENT_NAME = process.env.SWARM_AGENT_NAME || 'swarm'
const MANAGED_HOST_SWARM_ID = process.env.SWARM_E2E_MANAGED_HOST_SWARM_ID || ''
const MANAGED_HOST_NAME = process.env.SWARM_E2E_MANAGED_HOST_NAME || ''
const EXISTING_CHILD_SWARM_ID = process.env.SWARM_E2E_MANAGED_CHILD_SWARM_ID || ''
const EXISTING_CHILD_RUNTIME_WORKSPACE = process.env.SWARM_E2E_MANAGED_CHILD_RUNTIME_WORKSPACE || ''
const CONTAINER_NAME = process.env.SWARM_E2E_MANAGED_CONTAINER_NAME || `desktop-two-message-${Date.now()}`
const BASE_BRANCH = process.env.SWARM_WORKTREE_BASE_BRANCH || 'dev'
const BRANCH_NAME = process.env.SWARM_WORKTREE_BRANCH_NAME || `agent/e2e-desktop-managed-container-two-message-${Date.now()}`
const PROMPT_ONE = process.env.SWARM_E2E_PROMPT_ONE || 'E2E managed container worktree turn one. Reply with exactly: managed-container-turn-one-ok'
const PROMPT_TWO = process.env.SWARM_E2E_PROMPT_TWO || 'E2E managed container worktree turn two. Reply with exactly: managed-container-turn-two-ok'
const RUN_TIMEOUT_MS = Number(process.env.SWARM_E2E_RUN_TIMEOUT_MS || 180_000)

interface WorkspaceWire {
  path?: string
  workspace_name?: string
}

interface SessionWire {
  id?: string
  workspace_path?: string
  workspace_name?: string
  worktree_enabled?: boolean
  worktree_root_path?: string
  worktree_branch?: string
  metadata?: Record<string, unknown>
  lifecycle?: {
    run_id?: string
    active?: boolean
    phase?: string
    error?: string
    updated_at?: number
  } | null
}

interface SwarmTargetWire {
  swarm_id?: string
  name?: string
  kind?: string
  relationship?: string
  online?: boolean
  selectable?: boolean
}

interface Checkpoint {
  name: string
  epochMs: number
  detail?: string
}

function workspaceNameFromPath(value: string): string {
  const normalized = value.trim().replace(/[\\/]+$/, '')
  const parts = normalized.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || 'workspace'
}

function slugifySegment(value: string): string {
  return value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'workspace'
}

function pathHash(path: string): string {
  let hash = 2166136261
  for (let index = 0; index < path.length; index += 1) {
    hash ^= path.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(36)
}

function workspaceRouteSlugBase(workspace: WorkspaceWire): string {
  const name = String(workspace.workspace_name ?? '').trim() || workspaceNameFromPath(String(workspace.path ?? ''))
  const base = slugifySegment(name)
  return base === 'swarm' ? 'swarm-workspace' : base
}

function resolveWorkspaceSlug(workspaces: WorkspaceWire[], workspacePath: string): string {
  const candidates = workspaces.length > 0 ? workspaces : [{ path: workspacePath, workspace_name: SOURCE_WORKSPACE_NAME }]
  const counts = new Map<string, number>()
  for (const workspace of candidates) {
    const base = workspaceRouteSlugBase(workspace)
    counts.set(base, (counts.get(base) ?? 0) + 1)
  }
  const target = candidates.find((workspace) => String(workspace.path ?? '').trim() === workspacePath.trim()) ?? candidates[0]
  const base = workspaceRouteSlugBase(target)
  return (counts.get(base) ?? 0) > 1 ? `${base}-${pathHash(String(target.path ?? '')).slice(0, 6)}` : base
}

function mark(checkpoints: Checkpoint[], name: string, detail?: string): void {
  checkpoints.push({ name, epochMs: Date.now(), detail })
}

async function startVite(backendURL: string): Promise<{ vite: ChildProcessWithoutNullStreams; port: number }> {
  const probe = createServer()
  probe.listen(0, '127.0.0.1')
  await once(probe, 'listening')
  const address = probe.address()
  assert(address && typeof address === 'object')
  const port = address.port
  await new Promise<void>((resolve) => probe.close(() => resolve()))

  const localNode = './node_modules/node/bin/node'
  const nodeBin = existsSync(localNode) ? localNode : process.execPath
  const vite = spawn(nodeBin, ['./scripts/vite-launcher.mjs', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], {
    cwd: process.cwd(),
    env: { ...process.env, SWARM_BACKEND_URL: backendURL, SWARM_DESKTOP_PORT: String(port) },
  })

  let output = ''
  vite.stdout.on('data', (chunk) => { output += String(chunk) })
  vite.stderr.on('data', (chunk) => { output += String(chunk) })

  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/`)
      if (response.ok) return { vite, port }
    } catch {
      // wait for Vite
    }
    await new Promise((resolve) => setTimeout(resolve, 100))
  }

  vite.kill('SIGTERM')
  throw new Error(`Vite did not start on port ${port}. Output:\n${output}`)
}

async function stopVite(vite: ChildProcessWithoutNullStreams): Promise<void> {
  if (vite.exitCode !== null || vite.signalCode !== null) return
  const exited = once(vite, 'exit').then(() => undefined)
  vite.kill('SIGTERM')
  await Promise.race([exited, new Promise<void>((resolve) => setTimeout(resolve, 2_000))])
  if (vite.exitCode === null && vite.signalCode === null) {
    vite.kill('SIGKILL')
    await exited.catch(() => undefined)
  }
}

async function apiJson<T>(page: Page, appURL: string, path: string, init: Parameters<typeof page.request.fetch>[1] = {}): Promise<T> {
  const response = await page.request.fetch(`${appURL}${path}`, {
    ...init,
    headers: {
      Accept: 'application/json',
      Origin: appURL,
      Referer: `${appURL}/`,
      'Sec-Fetch-Site': 'same-origin',
      ...(init.headers ?? {}),
    },
  })
  const text = await response.text()
  if (!response.ok()) {
    throw new Error(`${init.method ?? 'GET'} ${path} failed HTTP ${response.status()}: ${text}`)
  }
  return (text.trim() ? JSON.parse(text) : {}) as T
}

function targetMatchesManagedHost(target: SwarmTargetWire): boolean {
  if (!target.online || !target.selectable) return false
  const swarmID = String(target.swarm_id ?? '').trim()
  const name = String(target.name ?? '').trim()
  if (MANAGED_HOST_SWARM_ID && swarmID === MANAGED_HOST_SWARM_ID) return true
  if (MANAGED_HOST_NAME && name === MANAGED_HOST_NAME) return true
  const kind = String(target.kind ?? '').trim().toLowerCase()
  const relationship = String(target.relationship ?? '').trim().toLowerCase()
  return kind === 'host' && relationship === 'managed'
}

async function createManagedContainerTarget(page: Page, appURL: string, checkpoints: Checkpoint[]): Promise<{ childSwarmId: string; runtimeWorkspacePath: string; deploymentId: string }> {
  await apiJson(page, appURL, '/v1/auth/desktop/session')

  if (EXISTING_CHILD_SWARM_ID && EXISTING_CHILD_RUNTIME_WORKSPACE) {
    mark(checkpoints, 'managed.container.reused', `child=${EXISTING_CHILD_SWARM_ID}`)
    return { childSwarmId: EXISTING_CHILD_SWARM_ID, runtimeWorkspacePath: EXISTING_CHILD_RUNTIME_WORKSPACE, deploymentId: '' }
  }

  const targets = await apiJson<{ targets?: SwarmTargetWire[] }>(page, appURL, '/v1/swarm/targets')
  const managedTarget = (targets.targets ?? []).find(targetMatchesManagedHost)
  assert(managedTarget?.swarm_id, `managed host target not found; set SWARM_E2E_MANAGED_HOST_SWARM_ID or SWARM_E2E_MANAGED_HOST_NAME. targets=${JSON.stringify(targets.targets ?? [])}`)

  const response = await apiJson<{
    ok?: boolean
    swarm?: { id?: string; deployment_id?: string }
    workspaces?: Array<{ binding?: { destination_workspace_path?: string } }>
  }>(page, appURL, '/v1/swarm/replicate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    data: {
      mode: 'local',
      swarm_name: CONTAINER_NAME,
      target_host_swarm_id: managedTarget.swarm_id,
      sync: { enabled: true, mode: 'managed' },
      workspaces: [{ source_workspace_path: SOURCE_WORKSPACE_PATH, replication_mode: 'bundle', writable: true }],
    },
    timeout: RUN_TIMEOUT_MS,
  })
  assert.equal(response.ok, true, `replicate failed: ${JSON.stringify(response)}`)
  const childSwarmId = String(response.swarm?.id ?? '').trim()
  const deploymentId = String(response.swarm?.deployment_id ?? '').trim()
  const runtimeWorkspacePath = String(response.workspaces?.[0]?.binding?.destination_workspace_path ?? '').trim()
  assert(childSwarmId && runtimeWorkspacePath, `replicate response missing child/runtime workspace: ${JSON.stringify(response)}`)
  mark(checkpoints, 'managed.container.created', `child=${childSwarmId} runtime=${runtimeWorkspacePath}`)
  return { childSwarmId, runtimeWorkspacePath, deploymentId }
}

async function waitForTargetOnline(page: Page, appURL: string, childSwarmId: string): Promise<void> {
  const deadline = Date.now() + RUN_TIMEOUT_MS
  while (Date.now() < deadline) {
    const response = await apiJson<{ targets?: SwarmTargetWire[] }>(page, appURL, `/v1/swarm/targets?swarm_id=${encodeURIComponent(childSwarmId)}`)
    if ((response.targets ?? []).some((target) => String(target.swarm_id ?? '').trim() === childSwarmId && target.online && target.selectable)) return
    await new Promise((resolve) => setTimeout(resolve, 2_000))
  }
  throw new Error(`timed out waiting for managed child target ${childSwarmId} online/selectable`)
}

async function openWorktreeSession(page: Page, appURL: string, childSwarmId: string, runtimeWorkspacePath: string, checkpoints: Checkpoint[]): Promise<{ session: SessionWire; slug: string }> {
  await apiJson(page, appURL, '/v1/auth/desktop/session')
  await apiJson(page, appURL, '/readyz')
  const workspaceOverview = await apiJson<{ workspaces?: WorkspaceWire[] }>(page, appURL, '/v1/workspace/overview?workspace_limit=200&discover_limit=200')
  const response = await apiJson<{ session?: SessionWire }>(page, appURL, `/v1/sessions?swarm_id=${encodeURIComponent(childSwarmId)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    data: {
      title: `Desktop managed container worktree two-message E2E ${new Date().toISOString()}`,
      workspace_path: SOURCE_WORKSPACE_PATH,
      host_workspace_path: SOURCE_WORKSPACE_PATH,
      runtime_workspace_path: runtimeWorkspacePath,
      workspace_name: SOURCE_WORKSPACE_NAME,
      mode: 'auto',
      agent_name: AGENT_NAME,
      worktree_mode: 'on',
      worktree_use_current_branch: false,
      worktree_base_branch: BASE_BRANCH,
      worktree_branch_name: BRANCH_NAME,
      preference: { provider: PROVIDER, model: MODEL, thinking: THINKING },
      metadata: { desktop_managed_container_worktree_two_message_e2e: true },
    },
    timeout: RUN_TIMEOUT_MS,
  })
  const session = response.session ?? {}
  assert(session.id, `session open response missing id: ${JSON.stringify(response)}`)
  assert.equal(session.worktree_enabled, true, `session is not worktree-enabled: ${JSON.stringify(session)}`)
  const primaryWorkspace = String(session.workspace_path ?? '').trim()
  const routedRuntimeWorkspace = String(session.metadata?.swarm_routed_runtime_workspace_path ?? '').trim()
  assert(primaryWorkspace, `session did not include a primary workspace path: ${JSON.stringify(session)}`)
  assert(routedRuntimeWorkspace && routedRuntimeWorkspace !== SOURCE_WORKSPACE_PATH, `session did not include a routed child runtime workspace: ${JSON.stringify(session)}`)
  mark(checkpoints, 'session.opened', `session=${session.id} primary=${primaryWorkspace} runtime=${routedRuntimeWorkspace}`)
  return { session, slug: resolveWorkspaceSlug(workspaceOverview.workspaces ?? [], SOURCE_WORKSPACE_PATH) }
}

async function waitForRunActive(page: Page, appURL: string, sessionId: string, previousRunId: string | null): Promise<string> {
  const deadline = Date.now() + 45_000
  while (Date.now() < deadline) {
    const response = await apiJson<{ session?: SessionWire }>(page, appURL, `/v1/sessions/${encodeURIComponent(sessionId)}`)
    const lifecycle = response.session?.lifecycle
    const runId = String(lifecycle?.run_id ?? '').trim()
    if (lifecycle?.active === true && runId && runId !== previousRunId) return runId
    const error = String(lifecycle?.error ?? '').trim()
    if (error) throw new Error(error)
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  throw new Error(`timed out waiting for new active run for session ${sessionId}`)
}

async function waitForRunInactive(page: Page, appURL: string, sessionId: string, runId: string): Promise<SessionWire> {
  const deadline = Date.now() + RUN_TIMEOUT_MS
  let last: SessionWire = {}
  while (Date.now() < deadline) {
    const response = await apiJson<{ session?: SessionWire }>(page, appURL, `/v1/sessions/${encodeURIComponent(sessionId)}`)
    last = response.session ?? {}
    const lifecycle = last.lifecycle
    const error = String(lifecycle?.error ?? '').trim()
    if (error) throw new Error(error)
    if (String(lifecycle?.run_id ?? '').trim() === runId && lifecycle?.active === false) return last
    await new Promise((resolve) => setTimeout(resolve, 1_000))
  }
  throw new Error(`timed out waiting for run ${runId} inactive; last=${JSON.stringify(last.lifecycle ?? null)}`)
}

async function sendPromptAndWait(page: Page, appURL: string, sessionId: string, prompt: string, previousRunId: string | null, checkpoints: Checkpoint[], label: string): Promise<string> {
  await apiJson(page, appURL, `/v1/sessions/${encodeURIComponent(sessionId)}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    data: { role: 'user', content: prompt },
    timeout: RUN_TIMEOUT_MS,
  })
  mark(checkpoints, `${label}.message.appended`)
  await apiJson(page, appURL, `/v1/sessions/${encodeURIComponent(sessionId)}/run/stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    data: { type: 'run.start', prompt, agent_name: AGENT_NAME, background: false },
    timeout: RUN_TIMEOUT_MS,
  })
  const runId = await waitForRunActive(page, appURL, sessionId, previousRunId)
  mark(checkpoints, `${label}.active`, `run=${runId}`)
  await waitForRunInactive(page, appURL, sessionId, runId)
  mark(checkpoints, `${label}.inactive`, `run=${runId}`)
  const pageText = await page.locator('body').innerText({ timeout: 5_000 }).catch(() => '')
  assert(!/stat workspace path/i.test(pageText), `desktop displayed workspace stat failure after ${label}: ${pageText}`)
  return runId
}

function writeEvidence(evidenceDir: string, summary: Record<string, unknown>, checkpoints: Checkpoint[]): void {
  mkdirSync(evidenceDir, { recursive: true })
  writeFileSync(join(evidenceDir, 'desktop-managed-container-worktree-two-message-summary.json'), `${JSON.stringify(summary, null, 2)}\n`)
  writeFileSync(join(evidenceDir, 'desktop-managed-container-worktree-two-message-checkpoints.json'), `${JSON.stringify(checkpoints, null, 2)}\n`)
}

test('desktop managed-container worktree chat can send two AI turns without primary statting the source workspace', { skip: !ENABLED, timeout: Math.max(240_000, RUN_TIMEOUT_MS + 120_000) }, async () => {
  const evidenceDir = process.env.SWARM_E2E_EVIDENCE_DIR || await mkdtemp(join(tmpdir(), 'swarm-desktop-managed-container-worktree-two-message-'))
  const checkpoints: Checkpoint[] = []
  const app = await startVite(BACKEND_URL)
  const appURL = `http://127.0.0.1:${app.port}`
  const browser = await chromium.launch({ headless: process.env.SWARM_E2E_HEADFUL !== '1' })
  let page: Page | null = null
  let summary: Record<string, unknown> = { ok: false, evidenceDir, backendURL: BACKEND_URL, sourceWorkspaceName: basename(SOURCE_WORKSPACE_PATH), provider: PROVIDER, model: MODEL }

  try {
    page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
    const child = await createManagedContainerTarget(page, appURL, checkpoints)
    await waitForTargetOnline(page, appURL, child.childSwarmId)
    const opened = await openWorktreeSession(page, appURL, child.childSwarmId, child.runtimeWorkspacePath, checkpoints)
    const sessionId = String(opened.session.id ?? '').trim()

    await page.goto(`${appURL}/${encodeURIComponent(opened.slug)}/${encodeURIComponent(sessionId)}`)
    await page.getByTestId('desktop-chat-scroller').waitFor({ state: 'visible', timeout: 30_000 })

    const firstRunId = await sendPromptAndWait(page, appURL, sessionId, PROMPT_ONE, null, checkpoints, 'turn1')
    const secondRunId = await sendPromptAndWait(page, appURL, sessionId, PROMPT_TWO, firstRunId, checkpoints, 'turn2')
    const finalSession = await apiJson<{ session?: SessionWire }>(page, appURL, `/v1/sessions/${encodeURIComponent(sessionId)}`)
    const finalWorkspace = String(finalSession.session?.workspace_path ?? '').trim()
    const finalRuntimeWorkspace = String(finalSession.session?.metadata?.swarm_routed_runtime_workspace_path ?? '').trim()
    assert.equal(finalWorkspace, SOURCE_WORKSPACE_PATH, `final primary mirror jumped away from the source workspace: ${JSON.stringify(finalSession.session)}`)
    assert(finalRuntimeWorkspace && finalRuntimeWorkspace !== SOURCE_WORKSPACE_PATH, `final session lost routed child runtime workspace metadata: ${JSON.stringify(finalSession.session)}`)

    summary = {
      ok: true,
      evidenceDir,
      backendURL: BACKEND_URL,
      childSwarmId: child.childSwarmId,
      deploymentId: child.deploymentId,
      sessionId,
      firstRunId,
      secondRunId,
      sourceWorkspaceName: basename(SOURCE_WORKSPACE_PATH),
      primaryWorkspacePath: finalWorkspace,
      routedRuntimeWorkspacePath: finalRuntimeWorkspace,
      worktreeBranch: finalSession.session?.worktree_branch ?? '',
      provider: PROVIDER,
      model: MODEL,
    }
    writeEvidence(evidenceDir, summary, checkpoints)
    console.log(`desktop managed-container worktree two-message E2E evidence\n${JSON.stringify(summary, null, 2)}`)
  } catch (error) {
    if (page) {
      await page.screenshot({ path: join(evidenceDir, 'desktop-managed-container-worktree-two-message-failure.png'), fullPage: true }).catch(() => undefined)
    }
    summary = { ...summary, ok: false, error: error instanceof Error ? error.message : String(error) }
    writeEvidence(evidenceDir, summary, checkpoints)
    throw error
  } finally {
    await browser.close().catch(() => undefined)
    await stopVite(app.vite)
  }
})
