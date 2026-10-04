import { useEffect, useMemo, useState } from 'react'
import { backgroundRead } from '../../../app/background-read'
import { requestJson } from '../../../app/api'
import { desktopProjects } from '../runtime/desktop-projects'
import { mapBackendTask } from '../state/desktop-projects-state'
import type { RunningTask } from './orchestrate-types'
import { taskReopenOperations } from './task-reopen-operation'
import type { TaskSessionCandidate } from '../runtime/desktop-projects-membership'
import { TaskSessionLeaseManager } from '../runtime/desktop-projects-membership'
import { requireDesktopV3RealtimeControllerReady } from '../realtime/v3-realtime-controller'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, subscribeDesktopV3Cache, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import { buildDesktopV3ChildCardHydrateInput, postDesktopV3SyncHydrate } from '../state/desktop-v3-sync-api'
import { hydrateResponseToAction } from '../state/desktop-v3-cache-wire'
import { hydrateDesktopV3ChildCard } from '../state/desktop-v3-session-hydrator'
import { taskAttentionContext, taskAttentionLabel, taskAttentionPermissions, taskAttentionSessionIds, submitTaskAttentionDecision, type TaskAttentionDecision } from '../state/task-attention'
import { DesktopPermissionModal } from '../permissions/components/desktop-permission-modal'
import { DesktopInlineBashPermissionCard } from '../chat/components/desktop-inline-bash-permission-card'
import { resolveSessionPermission } from '../chat/queries/chat-queries'
import type { DesktopPermissionRecord } from '../types/realtime'

export function useTaskAttention(task: TaskSessionCandidate) {
  const idsKey = useDesktopV3CacheSelector(state => JSON.stringify(taskAttentionSessionIds(state, task)))
  const ids = useMemo<string[]>(() => JSON.parse(idsKey), [idsKey])
  const attention = useSessionAttention(ids, `task-attention:${task.id}`)
  const projectId = useDesktopV3CacheSelector(state => {
    const owner = state.sessionsById[task.sessionId || '']
    return owner?.kind === 'full' && typeof owner.session.metadata?.project_id === 'string' ? owner.session.metadata.project_id : ''
  })
  const candidate = task as RunningTask
  return { ...attention, blocker: candidate.status === 'blocked' && candidate.lastError
    ? { task: candidate, projectId, reason: candidate.lastError } : undefined }
}

// Parent composer requests belong to exactly this session, not its task descendants.
export function SessionPermissionAttention({ sessionId }: { sessionId: string }) {
  const ids = useMemo(() => sessionId ? [sessionId] : [], [sessionId])
  const attention = useSessionAttention(ids, `composer-attention:${sessionId}`)
  return <TaskAttention key={sessionId} attention={attention} />
}

function useSessionAttention(ids: string[], ownerKeyPrefix: string) {
  const permissions = useDesktopV3CacheSelector(state => taskAttentionPermissions(state, ids),
    (a, b) => a.length === b.length && a.every((item, index) => item === b[index]))
  const summaryKey = useDesktopV3CacheSelector(state => JSON.stringify(ids.map(id => {
    const summary = state.permissionSummaryBySessionId[id]
    return [id, summary?.pendingApprovalCount || 0, summary?.updatedAt || 0]
  })))
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const [reconnect, setReconnect] = useState(0)
  useEffect(() => subscribeDesktopV3Cache(mutation => {
    // Reconnect may preserve the same summary count while replacing the pending
    // request. Reload details, not merely the count, before accepting an answer.
    if (mutation?.action.type === 'reconnect.applySnapshot') setReconnect(value => value + 1)
  }), [])
  const leases = useMemo(() => new TaskSessionLeaseManager({
    getControllerReady: requireDesktopV3RealtimeControllerReady,
    ownerKeyPrefix,
  }), [ownerKeyPrefix])
  useEffect(() => () => leases.cleanup(), [leases])
  useEffect(() => {
    const acquisition = leases.reconcile(ids)
    return acquisition.cancel
  }, [ids, leases])
  useEffect(() => {
    let active = true
    const controller = new AbortController()
    setError('')
    // Bounded sequential metadata reads; summaries changing trigger detail repair,
    // not a polling loop or a second permission cache. No transcript is requested.
    void (async () => {
      for (const id of ids) {
        if (!active) return
        const state = getDesktopV3CacheSnapshot()
        const summary = state.permissionSummaryBySessionId[id]
        if (reconnect || retry || summary?.pendingApprovalCount || taskAttentionPermissions(state, [id]).length) {
          const response = await backgroundRead(() => postDesktopV3SyncHydrate(buildDesktopV3ChildCardHydrateInput([id], { permissionSummary: true, activePlan: true }), controller.signal), controller.signal)
          if (active) dispatchDesktopV3Cache(hydrateResponseToAction(response, [id]))
        } else {
          await hydrateDesktopV3ChildCard(id, { activePlan: true, permissionSummary: true })
        }
      }
    })().catch(error => { if (active) setError(error instanceof Error ? error.message : 'Could not load pending requests') })
    return () => { active = false; controller.abort() }
  }, [ids, summaryKey, retry, reconnect])
  const unresolvedCount = useDesktopV3CacheSelector(state => ids.reduce((count, id) => count + Math.max(
    state.permissionSummaryBySessionId[id]?.pendingApprovalCount || 0,
    taskAttentionPermissions(state, [id]).length,
  ), 0))
  return { permissions, unresolvedCount, error, retry: () => setRetry(value => value + 1), blocker: undefined as { task: RunningTask; projectId: string; reason: string } | undefined }
}

