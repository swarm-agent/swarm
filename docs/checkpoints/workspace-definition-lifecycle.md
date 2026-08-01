# Workspace definition lifecycle

Workspace add is the single orchestration boundary. Both onboarding and Workspace Manager call `POST /v1/workspace/add`; the backend persists `definition_status: pending` before returning and owns the Router job, retries, durable result, and final error.

## Narrow validation commands

Tests are intentionally narrow; run only when validation is requested.

<copy label="backend store tests">cd swarmd && go test ./internal/store/pebble -run 'TestWorkspaceDefinition(GenerationRejectsStaleWrites|FailureIsDurable)$'</copy>

<copy label="backend prompt tests">cd swarmd && go test ./internal/api -run 'TestWorkspaceDefinition(PromptIsBoundedAndIncludesRootAgents|RouterProfileIsToolFreeReadMode)$'</copy>

<copy label="frontend lifecycle tests">cd web && node --import tsx --test src/features/workspaces/launcher/types/workspace.spec.ts src/features/workspaces/launcher/services/workspace-definition-lifecycle.spec.ts src/features/workspaces/launcher/components/workspace-definition-status.spec.ts src/features/workspaces/launcher/state/use-workspace-launcher.spec.ts src/features/desktop/onboarding/components/desktop-onboarding-workspace-definition.static.spec.ts</copy>

## End-to-end inspection

1. Configure a Router provider/model in Settings.
2. Add a workspace from Workspace Manager or onboarding.
3. Confirm the add response contains `definition_status: "pending"` and a positive `definition_generation`.
4. Keep Workspace Manager open. It polls the canonical workspace list while pending and should show an expandable definition when complete.
5. Re-add the same workspace. Confirm its generation increments and analysis runs again.
6. To inspect exhausted failure, configure an invalid Router model and re-add. The backend makes at most three total attempts, persists the final provider error, and returns guidance to change the Router model.

The API can be inspected directly with an authenticated desktop request:

<copy label="workspace list request">curl --fail --silent --show-error "$SWARM_API_URL/v1/workspace/list"</copy>

For durable session evidence, copy and inspect the configured local Pebble database with the checked-in helper. Use the hidden run session ID from daemon/session diagnostics:

<copy label="durable Router session inspection">./scripts/local-session-db-inspect.sh --session "workspace-definition-SESSION_ID" --copy-db --dump</copy>

Expected workspace API fields are `definition`, `definition_status`, `definition_attempt_count`, `definition_generation`, `definition_error`, `definition_model_suggestion`, and `definition_updated_at`. The durable Pebble record additionally retains the detailed definition lifecycle timestamps. Hidden Router sessions use `source: workspace_definition` and `navigation_hidden: true` metadata.
