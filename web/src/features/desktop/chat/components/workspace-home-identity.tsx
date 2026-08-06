import { useRef, useState, type ChangeEvent } from 'react'
import { ChevronDown, ImagePlus, Trash2, Upload } from 'lucide-react'

import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'

const MAX_WORKSPACE_ICON_BYTES = 1024 * 1024

interface WorkspaceHomeIdentityProps {
  workspace: WorkspaceEntry
  workspaces: WorkspaceEntry[]
  onOpenWorkspacePicker?: () => void
  onSetWorkspaceIcon?: (path: string, iconPNGDataURL: string) => Promise<void>
}

function SwarmMark({ className = 'size-7' }: { className?: string }) {
  return (
    <svg viewBox="0 0 400 400" className={className} aria-hidden="true">
      <rect x="20" y="20" width="360" height="360" rx="90" ry="90" fill="currentColor" opacity="0.15" />
      <rect x="60" y="60" width="280" height="280" rx="65" ry="65" fill="currentColor" opacity="0.35" />
      <rect x="100" y="100" width="200" height="200" rx="45" ry="45" fill="currentColor" opacity="0.60" />
      <rect x="140" y="140" width="120" height="120" rx="25" ry="25" fill="currentColor" opacity="0.85" />
      <rect x="180" y="180" width="40" height="40" rx="10" ry="10" fill="currentColor" />
    </svg>
  )
}

function WorkspaceIcon({ workspace, imageClassName, fallbackClassName }: { workspace: WorkspaceEntry; imageClassName: string; fallbackClassName: string }) {
  return workspace.iconPNGDataURL ? (
    <img src={workspace.iconPNGDataURL} alt="" className={`${imageClassName} object-cover`} />
  ) : (
    <SwarmMark className={fallbackClassName} />
  )
}

