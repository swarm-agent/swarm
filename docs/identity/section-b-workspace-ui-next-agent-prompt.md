# Section B — Workspace/UI/Git/Worktree/Media Identity Cutover Prompt

You are the coding agent assigned to implement Section B of the user-first identity cutover. Work from the repository root. Do not treat route auth/session middleware as sufficient: each converted API must have backing records, keys, indexes, and service calls scoped by canonical identity. If a route cannot prove its backing storage is converted, it must hard-refuse.

## Product identity model to preserve

- `UserID` is the primary actor for all personal state and mutations.
- `TeamID` is the sharing container. The active team selection must be explicit and must isolate user state by team context where team resources are visible.
- `WorkspaceID` must become the stable workspace authority. Filesystem path is only metadata/location, never ownership.
- `username` is product identity. `swarmName`, `swarmID`, sockets, tokens, paths, session IDs, deployment IDs, and git paths are not ownership.
- No fallback IDs, guessed ownership, silent legacy adoption, dual authoritative paths, or path-derived identity.

## Section scope and routes

Route registration is in `swarmd/internal/api/server_routes.go` lines 166-196.

Section B routes:

- `GET|POST /v1/ui/settings`
- `GET /v1/workspace/resolve`
- `POST /v1/workspace/select`
- `GET /v1/workspace/current`
- `GET /v1/workspace/list`
- `GET /v1/workspace/overview`
- `GET /v1/workspace/discover`
- `GET /v1/workspace/browse`
- `POST /v1/workspace/folders/create`
- `POST /v1/workspace/add`
- `POST /v1/workspace/directories/add`
- `POST /v1/workspace/directories/remove`
- `POST /v1/workspace/managed-links/upsert`
- `POST /v1/workspace/managed-links/remove`
- `POST /v1/workspace/theme`
- `POST /v1/workspace/rename`
- `POST /v1/workspace/move`
- `GET|POST /v1/workspace/todos`
- `POST /v1/workspace/delete`
- `POST /v1/workspace/video/scan`
- `POST /v1/workspace/video/storage/reveal`
- `GET|POST /v1/workspace/video/threads`
- `GET|POST /v1/workspace/video/threads/{id}` plus clip-media subpaths
- `GET|POST /v1/workspace/image/threads`
- `GET|POST /v1/workspace/image/threads/{id}`
- `GET /v1/workspace/git/status`
- `POST /v1/workspace/git/commit`
- `GET /v1/workspace/git/realtime`
- `GET|POST|DELETE /v1/worktrees`
- `GET /v1/manage-worktree`

Note: `/v1/workspace/image/storage/reveal` is registered outside this section in the image/provider route group. It shares the same thread/path ownership risk but belongs to Section C unless you intentionally split or coordinate that work.

## Current architecture evidence

