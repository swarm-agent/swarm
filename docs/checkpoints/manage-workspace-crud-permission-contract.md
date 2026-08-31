# `manage_workspace` CRUD permission contract

## Authority

- `manage_workspace` remains restricted to the compiled Swarm primary agent and the authenticated session principal.
- Catalog create, update, and delete use the account-scoped workspace service/store. No second workspace authority is introduced.
- Delete unlinks the saved catalog entry only. It never deletes the workspace directory or repository files.
- Session workspace changes use the canonical V3 session mutation path so projections and realtime clients receive the same durable update.

## Permission isolation

Workspace catalog mutations are independent permission capabilities:

| Tool action | Permission requirement / persistent rule identity |
| --- | --- |
| `create` | `workspace_create` |
| `update` / `edit` | `workspace_update` |
| `delete` | `workspace_delete` |

Always-allow or always-deny for one action does not authorize the others. Mutation execution accepts only backend-generated approved arguments carrying the matching `permission_scope`, bounded user-readable intent, exact target metadata, requested changes, and safety metadata.

## Active-session safety

A catalog mutation must not edit the workspace identity currently hosting the running session in place.

1. Resolve a different active account-owned workspace as the safe context.
2. Durably switch the same session through the canonical V3 workspace mutation.
3. Apply the identity- and generation-guarded catalog mutation.
4. For create/update, restore the session to the resulting target identity when valid.
5. For active delete, remain in the safe workspace because the deleted identity cannot be restored.
6. If mutation or restoration fails, report the exact failure. Never claim restoration or success when the durable session remains in the safe workspace.

Catalog identities whose path is the session's runtime managed-worktree checkout are rejected. The AI must target the source workspace identity instead.

## User-visible metadata

Desktop permission review presents the action, target name/path/id, requested field changes, whether a safe switch is required, restoration/remain-safe behavior, and action-specific persistent-permission scope. Backend permission payloads omit unrelated private content and keep canonical approved arguments hidden from the dedicated presentation.

## Focused evidence

- `swarmd/internal/permission/policy_test.go`
- `swarmd/internal/run/service_workspace_manage_worktree_test.go`
- `swarmd/internal/store/pebble/workspace_store_test.go`
- `web/src/features/desktop/permissions/services/manage-workspace-permission.spec.ts`
