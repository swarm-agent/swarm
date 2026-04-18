import { useEffect, useMemo, useState } from 'react'
import { Boxes, Plus, Save, Sparkles, Trash2 } from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import { Textarea } from '../../../../components/ui/textarea'
import { cn } from '../../../../lib/cn'
import { fetchDesktopOnboardingStatus } from '../../onboarding/api'
import type { DesktopOnboardingStatus } from '../../onboarding/types'
import { deleteContainerProfile } from '../mutations/delete-container-profile'
import { upsertContainerProfile } from '../mutations/upsert-container-profile'
import { listContainerProfiles } from '../queries/list-container-profiles'
import type { ContainerProfile, ContainerProfileDraft, ContainerRoleHint } from '../types/container-profiles'
import {
  createContainerProfileDraft,
  createEmptyContainerProfileDraft,
  sortContainerProfiles,
} from '../types/container-profiles'
import { ContainerMountPicker } from './container-mount-picker'
import { listWorkspaces } from '../../../workspaces/launcher/queries/list-workspaces'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import {
  buildQuickContainerProfileDraft,
  suggestQuickContainerProfileName,
} from '../services/quick-container-recommendation'

interface ContainerProfilesPanelProps {
  variant?: 'full' | 'compact'
}

const roleOptions: Array<{ value: ContainerRoleHint; title: string; description: string }> = [
  {
    value: 'workspace',
    title: 'Workspace-first',
    description: 'Best default for a local swarm launcher. Focus on folders and let runtime details happen automatically later.',
  },
  {
    value: 'child',
    title: 'Child swarm',
    description: 'Use this when the saved shape is meant for a dependent swarm node rather than a general local workspace swarm.',
  },
]

function formatUpdatedAt(value: number): string {
  if (!value) {
    return 'Not saved yet'
  }
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(value))
}

