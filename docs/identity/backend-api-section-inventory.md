# Backend API Identity Ownership Inventory Sections

Source rule for this document: do not rely on `user-team-identity-api-audit.md`. This split is based on `ls`, `swarmd/internal/api/server_routes.go`, backend source inspection by section, and explorer cross-checks against route registration plus Pebble key families.

Baseline: no surveyed persisted API data object is correctly converted to UserID/TeamID ownership. Current storage is global, path-derived, swarm/host-derived, resource-ID-only, session-only, filesystem-only, token-only, or unauthored. Auth/session guards are not enough: every converted API must have backing records/keys/indexes converted to canonical ownership, or the API must hard-refuse until converted.

## Section A — Identity/Auth/Onboarding/Vault/Bootstrap

Routes:
- `/healthz`, `/readyz`, `/ws`
- `/v1/auth/codex`
- `/v1/auth/codex/oauth/start`
- `/v1/auth/codex/oauth/status`
- `/v1/auth/codex/oauth/complete`
- `/v1/auth/credentials`
- `/v1/auth/credentials/verify`
- `/v1/auth/credentials/active`
- `/v1/auth/credentials/delete`
- `/v1/auth/desktop/session`
- `/v1/auth/attach/rotate`
- `/v1/vault`
- `/v1/vault/enable`
- `/v1/vault/unlock`
- `/v1/vault/lock`
- `/v1/vault/disable`
- `/v1/vault/export`
- `/v1/vault/import`
- `/v1/onboarding`

Current storage/ownership:
- Credentials: provider/key scoped, not user scoped (`auth/credential/...`, `auth/credential_active/...`).
- Credential tag index: global/provider scoped (`auth/index/auth_tag/...`).
- Credential active state: global active per provider, not per user (`auth/credential_active/...`).
- Codex auth/defaults: global/provider scoped (`auth/codex/default`).
- Attach auth default: daemon-global (`auth/attach/default`).
- Vault metadata: global (`auth/vault/meta`).
- Managed vault keys: scope ID based, not product UserID ownership (`auth/managed_vault_key/...`).
- Desktop local session: in-memory local session, not product identity.
- Onboarding/startup config: daemon/host setup, not product user ownership.

Required actions:
- Add identity bootstrap records as the first canonical identity source: User, hidden default Team, TeamMembership, current selection.
- Keep daemon setup/onboarding separate from product identity; `swarmName` remains daemon/device identity only.
- Convert credentials/Codex/vault to require authenticated UserID and scoped records.
- Convert `auth/credentials/active` from global active provider state to UserID-scoped active state.
- Desktop local auth must authenticate a session for an existing/bootstrapped UserID; it must not create ownership by itself.
- Health/readiness remain no persisted ownership.
- `/v1/auth/credentials*`, `/v1/auth/codex*`, and `/v1/vault*` must hard-refuse without product UserID context after cutover.
- Bootstrap/onboarding is the only identity-bootstrap exception; it must be username-first and must not silently adopt legacy/global identity.

## Section B — Workspace/UI/Git/Worktree/Media

Routes:
- `/v1/ui/settings`
- `/v1/workspace/resolve`
- `/v1/workspace/select`
- `/v1/workspace/current`
- `/v1/workspace/list`
- `/v1/workspace/overview`
- `/v1/workspace/discover`
- `/v1/workspace/browse`
- `/v1/workspace/folders/create`
- `/v1/workspace/add`
- `/v1/workspace/directories/add`
- `/v1/workspace/directories/remove`
- `/v1/workspace/managed-links/upsert`
- `/v1/workspace/managed-links/remove`
- `/v1/workspace/theme`
- `/v1/workspace/rename`
- `/v1/workspace/move`
- `/v1/workspace/todos`
- `/v1/workspace/delete`
- `/v1/workspace/video/scan`
- `/v1/workspace/video/storage/reveal`
- `/v1/workspace/video/threads`
- `/v1/workspace/video/threads/`
- `/v1/workspace/image/threads`
- `/v1/workspace/image/threads/`
- `/v1/workspace/git/status`
- `/v1/workspace/git/commit`
- `/v1/workspace/git/realtime`
- `/v1/worktrees`
- `/v1/manage-worktree`

