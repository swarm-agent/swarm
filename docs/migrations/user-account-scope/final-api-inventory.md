# Final API inventory

Plain English version.

This file has only 4 buckets:
- DONE
- DO NOW
- MANAGED HOSTING LAST
- FLOWS LAST

I removed the extra "defer" bucket because you did not ask for that.

Source of truth for the API list:
- `swarmd/internal/api/server_routes.go`

Checked against code in the actual handlers/stores.

---

## DONE

These are the API groups that are already far enough along in code to count as done for this migration track.

### Public / bootstrap / account foundation
- `/healthz`
- `/readyz`
- `/v1/auth/codex`
- `/v1/auth/codex/oauth/start`
- `/v1/auth/codex/oauth/status`
- `/v1/auth/codex/oauth/complete`
- `/v1/auth/credentials`
- `/v1/auth/credentials/verify`
- `/v1/auth/credentials/active`
- `/v1/auth/credentials/delete`
- `/v1/auth/desktop/session`
- `/me`
- `/v1/me`
- `/v1/onboarding`
- `/v1/account/team/upgrade`

### Workspace / worktree / path / git foundation
- `/v1/workspace/resolve`
- `/v1/workspace/select`
- `/v1/workspace/current`
- `/v1/workspace/list`
- `/v1/workspace/overview`
- `/v1/workspace/discover`
- `/v1/workspace/browse`
- `/v1/workspace/video/scan`
- `/v1/workspace/video/storage/reveal`
- `/v1/workspace/video/threads`
- `/v1/workspace/video/threads/`
- `/v1/workspace/image/threads`
- `/v1/workspace/image/threads/`
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
- `/v1/workspace/git/status`
- `/v1/workspace/git/commit`
- `/v1/workspace/git/realtime`
- `/v1/workspace/delete`
- `/v1/worktrees`
- `/v1/manage-worktree`
- `/v1/git/sync/inspect`
- `/v1/git/sync/apply`
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
- `/v1/swarm/peer/git/sync/apply`

### Sessions
- `/v1/sessions`
- `/v1/sessions/`

### Local containers / local deploy foundation
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
- `/v1/deploy/container/runtime`
- `/v1/deploy/container`
- `/v1/deploy/container/create`
- `/v1/deploy/container/settings`
- `/v1/deploy/container/action`
- `/v1/deploy/container/delete`
- `/v1/update/local-containers`

---

## DO NOW

Everything here is unfinished and should be worked now unless it belongs to managed hosting or flows.

### Vault
- `/v1/vault`
- `/v1/vault/enable`
- `/v1/vault/unlock`
- `/v1/vault/lock`
- `/v1/vault/disable`
- `/v1/vault/export`
- `/v1/vault/import`

### Models / providers / voice / STT
- `/v1/model`
- `/v1/model/catalog`
- `/v1/models/favorites`
- `/v1/models/favorites/delete`
- `/v1/providers`
- `/v1/image/providers`
- `/v1/stt/transcribe`
- `/v1/voice/status`
- `/v1/voice/profiles`
- `/v1/voice/profiles/upsert`
- `/v1/voice/profiles/delete`
- `/v1/voice/config`
- `/v1/voice/devices`
- `/v1/voice/test-stt`

### UI / image / integrations / custom tools
- `/v1/ui/settings`
- `/v2/custom-tools`
- `/v2/custom-tools/`
- `/v1/image/generations`
- `/v1/image/assets`
- `/v1/image/storage/reveal`
- `/v1/integrations`
- `/v1/integrations/workspaces`
- `/v1/integrations/workspaces/`
- `/v1/integrations/builder/sessions`

### Agents / permissions / notifications / context
- `/v2/agents`
- `/v2/agents/defaults/restore`
- `/v2/agents/defaults/reset`
- `/v2/agents/`
- `/v1/context/sources`
- `/v1/permissions`
- `/v1/permissions/`
- `/v1/alerts`
- `/v1/alerts/`
- `/v1/alerts/summary`
- `/v1/notifications`
- `/v1/notifications/`
- `/v1/notifications/summary`

