import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { LoaderCircle } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { getDesktopSessionCreateTarget, type DesktopChatRoute } from '../services/chat-routing'
import { RoutePicker } from './route-picker'
import {
  clearDesktopV3NewSessionOperation,
  createDesktopV3NewSessionOperation,
  loadDesktopV3NewSessionOperation,
  persistDesktopV3NewSessionOperation,
  startNewDesktopV3Session,
  type DesktopV3NewSessionOperation,
  type DesktopV3NewSessionPreference,
} from '../../session-v3/new-session-flow'

export interface DesktopV3NewSessionPaneProps {
  workspace: WorkspaceEntry
  workspaceSlug: string
  routeOptions: DesktopChatRoute[]
  pendingWorktreeBranch?: string | null
  agentName: string
  preference?: DesktopV3NewSessionPreference
}

export function completeDesktopV3NewSessionStarted(input: {
  workspacePath: string
  operation: DesktopV3NewSessionOperation
  mountedRef: { current: boolean }
  setOperation: (operation: DesktopV3NewSessionOperation | null) => void
  navigateToSession: (sessionId: string) => void
}): void {
  clearDesktopV3NewSessionOperation(input.workspacePath, input.operation.operationId)
  if (!input.mountedRef.current) return
  input.setOperation(null)
  input.navigateToSession(input.operation.sessionId)
}

