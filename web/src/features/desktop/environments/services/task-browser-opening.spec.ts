// Purpose: TaskBrowserOpening is the narrow user-click/async-check boundary.
// Prevent popup blocking, opener access, stale/detached/expired navigation and
// unsafe backend URLs without a browser runtime or provider workload.
import test from 'node:test'
import assert from 'node:assert/strict'
import { TaskBrowserOpening, attachmentError } from './task-browser-opening'
import type { TaskBrowserEndpoint } from '../types/environments'

const endpoint: TaskBrowserEndpoint = { id: 'front', name: 'Frontend', container_port: 80, host_port: 43123, ready: true, url: 'http://127.0.0.1:43123/' }
function fixture() {
  const urls: string[] = []
  const popup = { opener: {} as unknown, closed: false, close() { this.closed = true }, location: { replace(url: string) { urls.push(url) } } }
  let resolve!: (value: TaskBrowserEndpoint[]) => void
  let reject!: (error: Error) => void
  const pending = new Promise<TaskBrowserEndpoint[]>((yes, no) => { resolve = yes; reject = no })
  return { urls, popup, pending, resolve, reject }
}
test('reserves synchronously, severs opener and navigates only fresh selected endpoint', async () => {
  const f = fixture(), controller = new TaskBrowserOpening()
  const result = controller.open(() => f.popup, () => f.pending, 'front', () => true)
  assert.equal(f.popup.opener, null)
  assert.deepEqual(f.urls, [])
  f.resolve([{ ...endpoint, id: 'other' }, endpoint])
  await result
  assert.deepEqual(f.urls, [endpoint.url])
  assert.equal(f.popup.closed, false)
})
test('blocked popup never requests a check', async () => {
  let checks = 0
  await assert.rejects(new TaskBrowserOpening().open(() => null, async () => { checks++; return [endpoint] }, 'front', () => true), /Popup blocked/)
  assert.equal(checks, 0)
})
test('cancel/unmount, invalidation/expiry, failure, missing selection and unsafe URL close without navigation', async () => {
  for (const mode of ['cancel', 'invalid', 'failure', 'missing', 'unsafe', 'not-ready']) {
    const f = fixture(), controller = new TaskBrowserOpening()
    let valid = true
    const result = controller.open(() => f.popup, () => f.pending, 'front', () => valid)
    const rejected = assert.rejects(result)
    if (mode === 'cancel') { controller.cancel(); assert.equal(f.popup.closed, true) }
    if (mode === 'invalid') valid = false
    if (mode === 'failure') f.reject(new Error('backend refused'))
    else f.resolve(mode === 'missing' ? [] : [{ ...endpoint, ...(mode === 'unsafe' ? { url: 'javascript:alert(1)' } : {}), ...(mode === 'not-ready' ? { ready: false } : {}) }])
    await rejected
    assert.deepEqual(f.urls, [], mode)
    assert.equal(f.popup.closed, true, mode)
  }
})
test('attachment errors strip control/bidi characters and remain bounded', () => {
  assert.equal(attachmentError('bad\u0000\u202ehealth'), 'bad  health')
  assert.equal(attachmentError('x'.repeat(900)).length, 512)
})
