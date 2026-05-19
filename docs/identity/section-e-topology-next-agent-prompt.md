# Section E implementation prompt — Swarm / topology / managed hosts / peer / mirror / local containers / groups / targets

You are the coding agent assigned to Section E of the user-first identity cutover. Work only from repository evidence. Do not assume any current API object already has real product ownership. In this section, most current records are keyed by swarm ID, group ID, container ID, session ID, path, or daemon-global defaults. Your job is to convert or hard-refuse these APIs so product mutations and product resource visibility are scoped by explicit `UserID`, `TeamID`, and when workspace-bound `WorkspaceID`.

## Product model / nonnegotiables

- `UserID` is the primary actor. `TeamID` is a sharing container. `WorkspaceID` is required for workspace-bound resources.
- Username is product identity. `swarmName`, `swarmID`, peer token, socket path, attach token, hostname, filesystem path, session ID, deployment ID, and container ID are never ownership.
- No fallback IDs, guessed ownership, silent legacy adoption, dual authoritative ownership paths, or path-derived product identity.
- Converted APIs must either read/write records whose keys and JSON include the required product ownership fields, or hard-refuse before mutating/returning product resources.
- Topology and transport trust are not product authorization. Peer auth proves transport peer relationship only; it must resolve to an allowed product actor/scope before product mutation.

## Section scope and routes

Primary route registration evidence: `swarmd/internal/api/server_routes.go:55-96`, `server_routes.go:224-249`, and local transport duplicates at `server_routes.go:270-289`.

Routes in scope:

- Discovery/pairing/topology trust: `/v1/swarm/discovery`, `/v1/swarm/remote-candidates`, `/v1/swarm/invites`, `/v1/swarm/remote-pairing/start`, `/v1/swarm/remote-pairing/offer`, `/v1/swarm/remote-pairing/request`, `/v1/swarm/remote-pairing/pending`, `/v1/swarm/remote-pairing/finalize`, `/v1/swarm/remote-pairing/approve`, `/v1/swarm/enroll`, `/v1/swarm/pending-children`, `/v1/swarm/enrollment/`, `/v1/swarm/managed-host/remove`.
- Swarm state/targets/groups: `/v1/swarm/state`, `/v1/swarm/targets`, `/v1/swarm/target/current`, `/v1/swarm/target/select`, `/v1/swarm/groups`, `/v1/swarm/groups/upsert`, `/v1/swarm/groups/current`, `/v1/swarm/groups/members/delete`.
- Topology/mirror: `/v1/swarm/topology`, `/v1/swarm/topology/host-containers`, `/v1/swarm/topology/runtime-owner`, `/v1/swarm/topology/workspace-bindings`, `/v1/swarm/topology/session-route`, `/v1/swarm/mirror/resources`, `/v1/swarm/mirror/resources/delete`, `/v1/swarm/peer/mirror/snapshot`, `/v1/swarm/peer/mirror/watch`.
- Container topology: `/v1/swarm/containers/profiles`, `/v1/swarm/containers/profiles/upsert`, `/v1/swarm/containers/profiles/delete`, `/v1/swarm/containers/local/runtime`, `/v1/swarm/containers/local`, `/v1/swarm/containers/local/update-job`, `/v1/swarm/containers/local/create`, `/v1/swarm/containers/local/action`, `/v1/swarm/containers/local/delete`, `/v1/swarm/containers/local/prune-missing`.
- Replication and managed workspaces: `/v1/swarm/replicate`, `/v1/swarm/managed-workspaces/preflight`, `/v1/swarm/managed-workspaces/replicate`, `/v1/swarm/managed-workspaces/inventory`, `/v1/swarm/peer/managed-workspaces/preflight`, `/v1/swarm/peer/managed-workspaces/ensure-link`, `/v1/swarm/peer/managed-workspaces/link-existing`, `/v1/swarm/peer/managed-workspaces/import-bundle`, `/v1/swarm/peer/managed-workspaces/inventory`, `/v1/swarm/peer/workspaces/discover`, `/v1/swarm/peer/workspaces/create`, `/v1/swarm/peer/workspaces/import-bundle`, `/v1/swarm/peer/workspaces/transfer/`.
- Managed-host sessions and peer mutation routes: `/v1/swarm/managed-hosts/sessions/open`, `/v1/swarm/managed-hosts/sessions/message`, `/v1/swarm/managed-hosts/sessions/run`, `/v1/swarm/managed-hosts/sessions/stop`, `/v1/swarm/managed-hosts/workspace/git/commit`, `/v1/swarm/managed-hosts/git/sync/apply`, `/v1/swarm/managed-hosts/update/run`, `/v1/swarm/managed-hosts/update/status`, `/v1/swarm/peer/managed-host-sessions/open`, `/v1/swarm/peer/managed-host-sessions/message`, `/v1/swarm/peer/managed-host-sessions/run`, `/v1/swarm/peer/managed-host-sessions/run/stream`, `/v1/swarm/peer/managed-host-sessions/stop`, `/v1/swarm/peer/managed-host-sessions/event`, `/v1/swarm/peer/git/sync/apply`, `/v1/swarm/peer/update/run`, `/v1/swarm/peer/update/status`, `/v1/swarm/peer/permissions/create`, `/v1/swarm/peer/permissions/wait`, `/v1/swarm/peer/permissions/cancel_run`, `/v1/swarm/peer/permissions/mark_started`, `/v1/swarm/peer/permissions/mark_completed`, `/v1/swarm/peer/flows/apply`, `/v1/swarm/peer/flows/report`, `/v1/swarm/peer/sessions/open`, `/v1/swarm/peer/sessions/append_message`, `/v1/swarm/peer/sessions/mode`, `/v1/swarm/peer/sessions/title`, `/v1/swarm/peer/sessions/metadata`, `/v1/swarm/peer/sessions/lifecycle`, `/v1/swarm/peer/sessions/event`.

