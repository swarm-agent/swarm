# Checkpoint 2 API-zone clone prompts

These prompts define the strict refinement pass for the API identity/account-scope inventory. The previous zone reports are drafts. The next clone round must rewrite those reports into evidence-only implementation matrices that connect directly to the route matrix, Checkpoint 1 principal foundation, and the current source code.

No clone may implement code in this round. The only allowed output is its assigned Markdown report under `docs/migrations/user-account-scope/zone-reports/`.

## Critical purpose clarification for the next clones

The next workers are **CLONES, not explorers**. Their job is not to discover a new architecture, debate the migration, or implement runtime changes. The architecture and report structure already exist. Each clone must determine **WHAT exactly must be done next** in its assigned zone so a later implementation agent can execute the work without guessing.

Every clone must keep this mission in view:

1. Preserve the Checkpoint 1 account structure proven in git commit `b1d2dd9`:
   - Pebble-native `UserRecord`, `AccountScopeRecord`, `AccountUserRecord`, and current-selection records.
   - Bootstrap creates a user-owned personal `AccountScopeRecord` with `Type: personal`, `CreatedByUserID`, `UserID`, owner role, active `AccountUserRecord`, and current selection by `UserID`; it does not create a team by default.
   - Team records are explicit opt-in metadata for `/v1/account/team/upgrade`; `TeamID` is not the account scope, not IAM, and not trusted request authority.
   - Canonical `identity.Principal` with `UserID` and `AccountScopeID`.
   - Canonical API resolver `api.PrincipalFromRequest(r)` / `requireProductActor` only.
   - Browser, `X-Swarm-Token`, and local transport identity all resolving the same trusted product principal.
2. Convert zone routes and storage to account-scoped behavior only after the report proves the exact work required.
3. Treat every request-supplied user/account/scope value as untrusted locator data, never authority.
4. Produce a route-by-route and storage-by-storage **implementation handoff checklist** with literal next steps for the next implementation agent.
5. Mark missing evidence as `BLOCKED_UNPROVEN`. Do not infer safe behavior.

The clone deliverable is a zone report that answers, for each assigned route/storage area:

- What does the current code do now? Cite exact `path:line` evidence.
- What principal source is currently present at the handler boundary? Use the required enum.
- What account-scope invariant must exist after implementation?
- What exact code/storage/test steps must the next implementation agent perform?
- What VM proof command/evidence must prove the work?

## Mandatory clone self-prompt before editing

Before editing its zone report, each clone must write the following block at the top of the report and fill it in for its zone. This is required so the clone proves it understands the task before producing checklists:

```md
## Clone mission restatement
- I am a clone, not an explorer.
- I will not implement code in this pass.
- I will preserve the Checkpoint 1 Pebble-native account structure: `UserRecord`, personal `AccountScopeRecord`, active `AccountUserRecord`, current selection, canonical `identity.Principal`, and `api.PrincipalFromRequest(r)` / `requireProductActor`. Team records remain explicit opt-in metadata only; `TeamID` is not account scope.
- I will not introduce SQL, IAM, TeamID-as-scope, wrappers, shims, compatibility forks, silent fallback reads, or broad API conversion plans.
- I will treat request body/query/header/path IDs, peer data, attach tokens, env vars, and CLI flags as untrusted for `UserID` and `AccountScopeID`.
- My output is the exact implementation prompt/checklist for the next agent: route-by-route steps, storage key/index steps, tests, VM proof, blockers, and line evidence.
- Assigned zone:
- Assigned report path:
- Assigned matrix anchors:
```

## Required implementation-handoff prompt in every report

At the end of each zone report, each clone must add a section named exactly:

```md
## Prompt for the next implementation agent
```

That section must contain a literal step-by-step prompt for the next implementation agent. It must use this form:

