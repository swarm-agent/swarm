# Headless core host package — Stage 2B

This is the independent Linux amd64 host package. It contains the dedicated
Headless composition as `bin/swarmd`, the existing `swarmctl` operational CLI,
`swarm-fff-search`, the vendored `libfff_c.so`, licenses/notices, a manifest,
checksums, a fresh-install script and one systemd unit. It contains no Desktop,
TUI, setup app, SDK, Node distribution or frontend build output.

The older `containers/headless/app` installer and OCI/npm release pipeline keep
their existing contracts. They do not produce this package. This producer emits
an explicitly unqualified `candidate-*` archive; Stage 7 owns publication and
release qualification. Stage 2C owns independent update, drain and rollback.

## Build and verify

Use the repository's required Go toolchain, CGO compiler, jq, tar, sha256sum and
a writable isolated `TMPDIR`. From the repository root:

```sh
bash scripts/build-headless-core.sh candidate-local "$TMPDIR/core-output"
```

The output directory must not exist. The builder compiles `cmd/swarm-headless`
as the daemon, checks imports and linked symbols, and packages only its fixed
payload. It never installs or starts anything, invokes npm/Python, or builds a UI.
`manifest.json` records source commit, dirty-source status, compiler, native build
libc, platform, runtime prerequisites and checksum-inventory hash. Native builds
must deploy to a compatible glibc userspace; this is not a cross-distribution
compatibility promise. The archive's adjacent `.sha256` authenticates only bytes
against a trusted checksum, not publisher identity. Obtain both from a trusted
producer; publication signing remains a release concern.

Verify the archive checksum before extraction, extract into a new private
scratch directory, then verify the extracted package:

```sh
sha256sum --check swarm-headless-core-candidate-local-linux-amd64.tar.gz.sha256
tar -xzf swarm-headless-core-candidate-local-linux-amd64.tar.gz
bash swarm-headless-core-candidate-local-linux-amd64/verify.sh \
  swarm-headless-core-candidate-local-linux-amd64
```

The verifier rejects missing/extra payload files, symlinks/special files, changed
bytes, malformed checksum paths and mismatched composition/platform metadata.
Only extract an archive from the trusted build producer; the extracted shell
verifier is executable package code, not a verifier for hostile archives.

## Fresh installation

Prepare a fresh Linux amd64 systemd host with compatible glibc/shared libraries,
Git, Bash, Bubblewrap (`bwrap`), CA certificates, jq, coreutils, useradd/groupadd
and runuser. Bubblewrap requires usable unprivileged user, mount, PID and network
namespaces; the daemon probes them as its own user. Dependency
installation is an explicit operator action; this installer never downloads
packages or changes Docker, firewall, Tailscale or network policy.

On Ubuntu hosts enforcing AppArmor's unprivileged-user-namespace restrictions,
install `apparmor-profiles` and review its packaged
`/usr/share/apparmor/extra-profiles/bwrap-userns-restrict` profile. An operator
must install/load that profile for `/usr/bin/bwrap` before starting the service;
installing Bubblewrap alone may leave namespace setup blocked. Keep global
namespace restrictions enabled. The profile permits Bubblewrap's namespace
setup and strips capabilities from its children. Verify the daemon's execution
readiness after loading it and restarting the service.

The supplied unit deliberately leaves `ProtectKernelTunables` off: its masked
procfs paths prevent unprivileged Bubblewrap from mounting a private `/proc`.
The daemon remains an unprivileged service account with `NoNewPrivileges`;
agent commands use private namespaces and dropped capabilities. Do not enable
that setting without repeating actual service-level execution qualification.

To inspect installation layout without root or service changes:

```sh
bash /path/to/extracted-package/install.sh --stage-root "$TMPDIR/fresh-root"
```

On the intended fresh host, after reviewing the package:

```sh
sudo bash /path/to/extracted-package/install.sh --install
sudo systemctl daemon-reload
sudo systemctl enable --now swarm-headless.service
sudo swarmctl setup status
```

`--install` creates only the fresh service account, release files, CLI wrapper,
unit and service home. It does not activate the unit. It refuses existing Swarm
paths/accounts before writes; use the later qualified migration/update procedure
for an existing installation. Do not point it at a running Swarm installation.
The unit starts only the Headless daemon. No setup-app unit is installed or
required. systemd creates the canonical private directories:

- `/etc/swarmd` — startup configuration;
- `/var/lib/swarmd` — canonical databases, credentials and private owner socket;
- `/var/cache/swarmd`, `/run/swarmd`, `/var/log/swarmd` — cache, runtime and logs;
- `/var/lib/swarm` — service home and default working directory.

Startup/API defaults remain loopback-only. The owner CLI uses the mode-0600 Unix
socket as the daemon account; root's CLI wrapper drops to that account. It never
falls back to an unauthenticated network setup endpoint. Readiness is available
at `http://127.0.0.1:7781/readyz`; ordinary APIs require credentials. HTTP 200
and `ok=true` mean the control API is available for setup/diagnostics. Check
`agent_execution_ready`, `agent_execution_mode` and `agent_execution_reason`
separately before submitting commands. With missing isolation, the required
default reports `false`, `required`, `sandbox_unavailable`. The authenticated
`GET /v3/box/summary` reports the actual probe failure; startup logs and
`sandbox.unavailable` signals report blocked execution and the recovery action.

