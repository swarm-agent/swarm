import type { DesktopPermissionRecord } from '../../types/realtime'
import {
  parseAgentChangePermission,
  parseManageTodosPermission,
  parseSessionArchivePermission,
  parseExitPlanPermission,
  parsePlanUpdatePermission,
  buildPlanUpdateDiffPreview,
  parseTaskLaunchPermission,
  permissionKind,
  isPlanProposalPermission,
  permissionRequiresApproval,
  buildGenericPermissionMarkdown,
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
      resolved_tools: {
        preset: 'read_only',
        runtime_mode: 'auto',
        effective_execution_mode: 'read',
        allowed_tools: ['read', 'search'],
      },
      report_max_chars: 2400,
      disabled_tools: ['write', 'edit'],
      launches: [
        {
          launch_index: 1,
          requested_subagent_type: 'explorer',
          resolved_agent_name: 'explorer',
          meta_prompt: 'map repository structure',
          assignment_label: 'Repo map',
          subagent_provider: 'anthropic',
          subagent_model: 'claude-sonnet',
          effective_child_mode: 'auto',
          allow_bash: true,
          disabled_tools: ['write', 'edit'],
          resolved_tools: {
            preset: 'read_only',
            runtime_mode: 'auto',
            effective_execution_mode: 'read',
            allowed_tools: ['read', 'search'],
            disabled_tools: ['write', 'edit'],
            profile_allowed_tools: ['read'],
            profile_disabled_tools: ['write'],
            launch_disabled_tools: ['edit'],
            bash_prefixes: ['git status'],
          },
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
          tool_contract: {
            preset: 'read_only',
            tools: {
              read: { enabled: true },
              bash: { enabled: false },
            },
          },
        },
      },
      tool_inventory: {
        tools: [
          { name: 'read', contract_name: 'read', description: 'Read files', group: 'workspace_inspection', kind: 'built_in' },
          { name: 'manage-agent', contract_name: 'manage_agent', description: 'Manage agents', group: 'management', kind: 'built_in' },
          { name: 'bash', contract_name: 'bash', description: 'Run shell', group: 'shell', kind: 'built_in' },
        ],
        presets: [
          { id: 'read_only', label: 'Read only', description: 'Read tools only', enabled_tools: ['read'], disabled_by_default: ['bash'], bash_prefixes: [] },
        ],
      },
    }),
  })
  assert(permissionKind(permission) === 'agent-change', 'expected agent-change permission kind')
  assert(permissionRequiresApproval(permission, 'auto') === true, 'expected manage-agent approval requirement')
  const payload = parseAgentChangePermission(permission)
  assert(payload.agentName === 'review-bot', 'expected parsed agent name')
  assert(payload.mode === 'subagent', 'expected parsed mode')
  assert(payload.execution === 'read', 'expected parsed execution')
  assert(payload.tools === 'removed: bash', 'expected tool contract label')
  assert(payload.profile.name === 'review-bot', 'expected profile snapshot')
  assert(payload.toolInventory.tools.length === 3, 'expected tool inventory tools')
  assert(payload.toolInventory.tools.some((tool) => tool.contractName === 'manage_agent'), 'expected canonical tool contract name')
  assert(payload.toolInventory.presets[0]?.id === 'read_only', 'expected tool inventory preset')
  assert(payload.toolInventory.presets[0]?.enabledTools.join(',') === 'read', 'expected preset enabled tools')
  assert(payload.toolInventory.presets[0]?.disabledByDefault.join(',') === 'bash', 'expected preset disabled tools')
  assert(payload.approvedArguments.action === undefined, 'expected no approved args when absent')
  assert(payload.changes.some((change) => change.label === 'Result'), 'expected result change row')
}

