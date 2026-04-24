import { getDesktopSlashCommands, buildDesktopSlashPaletteState } from './slash-commands'
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

function testFastCommandIsReady(): void {
  const fast = getDesktopSlashCommands().find((command) => command.command === '/fast')
  assert(Boolean(fast), 'expected /fast command to exist')
  assert(fast?.state === 'ready', 'expected /fast command to be ready')
  assert((fast?.action as DesktopSlashCommandAction | undefined)?.kind === 'toggle-fast', 'expected /fast to toggle Fast')

  const palette = buildDesktopSlashPaletteState('/fast')
  assert(palette.exactMatch?.id === 'fast', 'expected /fast to match fast command')
  assert(palette.exactMatch?.state === 'ready', 'expected /fast exact match to be ready')
}

function main(): void {
  testPlanCommandIsReady()
  testSlashPaletteMatchesPlan()
  testFastCommandIsReady()
  console.log('slash-commands tests passed')
}

main()
