import { useEffect, useMemo, useState } from 'react'
import { ArrowDown, ArrowUp, FileCode2, GripVertical, Pencil, Pin, Play, Plus, ShieldCheck, Trash2, X } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Input } from '../../../../../components/ui/input'
import { Select } from '../../../../../components/ui/select'
import { browseWorkspacePath } from '../../../../workspaces/launcher/queries/browse-workspace-path'
import { listWorkspaces } from '../../../../workspaces/launcher/queries/list-workspaces'
import { resolveWorkspaceBySlug } from '../../../../workspaces/launcher/services/workspace-route'
import type { WorkspaceBrowseEntry } from '../../../../workspaces/launcher/types/workspace'
import { WorkspaceActionIcon, WorkspaceActionIconPicker, normalizeWorkspaceActionIcon } from './workspace-action-icons'
import { DesktopWorkspaceActionPanel } from '../../../chat/components/desktop-workspace-action-panel'
import {
  deleteWorkspaceAction,
  fetchWorkspaceActions,
  reorderWorkspaceActions,
  saveWorkspaceAction,
  type WorkspaceAction,
  type WorkspaceActionDefinition,
  type WorkspaceActionInput,
} from '../../../../workspaces/actions/types'

interface ActionsSettingsPageProps {
  workspaceSlug?: string
  workspacePath?: string
  workspaceName?: string
  compact?: boolean
  onRun?: (action: WorkspaceAction) => void
  onMutated?: (actions: WorkspaceAction[]) => void
}

type Draft = WorkspaceActionDefinition & { id: string }

const emptyDraft = (): Draft => ({ id: '', name: '', description: '', icon: 'zap', entrypoint: '', arguments: [], inputs: [], pinned: false })
const splitArguments = (value: string) => value.split('\n').filter((part) => part.length > 0)
function relativePath(root: string, path: string): string {
  const prefix = `${root.replace(/[\\/]+$/, '')}/`
  return path.startsWith(prefix) ? path.slice(prefix.length) : ''
}

function newInput(): WorkspaceActionInput {
  return { id: '', label: '', description: '', kind: 'text', required: false, placeholder: '', defaultValue: '', arguments: [] }
}

function validateDraft(draft: Draft): string[] {
  const errors: string[] = []
  if (!draft.name.trim()) errors.push('Name is required.')
  if (!draft.entrypoint.trim()) errors.push('Choose a script inside this workspace.')
  if (/^(?:[a-zA-Z]:[\\/]|[\\/])/.test(draft.entrypoint) || draft.entrypoint.split(/[\\/]/).includes('..')) errors.push('Entrypoint must be a non-traversing workspace-relative path.')
  const ids = new Set<string>()
  for (const input of draft.inputs) {
    if (!/^[a-zA-Z][a-zA-Z0-9_-]*$/.test(input.id)) errors.push('Every input needs a unique ID beginning with a letter.')
    if (ids.has(input.id)) errors.push(`Input ID “${input.id}” is duplicated.`)
    ids.add(input.id)
    if (!input.label.trim()) errors.push(`Input “${input.id || 'untitled'}” needs a label.`)
    if (input.kind === 'secret' && input.defaultValue) errors.push(`Secret input “${input.label || input.id}” cannot have a saved default.`)
  }
  return [...new Set(errors)]
}

function invocationPreview(draft: Draft): string {
  const parts = [draft.entrypoint || '<choose a script>', ...draft.arguments]
  for (const input of draft.inputs) parts.push(...input.arguments, `<${input.label || input.id}>`)
  return parts.join(' ')
}

