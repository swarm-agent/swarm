# API zone master implementation plan

This document is the dispatch source of truth for the Pebble-native user/account API scoping migration. It supersedes the original broad zone order where it conflicts with the product reality below.

## Product reality and migration target

- Remote SSH / remote deploy containers are retired. Do not spend implementation time adding account-scoped remote deploy stores or indexes.
- The active runtime product path is:
  1. local Swarm account/session/workspace/API use,
  2. local containers,
  3. account-scoped user/provider/model/voice settings,
  4. managed hosting,
  5. flows.
- Managed hosting and flows are intentionally last. The speed-run goal is to make the product buildable and manually testable for real local use before starting managed hosting and flow work.

## Non-negotiable invariants

- Identity/account objects remain Pebble-native.
- Every protected route derives `UserID` and `AccountScopeID` only from `api.PrincipalFromRequest(r)` or `requireProductActor`.
- Request headers, body, query, path IDs, peer headers, attach tokens, env vars, CLI flags, and local transport markers are never account authority.
- `TeamID` remains explicit opt-in metadata only; it is never account scope.
- No SQL, IAM, wrappers, shims, broad compatibility forks, duplicate behavior paths, or silent fallback reads.
- Existing global mutable keys may be used only as explicit one-way migration inputs. After an account-scoped miss, authenticated reads must not fall back to old global keys.
- Implementation is organized into small micro-slices inside a named slice group. Micro-slices are coding checkpoints only; do not write tests or run test commands during the group unless explicitly approved.
- After all micro-slices in a group are complete, run one group-level VM gate before the next dependent slice group is declared ready.
- VM gates must create two product users/accounts through canonical Pebble identity/session APIs and must inspect Pebble records or keys for account scope evidence.

## Current completed foundation

These are already complete or VM-proven in this migration thread and are prerequisites for the new order:

1. **Checkpoint 1 identity/account foundation**
   - Pebble-native user/account records.
   - Canonical principal resolver.
   - Product JWT/session identity proof.
   - Browser cookie, `X-Swarm-Token`, and local transport principal proof.

2. **Slice A — credential/account fixture foundation**
   - Account-scoped auth credential keys/routes and Codex OAuth account binding.
   - Two-account credential isolation proof.

3. **Slice B — workspace/worktree/path foundation**
   - Account-scoped workspace/current/worktree/path foundation.
   - Peer-managed workspace DB repair and targeted VM proof.
   - Account-owned path resolver is now the prerequisite for local container mount checks.

## Revised speed-run implementation order

### Slice 0 — migration plan correction and stale remote-deploy retirement notes

Status: **do first / docs-only before more code**.

Scope:
- Treat old remote SSH deploy as retired/dead in the migration plan.
- Split local containers out from old Z4B deploy/remote wording.
- Move managed hosting and flows to the final phases.
- Keep stale remote deploy route cleanup as a retirement/blocking task only, not an account-scoped product migration.

Do not implement:
- Remote deploy session account indexes.
- Remote SSH container create/start/settings/update flows.

Required proof:
- Docs identify local containers, managed hosting, and flows as separate phases.
- `api-route-zone-matrix.md` summary no longer dispatches remote deploy as a live foundation slice.

### Slice 1 group — core sessions/API usability foundation, excluding flows

Status: **next implementation group after docs correction**.

Goal:
- Make the app usable after account/workspace bootstrap: build it, create account, use normal API, create/list/read/update sessions.
- Provide the session ownership verifier needed by later local-container, settings, managed-hosting, permissions, agents, and flow work.

Execution rule for this group:
- Do **not** write new tests during Slice 1A through Slice 1D.
- Do **not** run test commands during Slice 1A through Slice 1D unless the user explicitly overrides this rule.
- Each micro-slice must stop after the smallest compile-safe implementation checkpoint and report changed files plus the next exact checkpoint.
- Run one VM-focused verification pass only after Slice 1A through Slice 1D are complete and ready to be verified as a group.

#### Slice 1A — Pebble session storage ownership only

Status: **complete** — commit `41307e7`; no tests written or run.

Scope:
- Add account ownership fields/indexes for core session storage:
  - sessions,
  - messages,
  - lifecycle,
  - plans,
  - usage,
  - any session subresource directly touched by normal `/v1/sessions` routes.