Current storage/ownership:
- UI settings: global default key (`ui/settings/default`).
- UI chat settings: global default key (`ui/chat_settings/default`).
- Current workspace: global selection (`workspace/current`).
- Workspace entries: path-derived keys/records (`workspace/entry/{path}`).
- Worktree config: global and path-derived (`worktree/global/config`, `worktree/config/{workspacePath}`).
- Todos: path-derived workspace keys (`workspace_todo/item/{workspacePath}/{todoID}`).
- Image/video threads: ID-keyed records with workspace_path filtering (`image/thread/{id}`, `video/thread/{id}`); no UserID/TeamID ownership.
- Git operations: filesystem path authority.

Required actions:
- Introduce stable WorkspaceID; path becomes metadata/location only.
- Workspace records become UserID/TeamID/WorkspaceID scoped, depending personal vs shared context.
- Current workspace selection must become UserID + active TeamID scoped so team switching cannot leak selection.
- UI settings/chat settings become UserID-scoped.
- Workspace media/todos/worktrees become WorkspaceID-scoped and validate UserID + TeamID membership.
- Git/worktree APIs must reject path-only authority and require a valid WorkspaceID under the authenticated user/team.
- Old path-derived workspace authority must be removed or hard-refused for converted routes.
- Hard-refuse until converted: `/v1/workspace/directories/add`, `/v1/workspace/directories/remove`, `/v1/workspace/rename`, `/v1/workspace/move`, `/v1/workspace/delete`, `/v1/workspace/todos`, git mutation routes, and any path-only workspace mutation.

## Section C — Agents/Custom Tools/MCP/Model/Providers/Image/Voice

Routes:
- `/v2/custom-tools`
- `/v2/custom-tools/`
- `/v2/agents`
- `/v2/agents/defaults/restore`
- `/v2/agents/defaults/reset`
- `/v2/agents/`
- `/v1/image/providers`
- `/v1/image/generations`
- `/v1/image/assets`
- `/v1/image/storage/reveal`
- `/v1/model`
- `/v1/model/catalog`
- `/v1/models/favorites`
- `/v1/models/favorites/delete`
- `/v1/providers`
- `/v1/stt/transcribe`
- `/v1/voice/status`
- `/v1/voice/profiles`
- `/v1/voice/profiles/upsert`
- `/v1/voice/profiles/delete`
- `/v1/voice/config`
- `/v1/voice/devices`
- `/v1/voice/test-stt`

Backend storage in this section without a registered HTTP route in `server_routes.go`:
- MCP server configuration storage exists (`mcp/server/...`) and is identity-sensitive even if no explicit API route is registered in `server_routes.go` today.

Current storage/ownership:
- Agents/custom tools: global names (`agent/profile/{name}`, `agent/custom_tool/{name}`).
- Active primary agent: global (`agent/active/primary`).
- Active subagents: global (`agent/active/subagent/...`).
- Agent versioning: global (`agent/version`).
- MCP servers: global server IDs (`mcp/server/{serverID}`).
- Model preference/favorites: global keys (`model_pref/global/default`, `model_favorite/...`).
- Model catalog/cache: global/system (`model_catalog/...`).
- Voice config/profiles/STT active profile: global keys (`voice/config/default`, `voice/profile/...`, `voice/profile_active/stt`).
- Image/video threads/assets: ID/path derived, no user/team ownership.
- Providers/model catalog/image providers: mostly read-only global/system data.

