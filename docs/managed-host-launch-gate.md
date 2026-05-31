# Managed-host Launch Gate

This Launch Gate proves real primary -> managed-host behavior through swarmd APIs only.
Remote SSH/deploy paths are out of scope for product-path evidence. Operator shell access is limited to host cleanup, daemon control for cold DB inspection, and log/artifact collection.

## Proof hosts

Use the host that matches the proof being run. Do not silently substitute hosts; record the host in the evidence directory metadata or run notes.

- `testbench` / `SwarmTarget1`: original verification host for migration gates and scripts that explicitly require the SSH testbench host. Use it when a harness says it must run on `SwarmTarget1` or when replaying older migration evidence.
- `testbench2`: newer managed-host/parity VM for Gate B managed-host proof and current no-mode/SwarmMode-removal parity checks, unless a specific migration gate explicitly names `testbench` / `SwarmTarget1`.

For SwarmMode removal proof, run local targeted tests first, then run clean onboarding proof on the clean VM used for that run, and run Gate B managed-host proof on `testbench2` unless the specific harness requires `testbench` / `SwarmTarget1`.

```sh
# Local source gate: functional code/tests/scripts must not depend on SwarmMode.
rg --glob '!dist/**' --glob '!.git/**' --glob '!.cache/**' 'swarm_mode|SwarmMode|swarmMode|requireSwarmModeEnabled'

# Local targeted tests before VM proof.
cd swarmd && go test ./internal/swarm ./internal/api -run 'EnsureLocalState|Group|Pairing|Managed|Onboarding'
```

## Matrix

| ID | Area | Required e2e proof | Status in minimal harness |
| --- | --- | --- | --- |
| 0 | Testbench sanity | Primary sees managed host online/selectable, valid workspace binding, healthy mirror endpoint, clean stale managed container/session-route baseline, target picker can select managed host and restore | Implemented |
| 1 | Harness extension | Repeatable evidence directory and matrix status artifacts | Implemented |
| 2 | Managed host AI | Open/message/run through primary managed-host APIs; primary mirror and topology route are correct | Implemented, provider key required |
| 3 | Managed DB persistence | Stop both daemons and inspect Pebble DB cold for session + assistant response on both hosts | Completed externally; artifacts captured on testbench |
| 4 | Managed container create | Create managed-host container from primary `/v1/swarm/replicate` API and prove managed topology/router/mirror attribution with no primary-local fallback | Implemented |
| 5 | Managed container CRUD | Update settings, stop/start, delete, prove stale routes/containers are gone, then recreate through primary APIs | Implemented |
| 6 | Managed container AI | Run session through primary routing to managed-host container and prove response, topology route, mirror, and route cleanup | Implemented, provider key required |
| 7 | Flow A | Primary main flow report/history | Pending |
| 8 | Flow B | Primary local-container flow report/history | Pending |
| 9 | Flow C | Managed host main flow report/history | Pending |
| 10 | Flow D | Managed-host container flow report/history | Pending |
| 11 | Full replay | Re-run after `swarm update dev` rebuilds both hosts | Pending |

## Minimal harness

Run the harness on the SSH testbench host, not from the developer workstation. The primary daemon currently binds admin traffic on loopback, so the default primary URL is `http://127.0.0.1:7781` when running on `SwarmTarget1`.

<copy label="ssh launch gate checkpoints 0-1">
ssh SwarmTarget1 'cd ${SWARM_GO_ROOT:-/path/to/swarm-go} && ./tests/swarmd/managed_host_launch_gate_e2e.sh \
  --scenario 0-1 \
  --managed-name SwarmTarget2 \
  --source-workspace-path ${SWARM_GO_ROOT:-/path/to/swarm-go}'
</copy>

If stale managed-host containers exist and should be removed, cleanup must still go through the primary managed-host API:

<copy label="ssh checkpoint 0 with API cleanup">
ssh SwarmTarget1 'cd ${SWARM_GO_ROOT:-/path/to/swarm-go} && ./tests/swarmd/managed_host_launch_gate_e2e.sh \
  --scenario 0 \
  --managed-name SwarmTarget2 \
  --source-workspace-path ${SWARM_GO_ROOT:-/path/to/swarm-go} \
  --cleanup-existing-managed-containers'