- Workspace routes call handlers directly from `server.go`; examples: select uses request `path` and calls `s.workspace.Select(req.Path)` (`server.go` lines 1388-1408), list calls `s.workspace.ListKnown` (`server.go` lines 1428-1455), add calls `s.workspace.Add(req.Path, ...)` (`server.go` lines 1541-1568), and mutations use path fields (`server.go` lines 1571-1708).
- Current workspace selection is a single global record. `WorkspaceStore.SetCurrent` writes `KeyWorkspaceCurrent` (`workspace/current`) and `GetCurrent` reads the same key (`workspace_store.go` lines 56-69 and 460-469).
- Workspace entries are keyed by path. `KeyWorkspaceEntry(path)` is `workspace/entry/{path}` (`keys.go` lines 27-29 and 198-200), and the record has path/name/directories but no `UserID`, `TeamID`, or `WorkspaceID` (`workspace_store.go` lines 36-46).
- Workspace list/active status compares global current path to entry path (`workspace/service.go` lines 368-402).
- Workspace scope resolution derives authority from path containment across stored directories and returns path-derived scope (`workspace/service.go` lines 416-480).
- UI settings are global. `UISettingsStore.Get/Update` read/write `ui/settings/default`; update deletes legacy `ui/chat_settings/default` (`ui_chat_settings_store.go` lines 127-178; keys at `keys.go` lines 17-18).
- Worktree config is path-keyed. `worktree/config/{workspacePath}` is read/written by `WorktreeStore.GetConfig/SetConfig` (`worktree_store.go` lines 31-68; key at `keys.go` lines 24-25 and 218-220). Legacy global migration still exists (`worktree_store.go` lines 71-132).
- Todos are path-keyed. Records include `WorkspacePath` and `OwnerKind` but no product owner fields (`todo_store.go` lines 24-40), and keys are `workspace_todo/item/{workspacePath}/{todoID}` (`keys.go` lines 226-235).
- Image/video workspace threads are ID-keyed and only filter by `WorkspacePath`: image `image/thread/{threadID}` (`image_thread_store.go` lines 42-66, 100-136), video `video/thread/{threadID}` (`video_thread_store.go` lines 42-66, 100-136). Their snapshots contain `WorkspacePath` and `WorkspaceName`, not `UserID`, `TeamID`, or `WorkspaceID`.
- Image/video handlers trust request `workspace_path` or `cwd` directly (`image_threads.go` lines 30-83; `video_threads.go` lines 33-86), and thread get/update by ID does not verify product actor ownership (`image_threads.go` lines 89-150; `video_threads.go` lines 92-157).
- Video scan/reveal use filesystem paths and thread metadata without product ownership checks (`video_scan.go` lines 99-122; `media_reveal.go` lines 49-97).
- Git commit resolves from `workspace_path`, `cwd`, or global current workspace, then runs git in that directory (`git_commit.go` lines 76-113 and 115-165). This is filesystem path authority, not product ownership.
- Worktree route accepts query/body `workspace_path` and calls worktree service by resolved path (`server.go` lines 683-780). The service emits events under `system:worktrees` with path resource IDs (`worktree/service.go` lines 119-147).
- Workspace overview aggregates workspace, todo, session, topology, and git state by path (`desktop_bootstrap.go` lines 143-214). This is a high-risk cross-section endpoint because it can leak records if only one component is converted.
- Managed workspace link routes in this section operate by `workspace_path` and topology binding ID (`swarm_managed_workspace_replication.go` lines 335-386). Coordinate with topology/peer Section E before enabling converted behavior.

## Final missed-risk check before implementation

Before writing code, verify these risks are covered in the active plan or create a checkpoint update before proceeding:

1. Workspace list/current/select must be isolated by `UserID + active TeamID`; switching team must not leak current workspace or previous team list.
2. Workspace path may remain in records only as location metadata. Every mutation must require `WorkspaceID` and membership/role validation.
3. `workspace_path`, `cwd`, `path`, `directory_path`, git repo path, and media folder path cannot authorize access by themselves.
4. Workspace overview must not stitch converted and unconverted records together. If one backing family is unconverted, omit with an explicit hard-refuse/blocked status or keep the whole endpoint blocked until the family is safe.
5. Media thread get/update/reveal by opaque thread ID must validate thread owner scope; otherwise one user can access another user/team thread by ID.
6. Todo `owner_kind=user` is not `UserID`; it is only a display/category flag. Do not confuse it with product user ownership.
7. Worktree generated `workspace_id` values are session/worktree identifiers, not the future product `WorkspaceID` unless deliberately replaced and migrated.
8. UI `SwarmSettings.Name` edits rename daemon/device identity through swarm service; keep this distinct from username/product identity.
9. Managed workspace link upsert/remove touches topology state. Do not flatten topology trust into product TeamID; require explicit product scope on the backing topology binding before enabling.
10. Remove or block legacy global worktree config migration once converted. Do not silently adopt `worktree/global/config` into a user/team record.

## Required storage/Pebble key changes

Implement canonical key families deliberately. Exact names may differ, but the shape must encode product ownership and the record must contain matching fields.

### UI settings

Current:

- `ui/settings/default`
- legacy `ui/chat_settings/default`

Required:

- User-scoped settings, for example `identity/user/{UserID}/ui/settings/default` or `ui/settings/user/{UserID}`.
- Record fields must include `UserID` and updated timestamp.
- Chat/default route settings must not be global. If workspace route defaults reference workspaces, store `WorkspaceID`, not path.
- Swarm/device name fields may still call daemon rename, but that must be explicitly classified as daemon state, not user identity.

### Workspace records and selection

Current:

- `workspace/current`
- `workspace/entry/{path}`

Required:

