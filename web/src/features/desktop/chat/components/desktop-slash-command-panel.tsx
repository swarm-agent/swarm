import { Key, FolderOpen, Shield, GitBranch, CircleHelp, Bot, Palette, Cpu, GitCommitHorizontal, Keyboard, ListChecks, Lightbulb, Plus, Shrink, type LucideIcon } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import type { DesktopSlashCommand, DesktopSlashPaletteState } from '../services/slash-commands'

interface DesktopSlashCommandPanelProps {
  palette: DesktopSlashPaletteState
  selectedIndex: number
  onHover: (index: number) => void
  onSelect: (command: DesktopSlashCommand) => void
}

function commandIcon(command: DesktopSlashCommand): LucideIcon {
  switch (command.id) {
    case 'auth':
      return Key
    case 'vault':
      return Shield
    case 'worktrees':
      return GitBranch
    case 'workspace':
      return FolderOpen
    case 'new':
    case 'new-worktree':
    case 'new-plan':
    case 'new-wp':
      return Plus
    case 'agents':
      return Bot
    case 'models':
      return Cpu
    case 'thinking':
      return Lightbulb
    case 'commit':
      return GitCommitHorizontal
    case 'plan':
      return ListChecks
    case 'keybindings':
      return Keyboard
    case 'compact':
      return Shrink
    case 'theme':
      return Palette
    case 'swarm':
      return CircleHelp
    default:
      return CircleHelp
  }
}

export function DesktopSlashCommandPanel({ palette, selectedIndex, onHover, onSelect }: DesktopSlashCommandPanelProps) {
  if (!palette.active) {
    return null
  }

  const commands = palette.matches.filter((command) => command.state === 'ready')
  if (commands.length === 0) {
    return null
  }

  return (
    <div className="overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-[var(--shadow-panel)]">
      <div className="border-b border-[var(--app-border)] px-3 py-2 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
        Slash commands
      </div>
      <div className="max-h-[min(360px,45vh)] overflow-y-auto p-1.5">
        {commands.map((command, index) => {
          const Icon = commandIcon(command)
          const selected = index === selectedIndex
          return (
            <button
              key={command.id}
              type="button"
              className={cn(
                'grid w-full grid-cols-[40px_minmax(0,1fr)_auto] items-center gap-3 rounded-xl px-3 py-2.5 text-left transition',
                selected
                  ? 'bg-[var(--app-surface-active)] text-[var(--app-text)] ring-1 ring-[var(--app-border-accent)]'
                  : 'text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]',
              )}
              onMouseDown={(event) => event.preventDefault()}
              onMouseEnter={() => onHover(index)}
              onFocus={() => onHover(index)}
              onClick={() => onSelect(command)}
            >
              <span className={cn(
                'flex h-9 w-9 items-center justify-center rounded-lg border',
                selected ? 'border-[var(--app-border-accent)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-bg-alt)] text-[var(--app-text-muted)]',
              )}>
                <Icon size={17} />
              </span>
              <span className="min-w-0">
                <span className="block truncate text-sm font-semibold text-[var(--app-text)]">{command.command}</span>
                <span className="block truncate text-xs leading-5 text-[var(--app-text-muted)]">{command.hint}</span>
              </span>
              <span className="hidden shrink-0 text-[11px] text-[var(--app-text-subtle)] sm:inline">{command.actionLabel}</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}
