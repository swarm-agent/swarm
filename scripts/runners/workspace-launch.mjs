#!/usr/bin/env node
// Requirement: attach-only trials may create isolated fixtures and owned sessions,
// never select ambient workspaces, restore shared settings, or deploy a candidate.
// Production authority: server_routes.go, sessions_v3_primary.go, git_snapshot.go.
import assert from 'node:assert/strict'
import { randomUUID, createHash } from 'node:crypto'
import { writeFile, rename } from 'node:fs/promises'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
import { AttachClient } from '../testbench-attach.mjs'

const digest = value => createHash('sha256').update(JSON.stringify(value)).digest('hex')
export function repositoryState(status) {
  assert(status?.has_git && status.repo_root && /^[a-f0-9]{40}$/.test(status.head_oid || ''), 'complete Git identity required')
  // gitstatus.Snapshot includes observation timing, not repository state.
  const { refreshed_at, duration_ms, ...state } = status
  return state
}
const terminal = new Set(['completed', 'failed', 'cancelled', 'expired', 'interrupted'])
export class WorkspaceTrial extends AttachClient {
  constructor(url, options = {}) {
    const controller = new AbortController()
    super(url, { ...options, signal: controller.signal })
    this.controller = controller
    this.id = `workspace-launch-${randomUUID()}`
    this.sessions = new Map()
    this.fixtures = new Map()
    this.activeRuns = new Map()
    this.pendingRuns = new Map()
    this.evidence = { trial: this.id, sessions: [], fixtures: [], cases: Array.from({ length: 12 }, (_, i) => ['a','b','c','d'].map(letter => ({ id: `S${String(i + 1).padStart(2, '0')}-${letter}`, status: 'not-run', reason: 'Full must-pass proof not implemented by baseline routing observation' }))).flat(), limitations: [], cleanup: [] }
    this.evidencePath = options.evidencePath
  }
  async persist() {
    if (!this.evidencePath) return
    const temporary = `${this.evidencePath}.next`
    await writeFile(temporary, JSON.stringify(this.evidence, null, 2), { mode: 0o600 })
    await rename(temporary, this.evidencePath)
  }
  async initialize() {
    this.identity = await this.inspect(process.env.SWARM_ATTACH_EXPECTED_RUNTIME || '')
    if (process.env.SWARM_ATTACH_EXPECTED_SETTINGS) assert.equal(this.identity.settings_sha256, process.env.SWARM_ATTACH_EXPECTED_SETTINGS)
    this.evidence.identity = this.identity
    this.settings = (await this.get('/v1/agent-model-settings')).agent_model_settings
    // Read-only baseline; no selection/restore operation exists in this runner.
    this.selection = digest(await super.request('GET', '/v1/workspace/current'))
    await this.persist()
  }
  owned(id) { assert(this.sessions.has(id), 'unowned session rejected'); return encodeURIComponent(id) }
  async hydrate(id) {
    this.owned(id)
    return super.request('POST', '/v3/sync/hydrate', {
      surface: 'desktop', session_ids: [id],
      history: { mode: 'tail', max_messages_per_session: 80, max_events_per_session: 120, manifest_policy: 'manifest' },
      resources: { messages: true, events: true, run_intents: true, current_run_state: true, session_view: true, active_plan: false, permission_summaries: true }, include_active: true,
    })
  }
  async repositories(id) {
    const result = await super.request('GET', `/v3/sessions/${this.owned(id)}/repositories?limit=20`)
    assert(!result.next_cursor, 'repository inventory truncated; no partial proof')
    assert(Array.isArray(result.items), 'repository rows missing')
    return result.items
  }
  async status(id, selected) {
    const rows = await this.repositories(id)
    assert(rows.some(row => row.workspace_path === selected), 'unowned repository selector')
    return repositoryState((await super.request('GET', `/v1/workspace/git/status?session_id=${this.owned(id)}&workspace_path=${encodeURIComponent(selected)}&recent_limit=20`)).status)
  }
  async fixture(parent, label) {
    assert(path.posix.isAbsolute(parent) && path.posix.normalize(parent) === parent && parent !== '/', 'explicit canonical fixture parent required')
    assert(/^[a-z]$/.test(label), 'invalid fixture label')
    const name = `${this.id}-${label}`
    const expected = path.posix.join(parent, name)
    const made = await super.request('POST', '/v1/workspace/folders/create', { parent_path: parent, name })
    assert.equal(made.folder?.path, expected, 'folder identity mismatch')
    this.evidence.fixtures.push({ path: expected, state: 'created' })
    await this.persist() // retain partial fixture identity even if setup fails
    const setup = await super.request('POST', '/v1/workspace/repository/setup', { path: expected, expected_resolved_path: expected })
    assert.match(setup.repository?.head_commit || '', /^[a-f0-9]{40}$/)
    const added = await super.request('POST', '/v1/workspace/add', { path: expected, name, make_current: false })
    const ws = added.workspace
    assert(ws?.workspace_id && ws.local_workspace_binding_id, 'stable workspace/binding identity missing')
    const fixture = { path: expected, workspace: ws, head: setup.repository.head_commit, marker: randomUUID() }
    this.fixtures.set(label, fixture)
    Object.assign(this.evidence.fixtures.at(-1), fixture, { state: 'registered' })
    await this.persist()
    return fixture
  }
  async create(fixture, label) {
    assert([...this.fixtures.values()].includes(fixture), 'unowned fixture')
    const key = `${this.id}:${label}`
    const body = {
      client_request_id: key, idempotency_key: key, title: key,
      workspace_path: fixture.path, workspace_name: fixture.workspace.name || label,
      workspace_binding_id: fixture.workspace.local_workspace_binding_id,
      swarm_id: this.identity.runtime_id, target_kind: 'host', target_relationship: 'self',
      mode: 'auto', agent_name: 'swarm', worktree_mode: 'on', worktree_branch_name: `agent/launch-${randomUUID()}`,
      preference: this.settings.swarm.action, model_profile: { use_agent_default: true },
      metadata: { runner_test_id: this.id },
    }
    const result = await super.request('POST', '/v3/sessions', body)
    const session = result.session
    assert(session?.id && session.worktree_enabled && session.worktree_root_path, 'isolated session missing')
    this.sessions.set(session.id, session)
    this.evidence.sessions.push({ id: session.id, worktree: session.worktree_root_path, request: key })
    await this.persist()
    assert.equal(session.workspace_path, fixture.path)
    assert.notEqual(session.worktree_root_path, fixture.path)
    const replay = await super.request('POST', '/v3/sessions', body)
    assert.equal(replay.session?.id, session.id)
    assert.equal(replay.session?.worktree_root_path, session.worktree_root_path)
    const rows = await this.repositories(session.id)
    const lane = rows.find(row => row.kind === 'parent' && row.workspace_path === session.worktree_root_path)
    assert(lane && lane.source_path === fixture.path && lane.base_commit === fixture.head, 'persisted lane/source/base mismatch')
    return session
  }
  interrupt() { this.deadline = performance.now(); this.controller.abort() }
  async prove(id, selected, files = ['marker.txt']) {
    this.owned(id)
    const inventory = await this.repositories(id)
    assert(selected.length > 0 && selected.length <= 8 && new Set(selected).size === selected.length, 'bounded unique proof selectors required')
    assert(selected.every(root => inventory.some(row => row.workspace_path === root && row.availability === 'available')), 'proof selector not authenticated')
    assert(files.length <= 4 && files.every(file => /^[a-z][a-z0-9.-]*$/.test(file)), 'proof file names must be bounded basenames')
    const command = proofCommand(selected, files)
    const { snapshot, runID } = await this.run(id, `Use Bash exactly once with command ${JSON.stringify(command)}, category=read, critical=false, explanation=["Read only the disposable trial repositories."]. Do not run any other tool or mutate files/settings. Do not rewrite the command. Finish after the tool result.`, [{ name: 'bash', args: { command, category: 'read', critical: false, explanation: ['Read only the disposable trial repositories.'] } }])
    const calls = completedTools(snapshot, id, runID)
    assert(calls.every(({ payload }) => (payload.tool_name || payload.name) === 'bash' && !payload.error), 'unexpected tool in filesystem proof')
    assert.equal(calls.length, 1, 'exactly one Bash proof required')
    const args = decodeObject(calls[0].payload.arguments)
    assert.equal(args.command, command, 'proof command changed')
    const result = decodeObject(calls[0].payload.output)
    assert.equal(result.exit_code, 0, 'filesystem proof failed')
    assert(!result.truncated && !result.timed_out, 'filesystem proof truncated')
    const proof = decodeObject(result.output)
    assert.deepEqual(proof.repositories.map(row => row.path), selected, 'proof roots differ')
    assert.equal(proof.cwd, this.sessions.get(id).worktree_root_path, 'Bash default cwd differs')
    this.evidence.proofs ||= []
    this.evidence.proofs.push({ run_id: runID, ...proof })
    await this.persist()
    return proof
  }
  async attach(id, fixtures, primary) {
    this.owned(id)
    assert(fixtures.every(item => [...this.fixtures.values()].includes(item)) && fixtures.includes(primary), 'unowned attachment fixture')
    const ids = fixtures.map(item => item.workspace.workspace_id)
    const { snapshot } = await this.run(id, `Use manage_workspace exactly once with action=set_session, workspace_ids=${JSON.stringify(ids)}, primary_workspace_id=${JSON.stringify(primary.workspace.workspace_id)}. Keep this same session. Do not change account default, settings, files, or launch workers. After the canonical restart, finish without another mutation.`, [{ name: 'manage_workspace', args: { action: 'set_session', workspace_ids: ids, primary_workspace_id: primary.workspace.workspace_id } }])
    const session = snapshot.sessions_by_id?.[id]
    assert.equal(session?.metadata?.swarm_v3_source_workspace_id, primary.workspace.workspace_id, 'explicit default not persisted')
    assert.equal(session.workspace_path, primary.path)
    assert(session.worktree_enabled && session.worktree_root_path !== primary.path, 'default escaped isolation')
    const rows = await this.repositories(id)
    const attached = rows.filter(row => row.kind === 'source' && row.attached)
    assert.deepEqual([...new Set(attached.map(row => row.workspace_id))].sort(), [...ids].sort(), 'attachment set differs')
    assert(attached.filter(row => row.default).every(row => row.workspace_id === primary.workspace.workspace_id) && attached.some(row => row.default), 'wrong explicit default badge')
    this.sessions.set(id, session)
    await this.unchanged()
    return session
  }
  async resolvePermissions(id, runID, pending, approvedCalls) {
    const sid = this.owned(id)
    assert(pending.length <= 8, 'permission response exceeds trial budget')
    // Preflight the complete list before resolving anything. Never approve an
    // arbitrary task, scope expansion, changed command, or another run.
    for (const record of pending) {
      assert(record.session_id === id && record.run_id === runID && record.status === 'pending' && /^[a-zA-Z0-9_-]+$/.test(record.id), 'permission ownership mismatch')
      const args = decodeObject(record.tool_call_arguments || record.tool_arguments)
      assert(approvedCalls.some(call => call.name === record.tool_name && JSON.stringify(sortedObject(call.args)) === JSON.stringify(sortedObject(args))), 'permission differs from exact trial call')
    }
    for (const record of pending) {
      const result = await super.request('POST', `/v3/sessions/${sid}/permissions/${record.id}/resolve`, { action: 'allow_once', reason: 'Exact owned launch fixture call only' })
      assert(!result.saved_rule, 'permission persisted an unexpected rule')
    }
  }
  async run(id, content, approvedCalls = []) {
    const sid = this.owned(id)
    assert.equal(this.activeRuns.size + this.pendingRuns.size, 0, 'provider fan-out exceeds one trial parent')
    // acceptSessionsV3Message accepts an explicit RunID. Journal it before POST;
    // a lost response must never lead to a second message or an ambient-run stop.
    const runID = `launch-${randomUUID()}`
    const requestID = `${this.id}:${randomUUID()}`
    this.pendingRuns.set(id, runID)
    this.evidence.sessions.find(item => item.id === id).pending_message = { request_id: requestID, run_id: runID }
    await this.persist()
    const response = await super.request('POST', `/v3/sessions/${sid}/messages`, {
      client_request_id: requestID, run_id: runID, role: 'user', content,
      metadata: { runner_test_id: this.id },
    })
    assert.equal(response.run_intent?.run_id || response.run_id, runID, 'admitted run identity mismatch')
    this.pendingRuns.delete(id)
    this.activeRuns.set(id, runID)
    this.evidence.sessions.find(item => item.id === id).run_id = runID
    await this.persist()
    let lastSeq = -1, lastProgress = performance.now(), lastLog = 0
    while (performance.now() < this.deadline - 20000) {
      const snapshot = await this.hydrate(id)
      const events = snapshot.events_by_session?.[id] || []
      const seq = Math.max(0, ...events.map(event => Number(event.seq || event.sequence || 0)))
      if (seq !== lastSeq) { lastSeq = seq; lastProgress = performance.now() }
      const runs = snapshot.run_intents_by_session?.[id] || []
      const run = runs.find(item => item.run_id === runID)
      if (performance.now() - lastLog >= 10000) {
        console.log(JSON.stringify({ trial: this.id, run_status: run?.status || 'awaiting-projection', event_seq: seq }))
        lastLog = performance.now()
      }
      if (terminal.has(run?.status)) {
        this.activeRuns.delete(id)
        assert.equal(run.status, 'completed', `provider run ended ${run.status}`)
        return { snapshot, runID }
      }
      const permissions = await super.request('GET', `/v3/sessions/${sid}/permissions?status=pending&limit=20`)
      if (permissions.permissions?.length) await this.resolvePermissions(id, runID, permissions.permissions, approvedCalls)
      assert(performance.now() - lastProgress < 90000, 'provider progress stalled')
      await new Promise(resolve => setTimeout(resolve, 1000))
    }
    throw new Error('provider stage deadline; reserve remaining time for owned cancellation')
  }
  async stopOwned() {
    // A fresh bounded client preserves cleanup capacity after the stage deadline.
    const cleanup = new AttachClient(this.origin, { stageMs: 20000, requestMs: 5000 })
    cleanup.token = this.token
    for (const [id, run] of this.pendingRuns) {
      try {
        this.owned(id)
        const snapshot = await cleanup.request('POST', '/v3/sync/hydrate', {
          surface: 'desktop', session_ids: [id],
          history: { mode: 'tail', max_messages_per_session: 1, max_events_per_session: 1, manifest_policy: 'manifest' },
          resources: { run_intents: true }, include_active: true,
        })
        const admitted = (snapshot.run_intents_by_session?.[id] || []).find(item => item.run_id === run)
        assert(admitted, 'lost response admission unresolved')
        this.pendingRuns.delete(id)
        if (!terminal.has(admitted.status)) this.activeRuns.set(id, run)
        else this.evidence.cleanup.push({ session: id, run, status: admitted.status })
      } catch { this.evidence.cleanup.push({ session: id, run, status: 'admission unresolved; manual exact-request inspection required' }) }
    }
    for (const [id, run] of this.activeRuns) {
      try {
        const result = await cleanup.request('POST', `/v3/sessions/${this.owned(id)}/run/stop`, { run_id: run, target_swarm_id: this.identity.runtime_id, reason: 'owned launch trial stopped' })
        assert(terminal.has(result.status), 'cancellation did not acknowledge terminal state')
        this.evidence.cleanup.push({ session: id, run, status: result.status })
        this.activeRuns.delete(id)
      } catch { this.evidence.cleanup.push({ session: id, run, status: 'unconfirmed; manual owned-run inspection required' }) }
    }
    await this.persist()
  }
  async unchanged() {
    assert.equal(digest((await this.get('/v1/agent-model-settings')).agent_model_settings), this.identity.settings_sha256, 'shared assignments drifted')
    assert.equal(digest(await super.request('GET', '/v1/workspace/current')), this.selection, 'shared workspace selection drifted')
  }
}

