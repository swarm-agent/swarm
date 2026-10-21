#!/usr/bin/env bash
# One-line installer for Swarm on this machine: headless, with its setup page.
#
# On Ubuntu 24.04 x86_64 (a server or your own PC), from your own account:
#   curl -fsSL https://swarmagent.dev/install | sudo bash
#
# It downloads the newest released build of Swarm and its setup page from
# GitHub, checks it against its published checksum and runs both as services;
# nothing is compiled and no Docker is installed. Environments (a folder's own
# container for its agents' commands, with its secrets) and the agent sandbox
# need Docker: `install.sh environments on` (or --environments on) installs
# Docker and gVisor then. --ref BRANCH builds that branch from source instead
# (with Docker), for development machines. Everything listens on 127.0.0.1
# only: the setup page on
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
# login and local socket to itself. By default the agent sandbox is off:
# agents run in Swarm on this machine. --sandbox on (or `install.sh sandbox
# on`) runs agents' commands, and Swarm's Git on their projects, in one
# sandbox per project: a container (under gVisor when it works on this
# machine) with no capabilities, Swarm's non-root user, only that project and
# its worktrees mounted, and a network that reaches the internet but never this
# server, your tailnet, private networks or cloud metadata. Agents run without
# permission prompts, sandbox or not (questions and hard denials can still
# pause jobs); --ask-permissions (or `install.sh permissions ask`) keeps the prompts. The
# secret gateway is on by default (--secrets-gateway off turns it off).
#
# The first visit to the setup page creates your login (username, password
# and an optional authenticator code).
# Later: `install.sh update` takes the newest commit of the branch that passed
# Swarm's AI review, builds it while work runs and restarts Swarm into it only
# when nothing runs, going back to the build before if it does not come up
# (`install.sh auto-update on`, or --auto-update, runs it every 5 minutes;
# off by default); `install.sh channel BRANCH` switches the branch it
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
# Run by sh (the launcher's old `curl ... | sh` line): say how to run it
# instead of failing on the first bash-only line. POSIX on purpose.
if [ -z "${BASH_VERSION:-}" ]; then
  echo "This is Swarm's box installer; it runs with bash as root:" >&2
  echo "  curl -fsSL https://swarmagent.dev/install | sudo bash" >&2
  echo "The desktop and terminal launcher installs with https://raw.githubusercontent.com/swarm-agent/swarm/main/install.sh" >&2
  exit 1
fi
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
STARTUP_CONFIG=/etc/swarmd/swarm.conf
GVISOR_LIST=/etc/apt/sources.list.d/gvisor.list
# This installer as kept in the checkout, for the commands it prints. An
# install of released builds keeps it at $STATE/install.sh instead (load_conf).
INSTALLER=$SRC/containers/headless/app/install.sh
# Released builds: the newest GitHub release of Swarm, and the release (tag and
# commit) this install was last pointed at.
RELEASE_REPO=swarm-agent/swarm
RELEASE_TARGET=$STATE/release-target
SWARMCTL=/usr/local/bin/swarmctl
# Everything Swarm serves on this machine listens on loopback.
LOCAL_APP_ORIGIN=http://127.0.0.1:8443
LOCAL_MCP_URL=http://127.0.0.1:7783/mcp
# Revocation IDs of the AI keys connect-ai gave each person (root-only).
AI_KEY_IDS=$STATE/ai-keys

# The earlier layout ran Swarm itself inside a container; reinstall/uninstall
# remove what it left behind.
LEGACY_CONTAINER=swarm
LEGACY_NETWORK=swarm-net
LEGACY_BRIDGE=br-swarm

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() {
  printf 'error: %s\n' "$*" >&2
  [[ $PROGRESS == json ]] && printf '{"swarm_progress":{"error":%s}}\n' "$(json_str "$*")"
  exit 1
}

# --progress=json (any command, any position) adds one JSON line per step on
# stdout for an app that drives the installer through pkexec or ssh, where
# extra file descriptors and the environment do not survive:
#   {"swarm_progress":{"step":"build","text":"Building Swarm ..."}}
#   {"swarm_progress":{"step":"done","text":"..."}}
#   {"swarm_progress":{"error":"..."}}
# Step ids, in install order: prerequisites, fetch, service-user, build, start,
# isolation, connect-ai, connect-app, done. Texts never carry secrets.
PROGRESS=off
json_str() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\t'/ }
  s=${s//$'\r'/}
  printf '"%s"' "$s"
}
step() {
  local id=$1
  shift
  say "$*"
  [[ $PROGRESS == json ]] && printf '{"swarm_progress":{"step":%s,"text":%s}}\n' "$(json_str "$id")" "$(json_str "$*")"
  return 0
}

# The account that asked for this: sudo sets SUDO_USER, pkexec (the desktop
# password prompt an app uses) sets PKEXEC_UID.
invoking_user() {
  if [[ -n ${SUDO_USER:-} ]]; then
    printf '%s' "$SUDO_USER"
  elif [[ ${PKEXEC_UID:-} =~ ^[0-9]+$ ]]; then
    id -nu "$PKEXEC_UID" 2>/dev/null || true
  fi
}

usage() {
  cat <<'USAGE'
Usage: install.sh [install] [--for-user NAME] [--relay URL] [--name NAME] [--ref BRANCH] [--no-ai-access]
                  [--environments on|off] [--secrets-gateway on|off] [--ask-permissions] [--sandbox on|off] [--auto-update] [--tailnet [--tag TAG] [--lock-ssh]]
                  [--app NAME [--app-access read|write|full]] [--progress=json]
       install.sh reinstall [--yes] [--delete-projects] [install options]
       install.sh uninstall [--yes] [--delete-projects]
       install.sh update | up | reset-login | status | check-isolation | check-sandbox
       swarmctl idle-off MINUTES|off|status
       install.sh auto-update [on|off] [--checks CONTEXT=APP[,...]|none] [--hook PATH]
       install.sh dev-sync /path/to/checkout | off
       install.sh channel [BRANCH]
       install.sh connect-ai [--for-user NAME] [--access write|full]
       install.sh connect-app NAME [--for-user NAME] [--access read|write|full]
       install.sh disconnect-app NAME [--for-user NAME]
       install.sh key PROVIDER                      (the API key on stdin)
       install.sh secret set NAME [--hosts HOST,...] (the value on stdin)
       install.sh secret grant NAME FOLDER --days N | --hours N
       install.sh daemon-env list | set NAME... | unset NAME...
       install.sh claude-code
       install.sh tailnet on [--tag TAG] [--lock-ssh] | off
       install.sh tailnet-identity on|off
       install.sh secrets-gateway on|off
       install.sh permissions auto|ask
       install.sh environments on|off
       install.sh sandbox on|off
       swarmctl workspace add PATH [PATH...] [--for-user NAME] | remove PATH | list
       swarmctl workspace publish PATH COMMAND [--sync] | publish PATH --off

  --for-user NAME  the person this install is for (default: the account that
                ran sudo); used for the AI connection and workspace folders
  --relay URL   your Swarm Control relay (prefills "Connect to Claude")
  --name NAME   Swarm machine name, and tailnet machine name (default: swarm)
  --ref BRANCH  build this branch from source (with Docker) instead of
                downloading the newest release (the default); for
                development machines (dev changes several times a day)
  --release-file ARCHIVE
                install this release archive (swarm-vX.Y.Z-linux-amd64.tar.gz,
                its .sha256 next to it) instead of downloading the newest;
                later updates download releases as usual
  --environments on|off
                off (the default): no Docker; agents run every command on
                this machine. on: installs Docker and gVisor so a folder
                can have an environment (its own container for its agents'
                commands, with its secrets)
  --no-ai-access  do not run Swarm's MCP (127.0.0.1:7783, and :8444 on the
                tailnet); connect-ai then has nothing to connect to
  --secrets-gateway on|off
                inject granted secrets through the egress gateway (on with
                environments, which it needs; see secrets-gateway below)
  --auto-update update by itself (install.sh auto-update below; off by default)
                Updates drain new runs before activation, never kill running jobs.
                UPDATE_DRAIN_SECONDS=7200 sets the drain limit (1..86400 seconds).
Expiry or installer failure reopens admissions; swarm_cancel_update cancels a drain.
Readiness is generation-bound and checked again immediately before restart.
                Timeout reports blockers and leaves admission held until restart.
  --ask-permissions
                agents ask before commands and file changes; by default they
                run without prompts
  --sandbox on|off
                off (the default): agents run in Swarm on this machine; on:
                their commands run in one sandbox per project
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
  connect-app NAME give an app (a console, a dashboard, your own frontend) its
                   own key in ~/.config/swarm/apps/NAME.key (that person only,
                   0600; write by default, --access read|write|full), separate
                   from the AI key. Running it again replaces that app's key.
  disconnect-app NAME  revoke the app's key and remove its file
  --app NAME       (install) also connect this app at the end of the install
  --progress=json  (any command) one JSON line per step on stdout, for apps
                   driving the installer; see docs/frontends.md
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
                   auto (the default): agents run without prompts, sandbox
                   or not; ask: they ask first. Applied at every start of
                   Swarm.
  environments on|off
                   on: install Docker and gVisor (once) so folders can have
                   environments with their secrets; turns the secret gateway
                   on. off: stop using them (Docker stays installed).
  sandbox on|off   off (the default): agents run in Swarm on this machine;
                   on: their commands run in one sandbox per project (this
                   turns environments on).

  reinstall / uninstall remove Swarm's services, binaries, sandboxes and state
  (owner, provider sign-in, AI keys, relay pairing, agent worktrees), its
  update timer and its Tailscale Serve entries. reinstall then installs
  again, reusing the saved --relay, --name and --ref unless you pass new ones.
  --yes              do not ask for confirmation
  --delete-projects  also delete the project folder (your workspaces)

  update           download the newest release (a source install: take the
                   newest commit of the branch it follows that passed its
                   checks (UPDATE_CHECKS, by default Swarm's
                   AI review on GitHub; a failed check blocks the commit,
                   and build it), while work runs, restart Swarm into it only when
                   nothing runs (no agent run, worker run or environment
                   build), and go back to the build before if it does not
                   come up within two minutes. The machine summary shows the
                   update state (machine.update) and what runs (busy).
  auto-update [on|off] [--checks CONTEXT=APP[,...]|none] [--hook PATH]
                   show automatic updates, or turn them on or off (off by
                   default): on runs update every 5 minutes. --checks names
                   the commit statuses a commit needs and the GitHub App that
                   must post each (default ai-review=swarm-go-ai-review[bot];
                   none updates without checks). --hook names a root-owned
                   command told when a commit is installed, blocked or
                   rolled back (SWARM_UPDATE_EVENT, _COMMIT, _RUNNING,
                   _REASON, _CHANNEL, _MACHINE).
  channel [BRANCH] show the branch this machine follows, or switch to BRANCH
                   (main or dev) without reinstalling; the next update builds
                   it once it is ahead of the running build
  daemon-env       settings for Swarm itself, kept in a root-only file and
                   applied with one restart: `list` shows names only,
                   `set NAME...` reads one value per name from stdin,
                   `unset NAME...` removes them
  claude-code      set up the Claude Code CLI for Swarm's own user: find it
                   (or install it with Anthropic's installer into
                   ~swarm/.local/bin), point Swarm at it
                   (SWARM_CLAUDE_CODE_CLI_PATH) and, in a terminal, start
                   `claude auth login` as that user; you finish the sign-in
                   in your browser. Swarm never reads or copies Claude
                   credentials. Safe to run again: it reports what is set
                   up. Then your AI makes Claude Code Swarm's provider
                   (swarm_connect_claude_code action activate).
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
  workspace add PATH [PATH...] [--for-user NAME] | remove PATH | list
                   make a folder the person owns (a Git repository or not)
                   visible and writable to
                   Swarm and its agent sandboxes at its real path (a unit
                   drop-in binds exactly that folder; Swarm's user gets
                   access to it; the owner keeps full rights on what Swarm
                   creates), then restart Swarm. The person or the AI setting
                   the machine up runs this as root; Swarm never changes the
                   machine itself. Then register the folder in place with
                   swarm_add_workspace. remove reverses it.
  workspace publish PATH COMMAND [--sync]
                   deliver jobs in PATH as pull requests instead of merging
                   them into the folder: Swarm runs COMMAND (root-owned,
                   outside home folders) on this machine with the job in
                   SWARM_PUBLISH_FOLDER, _JOB_ID, _BRANCH, _HEAD, _BASE_BRANCH,
                   _TITLE and _BODY_FILE; its last output line is the pull
                   request URL. A pull request is opened only when a job is
                   delivered (swarm_deliver_job). --sync starts each job from
                   the forge's latest base branch (COMMAND with
                   SWARM_PUBLISH_ACTION=update fetches it and prints COMMIT
                   BRANCH). publish PATH --off goes back to merging.
