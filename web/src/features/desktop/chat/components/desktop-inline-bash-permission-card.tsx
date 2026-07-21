import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { AlertTriangle, ChevronDown, ChevronUp, Terminal } from 'lucide-react'

import { parseBashIntentMetadata } from '../services/bash-intent-metadata'

import { requestJson } from '../../../../app/api'
import { Button } from '../../../../components/ui/button'
import { Textarea } from '../../../../components/ui/textarea'
import type { DesktopPermissionRecord } from '../../types/realtime'
import { safeString } from '../../permissions/services/desktop-permission-normalization'
import { permissionRequirementLabel } from '../../permissions/services/permission-payload'

const COLLAPSED_CONTENT_HEIGHT = 176
const COLLAPSED_VIEWPORT_RATIO = 0.4

export function bashPermissionCollapsedHeight(viewportHeight: number): number {
  if (!Number.isFinite(viewportHeight) || viewportHeight <= 0) return COLLAPSED_CONTENT_HEIGHT
  return Math.floor(viewportHeight * COLLAPSED_VIEWPORT_RATIO)
}

export function bashPermissionShouldStartExpanded(contentHeight: number, viewportHeight: number): boolean {
  return contentHeight > bashPermissionCollapsedHeight(viewportHeight)
}

type BashPermissionAction = 'approve' | 'deny' | 'approve_always' | 'always_deny'

interface DesktopInlineBashPermissionCardProps {
  permission: DesktopPermissionRecord
  pendingCount: number
  sessionMode: string
  onResolve: (
    permission: DesktopPermissionRecord,
    action: BashPermissionAction,
    reason: string,
  ) => Promise<void>
}

interface PermissionExplainResponse {
  explain?: {
    rule_preview?: string
  }
}

function savedBashPrefix(permission: DesktopPermissionRecord): string {
  if (permission.savedRule?.kind?.trim().toLowerCase() !== 'bash_prefix') return ''
  return permission.savedRule.pattern?.trim() || ''
}

function prefixFromRulePreview(preview: string): string {
  const trimmed = preview.trim()
  const match = /^(?:allow|deny)\s+bash(?:\s+command)?\s+prefix:\s*(.+)$/i.exec(trimmed)
  return match?.[1]?.trim() || trimmed
}

async function loadBashPrefix(permission: DesktopPermissionRecord, sessionMode: string): Promise<string> {
  const params = new URLSearchParams()
  params.set('mode', (permission.mode || sessionMode).trim())
  params.set('tool', safeString(permission.toolName))
  params.set('arguments', safeString(permission.toolArguments))
  const response = await requestJson<PermissionExplainResponse>(`/v1/permissions/explain?${params.toString()}`)
  return prefixFromRulePreview(response.explain?.rule_preview || '') || savedBashPrefix(permission)
}

