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

export interface AutomationV2SidecarProps {
  workspaceId: string
  workspacePath: string
  selectedAutomation?: AutomationV2Record | null
  activeSessionId?: string
  onSelectSession?: (sessionId: string) => void
  createRequest?: number
}

export function AutomationV2Sidecar({
  workspaceId,
  workspacePath,
  selectedAutomation,
  activeSessionId,
  onSelectSession,
  createRequest,
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
      {(conversations.length > 0 || selectedAutomation) && (
        <label className="relative flex items-center" title="Switch or reopen automation sessions">
          <History size={12} className="pointer-events-none absolute left-2 text-[var(--app-text-muted)]" aria-hidden="true" />
          <select
            className="h-7 max-w-[150px] truncate rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] pl-6 pr-2 text-[11px] font-medium text-[var(--app-text)] outline-none hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] sm:max-w-[180px]"
            value={directSessionId ?? '__bound__'}
            onChange={(e) => handleSelectSession(e.target.value)}
            aria-label="Prior automation sessions"
          >
            {selectedAutomation && (
              <option value="__bound__">
                ⚡ {selectedAutomation.document.title}
              </option>
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
        key={directSessionId ? `direct:${directSessionId}` : `bound:${selectedAutomation?.session_id ?? 'empty'}`}
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
      />
    </div>
  )
}
