import { apiFetch, readErrorMessage } from '../../../app/api'

export function desktopV3ArtifactEndpoint(sessionId: string, artifactId: string): string {
  const normalizedSessionId = sessionId.trim()
  const normalizedArtifactId = artifactId.trim()
  if (!normalizedSessionId || !normalizedArtifactId) {
    throw new Error('Artifact preview requires a session and artifact ID')
  }
  return `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/artifacts/${encodeURIComponent(normalizedArtifactId)}`
}

export function desktopV3ArtifactPreviewAccessEndpoint(sessionId: string): string {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) throw new Error('Artifact preview access requires a session ID')
  return `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/artifacts/preview-access`
}

export function desktopV3ArtifactPackageBaseEndpoint(sessionId: string, artifactId: string, previewToken: string): string {
  const normalizedToken = previewToken.trim()
  if (!normalizedToken) throw new Error('Artifact preview access token is required')
  return `${desktopV3ArtifactEndpoint(sessionId, artifactId)}/content/access/${encodeURIComponent(normalizedToken)}/`
}

export function buildDesktopV3ArtifactSandboxDocument(source: string, sessionId: string, artifactId: string, previewToken: string): string {
  const packageBase = new URL(desktopV3ArtifactPackageBaseEndpoint(sessionId, artifactId, previewToken), window.location.origin)
  const document = new DOMParser().parseFromString(source, 'text/html')
  const packageSource = packageBase.toString()
  const policy = document.createElement('meta')
  policy.httpEquiv = 'Content-Security-Policy'
  policy.content = [
    "default-src 'none'",
    "script-src 'unsafe-inline' blob:",
    `style-src 'unsafe-inline' ${packageSource}`,
    `img-src ${packageSource} data: blob:`,
    `font-src ${packageSource} data:`,
    `media-src ${packageSource} data: blob:`,
    `frame-src ${packageSource}`,
    "connect-src 'none'",
    "worker-src blob:",
    "object-src 'none'",
    `base-uri ${packageSource}`,
    "form-action 'none'",
  ].join('; ')
  const base = document.createElement('base')
  base.href = packageBase.toString()
  document.head.prepend(policy, base)
  return `<!doctype html>\n${document.documentElement.outerHTML}`
}

export async function fetchDesktopV3ArtifactPreviewToken(sessionId: string, artifactId: string, signal?: AbortSignal): Promise<string> {
  const response = await apiFetch(desktopV3ArtifactPreviewAccessEndpoint(sessionId), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ artifact_id: artifactId }),
    signal,
  })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  const payload = await response.json() as { token?: unknown }
  const token = typeof payload.token === 'string' ? payload.token.trim() : ''
  if (!token) throw new Error('Artifact preview access did not return a token')
  return token
}

export async function fetchDesktopV3Artifact(sessionId: string, artifactId: string, signal?: AbortSignal): Promise<Blob> {
  const response = await apiFetch(desktopV3ArtifactEndpoint(sessionId, artifactId), {
    method: 'GET',
    signal,
  })
  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }
  return response.blob()
}
