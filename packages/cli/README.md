# @swarm/cli — unpublished Linux headless candidate

A thin Node 22+ launcher for a **local** Docker or rootless Podman engine. Installs
are inert: no lifecycle scripts, daemon downloads, image pulls or container starts.
The executable is `swarm-headless`, not the native `swarm` launcher. Only Linux
amd64 container images are supported. Docker must use its local Unix socket;
remote contexts/connections are rejected. Rootless Podman is explicitly local.

Package names are candidate identities, not a claim of npm scope ownership.
Nothing here authorizes registry publication. GitHub review/merge and separate
publication approval precede registry delivery.

## Image pin

`runtime-image.json` is the packaged authority. Release packaging supplies `version`
(semver), `distribution: "ghcr"`, `reference` (exactly
`ghcr.io/swarm-agent/swarm-headless@sha256:<manifest>`), `manifestDigest`, `imageId`
(the config digest), `platform: "linux/amd64"` and `sourceCommit` (40 hex characters).
`swarm-headless install` explicitly pulls that reference for Linux amd64 and verifies
RepoDigests, config ID and platform. It creates no containers, networks or state
volumes. Failed pulls/verification stop without tag or local-image fallback; an
engine may retain downloaded image layers. Retry explicitly after resolving errors.

Start and setup reverify the local immutable image before running anything. Start
uses the verified config ID with `--pull=never`; neither operation downloads images.
Help/version never access the engine. No install marker or second pin authority exists.

The checked-in metadata remains explicitly `local-candidate`, **not a published
registry digest**. In that mode install only verifies an already built/loaded image;
it never pulls the candidate tag. Release packaging must replace metadata using real
qualified registry provenance and repeat package/live checks before publication.

To transfer the unpublished image, on the build host (outside source directories):

<copy>
docker save --output "$IMAGE_ARCHIVE" localhost/swarm-headless:0.1.0-headless.1
# On the intended isolated runtime host:
docker load --input "$IMAGE_ARCHIVE"
</copy>

For Podman use `podman save --format docker-archive` and `podman load`. Verify the
loaded image ID against `runtime-image.json`; the launcher fails if it is absent.

## Install and explicit lifecycle

Install the local tarball from a clean consumer directory; no Swarm checkout is
needed by the installed launcher. Set `CLI_TARBALL` to its absolute path.

<copy>
npm install "$CLI_TARBALL"
npx --no-install swarm-headless --help
npx --no-install swarm-headless install
npx --no-install swarm-headless start --project "$PROJECT_DIR"
npx --no-install swarm-headless status
npx --no-install swarm-headless stop
npx --no-install swarm-headless start --project "$PROJECT_DIR" --recreate
</copy>

Set `PROJECT_DIR` to one clean, committed, ordinary Git checkout (not a linked
worktree), readable/writable by image UID/GID 10001. The launcher rejects the home
directory, its ancestors, root and comma/newline mount injection. Symlinks resolve
to the selected canonical directory. It never recursively changes ownership.
Rootless engines remap IDs: prepare the selected project for that mapping first.

Use `--engine podman` on **every** operation for rootless Podman. Use the same
`--name example` on lifecycle/setup operations for a separate instance (`install`
is image-only and accepts only `--engine`). `--port` is start-only
(default 7783, host binding always `127.0.0.1`). The four state volumes are
`NAME-config`, `NAME-data`, `NAME-cache`, `NAME-logs`; the dedicated network is
`NAME-network`. No home, engine socket, privileged mode or host network is mounted.
Keep that network free of untrusted peers. Local engine authority is administrative.

`stop` retains the container and all volumes. `start --recreate` requires a stopped,
launcher-labelled instance and the same project; it removes only that container,
without `--force` or `--volumes`, then attaches the original named state. Missing
state volumes, foreign name collisions and failed inspections fail closed. A failed
replacement may leave a stopped container removed, but state volumes remain; inspect
the engine before retrying. Never attach the same data volume to concurrent daemons.
The launcher does not delete data or perform automatic recovery/polling. To upgrade,
stop with the old package, install the new version's pinned image, then use
`start --recreate` with the same name/project. Back up durable state beforehand;
image verification does not establish database downgrade compatibility.

## Setup (explicit choices only)

`setup` forwards only the existing `swarmctl setup` operations through container
exec. No host settings or model assignments are changed. No setup step is automatic.
Set `PROVIDER`, `MODEL`, `THINKING` explicitly and choose a private existing key file
outside the project. Do not enable shell tracing or put keys in command arguments.

<copy>
npx --no-install swarm-headless setup -- status
npx --no-install swarm-headless setup -- identity --username owner --name Headless
npx --no-install swarm-headless setup -- credential --provider "$PROVIDER" --api-key-stdin < "$PROVIDER_KEY_FILE"
for role in action plan compact finder coder designer router; do
  npx --no-install swarm-headless setup -- model --role "$role" --provider "$PROVIDER" --model "$MODEL" --thinking "$THINKING" || break
done
npx --no-install swarm-headless setup -- workspace --path /project --name Project
npx --no-install swarm-headless setup -- complete
</copy>

Execute steps individually and stop on any failure. The model loop explicitly
chooses the same model for all roles; use individual calls for different choices.
A saved credential is not proof of provider validity. Credentials are stdin-only;
the launcher refuses terminal key input and never allocates a TTY. `setup complete`
checks canonical prerequisites. `setup status` can be retried explicitly after the
private socket is ready; there is no TCP setup fallback.

To export a one-hour scoped SDK token, select a **new**, absolute token file in a
private directory outside the project:

<copy>
(umask 077; set -C; npx --no-install swarm-headless setup -- sdk-token --expires-in-seconds 3600 > "$SWARM_SDK_TOKEN_FILE")
# Revoke using the non-secret ID from the export, never the token itself:
npx --no-install swarm-headless setup -- revoke-sdk-token --id "$TOKEN_ID"
</copy>

Token export refuses terminal output; protect the plaintext file with mode 0600.
A failed export may already have minted a token: inspect/revoke before retrying.
Use the SDK's packaged `examples/headless-session.ts` with the loopback endpoint.
Scoped session write access is powerful and can approve tools; it is not a sandbox.

## Validation limits

`npm test` exercises command construction, negative paths and state-preserving
operation ordering against an injected engine. `tests/scripts/headless_npm_test.mjs`
builds the SDK outside the checkout, inspects packs, installs with normal lifecycle
behavior into an empty consumer and checks ESM/types/CLI commands. Neither starts
Swarm. Real setup, networking, permissions and restart persistence require the
separate isolated live qualification step.