USAGE
}

load_conf() {
  [[ -f $CONF ]] || die "Swarm is not installed on this machine ($CONF is missing)"
  # shellcheck disable=SC1090
  . "$CONF"
  if release_install; then INSTALLER=$STATE/install.sh; fi
}

# Released builds (SOURCE=release, the default for new installs) or a branch
# built from source (SOURCE=git: --ref, dev machines, installs from before).
release_install() { [[ ${SOURCE:-git} == release ]]; }

# Environments and the sandbox use Docker (and gVisor); off, Docker is not
# needed at all. Installs from before this setting had Docker: on.
environments_on() { [[ ${ENVIRONMENTS:-on} == on ]]; }

# The sandbox image's build context and firewall script: in the checkout, or in
# a released build (the running one unless RELEASE is given).
sandbox_dir() {
  if release_install; then echo "${1:-$OPT/current}/share/sandbox"; else echo "$SRC/containers/sandbox"; fi
}

# The commit to build or run: the checkout's HEAD, or the release this install
# was last pointed at (latest_release).
target_commit() {
  if release_install; then
    [[ -f $RELEASE_TARGET ]] || die "no release chosen yet; run: sudo bash $INSTALLER update"
    awk '{print $2}' "$RELEASE_TARGET"
  else
    git -C "$SRC" rev-parse HEAD
  fi
}

