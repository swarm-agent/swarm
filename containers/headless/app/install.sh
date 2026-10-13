#!/usr/bin/env bash
# One-line installer for Swarm on this machine: headless, with its setup page.
#
# On Ubuntu 24.04 x86_64 (a server or your own PC), from your own account:
#   curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/main/containers/headless/app/install.sh \
#     | sudo bash
#
# It installs Docker (unless a working one with buildx is already there) and
# gVisor, builds Swarm and the setup page from source and runs both as
# services. Everything listens on 127.0.0.1 only: the setup page on
# http://127.0.0.1:8443 (owner login, provider sign-in, models, workspace) and
# Swarm's MCP (Swarm Control) on http://127.0.0.1:7783/mcp, which answers only
# to AI keys (skip it with --no-ai-access). At the end it creates Swarm's owner
# for the person who ran it with sudo (or --for-user NAME), puts an AI key for
# their AI in a file only they can read (`install.sh connect-ai` does this
# later, or again) and prints the lines that connect Claude Code or Codex on
# this machine. Provider keys can also be set by script (`install.sh key
# PROVIDER`, the key on stdin). Nothing is opened in the firewall and no secret
# is passed to this script on its command line.
#
# Tailscale is optional, only for reaching this machine from other devices:
# --tailnet (or later `install.sh tailnet on`) installs Tailscale, joins your
# tailnet (one login link, or TS_AUTHKEY from the environment, passed to
# Tailscale through a private file; --tag TAG joins as that tag), admits only
# the tailnet inbound with ufw, serves the setup page at
# https://NAME.TAILNET.ts.net and the MCP at https://NAME.TAILNET.ts.net:8444/mcp
# for AI keys and for tailnet devices your Tailscale policy grants
# swarmagent.dev/cap/swarm (see containers/headless/app/TAILNET.md).
#
# Swarm runs as a system service under its own "swarm" user and keeps its keys,
# login and local socket to itself. Agents' commands, and Swarm's Git on their
# projects, run in one sandbox per project: a container (under gVisor when it
# works on this machine) with no capabilities, Swarm's non-root user, memory
# and process limits, only that project and its worktrees mounted, and a
# network that reaches the internet but never this server, your tailnet,
# private networks or cloud metadata. Agents run without permission prompts
# (so unattended jobs never wait on a person) only while that sandbox is
# active: Swarm runs with --sandbox=required and fails closed without it;
# --ask-permissions (or `install.sh permissions ask`) keeps the prompts. The
# secret gateway is on by default (--secrets-gateway off turns it off).
#
# The first visit to the setup page creates your login (username, password
# and an optional authenticator code).
# Later: `install.sh update` (also run by a 5-minute timer) rebuilds when the
# branch moves forward; `install.sh channel BRANCH` switches the branch it
# follows and keeps everything else; `install.sh reset-login` deletes the login
# so you can create a new one (Swarm itself and your work are kept).
# `install.sh daemon-env` keeps settings for Swarm itself (such as the X
# account its approved posts go out from) in a root-only file; values arrive
# on stdin and are never shown again.
# `install.sh reinstall` wipes Swarm's state and installs it fresh with the same
# settings; `install.sh uninstall` only removes it. Both keep Docker, gVisor,
# Tailscale, ufw and (unless --delete-projects) the project folder.
# Existing installs made before Tailscale was optional keep their tailnet
# setup on update (install.env records TAILNET=on for them).
set -euo pipefail

STATE=/var/lib/swarm-headless
SRC=$STATE/src
PROJECT=$STATE/project
CONF=$STATE/install.env
# Settings for Swarm itself (install.sh daemon-env), root-only; systemd reads it
# when it starts the daemon.
DAEMON_ENV=$STATE/daemon.env
STAGE=$STATE/stage
OPT=/opt/swarm
UNITS=/usr/local/lib/systemd/system
SERVICE_USER=swarm
SERVICE_HOME=/var/lib/swarm
DAEMON_UNIT=swarm-headless.service
APP_UNIT=swarm-headless-app.service
FIREWALL_UNIT=swarm-sandbox-firewall.service
SANDBOX_IMAGE=swarm-sandbox:local
SANDBOX_NETWORK=swarm-sandbox
SANDBOX_SUBNET=172.31.251.0/24
SANDBOX_BRIDGE=br-swarm-sbx
# The secret gateway (when on) listens on the sandbox bridge address, this port.
SECRETS_GATEWAY_PORT=8080
DATA_DIRS=(/etc/swarmd /var/lib/swarmd /var/cache/swarmd /var/log/swarmd)
APP_CONFIG=/etc/swarmd/headless-app
GVISOR_LIST=/etc/apt/sources.list.d/gvisor.list
# This installer as kept in the checkout, for the commands it prints.
INSTALLER=$SRC/containers/headless/app/install.sh
SWARMCTL=/usr/local/bin/swarmctl
# Everything Swarm serves on this machine listens on loopback.
LOCAL_APP_ORIGIN=http://127.0.0.1:8443
LOCAL_MCP_URL=http://127.0.0.1:7783/mcp
# Revocation IDs of the AI keys connect-ai gave each person (root-only).
AI_KEY_IDS=$STATE/ai-keys
# AI keys from connect-ai last a year; run it again to replace one.
AI_KEY_SECONDS=31536000

# The earlier layout ran Swarm itself inside a container; reinstall/uninstall
# remove what it left behind.
LEGACY_CONTAINER=swarm
LEGACY_NETWORK=swarm-net
LEGACY_BRIDGE=br-swarm

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
Usage: install.sh [install] [--for-user NAME] [--relay URL] [--name NAME] [--ref BRANCH] [--no-ai-access]
                  [--secrets-gateway on|off] [--ask-permissions] [--tailnet [--tag TAG] [--lock-ssh]]
       install.sh reinstall [--yes] [--delete-projects] [install options]
       install.sh uninstall [--yes] [--delete-projects]
       install.sh update | up | reset-login | status | check-isolation | check-sandbox
       install.sh dev-sync /path/to/checkout | off
       install.sh channel [BRANCH]
       install.sh connect-ai [--for-user NAME] [--access write|full]
       install.sh key PROVIDER                      (the API key on stdin)
       install.sh secret set NAME [--hosts HOST,...] (the value on stdin)
       install.sh secret grant NAME FOLDER --days N | --hours N
       install.sh daemon-env list | set NAME... | unset NAME...
       install.sh tailnet on [--tag TAG] [--lock-ssh] | off
       install.sh tailnet-identity on|off
       install.sh secrets-gateway on|off
       install.sh permissions auto|ask
       install.sh workspace add PATH [--for-user NAME] | remove PATH | list
       install.sh workspace publish PATH COMMAND [--auto] | publish PATH --off

  --for-user NAME  the person this install is for (default: the account that
                ran sudo); used for the AI connection and workspace folders
  --relay URL   your Swarm Control relay (prefills "Connect to Claude")
  --name NAME   Swarm machine name, and tailnet machine name (default: swarm)
  --ref BRANCH  swarm branch to build (default: main, the released build;
                dev follows development and changes several times a day)
  --no-ai-access  do not run Swarm's MCP (127.0.0.1:7783, and :8444 on the
                tailnet); connect-ai then has nothing to connect to
  --secrets-gateway on|off
                inject granted secrets through the egress gateway (default on;
                see secrets-gateway below)
  --ask-permissions
                agents ask before commands and file changes; by default they
                run without prompts, which Swarm allows only inside the sandbox
  --tailnet     also reach this machine from your other devices over
                Tailscale (installs and joins it; TS_AUTHKEY in the
                environment joins without a login link)
  --tag TAG     with --tailnet: join as this tag, e.g. tag:swarm (the tailnet
                policy must list it under tagOwners); first join only
  --lock-ssh    with --tailnet: also close public SSH (use after
                `tailscale ssh` works)

  connect-ai       create Swarm's owner for the person if there is none, then
                   mint an AI key for them (write by default; full also
                   approves tool calls and sets models, providers and
                   workers), store it in
                   ~/.config/swarm/ai-key (that person only, mode 0600; the
                   value is never printed) and print the Claude Code and Codex
                   connect lines. Running it again replaces the key.
  key PROVIDER     set the first provider API key, read from stdin (or typed
                   at a hidden prompt); never on a command line
  secret set NAME  create the secret for the websites in --hosts (when given)
                   and set its value from stdin (or a hidden prompt)
  secret grant NAME FOLDER
                   let the agents of that workspace folder use the secret for
                   --days and/or --hours (needs secrets-gateway on)
  tailnet on|off   add Tailscale later (as --tailnet), or stop serving on the
                   tailnet and go back to loopback only (Tailscale and ufw
                   stay installed)
  permissions auto|ask
                   auto: agents run without prompts while the sandbox is
                   active (the default); ask: they ask first. Applied at
                   every start of Swarm.

  reinstall / uninstall remove Swarm's services, binaries, sandboxes and state
  (owner, provider sign-in, AI keys, relay pairing, agent worktrees), its
  update timer and its Tailscale Serve entries. reinstall then installs
  again, reusing the saved --relay, --name and --ref unless you pass new ones.
  --yes              do not ask for confirmation
  --delete-projects  also delete the project folder (your workspaces)

  channel [BRANCH] show the branch this machine follows, or switch to BRANCH
                   (main or dev) without reinstalling; the next update builds
                   it once it is ahead of the running build
  daemon-env       settings for Swarm itself, kept in a root-only file and
                   applied with one restart: `list` shows names only,
                   `set NAME...` reads one value per name from stdin,
                   `unset NAME...` removes them
  check-isolation  prove from inside a sandbox that this server, the tailnet,
                   private networks and cloud metadata are unreachable
  check-sandbox    show each running agent sandbox and its hardening
  tailnet-identity on|off
                   let the AI gateway admit tailnet devices by the
                   swarmagent.dev/cap/swarm grant in your Tailscale policy
                   (needs Tailscale 1.92+; on by default for new installs)
  secrets-gateway on|off
                   inject secrets granted to a project (swarmctl secret) on
                   the way out to their allowed websites; agents only see
                   stand-ins (on by default)
  workspace add PATH [--for-user NAME] | remove PATH | list
                   make a folder the person owns (a Git repository or not)
                   visible and writable to
                   Swarm and its agent sandboxes at its real path (a unit
                   drop-in binds exactly that folder; Swarm's user gets
                   access to it; the owner keeps full rights on what Swarm
                   creates), then restart Swarm. The person or the AI setting
                   the machine up runs this as root; Swarm never changes the
                   machine itself. Then register the folder in place with
                   swarm_add_workspace. remove reverses it.
  workspace publish PATH COMMAND [--auto]
                   deliver jobs in PATH as pull requests instead of merging
                   them into the folder: Swarm runs COMMAND (root-owned,
                   outside home folders) on this machine with the job in
                   SWARM_PUBLISH_FOLDER, _JOB_ID, _BRANCH, _HEAD, _BASE_BRANCH,
                   _TITLE and _BODY_FILE; its last output line is the pull
                   request URL. --auto delivers each finished job with commits
                   without asking. publish PATH --off goes back to merging.
