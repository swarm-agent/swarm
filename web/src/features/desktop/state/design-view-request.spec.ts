import assert from 'node:assert/strict'
import test from 'node:test'
import { DesignViewRequest } from './design-view-request'

// Purpose: DesignViewRequest is the UI's exact-revision response fence. Resolve an
// obsolete request after a newer view and after unmount; neither may paint or report
// an error for the current view, even if the underlying reader ignores cancellation.
test('out-of-order revision and session responses cannot replace the current preview', async () => {
  const view = new DesignViewRequest()
  const painted: string[] = []
  const errors: unknown[] = []
  let finishOld!: (text: string) => void
  let oldSignal!: AbortSignal
  const old = view.load(signal => { oldSignal = signal; return new Promise(resolve => { finishOld = resolve }) }, text => painted.push(text), error => errors.push(error))
  await view.load(async () => 'new revision', text => painted.push(text), error => errors.push(error))
  assert.equal(oldSignal.aborted, true)
  finishOld('old revision'); await old
  assert.deepEqual(painted, ['new revision'])
  let rejectOld!: (error: Error) => void
  const unmounted = view.load(() => new Promise((_resolve, reject) => { rejectOld = reject }), text => painted.push(text), error => errors.push(error))
  view.cancel(); rejectOld(new Error('stale failure')); await unmounted
  assert.deepEqual(errors, [])
  assert.deepEqual(painted, ['new revision'])
})
