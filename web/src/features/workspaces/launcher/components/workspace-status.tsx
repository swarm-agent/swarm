interface WorkspaceStatusProps {
  kind: 'error' | 'empty'
  title: string
  message: string
  actionLabel?: string
  onAction?: () => void
}

const statusToneClass: Record<WorkspaceStatusProps['kind'], string> = {
  error: 'border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] text-[var(--app-danger)]',
  empty: 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]',
}

export function WorkspaceStatus({ kind, title, message, actionLabel, onAction }: WorkspaceStatusProps) {
  return (
    <section className={`grid gap-4 rounded-2xl border p-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center ${statusToneClass[kind]}`}>
      <div className="grid gap-1.5">
        <strong className="text-sm font-semibold text-[var(--app-text)]">{title}</strong>
        <p className="text-sm leading-6">{message}</p>
      </div>
      {actionLabel && onAction ? (
        <button
          type="button"
          className="inline-flex min-h-10 items-center justify-center rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 text-sm font-medium text-[var(--app-text)] transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]"
          onClick={onAction}
        >
          {actionLabel}
        </button>
      ) : null}
    </section>
  )
}
