export interface DesktopV3ArtifactCatalogRefreshLease {
  refresh(): void
  release(): void
}

export type DesktopV3ArtifactCatalogRefreshListener = () => void | Promise<void>

/**
 * Coordinates refresh demand for the canonical artifact API. This is not an
 * artifact cache: open consumers still fetch from /v3/artifacts and own their
 * rendered snapshot. Closed consumers are not retained or refreshed.
 */
export class DesktopV3ArtifactCatalogRefreshCoordinator {
  private readonly listeners = new Map<symbol, DesktopV3ArtifactCatalogRefreshListener>()
  private pendingDrain?: Promise<void>
  private disposed = false

  open(listener: DesktopV3ArtifactCatalogRefreshListener): DesktopV3ArtifactCatalogRefreshLease {
    if (this.disposed) throw new Error('Artifact catalog refresh coordinator is disposed')
    const token = Symbol('desktop-v3-artifact-catalog')
    this.listeners.set(token, listener)
    let released = false
    return {
      refresh: () => {
        if (!released) void this.schedule()
      },
      release: () => {
        if (released) return
        released = true
        this.listeners.delete(token)
      },
    }
  }

  schedule(): Promise<void> {
    if (this.disposed || this.listeners.size === 0) return Promise.resolve()
    if (this.pendingDrain) return this.pendingDrain
    let tracked!: Promise<void>
    const listeners = [...this.listeners.entries()]
    tracked = Promise.resolve().then(async () => {
      if (this.disposed || this.listeners.size === 0) return
      const active = listeners.filter(([token]) => this.listeners.has(token)).map(([, listener]) => listener)
      const results = await Promise.allSettled(active.map((listener) => listener()))
      for (const result of results) {
        if (result.status === 'rejected') console.error('[desktop-v3] artifact catalog refresh failed', result.reason)
      }
    }).finally(() => {
      if (this.pendingDrain === tracked) this.pendingDrain = undefined
    })
    this.pendingDrain = tracked
    return tracked
  }

  diagnostics(): { openCatalogs: number; pending: boolean } {
    return { openCatalogs: this.listeners.size, pending: this.pendingDrain !== undefined }
  }

  dispose(): void {
    this.disposed = true
    this.listeners.clear()
  }
}

const desktopV3ArtifactCatalogRefreshCoordinator = new DesktopV3ArtifactCatalogRefreshCoordinator()

export function openDesktopV3ArtifactCatalogRefresh(listener: DesktopV3ArtifactCatalogRefreshListener): DesktopV3ArtifactCatalogRefreshLease {
  return desktopV3ArtifactCatalogRefreshCoordinator.open(listener)
}

export function refreshOpenDesktopV3ArtifactCatalogs(): Promise<void> {
  return desktopV3ArtifactCatalogRefreshCoordinator.schedule()
}

export function desktopV3ArtifactCatalogRefreshDiagnostics(): { openCatalogs: number; pending: boolean } {
  return desktopV3ArtifactCatalogRefreshCoordinator.diagnostics()
}
