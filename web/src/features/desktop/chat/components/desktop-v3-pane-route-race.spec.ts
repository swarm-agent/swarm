import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import {
  buildDesktopV3ConversationRenderItems,
  buildDesktopV3LiveRunRenderItems,
  desktopV3RenderItemKey,
  DesktopV3RenderItemView,
  isDesktopV3PlanBlockedHandoffMessage,
  isDesktopV3PlanCheckpointHandoffMessage,
  isDesktopV3PlanExecutionBreakMessage,
  isDesktopV3PlanFinalHandoffMessage,
  parseDesktopV3HandoffSummary,
  resolveDesktopV3StickyBottomAttachment,
  completeDesktopV3ExistingMessage,
  resolveDesktopV3StopRunRequest,
  selectDesktopV3SuggestedPrompt,
} from './desktop-v3-existing-conversation-pane'
import { resolveDesktopV3AgentModelLock } from '../services/agent-model-preferences'
import {
  completeDesktopV3NewSessionStarted,
} from './desktop-v3-new-session-pane'
import {
  createDesktopV3ExistingMessageOperation,
  persistDesktopV3ExistingMessageOperation,
  loadDesktopV3ExistingMessageOperation,
} from '../../session-v3/existing-session-flow'
import {
  createDesktopV3NewSessionOperation,
  persistDesktopV3NewSessionOperation,
  loadDesktopV3NewSessionOperation,
} from '../../session-v3/new-session-flow'
import type { DesktopChatRoute } from '../services/chat-routing'
import type { AgentProfileRecord } from '../types/chat'

const conversationPaneSource = readFileSync(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

const route: DesktopChatRoute = {
  id: 'swarm:swarm-self:binding:binding-self',
  label: 'Self',
  swarmId: 'swarm-self',
  targetKind: 'host',
  targetRelationship: 'self',
  hostSwarmId: 'swarm-self',
  hostSwarmName: 'Host',
  hostWorkspacePath: '/workspace-a',
  hostWorkspaceName: 'workspace-a',
  runtimeWorkspacePath: '/workspace-a',
  workspaceBindingId: 'binding-self',
  workspaceName: 'workspace-a',
}

function withSessionStorage(run: (storage: Map<string, string>) => void): void {
  const previousWindow = globalThis.window
  const storage = new Map<string, string>()
  globalThis.window = {
    sessionStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
      removeItem: (key: string) => storage.delete(key),
    },
  } as Window & typeof globalThis
  try {
    run(storage)
  } finally {
    globalThis.window = previousWindow
  }
}

test('Existing A completion after navigation does not clear or overwrite existing B retained operation', () => withSessionStorage(() => {
  const operationA = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-a',
    prompt: 'blocked A',
  })
  const operationB = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-b',
    prompt: 'retained B',
  })
  persistDesktopV3ExistingMessageOperation(operationA)
  persistDesktopV3ExistingMessageOperation(operationB)

  let draftA = operationA.request.content
  let draftB = operationB.request.content
  let operationRefB = operationB
  completeDesktopV3ExistingMessage({
    sessionId: 'session-a',
    operation: operationA,
    mountedRef: { current: false },
    setOperation: () => {
      operationRefB = operationA
    },
    setDraft: (nextDraft) => {
      draftA = nextDraft
      draftB = nextDraft
    },
  })

  assert.equal(loadDesktopV3ExistingMessageOperation('session-a'), null)
  assert.equal(draftA, operationA.request.content)
  assert.equal(loadDesktopV3ExistingMessageOperation('session-b')?.operationId, operationB.operationId)
  assert.equal(operationRefB.operationId, operationB.operationId)
  assert.equal(draftB, operationB.request.content)
}))

test('Existing message completion clears the mounted composer draft after send', () => withSessionStorage(() => {
  const operation = createDesktopV3ExistingMessageOperation({
    sessionId: 'session-a',
    prompt: 'sent text',
  })
  persistDesktopV3ExistingMessageOperation(operation)

  let draft = operation.request.content
  let operationRef: typeof operation | null = operation
  completeDesktopV3ExistingMessage({
    sessionId: 'session-a',
    operation,
    mountedRef: { current: true },
    setOperation: (nextOperation) => {
      operationRef = nextOperation
    },
    setDraft: (nextDraft) => {
      draft = nextDraft
    },
  })

  assert.equal(loadDesktopV3ExistingMessageOperation('session-a'), null)
  assert.equal(operationRef, null)
  assert.equal(draft, '')
}))