# Point this install at the newest release on GitHub ("TAG COMMIT").
latest_release() {
  local body tag commit
  body=$(curl -fsS --max-time 30 -H 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/$RELEASE_REPO/releases/latest") || return 1
  tag=$(jq -r '.tag_name // empty' <<<"$body") || return 1
  commit=$(jq -r '.target_commitish // empty' <<<"$body") || return 1
  [[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ && $commit =~ ^[0-9a-f]{40}$ ]] || return 1
  printf '%s %s\n' "$tag" "$commit" >"$RELEASE_TARGET"
}

# Docker (with buildx) and gVisor, for environments, the sandbox and source
# builds. A working Docker already on the machine stays.
install_docker() {
  # Snap's Docker is confined: it cannot bind-mount project folders outside
  # home or take the gVisor runtime, so agent sandboxes cannot run on it, and
  # installing the distribution's Docker beside it leaves two daemons fighting
  # over one socket.
  if command -v snap >/dev/null && snap list docker >/dev/null 2>&1; then
    die "this machine runs Docker from snap, which cannot run Swarm's environments or sandbox (it cannot mount project folders or use gVisor). Remove it first with: sudo snap remove docker (this deletes the containers, images and volumes it holds), then run this again"
  fi
  say "Installing Docker and gVisor"
  if command -v docker >/dev/null && docker buildx version >/dev/null 2>&1; then
    echo "Using the Docker already on this machine ($(docker --version))"
  else
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io docker-buildx >/dev/null
  fi
  systemctl enable --now docker >/dev/null
  install_gvisor
  register_gvisor
  if id "$SERVICE_USER" >/dev/null 2>&1; then usermod -aG docker "$SERVICE_USER"; fi
}

# Build the sandbox image for COMMIT from CONTEXT (environments and sandbox).
build_sandbox_image() {
  docker build --platform linux/amd64 -t "swarm-sandbox:$1" "$2"
}

# Download a released build (its archive and checksum from GitHub), check it,
# and unpack its headless package as the release for COMMIT. Nothing is
# compiled. With environments on, also build the sandbox image from it.
fetch_release() {
  local tag=$1 commit=$2 release=$OPT/releases/$2 name url dir
  if [[ ! -f $release/.complete ]]; then
    name=swarm-$tag-linux-amd64.tar.gz
    url=https://github.com/$RELEASE_REPO/releases/download/$tag
    say "Downloading Swarm $tag"
    rm -rf "$STAGE"
    install -d -m 0700 "$STAGE"
    if [[ -n ${RELEASE_FILE:-} && $(basename "$RELEASE_FILE") == "$name" ]]; then
      # --release-file: the archive and its checksum file are already here.
      cp "$RELEASE_FILE" "$STAGE/$name" && cp "$RELEASE_FILE.sha256" "$STAGE/$name.sha256" ||
        die "$RELEASE_FILE needs its checksum file next to it ($name.sha256)"
    else
      curl -fsSL --retry 3 -o "$STAGE/$name" "$url/$name" || die "could not download $url/$name"
      curl -fsSL --retry 3 -o "$STAGE/$name.sha256" "$url/$name.sha256" || die "could not download $url/$name.sha256"
    fi
    (cd "$STAGE" && sha256sum --check --quiet "$name.sha256") || die "the download of Swarm $tag does not match its published checksum"
    tar -C "$STAGE" -xzf "$STAGE/$name" "swarm-$tag-linux-amd64/build-info.txt" "swarm-$tag-linux-amd64/headless" ||
      die "Swarm $tag has no headless build (it predates downloadable installs); a newer release is needed"
    dir=$STAGE/swarm-$tag-linux-amd64
    grep -qx "commit=$commit" "$dir/build-info.txt" || die "the release $tag was not built from ${commit:0:9}"
    [[ -x $dir/headless/bin/swarmd ]] || die "the release $tag has no headless daemon"
    install -d -m 0755 "$OPT/releases"
    rm -rf "$release.new" && mv "$dir/headless" "$release.new"
    chmod -R u=rwX,go=rX "$release.new"
    touch "$release.new/.complete"
    rm -rf "$release" && mv "$release.new" "$release"
    rm -rf "$STAGE"
  fi
  if environments_on && ! docker image inspect "swarm-sandbox:$commit" >/dev/null 2>&1; then
    build_sandbox_image "$commit" "$(sandbox_dir "$release")"
  fi
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
    echo "swarm: agents now run without permission prompts (install.sh permissions ask turns them back on)"
  fi
  # Installs from before released builds built from source with Docker, so
  # they keep doing that, with environments on.
  if [[ -z ${SOURCE:-} ]]; then
    SOURCE=git
    conf_set SOURCE git
  fi
  if [[ -z ${ENVIRONMENTS:-} ]]; then
    ENVIRONMENTS=on
    conf_set ENVIRONMENTS on
  fi
  if [[ -z ${SANDBOX:-} ]]; then
    SANDBOX=off
    conf_set SANDBOX off
    echo "swarm: the agent sandbox is off: agents run in Swarm on this machine (install.sh sandbox on turns it on)"
  fi
  # The removed runner listener's port: units are written without it.
  if [[ -n ${RUNNER_GATEWAY_PORT+set} ]]; then
    sed -i '/^RUNNER_GATEWAY_PORT=/d' "$CONF"
    unset RUNNER_GATEWAY_PORT
    echo "swarm: dropped the removed RUNNER_GATEWAY_PORT setting"
  fi
  if [[ -z ${AUTO_UPDATE:-} ]]; then
    AUTO_UPDATE=off
    conf_set AUTO_UPDATE off
    echo "swarm: automatic updates are now opt-in and off for this install; install.sh auto-update on turns them on (they install only commits that passed Swarm's AI review, and restart Swarm only when nothing runs)"
  fi
}

# The person an install, AI key or workspace is for: --for-user NAME, else the
# account that ran sudo. Never root, Swarm's own user or a system account.
person() {
  local name=${1:-$(invoking_user)}
  [[ -n $name ]] || die "cannot tell who this is for: run with sudo (or pkexec) from your own account, or pass --for-user NAME"
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
    bash "$(sandbox_dir "${1:-}")/firewall.sh"
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
  if ! environments_on; then
    echo "Environments are off: no sandbox or environment runs here, so there is nothing to isolate (install.sh environments on turns them on)."
    return 0
  fi
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
  if ! command -v docker >/dev/null; then
    echo "No Docker on this machine: environments and the sandbox are off."
    return 0
  fi
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
# agent run in progress; `update` instead builds first and switches only when
# nothing runs.
up() {
  load_conf
  local commit
  commit=$(target_commit)
  build_release "$commit"
  activate "$commit"
  clean_up_after_switch
}

# The commit the services run (the release $OPT/current points at).
running_commit() {
  local target
  target=$(readlink -f "$OPT/current" 2>/dev/null) || return 0
  if [[ -d $target ]]; then basename "$target"; fi
}

# Build a commit of the checkout into its own release and sandbox image
# (swarm-sandbox:COMMIT) next to the running one; the services keep running
# and nothing switches yet. A release already built is kept.
build_release() {
  local commit=$1 release=$OPT/releases/$1
  if release_install; then
    [[ $(awk '{print $2}' "$RELEASE_TARGET" 2>/dev/null) == "$commit" ]] || die "${commit:0:9} is not the release this install is pointed at"
    step build "Downloading Swarm $(awk '{print $1}' "$RELEASE_TARGET")"
    fetch_release "$(awk '{print $1}' "$RELEASE_TARGET")" "$commit"
    return
  fi
  [[ -f $release/.complete ]] && { ! environments_on || docker image inspect "swarm-sandbox:$commit" >/dev/null 2>&1; } && return 0
  step build "Building Swarm ${SWARM_REF}@${commit:0:9} (the first build takes several minutes)"
  rm -rf "$STAGE"
  docker build --platform linux/amd64 -f "$SRC/containers/headless/app/Dockerfile.host" \
    --build-arg VERSION="$SWARM_REF" --build-arg COMMIT="$commit" \
    --build-arg BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o "type=local,dest=$STAGE" "$SRC"
  if environments_on; then build_sandbox_image "$commit" "$SRC/containers/sandbox"; fi
  install -d -m 0755 "$OPT/releases"
  rm -rf "$release.new" && mv "$STAGE" "$release.new"
  # The exporter creates its top directory 0700; Swarm runs as its own user.
  chmod -R u=rwX,go=rX "$release.new"
  touch "$release.new/.complete"
  rm -rf "$release" && mv "$release.new" "$release"
}

# Switch the services to a built release and restart them (this stops any
# agent run in progress). The release that ran before stays as
# $OPT/previous, so an update that does not come up can go back to it.
cleanup_update_drain() {
  [[ -n ${UPDATE_DRAIN_GENERATION:-} ]] || return 0
  if [[ -n ${UPDATE_DRAIN_CLAIM:-} ]]; then
    local_api /v3/box/drain -X PUT -H 'Content-Type: application/json' -d "{\"generation\":\"$UPDATE_DRAIN_GENERATION\",\"claim\":\"$UPDATE_DRAIN_CLAIM\",\"release\":true}" >/dev/null || echo 'swarm: reservation cleanup failed; operator intervention required' >&2
  else
    local_api "/v3/box/drain?generation=$UPDATE_DRAIN_GENERATION" -X DELETE >/dev/null || echo 'swarm: drain cleanup unavailable' >&2
  fi
}

# Query the candidate without loading the installed config. Preserve all other
# lines and file ownership/mode; retain an exact dated backup before replacement.
migrate_removed_startup_keys() (
  local release=$1 keys key tmp backup
  [[ -f $STARTUP_CONFIG ]] || return 0
  [[ ! -L $STARTUP_CONFIG ]] || return 1
  keys=$(LD_LIBRARY_PATH="$release/lib" timeout 20 "$release/bin/swarmd" config removed-keys) || return 1
  [[ -n $keys ]] || return 0
  while IFS= read -r key; do
    [[ $key =~ ^[a-z][a-z0-9_]*$ ]] || return 1
  done <<<"$keys"
  tmp=$(mktemp "${STARTUP_CONFIG}.rewrite.XXXXXX") || return 1
  trap 'rm -f "$tmp"' EXIT
  cp -p "$STARTUP_CONFIG" "$tmp" || return 1
  awk -v keys="$keys" 'BEGIN { n=split(keys,a,"\n"); for(i=1;i<=n;i++) removed[a[i]]=1 }
    { key=$0; sub(/=.*/,"",key); gsub(/^[[:space:]]+|[[:space:]]+$/,"",key) }
    !($0 ~ /=/ && key in removed) { print }' "$STARTUP_CONFIG" >"$tmp" || return 1
  cmp -s "$STARTUP_CONFIG" "$tmp" && return 0
  backup=$(mktemp "${STARTUP_CONFIG}.removed-keys-$(date -u +%Y%m%dT%H%M%SZ).XXXXXX") || return 1
  cp -p "$STARTUP_CONFIG" "$backup" || return 1
  mv -f "$tmp" "$STARTUP_CONFIG"
)

activate() {
  local commit=$1 release=$OPT/releases/$1 old
  [[ -x $release/bin/swarmd ]] || die "release ${commit:0:9} is not built"
  if [[ -n ${UPDATE_DRAIN_GENERATION:-} ]]; then
    UPDATE_DRAIN_CLAIM=$(cat /proc/sys/kernel/random/uuid) || return 1
    local reservation
    reservation=$(local_api /v3/box/drain -X PUT -H 'Content-Type: application/json' -d "{\"generation\":\"$UPDATE_DRAIN_GENERATION\",\"claim\":\"$UPDATE_DRAIN_CLAIM\"}") || return 1
    [[ $(json_get reserved <<<"$reservation") == true ]] || return 1
  fi
  old=$(running_commit) || return 1
  if [[ -n $old && $old != "$commit" ]]; then
    # Installs from before per-commit sandbox images kept only the :local tag.
    if environments_on; then
      docker image inspect "swarm-sandbox:$old" >/dev/null 2>&1 ||
        docker tag "$SANDBOX_IMAGE" "swarm-sandbox:$old" 2>/dev/null || true
    fi
    ln -sfn "$OPT/releases/$old" "$OPT/previous.new" && mv -T "$OPT/previous.new" "$OPT/previous" || return 1
  fi
  if environments_on; then docker tag "swarm-sandbox:$commit" "$SANDBOX_IMAGE" || return 1; fi
  # Every install keeps its installer at $STATE/install.sh, the one path the
  # docs and Swarm's own answers name: a released build carries its own (later
  # updates use that copy); a source install copies the checkout's.
  if release_install; then
    install -m 0755 "$release/share/install.sh" "$STATE/install.sh" || return 1
  elif [[ -f $SRC/containers/headless/app/install.sh ]]; then
    install -m 0755 "$SRC/containers/headless/app/install.sh" "$STATE/install.sh" || return 1
  fi
  # Switch current only after preparation succeeds; never prune rollback data here.
  install -m 0755 /dev/stdin "$SWARMCTL" <<WRAPPER || return 1
#!/bin/sh
# swarmctl talks to Swarm's private socket, so it runs as Swarm's own user.
# The machine commands (workspace, idle-off) need root itself, so root runs them directly.
if [ "\$(id -un)" = $SERVICE_USER ]; then exec $OPT/current/bin/swarmctl "\$@"; fi
if [ "\$(id -u)" = 0 ]; then case "\${1:-}" in workspace|idle-off) exec env LD_LIBRARY_PATH=$OPT/current/lib $OPT/current/bin/swarmctl "\$@" ;; esac; fi
exec runuser -u $SERVICE_USER -- env HOME=$SERVICE_HOME LD_LIBRARY_PATH=$OPT/current/lib $OPT/current/bin/swarmctl "\$@"
WRAPPER

  if environments_on; then
    firewall "$release" || return 1
    register_gvisor || return 1
    choose_runtime || return 1
  else
    SANDBOX_RUNTIME=default
  fi
  sed -i '/^SANDBOX_RUNTIME=/d' "$CONF" || return 1
  echo "SANDBOX_RUNTIME=$SANDBOX_RUNTIME" >>"$CONF" || return 1
  install_units "$release" || return 1
  ln -sfn "$release" "$OPT/current.new" && mv -T "$OPT/current.new" "$OPT/current" || return 1
  UPDATE_SWITCHED=true
  systemctl restart "$DAEMON_UNIT" || return 1
  systemctl restart "$APP_UNIT" || return 1
  if release_install; then
    echo "Swarm is running: $(awk '{print $1}' "$RELEASE_TARGET") (${commit:0:9})"
  else
    echo "Swarm is running: ${SWARM_REF}@${commit:0:9}"
  fi
}

# Keep the running release, the one before it and any built release waiting
# to start; remove the others and their sandbox images. Without a running
# release nothing is removed.
prune_releases() {
  local keep=() dir commit
  [[ -n $(running_commit) ]] || return 0
  for dir in "$OPT/current" "$OPT/previous"; do
    [[ -e $dir ]] && keep+=("$(basename "$(readlink -f "$dir")")")
  done
  commit=$(target_commit 2>/dev/null) && keep+=("$commit")
  for dir in "$OPT/releases"/*; do
    [[ -d $dir ]] || continue
    commit=$(basename "$dir")
    [[ $commit =~ ^[0-9a-f]{40}$ ]] || continue
    [[ " ${keep[*]} " == *" $commit "* ]] && continue
    rm -rf "$dir"
    if command -v docker >/dev/null; then docker image rm "swarm-sandbox:$commit" >/dev/null 2>&1 || true; fi
  done
}

# Trim Docker's build cache to BUILD_CACHE_MAX (install.env; default 8gb,
# none keeps it all), newest kept first, and remove dangling images. Every
# build adds cache that nothing else removes.
prune_build_cache() {
  local max=${BUILD_CACHE_MAX:-8gb}
  command -v docker >/dev/null || return 0
  [[ $max == none ]] && return 0
  if [[ ! $max =~ ^[0-9]+(b|kb|mb|gb)$ ]]; then
    echo "BUILD_CACHE_MAX must look like 8gb, or none; Docker build cache left as it is" >&2
    return 0
  fi
  if docker builder prune --help 2>/dev/null | grep -q -- --max-used-space; then
    docker builder prune -af --max-used-space "$max" >/dev/null 2>&1 || true
  else
    docker builder prune -af --keep-storage "$max" >/dev/null 2>&1 || true
  fi
  docker image prune -f >/dev/null 2>&1 || true
}

# After a release has been switched to and came up: remove old releases and
# trim build cache, so updates cannot fill the disk. Never fails the caller.
clean_up_after_switch() {
  prune_releases || true
  prune_build_cache || true
}

# Development: build the committed HEAD of a checkout on this machine instead
# of the branch on GitHub, so a change can be tried without pushing it. The
# machine stays on that checkout (the update timer leaves it alone) until
# `dev-sync off` returns it to the branch.
dev_sync() {
  load_conf
  release_install && die "this machine installs released builds; to build from a checkout, reinstall it from source: sudo bash $INSTALLER reinstall --ref dev"
  local repo=${1:?usage: install.sh dev-sync /path/to/checkout | off}
  if [[ $repo == off ]]; then
    sed -i '/^DEV_SOURCE=/d' "$CONF"
    echo "Following $SWARM_REF on GitHub again; the next update brings it back."
    return 0
  fi
  repo=$(realpath "$repo")
  [[ -d $repo/.git && -f $repo/containers/headless/app/install.sh ]] || die "$repo is not a Swarm checkout"
  # The checkout belongs to another user; trust it for this one fetch only.
  # A local fetch reads it in a separate `git upload-pack`, which does not see
  # `-c` given to this git, so the trust goes to upload-pack itself: the git
  # directory, and the worktree for older git. The command runs in a shell.
  local upload_pack
  upload_pack="git -c safe.directory=$(printf %q "$repo/.git") -c safe.directory=$(printf %q "$repo") upload-pack"
  git -C "$SRC" fetch -q --upload-pack="$upload_pack" "$repo" HEAD || die "could not read $repo"
  sed -i '/^DEV_SOURCE=/d' "$CONF" && echo "DEV_SOURCE=$repo" >>"$CONF"
  git -C "$SRC" reset -q --hard FETCH_HEAD
  echo "swarm: building $(git -C "$SRC" rev-parse --short HEAD) from $repo"
  exec bash "$SRC/containers/headless/app/install.sh" apply-now
}

# ---- Updates ------------------------------------------------------------------
# Automatic updates are opt-in: `install.sh auto-update on` starts a 5-minute
# timer that runs `install.sh update`, which can also be run by hand. An update
# takes the newest commit of the branch this machine follows only when
#  - it contains the running build (never a downgrade, which Swarm's stored
#    state may not survive);
#  - it passed every check in UPDATE_CHECKS on GitHub, each posted by the
#    named GitHub App (by default Swarm's own AI review: ai-review posted by
#    swarm-go-ai-review[bot]); a release merge that adds nothing to the commit
#    it merged (dev into main) counts as that commit. A failed check blocks the
#    commit; a missing one waits for it;
# then builds it next to the running build while work goes on, and restarts
# Swarm into it only when nothing runs (GET /v3/box/busy: agent runs, worker
# runs, environment builds). If the new build does not come up (ready, its
# sandbox on as before, the setup page answering) within two minutes, Swarm goes
# back to the build it ran before and never retries that commit. Each step is
# written to $UPDATE_STATE, which the machine summary (and its status in the
# outbox) shows; with UPDATE_HOOK set to a root-owned command, it is also told
# when a commit is installed, blocked or rolled back.
UPDATE_STATE=$OPT/update-state.json
UPDATE_BAD=$STATE/update-bad
DEFAULT_UPDATE_CHECKS='ai-review=swarm-go-ai-review[bot]'
LOCAL_API_SOCKET=/var/lib/swarmd/local-transport/api.sock

local_api() { local path=$1; shift; curl -fsS --max-time 20 --unix-socket "$LOCAL_API_SOCKET" "http://swarm$path" "$@"; }

# True when systemd says the daemon keeps exiting: waiting to restart after a
# crash, or failed. A daemon an operator stopped is not crashing.
daemon_crashing() {
  local active sub
  active=$(systemctl show -p ActiveState --value "$DAEMON_UNIT" 2>/dev/null) || return 1
  sub=$(systemctl show -p SubState --value "$DAEMON_UNIT" 2>/dev/null) || return 1
  [[ $active == failed || $sub == auto-restart ]]
}

# Print one field (a.b.c) of the JSON on stdin, with the node of the running
# release; nothing when it is absent.
json_get() {
  "$OPT/current/bin/node" -e '
    let v; try { v = JSON.parse(require("fs").readFileSync(0, "utf8")); } catch { process.exit(1); }
    for (const k of process.argv[1].split(".")) v = v == null ? undefined : v[k];
    process.stdout.write(v === undefined || v === null ? "" : String(v));' "$1"
}

# Record the update state for the machine summary; tell UPDATE_HOOK when the
# state of a commit changes to installed, blocked, rolled_back or failed.
update_state() {
  local state=$1 reason=${2//[\"\\]/} candidate=${3:-} before=''
  [[ -f $UPDATE_STATE ]] && before=$(json_get state <"$UPDATE_STATE" || true)/$(json_get candidate <"$UPDATE_STATE" || true)
  printf '{"auto":"%s","channel":"%s","state":"%s","reason":"%s","running":"%s","candidate":"%s","at":%s}\n' \
    "${AUTO_UPDATE:-off}" "$SWARM_REF" "$state" "$reason" "$(running_commit)" "$candidate" "$(date +%s%3N)" >"$UPDATE_STATE.new"
  chmod 0644 "$UPDATE_STATE.new" && mv "$UPDATE_STATE.new" "$UPDATE_STATE"
  echo "swarm: $reason"
  [[ $before == "$state/$candidate" ]] || update_hook "$state" "$reason" "$candidate"
}

update_hook() {
  local hook=${UPDATE_HOOK:-} mode
  [[ -n $hook ]] || return 0
  case $1 in installed | blocked | rolled_back | failed) ;; *) return 0 ;; esac
  mode=$(stat -c '%u %a' "$hook" 2>/dev/null || true)
  if [[ $hook != /* || ! -f $hook || ${mode%% *} != 0 ]] || ((8#${mode##* } & 8#022)); then
    echo "swarm: UPDATE_HOOK $hook is not a file only root can change; not run" >&2
    return 0
  fi
  SWARM_UPDATE_EVENT=$1 SWARM_UPDATE_REASON=$2 SWARM_UPDATE_COMMIT=$3 SWARM_UPDATE_RUNNING=$(running_commit) \
    SWARM_UPDATE_CHANNEL=$SWARM_REF SWARM_UPDATE_MACHINE=${APP_DEVICE_NAME:-} timeout 60 "$hook" </dev/null ||
    echo "swarm: UPDATE_HOOK $hook failed" >&2
}

# owner/name of the Swarm checkout's origin on GitHub.
github_repo() {
  local url
  url=$(git -C "$SRC" remote get-url origin) || return 1
  [[ $url =~ ^(https://github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)$ ]] || return 1
  echo "${BASH_REMATCH[2]%.git}"
}

# The newest state of check CONTEXT that the GitHub App CREATOR posted on
# COMMIT: success, failure, error, pending, none or unknown (GitHub did not
# answer). Statuses of a public repository need no sign-in.
check_state() {
  local repo=$1 commit=$2 context=$3 creator=$4 body
  body=$(curl -fsS --max-time 20 -H 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/$repo/commits/$commit/statuses?per_page=100") || { echo unknown; return 0; }
  "$OPT/current/bin/node" -e '
    const [context, creator] = process.argv.slice(1);
    let list; try { list = JSON.parse(require("fs").readFileSync(0, "utf8")); } catch { list = null; }
    if (!Array.isArray(list)) { process.stdout.write("unknown"); process.exit(0); }
    const s = list.find(s => s && s.context === context && s.creator && s.creator.login === creator);
    process.stdout.write(s ? String(s.state) : "none");' "$context" "$creator" <<<"$body"
}

# Whether COMMIT passed every check in UPDATE_CHECKS; prints why not and
# returns 1 when a check failed (blocked), 2 while one is missing.
checks_passed() {
  local commit=$1 checks=${UPDATE_CHECKS:-$DEFAULT_UPDATE_CHECKS} repo target spec context creator state
  local -a specs
  [[ $checks == none ]] && return 0
  repo=$(github_repo) || { echo "the Swarm checkout's origin is not on GitHub, so its checks cannot be read (UPDATE_CHECKS=none updates without them)"; return 2; }
  target=$commit
  if git -C "$SRC" rev-parse -q --verify "$commit^2" >/dev/null && git -C "$SRC" diff --quiet "$commit^2" "$commit"; then
    target=$(git -C "$SRC" rev-parse "$commit^2")
  fi
  IFS=, read -ra specs <<<"$checks"
  for spec in "${specs[@]}"; do
    context=${spec%%=*} creator=${spec#*=}
    state=$(check_state "$repo" "$target" "$context" "$creator")
    case $state in
      success) ;;
      failure | error) echo "$context failed on ${target:0:9}, so it is not installed"; return 1 ;;
      *) echo "waiting for $context on ${target:0:9}"; return 2 ;;
    esac
  done
}

# Whether a restart now would interrupt work; prints what runs.
swarm_busy() {
  local out
  systemctl is-active -q "$DAEMON_UNIT" || return 1
  if out=$(local_api /v3/box/busy 2>/dev/null); then
    [[ $(json_get busy <<<"$out") == false ]] && return 1
    echo "$(json_get sessions <<<"$out") agent runs, $(json_get worker_runs <<<"$out") worker runs and $(json_get environment_builds <<<"$out") environment builds are in progress"
    return 0
  fi
  echo "daemon status unavailable; refusing to assume idle"
  return 0
}

# Restart Swarm for a setting change only when nothing runs: a restart ends
# agent runs in progress. Waits up to RESTART_WAIT_SECONDS (15 min); still busy
# then, the change stays saved and takes effect at Swarm's next restart.
restart_daemon_when_idle() {
  local work waited=0 limit=${RESTART_WAIT_SECONDS:-900}
  while work=$(swarm_busy); do
    ((waited < limit)) || die "Swarm was not restarted: it is still working ($work). The change is saved and takes effect when Swarm next restarts; run the same command again once the work is done."
    ((waited > 0)) || echo "Swarm is working ($work); waiting until it is idle (up to $((limit / 60)) min) before restarting it."
    sleep 10
    waited=$((waited + 10))
  done
  systemctl restart "$DAEMON_UNIT"
}

# Wait until Swarm answers on its private socket (it starts after a restart).
wait_daemon() {
  for _ in $(seq 1 90); do
    "$SWARMCTL" setup status >/dev/null 2>&1 && return 0
    sleep 1
  done
  die "Swarm is not answering; check: journalctl -u $DAEMON_UNIT"
}

# Whether Swarm came up on COMMIT within two minutes: ready on its socket and
# running that build, its sandbox on when it was on before, the setup page
# answering.
came_up() {
  local commit=$1 sandboxed=$2 out running
  for _ in $(seq 1 60); do
    sleep 2
    systemctl is-active --quiet "$DAEMON_UNIT" || continue
    out=$(local_api /v3/box/summary 2>/dev/null) || continue
    running=$(json_get machine.commit <<<"$out") || continue
    [[ -z $running || $running == "$commit" ]] || continue
    [[ $sandboxed == true && $(json_get sandbox.active <<<"$out") != true ]] && continue
    curl -fsS -o /dev/null --max-time 5 -H "Host: ${APP_ORIGIN#*://}" http://127.0.0.1:8443/ 2>/dev/null || continue
    post_update_smoke || return 1
    return 0
  done
  return 1
}

# Use the authenticated owner transport, never read or copy provider credentials.
# Failure returns to finish_update's existing rollback path.
post_update_smoke() {
  local route
  for route in /healthz /v1/onboarding /v3/projects /v3/runs; do
    if ! local_api "$route" >/dev/null; then
      echo "swarm: post-update smoke failed: $route; restoring previous build" >&2
      return 1
    fi
    echo "swarm: post-update smoke $route: PASS"
  done
}

# A running build that no branch on GitHub contains any more was left behind
# by a rewritten branch: it can never come back, so moving to the branch's new
# head (once it passes its checks) is not a downgrade. A build still on some
# branch (an older channel) is not orphaned.
running_build_orphaned() {
  git -C "$SRC" fetch -q --prune origin '+refs/heads/*:refs/remotes/origin/*' || return 1
  [[ -z $(git -C "$SRC" branch -r --contains "$1" 2>/dev/null) ]]
}

update() {
  load_conf
  if release_install; then
    # Released builds: the newest release on GitHub. Releases only move
    # forward and passed Swarm's release checks, so nothing else is checked.
    if ! latest_release; then
      update_state waiting "GitHub did not say which release is newest; Swarm keeps running and asks again at the next update" ""
      return 0
    fi
    local next
    next=$(target_commit)
    if [[ $next != "$(running_commit)" && ! ( -f $UPDATE_BAD && $(cat "$UPDATE_BAD") == "$next" ) ]]; then
      update_state building "downloading $(awk '{print $1}' "$RELEASE_TARGET")" "$next"
      if ! bash "$INSTALLER" build-release "$next"; then
        update_state failed "downloading $(awk '{print $1}' "$RELEASE_TARGET") failed; Swarm keeps running and tries again at the next update" "$next"
        return 1
      fi
      # Continue in the new release's own installer, so its changes (units,
      # run flags) apply now.
      exec bash "$OPT/releases/$next/share/install.sh" finish-update
    fi
    finish_update
    return 0
  fi
  if [[ -n ${DEV_SOURCE:-} ]]; then
    echo "swarm: built from $DEV_SOURCE; not following $SWARM_REF (install.sh dev-sync off to resume)"
    finish_update
    return 0
  fi
  git -C "$SRC" fetch -q origin "$SWARM_REF"
  local head new why
  head=$(git -C "$SRC" rev-parse HEAD)
  new=$(git -C "$SRC" rev-parse FETCH_HEAD)
  if [[ $head != "$new" ]]; then
    # Only move forward: a branch that does not contain the running build (after
    # a channel switch to an older branch, or a rewritten branch) would be a
    # downgrade. A release merge (main) that added nothing to the branch it
    # merged is not a downgrade when that branch's merged commit is in the new
    # build: this is a switch from main to dev.
    if ! git -C "$SRC" merge-base --is-ancestor "$head" "$new" &&
      ! { git -C "$SRC" rev-parse -q --verify "$head^2" >/dev/null &&
        git -C "$SRC" merge-base --is-ancestor "$head^2" "$new" &&
        git -C "$SRC" diff --quiet "$head^2" "$head"; } &&
      ! running_build_orphaned "$head"; then
      update_state waiting "$SWARM_REF (${new:0:9}) does not contain the build ${head:0:9}; waiting until it does" "$new"
      return 0
    fi
    why=$(checks_passed "$new") || {
      case $? in
        1) update_state blocked "$why" "$new" ;;
        *) update_state waiting_review "$why" "$new" ;;
      esac
      return 0
    }
    git -C "$SRC" reset -q --hard "$new"
    echo "swarm: ${head:0:9} -> ${new:0:9} passed its checks"
    # Continue in the installer just fetched, not this already-loaded copy, so
    # changes to the installer itself (units, firewall, run flags) apply now.
    exec bash "$INSTALLER" finish-update
  fi
  finish_update
}

# Bring the services to the checkout's commit: build it, wait until nothing
# runs, switch, and go back if it does not come up.
finish_update() (
  load_conf
  local commit running why sandboxed
  commit=$(target_commit)
  running=$(running_commit)
  if [[ $running == "$commit" ]]; then
    if release_install; then
      update_state current "running $(awk '{print $1}' "$RELEASE_TARGET"), the newest release"
    else
      update_state current "running ${commit:0:9}, the newest of $SWARM_REF that passed its checks"
    fi
    return 0
  fi
  if [[ -f $UPDATE_BAD && $(cat "$UPDATE_BAD") == "$commit" ]]; then
    update_state rolled_back "${commit:0:9} did not come up and was rolled back; waiting for a newer commit" "$commit"
    return 0
  fi
  update_state building "building ${commit:0:9}" "$commit"
  # A separate process, so a failing step stops the build (errexit).
  if ! bash "$INSTALLER" build-release "$commit"; then
    update_state failed "building ${commit:0:9} failed; Swarm keeps running ${running:0:9} and tries again at the next update" "$commit"
    return 1
  fi
  local drain drain_rc=0 limit=${UPDATE_DRAIN_SECONDS:-7200}
  [[ $limit =~ ^[0-9]+$ && $limit -ge 1 && $limit -le 86400 ]] || die "UPDATE_DRAIN_SECONDS must be 1..86400"
  UPDATE_DRAIN_GENERATION=''
  UPDATE_DRAIN_CLAIM=''
  trap 'cleanup_update_drain' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  # Capture HTTP status separately from curl's exit code: only a real 404
  # permits the older idle check, never a transport/auth/server failure.
  drain=$(local_api /v3/box/drain -X POST -H 'Content-Type: application/json' -d "{\"max_seconds\":$limit}" -w $'\n%{http_code}' 2>/dev/null) || drain_rc=$?
  if [[ $drain_rc == 22 && ${drain##*$'\n'} == 404 ]]; then
    local deadline=$((SECONDS + limit)) remaining pause request_timeout busy_status
    update_state draining "draining the older way (no drain endpoint on the running daemon)" "$commit"
    while :; do
      remaining=$((deadline - SECONDS))
      if (( remaining <= 0 )); then
        update_state blocked "older drain timed out; running jobs were not stopped" "$commit"
        return 1
      fi
      request_timeout=20
      (( remaining >= request_timeout )) || request_timeout=$remaining
      why=$(local_api /v3/box/busy --max-time "$request_timeout" 2>/dev/null) || {
        update_state blocked "older drain busy endpoint missing or unavailable; daemon left running" "$commit"
        return 1
      }
      busy_status=$(json_get busy <<<"$why" || true)
      if [[ $busy_status != true && $busy_status != false ]]; then
        update_state blocked "older drain busy status invalid; daemon left running" "$commit"
        return 1
      fi
      (( SECONDS < deadline )) || continue
      [[ $busy_status == false ]] && break
      remaining=$((deadline - SECONDS))
      pause=5
      (( remaining >= pause )) || pause=$remaining
      sleep "$pause"
    done
  elif (( drain_rc != 0 )) && daemon_crashing; then
    # A daemon that keeps exiting runs nothing and can never answer a drain;
    # waiting for it would leave the machine down for good (for example when
    # the running release rejects its own config). Go on: the migration below
    # and the rollback after activation still apply.
    update_state draining "Swarm is not running (it keeps exiting); nothing to drain" "$commit"
  elif (( drain_rc != 0 )); then
    update_state blocked "cannot establish admission drain; daemon left running" "$commit"
    return 1
  else
    drain=${drain%$'\n'*}
    UPDATE_DRAIN_GENERATION=$(json_get generation <<<"$drain")
    [[ -n $UPDATE_DRAIN_GENERATION ]] || return 1
    while :; do
      [[ $(json_get generation <<<"$drain") == "$UPDATE_DRAIN_GENERATION" ]] || return 1
      [[ $(json_get ready <<<"$drain") == true ]] && break
      if [[ $(json_get timed_out <<<"$drain") == true ]]; then
        update_state blocked "drain timed out; running jobs were not stopped: $drain" "$commit"
        return 1
      fi
      [[ $(json_get draining <<<"$drain") == true ]] || return 1
      update_state draining "waiting for existing runs: $drain" "$commit"
      sleep 5
      drain=$(local_api /v3/box/drain) || {
        update_state blocked "drain status unavailable; daemon left running" "$commit"
        return 1
      }
    done
  fi
  # The new build must have its sandbox on only when this install keeps it on
  # (SANDBOX=on); turning it off is a setting, not a failed update.
  sandboxed=$(local_api /v3/box/summary 2>/dev/null | json_get sandbox.active || true)
  [[ ${SANDBOX:-off} == on ]] || sandboxed=false
  if ! migrate_removed_startup_keys "$OPT/releases/$commit"; then
    update_state failed "cannot migrate startup config; daemon left running" "$commit"
    return 1
  fi
  UPDATE_SWITCHED=false
  local activation_failed=false
  if ! activate "$commit"; then
    [[ $UPDATE_SWITCHED == true ]] || return 1
    activation_failed=true
    # Cleanup is best-effort against a potentially dead daemon, never a barrier
    # to restoring the old release. Its boot-bound reservation is now obsolete.
    cleanup_update_drain
  fi
  UPDATE_DRAIN_GENERATION=''
  UPDATE_DRAIN_CLAIM=''
  trap - EXIT INT TERM
  verify_update_or_rollback "$commit" "$running" "$sandboxed" "$activation_failed"
)

# Shared acceptance/rollback decision, after activation has completed.
verify_update_or_rollback() {
  local commit=$1 running=$2 sandboxed=$3
  if [[ ${4:-false} != true ]] && came_up "$commit" "$sandboxed"; then
    rm -f "$UPDATE_BAD"
    update_state installed "${running:0:9} -> ${commit:0:9}" "$commit"
    clean_up_after_switch
    return 0
  fi
  echo "$commit" >"$UPDATE_BAD"
  if [[ -n $running && -x $OPT/releases/$running/bin/swarmd ]]; then
    if activate "$running" && came_up "$running" ''; then
      update_state rolled_back "${commit:0:9} did not come up; back on ${running:0:9}" "$commit"
      return 0
    fi
  fi
  update_state failed "${commit:0:9} did not come up and the build before it could not be restored; check: journalctl -u $DAEMON_UNIT" "$commit"
  return 1
}

# Turn automatic updates on or off, or show them.
auto_update() {
  load_conf
  local mode=${1:-} checks=${UPDATE_CHECKS:-} hook=${UPDATE_HOOK:-}
  if [[ -z $mode ]]; then
    echo "Automatic updates: ${AUTO_UPDATE:-off}; branch $SWARM_REF; checks ${UPDATE_CHECKS:-$DEFAULT_UPDATE_CHECKS}${UPDATE_HOOK:+; hook $UPDATE_HOOK}"
    [[ -f $UPDATE_STATE ]] && cat "$UPDATE_STATE"
    return 0
  fi
  shift
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  while (($#)); do
    case $1 in
      --checks) checks=${2:?--checks needs CONTEXT=APP[,...] or none}; shift 2 ;;
      --hook) hook=${2?--hook needs a path, or '' for none}; shift 2 ;;
      *) die "usage: install.sh auto-update on|off [--checks CONTEXT=APP[,...]|none] [--hook PATH]" ;;
    esac
  done
  [[ $mode == on || $mode == off ]] || die "usage: install.sh auto-update on|off [--checks CONTEXT=APP[,...]|none] [--hook PATH]"
  [[ -z $checks || $checks == none || $checks =~ ^[A-Za-z0-9._/-]+=[A-Za-z0-9._-]+(\[bot\])?(,[A-Za-z0-9._/-]+=[A-Za-z0-9._-]+(\[bot\])?)*$ ]] ||
    die "--checks is CONTEXT=APP[,...] (e.g. ai-review=swarm-go-ai-review[bot]) or none"
  [[ -z $hook || $hook == /* ]] || die "--hook must be an absolute path"
  conf_set AUTO_UPDATE "$mode"
  if [[ -n $checks ]]; then conf_set UPDATE_CHECKS "$checks"; else sed -i '/^UPDATE_CHECKS=/d' "$CONF"; fi
  if [[ -n $hook ]]; then conf_set UPDATE_HOOK "$hook"; else sed -i '/^UPDATE_HOOK=/d' "$CONF"; fi
  install_units
  if [[ $mode == on ]]; then
    echo "Automatic updates are on: every 5 minutes Swarm takes the newest commit of $SWARM_REF that passed ${checks:-$DEFAULT_UPDATE_CHECKS}, builds it while work runs and restarts into it only when nothing runs."
  else
    echo "Automatic updates are off; install.sh update updates by hand (with the same checks)."
  fi
}

# The branch this machine follows. Switching keeps the install, its state and
# the running build; the next update (within 5 minutes) builds the new branch
# once it contains the running build, so a switch never downgrades.
channel() {
  load_conf
  local ref=${1:-}
  if release_install; then
    [[ -z $ref ]] || die "this machine installs released builds; to follow a branch, reinstall it from source: sudo bash $INSTALLER reinstall --ref $ref"
    echo "Installing released builds; running $(awk '{print $1}' "$RELEASE_TARGET" 2>/dev/null || echo unknown)"
    return 0
  fi
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
  daemon_env_apply "$@"
}

# daemon_env without the root check, for commands that already made it.
daemon_env_apply() {
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
  restart_daemon_when_idle
  wait_ready
  if [[ $action == set ]]; then echo "Saved $*; Swarm restarted."; else echo "Removed $*; Swarm restarted."; fi
}

install_units() {
  load_conf
  conf_migrate
  local sdk_args='' runtime_arg='' secrets_arg='' permissions_arg ai_url='' ai_ip='' release
  # The app starts only when run as the main module, which Node decides on
  # the symlink-resolved path, so its unit names the release itself: the one
  # being activated (activate passes it, before current points at it; on a
  # first install current does not exist yet), else the running one.
  release=${1:-$(readlink -f "$OPT/current")}
  # Swarm's MCP for AI keys, on loopback (and on the tailnet through Serve).
  [[ ${AI_ACCESS:-off} == on ]] && sdk_args='--container-sdk-port=7783 --container-sdk-host=127.0.0.1'
  [[ ${AI_ACCESS:-off} == on && ${TAILNET:-off} == on && ${TAILNET_IDENTITY:-off} == on ]] && sdk_args+=' --tailnet-identity'
  [[ ${AI_ACCESS:-off} == on && ${TAILNET:-off} == on ]] && ai_url=${APP_AI_URL:-} ai_ip=${APP_AI_IP:-}
  [[ ${SANDBOX_RUNTIME:-default} == default ]] && runtime_arg='--sandbox-runtime=runc'
  # The gateway listens on the sandbox network, which only environments make.
  [[ ${SECRETS_GATEWAY:-off} == on ]] && environments_on && secrets_arg=--secrets-gateway=on
  # Jobs skip tool approvals by default (auto), sandbox or not: nobody is
  # there to answer a prompt on a headless machine. The installer's choice is applied at every start.
  case ${PERMISSIONS:-} in
    auto) permissions_arg=--bypass-permissions ;;
    ask) permissions_arg=--bypass-permissions=false ;;
    *) die "install.env has no valid PERMISSIONS (auto or ask); run install.sh permissions auto|ask" ;;
  esac
  # Off by default: agents run in Swarm on this machine. On: their commands
  # run in one Docker sandbox per project.
  local sandbox_arg
  case ${SANDBOX:-} in
    off) sandbox_arg=--sandbox=off ;;
    on) sandbox_arg="--sandbox=required --sandbox-backend=docker" ;;
    *) die "install.env has no valid SANDBOX (on or off); run install.sh sandbox on|off" ;;
  esac
  install -d "$UNITS"
  # Docker only with environments: the firewall unit, the daemon's Docker
  # access and the sandbox cgroup slice exist only then.
  local docker_after='' docker_requires='' docker_groups='' cgroup_arg='--sandbox-cgroup-parent=' cgroup_driver
  if environments_on; then
  docker_after="docker.service $FIREWALL_UNIT" docker_requires="Requires=docker.service $FIREWALL_UNIT" docker_groups="SupplementaryGroups=docker"
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
ExecStart=/bin/bash $(sandbox_dir)/firewall.sh

[Install]
WantedBy=multi-user.target docker.service
UNIT
  cgroup_driver=$(docker info --format '{{.CgroupDriver}}') || { echo 'Cannot determine Docker cgroup driver' >&2; return 1; }
  case "$cgroup_driver" in
    systemd) cgroup_arg='--sandbox-cgroup-parent=swarm-sandbox.slice' ;;
    cgroupfs) echo 'Docker cgroupfs: using per-container CPU shares instead of systemd slice' >&2 ;;
    *) echo "Unsupported Docker cgroup driver: $cgroup_driver" >&2; return 1 ;;
  esac
  else
    systemctl disable "$FIREWALL_UNIT" >/dev/null 2>&1 || true
    rm -f "$UNITS/$FIREWALL_UNIT"
  fi
  cat > "$UNITS/swarm-sandbox.slice" <<'UNIT'
[Unit]
Description=Low-priority Swarm agent commands
[Slice]
# No cap: weights affect contention only, never idle CPU or memory.
CPUWeight=25
IOWeight=25
UNIT
  # Swarm itself: its own user, its own private state directories, and Docker
  # access to start sandboxes. Nothing it holds is mounted into a sandbox.
  cat > "$UNITS/$DAEMON_UNIT" <<UNIT
[Unit]
Description=Swarm (headless)
After=network-online.target $docker_after
Wants=network-online.target
$docker_requires

[Service]
User=$SERVICE_USER
Group=$SERVICE_USER
$docker_groups
# 0007, not 0077: a sandbox for a person's folder runs as that person with
# Swarm's group, so what Swarm writes there must admit its group (Swarm's own
# state directories are 0700 and nobody else is in its group).
UMask=0007
Environment=HOME=$SERVICE_HOME SWARM_DISABLE_MINT_REPORT=1
# Folders whose jobs are delivered as pull requests (swarmctl workspace publish).
Environment=SWARM_PUBLISH_CONFIG=$PUBLISH
# The installer's update state, shown in the machine summary.
Environment=SWARM_UPDATE_STATE=$UPDATE_STATE
Environment=SWARM_INSTALLER=$STATE/install.sh
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
Slice=swarm.slice
CPUWeight=1000
IOWeight=1000
ExecStart=$OPT/current/bin/swarmd --cwd=$PROJECT $sandbox_arg $cgroup_arg $permissions_arg $runtime_arg $sdk_args $secrets_arg
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
Description=Update headless Swarm to its newest release (or checked commit of its branch)
After=network-online.target $docker_after
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/bin/bash $INSTALLER update
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
  systemctl enable "$DAEMON_UNIT" "$APP_UNIT" >/dev/null
  if environments_on; then systemctl enable "$FIREWALL_UNIT" >/dev/null; fi
  # Automatic updates only when the person turned them on (install.sh auto-update on).
  if [[ ${AUTO_UPDATE:-off} == on ]]; then
    systemctl enable --now swarm-headless-update.timer >/dev/null
  else
    systemctl disable --now swarm-headless-update.timer >/dev/null 2>&1 || true
  fi
}

# ---- Workspaces: the person's own folders, made visible to Swarm -------------
# Swarm runs as its own user with every home folder hidden (ProtectHome=yes),
# so it cannot see a person's folders. `swarmctl workspace add PATH`, run as
# root by the person or by the AI setting this machine up, makes exactly that
# folder (a Git repository or not) visible and writable to Swarm and its agent
# sandboxes at its real path: a unit drop-in binds the folder into the
# service, and Swarm's user gets access to that folder only (the owner keeps
# full rights on everything Swarm creates inside). Swarm itself never changes
# the machine; this is swarmctl's job, done from outside. Afterwards the
# folder is registered in place with swarm_add_workspace; nothing is copied.
WORKSPACES=$STATE/workspaces
# Root-owned: FOLDER<TAB>COMMAND[<TAB>auto,sync] per line; Swarm only reads it.
PUBLISH=$STATE/publish
WORKSPACES_DROPIN=$UNITS/$DAEMON_UNIT.d/50-workspaces.conf


workspace_listed() { [[ -f $WORKSPACES ]] && grep -v '^\s*$' "$WORKSPACES" || true; }

# Uninstall still revokes the folder ACLs; normal administration is in swarmctl.
workspace_revoke() {
  local path=$1 owner=$2
  setfacl -R -x "u:$SERVICE_USER" "$path" 2>/dev/null || true
  setfacl -R -d -x "u:$SERVICE_USER" "$path" 2>/dev/null || true
  setfacl -R -d -x "u:$owner" "$path" 2>/dev/null || true
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
    docker image ls --format '{{.Repository}}:{{.Tag}}' swarm-sandbox | xargs -r docker image rm >/dev/null 2>&1 || true
    docker network rm "$SANDBOX_NETWORK" >/dev/null 2>&1 || true
  fi
  if command -v iptables >/dev/null; then
    iptables -D DOCKER-USER -i "$SANDBOX_BRIDGE" -j SWARM-SANDBOX 2>/dev/null || true
    iptables -D INPUT -i "$SANDBOX_BRIDGE" -j SWARM-SANDBOX-HOST 2>/dev/null || true
    iptables -F SWARM-SANDBOX 2>/dev/null && iptables -X SWARM-SANDBOX 2>/dev/null || true
    iptables -F SWARM-SANDBOX-HOST 2>/dev/null && iptables -X SWARM-SANDBOX-HOST 2>/dev/null || true
  fi
  rm -rf "${DATA_DIRS[@]}" /run/swarmd "$SERVICE_HOME" "$OPT" "$STAGE" "$SWARMCTL" "$AI_KEY_IDS"
  rm -f "$CONF" "$UPDATE_BAD"
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
    mapfile -t saved < <(. "$CONF" && printf '%s\n' "${APP_RELAY_URL:-}" "${APP_DEVICE_NAME:-}" "${SWARM_REF:-}" "${AI_ACCESS:-}" "${SECRETS_GATEWAY:-}" "${PERMISSIONS:-}" "${AUTO_UPDATE:-}" "${UPDATE_CHECKS:-}" "${UPDATE_HOOK:-}" "${SANDBOX:-}" "${SOURCE:-git}" "${ENVIRONMENTS:-on}")
    saved=("${saved[@]:0:6}" "$(saved_tailnet)" "${saved[@]:6}")
  fi
  [[ -n ${saved[0]:-} ]] && args+=(--relay "${saved[0]}")
  [[ -n ${saved[1]:-} ]] && args+=(--name "${saved[1]}")
  # A source install keeps its branch (the retired container layout's
  # branch is on dev now); a release install stays on releases.
  [[ ${saved[11]:-git} == git && -n ${saved[2]:-} && ${saved[2]} != swarm-control ]] && args+=(--ref "${saved[2]}")
  [[ ${saved[12]:-off} == on ]] && args+=(--environments on)
  [[ ${saved[3]:-} == off ]] && args+=(--no-ai-access)
  [[ -n ${saved[4]:-} ]] && args+=(--secrets-gateway "${saved[4]}")
  [[ ${saved[5]:-} == ask ]] && args+=(--ask-permissions)
  [[ ${saved[7]:-} == on ]] && args+=(--auto-update)
  [[ -n ${saved[8]:-} ]] && args+=(--update-checks "${saved[8]}")
  [[ -n ${saved[9]:-} ]] && args+=(--update-hook "${saved[9]}")
  [[ ${saved[10]:-} == on ]] && args+=(--sandbox on)
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
  restart_daemon_when_idle
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
  restart_daemon_when_idle
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

# Agents in Swarm on this machine (off, the default) or their commands in one
# Docker sandbox per project (on).
set_sandbox() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  load_conf
  case ${1:-} in
    on|off) ;;
    *) die "usage: install.sh sandbox on|off" ;;
  esac
  # The sandbox runs on Docker: it brings environments (Docker, gVisor) with it.
  if [[ $1 == on ]] && ! environments_on; then set_environments on; load_conf; fi
  conf_set SANDBOX "$1"
  install_units
  restart_swarm
  if [[ $1 == on ]]; then
    echo "Agents' commands run in one sandbox per project."
  else
    echo "The sandbox is off: agents run in Swarm on this machine."
  fi
}

# Environments: a folder's own container for its agents' commands, with its
# secrets (swarm_set_environment with max_leases). They need Docker: on installs
# Docker and gVisor, builds the sandbox image and network and turns the secret
# gateway on; off stops using them (Docker stays installed).
set_environments() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  load_conf
  conf_migrate >/dev/null
  case ${1:-} in
    on)
      install_docker
      conf_set ENVIRONMENTS on
      ENVIRONMENTS=on
      conf_set SECRETS_GATEWAY on
      SECRETS_GATEWAY=on
      local commit
      commit=$(running_commit)
      [[ -n $commit ]] || die "Swarm is not running yet; install it first"
      if ! docker image inspect "swarm-sandbox:$commit" >/dev/null 2>&1; then
        build_sandbox_image "$commit" "$(sandbox_dir)"
      fi
      docker tag "swarm-sandbox:$commit" "$SANDBOX_IMAGE"
      firewall
      choose_runtime
      conf_set SANDBOX_RUNTIME "$SANDBOX_RUNTIME"
      install_units
      systemctl daemon-reload
      systemctl enable --now "$FIREWALL_UNIT" >/dev/null
      restart_swarm
      echo "Environments are on: a folder with an environment (swarm_set_environment with max_leases) runs its agents' commands in it, with its secrets."
      ;;
    off)
      [[ ${SANDBOX:-off} == off ]] || die "the sandbox needs environments; turn it off first: sudo bash $INSTALLER sandbox off"
      conf_set ENVIRONMENTS off
      ENVIRONMENTS=off
      conf_set SECRETS_GATEWAY off
      SECRETS_GATEWAY=off
      install_units
      systemctl daemon-reload
      restart_swarm
      echo "Environments are off: agents run every command on this machine. Docker stays installed."
      ;;
    *) die "usage: install.sh environments on|off" ;;
  esac
}

# Agents without prompts (auto, the default) or asking first.
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
    echo "Agents run without permission prompts."
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
  wait_daemon
  status=$("$SWARMCTL" setup status) || die "could not read Swarm's setup status"
  [[ $status == *identity=true* ]] && return 0
  "$SWARMCTL" setup identity --username "$who" --name "$APP_DEVICE_NAME" >/dev/null ||
    die "could not create Swarm's owner $who"
  echo "Created Swarm's owner ($who)."
}

# Give a person's AI, or one of their apps, on this machine its own key for
# Swarm's MCP, in a file only they can read. The key goes from Swarm to that
# file through a private root-only file and a pipe; it is never printed or put
# on a command line.
#   mint_ai_key WHO ACCESS          the AI key: ~/.config/swarm/ai-key
#   mint_ai_key WHO ACCESS APP      an app key: ~/.config/swarm/apps/APP.key
# Each key replaces (and revokes) only the previous key in the same place, so
# connecting an app never disconnects the person's AI and the other way round.
mint_ai_key() {
  local who=$1 access=$2 app=${3:-} home dir keyfile idfile tmp old_id='' token_name shown
  [[ ${AI_ACCESS:-off} == on ]] || die "Swarm's MCP is off on this machine (installed with --no-ai-access); reinstall without it to connect an AI"
  command -v jq >/dev/null || die "jq is missing (apt-get install jq)"
  ensure_owner "$who"
  home=$(person_home "$who")
  dir=$home/.config/swarm
  # Made as the person, so nothing below writes through a link they placed.
  runuser -u "$who" -- mkdir -p -m 0700 "$dir" || die "could not create $dir"
  [[ -d $dir && ! -L $dir && $(stat -c %U "$dir") == "$who" ]] || die "$dir must be a folder owned by $who"
  install -d -m 0700 -o root -g root "$AI_KEY_IDS"
  if [[ -n $app ]]; then
    runuser -u "$who" -- mkdir -p -m 0700 "$dir/apps" || die "could not create $dir/apps"
    [[ -d $dir/apps && ! -L $dir/apps && $(stat -c %U "$dir/apps") == "$who" ]] || die "$dir/apps must be a folder owned by $who"
    keyfile=$dir/apps/$app.key
    idfile=$AI_KEY_IDS/$who.app.$app
    token_name=app-$app-$who
    shown="~/.config/swarm/apps/$app.key"
  else
    keyfile=$dir/ai-key
    idfile=$AI_KEY_IDS/$who
    token_name=local-ai-$who
    shown="~/.config/swarm/ai-key"
  fi
  [[ -f $idfile ]] && old_id=$(<"$idfile")
  tmp=$(mktemp "$AI_KEY_IDS/.mint.XXXXXX")
  # The key works while it is used and stops after 90 days unused; nothing
  # renews it. connect-ai (or connect-app) again replaces it.
  if ! "$SWARMCTL" setup sdk-token --ai-access "$access" --name "$token_name" >"$tmp"; then
    rm -f "$tmp"
    die "no key was made; if Swarm made one anyway it shows under AI keys on the setup page"
  fi
  if ! jq -er '.token | select(startswith("swk_"))' "$tmp" |
    runuser -u "$who" -- sh -c 'umask 077 && cat >"$1.new" && mv -f "$1.new" "$1"' sh "$keyfile"; then
    rm -f "$tmp"
    die "could not write $keyfile; revoke the new $token_name key on the setup page"
  fi
  jq -er '.id' "$tmp" >"$idfile.new" && mv -f "$idfile.new" "$idfile"
  rm -f "$tmp"
  if [[ -n $old_id ]]; then
    "$SWARMCTL" setup revoke-sdk-token --id "$old_id" >/dev/null ||
      echo "warning: could not revoke the previous $token_name key (id $old_id); revoke it on the setup page" >&2
  fi
  if [[ -n $app ]]; then
    echo "Made a $access key for the $app app in $shown (only $who can read it; it works while used and stops after 90 days unused)."
  else
    echo "Made a $access AI key for $who in $shown (only $who can read it; it works while used and stops after 90 days unused)."
  fi
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

# An app name: what the key is for, also its file name.
app_name() {
  [[ ${1:-} =~ ^[a-z0-9][a-z0-9-]{0,39}$ ]] || die "the app name must be 1-40 lowercase letters, digits or dashes, e.g. swarm-console"
  printf '%s' "$1"
}

# Connect an app (a console, a dashboard, a frontend you build) on this
# machine: its own key in ~/.config/swarm/apps/NAME.key, separate from the AI
# key. The key reaches Swarm Control (/mcp) and the activity feed on the AI
# gateway, at the level given (read, write or full; full approves tool calls
# and manages models, providers and workers). Running it again replaces that
# app's key; disconnect-app revokes it. Discovery needs no key:
# GET http://127.0.0.1:7783/v3/hello. See docs/frontends.md.
connect_app() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash, or pkexec)"
  load_conf
  local app='' who='' access=write
  while (($#)); do
    case $1 in
      --for-user) who=${2:?--for-user needs a user name}; shift 2 ;;
      --access) access=${2:?--access needs read, write or full}; shift 2 ;;
      -*) die "usage: install.sh connect-app NAME [--for-user NAME] [--access read|write|full]" ;;
      *) [[ -z $app ]] || die "usage: install.sh connect-app NAME [--for-user NAME] [--access read|write|full]"; app=$(app_name "$1") || exit 1; shift ;;
    esac
  done
  [[ -n $app ]] || die "usage: install.sh connect-app NAME [--for-user NAME] [--access read|write|full]"
  [[ $access == read || $access == write || $access == full ]] || die "--access must be read, write or full"
  who=$(person "$who") || exit 1
  step connect-app "Connecting the $app app for $who"
  mint_ai_key "$who" "$access" "$app"
  echo "The app reads its key from that file and talks to $LOCAL_MCP_URL;"
  echo "it finds Swarm first, without a key, at ${LOCAL_MCP_URL%/mcp}/v3/hello."
  [[ $PROGRESS == json ]] && printf '{"swarm_progress":{"step":"done","text":%s}}\n' "$(json_str "The $app app is connected")"
  return 0
}

disconnect_app() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash, or pkexec)"
  load_conf
  local app='' who='' idfile home
  while (($#)); do
    case $1 in
      --for-user) who=${2:?--for-user needs a user name}; shift 2 ;;
      -*) die "usage: install.sh disconnect-app NAME [--for-user NAME]" ;;
      *) [[ -z $app ]] || die "usage: install.sh disconnect-app NAME [--for-user NAME]"; app=$(app_name "$1") || exit 1; shift ;;
    esac
  done
  [[ -n $app ]] || die "usage: install.sh disconnect-app NAME [--for-user NAME]"
  who=$(person "$who") || exit 1
  idfile=$AI_KEY_IDS/$who.app.$app
  if [[ -f $idfile ]]; then
    "$SWARMCTL" setup revoke-sdk-token --id "$(<"$idfile")" >/dev/null ||
      die "could not revoke the $app app's key; revoke app-$app-$who on the setup page"
    rm -f "$idfile"
  fi
  home=$(person_home "$who")
  runuser -u "$who" -- rm -f "$home/.config/swarm/apps/$app.key"
  echo "The $app app is disconnected: its key is revoked and its key file removed."
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

# Claude Code as Swarm's provider (install.sh claude-code). Swarm runs the
# unmodified `claude` CLI as its own service user, so that user needs the CLI
# and its own sign-in. Swarm never reads, copies or stores Claude credentials:
# the person signs that user in through Anthropic's own flow, and their own
# Claude login is never copied. Making the provider active is then the AI's
# step (swarm_connect_claude_code action activate).
CLAUDE_CODE_PATH_ENV=SWARM_CLAUDE_CODE_CLI_PATH
# Anthropic's installer for the native Claude Code build (~/.local/bin/claude).
CLAUDE_CODE_INSTALLER=https://claude.ai/install.sh
# The first Claude Code with --restricted, which Swarm requires.
CLAUDE_CODE_MIN_VERSION=2.1.248

# Run a command as Swarm's service user. It has no login shell, so this runs
# the command directly with that user's home.
service_user_run() {
  runuser -u "$SERVICE_USER" -- env HOME="$SERVICE_HOME" PATH="$SERVICE_HOME/.local/bin:$PATH" "$@"
}

# The value of NAME in the daemon's settings file, if set.
daemon_env_value() {
  [[ -f $DAEMON_ENV ]] || return 0
  sed -n "s/^$1=//p" "$DAEMON_ENV" | tail -n 1
}

# The claude executable Swarm's user can run: the one Swarm is already set to,
# then Anthropic's per-user install location, then the user's PATH.
claude_code_locate() {
  local candidate
  for candidate in "$(daemon_env_value "$CLAUDE_CODE_PATH_ENV")" "$SERVICE_HOME/.local/bin/claude" \
    "$(service_user_run sh -c 'command -v claude' 2>/dev/null || true)"; do
    [[ $candidate == /* ]] || continue
    service_user_run test -x "$candidate" 2>/dev/null && { printf '%s' "$candidate"; return 0; }
  done
  return 1
}

claude_code_version() {
  { service_user_run "$1" --version 2>/dev/null || true; } | { grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' || true; } | head -n 1
}

# True when the first version is at least the second.
version_at_least() {
  [[ $(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n 1) == "$2" ]]
}

claude_code_signed_in() {
  service_user_run "$1" auth status 2>/dev/null | grep -Eq '"loggedIn"[[:space:]]*:[[:space:]]*true'
}

stdin_is_terminal() { [[ -t 0 ]]; }

claude_code() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  claude_code_setup "$@"
}

claude_code_setup() {
  (($# == 0)) || die "usage: install.sh claude-code"
  load_conf
  id -u "$SERVICE_USER" >/dev/null 2>&1 || die "Swarm's user $SERVICE_USER does not exist; install Swarm first"
  local cli version
  if ! cli=$(claude_code_locate); then
    say "Installing Claude Code for Swarm's user ($SERVICE_USER) with Anthropic's installer"
    service_user_run bash -c 'set -o pipefail; curl -fsSL "$1" | bash' claude-install "$CLAUDE_CODE_INSTALLER" ||
      die "the Claude Code installer failed"
    cli=$(claude_code_locate) || die "Claude Code was installed but $SERVICE_USER cannot run it; expected $SERVICE_HOME/.local/bin/claude"
  fi
  version=$(claude_code_version "$cli")
  [[ -n $version ]] || die "$cli did not report a version"
  if ! version_at_least "$version" "$CLAUDE_CODE_MIN_VERSION"; then
    say "Updating Claude Code $version (Swarm needs $CLAUDE_CODE_MIN_VERSION or later)"
    service_user_run "$cli" update || die "claude update failed"
    version=$(claude_code_version "$cli")
    version_at_least "${version:-0.0.0}" "$CLAUDE_CODE_MIN_VERSION" ||
      die "Claude Code ${version:-unknown} is too old; Swarm needs $CLAUDE_CODE_MIN_VERSION or later"
  fi
  echo "Claude Code $version for Swarm's user ($SERVICE_USER): $cli"
  if [[ $(daemon_env_value "$CLAUDE_CODE_PATH_ENV") == "$cli" ]]; then
    echo "Swarm already uses it ($CLAUDE_CODE_PATH_ENV)."
  else
    printf '%s\n' "$cli" | daemon_env_apply set "$CLAUDE_CODE_PATH_ENV"
  fi
  if claude_code_signed_in "$cli"; then
    echo "Claude Code is signed in for $SERVICE_USER."
  elif stdin_is_terminal; then
    say "Sign Claude Code in for Swarm's user: open the link it shows in your browser and finish there"
    service_user_run "$cli" auth login || die "claude auth login did not finish; run install.sh claude-code again"
    claude_code_signed_in "$cli" || die "Claude Code is still not signed in for $SERVICE_USER; run install.sh claude-code again"
    echo "Claude Code is signed in for $SERVICE_USER."
  else
    die "Claude Code is installed but not signed in for $SERVICE_USER. Signing in needs the person at a terminal: they run 'sudo bash $INSTALLER claude-code' in their own terminal and finish the sign-in in their browser."
  fi
  echo "Next: your AI makes Claude Code Swarm's provider (swarm_connect_claude_code action activate)."
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
  local relay='' name=swarm ref=main source=release environments=off lock_ssh=0 ai_access=on tag='' secrets_gateway='' permissions=auto sandbox=off tailnet=off for_user='' auto_update=off update_checks='' update_hook='' app='' app_access=write
  while (($#)); do
    case $1 in
      --for-user) for_user=${2:?--for-user needs a user name}; shift 2 ;;
      --relay) relay=${2:?--relay needs a URL}; shift 2 ;;
      --name) name=${2:?--name needs a value}; shift 2 ;;
      --ref) ref=${2:?--ref needs a branch}; source=git; shift 2 ;;
      --release-file) RELEASE_FILE=$(realpath "${2:?--release-file needs an archive}"); shift 2 ;;
      --environments) environments=${2:?--environments needs on or off}; [[ $environments == on || $environments == off ]] || die "--environments needs on or off"; shift 2 ;;
      --tailnet) tailnet=on; shift ;;
      --no-tailnet) tailnet=off; shift ;;
      --tag) tag=${2:?--tag needs a tag such as tag:swarm}; shift 2 ;;
      --lock-ssh) lock_ssh=1; shift ;;
      --no-ai-access) ai_access=off; shift ;;
      --ai-access) ai_access=on; shift ;;
      --secrets-gateway) secrets_gateway=${2:?--secrets-gateway needs on or off}; shift 2 ;;
      --ask-permissions) permissions=ask; shift ;;
      --sandbox) sandbox=${2:?--sandbox needs on or off}; [[ $sandbox == on || $sandbox == off ]] || die "--sandbox needs on or off"; shift 2 ;;
      --auto-update) auto_update=on; shift ;;
      --update-checks) update_checks=${2:?--update-checks needs CONTEXT=APP[,...] or none}; shift 2 ;;
      --update-hook) update_hook=${2:?--update-hook needs a path}; shift 2 ;;
      --app) app=$(app_name "${2:-}") || exit 1; shift 2 ;;
      --app-access) app_access=${2:?--app-access needs read, write or full}; shift 2 ;;
      -h|--help) usage; exit 0 ;;
      *) usage >&2; die "unknown option $1" ;;
    esac
  done
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  [[ $(uname -m) == x86_64 ]] || die "Swarm's headless build is x86_64 only (this machine is $(uname -m))"
  [[ $name =~ ^[a-z0-9][a-z0-9-]{0,40}$ ]] || die "--name must be lowercase letters, digits and dashes"
  # The sandbox runs on Docker, so it brings environments with it; the secret
  # gateway reaches only environments and the sandbox, so it follows them.
  [[ $sandbox == on ]] && environments=on
  [[ -n $secrets_gateway ]] || secrets_gateway=$environments
  [[ $secrets_gateway == on || $secrets_gateway == off ]] || die "--secrets-gateway must be on or off"
  [[ $secrets_gateway == off || $environments == on ]] || die "--secrets-gateway on needs environments (secrets reach agents only inside them): add --environments on"
  [[ -z $relay || $relay =~ ^https://[A-Za-z0-9.-]+/?$ ]] || die "--relay must be an https origin such as https://swarm-relay.example.workers.dev"
  [[ $ref =~ ^[A-Za-z0-9._/-]+$ ]] || die "--ref must be a branch name"
  [[ -z $tag || $tag =~ ^tag:[a-z0-9][a-z0-9-]{0,40}$ ]] || die "--tag must look like tag:swarm"
  [[ $tailnet == on || (-z $tag && $lock_ssh == 0) ]] || die "--tag and --lock-ssh are for the tailnet; add --tailnet"
  [[ $tailnet == on || -z ${TS_AUTHKEY:-} ]] || die "TS_AUTHKEY is set but --tailnet is not: add --tailnet to join your tailnet, or unset TS_AUTHKEY"
  # The person the AI connection is for: --for-user (checked now), else the
  # account that ran sudo when it is a person's. Without one, the final output
  # names the connect-ai step instead.
  [[ $app_access == read || $app_access == write || $app_access == full ]] || die "--app-access must be read, write or full"
  local who=''
  if [[ -n $for_user ]]; then
    who=$(person "$for_user") || exit 1
  elif [[ -n $(invoking_user) ]]; then
    who=$(person "" 2>/dev/null) || who=''
  fi

  step prerequisites "Installing Git and the tools Swarm uses"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq git curl ca-certificates gnupg iproute2 jq acl >/dev/null
  # Docker only for environments (and the sandbox), or to build from source.
  if [[ $environments == on || $source == git ]]; then install_docker; fi

  local origin=$LOCAL_APP_ORIGIN ai_url='' ai_ip='' tailnet_identity=off
  if [[ $tailnet == on ]]; then
    join_tailnet "$name" "$tag" "$lock_ssh" "$ai_access"
    origin=https://$TS_DNS ai_url=https://$TS_DNS:8444/mcp ai_ip=$TS_IP tailnet_identity=$TS_IDENTITY
  fi

  install -d -m 0755 "$STATE"
  if [[ $source == git ]]; then
    step fetch "Fetching Swarm ($ref) to build it"
    if [[ -d $SRC/.git ]]; then
      git -C "$SRC" fetch -q origin "$ref" && git -C "$SRC" checkout -q -B "$ref" FETCH_HEAD
    else
      git clone -q --branch "$ref" https://github.com/swarm-agent/swarm.git "$SRC"
    fi
  else
    if [[ -n ${RELEASE_FILE:-} ]]; then
      local file_tag file_commit
      file_tag=$(basename "$RELEASE_FILE")
      [[ $file_tag =~ ^swarm-(v[0-9]+\.[0-9]+\.[0-9]+)-linux-amd64\.tar\.gz$ ]] || die "--release-file must be a release archive (swarm-vX.Y.Z-linux-amd64.tar.gz)"
      file_tag=${BASH_REMATCH[1]}
      file_commit=$(tar -xzOf "$RELEASE_FILE" "swarm-$file_tag-linux-amd64/build-info.txt" | sed -n 's/^commit=//p')
      [[ $file_commit =~ ^[0-9a-f]{40}$ ]] || die "$RELEASE_FILE has no build commit"
      printf '%s %s\n' "$file_tag" "$file_commit" >"$RELEASE_TARGET"
    else
      step fetch "Finding Swarm's newest release"
      latest_release || die "could not read Swarm's newest release from GitHub (https://github.com/$RELEASE_REPO/releases); check this machine reaches github.com and run this again"
    fi
    echo "Swarm $(awk '{print $1}' "$RELEASE_TARGET")"
  fi

  step service-user "Creating Swarm's service user"
  id "$SERVICE_USER" >/dev/null 2>&1 ||
    useradd --system --user-group --home-dir "$SERVICE_HOME" --create-home --shell /usr/sbin/nologin "$SERVICE_USER"
  if getent group docker >/dev/null; then usermod -aG docker "$SERVICE_USER"; fi
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
SOURCE=$source
ENVIRONMENTS=$environments
TAILNET=$tailnet
APP_ORIGIN=$origin
APP_RELAY_URL=${relay:-$previous_relay}
APP_DEVICE_NAME=$name
AI_ACCESS=$ai_access
TAILNET_IDENTITY=$tailnet_identity
SECRETS_GATEWAY=$secrets_gateway
PERMISSIONS=$permissions
SANDBOX=$sandbox
AUTO_UPDATE=$auto_update
UPDATE_CHECKS=$update_checks
UPDATE_HOOK=$update_hook
APP_AI_URL=$ai_url
APP_AI_IP=$ai_ip
SANDBOX_RUNTIME=default
CONF
  umask 022

  remove_legacy_layout
  up
  [[ $tailnet == on ]] && serve_tailnet "$ai_access" "$tailnet_identity"

  step start "Starting Swarm"
  wait_ready
  if [[ $environments == on ]]; then
    step isolation "Checking that environments cannot reach this server or your networks"
    check_isolation
  fi

  # The person's AI on this machine gets its key now (Swarm's owner is
  # created for them). A failure is reported, and the install exits non-zero.
  local key_made=0 rc=0
  if [[ -n $who && $ai_access == on ]]; then
    step connect-ai "Connecting $who's AI on this machine"
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
  # An app that drove this install (--app NAME) gets its own key the same way.
  if ((rc == 0)) && [[ -n $app && -n $who && $ai_access == on ]]; then
    step connect-app "Connecting the $app app for $who"
    set +e
    (
      set -e
      load_conf
      mint_ai_key "$who" "$app_access" "$app"
    )
    rc=$?
    set -e
    ((rc == 0)) || die "Swarm is running, but the $app app's key could not be made (see above); run: sudo bash $INSTALLER connect-app $app --for-user $who"
  fi
  install_done "$who" "$key_made"
  if ((rc != 0)); then
    die "Swarm is running, but the AI key for $who could not be made (see above); run: sudo bash $INSTALLER connect-ai --for-user $who"
  fi
  [[ $PROGRESS == json ]] && printf '{"swarm_progress":{"step":"done","text":%s}}\n' "$(json_str "Swarm is running")"
  return 0
}

# What runs, where, and the next steps.
install_done() {
  local who=$1 key_made=${2:-0}
  load_conf
  echo
  echo "Swarm is running on this machine; its services listen on 127.0.0.1 only:"
  echo "  Swarm          $(if release_install; then awk '{print $1}' "$RELEASE_TARGET"; else echo "built from $SWARM_REF"; fi): $(
    if [[ ${SANDBOX:-off} == on ]]; then echo "agents in one sandbox per project ($([[ $SANDBOX_RUNTIME == runsc ]] && echo gVisor || echo Docker runc))"; else echo "agents run on this machine (sandbox off)"; fi), $(
    [[ $PERMISSIONS == auto ]] && echo 'no permission prompts' || echo 'agents ask first')"
  if environments_on; then
    echo "  Environments   on (Docker): a folder's environment runs its agents' commands, with its secrets"
  else
    echo "  Environments   off: no Docker; turn on with: sudo bash $INSTALLER environments on"
  fi
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
  [[ $1 == off ]] || environments_on || die "secrets reach agents only inside environments; turn them on (this also turns the gateway on): sudo bash $INSTALLER environments on"
  sed -i "/^SECRETS_GATEWAY=/d" "$CONF" && echo "SECRETS_GATEWAY=$1" >>"$CONF"
  SECRETS_GATEWAY=$1
  install_units
  systemctl daemon-reload
  if environments_on; then systemctl restart "$FIREWALL_UNIT"; fi
  restart_daemon_when_idle
  wait_ready
  echo "Secret gateway is $1."
}


idle_off_response_is_idle() {
  "$OPT/current/bin/node" -e '
    try {
      const v = JSON.parse(require("fs").readFileSync(0, "utf8"));
      const counts = ["sessions", "worker_runs", "followups", "reviews"];
      if (!v || Array.isArray(v) || v.busy !== false ||
          counts.some(k => v[k] !== 0) ||
          Object.entries(v).some(([k,n]) => k !== "busy" && n !== 0)) process.exit(1);
    } catch { process.exit(1); }'
}

idle_off_check() {
  local minutes out busy now since boot saved_boot
  [[ -f $STATE/idle-off-minutes ]] || return 0
  read -r minutes <"$STATE/idle-off-minutes"
  [[ $minutes =~ ^[1-9][0-9]{0,5}$ ]] || return 0
  # Any transport/JSON/contract failure resets the continuous idle interval.
  if ! out=$(local_api /v3/box/busy) || ! idle_off_response_is_idle <<<"$out"; then
    rm -f "$STATE/idle-off-since"
    return 0
  fi
  read -r boot </proc/sys/kernel/random/boot_id
  now=$(cut -d. -f1 /proc/uptime)
  since=$now saved_boot=$boot
  if [[ -f $STATE/idle-off-since ]]; then read -r saved_boot since <"$STATE/idle-off-since" || since=$now; fi
  if [[ $saved_boot != "$boot" || ! $since =~ ^[0-9]+$ || $since -gt $now ]]; then since=$now; fi
  printf '%s %s\n' "$boot" "$since" >"$STATE/idle-off-since"
  (( now - since >= minutes * 60 )) || return 0
  # Do not shut down if the daemon cannot accept the one pre-shutdown signal.
  curl -fsS --max-time 20 --unix-socket "$LOCAL_API_SOCKET" -H 'Content-Type: application/json' \
    -d '{"kind":"external.idle_off","severity":"info","summary":"Installer powering off idle host","source":"installer"}' \
    http://swarm/v3/signals >/dev/null || { rm -f "$STATE/idle-off-since"; return 0; }
  rm -f "$STATE/idle-off-since"
  systemctl poweroff
}

status_cmd() {
  echo "Idle auto-off: $(cat "$STATE/idle-off-minutes" 2>/dev/null || echo off) (minutes when enabled)"
  systemctl --no-pager status swarm-idle-off.timer 2>/dev/null | grep -E '●|Active:' || true
  systemctl --no-pager status "$DAEMON_UNIT" "$APP_UNIT" | grep -E '●|Active:' || true
  if [[ -f $CONF ]]; then
    load_conf
    echo "Setup page: $APP_ORIGIN; Swarm MCP: $([[ ${AI_ACCESS:-off} == on ]] && echo "$LOCAL_MCP_URL" || echo off); tailnet: $(saved_tailnet); permissions: ${PERMISSIONS:-auto (after the next update)}; secret gateway: ${SECRETS_GATEWAY:-off}"
    echo "Environments: ${ENVIRONMENTS:-on}; sandbox: ${SANDBOX:-off}; builds: $(if release_install; then echo "releases (running $(awk '{print $1}' "$RELEASE_TARGET" 2>/dev/null))"; else echo "from source"; fi)"
    echo "Automatic updates: ${AUTO_UPDATE:-off}$(release_install || echo " (branch $SWARM_REF)")$([[ -f $UPDATE_STATE ]] && printf "; last update: %s" "$(json_get reason <"$UPDATE_STATE" 2>/dev/null || true)")"
  fi
  check_sandbox
  if [[ -f $CONF && $(saved_tailnet) == on ]]; then tailscale serve status; fi
}

main() {
  local arg
  local -a rest=()
  for arg in "$@"; do
    if [[ $arg == --progress=json ]]; then PROGRESS=json; else rest+=("$arg"); fi
  done
  set -- "${rest[@]+"${rest[@]}"}"
  case ${1:-install} in
    install) shift || true; install_all "$@" ;;
    -*) install_all "$@" ;;
    reinstall) shift; reinstall_all "$@" ;;
    uninstall) shift; uninstall_all "$@" ;;
    update) update ;;
    finish-update) finish_update ;;
    build-release) shift; load_conf; build_release "${1:?build-release needs a commit}" ;;
    idle-off) shift; exec "$SWARMCTL" idle-off "$@" ;;
    idle-off-check) idle_off_check ;;
    auto-update) shift; auto_update "$@" ;;
    dev-sync) shift; dev_sync "$@" ;;
    channel) shift; channel "$@" ;;
    daemon-env) shift; daemon_env "$@" ;;
    claude-code) shift; claude_code "$@" ;;
    up) up ;;
    reset-login) reset_login ;;
    # Installers before gated updates end their update here: finish it the
    # new way (build, wait until nothing runs, switch, roll back on failure).
    apply) remove_legacy_layout; finish_update ;;
    apply-now) remove_legacy_layout; up ;;
    firewall) firewall ;;
    check-isolation) check_isolation ;;
    check-sandbox) check_sandbox ;;
    connect-ai) shift; connect_ai "$@" ;;
    connect-app) shift; connect_app "$@" ;;
    disconnect-app) shift; disconnect_app "$@" ;;
    key) shift; provider_key "$@" ;;
    secret) shift; secret_cmd "$@" ;;
    tailnet) shift; tailnet_cmd "$@" ;;
    tailnet-identity) shift; set_tailnet_identity "$@" ;;
    secrets-gateway) shift; set_secrets_gateway "$@" ;;
    permissions) shift; set_permissions "$@" ;;
    sandbox) shift; set_sandbox "$@" ;;
    environments) shift; set_environments "$@" ;;
    workspace) shift; exec "$SWARMCTL" workspace "$@" ;;
    status) status_cmd ;;
    -h|--help|help) usage ;;
    *) usage >&2; exit 2 ;;
  esac
}

# Everything above is parsed before anything runs, so a partial download or an
# update replacing this file cannot execute half a script.
main "$@"
