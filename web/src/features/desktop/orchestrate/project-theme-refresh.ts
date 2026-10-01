/** Coalesce durable project invalidations without polling or losing the last event during a read. */
export function createProjectThemeRefresh(projectId: string, refresh: () => Promise<void>) {
  let active = true
  let pending = false
  let running = false
  const invalidate = (changedProjectId?: string) => {
    if (!active || (changedProjectId && changedProjectId !== projectId)) return
    pending = true
    if (running) return
    running = true
    void (async () => {
      while (active && pending) {
        pending = false
        try { await refresh() } catch { /* refresh owns and surfaces its error */ }
      }
      running = false
    })()
  }
  return { invalidate, dispose: () => { active = false; pending = false } }
}
