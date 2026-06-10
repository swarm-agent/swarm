import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopLiveAssistantSegment, DesktopSessionRecord } from '../../types/realtime'
import type { ChatMessageRecord } from '../types/chat'
import { buildLiveToolMessages, dedupeMessages, desktopChatVirtualItemKey, formatAgentTodoBadge, formatMobileAgentTodoBadge, imageSidebarStateFromToolMessage, isDesktopCompactionCheckpointMessage, isDesktopManualCompactionAckMessage, isSilentSpeechRecognitionError, liveAssistantDraftHasCanonicalReplay, metadataTodoSummary, orderDesktopTimelineItems, resolveMessageAssistantLabel, resolveSessionEffectiveAgentName, retainedAssistantSegmentsWithoutCanonicalReplay, savedRuleCountdownSeconds, sessionUsesReadOnlyFlowIdentity, shouldShowScrollLockReturnButton, visibleDesktopChatMessages } from './desktop-chat-panel'

test('formatAgentTodoBadge shows progress-first badge with active count', () => {
  assert.equal(formatAgentTodoBadge({ taskCount: 6, openCount: 2, inProgressCount: 1, activeText: '' }), '4/6 complete • 1 active')
})

test('isSilentSpeechRecognitionError suppresses iOS PWA speech silence aborts', () => {
  assert.equal(isSilentSpeechRecognitionError('', 'The operation couldn’t be completed. (kAFAssistantErrorDomain error 1107.)'), true)
  assert.equal(isSilentSpeechRecognitionError('', 'Error Domain=kAFAssistantErrorDomain Code=1107 "(null)"'), true)
  assert.equal(isSilentSpeechRecognitionError('network', 'Browser speech recognition hit a network error.'), false)
})

test('formatAgentTodoBadge shows complete state when no tasks remain open', () => {
  assert.equal(formatAgentTodoBadge({ taskCount: 6, openCount: 0, inProgressCount: 0, activeText: '' }), 'Complete · 6/6')
})

test('formatAgentTodoBadge shows active todo text when available', () => {
  const summary = { taskCount: 6, openCount: 2, inProgressCount: 1, activeText: 'Validate task badge states on desktop' }

  assert.equal(formatAgentTodoBadge(summary), 'Validate task badge states on desktop')
})

test('formatMobileAgentTodoBadge stays compact when active todo text is available', () => {
  const summary = { taskCount: 6, openCount: 2, inProgressCount: 1, activeText: 'Validate task badge states on mobile' }

  assert.equal(formatMobileAgentTodoBadge(summary), '4/6')
})

test('formatMobileAgentTodoBadge shows state labels at mobile edges', () => {
  assert.equal(formatMobileAgentTodoBadge({ taskCount: 5, openCount: 5, inProgressCount: 1, activeText: 'Start the checklist' }), 'Active')
  assert.equal(formatMobileAgentTodoBadge({ taskCount: 5, openCount: 0, inProgressCount: 0, activeText: '' }), 'Complete')
})

test('metadataTodoSummary reads agent-scoped counts from metadata', () => {
  assert.deepEqual(metadataTodoSummary({
    agent_todo_summary: {
      task_count: 5,
      open_count: 3,
      in_progress_count: 1,
      user: { task_count: 2, open_count: 1, in_progress_count: 0 },
      agent: { task_count: 3, open_count: 2, in_progress_count: 1, active_todo: { text: 'Make mobile badge readable' } },
    },
  }), {
    taskCount: 3,
    openCount: 2,
    inProgressCount: 1,
    activeText: 'Make mobile badge readable',
  })
})

test('savedRuleCountdownSeconds counts down the visible saved-rule notice', () => {
  assert.equal(savedRuleCountdownSeconds(5_000, 0), 5)
  assert.equal(savedRuleCountdownSeconds(5_000, 1), 5)
  assert.equal(savedRuleCountdownSeconds(5_000, 1_001), 4)
  assert.equal(savedRuleCountdownSeconds(5_000, 5_000), 0)
  assert.equal(savedRuleCountdownSeconds(null, 0), 0)
})

