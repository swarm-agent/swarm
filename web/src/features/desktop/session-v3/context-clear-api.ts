import { apiFetch, readErrorMessage } from '../../../app/api'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { outboxRecordToCacheEvent } from '../state/desktop-v3-cache-wire'
import type { V3RealtimeOutboxRecord } from '../state/desktop-v3-cache-types'

export class ContextClearRejected extends Error {}

export async function clearSessionContext(sessionId: string, clientRequestId: string, expectedLastEventSeq: number): Promise<void> {
  const result = await apiFetch(`/v3/sessions/${encodeURIComponent(sessionId)}/context/clear`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ client_request_id: clientRequestId, expected_last_event_seq: expectedLastEventSeq }),
  })
  if (!result.ok) {
    const message = await readErrorMessage(result)
    if (result.status >= 400 && result.status < 500) throw new ContextClearRejected(message)
    throw new Error(message)
  }
  const response = await result.json() as { ok: boolean; session_id: string; mutation: { realtime_outbox?: V3RealtimeOutboxRecord } }
  const outbox = response.mutation?.realtime_outbox
  if (!response.ok || response.session_id !== sessionId || outbox?.event?.session_id !== sessionId || outbox.event.event_type !== 'execution_epoch.began') {
    throw new Error('Context clear returned no authoritative receipt. Refresh the conversation before retrying.')
  }
  dispatchDesktopV3Cache({ type: 'realtime.applyEvent', event: outboxRecordToCacheEvent(outbox) })
}