function sortedObject(value) {
  if (Array.isArray(value)) return value.map(sortedObject)
  if (value && typeof value === 'object') return Object.fromEntries(Object.keys(value).sort().map(key => [key, sortedObject(value[key])]))
  return value
}

export function decodeObject(value) {
  const result = typeof value === 'string' ? JSON.parse(value) : value
  assert(result && typeof result === 'object' && !Array.isArray(result), 'structured tool evidence missing')
  return result
}

export function proofCommand(roots, files) {
  // Only authenticated disposable selectors reach this command. No shell Git
  // hooks, external diff, arbitrary traversal or unbounded repository walks.
  const code = `import os,json,subprocess,pathlib\nroots=json.loads(${JSON.stringify(JSON.stringify(roots))})\nfiles=json.loads(${JSON.stringify(JSON.stringify(files))})\nrows=[]\nfor root in roots:\n def git(*args):\n  return subprocess.run(['git','-C',root,*args],check=True,capture_output=True,text=True,timeout=3).stdout.strip()\n row={'path':root,'root':git('rev-parse','--show-toplevel'),'common':git('rev-parse','--path-format=absolute','--git-common-dir'),'head':git('rev-parse','HEAD'),'branch':git('branch','--show-current'),'status':git('status','--porcelain'),'files':{}}\n for name in files:\n  p=pathlib.Path(root)/name\n  if p.is_symlink(): raise RuntimeError('symlink proof file')\n  if p.exists():\n   with p.open('rb') as f: data=f.read(4097)\n   if len(data)>4096: raise RuntimeError('proof file exceeds limit')\n   row['files'][name]=data.decode('utf-8')\n  else: row['files'][name]=None\n rows.append(row)\nprint(json.dumps({'cwd':os.getcwd(),'repositories':rows}))`
  return `python3 -c '${code.replaceAll("'", "'\\''")}'`
}

