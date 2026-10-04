import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopProjectsRuntime } from './desktop-projects'
import { mapBackendTask, reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'
import { selectTaskPlanDocument } from '../orchestrate/orchestrate-plan-authority'
import { aggregateTaskLiveState, buildTaskAcceptancePayload } from '../orchestrate/orchestrate-task-helpers'
import { extractTaskSessionIds } from './desktop-projects-membership'
import { fetchProjectTaskCollection } from './project-task-collection'

const row = () => ({ id: 'task', revision: 2, session_id: 'owner', status: 'pending_approval', agent: 'plan',
  plan_binding: { plan_id: 'plan', session_id: 'owner', definition_revision: 3 },
  board_summary: { plan: { id: 'plan', session_id: 'owner', account_scope_id: 'account', version: 3,
    status: 'waiting_review', approval_state: 'pending', accepted_definition_receipt: '',
    document: { active_checkpoint_id: 'cp', checkpoints: [{ id: 'cp', status: 'pending' }] } },
    program: { program_id: 'program', state: 'running', jobs: [{ job_id: 'job', current_session_id: 'child', current_run_id: 'run', excluded_session_ids: ['old'] }] } },
})
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve() }

// Purpose: compact collection mapping must preserve review identity without
// manufacturing reviewable prose. Mapper/plan selector are the narrowest boundary
// preventing a stale or unrelated hydrated plan from becoming the accepted plan.
test('compact plan identity is separate from full review detail', () => {
  const task = mapBackendTask(row())
  assert.equal(task.detailLoaded, false)
  assert.equal(task.planDocument, undefined)
  assert.equal(task.taskProgramStatus, undefined)
  assert.deepEqual(buildTaskAcceptancePayload(task), { session_id: 'owner', plan_id: 'plan', definition_revision: 3 })
  const document = { checkpoints: [{ id: 'cp', title: 'Implement', tasks: ['Code'] }] }
  for (const plan of [{ id: 'other', session_id: 'owner', version: 3 }, { id: 'plan', session_id: 'other', version: 3 }, { id: 'plan', session_id: 'owner', version: 2 }]) {
    assert.equal(selectTaskPlanDocument(task, { ...plan, document }), undefined)
  }
  assert.equal(selectTaskPlanDocument(task, { id: 'plan', session_id: 'owner', version: 3, document }), document)
  assert.equal(selectTaskPlanDocument({ ...task, boardSummary: { ...task.boardSummary, plan_binding_stale: true } }, { document }), undefined)
  assert.equal(aggregateTaskLiveState({ ...task, status: 'planning' }, {}).status, 'pending_approval')
  assert.equal(aggregateTaskLiveState(task, {}).planDocument, undefined)
  assert.deepEqual(extractTaskSessionIds(task), ['child', 'owner'])
})

// Purpose: runtime collection/detail reducer must retain exact hydrated detail,
// but never carry it across changed review identity. Explicit inspection alone
// retrieves detail; collection failure must preserve rows without retry churn.
test('inspection hydrates detail; compact refresh retains it only for matching identity', async () => {
  let state: DesktopProjectsState = {}
  let current = row()
  let reads = 0
  let failure = false
  let lists = 0
  const document = { title: 'Full plan', checkpoints: [{ id: 'cp', title: 'Implement', tasks: ['Code'] }] }
  const runtime = new DesktopProjectsRuntime({ getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) }, subscribe: () => () => {},
    fetchTasks: async () => { lists++; if (failure) throw new Error('Task board is not ready. Use Retry.'); return { tasks: [current] } },
    fetchMedia: async () => ({ media: [] }),
    fetchTask: async () => { reads++; const { board_summary: _, ...detail } = current; return { task: { ...detail, plan_document: document, full_plan_markdown: 'Full prose', attempts: [{ id: 'past' }] } } },
  })
  const lease = runtime.acquire('project'); await lease.ready
  assert.equal(reads, 0)
  runtime.inspectTask('project', 'task'); await flush()
  assert.equal(reads, 1)
  await runtime.refresh('project', false)
  assert.equal(state.project.tasks[0].planDocument, document)
  assert.equal(state.project.tasks[0].fullPlanMarkdown, 'Full prose')
  assert.equal(state.project.tasks[0].attempts?.length, 1)
  failure = true
  const before = state.project.tasks
  await runtime.refresh('project', false); await flush()
  assert.equal(state.project.tasks, before)
  assert.match(state.project.error!, /not ready/)
  assert.equal(lists, 3)
  failure = false
  current = { ...current, revision: 3, plan_binding: { ...current.plan_binding, definition_revision: 4 } }
  await runtime.refresh('project', false)
  assert.equal(state.project.tasks[0].planDocument, undefined)
  assert.equal(state.project.tasks[0].detailLoaded, false)
  assert.equal(state.project.error, undefined)
  lease.release()
})

// Purpose: the collection HTTP boundary must expose migration 503/Retry-After
// instead of accepting partial rows or automatically polling. One fake transport
// response tests error semantics, not performance or live qualification.
test('migration response is visible and never interpreted as an empty board', async () => {
  const original = globalThis.fetch
  let calls = 0
  globalThis.fetch = async () => { calls++; return new Response('{}', { status: 503, headers: { 'Retry-After': '1' } }) }
  try {
    await assert.rejects(fetchProjectTaskCollection('project'), /not ready.*Retry/)
    assert.equal(calls, 1)
  } finally { globalThis.fetch = original }
})

// Purpose: aggregateTaskLiveState must not complete an unfinished accepted plan
// from a terminal program. Compact workflow fields can drive status but must not
// be published as a full document or synthesized checkpoint prose.
test('compact completed program retains unfinished-plan boundary', () => {
  const raw = row()
  raw.status = 'in_progress'
  raw.board_summary.plan.status = 'active'
  raw.board_summary.plan.approval_state = 'approved'
  raw.board_summary.program.state = 'completed'
  const task = aggregateTaskLiveState(mapBackendTask(raw), {})
  assert.equal(task.status, 'blocked')
  assert.equal(task.planDocument, undefined)
  assert.equal(task.activePlanCheckpoints, undefined)
})
