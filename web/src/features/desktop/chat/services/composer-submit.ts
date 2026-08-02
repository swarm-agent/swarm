import { buildDesktopSlashPaletteState, parseDesktopTaskCommand, type DesktopSlashCommand } from './slash-commands'

export type DesktopComposerSubmitResult = 'submitted' | 'stopped' | 'background-router-started' | 'background-router-failed'

export interface SubmitDesktopComposerInput<TAttachment = never> {
  draft: string
  canStop: boolean
  clear: () => void
  attachments?: TAttachment[]
  onSubmit: (draft: string, attachments: TAttachment[]) => void | Promise<void>
  onStop?: () => void | Promise<void>
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
}

export function desktopComposerBackgroundRouterCommand(draft: string): DesktopSlashCommand | null {
  const exactMatch = buildDesktopSlashPaletteState(draft).exactMatch
  return exactMatch?.action.kind === 'start-background-router-session' ? exactMatch : null
}

export async function submitDesktopComposer<TAttachment>(input: SubmitDesktopComposerInput<TAttachment>): Promise<DesktopComposerSubmitResult> {
  const backgroundRouterCommand = desktopComposerBackgroundRouterCommand(input.draft)

  if (backgroundRouterCommand) {
    if (!input.onSlashCommand) return 'background-router-failed'
    let dispatch: void | Promise<void>
    try {
      dispatch = input.onSlashCommand(backgroundRouterCommand, input.draft)
    } catch {
      return 'background-router-failed'
    }
    void Promise.resolve(dispatch).catch(() => {
      // The owning pane reports background launch failures through its toast.
    })
    if (!parseDesktopTaskCommand(input.draft).request) return 'background-router-failed'
    input.clear()
    return 'background-router-started'
  }

  if (input.canStop) {
    void input.onStop?.()
    return 'stopped'
  }

  input.clear()
  void input.onSubmit(input.draft, input.attachments ?? [])
  return 'submitted'
}
