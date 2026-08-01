// Machine identity types for desktop settings.
// Keep this separate from the existing "swarming" activity indicator settings:
// - `swarming` = live run/activity label copy.
// - `swarm.name` = persisted machine/device name edited by /swarm and desktop settings.
// The UI settings POST endpoint merges partial patches into the saved document, so callers
// should send only the setting section they intend to change.

export const DEFAULT_SWARM_NAME = 'Local'
export const DEFAULT_GLOBAL_THEME_ID = 'tide'
export const DEFAULT_SIDEBAR_HIDE_INACTIVE_HOURS = 12
export const REVIEW_AUTO_ARCHIVE_MINUTES = Array.from({ length: 12 }, (_, index) => (index + 1) * 5)

export interface UISwarmingSettingsWire {
  title?: string
  status?: string
}

export interface UISwarmSettingsWire {
  name?: string
  remote_ssh_targets?: string[]
}

export interface UIToolImageSettingsWire {
  default_model?: string
}

export interface UIToolSettingsWire {
  image?: UIToolImageSettingsWire
}

export interface UICompactAgentSettingsWire {
  provider?: string
  model?: string
  thinking?: string
  service_tier?: string
}

export interface UIAgentSettingsWire {
  compact?: UICompactAgentSettingsWire
  finder?: UICompactAgentSettingsWire
  coder?: UICompactAgentSettingsWire
  designer?: UICompactAgentSettingsWire
  router?: UICompactAgentSettingsWire
}

export type DesktopSessionMode = 'auto' | 'plan'
export type FollowupCheckpointPolicyDefault = 'require_approval' | 'auto_start'

export interface UIChatSettingsWire {
  show_header?: boolean
  thinking_tags?: boolean
  show_compact_button?: boolean
  default_new_session_mode?: DesktopSessionMode
  followup_checkpoint_policy_default?: FollowupCheckpointPolicyDefault
  review_auto_archive_minutes?: number
  sidebar_hide_inactive_hours?: number | null
  default_workspace_routes?: Record<string, string>
  tool_stream?: Record<string, unknown>
}

export interface UIThemeSettingsWire {
  active_id?: string
  custom_themes?: Array<Record<string, unknown>>
}

export interface UISettingsWire {
  theme?: UIThemeSettingsWire
  input?: Record<string, unknown>
  chat?: UIChatSettingsWire
  swarming?: UISwarmingSettingsWire
  swarm?: UISwarmSettingsWire
  tools?: UIToolSettingsWire
  agents?: UIAgentSettingsWire
  updated_at?: number
}

export interface GlobalThemeSettings {
  activeId: string
  activeLabel: string
}

export interface SwarmSettings {
  name: string
  defaultNewSessionMode: DesktopSessionMode
  followupCheckpointPolicyDefault: FollowupCheckpointPolicyDefault
  updatedAt: number
  raw: UISettingsWire
}

export function normalizeSwarmName(value: string): string {
  const trimmed = value.trim()
  return trimmed || DEFAULT_SWARM_NAME
}

export function normalizeSessionMode(value: unknown): DesktopSessionMode {
  return typeof value === 'string' && value.trim().toLowerCase() === 'plan' ? 'plan' : 'auto'
}

export function normalizeReviewAutoArchiveMinutes(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return 0
  return Math.min(60, Math.max(5, Math.ceil(value / 5) * 5))
}

export function normalizeSidebarHideInactiveHours(value: unknown): number | null {
  if (value === null || value === 'never' || value === 0) return null
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return DEFAULT_SIDEBAR_HIDE_INACTIVE_HOURS
  return Math.min(24 * 365, Math.max(1, Math.round(value)))
}

export function normalizeDefaultNewSessionMode(value: unknown): DesktopSessionMode {
  return normalizeSessionMode(value)
}

export function normalizeFollowupCheckpointPolicyDefault(value: unknown): FollowupCheckpointPolicyDefault {
  if (typeof value !== 'string') return 'auto_start'
  return ['approval', 'approve', 'require_approval', 'manual', 'ask'].includes(value.trim().toLowerCase())
    ? 'require_approval'
    : 'auto_start'
}

export function normalizeGlobalThemeSettings(payload?: UISettingsWire | null): GlobalThemeSettings {
  const activeId = typeof payload?.theme?.active_id === 'string' && payload.theme.active_id.trim()
    ? payload.theme.active_id.trim().toLowerCase()
    : DEFAULT_GLOBAL_THEME_ID

  return {
    activeId,
    activeLabel: normalizeThemeLabel(activeId),
  }
}

export function normalizeThinkingTagsEnabled(payload?: UISettingsWire | null): boolean {
  return typeof payload?.chat?.thinking_tags === 'boolean' ? payload.chat.thinking_tags : true
}

export function normalizeShowCompactButton(payload?: UISettingsWire | null): boolean {
  return payload?.chat?.show_compact_button === true
}

