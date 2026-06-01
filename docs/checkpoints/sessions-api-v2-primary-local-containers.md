# Sessions API v2: primary-first contract and local-container extension

Status: design draft for the primary + local-container implementation slice.

This document defines the first v2 session-open contract. The primary endpoint is the base shape every other v2 session-open API follows. The local-container endpoint adds one explicit execution class without reintroducing legacy routed-session ambiguity.

## Goal

Make session creation explicit, classed, and fail-closed:

1. The client chooses a runtime target before opening a session.
2. The client calls the endpoint for that target class.
3. The backend proves the selected runtime from persisted topology placement and workspace binding records.
4. The backend builds one `SessionExecution` identity from those authoritative records.
5. Execution uses that identity; responses and mirrors are projections, not new routing truth.

There is no generic session-open router in v2. The endpoint path is part of the contract.

## Endpoint families

Implemented in this slice:

| Execution class | Endpoint | Meaning |
| --- | --- | --- |
| Primary host runtime | `POST /v2/sessions/primary` | Execute directly on the primary host runtime. |
| Local container runtime | `POST /v2/sessions/local-containers` | Execute inside a local container whose authority host is the primary. |

Planned later, not implemented in this slice:

| Execution class | Future endpoint | Meaning |
| --- | --- | --- |
| Managed host primary runtime | `POST /v2/sessions/managed-primary` | Dispatch to a managed host authority and execute on that host runtime. |
| Managed host local container runtime | `POST /v2/sessions/managed-local-containers` | Dispatch to a managed host authority, then execute in one of its local containers. |

The local-container endpoint belongs under Sessions, not under container lifecycle APIs. Container lifecycle remains owned by local-container/deploy APIs; session open remains owned by Sessions API v2.

## Route registration shape

The runtime routes should be registered beside the legacy session endpoints while staying separate from them:

```go
mux.HandleFunc("/v2/sessions/primary", s.handleSessionsV2Primary)
mux.HandleFunc("/v2/sessions/local-containers", s.handleSessionsV2LocalContainers)
```

The handlers must not fall through to `handleSessions`, `handleSessionByID`, managed-host session handlers, or generic routed proxy logic.

## Request contract

Both implemented endpoints use the same request body. The endpoint class says what kind of runtime is allowed; the body identifies which selected runtime and workspace binding are being opened.

```json
{
  "swarm_id": "selected-runtime-swarm-id",
  "workspace_binding_id": "workspace-binding-id",
  "title": "optional display title",
  "mode": "plan|auto",
  "agent_name": "optional agent profile name",
  "worktree_mode": "off|on|inherit",
  "worktree_use_current_branch": true,
  "worktree_base_branch": "optional-base-branch",
  "worktree_branch_name": "optional-requested-branch",
  "preference": {
    "provider": "optional-provider",
    "model": "optional-model",
    "thinking": "optional-thinking",
    "service_tier": "optional-service-tier",
    "context_mode": "optional-context-mode"
  },
  "metadata": {}
}
```

Required for workspace-backed creates:

- `swarm_id`
- `workspace_binding_id`

Allowed non-routing options:

- `title`
- `mode`
- `agent_name`
- worktree intent fields
- model/provider preference fields
- validated metadata

Forbidden request fields for workspace-backed v2 session create:

- `workspace_path`
- `host_workspace_path`
- `runtime_workspace_path`
- `workspace_name` as binding lookup authority
- `target_swarm_id` as an alternate to `swarm_id`
- `backend_url`
- `child_backend_url`
- `target_backend_url`
- `next_hop_swarm_id`
- `next_hop_backend_url`
- any durable route/backend URL field

If a forbidden field is present, the request fails before any session record or route record is written.

## Authoritative records

V2 routing authority comes only from these persisted records:

| Authority | Fields used |
| --- | --- |
| Local node identity | primary/local host `swarm_id` |
| `TopologyRuntimePlacementRecord` | `RuntimeSwarmID`, `AuthorityHostSwarmID`, `AuthorityContainerID`, `RuntimeKind`, `PlacementGeneration`, `State` |
| `TopologyWorkspaceBindingRecord` | `BindingID`, `SourceWorkspaceID`, `SourceWorkspaceGeneration`, `SourceWorkspacePath`, `SourceWorkspaceName`, `DestinationRuntimeSwarmID`, `DestinationAuthorityHostSwarmID`, `DestinationRuntimeKind`, `DestinationContainerID`, `DestinationWorkspacePath`, `PlacementGeneration`, `BindingGeneration`, `State`, `AccessMode`, `AttestedByHostSwarmID` |

Not routing authority:

- desktop selected-target setting
- workspace name
- client workspace paths
- startup/config backend URL
- durable runtime/backend URL fields
- local container names or operational records without matching topology placement
- child/container `SessionSnapshot.WorkspacePath`

## Primary endpoint: `POST /v2/sessions/primary`

### Validation

The primary endpoint accepts only the primary host runtime.

