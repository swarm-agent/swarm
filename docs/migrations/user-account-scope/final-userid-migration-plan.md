# Final UserID/account-scope migration plan

This is the finish-line implementation plan for the remaining Pebble-native `UserID` / `AccountScopeID` migration.

It is based on the existing migration reports plus a fresh code pass and explorer runs. Three explorer runs completed: local containers/topology, managed hosting/deploy/topology, and flows. The Z5/vault explorer failed with a model 503, so vault was verified locally from current code.

## Executive summary

The remaining migration is no longer a broad “everything” problem. The critical unfinished center is the distributed runtime graph:

1. **Topology ownership** must become the central account boundary for local containers, managed hosts, routed sessions, peer traffic, and flow targets.
2. **Flows** must be account-scoped and must use topology/peer account bindings for target delivery, apply, report, and scheduler execution.
3. **Managed hosting/deploy sync/session routes** must be completed against account-bound topology and deployment records.
4. **Local containers are mostly migrated**, but they still need a final proof pass because topology records created from local container records do not currently carry account scope.
5. **Vault core routes are already account-aware in current code**, but vault must be tested with local containers and managed hosting sync because child vault unlock/export/import paths still depend on deploy/topology account bindings.

The best finish-line strategy is **topology-first, then flows and managed hosting together**, because flows and managed-host session/sync paths both need the same trusted mapping:

`principal account -> owned target/topology record -> owned deployment/session/workspace/vault resources`

## Non-negotiable invariants

- `UserID` and `AccountScopeID` come only from the canonical product principal:
  - `api.PrincipalFromRequest(r)` for product/browser/API routes.
  - `identity.PrincipalFromContext(ctx)` for service/background paths, but only when the principal was created from persisted account-owned state.
- Body/query/path/header IDs are locators only. They are never authority for account scope.
- Peer auth, attach tokens, bootstrap secrets, local transport, and callback URLs are not account authority by themselves. They must resolve to a persisted account-bound deployment/topology/session route.
- No account-scoped miss may fall back to a global mutable store read.
- Existing global keys are migration inputs only. After conversion, runtime reads must use account-aware records/indexes.
- VM proof must use two product accounts created through canonical identity/session APIs and inspect Pebble records/keys.

## Current verified state

### Done or mostly done

#### Core identity, workspaces, and local sessions

Already treated as the foundation by the existing migration docs. Session routes under `/v1/sessions` are marked complete in `docs/migrations/user-account-scope/api-zone-master-plan.md`.

#### Local container and local deploy storage

Current code has account fields and scoped helpers:

- `swarmd/internal/store/pebble/swarm_local_container_store.go`
- `swarmd/internal/store/pebble/swarm_container_profile_store.go`
- `swarmd/internal/store/pebble/deploy_container_store.go`

`DeployContainerRecord` includes `UserID` and `AccountScopeID` at `swarmd/internal/store/pebble/deploy_container_store.go:27-30`, with account helpers such as `PutForAccount`, `ListForAccount`, and `GetForAccount` at `swarmd/internal/store/pebble/deploy_container_store.go:178-310`.

Local container service paths use account-aware lists and mutation guards, for example `swarmd/internal/localcontainers/service.go` contains `ListForAccount`, account-scoped update planning, account-scoped delete/prune, and account-scoped deployment/credential cleanup.

#### Vault core route plumbing

The older Z5 report is stale for the current `swarmd/internal/api/vault.go`. The current code already resolves principal and calls account-specific auth methods:

- `handleVaultStatus` -> `VaultStatusForAccount` at `swarmd/internal/api/vault.go:21-27`.
- `handleVaultEnable` -> `EnableVaultForAccount` at `swarmd/internal/api/vault.go:50-55`.
- `handleVaultUnlock` -> `UnlockVaultForAccount` at `swarmd/internal/api/vault.go:79-91`.
- `handleVaultLock` -> `LockVaultForAccount` at `swarmd/internal/api/vault.go:107-112`.
- `handleVaultDisable` -> `DisableVaultForAccount` at `swarmd/internal/api/vault.go:136-141`.
- `handleVaultExport` -> `ExportCredentialsForAccount` at `swarmd/internal/api/vault.go:166-171`.
- `handleVaultImport` -> `ImportCredentialsForAccount` at `swarmd/internal/api/vault.go:208-213`.
- `withVaultGate` uses `VaultStatusForAccount` at `swarmd/internal/api/vault.go:235-246`.

