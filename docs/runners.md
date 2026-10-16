# Runners

## Implementation status

The startup configuration parser accepts `environment_provider = local|runners|auto`
and `environment_runner_wait_seconds` (default `120`, nonnegative seconds).
Existing configuration files retain their defaults. Provider wiring is tested
with in-process runner API handlers; this is not a live remote-runner qualification.

Runner mode, management commands and installer support are described below,
including remaining service compatibility limitations. The target design is not
an operational readiness claim.

## Set up a runner

On an x86_64 Linux machine with systemd, from a checkout of the same ref as the hub:

```sh
sudo bash containers/headless/app/install.sh runner --ref main
```

This installs Docker (keeps an existing Docker with buildx), attempts gVisor,
builds the binaries from source, and creates the Docker-enabled `swarm-runner`
service account. It does not create projects, a setup page, AI keys or a tailnet.
Use a private tailnet with HTTPS to reach the hub; do not expose the owner socket.

On the hub, mint a one-use token valid for 15 minutes, naming the runner:

```sh
sudo swarmctl runner token --name worker-one
sudo swarmctl runner list
sudo swarmctl runner remove worker-one
```

Transfer the token privately (not as a command-line argument). On the runner:

```sh
sudo swarmctl runner join https://hub.example --name worker-one < join-token
sudo swarmctl runner status
sudo bash /var/lib/swarm-runner-install/src/containers/headless/app/install.sh update
```

`token` and `join` default their name to the local hostname; because the hub
binds tokens to names, use matching `--name` values on different hosts.
`join` saves `/etc/swarm-runner/credential` and `runner.env` root-only (0600),
installs `swarm-runner.service`, and enables it. A failed install after enrollment
keeps the credential: revoke that runner on the hub before attempting enrollment
again. Status displays systemd state and recent journal entries; it cannot yet
provide a structured last-claim record.

**Remaining compatibility gaps:** systemd `LoadCredential` hands credentials to
the service user, but the current runner binary requires a root-owned credential.
Unprivileged service startup needs a follow-up in `swarmd/cmd/swarmd/runner.go`
(outside the management-command change). Hub listing now includes health and
engine, but current landing is not exposed by the store API. Automatic idle
restart needs a runner drain handshake; update builds the saved ref, waits at
most 120 seconds for the service to stop, and fails without activation if it is
still active. It never stops active work. Do not treat these commands as proof of
end-to-end landing readiness.

## Choosing where landings run

- `environment_provider = local` (default) uses a fresh local landing environment.
- `environment_provider = runners` queues an `archive-v1` payload for a runner.
- `environment_provider = auto` gives runners a chance to claim the landing, then
  uses local execution after `environment_runner_wait_seconds` (default `120`).
  Zero selects an immediate fallback when no claim is available.

The claim wait also bounds `runners` acquisition; exhaustion reports a clear
`environment_failed` reason rather than sending work back to the coder. Two
lease expiries or a runner-reported payload failure are infrastructure failures.
`auto` retries such a failure once locally. Actual check failures retain the
normal coder send-back behavior. Delivery records expose `environment` as
`local` or `runner:NAME`; provider selections are logged.

The hub retains bounded aggregate runner output, streamed into the landing's
bounded check output. The current protocol has batch exit codes, not per-check
output frames: aggregate output is attached to the first check result and retained
for any later failing check's diagnostic excerpt.

## Target design

The hub owns projects, folders, job clones, the landing queue, signals, keys and
agents. A runner uses the same `swarmd` binary on another machine with Docker
(and gVisor when available), claims one landing at a time and accepts no inbound
connections. It must not hold hub projects, provider keys or agents.

Runners connect outbound to the hub API through a tailnet, relay or HTTPS URL.
Runner credentials authorize only claim, output, result and heartbeat operations,
never ordinary hub APIs, landing integration or push. Join tokens are one-time,
expire after 15 minutes and have root-only storage; credentials are stored with
mode `0600` and are revocable.

