import { useEffect, useMemo, useRef, useState } from 'react'
import { Bot, Key, Palette, Shield, GitBranch, type LucideIcon } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { AgentsSettingsPage } from '../agents/components/agents-settings-page'
import { AuthSettingsPage } from '../auth/components/auth-settings-page'
import { PermissionsSettingsPage } from '../permissions/components/permissions-settings-page'
import { ThemesSettingsPage } from '../themes/components/themes-settings-page'
import { VaultSettingsPage } from '../vault/components/vault-settings-page'
import { WorktreeSettingsPage } from '../worktrees/components/worktree-settings-page'
import { cn } from '../../../../lib/cn'
import type { SettingsTabID } from '../types/settings-tabs'

interface DesktopSettingsModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onOpenSwarmDashboard: () => void
  initialTab?: SettingsTabID
}

const tabs: Array<{ id: SettingsTabID; label: string; icon: LucideIcon }> = [
  { id: 'agents', label: 'Agents', icon: Bot },
  { id: 'auth', label: 'Auth', icon: Key },
  { id: 'permissions', label: 'Permissions', icon: Shield },
  { id: 'swarm', label: 'Swarm', icon: Shield },
  { id: 'themes', label: 'Themes', icon: Palette },
  { id: 'vault', label: 'Vault', icon: Shield },
  { id: 'worktrees', label: 'Worktrees', icon: GitBranch },
]

tabs.sort((left, right) => left.label.localeCompare(right.label))

export function DesktopSettingsModal({ open, onOpenChange, onOpenSwarmDashboard, initialTab = 'agents' }: DesktopSettingsModalProps) {
  const [activeTab, setActiveTab] = useState<SettingsTabID>(initialTab === 'swarm' ? 'agents' : initialTab)
  const swarmRedirectedRef = useRef(false)

  useEffect(() => {
    if (!open) {
      return
    }

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onOpenChange(false)
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => {
      window.removeEventListener('keydown', handleKeyDown)
    }
  }, [open, onOpenChange])

  useEffect(() => {
    if (!open) {
      swarmRedirectedRef.current = false
      return
    }
    if (initialTab === 'swarm') {
      if (swarmRedirectedRef.current) {
        return
      }
      swarmRedirectedRef.current = true
      onOpenChange(false)
      onOpenSwarmDashboard()
      return
    }
    setActiveTab(initialTab)
  }, [initialTab, onOpenChange, onOpenSwarmDashboard, open])

  const agentsPageKey = useMemo(() => (open && activeTab === 'agents' ? `agents-${Date.now()}` : 'agents-closed'), [activeTab, open])

  if (!open) {
    return null
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Settings">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="w-full max-w-[960px] h-[720px] overflow-hidden border-0 bg-transparent p-0 shadow-none">
        <div className="flex h-full overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] shadow-[var(--shadow-panel)] transition-colors duration-300">
          
          {/* Left Sidebar */}
          <div className="flex w-[240px] shrink-0 flex-col border-r border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-6">
            <div className="mb-6 px-2 flex items-center justify-between">
              <h2 className="text-sm font-semibold tracking-tight text-[var(--app-text)]">Settings</h2>
            </div>
            
            <nav className="flex flex-col gap-1" role="tablist" aria-label="Settings tabs">
              {tabs.map((tab) => {
                const Icon = tab.icon
                return (
                  <button
                    key={tab.id}
                    type="button"
                    role="tab"
                    aria-selected={activeTab === tab.id}
                    className={cn(
                      'flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-all text-left',
                      activeTab === tab.id
                        ? 'bg-[var(--app-surface-elevated)] text-[var(--app-text)] shadow-sm ring-1 ring-[var(--app-border)]'
                        : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
                    )}
                    onClick={() => {
                      if (tab.id === 'swarm') {
                        onOpenChange(false)
                        onOpenSwarmDashboard()
                        return
                      }
                      setActiveTab(tab.id)
                    }}
                  >
                    <Icon size={16} className="shrink-0" />
                    <span>{tab.label}</span>
                  </button>
                )
              })}
            </nav>
          </div>

          {/* Main Content Area */}
          <div className="flex-1 flex flex-col relative overflow-hidden bg-[var(--app-bg)] transition-colors duration-300">
            <div className="absolute right-5 top-5 z-20">
              <ModalCloseButton className="shadow-[var(--shadow-panel)]" onClick={() => onOpenChange(false)} aria-label="Close settings" />
            </div>
            
            <div className="flex-1 overflow-y-auto px-10 pb-10 pt-20" role="tabpanel">
              <div className="max-w-3xl mx-auto h-full">
                {activeTab === 'agents' ? <AgentsSettingsPage key={agentsPageKey} /> : null}
                {activeTab === 'auth' ? <AuthSettingsPage /> : null}
                {activeTab === 'permissions' ? <PermissionsSettingsPage /> : null}
                {activeTab === 'themes' ? <ThemesSettingsPage /> : null}
                {activeTab === 'vault' ? <VaultSettingsPage /> : null}
                {activeTab === 'worktrees' ? <WorktreeSettingsPage /> : null}
              </div>
            </div>
          </div>

        </div>
      </DialogPanel>
    </Dialog>
  )
}
