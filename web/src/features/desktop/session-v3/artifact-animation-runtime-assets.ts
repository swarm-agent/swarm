import type { DesktopV3ArtifactAnimationProfile } from './artifact-api'

export interface DesktopV3ArtifactLocalRuntimeAssets {
  scripts: readonly string[]
  modules: Readonly<Record<string, string>>
  wasm: Readonly<Record<string, string>>
}

const EMPTY_ASSETS: DesktopV3ArtifactLocalRuntimeAssets = Object.freeze({ scripts: [], modules: {}, wasm: {} })
// Build and dev servers expose only this closed same-install directory; no package path is model-controlled.
const runtime = (filename: string) => `/swarm-animation-runtime/${filename}`

/** Returns only reviewed same-install assets. Heavy runtimes are fetched only after a governed iframe mounts. */
export function desktopV3ArtifactLocalRuntimeAssets(
  profile?: DesktopV3ArtifactAnimationProfile | null,
): DesktopV3ArtifactLocalRuntimeAssets {
  switch (profile?.profileId) {
    case 'spatial_3d':
      return { scripts: [], modules: { three: runtime('three.module.js') }, wasm: {} }
    case 'vector_playback':
      return {
        scripts: [],
        modules: {
          '@lottiefiles/dotlottie-web': runtime('dotlottie.js'),
          '@rive-app/canvas': runtime('rive.js'),
        },
        wasm: { dotLottie: runtime('dotlottie-player.wasm'), rive: runtime('rive.wasm'), riveFallback: runtime('rive_fallback.wasm') },
      }
    default:
      return EMPTY_ASSETS
  }
}
