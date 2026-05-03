import { useMemo } from 'react'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Film, FolderOpen } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { useDesktopStore } from '../../state/use-desktop-store'

export function VideoToolPage() {
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const workspaceVideoToolMatch = matchRoute({ to: '/$workspaceSlug/tools/video', fuzzy: false })
  const routeWorkspaceSlug = workspaceVideoToolMatch ? workspaceVideoToolMatch.workspaceSlug.trim() : ''
  const activeSessionId = useDesktopStore((state) => state.activeSessionId)

  const handleBack = useMemo(() => {
    if (routeWorkspaceSlug) {
      return () => {
        void navigate({ to: '/$workspaceSlug/tools', params: { workspaceSlug: routeWorkspaceSlug } })
      }
    }
    return () => {
      void navigate({ to: '/tools' })
    }
  }, [navigate, routeWorkspaceSlug])

  const handleBackToWorkspace = useMemo(() => {
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
    return () => {
      void navigate({ to: '/' })
    }
  }, [activeSessionId, navigate, routeWorkspaceSlug])

  return (
    <div className="absolute inset-0 overflow-y-auto bg-[var(--app-bg)] text-[var(--app-text)]">
      <div className="mx-auto flex min-h-full w-full max-w-6xl flex-col px-6 py-6 sm:px-8 sm:py-8">
        <header className="flex flex-col gap-5 border-b border-[var(--app-border)] pb-6 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <Button variant="ghost" className="mb-5 h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={handleBack}>
              <ArrowLeft size={15} />
              Back to tools
            </Button>
            <div className="flex items-center gap-3">
              <span className="grid h-11 w-11 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)] shadow-sm">
                <Film size={20} strokeWidth={1.8} />
              </span>
              <div>
                <p className="text-[11px] font-medium uppercase tracking-[0.28em] text-[var(--app-text-subtle)]">Swarm Tools</p>
                <h1 className="mt-1 text-3xl font-semibold tracking-[-0.055em] text-[var(--app-text)]">Video Tool</h1>
              </div>
            </div>
          </div>
          <Button variant="outline" className="h-10 rounded-xl" onClick={handleBackToWorkspace}>
            {routeWorkspaceSlug ? (activeSessionId ? 'Back to chat' : 'Back to workspace') : 'Back to launcher'}
          </Button>
        </header>

        <main className="grid flex-1 place-items-center py-12">
          <button
            type="button"
            className="flex aspect-square w-full max-w-sm flex-col items-center justify-center border border-dashed border-[var(--app-border)] bg-transparent p-8 text-center transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface)]"
          >
            <FolderOpen size={34} strokeWidth={1.6} className="text-[var(--app-primary)]" />
            <h2 className="mt-5 text-xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">Add a folder with videos in it.</h2>
            <p className="mt-2 text-sm text-[var(--app-text-muted)]">Swarm will make a swarm-video folder and leave the originals untouched.</p>
          </button>
        </main>
      </div>
    </div>
  )
}
