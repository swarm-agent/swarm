import type { QueryClient } from '@tanstack/react-query'
import { fetchAgentState, fetchAgentToolContract, fetchDraftModelPreference, fetchModelOptions } from '../desktop/chat/queries/chat-queries'
import { desktopV3SessionQueryKey, desktopV3SessionQueryOptions, ensureDesktopV3SessionSnapshot, writeDesktopV3SessionSnapshot, type DesktopV3SessionSnapshot } from '../desktop/state/desktop-v3-cache'
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

export function sessionMessagesQueryOptions(sessionId: string, queryClient?: QueryClient) {
  const normalizedSessionId = sessionId.trim()
  return {
    queryKey: sessionMessagesQueryKey(normalizedSessionId),
    queryFn: async () => {
      if (!queryClient) {
        throw new Error('Desktop V3 session messages require the canonical session snapshot cache.')
      }
      const snapshot = await ensureDesktopV3SessionSnapshot(queryClient, normalizedSessionId)
      return snapshot?.messages ?? []
    },
    staleTime: 60_000,
    enabled: normalizedSessionId !== '',
  }
}

export function sessionPreferenceQueryKey(sessionId: string) {
  return ['session-preference', sessionId] as const
}

export function sessionPreferenceQueryOptions(sessionId: string, queryClient?: QueryClient) {
  const normalizedSessionId = sessionId.trim()
  return {
    queryKey: sessionPreferenceQueryKey(normalizedSessionId),
    queryFn: async () => {
      if (!queryClient) {
        throw new Error('Desktop V3 session preference requires the canonical session snapshot cache.')
      }
      const snapshot = await ensureDesktopV3SessionSnapshot(queryClient, normalizedSessionId)
      return snapshot?.preference ?? { preference: { provider: '', model: '', thinking: '', serviceTier: '', contextMode: '', updatedAt: 0 }, contextWindow: 0, maxOutputTokens: 0 }
    },
    staleTime: 60_000,
    enabled: normalizedSessionId !== '',
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

export function ensureSessionRuntimeData(queryClient: QueryClient, sessionId: string) {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return Promise.resolve()
  }

  return ensureDesktopV3SessionSnapshot(queryClient, normalizedSessionId)
}

export function prefetchSessionRuntimeData(queryClient: QueryClient, sessionId: string) {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return Promise.resolve()
  }

  return queryClient.prefetchQuery(desktopV3SessionQueryOptions(normalizedSessionId))
    .then(() => {
      const snapshot = queryClient.getQueryData<DesktopV3SessionSnapshot | null>(desktopV3SessionQueryKey(normalizedSessionId))
      if (snapshot) {
        writeDesktopV3SessionSnapshot(queryClient, snapshot)
      }
    })
}
