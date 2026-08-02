import { getDesktopSlashCommands, buildDesktopSlashPaletteState, parseDesktopNewSessionCommand, parseDesktopTaskCommand } from './slash-commands'
import type { DesktopSlashCommandAction } from './slash-commands'

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message)
  }
}

function testPlanCommandIsReady(): void {
  const plan = getDesktopSlashCommands().find((command) => command.id === 'plan')
  assert(Boolean(plan), 'expected /plan command to exist')
  assert(plan?.state === 'ready', 'expected /plan command to be ready')
  assert((plan?.action as DesktopSlashCommandAction | undefined)?.kind === 'open-plan-modal', 'expected /plan to open the plan modal')
}

function testSlashPaletteMatchesPlan(): void {
  const palette = buildDesktopSlashPaletteState('/plan')
  assert(palette.active === true, 'expected slash palette to activate for /plan')
  assert(palette.exactMatch?.id === 'plan', 'expected /plan to be the exact match')
  assert(palette.matches[0]?.id === 'plan', 'expected /plan to be the first match')
}

function testCodexOpensUsageWithoutChangingModels(): void {
  const commands = getDesktopSlashCommands()
  const codex = commands.find((command) => command.command === '/codex')
  const models = commands.find((command) => command.command === '/models')
  assert((codex?.action as DesktopSlashCommandAction | undefined)?.kind === 'open-codex-usage', 'expected /codex to open Codex usage')
  assert((models?.action as DesktopSlashCommandAction | undefined)?.kind === 'open-model-picker', 'expected /models to keep opening model picker')
}

function testFastCommandIsRetired(): void {
  const commands = getDesktopSlashCommands()
  assert(commands.every((command) => command.command !== '/fast'), 'expected /fast command to be absent')

  const palette = buildDesktopSlashPaletteState('/fast')
  assert(palette.exactMatch === null, 'expected /fast not to resolve to a desktop command')
  assert(palette.matches.length === 0, 'expected /fast not to be suggested')
}

function testMCPCommandIsDeferredAndExaRequiresAPIKey(): void {
  const mcp = getDesktopSlashCommands().find((command) => command.id === 'mcp')
  assert(Boolean(mcp), 'expected /mcp command to exist')
  assert(mcp?.state === 'coming-soon', 'expected /mcp command to remain deferred')
  assert(mcp?.hint.includes('Swarm Sync'), 'expected /mcp hint to mention Swarm Sync')
  assert(mcp?.tips.some((tip) => tip.includes('active Exa API key')), 'expected /mcp tips to require an active Exa API key')
  assert(mcp?.tips.every((tip) => !tip.includes('Free Exa MCP search')), 'expected /mcp tips not to advertise free Exa MCP search')
}

function testWorktreeCommandIsRetiredAndWorktreesRemains(): void {
  const commands = getDesktopSlashCommands()
  const worktrees = commands.find((command) => command.command === '/worktrees')
  assert(commands.every((command) => command.command !== '/worktree'), 'expected /worktree command to be absent')
  assert((worktrees?.action as DesktopSlashCommandAction | undefined)?.kind === 'open-quick-settings', 'expected /worktrees to keep opening quick settings')

  const palette = buildDesktopSlashPaletteState('/worktree fix the sidebar')
  assert(palette.exactMatch === null, 'expected /worktree not to resolve to a desktop command')
  assert(palette.matches.length === 0, 'expected /worktree not to be suggested')
}

function testNewSessionCommandVariantsPrimeRouterChips(): void {
  const commands = getDesktopSlashCommands()
  const expected = [
    ['/new', false, false],
    ['/new worktree', true, false],
    ['/new plan', false, true],
    ['/new wp', true, true],
  ] as const

  for (const [commandText, worktreeRequested, planModeRequested] of expected) {
    const command = commands.find((candidate) => candidate.command === commandText)
    assert(Boolean(command), `expected ${commandText} to be listed`)
    assert(command?.action.kind === 'new-session', `expected ${commandText} to open a new session`)
    if (command?.action.kind === 'new-session') {
      assert(command.action.worktreeRequested === worktreeRequested, `expected ${commandText} worktree priming to match`)
      assert(command.action.planModeRequested === planModeRequested, `expected ${commandText} plan priming to match`)
    }
    assert(buildDesktopSlashPaletteState(commandText).exactMatch?.command === commandText, `expected ${commandText} to resolve exactly`)
    const parsed = parseDesktopNewSessionCommand(commandText)
    assert(parsed?.worktreeRequested === worktreeRequested, `expected ${commandText} parser worktree priming to match`)
    assert(parsed?.planModeRequested === planModeRequested, `expected ${commandText} parser plan priming to match`)
  }

  assert(parseDesktopNewSessionCommand('/new something-else') === null, 'expected unknown /new directives to be rejected')
}

