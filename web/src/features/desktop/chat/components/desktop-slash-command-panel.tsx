import { Key, FolderOpen, Shield, GitBranch, CircleHelp, Bot, Palette, Cpu, GitCommitHorizontal, Plus, Shrink, type LucideIcon } from 'lucide-react'
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
      return Plus
    case 'agents':
      return Bot
    case 'models':
      return Cpu
    case 'commit':
      return GitCommitHorizontal
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
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-5">
      {commands.map((command, index) => {
        const Icon = commandIcon(command)
        const selected = index === selectedIndex
        return (
          <button
            key={command.id}
            type="button"
            className={cn(
              'group rounded-2xl border px-4 py-3 text-left transition',
              selected
                ? 'border-[var(--app-border-accent)] bg-[var(--app-bg-alt)] shadow-[var(--shadow-panel)]'
                : 'border-[var(--app-border)] bg-[var(--app-surface)] hover:border-[var(--app-border-accent)] hover:bg-[var(--app-bg-alt)]',
            )}
            onMouseDown={(event) => event.preventDefault()}
            onMouseEnter={() => onHover(index)}
            onFocus={() => onHover(index)}
            onClick={() => onSelect(command)}
          >
            <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] text-[var(--app-text)]">
              <Icon size={18} />
            </div>
            <div className="truncate text-sm font-semibold text-[var(--app-text)]">{command.command}</div>
            <div className="mt-1 line-clamp-2 text-xs leading-5 text-[var(--app-text-muted)]">{command.hint}</div>
          </button>
        )
      })}
    </div>
  )
}