export function completedTools(snapshot, id, runID) {
  return (snapshot.events_by_session?.[id] || []).filter(event => event.event_type === 'session.tool.completed').map(event => {
    const payload = typeof event.payload === 'string' ? JSON.parse(event.payload) : event.payload
    return { event, payload }
  }).filter(({ event, payload }) => (event.run_id || payload?.run_id) === runID)
}

export async function routing(trial, parent) {
  const a = await trial.fixture(parent, 'a'), b = await trial.fixture(parent, 'b')
  const sa = await trial.create(a, 'a')
  const sb = await trial.attach(sa.id, [a, b], b)
  const oldLane = sa.worktree_root_path
  // Fixture markers are authored by real tools in owned lanes, not captured repos.
  const selected = [a.path, b.path, oldLane, sb.worktree_root_path]
  const beforeProof = await trial.prove(sb.id, selected)
  assert.notEqual(beforeProof.repositories[0].common, beforeProof.repositories[1].common, 'fixture repositories share Git authority')
  assert.equal(beforeProof.repositories[2].common, beforeProof.repositories[0].common)
  assert.equal(beforeProof.repositories[3].common, beforeProof.repositories[1].common)
  const before = await Promise.all([trial.status(sa.id, a.path), trial.status(sb.id, b.path), trial.status(sa.id, oldLane)])
  const exactCalls = [
    { name: 'write', args: { path: 'marker.txt', content: b.marker + '\n' } },
    { name: 'read', args: { path: 'marker.txt' } },
    { name: 'search', args: { query: b.marker, path: '.', max_results: 10 } },
    { name: 'find', args: { query: 'marker.txt', path: '.', max_results: 10 } },
    { name: 'list', args: { path: '.', max_entries: 20 } },
    { name: 'edit', args: { path: 'marker.txt', old_string: b.marker, new_string: b.marker + '-edited' } },
    { name: 'bash', args: { command: 'pwd', category: 'read', critical: false, explanation: ['Show the disposable session cwd.'] } },
  ]
  const prompt = `In this disposable test session only execute these tools in order with these exact arguments: ${JSON.stringify(exactCalls)}. Do not run task, change settings, commit, or access any other repository. Finish without claiming tests passed.`
  for (const id of ['S05-a', 'S05-b', 'S05-c', 'S05-d']) Object.assign(trial.evidence.cases.find(item => item.id === id), { status: 'fail', reason: 'entered routing mutation stage; exact postconditions not yet established' })
  await trial.persist()
  const { snapshot, runID } = await trial.run(sb.id, prompt, exactCalls)
  const tools = completedTools(snapshot, sb.id, runID)
  for (const tool of ['write', 'read', 'search', 'find', 'list', 'edit', 'bash']) {
    const outputs = tools.filter(({ payload }) => (payload.tool_name || payload.name) === tool)
    assert(outputs.length, `actual ${tool} completion evidence missing`)
    assert(outputs.every(({ payload }) => !payload.error), `${tool} reported an error`)
  }
  const after = await Promise.all([trial.status(sa.id, a.path), trial.status(sb.id, b.path), trial.status(sa.id, oldLane)])
  assert.deepEqual(after, before, 'wrong repository changed')
  const afterProof = await trial.prove(sb.id, selected)
  assert.deepEqual(afterProof.repositories.slice(0, 3), beforeProof.repositories.slice(0, 3), 'captured repositories or retained lane changed')
  assert.equal(afterProof.repositories[3].files['marker.txt'], b.marker + '-edited\n', 'edited bytes differ')
  assert.equal(afterProof.repositories[3].head, beforeProof.repositories[3].head, 'unexpected commit')
  await trial.unchanged()
  for (const id of ['S05-a', 'S05-b', 'S05-c', 'S05-d']) {
    Object.assign(trial.evidence.cases.find(item => item.id === id), { status: 'pass', reason: 'actual tool completions plus exact controlled Bash filesystem/common-dir evidence and unchanged captured roots' })
  }
  trial.evidence.observations = { owned_session: sb.id, run_id: runID, tool_names: tools.map(({ payload }) => payload.tool_name || payload.name), captured_status_unchanged: true }
  // Completion names alone cannot prove FFF/read/list fidelity. S05 additionally
  // requires exact controlled filesystem output; redaction fails closed.
  trial.evidence.limitations.push('S01/S03: allocator-count, account-default-A, removal and exact restart-boundary proof not implemented by this baseline trial', 'S04: exact read/search/find/list outputs still require retained payloads; S05 proof refuses missing or rewritten controlled Bash output', 'S07/S08: cross-repository worker integration and staged provider program not implemented; unsafe legacy runners remain denied')
  await trial.persist()
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const trial = new WorkspaceTrial(process.env.SWARM_DESKTOP_URL, { stageMs: 540000, evidencePath: process.argv[2] })
  let interrupted = false
  const stop = () => { interrupted = true; trial.interrupt() }
  process.on('SIGTERM', stop); process.on('SIGINT', stop)
  try {
    assert(Number(process.env.SWARM_LAUNCH_CLEANUP_SECONDS) === 25, 'reviewed supervisor cleanup budget required')
    assert(process.env.SWARM_ATTACH_EXPECTED_RUNTIME, 'pin independently verified candidate runtime before mutation')
    assert(process.env.SWARM_TESTBENCH_ATTACH_ONLY === '1', 'attach-only required')
    assert(process.env.SWARM_ATTACH_FIXTURE_PARENT, 'SWARM_ATTACH_FIXTURE_PARENT must name the reviewed disposable fixture parent on the candidate')
    const stage = process.argv[3] || 'routing'
    if (stage !== 'routing') throw new Error('worker stage disabled: owned child cancellation and exact task-permission review remain required')
    await trial.initialize()
    await routing(trial, process.env.SWARM_ATTACH_FIXTURE_PARENT)
    process.exitCode = 2 // incomplete must-pass coverage is never a green suite
  } catch (error) {
    // Never print provider/API response bodies.
    trial.evidence.failure = error.code || error.name || 'trial_failure'
    console.error(`workspace-launch: ${trial.evidence.failure}; inspect private evidence and owned sessions`)
    process.exitCode = 1
  } finally {
    await trial.stopOwned()
    trial.evidence.counts = { pass: trial.evidence.cases.filter(c => c.status === 'pass').length, fail: trial.evidence.cases.filter(c => c.status === 'fail').length, 'not-run': trial.evidence.cases.filter(c => c.status === 'not-run').length }
    if (interrupted || trial.activeRuns.size || trial.pendingRuns.size) process.exitCode = 1
    await trial.persist()
    process.off('SIGTERM', stop); process.off('SIGINT', stop)
  }
}