## Initialize without a UI

Run the following on the intended host. Choose the actual owner/machine names
and selected committed repository; the CLI never chooses a model for you.

```sh
sudo swarmctl setup identity --username OWNER --name MACHINE
sudo swarmctl setup workspace --path /absolute/selected/repository --name PROJECT
sudo swarmctl setup credential --provider PROVIDER --api-key-stdin < /protected/provider-key
sudo swarmctl setup model --role ROLE --provider PROVIDER --model MODEL --thinking THINKING
sudo swarmctl setup complete
sudo swarmctl setup status
```

Supply the key through the existing secret-file or secret-manager stdin mechanism,
never argv or chat. For machine identity providers, see
`swarmctl setup credential --help`; for device-code sign-in, see
`swarmctl setup codex-login --help`. Assign every canonical role (`action`,
`plan`, `compact`, `finder`, `coder`, `designer`, `router`) from the provider's
supported model/settings. `setup complete` fails until identity, a provider,
a workspace and all model assignments exist. Missing provider access must remain
visible, never be replaced with a fake credential or reported as ready.

For a local AI client, `sudo swarmctl mcp --access read` uses the existing stdio
bridge. Choose `write` or `full` only for the intended authority. For explicit
HTTP clients, `swarmctl setup sdk-token --help` describes scoped tokens; redirect
its output to an operator-owned mode-0600 file or pipe. Tokens go in headers.
Remote access requires a separately authorized private transport; this package
does not expose a public listener.

## Execution prerequisites

Daemon startup and owner setup require Git/Bash, CA certificates, FFF and its
resolved system libraries. This package includes FFF and search, not compilers
for every user project. Coding tasks require the selected repository's tools.
Both the packaged service and the dedicated binary require local isolation by
default. The headless backend is Bubblewrap: each command sees the host system
executables/libraries read-only, its assigned checkout and private scratch.
The daemon, provider credentials and owner API remain outside. No Docker image,
container daemon, managed lease or per-agent RAM reservation is required.

The initial local policy has a private network namespace: commands cannot reach
the host loopback API or the internet. Install required dependencies on the host
or use a prepared managed environment. Home-installed toolchains and caches are
not implicitly exposed. Daemon file mutations also reject symlink traversal and
Git administration paths; use canonical Git tools for staging/committing. This policy is filesystem/process/network isolation,
not a complete resource quota or a VM. Worker admission defaults to the logical
CPU count (minimum one), configurable with `--worker-max-concurrent-runs`; this
is a concurrency limit, not a per-agent memory allocation. Qualification must report remaining
resource and toolchain limits explicitly.

If Bubblewrap is absent or the kernel rejects its namespaces, setup and diagnostics
remain accessible but commands fail closed, including repository setup that
requires Git. Restore isolation before completing workspace registration. The installer checks for `bwrap`
before creating an account/files. It never installs dependencies, grants Docker
access or changes kernel/network policy. Inspect the authenticated box summary
for the actual backend and failure before submitting work.

Headless rejects `--sandbox=auto`. Explicit `--sandbox=off` selects direct host
execution under the daemon account's OS permissions, with permission bypass
disabled. It is an operator choice, never automatic recovery. Desktop keeps its
existing default. `--sandbox-backend=docker` explicitly selects the existing
container backend and requires that backend's image/network/service prerequisites.

Managed execution is an optional separate selection with a task/source/attempt
bound lease. A selected lease must override local mode, including local off;
unavailable or invalid selection must refuse, never fall back to host execution.
The current selector supports only a directly assigned current task Coder; a
Swarm parent’s separately delegated Coder child is not yet supported. The current
managed slice is prepared committed-source command validation;
remote file editing and a full repair cycle need separate qualification.

HTML/SVG capture requires Chrome at `/opt/google/chrome/chrome`; video requires
FFmpeg/FFprobe. Node may be needed by a user's artifact or tooling. Those are
execution dependencies, independent of the removed setup app. Install and qualify
them for the capabilities being deployed; core readiness does not prove artifact
rendering or provider-backed execution. No capability is deleted to avoid a
runtime prerequisite.

## Verification and remaining gates

`scripts/test-headless-core.sh` builds no UI and tests the extracted package in
new filesystem/network/PID namespaces with Bubblewrap. It runs the real packaged
daemon and CLI against fresh canonical paths, initializes identity/workspace,
checks default Bubblewrap readiness without Docker, then repeats with Bubblewrap
unavailable to verify refused execution and disabled bypass. It verifies explicit
off and control-versus-execution readiness; checks authenticated
access and token revocation, rejects Desktop routes and
incomplete provider setup, and checks durable identity after a restart. It cannot
reach the host daemon, host data or external providers. The staged installer is
also checked for repeat-install refusal and payload tampering.

This is isolated process deployment evidence, not a fresh VM/systemd boot test
or provider qualification. A real provider must be configured by its owner before
agent execution. Stage 2C update/recovery, H1–H8 agent/tool repairs, full task and
media qualification, and the final release gates remain required.
