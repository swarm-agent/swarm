import { useEffect, useState } from 'react'
import { MessageSquarePlus } from 'lucide-react'
import { Button } from '../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { cn } from '../../../lib/cn'
import { submitDesktopFeedback, type DesktopFeedbackCategory } from './feedback-api'

const CATEGORY_OPTIONS: Array<{ id: DesktopFeedbackCategory; label: string; description: string }> = [
  { id: 'bug', label: 'Issue', description: 'Something is broken or did not work as expected.' },
  { id: 'general', label: 'Comment', description: 'Share an observation or general note about Swarm.' },
  { id: 'feature', label: 'Suggestion', description: 'Request an improvement or a new capability.' },
]

export function DesktopFeedbackModal({ open, onOpenChange }: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const [category, setCategory] = useState<DesktopFeedbackCategory>('bug')
  const [message, setMessage] = useState('')
  const [openedAt, setOpenedAt] = useState(Date.now())
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [sent, setSent] = useState(false)

  useEffect(() => {
    if (!open) return
    setCategory('bug')
    setMessage('')
    setOpenedAt(Date.now())
    setSubmitting(false)
    setError(null)
    setSent(false)
  }, [open])

  if (!open) return null

  const normalizedMessage = message.trim()
  const canSubmit = normalizedMessage.length >= 3 && normalizedMessage.length <= 4000 && !submitting

  const handleSubmit = async () => {
    if (!canSubmit) return
    setSubmitting(true)
    setError(null)
    try {
      await submitDesktopFeedback({ category, message: normalizedMessage, formTime: openedAt })
      setSent(true)
      setMessage('')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Feedback could not be sent. Please try again.')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Send feedback" className="z-[90] p-4 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="w-[min(620px,calc(100vw-24px))] gap-0 rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-6 py-5">
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <MessageSquarePlus size={19} className="text-[var(--app-primary)]" />
              <h2 className="text-lg font-semibold text-[var(--app-text)]">Send feedback</h2>
            </div>
            <p className="text-sm text-[var(--app-text-muted)]">Help improve Swarm. Do not include secrets or private customer data.</p>
          </div>
          <Button variant="ghost" size="sm" onClick={() => onOpenChange(false)}>Close</Button>
        </div>

        <div className="space-y-5 px-6 py-5">
          {sent ? (
            <div className="rounded-2xl border border-[var(--app-success)]/35 bg-[color-mix(in_oklab,var(--app-success)_10%,transparent)] p-4 text-sm text-[var(--app-text)]" role="status">
              Thanks — your feedback was sent.
            </div>
          ) : (
            <>
              <fieldset className="space-y-2">
                <legend className="mb-2 text-sm font-semibold text-[var(--app-text)]">What kind of feedback is this?</legend>
                <div className="grid gap-2 sm:grid-cols-3">
                  {CATEGORY_OPTIONS.map((option) => {
                    const selected = category === option.id
                    return (
                      <button
                        key={option.id}
                        type="button"
                        className={cn(
                          'rounded-2xl border p-3 text-left transition',
                          selected
                            ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-text)]'
                            : 'border-[var(--app-border)] bg-[var(--app-bg-alt)] text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)]',
                        )}
                        aria-pressed={selected}
                        onClick={() => setCategory(option.id)}
                      >
                        <span className="block text-sm font-semibold">{option.label}</span>
                        <span className="mt-1 block text-xs leading-5">{option.description}</span>
                      </button>
                    )
                  })}
                </div>
              </fieldset>

              <label className="block space-y-2">
                <span className="text-sm font-semibold text-[var(--app-text)]">Tell us more</span>
                <textarea
                  autoFocus
                  rows={7}
                  maxLength={4000}
                  value={message}
                  onChange={(event) => setMessage(event.target.value)}
                  placeholder="Describe the issue, comment, or suggestion…"
                  className="w-full resize-y rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-3 text-sm text-[var(--app-text)] outline-none transition placeholder:text-[var(--app-text-subtle)] focus:border-[var(--app-primary)] focus:ring-2 focus:ring-[var(--app-primary)]/20"
                />
                <span className="flex justify-between text-xs text-[var(--app-text-subtle)]">
                  <span>3–4,000 characters</span>
                  <span>{message.length}/4000</span>
                </span>
              </label>

              {error ? <div className="rounded-xl border border-[var(--app-error)]/35 bg-[color-mix(in_oklab,var(--app-error)_9%,transparent)] px-3 py-2 text-sm text-[var(--app-error)]" role="alert">{error}</div> : null}

              <div className="flex justify-end gap-2">
                <Button variant="secondary" onClick={() => onOpenChange(false)}>Cancel</Button>
                <Button onClick={() => void handleSubmit()} disabled={!canSubmit}>{submitting ? 'Sending…' : 'Send feedback'}</Button>
              </div>
            </>
          )}
        </div>
      </DialogPanel>
    </Dialog>
  )
}
