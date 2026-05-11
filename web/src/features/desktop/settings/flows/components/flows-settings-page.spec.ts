import test from 'node:test'
import assert from 'node:assert/strict'

import { formToCreateInput, groupedAgentOptions, workspaceOptionsFromEntries } from './flows-settings-page'
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
  const agents = [{ key: 'missile::subagent', label: 'missile', helper: 'subagent', contractSummary: '', groupLabel: 'Assigned Subagents', profile }]

  const baseForm = {
    name: 'One shot',
    agentKey: 'missile::subagent',
    targetKey: 'target',
    scheduleMode: 'guided' as const,
    scheduleCadence: 'Daily' as const,
    dailyMode: 'once' as const,
    scheduleTimes: ['9:00 AM'],
    dailyRunCount: '4',
    dailyIntervalHours: '2',
    dailyWindowStart: '9:00 AM',
    dailyWindowEnd: '5:00 PM',
    highRunCountConfirmed: false,
    scheduleDay: 'Mon',
    scheduleDate: '1',
    timezone: 'America/New_York',
    cronExpression: '0 9,13,17 * * Mon-Fri',
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
  assert.equal(scheduled.schedule.time, '09:00')
  assert.deepEqual(scheduled.schedule.times, ['09:00'])
  assert.equal(scheduled.schedule.timezone, 'America/New_York')
  assert.equal(scheduled.intent.mode, 'target-owned schedule')

  const weekly = formToCreateInput({ ...baseForm, scheduleCadence: 'Weekly' }, targets, workspaces, agents)
  assert.equal(weekly.schedule.cadence, 'weekly')
  assert.equal(weekly.schedule.time, '09:00')
  assert.deepEqual(weekly.schedule.times, ['09:00'])
  assert.equal(weekly.schedule.weekday, 'Mon')

  const multiDayWeekly = formToCreateInput({ ...baseForm, scheduleCadence: 'Weekly', scheduleDay: 'Mon,Wed,Fri' }, targets, workspaces, agents)
  assert.equal(multiDayWeekly.schedule.weekday, 'Mon,Wed,Fri')

  const exact = formToCreateInput({ ...baseForm, scheduleMode: 'cron', cronExpression: '0 9 * * Mon' }, targets, workspaces, agents)
  assert.equal(exact.schedule.cron, '0 9 * * Mon')
  assert.equal(exact.schedule.cadence, 'daily')
  assert.equal(exact.schedule.time, '09:00')
  assert.deepEqual(exact.schedule.times, ['09:00'])

  const spreadDaily = formToCreateInput({ ...baseForm, dailyMode: 'times_between', dailyRunCount: '4', dailyWindowStart: '9:00 AM', dailyWindowEnd: '5:00 PM' }, targets, workspaces, agents)
  assert.deepEqual(spreadDaily.schedule.times, ['09:00', '11:40', '14:20', '17:00'])

  const intervalDaily = formToCreateInput({ ...baseForm, dailyMode: 'interval_window', dailyIntervalHours: '3', dailyWindowStart: '9:00 AM', dailyWindowEnd: '5:00 PM' }, targets, workspaces, agents)
  assert.deepEqual(intervalDaily.schedule.times, ['09:00', '12:00', '15:00'])

  const monthly = formToCreateInput({ ...baseForm, scheduleCadence: 'Monthly', scheduleDate: '15' }, targets, workspaces, agents)
  assert.equal(monthly.schedule.cadence, 'monthly')
  assert.equal(monthly.schedule.time, '09:00')
  assert.deepEqual(monthly.schedule.times, ['09:00'])
  assert.equal(monthly.schedule.month_day, 15)
})