Required actions:
- Agent profiles/custom tools become `team_shared` or explicitly scoped system defaults plus team overrides.
- Active primary and active subagent selections become UserID-scoped.
- Agent versioning must either become scoped metadata or derived/rebuildable system metadata; it cannot remain mutable global ownership state.
- MCP server configs must become TeamID-scoped for shared tools or UserID-scoped for personal extensions; classify each MCP record before cutover.
- Model preferences/favorites and voice config/profiles become UserID-scoped.
- Image generation and assets require UserID and, where workspace-backed, TeamID + WorkspaceID.
- Provider/catalog read-only system data may remain global only if it does not expose or mutate user-owned state.
- Hard-refuse until converted: `/v2/agents` mutations, `/v2/agents/` get/update/delete/activation/tool assignment that reads global names, `/v2/custom-tools` mutations, `/v2/custom-tools/` get/update/delete, model preference/favorite mutations, voice profile/config mutations, and image generation/assets tied to workspace state.

## Section D — Flows/Sessions/Runtime/Permissions/Notifications/Integrations

Routes:
- `/v3/flows`
- `/v3/flows/`
- `/v1/context/sources`
- `/v1/system/shutdown`
- `/v1/permissions`
- `/v1/permissions/`
- `/v1/alerts`
- `/v1/alerts/`
- `/v1/alerts/summary`
- `/v1/notifications`
- `/v1/notifications/`
- `/v1/notifications/summary`
- `/v1/update/status`
- `/v1/update/apply`
- `/v1/update/local-containers`
- `/v1/update/run`
- `/v1/git/sync/inspect`
- `/v1/git/sync/apply`
- `/v1/integrations`
- `/v1/integrations/workspaces`
- `/v1/integrations/workspaces/`
- `/v1/integrations/builder/sessions`
- `/v1/sessions`
- `/v1/sessions/`

Current storage/ownership:
- Flows: global flow IDs/definitions/outbox/mirrored runs (`flow/definition/...`, `flow/assignment_status/...`, `flow/outbox/...`, `flow/outbox_status/...`, `flow/mirrored_run/...`).
- Flow target records: global target state (`flow_target/accepted/...`, `flow_target/command_ledger/...`, `flow_target/due/...`, `flow_target/run...`).
- Sessions/messages/plans/modes/lifecycle/usage: global session IDs (`session/...`, `session_mode/...`, `session_lifecycle/...`, `msg/{sessionID}/...`, `session_plan/...`, `session_plan_active/...`, `session_turn_usage/...`, `session_usage_summary/...`).
- Permissions: session/run-keyed, not product actor owned (`perm/...`, `perm_pending/...`, `run_perm/...`, `run_wait/...`).
- Permission summary has `principalID` in the key (`perm_summary/{principalID}/{sessionID}`), but this is not currently established as canonical product UserID ownership.
- Permission policy: global (`perm_policy/current`).
- Notifications: global/swarm keyed (`notification/{swarmID}/...`, `notification_by_swarm/...`, `notification_summary/{swarmID}`).
- Notification permission refs: session/permission keyed, not user owned (`notification_permission_ref/{sessionID}/{permissionID}`).
- Integrations: global packs/tools/adapters/fragments/assignments/workspaces (`integration/pack/...`, `integration/pack_version/...`, `integration/tool/...`, `integration/adapter/...`, `integration/prompt_fragment/...`, `integration/assignment/...`, `integration/assignment_by_agent/...`, `integration/assignment_by_pack/...`, `integration/workspace/...`).
- Integration workspace sessions: workspace/session keyed without user/team ownership (`integration/workspace_session/...`, `integration/workspace_session_updated/...`).
- Context sources/git sync: path-derived workspace authority.
- Update/system shutdown: daemon/global actions.

Required actions:
- Sessions must carry UserID, plus TeamID/WorkspaceID where applicable.
- Messages, plans, modes, lifecycle, usage, run waits, and session indexes must be scoped through the owning session/UserID; sessionID alone is not ownership.
- Permissions must be bound to the owning session/UserID and validate actor on approval/action.
- `perm_summary/{principalID}/{sessionID}` must define `principalID` as canonical UserID or be replaced with a canonical UserID-scoped key.
- Flows become UserID + TeamID scoped; no global flow namespace for converted APIs.
- Flow run-now/target/outbox/ledger state must validate the initiating UserID, active TeamID, and target scope.
- Notifications become UserID/team scoped or explicit system notifications with non-user mutable state separated.
- Integrations become UserID + TeamID scoped; builder scope cannot be hardcoded to `swarm` as ownership.
- Integration workspace sessions require UserID + TeamID + WorkspaceID validation.
- Context/git sync must validate WorkspaceID ownership; path-only requests hard-refuse.
- Runtime APIs without identity context must hard-refuse except true daemon/system operations.
- Hard-refuse until converted: `/v3/flows` create/update/delete/run-now, `/v1/sessions` create/list/get/update/delete, `/v1/permissions*`, `/v1/integrations*` mutations, `/v1/context/sources` path-only access, and `/v1/git/sync/apply` path-only apply.

