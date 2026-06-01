import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { mkdtemp } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

const ENABLED = process.env.SWARM_LOCAL_CONTAINER_WORKTREE_SESSION_E2E === '1'
const PRIMARY_SSH = process.env.SWARM_PRIMARY_SSH || 'testbench'
const PRIMARY_API_URL = (process.env.SWARM_PRIMARY_API_URL || process.env.SWARM_BACKEND_URL || 'http://127.0.0.1:7781').replace(/\/+$/, '')
const SOURCE_WORKSPACE_PATH_ENV = process.env.SWARM_SOURCE_WORKSPACE_PATH || process.env.SWARM_E2E_SOURCE_WORKSPACE_PATH || process.env.SWARM_E2E_WORKSPACE_PATH || ''
const CONTAINER_NAME = process.env.SWARM_LOCAL_CONTAINER_WORKTREE_TEST_NAME || `local-worktree-e2e-${Date.now()}`
const BASE_BRANCH_ENV = process.env.SWARM_WORKTREE_BASE_BRANCH || ''
const BRANCH_NAME = process.env.SWARM_WORKTREE_BRANCH_NAME || `agent/e2e-local-container-worktree-${Date.now()}`
const SERVICE_UNIT = process.env.SWARM_SERVICE_UNIT || 'swarm.service'
const TIMEOUT_MS = Number(process.env.SWARM_LOCAL_CONTAINER_WORKTREE_TIMEOUT_MS || process.env.SWARM_E2E_RUN_TIMEOUT_MS || 420_000)
const CLEANUP = process.env.SWARM_LOCAL_CONTAINER_WORKTREE_CLEANUP === '1'
const REUSE_CHILD_SWARM_ID = process.env.SWARM_LOCAL_CONTAINER_CHILD_SWARM_ID || process.env.SWARM_E2E_MANAGED_CHILD_SWARM_ID || ''
const REUSE_WORKSPACE_BINDING_ID = process.env.SWARM_LOCAL_CONTAINER_WORKSPACE_BINDING_ID || process.env.SWARM_E2E_WORKSPACE_BINDING_ID || ''
const REUSE_RUNTIME_WORKSPACE_PATH = process.env.SWARM_LOCAL_CONTAINER_RUNTIME_WORKSPACE_PATH || process.env.SWARM_E2E_MANAGED_CHILD_RUNTIME_WORKSPACE || ''

const SESSION_CANONICAL_STATE_FAILURE = 'worktree_mode on did not create canonical worktree session state'
const FORBIDDEN_SESSION_CREATE_PATH_FIELDS = ['workspace_path', 'host_workspace_path', 'runtime_workspace_path'] as const

type JsonObject = Record<string, unknown>

type ProcessResult = {
  code: number | null
  signal: string | null
  stdout: string
  stderr: string
}

type ContainerTarget = {
  childSwarmId: string
  workspaceBindingId: string
  runtimeWorkspacePath: string
  deploymentId: string
  reused: boolean
}

type SessionWire = {
  id?: string
  workspace_path?: string
  worktree_enabled?: boolean
  worktree_root_path?: string
  worktree_branch?: string
  metadata?: Record<string, unknown>
}

type SessionRouteWire = {
  runtime_workspace_path?: string
  workspace_binding_id?: string
  runtime_swarm_id?: string
  backend_url?: string
  host_workspace_path?: string
  placement_generation?: number
  binding_generation?: number
}

type Checkpoint = {
  name: string
  epochMs: number
  detail?: string
}

function mark(checkpoints: Checkpoint[], name: string, detail?: string): void {
  checkpoints.push({ name, epochMs: Date.now(), detail })
}

function ensureDir(path: string): string {
  mkdirSync(path, { recursive: true })
  return path
}

function writeArtifact(evidenceDir: string, name: string, value: unknown): void {
  const path = join(evidenceDir, name)
  const body = typeof value === 'string' ? value : `${JSON.stringify(value, null, 2)}\n`
  writeFileSync(path, body)
}

function workspaceNameFromPath(value: string): string {
  const normalized = value.trim().replace(/[\\/]+$/, '')
  const parts = normalized.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || 'workspace'
}

