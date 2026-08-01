import { AlertTriangle, LoaderCircle, Sparkles } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import type { WorkspaceDefinitionFields } from '../types/workspace'

interface WorkspaceDefinitionStatusProps {
  workspace: Partial<WorkspaceDefinitionFields>
  compact?: boolean
}

export function WorkspaceDefinitionStatus({ workspace, compact = false }: WorkspaceDefinitionStatusProps) {
  if (workspace.definitionStatus === 'pending') {
    return (
      <div
        role="status"
        className="flex items-start gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-xs leading-5 text-[var(--app-text-muted)]"
      >
        <LoaderCircle size={14} className="mt-0.5 shrink-0 animate-spin text-[var(--app-primary)]" />
        <span>
          <strong className="font-medium text-[var(--app-text)]">Router is analyzing this workspace.</strong>
          {!compact ? ' Swarm will refresh this definition when the analysis finishes.' : null}
        </span>
      </div>
    )
  }

  if (workspace.definitionStatus === 'failed') {
    const error = workspace.definitionError || 'Router could not generate a workspace definition after three attempts.'
    const suggestion = workspace.definitionSuggestion || 'Change the Router model in Settings, then add this workspace again.'
    const attempts = workspace.definitionAttempts ?? 0
    return (
      <div
        role="alert"
        className="grid gap-1.5 rounded-lg border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs leading-5 text-[var(--app-danger)]"
      >
        <span className="flex items-start gap-2 font-medium">
          <AlertTriangle size={14} className="mt-0.5 shrink-0" />
          Workspace analysis failed{attempts > 0 ? ` after ${attempts} attempts` : ''}.
        </span>
        <span className="break-words">{error}</span>
        <span className="font-medium">{suggestion}</span>
        <Link to="/settings" className="w-fit font-semibold underline underline-offset-2">
          Change Router model
        </Link>
      </div>
    )
  }

  if (workspace.definitionStatus !== 'completed' || !workspace.definition) {
    return null
  }

  return (
    <details className="group rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-xs text-[var(--app-text-muted)]">
      <summary className="flex cursor-pointer list-none items-center gap-2 font-medium text-[var(--app-text)] marker:hidden">
        <Sparkles size={14} className="shrink-0 text-[var(--app-primary)]" />
        <span>Workspace definition</span>
        <span className="ml-auto text-[10px] uppercase tracking-wide text-[var(--app-text-subtle)] group-open:hidden">Read</span>
        <span className="ml-auto hidden text-[10px] uppercase tracking-wide text-[var(--app-text-subtle)] group-open:inline">Hide</span>
      </summary>
      <p className="mt-2 whitespace-pre-wrap break-words border-t border-[var(--app-border)] pt-2 leading-5 text-[var(--app-text-muted)]">
        {workspace.definition}
      </p>
    </details>
  )
}
