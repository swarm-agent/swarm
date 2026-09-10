import { readAutomations, mutateAutomation, type AutomationRead, type AutomationMutation, type AutomationResponse } from '../state/desktop-automation-api'
import { automationPageKey, type AutomationCacheAction, type AutomationPages } from '../state/desktop-automation-state'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'

export class DesktopAutomationRuntime {
  // Request/demand bookkeeping only. All backend-derived data lives in V3 state.
  private readonly demand = new Map<string, { input: AutomationRead; count: number }>()
  private readonly inFlight = new Map<string, Promise<void>>()
  constructor(private readonly deps = {
    read: readAutomations,
    mutate: mutateAutomation,
    pages: (): AutomationPages => getDesktopV3CacheSnapshot().automationPages,
    dispatch: (action: AutomationCacheAction) => dispatchDesktopV3Cache(action),
  }) {}

  acquire(input: AutomationRead): { ready: Promise<void>; release: () => void } {
    const key = automationPageKey(input)
    const current = this.demand.get(key)
    if (!current && this.demand.size >= 12) throw new Error('Too many automation pages open')
    this.demand.set(key, { input: { ...input }, count: (current?.count ?? 0) + 1 })
    let released = false
    return { ready: this.refresh(input), release: () => {
      if (released) return
      released = true
      const entry = this.demand.get(key)
      if (entry && --entry.count === 0) {
        this.demand.delete(key)
        this.deps.dispatch({ type: 'automation.evict', key })
      }
    } }
  }

  refresh(input: AutomationRead): Promise<void> {
    const key = automationPageKey(input)
    const pending = this.inFlight.get(key)
    if (pending) return pending
    const requestId = crypto.randomUUID()
    this.deps.dispatch({ type: 'automation.begin', key, input, requestId })
    const generation = this.deps.pages()[key].generation
    const promise = this.deps.read(input).then(data => {
      const records = [...(data.records ?? []), ...(data.record ? [data.record] : [])]
      if (records.some(record => record.scope.workspace_id !== input.workspace_id || (input.id && record.automation_id !== input.id))) throw new Error('Automation response scope mismatch')
      this.deps.dispatch({ type: 'automation.finish', key, requestId, generation, data })
    }).catch((error: unknown) => {
      this.deps.dispatch({ type: 'automation.finish', key, requestId, generation, error: error instanceof Error ? error.message : 'Automation request failed' })
    }).finally(() => {
      this.inFlight.delete(key)
      const page = this.deps.pages()[key]
      // One completion-triggered repair for new invalidations, never a timer or
      // retry loop on errors. Obsolete responses cannot erase a newer page.
      if (this.demand.has(key) && (!page || page.generation !== generation)) void this.refresh(input)
    })
    this.inFlight.set(key, promise)
    return promise
  }

  invalidate(workspaceId?: string): void {
    this.deps.dispatch({ type: 'automation.invalidate', workspaceId })
    for (const { input } of this.demand.values()) {
      if (!workspaceId || input.workspace_id === workspaceId) void this.refresh(input)
    }
  }

  acceptFrame(frame: { kind: string }): void {
    // Backend intentionally emits account-wide metadata-free invalidations.
    if (frame.kind === 'automation.updated' || frame.kind === 'cursor.error' || frame.kind === 'rehydrate.required') this.invalidate()
  }

  async mutate(input: AutomationMutation): Promise<AutomationResponse> {
    try {
      const response = await this.deps.mutate(input)
      this.invalidate(input.workspace_id)
      return response
    } catch (error) {
      // No optimistic grants, automatic mutation retry, or false success on 403/409.
      this.invalidate(input.workspace_id)
      throw error
    }
  }
}
export const desktopAutomations = new DesktopAutomationRuntime()
export function useAutomationPage(input: AutomationRead) {
  const key = automationPageKey(input)
  return useDesktopV3CacheSelector(state => state.automationPages[key])
}
