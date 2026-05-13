# Managed host / container / target data model refactor

> Refactor planning document.
> 
> Status: **Grouped checkpoint 1 complete. Grouped checkpoint 2 ownership-backbone execution design drafted.**
> 
> This document starts inventory-first, then adds the exact structural diagnosis and a clean source-of-truth repair direction.

## Scope boundaries for this checkpoint

- Runtime code only; no production code changes.
- Tests are omitted unless they expose a runtime source, key, path, or metadata convention.
- The goal here is to inventory current data sources involved in:
  - primary host identity/state
  - managed host identity/state
  - local containers
  - managed-host-owned containers
  - swarm targets
  - router picker / route selection
  - host/container ownership
  - host linking / managed host registration
  - replication / sync / mirror paths
  - workspace-to-target links
  - session-to-target routing

## Checkpoint 1: Current data source inventory

### Legend

- **RP**: affects router picker / chat route picker behavior
- **MHV**: affects managed host visibility
- **LCV**: affects local container visibility
- **PMHCV**: affects managed-host-owned container visibility on the primary
- **CO**: affects container ownership / host-container relationship
- Impact values:
  - **Y** = direct
  - **I** = indirect
  - **N** = no known effect
- Source classification:
  - **canonical** = intended persisted owner of data
  - **derived** = recomputed view
  - **cached** = stored copy of another truth
  - **duplicate** = repeated copy of an already-stored relationship
  - **mixed** = one source is acting as more than one of the above
  - **unclear** = source role is not obvious from current code
  - **legacy** = historical compatibility behavior still shaping runtime logic

---

## A. Backend persisted stores and config sources

### A1. Host, pairing, group, and node persistence

| File path | Source | What it owns / derives / mutates | Main writers | Main readers | RP | MHV | LCV | PMHCV | CO | Classification | Obvious inconsistency or risk |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `pkg/startupconfig/config.go` | `FileConfig`; `DeployContainerBootstrap`; `RemoteDeployBootstrap`; `SwarmRoleManaged`; `PairingState*` | Persists swarm mode, child/managed role, pairing state, parent swarm id, advertise endpoints, child bootstrap ids/secrets, remote deploy bootstrap/session state | onboarding save flows; pairing/remove flows; deploy/remotedeploy services | `currentSwarmState`; local/deploy/remotedeploy services; pairing APIs; managed-host/session routing APIs | I | Y | Y | Y | I | canonical (config), duplicate | Role/pairing/endpoint truth is split between config and `SwarmStore`; callers can treat config as authoritative even after store state changes |
| `swarmd/internal/store/pebble/swarm_store.go` | key `swarm/local_node/default`; `SwarmLocalNodeRecord` | Canonical persisted local host identity: `swarm_id`, role, keys, advertise mode/address, transports | `swarm.Service.EnsureLocalState` | `currentSwarmState`; pairing APIs; target resolution; proxying; mirror projection; deploy/local container services | I | Y | I | I | I | canonical | Local host identity is canonical here, but many readers still begin from startup config and then re-hydrate this record |
| `swarmd/internal/store/pebble/swarm_store.go` | key `swarm/local_pairing/default`; `SwarmLocalPairingRecord` | Canonical persisted pairing state, parent swarm id, invite/enrollment ids, managed-auth sync status | `swarm.Service`; deploy service; managed-host remove flows | `currentSwarmState`; onboarding UI/state; pairing flows; managed-host removal | I | Y | N | I | I | canonical, duplicate | Pairing truth is duplicated with config `PairingState`, `ParentSwarmID`, and child/managed role flags |
| `swarmd/internal/store/pebble/swarm_store.go` | key prefix `swarm/trusted_peer/`; `SwarmTrustedPeerRecord` | Persisted paired peer relationships, peer auth tokens/hashes, transports, relationship (`manager` / `managed`) | `swarm.Service` trust/approve/remove flows | `currentSwarmState`; `listTrustedPeerTargets`; managed mirror sync loop; managed-host removal | Y | Y | N | I | Y | canonical, duplicate | Same remote host can also appear in `swarm/node/*`, deployment records, mirror resources, and derived target lists |
| `swarmd/internal/store/pebble/swarm_store.go` | keys `swarm/current_group/default`, `swarm/group/*`, `swarm/group_membership/*`; `SwarmGroupRecord`; `SwarmGroupMembershipRecord` | Canonical group membership graph and current-group filter | swarm group APIs; cleanup in deploy/local container delete; managed-host removal | `currentSwarmState`; `swarmTargetsForRequestWithOptions`; group APIs/UI | Y | Y | I | I | I | canonical | Group membership controls which targets are visible, but group state is separate from target/deployment records and can drift during cleanup |
| `swarmd/internal/store/pebble/swarm_node_store.go` | key prefix `swarm/node/`; `SwarmNodeRecord` | Persistent node inventory: `swarm_id`, URLs, status, source, deployment id | mirror sync (`applyRemoteMirrorEvent`); remote-deploy registration paths; manual/discovery flows | `listSwarmNodeTargets`; managed session routing helpers; target resolution | Y | Y | N | I | I | mixed, cached, duplicate, unclear | Same host can be represented as trusted peer, node, deployment child, remote deploy session, or mirror host resource; this store is both inventory and cache |

### A2. Container, deployment, and remote-session persistence

| File path | Source | What it owns / derives / mutates | Main writers | Main readers | RP | MHV | LCV | PMHCV | CO | Classification | Obvious inconsistency or risk |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `swarmd/internal/store/pebble/swarm_local_container_store.go` | key prefix `swarm/local_container/`; `SwarmLocalContainerRecord` | Canonical local runtime container inventory: container id/name, runtime, image, host API URL, mounts, status | `localcontainers.Service.Create`; `Act`; `BulkDelete`; `PruneMissing`; deploy cleanup helpers | local container APIs; mirror projection; frontend local container list | I | N | Y | I | Y | canonical | Local containers are one container world; managed-host-owned containers do not land here on the primary |
| `swarmd/internal/store/pebble/deploy_container_store.go` | key prefix `deploy/container/`; `DeployContainerRecord` | Deployment records with host/child ids, attach state, sync owner, workspace bootstrap, group info | `deploy.Service.Create`; `UpdateSettings`; attach flows; `MirrorDeployment`; `swarm_replicate_container.go` mirroring | deploy APIs; `mapDeployContainerTarget`; mirror projection; routed-session stale-route replacement; frontend deployments | Y | Y | I | Y | Y | mixed, canonical, cached | This store carries both manager intent and mirrored managed-host deployment state; frontend treats it as both deployment list and target source |
| `swarmd/internal/store/pebble/remote_deploy_store.go` | key prefix `deploy/remote_session/`; `RemoteDeploySessionRecord`; nested `Payloads` | Remote deploy bootstrap/session state, payloads, child/host ids, sync owner, transfer metadata | `remotedeploy.Service` | remote deploy UI; `mapRemoteDeployTarget`; `ensureWorkspaceReplicationLinks` | Y | Y | N | I | Y | canonical, duplicate | Remote deploy sessions can synthesize workspace replication links, creating another durable link source alongside workspace-managed link flows |
| `swarmd/internal/store/pebble/swarm_container_profile_store.go` | key prefix `swarm/container_profile/`; `ContainerProfileRecord`; `ContainerProfileMount` | Saved profile templates for container mounts/network/access hints | container profile APIs | local/deploy create flows; frontend mount/profile UI | N | N | I | I | I | canonical | Not a target source itself, but it influences how containers are mounted and therefore what “ownership” looks like from the filesystem side |

### A3. Workspace, session, route, and selection persistence

| File path | Source | What it owns / derives / mutates | Main writers | Main readers | RP | MHV | LCV | PMHCV | CO | Classification | Obvious inconsistency or risk |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `swarmd/internal/store/pebble/workspace_store.go` | key prefix `workspace/entry/`; `WorkspaceEntry`; nested `WorkspaceReplicationLink`; sync struct `WorkspaceReplicationSync` | Canonical workspace catalog plus durable workspace→target links (`target_kind`, `target_swarm_id`, `target_workspace_path`, `writable`, sync flags) | workspace service add/save; `/v1/swarm/replicate`; managed workspace replicate/link APIs; `remotedeploy.Service.ensureWorkspaceReplicationLinks` | workspace list/overview; frontend route builder; managed workspace UI | Y | Y | I | I | Y | canonical (workspace), duplicate/mixed (replication links) | `ReplicationLinks` are written by several unrelated flows and overload `target_kind` / `target_workspace_path` for local child containers, remote child workspaces, and managed-host workspaces |
| `swarmd/internal/store/pebble/session_store.go` | key `session/{id}` plus lifecycle store; `SessionSnapshot`; `SessionLifecycleSnapshot`; metadata map | Canonical session state, lifecycle, displayed workspace path/name, metadata, owner transport | session service create/update/store-mirror/update-metadata | desktop bootstrap; chat queries; hosted-session adaptation; routed/managed session flows | Y | I | I | I | I | canonical, duplicate | Session metadata carries route/target/path data that also exists in `SessionRouteRecord`, `WorkspaceReplicationLink`, target selection store, and derived `swarmTarget` views |
| `swarmd/internal/store/pebble/session_route_store.go` | key `session_route/{session_id}`; `SessionRouteRecord` | Backend per-session route cache: child swarm id/backend URL, host workspace path, runtime workspace path | routed session create path in `server.go`; flow report mirror path | routed-session proxying; stale-route retirement/replacement | Y | I | N | I | Y | canonical (for proxy path), duplicate | Duplicates session metadata route fields and can be retired/replaced based on deployment lookup rather than explicit route update |
| `swarmd/internal/store/pebble/swarm_desktop_target_store.go` | key `swarm/desktop_target/current`; `SwarmDesktopTargetSelectionRecord` | Global selected swarm target for desktop UI | `handleSwarmSelectTarget`; self-default initialization in `selectedSwarmDesktopTargetID` | `swarmTargetsForRequestWithOptions`; `currentRemoteSwarmTargetForRequest`; workspace overview remote proxy | Y | Y | N | I | N | canonical (selection only), duplicate | Global target selection coexists with per-session route state and per-workspace default route state |