test('shouldShowScrollLockReturnButton appears only after scrolling at least halfway away from lock', () => {
  assert.equal(shouldShowScrollLockReturnButton({ scrollHeight: 2_000, scrollTop: 1_625, clientHeight: 360 }), false)
  assert.equal(shouldShowScrollLockReturnButton({ scrollHeight: 2_000, scrollTop: 1_420, clientHeight: 360 }), true)
  assert.equal(shouldShowScrollLockReturnButton({ scrollHeight: 2_000, scrollTop: 1_840, clientHeight: 120 }), false)
  assert.equal(shouldShowScrollLockReturnButton({ scrollHeight: 2_000, scrollTop: 1_780, clientHeight: 120 }), true)
})

function makeSession(overrides: Partial<DesktopSessionRecord> = {}): DesktopSessionRecord {
  return {
    id: 'session-1',
    title: 'Saved flow title',
    workspacePath: '/repo',
    workspaceName: 'repo',
    mode: 'auto',
    metadata: undefined,
    messageCount: 0,
    updatedAt: 0,
    createdAt: 0,
    permissionsHydrated: false,
    lifecycle: null,
    live: {
      runId: null,
      agentName: null,
      startedAt: null,
      status: 'idle',
      step: 0,
      toolName: null,
      toolCallId: null,
      toolArguments: null,
      toolOutput: '',
      retainedToolName: null,
      retainedToolCallId: null,
      retainedToolArguments: null,
      retainedToolOutput: '',
      retainedToolState: null,
      summary: null,
      lastEventType: null,
      lastEventAt: null,
      error: null,
      seq: 0,
      assistantDraft: '',
      retainedAssistantSegments: [],
      reasoningSummary: '',
      reasoningText: '',
      reasoningState: 'idle',
      reasoningSegment: 0,
      reasoningStartedAt: null,
      awaitingAck: false,
    },
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
    ...overrides,
  }
}

test('buildLiveToolMessages renders every live tool history entry with stable instance identity', () => {
  const session = makeSession()
  session.live.toolHistory = [
    {
      key: 'session-1\u001frun-1\u001fstep-2\u001fcall-reused\u001fstep-2:call-reused',
      sessionId: 'session-1',
      runId: 'run-1',
      stepId: 'step-2',
      callId: 'call-reused',
      toolInstanceId: 'step-2:call-reused',
      toolName: 'read',
      toolArguments: '{"path":"two.txt"}',
      toolOutput: 'second output',
      state: 'done',
      step: 2,
      startedAt: 20,
      updatedAt: 22,
      completedAt: 22,
    },
    {
      key: 'session-1\u001frun-1\u001fstep-1\u001fcall-reused\u001fstep-1:call-reused',
      sessionId: 'session-1',
      runId: 'run-1',
      stepId: 'step-1',
      callId: 'call-reused',
      toolInstanceId: 'step-1:call-reused',
      toolName: 'read',
      toolArguments: '{"path":"one.txt"}',
      toolOutput: 'first output',
      state: 'done',
      step: 1,
      startedAt: 10,
      updatedAt: 12,
      completedAt: 12,
    },
  ]

  const messages = buildLiveToolMessages(session)

  assert.equal(messages.length, 2)
  assert.deepEqual(messages.map((message) => message.callId), ['call-reused', 'call-reused'])
  assert.deepEqual(messages.map((message) => message.toolInstanceId), ['step-1:call-reused', 'step-2:call-reused'])
  assert.deepEqual(messages.map((message) => message.output), ['first output', 'second output'])
})

