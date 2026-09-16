import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { History, Plus } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { normalizeStructuredPlanDocument } from '../../chat/components/structured-plan-document'
import { DesktopPlanAgentSidecar } from '../../chat/components/desktop-plan-agent-sidecar'
import {
  createAutomationConversation,
  isAutomationManagementSession,
  loadAutomationConversations,
} from '../../state/desktop-automation-conversations'
import type { SessionSnapshot } from '../../state/desktop-v3-cache-types'
import type { AutomationV2Proposal, AutomationV2Record } from '../../state/desktop-automation-v2-api'
import { automationV2PermissionProposal } from '../../state/desktop-automation-v2-api'
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2'
import { getDesktopV3CacheSnapshot, useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import { getOccurrenceDayKey, getScheduleUpcomingCount } from './automation-v2-schedule'

export function isGenericSessionTitle(title?: string): boolean {
  if (!title) return true
  const lower = title.trim().toLowerCase()
  return (
    lower === 'new session' ||
    lower === 'automation conversation' ||
    lower === 'automation session' ||
    lower === 'worker conversation' ||
    lower === 'worker session' ||
    lower === 'new conversation' ||
    lower === 'new chat'
  )
}

export function formatAutomationSessionTitle(session: SessionSnapshot): string {
  const title = session.title?.trim()
  if (title && !isGenericSessionTitle(title)) {
    return title
  }
  if (session.message_count && session.message_count > 0 && (session.created_at || session.updated_at)) {
    const timestamp = session.created_at || session.updated_at
    const dateStr = new Date(timestamp).toLocaleDateString(undefined, {
      month: 'short',
      day: 'numeric',
    })
    const timeStr = new Date(timestamp).toLocaleTimeString(undefined, {
      hour: 'numeric',
      minute: '2-digit',
    })
    return `Chat · ${dateStr}, ${timeStr}`
  }
  return 'New chat'
}

export interface AutomationV2SidecarProps {
  workspaceId: string
  workspacePath: string
  workspaceBindingId?: string
  selectedAutomation?: AutomationV2Record | null
  activeSessionId?: string
  onSelectSession?: (sessionId: string) => void
  createRequest?: number
  records?: AutomationV2Record[]
  pendingProposals?: AutomationV2Proposal[]
  initialDraft?: string
  onClearSelectedAutomation?: () => void
}

export function AutomationV2Sidecar({
  workspaceId,
  workspacePath,
  workspaceBindingId,
  selectedAutomation,
  activeSessionId,
  onSelectSession,
  createRequest,
  records,
  pendingProposals,
  initialDraft,
  onClearSelectedAutomation,
}: AutomationV2SidecarProps) {
  const [conversations, setConversations] = useState<SessionSnapshot[]>([])
  const [loading, setLoading] = useState(true)
  const [creating, setCreating] = useState(false)
  const [directSessionId, setDirectSessionId] = useState<string | undefined>()
  const autoCreatedRef = useRef(false)
  const handledCreateRequest = useRef(0)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  // Load prior automation management conversations for this workspace
  const refreshConversations = useCallback(async (signal?: AbortSignal) => {
    try {
      setLoading(true)
      const page = await loadAutomationConversations(workspaceId, workspacePath, undefined, signal)
      if (!mountedRef.current) return
      const list = page.session_order
        .map((id) => page.sessions_by_id[id])
        .filter((session): session is SessionSnapshot => Boolean(session) && isAutomationManagementSession(session, workspaceId))
      setConversations(list)
      for (const session of list) {
        const cached = getDesktopV3CacheSnapshot()
        const perms = cached.permissionsBySession[session.id]
        if (!perms || perms.length === 0) {
          const summary = cached.permissionSummaryBySessionId[session.id]
          if ((summary?.pendingApprovalCount ?? 0) > 0 || session.metadata?.swarm_v3_session_purpose === 'automation_management') {
            void desktopAutomationV2.reconcileSession(session.id).catch(() => {})
          }
        }
      }
      return list
    } catch {
      // Bounded failure handling; conversations remain empty
      return []
    } finally {
      if (mountedRef.current) setLoading(false)
    }
  }, [workspaceId, workspacePath])

  useEffect(() => {
    const controller = new AbortController()
    void refreshConversations(controller.signal).then((list) => {
      // If opening without an explicit automation or active session selected, default to prepping a new chat
      if (!selectedAutomation && !activeSessionId && !directSessionId) {
        // If the most recent conversation is already empty (message_count === 0), reuse it as the new chat
        const emptyConv = list?.find((c) => !c.message_count || c.message_count === 0)
        if (emptyConv) {
          setDirectSessionId(emptyConv.id)
          onSelectSession?.(emptyConv.id)
        } else if (!autoCreatedRef.current) {
          autoCreatedRef.current = true
          void handleCreateNew()
        }
      }
    })
    return () => controller.abort()
  }, [refreshConversations, selectedAutomation, activeSessionId])

  // Sync with activeSessionId passed from parent (e.g. onChat callback from run feed or details)
  useEffect(() => {
    if (!activeSessionId) return
    if (selectedAutomation && activeSessionId === selectedAutomation.session_id) {
      setDirectSessionId(undefined)
    } else {
      setDirectSessionId(activeSessionId)
    }
  }, [activeSessionId, selectedAutomation])

  // Handle external create requests (e.g. "Add automation" in header)
  useEffect(() => {
    if (createRequest && createRequest !== handledCreateRequest.current) {
      handledCreateRequest.current = createRequest
      void handleCreateNew()
    }
  }, [createRequest])

  const handleCreateNew = async () => {
    if (creating) return
    setCreating(true)
    try {
      const clientRequestId = crypto.randomUUID()
      const newSession = await createAutomationConversation(workspacePath, clientRequestId, workspaceId, undefined, workspaceBindingId)
      if (!mountedRef.current) return
      setConversations((prev) => [newSession, ...prev.filter((s) => s.id !== newSession.id)])
      setDirectSessionId(newSession.id)
      onClearSelectedAutomation?.()
      onSelectSession?.(newSession.id)
    } catch {
      // Failed creation leaves current selection
    } finally {
      if (mountedRef.current) setCreating(false)
    }
  }

  const handleSelectSession = (value: string) => {
    if (value === '__new__' || value === '__all__') {
      onClearSelectedAutomation?.()
      const emptyConv = mergedConversations.find((c) => !c.message_count || c.message_count === 0)
      if (emptyConv) {
        setDirectSessionId(emptyConv.id)
        onSelectSession?.(emptyConv.id)
      } else {
        void handleCreateNew()
      }
    } else if (value === '__bound__') {
      setDirectSessionId(undefined)
      if (selectedAutomation) onSelectSession?.(selectedAutomation.session_id)
    } else if (value.startsWith('pending:')) {
      const targetSessionId = value.slice('pending:'.length)
      setDirectSessionId(targetSessionId)
      onSelectSession?.(targetSessionId)
    } else if (value.startsWith('automation:')) {
      const targetSessionId = value.slice('automation:'.length)
      setDirectSessionId(undefined)
      onSelectSession?.(targetSessionId)
    } else {
      setDirectSessionId(value)
      onSelectSession?.(value)
    }
  }

  // Live session cache selector for reactive title and message count updates
  const cacheSessionsById = useDesktopV3CacheSelector((state) => state.sessionsById)
  const mergedConversations = useMemo(() => {
    return conversations.map((c) => {
      const cached = cacheSessionsById[c.id]
      return cached && cached.kind === 'full' ? cached.session : c
    })
  }, [conversations, cacheSessionsById])

  const currentConversation = useMemo(() => {
    if (!directSessionId) return null
    const cached = cacheSessionsById[directSessionId]
    if (cached && cached.kind === 'full') return cached.session
    return conversations.find((c) => c.id === directSessionId) ?? null
  }, [cacheSessionsById, conversations, directSessionId])

  const currentEmptyConversation = useMemo(() => {
    return mergedConversations.find((c) => !c.message_count || c.message_count === 0) ?? null
  }, [mergedConversations])

  const pastConversations = useMemo(() => {
    return mergedConversations.filter((c) => {
      if (currentEmptyConversation && c.id === currentEmptyConversation.id) {
        return false
      }
      return true
    })
  }, [mergedConversations, currentEmptyConversation])

  const progressKey = useMemo(() => {
    if (!selectedAutomation) return null
    return automationV2PageKey({
      action: 'progress',
      workspace_id: workspaceId,
      session_id: selectedAutomation.session_id,
      timezone: 'UTC',
    })
  }, [selectedAutomation, workspaceId])
  const progressPage = useDesktopV3CacheSelector((state) => progressKey ? state.automationV2Pages?.[progressKey] : undefined)
  const isRunning = useDesktopV3CacheSelector((state) => {
    if (!selectedAutomation) return false
    const occurrences = progressPage?.data?.progress?.occurrences
    if (occurrences?.some((o) => o.state === 'running' || o.state === 'in_progress')) return true
    if (occurrences) {
      for (const occ of occurrences) {
        const intent = state.currentRunIntentBySession?.[occ.session_id]
        if (intent && ['pending_executor', 'running', 'dispatch_blocked'].includes(intent.status)) return true
      }
    }
    const selfIntent = state.currentRunIntentBySession?.[selectedAutomation.session_id]
    if (selfIntent && ['pending_executor', 'running', 'dispatch_blocked'].includes(selfIntent.status)) return true
    return false
  })

  const tz = progressPage?.data?.progress?.timezone || selectedAutomation?.document?.automation_v2?.schedule?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone
  const occurrences = progressPage?.data?.progress?.occurrences
  const forecast = progressPage?.data?.progress?.forecast

  const runsToday = useMemo(() => {
    if (!occurrences) return 0
    const todayKey = getOccurrenceDayKey(Date.now(), tz)
    return occurrences.filter((o) => getOccurrenceDayKey(o.due_at || 0, tz) === todayKey).length
  }, [occurrences, tz])

  const upcomingCount = useMemo(() => {
    if (!selectedAutomation || !selectedAutomation.enabled || selectedAutomation.cancelled) return 0
    return getScheduleUpcomingCount(
      selectedAutomation.document?.automation_v2?.schedule,
      selectedAutomation.next_due_at,
      Date.now(),
      tz,
      forecast,
    )
  }, [forecast, selectedAutomation, tz])

  const currentPendingProposal = useMemo(() => {
    const sId = directSessionId || (selectedAutomation ? selectedAutomation.session_id : undefined)
    if (!sId) return null
    return pendingProposals?.find((p) => p.session_id === sId) ?? null
  }, [directSessionId, selectedAutomation, pendingProposals])

  const activePendingPermission = useDesktopV3CacheSelector((state) => {
    const sId = directSessionId || (selectedAutomation ? selectedAutomation.session_id : undefined)
    if (!sId) return undefined
    return state.permissionsBySession[sId]?.find((p) => String(p.status).toLowerCase() === 'pending' && String(p.requirement).toLowerCase() === 'automation_v2_acceptance')
  })

  const sidecarStructuredDoc = useMemo(() => {
    if (currentPendingProposal?.document) {
      return normalizeStructuredPlanDocument(currentPendingProposal.document)
    }
    if (activePendingPermission) {
      const prop = automationV2PermissionProposal(activePendingPermission)
      if (prop?.document) {
        return normalizeStructuredPlanDocument(prop.document)
      }
    }
    return undefined
  }, [currentPendingProposal, activePendingPermission])

  const title = useMemo(() => {
    if (currentPendingProposal) {
      return `Review: ${currentPendingProposal.document.title}`
    }
    if (selectedAutomation && !directSessionId) {
      return `Optimize: ${selectedAutomation.document.title}`
    }
    if (currentConversation) {
      const convTitle = currentConversation.title?.trim()
      if (convTitle && !isGenericSessionTitle(convTitle)) {
        return convTitle
      }
      if (currentConversation.message_count && currentConversation.message_count > 0) {
        return formatAutomationSessionTitle(currentConversation)
      }
      return 'New worker chat'
    }
    if (selectedAutomation) {
      return `Optimize: ${selectedAutomation.document.title}`
    }
    return 'Worker Agent'
  }, [currentConversation, directSessionId, selectedAutomation])

  // Header switcher: dropdown for prior conversations and + New button
  const headerActions = (
    <div className="flex items-center gap-1.5" data-testid="automation-sidecar-session-switcher">
      {isRunning && (
        <span
          data-testid="sidecar-running-badge"
          className="inline-flex items-center gap-1 rounded-full bg-[var(--app-success-bg,rgba(34,197,94,0.14))] px-1.5 py-0.5 text-[9px] font-medium text-[var(--app-success)]"
          title="Worker is currently running an occurrence"
        >
          <span className="h-1.5 w-1.5 rounded-full bg-[var(--app-success)] animate-pulse" aria-hidden="true" />
          <span>Running</span>
        </span>
      )}
      {(runsToday > 0 || upcomingCount > 0) && (
        <span
          data-testid="sidecar-run-stats"
          className="hidden text-[10px] tabular-nums text-[var(--app-text-muted)] sm:inline-block"
          title={`${runsToday} executed today · ${upcomingCount} upcoming`}
        >
          {runsToday > 0 ? `${runsToday} today` : ''}
          {runsToday > 0 && upcomingCount > 0 ? ' · ' : ''}
          {upcomingCount > 0 ? `${upcomingCount} upcoming` : ''}
        </span>
      )}
      {(mergedConversations.length > 0 || selectedAutomation || (records && records.length > 0) || (pendingProposals && pendingProposals.length > 0)) && (
        <label className="relative flex items-center" title="Switch or reopen worker sessions">
          <History size={12} className="pointer-events-none absolute left-2 text-[var(--app-text-muted)]" aria-hidden="true" />
          <select
            className="h-7 max-w-[150px] truncate rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] pl-6 pr-2 text-[11px] font-medium text-[var(--app-text)] outline-none hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] sm:max-w-[180px]"
            value={
              selectedAutomation && !directSessionId
                ? '__bound__'
                : directSessionId
                  ? currentPendingProposal && directSessionId === currentPendingProposal.session_id
                    ? `pending:${directSessionId}`
                    : currentEmptyConversation && directSessionId === currentEmptyConversation.id
                      ? currentEmptyConversation.id
                      : directSessionId
                  : '__new__'
            }
            onChange={(e) => handleSelectSession(e.target.value)}
            aria-label="Prior worker sessions"
          >
            <option value={currentEmptyConversation ? currentEmptyConversation.id : '__new__'}>
              ✨ New chat
            </option>
            <option value="__all__">
              🌐 All workspace workers
            </option>
            {currentPendingProposal && (
              <option value={`pending:${currentPendingProposal.session_id}`}>
                ⏳ {currentPendingProposal.document.title} (Pending)
              </option>
            )}
            {selectedAutomation && (
              <option value="__bound__">
                ⚡ {selectedAutomation.document.title}
              </option>
            )}
            {pendingProposals && pendingProposals.filter((p) => p.session_id !== (directSessionId || selectedAutomation?.session_id)).length > 0 && (
              <optgroup label="Pending worker proposals">
                {pendingProposals
                  .filter((p) => p.session_id !== (directSessionId || selectedAutomation?.session_id))
                  .map((p) => (
                    <option key={p.session_id} value={`pending:${p.session_id}`}>
                      ⏳ {p.document.title} (Pending)
                    </option>
                  ))}
              </optgroup>
            )}
            {pastConversations.length > 0 && (
              <optgroup label="Recent worker chats">
                {pastConversations.map((c) => (
                  <option key={c.id} value={c.id}>
                    💬 {formatAutomationSessionTitle(c)}
                  </option>
                ))}
              </optgroup>
            )}
            {records && records.filter((r) => r.session_id !== selectedAutomation?.session_id).length > 0 && (
              <optgroup label="Worker sessions">
                {records
                  .filter((r) => r.session_id !== selectedAutomation?.session_id)
                  .map((r) => (
                    <option key={r.session_id} value={`automation:${r.session_id}`}>
                      ⚡ {r.document.title}
                    </option>
                  ))}
              </optgroup>
            )}
          </select>
        </label>
      )}
      <Button
        size="sm"
        variant="ghost"
        className="h-7 gap-1 px-2 text-[11px]"
        title="New worker conversation"
        onClick={() => void handleCreateNew()}
        disabled={creating || loading}
      >
        <Plus size={13} />
        <span>New</span>
      </Button>
    </div>
  )

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col overflow-hidden" data-testid="automation-v2-sidecar-container">
      <DesktopPlanAgentSidecar
        key={directSessionId ? `direct:${directSessionId}` : `bound:${selectedAutomation?.session_id ?? 'overview'}`}
        directSessionId={directSessionId}
        parentSessionId={directSessionId ? undefined : selectedAutomation?.session_id}
        automation={
          selectedAutomation
            ? {
                automation_v2: true,
                automation_id: selectedAutomation.automation_id,
                automation_revision: selectedAutomation.revision,
                workspace_id: workspaceId,
              }
            : {
                automation_v2: true,
                automation_id: '',
                automation_revision: 0,
                workspace_id: workspaceId,
              }
        }
        title={title}
        headerActions={headerActions}
        sidebarInline
        embedded
        permission={activePendingPermission}
        document={sidecarStructuredDoc ?? undefined}
        initialDraft={initialDraft}
      />
    </div>
  )
}
