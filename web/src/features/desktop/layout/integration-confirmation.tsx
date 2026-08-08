import { Archive, GitMerge, LoaderCircle } from 'lucide-react'

export interface IntegrationConfirmationProps {
  targetBranch: string | undefined
  worktreeBranch: string | undefined
  archiveAfter: boolean
  busy: boolean
  integrationComplete: boolean | undefined
  retrying: boolean
  onArchiveAfterChange: (checked: boolean) => void
  onConfirm: () => void
  onCancel: () => void
}

export function IntegrationConfirmation({
  targetBranch,
  worktreeBranch,
  archiveAfter,
  busy,
  integrationComplete,
  retrying,
  onArchiveAfterChange,
  onConfirm,
  onCancel,
}: IntegrationConfirmationProps) {
  const target = targetBranch?.trim() || 'the target branch'
  const source = worktreeBranch?.trim() || 'this session worktree'
  const shouldArchive = Boolean(archiveAfter || integrationComplete)
  const confirmLabel = integrationComplete
    ? 'Try archive again'
    : retrying
      ? shouldArchive ? 'Try integration and archive again' : 'Try integration again'
      : shouldArchive ? 'Integrate and archive' : `Integrate into ${target}`

  return (
    <div className="grid gap-2 p-2" data-integration-confirmation>
      <div className="min-w-0 px-1">
        <h3 className="text-sm font-semibold text-[var(--app-text)]">
          {integrationComplete ? 'Integration complete' : retrying ? `Try integration into ${target} again?` : `Confirm integration into ${target}`}
        </h3>
        <p className="mt-1 text-xs leading-5 text-[var(--app-text-subtle)]">
          {integrationComplete
            ? `The commits from ${source} are already in ${target}. Only the archive step remains.`
            : `This will apply the commits from ${source} to ${target}.`}
        </p>
      </div>

      <label className="flex cursor-pointer items-start gap-2 rounded-md border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2.5 text-xs text-[var(--app-text)]">
        <input
          type="checkbox"
          className="mt-0.5"
          checked={shouldArchive}
          disabled={busy || Boolean(integrationComplete)}
          onChange={(event) => onArchiveAfterChange(event.target.checked)}
        />
        <span className="min-w-0">
          <strong className="block">Archive session after integration</strong>
          <span className="mt-0.5 block leading-5 text-[var(--app-text-subtle)]">
            {integrationComplete ? 'Integration succeeded; retrying will only archive the session.' : 'Optional. The session is archived only if integration succeeds.'}
          </span>
        </span>
      </label>

      <div className="flex flex-wrap justify-end gap-2 pt-1">
        <button
          type="button"
          className="inline-flex min-h-10 items-center justify-center rounded-md border border-[var(--app-border)] px-3 py-1.5 text-xs font-medium text-[var(--app-text)] hover:bg-[var(--app-surface-subtle)] disabled:opacity-50"
          disabled={busy}
          onClick={onCancel}
        >
          Cancel
        </button>
        <button
          type="button"
          className="inline-flex min-h-10 items-center justify-center gap-1.5 rounded-md border border-[var(--app-primary)] bg-[var(--app-primary)] px-3 py-1.5 text-xs font-semibold text-[var(--app-primary-text)] disabled:opacity-50"
          disabled={busy}
          onClick={onConfirm}
        >
          {busy ? <LoaderCircle size={13} className="animate-spin" /> : shouldArchive ? <Archive size={13} /> : <GitMerge size={13} />}
          {busy ? integrationComplete ? 'Archiving…' : 'Integrating…' : confirmLabel}
        </button>
      </div>
    </div>
  )
}
