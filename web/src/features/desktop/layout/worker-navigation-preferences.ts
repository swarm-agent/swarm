import { useSyncExternalStore } from 'react'

// Presentation only. Never sent to worker APIs or used to suppress approval attention.
type Preferences = { collapsed?: boolean; hidden?: string[] }
const empty: Preferences = {}
const cache = new Map<string, Preferences>()
const event = 'swarm-worker-navigation-preferences'
function key(account: string, project: string) { return `swarm:worker-navigation:${encodeURIComponent(account)}:${encodeURIComponent(project)}` }
function read(storageKey: string): Preferences {
  if (cache.has(storageKey)) return cache.get(storageKey)!
  let value: Preferences = empty
  try {
    const parsed = JSON.parse(window.localStorage.getItem(storageKey) || '{}')
    value = { collapsed: parsed.collapsed === true, hidden: Array.isArray(parsed.hidden) ? parsed.hidden.filter((id: unknown) => typeof id === 'string') : [] }
  } catch { /* Storage unavailable: presentation remains usable for this visit. */ }
  cache.set(storageKey, value)
  return value
}
function subscribe(listener: () => void) {
  const changed = () => { cache.clear(); listener() }
  window.addEventListener('storage', changed)
  window.addEventListener(event, listener)
  return () => { window.removeEventListener('storage', changed); window.removeEventListener(event, listener) }
}
export function useWorkerNavigationPreferences(account: string, project: string) {
  const storageKey = key(account, project)
  const preferences = useSyncExternalStore(subscribe, () => read(storageKey), () => empty)
  const update = (patch: Preferences) => {
    const value = { ...read(storageKey), ...patch }
    try { window.localStorage.setItem(storageKey, JSON.stringify(value)) } catch { /* No worker mutation on storage failure. */ }
    cache.set(storageKey, value)
    window.dispatchEvent(new Event(event))
  }
  return { ...preferences, toggle: () => update({ collapsed: !preferences.collapsed }), hide: (id: string) => update({ hidden: [...new Set([...(preferences.hidden || []), id])] }), restore: (id: string) => update({ hidden: (preferences.hidden || []).filter(item => item !== id) }) }
}
