# Dedicated local Codex testbench

This runner-only extension supersedes the proposed host credential-lease design.
Use one **new dedicated test login**, not host Swarm credentials. No host API,
provider registration, service restart, live database access, or token export is
introduced. Normal swarmd builds do not import the bridge. Local candidate builds
use `testbench-codex-overlay.py` to replace the candidate provider registry and
configure the canonical account settings without modifying production source.

## Credential boundary

`swarmd/cmd/testbench-codex-broker` supports `-action init|login|status|serve` with
an explicit `-state` directory. Initialization requires a new private directory;
subsequent operations require its purpose marker, owner/mode checks and exclusive
process lock. Login uses the existing supported Codex device authorization flow;
it prints only the approval URL/code, never OAuth tokens. No secrets belong in
`.env`, argv, artifacts or logs. Stop this dedicated broker before renewing login.
Do not stop host Swarm or the pool supervisor.

The broker owns its independent Pebble credential store and serializes complete
provider requests, including expiry/401 refresh and persistence. At most eight
requests queue, each with a five-minute deadline. A durable in-flight marker
fails closed after an interrupted/failed request; renew the dedicated login
rather than replaying an uncertain rotating token. This is deliberately
conservative and may require renewal after a non-auth provider failure.

Serve accepts `-sockets` containing one to eight comma-separated absolute UNIX
socket paths. Provision **a distinct private 0700 parent and socket per lane**;
parents must be owned by the broker user. Never give two lanes the same socket.
A guest receives only its readonly socket bind, not the parent or credential
store. Socket mode permits the mapped guest UID, while private host ancestors
prevent other users from connecting. The trusted root pool operator remains the
management authority; this is not hostile same-UID isolation. Request destinations
are fixed; unknown operations/fields, model overrides and redirects are rejected.
No credential retrieval endpoint exists. Affinity keys are scoped by account and
lane. Text/tool response streaming is supported; media capability declarations
are not yet provided by the bridge and must fail closed, not fall back.

## Candidate and runner integration

Use the existing explicit root-owned pool configuration. Add non-secret
`SWARM_TESTBENCH_TARGET=local`, provider `codex`, model `gpt-5.6-luna` and thinking
`medium` for default/action/plan/coder/designer fields. Compact, Finder and Router
are also configured by the candidate startup helper. The shipped catalog must
resolve exactly; unsupported settings stop startup or requests.

Deploy with `python3 scripts/local_testbench_codex.py deploy --env-file CONFIG
--worktree WORKTREE --socket LANE_SOCKET` under the explicit trusted root operation.
The new manifest identifies `dedicated-codex-luna-medium-v1`; existing pool and
supervisor files are unchanged. Save the returned generation as non-secret
`SWARM_TESTBENCH_GENERATION`. The maintained `testbench-e2e-tunnel.sh check|run`
routes local mode to this client, verifies exact committed HEAD, profile, owned
units and generation, and does not auto-replace candidates or use SSH tunnels.
Root pool metadata operations require explicit sudo; runner argv drops to the
invoking non-root UID with supplementary groups cleared. Runner duration is
bounded at ten minutes with lease renewal and process-group termination.

Remote `container` mode retains the existing Fireworks validation/deployment and
SSH path. An existing unauthenticated local candidate is not a Codex candidate
and cannot pass the new profile check.

## Evidence and remaining live proof

Focused hermetic tests exercise rejection-before-execution, serialized lane
execution, affinity isolation, uncertain-request replay denial, all seven model
roles/defaults, empty candidate OAuth storage, overlay separation and local model
validation. Existing remote routing regression passes. The candidate overlay
compiles against runtime. These checks do **not** prove interactive entitlement,
real refresh/revocation, per-lane nspawn socket mapping or live provider streaming.
The separate deployment checkpoint owns that bounded live proof. No credentials
or host service were accessed during this implementation.

## Operator workflow

Use reviewed absolute paths for `CONFIG`, `WORKTREE`, `BROKER`, `STATE` and
`LANE_SOCKET`; these are non-secret operator selections, not literal defaults.
Provision each socket parent as root-owned mode 0700. Build the broker from the
same reviewed source and initialize a **new** state directory once. Never point
it at host Swarm storage.

```sh
"$BROKER" -action init -state "$STATE"
"$BROKER" -action login -state "$STATE"
"$BROKER" -action status -state "$STATE"
"$BROKER" -action serve -state "$STATE" -sockets "$LANE_SOCKET"
```

Login requires the user to approve the displayed device authorization in their
browser. Run serve under an explicitly managed dedicated process; do not share
its writable credential store with candidates. Status takes the owner lock, so
run it before serve, not concurrently. Stop only this broker to renew login.
Keep its executable outside disposable command scratch.

After required gates and an explicitly authorized clean commit, acquire a lane:

```sh
sudo python3 -B scripts/local_testbench_codex.py deploy --env-file "$CONFIG" --worktree "$WORKTREE" --socket "$LANE_SOCKET"
```

Save the exact returned generation in `GENERATION`. Inspect or run a scenario:

```sh
sudo python3 -B scripts/local_testbench_codex.py check --env-file "$CONFIG" --worktree "$WORKTREE" --generation "$GENERATION"
sudo python3 -B scripts/local_testbench_codex.py run --env-file "$CONFIG" --worktree "$WORKTREE" --generation "$GENERATION" -- node scripts/runners/SCENARIO.mjs
sudo bash scripts/testbench-local-deploy.sh status --env-file "$CONFIG" --worktree "$WORKTREE" --generation "$GENERATION"
sudo bash scripts/testbench-local-deploy.sh stop --env-file "$CONFIG" --worktree "$WORKTREE" --generation "$GENERATION"
```

Choose a maintained scenario with an endpoint-aware contract; `SCENARIO.mjs` is
not a supplied default. The wrapper supplies loopback endpoints, renews the lane
and drops runner privileges. Another chat must use its own clean worktree and
private broker socket, acquire its own generation, and never stop yours. Full
capacity fails explicitly; do not evict another lane. Configure slot and total
CPU/memory/disk budgets before pool initialization as described in
[local-testbench-pool.md](local-testbench-pool.md). `pool-status` observes occupancy;
`reap` reconciles expired owned resources, while `supervise` performs periodic
bounded sweeps. Do not change initialized pool configuration in place.

Final proof preflight: the fast critical gate, 31 focused Python tests, broker
and bridge Go tests, remote routing regression and atlas gate passed. The prior
unauthenticated lane was observed inactive after lease expiry. A new dedicated
broker store now reports `dedicated_login_configured=true` after user device
approval. This establishes login presence only; authenticated guest deployment
and Luna smoke proof remain required. No successful live provider request is
claimed by login status.
