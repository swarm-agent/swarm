// Cards may fetch only the authenticated small derivative, never an original
// URL, external media, data URL, or video metadata. Explicit open keeps originals.
export function taskPreviewURL(value?: string): string | undefined {
  if (!value?.startsWith('/v3/projects/')) return undefined
  const [path, query = ''] = value.split('?')
  if (!/^\/v3\/projects\/[^/]+\/tasks\/[^/]+\/deliverables\/[^/]+$/.test(path)) return undefined
  const params = new URLSearchParams(query)
  if (!/^[a-f0-9]{64}$/.test(params.get('sha256') || '')) return undefined
  if (!['media', 'thumbnail'].includes(params.get('field') || '')) return undefined
  params.set('preview', '1')
  return `${path}?${params}`
}