function hasForbiddenSessionCreatePathField(body: JsonObject): boolean {
  return FORBIDDEN_SESSION_CREATE_PATH_FIELDS.some((field) => Object.prototype.hasOwnProperty.call(body, field))
}

function assertStableSessionCreateContract(body: JsonObject): void {
  assert.equal(hasForbiddenSessionCreatePathField(body), false, `session create body contains forbidden path authority fields: ${JSON.stringify(body, null, 2)}`)
  assert.equal(typeof body.workspace_binding_id, 'string', `session create body missing workspace_binding_id: ${JSON.stringify(body, null, 2)}`)
  assert.equal(body.worktree_mode, 'on', `session create body must request worktree_mode:on: ${JSON.stringify(body, null, 2)}`)
}

function extractErrorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function includesCanonicalWorktreeFailure(value: unknown): boolean {
  return JSON.stringify(value).includes(SESSION_CANONICAL_STATE_FAILURE)
}

async function runProcess(file: string, args: string[], input = '', timeoutMs = TIMEOUT_MS): Promise<ProcessResult> {
  return await new Promise<ProcessResult>((resolve, reject) => {
    const child = spawn(file, args, { stdio: ['pipe', 'pipe', 'pipe'] })
    const timer = setTimeout(() => child.kill('SIGTERM'), timeoutMs)
    const stdout: Buffer[] = []
    const stderr: Buffer[] = []
    child.stdout.on('data', (chunk) => stdout.push(Buffer.from(chunk)))
    child.stderr.on('data', (chunk) => stderr.push(Buffer.from(chunk)))
    child.on('error', (error) => {
      clearTimeout(timer)
      reject(error)
    })
    child.on('close', (code, signal) => {
      clearTimeout(timer)
      resolve({ code, signal, stdout: Buffer.concat(stdout).toString('utf8'), stderr: Buffer.concat(stderr).toString('utf8') })
    })
    if (input) child.stdin.write(input)
    child.stdin.end()
  })
}

async function ssh(script: string, args: string[] = [], timeoutMs = TIMEOUT_MS): Promise<string> {
  const result = await runProcess('ssh', [PRIMARY_SSH, 'bash', '-s', '--', ...args], script, timeoutMs)
  if (result.code !== 0) {
    throw new Error(`ssh ${PRIMARY_SSH} failed code=${result.code} signal=${result.signal ?? ''}\nSTDERR:\n${result.stderr.slice(0, 8000)}\nSTDOUT:\n${result.stdout.slice(0, 8000)}`)
  }
  return result.stdout
}

async function remoteDetectCheckout(): Promise<string> {
  return (await ssh(`set -euo pipefail
for candidate in "$HOME/swarm-go" "$HOME/src/swarm-go" "$HOME/work/swarm-go"; do
  if [ -d "$candidate" ] && [ -f "$candidate/AGENTS.md" ] && [ -x "$candidate/rebuild" ]; then
    printf '%s\n' "$candidate"
    exit 0
  fi
done
find "$HOME" /opt /srv /tmp -maxdepth 4 -type d -name swarm-go 2>/dev/null \
  | while IFS= read -r candidate; do
      if [ -f "$candidate/AGENTS.md" ] && [ -x "$candidate/rebuild" ]; then
        printf '%s\n' "$candidate"
        exit 0
      fi
    done
exit 1
`, [], 30_000)).trim()
}

async function remoteGitBranch(sourceWorkspacePath: string): Promise<string> {
  return (await ssh('set -euo pipefail\ngit -C "$1" rev-parse --abbrev-ref HEAD\n', [sourceWorkspacePath], 30_000)).trim()
}