// Provider-worker stages intentionally share the reviewed fixture/cancellation
// client, never the legacy runner's model writes or ambient binding selection.
// Keep disabled admission until owned child cancellation and exact permission
// review are established; exporting an executable stage is not a live pass.
export async function workers(trial, parent, kind) {
  assert(['regular', 'program'].includes(kind), 'unsupported worker stage')
  const a = await trial.fixture(parent, 'a'), b = await trial.fixture(parent, 'b')
  const sa = await trial.create(a, kind)
  const session = await trial.attach(sa.id, [a, b], b)
  const before = await trial.prove(session.id, [a.path, b.path, sa.worktree_root_path, session.worktree_root_path], ['left.txt', 'right.txt'])
  const markers = { 'left.txt': `${a.marker}\n`, 'right.txt': `${b.marker}\n`, 'check.txt': 'verified\n' }
  const assignment = (file, source) => ({
    agent_type: 'coder', title: `Fixture ${file}`, workspace_path: source,
    meta_prompt: `Create only ${file} with exact UTF-8 bytes ${JSON.stringify(markers[file])}. Commit the scoped file and finish clean. Do not execute commands, change settings, or touch captured source. Tests not run; parent validation required.`,
    deliverable: `Committed ${file} with exact bytes`, owned_scope: [file],
    dependency_evidence: 'Disposable committed source; independent exact file scope',
    acceptance_criteria: ['Exact committed file and clean owned child lane'],
  })
  const left = assignment('left.txt', kind === 'regular' ? a.path : b.path)
  const right = assignment('right.txt', b.path)
  let request
  if (kind === 'regular') {
    const launches = [left, right].map(({ agent_type, acceptance_criteria, ...job }) => ({ ...job, subagent_type: agent_type, concurrency_reason: 'Independent disposable repository and exact file' }))
    request = { mode: 'regular', prompt: 'Create two exact disposable fixture commits in independent repositories.', launches }
  } else {
    // Later consumer owns a separate file and requires both integrated jobs.
    const later = assignment('check.txt', b.path)
    later.meta_prompt = `Read the integrated left.txt and right.txt and verify exact bytes ${JSON.stringify(markers)}. Create check.txt containing exactly verified\\n only if both match, commit only check.txt and finish clean. No commands. Tests not run; parent validation required.`
    later.deliverable = 'Committed check.txt after reading integrated prerequisites'
    request = { action: 'start', prompt: 'Execute one same-repository two-stage exact-byte fixture program.', program: {
      id: `fixture-${randomUUID()}`, max_concurrency: 2,
      stages: [{ id: 'write', dependency_evidence: 'Independent scopes at one committed base' }, { id: 'verify', depends_on: ['write'], dependency_evidence: 'Both prerequisite commits integrated' }],
      jobs: [{ ...left, id: 'left', stage_id: 'write' }, { ...right, id: 'right', stage_id: 'write' }, { ...later, id: 'verify', stage_id: 'verify', depends_on: ['left', 'right'] }],
    } }
  }
  const { snapshot, runID } = await trial.run(session.id, `Call task exactly once using this complete request: ${JSON.stringify(request)}. Do not change assignments, reduce jobs, approve permissions, retry failed workers or change settings. After return, stop and report the exact task outcome. No other tool.`)
  const calls = completedTools(snapshot, session.id, runID).filter(({ payload }) => (payload.tool_name || payload.name) === 'task')
  assert.equal(calls.length, 1, 'one task completion required')
  assert(!calls[0].payload.error, 'task failed')
  const output = decodeObject(calls[0].payload.output)
  const rows = await trial.repositories(session.id)
  const children = rows.filter(row => row.kind === 'worker' && row.session_id !== session.id && row.availability === 'available')
  assert.equal(children.length, kind === 'regular' ? 2 : 3, 'exact child lane count differs')
  const childPaths = children.map(row => row.workspace_path)
  const childProof = await trial.prove(session.id, childPaths, ['left.txt', 'right.txt', 'check.txt'])
  verifyWorkerProof(kind, before, children, childProof, markers)
  if (kind === 'regular') {
    for (const source of [a.path, b.path]) {
      const child = children.find(row => row.source_path === source)
      assert(child, 'missing repository-specific child')
      await trial.run(session.id, `Use manage-worktree integrate for only session_ids=${JSON.stringify([child.session_id])}, workspace_path=${JSON.stringify(source)}. Integrate only into this session's owned lane; never promote or advance the captured checkout. Do not commit dirty work, retry, change settings, or run other tools.`)
    }
  }
  const after = await trial.prove(session.id, [a.path, b.path, sa.worktree_root_path, session.worktree_root_path], ['left.txt', 'right.txt', 'check.txt'])
  for (let i = 0; i < 2; i++) {
    assert.equal(after.repositories[i].head, before.repositories[i].head, 'captured HEAD advanced')
    assert.equal(after.repositories[i].status, before.repositories[i].status, 'captured index/status changed')
    assert.equal(after.repositories[i].files['left.txt'], before.repositories[i].files['left.txt'])
    assert.equal(after.repositories[i].files['right.txt'], before.repositories[i].files['right.txt'])
  }
  if (kind === 'regular') {
    assert.equal(after.repositories[2].files['left.txt'], markers['left.txt'])
    assert.equal(after.repositories[2].files['right.txt'], null)
    assert.equal(after.repositories[3].files['right.txt'], markers['right.txt'])
    assert.equal(after.repositories[3].files['left.txt'], null)
  } else {
    assert.equal(after.repositories[3].files['left.txt'], markers['left.txt'])
    assert.equal(after.repositories[3].files['right.txt'], markers['right.txt'])
    assert.equal(after.repositories[3].files['check.txt'], 'verified\n')
  }
  assert(after.repositories.slice(2).every(row => row.status === ''), 'integration lane dirty')
  await trial.unchanged()
  trial.evidence.worker_observations = { kind, parent_run: runID, children: children.map(row => ({ session_id: row.session_id, path: row.workspace_path, base: row.base_commit })), task_state: output.state || output.status }
  trial.evidence.limitations.push('S07/S08: actual overlap intervals and child assignment/provider lineage require complete durable scheduling evidence; byte/common-dir observations alone do not establish all four subcases', 'S08: this two-stage Coder consumer is not the specified Finder and managed Designer source handoff')
  await trial.persist()
}