test('DB-sourced live manage-image tool messages drive the image sidebar state', () => {
  const session = makeSession()
  session.live.toolHistory = [
    {
      key: 'session-1\u001frun-image\u001fstep-image\u001fcall-image\u001fstep-image:call-image',
      sessionId: 'session-1',
      runId: 'run-image',
      stepId: 'step-image',
      callId: 'call-image',
      toolInstanceId: 'step-image:call-image',
      toolName: 'manage-image',
      toolArguments: '{"thread_id":"thread-image","title":"Sidebar image","provider":"openai","model":"gpt-image-1","count":2}',
      toolOutput: '{"thread_id":"thread-image","status":"generating","saved_count":1,"requested_count":2}',
      state: 'running',
      step: 3,
      seq: 42,
      startedAt: 40,
      updatedAt: 42,
      completedAt: null,
    },
  ]

  const [toolMessage] = buildLiveToolMessages(session)
  const sidebarState = imageSidebarStateFromToolMessage(toolMessage)

  assert.equal(toolMessage?.tool, 'manage-image')
  assert.equal(toolMessage?.timelineSeq, 42)
  assert.deepEqual(sidebarState, {
    open: true,
    threadId: 'thread-image',
    title: 'Sidebar image',
    provider: 'openai',
    model: 'gpt-image-1',
    requestedCount: 2,
    savedCount: 1,
    status: 'generating',
  })
})

test('desktop live timeline orders assistant segments and tool calls by V3 event sequence', () => {
  const ordered = orderDesktopTimelineItems([
    {
      type: 'live-tool',
      toolMessage: {
        pathId: 'run.v3.provider-tool-result.v1',
        tool: 'list',
        callId: 'call-1',
        toolInstanceId: 'step-1:call-1',
        target: null,
        commandText: '',
        argumentsText: '{}',
        output: 'listed',
        completedOutput: 'listed',
        error: '',
        durationMs: 0,
        summary: 'list',
        state: 'done',
        timelineSeq: 3,
        editDiff: null,
        previewLines: [],
        taskRows: [],
      },
    },
    { type: 'live-assistant', id: 'segment-a', content: 'SEGMENT A', timelineSeq: 2 },
    {
      type: 'live-tool',
      toolMessage: {
        pathId: 'run.v3.provider-tool-result.v1',
        tool: 'list',
        callId: 'call-2',
        toolInstanceId: 'step-2:call-2',
        target: null,
        commandText: '',
        argumentsText: '{}',
        output: 'listed again',
        completedOutput: 'listed again',
        error: '',
        durationMs: 0,
        summary: 'list',
        state: 'done',
        timelineSeq: 5,
        editDiff: null,
        previewLines: [],
        taskRows: [],
      },
    },
    { type: 'live-assistant', id: 'segment-b', content: 'SEGMENT B', timelineSeq: 4 },
  ])

  assert.deepEqual(ordered.map((item) => item.type === 'live-assistant' ? item.content : item.toolMessage.callId), [
    'SEGMENT A',
    'call-1',
    'SEGMENT B',
    'call-2',
  ])
})

test('desktop timeline orders canonical messages and retained live items by one backend sequence', () => {
  const ordered = orderDesktopTimelineItems([
    {
      type: 'message',
      message: makeMessage({
        id: 'canonical-tool',
        globalSeq: 5,
        role: 'tool',
        content: 'tool result',
        toolMessage: {
          pathId: 'run.v3.provider-tool-result.v1',
          tool: 'read',
          callId: 'call-5',
          toolInstanceId: 'step-5:call-5',
          target: null,
          commandText: '',
          argumentsText: '{}',
          output: 'tool result',
          completedOutput: 'tool result',
          error: '',
          durationMs: 0,
          summary: 'read',
          state: 'done',
          timelineSeq: 5,
          editDiff: null,
          previewLines: [],
          taskRows: [],
        },
      }),
    },
    { type: 'live-assistant', id: 'segment-4', content: 'assistant before tool', timelineSeq: 4 },
  ])

  assert.deepEqual(ordered.map((item) => item.type === 'message' ? item.message.id : item.id), [
    'segment-4',
    'canonical-tool',
  ])
})

