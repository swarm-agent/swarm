export type DesktopShortcutActionID =
  | 'quick-actions'
  | 'new-session'
  | 'settings'
  | 'search-chats'
  | 'latest-needs-approval'
  | 'previous-chat'
  | 'toggle-plan-auto'

export interface DesktopShortcutDefinition {
  id: DesktopShortcutActionID
  label: string
  description: string
  keys: string[]
  availability: string
  group: 'Navigation' | 'Chat' | 'Session mode'
}

export const DESKTOP_SHORTCUTS: DesktopShortcutDefinition[] = [
  {
    id: 'quick-actions',
    label: 'Open quick actions',
    description: 'Show Desktop shortcut actions and run the supported ones from one modal.',
    keys: ['⌘/Ctrl', 'K'],
    availability: 'Available anywhere in Desktop unless another modal or text field owns the shortcut.',
    group: 'Navigation',
  },
  {
    id: 'new-session',
    label: 'New session',
    description: 'Start a fresh chat in the current or top selected workspace.',
    keys: ['⌘/Ctrl', 'N'],
    availability: 'Requires a selected workspace.',
    group: 'Chat',
  },
  {
    id: 'settings',
    label: 'Open settings',
    description: 'Open Desktop Settings, preserving the current workspace route when possible.',
    keys: ['⌘/Ctrl', ','],
    availability: 'Available anywhere in Desktop.',
    group: 'Navigation',
  },
  {
    id: 'search-chats',
    label: 'Search chats',
    description: 'Open Desktop chat search.',
    keys: ['⌘/Ctrl', '⇧', 'F'],
    availability: 'Available anywhere in Desktop.',
    group: 'Navigation',
  },
  {
    id: 'latest-needs-approval',
    label: 'Latest needs approval',
    description: 'Jump to the newest visible chat that has a pending permission request.',
    keys: ['⌘/Ctrl', '⇧', 'A'],
    availability: 'Requires a session with pending permissions in the sidebar.',
    group: 'Chat',
  },
  {
    id: 'previous-chat',
    label: 'Previous chat',
    description: 'Return to the previously selected Desktop chat in this window.',
    keys: ['⌘/Ctrl', '['],
    availability: 'Available after switching between chats in the same Desktop window.',
    group: 'Chat',
  },
  {
    id: 'toggle-plan-auto',
    label: 'Toggle plan/auto mode',
    description: 'Switch the active chat composer between plan and auto mode.',
    keys: ['⇧', 'Tab'],
    availability: 'Only intercepted when the active composer supports mode switching and focus is safe to keep in chat.',
    group: 'Session mode',
  },
]

export function formatDesktopShortcutKeys(keys: string[]): string {
  return keys.join(' ')
}
