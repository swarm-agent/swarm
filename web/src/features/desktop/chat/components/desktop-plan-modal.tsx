import { useEffect, useMemo, useState } from 'react'
import { Copy, Check, AlertCircle, Save, Pencil, RotateCcw } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { Textarea } from '../../../../components/ui/textarea'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import type { DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord } from '../types/chat'
import { StructuredPlanDocumentView, structuredPlanDocumentToWire } from './structured-plan-document'

interface DesktopPlanModalProps {
  open: boolean
  plan: DesktopSessionPlanRecord | null
  revisions: DesktopSessionPlanRevisionRecord[]
  historyLoading: boolean
  saving: boolean
  error: string | null
  onOpenChange: (open: boolean) => void
  onCopy: (text: string) => Promise<boolean>
  onSave: (planText: string, document?: Record<string, unknown>) => Promise<void>
}

function useEscapeToClose(open: boolean, onClose: () => void) {
  useEffect(() => {
    if (!open) {
      return undefined
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
      }
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [open, onClose])
}

function formatRevisionTimestamp(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return 'unknown time'
  }
  try {
    return new Date(value).toLocaleString()
  } catch {
    return String(value)
  }
}

function revisionLabel(revision: DesktopSessionPlanRevisionRecord): string {
  if (revision.version > 0) {
    return `Revision ${revision.version}`
  }
  return 'Revision'
}

function diffLineClassName(line: string): string {
  if (line.startsWith('+')) {
    return 'bg-[var(--app-success-bg)] text-[var(--app-success)]'
  }
  if (line.startsWith('-')) {
    return 'bg-[var(--app-danger-bg)] text-[var(--app-danger)]'
  }
  if (line.startsWith('@@')) {
    return 'bg-[var(--app-primary-soft)] text-[var(--app-primary)]'
  }
  return 'text-[var(--app-text-muted)]'
}