async function remoteApiJson<T>(method: string, path: string, body: JsonObject | null, evidenceDir: string, artifactName: string, timeoutMs = 60_000): Promise<T> {
  const bodyText = body ? JSON.stringify(body) : ''
  const bodyB64 = Buffer.from(bodyText).toString('base64')
  const script = `set -euo pipefail
api_url="\${1%/}"
method="$2"
path="$3"
max_time="$4"
body_b64="\${5-}"
cookie_file="$(mktemp)"
response_file="$(mktemp)"
body_file=""
cleanup() { rm -f -- "$cookie_file" "$response_file"; if [ -n "$body_file" ]; then rm -f -- "$body_file"; fi; }
trap cleanup EXIT
curl -sS --connect-timeout 3 --max-time 20 \
  -H 'Accept: application/json' \
  -H "Origin: \${api_url}" \
  -H "Referer: \${api_url}/" \
  -H 'Sec-Fetch-Site: same-origin' \
  -c "$cookie_file" -b "$cookie_file" \
  "$api_url/v1/auth/desktop/session" >/dev/null || true
auth_token=""
if [ -s "$cookie_file" ]; then
  auth_token="$(awk '$6 == "swarm_desktop_session" { value=$7 } END { print value }' "$cookie_file")"
fi
args=(-sS --connect-timeout 3 --max-time "$max_time" -o "$response_file" -w '%{http_code}'
  -H 'Accept: application/json'
  -H "Origin: \${api_url}"
  -H "Referer: \${api_url}/"
  -H 'Sec-Fetch-Site: same-origin'
  -c "$cookie_file" -b "$cookie_file"
  -X "$method")
if [ -n "$auth_token" ]; then args+=(-H "Authorization: Bearer $auth_token"); fi
if [ -n "$body_b64" ]; then
  body_file="$(mktemp)"
  printf '%s' "$body_b64" | base64 -d >"$body_file"
  args+=(-H 'Content-Type: application/json' --data-binary "@$body_file")
fi
http_code="000"
if http_code="$(curl "\${args[@]}" "$api_url$path")"; then :; fi
cat -- "$response_file"
case "$http_code" in
  2*) exit 0 ;;
  *) printf 'HTTP %s for %s %s: %s\n' "$http_code" "$method" "$path" "$(cat -- "$response_file")" >&2; exit 22 ;;
esac
`
  const result = await runProcess('ssh', [PRIMARY_SSH, 'bash', '-s', '--', PRIMARY_API_URL, method, path, String(Math.ceil(timeoutMs / 1000)), bodyB64], script, timeoutMs + 15_000)
  writeArtifact(evidenceDir, artifactName, result.stdout || '{}')
  if (result.stderr.trim()) writeArtifact(evidenceDir, artifactName.replace(/\.json$/, '.stderr.txt'), result.stderr)
  if (result.code !== 0) {
    throw new Error(`${method} ${path} failed via ${PRIMARY_SSH} code=${result.code} signal=${result.signal ?? ''}\nSTDERR:\n${result.stderr.slice(0, 8000)}\nSTDOUT:\n${result.stdout.slice(0, 8000)}`)
  }
  try {
    return JSON.parse(result.stdout || '{}') as T
  } catch (error) {
    throw new Error(`${method} ${path} returned non-JSON response: ${extractErrorText(error)}\n${result.stdout.slice(0, 8000)}`)
  }
}

async function captureRemoteLogs(evidenceDir: string, label: string): Promise<string> {
  const script = `set +e
service_unit="$1"
container_name="$2"
printf '### capture=%s time=%s primary=%s service=%s container=%s\n' "$3" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$4" "$service_unit" "$container_name"
printf '### host=%s\n' "$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
printf '### service status: %s\n' "$service_unit"
systemctl --no-pager --full status "$service_unit" 2>&1 | sed -n '1,30p'
printf '\n### journalctl %s tail\n' "$service_unit"
journalctl -u "$service_unit" --no-pager -n 700 2>&1
printf '\n### journalctl diagnostic grep\n'
journalctl -u "$service_unit" --no-pager -n 5000 2>&1 | grep -Ei 'session_create_worktree_|desktop_routed_peer_open_|worktree_git_worktree_add_|worktree_mode on did not create canonical worktree session state|session|routed|peer|swarm_id|worktree|workspace_binding|authority|deploy|container|trusted principal' || true
if command -v podman >/dev/null 2>&1; then
  printf '\n### podman ps selected\n'
  podman ps -a --filter "name=\${container_name}" 2>&1 || true
  printf '\n### podman logs %s\n' "$container_name"
  podman logs --tail 700 "$container_name" 2>&1 || true
fi
if command -v docker >/dev/null 2>&1; then
  printf '\n### docker ps selected\n'
  docker ps -a --filter "name=\${container_name}" 2>&1 || true
  printf '\n### docker logs %s\n' "$container_name"
  docker logs --tail 700 "$container_name" 2>&1 || true
fi
`
  const result = await runProcess('ssh', [PRIMARY_SSH, 'bash', '-s', '--', SERVICE_UNIT, CONTAINER_NAME, label, PRIMARY_SSH], script, 90_000)
  const text = `STDOUT:\n${result.stdout}\nSTDERR:\n${result.stderr}`
  writeArtifact(evidenceDir, `${label}.log`, text)
  return text
}