## Section E — Swarm/Topology/Managed Hosts/Peer/Mirror/Local Containers/Groups/Targets

Routes:
- `/v1/swarm/discovery`
- `/v1/swarm/remote-candidates`
- `/v1/swarm/invites`
- `/v1/swarm/remote-pairing/start`
- `/v1/swarm/remote-pairing/offer`
- `/v1/swarm/remote-pairing/request`
- `/v1/swarm/remote-pairing/pending`
- `/v1/swarm/remote-pairing/finalize`
- `/v1/swarm/remote-pairing/approve`
- `/v1/swarm/managed-host/remove`
- `/v1/swarm/managed-host/container/delete`
- `/v1/swarm/managed-hosts/sessions/open`
- `/v1/swarm/managed-hosts/sessions/message`
- `/v1/swarm/managed-hosts/sessions/run`
- `/v1/swarm/managed-hosts/sessions/stop`
- `/v1/swarm/managed-hosts/workspace/git/commit`
- `/v1/swarm/managed-hosts/git/sync/apply`
- `/v1/swarm/managed-hosts/update/run`
- `/v1/swarm/managed-hosts/update/status`
- `/v1/swarm/enroll`
- `/v1/swarm/pending-children`
- `/v1/swarm/enrollment/`
- `/v1/swarm/state`
- `/v1/swarm/targets`
- `/v1/swarm/topology`
- `/v1/swarm/topology/host-containers`
- `/v1/swarm/topology/runtime-owner`
- `/v1/swarm/topology/workspace-bindings`
- `/v1/swarm/topology/session-route`
- `/v1/swarm/mirror/resources`
- `/v1/swarm/mirror/resources/delete`
- `/v1/swarm/target/current`
- `/v1/swarm/target/select`
- `/v1/swarm/groups`
- `/v1/swarm/groups/upsert`
- `/v1/swarm/groups/current`
- `/v1/swarm/groups/members/delete`
- `/v1/swarm/containers/profiles`
- `/v1/swarm/containers/profiles/upsert`
- `/v1/swarm/containers/profiles/delete`
- `/v1/swarm/containers/local/runtime`
- `/v1/swarm/containers/local`
- `/v1/swarm/containers/local/update-job`
- `/v1/swarm/containers/local/create`
- `/v1/swarm/containers/local/action`
- `/v1/swarm/containers/local/delete`
- `/v1/swarm/containers/local/prune-missing`
- `/v1/swarm/replicate`
- `/v1/swarm/managed-workspaces/preflight`
- `/v1/swarm/managed-workspaces/replicate`
- `/v1/swarm/managed-workspaces/inventory`
- `/v1/swarm/peer/managed-workspaces/preflight`
- `/v1/swarm/peer/managed-workspaces/ensure-link`
- `/v1/swarm/peer/managed-workspaces/link-existing`
- `/v1/swarm/peer/managed-workspaces/import-bundle`
- `/v1/swarm/peer/managed-workspaces/inventory`
- `/v1/swarm/peer/workspaces/discover`
- `/v1/swarm/peer/workspaces/create`
- `/v1/swarm/peer/workspaces/import-bundle`
- `/v1/swarm/peer/workspaces/transfer/`
- `/v1/swarm/peer/flows/apply`
- `/v1/swarm/peer/flows/report`
- `/v1/swarm/peer/sessions/open`
- `/v1/swarm/peer/sessions/append_message`
- `/v1/swarm/peer/sessions/mode`
- `/v1/swarm/peer/sessions/title`
- `/v1/swarm/peer/sessions/metadata`
- `/v1/swarm/peer/sessions/lifecycle`
- `/v1/swarm/peer/sessions/event`
- `/v1/swarm/peer/managed-host-sessions/open`
- `/v1/swarm/peer/managed-host-sessions/message`
- `/v1/swarm/peer/managed-host-sessions/run`
- `/v1/swarm/peer/managed-host-sessions/run/stream`
- `/v1/swarm/peer/managed-host-sessions/stop`
- `/v1/swarm/peer/managed-host-sessions/event`
- `/v1/swarm/peer/git/sync/apply`
- `/v1/swarm/peer/update/run`
- `/v1/swarm/peer/update/status`
- `/v1/swarm/peer/permissions/create`
- `/v1/swarm/peer/permissions/wait`
- `/v1/swarm/peer/permissions/cancel_run`
- `/v1/swarm/peer/permissions/mark_started`
- `/v1/swarm/peer/permissions/mark_completed`
- `/v1/swarm/peer/mirror/snapshot`
- `/v1/swarm/peer/mirror/watch`