Remaining vault work is end-to-end proof and deploy/managed-host child sync integration, not basic route conversion.

### Not done properly yet

#### Topology store and topology service

This is the central blocker.

`swarmd/internal/store/pebble/topology_store.go` models have no `UserID` or `AccountScopeID`:

- `TopologyRuntimeRecord` at lines 20-35.
- `TopologyHostContainerRecord` at lines 37-54.
- `TopologyAttachmentRecord` at lines 56-66.
- `TopologyWorkspaceBindingRecord` at lines 68-82.
- `TopologySessionRouteRecord` at lines 84-95.

Topology keys are global:

- `KeyTopologyRuntimePrefix` through `KeyTopologyMigrationStatusPrefix` at `swarmd/internal/store/pebble/keys.go:86-91`.

Topology read/write helpers are global:

- `Snapshot()` lists global records at `swarmd/internal/store/pebble/topology_store.go:204-239`.
- `GetRuntime`, `PutRuntime`, `ListRuntimes` are global at lines 242-307.
- `GetHostContainer`, `PutHostContainer`, `ListHostContainersByHost` are global at lines 313-382.
- `GetWorkspaceBinding`, `PutWorkspaceBinding`, `ListWorkspaceBindingsBySourcePath` are global at lines 505-572.
- `GetSessionRoute`, `PutSessionRoute`, `ListSessionRoutes` are global at lines 590-637.

The snapshot builder merges global source records:

- `swarmd/internal/topology/service.go:266-480` builds one global `TopologySnapshot`.
- Local containers are loaded with `s.localContainers.List(100000)` at `swarmd/internal/topology/service.go:333-359`.
- Deployments are loaded with `s.deployments.List(100000)` at `swarmd/internal/topology/service.go:363-415`.
- Remote deploy sessions are loaded with `s.remoteDeploys.List(100000)` at `swarmd/internal/topology/service.go:418-439`.

Local container topology sync also drops account ownership when writing topology records:

- `swarmd/internal/localcontainers/topology_sync.go:24-54` maps `SwarmLocalContainerRecord` into `TopologyHostContainerRecord` without account fields.

#### Flows

Flows are not account-scoped yet.

Flow storage has no account fields:

- `FlowDefinitionRecord` at `swarmd/internal/store/pebble/flow_store.go:31-39`.
- `FlowAssignmentStatusRecord` at lines 41-53.
- `FlowOutboxCommandRecord` at lines 55-69.
- `FlowRunSummaryRecord` at lines 71-87.
- `FlowCommandLedgerRecord`, `FlowDueRecord`, and `FlowRunClaimRecord` at lines 89-113.

Flow keys are global:

- `KeyFlowDefinitionPrefix` through `KeyFlowTargetRunClaimPrefix` at `swarmd/internal/store/pebble/keys.go:102-112`.

Flow store helpers read/write global state:

- `PutDefinition`, `GetDefinition`, `ListDefinitions`, `DeleteDefinition` at `swarmd/internal/store/pebble/flow_store.go:123-220`.
- Assignment/outbox/history/target accepted/due/run claim paths are also global across the rest of `flow_store.go`.

API handlers partially require principal only when resolving agent/workspace details, but not as the storage boundary:

- Collection list/create starts at `swarmd/internal/api/flows_v3.go:147-175` and calls global list/create helpers.
- Detail/update/delete starts at `swarmd/internal/api/flows_v3.go:178-213` and loads by global `flowID`.
- History/status/run-now directly read global flow records at `swarmd/internal/api/flows_v3.go:215-288`.
- `createFlowV3` writes global `FlowDefinitionRecord` at `swarmd/internal/api/flows_v3.go:291-329`.
- `updateFlowV3` writes global definitions at `swarmd/internal/api/flows_v3.go:372-413`.
- `deleteFlowV3` deletes global flow records at `swarmd/internal/api/flows_v3.go:416-454`.
- `flowV3ListRecords` merges all definitions and accepted assignments at `swarmd/internal/api/flows_v3.go:477-527`.

Flows must be blocked from final acceptance until they can prove two-account create/list/detail/update/delete/run-now isolation and peer apply/report isolation.

#### Managed hosting and deploy sync/session routes

The deploy store has account fields, but service and route layers still have gaps.

High-risk fallback paths remain in `swarmd/internal/deploy/service.go`:

