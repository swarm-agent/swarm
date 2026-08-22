import { patchUISettings } from '../queries/get-ui-settings'
import { withArtifactLibrarySettings } from '../types/swarm-settings'
import type { ArtifactLibrarySettings, UISettingsWire } from '../types/swarm-settings'

export async function saveArtifactLibrarySettings(
  current: UISettingsWire,
  settings: ArtifactLibrarySettings,
): Promise<UISettingsWire> {
  const next = withArtifactLibrarySettings(current, settings)
  return patchUISettings({
    artifacts: {
      library_directory: next.artifacts?.library_directory,
    },
  })
}
