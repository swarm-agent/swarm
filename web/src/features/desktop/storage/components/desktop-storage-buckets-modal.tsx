import { useEffect, useState } from 'react'
import {
  AlertTriangle,
  Check,
  Cloud,
  HardDrive,
  Loader2,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
} from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { cn } from '../../../../lib/cn'
import {
  deleteStorageBucket,
  listStorageBuckets,
  registerStorageBucket,
  scanStorageBucket,
} from '../api'
import type { StorageBucket, StorageScanSummary } from '../types'

export interface DesktopStorageBucketsModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onBucketsUpdated?: () => void
}

export function DesktopStorageBucketsModal({
  open,
  onOpenChange,
  onBucketsUpdated,
}: DesktopStorageBucketsModalProps) {
  const [buckets, setBuckets] = useState<StorageBucket[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [isAdding, setIsAdding] = useState(false)

  // Form state
  const [provider, setProvider] = useState<'s3' | 'gcs' | 'local'>('gcs')
  const [name, setName] = useState('')
  const [bucketName, setBucketName] = useState('')
  const [endpoint, setEndpoint] = useState('https://storage.googleapis.com')
  const [region, setRegion] = useState('auto')
  const [accessKeyId, setAccessKeyId] = useState('')
  const [secretAccessKey, setSecretAccessKey] = useState('')
  const [prefix, setPrefix] = useState('')
  const [submitting, setSubmitting] = useState(false)

  // Scanning state
  const [scanningBucketId, setScanningBucketId] = useState<string | null>(null)
  const [scanResult, setScanResult] = useState<{ id: string; summary: StorageScanSummary } | null>(
    null
  )

  const loadBuckets = async () => {
    setLoading(true)
    setError(null)
    try {
      const list = await listStorageBuckets()
      setBuckets(list)
    } catch (err: any) {
      setError(err?.message || 'Failed to load storage buckets')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (open) {
      void loadBuckets()
      setIsAdding(false)
      setScanResult(null)
    }
  }, [open])

  const handleProviderChange = (p: 's3' | 'gcs' | 'local') => {
    setProvider(p)
    if (p === 'gcs') {
      setEndpoint('https://storage.googleapis.com')
      setRegion('auto')
      if (!name || name === 'AWS S3 Bucket' || name === 'Local Directory') {
        setName('Google Cloud Storage')
      }
    } else if (p === 's3') {
      setEndpoint('')
      setRegion('us-east-1')
      if (!name || name === 'Google Cloud Storage' || name === 'Local Directory') {
        setName('AWS S3 Bucket')
      }
    } else {
      setEndpoint('')
      setRegion('')
      if (!name || name === 'Google Cloud Storage' || name === 'AWS S3 Bucket') {
        setName('Local Directory')
      }
    }
  }

  const handleAddBucket = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim() || !bucketName.trim()) {
      setError('Name and Bucket Name/Directory are required')
      return
    }

    setSubmitting(true)
    setError(null)
    try {
      const created = await registerStorageBucket({
        name: name.trim(),
        provider,
        bucket_name: bucketName.trim(),
        endpoint: endpoint.trim() || undefined,
        region: region.trim() || undefined,
        access_key_id: accessKeyId.trim() || undefined,
        secret_access_key: secretAccessKey.trim() || undefined,
        prefix: prefix.trim() || undefined,
        enabled: true,
      })

      // Trigger immediate scan
      try {
        await scanStorageBucket(created.id)
      } catch (scanErr) {
        console.warn('[storage] initial scan warning:', scanErr)
      }

      await loadBuckets()
      onBucketsUpdated?.()
      setIsAdding(false)
      // Reset form
      setName('')
      setBucketName('')
      setAccessKeyId('')
      setSecretAccessKey('')
      setPrefix('')
    } catch (err: any) {
      setError(err?.message || 'Failed to register storage bucket')
    } finally {
      setSubmitting(false)
    }
  }

  const handleDelete = async (id: string) => {
    if (!window.confirm('Are you sure you want to disconnect this storage bucket?')) {
      return
    }
    try {
      await deleteStorageBucket(id)
      await loadBuckets()
      onBucketsUpdated?.()
    } catch (err: any) {
      setError(err?.message || 'Failed to delete bucket')
    }
  }

  const handleScan = async (id: string) => {
    setScanningBucketId(id)
    setScanResult(null)
    try {
      const summary = await scanStorageBucket(id)
      setScanResult({ id, summary })
      onBucketsUpdated?.()
    } catch (err: any) {
      setError(err?.message || 'Failed to scan bucket')
    } finally {
      setScanningBucketId(null)
    }
  }

  if (!open) return null

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Storage Buckets" className="z-[85] p-4 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="w-[min(720px,calc(100vw-24px))] gap-0 rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
        {/* Header */}
        <div className="flex items-center justify-between border-b border-[var(--app-border)] px-6 py-5">
          <div className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-2xl bg-[var(--app-primary)]/10 text-[var(--app-primary)]">
              <Cloud size={20} />
            </div>
            <div>
              <h2 className="text-lg font-semibold text-[var(--app-text)]">Cloud Storage Buckets</h2>
              <p className="text-xs text-[var(--app-text-muted)]">
                Connect S3 or GCP Cloud Storage for cloud worker base context & deliverables
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {!isAdding && (
              <Button variant="secondary" size="sm" onClick={() => setIsAdding(true)}>
                <Plus size={14} className="mr-1" /> Add Bucket
              </Button>
            )}
            <ModalCloseButton onClick={() => onOpenChange(false)} />
          </div>
        </div>

        {/* Content */}
        <div className="max-h-[70vh] overflow-y-auto p-6 space-y-4">
          {error && (
            <div className="flex items-center gap-2 rounded-xl border border-rose-500/30 bg-rose-500/10 p-3 text-xs text-rose-300">
              <AlertTriangle size={16} className="shrink-0 text-rose-400" />
              <span>{error}</span>
            </div>
          )}

          {isAdding ? (
            <Card className="p-5 border-[var(--app-border-strong)] space-y-4">
              <div className="flex items-center justify-between border-b border-[var(--app-border)] pb-3">
                <h3 className="text-sm font-semibold text-[var(--app-text)]">Connect New Storage Bucket</h3>
                <Button variant="ghost" size="sm" onClick={() => setIsAdding(false)}>
                  Cancel
                </Button>
              </div>

              <form onSubmit={handleAddBucket} className="space-y-4">
                {/* Provider Picker */}
                <div>
                  <label className="text-xs font-medium text-[var(--app-text-muted)]">Provider</label>
                  <div className="mt-1.5 grid grid-cols-3 gap-2">
                    <button
                      type="button"
                      onClick={() => handleProviderChange('gcs')}
                      className={cn(
                        'flex flex-col items-center justify-center gap-1.5 rounded-xl border p-3 text-xs font-medium transition',
                        provider === 'gcs'
                          ? 'border-[var(--app-primary)] bg-[var(--app-primary)]/10 text-[var(--app-primary)] font-semibold'
                          : 'border-[var(--app-border)] bg-[var(--app-surface-muted)] text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                      )}
                    >
                      <Cloud size={18} />
                      Google Cloud (GCS)
                    </button>
                    <button
                      type="button"
                      onClick={() => handleProviderChange('s3')}
                      className={cn(
                        'flex flex-col items-center justify-center gap-1.5 rounded-xl border p-3 text-xs font-medium transition',
                        provider === 's3'
                          ? 'border-[var(--app-primary)] bg-[var(--app-primary)]/10 text-[var(--app-primary)] font-semibold'
                          : 'border-[var(--app-border)] bg-[var(--app-surface-muted)] text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                      )}
                    >
                      <Cloud size={18} />
                      AWS S3 / R2 / MinIO
                    </button>
                    <button
                      type="button"
                      onClick={() => handleProviderChange('local')}
                      className={cn(
                        'flex flex-col items-center justify-center gap-1.5 rounded-xl border p-3 text-xs font-medium transition',
                        provider === 'local'
                          ? 'border-[var(--app-primary)] bg-[var(--app-primary)]/10 text-[var(--app-primary)] font-semibold'
                          : 'border-[var(--app-border)] bg-[var(--app-surface-muted)] text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                      )}
                    >
                      <HardDrive size={18} />
                      Local Directory
                    </button>
                  </div>
                </div>

                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <div>
                    <label className="text-xs font-medium text-[var(--app-text-muted)]">Display Name</label>
                    <Input
                      className="mt-1"
                      placeholder="e.g. Social Media Workers"
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      required
                    />
                  </div>
                  <div>
                    <label className="text-xs font-medium text-[var(--app-text-muted)]">
                      {provider === 'local' ? 'Local Directory Path' : 'Bucket Name'}
                    </label>
                    <Input
                      className="mt-1"
                      placeholder={provider === 'local' ? '/path/to/storage' : 'my-bucket-name'}
                      value={bucketName}
                      onChange={(e) => setBucketName(e.target.value)}
                      required
                    />
                  </div>
                </div>

                {provider !== 'local' && (
                  <>
                    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                      <div>
                        <label className="text-xs font-medium text-[var(--app-text-muted)]">
                          Endpoint URL
                        </label>
                        <Input
                          className="mt-1"
                          placeholder={
                            provider === 'gcs'
                              ? 'https://storage.googleapis.com'
                              : 'https://s3.us-east-1.amazonaws.com (optional)'
                          }
                          value={endpoint}
                          onChange={(e) => setEndpoint(e.target.value)}
                        />
                      </div>
                      <div>
                        <label className="text-xs font-medium text-[var(--app-text-muted)]">Region</label>
                        <Input
                          className="mt-1"
                          placeholder={provider === 'gcs' ? 'auto' : 'us-east-1'}
                          value={region}
                          onChange={(e) => setRegion(e.target.value)}
                        />
                      </div>
                    </div>

                    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                      <div>
                        <label className="text-xs font-medium text-[var(--app-text-muted)]">
                          {provider === 'gcs' ? 'GCP HMAC Access ID' : 'Access Key ID'}
                        </label>
                        <Input
                          className="mt-1 font-mono text-xs"
                          placeholder={provider === 'gcs' ? 'GOOG...' : 'AKIA...'}
                          value={accessKeyId}
                          onChange={(e) => setAccessKeyId(e.target.value)}
                        />
                      </div>
                      <div>
                        <label className="text-xs font-medium text-[var(--app-text-muted)]">
                          {provider === 'gcs' ? 'GCP HMAC Secret' : 'Secret Access Key'}
                        </label>
                        <Input
                          type="password"
                          className="mt-1 font-mono text-xs"
                          placeholder="••••••••••••••••"
                          value={secretAccessKey}
                          onChange={(e) => setSecretAccessKey(e.target.value)}
                        />
                      </div>
                    </div>

                    <div>
                      <label className="text-xs font-medium text-[var(--app-text-muted)]">
                        Prefix / Subfolder (Optional)
                      </label>
                      <Input
                        className="mt-1 font-mono text-xs"
                        placeholder="workers/ (leave empty for root)"
                        value={prefix}
                        onChange={(e) => setPrefix(e.target.value)}
                      />
                    </div>

                    <div className="flex items-center gap-2 rounded-xl bg-[var(--app-surface-muted)] p-2.5 text-[11px] text-[var(--app-text-muted)]">
                      <ShieldCheck size={14} className="shrink-0 text-emerald-400" />
                      <span>
                        Protected Storage: Keys are stored in the local Pebble database and only used locally to sign SigV4 object requests.
                      </span>
                    </div>
                  </>
                )}

                <div className="flex justify-end gap-2 pt-2">
                  <Button variant="secondary" size="sm" type="button" onClick={() => setIsAdding(false)}>
                    Cancel
                  </Button>
                  <Button variant="primary" size="sm" type="submit" disabled={submitting}>
                    {submitting ? (
                      <>
                        <Loader2 size={14} className="mr-1 animate-spin" /> Connecting…
                      </>
                    ) : (
                      'Save & Connect Bucket'
                    )}
                  </Button>
                </div>
              </form>
            </Card>
          ) : null}

          {/* Bucket List */}
          {loading ? (
            <div className="py-8 text-center text-sm text-[var(--app-text-muted)]">
              <Loader2 size={24} className="mx-auto animate-spin mb-2 text-[var(--app-primary)]" />
              Loading connected buckets…
            </div>
          ) : buckets.length === 0 && !isAdding ? (
            <div className="rounded-2xl border border-dashed border-[var(--app-border)] p-8 text-center">
              <Cloud size={36} className="mx-auto text-[var(--app-text-subtle)] mb-2" />
              <h3 className="text-sm font-semibold text-[var(--app-text)]">No Storage Buckets Connected</h3>
              <p className="mt-1 text-xs text-[var(--app-text-muted)] max-w-md mx-auto">
                Connect your AWS S3, Google Cloud Storage, or MinIO bucket so cloud workers can sync progress and deliver outputs straight to your Desktop AI Inbox.
              </p>
              <Button
                variant="primary"
                size="sm"
                className="mt-4"
                onClick={() => {
                  setIsAdding(true)
                  handleProviderChange('gcs')
                }}
              >
                <Plus size={14} className="mr-1" /> Connect GCP or S3 Bucket
              </Button>
            </div>
          ) : (
            <div className="space-y-3">
              {buckets.map((b) => (
                <Card key={b.id} className="p-4 border-[var(--app-border)] space-y-3">
                  <div className="flex items-start justify-between">
                    <div className="flex items-center gap-3">
                      <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-[var(--app-surface-muted)] text-[var(--app-text)]">
                        {b.provider === 'local' ? <HardDrive size={18} /> : <Cloud size={18} />}
                      </div>
                      <div>
                        <div className="flex items-center gap-2">
                          <h4 className="text-sm font-medium text-[var(--app-text)]">{b.name}</h4>
                          <Badge className="text-[10px] uppercase font-mono">
                            {b.provider}
                          </Badge>
                          {b.enabled && (
                            <span className="flex items-center gap-1 text-[11px] text-emerald-400">
                              <Check size={12} /> Active
                            </span>
                          )}
                        </div>
                        <p className="text-xs text-[var(--app-text-muted)] font-mono mt-0.5">
                          {b.provider === 'local' ? b.bucket_name : `${b.bucket_name} (${b.region || 'auto'})`}
                        </p>
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => handleScan(b.id)}
                        disabled={scanningBucketId === b.id}
                      >
                        <RefreshCw
                          size={12}
                          className={cn('mr-1', scanningBucketId === b.id && 'animate-spin')}
                        />
                        {scanningBucketId === b.id ? 'Scanning…' : 'Scan Now'}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="text-rose-400 hover:text-rose-300"
                        onClick={() => handleDelete(b.id)}
                      >
                        <Trash2 size={14} />
                      </Button>
                    </div>
                  </div>

                  {scanResult && scanResult.id === b.id && (
                    <div className="rounded-xl bg-emerald-500/10 border border-emerald-500/20 p-2.5 text-xs text-emerald-300 flex items-center justify-between">
                      <span>
                        Scan Complete: {scanResult.summary.workers_found} workers,{' '}
                        {scanResult.summary.sessions_found} sessions,{' '}
                        {scanResult.summary.deliverables_found} deliverables discovered.
                      </span>
                    </div>
                  )}
                </Card>
              ))}
            </div>
          )}
        </div>
      </DialogPanel>
    </Dialog>
  )
}
