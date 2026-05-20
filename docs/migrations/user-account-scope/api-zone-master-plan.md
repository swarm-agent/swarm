# API zone master implementation plan

This plan consolidates the six refined zone reports in `docs/migrations/user-account-scope/zone-reports/` for Checkpoint 2. It is the dispatch plan for real implementation agents.

## Non-negotiable invariants

- Identity/account objects remain Pebble-native.
- Every protected route derives `UserID` and `AccountScopeID` only from `api.PrincipalFromRequest(r)` or `requireProductActor`.
- Request headers, body, query, path IDs, peer headers, attach tokens, env vars, CLI flags, and local transport markers are never account authority.
- `TeamID` remains explicit opt-in metadata only; it is never account scope.
- No SQL, IAM, wrappers, shims, broad compatibility forks, or silent fallback reads.
- Existing global mutable keys may be used only as explicit one-way migration inputs. After an account-scoped miss, authenticated reads must not fall back to old global keys.
- Each implementation slice must pass targeted Go tests plus the VM gate before the next dependent slice is declared ready.

## Readiness verdict

The refined zone reports are good enough for implementation dispatch, but not as broad “fix the whole zone” prompts. Real agents must receive the slices below, in order, with blockers respected.

## Priority order

### P0 — Security-critical principal boundary hardening

These slices close routes that currently trust auth wrapper presence, local transport, peer auth, or body IDs without a route-local trusted principal/account check.

1. **Z1 credential/auth route guards and credential key scope**
   - Ready now: `Z1-S001`, `Z1-S002`, `Z1-S003`, `Z1-S004`, `Z1-R54`, `Z1-R55`, `Z1-R56`, `Z1-R57`, `Z1-R58`, `Z1-R59`, `Z1-R60`.
   - Blocked portions: `Z1-R61`, `Z1-R63`, `/ws`, onboarding post-bootstrap counts.
   - Must deliver: account-scoped auth credential primary keys, active credential keys, OAuth session account binding, no legacy codex fallback after scoped miss.

2. **Z2 local destructive git/workspace path guards for unblocked workspace storage**
   - Ready now: `Z2-S001`, `Z2-S003`, workspace CRUD/current/list/select/resolve/browse/folder/theme/rename/move/delete routes, git status/commit/realtime/inspect/apply routes except peer/local unproven variants.
   - Blocked portions: todos/session joins, topology bindings, peer workspace routes, peer/local git sync account binding.
   - Must deliver: account-scoped workspace entries/current, account-scoped worktree config, trusted account-owned path resolver.

3. **Z4B machine-affecting deploy/update guards**
   - Ready now: `Z4B-S001`, `Z4B-S002`, `Z4B-S006`, deploy container list/create/settings/action/delete, remote deploy list/settings/delete/update/approve, shutdown/update principal guards.
   - Blocked portions: attach/bootstrap/sync routes, managed-host session routes, managed target/git/update routes.
   - Must deliver: deploy/remote records with `UserID` and `AccountScopeID`, account indexes, unauth shutdown/update denial.

4. **Z4A pairing/groups/targets/container/mirror foundational ownership**
   - Ready now: `Z4A-S001`, `Z4A-S002`, `Z4A-S004`, `Z4A-S005`, `Z4A-S006` partial, `Z4A-S007` partial; protected pairing approve/list/state, groups, current target, mirror, local container list/action/prune.
   - Blocked/unproven portions: public/bootstrap pairing boundary (`R75`, `R76`, `R79`, `R80`, `R82`, `R94`), topology derived records, workspace mounts, session route lookups, delete cascades.
   - Must deliver: account-bound invites/enrollments/trusted peers, groups/current group, current target, mirror peer relation filters, local container/profile account fields.

### P1 — Foundational account-scoped domain stores

5. **Z3 agents/flows/sessions/permissions core stores**
   - Ready now: `Z3-S001`, `Z3-S002`, `Z3-S003`, `Z3-S004`, `Z3-R135`, `Z3-R136`, `Z3-R170`-`Z3-R173`, `Z3-R223`, `Z3-R241`, `Z3-R242`.
   - Blocked portions: context sources (`Z2` workspace verifier), peer/local flow/session/permission routes (`Z4` peer/runtime binding), managed policy/provider inputs (`Z5`).
   - Must deliver: account fields/indexes for mutable agents, flows, sessions/messages/lifecycle/plans/usage, permission policy/records.

