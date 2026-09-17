import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate, useParams } from '@tanstack/react-router'
import {
  Activity,
  ArrowLeft,
  Coins,
  Download,
  Filter,
  Loader2,
  RefreshCw,
} from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { fetchSessionUsageDashboard } from '../services/usage-api'
import { UsageChartTokens } from '../components/usage-chart-tokens'
import { UsageProviderCards } from '../components/usage-provider-cards'
import { UsageModelsTable } from '../components/usage-models-table'
import { UsageMediaSection } from '../components/usage-media-section'
import { UsageSessionsTable } from '../components/usage-sessions-table'

export type UsageTabID = 'overview' | 'providers' | 'models' | 'media' | 'sessions'
export type UsageTimeRange = 'today' | '7d' | '30d' | '90d' | 'all'

export function UsagePage() {
  const params = useParams({ strict: false }) as { workspaceSlug?: string }
  const navigate = useNavigate()

  const [activeTab, setActiveTab] = useState<UsageTabID>('overview')
  const [timeRange, setTimeRange] = useState<UsageTimeRange>('30d')
  const [selectedProvider, setSelectedProvider] = useState<string | null>(null)
  const [selectedDate, setSelectedDate] = useState<string | null>(null)

  const workspaceSlug = params.workspaceSlug ?? ''

  // Query usage data
  const {
    data,
    isLoading,
    isFetching,
    error,
    refetch,
  } = useQuery({
    queryKey: ['session-usage-dashboard', timeRange, selectedProvider],
    queryFn: ({ signal }) =>
      fetchSessionUsageDashboard(
        {
          timeRange,
          provider: selectedProvider || undefined,
        },
        signal,
      ),
    staleTime: 30_000,
  })

  const handleBack = () => {
    if (workspaceSlug) {
      void navigate({ to: '/$workspaceSlug', params: { workspaceSlug } })
    } else {
      void navigate({ to: '/' })
    }
  }

  const handleOpenSession = (sessionId: string) => {
    if (workspaceSlug) {
      void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug, sessionId } })
    } else {
      void navigate({ to: '/' })
    }
  }

  const handleExportJSON = () => {
    if (!data) return
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `swarm-usage-${timeRange}-${new Date().toISOString().slice(0, 10)}.json`
    a.click()
    URL.revokeObjectURL(url)
  }

  const summary = data?.summary
  const totalTokens = summary?.total_tokens ?? 0
  const cacheTokens = summary?.cached_tokens ?? 0
  const cachePct = totalTokens > 0 ? (cacheTokens / totalTokens) * 100 : 0
  const billedCost = summary ? summary.total_cost_usd + summary.media_cost_usd : 0

  return (
    <div className="absolute inset-0 overflow-y-auto bg-[var(--app-bg)] text-[var(--app-text)]">
      <div className="mx-auto flex min-h-full w-full max-w-6xl flex-col px-4 py-6 sm:px-8 sm:py-8 space-y-6">
        {/* Top Header */}
        <header className="flex flex-col gap-4 border-b border-[var(--app-border)] pb-5 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            <Button
              variant="ghost"
              className="mb-2 h-8 px-2 text-xs text-[var(--app-text-muted)]"
              onClick={handleBack}
            >
              <ArrowLeft size={14} className="mr-1.5" />
              {workspaceSlug ? 'Back to workspace' : 'Back to launcher'}
            </Button>
            <div className="flex items-center gap-3">
              <span className="grid size-10 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)] shadow-sm">
                <Coins size={20} strokeWidth={1.8} />
              </span>
              <div>
                <h1 className="text-xl font-bold tracking-tight text-[var(--app-text)]">
                  Session Usage & Analytics
                </h1>
                <p className="text-xs text-[var(--app-text-muted)]">
                  Real-time telemetry, provider cost calculations, prompt cache savings, and media tracking.
                </p>
              </div>
            </div>
          </div>

          {/* Time Range & Action Controls */}
          <div className="flex flex-wrap items-center gap-2 self-start sm:self-auto">
            {/* Time range pill selector */}
            <div className="inline-flex rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-0.5 text-xs font-medium">
              {(
                [
                  { id: 'today', label: 'Today' },
                  { id: '7d', label: '7D' },
                  { id: '30d', label: '30D' },
                  { id: '90d', label: '90D' },
                  { id: 'all', label: 'All Time' },
                ] as const
              ).map((t) => (
                <button
                  key={t.id}
                  type="button"
                  onClick={() => setTimeRange(t.id)}
                  className={`rounded-md px-2.5 py-1 transition-colors ${
                    timeRange === t.id
                      ? 'bg-[var(--app-primary)] text-[var(--app-primary-foreground)]'
                      : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                  }`}
                >
                  {t.label}
                </button>
              ))}
            </div>

            {/* Export JSON */}
            <Button
              variant="outline"
              size="sm"
              className="h-8 px-2.5 text-xs"
              onClick={handleExportJSON}
              disabled={!data}
              title="Export usage data as JSON"
            >
              <Download size={13} className="mr-1" />
              Export
            </Button>

            {/* Refresh */}
            <Button
              variant="outline"
              size="sm"
              className="h-8 px-2.5 text-xs"
              onClick={() => void refetch()}
              disabled={isLoading || isFetching}
              title="Refresh usage telemetry"
            >
              <RefreshCw
                size={13}
                className={`mr-1 ${isFetching ? 'animate-spin text-[var(--app-primary)]' : ''}`}
              />
              Refresh
            </Button>
          </div>
        </header>

        {/* Loading / Error State */}
        {isLoading && !data && (
          <div className="flex h-64 flex-col items-center justify-center gap-3 text-xs text-[var(--app-text-muted)]">
            <Loader2 size={24} className="animate-spin text-[var(--app-primary)]" />
            <span>Analyzing session usage telemetry...</span>
          </div>
        )}

        {error && (
          <div className="rounded-xl border border-red-500/20 bg-red-500/10 p-4 text-xs text-red-600 dark:text-red-400">
            Failed to load usage statistics: {error instanceof Error ? error.message : String(error)}
          </div>
        )}

        {data && (
          <>
            {/* KPI Summary Cards */}
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-2 lg:grid-cols-5">
              {/* Card 1: Total Billed Cost */}
              <Card className="p-4 space-y-1">
                <span className="text-[11px] font-medium text-[var(--app-text-muted)]">
                  Est. Billed Cost
                </span>
                <div className="flex items-baseline gap-1.5">
                  <span className="text-2xl font-bold text-emerald-600 dark:text-emerald-400 font-mono">
                    ${billedCost.toFixed(2)}
                  </span>
                </div>
                <div className="text-[10px] text-[var(--app-text-subtle)] truncate">
                  Tokens: ${summary?.total_cost_usd.toFixed(2)} · Media: ${summary?.media_cost_usd.toFixed(2)}
                </div>
              </Card>

              {/* Card 2: Total Tokens */}
              <Card className="p-4 space-y-1">
                <span className="text-[11px] font-medium text-[var(--app-text-muted)]">
                  Total Tokens
                </span>
                <div className="text-2xl font-bold text-[var(--app-text)] font-mono">
                  {totalTokens >= 1_000_000
                    ? `${(totalTokens / 1_000_000).toFixed(2)}M`
                    : totalTokens.toLocaleString()}
                </div>
                <div className="text-[10px] text-[var(--app-text-subtle)] truncate">
                  In: {summary?.input_tokens.toLocaleString()} · Out: {summary?.output_tokens.toLocaleString()}
                </div>
              </Card>

              {/* Card 3: Prompt Cache Share */}
              <Card className="p-4 space-y-1">
                <span className="text-[11px] font-medium text-[var(--app-text-muted)]">
                  Prompt Cache
                </span>
                <div className="flex items-baseline gap-1.5">
                  <span className="text-2xl font-bold text-emerald-600 dark:text-emerald-400 font-mono">
                    {cachePct.toFixed(0)}%
                  </span>
                </div>
                <div className="text-[10px] text-[var(--app-text-subtle)] truncate">
                  {cacheTokens.toLocaleString()} cached tokens
                </div>
              </Card>

              {/* Card 4: Media Generation */}
              <Card className="p-4 space-y-1">
                <span className="text-[11px] font-medium text-[var(--app-text-muted)]">
                  Media Calls
                </span>
                <div className="text-2xl font-bold text-[var(--app-text)] font-mono">
                  {summary?.total_media_calls ?? 0}
                </div>
                <div className="text-[10px] text-[var(--app-text-subtle)] truncate">
                  Images: {data.media.image_count} · Videos: {data.media.video_count} · Audio: {data.media.audio_count}
                </div>
              </Card>

              {/* Card 5: Sessions & Turns */}
              <Card className="col-span-2 sm:col-span-2 lg:col-span-1 p-4 space-y-1">
                <span className="text-[11px] font-medium text-[var(--app-text-muted)]">
                  Activity
                </span>
                <div className="text-2xl font-bold text-[var(--app-text)] font-mono">
                  {summary?.total_turns ?? 0}
                </div>
                <div className="text-[10px] text-[var(--app-text-subtle)] truncate">
                  Across {summary?.total_sessions ?? 0} active sessions
                </div>
              </Card>
            </div>

            {/* Provider Filter Indicator */}
            {selectedProvider && (
              <div className="flex items-center gap-2 rounded-lg bg-[var(--app-surface-hover)] px-3 py-2 text-xs">
                <Filter size={13} className="text-[var(--app-primary)]" />
                <span>
                  Filtered by provider: <strong className="capitalize">{selectedProvider}</strong>
                </span>
                <button
                  type="button"
                  onClick={() => setSelectedProvider(null)}
                  className="ml-auto text-[11px] text-[var(--app-text-muted)] underline hover:text-[var(--app-text)]"
                >
                  Clear filter
                </button>
              </div>
            )}

            {/* Primary Interactive Chart */}
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <h3 className="text-sm font-semibold text-[var(--app-text)] flex items-center gap-1.5">
                  <Activity size={15} className="text-[var(--app-primary)]" />
                  Daily Usage Timeline
                </h3>
                {data.daily.length > 0 && (
                  <span className="text-[11px] text-[var(--app-text-muted)]">
                    {data.daily.length} active days
                  </span>
                )}
              </div>
              <UsageChartTokens
                daily={data.daily}
                selectedDate={selectedDate}
                onSelectDate={setSelectedDate}
              />
            </div>

            {/* Section Tabs */}
            <div className="border-b border-[var(--app-border)]">
              <nav className="-mb-px flex gap-6 text-xs font-medium">
                {(
                  [
                    { id: 'overview', label: 'Providers Overview' },
                    { id: 'models', label: `Models (${data.by_model.length})` },
                    { id: 'media', label: `Media Gen (${data.media.total_count})` },
                    { id: 'sessions', label: `Active Sessions (${data.recent_sessions.length})` },
                  ] as const
                ).map((tab) => (
                  <button
                    key={tab.id}
                    type="button"
                    onClick={() => setActiveTab(tab.id)}
                    className={`border-b-2 py-2.5 transition-colors ${
                      activeTab === tab.id
                        ? 'border-[var(--app-primary)] text-[var(--app-text)] font-semibold'
                        : 'border-transparent text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                    }`}
                  >
                    {tab.label}
                  </button>
                ))}
              </nav>
            </div>

            {/* Tab 1: Overview / Providers */}
            {activeTab === 'overview' && (
              <div className="space-y-6">
                <UsageProviderCards
                  providers={data.by_provider}
                  totalTokens={totalTokens}
                  selectedProvider={selectedProvider}
                  onSelectProvider={setSelectedProvider}
                />

                <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
                  <div className="space-y-3">
                    <h4 className="text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">
                      Top Models By Tokens
                    </h4>
                    <UsageModelsTable models={data.by_model.slice(0, 5)} />
                  </div>
                  <div className="space-y-3">
                    <h4 className="text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">
                      Recent Active Sessions
                    </h4>
                    <UsageSessionsTable
                      sessions={data.recent_sessions.slice(0, 5)}
                      onOpenSession={handleOpenSession}
                    />
                  </div>
                </div>
              </div>
            )}

            {/* Tab 2: Models Breakdown */}
            {activeTab === 'models' && <UsageModelsTable models={data.by_model} />}

            {/* Tab 3: Media Calls */}
            {activeTab === 'media' && <UsageMediaSection media={data.media} />}

            {/* Tab 4: Sessions */}
            {activeTab === 'sessions' && (
              <UsageSessionsTable
                sessions={data.recent_sessions}
                onOpenSession={handleOpenSession}
              />
            )}
          </>
        )}
      </div>
    </div>
  )
}