- `listRecordsForContext` falls back to `s.store.List(limit)` without principal at lines 1756-1760.
- `getRecordForContext` falls back to `s.store.Get(deploymentID)` without principal at lines 1763-1767.
- `persistRecordForContext` falls back to global `persistRecord` without principal at lines 1770-1783.
- `WorkspaceBootstrap` ignores context and loads global deployment with `s.store.Get(input.DeploymentID)` at lines 1810-1815.

Managed host sessions and deploy sync must be tied to account-owned topology/deployment/session route state before they are safe. Existing zone reports identify these files as the main attack points:

- `swarmd/internal/api/managed_host_sessions.go`
- `swarmd/internal/api/deploy_container.go`
- `swarmd/internal/api/swarm_replicate.go`
- `swarmd/internal/api/swarm_managed_workspace_replication.go`
- `swarmd/internal/api/git_sync.go`
- `swarmd/internal/remotedeploy/service.go`
- `swarmd/internal/store/pebble/remote_deploy_store.go`

Remote deploy is mostly retired, but any remaining list/get/settings/delete/update/approve surface must either be account-scoped or explicitly no-state/retired. Do not add new product behavior around remote SSH deploy.

#### Peer and local transport routes

Peer/local routes remain unsafe unless they resolve to persisted account-bound topology/session/flow/deploy records.

Critical families:

- Flow apply/report: `swarmd/internal/api/flows_peer.go`, `swarmd/internal/api/flows_report.go`.
- Peer sessions/permissions: `swarmd/internal/api/routed_sessions.go`, `swarmd/internal/api/routed_permissions.go`.
- Managed-host peer sessions/update: `swarmd/internal/api/managed_host_sessions.go`, `swarmd/internal/api/managed_dev_update.go`.
- Deploy attach/sync/apply local duplicates: `swarmd/internal/api/deploy_container.go` and local transport registrations in `swarmd/internal/api/server_routes.go`.

## Finish-line implementation order

### Phase 1 — Topology owner model and account-scoped indexes

Goal: make topology the account-owned runtime graph.

Implementation tasks:

1. Add `UserID` and `AccountScopeID` to:
   - `TopologyRuntimeRecord`
   - `TopologyHostContainerRecord`
   - `TopologyAttachmentRecord`
   - `TopologyWorkspaceBindingRecord`
   - `TopologySessionRouteRecord`
   - `TopologyMigrationStatusRecord` if status is account-specific.
2. Add account indexes/prefixes in `keys.go`, for example:
   - `topology/runtime_by_account/<account>/<swarmID>`
   - `topology/host_container_by_account/<account>/<hostContainerID>`
   - `topology/attachment_by_account/<account>/<attachmentID>`
   - `topology/workspace_binding_by_account/<account>/<bindingID>`
   - `topology/session_route_by_account/<account>/<sessionID>`
3. Add account-aware store APIs:
   - `SnapshotForAccount(accountScopeID)`
   - `ListRuntimesForAccount(accountScopeID, limit)`
   - `GetRuntimeForAccount(accountScopeID, swarmID)`
   - `PutRuntimeForAccount(record, userID, accountScopeID)`
   - equivalent APIs for host containers, attachments, workspace bindings, and session routes.
4. Keep global primary keys temporarily only as explicit migration inputs and reverse lookup support if needed. Runtime API reads must use account-filtered methods.
5. Update normalization so account fields are trimmed and preserved.
6. Add a one-way topology migration/backfill routine that can assign existing topology rows from source records where possible:
   - local container source -> local container `AccountScopeID`
   - deploy container source -> deploy container `AccountScopeID`
   - workspace binding source -> workspace record/link `AccountScopeID`
   - session route source -> session record `AccountScopeID`
   - ambiguous orphan topology rows -> mark blocked/unowned, not visible to product routes.

Stop condition:

- No product topology route or target resolver can list or resolve another account’s topology rows.
- Account-scoped misses do not fall back to global topology reads.

Primary files:

- `swarmd/internal/store/pebble/topology_store.go`
- `swarmd/internal/store/pebble/topology_container_sync.go`
- `swarmd/internal/store/pebble/keys.go`
- `swarmd/internal/topology/service.go`
- `swarmd/internal/api/topology.go`
- `swarmd/internal/api/swarm_targets.go`

### Phase 2 — Source propagation into topology

Goal: every source that writes topology must propagate account ownership.

