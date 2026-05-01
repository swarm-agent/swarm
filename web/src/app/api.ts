import { debugLog, createDebugTimer } from '../lib/debug-log'

let desktopSessionReady = false
let desktopSessionPromise: Promise<void> | null = null

async function readErrorMessage(response: Response): Promise<string> {
  const text = (await response.text()).trim()
  if (!text) {
    return `Request failed with status ${response.status}`
  }

  try {
    const payload = JSON.parse(text) as { error?: unknown }
    if (typeof payload.error === 'string' && payload.error.trim() !== '') {
      return payload.error
    }
  } catch {
    // Fall back to the raw response body when it is not JSON.
  }

  return text
}

async function bootstrapDesktopSession(): Promise<void> {
  const finish = createDebugTimer('app-api', 'bootstrap-desktop-session')
  debugLog('app-api', 'bootstrap-desktop-session:request')
  const response = await fetch('/v1/auth/desktop/session', {
    cache: 'no-store',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
    },
  })

  if (!response.ok) {
    finish({ ok: false, status: response.status })
    throw new Error(await readErrorMessage(response))
  }
  desktopSessionReady = true
  finish({ ok: true })
}

function clearDesktopSession() {
  debugLog('app-api', 'clear-desktop-session')
  desktopSessionReady = false
}

export async function ensureDesktopSession(forceRefresh = false): Promise<void> {
  if (forceRefresh) {
    clearDesktopSession()
  }
  if (desktopSessionReady) {
    debugLog('app-api', 'ensure-desktop-session:cache-hit')
    return
  }
  if (!desktopSessionPromise) {
    debugLog('app-api', 'ensure-desktop-session:start-bootstrap')
    desktopSessionPromise = bootstrapDesktopSession().finally(() => {
      debugLog('app-api', 'ensure-desktop-session:promise-cleared')
      desktopSessionPromise = null
    })
  } else {
    debugLog('app-api', 'ensure-desktop-session:await-inflight')
  }
  return desktopSessionPromise
}

export async function apiFetch(input: RequestInfo | URL, init?: RequestInit, attachAuth = true): Promise<Response> {
  const requestLabel = typeof input === 'string' ? input : input instanceof URL ? input.toString() : '[request]'
  const send = async (): Promise<Response> => {
    const headers = new Headers(init?.headers ?? {})
    headers.set('Accept', 'application/json')

    debugLog('app-api', 'apiFetch:send', {
      input: requestLabel,
      method: init?.method ?? 'GET',
      attachAuth,
    })
    return fetch(input, {
      ...init,
      cache: init?.cache ?? 'no-store',
      credentials: init?.credentials ?? 'same-origin',
      headers,
    })
  }

  let response = await send()
  if (attachAuth && response.status === 401) {
    debugLog('app-api', 'apiFetch:retry-on-401', {
      input: requestLabel,
      method: init?.method ?? 'GET',
    })
    clearDesktopSession()
    try {
      await ensureDesktopSession()
    } catch (error) {
      debugLog('app-api', 'apiFetch:desktop-session-bootstrap-failed', {
        input: requestLabel,
        method: init?.method ?? 'GET',
        message: error instanceof Error ? error.message : String(error),
      })
      return response
    }
    response = await send()
  }

  debugLog('app-api', 'apiFetch:response', {
    input: requestLabel,
    method: init?.method ?? 'GET',
    status: response.status,
    ok: response.ok,
  })
  return response
}

export async function requestJson<T>(input: RequestInfo | URL, init?: RequestInit, attachAuth = true): Promise<T> {
  const response = await apiFetch(input, init, attachAuth)

  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }

  return response.json() as Promise<T>
}

export { readErrorMessage }