- Primary files:
  - `swarmd/internal/store/pebble/session_store.go`
  - `swarmd/internal/store/pebble/session_usage_store.go` if usage records are touched.
  - `swarmd/internal/store/pebble/keys.go`
- Add account-scoped store helpers, but do not broadly convert API handlers yet.
- No tests written and no tests run in this micro-slice.

Stop condition:
- Store models and helpers are account-capable.
- Existing call sites are compile-adjusted only as needed.
- Report follow-up API call sites that still use global session helpers.

#### Slice 1B — `/v1/sessions` collection route only

Status: **complete** — `/v1/sessions` list/create now use canonical principal account scope; no tests written or run.

Scope:
- Convert only `/v1/sessions` list/create in `swarmd/internal/api/server.go`.
- Resolve principal using `api.PrincipalFromRequest(r)`.
- GET lists only sessions for `Principal.AccountScopeID`.
- POST stamps new sessions with `Principal.UserID` and `Principal.AccountScopeID`.
- Request body/query/metadata may be locators only; never account authority.
- Use Slice B workspace/path ownership where session creation depends on `cwd` or workspace selection.
- No tests written and no tests run in this micro-slice.

Stop condition:
- Collection list/create is account-scoped.
- No `/v1/sessions/` subroute conversion beyond compile-required helper wiring.

#### Slice 1C — `/v1/sessions/{id}` ownership verifier and basic subroutes

Status: **complete** — `/v1/sessions/` ID/subroute branches now use the shared canonical principal/account ownership verifier before local reads/writes/proxy decisions; no tests written or run.

Scope:
- Add a shared session ownership verifier for `/v1/sessions/` path branches.
- Use it before read/update/message/metadata/title/mode/lifecycle branches that are part of normal local session use.
- Session subresources inherit the persisted session account; path IDs remain locators only.
- No tests written and no tests run in this micro-slice.

Stop condition:
- Account B cannot be authorized by code path to read/update/message Account A's session by ID.
- Shared helper is available for later permissions, local-container, managed-hosting, and flow work.

#### Slice 1D — remaining normal local session subresources

Status: **complete** — remaining normal local `/v1/sessions/` plan/usage/run/stream/proxy branches use the shared ownership verifier before local execution or proxying, and run/stream execution reuses the verified principal; no tests written or run.

Scope:
- Finish only normal local `/v1/sessions/` subresources required for out-of-box session usability, such as plans, usage, run, stream/proxy branches where they are directly under `/v1/sessions/`.
- Keep all writes attached to the verified session account.
- If minimal session creation needs agent profile reads, preserve built-in templates as shared read-only data and defer mutable agent account scoping unless it is absolutely required for session creation to work.
- No tests written and no tests run in this micro-slice.

Stop condition:
- Normal local session use is account-scoped end-to-end.
- Broad agent/profile and permission conversion remain separate follow-up groups.

#### Slice 1E — group VM verification only after 1A–1D

Status: **complete** — VM proof passed with two product accounts and a real Fireworks model run using `accounts/fireworks/models/minimax-m2p7`; evidence directory `.tmp/user-account-scope/slice-1-sessions/20260521T043028Z-vm-proof`.

Scope:
- Add or update the VM verification harness only after 1A through 1D are complete.
- Run one VM proof with two product accounts:
  - Account A creates session.
  - Account B cannot list/read/update/message Account A session by ID.
  - Session subresources inherit account scope.
  - Pebble records contain account fields or account indexes.
  - Denied cross-account session probes leave no records behind.

Proof notes:
- Harness: `scripts/vm/slice-1-sessions-account-scope.sh`.
- The proof adds a real Fireworks credential through `/v1/auth/credentials`, verifies the credential connection, creates a session with model `accounts/fireworks/models/minimax-m2p7`, and runs `/v1/sessions/{id}/run` successfully.
- Account B list/read/message/metadata/preference/plans/usage/run probes against Account A's session all return not-found isolation responses.
- Pebble inspection confirms the session, messages, plans, and usage summary carry Account A `AccountScopeID`, and account indexes are present.