Implementation tasks:

1. Update local container topology sync:
   - `syncTopologyHostContainer` must copy `record.UserID` and `record.AccountScopeID` into `TopologyHostContainerRecord`.
   - `deleteTopologyHostContainer` must use account-aware topology lookup/delete when a principal/account is available.
2. Update deploy topology sync:
   - deployment-derived host containers, child runtimes, and attachments must copy deployment account fields.
   - attach/finalize must stamp route/topology account from the deployment, not child body/header values.
3. Update managed workspace bindings:
   - source workspace path must be verified through account-owned workspace APIs before binding is created.
   - binding records must store account fields from the principal/source workspace account.
4. Update session routes:
   - session routes must store account fields from the verified session or principal that opened the route.
   - routed session lookup must require the route account and session account to match.
5. Update topology snapshot rebuild:
   - either build per-account snapshots or build a global internal snapshot with account fields, then expose only account-filtered views.
   - do not call global `List()` source methods when an account-scoped rebuild is requested.

Stop condition:

- Account A local container/deploy/managed-host topology records include Account A fields and are invisible to Account B.
- Deleting Account A local/deploy record does not delete Account B topology rows even if runtime/container names collide.

Primary files:

- `swarmd/internal/localcontainers/topology_sync.go`
- `swarmd/internal/deploy/topology_sync.go`
- `swarmd/internal/deploy/service.go`
- `swarmd/internal/api/routed_sessions.go`
- `swarmd/internal/api/swarm_managed_workspace_replication.go`

### Phase 3 — Local containers final verification and sync hardening

Goal: prove the local container work is actually done end-to-end and fix any topology/sync regressions.

Implementation tasks:

1. Verify all local container and profile routes require canonical principal and use account-scoped service/store methods.
2. Verify local deploy routes do not fall back to global service paths.
3. Remove or hard-fail context-less local deploy service fallbacks where product API operations require account ownership:
   - `listRecordsForContext`
   - `getRecordForContext`
   - `persistRecordForContext`
   - `WorkspaceBootstrap`
4. Ensure local container update plan/job lists only principal-account local containers.
5. Verify local delete cleanup is account-scoped for:
   - local container record
   - deploy container record
   - credentials by owner swarm ID
   - workspace replication links
   - topology host container / attachment / workspace binding / session routes
6. Run local vault checks with containers:
   - account A enables/unlocks vault and syncs credentials to a local child container.
   - account B cannot export/sync account A credentials through a local container/deploy path.

Stop condition:

- Local container harness passes with two accounts and Pebble inspection proves records, topology rows, and cleanup are account-scoped.

Primary files:

- `swarmd/internal/api/swarm_local_containers.go`
- `swarmd/internal/api/swarm_container_profiles.go`
- `swarmd/internal/api/deploy_container.go`
- `swarmd/internal/api/update.go`
- `swarmd/internal/localcontainers/service.go`
- `swarmd/internal/deploy/service.go`
- `tests/swarmd/local_replicate_e2e.sh`

### Phase 4 — Managed hosting foundation on top of account-owned topology

Goal: make managed hosting use account-owned topology as the authorization boundary.

Implementation tasks:

1. Managed target selection:
   - `/v1/swarm/targets`
   - `/v1/swarm/target/current`
   - `/v1/swarm/target/select`
   must resolve targets from principal-account topology/deploy/peer records only.
2. Managed workspace replication:
   - preflight, replicate, inventory, link-existing, ensure-link, import-bundle must require principal or peer account binding.
   - source workspaces must be verified by account-owned workspace APIs.
   - target managed host must resolve through account-owned topology runtime/host records.
3. Managed host session routes:
   - open must create a session route with account fields from principal and verified target/workspace.
   - message/run/stop/stream/event must verify route/session/run account before forwarding or writing mirrored data.
4. Managed update/git sync routes:
   - target swarm IDs and workspace paths must resolve through account-owned topology/workspace binding records.
5. Deploy attach/bootstrap/sync/apply routes:
   - attach request/approve/finalize must derive account from principal or account-bound deployment/bootstrap record.
   - credential/agent/skill/permission/model-default export/import/apply must derive account from account-bound deployment/session/managed relation only.
6. Remote deploy retired routes:
   - preserve no-state responses for retired create/start routes.
   - remaining read/update/delete routes must be account-scoped or explicitly blocked as retired.

Stop condition:

