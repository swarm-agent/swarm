import type { SettingsTabID } from '../../settings/types/settings-tabs'

export type DesktopSlashCommandState = 'ready' | 'coming-soon'

export type DesktopSlashCommandAction =
  | { kind: 'open-settings'; tab: SettingsTabID | 'agents' }
  | { kind: 'open-quick-settings'; tab: Extract<SettingsTabID, 'media' | 'permissions' | 'themes' | 'worktrees'> }
  | { kind: 'open-permissions' }
  | { kind: 'open-workspace-launcher' }
  | { kind: 'open-model-picker' }
  | { kind: 'toggle-thinking' }
  | { kind: 'toggle-tips' }
  | { kind: 'open-codex-usage' }
  | { kind: 'open-commit-modal' }
  | { kind: 'ai-commit' }
  | { kind: 'open-plan-modal' }
  | { kind: 'open-action-chooser' }
  | { kind: 'open-quick-actions' }
  | { kind: 'compact-session' }
  | { kind: 'enable-new-session-worktree' }
  | { kind: 'new-session'; worktreeRequested: boolean; planModeRequested: boolean }
  | { kind: 'start-background-router-session' }
  | { kind: 'show-help' }
  | { kind: 'open-artifact-viewer' }
  | { kind: 'open-feedback' }

export interface DesktopSlashCommand {
  id: string
  command: string
  aliases: string[]
  hint: string
  actionLabel: string
  tips: string[]
  state: DesktopSlashCommandState
  action: DesktopSlashCommandAction
  developerOnly?: boolean
}

