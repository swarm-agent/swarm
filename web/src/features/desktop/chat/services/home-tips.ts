export const DESKTOP_HOME_TIPS = [
  'Ask Swarm for three theme variants, then pick one.',
  'Apply a theme to just one workspace.',
  'Turn a trusted project script into an Action.',
  'Ask Swarm to organize your workspace Actions.',
  'Save recurring project conventions as a Skill.',
  'Attach a Skill to load project-specific rules.',
  'Link directories when work spans repositories.',
  'Use a worktree session for risky or long tasks.',
  'Run Finders in parallel to audit separate areas.',
  'Send independent tasks to Coders in parallel.',
  'Ask Designer for UI variants before choosing.',
  'Keep design variants until you choose one.',
  'Find review worktrees missing from your checkout.',
  'Search past sessions for errors or decisions.',
  'Archive finished sessions together after review.',
  'Set auto-archive timing for reviewed sessions.',
  'Switch between Desktop and TUI mid-session.',
  'Use Plan mode to lock scope before coding.',
  'Add a checkpoint for a separate review step.',
  'Change direction by revising the active checkpoint.',
  'Assign different models to each system agent.',
  'Use a fast default model and a reasoning plan model.',
  'Save allow rules for trusted tool calls.',
  'Drag files, snippets, or images into chat.',
  'TUI: Ctrl+P opens plan and checkpoint status.',
  'TUI: Ctrl+X browses recent sessions.',
  'Type /tips to hide or show these tips.',
] as const

let lastDesktopHomeTipIndex = -1

export function selectDesktopHomeTipIndex(previous = lastDesktopHomeTipIndex, random = Math.random): number {
  if (DESKTOP_HOME_TIPS.length <= 1) return 0
  const selected = previous < 0 || previous >= DESKTOP_HOME_TIPS.length
    ? Math.floor(random() * DESKTOP_HOME_TIPS.length)
    : (previous + 1 + Math.floor(random() * (DESKTOP_HOME_TIPS.length - 1))) % DESKTOP_HOME_TIPS.length
  lastDesktopHomeTipIndex = selected
  return selected
}

export type DesktopTipsCommandMode = 'toggle' | 'on' | 'off' | 'status'

export function parseDesktopTipsCommand(input: string): DesktopTipsCommandMode | null {
  const match = input.trim().match(/^\/tips(?:\s+(\S+))?\s*$/i)
  if (!match) return null
  const mode = (match[1] ?? 'toggle').toLowerCase()
  return mode === 'toggle' || mode === 'on' || mode === 'off' || mode === 'status' ? mode : null
}

export function resolveDesktopTipsEnabled(mode: DesktopTipsCommandMode, current: boolean): boolean {
  if (mode === 'on') return true
  if (mode === 'off') return false
  if (mode === 'toggle') return !current
  return current
}

export interface DesktopTipsCommandResult<T> {
  mode: DesktopTipsCommandMode
  enabled: boolean
  saved: T | null
}

export async function executeDesktopTipsCommand<T>(
  input: string,
  current: boolean,
  persist: (enabled: boolean) => Promise<T>,
): Promise<DesktopTipsCommandResult<T> | null> {
  const mode = parseDesktopTipsCommand(input)
  if (!mode) return null
  const enabled = resolveDesktopTipsEnabled(mode, current)
  return {
    mode,
    enabled,
    saved: mode === 'status' ? null : await persist(enabled),
  }
}