function assertDiagnosticLogStages(logText: string): void {
  const hasSessionWorktree = /session_create_worktree_(allocation|before_apply|after_apply|before_create|after_create|after_attach|canonical_state_missing)/.test(logText)
  const hasPeerOpen = /desktop_routed_peer_open_(received|create_error|realized_session_error|success)/.test(logText)
  const hasGitWorktreeAdd = /worktree_git_worktree_add_(start|success|error)/.test(logText)
  const missing = [
    hasSessionWorktree ? '' : 'session_create_worktree_*',
    hasPeerOpen ? '' : 'desktop_routed_peer_open_*',
    hasGitWorktreeAdd ? '' : 'worktree_git_worktree_add_*',
  ].filter(Boolean)
  assert.equal(missing.length, 0, `missing backend diagnostic log stages: ${missing.join(', ')}\n${logText.slice(-8000)}`)
}

async function waitForChildTarget(childSwarmId: string, evidenceDir: string, checkpoints: Checkpoint[]): Promise<void> {
  const deadline = Date.now() + TIMEOUT_MS
  let attempt = 0
  while (Date.now() < deadline) {
    attempt += 1
    const response = await remoteApiJson<{ targets?: Array<{ swarm_id?: string; online?: boolean; selectable?: boolean }> }>('GET', `/v1/swarm/targets?swarm_id=${encodeURIComponent(childSwarmId)}`, null, evidenceDir, 'child_target_poll.json', 30_000)
    const target = (response.targets ?? []).find((candidate) => String(candidate.swarm_id ?? '').trim() === childSwarmId)
    if (target?.online === true && target?.selectable === true) {
      writeArtifact(evidenceDir, 'child_target.json', target)
      mark(checkpoints, 'child.target.online', `child=${childSwarmId} attempts=${attempt}`)
      return
    }
    await new Promise((resolve) => setTimeout(resolve, 3_000))
  }
  throw new Error(`timed out waiting for child target ${childSwarmId} online/selectable`)
}

