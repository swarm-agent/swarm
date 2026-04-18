import { cn } from '../../../../lib/cn'

export type SwarmSurfaceTab = 'swarm' | 'containers'

interface SwarmSurfaceTabsProps {
  activeTab: SwarmSurfaceTab
  onChange: (tab: SwarmSurfaceTab) => void
  sidebar?: boolean
}

const tabs: Array<{ id: SwarmSurfaceTab; label: string }> = [
  { id: 'swarm', label: 'Swarm' },
  { id: 'containers', label: 'Containers' },
]

export function SwarmSurfaceTabs({ activeTab, onChange, sidebar = false }: SwarmSurfaceTabsProps) {
  return (
    <div
      className={cn(
        sidebar
          ? 'flex flex-col gap-1 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-1'
          : 'inline-flex rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-1',
      )}
      role="tablist"
      aria-label="Swarm sections"
      aria-orientation={sidebar ? 'vertical' : 'horizontal'}
    >
      {tabs.map((tab) => (
        <button
          key={tab.id}
          type="button"
          role="tab"
          aria-selected={activeTab === tab.id}
          onClick={() => onChange(tab.id)}
          className={cn(
            sidebar
              ? 'rounded-xl px-4 py-3 text-left text-sm font-medium transition-all duration-200'
              : 'rounded-full px-5 py-2 text-sm font-medium transition-all duration-200',
            activeTab === tab.id
              ? 'bg-[var(--app-bg)] text-[var(--app-text)] shadow-sm border border-[var(--app-border-strong)]'
              : 'border border-transparent text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]',
          )}
        >
          {tab.label}
        </button>
      ))}
    </div>
  )
}