```md
You are the implementation agent for <ZONE>. Do not redesign the identity model. Preserve Checkpoint 1 Pebble-native account structure: user-owned personal account scope, active account-user membership, current selection, and canonical principal. Use only `api.PrincipalFromRequest(r)` / `requireProductActor` as trusted principal authority. Treat `TeamID` as explicit opt-in metadata only, never as account scope or request authority.

Step 1. Implement <checklist ID>: <exact route/storage change>. Evidence: <path:line>. Test/VM proof: <command or proof requirement>.
Step 2. Implement <checklist ID>: <exact route/storage change>. Evidence: <path:line>. Test/VM proof: <command or proof requirement>.
...
Stop if any prerequisite evidence is missing or any cross-zone blocker remains unresolved.
```

This final prompt is mandatory. It is the primary product of the clone round.

## Prior work that every clone must read first

Read these files before editing a zone report:

1. `docs/migrations/user-account-scope/api-route-zone-matrix.md`
   - Canonical route registry and zone assignment.
   - Matrix use rule: every report row must anchor to an exact `api-route-zone-matrix.md:<line>` row.
2. `docs/migrations/user-account-scope/checkpoint-1-vm-proof.md`
   - VM-proven identity foundation.
   - Evidence anchors: lines 21-35 list gate commands; lines 62-94 prove browser, `X-Swarm-Token`, and local transport all resolve the same session principal.
3. Canonical principal/session/account code:
   - `swarmd/internal/api/desktop_local_auth.go:239-256` for `PrincipalFromRequest`.
   - `swarmd/internal/api/desktop_local_auth.go:296-301` for `requireProductActor`.
   - `swarmd/internal/identity/principal.go:20-38` for canonical `Principal` fields and validity.
   - `swarmd/internal/identity/session.go:89-131` for issuing local product sessions.
   - `swarmd/internal/identity/session.go:134-180` for validating JWT claims against Pebble identity state.
   - `swarmd/internal/identity/service.go:96-121` for bootstrap creation of user-owned personal account scope and current selection without default team creation.
   - `swarmd/internal/identity/service.go:150-198` for explicit team opt-in metadata.
   - `swarmd/internal/store/pebble/identity_store.go:28-82` for canonical Pebble user/account/current-selection records.
4. The current draft report for the assigned zone. Treat it as input to verify, not authority.
5. The handler/store/service files named by the assigned matrix rows and the current draft report.

## Non-negotiable contract

1. Documentation only. Do not edit runtime source, tests, migrations, generated assets, or VM scripts.
2. Every protected route must derive `UserID` and `AccountScopeID` from the trusted request principal only: `api.PrincipalFromRequest(r)` or `requireProductActor` when it returns the same canonical actor/principal.
3. Never trust request body, query, headers, peer payloads, attach tokens, workspace paths, target IDs, session IDs, run IDs, container IDs, swarm IDs, env vars, CLI flags, or TeamID as user/account authority.
4. Local transport identity must use the same canonical resolver as browser sessions unless a route receives a proven bootstrap/internal verdict.
5. Peer auth proves transport identity only. Peer routes touching account-owned state require peer auth plus a persisted server-derived account/principal binding.
6. Pebble-native identity/account-scope objects are the foundation. Do not introduce SQL, IAM, TeamID-as-scope, wrappers, shims, compatibility forks, silent fallback reads, or broad API conversion plans.
7. Existing global records must be documented as conversion blockers or one-way data-shape requirements. Do not propose runtime fallback to old global keys after an account-scoped miss.
8. Every factual claim must cite exact evidence in `path:line` or `path:start-end` form. If line evidence is missing, mark the route/storage row `BLOCKED_UNPROVEN`.
9. Reports must use imperative, evidence-backed language. Banned report wording: `maybe`, `probably`, `should`, `could`, `might`, `optional`, `consider`, `likely`, `seems`, `appears`, `TBD`, `unclear`. Use `must`, `requires`, `proven`, `blocked`, or `not evidenced`.
10. Do not guess. A missing source trace is a blocker, not an invitation to infer behavior.

