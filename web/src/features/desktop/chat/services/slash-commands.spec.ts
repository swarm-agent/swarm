import { getDesktopSlashCommands, buildDesktopSlashPaletteState, parseDesktopTaskCommand } from './slash-commands'
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

function testFastCommandIsReady(): void {
  const fast = getDesktopSlashCommands().find((command) => command.command === '/fast')
  assert(Boolean(fast), 'expected /fast command to exist')
  assert(fast?.state === 'ready', 'expected /fast command to be ready')
  assert((fast?.action as DesktopSlashCommandAction | undefined)?.kind === 'toggle-fast', 'expected /fast to toggle Fast')

  const palette = buildDesktopSlashPaletteState('/fast')
  assert(palette.exactMatch?.id === 'fast', 'expected /fast to match fast command')
  assert(palette.exactMatch?.state === 'ready', 'expected /fast exact match to be ready')
}

function testMCPCommandIsDeferredAndExaRequiresAPIKey(): void {
  const mcp = getDesktopSlashCommands().find((command) => command.id === 'mcp')
  assert(Boolean(mcp), 'expected /mcp command to exist')
  assert(mcp?.state === 'coming-soon', 'expected /mcp command to remain deferred')
  assert(mcp?.hint.includes('Swarm Sync'), 'expected /mcp hint to mention Swarm Sync')
  assert(mcp?.tips.some((tip) => tip.includes('active Exa API key')), 'expected /mcp tips to require an active Exa API key')
  assert(mcp?.tips.every((tip) => !tip.includes('Free Exa MCP search')), 'expected /mcp tips not to advertise free Exa MCP search')
}

function testTaskCommandAcceptsFullArguments(): void {
  const task = getDesktopSlashCommands().find((command) => command.id === 'task')
  assert(Boolean(task), 'expected /task command to exist')
  assert(task?.state === 'ready', 'expected /task command to be ready')
  assert((task?.action as DesktopSlashCommandAction | undefined)?.kind === 'queue-ai-task', 'expected /task to queue an AI task')

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
  testFastCommandIsReady()
  testMCPCommandIsDeferredAndExaRequiresAPIKey()
  testTaskCommandAcceptsFullArguments()
  testTaskCommandParsesModeDirective()
  testKeybindingsWarnsAboutDesktopShortcuts()
  console.log('slash-commands tests passed')
}

main()
