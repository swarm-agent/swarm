import { useEffect, useState, useCallback } from 'react'
import {
  Cloud,
  CheckCircle2,
  AlertTriangle,
  RefreshCw,
  Trash2,
  Plus,
  Sparkles,
  HardDrive,
  Check,
  ShieldCheck,
  Radio,
} from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { cn } from '../../../../../lib/cn'
import {
  listStorageBuckets,
  getCanonicalStorageBucket,
  setCanonicalStorageBucket,
  listStorageProposals,
  rejectStorageProposal,
  scanStorageBucket,
  deleteStorageBucket,
} from '../../../storage/api'
import type { StorageBucket } from '../../../storage/types'
import { DesktopStorageBucketsModal } from '../../../storage/components/desktop-storage-buckets-modal'

export function CloudSettingsPage() {
  const [_loading, setLoading] = useState(true)
  const [buckets, setBuckets] = useState<StorageBucket[]>([])
  const [canonicalBucket, setCanonicalBucket] = useState<StorageBucket | null>(null)
  const [proposals, setProposals] = useState<StorageBucket[]>([])
  const [actionBusy, setActionBusy] = useState<string | null>(null)
  const [modalOpen, setModalOpen] = useState(false)
  const [scanMessage, setScanMessage] = useState<string | null>(null)
  const [errorMessage, setErrorMessage] = useState<string | null>(null)

  const loadData = useCallback(async () => {
    setLoading(true)
    setErrorMessage(null)
    try {
      const [allBuckets, canonical, allProposals] = await Promise.all([
        listStorageBuckets(),
        getCanonicalStorageBucket(),
        listStorageProposals(),
      ])
      setBuckets(allBuckets)
      setCanonicalBucket(canonical)
      setProposals(allProposals)
    } catch (err) {
      console.error('[cloud-settings] failed to load data:', err)
      setErrorMessage('Failed to load cloud storage configuration.')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadData()
  }, [loadData])

  const handleAcceptCanonical = async (bucketId: string) => {
    setActionBusy(bucketId)
    setErrorMessage(null)
    try {
      await setCanonicalStorageBucket(bucketId)
      await loadData()
    } catch (err) {
      console.error('[cloud-settings] failed to set canonical bucket:', err)
      setErrorMessage('Failed to set canonical cloud connection.')
    } finally {
      setActionBusy(null)
    }
  }

  const handleRejectProposal = async (bucketId: string) => {
    setActionBusy(`reject:${bucketId}`)
    setErrorMessage(null)
    try {
      await rejectStorageProposal(bucketId)
      await loadData()
    } catch (err) {
      console.error('[cloud-settings] failed to reject proposal:', err)
      setErrorMessage('Failed to reject proposal.')
    } finally {
      setActionBusy(null)
    }
  }

  const handleScanBucket = async (bucketId: string) => {
    setActionBusy(`scan:${bucketId}`)
    setScanMessage(null)
    setErrorMessage(null)
    try {
      const summary = await scanStorageBucket(bucketId)
      const newCount = summary.deliverables_new ?? summary.deliverables_found ?? 0
      setScanMessage(
        `Scanned successfully: ${summary.workers_found} workers found, ${newCount} deliverables.`
      )
      await loadData()
    } catch (err) {
      console.error('[cloud-settings] scan failed:', err)
      setErrorMessage('Failed to scan bucket.')
    } finally {
      setActionBusy(null)
    }
  }

  const handleDeleteBucket = async (bucketId: string) => {
    if (!window.confirm('Are you sure you want to disconnect this cloud storage bucket?')) {
      return
    }
    setActionBusy(`del:${bucketId}`)
    try {
      await deleteStorageBucket(bucketId)
      await loadData()
    } catch (err) {
      console.error('[cloud-settings] delete failed:', err)
      setErrorMessage('Failed to disconnect bucket.')
    } finally {
      setActionBusy(null)
    }
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div>
        <div className="flex items-center gap-2">
          <Cloud className="h-5 w-5 text-[var(--app-primary)]" />
          <h2 className="text-lg font-semibold text-[var(--app-text)]">Cloud & Storage Connections</h2>
        </div>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">
          Manage your canonical Swarm cloud storage connection (S3, Google Cloud Storage, Cloudflare R2).
          Autonomous workers store traces and publish deliverables to this private bucket with zero open inbound ports.
        </p>
      </div>

      {errorMessage ? (
        <div className="flex items-center gap-2 rounded-xl border border-[var(--app-error)] bg-[color-mix(in_srgb,var(--app-error)_12%,var(--app-surface))] p-3 text-sm text-[var(--app-error)]">
          <AlertTriangle className="h-4 w-4 shrink-0" />
          <span>{errorMessage}</span>
        </div>
      ) : null}

      {scanMessage ? (
        <div className="flex items-center gap-2 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] p-3 text-sm text-[var(--app-success)]">
          <CheckCircle2 className="h-4 w-4 shrink-0" />
          <span>{scanMessage}</span>
        </div>
      ) : null}

      {/* Pending Connection Proposals from AI Workers */}
      {proposals.length > 0 ? (
        <div className="space-y-3">
          <div className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wider text-[var(--app-warning)]">
            <Sparkles className="h-4 w-4" />
            <span>Pending AI Worker Connection Requests ({proposals.length})</span>
          </div>

          {proposals.map((prop) => (
            <div
              key={prop.id}
              className="relative overflow-hidden rounded-2xl border border-[var(--app-warning)] bg-[color-mix(in_srgb,var(--app-warning)_8%,var(--app-surface))] p-5 shadow-sm"
            >
              <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
                <div className="min-w-0 space-y-2">
                  <div className="flex items-center gap-2">
                    <span className="rounded-md bg-[var(--app-warning)] px-2 py-0.5 text-xs font-bold uppercase text-[var(--app-bg)]">
                      {prop.provider}
                    </span>
                    <span className="font-mono text-sm font-semibold text-[var(--app-text)]">
                      {prop.bucket_name}
                    </span>
                    <span className="rounded-full border border-[var(--app-border)] px-2 py-0.5 text-[11px] text-[var(--app-text-muted)]">
                      Pending Approval
                    </span>
                  </div>

                  <p className="text-xs text-[var(--app-text-muted)]">
                    Proposed by: <strong className="text-[var(--app-text)]">{prop.proposed_by || 'Autonomous AI Worker'}</strong>
                    {prop.proposal_reason ? ` • ${prop.proposal_reason}` : ''}
                  </p>

                  <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-[var(--app-text-subtle)] font-mono">
                    {prop.endpoint ? <span>Endpoint: {prop.endpoint}</span> : null}
                    {prop.region ? <span>Region: {prop.region}</span> : null}
                  </div>
                </div>

                <div className="flex shrink-0 items-center gap-2">
                  <Button
                    variant="primary"
                    size="sm"
                    disabled={actionBusy === prop.id}
                    onClick={() => void handleAcceptCanonical(prop.id)}
                    className="gap-1.5"
                  >
                    {actionBusy === prop.id ? (
                      <RefreshCw className="h-3.5 w-3.5 animate-spin" />
                    ) : (
                      <Check className="h-3.5 w-3.5" />
                    )}
                    Accept as Canonical Connection
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={actionBusy === `reject:${prop.id}`}
                    onClick={() => void handleRejectProposal(prop.id)}
                  >
                    Reject
                  </Button>
                </div>
              </div>
            </div>
          ))}
        </div>
      ) : null}

      {/* Canonical Connection Section */}
      <div className="space-y-3">
        <h3 className="text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">
          Canonical Swarm Cloud Connection
        </h3>

        {canonicalBucket ? (
          <div className="rounded-2xl border border-[var(--app-primary)] bg-[color-mix(in_srgb,var(--app-primary)_6%,var(--app-surface))] p-5 shadow-sm">
            <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
              <div className="space-y-2">
                <div className="flex items-center gap-2">
                  <span className="rounded-md bg-[var(--app-primary)] px-2 py-0.5 text-xs font-bold uppercase text-[var(--app-primary-text)]">
                    {canonicalBucket.provider}
                  </span>
                  <span className="font-mono text-base font-semibold text-[var(--app-text)]">
                    {canonicalBucket.bucket_name}
                  </span>
                  <span className="inline-flex items-center gap-1 rounded-full border border-[var(--app-primary)] bg-[var(--app-surface)] px-2.5 py-0.5 text-xs font-medium text-[var(--app-primary)]">
                    <ShieldCheck className="h-3 w-3" />
                    Canonical Primary
                  </span>
                </div>

                <p className="text-xs text-[var(--app-text-muted)]">
                  Display Name: <strong className="text-[var(--app-text)]">{canonicalBucket.name}</strong>
                  {canonicalBucket.prefix ? ` • Prefix: ${canonicalBucket.prefix}` : ''}
                </p>

                <div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-xs text-[var(--app-text-subtle)]">
                  {canonicalBucket.endpoint ? <span>Endpoint: {canonicalBucket.endpoint}</span> : null}
                  {canonicalBucket.region ? <span>Region: {canonicalBucket.region}</span> : null}
                  {canonicalBucket.access_key_id ? (
                    <span>
                      Access Key:{' '}
                      {canonicalBucket.access_key_id.length > 8
                        ? `${canonicalBucket.access_key_id.slice(0, 4)}...${canonicalBucket.access_key_id.slice(-4)}`
                        : '••••••••'}
                    </span>
                  ) : null}
                </div>
              </div>

              <div className="flex shrink-0 items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={actionBusy === `scan:${canonicalBucket.id}`}
                  onClick={() => void handleScanBucket(canonicalBucket.id)}
                  className="gap-1.5"
                >
                  <RefreshCw
                    className={cn('h-3.5 w-3.5', actionBusy === `scan:${canonicalBucket.id}` && 'animate-spin')}
                  />
                  Scan Deliverables
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={actionBusy === `del:${canonicalBucket.id}`}
                  onClick={() => void handleDeleteBucket(canonicalBucket.id)}
                  className="text-[var(--app-error)] hover:bg-[color-mix(in_srgb,var(--app-error)_10%,transparent)]"
                  title="Disconnect bucket"
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            </div>
          </div>
        ) : (
          <div className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-6 text-center">
            <HardDrive className="mx-auto h-8 w-8 text-[var(--app-text-subtle)]" />
            <h4 className="mt-2 text-sm font-semibold text-[var(--app-text)]">
              No Canonical Cloud Connection Configured
            </h4>
            <p className="mx-auto mt-1 max-w-md text-xs text-[var(--app-text-muted)]">
              Connect an S3 or Google Cloud Storage bucket so remote workers can persist execution context and
              publish signed deliverables back to your local Swarm without opening ports.
            </p>
            <div className="mt-4">
              <Button size="sm" onClick={() => setModalOpen(true)} className="gap-1.5">
                <Plus className="h-4 w-4" />
                Connect Cloud Storage
              </Button>
            </div>
          </div>
        )}
      </div>

      {/* All Configured Storage Connections */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">
            All Configured Buckets ({buckets.length})
          </h3>
          <Button variant="outline" size="sm" onClick={() => setModalOpen(true)} className="gap-1.5">
            <Plus className="h-3.5 w-3.5" />
            Add Bucket
          </Button>
        </div>

        {buckets.length === 0 ? (
          <p className="text-xs text-[var(--app-text-subtle)]">No buckets registered yet.</p>
        ) : (
          <div className="divide-y divide-[var(--app-border)] rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]">
            {buckets.map((bkt) => {
              const isCanonical = bkt.id === canonicalBucket?.id || bkt.canonical
              return (
                <div key={bkt.id} className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
                  <div className="min-w-0 space-y-1">
                    <div className="flex items-center gap-2">
                      <span className="rounded bg-[var(--app-surface-hover)] px-1.5 py-0.5 text-[11px] font-bold uppercase text-[var(--app-text-muted)]">
                        {bkt.provider}
                      </span>
                      <span className="font-mono text-sm font-medium text-[var(--app-text)]">
                        {bkt.bucket_name}
                      </span>
                      {isCanonical ? (
                        <span className="inline-flex items-center gap-1 text-xs font-medium text-[var(--app-primary)]">
                          <CheckCircle2 className="h-3.5 w-3.5" />
                          Canonical
                        </span>
                      ) : null}
                    </div>
                    <div className="flex flex-wrap gap-x-3 text-xs text-[var(--app-text-muted)]">
                      <span>Name: {bkt.name}</span>
                      {bkt.region ? <span>Region: {bkt.region}</span> : null}
                      {bkt.status ? <span>Status: {bkt.status}</span> : null}
                    </div>
                  </div>

                  <div className="flex shrink-0 items-center gap-2">
                    {!isCanonical ? (
                      <Button
                        variant="secondary"
                        size="sm"
                        disabled={actionBusy === bkt.id}
                        onClick={() => void handleAcceptCanonical(bkt.id)}
                        className="gap-1 text-xs"
                      >
                        <Radio className="h-3 w-3" />
                        Make Canonical
                      </Button>
                    ) : null}
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={actionBusy === `scan:${bkt.id}`}
                      onClick={() => void handleScanBucket(bkt.id)}
                      title="Scan for deliverables"
                    >
                      <RefreshCw
                        className={cn('h-3.5 w-3.5', actionBusy === `scan:${bkt.id}` && 'animate-spin')}
                      />
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={actionBusy === `del:${bkt.id}`}
                      onClick={() => void handleDeleteBucket(bkt.id)}
                      className="text-[var(--app-error)] hover:bg-[color-mix(in_srgb,var(--app-error)_10%,transparent)]"
                      title="Disconnect bucket"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>

      <DesktopStorageBucketsModal
        open={modalOpen}
        onOpenChange={(open) => {
          setModalOpen(open)
          if (!open) {
            void loadData()
          }
        }}
      />
    </div>
  )
}
