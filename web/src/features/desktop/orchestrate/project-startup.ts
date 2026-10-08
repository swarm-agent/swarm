/** Initial readiness is monotonic for a mounted project. Enrichment/refetches
 * never own the reveal. Navigation cancels the owner by changing its React key.
 */
export type ProjectStartupState =
  | { phase: 'loading' }
  | { phase: 'error'; message: string; retry: 'catalog' | 'tasks' | 'route' }
  | { phase: 'ready' }

export function projectStartupState(input: {
  catalogLoaded: boolean
  catalogError: string
  routeError: string
  projectId: string
  tasksObserved: boolean
  tasksError?: string
}): ProjectStartupState {
  if (input.catalogError) return { phase: 'error', message: input.catalogError, retry: 'catalog' }
  if (!input.catalogLoaded) return { phase: 'loading' }
  if (input.routeError) return { phase: 'error', message: input.routeError, retry: 'route' }
  // Task reads own their inline loading/error surface, never chat admission.
  return { phase: 'ready' }
}
