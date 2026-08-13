import { useEffect } from 'react'

import { openDesktopV3ArtifactCatalogRefresh, type DesktopV3ArtifactCatalogRefreshListener } from './artifact-catalog-refresh'

/** Registers refresh demand only while the canonical artifact catalog is open. */
export function useDesktopV3OpenArtifactCatalogRefresh(open: boolean, refresh: DesktopV3ArtifactCatalogRefreshListener): void {
  useEffect(() => {
    if (!open) return undefined
    const lease = openDesktopV3ArtifactCatalogRefresh(refresh)
    return () => lease.release()
  }, [open, refresh])
}