function testAgentChangeParsesApprovedContentFallback(): void {
  const permission = makePermission({
    toolName: 'manage_agent',
    requirement: 'agent_change',
    toolArguments: JSON.stringify({
      action: 'create',
      approved_arguments: {
        action: 'create',
        agent: 'desktop-bot',
        content: {
          name: 'desktop-bot',
          mode: 'subagent',
          provider: 'anthropic',
          model: 'claude-sonnet',
          prompt: 'Help from desktop.',
          enabled: true,
          tool_scope: {
            allow_tools: ['search', 'read'],
          },
        },
      },
    }),
  })
  const payload = parseAgentChangePermission(permission)
  assert(payload.agentName === 'desktop-bot', 'expected approved content agent name')
  assert(payload.mode === 'subagent', 'expected approved content mode')
  assert(payload.model === 'anthropic / claude-sonnet', 'expected approved content model')
  assert(payload.tools === 'limited to search, read', 'expected approved content tools')
  assert(payload.profile.prompt === 'Help from desktop.', 'expected approved content profile')
  assert(Object.keys(payload.approvedArguments).length > 0, 'expected approved arguments to be retained')
}

function testAgentChangeParsesToolContractFallback(): void {
  const permission = makePermission({
    toolName: 'manage_agent',
    requirement: 'agent_change',
    toolArguments: JSON.stringify({
      action: 'update',
      approved_arguments: {
        action: 'update',
        agent: 'contract-bot',
        content: {
          name: 'contract-bot',
          mode: 'subagent',
          enabled: true,
          tool_contract: {
            preset: 'read_write',
            tools: {
              websearch: { enabled: false },
            },
          },
        },
      },
    }),
  })
  const payload = parseAgentChangePermission(permission)
  assert(payload.agentName === 'contract-bot', 'expected contract content agent name')
  assert(payload.tools === 'removed: websearch', 'expected tool_contract to drive tools label')
  assert(payload.profile.tool_contract !== undefined, 'expected contract profile')
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
  assert(payload.resolvedTools.preset === 'read_only', 'expected top-level resolved tool preset')
  assert(payload.resolvedTools.allowedTools.join(',') === 'read,search', 'expected top-level resolved tools')
  assert(payload.disabledTools.length === 2, 'expected disabled tools at root')
  assert(payload.launches.length === 2, 'expected launch rows to be parsed')
  assert(payload.launches[0]?.requestedSubagentType === 'explorer', 'expected first launch requested subagent type')
  assert(payload.launches[0]?.assignmentLabel === 'Repo map', 'expected first launch assignment label')
  assert(payload.launches[0]?.assignment === 'map repository structure', 'expected meta_prompt to win over short assignment label')
  assert(payload.launches[0]?.subagentProvider === 'anthropic', 'expected resolved provider')
  assert(payload.launches[0]?.subagentModel === 'claude-sonnet', 'expected resolved model')
  assert(payload.launches[0]?.resolvedTools.preset === 'read_only', 'expected resolved tool preset')
  assert(payload.launches[0]?.resolvedTools.effectiveExecutionMode === 'read', 'expected effective execution mode')
  assert(payload.launches[0]?.resolvedTools.allowedTools.join(',') === 'read,search', 'expected allowed tools')
  assert(payload.launches[0]?.resolvedTools.profileAllowedTools.join(',') === 'read', 'expected profile allowed tools')
  assert(payload.launches[0]?.resolvedTools.profileDisabledTools.join(',') === 'write', 'expected profile disabled tools')
  assert(payload.launches[0]?.resolvedTools.launchDisabledTools.join(',') === 'edit', 'expected launch disabled tools')
  assert(payload.launches[0]?.resolvedTools.bashPrefixes.join(',') === 'git status', 'expected bash prefixes')
  assert(payload.launches[1]?.assignment === 'extract concise findings', 'expected second launch assignment')
  assert(payload.prompt === 'Map the relevant files and summarize findings.', 'expected full prompt to be parsed')
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

function testTypedPlanLifecycleKindAndPayloadParsing(): void {
  const followup = makePermission({
    toolName: 'plan_manage',
    requirement: 'plan_followup_request',
    toolArguments: JSON.stringify({
      title: 'Session checkpoint: add audit note',
      plan_id: 'plan_123',
      action: 'request_followup_checkpoint',
      change_request: 'Add an audit note before final review.',
      notes: 'Self-contained handoff context with relevant files and validation expectations.',
      approved_arguments: { action: 'request_followup_checkpoint', plan_id: 'plan_123', change_request: 'Add an audit note before final review.', notes: 'Self-contained handoff context with relevant files and validation expectations.' },
    }),
  })
  assert(permissionKind(followup) === 'plan-followup-request', 'expected plan-followup-request permission kind')
  assert(permissionRequiresApproval(followup, 'auto') === true, 'expected plan_followup_request approval requirement')
  const followupPayload = parsePlanUpdatePermission(followup)
  assert(followupPayload.action === 'request_followup_checkpoint', 'expected session checkpoint action')
  assert(followupPayload.changeRequest.includes('audit note'), 'expected exact change request')
  assert(followupPayload.notes.includes('Self-contained handoff context'), 'expected handoff notes')
  assert(followupPayload.approvedArguments.notes === 'Self-contained handoff context with relevant files and validation expectations.', 'expected approved notes')

  const revision = makePermission({
    toolName: 'plan_manage',
    requirement: 'plan_revision_request',
    toolArguments: JSON.stringify({ action: 'request_plan_revision', plan_id: 'plan_123' }),
  })
  assert(permissionKind(revision) === 'plan-revision-request', 'expected plan-revision-request permission kind')

  const newPlanDocument = {
    title: 'New plan',
    info: { goal: 'Render top-level document content' },
    checkpoints: [{ id: 'cp-new', title: 'New checkpoint', status: 'pending' }],
  }
  const newPlan = makePermission({
    toolName: 'plan_manage',
    requirement: 'plan_new_request',
    toolArguments: JSON.stringify({
      action: 'request_new_plan',
      title: 'New plan',
      document: newPlanDocument,
      approved_arguments: {
        action: 'request_new_plan',
        title: 'New plan',
        document: newPlanDocument,
        approval_confirmed: true,
        execution_granularity: 'checkpointed',
        continuation_policy: 'automatic',
        continue_automatically: true,
      },
    }),
  })
  assert(permissionKind(newPlan) === 'plan-new-request', 'expected plan-new-request permission kind')
  const newPlanPayload = parsePlanUpdatePermission(newPlan)
  assert(newPlanPayload.planId === '', 'expected separate new plan permission not to inject active plan id')
  assert(Boolean(newPlanPayload.document), 'expected top-level structured new-plan document')
  assert((newPlanPayload.document as { info?: { goal?: string } }).info?.goal === 'Render top-level document content', 'expected top-level document content')
  assert(!('plan_id' in newPlanPayload.approvedArguments), 'expected approved arguments not to inject active plan_id')
  assert(newPlanPayload.approvedArguments.approval_confirmed === true, 'expected approval confirmation argument')
  assert(((newPlanPayload.approvedArguments.document as { checkpoints?: Array<{ id?: string }> })?.checkpoints?.[0]?.id ?? '') === 'cp-new', 'expected approved arguments to preserve document')
}

function testPlanProposalClassifierCoversOnlyApprovalCards(): void {
  const proposalCases: Array<[string, string]> = [
    ['exit_plan_mode', 'permission'],
    ['plan_manage', 'plan_update'],
    ['plan_manage', 'plan_followup_request'],
    ['plan_manage', 'plan_revision_request'],
    ['plan_manage', 'plan_amendment_request'],
    ['plan_manage', 'plan_new_request'],
  ]
  for (const [toolName, requirement] of proposalCases) {
    assert(isPlanProposalPermission(makePermission({ toolName, requirement })), `expected ${requirement} inline plan proposal`)
  }
  assert(!isPlanProposalPermission(makePermission({ toolName: 'bash', requirement: 'permission' })), 'expected bash permission to remain modal')
  assert(!isPlanProposalPermission(makePermission({ toolName: 'ask_user', requirement: 'permission' })), 'expected ask-user permission to remain modal')
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

function testPlanAmendmentParsesDeltaAndApprovalArguments(): void {
  const document = {
    id: 'plan_123',
    title: 'Structured Plan',
    checkpoints: [
      { id: 'cp-1', title: 'First', status: 'completed', order: 1 },
      { id: 'cp-2', title: 'Deploy authority', status: 'pending', order: 2 },
    ],
  }
  const permission = makePermission({
    toolName: 'plan_manage',
    requirement: 'plan_amendment_request',
    toolArguments: JSON.stringify({
      title: 'Structured Plan',
      plan_id: 'plan_123',
      document,
      prior_document: { ...document, checkpoints: [{ id: 'cp-1', title: 'First', status: 'completed', order: 1 }, { id: 'cp-2', title: 'Old future', status: 'pending', order: 2 }] },
      current_revision: 4,
      base_revision: 4,
      plan_amendment_delta: {
        reason: 'Replace future work',
        base_revision: 4,
        current_revision: 4,
        replace_from_checkpoint_id: 'cp-2',
        preserved_checkpoints: [{ id: 'cp-1', title: 'First', status: 'completed' }],
        replaced_checkpoints: [{ id: 'cp-2', title: 'Old future', status: 'pending' }],
        replacement_checkpoints: [{ id: 'cp-2', title: 'Deploy authority', status: 'pending' }],
        next_checkpoint: { id: 'cp-2', title: 'Deploy authority', status: 'pending' },
        bullets: ['cp-1 remains completed and preserved.', 'Replacing pending future work from cp-2 (Old future).', 'Next checkpoint becomes cp-2 (Deploy authority).', 'Reason: Replace future work'],
      },
      approved_arguments: {
        action: 'amend_plan',
        plan_id: 'plan_123',
        base_revision: 4,
        override_stale: true,
        replace_from_checkpoint_id: 'cp-2',
        update_summary: 'Replace future work',
        document,
      },
    }),
  })
  assert(permissionKind(permission) === 'plan-amendment-request', 'expected plan amendment permission kind')
  assert(permissionRequiresApproval(permission, 'auto') === true, 'expected plan amendment approval requirement')
  const payload = parsePlanUpdatePermission(permission)
  assert(Boolean(payload.priorDocument), 'expected prior structured document')
  assert(payload.currentRevision === 4, 'expected current revision')
  assert(payload.planAmendmentDelta?.preservedCheckpoints[0]?.id === 'cp-1', 'expected preserved cp-1')
  assert(payload.planAmendmentDelta?.replacedCheckpoints[0]?.id === 'cp-2', 'expected replaced cp-2')
  assert(payload.planAmendmentDelta?.nextCheckpoint?.title === 'Deploy authority', 'expected replacement checkpoint title')
  assert(payload.planAmendmentDelta?.bullets.some((bullet) => bullet.includes('Reason')), 'expected reason bullet')
  assert(payload.approvedArguments.base_revision === 4, 'expected approved args to preserve base revision')
  assert(payload.approvedArguments.override_stale === true, 'expected approved args to preserve override stale')
  assert(payload.approvedArguments.replace_from_checkpoint_id === 'cp-2', 'expected approved args to preserve replace-from checkpoint')
}

function testPlanUpdateParsesStructuredDocument(): void {
  const document = {
    id: 'plan_123',
    title: 'Structured Plan',
    info: { goal: 'Ship structured UI' },
    checkpoints: [
      { id: 'cp-1', title: 'Model', status: 'done', order: 1 },
      { id: 'cp-2', title: 'UI', status: 'pending', order: 2 },
    ],
    active_checkpoint_id: 'cp-2',
  }
  const permission = makePermission({
    toolName: 'plan_manage',
    requirement: 'plan_update',
    toolArguments: JSON.stringify({
      title: 'Structured Plan',
      plan_id: 'plan_123',
      document,
      prior_document: { ...document, active_checkpoint_id: 'cp-1' },
      approved_arguments: { action: 'save', plan_id: 'plan_123', document },
    }),
  })
  const payload = parsePlanUpdatePermission(permission)
  assert(Boolean(payload.document), 'expected structured document passthrough')
  assert((payload.document as { id?: string }).id === 'plan_123', 'expected structured document id')
  assert(Boolean(payload.priorDocument), 'expected prior structured document')
  assert(((payload.approvedArguments.document as { id?: string })?.id ?? '') === 'plan_123', 'expected approved arguments to preserve document')
}

function testExitPlanParsesStructuredDocument(): void {
  const document = {
    id: 'plan_exit',
    title: 'Exit Structured Plan',
    info: { goal: 'Approve structured exit' },
    checkpoints: [{ id: 'cp-1', title: 'Exit', status: 'pending', order: 1 }],
  }
  const permission = makePermission({
    toolName: 'exit_plan_mode',
    requirement: 'permission',
    toolArguments: JSON.stringify({
      title: 'Exit Structured Plan',
      plan_id: 'plan_exit',
      plan: '# fallback',
      document,
      execution_granularity: 'checkpointed',
      continuation_policy: 'automatic',
      continue_automatically: true,
      execution_recommendation: {
        execution_granularity: 'checkpointed',
        continuation_policy: 'automatic',
        continue_automatically: true,
      },
      approved_arguments: {
        plan_id: 'plan_exit',
        document,
        continue_automatically: false,
      },
    }),
  })
  const payload = parseExitPlanPermission(permission)
  assert(payload.planId === 'plan_exit', 'expected exit plan id')
  assert(payload.body === '# fallback', 'expected fallback body')
  assert(Boolean(payload.document), 'expected exit document passthrough')
  assert((payload.document as { id?: string }).id === 'plan_exit', 'expected exit document id')
  assert(payload.approvedArguments.plan_id === 'plan_exit', 'expected approved arguments to preserve plan id')
  assert(payload.approvedArguments.continue_automatically === false, 'expected approved arguments to preserve execution controls')
  assert(!('executionRecommendation' in payload), 'expected exit plan parsing to ignore execution recommendations')
}

function testPlanUpdateDiffPreviewPreservesAllDiffRows(): void {
  const diffRows = [
    '  # Plan',
    '  1. Keep setup',
    '- 2. Old implementation detail',
    '+ 2. New implementation detail',
    '  3. Keep validation',
    '  4. Keep release notes',
    '  5. Keep follow-up',
    '  6. Keep owner',
    '  7. Keep timeline',
    '- 8. Remove noisy grid',
    '+ 8. Show full review',
    '  9. Keep done',
  ]
  const preview = buildPlanUpdateDiffPreview(diffRows, '', '')

  assert(preview.addedCount === 2, 'expected two added rows')
  assert(preview.removedCount === 2, 'expected two removed rows')
  assert(preview.totalRows === diffRows.length, 'expected every diff row to be shown')
  assert(preview.omittedUnchangedRows === 0, 'expected no hidden unchanged rows')
  assert(!preview.rows.some((row) => row.kind === 'gap'), 'expected no collapsed gaps')
  assert(preview.rows.some((row) => row.kind === 'context' && row.text.includes('Keep validation')), 'expected unchanged context to remain visible')
}

function testPlanUpdateDiffPreviewFallsBackToCompleteBeforeAfterRows(): void {
  const preview = buildPlanUpdateDiffPreview([], '# Plan\n1. Same\n2. Old\n3. Same', '# Plan\n1. Same\n2. New\n3. Same')

  assert(preview.addedCount === 1, 'expected one fallback added row')
  assert(preview.removedCount === 1, 'expected one fallback removed row')
  assert(preview.rows.length === 5, 'expected fallback to preserve unchanged rows')
  assert(preview.rows[0]?.kind === 'context' && preview.rows[0]?.text === '# Plan', 'expected first unchanged row')
  assert(preview.rows[1]?.kind === 'context' && preview.rows[1]?.text === '1. Same', 'expected second unchanged row')
  assert(preview.rows[2]?.kind === 'removed' && preview.rows[2]?.text === '2. Old', 'expected removed fallback row')
  assert(preview.rows[3]?.kind === 'added' && preview.rows[3]?.text === '2. New', 'expected added fallback row')
  assert(preview.rows[4]?.kind === 'context' && preview.rows[4]?.text === '3. Same', 'expected trailing unchanged row')
}

function testGenericBashPermissionFormatsCommandAsCodeBlock(): void {
  const command = [
    "perl -0pi -e 's/advertiseport = \\$\\{backendBasePort \\+ 1\\}/advertiseport = ${backendBasePort + 1}/' /tmp/swarm-dev-update-playwright-e2e-v2.mjs",
    'inspect line',
    '',
    "grep -n 'advertise_port' /tmp/swarm-dev-update-playwright-e2e-v2.mjs",
    "./scripts/swarm-harness-vm.sh run --no-sync -- bash -lc 'cd ~/swarm-go; cat > /tmp/swarm-dev-update-playwright-e2e-v2.mjs' < /tmp/swarm-dev-update-playwright-e2e-v2.mjs",
    "./scripts/swarm-harness-vm.sh run --no-sync -- bash -lc 'cd ~/swarm-go; node /tmp/swarm-dev-update-playwright-e2e-v2.mjs'",
  ].join('\n')
  const permission = makePermission({
    toolName: 'bash',
    requirement: 'bash',
    mode: 'auto',
    toolArguments: JSON.stringify({
      command: `\`${command}\``,
    }),
  })

  const body = buildGenericPermissionMarkdown(permission)
  assert(!body.includes('Tool: bash · Requirement: bash · Mode: auto'), 'expected permission metadata to stay out of markdown body')
  assert(body.includes('**Command**\n\n```bash\n'), 'expected bash code fence')
  assert(body.includes(command), 'expected unwrapped command body')
  assert(!body.includes(`\`${command}\``), 'expected wrapping backticks to be removed')
}

function testManageSessionsArchiveShowsHydratedFactsOnly(): void {
  const firstId = '3797e357-ef14-4983-aa70-625efb8be323'
  const permission = makePermission({
    toolName: 'manage_sessions',
    requirement: 'session_archive',
    toolArguments: JSON.stringify({
      action: 'archive',
      sessions: [{ title: 'Review search results', workspace_name: 'Swarm', state: 'needs_review', updated_at: 1783764535576 }],
      approved_arguments: {
        action: 'archive',
        session_ids: [firstId],
        expected_updated_at_by_id: { [firstId]: 1783764535576 },
      },
    }),
  })

  assert(permissionKind(permission) === 'session-archive', 'expected dedicated session archive permission kind')
  const payload = parseSessionArchivePermission(permission)
  assert(payload.sessions.length === 1, 'expected one parsed session')
  assert(payload.sessions[0]?.title === 'Review search results', 'expected parsed title')
  assert(payload.sessions[0]?.workspaceName === 'Swarm', 'expected parsed workspace')
  assert(payload.sessions[0]?.state === 'needs_review', 'expected parsed state')
  assert(payload.sessions[0]?.updatedAt === 1783764535576, 'expected parsed timestamp')
  assert(payload.approvedArguments.session_ids instanceof Array, 'expected approved arguments to be preserved')

  const body = buildGenericPermissionMarkdown(permission)
  assert(body.includes('Review search results'), 'expected hydrated title')
  assert(body.includes('needs_review'), 'expected hydrated state')
  assert(!body.includes(firstId), 'expected authoritative opaque id to stay hidden from the prompt')
  assert(!body.includes('Expected Updated At By Id'), 'expected concurrency map to stay hidden from the prompt')
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
  testAgentChangeParsesApprovedContentFallback()
  testAgentChangeParsesToolContractFallback()
  testTaskLaunchKindAndApproval()
  testTaskLaunchPayloadParsing()
  testManageTodosKindAndPayloadParsing()
  testTypedPlanLifecycleKindAndPayloadParsing()
  testPlanProposalClassifierCoversOnlyApprovalCards()
  testPlanUpdateKindAndPayloadParsing()
  testPlanAmendmentParsesDeltaAndApprovalArguments()
  testPlanUpdateParsesStructuredDocument()
  testExitPlanParsesStructuredDocument()
  testPlanUpdateDiffPreviewPreservesAllDiffRows()
  testPlanUpdateDiffPreviewFallsBackToCompleteBeforeAfterRows()
  testGenericBashPermissionFormatsCommandAsCodeBlock()
  testManageSessionsArchiveShowsHydratedFactsOnly()
  testManageTodosBatchParsing()
  console.log('permission-payload tests passed')
}

main()