Concrete inventory omission to include in this section or cross-link to the workspace section: `/v1/workspace/managed-links/upsert` and `/v1/workspace/managed-links/remove` are registered at `server_routes.go:185-186` and implemented in `swarm_managed_workspace_replication.go`; they create/remove topology workspace bindings and must be owned by `UserID` + `TeamID` + `WorkspaceID`.

## Current reality to verify before coding

Evidence anchors:

- Topology response structs expose swarm/path/session fields only: `swarmd/internal/api/topology.go:12-86`.
- Topology handlers filter by `host_swarm_id`, `runtime_swarm_id`, `source_workspace_path`, or `session_id`, not product owner: `topology.go:145-260`.
- Topology records contain `SwarmID`, `HostSwarmID`, `SourceWorkspacePath`, `DestinationWorkspacePath`, and `SessionID`, but no `UserID`/`TeamID`: `swarmd/internal/store/pebble/topology_store.go:20-95`.
- Topology keys are `topology/runtime/`, `topology/host_container/`, `topology/attachment/`, `topology/workspace_binding/`, `topology/session_route/`, and `topology/migration_status/`: `swarmd/internal/store/pebble/keys.go:54-59`.
- Swarm group records are swarm-group/network records, not teams: `swarmd/internal/store/pebble/swarm_store.go:104-120`; handlers authorize with master/host swarm membership, not product identity: `swarmd/internal/api/swarm_groups.go:11-45`, `swarm_groups.go:108-167`, `swarm_groups.go:170-260`.
- Current target selection is daemon-global `swarm/desktop_target/current`: `swarmd/internal/store/pebble/keys.go:53`; handler writes selected `swarm_id` only: `swarmd/internal/api/swarm_targets.go:107-153`.
- Target list aggregates self, trusted peers, nodes, deployments, remote deployments, and mirrored resources by swarm/group health: `swarm_targets.go:174-260`; this is not product authorization.
- Local container profile and container APIs call global services without actor/scope: `swarmd/internal/api/swarm_container_profiles.go:11-112`, `swarmd/internal/api/swarm_local_containers.go:33-253`.
- Container profile records have no owner fields and are keyed by `swarm/container_profile/{profileID}`: `swarmd/internal/store/pebble/swarm_container_profile_store.go:32-48`, `swarm_container_profile_store.go:81-111`.
- Local container records have no owner fields and are keyed by `swarm/local_container/{containerID}`: `swarmd/internal/store/pebble/swarm_local_container_store.go:21-37`, `swarm_local_container_store.go:70-100`.
- Mirror resources are keyed by local/remote sequence, managed swarm ID, kind, and ID, not product owner: `swarmd/internal/api/swarm_peer_mirror.go:114-180`, `swarm_peer_mirror.go:183-260`; storage uses `ManagedSwarmID` only for remote resources: `swarmd/internal/store/pebble/swarm_mirror_store.go` and key constants in `keys.go:60-64`, `keys.go:734-759`.
- Peer auth currently validates only `X-Swarm-Peer-*` style swarm peer credentials: `swarmd/internal/api/swarm_peer_workspaces.go:205-225`. This is transport trust, not product scope.
- Peer workspace APIs list/create/import workspace paths after peer auth only: `swarm_peer_workspaces.go:56-129`, `swarm_peer_workspaces.go:132-202`.
- Managed-host session open propagates swarm route/path metadata and creates session route records with child swarm/backend/path, not `UserID`/`TeamID`/`WorkspaceID`: `swarmd/internal/api/managed_host_sessions.go:36-123`, `managed_host_sessions.go:125-214`.
- Replication request names workspaces by source path and target swarm/container mode: `swarmd/internal/api/swarm_replicate.go:41-98`, `swarm_replicate.go:112-180`.
- Pairing/invites/enrollment records carry swarm IDs, group IDs, tokens, public keys, and transport details, not product owners: `swarmd/internal/api/swarm_pairing.go:29-162`; stored records are `SwarmInviteRecord`, `SwarmEnrollmentRecord`, and `SwarmTrustedPeerRecord` at `swarmd/internal/store/pebble/swarm_store.go:51-102`.
- Remote candidates expose network devices/endpoints and must stay bootstrap/network discovery, not product-owned resources: `swarmd/internal/api/remote_candidates.go:57-82`, `remote_candidates.go:97-129`.

