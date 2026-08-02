import assert from 'node:assert/strict'
import test from 'node:test'
import { desktopComposerTaskCommand, submitDesktopComposer } from './composer-submit'

function deferred() {
  let resolve!: () => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<void>((resolvePromise, rejectPromise) => {
    resolve = () => resolvePromise()
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

test('pending routed-session drafts classify /task for normal command dispatch', () => {
  assert.equal(desktopComposerTaskCommand('/task X')?.action.kind, 'queue-ai-task')
  assert.equal(desktopComposerTaskCommand(' /TASK plan X')?.action.kind, 'queue-ai-task')
  assert.equal(desktopComposerTaskCommand('X'), null)
})

test('exact /task with arguments takes precedence over stopping and clears only after queue success', async () => {
  const queued = deferred()
  const calls: string[] = []
  const submission = submitDesktopComposer({
    draft: '/task keep the active run intact',
    canStop: true,
    clear: () => { calls.push('clear') },
    onSubmit: () => { calls.push('submit') },
    onStop: () => { calls.push('stop') },
    onSlashCommand: async (command, draft) => {
      calls.push(`${command.action.kind}:${draft}`)
      await queued.promise
    },
  })

  await Promise.resolve()
  assert.deepEqual(calls, ['queue-ai-task:/task keep the active run intact'])

  queued.resolve()
  assert.equal(await submission, 'task-queued')
  assert.deepEqual(calls, ['queue-ai-task:/task keep the active run intact', 'clear'])
})

test('rejected /task retains the draft and never submits or stops', async () => {
  const calls: string[] = []
  const result = await submitDesktopComposer({
    draft: '/task retry me',
    canStop: true,
    clear: () => { calls.push('clear') },
    onSubmit: () => { calls.push('submit') },
    onStop: () => { calls.push('stop') },
    onSlashCommand: async () => {
      calls.push('queue')
      throw new Error('queue failed')
    },
  })

  assert.equal(result, 'task-queue-failed')
  assert.deepEqual(calls, ['queue'])
})

test('bare /task never falls through to ordinary stop handling', async () => {
  const calls: string[] = []
  const result = await submitDesktopComposer({
    draft: '/task',
    canStop: true,
    clear: () => { calls.push('clear') },
    onSubmit: () => { calls.push('submit') },
    onStop: () => { calls.push('stop') },
    onSlashCommand: async () => {
      calls.push('queue')
      throw new Error('task request is required')
    },
  })

  assert.equal(result, 'task-queue-failed')
  assert.deepEqual(calls, ['queue'])
})
