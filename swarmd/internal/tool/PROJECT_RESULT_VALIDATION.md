# Managed task-result validation

`project_result` is the exact `target.reference` from `manage_projects inspect_files`.
The resolver rechecks the account, catalog generation, current task attempt/session,
quiescence, ownership, clean tree and exact HEAD. Its resolved isolated root is an
internal capability, not a caller filesystem path or a host mount.

For a managed local Podman definition, `build` replaces only the saved product
commit with the selected result commit, exports from the authenticated isolated
root and preserves the separately authorized committed recipe. The saved definition
is not changed. A SHA-256 result-reference binding is retained in build provenance
and operation idempotency; the provider still exports exact committed Git objects.
Queued build/ensure/deploy and exec re-resolve before effects; failed or stale builds
are not admitted. Images have an empty deployment `workspace_path`.

## Parent validation

Tests and live dogfooding must be performed by the parent on the reviewed commit.
From `swarmd/` (Go and Git required):

```sh
go test ./internal/tool -run 'TestProject(Result|Inspection)|TestManagedBuildDefinitionRoundTrip|TestTaskEnvironment' -count=1 -timeout=120s
go test ./internal/environments/lifecycle -run 'TestProjectResult|TestManagedBuild|TestDeploymentWorkspace|TestTaskLease|TestPrepared' -count=1 -timeout=120s
go test ./internal/api -run '^TestProjectInspectionExactResult$' -count=1 -timeout=120s
```

For live validation after activating the reviewed daemon repair, obtain a fresh
`target.reference` from `inspect_files` for the quiescent committed task result.
Replace `RESULT` below with that complete JSON object; identifiers below are
placeholders, not authority. Keep the saved recipe source unchanged.

```json
{"action":"build","environment_id":"ENV","connection_id":"CONNECTION","project_result":RESULT,"idempotency_key":"result-build-unique-key","timeout_ms":600000}
{"action":"get_operation","operation_id":"BUILD_OPERATION","project_result":RESULT}
{"action":"ensure","environment_id":"ENV","connection_id":"CONNECTION","build_operation_id":"BUILD_OPERATION","project_result":RESULT,"ttl_millis":600000,"idempotency_key":"result-ensure-unique-key"}
{"action":"get_operation","operation_id":"ENSURE_OPERATION","project_result":RESULT}
{"action":"exec","deployment_id":"DEPLOYMENT","lease_id":"LEASE","project_result":RESULT,"command":["sh","-lc","go version && git --version"],"timeout_ms":30000,"max_output":4096}
{"action":"release","deployment_id":"DEPLOYMENT","lease_id":"LEASE","project_result":RESULT,"reason":"parent validation complete"}
```

Wait for actual successful operation receipts, not queued acceptance. Do not
busy-poll or substitute a registry image, configured dev checkout, Docker socket
or mounted identity. Exec/release require the same result deployment and the
parent's own receipt. Strict result revalidation also applies to release; if a
result advances, ordinary owner-authorized lease cleanup remains available without
claiming validation of the stale result. Live approval/infrastructure policy is
unchanged.

The existing Docker `local_mount` route remains supported. Task attachment records
retain the full build provenance in `PreparedDeploymentSource`; attach/acquire and
consumer exec/release continue through their existing CAS and receipt routes.
This change does not grant task consumers environment administration or bypass
attachment admission. No tool schemas or prompts are changed.

No tests or live validation are claimed by this implementation handoff.
