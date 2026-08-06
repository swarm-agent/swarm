import type { QueryClient } from '@tanstack/react-query'
import { agentSettingsStateQueryOptions, agentStateQueryOptions, draftModelQueryKey, draftModelQueryOptions } from '../../../queries/query-options'
import type { AgentStateRecord } from '../types/chat'

export async function refreshAgentModelMutationCaches(queryClient: QueryClient): Promise<AgentStateRecord> {
  const [draftModelResult, agentStateResult, agentSettingsStateResult] = await Promise.all([
    queryClient.fetchQuery({
      ...draftModelQueryOptions(),
      staleTime: 0,
    }),
    queryClient.fetchQuery({
      ...agentStateQueryOptions(),
      staleTime: 0,
    }),
    queryClient.fetchQuery({
      ...agentSettingsStateQueryOptions(),
      staleTime: 0,
    }),
  ])
  queryClient.setQueryData(draftModelQueryKey(), draftModelResult)
  queryClient.setQueryData(agentStateQueryOptions().queryKey, agentStateResult)
  queryClient.setQueryData(agentSettingsStateQueryOptions().queryKey, agentSettingsStateResult)
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: draftModelQueryKey(), refetchType: 'inactive' }),
    queryClient.invalidateQueries({ queryKey: agentStateQueryOptions().queryKey, refetchType: 'inactive' }),
    queryClient.invalidateQueries({ queryKey: agentSettingsStateQueryOptions().queryKey, refetchType: 'inactive' }),
    queryClient.invalidateQueries({ queryKey: ['agent-tool-contract'], refetchType: 'active' }),
  ])
  return agentSettingsStateResult
}
