// Purpose: MediaTaskCard maps durable candidates into one request turn, preserving
// exact output actions and unavailable-preview guards. mediaTaskThreads must use
// parent IDs rather than shared titles. Adapter/SSR assertions are the narrowest
// layer here; actual DOM identity and keyboard actions are tested in Chromium.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MediaTaskCard, MediaTaskThreads, mediaTaskThreads } from './media-task-card'
import { DesktopProjectsRuntime } from '../runtime/desktop-projects'
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import { taskWithCurrentSessions } from './task-card-sessions'
import { mapBackendTask, reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'
import { aggregateTaskLiveState } from './orchestrate-task-helpers'
import { CreativeThreadCard, type CreativeCardTurn } from './creative-thread-card'
import { isMediaGenerationPending } from '../tools/media-library/media-iteration-thread'

function buttons(node: React.ReactNode): React.ReactElement<any>[] {
  if (!React.isValidElement(node)) return []
  const props = node.props as { children?: React.ReactNode }
  return [...(node.type === 'button' ? [node] : []), ...React.Children.toArray(props.children).flatMap(buttons)]
}

test('ten candidates occupy one turn with exact ready image actions', () => {
  const task = mapBackendTask({ id: 'images', agent: 'image', status: 'in_progress', variant_count: 10,
    deliverables: Array.from({ length: 10 }, (_, i) => ({ id: `slot-${i}`, kind: 'image', title: `Image ${i}`,
      status: i === 0 ? 'ready' : i === 1 ? 'failed' : i < 5 ? 'generating' : 'queued',
      media_url: i === 0 ? 'data:image/png;base64,selected' : undefined })) })
  const calls: unknown[] = []
  const tree = MediaTaskCard({ task, onPreview: (output, mode) => calls.push([output.id, mode]) })!
  const html = renderToStaticMarkup(tree)
  assert.equal((html.match(/role="tab"/g) || []).length, 1)
  assert.equal((html.match(/class="creative-candidate-chip"/g) || []).length, 10)
  assert.match(html, /1 turn · 1 ready/)
  assert.doesNotMatch(html, /Reopen|Integrate|Plan, subtasks|Git/)
  const turns = tree.props.turns as CreativeCardTurn[]
  assert.equal(turns[0].outputs.length, 10)
  const edit = buttons(turns[0].outputs[0].actions).find(button => button.props.children === 'Edit')!
  edit.props.onClick()
  turns[0].outputs[0].open()
  assert.deepEqual(calls, [['slot-0', 'fine_tune'], ['slot-0', undefined]])
  assert.equal(turns[0].outputs.filter(output => output.ready).length, 1)
})

test('ready without a hydrated preview stays ready but cannot invoke UI actions', () => {
  const task = mapBackendTask({ id: 'images', agent: 'image', deliverables: [{ id: 'one', kind: 'image', title: 'Image', status: 'ready' }] })
  const tree = MediaTaskCard({ task })!
  const html = renderToStaticMarkup(tree)
  assert.match(html, /Preview loading/)
  assert.match(html, /1 turn · 1 ready/)
  const output = (tree.props.turns as CreativeCardTurn[])[0].outputs[0]
  assert.equal(output.ready, false)
  assert.equal(buttons(output.actions).find(button => button.props.children === 'Edit')!.props.disabled, true)
})

test('task grouping follows exact output parents and excludes code tasks', () => {
  const root = mapBackendTask({ id: 'root', title: 'Same title', agent: 'image', deliverables: [{ id: 'a', kind: 'image', status: 'ready' }] })
  const child = { ...root, id: 'child', deliverables: [{ ...root.deliverables![0], id: 'b', parentDeliverableId: 'a' }] }
  const independent = { ...root, id: 'independent', deliverables: [{ ...root.deliverables![0], id: 'c' }] }
  const code = { ...root, id: 'code', agentType: 'coder' }
  assert.deepEqual(mediaTaskThreads([child, independent, root, code]).map(thread => ({ id: thread.id, turns: thread.turns.map(task => task.id) })), [
    { id: 'root', turns: ['root', 'child'] }, { id: 'independent', turns: ['independent'] },
  ])
})

// Purpose: replacing card presentation must retain the owner's approval/archive/
// delete callbacks and permission surface, never turn an edit into UI-only status.
// Direct adapter closure assertions prove routing without mutating backend state.
test('card preserves attention and invokes only the explicitly selected task action', () => {
  const task = mapBackendTask({ id: 'pending', agent: 'video', status: 'pending_approval', deliverables: [] })
  const calls: string[] = []
  const tree = MediaTaskCard({ task, attention: <section aria-label="Task needs your attention">Review permission</section>,
    onApprove: () => calls.push('approve:pending'), onArchive: () => calls.push('archive:pending'), onDelete: () => calls.push('delete:pending') })!
  assert.match(renderToStaticMarkup(tree), /Task needs your attention/)
  assert.deepEqual(calls, [])
  const controls = buttons((tree.props.turns as CreativeCardTurn[])[0].controls)
  for (const name of ['Generate media', 'Archive', 'Delete']) controls.find(button => button.props.children === name)!.props.onClick()
  assert.deepEqual(calls, ['approve:pending', 'archive:pending', 'delete:pending'])
  assert.equal(task.status, 'pending_approval')
  const approving = MediaTaskCard({ task, isApproving: true, onApprove: () => calls.push('duplicate') })!
  assert.equal(buttons((approving.props.turns as CreativeCardTurn[])[0].controls)[0].props.disabled, true)
  const failed = MediaTaskCard({ task: { ...task, status: 'failed' }, onApprove: () => calls.push('unauthorized-retry') })!
  assert.equal(buttons((failed.props.turns as CreativeCardTurn[])[0].controls).length, 0)
  assert.deepEqual(calls, ['approve:pending', 'archive:pending', 'delete:pending'])
})

// Purpose: accepted direct video work has no chat session; missing output must not
// become Failed in Turn 1. Exercise the production mapper, session aggregator,
// revision-aware cache and card adapter together: the narrowest layer proving
// queued/running, completion, explicit failure and stale-response presentation.
test('direct video turn preserves durable generation lifecycle through reload and stale updates', () => {
  const pending = mapBackendTask({ id: 'video-task', agent: 'video', title: 'Video', revision: 1, status: 'in_progress' })
  const turn = (task: typeof pending) => (MediaTaskCard({ task: aggregateTaskLiveState(task, {}) })!.props.turns as CreativeCardTurn[])[0]
  assert.equal(turn(pending).status, 'running')
  assert.equal(turn(pending).outputs.length, 0)
  for (const status of ['pending', 'queued', 'in_progress', 'running']) {
    const task = mapBackendTask({ id: pending.id, agent: 'video', status, deliverables: [{ id: 'clip', kind: 'video', status: 'generating' }] })
    assert.equal(turn(task).status, status === 'in_progress' ? 'running' : status)
    assert.equal(turn(task).outputs[0].ready, false)
  }
  const ready = mapBackendTask({ id: pending.id, agent: 'video', revision: 2, status: 'needs_review', deliverables: [{ id: 'clip', kind: 'video', status: 'ready', media_url: 'data:video/mp4;base64,fixture' }] })
  let state: DesktopProjectsState = { project: { projectId: 'project', tasks: [pending], media: [], loading: false, stale: false, generation: 1 } }
  state = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'project', tasks: [ready] })
  state = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'project', tasks: [pending] })
  const reloaded = JSON.parse(JSON.stringify(state.project.tasks[0])) as typeof pending
  assert.equal(turn(reloaded).id, pending.id)
  assert.equal(turn(reloaded).status, 'needs_review')
  assert.equal(turn(reloaded).outputs[0].ready, true)
  const failed = mapBackendTask({ id: pending.id, agent: 'video', status: 'failed', last_error: 'Provider rejected generation' })
  assert.equal(turn(failed).status, 'failed')
  assert.match(renderToStaticMarkup(MediaTaskCard({ task: aggregateTaskLiveState(failed, {}) })!), /Provider rejected generation/)
  const rejected = { ...pending, status: 'rejected' as const }
  assert.equal(turn(rejected).status, 'rejected')
  // Session-backed tasks must still use explicit run failure evidence.
  assert.equal(aggregateTaskLiveState({ ...pending, sessionId: 'execution' }, { execution: { intent: { status: 'failed' } } }).status, 'failed')
})