</copy>

Checkpoint 2 requires a real provider credential already configured on the testbench. It opens a managed-host session through the primary API, sends a deterministic prompt, starts a background run through `/v1/sessions/<id>/run/stream`, then verifies the primary mirror and topology route artifacts.

<copy label="ssh checkpoint 2 managed host ai">
ssh SwarmTarget1 'cd ${SWARM_GO_ROOT:-/path/to/swarm-go} && ./tests/swarmd/managed_host_launch_gate_e2e.sh \
  --scenario 2 \
  --managed-name SwarmTarget2 \
  --source-workspace-path ${SWARM_GO_ROOT:-/path/to/swarm-go} \
  --provider codex \
  --model gpt-5.5 \
  --thinking low'
</copy>

Checkpoint 4 creates a real child container on `SwarmTarget2` through the primary API. It fails closed on stale managed containers, records baseline counts, posts only to `/v1/swarm/replicate` with `target_host_swarm_id`, and verifies the returned deployment, topology host container, runtime owner, workspace binding, and managed mirror resources all agree on managed-host identity.

<copy label="ssh checkpoint 4 managed container create">
ssh SwarmTarget1 'cd ${SWARM_GO_ROOT:-/path/to/swarm-go} && ./tests/swarmd/managed_host_launch_gate_e2e.sh \
  --scenario 4 \
  --managed-name SwarmTarget2 \
  --source-workspace-path ${SWARM_GO_ROOT:-/path/to/swarm-go} \
  --container-name launch-gate-cp4'
</copy>

Checkpoint 5 creates a managed-host child, updates deployment settings, stops/starts it through `/v1/deploy/container/action`, deletes it through `/v1/deploy/container/delete`, proves primary topology/deployment cleanup, then recreates a new managed-host child through `/v1/swarm/replicate`.

<copy label="ssh checkpoint 5 managed container crud">
ssh SwarmTarget1 'cd ${SWARM_GO_ROOT:-/path/to/swarm-go} && ./tests/swarmd/managed_host_launch_gate_e2e.sh \
  --scenario 5 \
  --managed-name SwarmTarget2 \
  --source-workspace-path ${SWARM_GO_ROOT:-/path/to/swarm-go} \
  --cp5-container-name launch-gate-cp5 \
  --cp5-recreate-container-name launch-gate-cp5-recreate'
</copy>

Checkpoint 6 creates a managed-host child container, opens a session through primary `/v1/sessions?swarm_id=<child>`, sends a deterministic prompt through `/v1/sessions/<id>/messages`, starts the run through `/v1/sessions/<id>/run/stream`, verifies the proof token and topology route, then deletes the deployment and proves the route is gone.

<copy label="ssh checkpoint 6 managed container ai">
ssh SwarmTarget1 'cd ${SWARM_GO_ROOT:-/path/to/swarm-go} && ./tests/swarmd/managed_host_launch_gate_e2e.sh \
  --scenario 6 \
  --managed-name SwarmTarget2 \
  --source-workspace-path ${SWARM_GO_ROOT:-/path/to/swarm-go} \
  --provider codex \
  --model gpt-5.5 \
  --thinking low \
  --cp6-container-name launch-gate-cp6'
</copy>

The harness writes `matrix_status.json` plus checkpoint subdirectories under the evidence directory on the SSH host. By default that directory is created under `/tmp`; set `--evidence-dir` or `SWARM_LAUNCH_GATE_EVIDENCE_DIR` for a stable remote location.

## Evidence rules

- Product-path proof must be primary swarmd API -> managed-host swarmd API.
- Managed-host source/state synchronization must use the git-sync update path already proved for dev updates.
- Do not seed managed-host DBs or workspaces manually to make a checkpoint pass.
- Do not allow fallback behavior: if a managed target, route, topology identity, mirror resource, or managed-host container baseline is wrong/missing, the checkpoint must fail loudly.
- Do not use remote SSH/deploy APIs as evidence.
- Shell is acceptable only for SSH-hosted harness execution, cleanup, stopping/starting daemons for cold DB inspection, and collecting logs/artifacts.

## Checkpoint 0 required artifacts

