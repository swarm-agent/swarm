import { useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronDown } from 'lucide-react'
import { applyWorkspaceTheme, setWorkspaceThemeCatalog, workspaceThemeDefaultId } from '../../../../workspaces/launcher/services/workspace-theme'
import { saveGlobalThemeSettings } from '../../swarm/mutations/save-global-theme-settings'
import { normalizeGlobalThemeSettings } from '../../swarm/types/swarm-settings'
import { useWorkspaceLauncher } from '../../../../workspaces/launcher/state/use-workspace-launcher'
import { WORKSPACE_THEME_OPTIONS, formatWorkspaceThemeLabel } from '../../../../workspaces/launcher/services/workspace-theme'
import { uiSettingsQueryOptions } from '../../../../queries/query-options'
import type { WorkspaceEntry } from '../../../../workspaces/launcher/types/workspace'
import { resolveWorkspaceBySlug } from '../../../../workspaces/launcher/services/workspace-route'

function describeWorkspaceTheme(themeId: string): string {
  const normalized = themeId.trim().toLowerCase()
  if (normalized === '' || normalized === 'inherit') {
    return 'Inherit (global)'
  }
  return formatWorkspaceThemeLabel(normalized)
}

interface ThemesSettingsPageProps {
  activeWorkspacePath?: string | null
  activeWorkspaceSlug?: string
}

