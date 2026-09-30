import React from 'react'
import { requestJson } from '../../../app/api'
import type { TaskAttempt } from './orchestrate-types'

export function TaskAttemptHistory({ projectId, taskId, onOpen }: { projectId: string; taskId: string; onOpen: (sessionId: string) => void }) {
  const [rows, setRows] = React.useState<TaskAttempt[]>([])
  const [cursor, setCursor] = React.useState(0)
  const [loaded, setLoaded] = React.useState(false)
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')
  const inFlight = React.useRef(false)
  const load = async (refresh = false) => {
    if (inFlight.current) return
    inFlight.current = true
    setBusy(true)
    try {
      const page = await requestJson<{ attempts: TaskAttempt[]; next_cursor: number }>(`/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/history?cursor=${refresh ? 0 : cursor}&limit=10`)
      setRows(previous => refresh ? page.attempts : [...previous, ...page.attempts])
      setCursor(page.next_cursor)
      setLoaded(true)
      setError('')
    } catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)) }
    finally { inFlight.current = false; setBusy(false) }
  }
  return <section aria-label="Task session history" onClick={event => event.stopPropagation()}>
    {(!loaded || cursor > 0) && <button type="button" disabled={busy} onClick={() => void load()}>{busy ? 'Loading history…' : loaded ? 'More history' : 'View task history'}</button>}
    {loaded && <button type="button" disabled={busy} onClick={() => void load(true)}>Refresh history</button>}
    {error && <p role="alert">{error}</p>}
    <ol>{rows.map(attempt => <li key={attempt.id}>
      <time>{attempt.created_at ? new Date(attempt.created_at).toLocaleString() : 'Date unknown'}</time>
      <button type="button" onClick={() => onOpen(attempt.session_id)}>Open {attempt.role} session</button>
      <p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{attempt.request || 'Original session; no dated follow-up request recorded.'}</p>
      <p>{attempt.status}{attempt.launch_state === 'launch_failed' ? ' — launch incomplete' : ''}</p>
      <p>{attempt.summary || 'No ready summary recorded for this attempt.'}</p>
      {attempt.last_error && <p>{attempt.last_error}</p>}
      {attempt.integration && <p>Integration: {attempt.integration.state} {attempt.integration.error}</p>}
    </li>)}</ol>
  </section>
}