### Swarm / topology / mirror / pairing / target ownership cleanup
- `/ws`
- `/v1/auth/attach/rotate`
- `/v1/swarm/discovery`
- `/v1/swarm/remote-candidates`
- `/v1/swarm/invites`
- `/v1/swarm/remote-pairing/start`
- `/v1/swarm/remote-pairing/offer`
- `/v1/swarm/remote-pairing/request`
- `/v1/swarm/remote-pairing/pending`
- `/v1/swarm/remote-pairing/finalize`
- `/v1/swarm/remote-pairing/approve`
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
- `/v1/swarm/peer/mirror/snapshot`
- `/v1/swarm/peer/mirror/watch`

### System / update / peer session / peer permissions cleanup
- `/v1/system/shutdown`
- `/v1/update/status`
- `/v1/update/apply`
- `/v1/update/run`
- `/v1/swarm/peer/sessions/open`
- `/v1/swarm/peer/sessions/append_message`
- `/v1/swarm/peer/sessions/mode`
- `/v1/swarm/peer/sessions/title`
- `/v1/swarm/peer/sessions/metadata`
- `/v1/swarm/peer/sessions/lifecycle`
- `/v1/swarm/peer/sessions/event`
- `/v1/swarm/peer/permissions/create`
- `/v1/swarm/peer/permissions/wait`
- `/v1/swarm/peer/permissions/cancel_run`
- `/v1/swarm/peer/permissions/mark_started`
- `/v1/swarm/peer/permissions/mark_completed`
- local transport duplicates:
  - `/v1/swarm/peer/sessions/open`
  - `/v1/swarm/peer/sessions/append_message`
  - `/v1/swarm/peer/sessions/mode`
  - `/v1/swarm/peer/sessions/title`
  - `/v1/swarm/peer/sessions/metadata`
  - `/v1/swarm/peer/sessions/lifecycle`
  - `/v1/swarm/peer/sessions/event`
  - `/v1/swarm/peer/permissions/create`
  - `/v1/swarm/peer/permissions/wait`
  - `/v1/swarm/peer/permissions/cancel_run`
  - `/v1/swarm/peer/permissions/mark_started`
  - `/v1/swarm/peer/permissions/mark_completed`

---

## MANAGED HOSTING LAST

These stay grouped last exactly as you asked.

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
- `/v1/swarm/peer/managed-host-sessions/open`
- `/v1/swarm/peer/managed-host-sessions/message`
- `/v1/swarm/peer/managed-host-sessions/run`
- `/v1/swarm/peer/managed-host-sessions/run/stream`
- `/v1/swarm/peer/managed-host-sessions/stop`
- `/v1/swarm/peer/managed-host-sessions/event`
- `/v1/swarm/peer/update/run`
- `/v1/swarm/peer/update/status`
- `/v1/deploy/container/package/suggest`
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
- local transport managed/deploy duplicates:
  - `/v1/deploy/container/package/defaults`
  - `/v1/deploy/container/package/validate`
  - `/v1/deploy/container/package/suggest`
  - `/v1/deploy/container/attach/request`
  - `/v1/deploy/container/attach/approve`
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

---

## FLOWS LAST

These stay grouped last exactly as you asked.

- `/v3/flows`
- `/v3/flows/`
- `/v1/swarm/peer/flows/apply`
- `/v1/swarm/peer/flows/report`
- local transport duplicates:
  - `/v1/swarm/peer/flows/apply`
  - `/v1/swarm/peer/flows/report`

---

## Bottom line in normal English

What I am saying is:
- some API groups are already done
- everything unfinished should be worked now
- except managed hosting, which stays last
- and flows, which stay last after that

File updated:
- `docs/migrations/user-account-scope/final-api-inventory.md`
