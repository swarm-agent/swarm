import type { SettingsTabID } from '../../settings/types/settings-tabs'

export type DesktopSlashCommandState = 'ready' | 'coming-soon'

export type DesktopSlashCommandAction =
  | { kind: 'open-settings'; tab: SettingsTabID | 'agents' }
  | { kind: 'open-quick-settings'; tab: Extract<SettingsTabID, 'permissions' | 'themes' | 'worktrees'> }
  | { kind: 'open-permissions' }
  | { kind: 'open-workspace-launcher' }
  | { kind: 'open-model-picker' }
  | { kind: 'toggle-thinking' }
  | { kind: 'open-codex-usage' }
  | { kind: 'open-commit-modal' }
  | { kind: 'open-plan-modal' }
  | { kind: 'open-quick-actions' }
  | { kind: 'compact-session' }
  | { kind: 'new-session'; worktreeRequested: boolean; planModeRequested: boolean }
  | { kind: 'start-background-router-session' }
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

export type DesktopTaskMode = 'plan' | 'auto'

export interface DesktopTaskCommandRequest {
  request: string
  mode: DesktopTaskMode
}

export interface DesktopNewSessionCommandRequest {
  worktreeRequested: boolean
  planModeRequested: boolean
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
    hint: 'Start a fresh session in this workspace',
    actionLabel: 'New Session',
    tips: ['/new', 'Open the Router composer with its default chips'],
    state: 'ready',
    action: { kind: 'new-session', worktreeRequested: false, planModeRequested: false },
  },
  {
    id: 'new-worktree',
    command: '/new worktree',
    aliases: [],
    hint: 'Start fresh with the Worktree chip on',
    actionLabel: 'New + Worktree',
    tips: ['/new worktree', 'Prime a new Router session in a managed worktree'],
    state: 'ready',
    action: { kind: 'new-session', worktreeRequested: true, planModeRequested: false },
  },
  {
    id: 'new-plan',
    command: '/new plan',
    aliases: [],
    hint: 'Start fresh with the Plan chip on',
    actionLabel: 'New + Plan',
    tips: ['/new plan', 'Prime a new Router session in plan mode'],
    state: 'ready',
    action: { kind: 'new-session', worktreeRequested: false, planModeRequested: true },
  },
  {
    id: 'new-wp',
    command: '/new wp',
    aliases: [],
    hint: 'Start fresh with Worktree and Plan on',
    actionLabel: 'New + Both',
    tips: ['/new wp', 'Prime both the Worktree and Plan chips'],
    state: 'ready',
    action: { kind: 'new-session', worktreeRequested: true, planModeRequested: true },
  },
  {
    id: 'task',
    command: '/task',
    aliases: [],
    hint: 'Start a background Router session',
    actionLabel: 'Start Background Router Session',
    tips: ['/task <request> (auto)', '/task plan <request>', 'The task opens a managed worktree session'],
    state: 'ready',
    action: { kind: 'start-background-router-session' },
  },
  {
    id: 'agents',
    command: '/agents',
    aliases: [],
    hint: 'Open Agent Setup',
    actionLabel: 'Open Agent Setup',
    tips: ['/agents', 'Configure Swarm and user-facing system agents'],
    state: 'ready',
    action: { kind: 'open-settings', tab: 'agents' },
  },
  {
    id: 'codex',
    command: '/codex',
    aliases: [],
    hint: 'View ChatGPT plan usage and reset credits',
    actionLabel: 'Open Codex Usage',
    tips: ['/codex', 'View five-hour and weekly usage', 'Use available usage-limit resets'],
    state: 'ready',
    action: { kind: 'open-codex-usage' },
  },
  {
    id: 'mcp',
    command: '/mcp',
    aliases: [],
    hint: 'MCP management is deferred until Swarm Sync integration',
    actionLabel: 'Deferred',
    tips: ['Generic MCP management is coming later', 'Exa web access requires an active Exa API key', 'Add one in Settings → Providers'],
    state: 'coming-soon',
    action: { kind: 'show-help' },
  },
  {
    id: 'thinking',
    command: '/thinking',
    aliases: [],
    hint: 'Turn off to hide thinking summary',
    actionLabel: 'Toggle Thinking Summary',
    tips: ['/thinking', 'Turn off to hide thinking summary', 'Toggle thinking summaries without opening Agent Setup'],
    state: 'ready',
    action: { kind: 'toggle-thinking' },
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
    id: 'keybindings',
    command: '/keybindings',
    aliases: ['/shortcuts', '/keys'],
    hint: 'Desktop shortcuts differ from TUI keybindings',
    actionLabel: 'Open Desktop Quick Actions',
    tips: [
      'Desktop shortcuts are separate from TUI keybindings',
      'This opens the Desktop quick actions modal',
      'Open Settings → Shortcuts for the full Desktop list',
    ],
    state: 'ready',
    action: { kind: 'open-quick-actions' },
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

export function parseDesktopNewSessionCommand(input: string): DesktopNewSessionCommandRequest | null {
  const match = input.trim().match(/^\/new(?:\s+([\s\S]*))?$/i)
  if (!match) return null

  const directive = (match[1] ?? '').trim().toLowerCase()
  switch (directive) {
    case '':
      return { worktreeRequested: false, planModeRequested: false }
    case 'worktree':
      return { worktreeRequested: true, planModeRequested: false }
    case 'plan':
      return { worktreeRequested: false, planModeRequested: true }
    case 'wp':
      return { worktreeRequested: true, planModeRequested: true }
    default:
      return null
  }
}

export function parseDesktopTaskCommand(input: string): DesktopTaskCommandRequest {
  const taskBody = input.trimStart().replace(/^\/task(?:\s+|$)/i, '').trim()
  if (!taskBody) {
    return { request: '', mode: 'auto' }
  }
  const firstToken = taskBody.match(/^(\S+)(?:\s+([\s\S]*))?$/)
  if (firstToken?.[1].toLowerCase() === 'plan') {
    return { request: (firstToken[2] ?? '').trim(), mode: 'plan' }
  }
  return { request: taskBody, mode: 'auto' }
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
  const fullQuery = normalizeSlashToken(trimmedBody)
  const exactMatch = query === ''
    ? null
    : DESKTOP_SLASH_COMMANDS.find((command) => commandTokens(command).includes(fullQuery))
      ?? DESKTOP_SLASH_COMMANDS.find((command) => commandTokens(command).includes(query))
      ?? null

  const matches = (hasArguments && exactMatch && commandTokens(exactMatch).includes(fullQuery)
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
