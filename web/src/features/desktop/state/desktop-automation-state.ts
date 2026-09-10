import type { AutomationRead, AutomationResponse } from './desktop-automation-api'

export interface AutomationPage {
  input: AutomationRead
  data?: AutomationResponse
  requestId?: string
  generation: number
  loading: boolean
  stale: boolean
  error?: string
}
export type AutomationPages = Record<string, AutomationPage>
export type AutomationCacheAction =
  | { type: 'automation.begin'; key: string; input: AutomationRead; requestId: string }
  | { type: 'automation.finish'; key: string; requestId: string; generation: number; data?: AutomationResponse; error?: string }
  | { type: 'automation.invalidate'; workspaceId?: string }
  | { type: 'automation.evict'; key: string }
export function automationPageKey(input: AutomationRead): string {
  return JSON.stringify(Object.entries({ ...input, limit: input.limit ?? 20 }).filter(([, value]) => value !== undefined).sort(([a], [b]) => a.localeCompare(b)))
}
export function reduceAutomationPages(pages: AutomationPages, action: AutomationCacheAction): AutomationPages {
  if (action.type === 'automation.invalidate') {
    return Object.fromEntries(Object.entries(pages).map(([key, page]) => [key, !action.workspaceId || page.input.workspace_id === action.workspaceId ? { ...page, generation: page.generation + 1, stale: true } : page]))
  }
  if (action.type === 'automation.evict') {
    const next = { ...pages }; delete next[action.key]; return next
  }
  const previous = pages[action.key]
  if (action.type === 'automation.begin') {
    return { ...pages, [action.key]: { ...previous, input: action.input, generation: previous?.generation ?? 0, requestId: action.requestId, loading: true, stale: previous?.stale ?? true, error: undefined } }
  }
  if (!previous || previous.requestId !== action.requestId) return pages
  // A durable invalidation during the request makes its response obsolete.
  if (previous.generation !== action.generation) return { ...pages, [action.key]: { ...previous, loading: false, requestId: undefined } }
  return { ...pages, [action.key]: { ...previous, data: action.data ?? previous.data, loading: false, requestId: undefined, stale: !!action.error, error: action.error } }
}