export function DesktopPlanModal({
  open,
  plan,
  revisions,
  historyLoading,
  saving,
  error,
  onOpenChange,
  onCopy,
  onSave,
}: DesktopPlanModalProps) {
  const [draft, setDraft] = useState('')
  const [documentDraft, setDocumentDraft] = useState('')
  const [documentDraftError, setDocumentDraftError] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'error'>('idle')
  const [selectedRevisionKey, setSelectedRevisionKey] = useState('current')

  useEffect(() => {
    if (!open) {
      return
    }
    setDraft(plan?.plan ?? '')
    setDocumentDraft(plan?.document ? JSON.stringify(structuredPlanDocumentToWire(plan.document), null, 2) : '')
    setDocumentDraftError(null)
    setEditing(false)
    setCopyState('idle')
    setSelectedRevisionKey('current')
  }, [open, plan?.id, plan?.updatedAt, plan?.plan, plan?.document])

  useEscapeToClose(open, () => onOpenChange(false))

  const title = useMemo(() => {
    const value = plan?.title?.trim() ?? ''
    return value || 'Current Plan'
  }, [plan?.title])

  const subtitle = useMemo(() => {
    const parts: string[] = []
    const planId = plan?.id?.trim() ?? ''
    const status = plan?.status?.trim() ?? ''
    const approvalState = plan?.approvalState?.trim() ?? ''
    if (planId) {
      parts.push(`Canonical plan ${planId}`)
    } else {
      parts.push('No active plan yet')
    }
    if (status) {
      parts.push(`status ${status}`)
    }
    if (approvalState) {
      parts.push(`approval ${approvalState}`)
    }
    return parts.join(' • ')
  }, [plan?.approvalState, plan?.id, plan?.status])

  const selectedRevision = useMemo(() => {
    if (selectedRevisionKey === 'current') {
      return null
    }
    return revisions.find((revision) => revision.key === selectedRevisionKey) ?? null
  }, [revisions, selectedRevisionKey])

  if (!open) {
    return null
  }

  const viewingRevision = selectedRevision !== null
  const selectedDocument = viewingRevision ? selectedRevision.document : (plan?.document ?? null)
  const preview = viewingRevision ? selectedRevision.plan : (draft.trim() !== '' ? draft : (plan?.plan ?? ''))
  const currentDocumentWire = plan?.document ? JSON.stringify(structuredPlanDocumentToWire(plan.document), null, 2) : ''
  const dirty = draft !== (plan?.plan ?? '') || documentDraft !== currentDocumentWire

  const handleCopy = async () => {
    const ok = await onCopy(preview)
    setCopyState(ok ? 'copied' : 'error')
  }

  const handleSave = async () => {
    setDocumentDraftError(null)
    let parsedDocument: Record<string, unknown> | undefined
    if (documentDraft.trim()) {
      try {
        const parsed = JSON.parse(documentDraft) as unknown
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          throw new Error('Structured document must be a JSON object.')
        }
        parsedDocument = parsed as Record<string, unknown>
      } catch (error) {
        setDocumentDraftError(error instanceof Error ? error.message : 'Structured document JSON is invalid.')
        return
      }
    }
    await onSave(draft, parsedDocument)
    setEditing(false)
    setSelectedRevisionKey('current')
  }

  const handleRestoreRevision = async () => {
    if (!selectedRevision) {
      return
    }
    setDraft(selectedRevision.plan)
    const revisionDocument = selectedRevision.document ? structuredPlanDocumentToWire(selectedRevision.document) : undefined
    await onSave(selectedRevision.plan, revisionDocument)
    setEditing(false)
    setSelectedRevisionKey('current')
  }

  const handleCancelEdit = () => {
    setDraft(plan?.plan ?? '')
    setDocumentDraft(currentDocumentWire)
    setDocumentDraftError(null)
    setEditing(false)
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label={title} className="z-[80] p-4 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="w-[min(1180px,calc(100vw-24px))] overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1280px,calc(100vw-48px))]">
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-6 py-5">
          <div className="min-w-0 flex-1">
            <h2 className="text-xl font-semibold tracking-tight text-[var(--app-text)]">{title}</h2>
            <p className="mt-1 text-sm text-[var(--app-text-muted)]">{subtitle}</p>
          </div>
          <ModalCloseButton onClick={() => onOpenChange(false)} aria-label="Close current plan dialog" />
        </div>

        <div className="grid max-h-[min(74vh,900px)] min-h-[520px] grid-cols-1 overflow-hidden lg:grid-cols-[300px_minmax(0,1fr)]">
          <aside className="min-h-0 overflow-y-auto border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-4 lg:border-b-0 lg:border-r">
            <div className="mb-3 flex items-center justify-between gap-2">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Plan revisions</span>
              {historyLoading ? <span className="text-xs text-[var(--app-text-muted)]">Loading…</span> : null}
            </div>
            <div className="grid gap-2">
              <button
                type="button"
                onClick={() => {
                  setEditing(false)
                  setSelectedRevisionKey('current')
                }}
                className={cn(
                  'rounded-2xl border px-3 py-3 text-left transition',
                  !viewingRevision
                    ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-text)]'
                    : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:text-[var(--app-text)]',
                )}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-semibold text-[var(--app-text)]">Current active plan</span>
                  <span className="rounded-full border border-[var(--app-border)] px-2 py-0.5 text-[10px] uppercase tracking-[0.08em] text-[var(--app-text-muted)]">active</span>
                </div>
                <div className="mt-1 text-xs text-[var(--app-text-muted)]">{formatRevisionTimestamp(plan?.updatedAt ?? 0)}</div>
              </button>

              {revisions.length > 0 ? revisions.map((revision) => (
                <button
                  key={revision.key}
                  type="button"
                  onClick={() => {
                    setEditing(false)
                    setSelectedRevisionKey(revision.key)
                  }}
                  className={cn(
                    'rounded-2xl border px-3 py-3 text-left transition',
                    selectedRevisionKey === revision.key
                      ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-text)]'
                      : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:text-[var(--app-text)]',
                  )}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm font-semibold text-[var(--app-text)]">{revisionLabel(revision)}</span>
                    {revision.parentRevision > 0 ? <span className="text-[10px] text-[var(--app-text-muted)]">from {revision.parentRevision}</span> : null}
                  </div>
                  <div className="mt-1 text-xs text-[var(--app-text-muted)]">{revision.updateSummary || revision.updateKind || 'Plan snapshot'}</div>
                  <div className="mt-1 text-[11px] text-[var(--app-text-subtle)]">{formatRevisionTimestamp(revision.updatedAt)}</div>
                </button>
              )) : (
                <div className="rounded-2xl border border-dashed border-[var(--app-border)] px-3 py-4 text-sm text-[var(--app-text-muted)]">
                  {plan?.id ? 'No stored revisions yet.' : 'Create a plan to start revision history.'}
                </div>
              )}
            </div>
          </aside>

          <div className="flex min-h-0 flex-col overflow-hidden">
            <div className="flex flex-wrap items-center justify-between gap-2 border-b border-[var(--app-border)] px-6 py-4">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                {editing ? 'Editing plan' : viewingRevision ? revisionLabel(selectedRevision) : 'Current plan'}
              </span>
              <div className="flex flex-wrap items-center gap-2">
                <Button type="button" variant="outline" size="sm" onClick={() => void handleCopy()}>
                  {copyState === 'copied' ? (
                    <Check className="size-4" />
                  ) : copyState === 'error' ? (
                    <AlertCircle className="size-4" />
                  ) : (
                    <Copy className="size-4" />
                  )}
                  {copyState === 'copied' ? 'Copied' : copyState === 'error' ? 'Copy failed' : 'Copy'}
                </Button>
                {viewingRevision ? (
                  <Button type="button" variant="primary" size="sm" onClick={() => void handleRestoreRevision()} disabled={saving}>
                    <RotateCcw className={cn('size-4', saving ? 'animate-pulse' : '')} />
                    {saving ? 'Restoring…' : 'Restore as latest'}
                  </Button>
                ) : editing ? (
                  <Button type="button" variant="secondary" size="sm" onClick={handleCancelEdit} disabled={saving}>
                    Cancel edit
                  </Button>
                ) : (
                  <Button type="button" variant="primary" size="sm" onClick={() => setEditing(true)}>
                    <Pencil className="size-4" />
                    Edit plan
                  </Button>
                )}
              </div>
            </div>

            <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
              {editing ? (
                <section className="grid gap-3">
                  {plan?.document ? (
                    <>
                      <label className="grid gap-2">
                        <span className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Structured plan document</span>
                        <Textarea
                          value={documentDraft}
                          onChange={(event) => {
                            setDocumentDraft(event.target.value)
                            setDocumentDraftError(null)
                          }}
                          placeholder="Edit structured plan info and checkpoints as JSON…"
                          className="min-h-[420px] w-full resize-y bg-[var(--app-bg-alt)] font-mono text-sm leading-6"
                        />
                      </label>
                      {documentDraftError ? (
                        <p className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{documentDraftError}</p>
                      ) : null}
                      <p className="text-xs text-[var(--app-text-muted)]">
                        This edits the canonical structured plan document: base info plus checkpoint objects. Saving records one revision.
                      </p>
                    </>
                  ) : (
                    <>
                      <Textarea
                        value={draft}
                        onChange={(event) => setDraft(event.target.value)}
                        placeholder="Write or paste the current session plan…"
                        className="min-h-[420px] w-full resize-y bg-[var(--app-bg-alt)] font-mono text-sm leading-6"
                      />
                      <p className="text-xs text-[var(--app-text-muted)]">
                        No structured document exists yet; saving this display text records a new revision.
                      </p>
                    </>
                  )}
                </section>
              ) : (
                <div className="grid gap-4">
                  {selectedDocument ? (
                    <StructuredPlanDocumentView document={selectedDocument} emptyText="No structured plan data is available for this plan." />
                  ) : (
                    <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-5">
                      {preview.trim() ? (
                        <ChatMarkdown content={preview} className="text-base leading-7" />
                      ) : (
                        <p className="text-sm text-[var(--app-text-muted)]">
                          No active plan yet. Use Edit plan to create one for this session.
                        </p>
                      )}
                    </section>
                  )}

                  {viewingRevision ? (
                    <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
                      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
                        <div>
                          <h3 className="text-sm font-semibold text-[var(--app-text)]">Stored diff</h3>
                          <p className="text-xs text-[var(--app-text-muted)]">
                            {selectedRevision.updateScope || selectedRevision.updateSummary || 'Changes from the parent revision'}
                          </p>
                        </div>
                        <span className="text-xs text-[var(--app-text-muted)]">
                          {selectedRevision.diffLines.length} lines
                        </span>
                      </div>
                      {selectedRevision.diffLines.length > 0 ? (
                        <pre className="max-h-[320px] overflow-auto rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] py-2 font-mono text-[12px] leading-5">
                          {selectedRevision.diffLines.map((line, index) => (
                            <div key={`${index}:${line}`} className={cn('whitespace-pre-wrap break-words px-3', diffLineClassName(line))}>{line || ' '}</div>
                          ))}
                        </pre>
                      ) : (
                        <p className="rounded-xl border border-dashed border-[var(--app-border)] px-3 py-3 text-sm text-[var(--app-text-muted)]">
                          This base revision has no parent diff.
                        </p>
                      )}
                    </section>
                  ) : null}
                </div>
              )}

              {error ? (
                <div className="mt-4 rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">
                  {error}
                </div>
              ) : null}
            </div>
          </div>
        </div>

        <div className="flex flex-wrap justify-end gap-2 border-t border-[var(--app-border)] px-6 py-4">
          <Button type="button" variant="ghost" onClick={() => onOpenChange(false)} disabled={saving}>
            Close
          </Button>
          {editing ? (
            <Button type="button" variant="primary" onClick={() => void handleSave()} disabled={saving || !dirty}>
              <Save className={cn('size-4', saving ? 'animate-pulse' : '')} />
              {saving ? 'Saving…' : 'Save plan'}
            </Button>
          ) : null}
        </div>
      </DialogPanel>
    </Dialog>
  )
}