## Required enums

### Route verdict enum

Every route matrix row must use exactly one of these values:

| Value | Meaning |
|---|---|
| `PUBLIC_PROVEN` | Route is public and line evidence proves it reads/writes no account-owned data. |
| `BOOTSTRAP_PROVEN` | Route lacks a product principal by design and line evidence proves the server-issued bootstrap material maps to server-side identity/account state. |
| `PRINCIPAL_REQUIRED` | Route must resolve canonical product principal before reading/writing account-owned state. |
| `PEER_AUTH_PLUS_PRINCIPAL_REQUIRED` | Route requires peer/local runtime auth plus persisted server-derived account/principal binding. |
| `BLOCKED_UNPROVEN` | Evidence does not prove a safe public/bootstrap/peer/principal boundary. |

### Principal source enum

Every route matrix row must use exactly one of these values in `Current principal source`:

| Value | Meaning |
|---|---|
| `PrincipalFromRequest` | Handler calls the canonical resolver. |
| `requireProductActor` | Handler calls the canonical actor guard. |
| `identity.SessionService.Validate` | Auth middleware/session path validates a product JWT and creates canonical actor/principal context. |
| `withAuth actor only` | Outer auth can attach actor/principal, but handler does not require or consume it. |
| `peer auth only` | Handler relies on peer auth without product principal/account binding. |
| `attach token only` | Handler can be authorized by attach token without product principal/account binding. |
| `NONE` | No current trusted principal source is evidenced at the handler boundary. |

## Required report structure

Each zone report must use this structure and keep the section names exact:

```md
# <Zone> API identity/account-scope report

## Clone mission restatement
- I am a clone, not an explorer.
- I will not implement code in this pass.
- I will preserve the Checkpoint 1 Pebble-native account structure: `UserRecord`, personal `AccountScopeRecord`, active `AccountUserRecord`, current selection, canonical `identity.Principal`, and `api.PrincipalFromRequest(r)` / `requireProductActor`. Team records remain explicit opt-in metadata only; `TeamID` is not account scope.
- I will not introduce SQL, IAM, TeamID-as-scope, wrappers, shims, compatibility forks, silent fallback reads, or broad API conversion plans.
- I will treat request body/query/header/path IDs, peer data, attach tokens, env vars, and CLI flags as untrusted for `UserID` and `AccountScopeID`.
- My output is the exact implementation prompt/checklist for the next agent: route-by-route steps, storage key/index steps, tests, VM proof, blockers, and line evidence.
- Assigned zone:
- Assigned report path:
- Assigned matrix anchors:

## Refinement status
- Report path:
- Source prompt: `docs/migrations/user-account-scope/api-zone-clone-prompts.md`
- Prior draft reviewed: yes/no
- Matrix anchors reviewed:
- Report quality gate: PASS/FAIL

## Scope
- Matrix rows reviewed:
- Route groups:
- Files inspected:
- Prior-work anchors used:

## Executive summary
- Protected route verdict counts:
- Public/bootstrap/internal route verdict counts:
- Highest-risk identity/scope issues:
- Storage/keyspace conversion blockers:
- Cross-zone dependencies:

## Route-by-route identity/scoping matrix
| Matrix anchor | Registry | Route/path prefix | Handler | Methods/ops with evidence | Route verdict | Current principal source | Principal source evidence | Storage prefixes/models/indexes with evidence | Request identity/scope inputs rejected as authority | Required invariant | Implementation checklist IDs | Test/VM proof required | Status |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|

## Before/after implementation checklist
| ID | Route/storage area | Before: verified current state | After: required invariant | Evidence | Implementation checklist item | Test/VM proof required | Status |
|---|---|---|---|---|---|---|---|

## Storage ownership and Pebble keys
| Storage area | Current key/prefix/model | Current scope evidence | Required account-scope invariant | Required index/key change | Cross-zone owner | Migration/backfill/blocker note | Status |
|---|---|---|---|---|---|---|---|

## Test-time principal strategy for this zone
- Existing tests that need canonical principal fixture:
- Canonical fixture setup required:
- Explicitly rejected shortcuts:

## VM gate plan
- Targeted Go tests:
- Full/zone VM command:
- Pebble DB evidence to inspect:

## Blockers / decisions needed
- None, or exact blockers with evidence.

## Prompt for the next implementation agent
You are the implementation agent for <ZONE>. Do not redesign the identity model. Preserve Checkpoint 1 Pebble-native account structure: user-owned personal account scope, active account-user membership, current selection, and canonical principal. Use only `api.PrincipalFromRequest(r)` / `requireProductActor` as trusted principal authority. Treat `TeamID` as explicit opt-in metadata only, never as account scope or request authority.

Step 1. Implement <checklist ID>: <exact route/storage change>. Evidence: <path:line>. Test/VM proof: <command or proof requirement>.
Step 2. Implement <checklist ID>: <exact route/storage change>. Evidence: <path:line>. Test/VM proof: <command or proof requirement>.
...
Stop if any prerequisite evidence is missing or any cross-zone blocker remains unresolved.
```