test('desktop timeline keeps testbench interleaved read/list/search stream in session order', () => {
  const liveTool = (callId: string, tool: string, timelineSeq: number): NonNullable<ChatMessageRecord['toolMessage']> => ({
    pathId: 'run.v3.provider-tool-result.v1',
    tool,
    callId,
    toolInstanceId: `step-${timelineSeq}:${callId}`,
    target: null,
    commandText: '',
    argumentsText: '{}',
    output: `${tool} output`,
    completedOutput: `${tool} output`,
    error: '',
    durationMs: 0,
    summary: tool,
    state: 'done',
    timelineSeq,
    editDiff: null,
    previewLines: [],
    taskRows: [],
  })

  const ordered = orderDesktopTimelineItems([
    { type: 'message', message: makeMessage({ id: 'assistant-hello', globalSeq: 16, role: 'assistant', content: 'HELLO' }) },
    { type: 'live-tool', toolMessage: liveTool('call-list', 'list', 17) },
    { type: 'message', message: makeMessage({ id: 'assistant-two', globalSeq: 20, role: 'assistant', content: 'SENTENCE TWO' }) },
    { type: 'live-tool', toolMessage: liveTool('call-read', 'read', 21) },
    { type: 'message', message: makeMessage({ id: 'assistant-three', globalSeq: 24, role: 'assistant', content: 'SENTENCE THREE' }) },
    { type: 'live-tool', toolMessage: liveTool('call-search', 'search', 25) },
  ])

  assert.deepEqual(ordered.map((item) => item.type === 'message' ? item.message.content : item.toolMessage.tool), [
    'HELLO',
    'list',
    'SENTENCE TWO',
    'read',
    'SENTENCE THREE',
    'search',
  ])
})

test('flow sessions are treated as read-only flow identity and resolve their real flow agent name', () => {
  const session = makeSession({
    metadata: {
      source: 'flow',
      lineage_kind: 'flow',
      flow_id: 'flow-123',
      flow_agent_name: 'memory',
      agent_name: 'swarm',
      requested_subagent: 'explorer',
    },
  })

  assert.equal(sessionUsesReadOnlyFlowIdentity(session), true)
  assert.equal(resolveSessionEffectiveAgentName(session, 'swarm'), 'memory')
})

test('non-flow sessions still resolve requested subagent before falling back to primary', () => {
  const session = makeSession({
    metadata: {
      requested_subagent: 'explorer',
    },
  })

  assert.equal(sessionUsesReadOnlyFlowIdentity(session), false)
  assert.equal(resolveSessionEffectiveAgentName(session, 'swarm'), 'explorer')
})

function makeMessage(overrides: Partial<ChatMessageRecord> = {}): ChatMessageRecord {
  return {
    id: 'message-1',
    sessionId: 'session-1',
    globalSeq: 1,
    role: 'assistant',
    content: 'hello',
    createdAt: 0,
    ...overrides,
  }
}

test('resolveMessageAssistantLabel uses per-turn message metadata before current agent fallback', () => {
  assert.equal(resolveMessageAssistantLabel(makeMessage({ metadata: { agent_name: 'swarm', model: 'gpt-5.4' } }), 'explorer'), 'swarm')
  assert.equal(resolveMessageAssistantLabel(makeMessage({ metadata: { agent_name: 'reviewer', model: 'claude-sonnet' } }), 'swarm'), 'reviewer')
  assert.equal(resolveMessageAssistantLabel(makeMessage(), 'explorer'), 'explorer')
})

test('desktop chat suppresses retained assistant replay segments once canonical messages load', () => {
  const segments: DesktopLiveAssistantSegment[] = [
    { id: 'live-assistant:1:1:0', content: 'First streamed answer.\n\nSecond sentence.', createdAt: 1, seq: 1 },
    { id: 'live-assistant:2:2:1', content: 'Still streaming after tool.', createdAt: 2, seq: 2 },
  ]
  const messages = [
    makeMessage({
      id: 'stored-assistant',
      globalSeq: 10,
      role: 'assistant',
      content: ' First streamed answer.\r\n\r\nSecond sentence. ',
    }),
    makeMessage({
      id: 'stored-tool',
      globalSeq: 11,
      role: 'tool',
      content: 'tool output',
    }),
  ]

  assert.deepEqual(
    retainedAssistantSegmentsWithoutCanonicalReplay(segments, messages).map((segment) => segment.id),
    ['live-assistant:2:2:1'],
  )
})

