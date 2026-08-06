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

test('consecutive search and read results form one group while unrelated tools create boundaries', () => {
  const makeToolMessage = (id: string, seq: number, tool: string, path: string): MessageSnapshot => {
    const toolMessage = buildStructuredToolMessage({
      tool,
      callId: `call-${id}`,
      argumentsText: JSON.stringify({ path, query: 'needle' }),
      outputText: tool === 'search'
        ? JSON.stringify({ search_mode: 'content', count: 1, total_matched: 1, results: [{ path, items: [{ line: 1, column: 1, text: 'needle' }] }] })
        : JSON.stringify({ path, count: 10 }),
    })
    assert.ok(toolMessage)
    if (!toolMessage) throw new Error('expected tool message')
    return { id, session_id: 'session-1', global_seq: seq, role: 'tool', content: '', created_at: seq, metadata: { call_id: `call-${id}` }, toolMessage }
  }
  const rendered: RenderedSessionMessages = {
    committed: [
      makeToolMessage('search-1', 1, 'search', 'web/src/one.ts'),
      makeToolMessage('read-1', 2, 'read', 'web/src/one.ts'),
      makeToolMessage('edit-1', 3, 'edit', 'web/src/one.ts'),
      makeToolMessage('read-2', 4, 'read', 'web/src/two.ts'),
      makeToolMessage('search-2', 5, 'search', 'web/src/two.ts'),
    ],
    pendingUser: [], liveRuns: [], runIntents: [], currentRunIntent: undefined, latestRunIntent: undefined,
  }
  const items = buildDesktopV3ConversationRenderItems(rendered)
  assert.deepEqual(items.map((item) => item.type), ['search-read-group', 'message', 'search-read-group'])
  const groups = items.filter((item) => item.type === 'search-read-group')
  assert.equal(groups.length, 2)
  assert.ok(groups.every((group) => group.toolMessages.length === 2))
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
