# Next-Agent Prompt: Section D + Section F Runtime, Sessions, Integrations, Deploy Cutover

You are the coding agent assigned to fix the identity ownership cutover for Section D and Section F. Do not implement this as one giant commit. This scope is high risk and must be split by resource family with a fresh-VM gate after each checkpoint.

Global product model:
- `UserID` is the primary actor and must be persisted on user-owned records.
- `TeamID` is the sharing container and must be persisted on shared/team-owned records.
- `WorkspaceID` is the stable workspace identity for workspace-bound records.
- `username` is product identity. `swarmName`, `swarmID`, socket identity, transport token, bootstrap secret, session ID, deployment ID, filesystem path, and container/host ID are not ownership.
- No fallback IDs, guessed ownership, silent legacy adoption, path-derived identity, dual authoritative paths, or legacy/global reads after conversion.
- A route is not converted until its handler, service methods, Pebble record structs, keys, indexes, list filters, mutation authorization, and negative tests all enforce the canonical ownership fields.

## Scope and routes

### Section D: Flows, Sessions, Runtime, Permissions, Notifications, Integrations
Routes from `swarmd/internal/api/server_routes.go`:
- `/v3/flows`, `/v3/flows/`
- `/v1/context/sources`
- `/v1/system/shutdown`
- `/v1/permissions`, `/v1/permissions/`
- `/v1/alerts`, `/v1/alerts/`, `/v1/alerts/summary`
- `/v1/notifications`, `/v1/notifications/`, `/v1/notifications/summary`
- `/v1/update/status`, `/v1/update/apply`, `/v1/update/local-containers`, `/v1/update/run`
- `/v1/git/sync/inspect`, `/v1/git/sync/apply`
- `/v1/integrations`, `/v1/integrations/workspaces`, `/v1/integrations/workspaces/`, `/v1/integrations/builder/sessions`
- `/v1/sessions`, `/v1/sessions/`

Also inspect peer/runtime routes that directly mutate these records:
- `/v1/swarm/peer/flows/apply`, `/v1/swarm/peer/flows/report`
- `/v1/swarm/peer/sessions/*`
- `/v1/swarm/peer/permissions/*`
- managed-host session/permission control routes where they call Section D services.

### Section F: Deploy, Container, Remote Deploy, Local Transport Sync
Routes from `swarmd/internal/api/server_routes.go`:
- `/v1/deploy/container/runtime`, `/v1/deploy/container`, `/v1/deploy/container/create`
- `/v1/deploy/container/package/defaults`, `/v1/deploy/container/package/validate`, `/v1/deploy/container/package/suggest`
- `/v1/deploy/container/settings`, `/v1/deploy/container/action`, `/v1/deploy/container/delete`
- `/v1/deploy/container/attach/child-state`, `/v1/deploy/container/attach/request`, `/v1/deploy/container/attach/approve`, `/v1/deploy/container/attach/finalize`
- `/v1/deploy/container/sync/credentials`, `/v1/deploy/container/sync/agents`, `/v1/deploy/container/sync/skills`, `/v1/deploy/container/sync/permissions`, `/v1/deploy/container/sync/model-defaults`
- `/v1/deploy/container/managed/credentials/apply`, `/v1/deploy/container/managed/agents/apply`, `/v1/deploy/container/managed/model-defaults/apply`, `/v1/deploy/container/managed/skills/apply`
- `/v1/deploy/container/workspaces/bootstrap`
- `/v1/deploy/remote/session`, `/v1/deploy/remote/session/create`, `/v1/deploy/remote/session/settings`, `/v1/deploy/remote/session/delete`, `/v1/deploy/remote/session/start`, `/v1/deploy/remote/session/update-job`, `/v1/deploy/remote/session/sync/credentials`, `/v1/deploy/remote/session/`
- local transport duplicates registered by `registerLocalTransportRoutes`.

## Evidence from current backend

