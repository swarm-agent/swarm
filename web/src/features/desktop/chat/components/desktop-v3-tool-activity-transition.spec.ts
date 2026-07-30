import test from 'node:test'
import assert from 'node:assert/strict'

import { buildStructuredToolMessage } from '../services/tool-message'
import { buildDesktopV3ConversationRenderItems, buildDesktopV3LiveRunRenderItems, desktopV3RenderItemKey } from './desktop-v3-existing-conversation-pane'
import type { LiveRunOverlay, MessageSnapshot } from '../../state/desktop-v3-cache-types'
import type { RenderedSessionMessages } from '../../state/desktop-v3-cache-selectors'

function liveRun(status = 'running'): LiveRunOverlay {
  return {
    sessionId: 'session-1',
    runId: 'run-1',
    status: 'running',
    toolCallsByCallId: {
      'call-edit': {
        callId: 'call-edit',
        toolInstanceId: status === 'ready' ? 'provider-tool:call-edit' : 'step-1:call-edit',
        toolName: 'edit',
        argumentsText: '{"path":"web/src/app.tsx"}',
        status: status === 'ready' ? 'completed' : status,
        updatedAt: 2,
        timelineSeq: 5,
      },
    },
  }
}

test('live activity keeps one stable call key through provider-ready and runtime states', () => {
  const provider = buildDesktopV3LiveRunRenderItems(liveRun('ready')).find((item) => item.type === 'live-tool')
  const runtime = buildDesktopV3LiveRunRenderItems(liveRun('running')).find((item) => item.type === 'live-tool')
  assert.ok(provider && runtime)
  if (!provider || !runtime) throw new Error('expected live tool items')
  assert.equal(desktopV3RenderItemKey(provider), 'live-tool:call-edit')
  assert.equal(desktopV3RenderItemKey(runtime), 'live-tool:call-edit')
})

test('committed result suppresses matching live activity instead of rendering a duplicate card', () => {
  const toolMessage = buildStructuredToolMessage({
    tool: 'edit', callId: 'call-edit', toolInstanceId: 'step-1:call-edit',
    argumentsText: '{"path":"web/src/app.tsx"}',
    outputText: '{"path":"web/src/app.tsx","old_string_preview":"old","new_string_preview":"new"}',
  })
  assert.ok(toolMessage)
  if (!toolMessage) throw new Error('expected structured tool result')
  const committed: MessageSnapshot = {
    id: 'message-tool-result', session_id: 'session-1', global_seq: 5, role: 'tool', content: '', created_at: 3,
    metadata: { call_id: 'call-edit', tool_instance_id: 'step-1:call-edit' },
    toolMessage,
  }
  const rendered: RenderedSessionMessages = {
    committed: [committed],
    pendingUser: [],
    liveRuns: [liveRun('completed')],
    runIntents: [],
    currentRunIntent: undefined,
    latestRunIntent: undefined,
  }
  const items = buildDesktopV3ConversationRenderItems(rendered)
  assert.equal(items.filter((item) => desktopV3RenderItemKey(item) === 'live-tool:call-edit').length, 1)
  assert.equal(items.some((item) => item.type === 'live-tool'), false)
})
