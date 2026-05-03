import test from 'node:test'
import assert from 'node:assert/strict'

import { formToCreateInput } from './flows-settings-page'
import type { FlowAgentProfile, FlowSwarmTarget, FlowWorkspaceEntry } from '../api'

test('formToCreateInput maps manual and scheduled flows without auto-run intent', () => {
  const target: FlowSwarmTarget = { swarm_id: 'local-swarm', kind: 'self', name: 'Local', online: true, selectable: true, current: true }
  const workspace: FlowWorkspaceEntry = {
    path: '/tmp/workspace',
    workspaceName: 'workspace',
    themeId: '',
    directories: [],
    isGitRepo: true,
    replicationLinks: [],
    sortIndex: 0,
    addedAt: 0,
    updatedAt: 0,
    lastSelectedAt: 0,
    active: true,
    worktreeEnabled: false,
  }
  const profile: FlowAgentProfile = {
    name: 'missile',
    mode: 'subagent',
    description: 'test agent',
    enabled: true,
    provider: 'test-provider',
    model: 'test-model',
    thinking: 'medium',
    prompt: '',
    executionSetting: '',
    exitPlanModeEnabled: false,
    toolScope: null,
    protected: false,
    updatedAt: 0,
  }
  const targets = [{ key: 'target', label: 'Local', helper: 'self', target }]
  const workspaces = [{ key: workspace.path, label: workspace.path, helper: 'active', workspace }]
  const agents = [{ key: 'missile::subagent', label: 'missile', helper: 'subagent', contractSummary: '', profile }]

  const baseForm = {
    name: 'One shot',
    agentKey: 'missile::subagent',
    targetKey: 'target',
    scheduleCadence: 'Daily' as const,
    scheduleTimes: ['9:00 AM'],
    scheduleDay: 'Mon',
    scheduleDate: '1',
    timezone: 'America/New_York',
    workspacePath: workspace.path,
    task: 'Run once',
  }

  const manual = formToCreateInput({ ...baseForm, scheduleCadence: 'On demand' }, targets, workspaces, agents)
  assert.equal(manual.enabled, false)
  assert.equal(manual.schedule.cadence, 'on_demand')
  assert.equal(manual.intent.mode, 'target-owned schedule')

  const scheduled = formToCreateInput(baseForm, targets, workspaces, agents)
  assert.equal(scheduled.enabled, true)
  assert.equal(scheduled.schedule.cadence, 'daily')
  assert.deepEqual(scheduled.schedule.times, ['09:00'])
  assert.equal(scheduled.schedule.timezone, 'America/New_York')
  assert.equal(scheduled.intent.mode, 'target-owned schedule')
})