async function createOrReuseLocalContainer(sourceWorkspacePath: string, evidenceDir: string, checkpoints: Checkpoint[]): Promise<ContainerTarget> {
  if (REUSE_CHILD_SWARM_ID && REUSE_WORKSPACE_BINDING_ID) {
    mark(checkpoints, 'local.container.reused', `child=${REUSE_CHILD_SWARM_ID} binding=${REUSE_WORKSPACE_BINDING_ID}`)
    return {
      childSwarmId: REUSE_CHILD_SWARM_ID,
      workspaceBindingId: REUSE_WORKSPACE_BINDING_ID,
      runtimeWorkspacePath: REUSE_RUNTIME_WORKSPACE_PATH,
      deploymentId: '',
      reused: true,
    }
  }

  const replicateRequest = {
    mode: 'local',
    swarm_name: CONTAINER_NAME,
    sync: { enabled: true, mode: 'managed' },
    workspaces: [{ source_workspace_path: sourceWorkspacePath, replication_mode: 'bundle', writable: true }],
  }
  writeArtifact(evidenceDir, 'replicate_request.json', replicateRequest)
  const response = await remoteApiJson<{
    ok?: boolean
    swarm?: { id?: string; deployment_id?: string }
    workspaces?: Array<{ binding?: { binding_id?: string; destination_workspace_path?: string } }>
  }>('POST', '/v1/swarm/replicate', replicateRequest, evidenceDir, 'replicate_response.json', TIMEOUT_MS)

  assert.equal(response.ok, true, `replicate failed: ${JSON.stringify(response, null, 2)}`)
  const childSwarmId = String(response.swarm?.id ?? '').trim()
  const deploymentId = String(response.swarm?.deployment_id ?? '').trim()
  const workspaceBindingId = String(response.workspaces?.[0]?.binding?.binding_id ?? '').trim()
  const runtimeWorkspacePath = String(response.workspaces?.[0]?.binding?.destination_workspace_path ?? '').trim()
  assert(childSwarmId, `replicate response missing child swarm id: ${JSON.stringify(response, null, 2)}`)
  assert(workspaceBindingId, `replicate response missing workspace binding id: ${JSON.stringify(response, null, 2)}`)
  assert(runtimeWorkspacePath, `replicate response missing runtime workspace path: ${JSON.stringify(response, null, 2)}`)
  mark(checkpoints, 'local.container.created', `child=${childSwarmId} binding=${workspaceBindingId} runtime=${runtimeWorkspacePath}`)
  return { childSwarmId, workspaceBindingId, runtimeWorkspacePath, deploymentId, reused: false }
}

async function cleanupCreatedContainer(evidenceDir: string, target: ContainerTarget | null): Promise<void> {
  await captureRemoteLogs(evidenceDir, 'final').catch((error) => writeArtifact(evidenceDir, 'final.log.capture-error.txt', extractErrorText(error)))
  if (!CLEANUP || !target?.deploymentId) return
  await remoteApiJson('POST', '/v1/deploy/container/delete', { ids: [target.deploymentId] }, evidenceDir, 'cleanup_container_delete.json', 180_000).catch((error) => {
    writeArtifact(evidenceDir, 'cleanup_container_delete.error.txt', extractErrorText(error))
  })
  await remoteApiJson('POST', '/v1/swarm/containers/local/delete', { id: target.deploymentId, name: CONTAINER_NAME, swarm_id: target.childSwarmId, ids: [target.deploymentId], names: [CONTAINER_NAME] }, evidenceDir, 'cleanup_local_container_delete.json', 180_000).catch((error) => {
    writeArtifact(evidenceDir, 'cleanup_local_container_delete.error.txt', extractErrorText(error))
  })
}

async function openWorktreeSession(target: ContainerTarget, sourceWorkspacePath: string, baseBranch: string, evidenceDir: string): Promise<{ session: SessionWire; route: SessionRouteWire }> {
  const sessionCreateRequest: JsonObject = {
    title: `Local container worktree E2E ${CONTAINER_NAME}`,
    workspace_name: workspaceNameFromPath(sourceWorkspacePath),
    workspace_binding_id: target.workspaceBindingId,
    mode: 'auto',
    agent_name: process.env.SWARM_AGENT_NAME || 'swarm',
    worktree_mode: 'on',
    worktree_use_current_branch: false,
    worktree_base_branch: baseBranch,
    worktree_branch_name: BRANCH_NAME,
    metadata: { desktop_local_container_worktree_session_e2e: true },
  }
  assertStableSessionCreateContract(sessionCreateRequest)
  writeArtifact(evidenceDir, 'session_create_request.json', sessionCreateRequest)

  const createResponse = await remoteApiJson<{ ok?: boolean; session?: SessionWire; warning?: string }>('POST', `/v1/sessions?swarm_id=${encodeURIComponent(target.childSwarmId)}`, sessionCreateRequest, evidenceDir, 'session_create_response.json', TIMEOUT_MS)
  assert.equal(createResponse.ok, true, `session create ok=false: ${JSON.stringify(createResponse, null, 2)}`)
  const session = createResponse.session ?? {}
  const sessionId = String(session.id ?? '').trim()
  assert(sessionId, `session create response missing session.id: ${JSON.stringify(createResponse, null, 2)}`)

  const afterOpen = await remoteApiJson<{ session?: SessionWire }>('GET', `/v1/sessions/${encodeURIComponent(sessionId)}`, null, evidenceDir, 'session_after_open.json', 30_000)
  const routeResponse = await remoteApiJson<{ route?: SessionRouteWire }>('GET', `/v1/swarm/topology/session-route?session_id=${encodeURIComponent(sessionId)}`, null, evidenceDir, 'session_route_after_open.json', 30_000)
  return { session: afterOpen.session ?? session, route: routeResponse.route ?? {} }
}

