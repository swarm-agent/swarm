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

function main(): void {
  testPlanCommandIsReady()
  testSlashPaletteMatchesPlan()
  console.log('slash-commands tests passed')
}

main()
