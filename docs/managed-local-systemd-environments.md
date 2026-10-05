# Managed local systemd test environments

This is an explicit `local_podman` adapter to the existing environment lifecycle,
not a workspace execution profile. Docker/SSH defaults are unchanged. The
running daemon must contain these changes before these actions are available.
No connection default, socket, host policy, install or onboarding is changed by
saving a definition.

## Admission prerequisites

Use `manage_connections create` with `kind: local_podman`, a name and the owning
workspace, without Docker/SSH transport configuration. Then use `check` and
`capabilities` on that connection. Checks inspect metadata only and require:

- Linux, local rootless Podman with a reported version; no remote/rootful fallback;
- crun and slirp4netns;
- cgroup v2, systemd cgroup manager, delegated cpu/memory/pids controllers;
- a reachable user systemd manager. Managed builds also invoke `systemd-run`.

A failed check does **not** prove any particular missing prerequisite unless its
receipt says so. Checks neither install packages nor change delegation, user
services, credentials, sockets or host policy. Engine compatibility and actual
systemd boot still require separately authorized runtime qualification.

## Exact committed build inputs

Save an environment with the following shape (replace placeholders with
account-authorized catalog identities and full lowercase commit OIDs). The
product commit must be the selected original candidate, **not current dev**.
The recipe is a separately authorized committed repository input; do not copy
its source into the product repository or substitute the latest recipe.

```json
{
  "name": "Manual onboarding test",
  "mode": "deployable",
  "role": "testing",
  "preferred_connection_id": "<local-podman-connection>",
  "build": {
    "product": {"workspace_id": "<product-workspace>", "workspace_generation": 1, "commit": "<exact-product-oid>"},
    "recipe": {"workspace_id": "<recipe-workspace>", "workspace_generation": 1, "commit": "<exact-recipe-oid>"},
    "recipe_directory": "<committed-recipe-directory>",
    "recipe_file": "<committed-recipe-directory>/Containerfile"
  },
  "container": {
    "image": "managed-build",
    "command": ["/sbin/init"],
    "rootless_systemd": {"cgroup_namespace": "private", "network": "slirp4netns", "pids_limit": 1024},
    "exposed_ports": [{"container_port": 15555, "host_port": 15555, "protocol": "tcp"}]
  },
  "provisioning": {"strategy": {"kind": "registry_image", "registry_image": {"image": "managed-build", "pull_policy": "never"}}},
  "deployment_policy": {"reuse": true, "max_instances": 1, "release_behavior": "none", "idle_timeout_seconds": 0}
}
```

Use `manage_environments create` with the nested `environment` object (or the
same top-level definition fields). Nested values are objects, not JSON strings.
Unknown fields, raw source host paths, mutable refs and runtime flag bags are
rejected. Build sources are resolved from the account catalog with generation
checks before and after execution. A saved definition is not a task-result
attestation: the selecting parent must verify the task receipt's exact commit
and recipe metadata before saving it. A task reference itself is not a source
path. Builds do not use `project_result` (that field is for leased validation).

The product tree is exported at context root and the selected recipe directory
under `.swarm-recipe/<recipe_directory>`. `COPY` in the recipe must use these
paths. Git archive exports committed content only; working-tree files and Git
metadata are not copied. Symlinks, hardlinks, special files and unsafe paths are
rejected. Credential-shaped files are excluded. Review committed sources for
secrets: filename exclusion is not a secret scanner. The managed exporter, not
the product's unrelated `.dockerignore`, defines the clean context. Archives
are capped at 512 MiB and 30,000 entries across both inputs.

## Action flow after reviewed integration/rebuild

1. `build` with `environment_id`, explicit `connection_id`, `timeout_ms` (at most
   600000), and a stable `idempotency_key`. Do not supply source workspace paths,
   runtime targets, commands, environment overrides or a previous build ID.
2. Retain the returned `operation_id`. Inspect `get_operation` or durable updates
   without busy polling. Only `status: succeeded` with `result.build` is usable.
   `cancel_operation` uses the ordinary supervised cancellation path.
3. `ensure` (or `deploy`) with the same environment/connection and
   `build_operation_id` equal to that exact successful operation. No tag/image
   override is accepted. An old source/connection receipt is rejected. The
   deployment retains the build provenance and inspects the actual image ID.
4. Use the returned deployment and lease with `exec` only for authorized setup.
   Onboarding remains a deliberate human operation, never an automatic build or
   deployment side effect. Release the exact lease with `release` when done.
   Release retains the container and user's state; explicit `destroy` is separate.

Runtime ports, including dynamically assigned ports, publish only to
`127.0.0.1`. The adapter emits `--systemd=always`, private cgroups,
`slirp4netns:allow_host_loopback=false`, a PID limit, `--pull=never` and no host
binds. No host HOME, provider credential, Docker socket or host network is
mounted. Recipe image installation and runtime setup remain separately reviewed.

## Build ownership and evidence

Builds run one job in an operation-owned systemd user unit with a 600-second
maximum, control-group termination, 1024-task/CPU/memory limits and a per-file
size limit. Image export is capped at 8 GiB. Recipe output is discarded rather
than persisting potential secrets. Build storage, auth/config files, HOME,
hooks and temporary directories are private to that operation under the daemon
cache root. Empty registry auth, no proxy inheritance, OCI isolation and private
rootless networking are explicit. The account's local engine imports only the
resulting image by immutable ID; labels bind it to operation, inputs and product
commit. Success returns source identities, definition/context digests and image
ID, not claims about install/onboarding health.

Cancellation/recovery stops and verifies only the operation unit, unmounts its
private store and removes its owned scratch. Failed/cancelled imported images
are selected by the operation label and removed without force; unrelated images
are never pruned. Unconfirmed cleanup is `cleanup_failed`, not success. A
successful image remains available to the accepted build receipt. Operators
must separately monitor disk capacity: per-file/context/export limits are not
a filesystem-wide quota. Rootless container isolation is not a sandbox for an
unreviewed hostile recipe; only explicitly reviewed committed recipes belong
in this workflow.

Focused unit/contract tests prove decoding, source/receipt admission, bounded
archive handling, argv isolation, cancellation and cleanup with injected
engines. They do not establish actual host capability, image build/install,
systemd boot or user onboarding. Those need exact-revision live receipts after
reviewed integration and daemon rebuild.