// Purpose: the board's actual Turn 1 markup must stay nonterminal while a direct
// video task has no session or playable artifact. The previous test inspected
// adapter props only and expected the wrong post-map in_progress spelling.
// Authority: DesktopProjectsRuntime -> taskWithCurrentSessions ->
// aggregateTaskLiveState -> MediaTaskThreads -> CreativeThreadCard. This hermetic
// runtime/SSR boundary checks accessible pending spinners and terminal labels,
// not browser pixels or live providers; presentation must not invent failure.
// Before the original guard, the initial in_progress payload maps to running and
// falls through aggregateTaskLiveState's missing-session branch to failed.
test('direct video Turn 1 markup follows API reloads and durable task events without false failure', { timeout: 10_000 }, async () => {
  const initial = { id: 'pending-video', project_id: 'video-project', title: 'Video', agent: 'video',
    status: 'in_progress', revision: 1, session_id: '', router_alert: 'Routing warning', deliverables: [] }
  let payload: any = initial
  let state: DesktopProjectsState = {}
  let detailError: Error | undefined
  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    fetchTasks: async () => ({ tasks: [JSON.parse(JSON.stringify(payload))] }),
    fetchMedia: async () => ({ media: [] }),
    fetchTask: async () => {
      if (detailError) throw detailError
      return { task: JSON.parse(JSON.stringify(payload)) }
    },
    subscribe: () => () => {},
  })
  const flush = async () => { for (let i = 0; i < 30; i++) await Promise.resolve() }
  const render = () => {
    const tasks = state['video-project'].tasks.map(task => aggregateTaskLiveState(
      taskWithCurrentSessions(task, createEmptyDesktopV3CacheState()), {}))
    return renderToStaticMarkup(<MediaTaskThreads tasks={tasks} visibleTaskIds={new Set([initial.id])} actions={() => ({})} />)
  }
  const assertTurn = (status: string) => {
    const html = render()
    assert.equal((html.match(/role="tab"/g) || []).length, 1)
    assert.match(html, /data-task-id="pending-video"/)
    if (isMediaGenerationPending(status)) {
      assert.match(html, /<strong>Turn 1<\/strong><\/span>/)
      assert.ok(html.includes(`role="status" aria-label="Turn 1: ${status}"`), html)
      assert.match(html, /lucide-loader-circle/)
    } else {
      assert.ok(html.includes(`<strong>Turn 1</strong><span>${status.replace(/_/g, ' ')}</span>`), html)
      assert.doesNotMatch(html, /aria-label="Turn 1:/)
    }
    if (status !== 'failed') assert.doesNotMatch(html, />failed</i)
    return html
  }
  const emit = async () => {
    runtime.acceptFrame({ kind: 'project.updated', project_id: initial.project_id, event: { payload: {
      project_id: initial.project_id, task_id: initial.id, action: 'task_updated', revision: payload.revision,
    } } })
    await flush()
  }
  let lease = runtime.acquire(initial.project_id)
  try {
    await lease.ready
    assert.doesNotMatch(assertTurn('running'), /<video/)
    for (const status of ['pending', 'queued', 'in_progress', 'running']) {
      payload = { ...payload, revision: payload.revision + 1, status,
        deliverables: [{ id: 'clip', kind: 'video', title: 'Clip', status: 'generating', thumbnail: 'video' }] }
      await emit()
      assert.doesNotMatch(assertTurn(status === 'in_progress' ? 'running' : status), /<video/)
    }
    // A failed read is transport evidence, not a terminal generation result.
    detailError = new Error('Status transport unavailable')
    await emit()
    assertTurn('running')
    assert.equal(state[initial.project_id].tasks[0].syncWarning, detailError.message)
    detailError = undefined
    // Fresh acquisition represents a persisted API reload, not a JSON copy of UI state.
    lease.release()
    lease = runtime.acquire(initial.project_id)
    await lease.ready
    assertTurn('running')
    const pending = payload
    payload = { ...payload, revision: payload.revision + 1, status: 'needs_review',
      deliverables: [{ id: 'clip', kind: 'video', title: 'Clip', status: 'ready', media_url: '/media/clip.mp4' }] }
    await emit()
    assert.match(assertTurn('needs_review'), /<video src="\/media\/clip.mp4"/)
    const ready = payload
    for (const stale of [pending, { ...pending, status: 'failed', last_error: 'Stale failure' }]) {
      payload = stale
      await emit() // late event/read cannot replace a newer completed revision
      assert.match(assertTurn('needs_review'), /<video src="\/media\/clip.mp4"/)
      assert.doesNotMatch(render(), /Stale failure/)
    }
    payload = ready
    lease.release()
    lease = runtime.acquire(initial.project_id)
    await lease.ready
    assert.match(assertTurn('needs_review'), /<video src="\/media\/clip.mp4"/)
    // Independent failure branch starts with accepted work, not successful work.
    lease.release()
    payload = initial
    lease = runtime.acquire(initial.project_id)
    await lease.ready
    assertTurn('running')
    payload = { ...initial, revision: initial.revision + 1, status: 'failed', last_error: 'Provider rejected generation',
      deliverables: [{ id: 'clip', kind: 'video', status: 'failed', description: 'Provider rejected generation' }] }
    await emit()
    assert.match(assertTurn('failed'), /role="alert">Provider rejected generation/)
    for (const status of ['cancelled', 'rejected']) {
      payload = { ...initial, revision: payload.revision + 1, status }
      await emit()
      assertTurn(status)
    }
  } finally { lease.release() }
})

