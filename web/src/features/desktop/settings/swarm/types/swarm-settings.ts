// Machine identity types for desktop settings.
// Keep this separate from the existing "swarming" activity indicator settings:
// - `swarming` = live run/activity label copy.
// - `swarm.name` = persisted machine/device name edited by /swarm and desktop settings.
// The UI settings POST endpoint merges partial patches into the saved document, so callers
// should send only the setting section they intend to change.

export const DEFAULT_SWARM_NAME = 'Local'
export const DEFAULT_GLOBAL_THEME_ID = 'crimson'
export const DEFAULT_SIDEBAR_HIDE_INACTIVE_HOURS = 12

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
}

export interface UIAgentSettingsWire {
  compact?: UICompactAgentSettingsWire
  explorer?: UICompactAgentSettingsWire
}

export type DesktopSessionMode = 'auto' | 'plan'
export type FollowupCheckpointPolicyDefault = 'require_approval' | 'auto_start'

export interface UIChatSettingsWire {
  show_header?: boolean
  thinking_tags?: boolean
  default_new_session_mode?: DesktopSessionMode
  followup_checkpoint_policy_default?: FollowupCheckpointPolicyDefault
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
  }
}

export function normalizeExplorerAgentSettings(payload?: UISettingsWire | null): Required<UICompactAgentSettingsWire> {
  return {
    provider: typeof payload?.agents?.explorer?.provider === 'string' ? payload.agents.explorer.provider.trim().toLowerCase() : '',
    model: typeof payload?.agents?.explorer?.model === 'string' ? payload.agents.explorer.model.trim() : '',
    thinking: typeof payload?.agents?.explorer?.thinking === 'string' ? payload.agents.explorer.thinking.trim() : '',
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
      },
    },
  }
}

export function withExplorerAgentSettings(current: UISettingsWire, explorer: UICompactAgentSettingsWire): UISettingsWire {
  return {
    ...current,
    agents: {
      ...(current.agents ?? {}),
      explorer: {
        provider: explorer.provider?.trim().toLowerCase() ?? '',
        model: explorer.model?.trim() ?? '',
        thinking: explorer.thinking?.trim() ?? '',
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
