import { buildDesktopSlashPaletteState, parseDesktopIntegrationCommand, parseDesktopTaskCommand, type DesktopSlashCommand } from './slash-commands'

export type DesktopComposerSubmitResult = 'submitted' | 'submit-failed' | 'stopped' | 'background-router-started' | 'background-router-failed'

export interface SubmitDesktopComposerInput<TAttachment = never, TSelection = never, TVideoAttachment = never> {
  draft: string
  canStop: boolean
  clear: () => void
  attachments?: TAttachment[]
  selections?: TSelection[]
  videoAttachments?: TVideoAttachment[]
  onSubmit: (draft: string, attachments: TAttachment[], selections: TSelection[], videoAttachments: TVideoAttachment[]) => void | Promise<void>
  onStop?: () => void | Promise<void>
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
}

// Run local integration commands before message/attachment decoration or Stop routing.
// Rejections retain the draft and must never fall through to an AI message.
export async function dispatchDesktopIntegrationCommand(input: {
  draft: string
  developerMode: boolean
  clear: () => void
  onSlashCommand?: (command: DesktopSlashCommand, draft: string) => void | Promise<void>
}): Promise<boolean> {
  if (!/^\/integrate(?:\s|$)/i.test(input.draft.trim())) return false
  parseDesktopIntegrationCommand(input.draft, { developerMode: input.developerMode })
  const command = buildDesktopSlashPaletteState(input.draft, { developerMode: input.developerMode }).exactMatch
  if (command?.action.kind !== 'integrate-session' || !input.onSlashCommand) throw new Error('Integration is unavailable in this composer.')
  await input.onSlashCommand(command, input.draft)
  input.clear()
  return true
}

export function desktopComposerBackgroundRouterCommand(draft: string): DesktopSlashCommand | null {
  const exactMatch = buildDesktopSlashPaletteState(draft).exactMatch
  return exactMatch?.action.kind === 'start-background-router-session' ? exactMatch : null
}

export async function submitDesktopComposer<TAttachment, TSelection = never, TVideoAttachment = never>(input: SubmitDesktopComposerInput<TAttachment, TSelection, TVideoAttachment>): Promise<DesktopComposerSubmitResult> {
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
    await input.onSubmit(input.draft, input.attachments ?? [], input.selections ?? [], input.videoAttachments ?? [])
  } catch {
    return 'submit-failed'
  }
  input.clear()
  return 'submitted'
}