- Stable workspace record keyed by `TeamID + WorkspaceID`, with `CreatedByUserID`, `OwnerUserID` for personal ownership if needed, display name, path/location metadata, directories, theme, timestamps, and sharing metadata.
- Team/user indexes for listing, for example `workspace/by_team/{TeamID}/{WorkspaceID}` and `workspace/by_user/{UserID}/{TeamID}/{WorkspaceID}` if personal/team listing needs both.
- Current selection keyed by `UserID + active TeamID`, for example `workspace/current/{UserID}/{TeamID}` storing `WorkspaceID` only plus metadata. Do not store a path as the authoritative selection.
- Path lookup/index may exist only as a non-authoritative locator, for example `workspace/path_index/{TeamID}/{pathHash} -> WorkspaceID`, and must validate membership before use.

### Workspace directories and browse/discover

Current:

- Directories are embedded paths under path-keyed workspace entries.
- Browse/discover reads filesystem paths without product ownership.

Required:

- Directories embedded under `WorkspaceID` record, with canonical path/location metadata only.
- Browse/discover is either a bootstrap-safe local picker with no persisted resource leakage, or a `WorkspaceID`-validated operation. Mutating add/remove directory must require workspace membership/admin role.
- Any path outside a known `WorkspaceID` must hard-refuse for mutations. Do not auto-create ownership from path.

### Todos

Current:

- `workspace_todo/item/{workspacePath}/{todoID}`
- Record has `WorkspacePath`, `OwnerKind`, optional `SessionID`, no product owner.

Required:

- `workspace_todo/item/{TeamID}/{WorkspaceID}/{todoID}` or equivalent.
- Record fields must include `TeamID`, `WorkspaceID`, `CreatedByUserID`, and if personal/user-owned visibility is intended, `OwnerUserID`.
- `OwnerKind` remains display/category only (`user` vs `agent`) and cannot substitute for `UserID`.
- Agent-owned todos linked to sessions must validate that the session belongs to the same `UserID/TeamID/WorkspaceID` before listing or mutation.

### Worktrees

Current:

- `worktree/config/{workspacePath}`
- legacy `worktree/global/config`
- managed worktree inventory by filesystem path/session-derived workspace id.

Required:

- `worktree/config/{TeamID}/{WorkspaceID}` or equivalent.
- Record fields: `TeamID`, `WorkspaceID`, `UpdatedByUserID`, config flags/branch fields, timestamps.
- Managed worktree records, if persisted, must include owning `TeamID`, product `WorkspaceID`, creating `UserID`, session ID, path metadata, and branch metadata.
- Delete legacy global config migration or convert it into an explicit one-time blocked migration requiring identity. No silent adoption.

### Image/video workspace threads and storage

Current:

- `image/thread/{threadID}` with `WorkspacePath` only.
- `video/thread/{threadID}` with `WorkspacePath` only.
- Media assets/clips contain local paths.

Required:

- Thread keys scoped by product ownership, for example `image/thread/{TeamID}/{WorkspaceID}/{threadID}` and `video/thread/{TeamID}/{WorkspaceID}/{threadID}`.
- Secondary indexes for list by `TeamID/WorkspaceID` if needed.
- Record fields: `UserID` or `CreatedByUserID`, `TeamID`, `WorkspaceID`, title, metadata, storage paths as non-authoritative metadata.
- Asset/clip paths must be validated as inside the authorized workspace/tool-storage root before reveal or stream. Thread ID alone is never enough.

### Git status/commit/realtime

Current:

- Mostly filesystem operations with no Pebble ownership record.
- `git commit` accepts `workspace_path`/`cwd`/global current selection and runs `git` there.
- Realtime manager keys watchers by normalized path.

Required:

- API request must specify `WorkspaceID` or derive it only from authenticated `UserID + active TeamID + current selection`.
- Resolve path from the owned workspace record; do not accept arbitrary path authority.
- Mutations such as commit require workspace role/permission validation. Status/realtime must validate membership before exposing repo state.
- Realtime watcher internals may watch paths, but subscriptions must be authorized by `TeamID/WorkspaceID`, and emitted events must include product scope, not only path.

## Hard-refuse rules until conversion

Hard-refuse these routes until their backing storage and service methods are fully converted:

- `/v1/workspace/select`, `/v1/workspace/current`, `/v1/workspace/list`, `/v1/workspace/overview` if they still use global selection or path-keyed entries.
- `/v1/workspace/add`, `/v1/workspace/directories/add`, `/v1/workspace/directories/remove`, `/v1/workspace/theme`, `/v1/workspace/rename`, `/v1/workspace/move`, `/v1/workspace/delete` if they accept path as ownership.
- `/v1/workspace/todos` until todo keys/records include product ownership.
- `/v1/workspace/git/commit` always until WorkspaceID/member validation exists; status/realtime must also refuse arbitrary path/cwd requests.
- `/v1/worktrees` POST/DELETE and `/v1/manage-worktree` until worktree config and managed inventory are WorkspaceID-scoped.
- `/v1/workspace/video/threads*`, `/v1/workspace/image/threads*`, `/v1/workspace/video/scan`, `/v1/workspace/video/storage/reveal` until thread/storage records include and validate `UserID + TeamID + WorkspaceID`.
- `/v1/workspace/managed-links/*` until topology binding ownership is scoped and validated with Section E.

