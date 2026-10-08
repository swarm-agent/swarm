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
  const controlClass = 'grid gap-3 rounded-2xl border border-[var(--app-border)] p-4'
  return <section aria-label="Project creation" className="min-h-0 flex-1 overflow-y-auto p-6 space-y-5">
    <header className="flex items-center justify-between gap-4"><h1 className="text-xl font-semibold">{state.project ? state.project.name : 'Create your project'}</h1><Button variant="outline" onClick={onCancel}>Back to projects</Button></header>
    <p>Select registered folders, confirm, then Swarm personalizes your project context before opening your project chat. Zero files are written to disk.</p>
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
        <p>{(state.draft?.workspaces || chosen).length > 0 ? "Swarm AI Router will synthesize project context directly into Swarm's durable Pebble database. Zero files will be written to disk." : "Creating project without attached workspaces. You can attach workspaces at any time from project settings."}</p>
        <Button disabled={state.busy || adding || !name.trim()} onClick={create}>{state.busy ? 'Creating project…' : state.draft ? 'Retry project creation' : 'Create project and generate context'}</Button>
        {!locked && <Button variant="outline" onClick={() => setConfirming(false)}>Back</Button>}
      </div> : <div className="flex items-center gap-3">
        <Button disabled={adding || !name.trim()} onClick={() => setConfirming(true)}>Review project</Button>
        {!chosen.length && (
          <Button variant="outline" disabled={adding || !name.trim() || state.busy} onClick={create}>
            Skip workspaces &amp; Talk to Swarm
          </Button>
        )}
      </div>}
    </> : <PersonalizingCard
      project={state.project}
      busy={state.busy}
      onRetry={() => void runtime.retry()}
      onRefresh={() => void runtime.refresh()}
      onOpen={() => { void runtime.open().then(id => { if (id && mounted.current) openRef.current(runtime.snapshot().project!, id) }) }}
    />}
  </section>
}

const TICKER_MESSAGES = [
  '✦ Synthesizing architecture & rules for Swarm…',
  '✦ Scanning repository structure & source files…',
  '✦ Ingesting AGENTS.md instructions for orchestrator…',
  '✦ Creating durable project context in Pebble store…',
  '✦ Tailoring AI assistance specifically for this codebase…',
]

const SYNTHESIS_STAGES = [
  'Registering project & workspaces in Pebble store',
  'AI Router scanning workspace structure & AGENTS.md',
  'Synthesizing architecture, conventions & instructions',
  'Preparing personalized Swarm orchestrator context',
]