### A4. Mirror persistence

| File path | Source | What it owns / derives / mutates | Main writers | Main readers | RP | MHV | LCV | PMHCV | CO | Classification | Obvious inconsistency or risk |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `swarmd/internal/store/pebble/swarm_mirror_store.go` | keys `swarm/mirror/local/seq`, `swarm/mirror/local/event/*`, `swarm/mirror/local/resource/*`, `swarm/mirror/remote/cursor/*`, `swarm/mirror/remote/resource/*`; `SwarmMirrorResourceRecord`; `SwarmMirrorEventRecord`; `SwarmMirrorCursorRecord` | Durable mirror/event cache for resource kinds `host`, `workspace`, `container`, `deployment`, `target` | `refreshLocalMirrorProjections`; `syncMirrorFromTarget`; `applyRemoteMirrorEvent`; mirror delete API | `listMirroredSwarmTargets`; `/v1/swarm/mirror/resources`; mirror UI; sync cursor logic | Y | Y | Y | Y | Y | cached, derived, duplicate | Mirror resources are derived, but persisted and then re-used as if they were an independent source; remote mirror deletion is a separate write path into the cache |

---

## B. Backend derived views, sync paths, caches, and metadata writers

| File path | Source | What it owns / derives / mutates | Main writers | Main readers | RP | MHV | LCV | PMHCV | CO | Classification | Obvious inconsistency or risk |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `swarmd/internal/api/onboarding.go`<br>`swarmd/internal/swarm/service.go` | `currentSwarmState(cfg)`; DTOs `LocalState`, `LocalNodeState`, `PairingState`, `TrustedPeer`; `EnsureLocalState` | Derived combined host state from startup config + `SwarmStore`; also writes back missing local-node/pairing defaults | `currentSwarmState`; `EnsureLocalState` | nearly every swarm API; target resolution; pairing UI; deploy/local container/bootstrap flows | Y | Y | I | I | I | derived, write-through | The central read model also mutates persistence, so “read current state” can create/update backing records |
| `swarmd/internal/api/swarm_targets.go` | `swarmTarget`; `swarmTargetsResponse`; `swarmTargetsForRequestWithOptions`; `mapSwarmNodeTarget`; `mapTrustedPeerTarget`; `mapDeployContainerTarget`; `mapRemoteDeployTarget`; `listMirroredSwarmTargets`; `swarmTargetIdentityKeys` | Derived unified target list from local state, trusted peers, node store, deploy container store, remote deploy sessions, mirror resources, and desktop target selection | derived at request time; current flag set from selection store / query param | router picker; session create path; workspace overview proxy; managed workspace target resolution; replicate flows | Y | Y | N | Y | Y | derived, duplicate | The router picker target list is assembled from many unrelated sources with dedupe by `swarm_id`/`backend_url`, which can hide disagreement instead of resolving it |
| `swarmd/internal/api/swarm_targets.go` | `swarmTargetHealthCache`; `applyCachedSwarmTargetHealth`; `refreshSwarmTargetHealth` | In-memory online/selectable probe cache layered on top of persisted target data | background probe goroutines | target list generation | Y | Y | N | I | N | cached | `online/selectable` can disagree with stored attach/status fields and with mirror/node records |
| `swarmd/internal/api/swarm_proxy.go` | `currentRemoteSwarmTargetForRequest`; `proxyRequestToSwarmTarget`; `outgoingPeerAuthTokenForTarget` | Resolves the “current target” from request query or global selection and uses it to proxy backend calls | derived at request time | session create/run flows; workspace overview remote proxy; managed/remote replication | Y | Y | N | I | I | derived | Many backend flows implicitly depend on whichever target is globally selected, even when per-session/per-workspace routing already exists |
| `swarmd/internal/api/desktop_bootstrap.go` | `workspaceOverviewResponse`; `handleWorkspaceOverview`; `handleWorkspaceOverviewForRemoteTarget`; `workspaceOverviewSessionsByWorkspace` | Derived workspace overview from workspace store, sessions, permissions, todo summaries, git state, and current target; can proxy overview to the currently selected remote target | request-time aggregation | frontend workspace overview query and cache | Y | Y | I | I | I | derived | Selecting a non-self target changes the meaning of `/v1/workspace/overview` for the entire frontend, including path semantics |
| `swarmd/internal/api/swarm_peer_mirror.go` | mirror kind constants `host/workspace/container/deployment/target`; `peerMirrorHostResource`; `refreshLocalMirrorProjections`; `managedMirrorSyncLoop`; `syncMirrorFromTarget`; `applyRemoteMirrorEvent` | Projects local stores into mirror resources, syncs remote mirror snapshots/events, and writes mirrored host data back into `SwarmNodeStore` | mirror sync loop; request-time snapshot/watch endpoints | target list; mirror API; node store; event hub | Y | Y | Y | Y | Y | derived, cached, mixed | Derived mirror resources feed back into durable node inventory, so cache state participates in future canonical target discovery |
| `swarmd/internal/api/routed_sessions.go` | `routedSessionTarget`; `replacementChildSwarmIDForRoutedSession`; `retireStaleRoutedSessionTarget` | Derived session→target resolution from `SessionRouteStore` plus deployment lookup and backend URL matching | request-time logic | routed session proxy path | Y | I | N | I | Y | derived, duplicate | Route validity depends on deployment lookup side effects; stale routes can be retired because another deployment now shares the same backend URL |
| `swarmd/internal/session/hosted_session.go` | `HostedSessionDescriptor`; `HostedSessionMetadata*` constants; `HostedSessionFromMetadata`; `adaptHostedSessionForRuntime`; `adaptHostedSessionForLocalRuntime` | Derives hosted/routed session behavior from metadata and rewrites visible workspace paths per local/remote context | metadata written by routed/managed/flow session code | session service; frontend chat queries; overview mapping | Y | I | N | I | I | derived, duplicate | The same persisted session can present different `workspace_path` values depending on whether the caller is local, remote, hosted, or managed-host routed |
| `swarmd/internal/api/server.go` | routed session create path: `routeMetadata`; `sessionRoutes.Put(...)`; `postPeerJSONToSwarmTarget(...)` | Creates a routed session by writing session metadata, durable `SessionRouteRecord`, and a peer-open request to the remote child | `/v1/sessions` POST when current target is remote | routed session proxy; frontend session fetch | Y | I | N | I | Y | mixed, duplicate | Route truth is written into both the session record and the session-route store at creation time |
| `swarmd/internal/api/managed_host_sessions.go` | `managedHostSessionMetadata`; `resolveManagedHostSessionTarget`; managed-host route metadata fields | Writes managed-host-specific session metadata (`swarm_managed_host_*`, `swarm_route_*`, `owner_transport=managed_host_peer`) | managed host session open/run flows | managed-host session APIs; session service; frontend route parsing | Y | Y | N | I | I | duplicate | Managed-host session metadata is a second route schema beside routed-session metadata; both also overlap `WorkspaceReplicationLink` and current target selection |
| `swarmd/internal/api/flows_runner.go`<br>`swarmd/internal/api/flows_mirror.go` | flow session metadata keys `swarm_target_*`, `target_*`, `owner_transport=flow_scheduler`, hosted-session metadata | Writes flow/session target metadata and mirrored hosted metadata for flow runs | flow scheduler / flow mirror paths | flow session UI, flow report, session service | I | I | N | I | I | duplicate, unclear | Flow sessions carry a separate target vocabulary (`swarm_target_*`, `target_*`) that overlaps but does not match the routed-session keys |
| `swarmd/internal/workspace/service.go` | `ensureRemoteChildWorkspaceEntries` | Auto-registers mounted remote child workspaces under the child runtime workspace root | workspace service on `ListKnown()` when child runtime is explicit | workspace list/overview | I | I | N | I | N | derived, cached | Workspace visibility can change because mounted directories were discovered locally, not because a workspace link or target relationship was explicitly created |
| `swarmd/internal/remotedeploy/service.go` | `ensureWorkspaceReplicationLinks(record)` | Derives durable `WorkspaceReplicationLink` entries from attached remote deploy payloads | remote deploy attach/approval path | workspace route builder; workspace overview | Y | I | N | I | Y | derived, duplicate | Remote deploy payloads can create the same workspace→target relationship graph that swarm replicate and managed-workspace link flows also write |
| `swarmd/internal/api/swarm_replicate_container.go`<br>`swarmd/internal/deploy/service.go` | `mirrorManagedHostDeployment`; `createReplicatedContainer`; `MirrorDeployment` | Mirrors managed-host-owned deployments into the primary’s `DeployContainerStore` after remote create | managed-host replicate create path | deploy list; target aggregation; managed-host delete path | Y | Y | N | Y | Y | cached, duplicate, mixed | The primary stores remote managed-host deployments in the same record shape as locally managed child deployments |
| `swarmd/internal/localcontainers/service.go`<br>`swarmd/internal/deploy/service.go` | `findChildAttachments`; `deleteContainer`; `deleteDeployment` | Cleanup logic that infers child ownership from local container/deployment records, then removes trusted peers, group memberships, replication links, and auth state | local/deploy delete paths | delete result reporting; swarm cleanup | I | Y | Y | Y | Y | derived | Ownership cleanup is inferred from runtime/deployment records rather than from one canonical host↔container relationship graph |

---

## C. Backend API families and endpoint surfaces

