import { type CSSProperties, type ReactNode } from 'react'
import { ArrowLeft, Loader2, Moon, Plus } from 'lucide-react'

export type SwarmToolSidebarSession = {
  id: string
  title: string
  subtitle?: string
}

export type SwarmToolSidebarAction = {
  id: string
  label: string
  icon?: ReactNode
  suffix?: ReactNode
  onClick: () => void
  disabled?: boolean
}

type SwarmToolSidebarProps = {
  backLabel: string
  onBack: () => void
  darkModeEnabled: boolean
  onToggleDarkMode: () => void
  darkModeStyle?: CSSProperties
  darkModeActiveClassName?: string
  toolIcon: ReactNode
  toolTitle: string
  toolDescription: string
  createLabel: string
  createTitle: string
  onCreateTitleChange: (value: string) => void
  createPlaceholder: string
  createPrefix?: ReactNode
  onCreate: () => void
  creating: boolean
  createDisabled?: boolean
  createButtonLabel?: string
  creatingButtonLabel?: string
  sessionsLabel: string
  sessionsLoading?: boolean
  sessions: SwarmToolSidebarSession[]
  selectedSessionId?: string | null
  onSelectSession: (sessionId: string) => void
  emptySessionsMessage: string
  defaultSessionTitle: string
  actions?: SwarmToolSidebarAction[]
  children?: ReactNode
}

export function SwarmToolSidebar({
  backLabel,
  onBack,
  darkModeEnabled,
  onToggleDarkMode,
  darkModeStyle,
  darkModeActiveClassName,
  toolIcon,
  toolTitle,
  toolDescription,
  createLabel,
  createTitle,
  onCreateTitleChange,
  createPlaceholder,
  createPrefix,
  onCreate,
  creating,
  createDisabled,
  createButtonLabel = 'Start session',
  creatingButtonLabel = 'Starting…',
  sessionsLabel,
  sessionsLoading,
  sessions,
  selectedSessionId,
  onSelectSession,
  emptySessionsMessage,
  defaultSessionTitle,
  actions = [],
  children,
}: SwarmToolSidebarProps) {
  return (
    <aside className="mr-5 flex w-[276px] shrink-0 flex-col border-r border-[var(--app-border)] pr-4 font-mono text-[12px] text-[var(--app-text-muted)]">
      <div className="mb-4 flex items-center justify-between gap-2">
        <button type="button" onClick={onBack} className="flex h-9 min-w-0 flex-1 items-center gap-2 px-2 text-left hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
          <ArrowLeft size={14} />
          <span className="truncate">{backLabel}</span>
        </button>
        <button
          type="button"
          onClick={onToggleDarkMode}
          style={darkModeStyle}
          className={`grid h-9 w-9 shrink-0 place-items-center border ${darkModeEnabled ? (darkModeActiveClassName ?? 'border-[var(--app-primary)] bg-[var(--app-surface)] text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]') : 'border-[var(--app-border)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}
          aria-label="Toggle dark mode override for this page"
          aria-pressed={darkModeEnabled}
          title="Toggle dark mode override for this page"
        >
          <Moon size={15} aria-hidden="true" />
        </button>
      </div>

      <div className="border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
        <div className="flex items-center gap-2 text-[var(--app-text)]">
          <span className="grid h-8 w-8 place-items-center border border-[var(--app-border)] bg-[var(--app-bg)] text-[var(--app-primary)]">
            {toolIcon}
          </span>
          <div className="min-w-0">
            <p className="text-[10px] uppercase tracking-[0.22em] text-[var(--app-text-subtle)]">Tool</p>
            <h1 className="truncate text-sm font-semibold">{toolTitle}</h1>
          </div>
        </div>
        <p className="mt-3 text-[11px] leading-5 text-[var(--app-text-muted)]">
          {toolDescription}
        </p>
      </div>

      <div className="mt-4 border border-[var(--app-border)] bg-[var(--app-bg)] p-3">
        {createPrefix ? <div className="mb-3">{createPrefix}</div> : null}
        <p className="text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">{createLabel}</p>
        <input
          value={createTitle}
          onChange={(event) => onCreateTitleChange(event.currentTarget.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              onCreate()
            }
          }}
          placeholder={createPlaceholder}
          className="mt-3 h-9 w-full border border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-[12px] text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-subtle)] focus:border-[var(--app-primary)]"
        />
        <button
          type="button"
          onClick={onCreate}
          disabled={creating || createDisabled}
          className="mt-2 flex h-9 w-full items-center justify-center gap-2 border border-[var(--app-border)] bg-transparent px-2 text-[12px] font-medium text-[var(--app-text)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
        >
          {creating ? <Loader2 size={14} className="animate-spin" /> : <Plus size={14} />}
          {creating ? creatingButtonLabel : createButtonLabel}
        </button>
      </div>

      <div className="mt-4 border-y border-[var(--app-border)] py-3">
        <div className="mb-2 flex items-center justify-between px-2">
          <p className="text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">{sessionsLabel}</p>
          {sessionsLoading ? <Loader2 size={12} className="animate-spin" /> : null}
        </div>
        <div className="max-h-[236px] overflow-y-auto pr-1">
          {sessions.length === 0 && !sessionsLoading ? (
            <div className="px-2 py-3 text-[11px] leading-5 text-[var(--app-text-subtle)]">
              {emptySessionsMessage}
            </div>
          ) : (
            sessions.map((session) => (
              <button
                key={session.id}
                type="button"
                onClick={() => onSelectSession(session.id)}
                className={`mb-1 w-full px-2 py-2 text-left hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] ${selectedSessionId === session.id ? 'bg-[var(--app-surface-active)] text-[var(--app-text)]' : ''}`}
              >
                <span className="block truncate text-[12px] font-medium">{session.title || defaultSessionTitle}</span>
                {session.subtitle ? (
                  <span className="mt-1 block truncate text-[10px] text-[var(--app-text-subtle)]">{session.subtitle}</span>
                ) : null}
              </button>
            ))
          )}
        </div>
      </div>

      {actions.length > 0 ? (
        <div className="mt-4 flex flex-col gap-1 border-b border-[var(--app-border)] pb-3">
          {actions.map((action) => (
            <button key={action.id} type="button" onClick={action.onClick} disabled={action.disabled} className="flex min-h-[30px] items-center justify-between gap-2 px-2 text-left disabled:cursor-not-allowed disabled:opacity-50 hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
              <span className="flex min-w-0 items-center gap-2">{action.icon}<span className="truncate">{action.label}</span></span>
              {action.suffix ? <span className="text-[10px] text-[var(--app-text-subtle)]">{action.suffix}</span> : null}
            </button>
          ))}
        </div>
      ) : null}

      {children}
    </aside>
  )
}