Current storage/ownership:
- Swarm node/local state: swarmID/swarmName/host-derived (`swarm/local_node/default`).
- Local pairing: daemon/global pairing state (`swarm/local_pairing/default`).
- Current swarm group: daemon/global (`swarm/current_group/default`).
- Groups/membership: swarm group/member records keyed by groupID and swarmID, not UserID/TeamID (`swarm/group/...`, `swarm/group_membership/...`, `swarm/group_membership_by_swarm/...`).
- Container profiles/local containers/nodes: daemon/global or swarm/container ID keyed (`swarm/container_profile/...`, `swarm/local_container/...`, `swarm/node/...`).
- Invites/enrollments/trusted peers: topology trust keyed by invite/enrollment/token/swarm peer, not product identity (`swarm/invite/...`, `swarm/invite_token/...`, `swarm/enrollment/...`, `swarm/trusted_peer/...`).
- Desktop target current: daemon/global (`swarm/desktop_target/current`).
- Topology runtime/host containers/attachments/workspace bindings/session routes: topology/session/host/path derived (`topology/runtime/...`, `topology/host_container/...`, `topology/attachment/...`, `topology/workspace_binding/...`, `topology/session_route/...`).
- Topology migration status: topology/global status (`topology/migration_status/...`).
- Targets/mirror: aggregated global swarm/container/workspace resources.
- Mirror local/remote resources and cursors: sequence/swarm/kind/ID scoped, not TeamID/UserID (`swarm/mirror/local/seq`, `swarm/mirror/local/event/...`, `swarm/mirror/local/resource/...`, `swarm/mirror/remote/cursor/...`, `swarm/mirror/remote/resource/{managedSwarmID}/{kind}/{id}`).
- Peer operations: peer/swarm auth, no product actor ownership.

Required actions:
- Host/swarmName/swarmID must never become product ownership identity.
- Swarm groups either map into explicit TeamID sharing scope or are separated as network topology only; they must not remain fake team identity.
- Targets/topology/mirror must filter and persist by validated UserID + TeamID.
- Mirror/replication must stop pooling resources only by peer swarmID; mirrored resources must include/validate TeamID and, when workspace-bound, WorkspaceID.
- Invites/enrollments/trusted peers must carry inviter/enroller UserID and target TeamID/admin authorization where they affect product access.
- Managed sessions/routes must carry UserID + TeamID + WorkspaceID when workspace-bound.
- Local container records must carry owning TeamID and creator/actor UserID where required.
- Peer auth must validate peer relationship plus propagated UserID/team/workspace scope; swarmID alone is insufficient.
- Discovery may have bootstrap-safe minimal output, but any real resources require identity.
- Hard-refuse until converted: target select/current for product resources, group management, local container create/action/delete, managed-host session open/run/message/stop, managed workspace replication/import/link, peer session/permission/mirror routes that lack propagated product actor context, and peer-driven delete/update actions without owner scope.