test('workspaceOptionsFromEntries only exposes supplied target workspace records', () => {
  const currentTargetWorkspaces: FlowWorkspaceEntry[] = [
    {
      path: '/target-a/one',
      workspaceName: 'one',
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
    },
    {
      path: '/target-a/one',
      workspaceName: 'duplicate should not render twice',
      themeId: '',
      directories: [],
      isGitRepo: true,
      replicationLinks: [],
      sortIndex: 1,
      addedAt: 0,
      updatedAt: 0,
      lastSelectedAt: 0,
      active: false,
      worktreeEnabled: false,
    },
  ]

  const options = workspaceOptionsFromEntries(currentTargetWorkspaces)
  assert.deepEqual(options.map((option) => option.key), ['/target-a/one'])
  assert.equal(options[0]?.workspace.path, '/target-a/one')
})

test('groupedAgentOptions keeps section labels separate from agent option values', () => {
  const profile = (name: string, mode: string): FlowAgentProfile => ({
    name,
    mode,
    description: '',
    enabled: true,
    provider: '',
    model: '',
    thinking: '',
    prompt: '',
    executionSetting: '',
    exitPlanModeEnabled: false,
    toolScope: null,
    protected: false,
    updatedAt: 0,
  })
  const agents = [
    { key: 'swarm::primary', label: 'swarm', helper: '', contractSummary: '', groupLabel: 'Primary', profile: profile('swarm', 'primary') },
    { key: 'explorer::subagent', label: 'explorer', helper: '', contractSummary: '', groupLabel: 'Assigned Subagents', profile: profile('explorer', 'subagent') },
    { key: 'cleanup::background', label: 'cleanup', helper: '', contractSummary: '', groupLabel: 'Background', profile: profile('cleanup', 'background') },
  ]

  const groups = groupedAgentOptions(agents)

  assert.deepEqual(groups.map((group) => group.label), ['Primary', 'Assigned Subagents', 'Background'])
  assert.deepEqual(groups.map((group) => group.options.map((option) => option.label)), [['swarm'], ['explorer'], ['cleanup']])
  assert.deepEqual(groups.flatMap((group) => group.options.map((option) => option.key)), ['swarm::primary', 'explorer::subagent', 'cleanup::background'])
})

test('formToCreateInput stores only selected real agent identifiers from every group', () => {
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
  const profile = (name: string, mode: string): FlowAgentProfile => ({
    name,
    mode,
    description: '',
    enabled: true,
    provider: '',
    model: '',
    thinking: '',
    prompt: '',
    executionSetting: '',
    exitPlanModeEnabled: false,
    toolScope: null,
    protected: false,
    updatedAt: 0,
  })
  const targets = [{ key: 'target', label: 'Local', helper: 'self', target }]
  const workspaces = [{ key: workspace.path, label: workspace.path, helper: 'active', workspace }]
  const agents = [
    { key: 'swarm::primary', label: 'swarm', helper: '', contractSummary: '', groupLabel: 'Primary', profile: profile('swarm', 'primary') },
    { key: 'explorer::subagent', label: 'explorer', helper: '', contractSummary: '', groupLabel: 'Assigned Subagents', profile: profile('explorer', 'subagent') },
    { key: 'cleanup::background', label: 'cleanup', helper: '', contractSummary: '', groupLabel: 'Background', profile: profile('cleanup', 'background') },
  ]

  for (const agent of agents) {
    const input = formToCreateInput({
      name: 'Grouped agent flow',
      agentKey: agent.key,
      targetKey: 'target',
      scheduleMode: 'guided' as const,
      scheduleCadence: 'Daily' as const,
      dailyMode: 'once' as const,
      scheduleTimes: ['9:00 AM'],
      dailyRunCount: '1',
      dailyIntervalHours: '2',
      dailyWindowStart: '9:00 AM',
      dailyWindowEnd: '5:00 PM',
      highRunCountConfirmed: false,
      scheduleDay: 'Mon',
      scheduleDate: '1',
      timezone: 'UTC',
      cronExpression: '',
      workspacePath: workspace.path,
      task: 'Run task',
    }, targets, workspaces, agents)

    assert.equal(input.agent.profile_name, agent.profile.name)
    assert.equal(input.agent.profile_mode, agent.profile.mode)
    assert.equal(input.agent.profile_name.includes(agent.groupLabel), false)
    assert.equal(input.agent.profile_name.includes('—'), false)
  }
})
