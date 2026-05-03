import assert from 'node:assert/strict'
import test from 'node:test'

import { applyDesktopChatRouteToSession, withDesktopChatRoute, type DesktopChatRoute } from './chat-routing'
import type { DesktopSessionRecord } from '../../types/realtime'

const remoteRoute: DesktopChatRoute = {
  id: 'swarm:child-swarm:/workspaces/swarm',
  label: 'child swarm',
  swarmId: 'child-swarm',
  hostWorkspacePath: '/home/dev/swarm',
  hostWorkspaceName: 'host swarm',
  runtimeWorkspacePath: '/workspaces/swarm',
}

function sessionRecord(overrides: Partial<DesktopSessionRecord> = {}): DesktopSessionRecord {
  return {
    id: 'session-1',
    title: 'Remote child session',
    workspacePath: '/workspaces/swarm',
    workspaceName: 'child swarm',
    mode: 'auto',
    metadata: {},
    messageCount: 0,
    updatedAt: 0,
    createdAt: 0,
    permissionsHydrated: true,
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
    live: {
      status: 'idle',
      activeRunId: null,
      startedAt: null,
      completedAt: null,
      lastEventAt: null,
      lastError: null,
    },
    runtimeWorkspacePath: '/workspaces/swarm',
    worktreeEnabled: false,
    worktreeRootPath: '',
    worktreeBaseBranch: '',
    worktreeBranch: '',
    gitBranch: '',
    gitHasGit: false,
    gitClean: false,
    gitDirtyCount: 0,
    gitStagedCount: 0,
    gitModifiedCount: 0,
    gitUntrackedCount: 0,
    gitConflictCount: 0,
    gitAheadCount: 0,
    gitBehindCount: 0,
    gitCommittedFileCount: 0,
    gitCommittedAdditions: 0,
    gitCommittedDeletions: 0,
    lifecycle: null,
    ...overrides,
  }
}

test('routed session fetch URL includes swarm_id so backend can proxy to child', () => {
  assert.equal(
    withDesktopChatRoute('/v1/sessions/session-1', remoteRoute),
    '/v1/sessions/session-1?swarm_id=child-swarm',
  )
})

test('routed session hydration preserves remote child workspace identity', () => {
  const mapped = applyDesktopChatRouteToSession(sessionRecord(), remoteRoute)

  assert.equal(mapped.workspacePath, '/workspaces/swarm')
  assert.equal(mapped.workspaceName, 'child swarm')
  assert.equal(mapped.runtimeWorkspacePath, '/workspaces/swarm')
})

test('routed local host mirror session remains grouped under host workspace', () => {
  const mapped = applyDesktopChatRouteToSession(sessionRecord({
    workspacePath: '/home/dev/swarm',
    workspaceName: 'host swarm',
  }), remoteRoute)

  assert.equal(mapped.workspacePath, '/home/dev/swarm')
  assert.equal(mapped.workspaceName, 'host swarm')
  assert.equal(mapped.runtimeWorkspacePath, '/workspaces/swarm')
})

test('flow sessions preserve their own workspace identity under routed child hydration', () => {
  const mapped = applyDesktopChatRouteToSession(sessionRecord({
    title: 'Memory sweep',
    workspacePath: '/workspaces/swarm',
    workspaceName: 'child swarm',
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-1',
    },
  }), remoteRoute)

  assert.equal(mapped.workspacePath, '/workspaces/swarm')
  assert.equal(mapped.workspaceName, 'child swarm')
  assert.equal(mapped.runtimeWorkspacePath, '/workspaces/swarm')
})