// Purpose: CreativeThreadCard owns loading presentation, not task lifecycle.
// SSR is the narrowest layer proving all pending states have accessible spinners
// while approval/terminal states and already-ready sibling previews stay intact.
// Empty/unready output wrappers must not take space beside the centered loader.
test('turn spinners replace pending text without hiding ready media or terminal states', () => {
  const render = (status: string, outputs: CreativeCardTurn['outputs'] = []) => renderToStaticMarkup(
    <CreativeThreadCard id="thread" title="Media" studio="image" turns={[{ id: 'turn', title: 'Media', status, outputs }]} />)
  const pendingOutput = { id: 'slot', title: 'Candidate', status: 'generating', ready: false, preview: null, open: () => {} }
  for (const status of ['requested', 'accepted', 'submitting', 'pending', 'queued', 'in_progress', 'running']) {
    for (const outputs of [[], [pendingOutput]]) {
      const html = render(status, outputs)
      assert.match(html, /<strong>Turn 1<\/strong><\/span>/)
      assert.ok(html.includes(`role="status" aria-label="Turn 1: ${status.replace(/_/g, ' ')}"`))
      assert.match(html, /motion-safe:animate-spin motion-reduce:animate-none/)
      assert.match(html, /aria-hidden="true"/)
      if (outputs.length) assert.match(html, /hidden="" class="creative-preview-output"/)
    }
  }
  for (const status of ['pending_approval', 'planning', 'completed', 'needs_review', 'failed', 'cancelled', 'interrupted', 'partial_failure', 'rejected']) {
    const html = render(status, [pendingOutput])
    assert.ok(html.includes(`<strong>Turn 1</strong><span>${status.replace(/_/g, ' ')}</span>`))
    assert.doesNotMatch(html, /aria-label="Turn 1:/)
  }
  const readyOutput = { ...pendingOutput, status: 'ready', ready: true, preview: <img src="/fixture.png" alt="Ready candidate" /> }
  const partial = render('running', [readyOutput, pendingOutput])
  assert.match(partial, /src="\/fixture.png"/)
  assert.doesNotMatch(partial, /aria-label="Turn 1:/)
})
