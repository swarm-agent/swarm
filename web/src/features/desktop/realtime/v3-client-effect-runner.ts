import type { QueryClient } from '@tanstack/react-query'

import { queryClient as desktopQueryClient } from '../../../app/query-client'
import { refreshAgentModelMutationCaches } from '../chat/queries/agent-preference-mutations'
import { applyDesktopRouteTheme } from '../layout/desktop-theme-controller'
import type { RealtimeMessage } from '../state/desktop-v3-cache-types'
import { agentStateQueryOptions, draftModelQueryOptions, modelOptionsQueryOptions, uiSettingsQueryOptions, workspaceOverviewQueryOptions } from '../../queries/query-options'
import { resolveWorkspaceBySlug } from '../../workspaces/launcher/services/workspace-route'
import { setWorkspaceThemeCatalog } from '../../workspaces/launcher/services/workspace-theme'
import type { WorkspaceOverviewResponse } from '../../workspaces/launcher/types/workspace-overview'
import type { UISettingsWire } from '../settings/swarm/types/swarm-settings'
import { refreshOpenDesktopV3ArtifactCatalogs } from '../session-v3/artifact-catalog-refresh'

export type DesktopV3ClientEffectType = 'refresh_agents' | 'refresh_themes' | 'refresh_providers' | 'refresh_artifacts'

export interface DesktopV3ClientEffect {
  type: DesktopV3ClientEffectType
}

export interface DesktopV3DurableClientEffects {
  eventIdentity: string
  effects: DesktopV3ClientEffect[]
}

export interface DesktopV3ClientEffectRunnerDeps {
  refreshAgents: () => Promise<void>
  refreshThemes: () => Promise<void>
  refreshProviders: () => Promise<void>
  refreshArtifacts: () => Promise<void>
  reportError: (effect: DesktopV3ClientEffectType, error: unknown) => void
}

const MAX_SEEN_DURABLE_EFFECT_EVENTS = 256
const CLIENT_EFFECT_TYPES = new Set<DesktopV3ClientEffectType>(['refresh_agents', 'refresh_themes', 'refresh_providers'])
const ARTIFACT_MUTATION_EVENT_TYPES = new Set([
  'session.artifact.created',
  'session.artifact.updated',
  'session.artifact.finalized',
  'session.artifact.failed',
  'session.artifact.unavailable',
  'session.artifact.selected',
  'session.artifact.variant.deleted',
  'session.artifact.collection.deleted',
])

export function durableClientEffectsFromRealtimeFrame(frame: RealtimeMessage): DesktopV3DurableClientEffects | null {
  if (frame.kind === 'auth.credentials.updated') {
    const eventSequence = numberValue(frame.auth?.event_sequence)
    const accountScopeID = stringValue(frame.auth?.account_scope_id)
    if (eventSequence <= 0 || !accountScopeID) return null
    return {
      eventIdentity: `auth:${accountScopeID}:${eventSequence}`,
      effects: [{ type: 'refresh_providers' }],
    }
  }
  if (frame.kind !== 'event') return null
  const event = recordValue(frame.event)
  if (!event) return null
  const eventType = stringValue(event.event_type) || stringValue(frame.event_type)
  const eventID = stringValue(event.id)
  const sessionID = stringValue(event.session_id) || stringValue(frame.session_id)
  const eventSeq = numberValue(event.seq)
  const eventIdentity = eventID || (sessionID && eventSeq > 0 ? `${sessionID}:${eventSeq}` : '')
  if (ARTIFACT_MUTATION_EVENT_TYPES.has(eventType)) {
    return eventIdentity ? { eventIdentity, effects: [{ type: 'refresh_artifacts' }] } : null
  }
  if (eventType !== 'session.tool.completed') return null

  const payload = recordValue(event.payload)
  const rawEffects = payload?.client_effects
  if (!Array.isArray(rawEffects)) return null

  const effects: DesktopV3ClientEffect[] = []
  const seenTypes = new Set<DesktopV3ClientEffectType>()
  for (const rawEffect of rawEffects) {
    const effectType = stringValue(recordValue(rawEffect)?.type) as DesktopV3ClientEffectType
    if (!CLIENT_EFFECT_TYPES.has(effectType) || seenTypes.has(effectType)) continue
    seenTypes.add(effectType)
    effects.push({ type: effectType })
  }
  if (effects.length === 0) return null

  if (!eventIdentity) return null
  return { eventIdentity, effects }
}

export class DesktopV3ClientEffectRunner {
  private readonly seenEventIdentities = new Set<string>()
  private readonly seenEventOrder: string[] = []
  private readonly pendingEffects = new Set<DesktopV3ClientEffectType>()
  private drainPromise?: Promise<void>
  private readonly deps: DesktopV3ClientEffectRunnerDeps

