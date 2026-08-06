import assert from 'node:assert/strict'
import test from 'node:test'
import { desktopComposerBackgroundRouterCommand, submitDesktopComposer } from './composer-submit'

function deferred() {
  let resolve!: () => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<void>((resolvePromise, rejectPromise) => {
    resolve = () => resolvePromise()
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

test('pending routed-session drafts classify every exact /task form for normal command dispatch', () => {
  for (const draft of ['/task X', ' /TASK plan X', '/task', '/task plan']) {
    assert.equal(desktopComposerBackgroundRouterCommand(draft)?.action.kind, 'start-background-router-session', draft)
  }
  assert.equal(desktopComposerBackgroundRouterCommand('X'), null)
})

test('exact /task dispatches immediately, clears without waiting, and never stops the active run', async () => {
  const launched = deferred()
  const calls: string[] = []
  const result = await submitDesktopComposer({
    draft: '/task keep the active run intact',
    canStop: true,
    clear: () => { calls.push('clear') },
    onSubmit: () => { calls.push('submit') },
    onStop: () => { calls.push('stop') },
    onSlashCommand: async (command, draft) => {
      calls.push(`${command.action.kind}:${draft}`)
      await launched.promise
    },
  })

  assert.equal(result, 'background-router-started')
  assert.deepEqual(calls, ['start-background-router-session:/task keep the active run intact', 'clear'])
  launched.resolve()
})

test('an asynchronous /task launch failure does not restore or pend the cleared input', async () => {
  const launched = deferred()
  const calls: string[] = []
  const result = await submitDesktopComposer({
    draft: '/task retry me',
    canStop: true,
    clear: () => { calls.push('clear') },
    onSubmit: () => { calls.push('submit') },
    onStop: () => { calls.push('stop') },
    onSlashCommand: async () => {
      calls.push('launch')
      await launched.promise
    },
  })

  assert.equal(result, 'background-router-started')
  assert.deepEqual(calls, ['launch', 'clear'])
  launched.reject(new Error('launch failed'))
  await Promise.resolve()
})

test('a synchronous /task dispatch failure retains the draft and never submits or stops', async () => {
  const calls: string[] = []
  const result = await submitDesktopComposer({
    draft: '/task retry me',
    canStop: true,
    clear: () => { calls.push('clear') },
    onSubmit: () => { calls.push('submit') },
    onStop: () => { calls.push('stop') },
    onSlashCommand: () => {
      calls.push('launch')
      throw new Error('launch failed')
    },
  })

  assert.equal(result, 'background-router-failed')
  assert.deepEqual(calls, ['launch'])
})

test('bare /task forms retain the draft and never fall through to ordinary stop handling', async () => {
  for (const draft of ['/task', '/task plan']) {
    const calls: string[] = []
    const result = await submitDesktopComposer({
      draft,
      canStop: true,
      clear: () => { calls.push('clear') },
      onSubmit: () => { calls.push('submit') },
      onStop: () => { calls.push('stop') },
      onSlashCommand: async (_command, submittedDraft) => {
        calls.push(`launch:${submittedDraft}`)
        throw new Error('task request is required')
      },
    })

    assert.equal(result, 'background-router-failed')
    assert.deepEqual(calls, [`launch:${draft}`])
  }
})
