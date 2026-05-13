import { useEffect, useMemo, useState } from 'react'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Film, Image, Sparkles } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { cn } from '../../../../lib/cn'
import { useDesktopStore } from '../../state/use-desktop-store'
import { fetchSwarmTopologySnapshot, type SwarmTopologySnapshotResponse } from '../../swarm/api/swarm-topology'

type ToolCard = {
  id: string
  name: string
  description: string
  status: string
  icon: 'video' | 'image'
}

type ToolSummary = {
  label: string
  value: number
}

const toolCards: ToolCard[] = [
  {
    id: 'video',
    name: 'Video Tool',
    description: 'Organize clips and drafts with DB-backed sessions; tool files live in Swarm’s private app-managed workspace bucket.',
    status: 'First tool',
    icon: 'video',
  },
  {
    id: 'image',
    name: 'Image Tool',
    description: 'Create DB-backed image sessions with generated assets stored in Swarm’s private app-managed workspace bucket.',
    status: 'Outline',
    icon: 'image',
  },
]

export function SwarmToolsPage() {
  const navigate = useNavigate()
  const [topology, setTopology] = useState<SwarmTopologySnapshotResponse | null>(null)
  const [topologyError, setTopologyError] = useState('')
  const matchRoute = useMatchRoute()
  const toolsRouteMatch = matchRoute({ to: '/tools', fuzzy: false })
  const workspaceToolsRouteMatch = matchRoute({ to: '/$workspaceSlug/tools', fuzzy: false })
  const routeWorkspaceSlug = workspaceToolsRouteMatch ? workspaceToolsRouteMatch.workspaceSlug.trim() : ''
  const activeSessionId = useDesktopStore((state) => state.activeSessionId)

  const backLabel = routeWorkspaceSlug ? (activeSessionId ? 'Back to chat' : 'Back to workspace') : 'Back to launcher'
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
    if (toolsRouteMatch) {
      return () => {
        void navigate({ to: '/' })
      }
    }
    return () => {
      void navigate({ to: '/' })
    }
  }, [activeSessionId, navigate, routeWorkspaceSlug, toolsRouteMatch])

  useEffect(() => {
    let cancelled = false
    void fetchSwarmTopologySnapshot()
      .then((response) => {
        if (cancelled) return
        setTopology(response)
        setTopologyError('')
      })
      .catch((error) => {
        if (cancelled) return
        setTopology(null)
        setTopologyError(error instanceof Error ? error.message : String(error))
      })
    return () => {
      cancelled = true
    }
  }, [])

  const topologySummary = useMemo<ToolSummary[]>(() => {
    const snapshot = topology
    if (!snapshot) return []
    return [
      { label: 'Runtimes', value: snapshot.migration_status.runtime_count },
      { label: 'Host containers', value: snapshot.migration_status.host_container_count },
      { label: 'Attachments', value: snapshot.migration_status.attachment_count },
      { label: 'Bindings', value: snapshot.migration_status.workspace_binding_count },
      { label: 'Session routes', value: snapshot.migration_status.session_route_count },
    ]
  }, [topology])

  const openTool = (toolID: string) => {
    if (toolID !== 'video' && toolID !== 'image') return
    if (routeWorkspaceSlug) {
      void navigate({ to: toolID === 'video' ? '/$workspaceSlug/tools/video' : '/$workspaceSlug/tools/image', params: { workspaceSlug: routeWorkspaceSlug } })
      return
    }
    void navigate({ to: toolID === 'video' ? '/tools/video' : '/tools/image' })
  }

  return (
    <div className="absolute inset-0 overflow-y-auto bg-[var(--app-bg)] text-[var(--app-text)]">
      <div className="mx-auto flex min-h-full w-full max-w-6xl flex-col px-6 py-6 sm:px-8 sm:py-8">
        <header className="flex flex-col gap-5 border-b border-[var(--app-border)] pb-6 sm:flex-row sm:items-end sm:justify-between">
          <div className="min-w-0">
            <Button variant="ghost" className="mb-5 h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={handleBack}>
              <ArrowLeft size={15} />
              {backLabel}
            </Button>
            <div className="flex items-center gap-3">
              <span className="grid h-11 w-11 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)] shadow-sm">
                <Sparkles size={20} strokeWidth={1.8} />
              </span>
              <div className="min-w-0">
                <p className="text-[11px] font-medium uppercase tracking-[0.28em] text-[var(--app-text-subtle)]">Swarm Tools</p>
                <h1 className="mt-1 text-3xl font-semibold tracking-[-0.055em] text-[var(--app-text)]">Tools</h1>
              </div>
            </div>
          </div>
          <p className="max-w-xl text-sm leading-6 text-[var(--app-text-muted)]">
            Small, focused helpers with private per-workspace app storage outside the repository, separate from main chat history.
          </p>
        </header>

        <main className="flex-1 py-8">
          <section className="mb-8 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5">
            <div className="flex flex-wrap items-start justify-between gap-4">
              <div>
                <p className="text-[11px] font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">Checkpoint 1 topology</p>
                <h2 className="mt-1 text-lg font-semibold tracking-[-0.04em] text-[var(--app-text)]">Canonical topology snapshot</h2>
                <p className="mt-2 max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">
                  New backend topology records now project the current runtime, host-container, attachment, binding, and session-route graph.
                </p>
              </div>
              {topology?.migration_status ? (
                <div className="rounded-full border border-[var(--app-border)] px-3 py-1 text-[11px] font-medium text-[var(--app-text-muted)]">
                  {topology.migration_status.version}
                </div>
              ) : null}
            </div>
            {topologyError ? (
              <p className="mt-4 text-sm text-[var(--app-danger)]">{topologyError}</p>
            ) : (
              <div className="mt-5 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
                {topologySummary.map((item) => (
                  <Card key={item.label} className="border-[var(--app-border)] bg-[var(--app-bg)] p-4">
                    <p className="text-[11px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">{item.label}</p>
                    <p className="mt-2 text-2xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">{item.value}</p>
                  </Card>
                ))}
              </div>
            )}
          </section>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
            {toolCards.map((tool) => (
              <button
                key={tool.id}
                type="button"
                onClick={() => openTool(tool.id)}
                className="group text-left"
                aria-label={`Open ${tool.name}`}
              >
                <Card className={cn(
                  'flex aspect-square min-h-[210px] flex-col overflow-hidden p-5 transition-all duration-200',
                  'hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:shadow-[var(--shadow-panel)]',
                )}
                >
                  <div className="flex items-start justify-between gap-3">
                    <span className="grid h-12 w-12 place-items-center rounded-2xl border border-[color-mix(in_srgb,var(--app-primary)_38%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-primary)_12%,transparent)] text-[var(--app-primary)]">
                      {tool.icon === 'video' ? <Film size={22} strokeWidth={1.8} /> : <Image size={22} strokeWidth={1.8} />}
                    </span>
                    <span className="rounded-full border border-[var(--app-border)] px-2 py-1 text-[10px] font-medium uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">
                      {tool.status}
                    </span>
                  </div>
                  <div className="mt-auto">
                    <h2 className="text-xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">{tool.name}</h2>
                    <p className="mt-2 line-clamp-3 text-sm leading-5 text-[var(--app-text-muted)]">{tool.description}</p>
                    <p className="mt-4 text-xs font-medium text-[var(--app-primary)] opacity-80 transition-opacity group-hover:opacity-100">Open tool →</p>
                  </div>
                </Card>
              </button>
            ))}
          </div>
        </main>
      </div>
    </div>
  )
}