function testTaskCommandAcceptsFullArguments(): void {
  const task = getDesktopSlashCommands().find((command) => command.id === 'task')
  assert(Boolean(task), 'expected /task command to exist')
  assert(task?.state === 'ready', 'expected /task command to be ready')
  assert((task?.action as DesktopSlashCommandAction | undefined)?.kind === 'start-background-router-session', 'expected /task to start a background Router session')

  const palette = buildDesktopSlashPaletteState('/task fix the sidebar now')
  assert(palette.exactMatch?.id === 'task', 'expected /task arguments to preserve the exact command match')
  assert(palette.hasArguments === true, 'expected /task request to be recognized as arguments')
}

function testTaskCommandParsesModeDirective(): void {
  const automatic = parseDesktopTaskCommand('/task fix the sidebar plan later')
  assert(automatic.mode === 'auto', 'expected ordinary /task to default to auto')
  assert(automatic.request === 'fix the sidebar plan later', 'expected ordinary /task text to be preserved')

  const planned = parseDesktopTaskCommand('  /TASK   plan   fix the sidebar\nwithout rewriting this ')
  assert(planned.mode === 'plan', 'expected first plan token to select plan mode')
  assert(planned.request === 'fix the sidebar\nwithout rewriting this', 'expected plan directive to be removed from the request')

  const planWordLater = parseDesktopTaskCommand('/task fix plan handling')
  assert(planWordLater.mode === 'auto', 'expected plan outside the first token to remain request text')
  assert(planWordLater.request === 'fix plan handling', 'expected later plan text to remain intact')
}

function testRetiredCommandsAreNotSuggested(): void {
  const retiredCommands = new Set(['/swarm', '/output', '/rebuild', '/reload', '/vault', '/voice'])
  const commands = getDesktopSlashCommands()
  assert(commands.every((command) => !retiredCommands.has(command.command)), 'expected retired slash commands to be absent')

  const visibleCommands = buildDesktopSlashPaletteState('/').matches.map((command) => command.command)
  assert(visibleCommands.every((command) => !retiredCommands.has(command)), 'expected retired slash commands to be absent from the composer palette')
}

function testKeybindingsWarnsAboutDesktopShortcuts(): void {
  const keybindings = getDesktopSlashCommands().find((command) => command.id === 'keybindings')
  assert(Boolean(keybindings), 'expected /keybindings command to exist')
  assert(keybindings?.state === 'ready', 'expected /keybindings command to be ready')
  assert((keybindings?.action as DesktopSlashCommandAction | undefined)?.kind === 'open-quick-actions', 'expected /keybindings to open quick actions')
  assert(keybindings?.hint.includes('Desktop shortcuts differ from TUI keybindings') === true, 'expected /keybindings hint to warn about Desktop shortcuts')
  assert(keybindings?.tips.some((tip) => tip.includes('Settings → Shortcuts')) === true, 'expected /keybindings tips to point to Settings → Shortcuts')
}

function main(): void {
  testPlanCommandIsReady()
  testSlashPaletteMatchesPlan()
  testCodexOpensUsageWithoutChangingModels()
  testFastCommandIsRetired()
  testMCPCommandIsDeferredAndExaRequiresAPIKey()
  testWorktreeCommandIsRetiredAndWorktreesRemains()
  testNewSessionCommandVariantsPrimeRouterChips()
  testTaskCommandAcceptsFullArguments()
  testTaskCommandParsesModeDirective()
  testRetiredCommandsAreNotSuggested()
  testKeybindingsWarnsAboutDesktopShortcuts()
  console.log('slash-commands tests passed')
}

main()