| File path | Endpoint(s) / handler(s) | What it owns / derives / mutates | Main writers | Main readers | RP | MHV | LCV | PMHCV | CO | Classification | Obvious inconsistency or risk |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `swarmd/internal/api/server_routes.go` → `swarm_pairing.go`, `swarm_targets.go` | `/v1/swarm/state` → `handleSwarmState`<br>`/v1/swarm/targets` → `handleSwarmTargets`<br>`/v1/swarm/target/current` → `handleSwarmCurrentTarget`<br>`/v1/swarm/target/select` → `handleSwarmSelectTarget` | Exposes current swarm state and derived target list; mutates global desktop target selection | target select writes `swarm/desktop_target/current` | desktop layout/router picker; replicate/session/overview code | Y | Y | N | I | I | mixed | Global selected target is a first-class backend write, but per-session route state is elsewhere |
| `swarmd/internal/api/swarm_local_containers.go` | `/v1/swarm/containers/local/runtime`<br>`/v1/swarm/containers/local`<br>`/create` `/action` `/delete` `/prune-missing` | Local container CRUD and local runtime inventory | local container service | frontend local container views; mirror projection; delete cleanup | I | N | Y | I | Y | canonical API for one container world | Separate CRUD family from deployment CRUD; primary-only local containers are not the same thing as managed-host-owned containers |
| `swarmd/internal/api/deploy_container.go` | `/v1/deploy/container`<br>`/create` `/settings` `/action` `/delete` | Deployment CRUD, attach state, sync settings; non-peer delete can fan out to target host before deleting local record | deploy service | frontend deployments; target aggregation; stale route cleanup | Y | Y | I | Y | Y | canonical API for second container/deployment world | Same endpoint family is used for local child deployments and mirrored managed-host-owned deployments |
| `swarmd/internal/api/swarm_replicate.go` | `/v1/swarm/replicate` | Creates local child deployments, managed-host-owned deployments, or remote workspace replication links; writes workspace links as side effect | swarm replicate API | frontend replicate UI; workspace route builder | Y | Y | I | Y | Y | mixed | One endpoint writes deployment state, target relationships, workspace links, and sometimes managed-host materialization state |
| `swarmd/internal/api/swarm_replicate_container.go` | `/v1/swarm/managed-host/container/delete` | Deletes containers on managed host by posting directly to remote `/v1/swarm/containers/local/delete`; can also mirror remote create into local deploy store | managed-host container delete/create flows | frontend managed-host delete UI | I | Y | N | Y | Y | mixed | Managed-host container deletion bypasses the local container list on the primary and talks to the remote local-container API directly |
| `swarmd/internal/api/swarm_pairing.go` | `/v1/swarm/managed-host/remove` plus remote pairing/linking endpoints | Links/registers managed hosts; removes managed hosts; mutates config, trusted peers, group memberships, and pairing state | pairing/remove flows | onboarding UI; desktop layout link-request UI | I | Y | N | I | I | mixed | Managed-host removal updates both config and store state and may attempt remote cleanup over peer auth |
| `swarmd/internal/api/swarm_managed_workspace_replication.go` | `/v1/swarm/managed-workspaces/inventory`<br>`/preflight` `/replicate`<br>`/v1/workspace/managed-links/upsert` `/remove`<br>peer managed-workspace endpoints | Managed-host workspace inventory, preflight, import/link execution, and durable managed-workspace link registration | managed-workspace APIs | managed host link review UI; workspace route builder | Y | Y | N | I | I | mixed | Writes `WorkspaceReplicationLink` records with `target_kind=managed_host`, introducing a third workspace→target relation path beside local/remote replicate flows |
| `swarmd/internal/api/swarm_peer_mirror.go` | `/v1/swarm/mirror/resources`<br>`/resources/delete`<br>`/v1/swarm/peer/mirror/snapshot`<br>`/watch` | Mirror resource read/delete plus peer snapshot/watch sync | mirror APIs and sync loop | mirror UI; sync loop; target aggregation | Y | Y | Y | Y | Y | cached API surface | A cache is exposed and writable as an API, even though it is not canonical data |
| `swarmd/internal/api/server.go` | `/v1/sessions` create path | Creates local or routed sessions; routed create writes session metadata + `SessionRouteRecord` + remote peer session | session service and routed session path | frontend chat create flow | Y | I | N | I | I | mixed | Session creation is also a route-creation and metadata-duplication path |
| `swarmd/internal/api/managed_host_sessions.go` | managed-host session endpoints:<br>`/v1/swarm/managed-hosts/sessions/open|message|run|stop`<br>peer equivalents under `/v1/swarm/peer/managed-host-sessions/*` | Managed-host-specific mirrored/routed session CRUD and stream behavior | managed host session APIs | frontend managed-host session paths; permission controls | Y | Y | N | I | I | mixed | Separate managed-host session transport stack overlaps routed-session stack but uses different metadata keys and path constants |
| `swarmd/internal/api/desktop_bootstrap.go` | `/v1/workspace/overview` | Returns local overview or proxies remote overview based on current selected target; bundles workspaces, sessions, pending permissions, todos, swarm target | overview aggregator | frontend workspace overview cache | Y | Y | I | I | I | derived | Current target selection changes whether overview is local or remote, so the same frontend query can mean different backend graphs |
| `swarmd/internal/api/swarm_groups.go` | `/v1/swarm/groups`<br>`/groups/upsert` `/groups/current` `/groups/members/delete` | Group CRUD and current-group selection | swarm group APIs | target filtering; groups UI | Y | Y | I | I | I | canonical API over separate group graph | Target visibility depends on group membership, but groups are not co-located with target records |
| `swarmd/internal/api/swarm_container_profiles.go` | `/v1/swarm/containers/profiles`<br>`/upsert` `/delete` | Profile template CRUD for mounts/network settings | container profile APIs | local/deploy create flows and UI | N | N | I | I | I | canonical | Container CRUD and container-template CRUD are separate models with overlapping mount/network semantics |

---

## D. Frontend data sources, stores, caches, and router-picker paths

| File path | Source | What it owns / derives / mutates | Main writers | Main readers | RP | MHV | LCV | PMHCV | CO | Classification | Obvious inconsistency or risk |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `web/src/features/desktop/swarm/api/swarm-targets.ts` | `SwarmTarget`; `fetchSwarmTargets()`; `fetchCurrentSwarmTarget()`; `selectSwarmTarget()` | Frontend DTO and query/mutation surface for the backend target list and global selected target | backend `/v1/swarm/targets` + `/target/select` | desktop layout; chat panel route building | Y | Y | N | I | I | derived client view | Frontend type only allows `kind: 'self' | 'local' | 'remote' | 'host'`, but backend can emit other kinds like `manager` / `mirrored` |
| `web/src/features/desktop/onboarding/api.ts` | `SwarmLocalState`; `SwarmLocalContainer`; `fetchSwarmState()`; `fetchSwarmLocalContainers()`; `fetchSwarmLocalRuntimeStatus()`; `removeManagedHostLink()` | Frontend DTO/query surface for swarm state, local containers, and managed-host removal | backend onboarding/swarm/local-container APIs | onboarding UI; settings; link/remove flows | I | Y | Y | I | I | derived client view | Host state, pairing state, and local container state are fetched through a different type surface than targets or deployments |
| `web/src/features/desktop/swarm/api/deploy-container.ts` | `DeployContainerDeployment`; `RemoteDeploySession`; `fetchDeployContainers()`; `fetchRemoteDeploySessions()`; `deleteManagedHostLocalContainersViaManager()` | Frontend DTO/query surface for deployments, remote deploy sessions, and managed-host container delete path | backend deploy/remote-deploy APIs | deployment UIs; update flow; target-related UI | Y | Y | I | Y | Y | derived client view | Frontend now has separate lists for local containers, deployments, remote deploy sessions, and mirror resources |
| `web/src/features/desktop/swarm/api/managed-workspace-replication.ts` | `ManagedWorkspaceInventoryResponse`; `preflightManagedWorkspaces()`; `replicateManagedWorkspaces()` | Managed-host workspace inventory/preflight/replicate client model | backend managed-workspace APIs | managed-host link review UI | Y | Y | N | I | I | derived client view | Introduces a workspace-link model separate from container/deployment/target models |
| `web/src/features/desktop/swarm/api/replicate-swarm.ts` | `replicateSwarm()` and its response/link DTOs | Client entry point for local child creation, remote workspace replication, and managed-host-targeted local-container creation | backend `/v1/swarm/replicate` | replicate UI | Y | Y | I | Y | Y | mixed client view | One client call returns both swarm/container info and workspace link info, reinforcing the mixed backend write path |
| `web/src/features/desktop/swarm/api/swarm-mirror.ts` | `fetchSwarmMirrorResources()`; `deleteSwarmMirrorResources()`; mirror host/workspace/container/deployment DTOs | Direct client access to mirror cache resources | backend mirror APIs | mirror/debug/admin UI | I | Y | Y | Y | Y | cached client view | Makes the persistent cache a first-class frontend source in addition to the canonical APIs |
| `web/src/features/workspaces/launcher/types/workspace.ts` | `WorkspaceEntry`; `WorkspaceReplicationLink`; `mapWorkspaceReplicationLink()` | Frontend workspace catalog and durable workspace→target link type | workspace APIs and mapping | chat route builder; workspace UIs | Y | Y | I | I | Y | canonical client mirror, duplicate | `targetKind` and `targetWorkspacePath` are overloaded across local child, remote child, and managed-host workspace semantics |
| `web/src/features/workspaces/launcher/types/workspace-overview.ts` | `WorkspaceOverviewResponse`; `WorkspaceOverviewSession`; `mapWorkspaceOverviewResponse()`; `overviewPrefersRuntimeWorkspacePaths()` | Maps overview sessions and rewrites workspace path behavior depending on `swarm_target` | backend `/v1/workspace/overview` | workspace overview UI/cache | Y | Y | I | I | I | derived | Current target kind/relationship can change how session paths are interpreted across the whole overview |
| `web/src/features/workspaces/launcher/queries/fetch-workspace-overview.ts`<br>`web/src/features/queries/query-options.ts` | query key `['workspace-overview']`; fetcher for `/v1/workspace/overview` | React Query cache for overview | query client; invalidation from desktop layout | workspace UI | Y | Y | I | I | I | cached | Same cache key may hold local or remote-proxied overview data depending on current target |
| `web/src/features/workspaces/launcher/services/workspace-overview-cache.ts` | `syncWorkspaceOverviewSession()`; worktree/theme cache patchers | Client-side cache mutation layer over overview results | websocket/session upserts; UI mutations | workspace sidebar/home UI | I | I | I | I | I | cached, duplicate | Local cache patching can temporarily diverge from backend truth and from per-session route metadata |
| `web/src/features/desktop/types/realtime.ts` | `DesktopSessionRecord`; `DesktopStoreState` (`sessions`, `live`, `pendingPermissions`, `runtimeWorkspacePath`) | Frontend durable/live session shape and state container contract | websocket + query hydration | chat UI; desktop layout | Y | I | I | I | I | duplicate client state | Session route/path state lives in multiple client forms: query data, Zustand store, live run state, and transformed route objects |
| `web/src/features/desktop/state/use-desktop-store.ts` | `useDesktopStore` Zustand store | Live client state for sessions, notifications, active workspace/session, websocket hydration, draft/session caches | websocket events; local mutations; query hydration | all desktop/chat components | Y | I | I | I | I | cached, duplicate | Adds a fourth state layer on top of backend session store, React Query, and session metadata-derived route objects |
| `web/src/features/desktop/chat/services/chat-routing.ts` | `DesktopChatRoute`; `buildDesktopChatRouteOptions()`; `desktopChatRouteFromSessionMetadata()`; `resolveDesktopChatRouteFromSession()`; `applyDesktopChatRouteToSession()` | Central frontend route builder from `WorkspaceReplicationLink[]`, `SwarmTarget[]`, and session metadata; rewrites session path view | derived in client | route picker; chat queries; chat panel | Y | Y | I | I | Y | derived, duplicate | Router truth is assembled client-side from unrelated stores instead of one backend-owned route view |
| `web/src/features/desktop/chat/components/route-picker.tsx` | `RoutePicker` | UI route picker; infers `managed` / `remote` / `local` route kind from route fields | client-only | chat panel | Y | Y | N | I | I | derived UI view | Route kind is inferred client-side from metadata/route fields, not received as one canonical backend enum |
| `web/src/features/desktop/chat/components/desktop-chat-panel.tsx` | `routeOptions`; `handleRouteChange`; `handleSetDefaultRoute`; create-session and start-run route usage | Composes route options, persists default route, forces new session on route change, sends route with session/run requests | user actions; query data; UI settings | main chat UI | Y | Y | N | I | I | mixed | Route selection, session creation, default route persistence, and prompt routing are handled in one component using several unrelated data sources |
| `web/src/features/desktop/layout/desktop-app-page.tsx` | `swarmTargetsQuery` with key `['swarm-targets']`; `currentSwarmTarget`; `swarmTopologySignature`; `availableSwarmTargets` passed to chat | Global target query, target grouping (`self/local/remote/host`), topology-based invalidation of overview cache | query client; user target switch | desktop shell; workspace sidebar | Y | Y | I | I | I | cached, derived | Topology changes invalidate overview globally, even though session routes and workspace routes are independently persisted elsewhere |
| `web/src/features/desktop/chat/queries/chat-queries.ts` | `fetchSession()`; `updateSessionMetadata()`; `applyDesktopChatRouteToSession()` usage | Reads session wire data and rewrites it again through route/path logic | backend session APIs | chat panel; desktop store hydration | Y | I | I | I | I | duplicate client transformation | Session path/route semantics are transformed again after fetch, so wire truth is not used directly |
| `web/src/features/desktop/settings/swarm/types/swarm-settings.ts`<br>`web/src/features/desktop/settings/swarm/mutations/save-default-workspace-route.ts` | `chat.default_workspace_routes`; `defaultWorkspaceRouteId()`; `withDefaultWorkspaceRoute()` | Per-workspace default route persistence in UI settings | settings mutation | chat panel route defaults | Y | I | N | I | N | canonical (settings), duplicate | Default route state is separate from both global current target and per-session route state |
| `web/src/features/workspaces/launcher/mutations/manage-workspace-managed-link.ts` | `upsertWorkspaceManagedLink()`; `removeWorkspaceManagedLink()` | Dedicated frontend mutation path for managed-host workspace links | managed-link UI | workspace list/route builder | Y | Y | N | I | I | duplicate write path | Same `WorkspaceReplicationLink` graph is written by a separate UI flow in addition to replicate and remote-deploy flows |
| `web/src/features/desktop/swarm/components/managed-host-link-request-modal.tsx`<br>`managed-host-workspace-replication-panel.tsx` | `managedHostTargetFromPairingResult()`; `ManagedHostWorkspaceLinkPanel` | Synthesizes temporary `SwarmTarget` from pairing approval result, then inventories/preflights/links managed-host workspaces | pairing approval UI; managed workspace APIs | link review UI | Y | Y | N | I | I | derived, temporary, duplicate | Frontend can temporarily invent a target object before the backend target inventory has converged |

