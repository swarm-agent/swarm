import { useEffect, useMemo, useState } from 'react'
import { Copy, Check, AlertCircle, Save, Pencil } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { Textarea } from '../../../../components/ui/textarea'
import { cn } from '../../../../lib/cn'
import { ChatMarkdown } from './chat-markdown'
import type { DesktopSessionPlanRecord } from '../types/chat'

interface DesktopPlanModalProps {
  open: boolean
  plan: DesktopSessionPlanRecord | null
  saving: boolean
  error: string | null
  onOpenChange: (open: boolean) => void
  onCopy: (text: string) => Promise<boolean>
  onSave: (planText: string) => Promise<void>
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

export function DesktopPlanModal({
  open,
  plan,
  saving,
  error,
  onOpenChange,
  onCopy,
  onSave,
}: DesktopPlanModalProps) {
  const [draft, setDraft] = useState('')
  const [editing, setEditing] = useState(false)
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'error'>('idle')

  useEffect(() => {
    if (!open) {
      return
    }
    setDraft(plan?.plan ?? '')
    setEditing(false)
    setCopyState('idle')
  }, [open, plan?.id, plan?.updatedAt, plan?.plan])

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
      parts.push(`Plan ${planId}`)
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

  if (!open) {
    return null
  }

  const preview = draft.trim() !== '' ? draft : (plan?.plan ?? '')
  const dirty = draft !== (plan?.plan ?? '')

  const handleCopy = async () => {
    const ok = await onCopy(preview)
    setCopyState(ok ? 'copied' : 'error')
  }

  const handleSave = async () => {
    await onSave(draft)
    setEditing(false)
  }

  const handleCancelEdit = () => {
    setDraft(plan?.plan ?? '')
    setEditing(false)
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label={title} className="z-[80] p-4 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="w-[min(960px,calc(100vw-24px))] overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1040px,calc(100vw-48px))]">
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-6 py-5">
          <div className="min-w-0 flex-1">
            <h2 className="text-xl font-semibold tracking-tight text-[var(--app-text)]">{title}</h2>
            <p className="mt-1 text-sm text-[var(--app-text-muted)]">{subtitle}</p>
          </div>
          <ModalCloseButton onClick={() => onOpenChange(false)} aria-label="Close current plan dialog" />
        </div>

        <div className="flex max-h-[min(72vh,860px)] flex-col gap-4 overflow-y-auto px-6 py-5">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
              {editing ? 'Editing plan' : 'Current plan'}
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
              {editing ? (
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

          {editing ? (
            <section className="grid gap-3">
              <Textarea
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                placeholder="Write or paste the current session plan…"
                className="min-h-[420px] w-full resize-y bg-[var(--app-bg-alt)] font-mono text-sm leading-6"
              />
              <p className="text-xs text-[var(--app-text-muted)]">
                Edit the plan in one full-width page, then save when ready.
              </p>
            </section>
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

          {error ? (
            <div className="rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">
              {error}
            </div>
          ) : null}
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