## Section F — Deploy/Container/Remote Deploy/Local Transport Sync

Routes:
- `/v1/deploy/container/runtime`
- `/v1/deploy/container`
- `/v1/deploy/container/create`
- `/v1/deploy/container/package/defaults`
- `/v1/deploy/container/package/validate`
- `/v1/deploy/container/package/suggest`
- `/v1/deploy/container/settings`
- `/v1/deploy/container/action`
- `/v1/deploy/container/delete`
- `/v1/deploy/container/attach/child-state`
- `/v1/deploy/container/attach/request`
- `/v1/deploy/container/attach/approve`
- `/v1/deploy/container/attach/finalize`
- `/v1/deploy/container/sync/credentials`
- `/v1/deploy/container/sync/agents`
- `/v1/deploy/container/sync/skills`
- `/v1/deploy/container/sync/permissions`
- `/v1/deploy/container/sync/model-defaults`
- `/v1/deploy/container/managed/credentials/apply`
- `/v1/deploy/container/managed/agents/apply`
- `/v1/deploy/container/managed/model-defaults/apply`
- `/v1/deploy/container/managed/skills/apply`
- `/v1/deploy/container/workspaces/bootstrap`
- `/v1/deploy/remote/session`
- `/v1/deploy/remote/session/create`
- `/v1/deploy/remote/session/settings`
- `/v1/deploy/remote/session/delete`
- `/v1/deploy/remote/session/start`
- `/v1/deploy/remote/session/update-job`
- `/v1/deploy/remote/session/sync/credentials`
- `/v1/deploy/remote/session/`
- local transport duplicate/sync routes registered in `registerLocalTransportRoutes`

Current storage/ownership:
- Deploy containers: global deployment IDs (`deploy/container/{deploymentID}`).
- Remote deploy sessions: global session IDs (`deploy/remote_session/{sessionID}`).
- Sync bundles: bootstrap secret/deployment-token authorized, not product identity.
- Local transport: socket/env trust, not UserID/TeamID.
- Managed apply: receives resources without product ownership validation.
- Peer auth/transport auth may authenticate the caller/pipe, but does not establish canonical product ownership.

Required actions:
- Deployments and remote sessions become TeamID-owned; creation/actions require UserID actor.
- Workspace-bound deploys must include WorkspaceID.
- Sync of credentials/agents/skills/permissions/model defaults must only transfer records authorized for that deployment team/user/workspace scope.
- Bootstrap secrets/session tokens may authenticate transport but cannot be ownership authority; they must resolve to a deployment/session record with TeamID/UserID/WorkspaceID as required.
- Existing auto-adoption/global deploy records must be removed or hard-refused for converted routes.
- Package default/validate/suggest can remain system/read-only if they do not mutate scoped data.
- Hard-refuse until converted: `/v1/deploy/container/create`, `/v1/deploy/container/settings`, `/v1/deploy/container/action`, `/v1/deploy/container/delete`, `/v1/deploy/remote/session/create`, remote session settings/delete/start/update, sync/apply routes that cannot prove matching deployment ownership, and peer-authorized deploy deletion without propagated product actor context.

## Cross-section hard-refuse rule

For every API: auth/session guards are not enough. The backing data object and key/index must include the correct canonical ownership fields, or the API must hard-refuse until its backing store is converted. Transport identity (`swarmID`, local socket, bootstrap secret, desktop local auth, attach token, sessionID, path, deploymentID) is never product ownership by itself.

## Source pointers for verification

- Route registration: `swarmd/internal/api/server_routes.go`.
- Managed host path constants: `swarmd/internal/api/managed_host_sessions.go`.
- Managed workspace path constants: `swarmd/internal/api/swarm_managed_workspace_replication.go`.
- Peer workspace path constants: `swarmd/internal/api/swarm_peer_workspaces.go`.
- Managed update path constants: `swarmd/internal/api/managed_dev_update.go`.
- Managed git sync path constants: `swarmd/internal/api/git_sync.go`.
- Current Pebble key families: `swarmd/internal/store/pebble/keys.go`.
