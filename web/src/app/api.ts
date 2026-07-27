
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

async function bootstrapDesktopSession(): Promise<DesktopSessionIdentity> {
  const response = await fetch('/v1/auth/desktop/session', {
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
  const send = async (): Promise<Response> => {
    const headers = new Headers(init?.headers ?? {})
    headers.set('Accept', 'application/json')

    return fetch(input, {
      ...init,
      cache: init?.cache ?? 'no-store',
      credentials: init?.credentials ?? 'same-origin',
      headers,
    })
  }

  let response = await send()
  if (attachAuth && response.status === 401) {
    clearDesktopSession()
    try {
      await ensureDesktopSession()
    } catch (error) {
      return response
    }
    response = await send()
  }

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
