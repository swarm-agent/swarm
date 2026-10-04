import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { RepositoryReviewPanel } from '../onboarding/components/repository-review-panel'
import { getDesktopSessionIdentitySnapshot, requestJson } from '../../../app/api'
import { Button } from '../../../components/ui/button'
import { Input } from '../../../components/ui/input'
import { desktopProjects } from '../runtime/desktop-projects'
import { ProjectCreationRuntime, projectCreationDeps, registerProjectFolder } from '../runtime/project-creation'
import type { CreationProject, CreationWorkspace, ProjectCreationDraft } from '../state/project-creation'

function draftKey() { return `swarm:project-creation:${getDesktopSessionIdentitySnapshot()?.accountScopeId || ''}` }
function loadDraft(): ProjectCreationDraft | undefined {
  try { return JSON.parse(sessionStorage.getItem(draftKey()) || 'null') || undefined } catch { return undefined }
}

export function ProjectCreationFlow({ project, onSaved, onOpen, onCancel }: {
  project?: CreationProject
  onSaved: (project: CreationProject) => void
  onOpen: (project: CreationProject, sessionId: string) => void
  onCancel: () => void
}) {
  const [runtime] = useState(() => new ProjectCreationRuntime({ ...projectCreationDeps,
    saveDraft: draft => sessionStorage.setItem(draftKey(), JSON.stringify(draft)),
    clearDraft: () => sessionStorage.removeItem(draftKey()),
  }, project ? undefined : loadDraft(), project))
  const state = useSyncExternalStore(runtime.subscribe, runtime.snapshot)
  const [name, setName] = useState(state.draft?.name || '')
  const [description, setDescription] = useState(state.draft?.description || '')
  const [workspaces, setWorkspaces] = useState<CreationWorkspace[]>(state.draft?.workspaces || [])
  const [selected, setSelected] = useState<string[]>(state.draft?.workspaces.map(w => w.workspace_id) || [])
  const [folder, setFolder] = useState('')
  const [reviewPath, setReviewPath] = useState('')
  const [adding, setAdding] = useState(false)
  const addingRef = useRef(false)
  const [catalogError, setCatalogError] = useState('')
  const [folderError, setFolderError] = useState('')
  const [confirming, setConfirming] = useState(!!state.draft)
  const savedRef = useRef(onSaved); savedRef.current = onSaved
  const openRef = useRef(onOpen); openRef.current = onOpen
  const mounted = useRef(true)
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const locked = !!state.draft || !!state.project || state.busy
  const chosen = workspaces.filter(w => selected.includes(w.workspace_id))
  const loadCatalog = async () => {
    try {
      const { workspaces: rows = [] } = await requestJson<{ workspaces?: Array<{ id?: string; workspace_id?: string; path: string; name?: string }> }>('/v1/workspace/list?limit=200')
      if (!mounted.current) return
      setWorkspaces(previous => {
        const catalog = rows.flatMap(w => (w.id || w.workspace_id) ? [{ workspace_id: (w.id || w.workspace_id)!, path: w.path, label: w.name || w.path, role: 'auxiliary' as const }] : [])
        return [...catalog, ...previous.filter(w => !catalog.some(c => c.workspace_id === w.workspace_id))]
      })
      setCatalogError('')
    } catch (error) { if (mounted.current) setCatalogError(error instanceof Error ? error.message : 'Unable to load workspaces') }
  }
  useEffect(() => { void loadCatalog() }, [])
  useEffect(() => {
    if (!state.project) return
    savedRef.current(state.project)
  }, [state.project])
  useEffect(() => {
    if (!state.project?.id) return
    const unsubscribe = desktopProjects.onProjectUpdate(id => { if (!id || id === state.project?.id) void runtime.refresh() })
    void runtime.refresh()
    return unsubscribe
  }, [runtime, state.project?.id])
  // Auto-open only a newly confirmed operation. Re-entered projects get an explicit
  // Continue action, so a background receipt cannot steal a different route.
  const autoOpen = useRef(false)
  useEffect(() => {
    if (!autoOpen.current || state.project?.context_generation?.status !== 'ready' || state.busy || state.error) return
    autoOpen.current = false
    void runtime.open().then(id => { if (id && mounted.current) openRef.current(runtime.snapshot().project!, id) })
  }, [runtime, state.project, state.busy, state.error])
  const addFolder = async () => {
    if (!folder.trim() || addingRef.current || locked) return
    addingRef.current = true; setAdding(true); setFolderError('')
    try {
      const registered = await registerProjectFolder(folder)
      if (!mounted.current) return
      setWorkspaces(rows => [...rows.filter(w => w.workspace_id !== registered.workspace_id), registered])
      setSelected(ids => [...new Set([...ids, registered.workspace_id])]); setFolder('')
    } catch (error) { if (mounted.current) setFolderError(error instanceof Error ? error.message : 'Unable to register folder') }
    finally { addingRef.current = false; if (mounted.current) setAdding(false) }
  }
  const create = () => {
    autoOpen.current = true
    void runtime.create(state.draft ?? { client_request_id: `desktop-project:${crypto.randomUUID()}`, name: name.trim(), description: description.trim(), workspaces: chosen })
  }
  const generation = state.project?.context_generation
  const controlClass = 'grid gap-3 rounded-2xl border border-[var(--app-border)] p-4'
  return <section aria-label="Project creation" className="min-h-0 flex-1 overflow-y-auto p-6 space-y-5">
    <header className="flex items-center justify-between gap-4"><h1 className="text-xl font-semibold">{state.project ? state.project.name : 'Create your project'}</h1><Button variant="outline" onClick={onCancel}>Back to projects</Button></header>
    <p>Select registered folders, confirm, then Swarm generates project.md before opening your project chat.</p>
    {state.error && <p role="alert">{state.error}</p>}
    {!state.project ? <>
      <fieldset disabled={locked || adding} className={controlClass}>
        <label>Project name<Input value={name} onChange={e => setName(e.target.value)} autoFocus /></label>
        <label>Description<Input value={description} onChange={e => setDescription(e.target.value)} /></label>
        <h2>Project folders</h2>
        {workspaces.map(w => <label key={w.workspace_id} className="flex gap-3 items-start break-all"><input type="checkbox" checked={selected.includes(w.workspace_id)} onChange={e => setSelected(ids => e.target.checked ? [...ids, w.workspace_id] : ids.filter(id => id !== w.workspace_id))} /><span>{w.label}<small className="block">{w.path}</small></span></label>)}
        <label>Add folder path<Input value={folder} onChange={e => setFolder(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); void addFolder() } }} /></label>
        <Button type="button" disabled={!folder.trim()} onClick={() => void addFolder()}>Add folder</Button>
      </fieldset>
      {adding && <p role="status">Registering folder…</p>}
      {folderError && <div role="alert">{folderError}<Button disabled={adding || locked} onClick={() => void addFolder()}>Retry adding folder</Button></div>}
      {catalogError && <div role="alert">{catalogError}<Button onClick={() => void loadCatalog()}>Reload workspaces</Button></div>}
      <p>Git is not required for project context or chat. Coding tasks require an explicitly selected, committed Git repository.</p>
      {chosen.map(w => <Button key={w.workspace_id} variant="outline" onClick={() => setReviewPath(w.path)}>Review Git setup: {w.label}</Button>)}
      {reviewPath && <RepositoryReviewPanel key={reviewPath} path={reviewPath} onCancel={() => setReviewPath('')} onReady={async () => { setReviewPath('') }} />}
      {confirming || state.draft ? <div className={controlClass}>
        <h2>Confirm project</h2><p>{state.draft?.name || name} · {(state.draft?.workspaces || chosen).length} folders</p>
        <ul>{(state.draft?.workspaces || chosen).map(w => <li key={w.workspace_id} className="break-all">{w.path}</li>)}</ul>
        <p>Swarm will read bounded workspace documentation and structure to generate project.md with your configured provider. No Git initialization or source changes.</p>
        <Button disabled={state.busy || adding || !name.trim() || !chosen.length} onClick={create}>{state.busy ? 'Creating project…' : state.draft ? 'Retry project creation' : 'Create project and generate context'}</Button>
        {!locked && <Button variant="outline" onClick={() => setConfirming(false)}>Back</Button>}
      </div> : <Button disabled={adding || !name.trim() || !chosen.length} onClick={() => setConfirming(true)}>Review project</Button>}
    </> : <div className={controlClass}>
      <h2>Project context · project.md</h2>
      <p role="status">{generation?.status === 'ready' ? 'Project context ready' : generation?.status === 'failed' ? 'Context generation failed' : 'Generating project context…'}</p>
      {generation?.error && <p role="alert">{generation.error}</p>}
      {generation?.router_alert && <p role="status">{generation.router_alert}</p>}
      {generation?.status === 'ready' ? <>
        <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words text-sm">{state.project.project_context}</pre>
        <Button disabled={state.busy} onClick={() => { void runtime.open().then(id => { if (id && mounted.current) openRef.current(runtime.snapshot().project!, id) }) }}>{state.busy ? 'Opening chat…' : 'Continue to project chat'}</Button>
      </> : <>
        <p>You can return from the project list. Retry resumes this project, not a duplicate. For provider failures, check credentials and model settings before retrying.</p>
        <Button disabled={state.busy} onClick={() => void runtime.retry()}>{state.busy ? 'Resuming…' : 'Retry / resume generation'}</Button>
      </>}
      <Button variant="outline" disabled={state.busy} onClick={() => void runtime.refresh()}>Refresh status</Button>
    </div>}
  </section>
}