export function normalizeDefaultWorkspaceRoutes(payload?: UISettingsWire | null): Record<string, string> {
  const routes = payload?.chat?.default_workspace_routes
  if (!routes || typeof routes !== 'object') {
    return {}
  }
  return Object.fromEntries(
    Object.entries(routes)
      .map(([workspacePath, routeId]) => [workspacePath.trim(), typeof routeId === 'string' ? routeId.trim() : ''] as const)
      .filter(([workspacePath, routeId]) => workspacePath !== '' && routeId !== ''),
  )
}

export function defaultWorkspaceRouteId(payload: UISettingsWire | null | undefined, workspacePath: string): string {
  return normalizeDefaultWorkspaceRoutes(payload)[workspacePath.trim()] ?? ''
}

export function withDefaultWorkspaceRoute(current: UISettingsWire, workspacePath: string, routeId: string): UISettingsWire {
  const normalizedWorkspacePath = workspacePath.trim()
  const normalizedRouteId = routeId.trim()
  const routes = normalizeDefaultWorkspaceRoutes(current)
  if (normalizedWorkspacePath && normalizedRouteId) {
    routes[normalizedWorkspacePath] = normalizedRouteId
  }
  return {
    ...current,
    chat: {
      ...(current.chat ?? {}),
      default_workspace_routes: routes,
    },
  }
}

export function withReviewAutoArchiveMinutes(current: UISettingsWire, minutes: number): UISettingsWire {
  return {
    ...current,
    chat: {
      ...(current.chat ?? {}),
      review_auto_archive_minutes: normalizeReviewAutoArchiveMinutes(minutes),
    },
  }
}

export function withSidebarHideInactiveHours(current: UISettingsWire, hours: number | null): UISettingsWire {
  return {
    ...current,
    chat: {
      ...(current.chat ?? {}),
      sidebar_hide_inactive_hours: hours === null ? 0 : normalizeSidebarHideInactiveHours(hours),
    },
  }
}

export function withDefaultNewSessionMode(current: UISettingsWire, mode: DesktopSessionMode): UISettingsWire {
  return {
    ...current,
    chat: {
      ...(current.chat ?? {}),
      default_new_session_mode: normalizeSessionMode(mode),
    },
  }
}

export function withFollowupCheckpointPolicyDefault(current: UISettingsWire, policy: FollowupCheckpointPolicyDefault): UISettingsWire {
  return {
    ...current,
    chat: {
      ...(current.chat ?? {}),
      followup_checkpoint_policy_default: normalizeFollowupCheckpointPolicyDefault(policy),
    },
  }
}

export function withThinkingTagsEnabled(current: UISettingsWire, enabled: boolean): UISettingsWire {
  return {
    ...current,
    chat: {
      ...(current.chat ?? {}),
      thinking_tags: enabled,
    },
  }
}

export function normalizeCompactAgentSettings(payload?: UISettingsWire | null): Required<UICompactAgentSettingsWire> {
  return {
    provider: typeof payload?.agents?.compact?.provider === 'string' ? payload.agents.compact.provider.trim().toLowerCase() : '',
    model: typeof payload?.agents?.compact?.model === 'string' ? payload.agents.compact.model.trim() : '',
    thinking: typeof payload?.agents?.compact?.thinking === 'string' ? payload.agents.compact.thinking.trim() : '',
    service_tier: typeof payload?.agents?.compact?.service_tier === 'string' ? payload.agents.compact.service_tier.trim().toLowerCase() : '',
  }
}

export function normalizeFinderAgentSettings(payload?: UISettingsWire | null): Required<UICompactAgentSettingsWire> {
  return {
    provider: typeof payload?.agents?.finder?.provider === 'string' ? payload.agents.finder.provider.trim().toLowerCase() : '',
    model: typeof payload?.agents?.finder?.model === 'string' ? payload.agents.finder.model.trim() : '',
    thinking: typeof payload?.agents?.finder?.thinking === 'string' ? payload.agents.finder.thinking.trim() : '',
    service_tier: typeof payload?.agents?.finder?.service_tier === 'string' ? payload.agents.finder.service_tier.trim().toLowerCase() : '',
  }
}

export function normalizeCoderAgentSettings(payload?: UISettingsWire | null): Required<UICompactAgentSettingsWire> {
  return {
    provider: typeof payload?.agents?.coder?.provider === 'string' ? payload.agents.coder.provider.trim().toLowerCase() : '',
    model: typeof payload?.agents?.coder?.model === 'string' ? payload.agents.coder.model.trim() : '',
    thinking: typeof payload?.agents?.coder?.thinking === 'string' ? payload.agents.coder.thinking.trim() : '',
    service_tier: typeof payload?.agents?.coder?.service_tier === 'string' ? payload.agents.coder.service_tier.trim().toLowerCase() : '',
  }
}

