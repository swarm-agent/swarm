import { getDesktopSlashCommands, buildDesktopSlashPaletteState, isDesktopWorktreeOnCommand, parseDesktopNewSessionCommand, parseDesktopTaskCommand } from './slash-commands'
import type { DesktopSlashCommandAction } from './slash-commands'

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message)
  }
}

function testFeedbackCommandOpensModal(): void {
  const feedback = getDesktopSlashCommands().find((command) => command.id === 'feedback')
  assert(Boolean(feedback), 'expected /feedback command to exist')
  assert(feedback?.state === 'ready', 'expected /feedback command to be ready')
  assert(feedback?.action.kind === 'open-feedback', 'expected /feedback to open the feedback modal')
  const palette = buildDesktopSlashPaletteState('/feedback')
  assert(palette.exactMatch?.id === 'feedback', 'expected /feedback to resolve exactly')
  assert(palette.matches[0]?.id === 'feedback', 'expected /feedback to lead palette matches')
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

function testMediaCommandOpensQuickSettings(): void {
  const media = getDesktopSlashCommands().find((command) => command.command === '/media')
  assert(Boolean(media), 'expected /media command to exist')
  assert(media?.state === 'ready', 'expected /media command to be ready')
  assert(media?.action.kind === 'open-quick-settings', 'expected /media to open quick settings')
  if (media?.action.kind === 'open-quick-settings') {
    assert(media.action.tab === 'media', 'expected /media to target media quick settings')
  }
  const palette = buildDesktopSlashPaletteState('/media')
  assert(palette.exactMatch?.id === 'media', 'expected /media to resolve exactly')
  assert(palette.matches[0]?.id === 'media', 'expected /media to lead palette matches')
}

function testAICommitCommandTriggersCanonicalWorkflow(): void {
  const commands = getDesktopSlashCommands()
  const aiCommit = commands.find((command) => command.command === '/commit ai')
  const commit = commands.find((command) => command.command === '/commit')

  assert(Boolean(aiCommit), 'expected /commit ai command to exist')
  assert(aiCommit?.state === 'ready', 'expected /commit ai command to be ready')
  assert(aiCommit?.action.kind === 'ai-commit', 'expected /commit ai to trigger AI Commit')
  assert(commit?.action.kind === 'open-commit-modal', 'expected bare /commit to keep opening the commit modal')

  const palette = buildDesktopSlashPaletteState('/commit ai')
  assert(palette.exactMatch?.id === 'commit-ai', 'expected /commit ai to resolve exactly')
  assert(palette.matches[0]?.id === 'commit-ai', 'expected /commit ai to lead palette matches')
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

function testWorktreeOnCommandIsReady(): void {
  const command = getDesktopSlashCommands().find((candidate) => candidate.command === '/wt on')
  assert(Boolean(command), 'expected /wt on command to exist')
  assert(command?.state === 'ready', 'expected /wt on command to be ready')
  assert(command?.action.kind === 'enable-new-session-worktree', 'expected /wt on to enable new-session worktree intent')

  const palette = buildDesktopSlashPaletteState('/wt on')
  assert(palette.exactMatch?.id === 'worktree-on', 'expected /wt on to resolve exactly')
  assert(palette.matches[0]?.id === 'worktree-on', 'expected /wt on to lead palette matches')
  assert(isDesktopWorktreeOnCommand(' /WT   on '), 'expected /wt on parser to ignore case and repeated whitespace')
  assert(!isDesktopWorktreeOnCommand('/wt on extra'), 'expected /wt on parser to reject extra arguments')
  assert(!isDesktopWorktreeOnCommand('/wt'), 'expected bare /wt to keep its worktree settings behavior')
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
    assert(parsed?.prompt === '', `expected bare ${commandText} to have no prompt`)
    assert(parsed?.worktreeRequested === worktreeRequested, `expected ${commandText} parser worktree priming to match`)
    assert(parsed?.planModeRequested === planModeRequested, `expected ${commandText} parser plan priming to match`)
  }

  const promptCases = [
    ['/new fix the sidebar', 'fix the sidebar', false, false, 'new'],
    ['/new worktree fix the sidebar', 'fix the sidebar', true, false, 'new-worktree'],
    ['/new plan fix the sidebar', 'fix the sidebar', false, true, 'new-plan'],
    ['/new wp fix the sidebar', 'fix the sidebar', true, true, 'new-wp'],
  ] as const
  for (const [input, prompt, worktreeRequested, planModeRequested, commandID] of promptCases) {
    const parsed = parseDesktopNewSessionCommand(input)
    assert(parsed?.prompt === prompt, `expected ${input} prompt to be preserved`)
    assert(parsed?.worktreeRequested === worktreeRequested, `expected ${input} worktree intent to match`)
    assert(parsed?.planModeRequested === planModeRequested, `expected ${input} plan intent to match`)
    const palette = buildDesktopSlashPaletteState(input)
    assert(palette.exactMatch?.id === commandID, `expected ${input} to resolve its longest command prefix`)
    assert(palette.matches[0]?.id === commandID, `expected ${input} to select its compound palette entry`)
  }
}

function testNewWpForwardsOnlyItsPromptToRouter(): void {
  const input = '  /NEW   wp   Keep WP in the prompt\nwith exact body text  '
  const parsed = parseDesktopNewSessionCommand(input)

  assert(parsed?.prompt === 'Keep WP in the prompt\nwith exact body text', 'expected /new wp to remove only its command prefix')
  assert(parsed?.worktreeRequested === true, 'expected /new wp to preserve managed-worktree intent')
  assert(parsed?.planModeRequested === true, 'expected /new wp to preserve plan-mode intent')
  assert(buildDesktopSlashPaletteState(input).exactMatch?.id === 'new-wp', 'expected /new wp prompt input to dispatch the compound command')
}

function testTaskCommandAcceptsFullArguments(): void {
  const commands = getDesktopSlashCommands()
  const task = commands.find((command) => command.id === 'task')
  const taskPlan = commands.find((command) => command.id === 'task-plan')
  assert(Boolean(task), 'expected /task command to exist')
  assert(Boolean(taskPlan), 'expected /task plan command to exist')
  assert(task?.state === 'ready', 'expected /task command to be ready')
  assert((task?.action as DesktopSlashCommandAction | undefined)?.kind === 'start-background-router-session', 'expected /task to start a background Router session')
  assert((taskPlan?.action as DesktopSlashCommandAction | undefined)?.kind === 'start-background-router-session', 'expected /task plan to start a background Router session')
  assert(task?.tips.some((tip) => tip.includes('/task <prompt>')) === true, 'expected /task guidance to show required prompt syntax')
  assert(taskPlan?.tips.some((tip) => tip.includes('/task plan <prompt>')) === true, 'expected /task plan guidance to show required prompt syntax')

  const palette = buildDesktopSlashPaletteState('/task fix the sidebar now')
  assert(palette.exactMatch?.id === 'task', 'expected /task arguments to preserve the exact command match')
  assert(palette.hasArguments === true, 'expected /task request to be recognized as arguments')

  const planPalette = buildDesktopSlashPaletteState('/task plan fix the sidebar now')
  assert(planPalette.exactMatch?.id === 'task-plan', 'expected /task plan arguments to select the compound command')
  assert(planPalette.matches[0]?.id === 'task-plan', 'expected /task plan to lead the palette matches')
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

function testTipsCommandIsReadyAndAcceptsArguments(): void {
  const tips = getDesktopSlashCommands().find((command) => command.id === 'tips')
  assert(Boolean(tips), 'expected /tips command to exist')
  assert(tips?.state === 'ready', 'expected /tips command to be ready')
  assert(tips?.action.kind === 'toggle-tips', 'expected /tips to toggle home tips')
  assert(tips?.tips.some((tip) => tip.includes('on|off|toggle|status')) === true, 'expected /tips argument discovery')

  const palette = buildDesktopSlashPaletteState('/tips off')
  assert(palette.exactMatch?.id === 'tips', 'expected /tips arguments to retain the exact command')
  assert(palette.hasArguments, 'expected /tips off to expose arguments')
  assert(palette.matches[0]?.id === 'tips', 'expected /tips to remain selected with arguments')
}

function testActionsCommandOpensQuickChooser(): void {
  const actions = getDesktopSlashCommands().find((command) => command.id === 'actions')
  assert(Boolean(actions), 'expected /actions command to exist')
  assert(actions?.state === 'ready', 'expected /actions command to be ready')
  assert(actions?.action.kind === 'open-action-chooser', 'expected /actions to open the in-chat Action chooser')
  assert(buildDesktopSlashPaletteState('/actions list').exactMatch?.id === 'actions', 'expected /actions list to resolve to the Action chooser')
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
  testFeedbackCommandOpensModal()
  testPlanCommandIsReady()
  testSlashPaletteMatchesPlan()
  testCodexOpensUsageWithoutChangingModels()
  testMediaCommandOpensQuickSettings()
  testAICommitCommandTriggersCanonicalWorkflow()
  testFastCommandIsRetired()
  testMCPCommandIsDeferredAndExaRequiresAPIKey()
  testWorktreeOnCommandIsReady()
  testWorktreeCommandIsRetiredAndWorktreesRemains()
  testNewSessionCommandVariantsPrimeRouterChips()
  testNewWpForwardsOnlyItsPromptToRouter()
  testTaskCommandAcceptsFullArguments()
  testTaskCommandParsesModeDirective()
  testRetiredCommandsAreNotSuggested()
  testTipsCommandIsReadyAndAcceptsArguments()
  testActionsCommandOpensQuickChooser()
  testKeybindingsWarnsAboutDesktopShortcuts()
  console.log('slash-commands tests passed')
}

main()
