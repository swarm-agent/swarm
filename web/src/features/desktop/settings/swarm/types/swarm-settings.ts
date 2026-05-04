// Machine identity types for desktop settings.
// Keep this separate from the existing "swarming" activity indicator settings:
// - `swarming` = live run/activity label copy.
// - `swarm.name` = persisted machine/device name edited by /swarm and desktop settings.
// The UI settings POST endpoint replaces the full document, so callers that save swarm
// settings must round-trip the full UI settings payload instead of posting a partial patch.

export const DEFAULT_SWARM_NAME = 'Local'

export interface UISwarmingSettingsWire {
  title?: string
  status?: string
}

export interface UISwarmSettingsWire {
  name?: string
  remote_ssh_targets?: string[]
}

export interface UIUpdateSettingsWire {
  local_container_warning_dismissed?: boolean
}

export interface UIToolImageSettingsWire {
  default_model?: string
}

export interface UIToolSettingsWire {
  image?: UIToolImageSettingsWire
}

export interface UIChatSettingsWire {
  show_header?: boolean
  thinking_tags?: boolean
  default_new_session_mode?: 'auto' | 'plan'
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
  updates?: UIUpdateSettingsWire
  tools?: UIToolSettingsWire
  updated_at?: number
}

export interface GlobalThemeSettings {
  activeId: string
  activeLabel: string
}

export interface SwarmSettings {
  name: string
  defaultNewSessionMode: 'auto' | 'plan'
  updatedAt: number
}

export function normalizeSwarmName(value: string): string {
  const trimmed = value.trim()
  return trimmed || DEFAULT_SWARM_NAME
}

export function normalizeDefaultNewSessionMode(value: unknown): 'auto' | 'plan' {
  return typeof value === 'string' && value.trim().toLowerCase() === 'plan' ? 'plan' : 'auto'
}

export function normalizeGlobalThemeSettings(payload?: UISettingsWire | null): GlobalThemeSettings {
  const activeId = typeof payload?.theme?.active_id === 'string' && payload.theme.active_id.trim()
    ? payload.theme.active_id.trim().toLowerCase()
    : 'crimson'

  return {
    activeId,
    activeLabel: normalizeThemeLabel(activeId),
  }
}

export function normalizeThinkingTagsEnabled(payload?: UISettingsWire | null): boolean {
  return typeof payload?.chat?.thinking_tags === 'boolean' ? payload.chat.thinking_tags : true
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

export function localContainerUpdateWarningDismissed(payload?: UISettingsWire | null): boolean {
  return payload?.updates?.local_container_warning_dismissed === true
}

export function withLocalContainerUpdateWarningDismissed(current: UISettingsWire, dismissed: boolean): UISettingsWire {
  return {
    ...current,
    updates: {
      ...(current.updates ?? {}),
      local_container_warning_dismissed: dismissed,
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
  return {
    name: normalizeSwarmName(typeof payload?.swarm?.name === 'string' ? payload.swarm.name : ''),
    defaultNewSessionMode: normalizeDefaultNewSessionMode(payload?.chat?.default_new_session_mode),
    updatedAt: typeof payload?.updated_at === 'number' ? payload.updated_at : 0,
  }
}
