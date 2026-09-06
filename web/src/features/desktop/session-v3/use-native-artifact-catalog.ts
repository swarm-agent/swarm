import { useCallback, useEffect, useRef, useState } from 'react'

import { fetchDesktopV3NativeArtifactCatalog, type DesktopV3NativeArtifactSummary } from './artifact-v3-api'
import { useDesktopV3OpenArtifactCatalogRefresh } from './use-artifact-catalog-refresh'

/** A session-scoped consumer of the canonical catalog, never a tool-output cache. */
export function useNativeArtifactCatalog(sessionId: string) {
  const [snapshot, setSnapshot] = useState<{ sessionId: string; artifacts: DesktopV3NativeArtifactSummary[]; loading: boolean; error: string }>({ sessionId, artifacts: [], loading: Boolean(sessionId), error: '' })
  const request = useRef<AbortController | null>(null)
  const refresh = useCallback(async () => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    if (!sessionId) return
    setSnapshot((current) => ({ sessionId, artifacts: current.sessionId === sessionId ? current.artifacts : [], loading: true, error: current.sessionId === sessionId ? current.error : '' }))
    try {
      const artifacts = await fetchDesktopV3NativeArtifactCatalog(sessionId, controller.signal)
      if (!controller.signal.aborted) setSnapshot({ sessionId, artifacts, loading: false, error: '' })
    } catch (cause) {
      if (!controller.signal.aborted) setSnapshot((current) => ({ ...current, loading: false, error: cause instanceof Error ? cause.message : 'Artifacts could not be loaded' }))
    }
  }, [sessionId])
  useEffect(() => {
    void refresh()
    return () => request.current?.abort()
  }, [refresh])
  useDesktopV3OpenArtifactCatalogRefresh(Boolean(sessionId), refresh)
  return { ...(snapshot.sessionId === sessionId ? snapshot : { artifacts: [], loading: Boolean(sessionId), error: '' }), refresh }
}
