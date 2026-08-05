export const DESKTOP_HOME_TIPS = [
  'Ask Swarm for three theme variants, then apply your favorite.',
  'Apply a theme to one workspace without changing your global theme.',
  'Turn a trusted project script into an Action with prompted inputs.',
  'Ask Swarm to organize your Actions; launch them from the Action menu.',
  'Turn recurring project conventions into a reusable Workspace Skill.',
  'Attach a Skill before prompting to load project-specific rules.',
  'Drop a Todo into chat to turn it into an active work session.',
  'Ask Swarm to update several workspace Todos in one pass.',
  'Link related directories when work crosses repository boundaries.',
  'Open risky or long-running work in an isolated worktree session.',
  'Run several Finders in parallel to audit different subsystems.',
  'Send independent implementation scopes to Coders in parallel.',
  'Ask Designer for multiple UI variants when the direction is unclear.',
  'Keep every design variant until you choose one to promote.',
  'Integrate selected Coder branches as one all-or-nothing batch.',
  'Find review worktrees that are missing from your current checkout.',
  'Search past sessions for an error, symbol, or earlier decision.',
  'Archive several finished sessions together after review.',
  'Set an auto-archive delay for reviewed sessions in Settings.',
  'Move between Desktop and TUI without abandoning the current session.',
  'Use Plan mode to lock scope and checkpoints before implementation.',
  'Add another checkpoint when work needs its own review boundary.',
  'Change direction explicitly; Swarm can revise the active checkpoint.',
  'Assign different models to Swarm, Finder, Coder, and Designer.',
  'Use a fast model for Default mode and a reasoning model for Plan mode.',
  'Save allow rules for trusted tools and deny rules for risky patterns.',
  'Drag files, snippets, or images into chat as working context.',
  'TUI: press Ctrl+P to inspect the full plan and checkpoint status.',
  'TUI: press Ctrl+W to switch the active workspace directory.',
  'TUI: press Ctrl+X to browse and resume recent sessions.',
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