Read-only helpers may remain only if they are explicitly classified as bootstrap-safe filesystem pickers and return no persisted product resources from another user/team. If unsure, hard-refuse.

## Phased commit/checkpoint order

Do not implement this section as one giant commit. Split and gate each phase.

### Phase B1 — Actor/team selection plumbing for workspace APIs

- Require authenticated actor context for Section B routes except explicitly documented bootstrap-safe pickers.
- Add helper(s) to resolve active `UserID` and active `TeamID`.
- Add hard-refuse tests proving converted routes fail when identity/team context is missing.
- Do not claim conversion of storage yet.

Checkpoint/update active plan before continuing if the repo does not already have canonical identity bootstrap/current selection records from Section A.

### Phase B2 — WorkspaceID records and current selection

- Introduce product `WorkspaceID` records keyed by `TeamID/WorkspaceID`.
- Convert add/select/current/list/theme/rename/move/delete to use `WorkspaceID` and membership checks.
- Keep path as metadata only. Do not silently import existing `workspace/entry/{path}` as owned data.
- Legacy `workspace/current` and `workspace/entry/{path}` must be ignored/hard-refused or blocked behind an explicit migration decision.

Checkpoint and run VM gate before touching dependent families.

### Phase B3 — Directory, browse/discover, overview isolation

- Convert directory add/remove to mutate directories under owned `WorkspaceID` records.
- Make browse/discover either bootstrap-safe local pickers or owned workspace operations.
- Rebuild overview so every component validates the same `UserID + TeamID + WorkspaceID` scope. If sessions/topology are not converted yet, return explicit blocked components or keep overview hard-refused.

Checkpoint before coupling to sessions/topology from other sections.

### Phase B4 — Todos

- Convert todo keys and records to `TeamID + WorkspaceID` scope, with `CreatedByUserID`/`OwnerUserID` where needed.
- Update API and tool/runtime callers that pass `workspace_path` so they resolve through `WorkspaceID`.
- Add negative tests for cross-user and cross-team todo isolation.

### Phase B5 — Worktrees and git

- Convert worktree config to `TeamID + WorkspaceID` scope.
- Change `/v1/worktrees` and `/v1/manage-worktree` to require WorkspaceID/membership.
- Change git status/commit/realtime to resolve the filesystem path only from the owned workspace record.
- Add hard-refuse for arbitrary `workspace_path`/`cwd` requests.

### Phase B6 — Media threads/storage

- Convert image/video thread keys and records to product scope.
- Validate list/get/update/reveal/clip media by `UserID + TeamID + WorkspaceID`.
- Ensure local asset/clip paths are inside authorized workspace/tool-storage roots.
- Coordinate `/v1/workspace/image/storage/reveal` with Section C if storage reveal is updated here.

### Phase B7 — Managed links coordination

- Do not enable `/v1/workspace/managed-links/*` until topology workspace bindings carry product ownership from Section E.
- Once Section E is ready, validate binding `TeamID + WorkspaceID` before upsert/remove.

## VM testing requirements

Every phase that changes behavior needs fresh-VM proof. Do not proceed past a failing VM gate.

### Fresh VM setup

Use a clean VM or reset app data so no previous Pebble records exist. Start the daemon from the built artifact. Capture:

- Build commit SHA.
- Clean data directory proof (generic listing or app log; no personal paths in committed docs).
- Daemon startup logs.
- API request/response transcripts with secrets redacted.
- Pebble key dump or store inspection showing only scoped keys for converted families.

### Identity/bootstrap prerequisite

Before Section B tests, bootstrap/log in two users and at least two team contexts if Section A supports this. If Section A is not available, Section B routes must hard-refuse and the VM proof should show refusal.

### Required VM pass/fail scenarios

1. **Missing identity hard-refuse**
   - Call converted workspace/UI/todo/git/worktree/media mutation routes without product identity.
   - Pass: all return explicit authorization/identity error and write no legacy/global/path-owned records.

