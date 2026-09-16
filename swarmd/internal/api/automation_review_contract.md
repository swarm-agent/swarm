# Accepted automation review/edit

The existing `POST /v3/sessions/{parent}/sidechats/plan` accepts an alternative exclusive body:

```json
{"automation_id":"exact-id","automation_revision":3,"workspace_id":"workspace"}
```

Do not send permission/plan fields with this body. Account and user come from authenticated transport. Parent access, workspace access, current definition revision, definition parent binding, and canonical approved plan document pins are checked before the existing V3 sidechat create/rebind mutation. The response retains `session_id` and `parent_session_id` and adds `automation_id` and `automation_revision`. The server-generated prompt contains `context_source: automation_definition` and the immutable definition record, not client content. Historical plan-permission review remains unchanged.

`Service.EditParentDefinition` now returns a **non-applied proposal**, not a saved definition. Its returned record revision is the expected live base revision; its bool is false. Runtime consumers must label the result pending/non-applied, never saved or enabled. Require the actual Plan sidechat runtime identity; its server-created automation-review metadata must match workspace/id/revision, and the live definition must still match. Omitted plans retain exact old pins. Supplied plans require explicit digests and exact, separately approved canonical revisions owned by the same parent/account. No arbitrary parent-session read capability is granted.

User acceptance uses the existing authenticated `POST /v3/automations` save action with `workspace_id`, `id`, a fresh `mutation_id`, `expected_revision` equal to the proposal record revision, and `definition` equal to the proposed definition. That canonical save CAS revalidates access and pins. Execution-affecting changes are paused and clear approval. Then use the existing `/v3/automations/approve` exact revision/digest (and typed `proposal` reference when required) and its explicit enable proposal. Do not add a conversion API or silently apply the AI proposal. Already-admitted occurrences retain their pinned revisions.

Validation handoff: new domain tests cover exact review identity, stale/account/parent/digest rejection, instruction replacement and omission, unapproved/foreign pins, forged sidechat context and no live mutation. The API test exercises actual V3 sidechat creation without permission history and rejection without session creation. These tests do not establish concurrent review/rebind linearizability or provider behavior. Not run; parent validation required. Parent owns gofmt, focused execution, atlas update, and test-audit ledger reconciliation outside this child scope.
