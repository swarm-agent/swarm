// Requirement: observers must yield on actionable failure, not wait for a
// potentially stale parent checkpoint. Signals are scoped to the selected run
// and program IDs so historical blocked programs cannot poison recovery.
export const decodeObject = value => {
  if (value && typeof value === 'object') return value
  try { return JSON.parse(value || '{}') } catch { return {} }
}
const stopped = new Set(['blocked', 'failed', 'cancelled', 'interrupted', 'expired'])
export function observerStopReason({ snapshot, sessionID, runID, programIDs = [], checkpoint, childReports = [] }) {
  const selected = new Set(programIDs)
  const programs = []
  const failures = []
  for (const message of snapshot.messages_by_session?.[sessionID] || []) {
    if (message.role !== 'tool') continue
    const envelope = decodeObject(message.content)
    const output = decodeObject(envelope.output || envelope.completed_output)
    const ownerRun = envelope.run_id || envelope.metadata?.run_id
    if (selected.has(output.program_id)) programs.push(output)
    if (runID && ownerRun === runID) {
      const error = envelope.metadata?.error || envelope.error || output.error || (output.permission?.approved === false ? output.permission.reason : '')
      if (error) failures.push({ kind: 'tool_failed', message: String(error), call_id: envelope.call_id })
    }
  }
  for (const event of snapshot.events_by_session?.[sessionID] || []) {
    const p = decodeObject(event.payload)
    const output = decodeObject(p.output || p.result || p.completed_output)
    if (selected.has(output.program_id)) {
      if (output.program_state) programs.push(output)
      const launch = output.launch
      if (launch && (stopped.has(launch.phase) || stopped.has(launch.child_state) || /^\s*(?:\*\*)?BLOCKED\b/im.test(launch.report || ''))) failures.push({ kind: 'child_stopped', program_id: output.program_id, child_session_id: launch.child_session_id, message: String(launch.error || launch.report || 'Child stopped').slice(0, 1200) })
    }
    if (runID && (p.run_id || event.run_id) === runID && event.event_type === 'session.tool.failed') failures.push({ kind: 'tool_failed', message: String(p.error || 'Tool failed'), call_id: p.call_id })
  }
  // Prefer the latest durable output for each selected program over old status.
  const latest = new Map(programs.map(p => [p.program_id, p]))
  for (const p of latest.values()) {
    if (stopped.has(p.program_state)) return { kind: 'program_stopped', program_id: p.program_id, state: p.program_state, message: p.blocker?.message || 'Program stopped', preserved_jobs: p.jobs?.filter(j => ['completed', 'integrated'].includes(j.state)).map(j => j.job_id) || [] }
    const job = p.jobs?.find(j => stopped.has(j.state))
    if (job) return { kind: 'child_stopped', program_id: p.program_id, job_id: job.job_id, state: job.state, message: job.blocker?.message || 'Child stopped' }
  }
  if (failures.length) return failures.at(-1)
  for (const report of childReports) {
    if (!selected.has(report.program_id)) continue
    if (stopped.has(report.state) || /^\s*(?:\*\*)?BLOCKED\b/im.test(report.text || '')) return { kind: 'child_stopped', program_id: report.program_id, child_session_id: report.session_id, message: String(report.text || 'Child stopped').slice(0, 1200) }
  }
  if (stopped.has(checkpoint?.status)) return { kind: 'checkpoint_stopped', state: checkpoint.status, message: checkpoint.report || checkpoint.result || 'Checkpoint stopped' }
  const run = (snapshot.run_intents_by_session?.[sessionID] || []).find(r => r.run_id === runID)
  if (run && stopped.has(run.status)) return { kind: 'run_stopped', state: run.status, message: run.blocked_reason || 'Run stopped' }
  if (run?.status === 'completed' && (selected.size === 0 || [...selected].some(id => latest.get(id)?.program_state !== 'completed'))) return { kind: 'terminal_without_evidence', message: 'Run completed without every selected program completion; inspect durable evidence before retrying.' }
  return null
}

export function createProgressWatch({ now = Date.now, stallMs = 60000, timeoutMs = 540000 } = {}) {
  const started = now()
  let changed = started, previous
  return signature => {
    const time = now()
    if (signature !== previous) { previous = signature; changed = time }
    if (time - started >= timeoutMs) return { kind: 'deadline', message: 'Observer deadline reached; no retry or replay was started.' }
    if (time - changed >= stallMs) return { kind: 'stalled', message: 'No durable progress; returning control for diagnosis instead of waiting indefinitely.' }
    return null
  }
}
