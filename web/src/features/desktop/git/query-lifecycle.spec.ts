// Purpose: TanStack Query observer ownership must reach fetchGitStatus's AbortSignal
// on route/workspace replacement and unmount, without aborting another observer.
// This tests the real query client and Git API, not source-string cancellation.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { fetchGitStatus, gitStatusQueryKey } from './api'

test('query switching/unmount aborts obsolete Git status while preserving shared observers', async () => {
  const original = globalThis.fetch
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const signals: AbortSignal[] = []
  const finish: Array<(response: Response) => void> = []
  globalThis.fetch = async (_input, init) => {
    signals.push(init!.signal!)
    return new Promise((resolve) => finish.push(resolve))
  }
  const options = (workspace: string) => ({
    queryKey: gitStatusQueryKey(workspace, 'session'),
    queryFn: ({ signal }: { signal: AbortSignal }) => fetchGitStatus(workspace, 12, 'session', signal),
  })
  const first = new QueryObserver(client, options('/workspace/a'))
  const second = new QueryObserver(client, options('/workspace/a'))
  const releaseFirst = first.subscribe(() => {})
  const releaseSecond = second.subscribe(() => {})
  try {
    assert.equal(signals.length, 1)
    first.setOptions(options('/workspace/b'))
    assert.equal(signals[0].aborted, false)
    assert.equal(signals.length, 2)
    releaseSecond()
    assert.equal(signals[0].aborted, true)
    first.setOptions(options('/workspace/a'))
    assert.equal(signals[1].aborted, true)
    assert.equal(signals.length, 3)
    finish[0](Response.json({ status: { branch: 'stale', files: [] } }))
    finish[2](Response.json({ status: { branch: 'current', files: [] } }))
    for (let i = 0; i < 40; i++) await Promise.resolve()
    assert.equal(first.getCurrentResult().data?.status.branch, 'current')
    first.setOptions(options('/workspace/c'))
    releaseFirst()
    assert.equal(signals[3].aborted, true)
  } finally { releaseFirst(); releaseSecond(); client.clear(); globalThis.fetch = original }
})