Explicitly defer from Slice 1 group:
- `/v3/flows` and `/v3/flows/`.
- Peer/local flow routes.
- Peer/local session routes.
- Managed-host session routes.
- Flow run/report/mirrored flow storage.
- Broad permissions conversion.
- Broad mutable agent/profile/custom-tool conversion.

### Slice 2 — local containers and local deployment records

Status: **run after Slice 1 and current Slice B workspace/path proof**. Split into micro-slices 2A–2F because this is a critical API/storage path.

Goal:
- Make local containers work under account scoping and prove them in the existing local container harness.
- Keep each implementation step small enough to audit. Do not mix in managed hosting, remote deploy, flows, broad topology/groups/mirror conversion, or package/workspace scan routes unless the local harness proves they are required.

#### Slice 2A — storage/account primitives only

Status: **complete** — local/profile/deploy container records now carry `UserID`/`AccountScopeID` plus account-aware Pebble helpers; no tests run.

Scope:
- Add `UserID` and `AccountScopeID` to:
  - `SwarmLocalContainerRecord`.
  - `ContainerProfileRecord`.
  - local-use `DeployContainerRecord`.
- Add account-aware store methods for list/get/delete/update paths needed by later slices.
- Ensure account-scoped misses do not fall back to global list/read paths.
- Preserve existing global keys only for explicit one-way migration/backfill needs, not runtime authorization fallback.
- No route behavior changes in this micro-slice.

Primary files:
- `swarmd/internal/store/pebble/swarm_local_container_store.go`
- `swarmd/internal/store/pebble/swarm_container_profile_store.go`
- `swarmd/internal/store/pebble/deploy_container_store.go`
- `swarmd/internal/store/pebble/keys.go`

#### Slice 2B — principal plumbing for profiles and read-only local container routes

Status: **complete** — profile list/delete and read-only local container runtime/list routes use canonical principal plumbing; no tests run.

Scope:
- Gate and account-scope:
  - `/v1/swarm/containers/profiles`
  - `/v1/swarm/containers/profiles/delete`
  - `/v1/swarm/containers/local/runtime`
  - `/v1/swarm/containers/local`
- Use only `api.PrincipalFromRequest` as identity authority.
- Profile/container lists filter by principal account.
- Deletes verify same-account ownership before mutation.
- Runtime status may remain machine-global only after principal gating; it must not leak account-owned records.

Primary files:
- `swarmd/internal/api/swarm_container_profiles.go`
- `swarmd/internal/api/swarm_local_containers.go`
- `swarmd/internal/containerprofiles/service.go`
- `swarmd/internal/localcontainers/service.go`

#### Slice 2C — create/upsert with Z2 workspace mount verification

Status: **complete** — profile upsert and local container create stamp canonical principal ownership and verify mounts through the account-owned workspace resolver; no tests run.

Scope:
- Account-scope create/upsert routes:
  - `/v1/swarm/containers/profiles/upsert`
  - `/v1/swarm/containers/local/create`
- Stamp `UserID` and `AccountScopeID` from canonical principal.
- Treat request-supplied workspace paths as locators only.
- Verify every workspace mount through the Slice B/Z2 account-owned path resolver before storing or starting a container.
- Reject body/query/header `user_id` or `account_scope_id` as authority.

Primary files:
- `swarmd/internal/api/swarm_container_profiles.go`
- `swarmd/internal/api/swarm_local_containers.go`
- `swarmd/internal/containerprofiles/service.go`
- `swarmd/internal/localcontainers/service.go`
- workspace/path resolver files as required by the existing Z2 implementation.

#### Slice 2D — local container lifecycle mutations and update plan/job

Status: **complete** — lifecycle/update handlers now require canonical principal, route through context, and service/store paths list/get/update/delete only principal-account local container records; no tests run.

Scope:
- Account-scope local lifecycle routes:
  - `/v1/swarm/containers/local/update-job`
  - `/v1/swarm/containers/local/action`
  - `/v1/swarm/containers/local/delete`
  - `/v1/swarm/containers/local/prune-missing`
  - `/v1/update/local-containers`