- Account B cannot open/message/run/stop/stream Account A managed-host session by guessing session ID, run ID, target swarm ID, route ID, deployment ID, or workspace path.
- Credential and vault sync exports only the principal/account-bound deployment’s credentials.

Primary files:

- `swarmd/internal/api/managed_host_sessions.go`
- `swarmd/internal/api/swarm_managed_workspace_replication.go`
- `swarmd/internal/api/swarm_replicate.go`
- `swarmd/internal/api/swarm_replicate_container.go`
- `swarmd/internal/api/git_sync.go`
- `swarmd/internal/api/managed_dev_update.go`
- `swarmd/internal/api/deploy_container.go`
- `swarmd/internal/api/remote_deploy.go`
- `swarmd/internal/deploy/service.go`
- `swarmd/internal/remotedeploy/service.go`
- `swarmd/internal/store/pebble/remote_deploy_store.go`

### Phase 5 — Flows account ownership and local API isolation

Goal: account-scope flow definitions, schedules, status, outbox, and run history.

Implementation tasks:

1. Add `UserID` and `AccountScopeID` to all flow persistence records:
   - `FlowDefinitionRecord`
   - `FlowAssignmentStatusRecord`
   - `FlowOutboxCommandRecord`
   - `FlowRunSummaryRecord`
   - `FlowCommandLedgerRecord`
   - `FlowDueRecord`
   - `FlowRunClaimRecord`
   - `flow.AcceptedAssignment` or a wrapper record if needed.
2. Add account indexes/prefixes in `keys.go`, for example:
   - `flow/definition_by_account/<account>/<flowID>`
   - `flow/outbox_by_account/<account>/<commandID>`
   - `flow/status_by_account/<account>/<flowID>/<target>`
   - `flow/run_by_account/<account>/<flowID>/<started>/<runID>`
   - account-scoped target accepted/due/run/claim prefixes.
3. Add account-aware flow store APIs:
   - `PutDefinitionForAccount`
   - `GetDefinitionForAccount`
   - `ListDefinitionsForAccount`
   - `DeleteDefinitionForAccount`
   - equivalent APIs for status/outbox/history/target accepted/due/run claim.
4. Route `/v3/flows` through `PrincipalFromRequest` at the collection/member entrypoint, not just in detail rendering.
5. Create/update must stamp creator `UserID` and `AccountScopeID` from the principal.
6. List/detail/history/status/run-now/update/delete must require the persisted flow account to match principal account.
7. Flow target selection must resolve through account-owned topology targets.
8. Flow workspace selection must resolve through account-owned workspace APIs.
9. Flow agent selection must resolve through account-owned agent profile APIs; built-in templates can remain shared read-only only after explicit proof.
10. Scheduler must carry saved account context. Remove hardcoded/silent fallback principals for background execution.

Stop condition:

- Account B cannot list/detail/update/delete/run-now Account A flow.
- Account B cannot infer Account A flow history/outbox/status by guessing flow IDs.
- Flow scheduler runs with the creator account and can access only that account’s workspace/model/credential/agent state.

Primary files:

- `swarmd/internal/store/pebble/flow_store.go`
- `swarmd/internal/store/pebble/keys.go`
- `swarmd/internal/flow/contracts.go`
- `swarmd/internal/flow/scheduler.go`
- `swarmd/internal/api/flows_v3.go`
- `swarmd/internal/api/flows_runner.go`

### Phase 6 — Peer/local flow apply/report and routed runtime hardening

Goal: make distributed flow/session/permission traffic account-safe.

Implementation tasks:

1. Peer auth must resolve to a trusted peer/topology record with `AccountScopeID`.
2. `/v1/swarm/peer/flows/apply` must verify:
   - authenticated peer belongs to the same account as the command/flow target.
   - command `flow_id`, `target_swarm_id`, `revision`, and `command_id` match persisted account-owned flow/outbox records.
3. `/v1/swarm/peer/flows/report` must verify:
   - reporting peer belongs to the account-bound target route.
   - reported session/run/flow IDs belong to the same account.
4. Peer/local session routes must verify session route account before append/mode/title/metadata/lifecycle/event side effects.
5. Peer/local permission routes must verify session/run/permission account before create/wait/cancel/mark-started/mark-completed.
6. Local transport duplicates must not bypass account checks. They either need a canonical local product principal or a persisted child-runtime account binding.

Stop condition:

