// Machine identity types for desktop settings.
// Keep this separate from the existing "swarming" activity indicator settings:
// - `swarming` = live run/activity label copy.
// - `swarm.name` = persisted machine/device name edited by /swarm and desktop settings.
// The UI settings POST endpoint merges partial patches into the saved document, so callers
// should send only the setting section they intend to change.

export const DEFAULT_SWARM_NAME = 'Local'
export const DEFAULT_SIDEBAR_HIDE_INACTIVE_HOURS = 12
export const REVIEW_AUTO_ARCHIVE_MINUTES = Array.from({ length: 12 }, (_, index) => (index + 1) * 5)
export const DEFAULT_PLAN_CONTEXT_GUARD_USED_PERCENT = 80
export const DEFAULT_PLAN_CONTEXT_GUARD_MAX_COMPACTIONS = 1
export const PLAN_CONTEXT_GUARD_USED_PERCENT_MIN = 50
export const PLAN_CONTEXT_GUARD_USED_PERCENT_MAX = 95
export const PLAN_CONTEXT_GUARD_MAX_COMPACTIONS = 3
export const DEFAULT_TASK_CONTEXT_MAX_COMPACTIONS = 5
export const TASK_CONTEXT_MAX_COMPACTIONS = 10

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

export interface UIMediaSettingsWire {
  transcription_model?: string
}

export interface UIArtifactSettingsWire {
  library_directory?: string
}

export type DesktopSessionMode = 'auto' | 'plan'
export type FollowupCheckpointPolicyDefault = 'require_approval' | 'auto_start'

export interface UIChatSettingsWire {
  show_header?: boolean
  show_tips?: boolean
  thinking_tags?: boolean
  show_compact_button?: boolean
  default_new_session_mode?: DesktopSessionMode
  followup_checkpoint_policy_default?: FollowupCheckpointPolicyDefault
  plan_context_guard_enabled?: boolean
  plan_context_guard_used_percent?: number
  plan_context_guard_max_compactions?: number
  task_context_max_compactions?: number
  review_auto_archive_minutes?: number
  sidebar_hide_inactive_hours?: number | null
  default_workspace_routes?: Record<string, string>
  tool_stream?: Record<string, unknown>
}

export interface UIThemeSettingsWire {
  active_id?: string
  default_theme_id?: string
  builtin_themes?: Array<Record<string, unknown>>
  custom_themes?: Array<Record<string, unknown>>
}

export interface UISettingsWire {
  theme?: UIThemeSettingsWire
  input?: Record<string, unknown>
  chat?: UIChatSettingsWire
  swarming?: UISwarmingSettingsWire
  swarm?: UISwarmSettingsWire
  tools?: UIToolSettingsWire
  media?: UIMediaSettingsWire
  artifacts?: UIArtifactSettingsWire
  updated_at?: number
}

export interface PlanContextGuardSettings {
  enabled: boolean
  usedPercent: number
  maxCompactions: number
}

export interface TaskContextSettings {
  maxCompactions: number
}

export interface ArtifactLibrarySettings {
  libraryDirectory: string
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

export function normalizePlanContextGuardEnabled(value: unknown): boolean {
  return typeof value === 'boolean' ? value : true
}

export function normalizePlanContextGuardUsedPercent(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value === 0) return DEFAULT_PLAN_CONTEXT_GUARD_USED_PERCENT
  return Math.min(PLAN_CONTEXT_GUARD_USED_PERCENT_MAX, Math.max(PLAN_CONTEXT_GUARD_USED_PERCENT_MIN, Math.round(value)))
}

export function normalizePlanContextGuardMaxCompactions(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return DEFAULT_PLAN_CONTEXT_GUARD_MAX_COMPACTIONS
  return Math.min(PLAN_CONTEXT_GUARD_MAX_COMPACTIONS, Math.max(0, Math.round(value)))
}

export function normalizePlanContextGuardSettings(payload?: UISettingsWire | null): PlanContextGuardSettings {
  return {
    enabled: normalizePlanContextGuardEnabled(payload?.chat?.plan_context_guard_enabled),
    usedPercent: normalizePlanContextGuardUsedPercent(payload?.chat?.plan_context_guard_used_percent),
    maxCompactions: normalizePlanContextGuardMaxCompactions(payload?.chat?.plan_context_guard_max_compactions),
  }
}

export function normalizeTaskContextMaxCompactions(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return DEFAULT_TASK_CONTEXT_MAX_COMPACTIONS
  return Math.min(TASK_CONTEXT_MAX_COMPACTIONS, Math.max(1, Math.round(value)))
}

export function normalizeTaskContextSettings(payload?: UISettingsWire | null): TaskContextSettings {
  return { maxCompactions: normalizeTaskContextMaxCompactions(payload?.chat?.task_context_max_compactions) }
}

export function normalizeArtifactLibraryDirectory(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function normalizeArtifactLibrarySettings(payload?: UISettingsWire | null): ArtifactLibrarySettings {
  return { libraryDirectory: normalizeArtifactLibraryDirectory(payload?.artifacts?.library_directory) }
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
  const configuredId = typeof payload?.theme?.active_id === 'string' ? payload.theme.active_id.trim().toLowerCase() : ''
  const defaultId = typeof payload?.theme?.default_theme_id === 'string' ? payload.theme.default_theme_id.trim().toLowerCase() : ''
  const activeId = configuredId || defaultId

  return {
    activeId,
    activeLabel: normalizeThemeLabel(activeId),
  }
}

export function normalizeShowTipsEnabled(payload?: UISettingsWire | null): boolean {
  return typeof payload?.chat?.show_tips === 'boolean' ? payload.chat.show_tips : true
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

export function withPlanContextGuardSettings(current: UISettingsWire, settings: PlanContextGuardSettings): UISettingsWire {
  return {
    ...current,
    chat: {
      ...(current.chat ?? {}),
      plan_context_guard_enabled: normalizePlanContextGuardEnabled(settings.enabled),
      plan_context_guard_used_percent: normalizePlanContextGuardUsedPercent(settings.usedPercent),
      plan_context_guard_max_compactions: normalizePlanContextGuardMaxCompactions(settings.maxCompactions),
    },
  }
}

export function withTaskContextSettings(current: UISettingsWire, settings: TaskContextSettings): UISettingsWire {
  return {
    ...current,
    chat: {
      ...(current.chat ?? {}),
      task_context_max_compactions: normalizeTaskContextMaxCompactions(settings.maxCompactions),
    },
  }
}

export function withArtifactLibrarySettings(current: UISettingsWire, settings: ArtifactLibrarySettings): UISettingsWire {
  return {
    ...current,
    artifacts: {
      ...(current.artifacts ?? {}),
      library_directory: normalizeArtifactLibraryDirectory(settings.libraryDirectory),
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

export function normalizeMediaTranscriptionModel(payload?: UISettingsWire | null): string {
  return typeof payload?.media?.transcription_model === 'string' ? payload.media.transcription_model.trim() : ''
}

export function withMediaTranscriptionModel(current: UISettingsWire, transcriptionModel: string): UISettingsWire {
  return {
    ...current,
    media: {
      ...(current.media ?? {}),
      transcription_model: transcriptionModel.trim(),
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
