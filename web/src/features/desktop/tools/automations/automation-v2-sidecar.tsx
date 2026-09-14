import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { History, Plus } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { DesktopPlanAgentSidecar } from '../../chat/components/desktop-plan-agent-sidecar'
import {
  createAutomationConversation,
  isAutomationManagementSession,
  loadAutomationConversations,
} from '../../state/desktop-automation-conversations'
import type { SessionSnapshot } from '../../state/desktop-v3-cache-types'
import type { AutomationV2Record } from '../../state/desktop-automation-v2-api'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import { getOccurrenceDayKey } from './automation-v2-workspace'

export interface AutomationV2SidecarProps {
  workspaceId: string
  workspacePath: string
  selectedAutomation?: AutomationV2Record | null
  activeSessionId?: string
  onSelectSession?: (sessionId: string) => void
  createRequest?: number
  records?: AutomationV2Record[]
  initialDraft?: string
  onClearSelectedAutomation?: () => void
}

export function AutomationV2Sidecar({
  workspaceId,
  workspacePath,
  selectedAutomation,
  activeSessionId,
  onSelectSession,
  createRequest,
  records,
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
      // If no automation is selected and no conversations exist, auto-create the initial conversation
      // so the user is immediately ready to chat by default without having to click into it
      if (!selectedAutomation && (!list || list.length === 0) && !autoCreatedRef.current) {
        autoCreatedRef.current = true
        void handleCreateNew()
      }
    })
    return () => controller.abort()
  }, [refreshConversations, selectedAutomation])

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
      const newSession = await createAutomationConversation(workspacePath, clientRequestId)
      if (!mountedRef.current) return
      setConversations((prev) => [newSession, ...prev.filter((s) => s.id !== newSession.id)])
      setDirectSessionId(newSession.id)
      onSelectSession?.(newSession.id)
    } catch {
      // Failed creation leaves current selection
    } finally {
      if (mountedRef.current) setCreating(false)
    }
  }

  const handleSelectSession = (value: string) => {
    if (value === '__bound__') {
      setDirectSessionId(undefined)
      if (selectedAutomation) onSelectSession?.(selectedAutomation.session_id)
    } else if (value === '__all__') {
      setDirectSessionId(undefined)
      onClearSelectedAutomation?.()
      onSelectSession?.('')
    } else if (value.startsWith('automation:')) {
      const targetSessionId = value.slice('automation:'.length)
      setDirectSessionId(undefined)
      onSelectSession?.(targetSessionId)
    } else {
      setDirectSessionId(value)
      onSelectSession?.(value)
    }
  }

  // Active title calculation
  const currentConversation = useMemo(() => {
    if (!directSessionId) return null
    return conversations.find((c) => c.id === directSessionId) ?? null
  }, [conversations, directSessionId])

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

  const tz = progressPage?.data?.progress?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone
  const occurrences = progressPage?.data?.progress?.occurrences
  const forecast = progressPage?.data?.progress?.forecast

  const runsToday = useMemo(() => {
    if (!occurrences) return 0
    const todayKey = getOccurrenceDayKey(Date.now(), tz)
    return occurrences.filter((o) => getOccurrenceDayKey(o.due_at || 0, tz) === todayKey).length
  }, [occurrences, tz])

  const upcomingCount = useMemo(() => {
    const now = Date.now()
    if (forecast && forecast.length > 0) {
      return forecast.filter((ms) => ms > now).length
    }
    return selectedAutomation?.next_due_at && selectedAutomation.next_due_at > now ? 1 : 0
  }, [forecast, selectedAutomation])

  const title = useMemo(() => {
    if (currentConversation) {
      return currentConversation.title || 'Automation conversation'
    }
    if (selectedAutomation) {
      return `Optimize: ${selectedAutomation.document.title}`
    }
    return 'Automations Assistant'
  }, [currentConversation, selectedAutomation])

  // Header switcher: dropdown for prior conversations and + New button
  const headerActions = (
    <div className="flex items-center gap-1.5" data-testid="automation-sidecar-session-switcher">
      {isRunning && (
        <span
          data-testid="sidecar-running-badge"
          className="inline-flex items-center gap-1 rounded-full bg-[var(--app-success-bg,rgba(34,197,94,0.14))] px-1.5 py-0.5 text-[9px] font-medium text-[var(--app-success)]"
          title="Automation is currently running an occurrence"
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
      {(conversations.length > 0 || selectedAutomation || (records && records.length > 0)) && (
        <label className="relative flex items-center" title="Switch or reopen automation sessions">
          <History size={12} className="pointer-events-none absolute left-2 text-[var(--app-text-muted)]" aria-hidden="true" />
          <select
            className="h-7 max-w-[150px] truncate rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] pl-6 pr-2 text-[11px] font-medium text-[var(--app-text)] outline-none hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] sm:max-w-[180px]"
            value={directSessionId ?? (selectedAutomation ? '__bound__' : '__all__')}
            onChange={(e) => handleSelectSession(e.target.value)}
            aria-label="Prior automation sessions"
          >
            {selectedAutomation && (
              <option value="__bound__">
                ⚡ {selectedAutomation.document.title}
              </option>
            )}
            <option value="__all__">
              🌐 All workspace automations
            </option>
            {records && records.filter((r) => r.session_id !== selectedAutomation?.session_id).length > 0 && (
              <optgroup label="Other automations">
                {records
                  .filter((r) => r.session_id !== selectedAutomation?.session_id)
                  .map((r) => (
                    <option key={r.session_id} value={`automation:${r.session_id}`}>
                      ⚡ {r.document.title}
                    </option>
                  ))}
              </optgroup>
            )}
            {conversations.length > 0 && (
              <optgroup label="Prior conversations">
                {conversations.map((c) => (
                  <option key={c.id} value={c.id}>
                    💬 {c.title || 'Automation conversation'}
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
        title="New automation conversation"
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
          directSessionId
            ? undefined
            : selectedAutomation
            ? {
                automation_v2: true,
                automation_id: selectedAutomation.automation_id,
                automation_revision: selectedAutomation.revision,
                workspace_id: workspaceId,
              }
            : undefined
        }
        title={title}
        headerActions={headerActions}
        sidebarInline
        embedded
        initialDraft={initialDraft}
      />
    </div>
  )
}