test('Workspace A creation completion after navigation does not navigate away from workspace B or clear B retained operation', () => withSessionStorage(() => {
  const operationA = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace-a',
    workspaceName: 'workspace-a',
    route,
    prompt: 'blocked A',
    agentName: 'swarm',
  })
  const operationB = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace-b',
    workspaceName: 'workspace-b',
    route: {
      ...route,
      hostWorkspacePath: '/workspace-b',
      runtimeWorkspacePath: '/workspace-b',
      workspaceName: 'workspace-b',
    },
    prompt: 'retained B',
    agentName: 'swarm',
  })
  persistDesktopV3NewSessionOperation(operationA)
  persistDesktopV3NewSessionOperation(operationB)

  let draftA = operationA.firstMessageRequest.content
  let visibleWorkspacePath = '/workspace-b'
  let operationRefB = operationB
  const navigations: string[] = []
  completeDesktopV3NewSessionStarted({
    workspacePath: '/workspace-a',
    operation: operationA,
    mountedRef: { current: false },
    setOperation: () => {
      operationRefB = operationA
    },
    setDraft: (nextDraft) => {
      draftA = nextDraft
    },
    navigateToSession: (sessionId) => {
      visibleWorkspacePath = '/workspace-a'
      navigations.push(sessionId)
    },
  })

  assert.equal(loadDesktopV3NewSessionOperation('/workspace-a'), null)
  assert.equal(draftA, operationA.firstMessageRequest.content)
  assert.equal(loadDesktopV3NewSessionOperation('/workspace-b')?.operationId, operationB.operationId)
  assert.equal(operationRefB.operationId, operationB.operationId)
  assert.equal(visibleWorkspacePath, '/workspace-b')
  assert.deepEqual(navigations, [])
}))

test('New session completion clears the mounted composer draft after send', () => withSessionStorage(() => {
  const operation = createDesktopV3NewSessionOperation({
    workspacePath: '/workspace-a',
    workspaceName: 'workspace-a',
    route,
    prompt: 'sent text',
    agentName: 'swarm',
  })
  persistDesktopV3NewSessionOperation(operation)

  let draft = operation.firstMessageRequest.content
  let operationRef: typeof operation | null = operation
  const navigations: string[] = []
  completeDesktopV3NewSessionStarted({
    workspacePath: '/workspace-a',
    operation,
    mountedRef: { current: true },
    setOperation: (nextOperation) => {
      operationRef = nextOperation
    },
    setDraft: (nextDraft) => {
      draft = nextDraft
    },
    navigateToSession: (sessionId) => {
      navigations.push(sessionId)
    },
  })

  assert.equal(loadDesktopV3NewSessionOperation('/workspace-a'), null)
  assert.equal(operationRef, null)
  assert.equal(draft, '')
  assert.deepEqual(navigations, [operation.sessionId])
}))


test('Desktop V3 stop resolves required target_swarm_id from the selected primary route', () => {
  assert.deepEqual(resolveDesktopV3StopRunRequest({ route, runId: ' run-1 ' }), {
    runId: 'run-1',
    targetSwarmId: 'swarm-self',
  })
})

test('Desktop V3 stop rejects unsupported routes instead of calling another stop path', () => {
  assert.throws(
    () => resolveDesktopV3StopRunRequest({
      route: { ...route, swarmId: '', targetRelationship: 'self', targetKind: 'host' },
      runId: 'run-1',
    }),
    /Sessions API stop requires a selected primary swarm_id/,
  )
  assert.throws(
    () => resolveDesktopV3StopRunRequest({
      route: { ...route, swarmId: 'child-swarm', targetRelationship: 'child', targetKind: 'container' },
      runId: 'run-1',
    }),
    /Desktop stop only supports the primary self V3 target/,
  )
})

test('Desktop V3 plan lifecycle messages render as conversation breaks', () => {
  const message = {
    id: 'plan-break-1',
    session_id: 'session-a',
    global_seq: 7,
    role: 'system',
    content: 'Checkpoint started\nPlan: Demo plan (plan-1)\nCheckpoint: cp-1 Build UI\nFresh context: previous checkpoint context cleared for this run.',
    metadata: { source: 'plan_execution_lifecycle', kind: 'plan_execution_break', action: 'start_checkpoint', execution_status: 'in_progress' },
    created_at: 7,
  }

  assert.equal(isDesktopV3PlanExecutionBreakMessage(message), true)
  assert.equal(isDesktopV3PlanFinalHandoffMessage(message), false)
  const items = buildDesktopV3ConversationRenderItems({ committed: [message], pendingUser: [], liveRuns: [], runIntents: [] })
  assert.equal(items[0]?.type, 'plan-break')
  if (items[0]?.type === 'plan-break') {
    assert.equal(items[0].headline, 'Checkpoint started')
    assert.equal(items[0].tone, 'primary')
    assert.equal(items[0].details.includes('Checkpoint: cp-1 Build UI'), true)
    const markup = renderToStaticMarkup(createElement(DesktopV3RenderItemView, {
      item: items[0], thinkingTagsEnabled: true, timerNow: 0, index: 0,
    }))
    assert.match(markup, /data-plan-transition-tone="primary"/)
    assert.match(markup, /Plan transition/)
    assert.match(markup, /Checkpoint started/)
    assert.doesNotMatch(markup, /rounded-2xl/)
    assert.doesNotMatch(markup, /text-center/)
  }
})


