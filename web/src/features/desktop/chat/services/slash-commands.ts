import type { SettingsTabID } from '../../settings/types/settings-tabs'

export type DesktopSlashCommandState = 'ready' | 'coming-soon'

export type DesktopSlashCommandAction =
  | { kind: 'open-settings'; tab: SettingsTabID }
  | { kind: 'open-quick-settings'; tab: Extract<SettingsTabID, 'permissions' | 'themes' | 'worktrees'> }
  | { kind: 'open-permissions' }
  | { kind: 'open-workspace-launcher' }
  | { kind: 'open-model-picker' }
  | { kind: 'toggle-fast' }
  | { kind: 'open-commit-modal' }
  | { kind: 'open-plan-modal' }
  | { kind: 'compact-session' }
  | { kind: 'new-session' }
  | { kind: 'show-help' }

export interface DesktopSlashCommand {
  id: string
  command: string
  aliases: string[]
  hint: string
  actionLabel: string
  tips: string[]
  state: DesktopSlashCommandState
  action: DesktopSlashCommandAction
}

export interface DesktopSlashPaletteState {
  active: boolean
  query: string
  hasArguments: boolean
  exactMatch: DesktopSlashCommand | null
  matches: DesktopSlashCommand[]
}

const DESKTOP_SLASH_COMMANDS: DesktopSlashCommand[] = [
  {
    id: 'help',
    command: '/help',
    aliases: [],
    hint: 'Browse slash commands and tips',
    actionLabel: 'Show slash command help',
    tips: ['Type / to browse commands', 'Press Enter to open a quick action', 'Press Tab to insert a command into the composer'],
    state: 'ready',
    action: { kind: 'show-help' },
  },
  {
    id: 'auth',
    command: '/auth',
    aliases: [],
    hint: 'Open auth settings',
    actionLabel: 'Open Settings → Auth',
    tips: ['/auth', 'Manage provider credentials', 'Use this to set up auth'],
    state: 'ready',
    action: { kind: 'open-settings', tab: 'auth' },
  },
  {
    id: 'swarm',
    command: '/swarm',
    aliases: [],
    hint: 'Open the Swarm dashboard',
    actionLabel: 'Open Swarm dashboard',
    tips: ['/swarm', 'Manage local and attached swarms', 'Inspect swarm status from settings'],
    state: 'ready',
    action: { kind: 'open-settings', tab: 'swarm' },
  },
  {
    id: 'vault',
    command: '/vault',
    aliases: [],
    hint: 'Open vault settings and status',
    actionLabel: 'Open Settings → Vault',
    tips: ['/vault', 'Check vault state', 'Manage vault export and import'],
    state: 'ready',
    action: { kind: 'open-settings', tab: 'vault' },
  },
  {
    id: 'worktrees',
    command: '/worktrees',
    aliases: ['/wt'],
    hint: 'Open worktree quick settings',
    actionLabel: 'Open Worktrees Quick Settings',
    tips: ['/worktrees', '/wt', 'Open the current worktrees quick settings'],
    state: 'ready',
    action: { kind: 'open-quick-settings', tab: 'worktrees' },
  },
  {
    id: 'workspace',
    command: '/workspace',
    aliases: ['/workspaces'],
    hint: 'Open the workspace launcher',
    actionLabel: 'Open Workspace Launcher',
    tips: ['/workspace', '/workspaces', 'Browse or switch saved workspaces'],
    state: 'ready',
    action: { kind: 'open-workspace-launcher' },
  },
  {
    id: 'new',
    command: '/new',
    aliases: [],
    hint: 'Start a new session in this workspace',
    actionLabel: 'Start New Session',
    tips: ['/new', 'Clear the current selection', 'Start a fresh conversation in this workspace'],
    state: 'ready',
    action: { kind: 'new-session' },
  },
  {
    id: 'agents',
    command: '/agents',
    aliases: [],
    hint: 'Open agent settings',
    actionLabel: 'Open Settings → Agents',
    tips: ['/agents', 'Manage shared agent profiles', 'Set the active primary agent'],
    state: 'ready',
    action: { kind: 'open-settings', tab: 'agents' },
  },
  {
    id: 'codex',
    command: '/codex',
    aliases: [],
    hint: 'Open Codex runtime controls',
    actionLabel: 'Open Model Picker',
    tips: ['/codex status', '/codex fast', '/fast toggles Fast'],
    state: 'ready',
    action: { kind: 'open-model-picker' },
  },
  {
    id: 'fast',
    command: '/fast',
    aliases: [],
    hint: 'Toggle Codex Fast for the current chat or draft',
    actionLabel: 'Toggle Codex Fast',
    tips: ['Available on Codex gpt-5.4/gpt-5.5', 'Alias tip: /codex fast', 'Use the model picker for gpt-5.4 1m context'],
    state: 'ready',
    action: { kind: 'toggle-fast' },
  },
  {
    id: 'mcp',
    command: '/mcp',
    aliases: [],
    hint: 'MCP management is deferred until Swarm Sync integration',
    actionLabel: 'Deferred',
    tips: ['Free Exa MCP search stays built in', 'Use an Exa API key for webfetch/deep fetch', 'Coming later'],
    state: 'coming-soon',
    action: { kind: 'show-help' },
  },
  {
    id: 'models',
    command: '/models',
    aliases: [],
    hint: 'Open the model picker',
    actionLabel: 'Open Model Picker',
    tips: ['/models', 'Browse providers and models', 'Press Enter to open the picker'],
    state: 'ready',
    action: { kind: 'open-model-picker' },
  },
  {
    id: 'commit',
    command: '/commit',
    aliases: ['/save'],
    hint: 'Open the save / commit modal',
    actionLabel: 'Open Save Changes Modal',
    tips: ['/commit', '/save', 'Open the desktop commit flow'],
    state: 'ready',
    action: { kind: 'open-commit-modal' },
  },
  {
    id: 'compact',
    command: '/compact',
    aliases: [],
    hint: 'Compact the current session context',
    actionLabel: 'Compact Session Context',
    tips: ['/compact 5%', 'Run compact now and set an auto-compact threshold', 'Use this when context gets too large'],
    state: 'ready',
    action: { kind: 'compact-session' },
  },
  {
    id: 'permissions',
    command: '/permissions',
    aliases: [],
    hint: 'Open permission policy quick settings',
    actionLabel: 'Open Permissions Quick Settings',
    tips: ['/permissions', 'Review always-allow and always-deny rules', 'Explain how a tool request will resolve'],
    state: 'ready',
    action: { kind: 'open-quick-settings', tab: 'permissions' },
  },
  {
    id: 'plan',
    command: '/plan',
    aliases: [],
    hint: 'Open the current session plan',
    actionLabel: 'Open Current Plan',
    tips: ['/plan', '/plan show', 'Review, copy, and edit the active session plan'],
    state: 'ready',
    action: { kind: 'open-plan-modal' },
  },
  {
    id: 'sandbox',
    command: '/sandbox',
    aliases: [],
    hint: 'Sandbox controls are not available yet',
    actionLabel: 'Coming soon',
    tips: ['/sandbox status', '/sandbox on', 'Coming later'],
    state: 'coming-soon',
    action: { kind: 'show-help' },
  },
  {
    id: 'sessions',
    command: '/sessions',
    aliases: [],
    hint: 'Session command helpers are not available yet',
    actionLabel: 'Coming soon',
    tips: ['/sessions', 'Use the sidebar for session switching today', 'Command support is coming later'],
    state: 'coming-soon',
    action: { kind: 'show-help' },
  },
  {
    id: 'theme',
    command: '/theme',
    aliases: ['/themes'],
    hint: 'Open theme quick settings',
    actionLabel: 'Open Theme Quick Settings',
    tips: ['/theme', '/themes', 'Set the desktop theme or workspace overrides'],
    state: 'ready',
    action: { kind: 'open-quick-settings', tab: 'themes' },
  },
  {
    id: 'voice',
    command: '/voice',
    aliases: [],
    hint: 'Voice controls are not available yet',
    actionLabel: 'Coming soon',
    tips: ['/voice open', '/voice devices', 'Coming later'],
    state: 'coming-soon',
    action: { kind: 'show-help' },
  },
]