  constructor(deps?: DesktopV3ClientEffectRunnerDeps) {
    this.deps = deps ?? createDefaultDesktopV3ClientEffectRunnerDeps()
  }

  accept(frame: RealtimeMessage): void {
    const durableEffects = durableClientEffectsFromRealtimeFrame(frame)
    if (!durableEffects || this.seenEventIdentities.has(durableEffects.eventIdentity)) return

    this.rememberEvent(durableEffects.eventIdentity)
    for (const effect of durableEffects.effects) this.pendingEffects.add(effect.type)
    this.scheduleDrain()
  }

  async waitForIdle(): Promise<void> {
    await this.drainPromise
    if (this.drainPromise || this.pendingEffects.size > 0) await this.waitForIdle()
  }

  private rememberEvent(eventIdentity: string): void {
    this.seenEventIdentities.add(eventIdentity)
    this.seenEventOrder.push(eventIdentity)
    while (this.seenEventOrder.length > MAX_SEEN_DURABLE_EFFECT_EVENTS) {
      const oldest = this.seenEventOrder.shift()
      if (oldest) this.seenEventIdentities.delete(oldest)
    }
  }

  private scheduleDrain(): void {
    if (this.drainPromise) return
    let trackedDrain!: Promise<void>
    trackedDrain = Promise.resolve().then(() => this.drainPendingEffects()).finally(() => {
      if (this.drainPromise === trackedDrain) this.drainPromise = undefined
      if (this.pendingEffects.size > 0) this.scheduleDrain()
    })
    this.drainPromise = trackedDrain
  }

  private async drainPendingEffects(): Promise<void> {
    while (this.pendingEffects.size > 0) {
      const batch = [...this.pendingEffects]
      this.pendingEffects.clear()
      for (const effect of batch) {
        try {
          if (effect === 'refresh_agents') await this.deps.refreshAgents()
          if (effect === 'refresh_themes') await this.deps.refreshThemes()
          if (effect === 'refresh_providers') await this.deps.refreshProviders()
          if (effect === 'refresh_artifacts') await this.deps.refreshArtifacts()
        } catch (error) {
          this.deps.reportError(effect, error)
        }
      }
    }
  }
}

export function createDefaultDesktopV3ClientEffectRunnerDeps(
  queryClient: QueryClient = desktopQueryClient,
): DesktopV3ClientEffectRunnerDeps {
  return {
    refreshAgents: async () => {
      await refreshAgentModelMutationCaches(queryClient)
    },
    refreshThemes: async () => {
      const settingsOptions = uiSettingsQueryOptions()
      const overviewOptions = workspaceOverviewQueryOptions([], 25)
      const [settings, overview] = await Promise.all([
        queryClient.fetchQuery({ ...settingsOptions, staleTime: 0 }),
        queryClient.fetchQuery({ ...overviewOptions, staleTime: 0 }),
      ])
      queryClient.setQueryData(settingsOptions.queryKey, settings)
      queryClient.setQueryData(overviewOptions.queryKey, overview)
      applyCanonicalDesktopTheme(settings, overview)
    },
    refreshProviders: async () => {
      const options = modelOptionsQueryOptions()
      const modelOptions = await queryClient.fetchQuery({ ...options, staleTime: 0 })
      queryClient.setQueryData(options.queryKey, modelOptions)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: draftModelQueryOptions().queryKey, refetchType: 'active' }),
        queryClient.invalidateQueries({ queryKey: agentStateQueryOptions().queryKey, refetchType: 'active' }),
        queryClient.invalidateQueries({ queryKey: ['auth-credentials'], refetchType: 'active' }),
      ])
    },
    refreshArtifacts: refreshOpenDesktopV3ArtifactCatalogs,
    reportError: (effect, error) => {
      console.error(`[desktop-v3] client effect ${effect} failed`, error)
    },
  }
}

export function applyCanonicalDesktopTheme(
  settings: UISettingsWire,
  overview: WorkspaceOverviewResponse,
  pathname = typeof window === 'undefined' ? '/' : window.location.pathname,
): void {
  setWorkspaceThemeCatalog(settings.theme)
  const selectedWorkspacePath = desktopRouteWorkspacePath(pathname, overview)
  applyDesktopRouteTheme(selectedWorkspacePath, overview.workspaces, settings, false)
}

export function desktopRouteWorkspacePath(pathname: string, overview: WorkspaceOverviewResponse): string | null {
  const firstSegment = pathname.split('/').filter(Boolean)[0] ?? ''
  if (!firstSegment || ['settings', 'tools', 'integrations', 'swarm'].includes(firstSegment.toLowerCase())) return null
  let slug = firstSegment
  try {
    slug = decodeURIComponent(firstSegment)
  } catch {
    return null
  }
  return resolveWorkspaceBySlug(overview.workspaces, slug)?.path ?? null
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}
