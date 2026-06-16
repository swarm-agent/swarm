
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
  desktopSessionReady = true
}

function clearDesktopSession() {
  desktopSessionReady = false
}

export async function ensureDesktopSession(forceRefresh = false): Promise<void> {
  if (forceRefresh) {
    clearDesktopSession()
  }
  if (desktopSessionReady) {
    return
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