## Matrix/checklist ID rules

1. Every route row receives at least one implementation checklist ID.
2. ID format:
   - Route item: `<ZONE>-R<api-route-zone-matrix line>`, for example `Z2-R195`.
   - Storage item: `<ZONE>-SNNN`, for example `Z5-S003`.
   - Cross-zone item: `<ZONE>-XNNN`, for example `Z4B-X002`.
3. A checklist item can cover multiple routes only when the same code/storage change enforces the same invariant for all listed routes. Cite every covered route matrix anchor in `Route/storage area`.
4. `Status` values in the checklist must be one of:
   - `READY_FOR_IMPLEMENTATION`
   - `BLOCKED_UNPROVEN`
   - `BLOCKED_CROSS_ZONE`
   - `PROVEN_NO_CHANGE`
5. `READY_FOR_IMPLEMENTATION` requires exact handler evidence, exact storage evidence, a route verdict, an implementation item, and a test/VM proof line.

## Acceptance checklist for each refined report

A refined report passes only when all checks are true:

- The report contains `## Clone mission restatement` directly under the title and fills in assigned zone, report path, and matrix anchors.
- The report contains `## Prompt for the next implementation agent` with numbered implementation steps mapped to checklist IDs.
- The `## Route-by-route identity/scoping matrix` contains every assigned matrix row exactly once.
- Every route row has a hard route verdict enum.
- Every route row has a hard current principal source enum.
- Every route row cites exact handler line evidence for methods/ops.
- Every storage claim cites exact key/model/store line evidence.
- Every route row maps to at least one before/after checklist ID.
- The before/after checklist has the exact requested columns: `ID`, `Route/storage area`, `Before: verified current state`, `After: required invariant`, `Evidence`, `Implementation checklist item`, `Test/VM proof required`, `Status`.
- No vague/banned wording remains outside quoted source names.
- Public/bootstrap/peer/internal verdicts are proven with line evidence. Unproven routes use `BLOCKED_UNPROVEN`.
- Tests use canonical seeded Pebble identity + `identity.SessionService` session/JWT only. Fake identity headers/body/query/context shortcuts are explicitly rejected.
- The report contains no code implementation plan that bypasses the master-plan consolidation step.

## Zone refinement launch prompts

Use one prompt per clone. Each clone must read this whole file, then read its assigned current report, matrix rows, handlers, stores, and services. Each clone rewrites only its assigned report file.

Each clone must first restate the mission in its report using `## Clone mission restatement`, then produce the implementation handoff. The clone must act as a checklist author for the next implementation agent. It must not act as an explorer and must not expand scope beyond assigned matrix anchors.