export interface DesktopSlashCommandOptions {
  developerMode?: boolean
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
  prompt: string
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
    id: 'feedback',
    command: '/feedback',
    aliases: [],
    hint: 'Send an issue, comment, or suggestion',
    actionLabel: 'Open Feedback',
    tips: ['/feedback', 'Choose Issue, Comment, or Suggestion and send a message'],
    state: 'ready',
    action: { kind: 'open-feedback' },
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
    id: 'worktree-on',
    command: '/wt on',
    aliases: [],
    hint: 'Enable a managed worktree for this new session',
    actionLabel: 'Enable Worktree',
    tips: ['/wt on', 'Available only before starting a new session'],
    state: 'ready',
    action: { kind: 'enable-new-session-worktree' },
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
    hint: 'Start fresh, optionally with <prompt>',
    actionLabel: 'New Session',
    tips: ['/new [<prompt>]', 'Bare opens an editable composer; a prompt starts immediately'],
    state: 'ready',
    action: { kind: 'new-session', worktreeRequested: false, planModeRequested: false },
  },
  {
    id: 'new-worktree',
    command: '/new worktree',
    aliases: [],
    hint: 'Start fresh in a worktree, optionally with <prompt>',
    actionLabel: 'New + Worktree',
    tips: ['/new worktree [<prompt>]', 'A non-empty prompt starts the routed worktree session immediately'],
    state: 'ready',
    action: { kind: 'new-session', worktreeRequested: true, planModeRequested: false },
  },
  {
    id: 'new-plan',
    command: '/new plan',
    aliases: [],
    hint: 'Start fresh in plan mode, optionally with <prompt>',
    actionLabel: 'New + Plan',
    tips: ['/new plan [<prompt>]', 'A non-empty prompt starts the routed plan session immediately'],
    state: 'ready',
    action: { kind: 'new-session', worktreeRequested: false, planModeRequested: true },
  },
  {
    id: 'new-wp',
    command: '/new wp',
    aliases: [],
    hint: 'Start fresh with Worktree and Plan, optionally with <prompt>',
    actionLabel: 'New + Both',
    tips: ['/new wp [<prompt>]', 'A non-empty prompt starts with both chips enabled'],
    state: 'ready',
    action: { kind: 'new-session', worktreeRequested: true, planModeRequested: true },
  },
  {
    id: 'task',
    command: '/task',
    aliases: [],
    hint: 'Start an automatic background Router task with <prompt>',
    actionLabel: 'Start Background Router Session',
    tips: ['/task <prompt>', 'Runs in auto mode through the background Router endpoint'],
    state: 'ready',
    action: { kind: 'start-background-router-session' },
  },
  {
    id: 'flag',
    command: '/flag',
    aliases: [],
    hint: 'Investigate a problem using the prior session dump',
    actionLabel: 'Start Diagnostic Task',
    tips: ['/flag <problem>', 'Dev mode only; starts /task with the prior session ID'],
    state: 'ready',
    action: { kind: 'start-background-router-session' },
    developerOnly: true,
  },
  {
    id: 'task-plan',
    command: '/task plan',
    aliases: [],
    hint: 'Start a planned background Router task with <prompt>',
    actionLabel: 'Start Background Router Plan',
    tips: ['/task plan <prompt>', 'Requests plan mode through the background Router endpoint'],
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
    id: 'tips',
    command: '/tips',
    aliases: [],
    hint: 'Hide, show, or check workspace home tips',
    actionLabel: 'Toggle Home Tips',
    tips: ['/tips [on|off|toggle|status]', 'Bare /tips toggles the persisted setting'],
    state: 'ready',
    action: { kind: 'toggle-tips' },
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
    id: 'media',
    command: '/media',
    aliases: [],
    hint: 'Open media quick settings',
    actionLabel: 'Open Media Quick Settings',
    tips: ['/media', 'Change source video folders and media models without leaving chat'],
    state: 'ready',
    action: { kind: 'open-quick-settings', tab: 'media' },
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
    id: 'commit-ai',
    command: '/commit ai',
    aliases: [],
    hint: 'Generate a commit message and commit all changes',
    actionLabel: 'Run AI Commit',
    tips: ['/commit ai', 'Generate a message and commit all changes'],
    state: 'ready',
    action: { kind: 'ai-commit' },
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
    id: 'artifact',
    command: '/artifact',
    aliases: ['/artifacts'],
    hint: 'Browse artifacts across your workspaces',
    actionLabel: 'Open Artifact Viewer',
    tips: ['/artifact', '/artifacts', 'Search plans, visual artifacts, and documents without leaving chat'],
    state: 'ready',
    action: { kind: 'open-artifact-viewer' },
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
    id: 'actions',
    command: '/actions',
    aliases: ['/actions list'],
    hint: 'Choose a saved workspace Action without leaving chat',
    actionLabel: 'Open Workspace Actions',
    tips: ['/actions', '/actions list', 'Pinned Actions appear first; choosing one opens a review step before it runs'],
    state: 'ready',
    action: { kind: 'open-action-chooser' },
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

function availableDesktopSlashCommands(options: DesktopSlashCommandOptions = {}): DesktopSlashCommand[] {
  return DESKTOP_SLASH_COMMANDS.filter((command) => !command.developerOnly || options.developerMode === true)
}

export function getDesktopSlashCommands(options: DesktopSlashCommandOptions = {}): DesktopSlashCommand[] {
  return availableDesktopSlashCommands(options).slice()
}

export function isDesktopWorktreeOnCommand(input: string): boolean {
  return /^\/wt\s+on$/i.test(input.trim())
}

export function parseDesktopNewSessionCommand(input: string): DesktopNewSessionCommandRequest | null {
  const match = input.trim().match(/^\/new(?:\s+([\s\S]*))?$/i)
  if (!match) return null

  const body = (match[1] ?? '').trim()
  if (!body) return { prompt: '', worktreeRequested: false, planModeRequested: false }

  const firstToken = body.match(/^(\S+)(?:\s+([\s\S]*))?$/)
  const directive = firstToken?.[1].toLowerCase() ?? ''
  const promptAfterDirective = (firstToken?.[2] ?? '').trim()
  switch (directive) {
    case 'worktree':
      return { prompt: promptAfterDirective, worktreeRequested: true, planModeRequested: false }
    case 'plan':
      return { prompt: promptAfterDirective, worktreeRequested: false, planModeRequested: true }
    case 'wp':
      return { prompt: promptAfterDirective, worktreeRequested: true, planModeRequested: true }
    default:
      return { prompt: body, worktreeRequested: false, planModeRequested: false }
  }
}

export function buildDesktopFlagTaskPrompt(input: string, priorSessionId: string): string | null {
  const match = input.trim().match(/^\/flag(?:\s+([\s\S]*))?$/i)
  if (!match) return null

  const problem = (match[1] ?? '').trim()
  const sessionId = priorSessionId.trim()
  if (!problem || !sessionId) return null

  return [
    'Investigate a developer flag from a prior Swarm session.',
    '',
    `Prior session ID: ${sessionId}`,
    'Reported problem:',
    problem,
    '',
    'First dump and inspect that session through the canonical development session-dump path. Use ./scripts/session-dump-via-api.sh with the matching loopback Desktop session URL; never inspect Pebble directly. Use the dump as evidence, then search the current workspace for the code paths responsible for the reported problem. Diagnose the likely cause, make a safe scoped correction when the evidence supports one, and report validation plus relevant filepaths. If the dump is unavailable, report the exact blocker instead of bypassing the canonical path.',
  ].join('\n')
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

export function buildDesktopSlashPaletteState(input: string, options: DesktopSlashCommandOptions = {}): DesktopSlashPaletteState {
  const commands = availableDesktopSlashCommands(options)
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
    : commands
        .flatMap((command) => commandTokens(command).map((token) => ({ command, token })))
        .filter(({ token }) => fullQuery === token || fullQuery.startsWith(`${token} `))
        .sort((left, right) => right.token.length - left.token.length)[0]?.command
      ?? commands.find((command) => commandTokens(command).includes(query))
      ?? null

  const exactMatchHasPrefix = Boolean(exactMatch && commandTokens(exactMatch).some((token) => fullQuery === token || fullQuery.startsWith(`${token} `)))
  const matches = (hasArguments && exactMatch && exactMatchHasPrefix
    ? [exactMatch]
    : commands
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