Current records are not correctly user/team owned:
- Session records contain `ID`, `WorkspacePath`, `WorkspaceName`, worktree fields, `Metadata`, timestamps, and lifecycle, but no canonical `UserID` or `TeamID` (`swarmd/internal/store/pebble/session_store.go:16`). `CreateSessionWithOptions` persists `session/{sessionID}` and only adds a metadata `workspace_id` for worktrees (`swarmd/internal/session/service.go:130`, `swarmd/internal/session/service.go:159`).
- Session listing scans global `session/` and filters by path for workspace views (`swarmd/internal/store/pebble/session_store.go:146`, `swarmd/internal/store/pebble/session_store.go:150`, `swarmd/internal/store/pebble/session_store.go:164`).
- Permission records contain session/run/call/tool fields only, no owner actor fields (`swarmd/internal/store/pebble/permission_store.go:30`). Permission service uses a default `principalID = "local"` for summaries, not product UserID (`swarmd/internal/permission/service.go:22`, `swarmd/internal/permission/service.go:122`).
- Notifications are keyed by `SwarmID`, with `WorkspacePath` only as context, not ownership (`swarmd/internal/store/pebble/notification_store.go:25`, `swarmd/internal/store/pebble/notification_store.go:84`, `swarmd/internal/store/pebble/notification_store.go:260`).
- Flow definitions, assignment status, outbox, target ledger, due records, and run claims are keyed by `flowID`, `targetSwarmID`, and command/run IDs only (`swarmd/internal/store/pebble/flow_store.go:31`, `swarmd/internal/store/pebble/keys.go:69`). `/v3/flows` creates/updates/deletes definitions through `s.flows.PutDefinition` and `s.flows.DeleteDefinition` without product owner fields (`swarmd/internal/api/flows_v3.go:289`, `swarmd/internal/api/flows_v3.go:370`, `swarmd/internal/api/flows_v3.go:414`).
- Integration builder scope is hardcoded as `swarm`; builder workspace path is under a global sessions data area (`swarmd/internal/api/integration_sessions.go:17`, `swarmd/internal/api/integration_sessions.go:510`). Integration workspaces and workspace sessions persist `workspace_id/session_id` without UserID or TeamID (`swarmd/internal/store/pebble/integration_store.go:105`, `swarmd/internal/store/pebble/integration_store.go:117`).
- Deploy container records contain deployment, bootstrap, child/host swarm, group, sync, and workspace bootstrap fields, but no canonical UserID/TeamID/WorkspaceID (`swarmd/internal/store/pebble/deploy_container_store.go:25`). They are stored under `deploy/container/{deploymentID}` (`swarmd/internal/store/pebble/keys.go:47`, `swarmd/internal/store/pebble/deploy_container_store.go:147`).
- Remote deploy sessions contain SSH target, transport, group, master/child/host swarm IDs, session token, sync, and payload path fields, but no canonical product owner fields (`swarmd/internal/store/pebble/remote_deploy_store.go:52`). They are stored under `deploy/remote_session/{sessionID}` (`swarmd/internal/store/pebble/remote_deploy_store.go:165`).
- Deploy attach/sync routes authenticate with bootstrap secrets, peer auth, deployment ID, or session token; those authenticate transport only and do not prove product ownership (`swarmd/internal/api/deploy_container.go:308`, `swarmd/internal/api/deploy_container.go:552`, `swarmd/internal/api/remote_deploy.go:215`).

## Final missed-risk check before coding

