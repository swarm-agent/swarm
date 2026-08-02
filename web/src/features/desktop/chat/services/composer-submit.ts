import { buildDesktopSlashPaletteState, type DesktopSlashCommand } from './slash-commands'

export type DesktopComposerSubmitResult = 'submitted' | 'stopped' | 'task-queued' | 'task-queue-failed'

export interface SubmitDesktopComposerInput<TAttachment = never> {
  draft: string
  canStop: boolean
  clear: () => void
  attachments?: TAttachment[]
  onSubmit: (draft: string, attachments: TAttachment[]) => void | Promise<void>
  onStop?: () => void | Promise<void>
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
}

export function desktopComposerTaskCommand(draft: string): DesktopSlashCommand | null {
  const exactMatch = buildDesktopSlashPaletteState(draft).exactMatch
  return exactMatch?.action.kind === 'queue-ai-task' ? exactMatch : null
}

export async function submitDesktopComposer<TAttachment>(input: SubmitDesktopComposerInput<TAttachment>): Promise<DesktopComposerSubmitResult> {
  const taskCommand = desktopComposerTaskCommand(input.draft)

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
  void input.onSubmit(input.draft, input.attachments ?? [])
  return 'submitted'
}
