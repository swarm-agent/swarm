
export interface DesktopSessionAuthIdentity {
  ok: true
  token?: string
  user_id: string
  username?: string
  account_scope_id: string
  expires_at?: string
}

let desktopSessionIdentity: DesktopSessionAuthIdentity | null = null
let desktopSessionPromise: Promise<DesktopSessionAuthIdentity> | null = null

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

async function bootstrapDesktopSession(): Promise<DesktopSessionAuthIdentity> {
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
  const identity = await response.json() as DesktopSessionAuthIdentity
  if (identity.ok !== true || typeof identity.user_id !== 'string' || identity.user_id.trim() === ''
    || typeof identity.account_scope_id !== 'string' || identity.account_scope_id.trim() === '') {
    throw new Error('desktop session identity response is missing user_id or account_scope_id')
  }
  desktopSessionIdentity = identity
  return identity
}

function clearDesktopSession() {
  desktopSessionIdentity = null
}

export async function ensureDesktopSession(forceRefresh = false): Promise<DesktopSessionAuthIdentity> {
  if (forceRefresh) {
    clearDesktopSession()
  }
  if (desktopSessionIdentity) {
    return desktopSessionIdentity
  }
  if (!desktopSessionPromise) {
    desktopSessionPromise = bootstrapDesktopSession().finally(() => {
      desktopSessionPromise = null
    })
  }
  return desktopSessionPromise
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