Before the first code change, search and confirm every call path in this section that can create, list, mutate, delete, sync, mirror, or apply data. At minimum inspect:
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/api/flows_v3.go`, `flows_peer.go`, `flows_runner.go`, `flows_report.go`, `flows_run_now.go`, `flows_scheduler.go`
- `swarmd/internal/api/routed_sessions.go`, `server.go`, `run_stream_ws.go`, `managed_host_sessions.go`, `topology_session_routes.go`
- `swarmd/internal/api/routed_permissions.go`, `managed_host_permission_controls.go`
- `swarmd/internal/api/notifications.go`
- `swarmd/internal/api/integrations.go`, `integration_sessions.go`
- `swarmd/internal/api/git_sync.go`, update handlers, context sources handlers
- `swarmd/internal/api/deploy_container.go`, `remote_deploy.go`, `swarm_replicate_container.go`, `swarm_local_containers.go`
- `swarmd/internal/session`, `run`, `permission`, `notification`, `integration`, `flow`, `deploy`, `remotedeploy`, `runtime`
- `swarmd/internal/store/pebble/*session*`, `*permission*`, `*notification*`, `flow_*`, `integration_store.go`, `deploy_container_store.go`, `remote_deploy_store.go`, `keys.go`

If you find a route or store family not listed here that mutates these records, update the active plan/checkpoint before implementing that family.

## Exact storage/Pebble key changes expected

Use the canonical identity key prefix selected by Section A. If Section A has not yet introduced the identity key helpers, stop and checkpoint before adding Section D/F keys. The expected key shape below is mandatory in meaning: owner scope components must precede resource IDs, and legacy global keys must not remain authoritative.

### Sessions and runtime
Current keys:
- `session/{sessionID}`
- `session_mode/{sessionID}`
- `session_lifecycle/{sessionID}`
- `msg/{sessionID}/{globalSeq}`
- `session_plan/{sessionID}/{planID}`
- `session_plan_active/{sessionID}`
- `session_turn_usage/{sessionID}/{runID}`
- `session_usage_summary/{sessionID}`
- `run_wait/{sessionID}/{runID}`

Expected replacement:
- `identity/user/{userID}/session/{sessionID}` for session snapshots.
- Add secondary indexes for list views instead of global scans, for example:
  - `identity/user/{userID}/session_updated/{reverseUpdatedAt}/{sessionID}`
  - `identity/team/{teamID}/workspace/{workspaceID}/session_updated/{reverseUpdatedAt}/{sessionID}` for workspace-bound sessions.
- Messages/plans/mode/lifecycle/usage/run-wait must be reachable only through the owned session and keyed with UserID and, when applicable, TeamID/WorkspaceID:
  - `identity/user/{userID}/session/{sessionID}/msg/{seq}`
  - `identity/user/{userID}/session/{sessionID}/plan/{planID}`
  - `identity/user/{userID}/session/{sessionID}/plan_active`
  - `identity/user/{userID}/session/{sessionID}/mode`
  - `identity/user/{userID}/session/{sessionID}/lifecycle`
  - `identity/user/{userID}/session/{sessionID}/usage/{runID}`
- `SessionSnapshot` must gain canonical `UserID`, `TeamID`, and optional `WorkspaceID`; metadata copies are not authoritative.
- Session create/list/get/update/delete/run must validate the current actor owns the session or is a member of the session team.
- Peer/managed-host session mirrors must propagate the product scope and reject missing or mismatched UserID/TeamID/WorkspaceID.

### Permissions
Current keys:
- `perm/{sessionID}/{permissionID}`
- `perm_pending/{sessionID}/{createdAt}/{permissionID}`
- `run_perm/{sessionID}/{runID}/{permissionID}`
- `run_wait/{sessionID}/{runID}`
- `perm_summary/{principalID}/{sessionID}`
- `perm_policy/current`

Expected replacement:
- Permission records must gain `UserID`, `TeamID`, `WorkspaceID` when workspace-bound, and actor fields for resolver decisions where appropriate.
- Replace `principalID = local` with canonical UserID. Do not map `local` to a user.
- Key permissions under the owning session/user scope:
  - `identity/user/{userID}/session/{sessionID}/permission/{permissionID}`
  - `identity/user/{userID}/session/{sessionID}/permission_pending/{createdAt}/{permissionID}`
  - `identity/user/{userID}/session/{sessionID}/run/{runID}/permission/{permissionID}`
  - `identity/user/{userID}/session/{sessionID}/permission_summary`
- Permission policy must be explicitly classified: user policy, team policy, or system default. If mutable, it cannot remain `perm_policy/current` global.
- Approval/deny/cancel/mark-started/mark-completed must load the owning session first and verify the current actor can mutate that session's permissions.

### Notifications and alerts
Current keys:
- `notification/{swarmID}/{notificationID}`
- `notification_by_swarm/{swarmID}/{createdAt}/{notificationID}`
- `notification_summary/{swarmID}`
- `notification_permission_ref/{sessionID}/{permissionID}`

Expected replacement:
- User/team notification records must gain `UserID`, optional `TeamID`, optional `WorkspaceID`, and optional `SessionID`/`PermissionID` references.
- Key user-visible notifications by user/team scope, not swarm:
  - `identity/user/{userID}/notification/{notificationID}`
  - `identity/user/{userID}/notification_by_time/{createdAt}/{notificationID}`
  - `identity/user/{userID}/notification_summary`
  - `identity/user/{userID}/session/{sessionID}/permission_ref/{permissionID}`
- System/daemon notifications may remain separate only if they are not user mutable and do not expose user data. Use a distinct system namespace.

### Flows
Current keys:
- `flow/definition/{flowID}`
- `flow/assignment_status/{flowID}/{targetSwarmID}`
- `flow/outbox/{commandID}`
- `flow/outbox_status/{status}/{nextAttemptAt}/{commandID}`
- `flow/mirrored_run/{flowID}/{startedAt}/{runID}`
- `flow_target/accepted/{flowID}`
- `flow_target/command_ledger/{flowID}/{revision}/{commandID}`
- `flow_target/due/{dueAt}/{flowID}/{revision}`
- `flow_target/run/{runID}`
- `flow_target/run_by_flow/{flowID}/{startedAt}/{runID}`
- `flow_target/run_claim/{flowID}/{revision}/{scheduledAt}`

Expected replacement:
- Flow definitions and all derivative state must include `UserID`, `TeamID`, and any `WorkspaceID`/target TeamID scope that the flow operates on.
- Key controller-owned flow definitions by team:
  - `identity/team/{teamID}/flow/definition/{flowID}`
  - `identity/team/{teamID}/flow/assignment_status/{flowID}/{targetSwarmID}`
  - `identity/team/{teamID}/flow/outbox/{commandID}`
  - `identity/team/{teamID}/flow/outbox_status/{status}/{nextAttemptAt}/{commandID}`
  - `identity/team/{teamID}/flow/mirrored_run/{flowID}/{startedAt}/{runID}`
- Key target-side accepted state by team and flow:
  - `identity/team/{teamID}/flow_target/accepted/{flowID}`
  - `identity/team/{teamID}/flow_target/command_ledger/{flowID}/{revision}/{commandID}`
  - `identity/team/{teamID}/flow_target/due/{dueAt}/{flowID}/{revision}`
  - `identity/team/{teamID}/flow_target/run/{runID}`
  - `identity/team/{teamID}/flow_target/run_by_flow/{flowID}/{startedAt}/{runID}`
  - `identity/team/{teamID}/flow_target/run_claim/{flowID}/{revision}/{scheduledAt}`
- `targetSwarmID` remains transport routing only. It must be validated against an authorized team topology/managed-host membership before installing or running a flow.
- Peer flow apply/report must include and validate product scope before touching target-side flow keys.

### Integrations
Current keys:
- `integration/pack/{packID}`
- `integration/pack_version/{packID}/{versionID}`
- `integration/tool/{packID}/{versionID}/{toolID}`
- `integration/adapter/{packID}/{versionID}/{adapterID}`
- `integration/prompt_fragment/{packID}/{versionID}/{fragmentID}`
- `integration/assignment/{assignmentID}`
- `integration/assignment_by_agent/{agentName}/{assignmentID}`
- `integration/assignment_by_pack/{packID}/{versionID}/{assignmentID}`
- `integration/workspace/{workspaceID}`
- `integration/workspace_session/{workspaceID}/{sessionID}`
- `integration/workspace_session_updated/{workspaceID}/{updatedAt}/{sessionID}`

Expected replacement:
- Classify packs/tools/adapters/fragments as system templates or team-owned draft/published records. Mutable drafts and assignments must be team scoped.
- Integration workspace records must include `UserID`, `TeamID`, and `WorkspaceID` where they represent a real workspace; builder-only scratch sessions must still be user-owned.
- Replace hardcoded `scope = swarm` with a product owner scope.
- Expected team-scoped mutable keys:
  - `identity/team/{teamID}/integration/pack/{packID}`
  - `identity/team/{teamID}/integration/pack_version/{packID}/{versionID}`
  - `identity/team/{teamID}/integration/assignment/{assignmentID}`
  - `identity/team/{teamID}/integration/assignment_by_agent/{agentName}/{assignmentID}`
  - `identity/team/{teamID}/integration/workspace/{workspaceID}`
  - `identity/team/{teamID}/integration/workspace/{workspaceID}/session/{sessionID}`
  - `identity/team/{teamID}/integration/workspace/{workspaceID}/session_updated/{updatedAt}/{sessionID}`
- System templates may use `system/integration/...` only if immutable/read-only for users.

### Deploy containers
Current keys:
- `deploy/container/{deploymentID}`

Expected replacement:
- `DeployContainerRecord` must gain `TeamID`, `CreatorUserID`, `LastActorUserID`, optional `WorkspaceID`, and explicit authorized target/child/host scope fields.
- Key deployments by team:
  - `identity/team/{teamID}/deploy/container/{deploymentID}`
  - `identity/team/{teamID}/deploy/container_by_child_swarm/{childSwarmID}/{deploymentID}` only as routing index, not ownership.
  - `identity/team/{teamID}/workspace/{workspaceID}/deploy/container/{deploymentID}` when workspace-bound.
- Bootstrap secret may authenticate attach/sync transport but must resolve to exactly one team-owned deployment record. If the deployment record lacks team/user scope, hard-refuse.
- Sync bundles for credentials/agents/skills/permissions/model-defaults must include scope metadata and be generated only from records authorized for the deployment TeamID/UserID/WorkspaceID.
- Managed apply endpoints must reject bundles whose embedded scope does not match the receiving managed deployment/session/team.

### Remote deploy
Current keys:
- `deploy/remote_session/{sessionID}`

Expected replacement:
- `RemoteDeploySessionRecord` must gain `TeamID`, `CreatorUserID`, `LastActorUserID`, optional `WorkspaceID`, and scope-bearing sync/payload metadata.
- Key remote deploy sessions by team:
  - `identity/team/{teamID}/deploy/remote_session/{sessionID}`
  - `identity/team/{teamID}/deploy/remote_session_by_child_swarm/{childSwarmID}/{sessionID}` as routing index only.
  - `identity/team/{teamID}/workspace/{workspaceID}/deploy/remote_session/{sessionID}` when workspace-bound.
- `SessionToken`, invite token, child swarm ID, SSH target, and remote root do not authorize product access. They must resolve to a team-owned session record before any list/get/settings/delete/start/update/sync action.
- Retired remote deploy create/start routes can remain `410 Gone`, but list/settings/delete/update/sync still must not expose or mutate cross-team legacy records.

## Hard-refuse rules until conversion

Hard-refuse with an explicit identity-scope error if the backing record lacks canonical owner fields or the request actor cannot be validated:
- `/v1/sessions` create/list/get/update/delete and `/v1/sessions/{id}/run` style routes.
- Any session mirror/peer route missing propagated UserID/TeamID/WorkspaceID.
- `/v1/permissions*` list/pending/summary/resolve and `/v1/swarm/peer/permissions*`.
- `/v3/flows` create/update/delete/run-now/status/history when records or target state are global or targetSwarm-only.
- `/v1/integrations*` mutations and builder session creation/listing while using global `scope=swarm` or global integration workspaces.
- `/v1/context/sources` and `/v1/git/sync/apply` if they rely on path-only workspace authority.
- Deploy container create/settings/action/delete/attach/sync/managed-apply/workspace-bootstrap unless the deployment resolves to TeamID and authorized actor/scope.
- Remote deploy list/get/settings/delete/update/sync unless the session record is team scoped; create/start are already retired but must not become fallback paths.
- Local transport duplicates must enforce the same product scope as public routes. Local socket trust is not identity.

Read-only package defaults/validate/suggest and true daemon/system status can remain system scoped if they do not expose or mutate user-owned data.

## Phased commit/checkpoint order

1. **Session ownership foundation for this section**
   - Add owned session record fields and owned session keys/indexes.
   - Convert session create/list/get/update/delete and session events/messages/plans/mode/lifecycle/usage.
   - Hard-refuse global/path-only session operations.
   - VM gate before continuing.

2. **Permissions and notifications**
   - Bind permissions to owned sessions and canonical UserID.
   - Replace `local` principal summaries.
   - Scope notification records and summaries to user/team.
   - Actor mismatch tests for permission approval/deny/cancel and notification visibility.
   - VM gate before continuing.

3. **Flows**
   - Convert flow definitions, outbox, assignment status, target accepted state, ledgers, due/run/claim records.
   - Add product-scope propagation and validation for peer apply/report.
   - Verify targetSwarmID only routes transport after TeamID/WorkspaceID validation.
   - VM gate before continuing.

4. **Integrations and builder sessions**
   - Replace hardcoded `swarm` builder ownership with UserID/TeamID scope.
   - Convert integration workspaces/sessions and mutable pack/assignment records.
   - Verify builder session runs cannot access another user's/team's integration workspace.
   - VM gate before continuing.

5. **Deploy containers**
   - Convert deploy container records and indexes to TeamID ownership.
   - Convert create/list/get/settings/action/delete/attach/sync/workspace-bootstrap.
   - Add scope metadata to sync bundles and reject mismatched apply.
   - VM gate before continuing.

6. **Remote deploy and local transport duplicates**
   - Convert list/get/settings/delete/update/sync to TeamID-owned session keys.
   - Confirm retired create/start remain gone and do not bypass identity.
   - Enforce the same product-scope validation on local transport routes.
   - VM gate before done.

Checkpoint/update active plan before each phase if any new store family, peer route, or migration-blocking ambiguity is found.

## VM testing requirements

Every phase must be proven on a fresh VM or equivalent clean machine state. Do not accept unit tests alone.

Required fresh-VM sequence per phase:
1. Start with an empty app data directory and empty Pebble DB.
2. Bootstrap a first user by username through the canonical identity bootstrap flow from Section A.
3. Create a second user or second team where the phase needs cross-owner negative tests.
4. Restart the daemon after data creation and verify persisted ownership fields survive restart.
5. Exercise the converted route family through HTTP/API calls, not direct store calls only.
6. Capture DB key evidence showing new owner-scoped keys and absence of new writes to legacy global keys.
7. Attempt forged/mismatched requests:
   - wrong UserID session cookie/token against another user's session/deployment/flow/integration workspace;
   - wrong TeamID on a team-owned record;
   - wrong WorkspaceID for workspace-bound session/deploy/flow/integration;
   - forged sessionID/permissionID/deploymentID with no owner match;
   - transport token/bootstrap secret that resolves to a record owned by a different team;
   - peer/managed-host route with valid transport auth but missing/mismatched product scope.
8. Restart again and repeat one read/list/mutate denial and one allowed read/list/mutate to prove persistence.

Pass criteria:
- Allowed actor can create/list/get/update/delete only their scoped records.
- Cross-user and cross-team attempts fail before mutating Pebble or filesystem/runtime state.
- Legacy/global/path-only records are not silently adopted.
- No route claims conversion from middleware/auth alone; backing keys and records show ownership.
- Transport-only credentials never authorize product ownership.

Fail criteria:
- Any converted route reads `session/{id}`, `flow/definition/{id}`, `deploy/container/{id}`, or similar legacy global keys as authoritative.
- Any record can be listed or mutated by sessionID/deploymentID/path/swarmID alone.
- Sync/apply accepts a bundle without matching TeamID/UserID/WorkspaceID scope.
- A daemon restart loses owner fields or reverts to global/path-derived behavior.

## Likely attack points

- `integrationBuilderScope = "swarm"` and `appstorage.DataDir("global-sessions", ...)` create a global ownership hole for builder sessions.
- `SessionSnapshot.Metadata["workspace_id"]` is currently convenience metadata and must not be treated as authoritative ownership.
- `permission.Service.principalID = "local"` can look like an owner but is not product UserID.
- Notification `SwarmID` indexes can leak alerts across users on the same daemon if not replaced.
- Flow target state is split between controller and target stores; both sides must carry TeamID/UserID/WorkspaceID or peer flow apply/report will become an ownership bypass.
- Deploy bootstrap secrets and remote deploy session tokens prove transport possession only; they must resolve to scoped deployment/session records.
- Peer/local transport routes bypass normal browser/auth assumptions; require explicit product scope propagation.

## Relevant filepaths

- `swarmd/internal/api/server_routes.go` — route registration for Section D/F and local transport duplicates.
- `swarmd/internal/store/pebble/keys.go` — current global key families to replace.
- `swarmd/internal/store/pebble/session_store.go` — session/message/plan/lifecycle storage with no canonical owner fields.
- `swarmd/internal/session/service.go` — session create/update/mirror paths.
- `swarmd/internal/store/pebble/permission_store.go` and `swarmd/internal/permission/service.go` — session-keyed permissions, `local` principal summary, policy handling.
- `swarmd/internal/store/pebble/notification_store.go` and `swarmd/internal/api/notifications.go` — swarm-keyed notifications/alerts.
- `swarmd/internal/store/pebble/flow_store.go`, `swarmd/internal/api/flows_v3.go`, `flows_peer.go`, `flows_runner.go` — global flow definitions and target state.
- `swarmd/internal/store/pebble/integration_store.go`, `swarmd/internal/api/integrations.go`, `integration_sessions.go` — integration records and global builder scope.
- `swarmd/internal/store/pebble/deploy_container_store.go`, `swarmd/internal/api/deploy_container.go`, `swarmd/internal/deploy/service.go` — deploy container records, attach, sync, managed apply.
- `swarmd/internal/store/pebble/remote_deploy_store.go`, `swarmd/internal/api/remote_deploy.go`, `swarmd/internal/remotedeploy/service.go` — remote deploy session records and token/sync flows.
- `swarmd/internal/api/managed_host_sessions.go`, `managed_host_permission_controls.go`, `run_stream_ws.go` — peer/managed-host runtime paths that must propagate product scope.
