import test from 'node:test'
import assert from 'node:assert/strict'

import {
  buildDesktopV3ConversationRenderItems,
  buildDesktopV3LiveRunRenderItems,
  desktopV3RenderItemKey,
  isDesktopV3PlanBlockedHandoffMessage,
  isDesktopV3PlanExecutionBreakMessage,
  isDesktopV3PlanFinalHandoffMessage,
  completeDesktopV3ExistingMessage,
  resolveDesktopV3StopRunRequest,
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
    metadata: { source: 'plan_execution_lifecycle', kind: 'plan_execution_break' },
    created_at: 7,
  }

  assert.equal(isDesktopV3PlanExecutionBreakMessage(message), true)
  assert.equal(isDesktopV3PlanFinalHandoffMessage(message), false)
  const items = buildDesktopV3ConversationRenderItems({ committed: [message], pendingUser: [], liveRuns: [], runIntents: [] })
  assert.equal(items[0]?.type, 'plan-break')
  if (items[0]?.type === 'plan-break') {
    assert.equal(items[0].headline, 'Checkpoint started')
    assert.equal(items[0].details.includes('Checkpoint: cp-1 Build UI'), true)
  }
})


test('Desktop V3 final checkpoint handoff renders separately after lifecycle break', () => {
  const lifecycle = {
    id: 'plan-break-final',
    session_id: 'session-a',
    global_seq: 7,
    role: 'system',
    content: 'All checkpoints complete; review required — Automatic mode\nPlan: Demo plan (plan-1)\nCompleted: Checkpoint 2 — UI\nNext: all checkpoints are complete; waiting for user review.',
    metadata: { source: 'plan_execution_lifecycle', kind: 'plan_execution_break' },
    created_at: 7,
  }
  const handoff = {
    id: 'plan-handoff-final',
    session_id: 'session-a',
    global_seq: 8,
    role: 'system',
    content: 'Final checkpoint handoff\n\nThe last checkpoint is complete. No additional checkpoint will start unless the user explicitly requests it.\n\nReport:\n## Summary\n- rendered separately\nResult: **done**\nValidation:\n- focused render regression',
    metadata: { source: 'plan_execution_final_handoff', kind: 'plan_final_checkpoint_handoff' },
    created_at: 8,
  }

  assert.equal(isDesktopV3PlanExecutionBreakMessage(handoff), false)
  assert.equal(isDesktopV3PlanFinalHandoffMessage(handoff), true)
  const items = buildDesktopV3ConversationRenderItems({ committed: [lifecycle, handoff], pendingUser: [], liveRuns: [], runIntents: [] })
  assert.deepEqual(items.map((item) => item.type), ['plan-break', 'plan-final-handoff'])
  if (items[0]?.type === 'plan-break') {
    assert.equal(items[0].details.some((detail) => detail.includes('Report: rendered separately')), false)
    assert.equal(items[0].details.includes('Next: all checkpoints are complete; waiting for user review.'), true)
  }
  if (items[1]?.type === 'plan-final-handoff') {
    assert.equal(items[1].headline, 'Final checkpoint handoff')
    assert.match(items[1].body, /Report:\n## Summary\n- rendered separately/)
    assert.match(items[1].body, /Result: \*\*done\*\*/)
    assert.match(items[1].body, /Validation:\n- focused render regression/)
    assert.doesNotMatch(items[1].body, /Markdown is supported in this handoff/)
  }
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
    assert.match(items[0].body, /Status: BLOCKED/)
    assert.match(items[0].body, /Plan: Demo plan/)
    assert.match(items[0].body, /Checkpoint: Checkpoint 1 — API/)
    assert.match(items[0].body, /Resolution required:/)
    assert.match(items[0].body, /Report:\n## Blocker\n- waiting on dependency/)
    assert.match(items[0].body, /Result: blocked/)
    assert.match(items[0].body, /Validation:\n- not run; blocked by dependency/)
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
  assert.match(locked.disabledReason, /update the model in Settings → Agents/)

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
  assert.match(plan.disabledReason, /update the plan model in Settings → Agents/)

  const auto = resolveDesktopV3AgentModelLock(profiles, 'swarm', 'auto')
  assert.equal(auto.locked, true)
  assert.equal(auto.provider, 'openai')
  assert.equal(auto.model, 'gpt-5.5')
  assert.equal(auto.thinking, 'medium')
  assert.equal(auto.serviceTier, '')
  assert.match(auto.disabledReason, /update the auto model in Settings → Agents/)
})