Required proof:

1. Authenticated principal is present.
2. `swarm_id` is non-empty.
3. `workspace_binding_id` is non-empty for workspace-backed create.
4. Local node identity exists.
5. `swarm_id` equals the primary/local node swarm id.
6. Runtime placement exists for `swarm_id`.
7. Placement is active.
8. Placement is self-placement:
   - `RuntimeSwarmID == swarm_id`
   - `AuthorityHostSwarmID == swarm_id`
   - `RuntimeKind == "host"`
   - `AuthorityContainerID == ""`
9. Workspace binding exists for `workspace_binding_id` in the principal account.
10. Binding matches selected runtime and placement:
    - `DestinationRuntimeSwarmID == swarm_id`
    - `DestinationAuthorityHostSwarmID == swarm_id`
    - `DestinationRuntimeKind == "host"`
    - `DestinationContainerID == ""`
    - `PlacementGeneration == placement.PlacementGeneration`
    - `State == "bound"`
11. Access mode permits the requested session operations.

### Execution construction

After validation, the handler builds a `SessionExecution` directly from placement + binding:

```json
{
  "execution_class": "primary",
  "runtime_swarm_id": "selected-runtime-swarm-id",
  "runtime_kind": "host",
  "authority_host_swarm_id": "selected-runtime-swarm-id",
  "authority_container_id": "",
  "workspace_binding_id": "workspace-binding-id",
  "source_workspace_id": "binding-source-workspace-id",
  "source_workspace_generation": 1,
  "source_workspace_name": "binding-source-display-name",
  "source_workspace_path": "binding-source-path",
  "runtime_workspace_path": "binding-destination-path",
  "placement_generation": 1,
  "binding_generation": 1
}
```

For primary self-bindings, source and runtime paths may be the same. That equality is a validated property of the binding, not a client assumption.

### Response

The response returns the created session and the frozen execution identity used to create it:

```json
{
  "ok": true,
  "session": {},
  "session_execution": {
    "execution_class": "primary",
    "runtime_swarm_id": "selected-runtime-swarm-id",
    "runtime_kind": "host",
    "authority_host_swarm_id": "selected-runtime-swarm-id",
    "workspace_binding_id": "workspace-binding-id",
    "placement_generation": 1,
    "binding_generation": 1
  }
}
```

The response may include display paths derived from the binding, but those paths are not accepted back as routing authority in later requests.

## Local-container endpoint: `POST /v2/sessions/local-containers`

The local-container endpoint uses the same request and response shape as primary, but the endpoint class changes validation and execution.

### Validation

Required proof:

1. Authenticated principal is present.
2. `swarm_id` is the selected container runtime swarm id.
3. `workspace_binding_id` is present.
4. Local node identity exists and is the primary authority host.
5. Runtime placement exists for `swarm_id`.
6. Placement is active.
7. Placement is a local container controlled by primary:
   - `RuntimeSwarmID == swarm_id`
   - `RuntimeKind == "container"`
   - `AuthorityHostSwarmID == primarySwarmID`
   - `AuthorityContainerID != ""`
8. Workspace binding exists and matches placement:
   - `DestinationRuntimeSwarmID == swarm_id`
   - `DestinationAuthorityHostSwarmID == primarySwarmID`
   - `DestinationRuntimeKind == "container"`
   - `DestinationContainerID == placement.AuthorityContainerID`
   - `DestinationWorkspacePath != ""`
   - `PlacementGeneration == placement.PlacementGeneration`
   - `State == "bound"`
   - `AttestedByHostSwarmID == primarySwarmID`
9. Access mode permits the requested session operations.

### Execution construction

The local-container handler builds the same `SessionExecution` shape with a different execution class:

```json
{
  "execution_class": "local_container",
  "runtime_swarm_id": "container-runtime-swarm-id",
  "runtime_kind": "container",
  "authority_host_swarm_id": "primary-host-swarm-id",
  "authority_container_id": "primary-owned-container-id",
  "workspace_binding_id": "workspace-binding-id",
  "source_workspace_id": "binding-source-workspace-id",
  "source_workspace_generation": 1,
  "source_workspace_name": "binding-source-display-name",
  "source_workspace_path": "primary/source/display/path/from-binding",
  "runtime_workspace_path": "container/runtime/path/from-binding",
  "placement_generation": 1,
  "binding_generation": 1
}
```

The runtime path used for execution is `binding.DestinationWorkspacePath`. The primary mirror/source identity is `binding.SourceWorkspaceID` plus `binding.SourceWorkspaceGeneration` and binding source display fields.

### Mirror rule

For local-container sessions, child/container session snapshots are runtime diagnostics only.

Primary-side mirror/session state must not adopt:

- child `SessionSnapshot.WorkspacePath`
- child worktree root paths as source workspace paths
- container-local `/workspaces/...` paths as primary workspace identity

Primary-side mirror identity comes from the frozen `SessionExecution` and workspace binding. Runtime-local paths remain runtime execution facts.

