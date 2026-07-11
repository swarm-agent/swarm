import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { commitWorkspaceChanges, fetchGitStatus, startGitRealtime } from './api'

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
})

test('git status APIs normalize null file lists and scope requests to the session', async () => {
  const capturedInputs: string[] = []
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    capturedInputs.push(String(input))
    return new Response(JSON.stringify({
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
    })
  }) as typeof fetch

  const status = await fetchGitStatus('/workspace/project', 12, 'session-1')
  const realtime = await startGitRealtime('/workspace/project', 'session-1')

  assert.deepEqual(status.status.files, [])
  assert.deepEqual(realtime.status.files, [])
  assert.deepEqual(capturedInputs, [
    '/v1/workspace/git/status?workspace_path=%2Fworkspace%2Fproject&session_id=session-1&recent_limit=12',
    '/v1/workspace/git/realtime?workspace_path=%2Fworkspace%2Fproject&session_id=session-1',
  ])
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
    sessionId: 'session-1',
  })

  assert.equal(capturedInput, '/v1/workspace/git/commit?session_id=session-1')
  assert.equal(capturedInit?.method, 'POST')
  assert.equal(new Headers(capturedInit?.headers).get('Content-Type'), 'application/json')
  assert.deepEqual(JSON.parse(String(capturedInit?.body)), {
    workspace_path: '/workspace/project',
    cwd: '/workspace/project',
    message: 'feat: direct manual commit',
    all: true,
  })
})