export function ContainerProfilesPanel({ variant = 'full' }: ContainerProfilesPanelProps) {
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [creatingQuickProfile, setCreatingQuickProfile] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)
  const [profiles, setProfiles] = useState<ContainerProfile[]>([])
  const [workspaces, setWorkspaces] = useState<WorkspaceEntry[]>([])
  const [onboardingStatus, setOnboardingStatus] = useState<DesktopOnboardingStatus | null>(null)
  const [quickProfileName, setQuickProfileName] = useState('')
  const [selectedProfileID, setSelectedProfileID] = useState<string | null>(null)
  const [draft, setDraft] = useState<ContainerProfileDraft>(createEmptyContainerProfileDraft())

  const isCompact = variant === 'compact'
  const quickDraft = useMemo(() => buildQuickContainerProfileDraft({
    name: quickProfileName,
    onboardingStatus,
    workspaces,
  }), [onboardingStatus, quickProfileName, workspaces])
  const isEditingExisting = Boolean(selectedProfileID)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    setStatus(null)

    void Promise.all([
      listContainerProfiles(),
      listWorkspaces().catch(() => []),
      fetchDesktopOnboardingStatus().catch(() => null),
    ])
      .then(([nextProfiles, nextWorkspaces, nextOnboarding]) => {
        if (cancelled) {
          return
        }
        setProfiles(nextProfiles)
        setWorkspaces(nextWorkspaces)
        setOnboardingStatus(nextOnboarding)
        setQuickProfileName(suggestQuickContainerProfileName(nextProfiles, nextOnboarding))
        if (nextProfiles.length > 0) {
          setSelectedProfileID(nextProfiles[0].id)
          setDraft(createContainerProfileDraft(nextProfiles[0]))
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'Failed to load launcher profiles')
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [])

  const handleSelectProfile = (profile: ContainerProfile) => {
    setSelectedProfileID(profile.id)
    setDraft(createContainerProfileDraft(profile))
    setError(null)
    setStatus(null)
  }

  const handleCreateNew = () => {
    setSelectedProfileID(null)
    setDraft(createEmptyContainerProfileDraft())
    setError(null)
    setStatus(null)
  }

  const handleSave = async () => {
    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      const saved = await upsertContainerProfile(draft)
      const nextProfiles = sortContainerProfiles([
        ...profiles.filter((profile) => profile.id !== saved.id),
        saved,
      ])
      setProfiles(nextProfiles)
      setSelectedProfileID(saved.id)
      setDraft(createContainerProfileDraft(saved))
      setStatus(`Saved launcher profile ${saved.name}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save launcher profile')
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    if (!selectedProfileID) {
      return
    }
    setDeleting(true)
    setError(null)
    setStatus(null)
    try {
      const deletedID = await deleteContainerProfile(selectedProfileID)
      const nextProfiles = profiles.filter((profile) => profile.id !== deletedID)
      setProfiles(nextProfiles)
      if (nextProfiles.length > 0) {
        setSelectedProfileID(nextProfiles[0].id)
        setDraft(createContainerProfileDraft(nextProfiles[0]))
      } else {
        setSelectedProfileID(null)
        setDraft(createEmptyContainerProfileDraft())
      }
      setStatus('Removed the saved launcher profile.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete launcher profile')
    } finally {
      setDeleting(false)
    }
  }

  const handleCreateQuickProfile = async () => {
    setCreatingQuickProfile(true)
    setError(null)
    setStatus(null)
    try {
      const saved = await upsertContainerProfile(quickDraft)
      const nextProfiles = sortContainerProfiles([
        ...profiles.filter((profile) => profile.id !== saved.id),
        saved,
      ])
      setProfiles(nextProfiles)
      setSelectedProfileID(saved.id)
      setDraft(createContainerProfileDraft(saved))
      setQuickProfileName(suggestQuickContainerProfileName(nextProfiles, onboardingStatus))
      setStatus(`Created launcher profile ${saved.name}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create launcher profile')
    } finally {
      setCreatingQuickProfile(false)
    }
  }

  return (
    <div className="grid gap-5">
      <div className={cn('flex flex-col gap-3 md:flex-row md:items-end md:justify-between', isCompact && 'md:items-start')}>
        <div className="space-y-2">
          <div className="flex items-center gap-3">
            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2 text-[var(--app-text-muted)]">
              <Boxes size={18} />
            </div>
            <h2 className="text-xl font-semibold text-[var(--app-text)]">{isCompact ? 'Saved launchers' : 'Launcher profiles'}</h2>
          </div>
          <p className="max-w-3xl text-sm text-[var(--app-text-muted)]">
            Save simple swarm launcher shapes around identity and folders. Runtime, ports, and networking stay automated in the main Add swarm flow.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone={profiles.length > 0 ? 'live' : 'neutral'}>{profiles.length} saved</Badge>
          <Button type="button" onClick={handleCreateNew} disabled={loading || saving || deleting}>
            <Plus size={14} />
            Add launcher profile
          </Button>
        </div>
      </div>

      {error ? <Card className="border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">{error}</Card> : null}
      {status ? <Card className="border-[var(--app-success-border)] bg-[var(--app-success-bg)] p-4 text-sm text-[var(--app-success)]">{status}</Card> : null}

      <Card className="p-5">
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1.05fr)_minmax(320px,0.95fr)]">
          <div className="grid gap-3">
            <div className="flex items-center gap-3">
              <div className="rounded-2xl border border-[var(--app-border)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))] p-2 text-[var(--app-primary)]">
                <Sparkles size={18} />
              </div>
              <div>
                <h3 className="text-lg font-semibold text-[var(--app-text)]">Quick recommendation</h3>
                <p className="text-sm text-[var(--app-text-muted)]">
                  Start from your current swarm and saved workspaces, then save the folder selection as a reusable launcher profile.
                </p>
              </div>
            </div>
            <div className="grid gap-2">
              <label className="text-sm font-medium text-[var(--app-text)]">Profile name</label>
              <Input
                value={quickProfileName}
                onChange={(event) => setQuickProfileName(event.target.value)}
                placeholder="Local swarm"
                disabled={loading || creatingQuickProfile}
              />
            </div>
            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
              <div className="font-medium text-[var(--app-text)]">What this saves</div>
              <ul className="mt-2 grid gap-1.5 pl-4 list-disc">
                <li>Name and intent for the launcher.</li>
                <li>Recommended workspace mounts from this machine.</li>
                <li>A simple role hint for workspace-first or child-first setup.</li>
              </ul>
            </div>
          </div>

          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg)] p-4">
            <div className="flex items-center justify-between gap-3">
              <div>
                <div className="text-sm font-semibold text-[var(--app-text)]">Recommended preview</div>
                <div className="mt-1 text-sm text-[var(--app-text-muted)]">{quickDraft.description || 'Ready to save as a launcher profile.'}</div>
              </div>
              <Badge tone="live">{quickDraft.mounts.length} folders</Badge>
            </div>
            <div className="mt-4 grid gap-2 text-sm text-[var(--app-text-muted)]">
              <div><span className="font-medium text-[var(--app-text)]">Role:</span> {quickDraft.roleHint}</div>
              <div><span className="font-medium text-[var(--app-text)]">Primary folders:</span> {quickDraft.mounts.length}</div>
            </div>
            <div className="mt-4 flex flex-col gap-2 sm:flex-row">
              <Button type="button" onClick={() => void handleCreateQuickProfile()} disabled={loading || creatingQuickProfile || saving || deleting}>
                <Sparkles size={14} />
                {creatingQuickProfile ? 'Saving…' : 'Save recommended profile'}
              </Button>
              <Button type="button" variant="outline" onClick={() => setDraft(quickDraft)} disabled={loading || creatingQuickProfile || saving || deleting}>
                Use in editor
              </Button>
            </div>
          </div>
        </div>
      </Card>

      <div className="grid gap-5 xl:grid-cols-[minmax(280px,0.82fr)_minmax(0,1.18fr)]">
        <Card className="p-5">
          <div className="flex items-center justify-between gap-3">
            <div>
              <h3 className="text-lg font-semibold text-[var(--app-text)]">Saved profiles</h3>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">Pick one to edit or save a new one.</p>
            </div>
            <Badge tone={profiles.length > 0 ? 'live' : 'neutral'}>{profiles.length}</Badge>
          </div>
          <div className="mt-4 grid gap-3">
            {loading ? (
              <div className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-5 text-sm text-[var(--app-text-muted)]">
                Loading launcher profiles…
              </div>
            ) : profiles.length === 0 ? (
              <div className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-5 text-sm text-[var(--app-text-muted)]">
                No saved launcher profiles yet.
              </div>
            ) : (
              profiles.map((profile) => (
                <button
                  key={profile.id}
                  type="button"
                  onClick={() => handleSelectProfile(profile)}
                  className={cn(
                    'rounded-2xl border p-4 text-left transition',
                    selectedProfileID === profile.id
                      ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,var(--app-surface))]'
                      : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] hover:border-[var(--app-border-strong)]',
                  )}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-semibold text-[var(--app-text)]">{profile.name}</span>
                    <Badge tone="neutral">{profile.roleHint}</Badge>
                  </div>
                  <div className="mt-2 text-sm text-[var(--app-text-muted)]">{profile.mounts.length} folders · {formatUpdatedAt(profile.updatedAt)}</div>
                </button>
              ))
            )}
          </div>
        </Card>

        <Card className="p-5">
          <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
            <div>
              <h3 className="text-lg font-semibold text-[var(--app-text)]">{isEditingExisting ? 'Edit launcher profile' : 'New launcher profile'}</h3>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">Keep it simple: name, short note, role hint, and folders.</p>
            </div>
            {isEditingExisting ? <Badge tone="live">Saved</Badge> : <Badge tone="neutral">Draft</Badge>}
          </div>

          <div className="mt-5 grid gap-5">
            <div className="grid gap-4 md:grid-cols-2">
              <div className="grid gap-2">
                <label className="text-sm font-medium text-[var(--app-text)]">Profile name</label>
                <Input
                  value={draft.name}
                  onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
                  placeholder="Local swarm"
                  disabled={saving || deleting}
                />
              </div>
              <div className="grid gap-2">
                <label className="text-sm font-medium text-[var(--app-text)]">Short note</label>
                <Textarea
                  value={draft.description}
                  onChange={(event) => setDraft((current) => ({ ...current, description: event.target.value }))}
                  placeholder="Optional note about when to use this launcher profile."
                  disabled={saving || deleting}
                  className="min-h-[104px]"
                />
              </div>
            </div>

            <div className="grid gap-3 md:grid-cols-2">
              {roleOptions.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  onClick={() => setDraft((current) => ({ ...current, roleHint: option.value }))}
                  disabled={saving || deleting}
                  className={cn(
                    'rounded-2xl border px-4 py-4 text-left transition',
                    draft.roleHint === option.value
                      ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,var(--app-surface))]'
                      : 'border-[var(--app-border)] bg-[var(--app-bg)] hover:border-[var(--app-border-strong)]',
                  )}
                >
                  <div className="text-sm font-semibold text-[var(--app-text)]">{option.title}</div>
                  <div className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]">{option.description}</div>
                </button>
              ))}
            </div>

            <ContainerMountPicker
              mounts={draft.mounts}
              workspaces={workspaces}
              disabled={saving || deleting}
              onChange={(mounts) => setDraft((current) => ({ ...current, mounts }))}
            />
          </div>

          <div className="mt-6 flex flex-col gap-3 border-t border-[var(--app-border)] pt-5 md:flex-row md:items-center md:justify-between">
            <div>
              {isEditingExisting ? (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => void handleDelete()}
                  disabled={saving || deleting}
                  className="text-[var(--app-danger)] hover:border-[var(--app-danger-border)] hover:bg-[var(--app-danger-bg)] hover:text-[var(--app-danger)]"
                >
                  <Trash2 size={14} />
                  {deleting ? 'Removing…' : 'Delete profile'}
                </Button>
              ) : null}
            </div>
            <div className="flex gap-3">
              <Button type="button" variant="outline" onClick={handleCreateNew} disabled={saving || deleting}>
                Reset
              </Button>
              <Button type="button" onClick={() => void handleSave()} disabled={saving || deleting || loading}>
                <Save size={14} />
                {saving ? 'Saving…' : 'Save profile'}
              </Button>
            </div>
          </div>
        </Card>
      </div>
    </div>
  )
}