- A valid peer for Account B cannot apply/report/update/write flow/session/permission state for Account A.
- Local transport without valid mapped account context cannot mutate account-owned records.

Primary files:

- `swarmd/internal/api/flows_peer.go`
- `swarmd/internal/api/flows_report.go`
- `swarmd/internal/api/routed_sessions.go`
- `swarmd/internal/api/routed_permissions.go`
- `swarmd/internal/api/server_routes.go`

### Phase 7 — Vault/container/managed-host end-to-end proof

Goal: prove current account-aware vault code stays correct through local containers and managed hosting.

Implementation tasks:

1. Verify vault metadata and unlock status are account scoped in Pebble:
   - Account A vault enable does not enable Account B vault.
   - Account A unlock does not unlock Account B vault.
   - Account A export excludes Account B credentials.
   - Account A import writes only Account A credentials.
2. Verify `withVaultGate` gates per account.
3. Verify child local container vault unlock uses account-bound deploy/container records.
4. Verify managed-host credential sync/export/import derives account from account-bound deployment/managed relation, not from payload/secret/peer header/local transport.
5. Verify managed vault key refs are account-bound and cannot be reused across accounts.

Stop condition:

- Vault passes two-account route tests and local-container/managed-host sync tests.

Primary files:

- `swarmd/internal/api/vault.go`
- `swarmd/internal/store/pebble/auth_vault.go`
- `swarmd/internal/store/pebble/auth_bundle.go`
- `swarmd/internal/deploy/service.go`
- `swarmd/internal/api/deploy_container.go`

### Phase 8 — Remaining cross-cutting API cleanup

Goal: finish or explicitly defer smaller non-core surfaces after topology/flows/managed hosting are safe.

Work items:

1. Agents/custom tools:
   - Mutable profiles/tools/active selections must be account-scoped.
   - Built-ins may remain shared read-only only after proof.
2. Permissions/notifications:
   - Permission policy and permission records must be account-scoped.
   - Notifications/summaries must be account-scoped and linked to owned session/permission/runtime state.
3. Integrations/images/UI settings:
   - Integrations and builder sessions need account-bound workspaces/sessions.
   - Images need account-bound image thread/assets/workspace checks.
   - UI settings must split account-owned preferences from device-global machine identity.
4. Provider/model/voice:
   - Existing work appears partly migrated, but final acceptance should run current two-account proof for model prefs/favorites, provider readiness, and voice profile/config isolation.

Stop condition:

- Final API inventory has no “DO NOW” routes that can leak or mutate another account’s state.
- Anything not implemented is explicitly marked shared/static, no-state/retired, or blocked behind a clear product decision.

## VM and test gates

### Gate A — topology/local container proof

Required checks:

- Create Account A and Account B through product identity/session APIs.
- Account A creates local container/profile/deploy container.
- Account B cannot list/read/action/delete/update Account A records.
- Account A topology snapshot includes Account A runtime/container/binding records with account fields.
- Account B topology snapshot does not include Account A records.
- Delete/prune/update for Account A leaves Account B records untouched.
- Pebble inspection proves account fields and account indexes exist.

Suggested command shape:

<copy label="topology/local container targeted tests">cd swarmd && go test ./internal/api ./internal/localcontainers ./internal/deploy ./internal/topology ./internal/store/pebble -run 'Topology|LocalContainer|DeployContainer|Account|Principal'</copy>

Suggested VM shape:

<copy label="local replicate VM gate">./scripts/swarm-harness-vm.sh local-replicate -- --dev-mode --skip-image-rebuild --verify-topology-cleanup</copy>

### Gate B — managed hosting proof

Required checks:

- Account A creates/uses managed host target and managed workspace binding.
- Account B cannot target/select/open/message/run/stop Account A managed host/session.
- Peer/local managed-host session events cannot spoof Account A IDs with Account B peer/local context.
- Git sync/update/status routes require account-owned target/workspace binding.
- Credential/agent/permission/model sync exports only account-bound data.
- Pebble inspection proves deploy/session-route/topology records carry account fields.

Suggested command shape:

<copy label="managed hosting targeted tests">cd swarmd && go test ./internal/api ./internal/deploy ./internal/remotedeploy ./internal/topology ./internal/store/pebble -run 'ManagedHost|ManagedWorkspace|DeployContainer|RemoteDeploy|SessionRoute|Topology|Account|Principal'</copy>