function assertOpenedWorktreeSession(target: ContainerTarget, sourceWorkspacePath: string, session: SessionWire, route: SessionRouteWire): void {
  assert.equal(session.workspace_path, sourceWorkspacePath, `primary mirror workspace_path should remain source workspace: ${JSON.stringify(session, null, 2)}`)
  assert.equal(session.worktree_enabled, true, `session did not mirror child worktree_enabled=true: ${JSON.stringify(session, null, 2)}`)
  assert(String(session.worktree_root_path ?? '').trim(), `session missing worktree_root_path: ${JSON.stringify(session, null, 2)}`)
  assert(String(session.worktree_branch ?? '').trim(), `session missing worktree_branch: ${JSON.stringify(session, null, 2)}`)

  const routeRuntime = String(route.runtime_workspace_path ?? '').trim()
  const hostedRuntime = String(session.metadata?.swarm_routed_runtime_workspace_path ?? '').trim()
  assert(routeRuntime, `topology session route missing runtime_workspace_path: ${JSON.stringify(route, null, 2)}`)
  if (target.runtimeWorkspacePath) {
    assert.notEqual(routeRuntime, target.runtimeWorkspacePath, `worktree_mode:on route runtime did not move to a child-realized worktree path: ${JSON.stringify(route, null, 2)}`)
  }
  assert.equal(routeRuntime, hostedRuntime, `hosted runtime metadata and topology route disagree: session=${JSON.stringify(session, null, 2)} route=${JSON.stringify(route, null, 2)}`)
  assert.equal(route.workspace_binding_id, target.workspaceBindingId, `route workspace_binding_id mismatch: ${JSON.stringify(route, null, 2)}`)
  assert.equal(route.runtime_swarm_id, target.childSwarmId, `route runtime_swarm_id mismatch: ${JSON.stringify(route, null, 2)}`)
  assert(!String(route.backend_url ?? '').trim(), `topology route exposed forbidden backend_url: ${JSON.stringify(route, null, 2)}`)
  assert(!String(route.host_workspace_path ?? '').trim(), `topology route exposed forbidden host_workspace_path: ${JSON.stringify(route, null, 2)}`)
  assert(Number(route.placement_generation ?? 0) > 0, `topology route missing placement_generation: ${JSON.stringify(route, null, 2)}`)
  assert(Number(route.binding_generation ?? 0) > 0, `topology route missing binding_generation: ${JSON.stringify(route, null, 2)}`)
}