## Path terminology

V2 uses distinct names for distinct facts:

| Field | Meaning | Owner | Accepted from client? |
| --- | --- | --- | --- |
| `source_workspace_id` | Stable primary workspace identity. | Workspace catalog / binding | No |
| `source_workspace_generation` | Source workspace version/generation. | Workspace catalog / binding | No |
| `source_workspace_path` | Primary/source display path from binding. | Binding | No |
| `destination_workspace_path` / `runtime_workspace_path` | Path visible to the selected runtime. | Binding attested by authority host | No |
| `runtime_cwd` | Exact cwd used for execution after worktree realization. | Session execution/worktree realization | No |
| `SessionSnapshot.WorkspacePath` | Session display/projection field. | Session service projection | No |

This eliminates path ambiguity:

- Primary host sessions derive both source and runtime paths from a self-binding.
- Local-container sessions derive primary/source identity from binding source fields and runtime execution path from binding destination fields.
- A container-local path can never overwrite primary/source path identity.
- A client cannot select a workspace by path or name.

## Settings isolation

Session API v2 does not use settings as placement authority.

Specifically, v2 session create must not route from:

- desktop selected target setting
- workspace overview current route setting
- local container profile settings
- deploy/container settings
- startup-config backend URLs
- remembered child backend URLs
- model/provider settings
- workspace name defaults

Settings may only affect non-routing behavior after placement is proven:

- model/provider preference chooses execution model, not runtime target
- worktree options choose worktree realization, not workspace binding
- metadata can annotate the session after validation, not alter routing
- UI selected target decides which endpoint the client calls, but the backend still validates `swarm_id` + placement + binding and fails on mismatch

This means changing settings cannot silently send a session to another runtime or reinterpret workspace paths.

## Fail-closed behavior

V2 handlers fail before writing session state when:

- principal is missing or invalid
- endpoint class and placement kind disagree
- selected `swarm_id` is missing or unknown
- workspace binding is missing
- binding does not match selected runtime
- binding placement generation is stale
- placement is missing, stale, inactive, or malformed
- authority host is not the expected host for the endpoint
- authority connection is unavailable for future remote endpoints
- forbidden routing/path/backend fields are present
- access mode disallows the requested operation

Suggested error classes:

| Condition | Status | Code |
| --- | --- | --- |
| Missing principal | `401` | `principal_required` |
| Malformed request / forbidden field | `400` | `session_v2_bad_request` |
| Wrong endpoint class | `400` | `session_v2_invalid_execution_class` |
| Missing placement or binding | `404` | `session_v2_authority_not_found` |
| Stale placement or binding mismatch | `409` | `session_v2_stale_authority` |
| Access mode denial | `403` | `session_v2_access_denied` |
| Future remote authority unavailable | `503` | `session_v2_authority_unavailable` |

## Client routing rule

The frontend/router uses selected target metadata only to choose the endpoint:

| Selected target | Client endpoint |
| --- | --- |
| primary host runtime | `/v2/sessions/primary` |
| local container runtime whose authority host is primary | `/v2/sessions/local-containers` |
| managed host primary runtime | not implemented in this slice |
| managed-host-owned local container runtime | not implemented in this slice |

The client sends only:

- selected runtime `swarm_id`
- selected route/workspace `workspace_binding_id`
- non-routing session options

The client never sends workspace path fields, backend URL fields, next-hop fields, or workspace-name lookup hints for workspace-backed v2 creates.

## Internal organization

Shared helpers should implement the common contract once:

1. Decode v2 request.
2. Reject forbidden fields.
3. Normalize `swarm_id` and `workspace_binding_id`.
4. Load principal-local node identity.
5. Load runtime placement.
6. Load workspace binding.
7. Validate binding against placement and endpoint class.
8. Build frozen `SessionExecution`.

Endpoint-specific handlers should only add class-specific checks and execution dispatch:

| Handler | Extra checks | Execution path |
| --- | --- | --- |
| `handleSessionsV2Primary` | host self-placement | direct primary/local session creation |
| `handleSessionsV2LocalContainers` | container placement with primary authority host | primary-as-authority local container session open |

Future managed endpoints reuse the same request and `SessionExecution` shape, adding only remote authority resolution through live `AuthorityConnection`.

## What this removes

This API shape removes the legacy ambiguity that caused routing and mirror mismatches:

- no recursive next-hop routing
- no generic `/v1/sessions?swarm_id=...` inference for v2 workspace-backed creates
- no path-based workspace binding lookup
- no workspace-name binding fallback
- no durable backend URL as routing authority
- no child snapshot path overwriting primary mirror identity
- no managed-host handler participation in primary/local-container v2 opens
- no hidden dependency on global desktop target selection

The result is one clear decision chain:

```text
selected runtime
  -> endpoint class
  -> RuntimePlacement
  -> authority host proof
  -> WorkspaceBinding
  -> SessionExecution
  -> execution
```