---

## E. Named metadata fields, query keys, caches, and events acting as additional sources

| File path | Source | What it owns / derives / mutates | Main writers | Main readers | RP | MHV | LCV | PMHCV | CO | Classification | Obvious inconsistency or risk |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `swarmd/internal/session/hosted_session.go` | Hosted-session metadata keys:<br>`swarm_routed_session`<br>`swarm_routed_host_swarm_id`<br>`swarm_routed_host_backend_url`<br>`swarm_routed_host_workspace_path`<br>`swarm_routed_runtime_workspace_path`<br>`swarm_routed_child_swarm_id` | Session-embedded route/host/child relationship data | routed session create path; flow mirror metadata; managed session metadata helpers | session service; frontend overview/chat mapping; hosted-session adaptation | Y | I | N | I | Y | duplicate | Route relationship is stored both as metadata and as `SessionRouteRecord` |
| `swarmd/internal/api/server.go` | Route metadata keys:<br>`swarm_route_id`<br>`swarm_route_label`<br>`swarm_route_target_kind`<br>`owner_transport=routed_session_peer` | Session-embedded route identity for routed sessions | `/v1/sessions` remote-target create path | frontend route parsing; session tests; debugging | Y | I | N | I | I | duplicate | Session metadata stores route id/label/kind, but there is no single canonical backend route object returned to the frontend |
| `swarmd/internal/api/managed_host_sessions.go` | Managed-host metadata keys:<br>`swarm_managed_host_*`<br>`swarm_route_target_relationship=managed`<br>`owner_transport=managed_host_peer` | Session-embedded managed-host route details and ownership transport | managed-host session open/run flows | managed-host APIs; session service; frontend route parsing | Y | Y | N | I | I | duplicate | Managed-host sessions use a different metadata namespace than routed sessions while still overlapping the same concepts |
| `swarmd/internal/api/flows_runner.go`<br>`swarmd/internal/api/flows_mirror.go` | Flow target metadata keys:<br>`swarm_target_swarm_id`<br>`swarm_target_name`<br>`swarm_target_kind`<br>`swarm_target_deployment_id`<br>`swarm_target_workspace_path`<br>`target_swarm_id`<br>`target_kind`<br>`target_name` | Flow-run target/session metadata | flow scheduler / mirror paths | flow report/mirror; session consumers | I | I | N | I | I | duplicate, unclear | Flow-run target metadata is another target schema that overlaps but does not match route metadata |
| `swarmd/internal/api/swarm_peer_mirror.go` | Event stream `swarm.mirror.updated`; mirror local event log records | Event/cursor trail for mirror cache updates | mirror sync/projection code | event hub; mirror watchers | I | Y | Y | Y | Y | cached, event log | Cache updates emit their own event stream, which can be mistaken for canonical resource mutation |
| `web/src/features/desktop/layout/desktop-app-page.tsx` | React Query key `['swarm-targets']` | Global cached target list | query client + target fetch | desktop shell; chat route builder | Y | Y | N | I | I | cached | Global UI cache can outlive route/session mutations and become the frontend's de facto source of truth |
| `web/src/features/workspaces/launcher/queries/fetch-workspace-overview.ts` | React Query key `['workspace-overview']` | Cached local/remote workspace overview | query client; topology invalidation | workspace UI | Y | Y | I | I | I | cached | Same cache key can hold remote-target-proxied data because current target selection changes backend behavior |
| `web/src/features/desktop/settings/swarm/types/swarm-settings.ts` | UI settings key `chat.default_workspace_routes` | Per-workspace default route persistence | default-route settings mutation | desktop chat panel | Y | I | N | I | N | canonical (settings), duplicate | Defaults coexist with global target selection and per-session route state |
| `web/src/features/desktop/layout/desktop-app-page.tsx` | `swarmTopologySignature` | Derived client cache-invalidation signature from target ids/relationships/roles/status/backend URL/current flag | desktop layout | invalidates overview query | Y | Y | I | I | I | derived, cached | This signature is built from already-derived target rows, so any disagreement in source rows propagates into cache invalidation behavior |

---

## F. Notable overlap patterns already visible from the inventory

These are **inventory observations only**, not the final diagnosis.

1. **Route / target selection already has at least four durable state paths**
   - global selected target: `swarm/desktop_target/current`
   - per-session route record: `session_route/{session_id}`
   - per-session metadata route fields: `swarm_route_*`, `swarm_routed_*`, `swarm_managed_host_*`
   - per-workspace default route settings: `chat.default_workspace_routes`

2. **Host identity and visibility are assembled from multiple persistent stores**
   - config (`FileConfig`)
   - `swarm/local_node/default`
   - `swarm/trusted_peer/*`
   - `swarm/node/*`
   - mirrored `host` and `target` resources
   - deployment child fields and remote deploy child fields

3. **Container identity exists in multiple incompatible shapes**
   - `SwarmLocalContainerRecord` for local runtime containers
   - `DeployContainerRecord` for deployments / attached children / mirrored managed-host deployments
   - mirrored `container` resources
   - mirrored `deployment` resources
   - remote deploy payload/session records

4. **Workspace-to-target links are written by several unrelated flows**
   - `/v1/swarm/replicate`
   - managed workspace link/import APIs
   - `remotedeploy.Service.ensureWorkspaceReplicationLinks`
   - direct managed-link UI mutations

5. **The router picker is consuming a backend-derived target view, but that view is already stitched from unrelated stores**
   - local state
   - trusted peers
   - node inventory
   - deployment records
   - remote deploy sessions
   - mirror resources
   - global selection store
   - in-memory health cache

