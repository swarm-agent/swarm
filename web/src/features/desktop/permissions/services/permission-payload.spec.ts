import type { DesktopPermissionRecord } from '../../types/realtime'
import {
  parseAgentChangePermission,
  parseManageTodosPermission,
  parsePlanUpdatePermission,
  parseTaskLaunchPermission,
  permissionKind,
  permissionRequiresApproval,
} from './permission-payload'

function makePermission(overrides: Partial<DesktopPermissionRecord> = {}): DesktopPermissionRecord {
  return {
    id: 'perm_1',
    sessionId: 'session_1',
    runId: 'run_1',
    callId: 'call_1',
    toolName: 'task',
    toolArguments: JSON.stringify({
      description: 'Inspect the repo',
      prompt: 'Map the relevant files and summarize findings.',
      launch_count: 2,
      allow_bash: true,
      effective_child_mode: 'auto',
      report_max_chars: 2400,
      disabled_tools: ['write', 'edit'],
      launches: [
        {
          launch_index: 1,
          requested_subagent_type: 'explorer',
          resolved_agent_name: 'explorer',
          meta_prompt: 'map repository structure',
          effective_child_mode: 'auto',
          allow_bash: true,
          disabled_tools: ['write', 'edit'],
        },
        {
          launch_index: 2,
          requested_subagent_type: 'memory',
          resolved_agent_name: 'memory',
          meta_prompt: 'extract concise findings',
          effective_child_mode: 'auto',
          allow_bash: false,
          disabled_tools: ['write', 'edit', 'bash'],
        },
      ],
    }),
    status: 'pending',
    decision: '',
    reason: '',
    requirement: 'task_launch',
    mode: 'auto+bypass_permissions',
    createdAt: 1,
    updatedAt: 1,
    resolvedAt: 0,
    permissionRequestedAt: 1,
    ...overrides,
  }
}

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message)
  }
}

function testAgentChangeKindAndPayloadParsing(): void {
  const permission = makePermission({
    toolName: 'manage-agent',
    requirement: 'agent_change',
    toolArguments: JSON.stringify({
      action: 'create',
      summary: 'Create @review-bot · subagent · read · all enabled',
      change: {
        operation: 'create',
        target: 'agent_profile',
        after: {
          name: 'review-bot',
          mode: 'subagent',
          description: 'Reviews diffs',
          prompt: 'Review code changes carefully.',
          execution_setting: 'read',
          enabled: true,
          tool_scope: {},
        },
      },
    }),
  })
  assert(permissionKind(permission) === 'agent-change', 'expected agent-change permission kind')
  assert(permissionRequiresApproval(permission, 'auto') === true, 'expected manage-agent approval requirement')
  const payload = parseAgentChangePermission(permission)
  assert(payload.agentName === 'review-bot', 'expected parsed agent name')
  assert(payload.mode === 'subagent', 'expected parsed mode')
  assert(payload.execution === 'read', 'expected parsed execution')
  assert(payload.tools === 'all enabled', 'expected full tools label')
  assert(payload.changes.some((change) => change.label === 'Result'), 'expected result change row')
}

function testTaskLaunchKindAndApproval(): void {
  const permission = makePermission()
  assert(permissionKind(permission) === 'task-launch', 'expected task-launch permission kind')
  assert(permissionRequiresApproval(permission, 'auto') === true, 'expected task launch approval requirement')
}

function testTaskLaunchPayloadParsing(): void {
  const payload = parseTaskLaunchPermission(makePermission())
  assert(payload.title === 'Review Task Launch', 'expected task launch title')
  assert(payload.launchCount === 2, 'expected launch count to be parsed')
  assert(payload.allowBash === true, 'expected allowBash to be true')
  assert(payload.effectiveChildMode === 'auto', 'expected effective child mode')
  assert(payload.disabledTools.length === 2, 'expected disabled tools at root')
  assert(payload.launches.length === 2, 'expected launch rows to be parsed')
  assert(payload.launches[0]?.requestedSubagentType === 'explorer', 'expected first launch requested subagent type')
  assert(payload.launches[1]?.assignment === 'extract concise findings', 'expected second launch assignment')
  assert(payload.summary.includes('Bypass permissions does not skip this review.'), 'expected bypass warning in summary')
}

