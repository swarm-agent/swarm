import { STARTUP_REQUEST_TIMEOUT_MS, throwIfAborted, waitForSignal, withRequestDeadline } from './request-lifecycle'

export interface DesktopSessionIdentity {
  userId: string
  accountScopeId: string
  username?: string
  expiresAt?: string
}

interface DesktopSessionBootstrapResponse {
  ok?: boolean
  user_id?: string
  userID?: string
  account_scope_id?: string
  accountScopeID?: string
  username?: string
  expires_at?: string
  expiresAt?: string
}

let desktopSessionReady = false
let desktopSessionIdentity: DesktopSessionIdentity | null = null
let desktopSessionPromise: Promise<DesktopSessionIdentity> | null = null

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

function bootstrapDesktopSession(): Promise<DesktopSessionIdentity> {
  return withRequestDeadline(readDesktopSession, STARTUP_REQUEST_TIMEOUT_MS)
}

async function readDesktopSession(signal: AbortSignal): Promise<DesktopSessionIdentity> {
  const response = await fetch('/v1/auth/desktop/session', {
    signal,
    cache: 'no-store',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
    },
  })

  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }
  const payload = await response.json() as DesktopSessionBootstrapResponse
  const userId = stringField(payload.user_id) || stringField(payload.userID)
  const accountScopeId = stringField(payload.account_scope_id) || stringField(payload.accountScopeID)
  if (!userId || !accountScopeId) {
    throw new Error('desktop session bootstrap did not return user/account identity')
  }
  const identity: DesktopSessionIdentity = {
    userId,
    accountScopeId,
    username: stringField(payload.username),
    expiresAt: stringField(payload.expires_at) || stringField(payload.expiresAt),
  }
  throwIfAborted(signal)
  desktopSessionIdentity = identity
  desktopSessionReady = true
  return identity
}

function clearDesktopSession() {
  desktopSessionReady = false
  desktopSessionIdentity = null
}

export function getDesktopSessionIdentitySnapshot(): DesktopSessionIdentity | null {
  return desktopSessionIdentity
}

export function updateDesktopSessionUsername(username: string) {
  const normalized = username.trim()
  if (desktopSessionIdentity && normalized) {
    desktopSessionIdentity = { ...desktopSessionIdentity, username: normalized }
  }
}

export async function ensureDesktopSession(forceRefresh = false): Promise<DesktopSessionIdentity> {
  if (forceRefresh) {
    clearDesktopSession()
  }
  if (desktopSessionReady && desktopSessionIdentity) {
    return desktopSessionIdentity
  }
  if (!desktopSessionPromise) {
    desktopSessionPromise = bootstrapDesktopSession().finally(() => {
      desktopSessionPromise = null
    })
  }
  return desktopSessionPromise
}

function stringField(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

export async function apiFetch(input: RequestInfo | URL, init?: RequestInit, attachAuth = true): Promise<Response> {
  const signal = init?.signal ?? (input instanceof Request ? input.signal : undefined)
  const send = async (): Promise<Response> => {
    throwIfAborted(signal)
    const headers = new Headers(init?.headers ?? {})
    headers.set('Accept', 'application/json')

    return fetch(input, {
      ...init,
      cache: init?.cache ?? 'no-store',
      credentials: init?.credentials ?? 'same-origin',
      headers,
    })
  }

  let response = await waitForSignal(send(), signal)
  throwIfAborted(signal)
  if (attachAuth && response.status === 401) {
    clearDesktopSession()
    try {
      await waitForSignal(ensureDesktopSession(), signal)
    } catch (error) {
      throwIfAborted(signal)
      return response
    }
    throwIfAborted(signal)
    void response.body?.cancel().catch(() => undefined)
    response = await waitForSignal(send(), signal)
  }

  throwIfAborted(signal)
  return response
}

export async function requestJson<T>(input: RequestInfo | URL, init?: RequestInit, attachAuth = true, timeoutMs?: number): Promise<T> {
  return withRequestDeadline(async (signal) => {
    const response = await apiFetch(input, { ...init, signal }, attachAuth)
    if (!response.ok) throw new Error(await readErrorMessage(response))
    return response.json() as Promise<T>
  }, timeoutMs, init?.signal ?? (input instanceof Request ? input.signal : undefined))
}

// Explicit startup/read opt-in; mutations and streaming apiFetch calls have no
// new global deadline.
export function requestStartupJson<T>(input: RequestInfo | URL, init?: RequestInit, attachAuth = true): Promise<T> {
  return requestJson<T>(input, init, attachAuth, STARTUP_REQUEST_TIMEOUT_MS)
}

export { readErrorMessage }
