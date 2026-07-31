import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { UserRound } from 'lucide-react'
import { requestJson, updateDesktopSessionUsername } from '../../../../../app/api'
import { Button } from '../../../../../components/ui/button'
import { Card } from '../../../../../components/ui/card'
import { Input } from '../../../../../components/ui/input'

interface AccountIdentityWire {
  bootstrapped?: boolean
  userID?: string
  user_id?: string
  username?: string
  teamID?: string | null
  team_id?: string | null
  teamDisplayName?: string
  team_display_name?: string
  teamDefault?: boolean
  team_default?: boolean
  membershipRole?: string
  membership_role?: string
}

interface AccountIdentity {
  bootstrapped: boolean
  userID: string
  username: string
  teamID: string
  teamDisplayName: string
  teamDefault: boolean
  membershipRole: string
}

interface UsernameUpdateResponse {
  userID?: string
  user_id?: string
  username?: string
}

export function AccountSettingsPage() {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ['desktop-account-context'],
    queryFn: async (): Promise<AccountIdentity> => {
      const account = await requestJson<AccountIdentityWire>('/v1/me')
      const userID = String(account.userID ?? account.user_id ?? '').trim()
      return {
        bootstrapped: Boolean(account.bootstrapped ?? userID !== ''),
        userID,
        username: String(account.username ?? '').trim(),
        teamID: String(account.teamID ?? account.team_id ?? '').trim(),
        teamDisplayName: String(account.teamDisplayName ?? account.team_display_name ?? '').trim(),
        teamDefault: Boolean(account.teamDefault ?? account.team_default),
        membershipRole: String(account.membershipRole ?? account.membership_role ?? '').trim(),
      }
    },
    staleTime: 10_000,
  })
  const identity = query.data
  const [username, setUsername] = useState('')
  const [savedUsername, setSavedUsername] = useState<string | null>(null)
  const bootstrapped = Boolean(identity?.bootstrapped && identity.userID.trim() !== '')
  const hasTeam = Boolean(identity?.teamID.trim())

  useEffect(() => {
    if (identity) setUsername(identity.username)
  }, [identity])

  const renameMutation = useMutation({
    mutationFn: async (nextUsername: string): Promise<{ userID: string; username: string }> => {
      const updated = await requestJson<UsernameUpdateResponse>('/v1/account/username', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: nextUsername }),
      })
      const updatedUsername = String(updated.username ?? '').trim()
      const updatedUserID = String(updated.userID ?? updated.user_id ?? '').trim()
      if (!identity || !updatedUsername || updatedUserID !== identity.userID) {
        throw new Error('Username update returned an invalid user identity')
      }
      return { userID: updatedUserID, username: updatedUsername }
    },
    onSuccess: (updated) => {
      queryClient.setQueryData<AccountIdentity>(['desktop-account-context'], (current) => current ? { ...current, username: updated.username } : current)
      updateDesktopSessionUsername(updated.username)
      setUsername(updated.username)
      setSavedUsername(updated.username)
    },
  })

  const submitUsername = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setSavedUsername(null)
    const nextUsername = username.trim()
    if (!nextUsername || nextUsername === identity?.username || renameMutation.isPending) return
    renameMutation.mutate(nextUsername)
  }

  return (
    <div className="grid gap-6">
      <div className="grid gap-2">
        <div className="flex items-center gap-3">
          <div className="grid h-10 w-10 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text)]">
            <UserRound size={18} />
          </div>
          <div>
            <h2 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">Account</h2>
            <p className="text-sm text-[var(--app-text-muted)]">Private account settings for this local daemon.</p>
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
        <>
          <Card className="grid gap-4 border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-5">
            <form className="grid gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3" onSubmit={submitUsername}>
              <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="account-username">
                Username
              </label>
              <div className="flex flex-col gap-2 sm:flex-row">
                <Input
                  id="account-username"
                  name="username"
                  autoComplete="username"
                  value={username}
                  onChange={(event) => {
                    setUsername(event.target.value)
                    setSavedUsername(null)
                  }}
                  disabled={renameMutation.isPending}
                  aria-describedby="account-username-help"
                />
                <Button
                  type="submit"
                  variant="primary"
                  disabled={renameMutation.isPending || !username.trim() || username.trim() === identity.username}
                >
                  {renameMutation.isPending ? 'Saving…' : 'Save username'}
                </Button>
              </div>
              <p id="account-username-help" className="text-xs leading-5 text-[var(--app-text-muted)]">
                Renaming updates this user in place. Your user ID remains {identity.userID}.
              </p>
              {renameMutation.error ? (
                <p className="text-sm text-[var(--app-warning)]" role="alert">
                  {renameMutation.error instanceof Error ? renameMutation.error.message : 'Failed to update username'}
                </p>
              ) : null}
              {savedUsername ? <p className="text-sm text-[var(--app-success)]">Username changed to {savedUsername}.</p> : null}
            </form>
            <ReadonlyAccountRow label="Account mode" value={hasTeam ? 'Team shared' : 'User private'} />
            {hasTeam ? (
              <>
                <ReadonlyAccountRow label="Team name" value={identity.teamDisplayName || '—'} />
                <ReadonlyAccountRow label="Team ID" value={identity.teamID} />
                <ReadonlyAccountRow label="Membership role" value={identity.membershipRole || '—'} />
              </>
            ) : null}
            <p className="text-xs leading-5 text-[var(--app-text-muted)]">
              This account is private to your user. Team sharing will be available in a future update.
            </p>
          </Card>

          {!hasTeam ? (
            <Card className="grid gap-4 border-[var(--app-border)] bg-[var(--app-surface)] p-5">
              <div className="grid gap-1 opacity-75">
                <h3 className="text-base font-semibold text-[var(--app-text-muted)]">Upgrade to a team</h3>
                <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                  Team sharing is not available yet. Your account remains User private for now.
                </p>
              </div>
              <div>
                <button
                  type="button"
                  disabled
                  className="cursor-not-allowed rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-2 text-sm font-semibold text-[var(--app-text-muted)] opacity-70"
                >
                  Teams are coming soon
                </button>
              </div>
            </Card>
          ) : (
            <Card className="border-[var(--app-success-border)] bg-[var(--app-success-bg)] p-5 text-sm leading-6 text-[var(--app-success)]">
              Team mode is enabled for {identity.teamDisplayName || 'this account'}. A second team cannot be created.
            </Card>
          )}
        </>
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