6. **Z5 independent user settings/provider state**
   - Ready now: vault metadata/status/enable/lock/disable/export/import; model preference/favorites/catalog proof; provider readiness split; voice/STT config/profile routes.
   - Blocked portions: custom tools/integration sessions (`Z3`), image threads/assets (`Z2`), UI settings device-global split (`Z4A`), child vault/deploy sync (`Z4`).
   - Must deliver: account-scoped vault metadata/unlock cache, model preference/favorites, provider readiness from principal account credentials, voice config/profiles.

### P2 — Cross-zone dependent route completion

7. **Z2 + Z3 joins**
   - Todos, workspace overview, context sources, session `cwd` joins.
   - Requires Z2 workspace verifier and Z3 session account verifier.

8. **Z2 + Z4 topology/peer workspace/managed workspace flows**
   - Managed workspace replication, workspace bindings, peer workspace routes, managed-host workspace git.
   - Requires Z4A/Z4B account-bound target/runtime/peer relation helpers.

9. **Z4B + Z3/Z5 sync and managed-host sessions**
   - Managed-host session open/message/run/stop/stream/event and deploy/remote credential/agent/model/permission sync.
   - Requires Z3 session/run/permission account ownership and Z5 credential/model/account-scoped exporters.

10. **Z5 integrations/custom tools/image/UI completion**
    - Requires Z3 agent/session binding, Z2 workspace/image ownership, and device-global UI split proof.

## Blocker dependency graph

| Blocker | Needed by | Owner slice | Required output |
|---|---|---|---|
| Canonical two-account fixture | All zones | Z1/P0 | Product session/JWT fixture using Pebble identity only; no fake headers/context. |
| Account-scoped credentials | Z1, Z5, Z4 sync | Z1/P0 + Z5/P1 | Credential primary, active, tag indexes keyed by account; export/import account filtered. |
| Workspace ownership verifier | Z2, Z3, Z4A, Z4B, Z5 | Z2/P0 | Function/service path that accepts principal/account and proves workspace/current/path ownership. |
| Session ownership verifier | Z2, Z3, Z4A, Z4B, Z5 | Z3/P1 | Account-filtered session lookup usable before subresource writes. |
| Peer/runtime/target account binding | Z2, Z3, Z4A, Z4B | Z4A/Z4B/P0 | Trusted peer/runtime/target relation lookup returning account scope from persisted records. |
| Deploy/remote account records | Z4A, Z4B, Z5 | Z4B/P0 | Deploy container and remote session records include user/account fields and by-account indexes. |
| Provider/model/credential readiness split | Z3, Z4B, Z5 | Z5/P1 | Static catalog stays shared; readiness/credentials use principal account. |
| Device-global UI boundary | Z5 with Z4A | Z5/P2 + Z4A | Account UI settings separated from local swarm machine identity rename/update. |

## Dispatch slices for real agents

### Slice A — Z1 credential/account fixture agent

Status: **implementation staged; VM proof pending**.

Scope:
- Implement `Z1-S001`, `Z1-S002`, `Z1-S003`, `Z1-S004`, `Z1-R54`-`Z1-R60`.
- Preserve proven `Z0` public health/ready, desktop session bootstrap, `/me`, and team upgrade behavior.
- Do not implement blocked cleanup, attach rotate, `/ws`, or onboarding counts until dependency owners land.

VM proof:
- Two accounts create same provider/id credentials without cross-read.
- Active credential differs per account.
- OAuth status/complete denies cross-account session IDs.
- Scoped codex miss does not read `auth/codex/default`.

### Slice B — Z2 workspace/worktree/path foundation agent

Status: **ready after Slice A fixture is available**.

Scope:
- Implement `Z2-S001`, `Z2-S003`, and unblocked workspace/worktree/git route guards.
- Add account-owned path resolver used by later Z3/Z4/Z5 slices.
- Do not implement peer/local git sync, topology binding, todos/session joins, or managed workspace replication until blockers land.

VM proof:
- Account A/B workspace list/current/select/rename/delete/theme/directories isolation.
- No authenticated route reads `workspace/current` after account-scoped miss.
- Worktree config has no global fallback.
- Destructive git sync apply to another account path denied.

### Slice C — Z4A target/peer/group/mirror foundation agent

Status: **partially ready after Slice A; topology/mount/session pieces wait on B/D**.

Scope:
- Implement account-bound invites/enrollments/trusted peers, groups/current group, current target, mirror peer filters, local container/profile account fields for non-mount operations.
- Decide and document principal-gated vs account-bound bootstrap behavior for unproven pairing routes before implementing them.

