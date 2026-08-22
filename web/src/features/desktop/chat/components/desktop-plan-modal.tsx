import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertCircle,
  Check,
  CheckCircle2,
  Circle,
  Copy,
  PlayCircle,
} from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { ChatMarkdown } from './chat-markdown'
import { StructuredPlanDocumentView, normalizeStructuredPlanDocument } from './structured-plan-document'
import type { DesktopSessionPlanCheckpoint, DesktopSessionPlanDocument, DesktopSessionPlanRecord } from '../types/chat'

interface DesktopPlanModalProps {
  open: boolean
  plan: DesktopSessionPlanRecord | null
  error: string | null
  onOpenChange: (open: boolean) => void
  onCopy: (text: string) => Promise<boolean>
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

function displayCheckpointNumber(value: string, fallback = ''): string {
  const match = value.trim().match(/^cp[-_ ]?(\d+)$/i)
  return match ? match[1] : fallback
}

function firstNonBlankText(...values: Array<string | null | undefined>): string {
  for (const value of values) {
    const normalized = (value ?? '').trim()
    if (normalized) return normalized
  }
  return ''
}

function appendTextSection(lines: string[], title: string, value: string) {
  const normalized = value.trim()
  if (!normalized) return
  lines.push('', `## ${title}`, '', normalized)
}

function appendBulletSection(lines: string[], title: string, values: string[]) {
  const normalized = values.map((value) => value.trim()).filter(Boolean)
  if (normalized.length === 0) return
  lines.push('', `## ${title}`, '')
  normalized.forEach((value) => lines.push(`- ${value}`))
}

function appendFieldList(lines: string[], title: string, fields: Array<[string, string | number | undefined]>) {
  const normalized = fields
    .map(([label, value]): [string, string] => [label, typeof value === 'number' ? (value > 0 ? String(value) : '') : (value ?? '').trim()])
    .filter(([, value]) => value !== '')
  if (normalized.length === 0) return
  lines.push('', `## ${title}`, '')
  normalized.forEach(([label, value]) => lines.push(`- ${label}: ${value}`))
}

export function structuredPlanCopyText(document: DesktopSessionPlanDocument): string {
  const lines: string[] = [`# ${firstNonBlankText(document.title, document.id, 'Plan')}`]
  const identityFields: Array<[string, string]> = [
    ['Plan ID', document.id],
    ['Status', document.status],
    ['Schema version', document.schemaVersion],
    ['Revision', document.revisionId],
    ['Active checkpoint', displayCheckpointNumber(document.activeCheckpointId)],
  ]
  const presentIdentityFields = identityFields.filter(([, value]) => value.trim() !== '')
  if (presentIdentityFields.length > 0) {
    lines.push('')
    presentIdentityFields.forEach(([label, value]) => lines.push(`- ${label}: ${value}`))
  }

  appendTextSection(lines, 'Goal', document.info.goal)
  appendTextSection(lines, 'Scope', document.info.scope)
  appendTextSection(lines, 'Context', document.info.context)
  appendBulletSection(lines, 'Decisions', document.info.decisions)
  appendBulletSection(lines, 'Success criteria', document.info.successCriteria)
  appendBulletSection(lines, 'Constraints', document.info.constraints)
  appendBulletSection(lines, 'Assumptions', document.info.assumptions)
  appendBulletSection(lines, 'Open questions', document.info.openQuestions)
  appendBulletSection(lines, 'Relevant files', document.info.relevantFiles)
  appendTextSection(lines, 'Validation strategy', document.info.validationStrategy)
  appendFieldList(lines, 'Execution policy', [
    ['Mode', document.executionPolicy?.mode],
    ['Shape', document.executionPolicy?.shape],
    ['Session checkpoint policy', document.executionPolicy?.followupCheckpointPolicy],
  ])
  appendFieldList(lines, 'Execution state', [
    ['Status', document.executionState?.status],
    ['Active attempt', document.executionState?.activeAttemptId],
    ['Current run', document.executionState?.currentRunId],
    ['Current session', document.executionState?.currentSessionId],
    ['Parent session', document.executionState?.parentSessionId],
    ['Last checkpoint', document.executionState?.lastCheckpointId],
    ['Last attempt', document.executionState?.lastAttemptId],
    ['Last outcome', document.executionState?.lastOutcome],
    ['Started at', document.executionState?.startedAt],
    ['Updated at', document.executionState?.updatedAt],
    ['Completed at', document.executionState?.completedAt],
  ])

  if (document.originalCheckpoints.length > 0) {
    lines.push('', '## Original checkpoint plan')
    document.originalCheckpoints.forEach((checkpoint, index) => {
      lines.push('', `### ${index + 1}. ${firstNonBlankText(checkpoint.title, `Checkpoint ${index + 1}`)}`)
      const fields: Array<[string, string | number]> = [
        ['Checkpoint', String(index + 1)],
        ['Status', checkpoint.status],
        ['Objective', checkpoint.objective],
        ['Notes', checkpoint.notes],
        ['Report', checkpoint.report],
        ['Result', checkpoint.result],
      ]
      fields
        .map(([label, value]): [string, string] => [label, typeof value === 'number' ? (value > 0 ? String(value) : '') : value.trim()])
        .filter(([, value]) => value !== '')
        .forEach(([label, value]) => lines.push(`- ${label}: ${value}`))
      if (checkpoint.tasks.length > 0) {
        lines.push('- Tasks:')
        checkpoint.tasks.forEach((task) => lines.push(`  - ${task}`))
      }
      if (checkpoint.acceptanceCriteria.length > 0) {
        lines.push('- Acceptance criteria:')
        checkpoint.acceptanceCriteria.forEach((criterion) => lines.push(`  - ${criterion}`))
      }
    })
  }

  if (document.checkpoints.length > 0) {
    lines.push('', document.originalCheckpoints.length > 0 ? '## Execution checkpoints' : '## Checkpoints')
    document.checkpoints.forEach((checkpoint, index) => {
      lines.push('', `### ${index + 1}. ${firstNonBlankText(checkpoint.title, `Checkpoint ${index + 1}`)}`)
      const fields: Array<[string, string | number]> = [
        ['Checkpoint', String(index + 1)],
        ['Status', checkpoint.status],
        ['Objective', checkpoint.objective],
        ['Attempt', checkpoint.attemptId],
        ['Run', checkpoint.runId],
        ['Session', checkpoint.sessionId],
        ['Started at', checkpoint.startedAt],
        ['Completed at', checkpoint.completedAt],
        ['Notes', checkpoint.notes],
        ['Report', checkpoint.report],
        ['Result', checkpoint.result],
      ]
      fields
        .map(([label, value]): [string, string] => [label, typeof value === 'number' ? (value > 0 ? String(value) : '') : value.trim()])
        .filter(([, value]) => value !== '')
        .forEach(([label, value]) => lines.push(`- ${label}: ${value}`))
      if (checkpoint.tasks.length > 0) {
        lines.push('- Tasks:')
        checkpoint.tasks.forEach((task) => lines.push(`  - ${task}`))
      }
      if (checkpoint.acceptanceCriteria.length > 0) {
        lines.push('- Acceptance criteria:')
        checkpoint.acceptanceCriteria.forEach((criterion) => lines.push(`  - ${criterion}`))
      }
      if (checkpoint.changedFiles.length > 0) {
        lines.push('- Changed files:')
        checkpoint.changedFiles.forEach((file) => lines.push(`  - ${file}`))
      }
      if (checkpoint.validation.length > 0) {
        lines.push('- Validation:')
        checkpoint.validation.forEach((entry) => lines.push(`  - ${entry}`))
      }
      if (checkpoint.review) {
        appendFieldList(lines, 'Review', [
          ['Status', checkpoint.review.status],
          ['Reviewer', checkpoint.review.reviewerId],
          ['Reviewer type', checkpoint.review.reviewerType],
          ['Result', checkpoint.review.result],
          ['Notes', checkpoint.review.notes],
          ['Reviewed at', checkpoint.review.reviewedAt],
        ])
      }
    })
  }

  return lines.join('\n').replace(/\n{3,}/g, '\n\n').trim()
}

export function selectedPlanCopyText(document: DesktopSessionPlanDocument | null, markdownFallback: string): string {
  if (document) {
    const structuredText = structuredPlanCopyText(document)
    if (structuredText) return structuredText
  }
  return markdownFallback
}

function CheckpointStatusIcon({ status, active }: { status: string; active: boolean }) {
  const normStatus = status.toLowerCase()
  if (normStatus === 'completed' || normStatus === 'done' || normStatus === 'success') {
    return <CheckCircle2 className="size-4 text-[var(--app-success)]" />
  }
  if (active || normStatus === 'in_progress' || normStatus === 'in-progress' || normStatus === 'active') {
    return <PlayCircle className="size-4 text-[var(--app-primary)]" />
  }
  return <Circle className="size-4 text-[var(--app-text-muted)]" />
}

function PlanModalDocumentView({ document, emptyText }: { document: DesktopSessionPlanDocument | null; emptyText: string }) {
  const structuredDocument = normalizeStructuredPlanDocument(document)
  if (!structuredDocument) {
    return <section className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-5 text-sm text-[var(--app-text-muted)]">{emptyText}</section>
  }
  return (
    <div className="grid min-h-0 gap-4">
      <StructuredPlanDocumentView document={structuredDocument} review />
    </div>
  )
}

function checkpointTitle(checkpoint: DesktopSessionPlanCheckpoint | null | undefined, fallbackNumber = 0): string {
  return firstNonBlankText(checkpoint?.title, fallbackNumber > 0 ? `Checkpoint ${fallbackNumber}` : '', 'No checkpoint selected')
}

function ActiveCheckpointHeader({ document }: { document: DesktopSessionPlanDocument | null }) {
  if (!document) return null
  const checkpoints = document.checkpoints
  const activeCheckpoint = checkpoints.find((checkpoint) => checkpoint.id === document.activeCheckpointId) ?? null
  if (!activeCheckpoint) return null
  const activeIndex = checkpoints.findIndex((checkpoint) => checkpoint.id === activeCheckpoint.id)
  const totalCount = checkpoints.length
  const checkpointPosition = activeIndex >= 0 && totalCount > 0 ? `Checkpoint ${activeIndex + 1} of ${totalCount}` : 'Active checkpoint'
  const title = checkpointTitle(activeCheckpoint, activeIndex + 1)
  return (
    <div className="mt-1.5 flex min-w-0 flex-wrap items-start gap-2 text-sm leading-5" title={`${checkpointPosition}: ${title}`}>
      <span className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2 py-0.5 text-xs font-semibold text-[var(--app-primary)]">
        <CheckpointStatusIcon status={activeCheckpoint.status} active />
        {checkpointPosition}
      </span>
      <span className="min-w-0 flex-1 whitespace-normal break-words text-[var(--app-text-muted)]">
        {title}
      </span>
    </div>
  )
}

export function DesktopPlanModal({
  open,
  plan,
  error,
  onOpenChange,
  onCopy,
}: DesktopPlanModalProps) {
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'error'>('idle')
  const modalWasOpenRef = useRef(false)
  const syncedPlanIdRef = useRef<string | null>(null)

  useEffect(() => {
    if (!open) {
      modalWasOpenRef.current = false
      syncedPlanIdRef.current = null
      return
    }

    const planId = plan?.id ?? ''
    const opening = !modalWasOpenRef.current
    const planChanged = syncedPlanIdRef.current !== null && syncedPlanIdRef.current !== planId
    modalWasOpenRef.current = true
    syncedPlanIdRef.current = planId

    if (opening || planChanged) {
      setCopyState('idle')
    }
  }, [open, plan?.id, plan?.document])

  useEscapeToClose(open, () => onOpenChange(false))

  const title = useMemo(() => {
    const value = plan?.title?.trim() ?? ''
    return value || 'Current Plan'
  }, [plan?.title])

  if (!open) {
    return null
  }

  const selectedDocument = plan?.document ?? null
  const preview = plan?.plan ?? ''

  const handleCopy = async () => {
    const ok = await onCopy(selectedPlanCopyText(selectedDocument, preview))
    setCopyState(ok ? 'copied' : 'error')
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label={title} className="z-[80] p-3 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="grid max-h-[min(900px,calc(100vh-48px))] w-[min(1180px,calc(100vw-24px))] grid-rows-[auto_minmax(0,1fr)] overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1280px,calc(100vw-48px))]">
        <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-4 border-b border-[var(--app-border)] px-6 py-4">
          <div className="min-w-0">
            <h2 className="truncate text-xl font-semibold tracking-tight text-[var(--app-text)]">{title}</h2>
            <ActiveCheckpointHeader document={selectedDocument} />
          </div>
          <div className="flex min-w-0 shrink-0 flex-nowrap items-center justify-end gap-2 overflow-x-auto whitespace-nowrap">
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
            <ModalCloseButton onClick={() => onOpenChange(false)} aria-label="Close current plan dialog" />
          </div>
        </div>

        <div className="min-h-0 overflow-y-auto px-6 py-5">
          <div className="grid gap-4">
              {selectedDocument ? (
                <PlanModalDocumentView
                  document={selectedDocument}
                  emptyText="No structured plan data is available for this plan."
                />
              ) : (
                <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-5">
                  {preview.trim() ? (
                    <ChatMarkdown content={preview} className="text-base leading-7" />
                  ) : (
                    <p className="text-sm text-[var(--app-text-muted)]">
                      No active plan is available for this session.
                    </p>
                  )}
                </section>
              )}
            </div>

          {error ? (
            <div className="mt-4 rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">
              {error}
            </div>
          ) : null}
        </div>

      </DialogPanel>
    </Dialog>
  )
}
