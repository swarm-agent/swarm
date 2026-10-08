// Invoked only by TestProjectGitSubscriptionsBoard with an authenticated,
// temporary Go HTTP/Git fixture. This is the real board runtime and transport,
// not a synthetic session.worktree.updated producer or a benchmark.
import assert from 'node:assert/strict'
import { setTimeout as delay } from 'node:timers/promises'
import { DesktopProjectsRuntime } from './desktop-projects'
import { reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'
import { taskCardFacts } from '../orchestrate/task-card-summary'

async function main() {
  const base = process.env.SWARM_GIT_FIXTURE_URL!
  const isolation = process.env.SWARM_GIT_FIXTURE_ISOLATION === '1'
  assert.ok(base?.startsWith('http://127.0.0.1:'))
  const nativeFetch = globalThis.fetch
  let detailReads = 0
  let streamStarts = 0
  let streamAborts = 0
  const responses: Array<{ controller: AbortController; signal: AbortSignal }> = []
  globalThis.fetch = async (input, init) => {
    const url = new URL(String(input), base)
    if (url.pathname.endsWith('/subscriptions')) {
      streamStarts++
      const controller = new AbortController()
      const signal = init!.signal!
      signal.addEventListener('abort', () => { streamAborts++; controller.abort() }, { once: true })
      responses.push({ controller, signal })
      return nativeFetch(url, { ...init, signal: controller.signal })
    }
    if (/\/tasks\/(integrated|candidate)$/.test(url.pathname)) detailReads++
    return nativeFetch(url, init)
  }
  let state: DesktopProjectsState = {}
  let hold: { promise: Promise<void>; release: () => void } | undefined
  let held = false
  let releaseHeld: (() => void) | undefined
  const publications: DesktopProjectsState[] = []
  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action); publications.push(state) },
    subscribe: () => () => {}, // No task/session activity or Git dialog mounted.
    fetchMedia: async () => ({ media: [] }),
    fetchTask: async (project, task) => {
      const response = await fetch(`/v3/projects/${project}/tasks/${task}`)
      assert.equal(response.status, 200)
      const value = await response.json()
      if (hold && task === 'candidate') { const pending = hold; hold = undefined; held = true; await pending.promise }
      return value
    },
  })
  const wait = async (predicate: () => boolean) => {
    const end = Date.now() + 10_000
    while (!predicate()) { assert.ok(Date.now() < end, 'condition timed out'); await delay(10) }
  }
  const task = (id: string) => state.project?.tasks.find(task => task.id === id)
  const observed = (id: string, expected: string) => task(id)?.deliveryAssessment?.state === expected &&
    task(id)?.deliveryAssessment?.freshness === 'observed' && task(id)?.gitStatus !== 'stale'
  const mutate = async (action: string) => {
    const response = await nativeFetch(`${base}/fixture?action=${action}`)
    assert.equal(response.status, 200)
    return response.json() as Promise<{ source: string; target: string; count: number }>
  }
  const settle = async () => { await delay(600) }
  const lease = runtime.acquire('project')
  try {
    await lease.ready
    await wait(() => observed('integrated', 'integrated') && observed('candidate', 'candidate_work'))
    const assertDeleted = () => {
      if (!isolation) return
      assert.equal(task('deleted')?.gitStatus, 'stale')
      assert.match(task('deleted')?.syncWarning ?? '', /selector is missing, stale, or not authorized|repository is unavailable/)
      assert.deepEqual(task('deleted')?.deliveryAssessment?.allowed_actions ?? [], [])
    }
    if (isolation) await wait(() => task('deleted')?.gitStatus === 'stale' && !!task('deleted')?.syncWarning)
    assertDeleted()
    assert.equal(taskCardFacts(task('integrated')!).git, 'Integrated')
    assert.notEqual(task('candidate')!.deliveryAssessment!.reason_code, 'not_assessed')
    assert.ok(publications.some(snapshot => snapshot.project?.tasks.some(task => task.deliveryAssessment?.reason_code === 'not_assessed')), 'first-load placeholder was hydrated, not relabeled')
    assert.equal(streamStarts, 1, 'board owns one deduplicated repository stream')
    const duplicate = runtime.acquire('project'); await duplicate.ready; duplicate.release()
    assert.equal(streamStarts, 1)

    // Requirement 1: With an already hydrated board, repeatedly select/expand/switch
    // tasks (100 interactions) and assert ZERO additional Git assessment requests,
    // ZERO additional Git commands, and NO subscription churn.
    const preInteractionsReads = detailReads
    const preInteractionsGitCount = (await mutate('count')).count
    for (let i = 0; i < 100; i++) {
      const targetId = i % 2 === 0 ? 'integrated' : 'candidate'
      runtime.inspectTask('project', targetId)
    }
    await delay(50)
    assert.equal(detailReads, preInteractionsReads, '100 task interactions trigger zero additional Git assessment requests')
    assert.equal((await mutate('count')).count, preInteractionsGitCount, '100 task interactions trigger zero additional Git commands')
    assert.equal(streamStarts, 1, '100 task interactions cause zero subscription churn')

    await settle()
    const reads = detailReads
    const before = await mutate('count')
    await delay(65_000) // Exceeds the compatibility realtime reconciler's 30–60s interval.
    assert.equal(detailReads, reads, 'zero periodic task assessments')
    assert.equal((await mutate('count')).count, before.count, 'zero periodic Git commands')
    assert.equal(streamStarts, 1, 'failed selectors cannot recreate the healthy transport')
    assertDeleted()

    const t0 = performance.now()
    let oids = await mutate('source')
    await wait(() => observed('candidate', 'candidate_work') && task('candidate')!.deliveryAssessment!.source_oid === oids.source)
    const sourceLatency = Math.round(performance.now() - t0)
    console.error(`[telemetry] external source change observed in ${sourceLatency}ms`)
    assert.equal(task('candidate')!.deliveryAssessment!.target_oid, oids.target)
    assertDeleted()
    const t1 = performance.now()
    oids = await mutate('integrate')
    await wait(() => observed('candidate', 'integrated') && task('candidate')!.deliveryAssessment!.target_oid === oids.target)
    const integrateLatency = Math.round(performance.now() - t1)
    console.error(`[telemetry] external target integrate observed in ${integrateLatency}ms`)
    assertDeleted()
    const t2 = performance.now()
    oids = await mutate('reset')
    await wait(() => observed('candidate', 'candidate_work') && task('candidate')!.deliveryAssessment!.target_oid === oids.target)
    const resetLatency = Math.round(performance.now() - t2)
    console.error(`[telemetry] external target reset observed in ${resetLatency}ms`)
    await mutate('dirty-source')
    await wait(() => observed('candidate', 'candidate_work') && !!task('candidate')!.deliveryAssessment!.source_dirty)
    assert.equal(task('candidate')!.deliveryAssessment!.target_dirty, false)
    await mutate('dirty-target')
    await wait(() => observed('candidate', 'candidate_work') && !!task('candidate')!.deliveryAssessment!.target_dirty)
    assert.equal(task('candidate')!.deliveryAssessment!.source_oid, oids.source)
    assert.equal(task('candidate')!.deliveryAssessment!.target_oid, oids.target)
    assert.deepEqual(task('candidate')!.deliveryAssessment!.allowed_actions, [])
    assert.equal(task('integrated')!.deliveryAssessment!.state, 'integrated', 'target dirtiness does not erase ancestry')
    await mutate('clean')
    await wait(() => observed('candidate', 'candidate_work') && !task('candidate')!.deliveryAssessment!.source_dirty && !task('candidate')!.deliveryAssessment!.target_dirty)

    // Hold a real earlier assessment while external integration invalidates it.
    let release!: () => void
    hold = { promise: new Promise<void>(resolve => { release = resolve }), release: () => release() }
    releaseHeld = () => release()
    const heldStart = detailReads
    await mutate('burst')
    await wait(() => held)
    oids = await mutate('integrate')
    await delay(400) // allow native debounce to fence the held result
    const publishStart = publications.length
    release()
    await wait(() => observed('candidate', 'integrated') && task('candidate')!.deliveryAssessment!.target_oid === oids.target)
    assert.ok(detailReads - heldStart <= 4, 'burst coalesces, including shared target consumers')
    for (const publication of publications.slice(publishStart)) {
      const candidate = publication.project?.tasks.find(task => task.id === 'candidate')
      assert.ok(candidate?.gitStatus === 'stale' || candidate?.deliveryAssessment?.state === 'integrated', 'stale in-flight candidate result was published')
    }

    // Transport loss uses the real reader/reconnect path; source/session unchanged.
    responses.at(-1)!.controller.abort()
    await wait(() => !!task('candidate')?.syncWarning)
    await mutate('reset')
    await wait(() => streamStarts === 2 && observed('candidate', 'candidate_work'))
    assert.equal(task('candidate')!.syncWarning, undefined)

    const repositories = [{ workspace_path: task('candidate')!.workspacePath, session_id: 'candidate', branch: 'agent/candidate' }]
    const foreignController = new AbortController()
    const foreign = await nativeFetch(`${base}/v1/workspace/git/subscriptions`, {
      method: 'POST', signal: foreignController.signal, headers: { 'Content-Type': 'application/json', 'X-Fixture-Foreign': '1' }, body: JSON.stringify({ repositories }),
    })
    try {
      assert.equal(foreign.status, 200, 'selector rejection is scoped even when every selector fails')
      const reader = foreign.body!.getReader()
      let frame = ''
      while (!frame.includes('\n\n')) frame += new TextDecoder().decode((await reader.read()).value)
      const notice = JSON.parse(frame.split('\n\n')[0].slice(6))
      assert.deepEqual(notice, { index: 0, kind: 'lost', reason_code: 'selector_unavailable',
        error: 'Git repository selector is missing, stale, or not authorized; retry on reconnect' })
      await reader.cancel()
    } finally { foreignController.abort() }
    assertDeleted()
    if (isolation) {
      await mutate('restore')
      // Restoring the path alone does not invent knowledge. A supported explicit
      // reconnect repairs setup, after which ready triggers a new assessment.
      assertDeleted()
      runtime.acceptFrame({ kind: 'rehydrate.required' })
      await wait(() => observed('deleted', 'candidate_work'))
      assert.equal(task('deleted')!.syncWarning, undefined)
      await wait(() => observed('integrated', 'integrated'))
      assert.equal(streamStarts, 3)
      // Failure after ready/SSE must release only this lane, not terminate the
      // shared stream or keep the now-unwatched observation fresh.
      await mutate('prune')
      await wait(() => task('deleted')?.gitStatus === 'stale' && !!task('deleted')?.syncWarning)
      assertDeleted()
      oids = await mutate('source')
      await wait(() => observed('candidate', 'candidate_work') && task('candidate')!.deliveryAssessment!.source_oid === oids.source)
      assert.equal(streamStarts, 3, 'post-header selector loss kept healthy stream live')
      assertDeleted()
    }
  } finally {
    releaseHeld?.()
    hold?.release()
    lease.release()
    runtime.reset()
    await settle()
    globalThis.fetch = nativeFetch
  }
  assert.ok(responses.every(response => response.signal.aborted), 'all board-owned streams released')
  assert.ok(streamAborts >= streamStarts)
}
main().catch(error => { console.error(error); process.exitCode = 1 })