### Prompt 1: Z0/Z1 auth, local transport identity, onboarding, sessions, credentials

```text
You are the Z0/Z1 Checkpoint 2 report refinement clone.

Rewrite only:
`docs/migrations/user-account-scope/zone-reports/z0-z1-auth-local-identity.md`

Assigned matrix anchors:
- Z0/Z1 rows: `api-route-zone-matrix.md:51-63`, `api-route-zone-matrix.md:71-74`, `api-route-zone-matrix.md:268-269`.

Required route groups:
- Z0 health/readiness on main and local transport.
- Z1 `/ws` desktop stream.
- Z1 codex auth/OAuth and auth credentials.
- Z1 desktop local session bootstrap and attach-token rotation.
- Z1 `/me`, onboarding, and legacy `/v1/account/team/upgrade`.

Mandatory source focus:
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/api/server.go`
- `swarmd/internal/api/desktop_local_auth.go`
- `swarmd/internal/api/desktop_bootstrap.go`
- `swarmd/internal/api/me.go`
- `swarmd/internal/api/onboarding.go`
- `swarmd/internal/api/account.go`
- `swarmd/internal/api/auth_defaults.go`
- `swarmd/internal/api/auth_verify.go`
- `swarmd/internal/api/auth_cleanup.go`
- `swarmd/internal/api/codex_oauth.go`
- `swarmd/internal/identity/`
- `swarmd/internal/store/pebble/identity_store.go`
- `swarmd/internal/store/pebble/identity_session_store.go`
- `swarmd/internal/store/pebble/auth_store.go`
- `swarmd/internal/auth/`
- `swarmd/internal/security/`

Refinement tasks:
1. Replace the current report with the required report structure.
2. Verify every route verdict against the enum.
3. Prove public health/readiness and bootstrap session/onboarding boundaries with exact lines.
4. Mark credential/codex/attach/session stream risks with exact storage and handler evidence.
5. Add before/after checklist IDs for every route and every credential/session storage conversion requirement.
6. Define VM proof from Checkpoint 1 plus Z1-specific two-account credential/session checks.
```

### Prompt 2: Z2 workspaces, worktrees, files/media, git, managed/peer workspace replication

```text
You are the Z2 Checkpoint 2 report refinement clone.

Rewrite only:
`docs/migrations/user-account-scope/zone-reports/z2-workspaces-worktrees-git.md`

Assigned matrix anchors:
- Z2 rows: `api-route-zone-matrix.md:90`, `api-route-zone-matrix.md:122-134`, `api-route-zone-matrix.md:192-220`, `api-route-zone-matrix.md:235-236`, `api-route-zone-matrix.md:258`, `api-route-zone-matrix.md:295`.

Required route groups:
- Managed-host workspace git commit.
- Swarm workspace replication.
- Peer/managed workspace preflight, ensure-link, link-existing, import, inventory, discover, create, transfer.
- Workspace resolve/select/current/list/overview/discover/browse.
- Workspace media threads/storage reveal/image threads/video scan.
- Workspace folders, directories, managed links, theme, rename, move, todos, delete.
- Workspace git status/commit/realtime, worktrees, manage-worktree, git sync inspect/apply, peer git sync apply on main and local transport.

Mandatory source focus:
- `swarmd/internal/api/server_routes.go`
- Workspace, media, git, worktree, replication, managed workspace, peer workspace handlers under `swarmd/internal/api/`
- `swarmd/internal/workspace/`
- `swarmd/internal/worktree/`
- `swarmd/internal/store/pebble/workspace_store.go`
- `swarmd/internal/store/pebble/worktree_store.go`
- `swarmd/internal/store/pebble/todo_store.go`
- `swarmd/internal/store/pebble/video_thread_store.go`
- `swarmd/internal/store/pebble/image_thread_store.go`
- `swarmd/internal/store/pebble/topology_store.go`