export function PersonalizingCard({
  project,
  busy,
  onRetry,
  onRefresh,
  onOpen,
}: {
  project: CreationProject
  busy: boolean
  onRetry: () => void
  onRefresh: () => void
  onOpen: () => void
}) {
  const generation = project.context_generation
  const isReady = generation?.status === 'ready'
  const isFailed = generation?.status === 'failed'
  const [progress, setProgress] = useState(20)
  const [tickerIndex, setTickerIndex] = useState(0)

  useEffect(() => {
    if (isReady) {
      setProgress(100)
      return
    }
    if (isFailed) return

    const timer = setInterval(() => {
      setProgress((prev) => {
        if (prev >= 92) return 92
        const delta = Math.max(1, Math.round((92 - prev) * 0.15))
        return Math.min(92, prev + delta)
      })
    }, 450)

    const tickerTimer = setInterval(() => {
      setTickerIndex((prev) => (prev + 1) % TICKER_MESSAGES.length)
    }, 2400)

    return () => {
      clearInterval(timer)
      clearInterval(tickerTimer)
    }
  }, [isReady, isFailed])

  const stage = isReady ? 5 : progress < 35 ? 1 : progress < 65 ? 2 : progress < 90 ? 3 : 4

  return (
    <div className="grid gap-4 rounded-2xl border border-[var(--app-border)] p-6 bg-[color-mix(in_oklab,var(--app-surface)_92%,black)]">
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <div className="size-9 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] grid place-items-center font-bold text-sm text-[var(--app-primary)]">
            {isReady ? '✓' : '✦'}
          </div>
          <div>
            <h2 className="text-lg font-bold tracking-tight text-[var(--app-text)]">
              Personalizing your Project..
            </h2>
            <p className="text-xs text-[var(--app-text-muted)]">
              Project: <span className="font-semibold text-[var(--app-text)]">{project.name}</span>
            </p>
          </div>
        </div>
        <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-0.5 text-[11px] font-mono font-medium text-[var(--app-text-muted)]">
          {isReady ? 'Ready' : isFailed ? 'Failed' : `${progress}%`}
        </span>
      </div>

      <div role="status" className="sr-only">
        {isReady ? 'Project context ready' : isFailed ? 'Context generation failed' : 'Generating project context…'}
      </div>

      {!isReady && !isFailed && (
        <>
          <div className="grid gap-1.5">
            <div className="flex items-center justify-between text-xs text-[var(--app-text-muted)]">
              <span>Personalizing context…</span>
              <span className="font-mono">{progress}%</span>
            </div>
            <div className="h-2 w-full overflow-hidden rounded-full bg-[var(--app-surface-subtle)] border border-[var(--app-border)]">
              <div
                className="h-full bg-[var(--app-primary)] transition-all duration-300 ease-out"
                style={{ width: `${progress}%` }}
              />
            </div>
          </div>

          <div className="grid gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 text-xs">
            <div className="font-semibold uppercase tracking-wider text-[var(--app-text-muted)] text-[10px]">
              AI Synthesis Stages
            </div>
            {SYNTHESIS_STAGES.map((label, idx) => {
              const isDone = stage > idx + 1 || (idx === 0)
              const isCurrent = stage === idx + 1
              return (
                <div key={label} className="flex items-center gap-2">
                  <span className={isDone ? 'text-emerald-400 font-bold' : isCurrent ? 'text-[var(--app-primary)] animate-pulse' : 'text-slate-500'}>
                    {isDone ? '✓' : isCurrent ? '✦' : '○'}
                  </span>
                  <span className={isDone ? 'text-[var(--app-text)] font-medium' : isCurrent ? 'text-[var(--app-text)] font-semibold' : 'text-[var(--app-text-muted)]'}>
                    {label}
                  </span>
                  {isCurrent && <span className="ml-auto text-[10px] text-[var(--app-primary)] animate-pulse">active…</span>}
                </div>
              )
            })}
          </div>

          <div className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-xs text-[var(--app-text-muted)] font-mono flex items-center gap-2 overflow-hidden">
            <span className="text-[var(--app-primary)] inline-block animate-spin text-sm">⚙</span>
            <span className="truncate">{TICKER_MESSAGES[tickerIndex]}</span>
          </div>

          <div className="flex items-center justify-between pt-1">
            <p className="text-xs text-[var(--app-text-muted)]">
              Context stored strictly in Pebble store · Zero files written to disk
            </p>
            <Button variant="outline" size="sm" disabled={busy} onClick={onRefresh}>
              Refresh status
            </Button>
          </div>
        </>
      )}

      {isFailed && (
        <div className="grid gap-3 rounded-xl border border-rose-500/30 bg-rose-500/10 p-4">
          <p role="alert" className="text-xs text-rose-300">
            {generation?.error || 'Context generation failed'}
          </p>
          {generation?.router_alert && (
            <p role="status" className="text-xs text-amber-300">
              {generation.router_alert}
            </p>
          )}
          <p className="text-xs text-[var(--app-text-muted)]">
            You can return from the project list. Retry resumes this project, not a duplicate. For provider failures, check credentials and model settings before retrying.
          </p>
          <div className="flex items-center gap-3">
            <Button disabled={busy} onClick={onRetry}>
              {busy ? 'Resuming…' : 'Retry / resume generation'}
            </Button>
            <Button variant="outline" disabled={busy} onClick={onRefresh}>
              Refresh status
            </Button>
          </div>
        </div>
      )}

      {isReady && (
        <div className="grid gap-4 rounded-xl border border-emerald-500/30 bg-emerald-500/10 p-5">
          <div className="flex items-center gap-2.5">
            <span className="text-xl">✨</span>
            <div>
              <h3 className="text-sm font-bold text-white">Project Ready: {project.name}</h3>
              <p className="text-xs text-emerald-300/80">
                Personalized project context stored cleanly in Pebble store.
              </p>
            </div>
          </div>
          {project.project_context && (
            <details className="text-xs text-[var(--app-text-muted)]">
              <summary className="cursor-pointer font-medium hover:text-[var(--app-text)]">
                View synthesized project context
              </summary>
              <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded bg-black/50 p-3 font-mono text-[11px] text-slate-300">
                {project.project_context}
              </pre>
            </details>
          )}
          <div className="flex items-center gap-3 pt-1">
            <Button disabled={busy} onClick={onOpen} className="font-semibold">
              {busy ? 'Opening chat…' : 'Continue to project chat'}
            </Button>
            <Button variant="outline" disabled={busy} onClick={onRefresh}>
              Refresh status
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