USAGE
}

load_conf() {
  [[ -f $CONF ]] || die "Swarm is not installed on this machine ($CONF is missing)"
  # shellcheck disable=SC1090
  . "$CONF"
}

# Replace one setting in install.env.
conf_set() {
  sed -i "/^$1=/d" "$CONF" && printf '%s=%s\n' "$1" "$2" >>"$CONF"
}

# TAILNET from install.env. Installs from before Tailscale was optional did
# not record it; they all served on the tailnet and recorded its origin.
saved_tailnet() {
  # shellcheck disable=SC1090
  (
    . "$CONF"
    if [[ -n ${TAILNET:-} ]]; then echo "$TAILNET"
    elif [[ ${APP_ORIGIN:-} == https://*.ts.net ]]; then echo on
    else echo off; fi
  )
}

# Settings added after the first installs: write them for an install that
# predates them, from what that install already recorded, and say so.
conf_migrate() {
  if [[ -z ${TAILNET:-} ]]; then
    TAILNET=$(saved_tailnet)
    conf_set TAILNET "$TAILNET"
    echo "swarm: recorded TAILNET=$TAILNET for this install"
  fi
  if [[ -z ${PERMISSIONS:-} ]]; then
    PERMISSIONS=auto
    conf_set PERMISSIONS auto
    echo "swarm: agents now run without permission prompts while the sandbox is active (install.sh permissions ask turns them back on)"
  fi
}

# The person an install, AI key or workspace is for: --for-user NAME, else the
# account that ran sudo. Never root, Swarm's own user or a system account.
person() {
  local name=${1:-${SUDO_USER:-}}
  [[ -n $name ]] || die "cannot tell who this is for: run with sudo from your own account, or pass --for-user NAME"
  [[ $name =~ ^[a-z_][a-z0-9_.-]{0,31}$ ]] || die "--for-user must be a user name"
  id -u "$name" >/dev/null 2>&1 || die "there is no user $name on this machine"
  [[ $name != root && $name != "$SERVICE_USER" ]] || die "$name is not a person's account; pass --for-user NAME"
  local uid
  uid=$(id -u "$name")
  [[ $uid -ge 1000 && $uid -ne 65534 ]] || die "$name is a system account; pass --for-user NAME"
  printf '%s' "$name"
}

person_home() {
  local home
  home=$(getent passwd "$1" | cut -d: -f6)
  [[ $home == /* && -d $home ]] || die "$1 has no home folder"
  printf '%s' "$home"
}

# Swarm must be running and have its owner (made by connect-ai, the setup page
# or swarmctl setup identity) before keys can be set.
require_owner() {
  systemctl is-active -q "$DAEMON_UNIT" || die "Swarm is not running; check: journalctl -u $DAEMON_UNIT"
  local status
  status=$("$SWARMCTL" setup status) || die "could not read Swarm's setup status"
  [[ $status == *identity=true* ]] ||
    die "Swarm has no owner yet: run install.sh connect-ai (it creates the owner for you) or open the setup page ($APP_ORIGIN), then run this again"
}

# Read one secret into the named variable: from a hidden prompt on a terminal,
# else all of stdin. It never reaches a command line or the screen.
read_secret() {
  local -n into=$1
  if [[ -t 0 ]]; then
    printf '%s (it is not shown), then Enter: ' "$2" >&2
    IFS= read -rs into || true
    echo >&2
  else
    into=$(cat)
  fi
  [[ -n $into ]] || die "nothing was entered"
}

# The port the sandbox firewall opens to the secret gateway, or nothing when the
# gateway is off.
gateway_port() {
  local on=${SECRETS_GATEWAY:-}
  [[ -z $on && -f $CONF ]] && on=$(. "$CONF" && printf "%s" "${SECRETS_GATEWAY:-off}")
  [[ $on == on ]] && printf "%s" "$SECRETS_GATEWAY_PORT"
  return 0
}

# Sandbox network isolation (idempotent; also run at boot by
# swarm-sandbox-firewall.service, since iptables rules do not survive reboot).
firewall() {
  SWARM_SANDBOX_GATEWAY_PORT=$(gateway_port) \
  SWARM_SANDBOX_NETWORK=$SANDBOX_NETWORK SWARM_SANDBOX_BRIDGE=$SANDBOX_BRIDGE SWARM_SANDBOX_SUBNET=$SANDBOX_SUBNET \
    bash "$SRC/containers/sandbox/firewall.sh"
}

# Flags every probe container shares with a real sandbox (see
# swarmd/internal/sandbox RunArgs), so a probe that works proves the runtime
# and the network rules a sandbox gets.
sandbox_probe() {
  local runtime=${SANDBOX_RUNTIME:-} uid gid
  uid=$(id -u "$SERVICE_USER") gid=$(id -g "$SERVICE_USER")
  local args=(run --rm --init --user "$uid:$gid" --cap-drop ALL --security-opt no-new-privileges
    --memory 1g --pids-limit 256 --network "$SANDBOX_NETWORK" --dns 1.1.1.1 --dns 9.9.9.9
    --tmpfs "/run/swarm:rw,nosuid,nodev,noexec,size=16m,uid=$uid,gid=$gid,mode=0700"
    --tmpfs /tmp:rw,nosuid,nodev,exec,size=64m,mode=1777)
  [[ -n $runtime && $runtime != default ]] && args+=(--runtime "$runtime")
  docker "${args[@]}" "$SANDBOX_IMAGE" "$@"
}

# Pick gVisor when it runs a sandbox correctly on this machine; otherwise the
# engine's default runtime with the same hardening.
choose_runtime() {
  SANDBOX_RUNTIME=default
  if docker info --format '{{json .Runtimes}}' 2>/dev/null | grep -q '"runsc"'; then
    if SANDBOX_RUNTIME=runsc sandbox_probe bash -c 'test -w /run/swarm && git --version >/dev/null && id -u' >/dev/null 2>&1; then
      SANDBOX_RUNTIME=runsc
    else
      echo "warning: gVisor (runsc) is installed but could not run a sandbox here; using Docker's default runtime" >&2
    fi
  fi
  echo "Sandbox runtime: $([[ $SANDBOX_RUNTIME == runsc ]] && echo 'gVisor (runsc)' || echo 'Docker default (runc)')"
}

# Prove the isolation from inside a sandbox: this server, the tailnet, private
# ranges and cloud metadata must be unreachable; the internet reachable.
check_isolation() {
  load_conf
  docker image inspect "$SANDBOX_IMAGE" >/dev/null 2>&1 || die "the sandbox image is not built"
  local host_ts host_ip gateway secrets target name want got failed=0
  host_ts=$(tailscale ip -4 2>/dev/null | head -n1 || true)
  host_ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' || true)
  gateway=${SANDBOX_SUBNET%.*}.1
  secrets=$(gateway_port)
  printf '%-40s %-10s %s\n' "From inside an agent sandbox to" "expected" "result"
  for target in \
    ${secrets:+"$gateway:$secrets|the secret gateway|reachable"} \
    "${host_ts:-100.100.100.100}:443|this server's tailnet address|blocked" \
    "100.100.100.100:53|Tailscale DNS|blocked" \
    "169.254.169.254:80|cloud metadata|blocked" \
    "$gateway:22|this server via the sandbox bridge|blocked" \
    "$gateway:8443|the setup app's port|blocked" \
    "${host_ip:-$gateway}:22|this server's own address|blocked" \
    "10.0.0.1:22|a private network|blocked" \
    "1.1.1.1:443|the internet|reachable"; do
    IFS='|' read -r target name want <<<"$target"
    if sandbox_probe timeout 4 bash -c "exec 3<>/dev/tcp/${target%:*}/${target#*:}" >/dev/null 2>&1; then got=reachable; else got=blocked; fi
    [[ $got == "$want" ]] || failed=1
    printf '%-40s %-10s %s\n' "$name ($target)" "$want" "$got$([[ $got == "$want" ]] || echo '  <-- NOT AS EXPECTED')"
  done
  ((failed == 0)) || die "isolation check failed"
  echo "Isolation is as expected."
}

# Show each agent sandbox as Docker sees it, for checking from outside.
check_sandbox() {
  local ids
  ids=$(docker ps -q --filter label=swarm.sandbox=1)
  if [[ -z $ids ]]; then
    echo "No agent sandbox is running (one starts when an agent runs a command)."
    return 0
  fi
  # shellcheck disable=SC2086
  docker inspect $ids --format '{{index .Config.Labels "swarm.sandbox.root"}}
  container: {{.Name}}  runtime: {{.HostConfig.Runtime}}  user: {{.Config.User}}
  privileged: {{.HostConfig.Privileged}}  cap-drop: {{.HostConfig.CapDrop}}  cap-add: {{.HostConfig.CapAdd}}
  security-opt: {{.HostConfig.SecurityOpt}}  pids-limit: {{.HostConfig.PidsLimit}}  memory: {{.HostConfig.Memory}}
  network: {{.HostConfig.NetworkMode}}
  mounts:{{range .Mounts}} {{.Type}}:{{.Source}}{{end}}
'
}

# Build Swarm and the app from the checkout, install them under $OPT,
# build the sandbox image and (re)start the services. Restarting stops any
# agent run in progress.
up() {
  load_conf
  local commit release
  commit=$(git -C "$SRC" rev-parse HEAD)
  release=$OPT/releases/$commit
  say "Building Swarm ${SWARM_REF}@${commit:0:9} (the first build takes several minutes)"
  rm -rf "$STAGE"
  docker build --platform linux/amd64 -f "$SRC/containers/headless/app/Dockerfile.host" \
    --build-arg VERSION="$SWARM_REF" --build-arg COMMIT="$commit" \
    --build-arg BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o "type=local,dest=$STAGE" "$SRC"
  docker build --platform linux/amd64 -t "$SANDBOX_IMAGE" "$SRC/containers/sandbox"
  install -d -m 0755 "$OPT/releases"
  rm -rf "$release.new" && mv "$STAGE" "$release.new"
  # The exporter creates its top directory 0700; Swarm runs as its own user.
  chmod -R u=rwX,go=rX "$release.new"
  rm -rf "$release" && mv "$release.new" "$release"
  ln -sfn "$release" "$OPT/current.new" && mv -T "$OPT/current.new" "$OPT/current"
  # Keep the running release and the one before it.
  find "$OPT/releases" -mindepth 1 -maxdepth 1 -type d ! -path "$release" -printf '%T@ %p\n' 2>/dev/null |
    sort -rn | tail -n +2 | cut -d' ' -f2- | xargs -r rm -rf
  install -m 0755 /dev/stdin "$SWARMCTL" <<WRAPPER
#!/bin/sh
# swarmctl talks to Swarm's private socket, so it runs as Swarm's own user.
if [ "\$(id -un)" = $SERVICE_USER ]; then exec $OPT/current/bin/swarmctl "\$@"; fi
exec runuser -u $SERVICE_USER -- env HOME=$SERVICE_HOME LD_LIBRARY_PATH=$OPT/current/lib $OPT/current/bin/swarmctl "\$@"
WRAPPER

  firewall
  register_gvisor
  choose_runtime
  sed -i '/^SANDBOX_RUNTIME=/d' "$CONF" && echo "SANDBOX_RUNTIME=$SANDBOX_RUNTIME" >>"$CONF"
  install_units
  systemctl restart "$DAEMON_UNIT"
  systemctl restart "$APP_UNIT"
  echo "Swarm is running: ${SWARM_REF}@${commit:0:9}"
}

# Development: build the committed HEAD of a checkout on this machine instead
# of the branch on GitHub, so a change can be tried without pushing it. The
# machine stays on that checkout (the update timer leaves it alone) until
# `dev-sync off` returns it to the branch.
dev_sync() {
  load_conf
  local repo=${1:?usage: install.sh dev-sync /path/to/checkout | off}
  if [[ $repo == off ]]; then
    sed -i '/^DEV_SOURCE=/d' "$CONF"
    echo "Following $SWARM_REF on GitHub again; the next update brings it back."
    return 0
  fi
  repo=$(realpath "$repo")
  [[ -d $repo/.git && -f $repo/containers/headless/app/install.sh ]] || die "$repo is not a Swarm checkout"
  # The checkout belongs to another user; trust it for this one fetch only.
  git -C "$SRC" -c safe.directory="$repo" fetch -q "$repo" HEAD || die "could not read $repo"
  sed -i '/^DEV_SOURCE=/d' "$CONF" && echo "DEV_SOURCE=$repo" >>"$CONF"
  git -C "$SRC" reset -q --hard FETCH_HEAD
  echo "swarm: building $(git -C "$SRC" rev-parse --short HEAD) from $repo"
  exec bash "$SRC/containers/headless/app/install.sh" apply
}

# Pull-based updates: rebuild only when the branch moved.
update() {
  load_conf
  if [[ -n ${DEV_SOURCE:-} ]]; then
    echo "swarm: built from $DEV_SOURCE; not following $SWARM_REF (install.sh dev-sync off to resume)"
    return 0
  fi
  git -C "$SRC" fetch -q origin "$SWARM_REF"
  local old new
  old=$(git -C "$SRC" rev-parse HEAD)
  new=$(git -C "$SRC" rev-parse FETCH_HEAD)
  [[ $old == "$new" ]] && return 0
  # Only move forward: a branch that does not contain the running build (after
  # a channel switch to an older branch, or a rewritten branch) would be a
  # downgrade, which Swarm's stored state may not survive. Wait until it does.
  # A release merge (main) that added nothing to the branch it merged is not a
  # downgrade when that branch's merged commit is in the new build: this is a
  # switch from main to dev.
  if ! git -C "$SRC" merge-base --is-ancestor "$old" "$new" &&
    ! { git -C "$SRC" rev-parse -q --verify "$old^2" >/dev/null &&
      git -C "$SRC" merge-base --is-ancestor "$old^2" "$new" &&
      git -C "$SRC" diff --quiet "$old^2" "$old"; }; then
    echo "swarm: $SWARM_REF (${new:0:9}) does not contain the running build ${old:0:9}; waiting until it does"
    return 0
  fi
  git -C "$SRC" reset -q --hard "$new"
  echo "swarm: ${old:0:9} -> ${new:0:9}"
  # Continue in the installer just fetched, not this already-loaded copy, so
  # changes to the installer itself (units, firewall, run flags) apply now.
  exec bash "$SRC/containers/headless/app/install.sh" apply
}

# The branch this machine follows. Switching keeps the install, its state and
# the running build; the next update (within 5 minutes) builds the new branch
# once it contains the running build, so a switch never downgrades.
channel() {
  load_conf
  local ref=${1:-}
  if [[ -z $ref ]]; then
    echo "Following ${SWARM_REF:-main}${DEV_SOURCE:+ (built from $DEV_SOURCE for now; install.sh dev-sync off to resume)}; running $(git -C "$SRC" rev-parse --short HEAD)"
    return 0
  fi
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  [[ $ref =~ ^[A-Za-z0-9._/-]+$ ]] || die "the branch must be a branch name such as main or dev"
  git -C "$SRC" ls-remote --exit-code --heads origin "$ref" >/dev/null || die "the Swarm repository has no branch $ref"
  sed -i '/^SWARM_REF=/d' "$CONF" && echo "SWARM_REF=$ref" >>"$CONF"
  echo "Following $ref; the next update builds it once it contains the running build $(git -C "$SRC" rev-parse --short HEAD)."
}

# Settings for Swarm itself, such as TWITTER_* for approved X posts. One
# NAME=value per line in a root-only file that systemd reads when it starts the
# daemon. Values come on stdin, one line per name, so they never appear in a
# process list or shell history, and they are never printed.
daemon_env() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  local action=${1:-list} name value line keep
  shift || true
  if [[ $action == list ]]; then
    [[ -f $DAEMON_ENV ]] && sed -n 's/^\([A-Z][A-Z0-9_]*\)=.*/\1/p' "$DAEMON_ENV"
    return 0
  fi
  [[ ($action == set || $action == unset) && $# -gt 0 ]] || die "usage: install.sh daemon-env list | set NAME... | unset NAME..."
  for name; do
    [[ $name =~ ^[A-Z][A-Z0-9_]{1,63}$ ]] || die "the name must be capital letters, digits and _"
  done
  install -m 0600 -o root -g root /dev/null "$DAEMON_ENV.new"
  if [[ -f $DAEMON_ENV ]]; then
    while IFS= read -r line; do
      keep=1
      for name; do [[ ${line%%=*} == "$name" ]] && keep=0; done
      ((keep)) && printf '%s\n' "$line" >>"$DAEMON_ENV.new"
    done <"$DAEMON_ENV"
  fi
  if [[ $action == set ]]; then
    for name; do
      IFS= read -r value || true
      # systemd reads the file without shell quoting; keep values to characters
      # that need none (API keys and tokens fit).
      [[ $value =~ ^[A-Za-z0-9._~+/=:@-]{1,2048}$ ]] || { rm -f "$DAEMON_ENV.new"; die "the value for $name must be one line of letters, digits and ._~+/=:@-"; }
      printf '%s=%s\n' "$name" "$value" >>"$DAEMON_ENV.new"
    done
  fi
  mv -f "$DAEMON_ENV.new" "$DAEMON_ENV"
  # Older installs have no EnvironmentFile line in the unit yet.
  grep -q '^EnvironmentFile=-' "$UNITS/$DAEMON_UNIT" || install_units
  systemctl daemon-reload
  systemctl restart "$DAEMON_UNIT"
  wait_ready
  if [[ $action == set ]]; then echo "Saved $*; Swarm restarted."; else echo "Removed $*; Swarm restarted."; fi
}

install_units() {
  load_conf
  conf_migrate
  local sdk_args='' runtime_arg='' secrets_arg='' permissions_arg ai_url='' ai_ip='' release
  # The app starts only when run as the main module, which Node decides on
  # the symlink-resolved path, so its unit names the release itself.
  release=$(readlink -f "$OPT/current")
  # Swarm's MCP for AI keys, on loopback (and on the tailnet through Serve).
  [[ ${AI_ACCESS:-off} == on ]] && sdk_args='--container-sdk-port=7783 --container-sdk-host=127.0.0.1'
  [[ ${AI_ACCESS:-off} == on && ${TAILNET:-off} == on && ${TAILNET_IDENTITY:-off} == on ]] && sdk_args+=' --tailnet-identity'
  [[ ${AI_ACCESS:-off} == on && ${TAILNET:-off} == on ]] && ai_url=${APP_AI_URL:-} ai_ip=${APP_AI_IP:-}
  [[ ${SANDBOX_RUNTIME:-default} == default ]] && runtime_arg='--sandbox-runtime=runc'
  [[ ${SECRETS_GATEWAY:-off} == on ]] && secrets_arg=--secrets-gateway=on
  # Unattended jobs never wait on a person: with --sandbox=required Swarm does
  # not start without its sandbox, and bypass applies only while it is active.
  # The installer's choice is applied at every start.
  case ${PERMISSIONS:-} in
    auto) permissions_arg=--bypass-permissions ;;
    ask) permissions_arg=--bypass-permissions=false ;;
    *) die "install.env has no valid PERMISSIONS (auto or ask); run install.sh permissions auto|ask" ;;
  esac
  install -d "$UNITS"
  cat > "$UNITS/$FIREWALL_UNIT" <<UNIT
[Unit]
Description=Keep Swarm's agent sandboxes off this server, the tailnet and private networks
After=docker.service ufw.service
Requires=docker.service
PartOf=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
Environment=SWARM_SANDBOX_NETWORK=$SANDBOX_NETWORK SWARM_SANDBOX_BRIDGE=$SANDBOX_BRIDGE SWARM_SANDBOX_SUBNET=$SANDBOX_SUBNET SWARM_SANDBOX_GATEWAY_PORT=$(gateway_port)
ExecStart=/bin/bash $SRC/containers/sandbox/firewall.sh

[Install]
WantedBy=multi-user.target docker.service
UNIT
  # Swarm itself: its own user, its own private state directories, and Docker
  # access to start sandboxes. Nothing it holds is mounted into a sandbox.
  cat > "$UNITS/$DAEMON_UNIT" <<UNIT
[Unit]
Description=Swarm (headless, agents in sandboxes)
After=network-online.target docker.service $FIREWALL_UNIT
Wants=network-online.target
Requires=docker.service $FIREWALL_UNIT

[Service]
User=$SERVICE_USER
Group=$SERVICE_USER
SupplementaryGroups=docker
# 0007, not 0077: a sandbox for a person's folder runs as that person with
# Swarm's group, so what Swarm writes there must admit its group (Swarm's own
# state directories are 0700 and nobody else is in its group).
UMask=0007
Environment=HOME=$SERVICE_HOME SWARM_DISABLE_MINT_REPORT=1
# Folders whose jobs are delivered as pull requests (install.sh workspace publish).
Environment=SWARM_PUBLISH_CONFIG=$PUBLISH
Environment=LD_LIBRARY_PATH=$OPT/current/lib
Environment=PATH=$OPT/current/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
EnvironmentFile=-$DAEMON_ENV
StateDirectory=swarmd
StateDirectoryMode=0700
CacheDirectory=swarmd
CacheDirectoryMode=0700
RuntimeDirectory=swarmd
RuntimeDirectoryMode=0700
RuntimeDirectoryPreserve=yes
ConfigurationDirectory=swarmd
ConfigurationDirectoryMode=0700
LogsDirectory=swarmd
LogsDirectoryMode=0700
WorkingDirectory=$PROJECT
ExecStart=$OPT/current/bin/swarmd --desktop-port=0 --cwd=$PROJECT --sandbox=required $permissions_arg $runtime_arg $sdk_args $secrets_arg
Restart=on-failure
RestartSec=3
TimeoutStopSec=20
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
ReadWritePaths=$SERVICE_HOME $PROJECT

[Install]
WantedBy=multi-user.target
UNIT
  # The setup app talks to Swarm over its private socket and listens on
  # loopback only; with the tailnet, Tailscale Serve is its only other way in.
  cat > "$UNITS/$APP_UNIT" <<UNIT
[Unit]
Description=Swarm setup app (loopback only)
After=$DAEMON_UNIT
BindsTo=$DAEMON_UNIT

[Service]
User=$SERVICE_USER
Group=$SERVICE_USER
UMask=0077
Environment=HOME=$SERVICE_HOME SWARM_DISABLE_MINT_REPORT=1 APP_LISTEN=127.0.0.1 APP_PROJECT=$PROJECT
Environment=APP_ORIGIN=${APP_ORIGIN} APP_RELAY_URL=${APP_RELAY_URL} APP_DEVICE_NAME=${APP_DEVICE_NAME}
Environment=APP_AI_URL=$ai_url APP_AI_IP=$ai_ip
ExecStartPre=/bin/sh -c 'for i in \$(seq 1 60); do curl -fsS --unix-socket /var/lib/swarmd/local-transport/api.sock -o /dev/null http://swarmd/readyz && exit 0; sleep 1; done; exit 1'
ExecStart=$release/bin/node $release/app/examples/headless-app/server.mjs
WorkingDirectory=$release/app/examples/headless-app
Restart=on-failure
RestartSec=3
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ReadWritePaths=$APP_CONFIG $PROJECT

[Install]
WantedBy=multi-user.target
UNIT
  cat > "$UNITS/swarm-headless-update.service" <<UNIT
[Unit]
Description=Rebuild headless Swarm when its branch changes
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/bin/bash $SRC/containers/headless/app/install.sh update
UNIT
  cat > "$UNITS/swarm-headless-update.timer" <<'UNIT'
[Unit]
Description=Check for headless Swarm updates every 5 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
UNIT
  install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0700 "${DATA_DIRS[0]}" "$APP_CONFIG"
  systemctl daemon-reload
  systemctl enable "$FIREWALL_UNIT" "$DAEMON_UNIT" "$APP_UNIT" >/dev/null
  systemctl enable --now swarm-headless-update.timer >/dev/null
}

# ---- Workspaces: the person's own folders, made visible to Swarm -------------
# Swarm runs as its own user with every home folder hidden (ProtectHome=yes),
# so it cannot see a person's folders. `install.sh workspace add PATH`, run as
# root by the person or by the AI setting this machine up, makes exactly that
# folder (a Git repository or not) visible and writable to Swarm and its agent
# sandboxes at its real path: a unit drop-in binds the folder into the
# service, and Swarm's user gets access to that folder only (the owner keeps
# full rights on everything Swarm creates inside). Swarm itself never changes
# the machine; this is the installer's job, done from outside. Afterwards the
# folder is registered in place with swarm_add_workspace (or
# `swarmctl workspace add`); nothing is copied.
WORKSPACES=$STATE/workspaces
# Root-owned: FOLDER<TAB>COMMAND[<TAB>auto] per line; Swarm only reads it.
PUBLISH=$STATE/publish
WORKSPACES_DROPIN=$UNITS/$DAEMON_UNIT.d/50-workspaces.conf

# A folder Swarm may work in: absolute, no "..", an existing directory with no
# link in its path, owned by the person it is added for (never root's, Swarm's
# or a system account's), not a system or settings folder, not that person's
# whole home and not inside anyone else's home.
workspace_check() {
  local path=$1 who=$2 real owner home top sub other uid other_home dir
  [[ $path == /* ]] || die "path must be absolute"
  [[ $path != *..* && $path != *$'\n'* ]] || die "path must not contain .. or newlines"
  [[ -d $path ]] || die "$path is not a directory"
  real=$(realpath -e "$path") || die "$path cannot be resolved"
  [[ $real == "$path" ]] || die "$path goes through a link; use $real"
  [[ $path != / ]] || die "$path is a system folder"
  top=${path#/} top=${top%%/*}
  case $top in
    bin|boot|dev|etc|lib|lib32|lib64|libx32|proc|root|run|sbin|snap|sys|tmp|usr) die "$path is a system folder" ;;
  esac
  if [[ $top == var ]]; then
    sub=${path#/var} sub=${sub#/} sub=${sub%%/*}
    case $sub in
      ''|cache|lib|lock|log|mail|run|spool|tmp) die "$path is a system folder" ;;
    esac
  fi
  for dir in "${DATA_DIRS[@]}" "$SERVICE_HOME" "$STATE" "$OPT"; do
    [[ $path != "$dir" && $path != "$dir"/* ]] || die "$path is Swarm's own storage"
  done
  case /$path/ in
    */.*/*) die "$path is a settings or credentials folder" ;;
  esac
  owner=$(stat -c %U "$path")
  [[ $owner != root && $owner != "$SERVICE_USER" ]] || die "$path must belong to a person, not $owner"
  [[ $(id -u "$owner") -ge 1000 ]] || die "$path belongs to a system account"
  [[ $owner == "$who" ]] || die "$path belongs to $owner, not $who (pass --for-user $owner if it is for them)"
  home=$(person_home "$who")
  [[ $path != "$home" && $home != "$path"/* ]] || die "$path is $who's whole home folder (or holds it); add the project folder inside it"
  while IFS=: read -r other _ uid _ _ other_home _; do
    [[ $other != "$who" && $uid -ge 1000 && $uid -ne 65534 && $other_home == /?* ]] || continue
    [[ $path != "$other_home" && $path != "$other_home"/* ]] || die "$path is inside $other's home folder"
  done < <(getent passwd)
  WORKSPACE_OWNER=$owner
}

workspace_listed() { [[ -f $WORKSPACES ]] && grep -v '^\s*$' "$WORKSPACES" || true; }

# The drop-in: ProtectHome=tmpfs hides every home folder except the bound
# ones, which appear at their real paths, writable.
workspace_dropin() {
  local paths p
  paths=$(workspace_listed)
  if [[ -z $paths ]]; then
    rm -f "$WORKSPACES_DROPIN"
    rmdir "$(dirname "$WORKSPACES_DROPIN")" 2>/dev/null || true
    return
  fi
  install -d -m 0755 "$(dirname "$WORKSPACES_DROPIN")"
  {
    echo "# Written by install.sh workspace; each folder was added by this machine's administrator."
    echo "[Service]"
    echo "ProtectHome=tmpfs"
    while IFS= read -r p; do
      echo "BindPaths=$p"
      echo "ReadWritePaths=$p"
    done <<<"$paths"
  } >"$WORKSPACES_DROPIN.tmp"
  chmod 0644 "$WORKSPACES_DROPIN.tmp"
  mv -f "$WORKSPACES_DROPIN.tmp" "$WORKSPACES_DROPIN"
}

# Swarm's user gets rwx on the folder (and x on the folders above it, where
# missing); the owner keeps rwx on everything Swarm creates inside.
workspace_grant() {
  local path=$1 owner=$2 parent
  setfacl -R -m "u:$SERVICE_USER:rwX" "$path"
  setfacl -R -d -m "u:$SERVICE_USER:rwX" -m "u:$owner:rwX" "$path"
  parent=$(dirname "$path")
  while [[ $parent != / && $parent != /home ]]; do
    runuser -u "$SERVICE_USER" -- test -x "$parent" || setfacl -m "u:$SERVICE_USER:--x" "$parent"
    parent=$(dirname "$parent")
  done
}
workspace_revoke() {
  local path=$1 owner=$2
  setfacl -R -x "u:$SERVICE_USER" "$path" 2>/dev/null || true
  setfacl -R -d -x "u:$SERVICE_USER" "$path" 2>/dev/null || true
  setfacl -R -d -x "u:$owner" "$path" 2>/dev/null || true
}

workspace_restart() {
  systemctl daemon-reload
  if systemctl is-active -q "$DAEMON_UNIT"; then
    systemctl restart "$DAEMON_UNIT"
    echo "Swarm restarted with the new folder list."
  fi
}

workspace() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  command -v setfacl >/dev/null || die "setfacl is missing (apt-get install acl)"
  case ${1:-} in
    add)
      local who=''
      if [[ $# == 4 && $3 == --for-user ]]; then
        who=$4
      else
        [[ $# == 2 ]] || die "usage: install.sh workspace add PATH [--for-user NAME]"
      fi
      who=$(person "$who") || exit 1
      workspace_check "$2" "$who"
      install -d -m 0755 "$STATE"
      if ! workspace_listed | grep -qxF "$2"; then
        printf '%s\n' "$2" >>"$WORKSPACES"
        chmod 0644 "$WORKSPACES"
      fi
      workspace_grant "$2" "$WORKSPACE_OWNER"
      workspace_dropin
      workspace_restart
      echo "$2 is visible to Swarm (owner $WORKSPACE_OWNER). Register it in place: swarm_add_workspace $2"
      ;;
    remove)
      [[ $# == 2 ]] || die "usage: install.sh workspace remove PATH"
      [[ $2 == /* && $2 != *..* ]] || die "path must be absolute"
      workspace_listed | grep -qxF "$2" || die "$2 is not a Swarm workspace folder on this machine"
      grep -vxF "$2" "$WORKSPACES" >"$WORKSPACES.tmp" || true
      mv -f "$WORKSPACES.tmp" "$WORKSPACES"
      [[ ! -d $2 ]] || workspace_revoke "$2" "$(stat -c %U "$2")"
      workspace_dropin
      workspace_restart
      echo "$2 is no longer visible to Swarm."
      ;;
    list)
      [[ $# == 1 ]] || die "usage: install.sh workspace list"
      workspace_listed
      [[ ! -s $PUBLISH ]] || { echo "Delivered as pull requests:"; sed 's/^/  /' "$PUBLISH"; }
      ;;
    publish)
      # Jobs in PATH are delivered as pull requests by COMMAND instead of a
      # local merge; --auto delivers each finished job with commits unasked.
      # COMMAND runs on this machine as Swarm's user, never in a sandbox.
      local usage="usage: install.sh workspace publish PATH COMMAND [--auto] | publish PATH --off"
      [[ $# == 3 || $# == 4 ]] || die "$usage"
      local path=$2 command=$3 auto=${4:-} line
      workspace_listed | grep -qxF "$path" || die "$path is not a Swarm workspace folder on this machine (install.sh workspace add $path)"
      install -d -m 0755 "$STATE"
      install -m 0644 -o root -g root /dev/null "$PUBLISH.new"
      if [[ -f $PUBLISH ]]; then
        while IFS= read -r line; do [[ ${line%%$'\t'*} == "$path" ]] || printf '%s\n' "$line" >>"$PUBLISH.new"; done <"$PUBLISH"
      fi
      if [[ $command == --off ]]; then
        [[ -z $auto ]] || { rm -f "$PUBLISH.new"; die "$usage"; }
        mv -f "$PUBLISH.new" "$PUBLISH"
        echo "Jobs in $path are merged into the folder again."
        return
      fi
      [[ -z $auto || $auto == --auto ]] || { rm -f "$PUBLISH.new"; die "$usage"; }
      [[ $command == /* && $command != *..* && -f $command && -x $command ]] || { rm -f "$PUBLISH.new"; die "COMMAND must be an absolute path to an executable file"; }
      [[ $command != /home/* && $command != /root/* ]] || { rm -f "$PUBLISH.new"; die "COMMAND must be outside home folders (Swarm cannot see them); /usr/local/lib is a good place"; }
      [[ $(stat -c %u "$command") == 0 && $(( 8#$(stat -c %a "$command") & 8#022 )) == 0 ]] || { rm -f "$PUBLISH.new"; die "COMMAND must be owned by root and writable only by root"; }
      printf '%s\t%s%s\n' "$path" "$command" "${auto:+$'\t'auto}" >>"$PUBLISH.new"
      mv -f "$PUBLISH.new" "$PUBLISH"
      echo "Jobs in $path are delivered as pull requests by $command${auto:+, each finished job with commits without asking}."
      ;;
    *) die "usage: install.sh workspace add PATH [--for-user NAME] | remove PATH | list | publish PATH COMMAND [--auto] | publish PATH --off" ;;
  esac
}

# Wait until the app answers on its loopback port (with the Host it accepts).
wait_ready() {
  load_conf
  for _ in $(seq 1 120); do
    curl -fsS -o /dev/null -H "Host: ${APP_ORIGIN#*://}" http://127.0.0.1:8443/ 2>/dev/null && return 0
    sleep 1
  done
  die "the app has not started; check: journalctl -u $DAEMON_UNIT -u $APP_UNIT"
}

# Forgotten password or lost authenticator: delete the login (not Swarm or
# your work) and restart the app, which also signs out every browser. The next
# visit to the setup page creates a new login.
reset_login() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  rm -f "$APP_CONFIG/account.json"
  systemctl restart "$APP_UNIT"
  wait_ready
  load_conf
  echo "Login deleted. Open the setup page ($APP_ORIGIN) to create a new one."
}

remove_legacy_layout() {
  systemctl disable swarm-headless-firewall.service >/dev/null 2>&1 || true
  rm -f "$UNITS/swarm-headless-firewall.service"
  if command -v docker >/dev/null; then
    docker rm -f "$LEGACY_CONTAINER" >/dev/null 2>&1 || true
    docker volume rm swarm-config swarm-data swarm-cache swarm-logs >/dev/null 2>&1 || true
    docker image rm swarm-app:local swarm-headless:local >/dev/null 2>&1 || true
    docker network rm "$LEGACY_NETWORK" >/dev/null 2>&1 || true
  fi
  if command -v iptables >/dev/null; then
    iptables -D DOCKER-USER -i "$LEGACY_BRIDGE" -j SWARM-ISOLATE 2>/dev/null || true
    iptables -D INPUT -i "$LEGACY_BRIDGE" -j SWARM-HOST 2>/dev/null || true
    iptables -F SWARM-ISOLATE 2>/dev/null && iptables -X SWARM-ISOLATE 2>/dev/null || true
    iptables -F SWARM-HOST 2>/dev/null && iptables -X SWARM-HOST 2>/dev/null || true
  fi
}

# Remove everything install_all created for Swarm itself. Docker, gVisor,
# Tailscale and ufw stay: Tailscale may be how you reach this server.
uninstall_all() {
  local yes=0 delete_projects=0
  while (($#)); do
    case $1 in
      --yes) yes=1; shift ;;
      --delete-projects) delete_projects=1; shift ;;
      *) usage >&2; die "unknown option $1" ;;
    esac
  done
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  if ((!yes)); then
    local what='Swarm, its sign-ins, AI keys, relay pairing and agent worktrees'
    ((delete_projects)) && what="$what, and every project in $PROJECT"
    { : </dev/tty; } 2>/dev/null || die "no terminal to confirm on; pass --yes to continue"
    printf 'This deletes %s. Type yes to continue: ' "$what" >/dev/tty
    local answer
    read -r answer </dev/tty
    [[ $answer == yes ]] || die "cancelled; nothing was changed"
  fi

  say "Removing Swarm"
  local ws tailnet=off
  [[ -f $CONF ]] && tailnet=$(saved_tailnet)
  while IFS= read -r ws; do
    [[ -n $ws && -d $ws ]] || continue
    workspace_revoke "$ws" "$(stat -c %U "$ws")"
  done < <(workspace_listed)
  rm -rf "$(dirname "$WORKSPACES_DROPIN")" "$WORKSPACES"
  systemctl disable --now swarm-headless-update.timer >/dev/null 2>&1 || true
  systemctl disable --now "$APP_UNIT" "$DAEMON_UNIT" >/dev/null 2>&1 || true
  systemctl disable "$FIREWALL_UNIT" >/dev/null 2>&1 || true
  rm -f "$UNITS/swarm-headless-update.service" "$UNITS/swarm-headless-update.timer" \
    "$UNITS/$APP_UNIT" "$UNITS/$DAEMON_UNIT" "$UNITS/$FIREWALL_UNIT"
  remove_legacy_layout
  systemctl daemon-reload
  # Only Swarm's own Serve entries; Tailscale used for anything else is left alone.
  if [[ $tailnet == on ]] && command -v tailscale >/dev/null; then
    tailscale serve --https=443 off >/dev/null 2>&1 || true
    tailscale serve --https=8444 off >/dev/null 2>&1 || true
  fi
  if command -v docker >/dev/null; then
    local box
    for box in $(docker ps -aq --filter label=swarm.sandbox=1); do docker rm -f "$box" >/dev/null 2>&1 || true; done
    docker volume ls -q | grep '^swarm-sandbox-.*-home$' | xargs -r docker volume rm >/dev/null 2>&1 || true
    docker image rm "$SANDBOX_IMAGE" >/dev/null 2>&1 || true
    docker network rm "$SANDBOX_NETWORK" >/dev/null 2>&1 || true
  fi
  if command -v iptables >/dev/null; then
    iptables -D DOCKER-USER -i "$SANDBOX_BRIDGE" -j SWARM-SANDBOX 2>/dev/null || true
    iptables -D INPUT -i "$SANDBOX_BRIDGE" -j SWARM-SANDBOX-HOST 2>/dev/null || true
    iptables -F SWARM-SANDBOX 2>/dev/null && iptables -X SWARM-SANDBOX 2>/dev/null || true
    iptables -F SWARM-SANDBOX-HOST 2>/dev/null && iptables -X SWARM-SANDBOX-HOST 2>/dev/null || true
  fi
  rm -rf "${DATA_DIRS[@]}" /run/swarmd "$SERVICE_HOME" "$OPT" "$STAGE" "$SWARMCTL" "$AI_KEY_IDS"
  rm -f "$CONF"
  if ((delete_projects)); then
    rm -rf "$PROJECT"
  elif [[ -d $PROJECT ]]; then
    echo "Kept your projects in $PROJECT"
  fi
  echo "Swarm is removed. Docker, gVisor, Tailscale and ufw are unchanged."
  echo "AI key files made by connect-ai (~/.config/swarm/ai-key) no longer work; delete them or run connect-ai after a new install."
}

# Wipe and install again with the saved settings unless new ones are given.
reinstall_all() {
  local uninstall=() install=() saved=() args=()
  while (($#)); do
    case $1 in
      --yes|--delete-projects) uninstall+=("$1"); shift ;;
      *) install+=("$1"); shift ;;
    esac
  done
  if [[ -f $CONF ]]; then
    # shellcheck disable=SC1090
    mapfile -t saved < <(. "$CONF" && printf '%s\n' "${APP_RELAY_URL:-}" "${APP_DEVICE_NAME:-}" "${SWARM_REF:-}" "${AI_ACCESS:-}" "${SECRETS_GATEWAY:-}" "${PERMISSIONS:-}")
    saved+=("$(saved_tailnet)")
  fi
  [[ -n ${saved[0]:-} ]] && args+=(--relay "${saved[0]}")
  [[ -n ${saved[1]:-} ]] && args+=(--name "${saved[1]}")
  # The retired container layout's branch: everything it carried is on dev.
  [[ -n ${saved[2]:-} && ${saved[2]} != swarm-control ]] && args+=(--ref "${saved[2]}")
  [[ ${saved[3]:-} == off ]] && args+=(--no-ai-access)
  [[ -n ${saved[4]:-} ]] && args+=(--secrets-gateway "${saved[4]}")
  [[ ${saved[5]:-} == ask ]] && args+=(--ask-permissions)
  [[ ${saved[6]:-} == on ]] && args+=(--tailnet)
  # Later options win, so anything passed now overrides the saved settings.
  uninstall_all "${uninstall[@]}"
  install_all "${args[@]}" "${install[@]}"
}

install_gvisor() {
  command -v runsc >/dev/null && return 0
  # gVisor's signed apt repository (https://gvisor.dev/docs/user_guide/install/).
  if curl -fsSL https://gvisor.dev/archive.key | gpg --dearmor --yes -o /usr/share/keyrings/gvisor-archive-keyring.gpg &&
    echo "deb [arch=amd64 signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" \
      >"$GVISOR_LIST" &&
    apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq runsc >/dev/null; then
    return 0
  fi
  echo "warning: gVisor could not be installed; sandboxes use Docker's default runtime" >&2
  rm -f "$GVISOR_LIST"
}

register_gvisor() {
  command -v runsc >/dev/null || return 0
  local runtimes restart=0
  runtimes=$(docker info --format '{{json .Runtimes}}' 2>/dev/null)
  if ! grep -q '"runsc"' <<<"$runtimes"; then
    runsc install >/dev/null && restart=1
  fi
  # Swarm builds folders' environments with docker commit, which sees only
  # writes outside gVisor's in-sandbox overlay: builds run on this runtime.
  if ! grep -q '"runsc-build"' <<<"$runtimes"; then
    runsc install --runtime=runsc-build -- --overlay2=none >/dev/null && restart=1
  fi
  if ((restart)); then systemctl restart docker; fi
  return 0
}

tailscale_new_enough() {
  local version major minor
  version=$(tailscale version 2>/dev/null | head -n1)
  IFS=. read -r major minor _ <<<"$version"
  minor=${minor%%-*}
  [[ $major =~ ^[0-9]+$ && $minor =~ ^[0-9]+$ ]] || return 1
  ((major > 1 || (major == 1 && minor >= 92)))
}

# The AI gateway on the tailnet. With tailnet identity, Serve forwards the
# caller's swarmagent.dev/cap/swarm grant from the tailnet policy.
serve_ai_gateway() {
  local args=(serve --bg --https=8444)
  [[ $1 == on ]] && args+=(--accept-app-caps=swarmagent.dev/cap/swarm)
  tailscale serve --https=8444 off >/dev/null 2>&1 || true
  tailscale "${args[@]}" http://127.0.0.1:7783 >/dev/null || die "tailscale serve for the AI gateway failed"
}

# Turn tailnet identity on or off for an existing install, keeping its state.
set_tailnet_identity() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  load_conf
  [[ $(saved_tailnet) == on ]] || die "this machine is not on a tailnet (install.sh tailnet on adds it)"
  [[ ${AI_ACCESS:-off} == on ]] || die "the AI gateway is off on this machine (installed with --no-ai-access)"
  case ${1:-} in
    on)
      tailscale_new_enough || tailscale update --yes >/dev/null 2>&1 || true
      tailscale_new_enough || die "Tailscale 1.92 or newer is required (this machine has $(tailscale version | head -n1))"
      ;;
    off) ;;
    *) die "usage: install.sh tailnet-identity on|off" ;;
  esac
  sed -i '/^TAILNET_IDENTITY=/d' "$CONF" && echo "TAILNET_IDENTITY=$1" >>"$CONF"
  serve_ai_gateway "$1"
  install_units
  systemctl restart "$DAEMON_UNIT"
  wait_ready
  echo "Tailnet identity is $1. Swarm's log says which mode the gateway runs in:"
  journalctl -u "$DAEMON_UNIT" -n 200 --no-pager 2>/dev/null | grep -E 'tailnet identity (on|off)' | tail -n1 || true
}

# Install Tailscale, join the tailnet as NAME (as TAG when given) and admit only
# the tailnet inbound with ufw (SSH stays open unless LOCK_SSH is 1). Sets
# TS_DNS, TS_IP and TS_IDENTITY (on when the AI gateway can admit tailnet
# devices by your Tailscale policy).
join_tailnet() {
  local name=$1 tag=$2 lock_ssh=$3 ai_access=$4
  say "Joining your tailnet"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ufw jq >/dev/null
  command -v tailscale >/dev/null || curl -fsSL https://tailscale.com/install.sh | sh
  if ! tailscale status >/dev/null 2>&1; then
    local up=(up --ssh --hostname="$name") keyfile=''
    [[ -n $tag ]] && up+=(--advertise-tags="$tag")
    if [[ -n ${TS_AUTHKEY:-} ]]; then
      keyfile=$(mktemp)
      chmod 600 "$keyfile"
      printf '%s' "$TS_AUTHKEY" >"$keyfile"
      up+=(--auth-key="file:$keyfile")
    else
      echo "Open the login link below in your browser and approve this machine."
    fi
    if ! tailscale "${up[@]}"; then
      [[ -n $keyfile ]] && rm -f "$keyfile"
      die "could not join the tailnet"
    fi
    [[ -n $keyfile ]] && rm -f "$keyfile"
  fi
  # Tailnet identity needs Serve to set (and strip forged copies of) the app
  # capability header, which Tailscale does from 1.92.
  TS_IDENTITY=off
  if [[ $ai_access == on ]]; then
    tailscale_new_enough || tailscale update --yes >/dev/null 2>&1 || true
    if tailscale_new_enough; then
      TS_IDENTITY=on
    else
      echo "warning: Tailscale $(tailscale version | head -n1) is older than 1.92; the AI gateway accepts AI keys only" >&2
    fi
  fi
  TS_DNS=$(tailscale status --json | jq -r '(.Self.DNSName // "") | rtrimstr(".")')
  [[ $TS_DNS == *.ts.net ]] || die "no tailnet name yet: turn on MagicDNS in the Tailscale admin console (DNS page), then run this again"
  TS_IP=$(tailscale ip -4 | head -n1)

  say "Firewall: nothing inbound except your tailnet"
  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  ufw allow in on tailscale0 >/dev/null
  ufw allow 41641/udp >/dev/null
  # Keep SSH open only where an SSH server is installed (it registers ufw's
  # OpenSSH profile); a desktop without one has nothing to keep open.
  if ((lock_ssh)); then
    ufw delete allow OpenSSH >/dev/null 2>&1 || true
  elif ufw app info OpenSSH >/dev/null 2>&1; then
    ufw allow OpenSSH >/dev/null
  fi
  ufw --force enable >/dev/null
}

# Serve the setup page (https 443) and, when the MCP is on, the AI gateway
# (https 8444) on the tailnet, both through Tailscale Serve to loopback.
serve_tailnet() {
  local ai_access=$1 identity=$2
  say "Serving Swarm on your tailnet"
  tailscale serve --bg 8443 >/dev/null ||
    die "tailscale serve failed: turn on HTTPS certificates in the Tailscale admin console (DNS page), then run this again"
  if [[ $ai_access == on ]]; then
    serve_ai_gateway "$identity"
  else
    tailscale serve --https=8444 off >/dev/null 2>&1 || true
  fi
}

# Restart Swarm and its setup page and wait until the page answers.
restart_swarm() {
  systemctl restart "$DAEMON_UNIT"
  systemctl restart "$APP_UNIT"
  wait_ready
}

# Add Tailscale to an install, or stop serving on the tailnet.
tailnet_cmd() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  load_conf
  conf_migrate
  local usage="usage: install.sh tailnet on [--tag TAG] [--lock-ssh] | off"
  case ${1:-} in
    on)
      shift
      local tag='' lock_ssh=0
      while (($#)); do
        case $1 in
          --tag) tag=${2:?--tag needs a tag such as tag:swarm}; shift 2 ;;
          --lock-ssh) lock_ssh=1; shift ;;
          *) die "$usage" ;;
        esac
      done
      [[ -z $tag || $tag =~ ^tag:[a-z0-9][a-z0-9-]{0,40}$ ]] || die "--tag must look like tag:swarm"
      join_tailnet "$APP_DEVICE_NAME" "$tag" "$lock_ssh" "${AI_ACCESS:-off}"
      conf_set TAILNET on
      conf_set APP_ORIGIN "https://$TS_DNS"
      conf_set TAILNET_IDENTITY "$TS_IDENTITY"
      conf_set APP_AI_URL "https://$TS_DNS:8444/mcp"
      conf_set APP_AI_IP "$TS_IP"
      install_units
      restart_swarm
      serve_tailnet "${AI_ACCESS:-off}" "$TS_IDENTITY"
      echo "Swarm is on your tailnet. The setup page is now https://$TS_DNS (from this machine too)."
      if [[ ${AI_ACCESS:-off} == on ]]; then
        echo "Swarm's MCP: https://$TS_DNS:8444/mcp on the tailnet, and $LOCAL_MCP_URL on this machine."
      fi
      ;;
    off)
      [[ $# == 1 ]] || die "$usage"
      if [[ $TAILNET != on ]]; then
        echo "Swarm is not served on a tailnet; nothing to change."
        return 0
      fi
      tailscale serve --https=443 off >/dev/null || echo "warning: could not remove Tailscale Serve for https 443; check: tailscale serve status" >&2
      tailscale serve --https=8444 off >/dev/null 2>&1 || true
      conf_set TAILNET off
      conf_set APP_ORIGIN "$LOCAL_APP_ORIGIN"
      conf_set TAILNET_IDENTITY off
      conf_set APP_AI_URL ''
      conf_set APP_AI_IP ''
      install_units
      restart_swarm
      echo "Swarm no longer serves on your tailnet; the setup page is $LOCAL_APP_ORIGIN on this machine. Tailscale and ufw are unchanged."
      ;;
    *) die "$usage" ;;
  esac
}

# Agents without prompts (auto, inside the sandbox only) or asking first.
set_permissions() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  load_conf
  case ${1:-} in
    auto|ask) ;;
    *) die "usage: install.sh permissions auto|ask" ;;
  esac
  conf_set PERMISSIONS "$1"
  install_units
  restart_swarm
  if [[ $1 == auto ]]; then
    echo "Agents run without permission prompts, only while their sandbox is active."
  else
    echo "Agents ask before commands and file changes."
  fi
}

# The lines that connect a person's AI on this machine to Swarm's MCP, reading
# the key from their private file. They run as that person, not root.
print_connect() {
  cat <<CONNECT
  As $1, connect your AI (the key stays in ~/.config/swarm/ai-key):
    Claude Code, with Swarm's skills (the plugin finds the key by itself):
      claude plugin marketplace add swarm-agent/swarm && claude plugin install swarm@swarm
    Claude Code, tools only:
      claude mcp add --scope user --transport http swarm $LOCAL_MCP_URL --header "Authorization: Bearer \$(cat ~/.config/swarm/ai-key)"
    Codex (start it with SWARM_AI_KEY="\$(cat ~/.config/swarm/ai-key)" codex):
      codex mcp add swarm --url $LOCAL_MCP_URL --bearer-token-env-var SWARM_AI_KEY
CONNECT
}

# Swarm's owner, created for the person when Swarm has none yet (the same
# call as `swarmctl setup identity`; the setup page then keeps this owner and
# its first visit still creates your login).
ensure_owner() {
  local who=$1 status
  systemctl is-active -q "$DAEMON_UNIT" || die "Swarm is not running; check: journalctl -u $DAEMON_UNIT"
  status=$("$SWARMCTL" setup status) || die "could not read Swarm's setup status"
  [[ $status == *identity=true* ]] && return 0
  "$SWARMCTL" setup identity --username "$who" --name "$APP_DEVICE_NAME" >/dev/null ||
    die "could not create Swarm's owner $who"
  echo "Created Swarm's owner ($who)."
}

# Give a person's AI on this machine its own AI key for Swarm's MCP, in a file
# only they can read. The key goes from Swarm to that file through a private
# root-only file and a pipe; it is never printed or put on a command line.
mint_ai_key() {
  local who=$1 access=$2 home dir keyfile idfile tmp old_id=''
  [[ ${AI_ACCESS:-off} == on ]] || die "Swarm's MCP is off on this machine (installed with --no-ai-access); reinstall without it to connect an AI"
  command -v jq >/dev/null || die "jq is missing (apt-get install jq)"
  ensure_owner "$who"
  home=$(person_home "$who")
  dir=$home/.config/swarm
  keyfile=$dir/ai-key
  # Made as the person, so nothing below writes through a link they placed.
  runuser -u "$who" -- mkdir -p -m 0700 "$dir" || die "could not create $dir"
  [[ -d $dir && ! -L $dir && $(stat -c %U "$dir") == "$who" ]] || die "$dir must be a folder owned by $who"
  install -d -m 0700 -o root -g root "$AI_KEY_IDS"
  idfile=$AI_KEY_IDS/$who
  [[ -f $idfile ]] && old_id=$(<"$idfile")
  tmp=$(mktemp "$AI_KEY_IDS/.mint.XXXXXX")
  if ! "$SWARMCTL" setup sdk-token --ai-access "$access" --name "local-ai-$who" \
    --expires-in-seconds "$AI_KEY_SECONDS" >"$tmp"; then
    rm -f "$tmp"
    die "no AI key was made; if Swarm made one anyway it shows under AI keys on the setup page"
  fi
  if ! jq -er '.token | select(startswith("swk_"))' "$tmp" |
    runuser -u "$who" -- sh -c 'umask 077 && cat >"$1.new" && mv -f "$1.new" "$1"' sh "$keyfile"; then
    rm -f "$tmp"
    die "could not write $keyfile; revoke the new local-ai-$who key on the setup page"
  fi
  jq -er '.id' "$tmp" >"$idfile.new" && mv -f "$idfile.new" "$idfile"
  rm -f "$tmp"
  if [[ -n $old_id ]]; then
    "$SWARMCTL" setup revoke-sdk-token --id "$old_id" >/dev/null ||
      echo "warning: could not revoke $who's previous AI key (id $old_id); revoke it on the setup page" >&2
  fi
  echo "Made a $access AI key for $who in ~/.config/swarm/ai-key (only $who can read it; valid for a year)."
}

connect_ai() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  load_conf
  local who='' access=write
  while (($#)); do
    case $1 in
      --for-user) who=${2:?--for-user needs a user name}; shift 2 ;;
      --access) access=${2:?--access needs write or full}; shift 2 ;;
      *) die "usage: install.sh connect-ai [--for-user NAME] [--access write|full]" ;;
    esac
  done
  [[ $access == write || $access == full ]] || die "--access must be write or full"
  who=$(person "$who") || exit 1
  mint_ai_key "$who" "$access"
  print_connect "$who"
  echo
  echo "Then tell your AI: set up Swarm"
}

# Set the first provider API key by script; the key comes on stdin (or a
# hidden prompt) and reaches Swarm only through a pipe.
provider_key() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  [[ $# == 1 && $1 =~ ^[a-z0-9][a-z0-9._-]{0,63}$ ]] ||
    die "usage: install.sh key PROVIDER (the API key on stdin, or typed at a hidden prompt)"
  load_conf
  require_owner
  local key
  read_secret key "Paste the $1 API key"
  printf '%s' "$key" | "$SWARMCTL" setup credential --provider "$1" --api-key-stdin
  key=''
}

# Secrets agents can use but never see (swarmctl secret, over Swarm's socket).
secret_cmd() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  local usage="usage: install.sh secret set NAME [--hosts HOST,...] | secret grant NAME FOLDER --days N | --hours N"
  local action=${1:-} name=${2:-}
  [[ $name =~ ^[A-Za-z0-9_.-]{1,128}$ ]] || die "$usage"
  shift 2
  load_conf
  require_owner
  case $action in
    set)
      local hosts='' value
      while (($#)); do
        case $1 in
          --hosts) hosts=${2:?--hosts needs HOST,...}; shift 2 ;;
          *) die "$usage" ;;
        esac
      done
      [[ -z $hosts || $hosts =~ ^[A-Za-z0-9.,*:-]+$ ]] || die "--hosts must be website names separated by commas"
      if [[ -n $hosts ]]; then
        "$SWARMCTL" secret create --name "$name" --hosts "$hosts" ||
          die "could not create the secret $name (if it exists, run without --hosts to set its value)"
      fi
      read_secret value "Paste the value for $name"
      printf '%s' "$value" | "$SWARMCTL" secret set-value --name "$name" --value-stdin
      value=''
      ;;
    grant)
      local folder=${1:-} days=0 hours=0
      [[ $folder == /* ]] || die "$usage"
      shift
      while (($#)); do
        case $1 in
          --days) days=${2:?--days needs a number}; shift 2 ;;
          --hours) hours=${2:?--hours needs a number}; shift 2 ;;
          *) die "$usage" ;;
        esac
      done
      [[ $days =~ ^[0-9]{1,4}$ && $hours =~ ^[0-9]{1,6}$ ]] && ((days + hours > 0)) ||
        die "set --days and/or --hours for how long the grant lasts"
      [[ ${SECRETS_GATEWAY:-off} == on ]] ||
        echo "note: the secret gateway is off on this machine; agents get the secret once install.sh secrets-gateway on" >&2
      "$SWARMCTL" secret grant --name "$name" --workspace "$folder" --days "$days" --hours "$hours"
      ;;
    *) die "$usage" ;;
  esac
}

install_all() {
  local relay='' name=swarm ref=main lock_ssh=0 ai_access=on tag='' secrets_gateway=on permissions=auto tailnet=off for_user=''
  while (($#)); do
    case $1 in
      --for-user) for_user=${2:?--for-user needs a user name}; shift 2 ;;
      --relay) relay=${2:?--relay needs a URL}; shift 2 ;;
      --name) name=${2:?--name needs a value}; shift 2 ;;
      --ref) ref=${2:?--ref needs a branch}; shift 2 ;;
      --tailnet) tailnet=on; shift ;;
      --no-tailnet) tailnet=off; shift ;;
      --tag) tag=${2:?--tag needs a tag such as tag:swarm}; shift 2 ;;
      --lock-ssh) lock_ssh=1; shift ;;
      --no-ai-access) ai_access=off; shift ;;
      --ai-access) ai_access=on; shift ;;
      --secrets-gateway) secrets_gateway=${2:?--secrets-gateway needs on or off}; shift 2 ;;
      --ask-permissions) permissions=ask; shift ;;
      -h|--help) usage; exit 0 ;;
      *) usage >&2; die "unknown option $1" ;;
    esac
  done
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  [[ $(uname -m) == x86_64 ]] || die "Swarm's headless build is x86_64 only (this machine is $(uname -m))"
  [[ $name =~ ^[a-z0-9][a-z0-9-]{0,40}$ ]] || die "--name must be lowercase letters, digits and dashes"
  [[ $secrets_gateway == on || $secrets_gateway == off ]] || die "--secrets-gateway must be on or off"
  [[ -z $relay || $relay =~ ^https://[A-Za-z0-9.-]+/?$ ]] || die "--relay must be an https origin such as https://swarm-relay.example.workers.dev"
  [[ $ref =~ ^[A-Za-z0-9._/-]+$ ]] || die "--ref must be a branch name"
  [[ -z $tag || $tag =~ ^tag:[a-z0-9][a-z0-9-]{0,40}$ ]] || die "--tag must look like tag:swarm"
  [[ $tailnet == on || (-z $tag && $lock_ssh == 0) ]] || die "--tag and --lock-ssh are for the tailnet; add --tailnet"
  [[ $tailnet == on || -z ${TS_AUTHKEY:-} ]] || die "TS_AUTHKEY is set but --tailnet is not: add --tailnet to join your tailnet, or unset TS_AUTHKEY"
  # The person the AI connection is for: --for-user (checked now), else the
  # account that ran sudo when it is a person's. Without one, the final output
  # names the connect-ai step instead.
  local who=''
  if [[ -n $for_user ]]; then
    who=$(person "$for_user") || exit 1
  elif [[ -n ${SUDO_USER:-} ]]; then
    who=$(person "" 2>/dev/null) || who=''
  fi

  # Snap's Docker is confined: it cannot bind-mount project folders outside
  # home or take the gVisor runtime, so agent sandboxes cannot run on it, and
  # installing the distribution's Docker beside it leaves two daemons fighting
  # over one socket.
  if command -v snap >/dev/null && snap list docker >/dev/null 2>&1; then
    die "this machine runs Docker from snap, which cannot run Swarm's agent sandboxes (it cannot mount project folders or use gVisor). Remove it first with: sudo snap remove docker (this deletes the containers, images and volumes it holds), then run this again"
  fi

  say "Installing Docker, gVisor and Git"
  local packages=(git curl ca-certificates gnupg iproute2 jq acl)
  # A working Docker with buildx (such as Docker's own docker-ce) stays:
  # the distribution's packages would conflict with it.
  if command -v docker >/dev/null && docker buildx version >/dev/null 2>&1; then
    echo "Using the Docker already on this machine ($(docker --version))"
  else
    packages+=(docker.io docker-buildx)
  fi
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${packages[@]}" >/dev/null
  systemctl enable --now docker >/dev/null
  install_gvisor
  register_gvisor

  local origin=$LOCAL_APP_ORIGIN ai_url='' ai_ip='' tailnet_identity=off
  if [[ $tailnet == on ]]; then
    join_tailnet "$name" "$tag" "$lock_ssh" "$ai_access"
    origin=https://$TS_DNS ai_url=https://$TS_DNS:8444/mcp ai_ip=$TS_IP tailnet_identity=$TS_IDENTITY
  fi

  say "Fetching Swarm ($ref)"
  install -d -m 0755 "$STATE"
  if [[ -d $SRC/.git ]]; then
    git -C "$SRC" fetch -q origin "$ref" && git -C "$SRC" checkout -q -B "$ref" FETCH_HEAD
  else
    git clone -q --branch "$ref" https://github.com/swarm-agent/swarm.git "$SRC"
  fi

  say "Creating Swarm's service user"
  id "$SERVICE_USER" >/dev/null 2>&1 ||
    useradd --system --user-group --home-dir "$SERVICE_HOME" --create-home --shell /usr/sbin/nologin "$SERVICE_USER"
  usermod -aG docker "$SERVICE_USER"
  install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0700 "$SERVICE_HOME"
  install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0750 "$PROJECT"
  # Projects from the earlier container layout belong to its uid 10001.
  chown -R "$SERVICE_USER:$SERVICE_USER" "$PROJECT"
  # Sandboxes have no account database entry for Swarm's uid, so Git needs a
  # configured identity for agent commits; keep any the owner already set.
  runuser -u "$SERVICE_USER" -- env HOME="$SERVICE_HOME" sh -c \
    "git config --global user.name >/dev/null || git config --global user.name 'Swarm agent'; \
     git config --global user.email >/dev/null || git config --global user.email 'swarm@$name.invalid'"

  local previous_relay='' previous_tailnet=off
  # shellcheck disable=SC1090
  [[ -f $CONF ]] && previous_relay=$(. "$CONF" && printf '%s' "${APP_RELAY_URL:-}")
  [[ -f $CONF ]] && previous_tailnet=$(saved_tailnet)
  if [[ $previous_tailnet == on && $tailnet == off ]] && command -v tailscale >/dev/null; then
    echo "This install served Swarm on your tailnet; installing without --tailnet stops that (Tailscale stays)."
    tailscale serve --https=443 off >/dev/null 2>&1 || true
    tailscale serve --https=8444 off >/dev/null 2>&1 || true
  fi
  umask 077
  cat > "$CONF" <<CONF
SWARM_REF=$ref
TAILNET=$tailnet
APP_ORIGIN=$origin
APP_RELAY_URL=${relay:-$previous_relay}
APP_DEVICE_NAME=$name
AI_ACCESS=$ai_access
TAILNET_IDENTITY=$tailnet_identity
SECRETS_GATEWAY=$secrets_gateway
PERMISSIONS=$permissions
APP_AI_URL=$ai_url
APP_AI_IP=$ai_ip
SANDBOX_RUNTIME=default
CONF
  umask 022

  remove_legacy_layout
  up
  [[ $tailnet == on ]] && serve_tailnet "$ai_access" "$tailnet_identity"

  wait_ready
  say "Checking that agent sandboxes cannot reach this server or your networks"
  check_isolation

  # The person's AI on this machine gets its key now (Swarm's owner is
  # created for them). A failure is reported, and the install exits non-zero.
  local key_made=0 rc=0
  if [[ -n $who && $ai_access == on ]]; then
    say "Connecting $who's AI on this machine"
    set +e
    (
      set -e
      load_conf
      mint_ai_key "$who" write
    )
    rc=$?
    set -e
    ((rc == 0)) && key_made=1
  fi
  install_done "$who" "$key_made"
  if ((rc != 0)); then
    die "Swarm is running, but the AI key for $who could not be made (see above); run: sudo bash $INSTALLER connect-ai --for-user $who"
  fi
}

# What runs, where, and the next steps.
install_done() {
  local who=$1 key_made=${2:-0}
  load_conf
  echo
  echo "Swarm is running on this machine; its services listen on 127.0.0.1 only:"
  echo "  Swarm          swarm-headless: agents in one sandbox per project ($(
    [[ $SANDBOX_RUNTIME == runsc ]] && echo gVisor || echo Docker runc)), $(
    [[ $PERMISSIONS == auto ]] && echo 'no permission prompts inside the sandbox' || echo 'agents ask first'), secret gateway $SECRETS_GATEWAY"
  if [[ $TAILNET == on ]]; then
    echo "  Setup page     127.0.0.1:8443, opened as $APP_ORIGIN (swarm-headless-app)"
  else
    echo "  Setup page     $LOCAL_APP_ORIGIN (swarm-headless-app)"
  fi
  if [[ $AI_ACCESS == on ]]; then
    echo "  Swarm MCP      $LOCAL_MCP_URL (AI keys only)"
  fi
  if [[ $TAILNET == on ]]; then
    echo "  On your tailnet (Tailscale Serve): $APP_ORIGIN$([[ $AI_ACCESS == on ]] && echo " and $APP_AI_URL")"
  fi
  echo
  echo "Setup page (your login, provider keys, models):"
  if [[ $TAILNET == on ]]; then
    echo "  $APP_ORIGIN   (from this machine or any device on your tailnet)"
  else
    echo "  on this machine:     $LOCAL_APP_ORIGIN"
    echo "  from your computer:  ssh -N -L 8443:127.0.0.1:8443 ${who:-USER}@$(hostname)"
    echo "                       then open $LOCAL_APP_ORIGIN there"
  fi
  echo "Provider key by script instead (the key on stdin, never shown):"
  echo "  sudo bash $INSTALLER key PROVIDER < key-file"
  if [[ $AI_ACCESS == on ]]; then
    if ((key_made)); then
      echo "Your AI on this machine:"
    else
      echo "Your AI on this machine needs its key first (this also creates Swarm's owner if needed):"
      echo "  sudo bash $INSTALLER connect-ai --for-user ${who:-NAME}"
    fi
    print_connect "${who:-NAME}"
  fi
  echo
  echo "Then tell your AI: set up Swarm"
}

# Turn the agent secret gateway on or off for an existing install. When on,
# secrets the owner grants to a project are injected on the way out to their
# allowed websites; agents only ever hold stand-ins.
set_secrets_gateway() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  load_conf
  case ${1:-} in
    on|off) ;;
    *) die "usage: install.sh secrets-gateway on|off" ;;
  esac
  sed -i "/^SECRETS_GATEWAY=/d" "$CONF" && echo "SECRETS_GATEWAY=$1" >>"$CONF"
  SECRETS_GATEWAY=$1
  install_units
  systemctl daemon-reload
  systemctl restart "$FIREWALL_UNIT"
  systemctl restart "$DAEMON_UNIT"
  wait_ready
  echo "Secret gateway is $1."
}

status_cmd() {
  systemctl --no-pager status "$DAEMON_UNIT" "$APP_UNIT" | grep -E '●|Active:' || true
  if [[ -f $CONF ]]; then
    load_conf
    echo "Setup page: $APP_ORIGIN; Swarm MCP: $([[ ${AI_ACCESS:-off} == on ]] && echo "$LOCAL_MCP_URL" || echo off); tailnet: $(saved_tailnet); permissions: ${PERMISSIONS:-auto (after the next update)}; secret gateway: ${SECRETS_GATEWAY:-off}"
  fi
  check_sandbox
  if [[ -f $CONF && $(saved_tailnet) == on ]]; then tailscale serve status; fi
}

main() {
  case ${1:-install} in
    install) shift || true; install_all "$@" ;;
    -*) install_all "$@" ;;
    reinstall) shift; reinstall_all "$@" ;;
    uninstall) shift; uninstall_all "$@" ;;
    update) update ;;
    dev-sync) shift; dev_sync "$@" ;;
    channel) shift; channel "$@" ;;
    daemon-env) shift; daemon_env "$@" ;;
    up) up ;;
    reset-login) reset_login ;;
    apply) remove_legacy_layout; up ;;
    firewall) firewall ;;
    check-isolation) check_isolation ;;
    check-sandbox) check_sandbox ;;
    connect-ai) shift; connect_ai "$@" ;;
    key) shift; provider_key "$@" ;;
    secret) shift; secret_cmd "$@" ;;
    tailnet) shift; tailnet_cmd "$@" ;;
    tailnet-identity) shift; set_tailnet_identity "$@" ;;
    secrets-gateway) shift; set_secrets_gateway "$@" ;;
    permissions) shift; set_permissions "$@" ;;
    workspace) shift; workspace "$@" ;;
    status) status_cmd ;;
    -h|--help|help) usage ;;
    *) usage >&2; exit 2 ;;
  esac
}

# Everything above is parsed before anything runs, so a partial download or an
# update replacing this file cannot execute half a script.
main "$@"