function testManageTodosKindAndPayloadParsing(): void {
  const permission = makePermission({
    toolName: 'manage_todos',
    requirement: 'permission',
    toolArguments: JSON.stringify({
      action: 'create',
      text: 'Add timeline preview for tasks',
      priority: 'high',
      group: 'ui',
      tags: ['tasks', 'tui'],
      in_progress: true,
      workspace_path: '/workspace/demo',
    }),
  })
  assert(permissionKind(permission) === 'manage-todos', 'expected manage-todos permission kind')
  assert(permissionRequiresApproval(permission, 'plan') === true, 'expected manage_todos approval requirement')
  const payload = parseManageTodosPermission(permission)
  assert(payload.title.includes('Create Task'), 'expected manage_todos title')
  assert(payload.body.includes('[ ] Add timeline preview for tasks'), 'expected checklist preview in body')
  assert(payload.body.includes('`#tasks`'), 'expected tags in body')
}

function testPlanUpdateKindAndPayloadParsing(): void {
  const permission = makePermission({
    toolName: 'plan_manage',
    requirement: 'plan_update',
    toolArguments: JSON.stringify({
      title: 'Plan: update approval flow',
      plan_id: 'plan_123',
      prior_title: 'Plan: old title',
      prior_plan: '# Old\n1. Before',
      plan: '# New\n1. After',
      diff_lines: ['@@ -1 +1 @@', '-1. Before', '+1. After'],
    }),
  })
  assert(permissionKind(permission) === 'plan-update', 'expected plan-update permission kind')
  assert(permissionRequiresApproval(permission, 'auto') === true, 'expected plan_update approval requirement')
  const payload = parsePlanUpdatePermission(permission)
  assert(payload.title === 'Plan: update approval flow', 'expected plan update title')
  assert(payload.planId === 'plan_123', 'expected plan update id')
  assert(payload.priorTitle === 'Plan: old title', 'expected prior title')
  assert(payload.priorPlan.includes('Before'), 'expected prior plan body')
  assert(payload.plan.includes('After'), 'expected updated plan body')
  assert(payload.diffLines.length === 3, 'expected diff lines to be parsed')
  assert(Object.keys(payload.approvedArguments).length === 0, 'expected no approved arguments by default')
}

function testManageTodosBatchParsing(): void {
  const permission = makePermission({
    toolName: 'manage_todos',
    requirement: 'permission',
    toolArguments: JSON.stringify({
      action: 'batch',
      workspace_path: '/workspace/demo',
      operations: [
        { action: 'create', text: 'First batched task' },
        { action: 'update', id: 'todo_123', text: 'Rename existing task' },
        { action: 'delete', id: 'todo_456' },
      ],
    }),
  })
  const payload = parseManageTodosPermission(permission)
  assert(payload.title.includes('Atomic Batch'), 'expected batch title')
  assert(payload.isBatch === true, 'expected batch payload mode')
  assert(payload.batchRows.length === 3, 'expected three batch rows')
  assert(payload.batchRows[0]?.text === '[ ] First batched task', 'expected first task row text')
  assert(payload.batchRows[0]?.metadata.includes('action=create'), 'expected create metadata')
  assert(payload.batchRows[1]?.text === '[ ] Rename existing task', 'expected updated task row')
  assert(payload.batchRows[1]?.metadata.includes('action=update'), 'expected update action metadata')
  assert(payload.batchRows[1]?.metadata.includes('id=todo_123'), 'expected update id metadata')
  assert(payload.batchRows[2]?.text === '[ ] Delete todo_456', 'expected delete row')
  assert(payload.summaryLine === 'Atomic batch for `/workspace/demo` on User Todos with `3` operations.', 'expected atomic batch summary line')
  assert(payload.body.includes('Atomic batch preview'), 'expected markdown fallback heading')
  assert(payload.summaryLine.includes('User Todos'), 'expected default owner label')
}

function main(): void {
  testAgentChangeKindAndPayloadParsing()
  testTaskLaunchKindAndApproval()
  testTaskLaunchPayloadParsing()
  testManageTodosKindAndPayloadParsing()
  testPlanUpdateKindAndPayloadParsing()
  testManageTodosBatchParsing()
  console.log('permission-payload tests passed')
}

main()