export function verifyWorkerProof(kind, before, children, proof, markers) {
  assert.equal(new Set(children.map(row => row.workspace_path)).size, children.length, 'workers share writable lane')
  for (const [index, child] of children.entries()) {
    const actual = proof.repositories[index]
    const source = before.repositories.find(row => row.path === child.source_path)
    assert(source, 'child source not a trial fixture')
    assert.equal(actual.path, child.workspace_path)
    assert.equal(actual.common, source.common, 'child common-dir differs')
    assert.equal(actual.branch, child.branch, 'child branch differs')
    assert.equal(actual.status, '', 'dirty handoff rejected')
    assert.match(actual.head, /^[a-f0-9]{40}$/, 'invalid committed child HEAD')
    assert.match(child.base_commit, /^[a-f0-9]{40}$/)
    assert.notEqual(actual.head, child.base_commit, 'no committed child change')
  }
  for (const file of ['left.txt', 'right.txt']) assert(proof.repositories.some(row => row.files[file] === markers[file]), 'exact committed marker missing')
  if (kind === 'regular') {
    for (const file of ['left.txt', 'right.txt']) assert.equal(proof.repositories.filter(row => row.files[file] !== null).length, 1, 'cross-repository file leaked')
    assert(children.every(child => child.base_commit === before.repositories.find(row => row.path === child.source_path).head), 'regular immutable base differs')
  } else {
    const initial = children.filter(child => child.base_commit === before.repositories[3].head)
    assert.equal(initial.length, 2, 'initial siblings lack common immutable base')
    const nextIndex = proof.repositories.findIndex(row => row.files['check.txt'] === 'verified\n')
    const next = proof.repositories[nextIndex]
    assert(next && next.files['left.txt'] === markers['left.txt'] && next.files['right.txt'] === markers['right.txt'], 'later consumer lacks integrated bytes')
    assert.notEqual(children[nextIndex].base_commit, before.repositories[3].head, 'later stage forked initial base')
    const initialRows = children.map((child, i) => ({ child, row: proof.repositories[i] })).filter(({ child }) => child.base_commit === before.repositories[3].head)
    for (const file of ['left.txt', 'right.txt']) assert.equal(initialRows.filter(({ row }) => row.files[file] !== null).length, 1, 'parallel scopes leaked into sibling')
  }
}
