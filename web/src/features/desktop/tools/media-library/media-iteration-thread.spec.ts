// Requirement: each request remains a distinct turn as its exact task outputs
// arrive, including pending/failed turns and multi-output descendants. Prevent
// unrelated generations or library filters from replacing the followed result.
// Authority: MediaViewerModal selection, OrchestrateView task/output ID mapping,
// getMediaIterationJobs/getMediaIterationOutputs. Pure unit tests are the narrowest
// layer for graph/result matching; browser auto-selection still needs UI proof.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { getMediaIterationJobs, getMediaIterationOutputs, isMediaGenerationPending } from './media-iteration-thread'
import type { MediaGenerationJob } from './media-generation'
import type { MediaLibraryItem } from './types'

const asset = (id: string, parentId?: string): MediaLibraryItem => ({
  id, parentId, directUrl: `/media/${id}`, kind: 'image',
} as MediaLibraryItem)
const job = (id: string, sourceId: string, createdAt: number, outputIds: string[] = [], status = 'queued'): MediaGenerationJob => ({
  id, sourceId, createdAt, outputIds, status, title: 'Request', count: 2,
})

test('pending and failed requests stay in chronological thread across descendant outputs', () => {
  const jobs = [job('child', 'a', 3), job('unrelated', 'other', 0), job('failed', 'root', 2, [], 'failed'), job('first', 'root', 1, ['a', 'b'], 'completed')]
  assert.deepEqual(getMediaIterationJobs('b', [asset('root'), asset('a'), asset('b')], jobs).map((turn) => turn.id), ['first', 'failed', 'child'])
  assert.deepEqual(getMediaIterationJobs(undefined, [], jobs), [])
})

test('an optimistic turn gains outputs without changing identity or duplicating the turn', () => {
  const pending = job('request', 'root', 1)
  assert.deepEqual(getMediaIterationOutputs(pending, [asset('unrelated', 'root')]), [])
  const ready = { ...pending, taskId: 'task', status: 'completed', outputIds: ['b', 'a'] }
  const outputs = [asset('unrelated', 'root'), asset('a'), asset('b')]
  assert.deepEqual(getMediaIterationJobs('root', outputs, [ready]).map((turn) => turn.id), ['request'])
  assert.deepEqual(getMediaIterationOutputs(ready, outputs).map((item) => item.id), ['b', 'a'])
})

test('result matching waits for loaded media and excludes unrelated or URL-less outputs', () => {
  const pending = job('request', 'root', 1, ['missing', 'no-url', 'ready'])
  const items = [{ ...asset('no-url'), directUrl: '' }, asset('unrelated'), asset('ready')]
  assert.deepEqual(getMediaIterationOutputs(pending, items).map((item) => item.id), ['ready'])
  assert.deepEqual(getMediaIterationOutputs(undefined, items), [])
})

test('lineage cycles terminate and terminal failures are not presented as pending', () => {
  const items = [asset('a', 'b'), asset('b', 'a')]
  assert.deepEqual(getMediaIterationJobs('a', items, [job('request', 'b', 1)]).map((turn) => turn.id), ['request'])
  for (const status of ['submitting', 'pending', 'queued', 'in_progress', 'running']) assert.equal(isMediaGenerationPending(status), true)
  for (const status of ['completed', 'failed', 'partial_failure', 'cancelled']) assert.equal(isMediaGenerationPending(status), false)
})

// Purpose: lineage, not clock skew or duplicate hydration, orders viewer turns.
// getMediaIterationJobs must retain failed descendants without inventing outputs.
test('parent output dependencies precede children despite reversed timestamps', () => {
  const root = job('root', 'a', 100, ['a', 'b'], 'completed')
  const child = job('child', 'b', 1, ['c'], 'completed')
  const failed = job('failed', 'c', 0, [], 'failed')
  assert.deepEqual(getMediaIterationJobs('a', [asset('a'), asset('b'), asset('c', 'b')], [failed, child, root, child]).map(turn => turn.id), ['root', 'child', 'failed'])
  assert.deepEqual(getMediaIterationOutputs(failed, [asset('c')]), [])
})
