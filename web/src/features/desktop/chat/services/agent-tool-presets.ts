export interface AgentToolPresetOption {
  id: string
  label: string
  description: string
  enabledTools: string[]
  disabledByDefault: string[]
  bashPrefixes: string[]
}

export const CUSTOM_AGENT_TOOL_PRESET_ID = 'custom'

export const AGENT_TOOL_PRESET_OPTIONS: AgentToolPresetOption[] = [
  {
    id: CUSTOM_AGENT_TOOL_PRESET_ID,
    label: 'Custom',
    description: 'Fully custom tool contract. Tools are controlled by explicit allow/block choices instead of a preset bundle.',
    enabledTools: [],
    disabledByDefault: [],
    bashPrefixes: [],
  },
  {
    id: 'read_only',
    label: 'Read only',
    description: 'Inspect workspace files and web content without file mutation or shell execution.',
    enabledTools: ['read', 'search', 'list', 'websearch', 'webfetch', 'skill_use', 'plan_manage', 'ask_user', 'exit_plan_mode'],
    disabledByDefault: ['write', 'edit', 'bash', 'task'],
    bashPrefixes: [],
  },
  {
    id: 'integration_builder',
    label: 'Integration builder',
    description: 'Inspect local/web context and manage Integration Pack drafts without shell or file mutation tools.',
    enabledTools: ['read', 'search', 'list', 'websearch', 'webfetch', 'manage_integrations'],
    disabledByDefault: ['write', 'edit', 'bash', 'task'],
    bashPrefixes: [],
  },
  {
    id: 'read_write',
    label: 'Read/write',
    description: 'Inspect and edit workspace files without shell execution or delegation.',
    enabledTools: ['read', 'search', 'list', 'write', 'edit', 'websearch', 'webfetch', 'skill_use', 'plan_manage', 'ask_user', 'exit_plan_mode'],
    disabledByDefault: ['bash', 'task'],
    bashPrefixes: [],
  },
  {
    id: 'bash_git_only',
    label: 'Git shell only',
    description: 'Allow read tools plus bash restricted to git status/diff/log/show prefixes.',
    enabledTools: ['read', 'search', 'list', 'bash', 'skill_use', 'plan_manage', 'ask_user', 'exit_plan_mode'],
    disabledByDefault: ['write', 'edit', 'task'],
    bashPrefixes: ['git status', 'git diff', 'git log', 'git show'],
  },
  {
    id: 'background_commit',
    label: 'Background commit',
    description: 'Allow only read/list/search plus git status/diff/add/commit tools for durable commits.',
    enabledTools: ['read', 'search', 'list', 'git_status', 'git_diff', 'git_add', 'git_commit'],
    disabledByDefault: ['write', 'edit', 'bash', 'task'],
    bashPrefixes: [],
  },
]

export function agentToolPresetByID(id: string): AgentToolPresetOption | null {
  const normalized = id.trim().toLowerCase()
  if (!normalized) return null
  return AGENT_TOOL_PRESET_OPTIONS.find((preset) => preset.id === normalized) ?? null
}