export function DesktopV3NewSessionPane({
  workspace,
  workspaceSlug,
  routeOptions,
  pendingWorktreeBranch,
  agentName: agentNameProp,
  preference,
}: DesktopV3NewSessionPaneProps) {
  const navigate = useNavigate()
  const mountedRef = useRef(true)
  const storedOperation = useMemo(
    () => loadDesktopV3NewSessionOperation(workspace.path),
    [workspace.path],
  )
  const operationRef = useRef<DesktopV3NewSessionOperation | null>(storedOperation)
  const writableRoutes = useMemo(
    () => routeOptions.filter((route) => {
      const target = getDesktopSessionCreateTarget(route)
      return target.endpoint === '/v3/sessions'
        && Boolean(target.swarmId?.trim())
        && Boolean(target.workspaceBindingId?.trim())
    }),
    [routeOptions],
  )
  const unsupportedReason = useMemo(() => {
    if (writableRoutes.length > 0) return null
    for (const route of routeOptions) {
      const target = getDesktopSessionCreateTarget(route)
      if (target.endpoint === null) return target.unsupportedReason
    }
    return 'No writable self/host Desktop V3 route is available for this workspace.'
  }, [routeOptions, writableRoutes.length])
  const [selectedRouteId, setSelectedRouteId] = useState(writableRoutes[0]?.id ?? routeOptions[0]?.id ?? '')
  const selectedRoute = useMemo(
    () => writableRoutes.find((route) => route.id === selectedRouteId) ?? writableRoutes[0] ?? routeOptions[0],
    [routeOptions, selectedRouteId, writableRoutes],
  )
  const retainedOperation = operationRef.current
  const [draft, setDraft] = useState(retainedOperation?.firstMessageRequest.content ?? '')
  const [starting, setStarting] = useState(false)
  const [startError, setStartError] = useState<string | null>(null)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  useEffect(() => {
    if (writableRoutes.length === 0) return
    if (!writableRoutes.some((route) => route.id === selectedRouteId)) {
      setSelectedRouteId(writableRoutes[0]?.id ?? '')
    }
  }, [selectedRouteId, writableRoutes])

  useEffect(() => {
    const operation = loadDesktopV3NewSessionOperation(workspace.path)
    operationRef.current = operation
    if (operation) {
      setDraft(operation.firstMessageRequest.content)
    }
  }, [workspace.path])

  const hasRetainedOperation = Boolean(operationRef.current)
  const submitLabel = hasRetainedOperation ? 'Retry starting session' : 'Start session'
  const canSubmit = Boolean(
    !starting
      && selectedRoute
      && !unsupportedReason
      && (hasRetainedOperation || draft.trim()),
  )

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (starting || !selectedRoute) return

    setStarting(true)
    setStartError(null)
    try {
      const existingOperation = operationRef.current
      const operation = existingOperation ?? (() => {
        const agentName = agentNameProp.trim()
        if (!agentName) {
          throw new Error('New Desktop V3 session requires agent_name')
        }
        return createDesktopV3NewSessionOperation({
          workspacePath: workspace.path,
          workspaceName: workspace.workspaceName,
          route: selectedRoute,
          prompt: draft,
          mode: 'auto',
          agentName,
          preference,
          sessionMetadata: {
            source: 'desktop-v3',
            workspace_path: workspace.path,
          },
          messageMetadata: {
            source: 'desktop-v3',
          },
          worktree: pendingWorktreeBranch
            ? { mode: 'on', branchName: pendingWorktreeBranch }
            : undefined,
        })
      })()
      operationRef.current = operation
      setDraft(operation.firstMessageRequest.content)
      persistDesktopV3NewSessionOperation(operation)

      await startNewDesktopV3Session({
        operation,
        onSessionStarted: () => {
          completeDesktopV3NewSessionStarted({
            workspacePath: workspace.path,
            operation,
            mountedRef,
            setOperation: (nextOperation) => {
              operationRef.current = nextOperation
            },
            navigateToSession: (sessionId) => {
              void navigate({
                to: '/$workspaceSlug/$sessionId',
                params: { workspaceSlug, sessionId },
                replace: true,
              })
            },
          })
        },
      })
    } catch (error) {
      if (mountedRef.current) {
        setStartError(error instanceof Error ? error.message : String(error))
      }
    } finally {
      if (mountedRef.current) {
        setStarting(false)
      }
    }
  }

  function handleAbandonRetainedOperation() {
    const operation = operationRef.current
    if (!operation) return
    const confirmed = window.confirm(
      'Abandon this pending new-session operation? The session or first message may already exist. Retrying is the safest way to avoid duplicates.',
    )
    if (!confirmed) return
    clearDesktopV3NewSessionOperation(workspace.path, operation.operationId)
    operationRef.current = null
    setDraft('')
    setStartError(null)
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]" data-testid="desktop-v3-new-session-pane">
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-6 sm:px-8">
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-4">
          <div>
            <p className="text-xs font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">New Desktop V3 session</p>
            <h1 className="mt-2 text-2xl font-semibold text-[var(--app-text)]">Start in {workspace.workspaceName || workspace.path}</h1>
            <p className="mt-2 text-sm text-[var(--app-text-muted)]">
              This route creates a new durable session, subscribes it on the global V3 socket, then sends the first message.
            </p>
          </div>

          {selectedRoute && routeOptions.length > 1 ? (
            <div className="max-w-sm">
              <RoutePicker
                currentRoute={selectedRoute}
                routes={routeOptions}
                onSelect={setSelectedRouteId}
                disabled={starting || hasRetainedOperation}
                title="Route new session to"
              />
            </div>
          ) : null}

          {unsupportedReason ? (
            <div className="rounded-2xl border border-[var(--app-danger-border,var(--app-border))] bg-[var(--app-danger-bg,var(--app-surface))] px-4 py-3 text-sm text-[var(--app-danger)]" role="alert">
              {unsupportedReason}
            </div>
          ) : null}

          {hasRetainedOperation ? (
            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
              A pending new-session operation is retained for safe retry. Edits are disabled until it succeeds or you abandon it.
              <div className="mt-3">
                <Button type="button" variant="ghost" onClick={handleAbandonRetainedOperation} disabled={starting}>
                  Abandon retained operation
                </Button>
              </div>
            </div>
          ) : null}
        </div>
      </div>

      <form onSubmit={handleSubmit} className="border-t border-[var(--app-border)] bg-[var(--app-bg)] px-4 pb-[calc(var(--app-safe-area-bottom)_+_1rem)] pt-3 sm:px-8" data-testid="desktop-v3-new-session-composer">
        <div className="mx-auto flex w-full max-w-3xl items-end gap-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 shadow-sm focus-within:border-[var(--app-border-strong)]">
          <textarea
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' && !event.shiftKey) {
                event.preventDefault()
                event.currentTarget.form?.requestSubmit()
              }
            }}
            rows={1}
            placeholder={hasRetainedOperation ? 'Retained first prompt' : 'Start a new session…'}
            aria-label="Start a new Desktop V3 session"
            data-testid="desktop-v3-new-session-input"
            disabled={starting || hasRetainedOperation}
            className="max-h-40 min-h-10 flex-1 resize-none bg-transparent py-2 text-sm leading-5 text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-subtle)] disabled:opacity-80"
          />
          <Button type="submit" disabled={!canSubmit} className="h-10 rounded-xl px-4" data-testid="desktop-v3-new-session-send">
            {starting ? <LoaderCircle size={16} className="animate-spin" /> : submitLabel}
          </Button>
        </div>
        {startError ? (
          <div className="mx-auto mt-2 w-full max-w-3xl text-xs text-[var(--app-danger)]" role="alert">
            {startError}
          </div>
        ) : null}
      </form>
    </div>
  )
}
