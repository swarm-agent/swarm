import { useMemo } from 'react'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Film, FolderOpen, ListVideo, Music2, Play } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { useDesktopStore } from '../../state/use-desktop-store'

type ClipMock = {
  id: string
  label: string
  name: string
  range: string
  duration: string
  width: string
}

const orderedClips: ClipMock[] = [
  { id: 'clip-1', label: '01', name: 'Intro shot', range: '00:00–00:08', duration: '8s', width: '24%' },
  { id: 'clip-2', label: '02', name: 'Main clip', range: '00:08–00:28', duration: '20s', width: '42%' },
  { id: 'clip-3', label: '03', name: 'Cutaway', range: '00:28–00:38', duration: '10s', width: '27%' },
]

const waveformBars = [28, 44, 36, 58, 42, 68, 52, 74, 46, 62, 38, 55, 70, 48, 32, 60, 45, 66, 40, 54, 34, 50, 64, 42]

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
      <div className="mx-auto flex min-h-full w-full max-w-7xl flex-col px-6 py-6 sm:px-8 sm:py-8">
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

        <main className="flex flex-1 flex-col gap-8 py-6">
          <section className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_280px]">
            <div className="min-w-0">
              <div className="mb-3 flex items-center justify-between gap-3">
                <h2 className="text-sm font-semibold text-[var(--app-text)]">Preview</h2>
                <Button variant="ghost" className="h-8 rounded-xl px-2 text-xs text-[var(--app-text-muted)]">
                  <FolderOpen size={14} />
                  Choose folder
                </Button>
              </div>

              <div className="relative grid aspect-video min-h-[340px] place-items-center overflow-hidden border border-[var(--app-border)] bg-[linear-gradient(135deg,color-mix(in_srgb,var(--app-surface)_88%,black),color-mix(in_srgb,var(--app-bg)_92%,black))]">
                <div className="absolute left-4 top-4 text-xs text-white/55">16:9 · draft preview</div>
                <div className="text-center">
                  <Film className="mx-auto text-white/45" size={42} strokeWidth={1.5} />
                  <p className="mt-3 text-sm font-medium text-white/80">Video sits here</p>
                </div>
                <div className="absolute bottom-0 left-0 right-0 flex items-center gap-3 border-t border-white/10 bg-black/35 px-4 py-3 text-white">
                  <button className="grid h-8 w-8 place-items-center rounded-full bg-white text-black" type="button" aria-label="Play preview">
                    <Play size={14} fill="currentColor" />
                  </button>
                  <div className="h-1 flex-1 bg-white/20">
                    <div className="h-full w-[38%] bg-white" />
                  </div>
                  <span className="text-xs tabular-nums text-white/65">00:14 / 00:38</span>
                </div>
              </div>
            </div>

            <aside className="min-w-0 border-t border-[var(--app-border)] pt-5 xl:border-l xl:border-t-0 xl:pl-6 xl:pt-0">
              <div className="mb-4 flex items-center gap-2">
                <ListVideo size={16} className="text-[var(--app-primary)]" />
                <h2 className="text-sm font-semibold text-[var(--app-text)]">Clip order</h2>
              </div>
              <div className="divide-y divide-[var(--app-border)]">
                {orderedClips.map((clip) => (
                  <div key={clip.id} className="flex items-center gap-3 py-3">
                    <span className="w-8 shrink-0 text-xs font-semibold text-[var(--app-primary)]">{clip.label}</span>
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm text-[var(--app-text)]">{clip.name}</p>
                      <p className="mt-1 text-xs text-[var(--app-text-muted)]">{clip.range} · {clip.duration}</p>
                    </div>
                  </div>
                ))}
              </div>
            </aside>
          </section>

          <section>
            <div className="mb-3 flex items-center justify-between gap-3">
              <h2 className="text-sm font-semibold text-[var(--app-text)]">Timeline</h2>
              <span className="text-xs text-[var(--app-text-muted)]">00:00 — 00:40</span>
            </div>

            <div className="border-y border-[var(--app-border)] py-4">
              <div className="mb-3 grid grid-cols-5 text-[10px] uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">
                <span>00:00</span>
                <span>00:10</span>
                <span>00:20</span>
                <span>00:30</span>
                <span className="text-right">00:40</span>
              </div>

              <div className="space-y-2 overflow-x-auto pb-2">
                <div className="flex min-w-[720px] gap-2">
                  {orderedClips.map((clip) => (
                    <div
                      key={clip.id}
                      className="min-w-[120px] border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-3"
                      style={{ width: clip.width }}
                    >
                      <div className="flex items-center justify-between gap-2 text-[11px] text-[var(--app-text-subtle)]">
                        <span>{clip.label}</span>
                        <span>{clip.duration}</span>
                      </div>
                      <p className="mt-3 truncate text-sm font-medium text-[var(--app-text)]">{clip.name}</p>
                      <p className="mt-1 text-xs text-[var(--app-text-muted)]">{clip.range}</p>
                    </div>
                  ))}
                </div>

                <div className="relative min-w-[720px] border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-3">
                  <div className="mb-2 flex items-center gap-2 text-xs text-[var(--app-text-muted)]">
                    <Music2 size={14} className="text-[var(--app-primary)]" />
                    Sound clip · 00:04–00:38
                  </div>
                  <div className="ml-[8%] flex h-10 w-[84%] items-center gap-1 bg-[color-mix(in_srgb,var(--app-primary)_10%,var(--app-surface))] px-2">
                    {waveformBars.map((height, index) => (
                      <span
                        key={`${height}-${index}`}
                        className="w-full bg-[color-mix(in_srgb,var(--app-primary)_42%,var(--app-border))]"
                        style={{ height: `${height}%` }}
                      />
                    ))}
                  </div>
                </div>
              </div>
            </div>
          </section>
        </main>
      </div>
    </div>
  )
}
