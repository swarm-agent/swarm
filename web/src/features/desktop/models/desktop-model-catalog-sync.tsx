import { useEffect, useRef } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { agentStateQueryOptions, modelOptionsQueryOptions } from '../../queries/query-options'
import { checkModelCatalogSnapshot } from './model-catalog-api'

const MODEL_CATALOG_POLL_INTERVAL_MS = 60 * 60_000

export function DesktopModelCatalogSync() {
  const queryClient = useQueryClient()
  const appliedSnapshotRef = useRef('')
  const catalogCheckQuery = useQuery({
    queryKey: ['model-catalog-snapshot-check'] as const,
    queryFn: ({ signal }: { signal?: AbortSignal }) => checkModelCatalogSnapshot(signal),
    staleTime: 0,
    refetchInterval: MODEL_CATALOG_POLL_INTERVAL_MS,
    refetchIntervalInBackground: true,
    refetchOnMount: 'always',
    retry: 0,
  })

  useEffect(() => {
    const refresh = catalogCheckQuery.data?.refresh
    if (!refresh) {
      return
    }
    const snapshotID = refresh.snapshot_id?.trim() ?? ''
    const snapshotVersion = refresh.snapshot_version?.trim() ?? ''
    if (!snapshotID && !snapshotVersion) {
      return
    }
    const snapshotSignature = [
      snapshotID,
      snapshotVersion,
      refresh.generated_at?.trim() ?? '',
    ].join(':')
    if (appliedSnapshotRef.current === snapshotSignature) {
      return
    }
    appliedSnapshotRef.current = snapshotSignature
    void Promise.all([
      queryClient.invalidateQueries({ queryKey: modelOptionsQueryOptions().queryKey }),
      queryClient.invalidateQueries({ queryKey: agentStateQueryOptions().queryKey }),
    ])
  }, [catalogCheckQuery.data, queryClient])

  return null
}