test('Desktop V3 intermediate checkpoint handoff preserves global sequence and uses message rendering', () => {
  const laterMessage = {
    id: 'assistant-after-handoff',
    session_id: 'session-a',
    global_seq: 12,
    role: 'assistant',
    content: 'Next checkpoint started.',
    created_at: 12,
  }
  const handoff = {
    id: 'plan-handoff-checkpoint',
    session_id: 'session-a',
    global_seq: 11,
    role: 'system',
    content: 'Checkpoint handoff\n\nReport:\n- API complete\n\n<swarm-handoff-summary>\nAPI complete; continuing to the next checkpoint.\n</swarm-handoff-summary>\n\nResult: continuing',
    metadata: { source: 'plan_execution_checkpoint_handoff', kind: 'plan_checkpoint_handoff' },
    created_at: 11,
  }

  assert.equal(isDesktopV3PlanExecutionBreakMessage(handoff), false)
  assert.equal(isDesktopV3PlanCheckpointHandoffMessage(handoff), true)
  const items = buildDesktopV3ConversationRenderItems({
    committed: [laterMessage, handoff],
    pendingUser: [],
    liveRuns: [],
    runIntents: [],
  })
  assert.deepEqual(items.map((item) => item.type), ['plan-checkpoint-handoff', 'message'])
  assert.equal(items[0]?.timelineSeq, 11)
  if (items[0]?.type === 'plan-checkpoint-handoff') {
    const markup = renderToStaticMarkup(createElement(DesktopV3RenderItemView, {
      item: items[0], thinkingTagsEnabled: true, timerNow: 0, index: 0,
    }))
    assert.match(markup, /data-testid="desktop-v3-plan-checkpoint-handoff"/)
    assert.doesNotMatch(markup, /desktop-v3-plan-execution-break/)
    assert.equal(items[0].summary, 'API complete; continuing to the next checkpoint.')
    assert.doesNotMatch(items[0].body, /swarm-handoff-summary/)
    assert.match(markup, /data-testid="desktop-v3-plan-checkpoint-handoff-summary"/)
    assert.match(markup, /At a glance/)
    assert.match(markup, /API complete/)
  }
})


test('Desktop V3 handoff summary parser extracts the copied durable followup shape without mutating source content', () => {
  const content = 'The last checkpoint is complete. No additional checkpoint will start unless the user explicitly requests it.\n\nReport:\n## Outcome\nThis durable report contains exactly one non-empty `<swarm-handoff-summary>` block near the bottom.\n\n<swarm-handoff-summary>\n**Outcome:** Follow-up checkpoint `followup-3` is ready for review.\n\n**Next action:** Review the final handoff.\n</swarm-handoff-summary>\n\nResult: checkpoint completed successfully\n\nValidation:\n- Confirmed the report contains exactly one non-empty <swarm-handoff-summary> block.'
  const parsed = parseDesktopV3HandoffSummary(content)
  assert.equal(parsed.summary, '**Outcome:** Follow-up checkpoint `followup-3` is ready for review.\n\n**Next action:** Review the final handoff.')
  assert.equal(parsed.body, 'The last checkpoint is complete. No additional checkpoint will start unless the user explicitly requests it.\n\nReport:\n## Outcome\nThis durable report contains exactly one non-empty `<swarm-handoff-summary>` block near the bottom.\n\nResult: checkpoint completed successfully\n\nValidation:\n- Confirmed the report contains exactly one non-empty <swarm-handoff-summary> block.')
  assert.equal(content.includes('<swarm-handoff-summary>'), true)
})


test('Desktop V3 handoff summary parser keeps absent malformed duplicated and fenced contracts readable', () => {
  const cases = [
    'Report only',
    'Before <swarm-handoff-summary>unfinished',
    '<swarm-handoff-summary>\none\n</swarm-handoff-summary>\n<swarm-handoff-summary>\ntwo\n</swarm-handoff-summary>',
    '```xml\n<swarm-handoff-summary>example</swarm-handoff-summary>\n```\nReport remains',
    '~~~\n<swarm-handoff-summary>example</swarm-handoff-summary>\n~~~',
  ]
  for (const content of cases) {
    assert.deepEqual(parseDesktopV3HandoffSummary(content), { body: content, summary: '' })
  }
})


