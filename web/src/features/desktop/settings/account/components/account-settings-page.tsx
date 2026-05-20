import { useQuery } from '@tanstack/react-query'
import { UserRound } from 'lucide-react'
import { Card } from '../../../../../components/ui/card'
import { fetchDesktopOnboardingStatus } from '../../../onboarding/api'

export function AccountSettingsPage() {
  const query = useQuery({
    queryKey: ['desktop-onboarding-status', 'account-settings'],
    queryFn: fetchDesktopOnboardingStatus,
    staleTime: 10_000,
  })

  const identity = query.data?.identity
  const bootstrapped = Boolean(identity?.bootstrapped && identity.userID.trim() !== '')

  return (
    <div className="grid gap-6">
      <div className="grid gap-2">
        <div className="flex items-center gap-3">
          <div className="grid h-10 w-10 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text)]">
            <UserRound size={18} />
          </div>
          <div>
            <h2 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">Account</h2>
            <p className="text-sm text-[var(--app-text-muted)]">Read-only product actor context for this local daemon.</p>
          </div>
        </div>
      </div>

      {query.error ? (
        <Card className="border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-4 text-sm text-[var(--app-warning)]">
          Account context is unavailable: {query.error instanceof Error ? query.error.message : 'failed to load account context'}
        </Card>
      ) : null}

      {!query.error && !bootstrapped ? (
        <Card className="border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] p-5 text-sm leading-6 text-[var(--app-warning)]">
          No product user exists yet. Complete onboarding to create the initial local owner identity for this installed daemon.
        </Card>
      ) : null}

      {bootstrapped && identity ? (
        <Card className="grid gap-4 border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-5">
          <ReadonlyAccountRow label="Username" value={identity.username || '—'} />
          <ReadonlyAccountRow label="User ID" value={identity.userID} />
          <ReadonlyAccountRow label="Default owner container" value={identity.teamID || '—'} />
          <ReadonlyAccountRow label="Membership role" value={identity.membershipRole || '—'} />
          <p className="text-xs leading-5 text-[var(--app-text-muted)]">
            Username and team membership cannot be changed from Checkpoint 1 Account settings. Swarm name remains separate in Swarm settings and the sidebar.
          </p>
        </Card>
      ) : null}
    </div>
  )
}

function ReadonlyAccountRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3">
      <div className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">{label}</div>
      <div className="break-all text-sm font-medium text-[var(--app-text)]">{value}</div>
    </div>
  )
}
