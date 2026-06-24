import { fetchAgentState, fetchAgentStateSummary, fetchAgentToolContract, fetchDraftModelPreference, fetchModelOptions } from '../desktop/chat/queries/chat-queries'
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
    queryFn: ({ signal }: { signal?: AbortSignal }) => fetchAgentStateSummary(signal),
    staleTime: 5 * 60_000,
  }
}

export function agentSettingsStateQueryOptions() {
  return {
    queryKey: ['agent-state', 'settings'] as const,
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
