// Navigation is URL state only; it never selects a candidate or persists a head.
export interface NativeArtifactNavigation { artifactId: string; revisionRef: string; waveId: string }
export function readNativeArtifactNavigation(search: string): NativeArtifactNavigation {
  const params = new URLSearchParams(search)
  return { artifactId: params.get('native_artifact') || '', revisionRef: params.get('native_revision') || '', waveId: params.get('native_wave') || '' }
}
export function writeNativeArtifactNavigation(value: NativeArtifactNavigation | null) {
  const url = new URL(window.location.href)
  for (const key of ['native_artifact', 'native_revision', 'native_wave']) url.searchParams.delete(key)
  if (value) {
    url.searchParams.set('native_artifact', value.artifactId)
    if (value.revisionRef) url.searchParams.set('native_revision', value.revisionRef)
    if (value.waveId) url.searchParams.set('native_wave', value.waveId)
  }
  window.history.replaceState(window.history.state, '', url)
}
