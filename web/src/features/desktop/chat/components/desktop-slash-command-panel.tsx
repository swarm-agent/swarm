import { Key, FolderOpen, Shield, GitBranch, CircleHelp, Bot, Palette, Cpu, GitCommitHorizontal, Images, Keyboard, ListChecks, Lightbulb, MessageSquarePlus, Plus, Shrink, type LucideIcon } from 'lucide-react'
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
    case 'feedback':
      return MessageSquarePlus
    case 'auth':
      return Key
    case 'vault':
      return Shield
    case 'worktrees':
    case 'worktree-on':
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
    case 'commit-ai':
      return GitCommitHorizontal
    case 'plan':
      return ListChecks
    case 'artifact':
      return Images
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
    <div role="listbox" aria-label="Slash commands" className="overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-[var(--shadow-panel)]">
      <div className="border-b border-[var(--app-border)] px-3 py-1.5 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
        Slash commands
      </div>
      <div className="max-h-[min(240px,35vh)] overflow-y-auto p-1">
        {commands.map((command, index) => {
          const Icon = commandIcon(command)
          const selected = index === selectedIndex
          return (
            <button
              key={command.id}
              role="option"
              aria-selected={selected}
              type="button"
              className={cn(
                'grid w-full grid-cols-[28px_minmax(0,1fr)] items-center gap-2 rounded-lg px-2 py-1.5 text-left transition',
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
                'flex h-7 w-7 items-center justify-center rounded-md border',
                selected ? 'border-[var(--app-border-accent)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-bg-alt)] text-[var(--app-text-muted)]',
              )}>
                <Icon size={14} />
              </span>
              <span className="min-w-0">
                <span className="block truncate text-xs font-semibold text-[var(--app-text)]">{command.command}</span>
                <span className="block truncate text-[11px] leading-4 text-[var(--app-text-muted)]">{command.hint}</span>
              </span>
            </button>
          )
        })}
      </div>
    </div>
  )
}