export function ActionsSettingsPage({ workspaceSlug = '', workspacePath: providedWorkspacePath = '', workspaceName: providedWorkspaceName = '', compact = false, onRun, onMutated }: ActionsSettingsPageProps) {
  const [workspacePath, setWorkspacePath] = useState(providedWorkspacePath)
  const [workspaceName, setWorkspaceName] = useState(providedWorkspaceName)
  const [actions, setActions] = useState<WorkspaceAction[]>([])
  const [draft, setDraft] = useState<Draft | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [browserPath, setBrowserPath] = useState('')
  const [browserEntries, setBrowserEntries] = useState<WorkspaceBrowseEntry[]>([])
  const [browsing, setBrowsing] = useState(false)
  const [request, setRequest] = useState(0)
  const [selectedRunAction, setSelectedRunAction] = useState<WorkspaceAction | null>(null)
  const draftErrors = useMemo(() => draft ? validateDraft(draft) : [], [draft])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError('')
    const load = providedWorkspacePath
      ? Promise.resolve({ path: providedWorkspacePath, name: providedWorkspaceName || providedWorkspacePath })
      : listWorkspaces().then((workspaces) => {
          const workspace = resolveWorkspaceBySlug(workspaces, workspaceSlug)
          if (!workspace) throw new Error('Open Actions from a workspace settings route.')
          return { path: workspace.path, name: workspace.workspaceName || workspace.path }
        })
    void load.then((workspace) => {
      if (cancelled) return
      setWorkspacePath(workspace.path)
      setWorkspaceName(workspace.name)
      return fetchWorkspaceActions(workspace.path)
    }).then((next) => {
      if (!cancelled && next) {
        setActions(next)
        onMutated?.(next)
      }
    }).catch((cause) => {
      if (!cancelled) setError(cause instanceof Error ? cause.message : 'Could not load Actions.')
    }).finally(() => {
      if (!cancelled) setLoading(false)
    })
    return () => { cancelled = true }
  }, [onMutated, providedWorkspaceName, providedWorkspacePath, request, workspaceSlug])

  const refresh = async () => {
    const next = await fetchWorkspaceActions(workspacePath)
    setActions(next)
    onMutated?.(next)
    return next
  }

  const openBrowser = async (path = workspacePath) => {
    setBrowsing(true)
    setError('')
    try {
      const result = await browseWorkspacePath(path, true)
      const normalizedRoot = workspacePath.replace(/[\\/]+$/, '')
      const resolved = result.resolvedPath.replace(/[\\/]+$/, '')
      if (resolved !== normalizedRoot && !resolved.startsWith(`${normalizedRoot}/`) && !resolved.startsWith(`${normalizedRoot}\\`)) throw new Error('Script browsing is limited to this workspace.')
      setBrowserPath(result.resolvedPath)
      setBrowserEntries(result.entries)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not browse workspace files.')
    } finally {
      setBrowsing(false)
    }
  }

  const beginEdit = (action?: WorkspaceAction) => {
    setNotice('')
    setBrowserEntries([])
    setBrowserPath('')
    setDraft(action ? { ...action, icon: normalizeWorkspaceActionIcon(action.icon), arguments: [...action.arguments], inputs: action.inputs.map((input) => ({ ...input, arguments: [...input.arguments] })) } : emptyDraft())
  }

  const submit = async () => {
    if (!draft || draftErrors.length) return
    setSaving(true)
    setError('')
    try {
      await saveWorkspaceAction(workspacePath, draft, draft.id)
      await refresh()
      setDraft(null)
      setNotice('Action saved. Nothing was executed.')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not save Action.')
    } finally {
      setSaving(false)
    }
  }

  const remove = async (action: WorkspaceAction) => {
    if (typeof window !== 'undefined' && !window.confirm(`Delete “${action.name}”? This removes the definition but does not run its script.`)) return
    setError('')
    try {
      await deleteWorkspaceAction(workspacePath, action.id)
      await refresh()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not delete Action.')
    }
  }

  const togglePin = async (action: WorkspaceAction) => {
    setError('')
    try {
      await saveWorkspaceAction(workspacePath, { ...action, pinned: !action.pinned }, action.id)
      await refresh()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not update pin.')
    }
  }

  const move = async (index: number, direction: -1 | 1) => {
    const nextIndex = index + direction
    if (nextIndex < 0 || nextIndex >= actions.length) return
    const next = [...actions]
    ;[next[index], next[nextIndex]] = [next[nextIndex], next[index]]
    setActions(next)
    try {
      setActions(await reorderWorkspaceActions(workspacePath, next.map((action) => action.id)))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not reorder Actions.')
      await refresh()
    }
  }

  const runAction = (action: WorkspaceAction) => {
    if (onRun) onRun(action)
    else setSelectedRunAction(action)
  }

  const updateInput = (index: number, patch: Partial<WorkspaceActionInput>) => {
    setDraft((current) => current ? { ...current, inputs: current.inputs.map((input, itemIndex) => itemIndex === index ? { ...input, ...patch } : input) } : current)
  }

  if (!workspaceSlug && !providedWorkspacePath) return <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4 text-sm text-[var(--app-warning)]">Actions are workspace-scoped. Open a workspace, then choose Settings → Actions.</div>

  return (
    <div className={compact ? 'flex flex-col gap-3' : 'flex flex-col gap-6'} data-actions-management-surface={compact ? 'compact' : 'full'}>
      <header>
        <h1 className={compact ? 'text-base font-semibold text-[var(--app-text)]' : 'text-xl font-semibold text-[var(--app-text)]'}>{compact ? 'Manage Workspace Actions' : 'Actions'}</h1>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">Reusable, workspace-scoped script definitions. Saving or editing an Action never runs it.</p>
        <div className={compact ? 'mt-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2' : 'mt-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3'}>
          <div className="text-sm font-medium text-[var(--app-text)]">{workspaceName || 'Workspace'}</div>
          <div className="mt-1 break-all font-mono text-xs text-[var(--app-text-muted)]">{workspacePath || 'Resolving workspace…'}</div>
        </div>
      </header>

      <div className={compact ? 'rounded-lg border border-[var(--app-primary)]/30 bg-[var(--app-surface)] p-3 text-xs text-[var(--app-text-muted)]' : 'rounded-xl border border-[var(--app-primary)]/30 bg-[var(--app-surface)] p-4 text-sm text-[var(--app-text-muted)]'}>
        <div className="flex gap-3"><ShieldCheck className="mt-0.5 shrink-0 text-[var(--app-primary)]" size={18} /><div><strong className="text-[var(--app-text)]">Definitions are not shell commands.</strong> The entrypoint stays inside this workspace and every argument is stored separately. Actions run only after an explicit Run gesture. Secret prompts are masked and are never stored as defaults.</div></div>
      </div>

      {error ? <div role="alert" className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">{error}<button type="button" className="ml-2 font-semibold underline" onClick={() => setRequest((current) => current + 1)}>Retry</button></div> : null}
      {notice ? <div role="status" className="rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-sm text-[var(--app-success)]">{notice}</div> : null}

      <section className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]">
        <div className="flex items-center justify-between gap-3 border-b border-[var(--app-border)] px-4 py-3">
          <div><h2 className="font-semibold text-[var(--app-text)]">Workspace Actions</h2><p className="text-xs text-[var(--app-text-muted)]">Pin favorites and reorder how they appear in Action menus.</p></div>
          <Button onClick={() => beginEdit()} disabled={loading || !workspacePath}><Plus size={15} />New Action</Button>
        </div>
        {loading ? <p className="p-4 text-sm text-[var(--app-text-muted)]">Loading Actions…</p> : actions.length === 0 ? <p className="p-4 text-sm text-[var(--app-text-muted)]">No Actions yet. Create one without writing JSON or shell syntax.</p> : (
          <div className="divide-y divide-[var(--app-border)]">
            {actions.map((action, index) => <div key={action.id} className="flex items-center gap-3 px-4 py-3">
              <GripVertical size={15} className="shrink-0 text-[var(--app-text-muted)]" />
              <WorkspaceActionIcon icon={action.icon} className="shrink-0 text-[var(--app-primary)]" />
              <div className="min-w-0 flex-1"><div className="flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]">{action.name}{action.pinned ? <Pin size={12} className="text-[var(--app-primary)]" /> : null}</div><code className="block truncate text-xs text-[var(--app-text-muted)]">{action.entrypoint}</code></div>
              <div className="flex shrink-0 items-center gap-1">
                <button type="button" aria-label={`Run ${action.name}`} onClick={() => runAction(action)} className="rounded-lg p-2 text-[var(--app-primary)] hover:bg-[var(--app-primary-soft)]"><Play size={14} /></button>
                <button type="button" aria-label={`Move ${action.name} up`} disabled={index === 0} onClick={() => void move(index, -1)} className="rounded-lg p-2 hover:bg-[var(--app-surface-hover)] disabled:opacity-30"><ArrowUp size={14} /></button>
                <button type="button" aria-label={`Move ${action.name} down`} disabled={index === actions.length - 1} onClick={() => void move(index, 1)} className="rounded-lg p-2 hover:bg-[var(--app-surface-hover)] disabled:opacity-30"><ArrowDown size={14} /></button>
                <button type="button" aria-label={`${action.pinned ? 'Unpin' : 'Pin'} ${action.name}`} onClick={() => void togglePin(action)} className="rounded-lg p-2 hover:bg-[var(--app-surface-hover)]"><Pin size={14} /></button>
                <button type="button" aria-label={`Edit ${action.name}`} onClick={() => beginEdit(action)} className="rounded-lg p-2 hover:bg-[var(--app-surface-hover)]"><Pencil size={14} /></button>
                <button type="button" aria-label={`Delete ${action.name}`} onClick={() => void remove(action)} className="rounded-lg p-2 text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)]"><Trash2 size={14} /></button>
              </div>
            </div>)}
          </div>
        )}
      </section>

      {selectedRunAction ? <DesktopWorkspaceActionPanel workspacePath={workspacePath} action={selectedRunAction} autoCloseOnSuccess={false} onClose={() => setSelectedRunAction(null)} /> : null}

      {draft ? <section className="rounded-xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-4 shadow-sm">
        <div className="flex items-center justify-between"><div><h2 className="font-semibold text-[var(--app-text)]">{draft.id ? 'Edit Action' : 'New Action'}</h2><p className="text-xs text-[var(--app-text-muted)]">Build a structured invocation. One line equals one fixed argument.</p></div><button type="button" onClick={() => setDraft(null)} aria-label="Close editor" className="rounded-lg p-2 hover:bg-[var(--app-surface-hover)]"><X size={16} /></button></div>
        <div className="mt-4 grid gap-4 md:grid-cols-2">
          <label className="grid gap-1 text-sm font-medium">Name<Input value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} placeholder="Run checks" /></label>
          <WorkspaceActionIconPicker value={draft.icon} onChange={(icon) => setDraft({ ...draft, icon: normalizeWorkspaceActionIcon(icon) })} disabled={saving} />
          <label className="grid gap-1 text-sm font-medium md:col-span-2">Description<Input value={draft.description} onChange={(event) => setDraft({ ...draft, description: event.target.value })} placeholder="What this Action does" /></label>
          <div className="md:col-span-2">
            <label className="grid gap-1 text-sm font-medium">Workspace script<div className="flex gap-2"><Input readOnly value={draft.entrypoint} placeholder="Choose a file inside the workspace" /><Button variant="outline" onClick={() => void openBrowser()}><FileCode2 size={15} />Browse</Button></div></label>
            {browserPath ? <div className="mt-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] p-2"><div className="mb-2 flex items-center justify-between gap-2 text-xs text-[var(--app-text-muted)]"><span className="truncate">{browserPath}</span>{browserPath !== workspacePath ? <button type="button" onClick={() => void openBrowser(browserPath.replace(/[\\/][^\\/]+$/, ''))} className="font-medium text-[var(--app-primary)]">Up</button> : null}</div><div className="max-h-48 overflow-y-auto">{browsing ? <p className="p-2 text-xs">Loading…</p> : browserEntries.map((entry) => <button key={entry.path} type="button" onClick={() => entry.isDirectory ? void openBrowser(entry.path) : setDraft({ ...draft, entrypoint: relativePath(workspacePath, entry.path) })} className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-[var(--app-surface-hover)]"><FileCode2 size={14} className={entry.isDirectory ? 'text-[var(--app-primary)]' : 'text-[var(--app-text-muted)]'} />{entry.name}{entry.isDirectory ? '/' : ''}</button>)}</div></div> : null}
          </div>
          <label className="grid gap-1 text-sm font-medium md:col-span-2">Fixed arguments (one per line)<textarea value={draft.arguments.join('\n')} onChange={(event) => setDraft({ ...draft, arguments: splitArguments(event.target.value) })} className="min-h-24 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 font-mono text-sm outline-none focus:border-[var(--app-primary)]" placeholder={'--mode\ncheck'} /></label>
          <label className="flex items-center gap-2 text-sm font-medium"><input type="checkbox" checked={draft.pinned} onChange={(event) => setDraft({ ...draft, pinned: event.target.checked })} />Pin in quick Action menus</label>
        </div>

        <div className="mt-5 border-t border-[var(--app-border)] pt-4">
          <div className="flex items-center justify-between"><div><h3 className="text-sm font-semibold">Prompted inputs</h3><p className="text-xs text-[var(--app-text-muted)]">Each prompt appends its value after the separate argument parts you define, such as <code>--token</code> then <code>&lt;API token&gt;</code>.</p></div><Button variant="outline" onClick={() => setDraft({ ...draft, inputs: [...draft.inputs, newInput()] })}><Plus size={14} />Add input</Button></div>
          <div className="mt-3 grid gap-3">{draft.inputs.map((input, index) => <div key={index} className="rounded-xl border border-[var(--app-border)] p-3">
            <div className="grid gap-3 md:grid-cols-2"><label className="grid gap-1 text-xs font-medium">Input ID<Input value={input.id} onChange={(event) => updateInput(index, { id: event.target.value.trim() })} placeholder="token" /></label><label className="grid gap-1 text-xs font-medium">Label<Input value={input.label} onChange={(event) => updateInput(index, { label: event.target.value })} placeholder="API token" /></label><label className="grid gap-1 text-xs font-medium">Kind<Select value={input.kind} onChange={(event) => updateInput(index, { kind: event.target.value === 'secret' ? 'secret' : 'text', ...(event.target.value === 'secret' ? { defaultValue: '' } : {}) })}><option value="text">Text</option><option value="secret">Secret (masked)</option></Select></label><label className="grid gap-1 text-xs font-medium">Placeholder<Input value={input.placeholder} onChange={(event) => updateInput(index, { placeholder: event.target.value })} /></label><label className="grid gap-1 text-xs font-medium md:col-span-2">Arguments before value (one part per line)<textarea value={input.arguments.join('\n')} onChange={(event) => updateInput(index, { arguments: splitArguments(event.target.value) })} className="min-h-20 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 font-mono text-sm" placeholder="--token" /></label><label className="grid gap-1 text-xs font-medium">Default value<Input type={input.kind === 'secret' ? 'password' : 'text'} disabled={input.kind === 'secret'} value={input.defaultValue} onChange={(event) => updateInput(index, { defaultValue: event.target.value })} placeholder={input.kind === 'secret' ? 'Secrets are never saved' : ''} /></label><div className="flex items-end justify-between"><label className="flex h-10 items-center gap-2 text-xs font-medium"><input type="checkbox" checked={input.required} onChange={(event) => updateInput(index, { required: event.target.checked })} />Required</label><button type="button" onClick={() => setDraft({ ...draft, inputs: draft.inputs.filter((_, itemIndex) => itemIndex !== index) })} className="flex h-10 items-center gap-1 rounded-lg px-2 text-xs font-semibold text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)]"><Trash2 size={13} />Remove</button></div></div>
          </div>)}</div>
        </div>

        <div className="mt-5 rounded-lg bg-[var(--app-code-bg,var(--app-bg))] p-3"><div className="text-xs font-semibold text-[var(--app-text-muted)]">Invocation preview</div><code className="mt-1 block overflow-x-auto whitespace-nowrap text-xs text-[var(--app-text)]">{invocationPreview(draft)}</code></div>
        {draftErrors.length ? <ul role="alert" className="mt-3 list-disc space-y-1 pl-5 text-xs text-[var(--app-danger)]">{draftErrors.map((message) => <li key={message}>{message}</li>)}</ul> : null}
        <div className="mt-4 flex items-center justify-between gap-3"><p className="text-xs text-[var(--app-text-muted)]">Save only updates the definition. It cannot execute this script.</p><Button disabled={saving || draftErrors.length > 0} onClick={() => void submit()}>{saving ? 'Saving…' : 'Save Action'}</Button></div>
      </section> : null}
    </div>
  )
}
