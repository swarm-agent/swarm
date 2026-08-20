import { useEffect, useState, type FormEvent } from 'react'
import { Button } from '../../../../../components/ui/button'
import { Input } from '../../../../../components/ui/input'
import { normalizeArtifactLibraryDirectory, type ArtifactLibrarySettings } from '../../swarm/types/swarm-settings'

interface ArtifactLibrarySettingsProps {
  value: ArtifactLibrarySettings
  saving: boolean
  error?: string | null
  onSave: (value: ArtifactLibrarySettings) => void
}

export function ArtifactLibrarySettingsSection({ value, saving, error, onSave }: ArtifactLibrarySettingsProps) {
  const [libraryDirectory, setLibraryDirectory] = useState(value.libraryDirectory)
  useEffect(() => setLibraryDirectory(value.libraryDirectory), [value.libraryDirectory])

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    onSave({ libraryDirectory: normalizeArtifactLibraryDirectory(libraryDirectory) })
  }

  return (
    <section aria-labelledby="artifact-library-title" className="space-y-4">
      <div>
        <h3 id="artifact-library-title" className="text-base font-semibold text-[var(--app-text)]">Artifact working copies</h3>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">
          “Show in folder” publishes a verified local working copy here. Downloads remain available for remote access, and managed private storage remains authoritative.
        </p>
      </div>
      {error ? <div role="alert" className="text-sm text-[var(--app-danger)]">{error}</div> : null}
      <form className="grid gap-3" onSubmit={submit}>
        <label className="grid gap-1.5 text-xs font-medium text-[var(--app-text-muted)]">
          Working-copy directory
          <Input value={libraryDirectory} disabled={saving} onChange={(event) => setLibraryDirectory(event.target.value)} placeholder="Use the system cache directory" autoComplete="off" spellCheck={false} />
        </label>
        <p className="text-xs text-[var(--app-text-subtle)]">Leave blank to use the platform cache (for example $XDG_CACHE_HOME/swarm/artifacts on Linux). The directory changes only when you save.</p>
        <div><Button type="submit" variant="primary" disabled={saving}>{saving ? 'Saving…' : 'Save working-copy location'}</Button></div>
      </form>
    </section>
  )
}