- `checkpoint-0/readyz.json`
- `checkpoint-0/targets.json`
- `checkpoint-0/managed_target.json`
- `checkpoint-0/topology.json`
- `checkpoint-0/mirror_resources.json`
- `checkpoint-0/checkpoint_0_counts.json`
- `checkpoint-0/target_picker.json`

## Checkpoint 1 required artifacts

- `checkpoint-1/harness_metadata.json`
- `checkpoint-1/matrix_template.json`
- root `matrix_status.json`

## Checkpoint 2 required artifacts

- `checkpoint-2/open_session.json`
- `checkpoint-2/user_message.json`
- `checkpoint-2/run_start.json`
- `checkpoint-2/primary_session.json`
- `checkpoint-2/primary_session_metadata.json`
- `checkpoint-2/primary_messages.json`
- `checkpoint-2/primary_topology_session_route.json`
- `checkpoint-2/primary_topology_after_session.json`
- `checkpoint-2/mirror_resources_after_run.json`
- `checkpoint-2/checkpoint_2_summary.json`

## Checkpoint 4 required artifacts

- `checkpoint-4/primary_topology_before_create.json`
- `checkpoint-4/baseline_counts.json`
- `checkpoint-4/replicate_request.redacted.json`
- `checkpoint-4/replicate_response.json`
- `checkpoint-4/primary_deployments_after_create.json`
- `checkpoint-4/primary_topology_after_create.json`
- `checkpoint-4/primary_topology_host_containers.json`
- `checkpoint-4/primary_topology_runtime_owner.json`
- `checkpoint-4/primary_topology_workspace_bindings.json`
- `checkpoint-4/mirror_resources_after_create.json`
- `checkpoint-4/checkpoint_4_summary.json`

## Checkpoint 5 required artifacts

- `checkpoint-5/primary_topology_before_crud.json`
- `checkpoint-5/baseline_counts.json`
- `checkpoint-5/replicate_request.redacted.json`
- `checkpoint-5/replicate_response.json`
- `checkpoint-5/primary_deployments_after_create.json`
- `checkpoint-5/primary_topology_after_create.json`
- `checkpoint-5/settings_response.json`
- `checkpoint-5/action_stop_response.json`
- `checkpoint-5/action_start_response.json`
- `checkpoint-5/primary_topology_after_update_actions.json`
- `checkpoint-5/delete_response.json`
- `checkpoint-5/primary_topology_after_delete.json`
- `checkpoint-5/primary_deployments_after_delete.json`
- `checkpoint-5/primary_workspace_bindings_after_delete.json`
- `checkpoint-5/recreate_request.redacted.json`
- `checkpoint-5/recreate_response.json`
- `checkpoint-5/primary_topology_after_recreate.json`
- `checkpoint-5/primary_deployments_after_recreate.json`
- `checkpoint-5/checkpoint_5_summary.json`

## Checkpoint 6 required artifacts

- `checkpoint-6/primary_topology_before_create.json`
- `checkpoint-6/baseline_counts.json`
- `checkpoint-6/replicate_request.redacted.json`
- `checkpoint-6/replicate_response.json`
- `checkpoint-6/primary_deployments_after_create.json`
- `checkpoint-6/primary_topology_after_create.json`
- `checkpoint-6/primary_topology_host_containers.json`
- `checkpoint-6/primary_topology_runtime_owner.json`
- `checkpoint-6/primary_topology_workspace_bindings.json`
- `checkpoint-6/mirror_resources_after_create.json`
- `checkpoint-6/open_session.json`
- `checkpoint-6/user_message.json`
- `checkpoint-6/run_start.json`
- `checkpoint-6/primary_session.json`
- `checkpoint-6/primary_session_metadata.json`
- `checkpoint-6/primary_messages.json`
- `checkpoint-6/primary_topology_session_route.json`
- `checkpoint-6/primary_topology_after_session.json`
- `checkpoint-6/mirror_resources_after_run.json`
- `checkpoint-6/delete_response.json`
- `checkpoint-6/primary_topology_after_delete.json`
- `checkpoint-6/primary_deployments_after_delete.json`
- `checkpoint-6/primary_workspace_bindings_after_delete.json`
- `checkpoint-6/primary_topology_session_route_after_delete.json`
- `checkpoint-6/checkpoint_6_summary.json`