export function WorkspaceHomeIdentity({
  workspace,
  workspaces,
  onOpenWorkspacePicker,
  onSetWorkspaceIcon,
}: WorkspaceHomeIdentityProps) {
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const [managerOpen, setManagerOpen] = useState(false)
  const [targetPath, setTargetPath] = useState('')
  const [error, setError] = useState('')
  const [savingPath, setSavingPath] = useState('')
  const canSwitch = workspaces.length > 1 && Boolean(onOpenWorkspacePicker)

  function choosePNG(path: string) {
    setTargetPath(path)
    setError('')
    fileInputRef.current?.click()
  }

  async function handleFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file || !targetPath) return
    if (file.type !== 'image/png' || !file.name.toLowerCase().endsWith('.png')) {
      setError('Choose a PNG file. Other image formats are not supported.')
      return
    }
    if (file.size === 0 || file.size > MAX_WORKSPACE_ICON_BYTES) {
      setError('Choose a non-empty PNG smaller than 1 MB.')
      return
    }
    setSavingPath(targetPath)
    setError('')
    try {
      const dataURL = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader()
        reader.onload = () => resolve(String(reader.result ?? ''))
        reader.onerror = () => reject(new Error('Failed to read PNG.'))
        reader.readAsDataURL(file)
      })
      if (!dataURL.startsWith('data:image/png;base64,')) throw new Error('Choose a valid PNG file.')
      await onSetWorkspaceIcon?.(targetPath, dataURL)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Failed to save workspace icon.')
    } finally {
      setSavingPath('')
    }
  }

  async function removePNG(path: string) {
    setSavingPath(path)
    setError('')
    try {
      await onSetWorkspaceIcon?.(path, '')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Failed to remove workspace icon.')
    } finally {
      setSavingPath('')
    }
  }

  return (
    <div className="flex flex-col items-center">
      <button
        type="button"
        className="mb-3 grid size-14 place-items-center overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)] shadow-sm transition-colors hover:bg-[var(--app-surface-hover)] disabled:opacity-60"
        aria-label="Manage workspace icons"
        title="Manage workspace icons"
        disabled={!onSetWorkspaceIcon}
        onClick={() => {
          setError('')
          setManagerOpen(true)
        }}
      >
        <WorkspaceIcon workspace={workspace} imageClassName="size-full" fallbackClassName="size-7" />
      </button>

      {canSwitch ? (
        <button
          type="button"
          className="inline-flex items-center gap-1 text-lg font-semibold text-[var(--app-text)] hover:text-[var(--app-primary)]"
          aria-label={`Switch workspace. Current workspace: ${workspace.workspaceName}`}
          onClick={onOpenWorkspacePicker}
        >
          <span>{workspace.workspaceName}</span>
          <ChevronDown size={16} aria-hidden="true" />
        </button>
      ) : (
        <h1 className="text-lg font-semibold text-[var(--app-text)]">{workspace.workspaceName}</h1>
      )}

      {managerOpen ? (
        <Dialog
          role="dialog"
          aria-modal="true"
          aria-label="Workspace icon manager"
          className="z-[90] grid place-items-center p-3 sm:p-6"
          onKeyDown={(event) => {
            if (event.key === 'Escape') setManagerOpen(false)
          }}
        >
          <DialogBackdrop onClick={() => setManagerOpen(false)} />
          <DialogPanel
            className="max-h-[min(620px,calc(100vh-24px))] place-self-center gap-0 overflow-hidden border-0 bg-transparent p-0 shadow-none"
            style={{ width: 'min(546px, calc(100vw - 24px))' }}
          >
            <div className="flex max-h-[inherit] min-h-0 flex-col overflow-hidden rounded-3xl bg-[var(--app-surface)]">
              <div className="flex shrink-0 items-center justify-between gap-4 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-5 py-4 text-left">
                <div className="flex min-w-0 items-center gap-3">
                  <span className="grid size-10 shrink-0 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)]">
                    <ImagePlus size={20} aria-hidden="true" />
                  </span>
                  <div className="min-w-0">
                    <h2 className="font-semibold text-[var(--app-text)]">Workspace icons</h2>
                    <p className="mt-0.5 text-xs text-[var(--app-text-muted)]">Custom icons must be PNG files smaller than 1 MB.</p>
                  </div>
                </div>
                <ModalCloseButton onClick={() => setManagerOpen(false)} aria-label="Close workspace icon manager" />
              </div>

              <div className="min-h-0 flex-1 space-y-2 overflow-y-auto p-4 text-left">
                {workspaces.map((entry) => {
                  const saving = savingPath === entry.path
                  return (
                    <div key={entry.path} className="flex items-center gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3">
                      <span className="grid size-11 shrink-0 place-items-center overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)]">
                        <WorkspaceIcon workspace={entry} imageClassName="size-full" fallbackClassName="size-6" />
                      </span>
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="truncate text-sm font-medium text-[var(--app-text)]">{entry.workspaceName}</span>
                          {entry.path === workspace.path ? <span className="text-[10px] font-semibold uppercase tracking-wide text-[var(--app-primary)]">Current</span> : null}
                        </div>
                        <p className="truncate text-xs text-[var(--app-text-muted)]" title={entry.path}>{entry.path}</p>
                      </div>
                      <button
                        type="button"
                        className="inline-flex shrink-0 items-center gap-1.5 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-2 text-xs font-medium text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:cursor-wait disabled:opacity-50"
                        disabled={saving || Boolean(savingPath)}
                        onClick={() => choosePNG(entry.path)}
                      >
                        <Upload size={14} aria-hidden="true" />
                        {entry.iconPNGDataURL ? 'Change' : 'Upload'}
                      </button>
                      {entry.iconPNGDataURL ? (
                        <button
                          type="button"
                          className="grid size-8 shrink-0 place-items-center rounded-xl text-[var(--app-text-muted)] hover:bg-[var(--app-danger)]/10 hover:text-[var(--app-danger)] disabled:cursor-wait disabled:opacity-50"
                          aria-label={`Remove custom icon for ${entry.workspaceName}`}
                          title="Use default Swarm icon"
                          disabled={saving || Boolean(savingPath)}
                          onClick={() => void removePNG(entry.path)}
                        >
                          <Trash2 size={15} aria-hidden="true" />
                        </button>
                      ) : null}
                    </div>
                  )
                })}
                {error ? <p className="px-1 text-xs text-[var(--app-danger)]" role="alert">{error}</p> : null}
              </div>
            </div>
          </DialogPanel>
          <input ref={fileInputRef} type="file" accept="image/png,.png" className="hidden" onChange={handleFile} />
        </Dialog>
      ) : null}
    </div>
  )
}