- Every operation must load/operate only on principal-account container records.
- Delete/prune cascades must not touch unscoped cross-zone state unless the target record is already proven same-account.
- Update plan/job must exclude other accounts' containers.

Primary files:
- `swarmd/internal/api/swarm_local_containers.go`
- `swarmd/internal/api/update.go`
- `swarmd/internal/localcontainers/service.go`
- `swarmd/internal/store/pebble/swarm_local_container_store.go`

#### Slice 2E — local deploy container records only

Scope:
- Account-scope only local lifecycle-ready deploy routes:
  - `/v1/deploy/container/runtime`
  - `/v1/deploy/container`
  - `/v1/deploy/container/create`
  - `/v1/deploy/container/settings`
  - `/v1/deploy/container/action`
  - `/v1/deploy/container/delete`
- Stamp local `deploy/container/<id>` records from principal.
- List/action/settings/delete verify principal account.
- Preserve package defaults/validate static/no-state behavior if touched.

Primary files:
- `swarmd/internal/api/deploy_container.go`
- `swarmd/internal/deploy/service.go`
- `swarmd/internal/store/pebble/deploy_container_store.go`

#### Slice 2F — group verification gate only

Scope:
- Extend the existing local replicate/local container harness for two product users/accounts.
- Account A can create/use a local container.
- Account B cannot list/action/delete Account A local container or local deployment record by ID.
- Account A and B can have independent local container records.
- Workspace mounts are checked through the account-owned path resolver.
- Pebble shows `swarm/local_container` and local `deploy/container` records include account scope or account indexes.
- No global list/read fallback after scoped miss.

Primary file:
- `tests/swarmd/local_replicate_e2e.sh`

Suggested command shape for Slice 2F only:

<copy label="targeted local container tests">cd swarmd && go test ./internal/api ./internal/localcontainers ./internal/store/pebble -run 'LocalContainer|DeployContainer|UpdateLocalContainer|Principal|Account'</copy>

<copy label="VM local replicate harness">./scripts/swarm-harness-vm.sh local-replicate -- --verify-topology-cleanup</copy>

Explicitly defer until after Slice 2F:
- Managed-host session open/message/run/stop/stream/event.
- Managed-host update/run/status.
- Deploy attach/bootstrap/sync/export/import/apply routes.
- Remote deploy session list/settings/delete/update/approve account migration.
- Peer/local transport managed-host duplicates.
- Broad topology/groups/mirror conversion.
- Package suggest/workspace scanning unless required by the local container harness.

### Slice 3 — Z5 local usability foundation

Status: **run after local containers are VM-proven, or in parallel only if it does not touch local-container/managed-host state**.

Goal:
- Make normal local product settings usable per account before managed hosting/flows.

Scope:
- Account-scope independent Z5 state:
  - vault metadata/status/enable/lock/disable/export/import where needed for local credential usability,
  - model preference and model favorites,
  - static model/catalog proof remains shared and credential-free,
  - provider readiness split: static catalog shared, credential readiness by principal account,
  - voice/STT config/profile/status/test routes.
- Split account-owned user preferences from device-global machine identity before mutating `/v1/ui/settings` broadly. If the split is not complete, do not finish UI settings in this slice.

Explicitly defer:
- Custom tools if they require Z3 agent execution binding.
- Integrations and builder sessions.
- Image assets/generation if they require workspace/image ownership not proven in the slice.
- Child vault sync and deploy credential sync.
- Managed hosting sync/export/import.

Primary files:
- `swarmd/internal/api/vault.go`
- `swarmd/internal/store/pebble/auth_store.go`
- `swarmd/internal/api/server.go` model/provider/voice/UI handlers
- `swarmd/internal/store/pebble/model_store.go`
- `swarmd/internal/store/pebble/model_favorite_store.go`
- `swarmd/internal/store/pebble/voice_store.go`
- `swarmd/internal/store/pebble/ui_chat_settings_store.go` only for explicit account-settings/device-global split work.

Required tests/VM proof:
- Two product accounts have isolated model defaults/favorites.
- Provider readiness differs by account credential state without leaking another account credential status.
- Voice profiles/config are isolated by account.
- Vault/export/import, if included, never exports or imports another account credential.
- Static catalogs contain no account fields or credential readiness.
- Pebble records or indexes prove account scope.

