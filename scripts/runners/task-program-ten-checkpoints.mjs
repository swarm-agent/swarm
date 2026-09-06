#!/usr/bin/env node
// Purpose: provider-backed acceptance of ten attached programs and interleaved
// inline programs through the real V3 checkpoint lifecycle. Threats: false
// completion from launch-only evidence, replayed jobs, lost mixed handoffs,
// wrong-repository integration, and silently advancing an unproved checkpoint.
// Run beside the daemon; init creates owned fixtures, observe is resumable and
// bounded to one checkpoint (<=10 minutes). Credentials are memory-only.
import crypto from 'node:crypto'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises'
import path from 'node:path'
const exec = promisify(execFile)
const args = process.argv.slice(2)
const opt = (k, d = '') => args.includes(k) ? args[args.indexOf(k) + 1] : d
const action = opt('--action', 'observe')
const base = opt('--api-url', process.env.SWARM_RUNNER_API_URL || '').replace(/\/$/, '')
const duration = Number(opt('--timeout-ms', '540000'))
const check = (ok, msg) => { if (!ok) throw Error(msg) }
check(['init', 'observe'].includes(action) && /^https?:\/\//.test(base), 'Require init|observe and api-url')
check(duration >= 30000 && duration <= 600000, 'Stage bound must be 30–600 seconds')
const decode = x => { if (typeof x === 'object' && x) return x; try { return JSON.parse(x || '{}') } catch { return {} } }
const headers = { Origin: base, Referer: base + '/app', 'Sec-Fetch-Site': 'same-origin' }
async function api(method, route, body) {
  const r = await fetch(base + route, { method, headers: { ...headers, ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(20000) })
  const data = decode(await r.text()); check(r.ok, `${method} ${route}: ${r.status} ${String(data.error || data.message || '').slice(0, 500)}`); return data
}
const auth = await api('GET', '/v1/auth/desktop/session')
check(auth.token, 'Desktop authentication unavailable')
headers['X-Swarm-Token'] = auth.token; headers.Cookie = `swarm_desktop_session=${auth.token}`
const git = async (repo, ...a) => (await exec('git', ['-C', repo, ...a], { timeout: 20000, maxBuffer: 1024 * 1024 })).stdout.trim()
let evidencePath = opt('--evidence')
let e
const save = () => writeFile(evidencePath, JSON.stringify(e, null, 2), { mode: 0o600 })
const hydrate = sid => api('POST', '/v3/sync/hydrate', { surface: 'desktop', session_ids: [sid], history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' }, resources: { messages: true, events: true, run_intents: true, session_view: true, active_plan: true, current_run_state: true }, include_active: true })
function taskOutputs(snapshot) {
  return (snapshot.messages_by_session?.[e.parent] || []).filter(m => m.role === 'tool').map(m => decode(m.content)).filter(m => (m.tool_name || m.tool) === 'task').map(m => decode(m.output || m.completed_output))
}
function finder(id, stage, target, scope, prompt, dependencies = []) {
  return { id, stage_id: stage, agent_type: 'finder', title: 'Committed Evidence Reader', workspace_path: target, owned_scope: scope, depends_on: dependencies, meta_prompt: prompt, deliverable: 'Exact evidence with source identity', dependency_evidence: 'Committed source or integrated declared dependency is available.', acceptance_criteria: ['Quotes exact evidence, not invented values.'] }
}
function standardProgram(n, target, research) {
  const file = `result-${n}.txt`
  return { id: `${e.id}_attached_${n}`, stages: [{ id: 'research', dependency_evidence: 'Committed catalog exists.' }, { id: 'write', depends_on: ['research'], dependency_evidence: 'Exact Finder report is ready.' }], jobs: [
    finder('research', 'research', research, ['catalog.md'], 'Read catalog.md. Quote the exact heading and subtitle, with source evidence. These values are required by the dependent Coder.'),
    { id: 'write', stage_id: 'write', agent_type: 'coder', title: 'Scoped Evidence Consumer', workspace_path: target, owned_scope: [file], depends_on: ['research'], meta_prompt: `Consume the exact authenticated Finder dependency report. Create ${file} with exactly the researched heading followed by a newline and CHECKPOINT_${n} followed by a newline. Commit only this file and finish clean.`, deliverable: `Clean committed ${file}`, dependency_evidence: 'Finder supplies the exact heading.', acceptance_criteria: ['Exact heading and checkpoint marker committed within scope.'] }
  ] }
}
try {
  if (action === 'init') {
    check(process.env.TMPDIR, 'TMPDIR required'); check(/^[a-f0-9]{40}$/.test(opt('--candidate')), 'Exact verified candidate required')
    const root = await mkdtemp(path.join(process.env.TMPDIR, 'ten-programs-'))
    e = { id: `ten_${crypto.randomBytes(5).toString('hex')}`, candidate: opt('--candidate'), root, programs: {}, checkpoints: {}, result: 'NOT_DONE', started_at: new Date().toISOString() }
    evidencePath = path.join(root, 'evidence.json')
    const marker = `OBSERVED_${crypto.randomBytes(8).toString('hex')}`
    e.marker = marker; e.repositories = []
    for (const name of ['source', 'research']) {
      const repo = path.join(root, name); await mkdir(repo)
      await exec('git', ['init', '-b', 'dev', repo]); await git(repo, 'config', 'user.name', 'Swarm Testbench'); await git(repo, 'config', 'user.email', 'testbench@example.invalid')
      await writeFile(path.join(repo, 'catalog.md'), `# Committed catalog\nHeading: ${marker}\nSubtitle: Committed inputs, visible outcomes.\n`)
      await git(repo, 'add', 'catalog.md'); await git(repo, 'commit', '-m', 'test: seed checkpoint evidence')
      const b = await api('POST', '/v1/workspace/add', { path: repo, name: `${e.id}-${name}`, make_current: false })
      e.repositories.push({ path: repo, head: await git(repo, 'rev-parse', 'HEAD'), binding: b.local_workspace_binding_id })
    }
    const [source, research] = e.repositories
    await api('POST', '/v1/workspace/directories/add', { workspace_path: source.path, directory_path: research.path })
    const settings = (await api('GET', '/v1/agent-model-settings')).agent_model_settings
    const topology = await api('GET', '/v1/swarm/topology')
    const runtime = topology.runtimes.find(x => x.relationship === 'self')
    const created = await api('POST', '/v3/sessions', { client_request_id: e.id, title: `${e.id} ten checkpoint acceptance`, workspace_path: source.path, workspace_binding_id: source.binding, swarm_id: runtime.swarm_id, target_kind: 'host', target_relationship: 'self', mode: 'auto', agent_name: 'swarm', preference: settings.swarm.action, worktree_mode: 'on', worktree_base_branch: 'dev', worktree_branch_name: `agent/${e.id}`, metadata: { runner_test: 'task-program-ten-checkpoints', runner_test_id: e.id } })
    e.parent = created.session.id; e.parent_root = created.session.worktree_root_path
    check(created.session.worktree_enabled && e.parent_root !== source.path, 'Missing parent isolation')
    const checkpoints = []
    for (let n = 1; n <= 10; n++) {
      const target = n % 2 ? source.path : research.path
      const program = standardProgram(n, target, research.path)
      if (n === 1) {
        program.stages.splice(1, 0, { id: 'concepts', depends_on: ['research'], dependency_evidence: 'Both requested alternatives need the exact report.' })
        program.stages.find(s => s.id === 'write').depends_on = ['concepts']
        for (let i = 1; i <= 2; i++) program.jobs.push({ id: `concept-${i}`, stage_id: 'concepts', agent_type: 'designer', title: 'Requested Concept Alternative', output_mode: 'managed', depends_on: ['research'], meta_prompt: `Create alternative ${i} of two explicitly requested static HTML concept cards. Use the exact heading and subtitle from the Finder report. ${i === 1 ? 'Cream background and dark editorial typography.' : 'Navy background and mint technical typography.'} Fit all content legibly in 960x540. No images, animation, network or extra claims. Use semantic main with stable id. Finish a validated native V3 project.`, deliverable: 'One exact native HTML alternative', dependency_evidence: 'Finder report ready.', acceptance_criteria: ['Exact heading and subtitle in ready HTML.'] })
        const writer = program.jobs.find(j => j.id === 'write')
        writer.depends_on.push('concept-1'); writer.meta_prompt += ' Also verify the exact heading occurs in the authenticated first Designer source before writing. Do not select or merge the second alternative.'
        program.stages.push({ id: 'audit', depends_on: ['write'], dependency_evidence: 'Coder result integrated.' })
        program.jobs.push(finder('audit', 'audit', source.path, ['result-1.txt'], 'Use the authenticated committed Coder dependency evidence to quote both lines of result-1.txt and its commit identity. Do not read a stale captured checkout.', ['write']))
      }
      program.jobs.sort((a, b) => program.stages.findIndex(s => s.id === a.stage_id) - program.stages.findIndex(s => s.id === b.stage_id))
      const tasks = ['Call task action=start without prompt or program to load this exact approved task_program; do not reconstruct or alter it.', 'Verify the task result is completed, then complete this checkpoint through plan_manage. Do not launch undeclared workers or manually start another checkpoint.']
      if ([3, 7].includes(n)) {
        const inline = { id: `${e.id}_inline_${n}`, stages: [{ id: 'inspect', dependency_evidence: 'Catalog is committed.' }], jobs: [finder('inspect', 'inspect', research.path, ['catalog.md'], `Read catalog.md and quote both exact strings. This additional inline proof is interleaved at checkpoint ${n}.`)] }
        tasks.splice(1, 0, `After the attached program completes and before completing this checkpoint, start exactly this additional inline Task Program with a nonempty top-level prompt and its complete program object: ${JSON.stringify(inline)}`)
        e.programs[inline.id] = { kind: 'inline', checkpoint: n, definition: inline }
      }
      e.programs[program.id] = { kind: 'attached', checkpoint: n, target, file: `result-${n}.txt`, definition: program }
      checkpoints.push({ id: `cp-${n}`, title: `Checkpoint ${n}: exact program handoff`, status: 'pending', order: n, tasks, acceptance_criteria: [`Attached program ${program.id} is completed with exact committed output.`, ...([3, 7].includes(n) ? ['The additional inline program completes before this checkpoint.'] : [])], task_program: program })
    }
    e.plan_id = `${e.id}_plan`
    const document = { title: `${e.id} ten attached and two inline programs`, info: { goal: 'Execute ten ordered checkpoints with attached Task Programs, mixed dependencies, two repository targets and interleaved inline programs.' }, execution_policy: { mode: 'automatic', shape: 'checkpointed' }, active_checkpoint_id: 'cp-1', checkpoints }
    await save()
    await api('POST', `/v3/sessions/${e.parent}/plans`, { plan_id: e.plan_id, title: document.title, document, status: 'approved', approval_state: 'approved', activate: true })
    await api('POST', `/v3/sessions/${e.parent}/plan-mode/checkpoints/cp-1/start`, { plan_id: e.plan_id })
    console.log(JSON.stringify({ parent: e.parent, evidence_path: evidencePath, candidate: e.candidate }))
  } else {
    e = JSON.parse(await readFile(evidencePath, 'utf8'))
    const n = Number(opt('--checkpoint')); check(n >= 1 && n <= 10, 'checkpoint 1–10 required')
    const deadline = Date.now() + duration; let nextBeat = 0
    while (Date.now() < deadline) {
      const snapshot = await hydrate(e.parent)
      const active = await api('GET', `/v3/sessions/${e.parent}/plans/active`)
      const doc = (active.plan || active).document
      check(doc?.checkpoints?.length === 10, 'Canonical ten-checkpoint plan unavailable')
      const cp = doc.checkpoints.find(c => c.id === `cp-${n}`)
      for (const o of taskOutputs(snapshot)) if (e.programs[o.program_id]) e.programs[o.program_id].output = o
      const bootstrap = await api('POST', '/v3/sync/bootstrap', { surface: 'desktop', selector: { kind: 'global', global: true, recent: { limit: 100 } }, history: { mode: 'none' }, resources: { current_run_state: true }, include_active: true })
      const children = Object.values(bootstrap.sessions_by_id || {}).filter(s => s.metadata?.parent_session_id === e.parent)
      e.children = Object.fromEntries([...Object.entries(e.children || {}), ...children.map(s => [s.id, { id: s.id, worktree: s.worktree_root_path, branch: s.worktree_branch, metadata: s.metadata }])])
      for (const sid of [e.parent, ...children.map(c => c.id)]) {
        const pending = (await api('GET', `/v3/sessions/${sid}/permissions?status=pending&limit=20`)).permissions || []
        for (const p of pending) {
          check(sid === e.parent && p.tool_name === 'task', `Unexpected permission ${p.tool_name} on ${sid}; preserve for scoped review`)
          const call = decode(p.tool_call_arguments || p.tool_arguments)
          check(call.action === 'start' && !call.launches, 'Unexpected task operation')
          if (call.program) {
            const declared = e.programs[call.program.id]?.definition
            const canonical = x => JSON.stringify(x, (k, v) => v && !Array.isArray(v) && typeof v === 'object' ? Object.fromEntries(Object.entries(v).sort(([a], [b]) => a.localeCompare(b))) : v)
            check(declared && canonical(call.program) === canonical(declared), 'Task permission changed the declared program')
          } else check(doc.checkpoints.some(c => c.id === doc.active_checkpoint_id && c.task_program), 'No active attached program')
          await api('POST', `/v3/sessions/${sid}/permissions/${p.id}/resolve`, { action: 'allow_once', reason: `${e.id}: approved bounded declared program fixture` })
        }
      }
      const expected = Object.values(e.programs).filter(p => p.checkpoint === n)
      check(!expected.some(p => ['blocked', 'failed'].includes(p.output?.program_state)), 'Program failure; preserve completed children and exact evidence')
      check(!['blocked', 'failed'].includes(cp.status), `Checkpoint ${cp.status}; inspect retained evidence`)
      if (Date.now() >= nextBeat) { console.error(JSON.stringify({ checkpoint: cp.id, status: cp.status, active: doc.active_checkpoint_id, children: Object.keys(e.children).length, programs: expected.map(p => p.output?.program_state || 'pending') })); nextBeat = Date.now() + 15000; await save() }
      if (cp.status === 'completed') {
        check(expected.every(p => p.output?.program_state === 'completed'), 'Checkpoint completed without every required completed program')
        for (const p of expected) {
          check(p.output.jobs.length === p.definition.jobs.length && p.output.jobs.every(j => ['completed', 'integrated'].includes(j.state)), 'Missing successful job handoff')
          if (p.kind === 'attached') {
            const job = p.output.jobs.find(j => j.job_id === 'write')
            const child = e.children[job.child_session_id]; check(child?.worktree, 'Missing Coder worktree identity')
            check(await git(child.worktree, 'status', '--porcelain') === '', 'Dirty completed child')
            check(await readFile(path.join(child.worktree, p.file), 'utf8') === `${e.marker}\nCHECKPOINT_${n}\n`, 'Wrong committed dependency output')
            p.child_head = await git(child.worktree, 'rev-parse', 'HEAD')
          }
        }
        for (const repo of e.repositories) { check(await git(repo.path, 'rev-parse', 'HEAD') === repo.head, 'Captured HEAD changed'); check(await git(repo.path, 'status', '--porcelain') === '', 'Captured source dirty') }
        e.checkpoints[cp.id] = { status: cp.status, attempt_id: cp.attempt_id, run_id: cp.run_id, observed_at: new Date().toISOString() }
        await writeFile(path.join(e.root, `checkpoint-${n}.json`), JSON.stringify({ snapshot, active }), { mode: 0o600 })
        if (n === 10) { check(Object.keys(e.checkpoints).length === 10, 'Earlier checkpoint proofs missing'); e.result = 'STRUCTURAL_PASS_PIXELS_AND_INTEGRATION_AUDIT_PENDING'; e.artifacts = await api('GET', `/v3/sessions/${e.parent}/artifacts-v3`) }
        await save(); console.log(JSON.stringify({ checkpoint: n, result: 'OBSERVED', evidence_path: evidencePath })); break
      }
      await new Promise(r => setTimeout(r, 1500))
    }
    check(e.checkpoints[`cp-${n}`], 'Bounded checkpoint deadline exceeded; resume observation, never replay')
  }
} catch (error) {
  if (e) { e.last_error = String(error.stack || error); await save() }
  console.error(String(error.stack || error)); process.exitCode = 2
}