function normalizeSlashToken(value: string): string {
  return value.trim().toLowerCase().replace(/^\/+/, '')
}

function commandTokens(command: DesktopSlashCommand): string[] {
  return [command.command, ...command.aliases].map(normalizeSlashToken).filter((value, index, values) => value !== '' && values.indexOf(value) === index)
}

function commandMatchRank(command: DesktopSlashCommand, query: string): number {
  if (!query) {
    return 1
  }
  let best = 0
  for (const token of commandTokens(command)) {
    if (token === query) {
      return 3
    }
    if (token.startsWith(query)) {
      best = Math.max(best, 2)
      continue
    }
    if (token.includes(query)) {
      best = Math.max(best, 1)
    }
  }
  return best
}

function sortCommands(left: DesktopSlashCommand, right: DesktopSlashCommand, query: string): number {
  const leftRank = commandMatchRank(left, query)
  const rightRank = commandMatchRank(right, query)
  if (leftRank !== rightRank) {
    return rightRank - leftRank
  }
  if (left.state !== right.state) {
    return left.state === 'ready' ? -1 : 1
  }
  return left.command.localeCompare(right.command)
}

export function getDesktopSlashCommands(): DesktopSlashCommand[] {
  return DESKTOP_SLASH_COMMANDS.slice()
}

export function buildDesktopSlashPaletteState(input: string): DesktopSlashPaletteState {
  const trimmedStart = input.trimStart()
  if (!trimmedStart.startsWith('/')) {
    return {
      active: false,
      query: '',
      hasArguments: false,
      exactMatch: null,
      matches: [],
    }
  }

  const slashBody = trimmedStart.slice(1)
  const trimmedBody = slashBody.trim()
  const parts = trimmedBody === '' ? [] : trimmedBody.split(/\s+/)
  const query = normalizeSlashToken(parts[0] ?? '')
  const hasArguments = parts.length > 1
  const exactMatch = query === ''
    ? null
    : DESKTOP_SLASH_COMMANDS.find((command) => commandTokens(command).includes(query)) ?? null

  const matches = (hasArguments && exactMatch
    ? [exactMatch]
    : DESKTOP_SLASH_COMMANDS
        .filter((command) => commandMatchRank(command, query) > 0)
        .sort((left, right) => sortCommands(left, right, query)))

  return {
    active: true,
    query,
    hasArguments,
    exactMatch,
    matches,
  }
}
