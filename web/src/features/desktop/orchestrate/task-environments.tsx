import { useEffect, useRef, useState } from 'react'
import type { RunningTask } from './orchestrate-types'
import type { TaskBrowserEndpoint, TaskEnvironmentAttachment } from '../environments/types/environments'
import { checkTaskBrowserAccess, verifiedBrowserURL } from '../environments/services/task-browser-access'

export function TaskEnvironments({ task, projectId }: { task: RunningTask; projectId?: string }) {
  return <div aria-label="Attached environments" className="grid gap-2 text-xs">
    {task.environmentAttachments?.map(attachment => <Attachment key={JSON.stringify([attachment, task.environmentsStale, task.activeAttemptId])}
      attachment={attachment} unavailable={Boolean(task.environmentsStale || attachment.project_id !== projectId || attachment.task_id !== task.id || (attachment.attempt_id && attachment.attempt_id !== task.activeAttemptId))} />)}
  </div>
}

function Attachment({ attachment: a, unavailable }: { attachment: TaskEnvironmentAttachment; unavailable: boolean }) {
  const [endpoints, setEndpoints] = useState<TaskBrowserEndpoint[] | null>(null)
  const [selected, setSelected] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [expired, setExpired] = useState(Date.now() >= a.expires_at)
  const generation = useRef(0)
  useEffect(() => {
    const timer = setTimeout(() => { generation.current++; setExpired(true); setEndpoints(null) }, Math.max(0, Math.min(a.expires_at - Date.now(), 2147483647)))
    return () => { generation.current++; clearTimeout(timer) }
  }, [a.expires_at])
  const usable = !unavailable && !expired && a.state === 'ready'
  const endpoint = endpoints?.find(item => item.id === selected)
  const check = async (open: boolean) => {
    const version = ++generation.current
    const endpointId = selected
    setEndpoints(null)
    setError('')
    setBusy(true)
    try {
      if (!usable || Date.now() >= a.expires_at) throw new Error('Unavailable')
      const fresh = await checkTaskBrowserAccess(a)
      if (version !== generation.current || Date.now() >= a.expires_at) return
      if (open) {
        const match = fresh.find(item => item.id === endpointId)
        const url = match && verifiedBrowserURL(match)
        if (!url) throw new Error('Not ready')
        window.open(url, '_blank', 'noopener,noreferrer')
      }
      setEndpoints(fresh)
      setSelected(fresh.length === 1 ? fresh[0].id : endpointId)
    } catch {
      if (version === generation.current) setError('Browser access unavailable. Check deployment health and refresh the task, then retry.')
    } finally { if (version === generation.current) setBusy(false) }
  }
  const state = unavailable || expired ? 'stale' : a.state
  return <section className="rounded border border-[var(--app-border)] p-2 break-words" onClick={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}>
    <div><strong>{a.environment_name.slice(0, 256)}</strong> · {state}</div>
    {['failed', 'stopped', 'stale'].includes(state) && <p role="alert">Environment unavailable. Ask Orchestrator to refresh or prepare this attachment.</p>}
    {a.source.deployment_id && <a href={`/environments?tab=deployments&workspace_id=${encodeURIComponent(a.source.workspace_id)}&deployment_id=${encodeURIComponent(a.source.deployment_id)}`}>Deployment details</a>}
    {!a.source.deployment_id && <p>Preparing · no deployment yet</p>}
    <div className="flex flex-wrap gap-2 mt-1">
      <button type="button" disabled={!usable || busy} onClick={() => void check(false)}>Check browser access</button>
      {endpoints && endpoints.length > 1 && <select aria-label="Frontend endpoint" value={selected} onChange={event => setSelected(event.target.value)}>
        <option value="">Select frontend endpoint</option>
        {endpoints.map(item => <option key={item.id} value={item.id}>{item.name.slice(0, 128)}</option>)}
      </select>}
      {endpoint && <button type="button" disabled={!usable || busy || !verifiedBrowserURL(endpoint)} onClick={() => void check(true)}>Open in browser</button>}
    </div>
    {endpoints?.length === 0 && <p>Backend-only environment · no frontend endpoint configured. Agent execution remains available.</p>}
    {endpoint && !verifiedBrowserURL(endpoint) && <p>Frontend not ready. Check deployment health, then retry.</p>}
    {error && <p role="alert">{error}</p>}
  </section>
}