export function normalizeDesignerAgentSettings(payload?: UISettingsWire | null): Required<UICompactAgentSettingsWire> {
  return {
    provider: typeof payload?.agents?.designer?.provider === 'string' ? payload.agents.designer.provider.trim().toLowerCase() : '',
    model: typeof payload?.agents?.designer?.model === 'string' ? payload.agents.designer.model.trim() : '',
    thinking: typeof payload?.agents?.designer?.thinking === 'string' ? payload.agents.designer.thinking.trim() : '',
    service_tier: typeof payload?.agents?.designer?.service_tier === 'string' ? payload.agents.designer.service_tier.trim().toLowerCase() : '',
  }
}

export function normalizeRouterAgentSettings(payload?: UISettingsWire | null): Required<UICompactAgentSettingsWire> {
  return {
    provider: typeof payload?.agents?.router?.provider === 'string' ? payload.agents.router.provider.trim().toLowerCase() : '',
    model: typeof payload?.agents?.router?.model === 'string' ? payload.agents.router.model.trim() : '',
    thinking: typeof payload?.agents?.router?.thinking === 'string' ? payload.agents.router.thinking.trim() : '',
    service_tier: typeof payload?.agents?.router?.service_tier === 'string' ? payload.agents.router.service_tier.trim().toLowerCase() : '',
  }
}

export function withCompactAgentSettings(current: UISettingsWire, compact: UICompactAgentSettingsWire): UISettingsWire {
  return {
    ...current,
    agents: {
      ...(current.agents ?? {}),
      compact: {
        provider: compact.provider?.trim().toLowerCase() ?? '',
        model: compact.model?.trim() ?? '',
        thinking: compact.thinking?.trim() ?? '',
        service_tier: compact.service_tier?.trim().toLowerCase() ?? '',
      },
    },
  }
}

export function withFinderAgentSettings(current: UISettingsWire, finder: UICompactAgentSettingsWire): UISettingsWire {
  return {
    ...current,
    agents: {
      ...(current.agents ?? {}),
      finder: {
        provider: finder.provider?.trim().toLowerCase() ?? '',
        model: finder.model?.trim() ?? '',
        thinking: finder.thinking?.trim() ?? '',
        service_tier: finder.service_tier?.trim().toLowerCase() ?? '',
      },
    },
  }
}

export function withCoderAgentSettings(current: UISettingsWire, coder: UICompactAgentSettingsWire): UISettingsWire {
  return {
    ...current,
    agents: {
      ...(current.agents ?? {}),
      coder: {
        provider: coder.provider?.trim().toLowerCase() ?? '',
        model: coder.model?.trim() ?? '',
        thinking: coder.thinking?.trim() ?? '',
        service_tier: coder.service_tier?.trim().toLowerCase() ?? '',
      },
    },
  }
}

export function withDesignerAgentSettings(current: UISettingsWire, designer: UICompactAgentSettingsWire): UISettingsWire {
  return {
    ...current,
    agents: {
      ...(current.agents ?? {}),
      designer: {
        provider: designer.provider?.trim().toLowerCase() ?? '',
        model: designer.model?.trim() ?? '',
        thinking: designer.thinking?.trim() ?? '',
        service_tier: designer.service_tier?.trim().toLowerCase() ?? '',
      },
    },
  }
}

export function withRouterAgentSettings(current: UISettingsWire, router: UICompactAgentSettingsWire): UISettingsWire {
  return {
    ...current,
    agents: {
      ...(current.agents ?? {}),
      router: {
        provider: router.provider?.trim().toLowerCase() ?? '',
        model: router.model?.trim() ?? '',
        thinking: router.thinking?.trim() ?? '',
        service_tier: router.service_tier?.trim().toLowerCase() ?? '',
      },
    },
  }
}

export function normalizeImageDefaultModel(payload?: UISettingsWire | null): string {
  return typeof payload?.tools?.image?.default_model === 'string' ? payload.tools.image.default_model.trim() : ''
}

export function withImageDefaultModel(current: UISettingsWire, defaultModel: string): UISettingsWire {
  return {
    ...current,
    tools: {
      ...(current.tools ?? {}),
      image: {
        ...(current.tools?.image ?? {}),
        default_model: defaultModel.trim(),
      },
    },
  }
}

function normalizeThemeLabel(themeId: string): string {
  return themeId
    .split('-')
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ')
}

export function normalizeSwarmSettings(payload?: UISettingsWire | null): SwarmSettings {
  const raw = payload ?? {}
  return {
    name: normalizeSwarmName(typeof raw.swarm?.name === 'string' ? raw.swarm.name : ''),
    defaultNewSessionMode: normalizeDefaultNewSessionMode(raw.chat?.default_new_session_mode),
    followupCheckpointPolicyDefault: normalizeFollowupCheckpointPolicyDefault(raw.chat?.followup_checkpoint_policy_default),
    updatedAt: typeof raw.updated_at === 'number' ? raw.updated_at : 0,
    raw,
  }
}
