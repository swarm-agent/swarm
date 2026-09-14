import { useEffect, useMemo } from 'react'
import { buildDesktopV3ChildCardHydrateInput, postDesktopV3SyncHydrate } from '../state/desktop-v3-sync-api'
import { hydrateResponseToAction } from '../state/desktop-v3-cache-wire'
import { readAutomationV2, mutateAutomationV2, type AutomationV2Read, type AutomationV2Mutation } from '../state/desktop-automation-v2-api'
import { automationV2PageKey, type AutomationV2Pages, type AutomationV2CacheAction } from '../state/desktop-automation-v2-state'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
export class DesktopAutomationV2Runtime {
  private demand = new Map<string, { input: AutomationV2Read; count: number }>()
  private flights = new Map<string, Promise<void>>()
  private hydrations = new Map<string, { again: boolean; promise: Promise<void> }>()
  private waitingHydrations = new Map<string, { resolve: () => void; reject: (error: unknown) => void; promise: Promise<void> }>()
  async reconcileSession(sessionId: string): Promise<void> {
    const queued = this.waitingHydrations.get(sessionId)
    if (queued) return queued.promise
    const old = this.hydrations.get(sessionId)
    if (old) { old.again = true; return old.promise }
    if (this.hydrations.size >= 2) {
      if (this.waitingHydrations.size >= 100) throw new Error('Automation hydration capacity reached; refresh the session.')
      let resolve!: () => void, reject!: (error: unknown) => void
      const promise = new Promise<void>((yes, no) => { resolve = yes; reject = no })
      this.waitingHydrations.set(sessionId, { resolve, reject, promise })
      return promise
    }
    const entry = { again: false, promise: Promise.resolve() }
    entry.promise = postDesktopV3SyncHydrate(buildDesktopV3ChildCardHydrateInput([sessionId], { activePlan: true, permissionSummary: true })).then(response => {
      dispatchDesktopV3Cache(hydrateResponseToAction(response, [sessionId]))
    }).finally(() => {
      this.hydrations.delete(sessionId)
      const waiting = this.waitingHydrations.entries().next().value
      if (waiting) { this.waitingHydrations.delete(waiting[0]); void this.reconcileSession(waiting[0]).then(waiting[1].resolve, waiting[1].reject) }
      if (entry.again) void this.reconcileSession(sessionId).catch(() => this.invalidate())
    })
    this.hydrations.set(sessionId, entry)
    return entry.promise
  }
  constructor(private deps = { read: readAutomationV2, mutate: mutateAutomationV2, pages: (): AutomationV2Pages => getDesktopV3CacheSnapshot().automationV2Pages, dispatch: (action: AutomationV2CacheAction) => dispatchDesktopV3Cache(action) }) {}
  acquire(input: AutomationV2Read) {
    const key = automationV2PageKey(input), old = this.demand.get(key)
    if (!old && this.demand.size >= 48) throw new Error('Too many automation pages open')
    this.demand.set(key, { input, count: (old?.count ?? 0) + 1 })
    let released = false
    return { ready: this.refresh(input), release: () => {
      if (released) return
      released = true
      const entry = this.demand.get(key)
      if (entry && --entry.count === 0) { this.demand.delete(key); this.flights.delete(key); this.deps.dispatch({ type: 'automationV2.evict', key }) }
    } }
  }
  refresh(input: AutomationV2Read): Promise<void> {
    const key = automationV2PageKey(input), old = this.flights.get(key)
    if (old) return old
    const requestId = crypto.randomUUID()
    this.deps.dispatch({ type: 'automationV2.begin', key, input, requestId })
    const generation = this.deps.pages()[key].generation
    const promise: Promise<void> = this.deps.read(input).then(data => {
      if (this.flights.get(key) !== promise) return
      const records = [...(data.records ?? []), ...(data.record ? [data.record] : []), ...(data.proposal ? [data.proposal] : []), ...(data.progress ? [data.progress.record, ...data.progress.occurrences.map(occurrence => occurrence.accepted)] : [])]
      if (records.some(r => r.workspace_id !== input.workspace_id || (input.session_id && r.session_id !== input.session_id)) || (data.progress && data.progress.timezone !== input.timezone)) throw new Error('Automation response scope mismatch')
      this.deps.dispatch({ type: 'automationV2.finish', key, requestId, generation, data })
    }).catch(error => {
      if (this.flights.get(key) === promise) this.deps.dispatch({ type: 'automationV2.finish', key, requestId, generation, error: error instanceof Error ? error.message : 'Automation request failed' })
    }).finally(() => {
      if (this.flights.get(key) !== promise) return
      this.flights.delete(key)
      if (this.demand.has(key) && this.deps.pages()[key]?.generation !== generation) void this.refresh(input)
    })
    this.flights.set(key, promise)
    return promise
  }
  invalidate(workspaceId?: string, sessionId?: string) {
    this.deps.dispatch({ type: 'automationV2.invalidate', workspaceId, sessionId })
    for (const { input } of this.demand.values()) if ((!workspaceId || input.workspace_id === workspaceId) && (!sessionId || !input.session_id || input.session_id === sessionId)) void this.refresh(input)
  }
  acceptFrame(frame: { kind: string; [key: string]: unknown }) {
    if (['cursor.error', 'rehydrate.required'].includes(frame.kind)) { this.invalidate(); return }
    const event = frame.event as { type?: string; event_type?: string; session_id?: string } | undefined
    const type = String(event?.type ?? event?.event_type ?? frame.event_type ?? '')
    if (frame.kind === 'event' && type.startsWith('session.automation_v2.')) {
      const id = String(event?.session_id ?? frame.session_id ?? '')
      this.invalidate(undefined, id || undefined)
      if (id && (type.endsWith('.proposed') || type.endsWith('.accepted'))) void this.reconcileSession(id).catch(() => this.invalidate())
    }
  }
  async mutate(input: AutomationV2Mutation) {
    try {
      const result = await this.deps.mutate(input)
      if (input.action === 'accept_automation' || input.action === 'propose_automation') await this.reconcileSession(input.session_id)
      return result
    } finally { this.invalidate(input.workspace_id, input.session_id) }
  }
}
export const desktopAutomationV2 = new DesktopAutomationV2Runtime()
export function useAutomationV2Page(input: AutomationV2Read) {
  const key = automationV2PageKey(input)
  const stable = useMemo(() => input, [key])
  const page = useDesktopV3CacheSelector(state => state.automationV2Pages?.[key])
  useEffect(() => desktopAutomationV2.acquire(stable).release, [stable])
  return page
}
