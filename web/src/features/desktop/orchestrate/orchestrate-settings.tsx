import { useState } from 'react'
import { Button } from '../../../components/ui/button'
import { MemoryModal } from '../memory/memory-page'
import { Link, useRouterState } from '@tanstack/react-router'
import { AuthSettingsPage } from '../settings/auth/components/auth-settings-page'
import { PermissionsSettingsPage } from '../settings/permissions/components/permissions-settings-page'
import { VaultSettingsPage } from '../settings/vault/components/vault-settings-page'
import { ThemesSettingsPage } from '../settings/themes/components/themes-settings-page'
import { NotificationsSettingsPage } from '../settings/notifications/components/notifications-settings-page'
import { MediaSettingsPage } from '../settings/media/components/media-settings-page'
import { swarmPageLink } from './swarm-navigation'

const sections = ['providers', 'permissions', 'vault', 'appearance', 'notifications', 'media'] as const
export function OrchestrateSettings({ workspaceSlug }: { workspaceSlug?: string }) {
  const [memoryOpen, setMemoryOpen] = useState(false)
  const hash = useRouterState({ select: (state) => state.location.hash })
  const selected = sections.find((section) => section === hash) ?? 'providers'
  return <section className="min-h-0 min-w-0 overflow-y-auto space-y-5 p-4 text-[var(--app-text)]">
    {memoryOpen && <MemoryModal onClose={() => setMemoryOpen(false)} />}
    <header><h1 className="text-xl font-semibold">Settings</h1><p className="text-sm text-[var(--app-text-muted)]">Provider authentication, safety and preferences are account-wide unless a control explicitly labels a narrower scope. Models are configured in Agents; Project Charter is project-scoped.</p></header>
    <div className="flex flex-wrap gap-2"><Link {...swarmPageLink(workspaceSlug, 'agents')} className="rounded-lg border border-[var(--app-border)] px-3 py-2">Agents & models</Link><Link {...swarmPageLink(workspaceSlug, 'charter')} className="rounded-lg border border-[var(--app-border)] px-3 py-2">Project Charter</Link><Button variant="outline" aria-haspopup="dialog" onClick={() => setMemoryOpen(true)}>Memory</Button></div>
    <nav aria-label="Settings sections" className="swarm-settings-nav">
      {sections.map((section) => <Link key={section} {...swarmPageLink(workspaceSlug, 'settings')} hash={section} activeOptions={{ includeHash: true }} aria-current={selected === section ? 'page' : undefined} className="swarm-settings-section capitalize">{section}</Link>)}
    </nav>
    <div className="min-w-0">
      {selected === 'providers' && <AuthSettingsPage />}
      {selected === 'permissions' && <PermissionsSettingsPage orchestrate />}
      {selected === 'vault' && <VaultSettingsPage />}
      {selected === 'appearance' && <ThemesSettingsPage activeWorkspaceSlug={workspaceSlug} />}
      {selected === 'notifications' && <NotificationsSettingsPage />}
      {selected === 'media' && <MediaSettingsPage workspaceSlug={workspaceSlug} />}
    </div>
  </section>
}
