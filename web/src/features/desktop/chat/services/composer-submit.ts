import { buildDesktopSlashPaletteState, parseDesktopTaskCommand, type DesktopSlashCommand } from './slash-commands'

export type DesktopComposerSubmitResult = 'submitted' | 'submit-failed' | 'stopped' | 'background-router-started' | 'background-router-failed'

export interface SubmitDesktopComposerInput<TAttachment = never, TSelection = never> {
  draft: string
  canStop: boolean
  clear: () => void
  attachments?: TAttachment[]
  selections?: TSelection[]
  onSubmit: (draft: string, attachments: TAttachment[], selections: TSelection[]) => void | Promise<void>
  onStop?: () => void | Promise<void>
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
}

export function desktopComposerBackgroundRouterCommand(draft: string): DesktopSlashCommand | null {
  const exactMatch = buildDesktopSlashPaletteState(draft).exactMatch
  return exactMatch?.action.kind === 'start-background-router-session' ? exactMatch : null
}

export async function submitDesktopComposer<TAttachment, TSelection = never>(input: SubmitDesktopComposerInput<TAttachment, TSelection>): Promise<DesktopComposerSubmitResult> {
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

  try {
    await input.onSubmit(input.draft, input.attachments ?? [], input.selections ?? [])
  } catch {
    return 'submit-failed'
  }
  input.clear()
  return 'submitted'
}
