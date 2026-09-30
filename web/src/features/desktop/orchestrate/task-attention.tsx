import { useEffect, useMemo, useState } from 'react'
import type { TaskSessionCandidate } from '../runtime/desktop-projects-membership'
import { TaskSessionLeaseManager } from '../runtime/desktop-projects-membership'
import { requireDesktopV3RealtimeControllerReady } from '../realtime/v3-realtime-controller'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
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
  const permissions = useDesktopV3CacheSelector(state => taskAttentionPermissions(state, ids),
    (a, b) => a.length === b.length && a.every((item, index) => item === b[index]))
  const summaryKey = useDesktopV3CacheSelector(state => JSON.stringify(ids.map(id => {
    const summary = state.permissionSummaryBySessionId[id]
    return [id, summary?.pendingApprovalCount || 0, summary?.updatedAt || 0]
  })))
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const leases = useMemo(() => new TaskSessionLeaseManager({
    getControllerReady: requireDesktopV3RealtimeControllerReady,
    ownerKeyPrefix: `task-attention:${task.id}`,
  }), [task.id])
  useEffect(() => () => leases.cleanup(), [leases])
  useEffect(() => {
    const acquisition = leases.reconcile(ids)
    return acquisition.cancel
  }, [ids, leases])
  useEffect(() => {
    let active = true
    setError('')
    // Bounded sequential metadata reads; summaries changing trigger detail repair,
    // not a polling loop or a second permission cache. No transcript is requested.
    void (async () => {
      for (const id of ids) {
        if (!active) return
        const state = getDesktopV3CacheSnapshot()
        const summary = state.permissionSummaryBySessionId[id]
        if (summary?.pendingApprovalCount || taskAttentionPermissions(state, [id]).length) {
          const response = await postDesktopV3SyncHydrate(buildDesktopV3ChildCardHydrateInput([id], { permissionSummary: true, activePlan: true }))
          if (active) dispatchDesktopV3Cache(hydrateResponseToAction(response, [id]))
        } else {
          await hydrateDesktopV3ChildCard(id, { activePlan: true })
        }
      }
    })().catch(error => { if (active) setError(error instanceof Error ? error.message : 'Could not load pending requests') })
    return () => { active = false }
  }, [ids, summaryKey, retry])
  const unresolvedCount = useDesktopV3CacheSelector(state => ids.reduce((count, id) => count + Math.max(
    state.permissionSummaryBySessionId[id]?.pendingApprovalCount || 0,
    taskAttentionPermissions(state, [id]).length,
  ), 0))
  return { permissions, unresolvedCount, error, retry: () => setRetry(value => value + 1) }
}

export function TaskAttention({ attention }: { attention: ReturnType<typeof useTaskAttention> }) {
  const [selectedKey, setSelectedKey] = useState('')
  const [failure, setFailure] = useState('')
  const key = (permission: DesktopPermissionRecord) => JSON.stringify([permission.sessionId, permission.id])
  const selected = attention.permissions.find(permission => key(permission) === selectedKey) || null
  async function resolve(action: TaskAttentionDecision, reason: string, args?: Record<string, unknown>) {
    if (!selected) throw new Error('This request is no longer pending')
    setFailure('')
    try {
      await submitTaskAttentionDecision(selected, action, reason, args, {
        getState: getDesktopV3CacheSnapshot,
        resolve: (sessionId, id, action, reason, args) => resolveSessionPermission(sessionId, id, action, reason, args, { sessionApi: 'v3' }),
        commit: permission => dispatchDesktopV3Cache({ type: 'permission.resolveResult', sessionId: selected.sessionId, permissionId: selected.id, permission }),
      })
      setSelectedKey('')
    } catch (error) {
      setFailure(error instanceof Error ? error.message : 'Decision failed. Please retry.')
      throw error
    }
  }
  if (!attention.unresolvedCount && !attention.permissions.length && !attention.error) return null
  return <section aria-label="Task needs your attention" className="m-2 rounded-lg border border-amber-400/60 bg-amber-500/10 p-3 text-sm text-amber-100" onClick={event => event.stopPropagation()} onKeyDown={event => event.stopPropagation()}>
    <p role="status" className="font-semibold">Waiting for you · {attention.unresolvedCount} pending</p>
    <ul className="mt-2 space-y-2">
      {attention.permissions.map(permission => <li key={key(permission)} className="min-w-0">
        <p className="font-semibold">{taskAttentionLabel(permission)}</p>
        <p className="line-clamp-2 break-words text-xs" title={taskAttentionContext(permission)}>{taskAttentionContext(permission)}</p>
        <button type="button" className="mt-1 rounded border border-amber-300/50 px-3 py-1 font-semibold hover:bg-amber-500/20" onClick={() => { setFailure(''); setSelectedKey(key(permission)) }}>{taskAttentionLabel(permission) === 'Needs your input' ? 'Answer' : 'Review permission'}</button>
      </li>)}
    </ul>
    {attention.unresolvedCount > attention.permissions.length && <p>Loading pending requests…</p>}
    {attention.error && <p role="alert">{attention.error} <button type="button" onClick={attention.retry}>Retry loading requests</button></p>}
    {failure && <p role="alert">{failure}</p>}
    {selected?.toolName === 'bash' ? <div className="mt-3">
      <button type="button" onClick={() => setSelectedKey('')}>Close review</button>
      <DesktopInlineBashPermissionCard key={selectedKey} permission={selected} pendingCount={attention.unresolvedCount} sessionMode={selected.mode} onResolve={(_permission, action, reason) => resolve(action, reason)} onOpenPermissions={() => window.location.assign('/settings?tab=permissions')} />
    </div> : <DesktopPermissionModal key={selectedKey} dismissWithoutDecision open={Boolean(selected)} permission={selected} pendingCount={attention.unresolvedCount} sessionMode={selected?.mode || 'auto'} onOpenChange={open => { if (!open) setSelectedKey('') }} onResolve={resolve} />}
  </section>
}