export function ThemesSettingsPage({ activeWorkspacePath, activeWorkspaceSlug = '' }: ThemesSettingsPageProps = {}) {
  const { workspaces, currentWorkspacePath, setWorkspaceTheme } = useWorkspaceLauncher({ applyDocumentTheme: false })
  const routeWorkspacePath = activeWorkspaceSlug.trim()
    ? resolveWorkspaceBySlug(workspaces, activeWorkspaceSlug)?.path ?? null
    : null
  const resolvedActiveWorkspacePath = activeWorkspacePath === undefined
    ? routeWorkspacePath || currentWorkspacePath
    : activeWorkspacePath?.trim() || null
  const [savingPath, setSavingPath] = useState<string | null>(null)
  const [savingGlobalTheme, setSavingGlobalTheme] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [globalThemeId, setGlobalThemeId] = useState(workspaceThemeDefaultId())
  const [globalThemeLabel, setGlobalThemeLabel] = useState('')
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())

  useEffect(() => {
    const settings = uiSettingsQuery.data
    if (!settings) {
      return
    }
    setWorkspaceThemeCatalog(settings.theme)
    const globalTheme = normalizeGlobalThemeSettings(settings)
    setGlobalThemeId(globalTheme.activeId)
    setGlobalThemeLabel(globalTheme.activeLabel)
  }, [uiSettingsQuery.data])

  const workspaceCountLabel = useMemo(() => {
    if (workspaces.length === 1) {
      return '1 saved workspace'
    }
    return `${workspaces.length} saved workspaces`
  }, [workspaces.length])
  const activeWorkspace = useMemo(
    () => workspaces.find((workspace) => workspace.path === resolvedActiveWorkspacePath) ?? null,
    [resolvedActiveWorkspacePath, workspaces],
  )
  const orderedWorkspaces = useMemo(
    () => activeWorkspace
      ? [activeWorkspace, ...workspaces.filter((workspace) => workspace.path !== activeWorkspace.path)]
      : workspaces,
    [activeWorkspace, workspaces],
  )

  const handleGlobalThemeChange = async (newThemeId: string) => {
    setSavingGlobalTheme(true)
    setError(null)
    try {
      const nextSettings = await saveGlobalThemeSettings(newThemeId)
      const normalized = normalizeGlobalThemeSettings(nextSettings)
      setGlobalThemeId(normalized.activeId)
      setGlobalThemeLabel(normalized.activeLabel)
      if (!activeWorkspace?.themeId?.trim()) {
        applyWorkspaceTheme(normalized.activeId)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update global theme')
    } finally {
      setSavingGlobalTheme(false)
    }
  }

  const handleThemeChange = async (workspace: WorkspaceEntry, newThemeId: string) => {
    setSavingPath(workspace.path)
    setError(null)
    try {
      await setWorkspaceTheme(workspace.path, newThemeId)
      if (workspace.path === resolvedActiveWorkspacePath) {
        const normalizedThemeId = newThemeId.trim().toLowerCase()
        applyWorkspaceTheme(normalizedThemeId === '' || normalizedThemeId === 'inherit' ? globalThemeId : normalizedThemeId)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update workspace theme')
    } finally {
      setSavingPath(null)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 flex flex-col gap-4">
        <div>
          <h1 className="text-xl font-semibold text-[var(--app-text)]">Themes</h1>
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">
            Set your <strong>global theme</strong> for the desktop, then optionally let individual workspaces override it.
            If a workspace is set to <strong>Inherit (global)</strong>, it will use the global theme instead.
          </p>
        </div>

        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm text-[var(--app-text-muted)]">
          <div>
            Workspace colors follow the <strong>active Desktop workspace</strong>. The active workspace is highlighted below
            so you can see which theme is controlling this window.
          </div>
          {resolvedActiveWorkspacePath ? (
            <div className="mt-2 min-w-0 text-xs text-[var(--app-text-subtle)]" data-testid="themes-active-workspace">
              Active workspace: <strong className="text-[var(--app-text)]">{activeWorkspace?.workspaceName || resolvedActiveWorkspacePath}</strong>
              {activeWorkspace ? <span className="ml-1 break-all">({activeWorkspace.path})</span> : null}
            </div>
          ) : null}
        </div>

        <p className="text-xs text-[var(--app-text-subtle)]">{workspaceCountLabel}</p>
      </div>

      <div className="flex-1 overflow-y-auto pb-12 pr-2">
        {error ? (
          <div className="mb-6 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">
            {error}
          </div>
        ) : null}

        <div className="grid gap-4">
          <div className="rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] px-4 py-4 shadow-sm">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="flex min-w-0 flex-1 flex-col gap-1">
                <div className="text-sm font-semibold text-[var(--app-text)]">Global theme</div>
                <div className="text-xs text-[var(--app-text-muted)]">
                  This is the default desktop theme. It applies immediately, and any workspace set to inherit will use it.
                </div>
                <div className="text-xs text-[var(--app-text-subtle)]">
                  Current global theme: <strong className="text-[var(--app-text)]">{globalThemeLabel}</strong>
                </div>
              </div>

              <div className="relative w-full shrink-0 sm:w-64">
                <select
                  value={globalThemeId}
                  onChange={(e) => void handleGlobalThemeChange(e.target.value)}
                  disabled={savingGlobalTheme}
                  className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                >
                  {WORKSPACE_THEME_OPTIONS.map((option) => (
                    <option key={option.id} value={option.id}>
                      {option.label}
                    </option>
                  ))}
                </select>
                <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
              </div>
            </div>
          </div>

          {workspaces.length === 0 ? (
            <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-6 py-8 text-center text-sm text-[var(--app-text-muted)]">
              You don't have any saved workspaces yet.
            </div>
          ) : (
            orderedWorkspaces.map((workspace) => {
              const isCurrent = workspace.path === resolvedActiveWorkspacePath
              const busy = savingPath === workspace.path
              const normalizedThemeId = (workspace.themeId || 'inherit').trim().toLowerCase() || 'inherit'
              const effectiveThemeLabel = normalizedThemeId === 'inherit' ? globalThemeLabel : describeWorkspaceTheme(normalizedThemeId)

              return (
                <div
                  key={workspace.path}
                  className={[
                    'rounded-2xl border px-4 py-4 shadow-sm transition-colors',
                    isCurrent
                      ? 'order-first border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface-subtle))]'
                      : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]',
                  ].join(' ')}
                >
                  <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                    <div className="flex min-w-0 flex-1 flex-col gap-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate font-semibold text-[var(--app-text)]">{workspace.workspaceName}</span>
                        {isCurrent ? (
                          <span className="shrink-0 rounded bg-[var(--app-primary)] px-1.5 py-0.5 text-[10px] font-bold uppercase tracking-wider text-[var(--app-primary-text)]">
                            Active
                          </span>
                        ) : null}
                      </div>
                      <span className="truncate text-xs text-[var(--app-text-muted)]">{workspace.path}</span>
                      <span className="mt-1 text-xs text-[var(--app-text-subtle)]">
                        Effective theme: <strong className="text-[var(--app-text)]">{effectiveThemeLabel}</strong>
                        {normalizedThemeId === 'inherit' ? ' (from global theme)' : ' (workspace override)'}
                      </span>
                      <span className="mt-1 text-xs text-[var(--app-text-muted)]">
                        Open this workspace on Desktop to make its theme control the window.
                      </span>
                      {isCurrent ? (
                        <span className="mt-1 text-xs text-[var(--app-warning)]">
                          This workspace is active now, so changing its theme updates the desktop immediately.
                        </span>
                      ) : null}
                    </div>

                    <div className="relative w-full shrink-0 sm:w-56">
                      <select
                        value={normalizedThemeId}
                        onChange={(e) => void handleThemeChange(workspace, e.target.value)}
                        disabled={busy}
                        className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                      >
                        <option value="inherit">Inherit (global)</option>
                        {WORKSPACE_THEME_OPTIONS.map((option) => (
                          <option key={option.id} value={option.id}>
                            {option.label}
                          </option>
                        ))}
                      </select>
                      <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
                    </div>
                  </div>
                </div>
              )
            })
          )}
        </div>
      </div>
    </div>
  )
}