Suggested command shape:

<copy label="targeted Z5 usability tests">cd swarmd && go test ./internal/api ./internal/auth ./internal/model ./internal/voice ./internal/store/pebble -run 'Vault.*Account|Credential.*Account|Model.*Account|Voice.*Account|Provider.*Account|Principal'</copy>

### Slice 4 — out-of-box local product acceptance gate

Status: **required before managed hosting or flows**.

Goal:
- Prove the local product can be built and manually tested for real local use, with managed hosting and flows still disabled/deferred.

Required VM proof:
- Fresh VM / clean Pebble state.
- Build succeeds.
- First account bootstrap succeeds.
- Browser cookie and `X-Swarm-Token` principal paths still resolve the same trusted principal shape.
- Account can create workspace and use workspace APIs.
- Account can create/list/read/update a normal session.
- Account can configure model/provider/voice basics from account-scoped state.
- Account can create/use/list/action/delete a local container via the local harness.
- A second account cannot read or mutate the first account's workspace, session, settings, local container, or local deployment records.
- Pebble inspect confirms account-scoped records/indexes for all converted sections.
- Denied cross-account probes leave no records behind.

Required output:
- Commit evidence and VM proof artifact path before starting managed hosting.

### Slice 5 — managed hosting foundation

Status: **deferred until Slice 4 passes**.

Scope:
- Managed hosting target/runtime/peer/account binding.
- Managed-host sessions and updates.
- Managed workspace/topology/account binding.
- Deploy attach/bootstrap/sync/export/import/apply only after account-bound deployment/session plus Z3/Z5 ownership APIs exist.

Required prerequisite outputs:
- Session ownership verifier from Slice 1.
- Workspace/path verifier from Slice B.
- Local container/deploy account proof from Slice 2.
- Provider/credential/model readiness proof from Slice 3.

### Slice 6 — flows foundation and managed flow completion

Status: **final major migration phase after managed hosting foundation**.

Scope:
- `/v3/flows` and `/v3/flows/` account-scoped flow definitions/status/outbox/runs.
- Peer/local flow apply/report only after managed peer/runtime account binding is proven.
- Flow/session/permission joins and managed run/report paths.

Required prerequisite outputs:
- Managed hosting account binding from Slice 5.
- Session and permission ownership from Slice 1 or its follow-up.
- Provider/model/account-scoped readiness from Slice 3.

## Remote deploy retirement rule

`swarmd/internal/api/remote_deploy.go` is not a live product migration target. Remote SSH deploy routes must not receive new account-scoped product storage as part of the speed-run. If touched because a gate fails, make behavior clearly retired/no-state or explicitly denied. Do not implement fallback reads from old remote deploy records.

## VM gate requirements for every slice

Every dispatched implementation agent must:

1. Run targeted Go tests named in the slice.
2. Run the Checkpoint 1 principal proof first or include equivalent checks in the slice VM gate.
3. Create two product users/accounts using canonical Pebble identity/session APIs.
4. Exercise browser cookie and `X-Swarm-Token`; exercise local transport where the slice owns local transport behavior.
5. Inspect Pebble keys/records and prove account-scoped keys or fields exist.
6. Prove denied cross-account/spoof requests leave no records behind.
7. Document exact VM command, pass/fail output, and proof artifact path before marking any route complete.

## Do-not-dispatch list until blockers resolve

Do not launch broad agents for these as standalone “fix it” tasks:

- Remote deploy account migration. Remote deploy is retired/dead.
- Managed hosting before Slice 4 passes.
- Flows before managed hosting account binding exists.
- Peer/local flow/session/permission routes before managed peer/runtime/session route binding exists.
- Deploy attach/bootstrap/sync/import/export until account-bound local/managed deployment/session plus Z3/Z5 ownership APIs exist.
- Integrations/custom tools/image assets until Z3 session/agent and Z2 workspace/image ownership are proven.

## Next action

Implement Slice 0 documentation correction, then dispatch Slice 1 core sessions/API usability. After Slice 1 VM proof, dispatch Slice 2 local containers with the extended local container harness. Do not start managed hosting or flows until Slice 4 out-of-box local product acceptance passes.