### Gate C — flows proof

Required checks:

- Account A creates a flow.
- Account B cannot list/detail/update/delete/run-now Account A flow.
- Account B cannot read Account A flow history/status/outbox by guessed flow ID.
- Account A flow target resolves only to Account A topology target.
- Scheduler runs with Account A principal/account context.
- Peer apply/report cannot spoof flow/session/run IDs across accounts.
- Pebble inspection proves account fields/indexes for definition/status/outbox/due/run/claim/accepted records.

Suggested command shape:

<copy label="flows targeted tests">cd swarmd && go test ./internal/api ./internal/flow ./internal/store/pebble -run 'Flow.*Account|FlowsV3|FlowPeer|FlowReport|Scheduler|Principal'</copy>

### Gate D — vault with containers and managed hosting proof

Required checks:

- Account A and B have independent vault enable/unlock/lock/disable/export/import behavior.
- Account A local container credential sync cannot export Account B credentials.
- Account A managed-host credential sync/apply cannot export/import Account B credentials.
- Locked Account A vault blocks Account A protected operations without affecting Account B.
- Pebble inspection proves account-scoped vault metadata and credential records.

Suggested command shape:

<copy label="vault/container targeted tests">cd swarmd && go test ./internal/api ./internal/auth ./internal/deploy ./internal/localcontainers ./internal/store/pebble -run 'Vault.*Account|Credential.*Account|Container.*Vault|Managed.*Credential|Principal'</copy>

### Gate E — final acceptance VM

Required checks:

- Fresh VM / clean Pebble state.
- Build succeeds.
- Account A and Account B bootstrap through product APIs.
- Browser cookie and `X-Swarm-Token` resolve canonical principals.
- Account A can use workspace/session/settings/local container/managed host/flows.
- Account B cannot read or mutate Account A state across all converted routes.
- Pebble scan confirms no new writes to legacy global mutable keys except explicit static/shared catalogs.
- Denied cross-account probes leave no records behind.

## Attack points to explicitly test

- Guessing `flow_id` across accounts on `/v3/flows/{id}`, `/history`, `/status`, and `/run-now`.
- Guessing `session_id` across accounts on managed-host message/run/stop/stream/event routes.
- Guessing `target_swarm_id` or `managed_swarm_id` across accounts on target/select, git sync, update, workspace replication, and managed-host sessions.
- Guessing `deployment_id` or `bootstrap_secret` across accounts on attach/sync/workspace bootstrap routes.
- Using local transport to bypass missing browser principal.
- Using peer auth for one account to report/apply state for another account.
- Locking/unlocking/exporting vault state across accounts.
- Reusing same local container name/profile ID/flow ID/custom tool name across accounts.

## Final done criteria

The migration is done only when all of the following are true:

1. Every mutable product record touched by local containers, managed hosting, topology, flows, sessions, vault sync, and peer/local transport has a server-derived `AccountScopeID` and usually `UserID`.
2. Every product route resolves canonical principal or derives account from a persisted trusted relation before read/write.
3. Every peer/local route maps transport identity to persisted account-bound topology/deploy/session/flow state before side effects.
4. No account-scoped miss falls back to a global mutable read.
5. Global keys are either static/shared by design, retired/no-state, or explicit one-way migration inputs only.
6. Two-account VM gates pass for local containers, managed hosting, flows, and vault sync.

## Relevant filepaths

- `swarmd/internal/store/pebble/topology_store.go`
- `swarmd/internal/topology/service.go`
- `swarmd/internal/localcontainers/topology_sync.go`
- `swarmd/internal/store/pebble/flow_store.go`
- `swarmd/internal/api/flows_v3.go`
- `swarmd/internal/api/flows_runner.go`
- `swarmd/internal/api/flows_peer.go`
- `swarmd/internal/api/flows_report.go`
- `swarmd/internal/deploy/service.go`
- `swarmd/internal/api/deploy_container.go`
- `swarmd/internal/api/managed_host_sessions.go`
- `swarmd/internal/api/swarm_managed_workspace_replication.go`
- `swarmd/internal/api/swarm_replicate.go`
- `swarmd/internal/api/git_sync.go`
- `swarmd/internal/api/vault.go`
- `swarmd/internal/store/pebble/auth_vault.go`
- `swarmd/internal/store/pebble/keys.go`
- `tests/swarmd/local_replicate_e2e.sh`
