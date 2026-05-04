import { useMemo } from 'react'
import { ArrowRight, ExternalLink, Image, LoaderCircle, Sparkles, X } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { Button } from '../../../../components/ui/button'
import { buildWorkspaceRouteSlugMap, workspaceRouteSlugBase } from '../../../workspaces/launcher/services/workspace-route'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import { fetchImageThread, imageAssetURL, type ImageAsset, type ImageThreadRecord } from '../services/image-threads'

export type ImageSessionSidebarState = {
  open: boolean
  threadId: string
  workspacePath?: string
  workspaceName?: string
  title?: string
  provider?: string
  model?: string
  requestedCount?: number
  savedCount?: number
  status?: string
}

type ImageSessionSidebarProps = {
  state: ImageSessionSidebarState | null
  onClose: () => void
}

function formatProviderModel(provider?: string, model?: string): string {
  const parts = [provider, model].map((value) => value?.trim()).filter(Boolean)
  return parts.length > 0 ? parts.join(' / ') : 'model pending'
}

function imageCountLabel(state: ImageSessionSidebarState, thread: ImageThreadRecord | null): string {
  const saved = typeof state.savedCount === 'number' && state.savedCount >= 0
    ? state.savedCount
    : thread?.imageAssets.length ?? 0
  const requested = typeof state.requestedCount === 'number' && state.requestedCount > 0 ? state.requestedCount : saved
  return requested > 0 ? `${saved} / ${requested} saved` : `${saved} saved`
}

function assetURL(threadId: string, asset: ImageAsset): string {
  return asset.url ?? imageAssetURL(threadId, asset.id)
}

function imageToolPath(thread: ImageThreadRecord | null, state: ImageSessionSidebarState, workspaceSlug: string): string {
  const threadId = thread?.id || state.threadId
  if (!threadId) {
    return '/tools/image'
  }
  return workspaceSlug ? `/${workspaceSlug}/tools/image/${threadId}` : `/tools/image/${threadId}`
}

export function ImageSessionSidebar({ state, onClose }: ImageSessionSidebarProps) {
  const navigate = useNavigate()
  const open = Boolean(state?.open && state.threadId.trim())
  const threadId = state?.threadId.trim() ?? ''
  const threadQuery = useQuery({
    queryKey: ['image-tool-thread', threadId],
    queryFn: () => fetchImageThread(threadId),
    enabled: open,
    staleTime: 2_000,
    refetchInterval: open && state?.status !== 'completed' ? 2_500 : false,
  })
  const workspacesQuery = useQuery({
    queryKey: ['image-session-sidebar-workspaces'],
    queryFn: () => listWorkspaces(200),
    staleTime: 30_000,
    enabled: open,
  })
  const thread = threadQuery.data ?? null
  const workspaceSlug = useMemo(() => {
    const workspacePath = thread?.workspacePath || state?.workspacePath || ''
    if (!workspacePath) {
      return ''
    }
    const workspaces = workspacesQuery.data ?? []
    const slugByPath = buildWorkspaceRouteSlugMap(workspaces)
    const slug = slugByPath.get(workspacePath)
    if (slug) {
      return slug
    }
    return workspaceRouteSlugBase({ path: workspacePath, workspaceName: thread?.workspaceName || state?.workspaceName || '' })
  }, [state?.workspaceName, state?.workspacePath, thread?.workspaceName, thread?.workspacePath, workspacesQuery.data])

  if (!open || !state) {
    return null
  }

  const orderedAssets = thread
    ? [
        ...thread.imageAssetOrder
          .map((assetId) => thread.imageAssets.find((asset) => asset.id === assetId))
          .filter((asset): asset is ImageAsset => Boolean(asset)),
        ...thread.imageAssets.filter((asset) => !thread.imageAssetOrder.includes(asset.id)),
      ]
    : []
  const title = thread?.title || state.title || 'Image session'
  const workspaceLabel = thread?.workspaceName || state.workspaceName || thread?.workspacePath || state.workspacePath || 'workspace'
  const routePath = imageToolPath(thread, state, workspaceSlug)
  const status = state.status?.trim() || (threadQuery.isLoading ? 'loading' : 'ready')

  return (
    <aside className="hidden w-[360px] shrink-0 flex-col border-l border-[var(--app-border)] bg-[var(--app-surface)] lg:flex">
      <div className="flex min-h-[60px] items-center justify-between gap-3 border-b border-[var(--app-border)] px-4">
        <div className="min-w-0">
          <p className="text-[10px] font-bold uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">Media session</p>
          <h2 className="mt-1 truncate text-sm font-semibold text-[var(--app-text)]" title={title}>{title}</h2>
        </div>
        <button
          type="button"
          onClick={onClose}
          className="grid h-9 w-9 shrink-0 place-items-center rounded-xl border border-[var(--app-border)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
          aria-label="Close image sidebar"
        >
          <X size={16} />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
          <div className="flex items-start gap-3">
            <div className="grid h-10 w-10 shrink-0 place-items-center rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)]">
              {threadQuery.isLoading ? <LoaderCircle className="animate-spin" size={18} /> : <Image size={18} />}
            </div>
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-semibold text-[var(--app-text)]">{workspaceLabel}</div>
              <div className="mt-1 truncate text-xs text-[var(--app-text-muted)]">{formatProviderModel(state.provider, state.model)}</div>
              <div className="mt-2 flex flex-wrap gap-2 text-[11px] text-[var(--app-text-muted)]">
                <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1">{status}</span>
                <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1">{imageCountLabel(state, thread)}</span>
              </div>
            </div>
          </div>
          <div className="mt-3 grid grid-cols-2 gap-2">
            <Button
              variant="outline"
              className="h-9 rounded-xl text-xs"
              onClick={() => void navigate({ to: routePath })}
            >
              <ArrowRight size={13} />Open session
            </Button>
            <Button
              variant="outline"
              className="h-9 rounded-xl text-xs"
              onClick={() => window.open(routePath, '_blank', 'noopener,noreferrer')}
            >
              <ExternalLink size={13} />New tab
            </Button>
          </div>
        </div>

        <div className="mt-4">
          <div className="mb-2 flex items-center justify-between text-[10px] font-bold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">
            <span>Generated assets</span>
            <span>{orderedAssets.length}</span>
          </div>
          {threadQuery.isError ? (
            <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">
              {threadQuery.error instanceof Error ? threadQuery.error.message : 'Unable to load image session'}
            </div>
          ) : orderedAssets.length > 0 ? (
            <div className="grid grid-cols-2 gap-2">
              {orderedAssets.slice(0, 8).map((asset, index) => (
                <a
                  key={asset.id}
                  href={assetURL(thread?.id || state.threadId, asset)}
                  target="_blank"
                  rel="noreferrer"
                  className="group rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-2 hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)]"
                >
                  <div className="grid aspect-square place-items-center overflow-hidden rounded-lg bg-[var(--app-surface)]">
                    <img src={assetURL(thread?.id || state.threadId, asset)} alt={asset.name} className="h-full w-full object-contain transition group-hover:scale-[1.02]" />
                  </div>
                  <p className="mt-2 truncate text-xs font-medium text-[var(--app-text)]">{index + 1}. {asset.name}</p>
                </a>
              ))}
            </div>
          ) : (
            <div className="rounded-xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-8 text-center text-sm text-[var(--app-text-muted)]">
              <Sparkles className="mx-auto mb-3 text-[var(--app-primary)]" size={28} />
              Waiting for generated images to be saved.
            </div>
          )}
        </div>
      </div>
    </aside>
  )
}