test('desktop chat keeps retained assistant replay segments until matching canonical assistant exists', () => {
  const segments: DesktopLiveAssistantSegment[] = [
    { id: 'live-assistant:1:1:0', content: 'First streamed answer.', createdAt: 1, seq: 1 },
  ]

  assert.deepEqual(retainedAssistantSegmentsWithoutCanonicalReplay(segments, []).map((segment) => segment.id), ['live-assistant:1:1:0'])
  assert.deepEqual(
    retainedAssistantSegmentsWithoutCanonicalReplay(segments, [makeMessage({ role: 'tool', content: 'First streamed answer.' })]).map((segment) => segment.id),
    ['live-assistant:1:1:0'],
  )
})

test('desktop chat suppresses live assistant draft once matching canonical assistant loads', () => {
  const messages = [makeMessage({ role: 'assistant', content: 'Hey! What can I help with?' })]

  assert.equal(liveAssistantDraftHasCanonicalReplay('Hey! What can I help with?', messages), true)
  assert.equal(liveAssistantDraftHasCanonicalReplay(' Hey! What can I help with?\r\n', messages), true)
  assert.equal(liveAssistantDraftHasCanonicalReplay('Still streaming…', messages), false)
  assert.equal(liveAssistantDraftHasCanonicalReplay('Hey! What can I help with?', [makeMessage({ role: 'tool', content: 'Hey! What can I help with?' })]), false)
})

test('desktop chat shows the context compact checkpoint and hides the duplicate ack', () => {
  const checkpoint = makeMessage({
    id: 'checkpoint',
    role: 'system',
    content: '[context-compact] index=3 origin=manual\n\nCompacted recap:\nsummary',
  })
  const ack = makeMessage({
    id: 'ack',
    globalSeq: 2,
    role: 'assistant',
    content: 'Manual context compact complete (Compact #3).',
  })

  assert.equal(isDesktopCompactionCheckpointMessage(checkpoint), true)
  assert.equal(isDesktopManualCompactionAckMessage(ack), true)
  assert.deepEqual(visibleDesktopChatMessages([checkpoint, ack]).map((message) => message.id), ['checkpoint'])
})


test('thinking tags visibility is part of desktop virtual item keys', () => {
  const baseKey = 'message-reasoning-1'

  assert.equal(desktopChatVirtualItemKey(baseKey, true), 'thinking-tags:on:message-reasoning-1')
  assert.equal(desktopChatVirtualItemKey(baseKey, false), 'thinking-tags:off:message-reasoning-1')
  assert.notEqual(desktopChatVirtualItemKey(baseKey, true), desktopChatVirtualItemKey(baseKey, false))
})

test('dedupeMessages reconciles pending user messages without dropping unrelated sequence collisions', () => {
  const pending = makeMessage({
    id: 'pending-user:session-1:12',
    sessionId: 'session-1',
    globalSeq: 12,
    role: 'user',
    content: 'send this',
    metadata: { client_request_id: 'desktop-v3-message:pending-user:session-1:12' },
  })
  const canonical = makeMessage({
    id: 'msg-user-12',
    sessionId: 'session-1',
    globalSeq: 12,
    role: 'user',
    content: 'send this',
    metadata: { client_request_id: 'desktop-v3-message:pending-user:session-1:12' },
  })
  const liveTool = makeMessage({
    id: 'live-tool:call-12',
    sessionId: 'session-1',
    globalSeq: 12,
    role: 'tool',
    content: '{"path_id":"run.tool-history.v2","tool":"bash","call_id":"call-12"}',
  })
  const assistant = makeMessage({
    id: 'msg-assistant-12',
    sessionId: 'session-1',
    globalSeq: 12,
    role: 'assistant',
    content: 'working',
  })

  const deduped = dedupeMessages([pending, liveTool, assistant, canonical])

  assert.deepEqual(deduped.map((message) => message.id), ['msg-user-12', 'live-tool:call-12', 'msg-assistant-12'])
})
