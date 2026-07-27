import { requestJson } from '../../../app/api'

import type {
  V3SessionEvent,
  V3SessionProjection,
  V3SessionRunIntent,
} from '../state/desktop-v3-cache-types'

export interface DesktopV3SessionEventsPage {
  ok: true
  session_id: string
  events: V3SessionEvent[]
  projection: V3SessionProjection
  run_intents?: V3SessionRunIntent[]
  high_watermark_seq: number
  next_seq: number
  applied_seq: number
}

export async function getDesktopV3SessionEventsPage(input: {
  sessionId: string
  afterSeq: number
  limit?: number
}): Promise<DesktopV3SessionEventsPage> {
  const sessionId = input.sessionId.trim()
  if (!sessionId) throw new Error('Desktop V3 event repair requires sessionId')

  const afterSeq = Math.max(0, Math.floor(input.afterSeq))
  const limit = Math.min(500, Math.max(1, Math.floor(input.limit ?? 500)))
  const query = new URLSearchParams({
    after_seq: String(afterSeq),
    limit: String(limit),
  })

  return requestJson<DesktopV3SessionEventsPage>(
    `/v3/sessions/${encodeURIComponent(sessionId)}/events?${query.toString()}`,
    { method: 'GET' },
  )
}