`local` remains the default. `runners` waits for a healthy idle runner until the
acquisition context ends. `auto` waits up to `environment_runner_wait_seconds`
for a runner, then uses local execution.

The hub sends the folder environment definition (base image, setup and environment),
checks and timeout, and a routed-Git-produced `archive-v1` source archive of the
rebased head tree (not Git history). Transfers must reject payloads above 512 MiB and exclude secrets, keys
and stand-ins. A runner builds/reuses an image by definition hash, unpacks the
exact commit into a fresh container, and executes checks with `bash -lc` using
the local provider's working-directory layout and timeout.

The protocol uses `POST /v3/runners/claim`, bounded output chunks through
`PUT /v3/runners/landings/ID/output`, heartbeats every 20 seconds, and results
through `PUT /v3/runners/landings/ID/result`. Results carry exit codes and the same
failing-line excerpt rule as local send-back. Containers are destroyed after use;
runner disk below 2 GiB or unavailable Docker makes a runner unhealthy and stops
new claims.

Claims require durable leases with a 60-second heartbeat expiry. Expiry produces
`lease_expired` on the first attempt, allowing one retry. The second expiry
produces `environment_failed` and `job.environment_failed` naming both attempts.
Local fallback under `auto` is handled by the provider. Infrastructure failures
never send work back to the coder. Restart recovery must reattach a result to its
exact durable claim or rerun the landing, never accept a stale result as success.
A runner can make landing slower, never wrong: only the hub can integrate or push.

## Planned operator workflow (not yet available)

1. On the hub, `swarmctl runner token` prints a one-time token.
2. On the runner, `swarmctl runner join HUB_URL` reads that token from stdin,
   stores the credential, writes service configuration and starts the service.
3. The service runs `swarmd runner --hub URL --credential-file PATH`.
4. `swarmctl runner list` shows name, idle/busy/unhealthy state, Docker/gVisor
   availability and last seen; `swarmctl runner remove NAME` revokes access.

The planned `install.sh runner` mode installs Docker, gVisor, the binary and
service without projects, setup UI or AI keys. `swarm_status` must expose the
provider and runner counts/states, with `runner.joined`, `runner.removed`,
`runner.unhealthy` and `job.environment_failed` signals.

## Required acceptance evidence

Before enabling remote providers, observe a real runner landing with no hub check
container, then kill a runner mid-check and observe durable environment failure,
retry and local fallback under `auto` without coder send-back. Also verify
revocation, credential scope refusals, transfer size limits and restart recovery.
Parser tests alone establish none of these properties.

## API

Runner hub support is under development; local landing checks are unchanged.

Owner-only routes (scoped keys are refused):

- `POST /v3/runners/join-tokens`, body `{name}`: returns a single-use join token, valid for 15 minutes.
- `GET /v3/runners`: credential metadata, excluding hashes and secrets.
- `DELETE /v3/runners/NAME`: revoke the named credential.

`POST /v3/runners/join`, body `{token}`, exchanges the join token for a long-lived credential returned once. Credentials have kind `runner` and exactly `runners:claim` and `runners:report` scopes. They cannot access other product routes.

Runner-only routes:

- `POST /v3/runners/claim` (`runners:claim`): claims at most one request, with a 60-second lease. Waits up to 30 seconds when no request exists, wakes on creation, and returns `{landing: null}` on timeout.
- `PUT /v3/runners/landings/ID/heartbeat` (`runners:report`): `{lease_id}` extends a live lease.
- `PUT /v3/runners/landings/ID/output` (`runners:report`): `{lease_id, output}` appends at most 64 KiB, retaining the newest 2 MiB.
- `PUT /v3/runners/landings/ID/result` (`runners:report`): `{lease_id, exit_codes, duration_ms}` terminates a live lease. Exit codes must match the check count.
- `PUT /v3/runners/status` (`runners:report`): `{state, reason, docker, gvisor}`, where state is idle, busy, or unhealthy.
- `GET /v3/runners/landings/ID/payload` (`runners:claim`): serves the gzip archive only to the current claiming runner in the same account with an unexpired lease.
- `GET /v3/runners/landings/ID/bundle` returns 410 Gone; use `payload`.