## Required storage and key changes

Do not just add middleware. For each converted route, change the backing records and indexes.

1. Topology records:
   - Keep transport topology facts separate from product ownership where they are truly daemon/network facts.
   - For topology records that expose or mutate product resources, add `UserID`, `TeamID`, and `WorkspaceID` where workspace-bound.
   - Replace product-resource lookups by raw path/session/swarm with owner-scoped indexes, for example `topology/workspace_binding/by_team/{teamID}/workspace/{workspaceID}/...` and `topology/session_route/by_team/{teamID}/workspace/{workspaceID}/session/{sessionID}`. Exact key names may vary, but old path-only keys cannot remain authoritative for product access.
   - `SourceWorkspacePath` may remain metadata only after `WorkspaceID` is authoritative.

2. Swarm groups:
   - Decide and implement one canonical model: either groups are network topology only, or each group maps to a real `TeamID` record.
   - If mapped to teams, add explicit `TeamID`, creator/admin `UserID`, and membership authorization; update group/membership keys so `groupID`/`swarmID` are not product owner.
   - If kept topology-only, group APIs cannot grant product sharing and must hard-refuse product actions that require team scope.

3. Targets and current target:
   - Replace daemon-global `swarm/desktop_target/current` with user/team-scoped selection, e.g. current target per `UserID` and `TeamID`.
   - List/select only targets visible to the actor’s team scope. A selectable swarm/container/mirror target must carry or resolve to an allowed `TeamID`, and workspace-bound targets must resolve to `WorkspaceID`.

4. Local containers and container profiles:
   - Add owner fields to records: at minimum `TeamID`, `CreatedByUserID`, `UpdatedByUserID`; add `WorkspaceID` for mounts/bindings that reference workspaces.
   - Change list/get/update/delete to require actor scope and filter by `TeamID`.
   - Do not auto-adopt existing global containers/profiles. Legacy records without owner scope must be invisible or refused until an explicit migration/adoption flow is approved and audited.

5. Mirror and peer resources:
   - Add product scope to mirrored resources and events: `TeamID`, source `UserID` or actor context, and `WorkspaceID` where resource kind is workspace/session/permission/flow-target.
   - Remote mirror cursors keyed only by `managedSwarmID` are insufficient for product resource pooling. Cursor/resource keys must partition by team/scope or the mirror payload must be hard-filtered and validated before storage.
   - Peer snapshot/watch must require peer trust plus propagated product scope, not just swarm peer token.

6. Managed-host sessions and peer sessions:
   - Session open/message/run/stop/event must carry and persist `UserID`, `TeamID`, `WorkspaceID` for workspace-bound sessions.
   - Session route records must include the product scope and route lookups must verify the actor can access that session/workspace/team.
   - Mirrored session storage must not write an ownerless mirror into global session indexes.

7. Pairing/invites/enrollment/trusted peers:
   - Invites that can create product access must include inviter `UserID`, target `TeamID`, and required admin permission.
   - Enrollment approval must validate actor permission for the target team and write trusted peer relationships scoped to that team if they affect product resource access.
   - Discovery can remain bootstrap-safe network output only; once a route exposes workspaces, sessions, containers, targets, groups, mirror resources, or permissions, it must require product identity.

