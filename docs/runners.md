# Runners

## Implementation status

The startup configuration parser accepts `environment_provider = local|runners|auto`
and `environment_runner_wait_seconds` (default `120`, nonnegative seconds).
Existing configuration files retain their defaults. **Only `local` is wired into
landing execution today.** Selecting `runners` or `auto` still fails at the API
provider initialization boundary; it does not silently execute locally.

The design below is the target contract, not an operational readiness claim.
The configuration change alone does not implement runner support. Initial hub
credentials and lease APIs are described below under **API**, including their
remaining gaps. Runner execution, management commands and installer support
remain unavailable.

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
checks and timeout, and a routed-Git-produced bundle of base..head with a shallow
base tree. Transfers must reject payloads above 512 MiB and exclude secrets, keys
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
`environment_failed` and `job.environment_failed` naming the runner, retries once
on another runner, then falls back locally under `auto`. Infrastructure failures
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

- `POST /v3/runners/claim` (`runners:claim`): claims at most one request, with a 60-second lease. Currently returns immediately, including when no request exists.
- `PUT /v3/runners/landings/ID/heartbeat` (`runners:report`): `{lease_id}` extends a live lease.
- `PUT /v3/runners/landings/ID/output` (`runners:report`): `{lease_id, output}` appends at most 64 KiB, retaining the newest 2 MiB.
- `PUT /v3/runners/landings/ID/result` (`runners:report`): `{lease_id, exit_codes, duration_ms}` terminates a live lease. Exit codes must match the check count.
- `PUT /v3/runners/status` (`runners:report`): `{state, reason, docker, gvisor}`, where state is idle, busy, or unhealthy.
- `GET /v3/runners/landings/ID/bundle` is reserved for `runners:claim` but currently returns 503: bundle creation and delivery are not implemented.

Lease expiry is reconciled during claim/report mutations. The first expiry allows one retry; the second becomes `environment_failed`. Stale lease IDs cannot mutate a replacement claim. No provider creates these requests yet.

Remaining work: bundled source construction and size enforcement, long-poll notification, proactive expiry and environment-failure signals, secret-free environment validation, and complete HTTP/restart coverage. This is not a ready-to-use remote landing provider.
