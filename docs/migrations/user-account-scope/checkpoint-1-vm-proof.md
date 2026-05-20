# Checkpoint 1 VM proof

Status: PASS in swarm-harness VM on 2026-05-20.

## Exact command

```sh
scripts/vm/checkpoint-1-user-account-foundation.sh
```

The script self-dispatches through `scripts/swarm-harness-vm.sh` and refuses to count host-local execution as VM proof unless explicitly overridden for debugging.

## Observed VM run

- Evidence dir: `.tmp/checkpoint-1/user-account-foundation/20260520T104820Z-vm-proof`
- Pebble DB path in guest: `/var/tmp/swarm-checkpoint1-user-account-CT72jb/state/swarmd.pebble`
- App URL: `http://127.0.0.1:20798`
- Desktop URL: `http://127.0.0.1:20799`
- Local transport socket: `<data-dir>/local-transport/api.sock`

## Commands run by gate

```sh
cd swarmd && go test ./internal/store/pebble ./internal/identity ./internal/api -run 'Test(DesktopSession|LocalTransportSession|ProtectedCreateAPIs|Session|Onboarding|.*Identity|.*Principal)'
cd swarmd && go build -o <tmp>/swarmd ./cmd/swarmd
curl /v1/auth/desktop/session before bootstrap
curl /v1/onboarding
curl /v1/auth/desktop/session after bootstrap
curl /me with desktop cookie
curl /me with X-Swarm-Token
curl --unix-socket <data-dir>/local-transport/api.sock /v1/auth/desktop/session
curl --unix-socket <data-dir>/local-transport/api.sock /me with X-Swarm-Token
cd swarmd && go run ./cmd/pebble-inspect --db <db-copy> --check identity-foundation --json
cd swarmd && go run ./cmd/pebble-inspect --db <db-copy> --check no-teams-no-iam --json
```

## Key prefixes scanned

- `identity/user/`
- `identity/auth-subject/`
- `account/scope/`
- `account/user/`
- `account/user-by-user/`
- `identity/team/`
- `identity/membership/`
- `iam/`, `iam/grant/`, `iam/role/`, `iam/permission/`

## Decoded object counts and invariants

`identity-foundation` output:

```json
{"check":"identity-foundation","passed":true,"users":1,"accountScopes":1,"accountUsers":1,"authSubjectIndexes":1}
```

`no-teams-no-iam` output:

```json
{"check":"no-teams-no-iam","passed":true}
```

## JWT/session/TUI proof

Both desktop/browser cookie and header/local-transport paths now prove the same trusted principal shape through the server-side resolver.

Desktop `X-Swarm-Token` `/me` proof:

```json
{
  "type": "user",
  "userID": "user_554221aef84f6fd0e14dd5e018d49388",
  "accountScopeID": "acct_b4642d0fa2469b51c2f22133d91897b2",
  "teamID": null,
  "accountScopeSource": "session",
  "auth_provider": "swarm-desktop-local"
}
```

Local transport session + `X-Swarm-Token` `/me` proof:

```json
{
  "type": "user",
  "userID": "user_554221aef84f6fd0e14dd5e018d49388",
  "accountScopeID": "acct_b4642d0fa2469b51c2f22133d91897b2",
  "teamID": null,
  "accountScopeSource": "session",
  "auth_provider": "swarm-desktop-local"
}
```

## Observed pass/fail

PASS. The VM gate now hard-fails if either desktop `X-Swarm-Token` or local transport `X-Swarm-Token` principal proof does not return HTTP 200 with `type=user`, non-empty `userID`, non-empty `accountScopeID`, `teamID=null`, and `accountScopeSource=session`.

## Limitations

Checkpoint 1 intentionally does not convert all protected APIs, add IAM, add TeamID scope, or implement Checkpoint 2 zone scoping.