Refinement tasks:
1. Replace the current report with the required report structure.
2. Verify every workspace path/current/list operation with exact handler and store lines.
3. Mark path/workspace/current selection as lookup material only, never account authority.
4. Add before/after checklist IDs for workspace current, workspace entry, worktree config, todo, media thread, topology binding, git sync, and peer git sync scoping.
5. Give hard verdicts for peer and local-transport rows using `PEER_AUTH_PLUS_PRINCIPAL_REQUIRED` or `BLOCKED_UNPROVEN` when source evidence does not prove account binding.
6. Define two-account VM proof for list/current/path-only/cross-account denial and Pebble key/index isolation.
```

### Prompt 3: Z3 agents, sessions, flows, runs, permissions, notifications/context

```text
You are the Z3 Checkpoint 2 report refinement clone.

Rewrite only:
`docs/migrations/user-account-scope/zone-reports/z3-agents-sessions-flows-runs.md`

Assigned matrix anchors:
- Z3 rows: `api-route-zone-matrix.md:135-136`, `api-route-zone-matrix.md:170-173`, `api-route-zone-matrix.md:221`, `api-route-zone-matrix.md:223-230`, `api-route-zone-matrix.md:241-251`, `api-route-zone-matrix.md:261-265`, `api-route-zone-matrix.md:285-293`, `api-route-zone-matrix.md:298-302`.

Required route groups:
- V3 flows and flow detail/history/status/run-now.
- V2 agents/defaults/active/profile/custom tool links that affect agents.
- Context sources.
- Permissions policy/routes.
- Alerts/notifications list/update/summary.
- Sessions/conversations and all `/v1/sessions/` subroutes.
- Peer flow/session/permission routes on main and local transport.

Mandatory source focus:
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/api/agent_v2.go`
- `swarmd/internal/api/flows_v3.go`
- `swarmd/internal/api/flows_*.go`
- `swarmd/internal/api/routed_sessions.go`
- `swarmd/internal/api/routed_permissions.go`
- `swarmd/internal/api/notifications.go`
- `swarmd/internal/api/managed_host_permission_controls.go`
- `swarmd/internal/agent/`
- `swarmd/internal/session/`
- `swarmd/internal/run/`
- `swarmd/internal/permission/`
- `swarmd/internal/notification/`
- `swarmd/internal/store/pebble/agent_store.go`
- `swarmd/internal/store/pebble/flow_store.go`
- `swarmd/internal/store/pebble/session_store.go`
- `swarmd/internal/store/pebble/permission_store.go`
- `swarmd/internal/store/pebble/notification_store.go`

Refinement tasks:
1. Replace the current report with the required report structure.
2. Inventory every global agent/flow/session/permission/notification key and model with exact evidence.
3. Identify creation points that must capture `Principal.UserID` and `Principal.AccountScopeID`.
4. Identify follow-up routes that must verify persisted account scope before using sessionID/runID/permissionID/flowID.
5. Add before/after checklist IDs for all route families and each shared storage/index conversion.
6. Define two-account VM proof for cross-account session/flow/permission/notification denial and peer spoof rejection.
```

### Prompt 4: Z4A swarm topology, pairing, targets/groups, local containers/profiles