export function TaskAttention({ attention }: { attention: Omit<ReturnType<typeof useTaskAttention>, 'blocker'> & { blocker?: ReturnType<typeof useTaskAttention>['blocker'] } }) {
  const [reviewed, setReviewed] = useState<DesktopPermissionRecord | null>(null)
  const [failure, setFailure] = useState('')
  const [input, setInput] = useState('')
  const [supplying, setSupplying] = useState(false)
  const [resuming, setResuming] = useState(false)
  async function resume() {
    const blocker = attention.blocker
    if (!blocker || !blocker.projectId || !input.trim() || resuming) return
    setResuming(true)
    setFailure('')
    const { task, projectId } = blocker
    try {
      const result = await taskReopenOperations.run(projectId, task.id, task, input,
        body => requestJson<{ status: string; task: any }>(`/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(task.id)}/reopen`,
          { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
        returned => {
          desktopProjects.setOptimisticTasks(projectId, tasks => tasks.map(item => item.id === task.id ? mapBackendTask(returned) : item))
          desktopProjects.invalidate(projectId)
        })
      if (!result.ok) { setFailure(result.error); desktopProjects.invalidate(projectId) }
      else { setInput(''); setSupplying(false) }
    } finally { setResuming(false) }
  }
  const key = (permission: DesktopPermissionRecord) => JSON.stringify([permission.sessionId, permission.id])
  const selectedKey = reviewed ? key(reviewed) : ''
  // Do not silently replace the source the user opened with changed arguments.
  const selected = attention.permissions.find(permission => key(permission) === selectedKey
    && permission.runId === reviewed?.runId && permission.callId === reviewed?.callId
    && permission.updatedAt === reviewed?.updatedAt && permission.toolArguments === reviewed?.toolArguments) || null
  async function resolve(action: TaskAttentionDecision, reason: string, args?: Record<string, unknown>) {
    if (!selected) throw new Error('This request is no longer pending')
    setFailure('')
    try {
      await submitTaskAttentionDecision(selected, action, reason, args, {
        getState: getDesktopV3CacheSnapshot,
        resolve: (sessionId, id, action, reason, args) => resolveSessionPermission(sessionId, id, action, reason, args, { sessionApi: 'v3' }),
        commit: permission => dispatchDesktopV3Cache({ type: 'permission.resolveResult', sessionId: selected.sessionId, permissionId: selected.id, permission }),
      })
      setReviewed(current => current && key(current) === key(selected) ? null : current)
    } catch (error) {
      setFailure(error instanceof Error ? error.message : 'Decision failed. Please retry.')
      throw error
    }
  }
  if (!attention.blocker && !attention.unresolvedCount && !attention.permissions.length && !attention.error) return null
  return <section aria-label="Task needs your attention" className="m-2 rounded-lg border border-amber-400/60 bg-amber-500/10 p-3 text-sm text-amber-100" onClick={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}>
    {attention.blocker && <div role="status">
      <strong>Required input missing</strong>
      <p className="break-words">{attention.blocker.reason}</p>
      <button type="button" onClick={() => setSupplying(true)}>Supply input and resume</button>
      {supplying && <div>
        <label>Required input or resolution<textarea value={input} onChange={event => setInput(event.target.value)} disabled={resuming} /></label>
        <button type="button" disabled={resuming || !input.trim() || !attention.blocker.projectId} onClick={() => void resume()}>{resuming ? 'Resuming…' : 'Resume task'}</button>
      </div>}
    </div>}
    {attention.unresolvedCount > 0 && <p role="status" className="font-semibold">Waiting for you · {attention.unresolvedCount} pending</p>}
    <ul className="mt-2 space-y-2">
      {attention.permissions.map(permission => <li key={key(permission)} className="min-w-0">
        <p className="font-semibold">{taskAttentionLabel(permission)}</p>
        <p className="line-clamp-2 break-words text-xs" title={taskAttentionContext(permission)}>{taskAttentionContext(permission)}</p>
        <button type="button" className="mt-1 rounded border border-amber-300/50 px-3 py-1 font-semibold hover:bg-amber-500/20" onClick={() => { setFailure(''); setReviewed(permission) }}>{taskAttentionLabel(permission) === 'Needs your input' ? 'Answer' : 'Review permission'}</button>
      </li>)}
    </ul>
    {attention.unresolvedCount > attention.permissions.length && <p>Loading pending requests…</p>}
    {attention.error && <p role="alert">{attention.error} <button type="button" onClick={attention.retry}>Retry loading requests</button></p>}
    {failure && <p role="alert">{failure}</p>}
    {selected?.toolName === 'bash' ? <div className="mt-3">
      <button type="button" onClick={() => setReviewed(null)}>Close review</button>
      <DesktopInlineBashPermissionCard key={selectedKey} permission={selected} pendingCount={attention.unresolvedCount} sessionMode={selected.mode} onResolve={(_permission, action, reason) => resolve(action, reason)} onOpenPermissions={() => window.location.assign('/settings?tab=permissions')} />
    </div> : <DesktopPermissionModal key={selectedKey} dismissWithoutDecision open={Boolean(selected)} permission={selected} pendingCount={attention.unresolvedCount} sessionMode={selected?.mode || 'auto'} onOpenChange={open => { if (!open) setReviewed(null) }} onResolve={resolve} />}
  </section>
}
