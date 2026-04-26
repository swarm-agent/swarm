import { useEffect, useMemo, useState } from 'react'
import { useMatchRoute, useNavigate, useSearch } from '@tanstack/react-router'
import { Bot, GitBranch, Home, Key, Palette, Shield, type LucideIcon } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { AgentsSettingsPage } from '../agents/components/agents-settings-page'
import { AuthSettingsPage } from '../auth/components/auth-settings-page'
import { PermissionsSettingsPage } from '../permissions/components/permissions-settings-page'
import { ThemesSettingsPage } from '../themes/components/themes-settings-page'
import { VaultSettingsPage } from '../vault/components/vault-settings-page'
import { WorktreeSettingsPage } from '../worktrees/components/worktree-settings-page'
import { DesktopSwarmDashboard } from '../../swarm/desktop-swarm-dashboard'
import { cn } from '../../../../lib/cn'
import { normalizeSettingsTabID, type SettingsTabID } from '../types/settings-tabs'
import { useDesktopStore } from '../../state/use-desktop-store'

const settingsTabs: Array<{ id: SettingsTabID; label: string; icon: LucideIcon }> = [
  { id: 'agents', label: 'Agents', icon: Bot },
  { id: 'auth', label: 'Auth', icon: Key },
  { id: 'permissions', label: 'Permissions', icon: Shield },
  { id: 'swarm', label: 'Swarm', icon: Shield },
  { id: 'themes', label: 'Themes', icon: Palette },
  { id: 'vault', label: 'Vault', icon: Shield },
  { id: 'worktrees', label: 'Worktrees', icon: GitBranch },
]

const tabs = [...settingsTabs].sort((left, right) => left.label.localeCompare(right.label))

interface SettingsSearchParams {
  tab?: unknown
}

export function DesktopSettingsPage() {
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const settingsRouteMatch = matchRoute({ to: '/settings', fuzzy: false })
  const workspaceSettingsMatch = matchRoute({ to: '/$workspaceSlug/settings', fuzzy: false })
  const routeWorkspaceSlug = workspaceSettingsMatch ? workspaceSettingsMatch.workspaceSlug.trim() : ''
  const activeSessionId = useDesktopStore((state) => state.activeSessionId)
  const search = useSearch({ strict: false }) as SettingsSearchParams
  const [activeTab, setActiveTab] = useState<SettingsTabID>(() => normalizeSettingsTabID(search.tab))

  useEffect(() => {
    setActiveTab(normalizeSettingsTabID(search.tab))
  }, [search.tab])

  const agentsPageKey = useMemo(() => (activeTab === 'agents' ? `agents-${Date.now()}` : 'agents-closed'), [activeTab])

  const handleBack = useMemo(() => {
    if (routeWorkspaceSlug) {
      if (activeSessionId) {
        return () => {
          void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug: routeWorkspaceSlug, sessionId: activeSessionId } })
        }
      }
      return () => {
        void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug } })
      }
    }
    if (settingsRouteMatch) {
      return () => {
        void navigate({ to: '/' })
      }
    }
    return () => {
      void navigate({ to: '/' })
    }
  }, [activeSessionId, navigate, routeWorkspaceSlug, settingsRouteMatch])

  const handleTabChange = (tab: SettingsTabID) => {
    setActiveTab(tab)
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/settings', params: { workspaceSlug: routeWorkspaceSlug }, search: { tab } })
      return
    }
    void navigate({ to: '/settings', search: { tab } })
  }

  return (
    <div className="absolute inset-0 flex overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]">
      <aside className="flex w-[240px] shrink-0 flex-col border-r border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-5">
        <Button variant="outline" className="h-11 justify-start rounded-xl" onClick={handleBack}>
          <Home size={16} />
          {routeWorkspaceSlug ? (activeSessionId ? 'Back to chat' : 'Back to workspace') : 'Back to launcher'}
        </Button>

        <div className="mt-6 px-2">
          <h1 className="text-sm font-semibold tracking-tight text-[var(--app-text)]">Settings</h1>
          <p className="mt-1 text-xs text-[var(--app-text-muted)]">Desktop preferences in one place.</p>
        </div>

        <nav className="mt-5 flex flex-col gap-1" role="tablist" aria-label="Settings sections" aria-orientation="vertical">
          {tabs.map((tab) => {
            const Icon = tab.icon
            return (
              <button
                key={tab.id}
                type="button"
                role="tab"
                aria-selected={activeTab === tab.id}
                className={cn(
                  'flex items-center gap-3 rounded-xl px-4 py-3 text-left text-sm font-medium transition-all duration-200',
                  activeTab === tab.id
                    ? 'border border-[var(--app-border-strong)] bg-[var(--app-bg)] text-[var(--app-text)] shadow-sm'
                    : 'border border-transparent text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
                )}
                onClick={() => handleTabChange(tab.id)}
              >
                <Icon size={16} className="shrink-0" />
                <span>{tab.label}</span>
              </button>
            )
          })}
        </nav>
      </aside>

      <main className="min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex min-h-full w-full max-w-6xl flex-col px-6 py-8">
          <div className="w-full max-w-4xl">
            {activeTab === 'agents' ? <AgentsSettingsPage key={agentsPageKey} /> : null}
            {activeTab === 'auth' ? <AuthSettingsPage /> : null}
            {activeTab === 'permissions' ? <PermissionsSettingsPage /> : null}
            {activeTab === 'swarm' ? <DesktopSwarmDashboard /> : null}
            {activeTab === 'themes' ? <ThemesSettingsPage /> : null}
            {activeTab === 'vault' ? <VaultSettingsPage /> : null}
            {activeTab === 'worktrees' ? <WorktreeSettingsPage /> : null}
          </div>
        </div>
      </main>
    </div>
  )
}