VM proof:
- Cross-account group/target/invite/enrollment/mirror access denied.
- Peer mirror routes map peer auth to trusted peer account.
- Unproven public/bootstrap routes expose no account-owned data or require principal.

### Slice D — Z3 agent/session/permission foundation agent

Status: **ready after Slice A; path/context pieces wait on Slice B; peer/local pieces wait on Slice C**.

Scope:
- Implement account-scoped agent profiles/custom tools where owned by Z3, flows, sessions, permissions, and notifications where source account is known.
- Provide session ownership verifier for Z2/Z4/Z5.

VM proof:
- Account A/B agent/flow/session/permission isolation.
- Session subroutes deny cross-account session IDs.
- Permission policy is not global.

### Slice E — Z4B deploy/remote/update foundation agent

Status: **ready after Slice A; managed-host/sync pieces wait on B/C/D/F**.

Scope:
- Implement account-scoped deploy container store, remote deploy store, update/shutdown principal guards.
- Preserve static/no-state package defaults/validate and retired remote create/start behavior.

VM proof:
- Account A/B deploy and remote session isolation.
- Unauthenticated shutdown/update/apply/run denied.
- Existing global records without account fields block clearly or migrate explicitly; no fallback.

### Slice F — Z5 settings/provider/vault foundation agent

Status: **ready after Slice A for independent settings; dependent pieces wait on B/D/E**.

Scope:
- Implement account-scoped vault metadata/unlock, credential export/import consumers, model preference/favorites, provider readiness split, voice/STT config/profile routes.
- Do not finish custom tools, image assets, integrations, UI device-global split, or child vault sync until dependencies land.

VM proof:
- Account A/B vault, model, favorites, voice, provider readiness isolated.
- Static catalog has no account credential state.
- Export/import does not leak credentials across accounts.

## Zone status summary

| Zone | Report | Implementation status |
|---|---|---|
| Z0/Z1 | `zone-reports/z0-z1-auth-local-identity.md` | Slice A implementation staged for auth credential keys/routes and Codex OAuth account binding; VM proof pending. `/ws`, attach rotate, credential delete cleanup, and onboarding post-bootstrap counts remain blocked. |
| Z2 | `zone-reports/z2-workspaces-worktrees-git.md` | Ready for workspace/current/worktree/path foundation after Slice A; peer/topology/todo/media dependencies remain. |
| Z3 | `zone-reports/z3-agents-sessions-flows-runs.md` | Ready for core agent/flow/session/permission stores after Slice A; peer/local/context dependencies remain. |
| Z4A | `zone-reports/z4a-swarm-topology-local-containers.md` | Ready for groups/targets/pairing protected records/mirror partial; bootstrap/topology/mount/session pieces blocked. |
| Z4B | `zone-reports/z4b-deploy-managed-hosts-runtime.md` | Ready for deploy/remote/update foundation; managed-host/session/sync/target pieces blocked. |
| Z5 | `zone-reports/z5-integrations-credentials-settings.md` | Ready for vault/model/provider/voice foundation; image/custom-tool/integration/UI split dependencies remain. |

## VM gate requirements

Every dispatched implementation agent must:

1. Run targeted Go tests named in its zone report.
2. Run the Checkpoint 1 VM proof first or include it in the Checkpoint 2 VM gate.
3. Create two product users/accounts using canonical Pebble identity/session APIs.
4. Exercise browser cookie, `X-Swarm-Token`, and local transport principal paths where applicable.
5. Inspect Pebble keys/records and prove account-scoped keys or fields exist.
6. Prove denied cross-account/spoof requests leave no records behind.
7. Document VM commands and proof output before marking any route complete.

## Do-not-dispatch list until blockers resolve

Do not launch broad agents for these as standalone “fix it” tasks:

- Z2 peer/local git sync and peer workspace replication until Z4 peer/runtime binding exists.
- Z3 peer/local flow/session/permission routes until Z4 peer/runtime/session route binding exists.
- Z4A public/bootstrap pairing boundary until product decision: principal-gated or account-bound bootstrap tokens.
- Z4B attach/bootstrap/sync/import/export until account-bound deployment/session plus Z3/Z5 ownership APIs exist.
- Z5 integrations/custom tools/image/UI settings until Z3 session/agent, Z2 workspace/image, and device-global UI boundaries exist.

## Next action

Run the Slice A VM gate first. After Slice A passes VM proof, dispatch Slice B and the independent portions of Slices E/F. Dispatch C/D in parallel only where their blockers are satisfied by Slice B/A outputs. Do not start P2 dependent route completion until the owner verifier APIs are merged and VM-proven.