2. **Current workspace selection isolation**
   - User A in Team 1 selects Workspace 1.
   - User A switches to Team 2.
   - User B logs in.
   - Pass: each `current` response is isolated by `UserID + TeamID`; no shared `workspace/current` behavior.

3. **Workspace list isolation**
   - Create workspace records in two teams and for two users.
   - Pass: list/overview only returns workspaces allowed by authenticated user and active team.

4. **Path-only hard-refuse**
   - Try add/select/theme/rename/move/delete/todos/git/worktrees/media calls using only `path`, `workspace_path`, or `cwd` without `WorkspaceID` or current selection.
   - Pass: mutations hard-refuse. Read-only picker exceptions must not return persisted resources.

5. **Persistence/restart**
   - Create UI setting, workspace, selection, todo, worktree config, and media thread under User A/Team 1.
   - Restart daemon.
   - Pass: same user/team sees records; other user/team does not; key dump shows scoped keys and no writes to legacy keys.

6. **Cross-team negative tests**
   - Use WorkspaceID from Team 1 while active team is Team 2.
   - Pass: 403/404-style refusal, no mutation.

7. **Media/thread ID negative tests**
   - Create a video/image thread in one user/team/workspace.
   - Attempt get/update/reveal from another user/team using thread ID.
   - Pass: refusal; no path leak in error body.

8. **Git/worktree negative tests**
   - Attempt git commit/status/realtime or worktree config using arbitrary filesystem path not attached to authorized WorkspaceID.
   - Pass: refusal before filesystem/git operation.

9. **Legacy key absence**
   - After converted operations and restart, inspect store keys.
   - Pass: converted operations do not write `workspace/current`, `workspace/entry/{path}`, `ui/settings/default`, `worktree/config/{path}`, `workspace_todo/item/{path}/{id}`, `image/thread/{id}`, or `video/thread/{id}`.

### Captured artifacts

For each phase, save or report:

- Command transcript for build/test/start.
- API request/response transcript for positive and negative cases.
- Store key evidence before and after restart.
- Daemon logs around refusal and successful mutations.
- A short pass/fail table listing each route family.

Do not capture secrets, real API keys, local usernames, machine hostnames, or machine-specific absolute paths in committed artifacts.

## When to checkpoint or update the active plan

Checkpoint/update the active plan before proceeding when:

- You discover Section A identity bootstrap/current-selection records are missing or incompatible.
- You must choose canonical key names for workspace/user/team indexes.
- You are about to leave a route hard-refused because another section is not converted.
- You find a route in Section B whose backing storage belongs to Section C/D/E/F.
- You finish each phase and have VM proof artifacts.
- You find any legacy auto-adoption/migration path that would silently assign global/path data to a user/team.

## Relevant filepaths

- `swarmd/internal/api/server_routes.go` — Section B route registration.
- `swarmd/internal/api/server.go` — workspace handlers, UI settings handler, worktree/todo route handlers.
- `swarmd/internal/api/desktop_bootstrap.go` — workspace overview aggregation and current/list/session/todo/git joins.
- `swarmd/internal/workspace/service.go` — path-derived workspace service and current/list/scope logic.
- `swarmd/internal/store/pebble/workspace_store.go` — global current workspace and path-keyed workspace entries.
- `swarmd/internal/store/pebble/keys.go` — current legacy key families.
- `swarmd/internal/store/pebble/ui_chat_settings_store.go` — global UI settings storage.
- `swarmd/internal/worktree/service.go` and `swarmd/internal/store/pebble/worktree_store.go` — path-keyed worktree config and legacy global migration.
- `swarmd/internal/store/pebble/todo_store.go` and `swarmd/internal/todo/service.go` — path-keyed todo records and owner_kind handling.
- `swarmd/internal/api/git_commit.go`, `swarmd/internal/api/git_status.go`, `swarmd/internal/api/git_realtime.go` — filesystem path git authority.
- `swarmd/internal/api/image_threads.go`, `swarmd/internal/api/video_threads.go`, `swarmd/internal/api/video_scan.go`, `swarmd/internal/api/media_reveal.go` — workspace media thread/storage path handling.
- `swarmd/internal/store/pebble/image_thread_store.go` and `swarmd/internal/store/pebble/video_thread_store.go` — ID-keyed media records with workspace path filtering.
- `swarmd/internal/api/swarm_managed_workspace_replication.go` — Section B managed-link routes that must coordinate with topology/peer ownership.