The creator supplies `source_clone_path`, `base_commit`, and `head_commit`. Creation runs routed `git archive --format=tar.gz <head_commit>` and persists `payload_format: "archive-v1"`, `archive_sha256`, and `archive_bytes`. Payloads live in private daemon-owned request directories beneath the store, never the source clone. The compressed archive cap is 512 MiB; creation failures are recorded as `environment_failed` with a clear `delivery_reason`. Terminal requests have their archive removed. Runners needing Git initialize a repository and commit this tree; the runner implementation must expose the original head as `SWARM_LANDING_HEAD`.

A daemon ticker proactively reconciles 60-second leases. The first expiry becomes claimable `lease_expired`; the second becomes `environment_failed`, with signal `job.environment_failed` and `delivery_reason: "environment failed on runners A, B"`. Stale lease IDs cannot mutate a replacement claim. No provider creates these requests yet.

Environment validation rejects account secret slot names in keys or values and all `swarm-secret://` stand-ins before creating an archive or request. This intentionally excludes all account slots, not only currently granted folder slots. `swarm_status` includes runner counts by state, last seen in each runner item, and pending/claimed counts. Join, removal and unhealthy reports emit their respective runner signals.

## Runner mode

`swarmd runner --hub URL --credential-file PATH [--work-dir DIR] [--name NAME]`
starts an outbound-only landing worker, not the daemon. The credential is a raw
bearer token in a root-owned regular file with no group/other permissions;
symlinks are refused. Use HTTPS or an explicitly protected private transport.
It needs Docker, registered `runsc` and `runsc-build` runtimes, the installed
Swarm sandbox image/network, and at least 2 GiB free under its work directory.
No startup configuration keys are changed. Without `--work-dir`, it creates a
disposable directory using the operating system temporary-directory contract.

The loop claims one request, renews its lease every 20 seconds, streams bounded
output, and reports codes/duration and the local failing-lines excerpt. Status
is reported at startup, transitions, and every 60 seconds; engine/disk failures
stop new claims. Environment images reuse the local definition hash and sandbox
builder. A fresh local landing sandbox unpacks the verified archive and runs each
check with `bash -lc`. Request directories, containers and home volumes are
released after use; definition-keyed images may be reused. It never starts
agents, receives provider keys, integrates commits, pushes, or listens inbound.

Claims must carry `payload_format: "archive-v1"`, `base_commit`, `head_commit`,
`archive_sha256` and `archive_bytes`. The runner fetches `/payload`, verifies
its exact byte count and SHA-256 (maximum 512 MiB), then extracts it inside the
fresh container at `project/checkout`. It initializes a repository and commits
the tree once for repository-aware checks; this local commit is not the real
head. Checks receive the real IDs as `SWARM_LANDING_HEAD` and
`SWARM_LANDING_BASE`, overriding any same-named definition variables. No Git
history is transferred. Payload fetch, integrity, or preparation failures report
`state: "environment_failed"` with the reason in output and no check codes.
There is no bundle fallback. Hub environment-failure result support remains
separate work; incompatible hubs cause an explicit error, not success.
Fake-hub and archive preparation tests do not prove live Docker/hub interoperability.

Remaining work: the production provider caller (job 5c), hub environment-failure result support, and full live/restart qualification. Focused tests exercise real-clone archives, ownership, cleanup, the size-limiting writer, concurrent waiters, fake-clock expiry, secret rejection and monitoring. They do not establish crash-atomic archive/request/signal persistence or live remote execution. This is not a ready-to-use remote landing provider.
