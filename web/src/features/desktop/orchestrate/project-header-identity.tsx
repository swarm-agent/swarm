import { useRef, useState } from 'react'
import { ChevronDown } from 'lucide-react'
import type { ProjectSummary } from './orchestrate-types'

// Same checked-in Swarm mark used by workspace-home-identity.tsx.
export function ProjectMark({ image }: { image?: string }) {
  return image ? <img src={image} alt="Project image" className="swarm-project-image" /> : (
    <svg viewBox="0 0 400 400" className="swarm-project-image" role="img" aria-label="Swarm mark">
      <rect x="20" y="20" width="360" height="360" rx="90" ry="90" fill="currentColor" opacity="0.15" />
      <rect x="60" y="60" width="280" height="280" rx="65" ry="65" fill="currentColor" opacity="0.35" />
      <rect x="100" y="100" width="200" height="200" rx="45" ry="45" fill="currentColor" opacity="0.60" />
      <rect x="140" y="140" width="120" height="120" rx="25" ry="25" fill="currentColor" opacity="0.85" />
    </svg>
  )
}

export function ProjectHeaderIdentity({ project, projects, onSelect, onCreate, onManage }: {
  project?: ProjectSummary; projects: ProjectSummary[]; onSelect: (id: string) => void
  onCreate: () => void; onManage: () => void
}) {
  return <div className="swarm-project-identity">
    <ProjectMark image={project?.iconPNGDataURL} />
    <span className="swarm-project-name" aria-hidden="true">{project?.name || 'Select project'}</span>
    <ChevronDown size={12} className="swarm-project-chevron" aria-hidden="true" />
    <select title={project?.name || 'Select project'} aria-label="Current project" value={project?.id || ''} onChange={event => {
      if (event.target.value === '__create') onCreate()
      else if (event.target.value === '__manage') onManage()
      else onSelect(event.target.value)
    }}>
      {!project && <option value="">Select project</option>}
      {projects.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
      <option value="__create">Create project…</option>
      <option value="__manage">Manage projects…</option>
    </select>
  </div>
}

export function ProjectImageSettings({ project, onSave }: {
  project: ProjectSummary; onSave: (id: string, image: string) => Promise<void>
}) {
  const input = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const save = async (file?: File) => {
    if (busy) return
    setBusy(true); setError('')
    try {
      let image = ''
      if (file) {
        if (file.type !== 'image/png' || !file.size || file.size > 1024 * 1024) throw new Error('Choose a non-empty PNG no larger than 1 MB.')
        image = await new Promise<string>((resolve, reject) => {
          const reader = new FileReader()
          reader.onload = () => typeof reader.result === 'string' && reader.result.startsWith('data:image/png;base64,') ? resolve(reader.result) : reject(new Error('Invalid PNG data.'))
          reader.onerror = reader.onabort = () => reject(new Error('Unable to read PNG.'))
          reader.readAsDataURL(file)
        })
      }
      await onSave(project.id, image)
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Unable to save project image.') }
    finally { setBusy(false) }
  }
  return <section aria-label="Project image" className="swarm-project-image-settings">
    <ProjectMark image={project.iconPNGDataURL} />
    <input ref={input} type="file" accept="image/png" aria-label="Project PNG" hidden onChange={event => {
      const file = event.target.files?.[0]; event.target.value = ''; if (file) void save(file)
    }} />
    <button type="button" disabled={busy} onClick={() => input.current?.click()}>Change project image</button>
    <button type="button" disabled={busy || !project.iconPNGDataURL} onClick={() => void save()}>Reset project image</button>
    {error && <p role="alert">{error}</p>}
  </section>
}
