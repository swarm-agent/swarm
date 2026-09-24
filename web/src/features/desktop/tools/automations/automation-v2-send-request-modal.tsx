import { useEffect, useState } from 'react'
import {
  SendHorizontal,
  Bot,
  ExternalLink,
  CheckCircle2,
  LoaderCircle,
  AlertCircle,
  Sparkles,
  Clock,
  Briefcase,
} from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import type { AutomationV2Occurrence, AutomationV2Record } from '../../state/desktop-automation-v2-api'
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2'
import { scheduleLabel, type AutomationSchedule } from './automation-v2-schedule'

export interface AutomationV2SendRequestModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  record: AutomationV2Record | null
  workspaceId?: string
  workspaceSlug?: string
  onOpenSession?: (sessionId: string) => void
}

export function AutomationV2SendRequestModal({
  open,
  onOpenChange,
  record,
  workspaceId,
  workspaceSlug,
  onOpenSession,
}: AutomationV2SendRequestModalProps) {
  const [prompt, setPrompt] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [startedOccurrence, setStartedOccurrence] = useState<AutomationV2Occurrence | null>(null)

  useEffect(() => {
    if (!open) return
    setPrompt('')
    setSubmitting(false)
    setError(null)
    setStartedOccurrence(null)
  }, [open, record?.automation_id, record?.session_id])

  if (!open || !record) return null

  const workerTitle = record.document?.title || record.automation_id || 'Worker'
  const hasPreconfiguredJobs = Array.isArray(record.document?.checkpoints) && record.document.checkpoints.length > 0
  const isSpecialist = !hasPreconfiguredJobs
  const docAny = record.document as Record<string, any> | undefined
  const rawSchedule = (docAny?.automation_v2?.schedule ||
    docAny?.worker_v2?.schedule) as AutomationSchedule | undefined
  const scheduleKind = rawSchedule?.kind || 'trigger'
  const scheduleText = rawSchedule ? scheduleLabel(rawSchedule) : 'On demand'

  const canSubmit = !submitting && (!isSpecialist || prompt.trim().length > 0)

  const handleSend = async () => {
    if (!canSubmit) return
    setSubmitting(true)
    setError(null)

    try {
      const targetWorkerId = record.automation_id || record.session_id
      const trimmedPrompt = prompt.trim()

      const res = await desktopAutomationV2.trigger({
        workspace_id: record.workspace_id || workspaceId || '',
        worker_id: targetWorkerId,
        session_id: record.session_id,
        prompt: trimmedPrompt || undefined,
      })

      if (res.ok && res.occurrence) {
        setStartedOccurrence(res.occurrence)
      } else {
        setError(res.error || 'Failed to start worker session. Please try again.')
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start worker session.')
    } finally {
      setSubmitting(false)
    }
  }

  const handleOpenStartedSession = () => {
    if (startedOccurrence && onOpenSession) {
      onOpenSession(startedOccurrence.session_id)
      onOpenChange(false)
    }
  }

  return (
    <Dialog
      role="dialog"
      aria-modal="true"
      aria-label={`Send request to ${workerTitle}`}
      className="z-[90] p-4 sm:p-6"
    >
      <DialogBackdrop onClick={() => !submitting && onOpenChange(false)} />
      <DialogPanel className="w-[min(620px,calc(100vw-24px))] gap-0 rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
        {/* Header */}
        <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-6 py-5">
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <div className="flex h-8 w-8 items-center justify-center rounded-xl bg-[var(--app-primary)]/10 text-[var(--app-primary)]">
                <SendHorizontal size={16} />
              </div>
              <div>
                <h2 className="text-base font-semibold text-[var(--app-text)]">Send request to worker</h2>
                <p className="text-xs text-[var(--app-text-muted)] truncate max-w-[420px]">
                  {workerTitle}
                </p>
              </div>
            </div>
          </div>
          <Button
            variant="ghost"
            size="sm"
            className="rounded-xl text-xs"
            disabled={submitting}
            onClick={() => onOpenChange(false)}
          >
            Close
          </Button>
        </div>

        {/* Modal Body */}
        <div className="p-6 space-y-5">
          {/* Worker Info Card */}
          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-hover)]/40 p-3.5 space-y-2 text-xs">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2 font-medium text-[var(--app-text)]">
                <Bot size={14} className="text-[var(--app-primary)]" />
                <span className="truncate max-w-[300px]">{workerTitle}</span>
              </div>
              <span className="inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-[10px] font-medium bg-[var(--app-surface)] border border-[var(--app-border)] text-[var(--app-text-muted)]">
                <Clock size={10} />
                {scheduleText || (scheduleKind === 'trigger' ? 'On-Demand Trigger' : scheduleKind)}
              </span>
            </div>

            {record.document?.info?.goal ? (
              <p className="text-[11px] text-[var(--app-text-muted)] line-clamp-2 leading-relaxed">
                {String(record.document.info.goal)}
              </p>
            ) : null}

            <div className="flex items-center gap-2 pt-1 border-t border-[var(--app-border)]/50 text-[10px] text-[var(--app-text-muted)]">
              <span className="flex items-center gap-1 font-mono">
                <Briefcase size={10} />
                ID: {record.automation_id || record.session_id}
              </span>
              {isSpecialist && (
                <span className="ml-auto inline-flex items-center gap-1 font-medium text-[var(--app-primary)]">
                  <Sparkles size={10} />
                  Specialist Worker
                </span>
              )}
            </div>
          </div>

          {startedOccurrence ? (
            /* Success & Immediate Session Feedback State */
            <div
              className="space-y-4 rounded-2xl border border-[var(--app-success-border,rgba(34,197,94,0.3))] bg-[var(--app-success-soft,rgba(34,197,94,0.06))] p-5 text-sm"
              data-testid="send-request-session-feedback"
            >
              <div className="flex items-start gap-3">
                <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-emerald-500/15 text-emerald-600 dark:text-emerald-400">
                  <CheckCircle2 size={20} />
                </div>
                <div className="space-y-1 min-w-0">
                  <h3 className="font-semibold text-[var(--app-text)]">Session started!</h3>
                  <p className="text-xs text-[var(--app-text-muted)] leading-relaxed">
                    The worker session has been created and is executing in an isolated worktree. You can open the session now to inspect progress or converse with the worker.
                  </p>
                </div>
              </div>

              <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-xs space-y-1.5 font-mono">
                <div className="flex items-center justify-between text-[11px]">
                  <span className="text-[var(--app-text-muted)]">Session ID:</span>
                  <span className="font-semibold text-[var(--app-text)] select-all">{startedOccurrence.session_id}</span>
                </div>
                {startedOccurrence.run_id && (
                  <div className="flex items-center justify-between text-[11px]">
                    <span className="text-[var(--app-text-muted)]">Run ID:</span>
                    <span className="text-[var(--app-text-muted)] select-all">{startedOccurrence.run_id}</span>
                  </div>
                )}
                <div className="flex items-center justify-between text-[11px]">
                  <span className="text-[var(--app-text-muted)]">Status:</span>
                  <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400 font-medium capitalize">
                    <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
                    {startedOccurrence.state || 'Admitted & Executing'}
                  </span>
                </div>
              </div>

              <div className="flex items-center justify-end gap-2 pt-2">
                <Button
                  size="sm"
                  variant="outline"
                  className="rounded-xl text-xs"
                  onClick={() => onOpenChange(false)}
                >
                  Done
                </Button>
                {onOpenSession ? (
                  <Button
                    size="sm"
                    className="gap-1.5 rounded-xl text-xs font-semibold"
                    onClick={handleOpenStartedSession}
                    data-testid="send-request-open-session-btn"
                  >
                    <span>Open session</span>
                    <ExternalLink size={12} />
                  </Button>
                ) : workspaceSlug ? (
                  <a
                    href={`/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(startedOccurrence.session_id)}`}
                    className="inline-flex items-center gap-1.5 rounded-xl bg-[var(--app-primary)] px-3 py-1.5 text-xs font-semibold text-white shadow-2xs hover:opacity-90 transition-opacity"
                    data-testid="send-request-open-session-link"
                  >
                    <span>Open session</span>
                    <ExternalLink size={12} />
                  </a>
                ) : null}
              </div>
            </div>
          ) : (
            /* Request Entry State */
            <div className="space-y-4">
              <div className="space-y-1.5">
                <div className="flex items-center justify-between">
                  <label htmlFor="worker-request-prompt" className="text-xs font-medium text-[var(--app-text)]">
                    {isSpecialist ? 'Request prompt (required)' : 'Request prompt / additional context (optional)'}
                  </label>
                  <span className="text-[10px] text-[var(--app-text-muted)]">
                    {prompt.length} / 4000
                  </span>
                </div>
                <textarea
                  id="worker-request-prompt"
                  data-testid="send-request-prompt-input"
                  rows={4}
                  maxLength={4000}
                  value={prompt}
                  onChange={(e) => setPrompt(e.target.value)}
                  placeholder={
                    isSpecialist
                      ? "Enter your instructions for this specialist worker (e.g., 'Draft three LinkedIn posts summarizing our release' or 'Analyze recent pull requests for security regressions')..."
                      : "Add optional task instructions or context for this execution run..."
                  }
                  className="w-full resize-none rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-xs text-[var(--app-text)] placeholder:text-[var(--app-text-muted)]/60 focus:border-[var(--app-primary)] focus:outline-none transition-colors"
                  disabled={submitting}
                />
                <p className="text-[11px] text-[var(--app-text-muted)]">
                  {isSpecialist
                    ? 'This specialist worker has no pre-baked checklist; it executes dynamic prompts on-demand in its target workspace.'
                    : 'A new execution session will be admitted and run in an isolated worktree.'}
                </p>
              </div>

              {error && (
                <div
                  className="flex items-start gap-2 rounded-xl border border-[var(--app-danger-border,rgba(239,68,68,0.3))] bg-[var(--app-danger-soft,rgba(239,68,68,0.06))] p-3 text-xs text-[var(--app-danger)]"
                  role="alert"
                >
                  <AlertCircle size={14} className="shrink-0 mt-0.5" />
                  <p>{error}</p>
                </div>
              )}

              {/* Action Buttons */}
              <div className="flex items-center justify-end gap-2 pt-2 border-t border-[var(--app-border)]/50">
                <Button
                  variant="ghost"
                  size="sm"
                  className="rounded-xl text-xs"
                  disabled={submitting}
                  onClick={() => onOpenChange(false)}
                >
                  Cancel
                </Button>
                <Button
                  size="sm"
                  className="gap-1.5 rounded-xl text-xs font-semibold"
                  disabled={!canSubmit}
                  onClick={handleSend}
                  data-testid="send-request-submit-button"
                >
                  {submitting ? (
                    <>
                      <LoaderCircle size={13} className="animate-spin" />
                      <span>Starting session...</span>
                    </>
                  ) : (
                    <>
                      <SendHorizontal size={13} />
                      <span>Send request</span>
                    </>
                  )}
                </Button>
              </div>
            </div>
          )}
        </div>
      </DialogPanel>
    </Dialog>
  )
}