6. **Managed-host-owned container visibility on the primary is especially split**
   - remote create is mirrored into local `DeployContainerStore`
   - remote delete can go through `/v1/swarm/managed-host/container/delete` → remote `/v1/swarm/containers/local/delete`
   - local container list on the primary does not own these containers
   - mirror cache can also expose `container` / `deployment` resources for the same remote system

7. **Session path truth is context-sensitive, not a single stored path**
   - session store persists `workspace_path`
   - hosted/routed metadata can persist host and runtime workspace paths
   - backend hosted-session adaptation rewrites paths for local/remote contexts
   - frontend overview mapping can prefer runtime paths when `swarm_target` is non-self
   - chat route application rewrites session paths again client-side

---

## Runtime filepaths inventoried in this checkpoint

### Backend
- `pkg/startupconfig/config.go`
- `swarmd/internal/store/pebble/keys.go`
- `swarmd/internal/store/pebble/swarm_store.go`
- `swarmd/internal/store/pebble/swarm_node_store.go`
- `swarmd/internal/store/pebble/swarm_local_container_store.go`
- `swarmd/internal/store/pebble/deploy_container_store.go`
- `swarmd/internal/store/pebble/remote_deploy_store.go`
- `swarmd/internal/store/pebble/workspace_store.go`
- `swarmd/internal/store/pebble/session_store.go`
- `swarmd/internal/store/pebble/session_route_store.go`
- `swarmd/internal/store/pebble/swarm_desktop_target_store.go`
- `swarmd/internal/store/pebble/swarm_mirror_store.go`
- `swarmd/internal/store/pebble/swarm_container_profile_store.go`
- `swarmd/internal/swarm/service.go`
- `swarmd/internal/workspace/service.go`
- `swarmd/internal/workspace/replication.go`
- `swarmd/internal/localcontainers/service.go`
- `swarmd/internal/deploy/service.go`
- `swarmd/internal/remotedeploy/service.go`
- `swarmd/internal/session/hosted_session.go`
- `swarmd/internal/session/service.go`
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/api/server.go`
- `swarmd/internal/api/onboarding.go`
- `swarmd/internal/api/desktop_bootstrap.go`
- `swarmd/internal/api/swarm_targets.go`
- `swarmd/internal/api/swarm_proxy.go`
- `swarmd/internal/api/swarm_peer_mirror.go`
- `swarmd/internal/api/swarm_local_containers.go`
- `swarmd/internal/api/deploy_container.go`
- `swarmd/internal/api/swarm_replicate.go`
- `swarmd/internal/api/swarm_replicate_container.go`
- `swarmd/internal/api/swarm_pairing.go`
- `swarmd/internal/api/swarm_managed_workspace_replication.go`
- `swarmd/internal/api/managed_host_sessions.go`
- `swarmd/internal/api/routed_sessions.go`
- `swarmd/internal/api/flows_runner.go`
- `swarmd/internal/api/flows_mirror.go`
- `swarmd/internal/api/swarm_groups.go`
- `swarmd/internal/api/swarm_container_profiles.go`

### Frontend
- `web/src/features/desktop/swarm/api/swarm-targets.ts`
- `web/src/features/desktop/onboarding/api.ts`
- `web/src/features/desktop/swarm/api/deploy-container.ts`
- `web/src/features/desktop/swarm/api/managed-workspace-replication.ts`
- `web/src/features/desktop/swarm/api/replicate-swarm.ts`
- `web/src/features/desktop/swarm/api/swarm-mirror.ts`
- `web/src/features/desktop/chat/services/chat-routing.ts`
- `web/src/features/desktop/chat/components/route-picker.tsx`
- `web/src/features/desktop/chat/components/desktop-chat-panel.tsx`
- `web/src/features/desktop/chat/queries/chat-queries.ts`
- `web/src/features/desktop/layout/desktop-app-page.tsx`
- `web/src/features/desktop/state/use-desktop-store.ts`
- `web/src/features/desktop/types/realtime.ts`
- `web/src/features/desktop/settings/swarm/types/swarm-settings.ts`
- `web/src/features/desktop/settings/swarm/mutations/save-default-workspace-route.ts`
- `web/src/features/workspaces/launcher/types/workspace.ts`
- `web/src/features/workspaces/launcher/types/workspace-overview.ts`
- `web/src/features/workspaces/launcher/queries/fetch-workspace-overview.ts`
- `web/src/features/workspaces/launcher/services/workspace-overview-cache.ts`
- `web/src/features/workspaces/launcher/mutations/manage-workspace-managed-link.ts`
- `web/src/features/queries/query-options.ts`
- `web/src/features/desktop/swarm/components/managed-host-link-request-modal.tsx`
- `web/src/features/desktop/swarm/components/managed-host-workspace-replication-panel.tsx`
- `web/src/features/desktop/swarm/types/container-mounts.ts`

---

## G. Checkpoint 2: Exact structural diagnosis

### G1. The exact problem is not “bad dedupe”; it is missing canonical topology ownership

Swarm does **not** persist one authoritative topology graph for the relationships between:
- swarm runtimes / hosts
- host-scoped containers
- child-swarm attachments
- workspace replication bindings
- session routing

Instead, each flow writes whichever slice it happens to know into a different store:
- pairing/trust writes peer state
- discovery/mirror writes node state
- deployment flows write deployment state
- remote deploy writes remote-session state
- replication flows write workspace links
- session create writes route metadata plus `SessionRouteRecord`
- desktop target selection writes a global selected target

Read paths then reconstruct “the truth” by stitching those slices together. That is why the system feels impossible to reason about: the relationships do not exist in one canonical place as first-class data.

### G2. `swarmTarget` is a projection pretending to be an entity

`swarmTargetsForRequestWithOptions` currently assembles one target list from:
- local swarm state
- trusted peers
- node inventory
- deployment records
- remote deploy sessions
- mirrored targets
- global selected-target state
- in-memory health cache

That means one `swarmTarget` row can represent fundamentally different things:
- the primary host
- a managed host peer
- a local child swarm created by a deployment
- a remote child swarm from remote deploy
- a mirrored/cache-derived artifact

The dedupe step (`swarmTargetIdentityKeys` over `swarm_id` and `backend_url`) hides disagreements instead of resolving them. If two stores disagree, one row survives and the conflict disappears from the API.

**Conclusion:** `swarmTarget` is not a canonical domain model. It is a lossy read model that is currently being treated as truth.

### G3. The container model is structurally split, which is why managed-host containers are not truly manageable

There are currently two incompatible container worlds on the primary:

1. **Host-local container truth**
   - `SwarmLocalContainerRecord`
   - `/v1/swarm/containers/local*`
   - canonical only for containers owned by the current host

2. **Deployment / attached-child / mirrored-managed-host truth**
   - `DeployContainerRecord`
   - `/v1/deploy/container*`
   - used for local child deployments **and** for mirrored managed-host-owned containers

The exact managed-host failure is:
- create path: managed-host container creation goes through remote `/v1/deploy/container/create`, then the primary mirrors the result into `DeployContainerStore` via `mirrorManagedHostDeployment`
- delete path: managed-host container delete does **not** operate on that same model; it bypasses it and posts directly to remote `/v1/swarm/containers/local/delete` through `/v1/swarm/managed-host/container/delete`
- local container APIs on the primary do not own those managed-host containers at all

So the primary has no single canonical answer to either of these questions:
- “what containers exist on host X?”
- “what is the entry point to act on container Y on host X?”

That is the exact reason the AI cannot manage local + managed-host containers as one coherent system. It must guess between local-container state, deploy state, remote delete shims, remote deploy sessions, and mirror data.

### G4. Route selection is ambient state plus duplicated metadata, not one explicit relationship

Route truth currently exists in multiple durable places:
- global desktop target selection: `swarm/desktop_target/current`
- per-session route record: `session_route/{session_id}`
- per-session metadata: `swarm_route_*`, `swarm_routed_*`, `swarm_managed_host_*`
- per-workspace UI defaults: `chat.default_workspace_routes`

The backend also uses target selection as ambient control state:
- `currentRemoteSwarmTargetForRequest` resolves from request query or global selection
- session create uses that ambient target to decide whether to create a routed session
- `/v1/workspace/overview` can silently mean local overview or remote-proxied overview depending on that current target

The frontend then reconstructs route truth again from:
- `WorkspaceReplicationLink[]`
- `SwarmTarget[]`
- session metadata
- path rewrite heuristics
- default-route settings

**Conclusion:** route selection is not modeled as one explicit relationship. It is inferred from several overlapping stores plus implicit current-target state.

### G5. `WorkspaceReplicationLink` is overloaded beyond recovery in its current shape

`WorkspaceReplicationLink` currently tries to represent all of the following with one generic tuple:
- local child workspace replication
- remote child workspace replication
- managed-host workspace linking/import

Because of that, `target_kind`, `target_swarm_id`, and `target_workspace_path` do not describe one stable relationship type. The route builder has to infer meaning from combinations of fields and external target lookups.

**Conclusion:** workspace links are carrying more than one relationship type and therefore cannot remain the sole durable representation for routing semantics.

### G6. Mirror/cache data is still participating in truth construction

Mirror resources are derived cache data, but they are still used in future target discovery and visibility. That means cached projections can influence later canonical-looking rows.

**Conclusion:** a cache is feeding the source-of-truth path. That guarantees drift and makes reconciliation impossible to reason about.

### G7. The single architectural failure underneath all of this

The system has **no canonical topology graph**.

More specifically, it lacks one authoritative model for:
- which swarm runtime exists
- which host owns which container
- whether a container backs a child swarm runtime
- which workspace binds to which runtime
- which route a session actually uses

Everything else in this document is a symptom of that missing graph.

---

## H. Checkpoint 3: Clean source-of-truth repair direction

### H1. Non-negotiable refactor rule

The fix is **not** to add another mapper, another target kind, another metadata namespace, or another sync pass.

The fix is to introduce **one canonical topology model**, make every mutation update that model first, and make every target/route/container view derive from it.

### H2. Canonical concepts the backend must own

These names are conceptual for the refactor document. They do **not** require immediate public/API renames.

#### 1. Canonical swarm runtime record

One canonical record per addressable swarm backend (`swarm_id`).

It should own:
- runtime identity: `swarm_id`, display name
- runtime role/relationship: self / managed / manager / child
- reachable endpoints: backend URL, desktop URL
- lifecycle/attach status
- ownership links to the host/container that back this runtime when applicable
- group visibility membership by reference, not by ad hoc target synthesis

This becomes the only backend truth from which `swarmTarget`-style picker rows are derived.

**Important:** trusted peers, node discovery, deploy attach, remote deploy attach, and mirror sync may all contribute observations, but they must converge into **this one runtime record**, not produce separate target rows.

#### 2. Canonical host-scoped container record

One canonical record per actual container on a host.

Key requirement:
- the same record shape must represent containers on the primary **and** containers on managed hosts

It should be keyed by host ownership first, not by whichever flow discovered it. Conceptually:
- `host_swarm_id`
- `container_id` or stable container ref
- runtime/image/name/status
- host-local API/control information
- mounts/workspace bindings
- optional link to the child swarm runtime it backs

This is the missing entry point today.

With this model, the system can always answer:
- list containers for host X
- get container Y on host X
- act on container Y on host X

On self, that entry point executes locally. On a managed host, it proxies through peer auth. But it is still the **same canonical model and API family**.

#### 3. Explicit container-to-runtime attachment relation

If a container boots a child swarm, that relationship must be stored explicitly.

Today that relationship is smeared across:
- `DeployContainerRecord`
- `SwarmNodeRecord`
- route metadata
- remote deploy session records

After the refactor, the system should be able to answer directly:
- does this container back a child swarm runtime?
- if yes, which `swarm_id`?
- if a runtime is attached, which host/container owns it?

This is the relation that lets container management, target selection, and routing speak about the same object graph.

#### 4. Canonical workspace binding record

Workspace routing/replication must stop being represented as a generic overloaded `WorkspaceReplicationLink`.

The durable workspace binding should explicitly reference canonical IDs, not inferred target kinds:
- source workspace path
- destination runtime `swarm_id`
- destination host `swarm_id` when relevant
- destination container ref when relevant
- destination workspace/runtime path
- replication mode
- writable/sync flags

That gives one stable meaning to a workspace binding:
- “this source workspace is bound to this exact destination runtime/path relationship”

#### 5. Canonical session route record

There must be exactly **one** backend source of truth for a session’s route.

`SessionRouteRecord` is the right place conceptually, but it must become the only durable route authority. It should reference canonical runtime/workspace binding/container ownership IDs rather than duplicating loose strings.

Session metadata may still carry display/debug hints, but:
- metadata is derived from the canonical route record
- metadata is not independently authored route truth
- route updates happen by updating the canonical session-route record only

### H3. What each existing store should become after the refactor

- `SwarmLocalContainerStore`
  - becomes one backing writer/reader for the canonical host-container model when `host_swarm_id == local`
  - no longer represents a special container world separate from managed hosts

- `DeployContainerStore`
  - stops acting as target inventory and stops acting as mirrored managed-host container truth
  - becomes orchestration/deployment intent only: bootstrap, attach settings, sync settings, approval state, etc.
  - references canonical container/runtime IDs instead of being those identities

- `RemoteDeploySessionStore`
  - remains remote deploy orchestration/session state
  - stops being an alternative target graph and stops minting durable workspace links as a side effect

- `WorkspaceEntry.ReplicationLinks`
  - either becomes a thin derived compatibility view over canonical workspace bindings or is replaced internally by a more explicit binding model
  - must stop overloading `target_kind` as a route semantics carrier

- `SessionRouteStore`
  - survives, but becomes the only route truth
  - session metadata becomes generated/derived

- `SwarmDesktopTargetSelectionStore`
  - must stop driving backend behavior
  - if retained at all, it becomes UI preference only
  - it must not decide the behavior of `/v1/sessions`, `/v1/workspace/overview`, or replication flows

- `SwarmMirrorStore`
  - remains cache/telemetry/debug state only
  - must not participate in canonical target discovery or canonical node/container ownership

### H4. Hard cuts required if we want the cleanup to be real

These are not optional polish items. If any of these stay half-alive, the zig-zag remains.

1. **Stop using `swarmTargetsForRequestWithOptions` as a topology constructor**
   - target lists must derive from canonical runtime records only

2. **Stop mirroring managed-host-owned containers into `DeployContainerRecord` as if that were canonical container truth**
   - managed-host containers must live in the canonical host-container model

3. **Stop using global backend target selection as ambient routing state**
   - every route-sensitive backend operation must receive an explicit runtime/host target

4. **Stop writing route truth into multiple metadata namespaces**
   - `SessionRouteRecord` is truth; metadata is derived

5. **Stop overloading workspace replication links to encode route kinds**
   - workspace bindings must reference canonical destination IDs

6. **Stop allowing mirror/cache state to feed canonical discovery**
   - caches can describe truth, never define it

### H5. The clean behavioral outcome we want

After this refactor, the system should behave like this:

- every addressable swarm runtime has one canonical runtime record
- every actual container has one canonical host-owned container record
- if a container backs a child swarm, that relation is explicit
- every workspace binding points to canonical destination IDs
- every session route points to one canonical route record
- target picker rows are derived views, not entities
- frontend route building consumes one backend-owned route/binding model instead of reconstructing truth from unrelated stores
- local and managed-host containers are manageable through the same conceptual entry point

### H6. Why this is the right prerequisite for networking work

Networking refactors need stable answers to these questions:
- what thing am I routing to?
- who owns it?
- what URL is canonical for it?
- what workspace path is canonical for it?
- what container/runtime/host relationship produced it?

Right now those answers are distributed across target synthesis, deployment mirrors, session metadata, workspace links, and cache state.

Once the canonical topology model exists, networking work can operate on explicit relationships instead of inference chains. That is the actual cleanup needed before further swarm routing/networking work can become reliable.

---

## I. Canonical execution contract for implementation

This section is the **non-negotiable implementation contract** for any AI or human performing this refactor.

### I1. Hard rules

1. **Canonical records own truth; everything else is a projection**
   - `swarmTarget`, `WorkspaceReplicationLink`, session metadata, overview rows, UI settings, mirror rows, and frontend route objects are never allowed to become canonical truth

2. **No new helper truth layers**
   - do not add helper stores, helper structs, helper caches, helper mappers, or helper reconciliation objects that quietly become a second source of truth
   - if a relationship is missing, add it to the canonical model instead of inventing a convenience layer

3. **No morphing one data object into another domain’s owner**
   - do not extend a projection record so it can “also” carry topology truth
   - do not make `swarmTarget` carry ownership semantics
   - do not make `WorkspaceReplicationLink` carry route semantics beyond compatibility projection
   - do not make `DeployContainerRecord` continue serving as container truth
   - do not make session metadata serve as route truth

4. **Every durable relationship gets one canonical owner**
   - if a field or relationship cannot be named with exactly one canonical record and one canonical writer, it is not ready to implement

5. **Dual-write is migration only**
   - temporary dual-write is allowed only when the canonical write happens first and legacy output is projected second
   - no long-term bi-directional syncing
   - no “keep both truths in sync forever” plan

6. **No ambient backend routing state**
   - backend behavior must not depend on global selected-target state once canonical route cutover lands

7. **Caches and mirrors observe truth only**
   - they can cache canonical outputs
   - they cannot define identity, ownership, or routing semantics

8. **Read paths must stop stitching unrelated truths**
   - if a read model still needs to infer topology by combining unrelated durable stores, the checkpoint is not complete

### I2. Quick rejection test for proposed changes

Reject any implementation step that does one of these:
- adds a new metadata key instead of fixing canonical route ownership
- adds a new `target_kind` instead of fixing canonical binding/runtime ownership
- adds fields to `DeployContainerRecord` to keep using it as mixed truth
- adds a helper mapper/store to bridge two truths instead of removing one
- keeps managed-host create and delete operating through different identities
- lets mirror/cache state continue to define canonical discovery
- teaches the frontend new inference rules because backend truth is still ambiguous

---

## J. Grouped checkpoint plan (authoritative)

This grouped plan replaces the overly fine-grained execution order as the **authoritative implementation sequence**.

### J1. Why the checkpoints are grouped this way

The grouping is designed to be:
- **large enough** to remove an entire class of ambiguity
- **small enough** to remain executable without turning into a rewrite blob
- **strictly ordered** by dependency on canonical ownership

Mapping from the earlier fine-grained breakdown:
- grouped checkpoint 1 = old checkpoints 0 + 1
- grouped checkpoint 2 = old checkpoints 2 + 3
- grouped checkpoint 3 = old checkpoints 4 + 5
- grouped checkpoint 4 = old checkpoint 6
- grouped checkpoint 5 = old checkpoints 7 + 8

### J2. Grouped Checkpoint 1 — Canonical topology contract and topology layer

**Purpose**
- establish the canonical model before any behavior cutover

**Includes**
- freeze canonical record set:
  - runtime record
  - host-container record
  - container↔runtime attachment relation
  - workspace binding record
  - session route record
- define canonical IDs and ownership rules for each
- add the topology persistence/service/query layer

**What is allowed here**
- new canonical storage
- new canonical query methods
- migration scaffolding

**What is not allowed here**
- no reader cutovers yet
- no public/API reshaping yet
- no projection structs gaining new truth responsibilities

**Definition of done**
- the topology layer can answer from its own records alone:
  - what runtimes exist
  - what containers belong to host X
  - whether container Y backs runtime R
  - what bindings exist for workspace W
  - what route session S uses

**Fail condition**
- if those answers still require stitching deploy + mirror + metadata + target rows, this checkpoint is not done

### J3. Grouped Checkpoint 2 — Canonical ownership backbone: runtimes, containers, attachments

**Purpose**
- move real operational ownership into the canonical graph
- eliminate the current split between runtime truth and container truth

**Why this is grouped together**
- runtime identity and container ownership are not separable in practice
- the system must explicitly know which host owns which container and whether that container backs a child runtime
- this is the first checkpoint that removes the operational mess instead of just documenting it

**Sub-gate A — canonical runtime convergence**
- pairing/trust flows converge into canonical runtime records
- node/discovery flows converge into canonical runtime records
- deploy attach / detach converge into canonical runtime records
- remote deploy attach / detach converge into canonical runtime records
- mirror-fed runtime visibility stops acting as an independent truth source

**Sub-gate B — canonical host-container convergence**
- local container CRUD writes canonical host-container truth
- managed-host create/delete writes canonical host-container truth
- container↔runtime attachment becomes explicit
- managed-host create and delete operate on the same canonical host-container identity

**Hard bans**
- do not keep `DeployContainerRecord` as canonical container inventory
- do not mirror managed-host containers into deploy records and call that truth
- do not leave managed-host delete on a separate identity path from managed-host create

**Definition of done**
- every addressable runtime exists as one canonical runtime record
- every actual container exists as one canonical host-container record
- container attachment to child runtime is explicit
- the backend can answer from one model:
  - list containers on self
  - list containers on managed host X
  - get container Y on host X
  - act on container Y on host X
  - resolve which runtime container Y backs, if any

**Why this is the first real behavior checkpoint**
- this is where AI/tooling finally gets one canonical container entry point instead of guessing across multiple stores

### J4. Grouped Checkpoint 3 — Canonical bindings and canonical routes

**Purpose**
- stop overloading workspace links and stop duplicating route truth

**Why this is grouped together**
- session routes should point at explicit canonical bindings
- route cleanup without binding cleanup would just move ambiguity around

**Sub-gate A — canonical workspace bindings**
- replace direct authoritative writes to overloaded `WorkspaceReplicationLink`
- make local child replication, remote child replication, and managed-host link/import explicit binding variants within the canonical model
- keep `WorkspaceReplicationLink` only as a compatibility projection if needed

**Sub-gate B — canonical session routes**
- `SessionRouteRecord` becomes the only route truth
- routed-session, managed-host-session, and flow-route writers converge on that one route owner
- session metadata becomes derived output only
- global selected target stops deciding backend route behavior

**Hard bans**
- no new `target_kind` values to encode missing canonical semantics
- no new metadata namespaces to carry route truth
- no route behavior that depends on current selected target once this checkpoint lands

**Definition of done**
- every workspace destination can be resolved through canonical binding records, not target-kind inference
- every session route can be resolved through one canonical route record, not metadata drift or ambient UI state
- `/v1/workspace/overview` and session creation no longer change meaning based on selected-target ambient state

### J5. Grouped Checkpoint 4 — Backend projection cutover

**Purpose**
- rebuild all backend-facing read models as honest projections of canonical truth

**What gets cut over**
- target list generation
- current-target resolution semantics
- proxy resolution inputs
- overview/session read-model dependencies that still depend on old stitched truths

**Canonical rule**
- backend projections may read canonical records and emit compatibility output
- they may not synthesize identity/ownership/routing by treating old stores as peer truth sources

**Hard bans**
- no direct target synthesis from:
  - deployment rows
  - remote deploy session rows
  - mirror target rows
  - node inventory as an independent source of target identity
- no backend routing behavior controlled by UI preference state

**Definition of done**
- `swarmTarget` is a pure projection of canonical runtime truth
- current-target selection is UI preference only
- backend read paths stop constructing topology from unrelated legacy stores

**Minimum safe point for networking work**
- this is the earliest safe checkpoint after which serious networking/routing work can resume
- by this point runtime, container, binding, and route truth are canonical, and backend projections are no longer stitched inference graphs

### J6. Grouped Checkpoint 5 — Frontend cutover and legacy truth demolition

**Purpose**
- stop frontend inference and permanently remove the old zig-zag paths

**Sub-gate A — frontend cutover**
- frontend consumes canonical backend route/binding/container views
- chat routing stops reconstructing truth from `WorkspaceReplicationLink[]` + `SwarmTarget[]` + session metadata + heuristics
- local and managed-host container management use one conceptual container surface
- default workspace routes remain UI preference only

**Sub-gate B — legacy demolition**
- demote/remove old truth roles for:
  - `DeployContainerStore` as container truth
  - `WorkspaceReplicationLink` as canonical binding/route truth
  - session metadata as independently-authored route truth
  - global selected target as backend routing input
  - mirror resources as topology-defining sources

**Definition of done**
- the frontend no longer needs topology inference logic to understand routing and ownership
- no persisted relationship in scope has more than one authoritative owner
- any remaining legacy field/store is compatibility output only or deletable

---

## K. Ordering constraints, pass/fail gates, and start point

### K1. Required order

1. **Checkpoint 1 before everything else**
   - otherwise later work will keep inventing conflicting shapes

2. **Checkpoint 2 before checkpoint 3**
   - bindings and routes need explicit runtime/container ownership underneath them

3. **Checkpoint 3 before checkpoint 4**
   - projections cannot become clean until binding/route truth is clean

4. **Checkpoint 4 before checkpoint 5B**
   - do not demolish legacy truth roles until backend projections are already canonical

### K2. Non-negotiable starting point

Do **not** start implementation with any of these:
- patching `swarmTargetsForRequestWithOptions`
- tweaking route picker logic
- adding another metadata key
- adding another `target_kind`
- teaching the frontend more inference rules
- extending `DeployContainerRecord` so it can keep doing mixed-domain work
- adding helper stores or helper objects to paper over missing canonical relationships

Those all preserve the current failure mode.

### K3. Pass/fail rule for every checkpoint

A checkpoint passes only if the canonical model alone can answer the questions that checkpoint owns.

Examples:
- checkpoint 2 fails if managed-host and local containers still require different identity paths
- checkpoint 3 fails if route meaning can still drift by editing metadata or relying on selected-target state
- checkpoint 4 fails if target views still need peer + node + deploy + remote-session + mirror stitching
- checkpoint 5 fails if the frontend still has to infer topology from unrelated outputs

If a checkpoint still depends on combining multiple old truths, it is not complete, even if the UI appears to work.

### K4. Recommended first implementation move

If implementation begins now, the first execution block should be:
- grouped checkpoint 1 in full

Then the first behavior-moving execution block should be:
- grouped checkpoint 2 in full

That is the earliest point where this refactor starts paying down the actual operational pain instead of just rearranging projections.

---

## L. Grouped checkpoint 2 execution design (authoritative)

This section translates grouped checkpoint 2 into the exact ownership cutovers required to make runtime/container truth canonical.

This is still a **planning-only** document update. It does **not** imply production code changes in this commit.

### L1. Checkpoint 2 boundary

Grouped checkpoint 2 owns exactly these questions:
- what runtimes exist as real backend-addressable things
- which host owns which actual container
- whether a given container backs a child runtime
- which operational delete/attach flows are allowed to mutate that ownership graph

Grouped checkpoint 2 does **not** yet own:
- workspace binding cleanup (`WorkspaceReplicationLink` overload remains checkpoint 3)
- session route cleanup (`SessionRouteRecord` sole-truth cutover remains checkpoint 3)
- target/view projection cleanup (`swarmTarget` cutover remains checkpoint 4)
- frontend inference removal (checkpoint 5)

That means compatibility DTOs may temporarily remain, but operational ownership logic must already resolve through canonical runtime/container/attachment records.

Checkpoint 2 passes only if the backend can answer ownership questions without scanning `DeployContainerRecord`, `SwarmNodeRecord`, mirror target rows, or session metadata as peer truth sources.

### L2. Canonical records that become operational truth in checkpoint 2

Checkpoint 1 froze the canonical record set conceptually. Checkpoint 2 is where three of those records become real operational truth.

#### 1. Canonical runtime record

- primary key: `swarm_id`
- owns:
  - runtime identity
  - runtime relationship (`self`, `manager`, `managed`, `child`)
  - canonical backend/desktop endpoints
  - lifecycle / reachability status
  - ownership link to the backing host/container when the runtime is container-backed
  - canonical references to group visibility membership

Rule:
- if any backend flow can route to, attach to, list, or delete a runtime-backed thing, that runtime must exist here first
- other stores may contribute observations, but they may not own runtime existence separately

#### 2. Canonical host-container record

- primary key: `host_container_id`
- natural lookup key: `(host_swarm_id, runtime_container_ref)`
- `runtime_container_ref` prefers runtime-native container identity and may temporarily fall back to launch container name until the runtime reports its final container id
- owns:
  - owning host swarm id
  - runtime/container identifiers
  - image/runtime/name/status
  - host-local control endpoint details
  - mounts / workspace materialization state
  - last observed lifecycle state

Rule:
- local containers and managed-host containers use the same record shape, the same identity semantics, and the same delete/action path
- no second “managed-host container world” is allowed to survive beside this record

#### 3. Canonical container↔runtime attachment record

- primary key: `attachment_id`
- uniqueness rules:
  - one active attachment from `host_container_id` to a given `runtime_swarm_id`
  - one active backing container for a given child runtime
- owns:
  - `host_container_id`
  - `runtime_swarm_id`
  - attach state (`launching`, `attach_requested`, `attached`, `detached`, `failed`, `removed`)
  - orchestration references such as deployment id / remote deploy session id when needed
  - attach timestamps and last attach error

Rule:
- this is the only durable answer to “what container backs runtime X?”
- `DeployContainerRecord.ChildSwarmID`, `SwarmNodeRecord.DeploymentID`, remote-session fields, and metadata fields may project this relation during migration, but they may not own it

### L3. Runtime writer convergence rules

| Existing path | Canonical responsibility after checkpoint 2 | Legacy/projection status after checkpoint 2 | Forbidden after checkpoint 2 |
| --- | --- | --- | --- |
| `swarm.Service.EnsureLocalState` | creates/updates the canonical self runtime row first | `SwarmLocalNodeRecord` and `SwarmLocalPairingRecord` can remain as compatibility/config projections | self runtime existence may not depend on config + store re-stitching |
| pairing / trust approve-remove flows | upsert canonical runtime relationship rows keyed by `swarm_id` | `SwarmTrustedPeerRecord` may retain peer-auth credential material keyed to that runtime | trusted-peer rows may not act as a second runtime inventory |
| node / discovery flows | update endpoint + reachability observations on the canonical runtime row; may create a canonical runtime row only when a real `swarm_id` and addressable endpoint are known | `SwarmNodeRecord` can remain only as discovery cache / compatibility output | node rows may not define runtime relationship, ownership, or target identity independently |
| deploy attach / detach flows | create/update the canonical child runtime row and the canonical container attachment row | `DeployContainerRecord` may project attach state and orchestration metadata | attach truth may not live primarily in deploy rows |
| remote deploy attach / detach flows | same as deploy attach/detach: write canonical runtime + attachment truth | `RemoteDeploySessionRecord` may retain orchestration/session payload state | remote deploy sessions may not act as a parallel runtime graph |
| mirror sync (`applyRemoteMirrorEvent`, projection refresh) | update observation fields on canonical runtime rows only when the runtime is real and identified | mirror rows remain telemetry/cache/debug state | mirror events may not mint separate target truth or backfeed `SwarmNodeStore` as topology authority |

Operational rule:
- if two existing writers can both mention the same `swarm_id`, they must converge into one canonical runtime row rather than produce two durable identities and hope target dedupe hides it later

### L4. Host-container writer convergence rules

| Existing path | Canonical responsibility after checkpoint 2 | Legacy/projection status after checkpoint 2 | Forbidden after checkpoint 2 |
| --- | --- | --- | --- |
| `localcontainers.Service.Create/Act/BulkDelete/PruneMissing` | writes canonical host-container truth first for `host_swarm_id == local` | `SwarmLocalContainerRecord` may remain as a local-runtime execution projection during migration | local container store may not remain a separate container world |
| `deploy.Service.Create` | creates deployment intent plus canonical host-container + attachment truth for local child containers | `DeployContainerRecord` may retain bootstrap/sync/approval intent and compatibility fields | deploy create may not mint authoritative container identity on its own |
| `deploy.Service.deleteDeployment` | resolves the canonical host-container + canonical attachment graph first, then cleans orchestration intent | deploy row deletion becomes secondary cleanup | `DeployContainerRecord.ChildSwarmID` may not remain the ownership source |
| managed-host create path in `swarm_replicate_container.go` | after the remote host creates the actual container, the primary upserts one canonical host-container record for that managed host | any compatibility deployment row must reference canonical ids only | managed-host create may not mirror remote containers into `DeployContainerRecord` and call that truth |
| managed-host delete path in `swarm_replicate_container.go` | resolves the same canonical host-container identity written by create, then proxies delete to the host and removes/tombstones that same canonical row | compatibility rows may be deleted after canonical cleanup | managed-host create and delete may not use different identity paths |
| `deploy.Service.MirrorDeployment` | if it survives temporarily at all, it may only project orchestration state referencing canonical ids | none beyond compatibility | it may not author runtime/container topology truth |

Operational rule:
- the backend must be able to answer “list containers on host X”, “get container Y on host X”, and “act on container Y on host X” from one canonical host-container model regardless of whether host X is self or a managed host

### L5. Exact demotions required for existing stores

#### `SwarmLocalContainerStore`

After checkpoint 2:
- may remain as a local-host execution-facing projection or backing persistence detail
- must no longer represent a special container universe distinct from managed hosts
- must no longer be scanned to infer child-runtime ownership without going through canonical attachments

#### `DeployContainerStore`

After checkpoint 2:
- owns orchestration intent only: bootstrap secret, attach workflow state, sync settings, approval state, workspace bootstrap intent
- must reference canonical ids such as `host_container_id` and `attachment_id`
- must stop acting as:
  - canonical container inventory
  - managed-host mirrored container truth
  - primary answer to “what runtime does this container back?”

#### `SwarmNodeStore`

After checkpoint 2:
- may remain only as discovery cache / observation projection if still needed
- must stop being an independent inventory of addressable runtimes
- must not be the place where mirror sync reintroduces topology truth through the side door

#### `SwarmTrustedPeerRecord`

After checkpoint 2:
- may retain peer-auth and trust material keyed to canonical runtime identity
- must stop acting as a second runtime list or second managed-host identity graph

#### `RemoteDeploySessionRecord`

After checkpoint 2:
- remains orchestration/session state
- may retain payloads, transfer state, approval state, SSH/systemd/bootstrap context
- must stop owning runtime identity, runtime attachment, or container ownership

#### `SwarmMirrorStore`

After checkpoint 2:
- remains cache/telemetry/debug state only
- may observe canonical runtime/container state
- must not define runtime existence, runtime ownership, or container ownership

### L6. Delete and cleanup cutover

Checkpoint 2 is not real unless delete/cleanup logic stops inferring ownership by scanning the wrong stores.

The canonical delete algorithm becomes:
1. resolve the requested container/deployment action to one canonical `host_container_id`
2. load the canonical host-container record
3. load any active canonical attachment rows for that container
4. if the container is remote-managed, proxy delete/action using `host_swarm_id` and canonical host endpoint data from that record
5. update/remove canonical attachment rows first
6. update/remove canonical runtime ownership state second
7. clean legacy projections third
8. remove secondary relationship state keyed from the canonical runtime id (group membership, peer auth material, temporary workspace-link cleanup) as migration fallout, not as the source-of-truth lookup path

Mandatory consequences:
- `localcontainers.Service.findChildAttachments` must stop being authoritative ownership discovery
- `deploy.Service.deleteDeployment` must stop treating `DeployContainerRecord.ChildSwarmID` as the cleanup graph
- managed-host delete must resolve the exact same canonical host-container identity that managed-host create wrote

Checkpoint-2-safe temporary compromise:
- checkpoint 2 may still remove `WorkspaceReplicationLink` entries by resolved child runtime id during cleanup
- but the child runtime id used for that cleanup must come from canonical attachment/runtime truth, not from deploy-row inference
- full binding ownership cleanup still belongs to checkpoint 3

### L7. Ordered execution block for checkpoint 2

1. **Finish any missing topology writer/query scaffolding from checkpoint 1**
   - enough to create/query canonical runtime, host-container, and attachment rows directly
2. **Converge self + pairing/trust runtime writers**
   - main hotspots: `swarmd/internal/swarm/service.go`, `swarmd/internal/store/pebble/swarm_store.go`
3. **Converge node/discovery/mirror writers into observation-only runtime updates**
   - main hotspots: `swarmd/internal/store/pebble/swarm_node_store.go`, `swarmd/internal/api/swarm_peer_mirror.go`
4. **Cut local container CRUD to canonical host-container writes first**
   - main hotspots: `swarmd/internal/localcontainers/service.go`, `swarmd/internal/store/pebble/swarm_local_container_store.go`
5. **Cut deploy create/attach/detach to canonical runtime + attachment writes**
   - main hotspots: `swarmd/internal/deploy/service.go`, `swarmd/internal/store/pebble/deploy_container_store.go`
6. **Cut managed-host create/delete to the same canonical host-container path**
   - main hotspot: `swarmd/internal/api/swarm_replicate_container.go`
7. **Cut remote-deploy attach/detach to canonical runtime + attachment truth**
   - main hotspots: `swarmd/internal/remotedeploy/service.go`, `swarmd/internal/store/pebble/remote_deploy_store.go`
8. **Replace cleanup inference with attachment-driven cleanup**
   - main hotspots: `swarmd/internal/localcontainers/service.go`, `swarmd/internal/deploy/service.go`
9. **Prove the checkpoint by canonical queries alone**
   - if a required answer still needs deploy + node + mirror + metadata stitching, the checkpoint is not done

### L8. Pass/fail audit for checkpoint 2

Checkpoint 2 passes only if all of these are true:
- one canonical query can list containers for any host by `host_swarm_id`
- one canonical query can resolve whether a container backs a runtime and which `swarm_id` that runtime has
- managed-host create and managed-host delete both resolve the same canonical `host_container_id`
- deleting a deployment row does not destroy container/runtime ownership truth because that truth already lives canonically elsewhere
- runtime existence no longer depends on `SwarmNodeStore`, mirror target rows, or trusted-peer rows being stitched together
- cleanup logic no longer scans deployment rows to discover child ownership

Checkpoint 2 fails if any of these remain true:
- `MirrorDeployment` still writes authoritative managed-host container rows
- `DeployContainerRecord.ChildSwarmID` is still required to know what runtime a container backs
- `localcontainers.Service.findChildAttachments` still performs authoritative ownership discovery
- `applyRemoteMirrorEvent` still backfeeds `SwarmNodeStore` as runtime truth
- local and managed-host containers still require different identity paths for create vs delete vs act

### L9. Why checkpoint 2 is the hard mandatory midpoint

This is the point where the refactor stops being a diagram exercise.

Once checkpoint 2 lands for real:
- runtime identity is no longer spread across local node state, trusted peers, node rows, deploy rows, remote deploy rows, and mirror artifacts
- container ownership is no longer split between local container records and mirrored managed-host deployment rows
- child-runtime cleanup no longer depends on guessing from deployment metadata
- checkpoint 3 can safely bind workspaces and routes to explicit canonical runtime/container ownership instead of inference chains

If checkpoint 2 is watered down, then checkpoint 3 will just re-encode the current ambiguity in new binding/route fields.

### L10. Relevant filepaths for grouped checkpoint 2

- `docs/refactors/managed-host-container-target-data-model.md`
- `swarmd/internal/swarm/service.go`
- `swarmd/internal/store/pebble/swarm_store.go`
- `swarmd/internal/store/pebble/swarm_node_store.go`
- `swarmd/internal/store/pebble/swarm_local_container_store.go`
- `swarmd/internal/store/pebble/deploy_container_store.go`
- `swarmd/internal/store/pebble/remote_deploy_store.go`
- `swarmd/internal/localcontainers/service.go`
- `swarmd/internal/deploy/service.go`
- `swarmd/internal/api/swarm_replicate_container.go`
- `swarmd/internal/api/swarm_peer_mirror.go`
