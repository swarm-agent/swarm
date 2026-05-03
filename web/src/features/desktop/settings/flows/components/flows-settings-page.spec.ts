import test from 'node:test'
import assert from 'node:assert/strict'

import { formToCreateInput } from './flows-settings-page'
import type { FlowAgentProfile, FlowSwarmTarget, FlowWorkspaceEntry } from '../api'

test('formToCreateInput differentiates one-shot background from manual one-shot', () => {
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
    scheduleTime: '9:00 AM',
    scheduleDay: 'Mon',
    scheduleDate: '1',
    workspacePath: workspace.path,
    context: 'target-owned schedule',
    task: 'Run once',
  }

  const oneShot = formToCreateInput({ ...baseForm, mode: 'One-shot background job' }, targets, workspaces, agents)
  assert.equal(oneShot.enabled, true)
  assert.equal(oneShot.schedule.cadence, 'on_demand')
  assert.equal(oneShot.intent.mode, 'one_shot_background')

  const manual = formToCreateInput({ ...baseForm, mode: 'Manual one-shot' }, targets, workspaces, agents)
  assert.equal(manual.enabled, false)
  assert.equal(manual.schedule.cadence, 'on_demand')
  assert.equal(manual.intent.mode, 'target-owned schedule')
})
