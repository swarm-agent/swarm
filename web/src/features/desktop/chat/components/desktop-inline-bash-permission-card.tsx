import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ChevronDown, ChevronUp, Terminal } from 'lucide-react'

import { requestJson } from '../../../../app/api'
import { Button } from '../../../../components/ui/button'
import { Textarea } from '../../../../components/ui/textarea'
import { cn } from '../../../../lib/cn'
import type { DesktopPermissionRecord } from '../../types/realtime'
import { safeString } from '../../permissions/services/desktop-permission-normalization'
import {
  buildGenericPermissionMarkdown,
  permissionRequirementLabel,
} from '../../permissions/services/permission-payload'
import { ChatMarkdown } from './chat-markdown'

const COLLAPSED_CONTENT_HEIGHT = 176

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
  const [expanded, setExpanded] = useState(false)
  const [canExpand, setCanExpand] = useState(false)
  const [busyAction, setBusyAction] = useState<BashPermissionAction | null>(null)
  const [noteOpen, setNoteOpen] = useState(false)
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const [persistentPrefix, setPersistentPrefix] = useState(() => savedBashPrefix(permission))
  const body = buildGenericPermissionMarkdown(permission)
  const modeLabel = (permission.mode || sessionMode).trim() || 'auto'

  useEffect(() => {
    setExpanded(false)
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

    const measure = () => setCanExpand(content.scrollHeight > COLLAPSED_CONTENT_HEIGHT + 1)
    measure()
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    observer?.observe(content)
    window.addEventListener('resize', measure)
    return () => {
      observer?.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [body, noteOpen, persistentPrefix])

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
          <div className="text-xs font-semibold text-[var(--app-text)]">Bash permission</div>
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
            onClick={() => setExpanded((current) => !current)}
          >
            {expanded ? 'Collapse' : 'Expand'}
            {expanded ? <ChevronUp className="size-3.5" aria-hidden="true" /> : <ChevronDown className="size-3.5" aria-hidden="true" />}
          </button>
        ) : null}
      </div>

      <div
        id={`bash-permission-content-${permission.id}`}
        className={cn(
          'min-w-0 overflow-hidden border-t border-[var(--app-border)] px-3 py-3 sm:px-4',
          expanded ? 'max-h-none' : 'max-h-44',
        )}
      >
        <div ref={contentRef} className="min-w-0">
          <ChatMarkdown
            content={body}
            className="text-sm leading-6 [&_pre]:rounded-none [&_pre]:border-0 [&_pre]:bg-transparent [&_pre]:p-0 [&_pre]:shadow-none [&_pre_code]:text-[13px]"
          />
          <div className="mt-3 border-t border-[var(--app-border)] pt-2 text-[11px] leading-5 text-[var(--app-text-subtle)]">
            <span className="font-medium text-[var(--app-text-muted)]">Always allow prefix: </span>
            <span className="whitespace-pre-wrap break-words font-mono text-[var(--app-text)] [overflow-wrap:anywhere]">
              {persistentPrefix || 'available after approval'}
            </span>
            <div>Future Bash commands starting with this prefix will be approved automatically.</div>
          </div>
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