## Hard-refuse rules until converted

Add explicit refusal gates before mutation or product-resource reads for:

- target select/current/list for product resources unless actor `UserID`/`TeamID` scope is active and backing selection/resource indexes are owner-scoped;
- group upsert/current/member delete unless the group/team mapping is explicit;
- local container create/action/delete/prune and profile upsert/delete unless records are owner-scoped;
- topology workspace bindings/session routes/runtime-owner lookups that return workspace/session/container product context without owner verification;
- mirror resource list/delete and peer mirror snapshot/watch without propagated product scope;
- managed-host session open/message/run/stop/event and peer session/permission/flow/git/update routes without validated product actor + team + workspace scope;
- managed workspace replication/import/link/create/discover when requested by peer auth alone;
- any peer-driven delete/update/action that identifies the target by path, swarm ID, session ID, container ID, deployment ID, or mirror ID alone.

Discovery-only network routes may return minimal network/bootstrap data, but must not include product resources or silently infer ownership.

## Phased commit/checkpoint order

Split this section. Do not land one giant conversion.

1. Section E.1 — Refusal and scope plumbing:
   - Add request actor/scope extraction for Section E handlers.
   - Add hard-refuse checks for unconverted product-resource routes.
   - Add tests proving ownerless legacy product routes fail.

2. Section E.2 — Targets/groups decision:
   - Implement the team-vs-topology group model.
   - Convert target current/select/list to actor/team-scoped storage and filtering.

3. Section E.3 — Local containers/profiles:
   - Convert record structs, keys/indexes, services, handlers, and tests.
   - Refuse or explicitly migrate legacy ownerless records; no silent adoption.

4. Section E.4 — Topology workspace/session routes:
   - Convert product-bearing topology bindings/routes to `TeamID`/`WorkspaceID` and actor authorization.
   - Keep network topology facts separate from product ownership.

5. Section E.5 — Managed hosts, peer sessions, replication, mirror:
   - Propagate actor/team/workspace scope over peer requests.
   - Validate peer relationship plus product scope on the receiving side before mutation.
   - Partition mirror/resources/cursors by team/scope and verify workspace ownership.

Checkpoint/update the active plan after each phase with exact converted routes, backing keys, tests, and fresh-VM proof. Do not proceed past a failing VM gate.

## Fresh-VM test requirements

For every phase, prove from a fresh data directory / fresh VM equivalent:

1. Start with no product identity: guarded product routes hard-refuse; discovery-only network route responses remain minimal and safe.
2. Bootstrap/login as a user and select/create the required team/workspace through the canonical identity flow.
3. Create resources through the converted API; capture HTTP status and response JSON showing `UserID`, `TeamID`, and `WorkspaceID` where required.
4. Restart the daemon; prove records persist under owner-scoped keys and legacy/global keys are not authoritative.
5. Negative tests:
   - second user cannot list/select/mutate first user/team resources;
   - wrong team cannot access target/group/container/topology/mirror/session/managed-host resources;
   - peer token without propagated product scope is rejected;
   - path-only, swarmID-only, sessionID-only, containerID-only, deploymentID-only, and mirrorID-only requests fail.
6. Multi-VM proof for managed host/peer/mirror:
   - primary and managed host have distinct swarm IDs, but product mutation succeeds only when propagated actor/team/workspace scope is valid;
   - topology remains transport topology and does not become ownership;
   - mirror resources are partitioned by team/workspace and do not leak across teams after restart.

Artifacts to capture in checkpoint notes: route list, request/response snippets with fake IDs only, Pebble key dump or store inspection proving scoped keys, restart proof, and negative failure logs. Keep artifacts public-repo safe: no real tokens, local usernames, hostnames, absolute home paths, or secrets.

## Final missed-risk check before coding

- Search for all calls to `requirePeerAuth`, `swarmTargetsForRequest`, `UpsertSessionRoute`, `ListWorkspaceBindingsBySourcePath`, `StoreMirroredSessionWithEvent`, `ListRemoteResources`, `PutProfile`, `Put`, `SetCurrentGroup`, and `workspace.Add` from Section E handlers.
- Verify local transport route duplicates enforce the same product scope as normal peer routes.
- Verify background mirror sync does not import ownerless resources into a global store.
- Verify any UI route that indirectly invokes Section E services also passes actor/team/workspace scope.
- If any route still relies on swarm ID, group ID, path, session ID, deployment ID, or container ID as the only selector for product data, stop and either convert its backing store or hard-refuse it.