export function DesktopInlineBashPermissionCard({
  permission,
  pendingCount,
  sessionMode,
  onResolve,
}: DesktopInlineBashPermissionCardProps) {
  const contentRef = useRef<HTMLDivElement | null>(null)
  const expansionWasChosenRef = useRef(false)
  const measuredPermissionIdRef = useRef(permission.id)
  const [expanded, setExpanded] = useState(false)
  const [canExpand, setCanExpand] = useState(false)
  const [collapsedContentHeight, setCollapsedContentHeight] = useState(COLLAPSED_CONTENT_HEIGHT)
  const [busyAction, setBusyAction] = useState<BashPermissionAction | null>(null)
  const [noteOpen, setNoteOpen] = useState(false)
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const [persistentPrefix, setPersistentPrefix] = useState(() => savedBashPrefix(permission))
  const intent = parseBashIntentMetadata(permission.toolArguments)
  const modeLabel = (permission.mode || sessionMode).trim() || 'auto'

  useEffect(() => {
    setBusyAction(null)
    setNoteOpen(false)
    setNote('')
    setError('')
    setPersistentPrefix(savedBashPrefix(permission))

    let cancelled = false
    void loadBashPrefix(permission, sessionMode)
      .then((prefix) => {
        if (!cancelled && prefix) setPersistentPrefix(prefix)
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [permission.id, permission.mode, permission.savedRule, permission.toolArguments, sessionMode])

  useLayoutEffect(() => {
    const content = contentRef.current
    if (!content) return undefined

    const measure = () => {
      if (measuredPermissionIdRef.current !== permission.id) {
        measuredPermissionIdRef.current = permission.id
        expansionWasChosenRef.current = false
      }
      const viewportHeight = typeof window === 'undefined' ? 0 : window.innerHeight
      const nextCollapsedHeight = bashPermissionCollapsedHeight(viewportHeight)
      const nextCanExpand = bashPermissionShouldStartExpanded(content.scrollHeight, viewportHeight)
      setCollapsedContentHeight(nextCollapsedHeight)
      setCanExpand(nextCanExpand)
      if (!nextCanExpand) {
        expansionWasChosenRef.current = false
        setExpanded(false)
      } else if (!expansionWasChosenRef.current) {
        setExpanded(true)
      }
    }
    measure()
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    observer?.observe(content)
    window.addEventListener('resize', measure)
    return () => {
      observer?.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [intent, noteOpen, permission.id, persistentPrefix])

  const resolve = async (action: BashPermissionAction) => {
    if (busyAction) return
    setBusyAction(action)
    setError('')
    try {
      await onResolve(permission, action, note.trim())
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
      setBusyAction(null)
    }
  }

  return (
    <section
      className="min-w-0 rounded-xl border border-[var(--app-border-strong)] bg-transparent"
      data-testid="desktop-inline-bash-permission-card"
    >
      <div className="flex min-w-0 items-center gap-2.5 px-3 py-2.5 sm:px-4">
        <Terminal className="size-4 shrink-0 text-[var(--app-text-muted)]" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <div className="text-xs font-semibold text-[var(--app-text)]">Bash permission</div>
            {intent ? (
              <span className="rounded-sm border border-[var(--app-border-strong)] px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-muted)]">
                {intent.category}
              </span>
            ) : null}
          </div>
          <div className="mt-0.5 flex flex-wrap gap-x-2 text-[11px] text-[var(--app-text-subtle)]">
            <span>Approval required</span>
            <span aria-hidden="true">·</span>
            <span>{permissionRequirementLabel(permission.requirement)}</span>
            <span aria-hidden="true">·</span>
            <span>mode {modeLabel}</span>
            {pendingCount > 1 ? <span>· {pendingCount} pending</span> : null}
          </div>
        </div>
        {canExpand ? (
          <button
            type="button"
            className="inline-flex shrink-0 items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
            aria-expanded={expanded}
            aria-controls={`bash-permission-content-${permission.id}`}
            onClick={() => {
              expansionWasChosenRef.current = true
              setExpanded((current) => !current)
            }}
          >
            {expanded ? 'Collapse' : 'Expand'}
            {expanded ? <ChevronUp className="size-3.5" aria-hidden="true" /> : <ChevronDown className="size-3.5" aria-hidden="true" />}
          </button>
        ) : null}
      </div>

      <div
        id={`bash-permission-content-${permission.id}`}
        className="min-w-0 overflow-hidden border-t border-[var(--app-border)] px-3 py-3 sm:px-4"
        style={{ maxHeight: expanded ? undefined : collapsedContentHeight }}
      >
        <div ref={contentRef} className="min-w-0">
          {intent ? (
            <>
              {intent.critical ? (
                <div className="mb-3 flex items-start gap-2 border-l-2 border-[var(--app-warning)] pl-2.5 text-xs leading-5 text-[var(--app-warning)]" role="alert">
                  <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
                  <div><span className="font-semibold">Pay attention before approving.</span> The AI marked this command as critical.</div>
                </div>
              ) : null}
              <div className="text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">What this command will do</div>
              {intent.explanation.length === 1 ? (
                <p className="mt-1.5 text-sm leading-5 text-[var(--app-text)]">{intent.explanation[0]}</p>
              ) : (
                <ul className="mt-2 grid list-disc gap-2 pl-5 text-sm leading-5 text-[var(--app-text)]">
                  {intent.explanation.map((item, index) => <li key={`${index}-${item}`} className="pl-1 marker:text-[var(--app-text-subtle)]">{item}</li>)}
                </ul>
              )}
            </>
          ) : (
            <div className="border-l-2 border-[var(--app-danger)] pl-2.5 text-sm leading-5 text-[var(--app-danger)]" role="alert">
              Invalid Bash request: precise explanation, read/write/update category, and critical flag are required.
            </div>
          )}
          <div className="mt-3 border-t border-[var(--app-border)] pt-2 text-[11px] leading-5 text-[var(--app-text-subtle)]">
            <span className="font-medium text-[var(--app-text-muted)]">Always allow prefix: </span>
            <span className="whitespace-pre-wrap break-words font-mono text-[var(--app-text)] [overflow-wrap:anywhere]">
              {persistentPrefix || 'available after approval'}
            </span>
            <div>Future Bash commands starting with this prefix will be approved automatically.</div>
          </div>
          {intent ? (
            <>
              <div className="mt-3 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">Command</div>
              <pre className="mt-1.5 overflow-x-auto whitespace-pre-wrap break-words bg-transparent p-0 font-mono text-[12px] leading-5 text-[var(--app-text)] [overflow-wrap:anywhere]">{intent.command}</pre>
            </>
          ) : null}
          {noteOpen ? (
            <label className="mt-3 grid gap-1.5">
              <span className="text-[11px] font-medium text-[var(--app-text-subtle)]">Response note</span>
              <Textarea
                value={note}
                onChange={(event) => setNote(event.target.value)}
                placeholder="Optional note…"
                rows={2}
                className="min-h-14 resize-none bg-transparent"
              />
            </label>
          ) : null}
          {error ? <div className="mt-2 text-xs text-[var(--app-danger)]" role="alert">{error}</div> : null}
        </div>
      </div>

      <div className="flex flex-wrap items-center justify-end gap-2 border-t border-[var(--app-border)] px-3 py-2.5 sm:px-4" data-testid="desktop-inline-bash-controls">
        <button
          type="button"
          className="mr-auto rounded-md px-2 py-1 text-xs text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
          onClick={() => setNoteOpen((current) => !current)}
          disabled={Boolean(busyAction)}
        >
          {noteOpen ? 'Hide note' : note.trim() ? 'Edit note' : 'Add note'}
        </button>
        <Button type="button" variant="ghost" size="sm" onClick={() => void resolve('always_deny')} disabled={Boolean(busyAction)}>
          Always deny
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={() => void resolve('approve_always')} disabled={Boolean(busyAction)}>
          Always allow
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={() => void resolve('deny')} disabled={Boolean(busyAction)}>
          {busyAction === 'deny' ? 'Denying…' : 'Deny'}
        </Button>
        <Button type="button" variant="primary" size="sm" onClick={() => void resolve('approve')} disabled={Boolean(busyAction)}>
          {busyAction === 'approve' ? 'Approving…' : 'Approve'}
        </Button>
      </div>
    </section>
  )
}
