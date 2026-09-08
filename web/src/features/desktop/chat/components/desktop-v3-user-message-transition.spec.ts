import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'
import { buildDesktopV3ConversationRenderItems, desktopV3RenderItemKey, desktopV3UserMessageElement } from './desktop-v3-existing-conversation-pane'
import type { MessageSnapshot, PendingUserMessage } from '../../state/desktop-v3-cache-types'

// Requirement: confirmation must retain the user bubble's row key and immediate
// React child type. Regression: distinct pending/committed wrappers remount an
// otherwise identical bubble. The production render-item builder and element
// factory are the narrowest layer proving React reconciliation identity; this
// does not establish browser paint continuity across new-session navigation.
const pending: PendingUserMessage = {
  clientRequestId: 'request-1', messageId: 'message-1', sessionId: 'session-1',
  role: 'user', content: 'Keep this message visible', createdAt: 1, timelineSeq: 2, status: 'pending',
}
const committed: MessageSnapshot = {
  id: pending.messageId, session_id: pending.sessionId, role: 'user',
  content: pending.content, created_at: 1, global_seq: 2,
}
function items(messages: MessageSnapshot[], pendingUser: PendingUserMessage[]) {
  return buildDesktopV3ConversationRenderItems({ committed: messages, pendingUser, liveRuns: [], runIntents: [] })
}

test('user confirmation and overlap retain a single identical bubble element and row key', () => {
  const phases = [items([], [pending]), items([committed], [pending]), items([committed], [])]
  const first = desktopV3UserMessageElement(phases[0][0])!
  for (const phase of phases) {
    assert.equal(phase.length, 1)
    assert.equal(desktopV3RenderItemKey(phase[0]), pending.messageId)
    const element = desktopV3UserMessageElement(phase[0])!
    assert.equal(element.type, first.type)
    assert.equal(element.key, first.key)
    assert.equal(renderToStaticMarkup(element), renderToStaticMarkup(first))
  }
})

test('failure and retry preserve the bubble type and expose the error only while failed', () => {
  const first = desktopV3UserMessageElement(items([], [pending])[0])!
  const failed = desktopV3UserMessageElement(items([], [{ ...pending, status: 'failed', error: 'Send failed' }])[0])!
  const confirmed = desktopV3UserMessageElement(items([committed], [])[0])!
  assert.equal(failed.type, first.type)
  assert.equal(confirmed.type, first.type)
  assert.match(renderToStaticMarkup(failed), /Send failed/)
  assert.doesNotMatch(renderToStaticMarkup(confirmed), /Send failed/)
})

test('identical text in another message is retained and non-user rows are not intercepted', () => {
  const phase = items([committed, { ...committed, id: 'message-2', global_seq: 3 }], [])
  assert.deepEqual(phase.map(desktopV3RenderItemKey), ['message-1', 'message-2'])
  const assistant = items([{ ...committed, role: 'assistant' }], [])[0]
  assert.equal(desktopV3UserMessageElement(assistant), null)
})
