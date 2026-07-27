import { buildDesktopSlashPaletteState, type DesktopSlashCommand } from './slash-commands'

export type DesktopComposerSubmitResult = 'submitted' | 'stopped' | 'task-queued' | 'task-queue-failed'

export interface SubmitDesktopComposerInput {
  draft: string
  canStop: boolean
  clear: () => void
  onSubmit: (draft: string) => void | Promise<void>
  onStop?: () => void | Promise<void>
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
}

export async function submitDesktopComposer(input: SubmitDesktopComposerInput): Promise<DesktopComposerSubmitResult> {
  const submittedPalette = buildDesktopSlashPaletteState(input.draft)
  const taskCommand = submittedPalette.exactMatch?.action.kind === 'queue-ai-task'
    ? submittedPalette.exactMatch
    : null

  if (taskCommand) {
    if (!input.onSlashCommand) return 'task-queue-failed'
    try {
      await input.onSlashCommand(taskCommand, input.draft)
      input.clear()
      return 'task-queued'
    } catch {
      return 'task-queue-failed'
    }
  }

  if (input.canStop) {
    void input.onStop?.()
    return 'stopped'
  }

  input.clear()
  void input.onSubmit(input.draft)
  return 'submitted'
}