test('local-container routed session open creates canonical worktree state using only swarm_id plus workspace_binding_id', { skip: !ENABLED, timeout: Math.max(180_000, TIMEOUT_MS + 180_000) }, async () => {
  assert(PRIMARY_SSH, 'SWARM_PRIMARY_SSH is required for this diagnostic E2E because it captures journal/container logs')
  const evidenceDir = ensureDir(process.env.SWARM_LOCAL_CONTAINER_WORKTREE_ARTIFACT_DIR || process.env.SWARM_E2E_EVIDENCE_DIR || await mkdtemp(join(tmpdir(), 'swarm-local-container-worktree-session-e2e-')))
  const checkpoints: Checkpoint[] = []
  let target: ContainerTarget | null = null
  let summary: JsonObject = { ok: false, evidenceDir, primarySsh: PRIMARY_SSH, primaryApiUrl: PRIMARY_API_URL, containerName: CONTAINER_NAME, requestedBranch: BRANCH_NAME }

  try {
    const sourceWorkspacePath = SOURCE_WORKSPACE_PATH_ENV ? SOURCE_WORKSPACE_PATH_ENV.trim() : await remoteDetectCheckout()
    const baseBranch = BASE_BRANCH_ENV || await remoteGitBranch(sourceWorkspacePath)
    assert(baseBranch && baseBranch !== 'HEAD', `base branch must be an explicit branch name, got ${JSON.stringify(baseBranch)}`)
    summary = { ...summary, sourceWorkspacePath, baseBranch }

    await remoteApiJson('GET', '/readyz', null, evidenceDir, 'readyz.json', 20_000)
    await remoteApiJson('GET', '/v1/auth/desktop/session', null, evidenceDir, 'desktop_session.json', 20_000)
    await captureRemoteLogs(evidenceDir, 'before')

    target = await createOrReuseLocalContainer(sourceWorkspacePath, evidenceDir, checkpoints)
    await waitForChildTarget(target.childSwarmId, evidenceDir, checkpoints)
    await captureRemoteLogs(evidenceDir, 'after_replicate')

    let opened: { session: SessionWire; route: SessionRouteWire }
    try {
      opened = await openWorktreeSession(target, sourceWorkspacePath, baseBranch, evidenceDir)
    } catch (error) {
      const logs = await captureRemoteLogs(evidenceDir, 'session_create_failed')
      const details = extractErrorText(error)
      const expectedFailure = details.includes(SESSION_CANONICAL_STATE_FAILURE) || logs.includes(SESSION_CANONICAL_STATE_FAILURE)
      if (expectedFailure) {
        assertDiagnosticLogStages(logs)
        summary = { ...summary, ok: false, expectedDiagnosticFailure: SESSION_CANONICAL_STATE_FAILURE, error: details }
        writeArtifact(evidenceDir, 'summary.json', summary)
        throw new Error(`${SESSION_CANONICAL_STATE_FAILURE}\nArtifacts: ${evidenceDir}\n${details}`)
      }
      summary = { ...summary, ok: false, error: details }
      writeArtifact(evidenceDir, 'summary.json', summary)
      throw error
    }

    assertOpenedWorktreeSession(target, sourceWorkspacePath, opened.session, opened.route)
    const logs = await captureRemoteLogs(evidenceDir, 'after_open')
    assertDiagnosticLogStages(logs)

    summary = {
      ...summary,
      ok: true,
      childSwarmId: target.childSwarmId,
      deploymentId: target.deploymentId,
      workspaceBindingId: target.workspaceBindingId,
      baseRuntimeWorkspacePath: target.runtimeWorkspacePath,
      sessionId: opened.session.id ?? '',
      routeRuntimeWorkspacePath: opened.route.runtime_workspace_path ?? '',
      worktreeRootPath: opened.session.worktree_root_path ?? '',
      worktreeBranch: opened.session.worktree_branch ?? '',
      requestContract: 'POST /v1/sessions?swarm_id=<local-container> with workspace_binding_id and no workspace_path/host_workspace_path/runtime_workspace_path',
      diagnosticLogStagesAsserted: ['session_create_worktree_*', 'desktop_routed_peer_open_*', 'worktree_git_worktree_add_*'],
      expectedFailureSignal: SESSION_CANONICAL_STATE_FAILURE,
      evidence: [
        'replicate_request.json',
        'replicate_response.json',
        'session_create_request.json',
        'session_create_response.json',
        'session_after_open.json',
        'session_route_after_open.json',
        'before.log',
        'after_replicate.log',
        'after_open.log',
        'final.log',
      ],
    }
    writeArtifact(evidenceDir, 'checkpoints.json', checkpoints)
    writeArtifact(evidenceDir, 'summary.json', summary)
    console.log(`desktop local-container worktree session E2E evidence\n${JSON.stringify(summary, null, 2)}`)
  } catch (error) {
    if (!includesCanonicalWorktreeFailure(error)) {
      summary = { ...summary, ok: false, error: extractErrorText(error) }
      writeArtifact(evidenceDir, 'summary.json', summary)
    }
    writeArtifact(evidenceDir, 'checkpoints.json', checkpoints)
    throw error
  } finally {
    await cleanupCreatedContainer(evidenceDir, target)
  }
})
