import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { commitWorkspaceChanges, fetchGitStatus, startGitRealtime } from './api'

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
})

test('git status APIs normalize null file lists from clean snapshots', async () => {
  globalThis.fetch = (async (input: RequestInfo | URL) => new Response(JSON.stringify({
    ok: true,
    workspace_path: '/workspace/project',
    watch_token: 'watch-1',
    status: {
      workspace_path: '/workspace/project',
      has_git: true,
      clean: true,
      files: null,
    },
  }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })) as typeof fetch

  const status = await fetchGitStatus('/workspace/project')
  const realtime = await startGitRealtime('/workspace/project')

  assert.deepEqual(status.status.files, [])
  assert.deepEqual(realtime.status.files, [])
})

test('commitWorkspaceChanges posts exact manual commit request to git commit API', async () => {
  let capturedInput: RequestInfo | URL | undefined
  let capturedInit: RequestInit | undefined
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    capturedInput = input
    capturedInit = init
    return new Response(JSON.stringify({ ok: true, exit_code: 0 }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  await commitWorkspaceChanges({
    workspacePath: '/workspace/project',
    cwd: '/workspace/project',
    message: 'feat: direct manual commit',
  })

  assert.equal(capturedInput, '/v1/workspace/git/commit')
  assert.equal(capturedInit?.method, 'POST')
  assert.equal(new Headers(capturedInit?.headers).get('Content-Type'), 'application/json')
  assert.deepEqual(JSON.parse(String(capturedInit?.body)), {
    workspace_path: '/workspace/project',
    cwd: '/workspace/project',
    message: 'feat: direct manual commit',
    all: true,
  })
})
