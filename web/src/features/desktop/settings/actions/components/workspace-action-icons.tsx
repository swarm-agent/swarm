import { Braces, Bug, FileCode2, Hammer, Play, RefreshCcw, Rocket, ShieldCheck, Sparkles, Terminal, Wrench, Zap, type LucideIcon } from 'lucide-react'
import { cn } from '../../../../../lib/cn'

interface IconChoice {
  id: string
  label: string
  icon: LucideIcon
}

export const WORKSPACE_ACTION_ICON_CHOICES: readonly IconChoice[] = [
  { id: 'zap', label: 'Quick', icon: Zap },
  { id: 'play', label: 'Run', icon: Play },
  { id: 'terminal', label: 'Terminal', icon: Terminal },
  { id: 'file-code', label: 'Script', icon: FileCode2 },
  { id: 'braces', label: 'Code', icon: Braces },
  { id: 'hammer', label: 'Build', icon: Hammer },
  { id: 'wrench', label: 'Tools', icon: Wrench },
  { id: 'bug', label: 'Debug', icon: Bug },
  { id: 'refresh', label: 'Refresh', icon: RefreshCcw },
  { id: 'shield', label: 'Verify', icon: ShieldCheck },
  { id: 'rocket', label: 'Deploy', icon: Rocket },
  { id: 'sparkles', label: 'Magic', icon: Sparkles },
] as const

export const DEFAULT_WORKSPACE_ACTION_ICON = 'zap'

export function normalizeWorkspaceActionIcon(value: string): string {
  return WORKSPACE_ACTION_ICON_CHOICES.some((choice) => choice.id === value) ? value : DEFAULT_WORKSPACE_ACTION_ICON
}

export function WorkspaceActionIcon({ icon, className, size = 16 }: { icon: string; className?: string; size?: number }) {
  const choice = WORKSPACE_ACTION_ICON_CHOICES.find((candidate) => candidate.id === normalizeWorkspaceActionIcon(icon)) ?? WORKSPACE_ACTION_ICON_CHOICES[0]
  const Icon = choice.icon
  return <Icon className={className} size={size} aria-hidden="true" />
}

export function WorkspaceActionIconPicker({ value, onChange, disabled = false }: { value: string; onChange: (icon: string) => void; disabled?: boolean }) {
  const normalized = normalizeWorkspaceActionIcon(value)
  return (
    <fieldset className="grid gap-2 md:col-span-2" disabled={disabled}>
      <legend className="text-sm font-medium">Icon</legend>
      <div className="grid grid-cols-4 gap-2 sm:grid-cols-6" data-workspace-action-icon-picker>
        {WORKSPACE_ACTION_ICON_CHOICES.map((choice) => {
          const selected = choice.id === normalized
          return (
            <button
              key={choice.id}
              type="button"
              aria-label={`Choose ${choice.label} icon`}
              aria-pressed={selected}
              title={choice.label}
              onClick={() => onChange(choice.id)}
              className={cn(
                'grid min-h-12 place-items-center rounded-lg border text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
                selected ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-bg)]',
              )}
            >
              <choice.icon size={18} aria-hidden="true" />
            </button>
          )
        })}
      </div>
    </fieldset>
  )
}