```text
You are the Z4A Checkpoint 2 report refinement clone.

Rewrite only:
`docs/migrations/user-account-scope/zone-reports/z4a-swarm-topology-local-containers.md`

Assigned matrix anchors:
- Z4A rows: `api-route-zone-matrix.md:75-83`, `api-route-zone-matrix.md:94-121`, `api-route-zone-matrix.md:266-267`, `api-route-zone-matrix.md:303-304`.

Required route groups:
- Swarm discovery, remote candidates, invites, remote pairing start/offer/request/pending/finalize/approve.
- Enrollment and pending children.
- Swarm state/targets/topology/host containers/runtime owner/workspace bindings/session route.
- Mirror resources/delete and peer mirror snapshot/watch on main and local transport.
- Current target/select.
- Groups/current/members.
- Container profiles and local containers/runtime/update/create/action/delete/prune.

Mandatory source focus:
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/api/swarm_pairing*.go`
- `swarmd/internal/api/remote_candidates.go`
- `swarmd/internal/api/swarm_targets.go`
- `swarmd/internal/api/swarm_groups.go`
- `swarmd/internal/api/swarm_container_profiles.go`
- `swarmd/internal/api/swarm_local_containers.go`
- `swarmd/internal/api/swarm_peer_mirror.go`
- `swarmd/internal/api/topology.go`
- `swarmd/internal/api/topology_session_routes.go`
- `swarmd/internal/swarm/`
- `swarmd/internal/topology/`
- `swarmd/internal/localcontainers/`
- `swarmd/internal/containerprofiles/`
- related Pebble stores under `swarmd/internal/store/pebble/`

Refinement tasks:
1. Replace the current report with the required report structure.
2. Prove which discovery/pairing/enrollment endpoints are `BOOTSTRAP_PROVEN`; mark unproven ones `BLOCKED_UNPROVEN`.
3. Treat swarm ID, group ID, target ID, container ID, mirror resource ID, peer auth, and local transport as non-account authority.
4. Add before/after checklist IDs for groups, current group, target/current target, trusted peers/invites/enrollments, container profiles, local containers, mirror resources, and topology rows.
5. Define cross-zone blockers for Z2 workspace bindings, Z3 session routes, Z4B deploy records, and Z5 credential sync.
6. Define two-account VM proof for target/group/profile/container/topology/mirror isolation and peer mirror spoof rejection.
```

### Prompt 5: Z4B deploy containers, remote deploy sessions, managed-host sessions/update/local transport deploy routes

```text
You are the Z4B Checkpoint 2 report refinement clone.

Rewrite only:
`docs/migrations/user-account-scope/zone-reports/z4b-deploy-managed-hosts-runtime.md`

Assigned matrix anchors:
- Z4B rows: `api-route-zone-matrix.md:84-89`, `api-route-zone-matrix.md:91-93`, `api-route-zone-matrix.md:137-167`, `api-route-zone-matrix.md:222`, `api-route-zone-matrix.md:231-234`, `api-route-zone-matrix.md:252-257`, `api-route-zone-matrix.md:259-260`, `api-route-zone-matrix.md:270-284`, `api-route-zone-matrix.md:294`, `api-route-zone-matrix.md:296-297`.

Required route groups:
- Managed host removal/container delete/session open/message/run/stop/git sync/update.
- Deploy container runtime/list/create/package/settings/action/delete/attach/sync/managed apply/workspace bootstrap.
- Remote deploy session list/create/settings/delete/start/update-job/sync/approve.
- System shutdown and update routes.
- Peer managed-host session/update routes and local-transport duplicates.

Mandatory source focus:
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/api/deploy_container.go`
- `swarmd/internal/api/deploy_container_packages.go`
- `swarmd/internal/api/remote_deploy.go`
- `swarmd/internal/api/managed_host_sessions.go`
- `swarmd/internal/api/managed_host_permission_controls.go`
- `swarmd/internal/api/managed_dev_update.go`
- `swarmd/internal/api/update.go`
- `swarmd/internal/api/swarm_replicate_container.go`
- `swarmd/internal/deploy/`
- `swarmd/internal/remotedeploy/`
- `swarmd/internal/update/`
- related Pebble stores under `swarmd/internal/store/pebble/`

Refinement tasks:
1. Replace the current report with the required report structure.
2. Verify every browser-facing deploy/remote/managed-host route that needs canonical principal.
3. Verify every peer/local/bootstrap/sync route that needs persisted account binding beyond peer auth/bootstrap secret.
4. Treat deployment ID, remote session ID, run ID, session ID, child swarm ID, target swarm ID, bootstrap secret, runtime callback URL, and peer auth as non-account authority.
5. Add before/after checklist IDs for deploy records, remote deploy records, session routes, managed-host sessions, update jobs, credential/model/agent/skill sync, and local transport duplicates.
6. Define two-account VM proof for deployment/session/job/list isolation, credential sync scope, and peer/local spoof denial.
```

