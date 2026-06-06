import type { QueryClient } from '@tanstack/react-query'
import { fetchAgentState, fetchAgentToolContract, fetchDraftModelPreference, fetchModelOptions, fetchSessionMessages, fetchSessionPreference } from '../desktop/chat/queries/chat-queries'
import { ensureDesktopV3SessionSnapshot } from '../desktop/state/desktop-v3-cache'
import { getUISettings } from '../desktop/settings/swarm/queries/get-ui-settings'
import { fetchWorkspaceOverview } from '../workspaces/launcher/queries/fetch-workspace-overview'

const DEFAULT_SESSION_LIMIT = 25

function normalizeRoots(roots: string[]): string[] {
  return roots.map((value) => value.trim()).filter((value) => value !== '')
}

export function workspaceOverviewQueryKey(roots: string[] = [], sessionLimit = DEFAULT_SESSION_LIMIT) {
  return ['workspace-overview', { roots: normalizeRoots(roots), sessionLimit }] as const
}

export function workspaceOverviewQueryOptions(roots: string[] = [], sessionLimit = DEFAULT_SESSION_LIMIT) {
  const normalizedRoots = normalizeRoots(roots)
  return {
    queryKey: workspaceOverviewQueryKey(normalizedRoots, sessionLimit),
    queryFn: () => fetchWorkspaceOverview(normalizedRoots, sessionLimit),
    staleTime: 30_000,
  }
}

export function uiSettingsQueryKey() {
  return ['ui-settings'] as const
}

export function uiSettingsQueryOptions() {
  return {
    queryKey: uiSettingsQueryKey(),
    queryFn: () => getUISettings(),
    staleTime: 30_000,
  }
}

export function sessionMessagesQueryKey(sessionId: string) {
  return ['session-messages', sessionId] as const
}

export function sessionMessagesQueryOptions(sessionId: string, sessionApi?: string | null, queryClient?: QueryClient) {
  const normalizedSessionId = sessionId.trim()
  const normalizedSessionApi = sessionApi?.trim().toLowerCase() || 'v3'
  return {
    queryKey: sessionMessagesQueryKey(normalizedSessionId),
    queryFn: async ({ signal }: { signal?: AbortSignal }) => {
      if (normalizedSessionApi === 'v3') {
        if (!queryClient) {
          throw new Error('Desktop V3 session messages require the canonical session snapshot cache.')
        }
        const snapshot = await ensureDesktopV3SessionSnapshot(queryClient, normalizedSessionId)
        return snapshot?.messages ?? []
      }
      return fetchSessionMessages(normalizedSessionId, signal, 0, { sessionApi })
    },
    staleTime: 60_000,
    enabled: normalizedSessionId !== '',
  }
}

export function sessionPreferenceQueryKey(sessionId: string) {
  return ['session-preference', sessionId] as const
}

export function sessionPreferenceQueryOptions(sessionId: string) {
  return {
    queryKey: sessionPreferenceQueryKey(sessionId),
    queryFn: () => fetchSessionPreference(sessionId),
    staleTime: 60_000,
    enabled: sessionId.trim() !== '',
  }
}

export function draftModelQueryKey() {
  return ['draft-model'] as const
}

export function draftModelQueryOptions() {
  return {
    queryKey: draftModelQueryKey(),
    queryFn: ({ signal }: { signal?: AbortSignal }) => fetchDraftModelPreference(signal),
    staleTime: 60_000,
  }
}

export function agentStateQueryOptions() {
  return {
    queryKey: ['agent-state'] as const,
    queryFn: ({ signal }: { signal?: AbortSignal }) => fetchAgentState(signal),
    staleTime: 5 * 60_000,
  }
}

export function agentToolContractQueryOptions(name: string) {
  const normalizedName = name.trim()
  return {
    queryKey: ['agent-tool-contract', normalizedName] as const,
    queryFn: ({ signal }: { signal?: AbortSignal }) => fetchAgentToolContract(normalizedName, signal),
    staleTime: 30_000,
    enabled: normalizedName !== '',
  }
}

export function modelOptionsQueryOptions() {
  return {
    queryKey: ['model-options'] as const,
    queryFn: ({ signal }: { signal?: AbortSignal }) => fetchModelOptions(signal),
    staleTime: 5 * 60_000,
  }
}

export function ensureSessionRuntimeData(queryClient: QueryClient, sessionId: string, sessionApi?: string | null) {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return Promise.resolve()
  }

  if ((sessionApi?.trim().toLowerCase() || 'v3') === 'v3') {
    return ensureDesktopV3SessionSnapshot(queryClient, normalizedSessionId)
  }

  const messagesKey = sessionMessagesQueryKey(normalizedSessionId)
  const preferenceKey = sessionPreferenceQueryKey(normalizedSessionId)

  return Promise.all([
    queryClient.getQueryData(messagesKey) ? Promise.resolve() : queryClient.prefetchQuery(sessionMessagesQueryOptions(normalizedSessionId, sessionApi)),
    queryClient.getQueryData(preferenceKey) ? Promise.resolve() : queryClient.prefetchQuery(sessionPreferenceQueryOptions(normalizedSessionId)),
  ])
}

export async function prefetchSessionRuntimeData(queryClient: QueryClient, sessionId: string, sessionApi?: string | null) {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return
  }

  await ensureSessionRuntimeData(queryClient, normalizedSessionId, sessionApi)
}