test('Desktop V3 final checkpoint handoff suppresses its redundant lifecycle review card', () => {
  const lifecycle = {
    id: 'plan-break-final',
    session_id: 'session-a',
    global_seq: 7,
    role: 'system',
    content: 'All checkpoints complete; review required — Automatic mode\nPlan: Demo plan (plan-1)\nCompleted: Checkpoint 2 — UI\nNext: all checkpoints are complete; waiting for user review.',
    metadata: {
      source: 'plan_execution_lifecycle',
      kind: 'plan_execution_break',
      action: 'complete_checkpoint',
      next_action: 'await_review',
      plan_id: 'plan-1',
      checkpoint_id: 'cp-2',
    },
    created_at: 7,
  }
  const handoff = {
    id: 'plan-handoff-final',
    session_id: 'session-a',
    global_seq: 401,
    role: 'system',
    content: 'Final checkpoint handoff\n\nThe last checkpoint is complete. No additional checkpoint will start unless the user explicitly requests it.\n\nReport:\n## Outcome\nThis durable report contains exactly one non-empty `<swarm-handoff-summary>` block near the bottom.\n\n<swarm-handoff-summary>\n**Done** — ready to review.\n</swarm-handoff-summary>\n\nResult: **done**\nValidation:\n- Confirmed the report contains exactly one non-empty <swarm-handoff-summary> block.',
    metadata: {
      source: 'plan_execution_final_handoff',
      kind: 'plan_final_checkpoint_handoff',
      plan_id: 'plan-1',
      checkpoint_id: 'cp-2',
      recommendation: { decision: 'ship', action: 'review', reason: 'complete', action_state: 'ready' },
    },
    created_at: 8,
  }

  assert.equal(isDesktopV3PlanExecutionBreakMessage(handoff), false)
  assert.equal(isDesktopV3PlanFinalHandoffMessage(handoff), true)
  const items = buildDesktopV3ConversationRenderItems({ committed: [lifecycle, handoff], pendingUser: [], liveRuns: [], runIntents: [] })
  assert.deepEqual(items.map((item) => item.type), ['plan-final-handoff'])
  if (items[0]?.type === 'plan-final-handoff') {
    assert.equal(items[0].headline, 'Final checkpoint handoff')
    assert.equal(items[0].summary, '**Done** — ready to review.')
    assert.match(items[0].body, /Report:\n## Outcome/)
    assert.match(items[0].body, /This durable report contains exactly one non-empty `<swarm-handoff-summary>` block/)
    assert.match(items[0].body, /Result: \*\*done\*\*/)
    assert.match(items[0].body, /Validation:\n- Confirmed the report contains exactly one non-empty <swarm-handoff-summary> block\./)
    assert.equal(
      items[0].body
        .split(/\r?\n/)
        .some((line) => line.trim() === '<swarm-handoff-summary>' || line.trim() === '</swarm-handoff-summary>'),
      false,
    )
    assert.doesNotMatch(items[0].body, /Markdown is supported in this handoff/)
    assert.equal(items[0].message.content, handoff.content)
    assert.deepEqual(items[0].message.metadata?.recommendation, handoff.metadata.recommendation)
    const markup = renderToStaticMarkup(createElement(DesktopV3RenderItemView, {
      item: items[0], thinkingTagsEnabled: true, timerNow: 0, index: 0,
    }))
    assert.match(markup, /aria-label="At a glance"/)
    assert.equal((markup.match(/>At a glance</g) ?? []).length, 1)
    assert.match(markup, /<strong><span>Done<\/span><\/strong>/)
    assert.match(markup, /ready to review/)
    assert.equal((markup.match(/data-testid="desktop-v3-plan-final-handoff-summary"/g) ?? []).length, 1)
  }
})


test('Desktop V3 keeps intermediate and explicit checkpoint review cards', () => {
  const cases = [
    {
      id: 'plan-break-intermediate-review',
      action: 'complete_checkpoint',
      content: 'Checkpoint complete — Review each checkpoint\nPlan: Demo plan\nCompleted: Checkpoint 1 — API\nNext: waiting for checkpoint review.',
    },
    {
      id: 'plan-break-explicit-review',
      action: 'mark_needs_review',
      content: 'Checkpoint paused for review — Automatic mode\nPlan: Demo plan\nCheckpoint: Checkpoint 1 — API\nNext: waiting for checkpoint review.',
    },
  ]

  for (const [index, fixture] of cases.entries()) {
    const message = {
      id: fixture.id,
      session_id: 'session-a',
      global_seq: index + 1,
      role: 'system',
      content: fixture.content,
      metadata: {
        source: 'plan_execution_lifecycle',
        kind: 'plan_execution_break',
        action: fixture.action,
        next_action: 'await_review',
        plan_id: 'plan-1',
        checkpoint_id: 'cp-1',
      },
      created_at: index + 1,
    }
    const items = buildDesktopV3ConversationRenderItems({ committed: [message], pendingUser: [], liveRuns: [], runIntents: [] })
    assert.deepEqual(items.map((item) => item.type), ['plan-break'])
    const markup = renderToStaticMarkup(createElement(DesktopV3RenderItemView, {
      item: items[0], thinkingTagsEnabled: true, timerNow: 0, index,
    }))
    assert.match(markup, /data-testid="desktop-v3-checkpoint-review-card"/)
  }
})


test('Desktop V3 structured final handoff is compact, keeps evidence collapsed, and exposes inert chat suggestions', () => {
  const prompt = 'Review the completed Desktop handoff.'
  const handoff = {
    id: 'plan-handoff-structured',
    session_id: 'session-a',
    global_seq: 402,
    role: 'system',
    content: 'Ready to ship\n\nThe Desktop handoff is complete.',
    metadata: {
      source: 'plan_execution_final_handoff',
      kind: 'plan_final_checkpoint_handoff',
      checkpoint_id: 'cp-2',
      outcome: 'completed',
      final_handoff: {
        schema_version: 1,
        title: 'Ready to ship',
        overview: 'The Desktop now shows one compact final focus card.',
        impact_bullets: ['Full evidence is opt-in', 'Suggestions remain ordinary messages'],
        recommendation: { decision: 'ship', action: 'review', reason: 'Acceptance criteria are met', action_state: 'ready' },
        suggested_prompts: [{ label: 'Review changes', prompt }],
        details: {
          report: '## Full report\nSensitive-to-space **Markdown** evidence.',
          result: 'done',
          changed_files: ['web/src/final.tsx'],
          validation: ['focused test passed'],
        },
      },
    },
    created_at: 9,
  }

  const items = buildDesktopV3ConversationRenderItems({ committed: [handoff], pendingUser: [], liveRuns: [], runIntents: [] })
  assert.equal(items[0]?.type, 'plan-final-handoff')
  if (items[0]?.type !== 'plan-final-handoff') return
  assert.equal(items[0].finalHandoff?.overview, 'The Desktop now shows one compact final focus card.')
  assert.equal(items[0].body, '')
  assert.equal(items[0].summary, '')

  let selectedPrompt = ''
  const element = createElement(DesktopV3RenderItemView, {
    item: items[0], thinkingTagsEnabled: true, timerNow: 0, index: 0,
    onSuggestedPrompt: (selected: string) => { selectedPrompt = selected },
  })
  const markup = renderToStaticMarkup(element)
  assert.match(markup, /data-testid="desktop-v3-structured-final-handoff"/)
  assert.match(markup, /Ready to ship/)
  assert.match(markup, /The Desktop now shows one compact final focus card/)
  assert.match(markup, /Recommendation/)
  assert.match(markup, /Next steps/)
  assert.equal((markup.match(/<details>/g) ?? []).length, 3)
  assert.match(markup, /<summary[^>]*>Details<\/summary>/)
  assert.match(markup, /<summary[^>]*>Files \(1\)<\/summary>/)
  assert.match(markup, /<summary[^>]*>Validation \(1\)<\/summary>/)
  assert.doesNotMatch(markup, /<details open/)
  assert.match(markup, />Full report</)
  assert.match(markup, />Markdown</)
  assert.match(markup, /web\/src\/final\.tsx/)
  assert.match(markup, /focused test passed/)
  assert.doesNotMatch(markup, /swarm-handoff-summary/)
  assert.equal((markup.match(/Recommendation/g) ?? []).length, 1)

  selectDesktopV3SuggestedPrompt(prompt, (selected) => { selectedPrompt = selected })
  assert.equal(selectedPrompt, prompt)
  assert.equal(Object.hasOwn(handoff.metadata, 'action'), false)
  assert.equal(Object.hasOwn(handoff.metadata, 'tool'), false)
  assert.equal(Object.hasOwn(handoff.metadata, 'command'), false)
})


test('Desktop V3 blocked checkpoint handoff renders as one standalone handoff', () => {
  const handoff = {
    id: 'plan-handoff-blocked',
    session_id: 'session-a',
    global_seq: 10,
    role: 'system',
    content: 'Blocked checkpoint handoff\n\nStatus: BLOCKED\nPlan: Demo plan\nCheckpoint: Checkpoint 1 — API\nResolution required: resolve the named external dependency, input, or permission in the report before continuing checkpoint execution.\n\nReport:\n## Blocker\n- waiting on dependency\nResult: blocked\nValidation:\n- not run; blocked by dependency',
    metadata: { source: 'plan_execution_blocked_handoff', kind: 'plan_blocked_checkpoint_handoff' },
    created_at: 10,
  }

  assert.equal(isDesktopV3PlanExecutionBreakMessage(handoff), false)
  assert.equal(isDesktopV3PlanFinalHandoffMessage(handoff), false)
  assert.equal(isDesktopV3PlanBlockedHandoffMessage(handoff), true)
  const items = buildDesktopV3ConversationRenderItems({ committed: [handoff], pendingUser: [], liveRuns: [], runIntents: [] })
  assert.deepEqual(items.map((item) => item.type), ['plan-blocked-handoff'])
  if (items[0]?.type === 'plan-blocked-handoff') {
    assert.equal(items[0].headline, 'Blocked checkpoint handoff')
    assert.equal(items[0].summary, '')
    assert.match(items[0].body, /Status: BLOCKED/)
    assert.match(items[0].body, /Plan: Demo plan/)
    assert.match(items[0].body, /Checkpoint: Checkpoint 1 — API/)
    assert.match(items[0].body, /Resolution required:/)
    assert.match(items[0].body, /Report:\n## Blocker\n- waiting on dependency/)
    assert.match(items[0].body, /Result: blocked/)
    assert.match(items[0].body, /Validation:\n- not run; blocked by dependency/)
    const markup = renderToStaticMarkup(createElement(DesktopV3RenderItemView, {
      item: items[0], thinkingTagsEnabled: true, timerNow: 0, index: 0,
    }))
    assert.match(markup, /data-testid="desktop-v3-plan-blocked-handoff"/)
    assert.doesNotMatch(markup, /At a glance/)
  }
})


test('Desktop V3 committed tool message reuses matching live tool render key', () => {
  const liveItems = buildDesktopV3LiveRunRenderItems({
    sessionId: 'session-a',
    runId: 'run-a',
    status: 'running',
    toolCallsByCallId: {
      'call-1': {
        callId: 'call-1',
        toolInstanceId: 'tool-instance-1',
        toolName: 'search',
        outputText: '{"summary":"done"}',
        status: 'completed',
        updatedAt: 40,
        timelineSeq: 4,
      },
    },
    lastEventSeqSeen: 4,
  })
  const committedItems = buildDesktopV3ConversationRenderItems({
    committed: [{
      id: 'msg-tool-1',
      session_id: 'session-a',
      global_seq: 5,
      role: 'tool',
      content: JSON.stringify({
        path_id: 'run.tool-history.v2',
        run_id: 'run-a',
        call_id: 'call-1',
        tool_instance_id: 'tool-instance-1',
        tool: 'search',
        output: '{"summary":"done"}',
      }),
      created_at: 50,
    }],
    pendingUser: [],
    liveRuns: [],
    runIntents: [],
  })

  assert.equal(liveItems[0]?.type, 'live-tool')
  assert.equal(committedItems[0]?.type, 'message')
  assert.equal(desktopV3RenderItemKey(committedItems[0]), desktopV3RenderItemKey(liveItems[0]))
})


test('Desktop V3 new user message follows the active stream instead of sorting by wall-clock time', () => {
  const items = buildDesktopV3ConversationRenderItems({
    committed: [{
      id: 'msg-user-1',
      session_id: 'session-a',
      global_seq: 1,
      role: 'user',
      content: 'first turn',
      created_at: 1,
    }],
    pendingUser: [{
      clientRequestId: 'client-next-turn',
      messageId: 'msg-next-turn',
      sessionId: 'session-a',
      role: 'user',
      content: 'next turn',
      runId: 'run-next-turn',
      createdAt: 1_000_000,
      timelineSeq: 6,
      status: 'pending',
    }],
    liveRuns: [{
      sessionId: 'session-a',
      runId: 'run-a',
      status: 'running',
      assistantDraft: { content: 'streaming answer', updatedAt: 50, timelineSeq: 5 },
      toolCallsByCallId: {},
      lastEventSeqSeen: 5,
    }],
    runIntents: [],
  })

  assert.deepEqual(items.map((item) => item.type), ['message', 'live-assistant', 'pending-user'])
  assert.equal(desktopV3RenderItemKey(items[2]!), 'msg-next-turn')
})

test('Desktop V3 paused run resend stays ordered through commit and delayed stale-run updates', () => {
  const oldRun = {
    sessionId: 'session-a',
    runId: 'run-paused',
    status: 'cancelled' as const,
    assistantDraft: { content: 'old paused answer', updatedAt: 20, timelineSeq: 2 },
    toolCallsByCallId: {},
    lastEventSeqSeen: 2,
  }
  const pendingUser = {
    clientRequestId: 'client-after-pause',
    messageId: 'msg-after-pause',
    sessionId: 'session-a',
    role: 'user' as const,
    content: 'continue after pause',
    runId: 'run-after-pause',
    createdAt: 30,
    timelineSeq: 3,
    status: 'pending' as const,
  }
  const pendingItems = buildDesktopV3ConversationRenderItems({
    committed: [{
      id: 'msg-initial', session_id: 'session-a', global_seq: 1,
      role: 'user', content: 'initial', created_at: 1,
    }],
    pendingUser: [pendingUser],
    liveRuns: [oldRun],
    runIntents: [],
  })
  const committedMessage = {
    id: pendingUser.messageId,
    session_id: 'session-a',
    global_seq: 3,
    role: 'user' as const,
    content: pendingUser.content,
    created_at: pendingUser.createdAt,
  }
  const committedItems = buildDesktopV3ConversationRenderItems({
    committed: [{
      id: 'msg-initial', session_id: 'session-a', global_seq: 1,
      role: 'user', content: 'initial', created_at: 1,
    }, committedMessage],
    pendingUser: [],
    liveRuns: [{
      ...oldRun,
      assistantDraft: { content: 'delayed old-run update', updatedAt: 40, timelineSeq: 2 },
      lastEventSeqSeen: 4,
    }],
    runIntents: [],
  })

  assert.deepEqual(pendingItems.map((item) => item.type), ['message', 'live-assistant', 'pending-user'])
  assert.deepEqual(committedItems.map((item) => item.type), ['message', 'live-assistant', 'message'])
  assert.equal(desktopV3RenderItemKey(pendingItems.at(-1)!), pendingUser.messageId)
  assert.equal(desktopV3RenderItemKey(committedItems.at(-1)!), pendingUser.messageId)
  assert.equal(committedItems.at(-1)?.type, 'message')
})

test('Desktop V3 sticky-bottom attachment requires upward intent to cross the buffered release zone', () => {
  const resolve = (bottomDistance: number, wasAttached: boolean, userEscapeIntent = false) =>
    resolveDesktopV3StickyBottomAttachment({ bottomDistance, wasAttached, userEscapeIntent, bottomBufferPx: 140 })

  assert.equal(resolve(0, true), true)
  assert.equal(resolve(140, true, true), true)
  assert.equal(resolve(141, true, true), false)
  assert.equal(resolve(500, true), true, 'layout growth cannot release an attached transcript')
  assert.equal(resolve(140, false), false)
  assert.equal(resolve(2, false), false)
  assert.equal(resolve(0, false), true)
})

test('Desktop V3 sticky-bottom lifecycle is intent-aware and released state controls jump-to-latest', () => {
  const hookStart = conversationPaneSource.indexOf('export function useDesktopV3StickyBottomScroll')
  const hookEnd = conversationPaneSource.indexOf('function numericTimelineSeq', hookStart)
  assert.ok(hookStart >= 0 && hookEnd > hookStart, 'expected sticky-bottom hook source')
  const hookSource = conversationPaneSource.slice(hookStart, hookEnd)

  assert.doesNotMatch(hookSource, /forceFollow|ACTIVITY_FOLLOW_MS|userDetachedRef|smoothFollowUntilRef/)
  assert.match(hookSource, /userEscapeIntent: userEscapeIntentRef\.current/)
  assert.match(hookSource, /addEventListener\("wheel"/)
  assert.match(hookSource, /addEventListener\("touchmove"/)
  assert.match(hookSource, /addEventListener\("pointerdown"/)
  assert.match(hookSource, /addEventListener\("keydown"/)
  assert.match(hookSource, /element\.scrollTo\(\{ top: element\.scrollTop, behavior: "auto" \}\)/)
  assert.match(hookSource, /autoFollowRef\.current = false/)
  assert.match(hookSource, /cancelScheduledScroll\(\)/)
  assert.match(conversationPaneSource, /scrollToBottom\("smooth"\)/)
  assert.match(conversationPaneSource, /preserveScrollPositionForPrepend\(\)/)
  assert.match(conversationPaneSource, /data-testid="desktop-chat-tail-anchor"/)
  assert.match(conversationPaneSource, /\[overflow-anchor:none\]/)
  assert.match(conversationPaneSource, /\{!isAtBottom \? \(/)
  assert.doesNotMatch(conversationPaneSource, /!isAtBottom && hasUnseenLatest/)
})

test('Desktop V3 pending user keeps its render key when the canonical message arrives', () => {
  const pendingItems = buildDesktopV3ConversationRenderItems({
    committed: [],
    pendingUser: [{
      clientRequestId: 'client-next-turn',
      messageId: 'msg-next-turn',
      sessionId: 'session-a',
      role: 'user',
      content: 'next turn',
      runId: 'run-next-turn',
      createdAt: 10,
      timelineSeq: 2,
      status: 'pending',
    }],
    liveRuns: [],
    runIntents: [],
  })
  const committedMessage = {
    id: 'msg-next-turn',
    session_id: 'session-a',
    global_seq: 2,
    role: 'user' as const,
    content: 'next turn',
    created_at: 10,
  }
  const committedItems = buildDesktopV3ConversationRenderItems({
    committed: [committedMessage],
    pendingUser: [],
    liveRuns: [],
    runIntents: [],
  })
  const overlappingItems = buildDesktopV3ConversationRenderItems({
    committed: [committedMessage],
    pendingUser: pendingItems[0]?.type === 'pending-user' ? [pendingItems[0].message] : [],
    liveRuns: [],
    runIntents: [],
  })

  assert.equal(desktopV3RenderItemKey(pendingItems[0]!), desktopV3RenderItemKey(committedItems[0]!))
  assert.deepEqual(overlappingItems.map((item) => item.type), ['pending-user'])
})

test('Desktop V3 live run render items preserve backend event order', () => {
  const items = buildDesktopV3LiveRunRenderItems({
    sessionId: 'session-a',
    runId: 'run-a',
    status: 'running',
    assistantDraft: { content: 'answer', updatedAt: 50, timelineSeq: 5 },
    toolCallsByCallId: {
      'call-1': { callId: 'call-1', toolName: 'search', updatedAt: 40, timelineSeq: 4 },
    },
    reasoning: {
      key: 'reasoning-1',
      state: 'running',
      summary: '',
      text: 'thinking',
      startedAt: 30,
      updatedAt: 30,
      timelineSeq: 3,
    },
    reasoningByKey: {
      'reasoning-1': {
        key: 'reasoning-1',
        state: 'running',
        summary: '',
        text: 'thinking',
        startedAt: 30,
        updatedAt: 30,
        timelineSeq: 3,
      },
    },
    lastEventSeqSeen: 5,
  })

  assert.deepEqual(items.map((item) => item.type), ['live-reasoning', 'live-tool', 'live-assistant'])
})

function agentProfile(overrides: Partial<AgentProfileRecord>): AgentProfileRecord {
  return {
    name: 'swarm',
    mode: 'primary',
    description: '',
    provider: '',
    model: '',
    thinking: '',
    modelMode: 'single',
    planProvider: '',
    planModel: '',
    planThinking: '',
    planServiceTier: '',
    autoProvider: '',
    autoModel: '',
    autoThinking: '',
    autoServiceTier: '',
    prompt: '',
    runtimeMode: 'plan_auto',
    defaultSessionMode: 'plan',
    executionSetting: '',
    exitPlanModeEnabled: true,
    toolScope: null,
    toolContract: null,
    enabled: true,
    protected: false,
    updatedAt: 0,
    ...overrides,
  }
}

test('Desktop V3 agent model lock is derived synchronously from loaded agent profiles', () => {
  const locked = resolveDesktopV3AgentModelLock([
    agentProfile({ name: 'swarm', provider: 'codex', model: 'gpt-5.4', thinking: 'high', autoServiceTier: 'fast' }),
    agentProfile({ name: 'default-agent', provider: '', model: '', thinking: '' }),
  ], 'swarm')

  assert.equal(locked.locked, true)
  assert.equal(locked.provider, 'codex')
  assert.equal(locked.model, 'gpt-5.4')
  assert.equal(locked.thinking, 'high')
  assert.equal(locked.serviceTier, 'fast')
  assert.equal(locked.disabledReason, '')

  const unlocked = resolveDesktopV3AgentModelLock([
    agentProfile({ name: 'default-agent', provider: '', model: '', thinking: '' }),
  ], 'default-agent')

  assert.equal(unlocked.locked, false)
  assert.equal(unlocked.disabledReason, '')
})

test('Desktop V3 split agent model lock resolves by composer mode', () => {
  const profiles = [
    agentProfile({
      name: 'swarm',
      modelMode: 'split',
      planProvider: 'codex',
      planModel: 'gpt-5.4',
      planThinking: 'high',
      planServiceTier: 'fast',
      autoProvider: 'openai',
      autoModel: 'gpt-5.5',
      autoThinking: 'medium',
      autoServiceTier: '',
    }),
  ]

  const plan = resolveDesktopV3AgentModelLock(profiles, 'swarm', 'plan')
  assert.equal(plan.locked, true)
  assert.equal(plan.provider, 'codex')
  assert.equal(plan.model, 'gpt-5.4')
  assert.equal(plan.thinking, 'high')
  assert.equal(plan.serviceTier, 'fast')
  assert.equal(plan.disabledReason, '')

  const auto = resolveDesktopV3AgentModelLock(profiles, 'swarm', 'auto')
  assert.equal(auto.locked, true)
  assert.equal(auto.provider, 'openai')
  assert.equal(auto.model, 'gpt-5.5')
  assert.equal(auto.thinking, 'medium')
  assert.equal(auto.serviceTier, '')
  assert.equal(auto.disabledReason, '')
})