### Prompt 6: Z5 integrations, credentials/secrets/vault, providers, models, voice, UI/global settings, custom tools

```text
You are the Z5 Checkpoint 2 report refinement clone.

Rewrite only:
`docs/migrations/user-account-scope/zone-reports/z5-integrations-credentials-settings.md`

Assigned matrix anchors:
- Z5 rows: `api-route-zone-matrix.md:64-70`, `api-route-zone-matrix.md:168-169`, `api-route-zone-matrix.md:174-191`, `api-route-zone-matrix.md:237-240`.
- Coordination-only Z1 credential rows: `api-route-zone-matrix.md:58-61`. Do not claim ownership of these rows; document Z5 dependency requirements.

Required route groups:
- Vault status/enable/unlock/lock/disable/export/import.
- Custom tools list/get/put/delete.
- Image providers/generations/assets/storage reveal.
- Model preference/catalog/favorites/providers.
- STT and voice status/profiles/config/devices/test.
- UI settings.
- Integrations packs/workspaces/workspace sessions/builder sessions.
- Z1 credential coordination requirements.

Mandatory source focus:
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/api/vault.go`
- `swarmd/internal/api/tool_storage.go`
- `swarmd/internal/api/agent_v2.go` for custom tools only
- `swarmd/internal/api/image_generation.go`
- `swarmd/internal/api/media_reveal.go`
- `swarmd/internal/api/integrations.go`
- `swarmd/internal/api/integration_sessions.go`
- provider/model/voice/UI settings handlers under `swarmd/internal/api/`
- `swarmd/internal/auth/`
- `swarmd/internal/integration/`
- `swarmd/internal/imagegen/`
- `swarmd/internal/model/`
- `swarmd/internal/voice/`
- `swarmd/internal/uisettings/`
- custom tool/MCP/tool stores and related Pebble stores under `swarmd/internal/store/pebble/`

Refinement tasks:
1. Replace the current report with the required report structure.
2. Inventory every global credential/vault/custom-tool/model/favorite/provider-readiness/image/voice/UI/integration key and model with exact evidence.
3. Mark static catalog/provider data as global only when exact evidence proves no account-owned credential/readiness/preference state is returned.
4. Treat provider IDs, credential IDs, `AccountID`, profile IDs, model names, device IDs, workspace IDs, thread IDs, asset IDs, pack IDs, and credential refs as locators/metadata only.
5. Add before/after checklist IDs for every Z5 route family and each shared storage/index conversion.
6. Define two-account VM proof for credential/vault/export/import/settings/custom tool/image/integration isolation and Z4 credential sync scope.
```

## Launch shape

Launch all six refinement clones in one batched task when tooling allows it. Each clone edits a separate report file, so the work is independent. After all reports return, run a local structure check before consolidation:

- Confirm each report contains `## Clone mission restatement`.
- Confirm each report states `I am a clone, not an explorer.`
- Confirm each report contains `## Route-by-route identity/scoping matrix`.
- Confirm each report contains `## Before/after implementation checklist`.
- Confirm each report contains `## Prompt for the next implementation agent`.
- Confirm each final implementation-agent prompt has numbered steps tied to checklist IDs.
- Confirm each report uses only the allowed route verdict enum values.
- Confirm each report uses only the allowed principal source enum values.
- Confirm no banned vague wording remains in the report body.

Only after those checks pass may the reports be consolidated into:
`docs/migrations/user-account-scope/api-zone-master-plan.md`

Implementation starts only after master-plan consolidation resolves route/storage ownership conflicts.
