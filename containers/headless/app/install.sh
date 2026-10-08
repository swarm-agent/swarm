#!/usr/bin/env bash
# One-line installer for a private, headless Swarm with its setup app.
#
# On a fresh Ubuntu 24.04 x86_64 server, as root:
#   curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/swarm-control/containers/headless/app/install.sh \
#     | bash -s -- --relay https://swarm-relay.YOU.workers.dev
#
# It installs Docker and Tailscale, joins your tailnet (you open one login
# link), builds Swarm and the app from source, and serves the app only on your
# tailnet at https://NAME.TAILNET.ts.net. The app then walks you through the
# rest: owner, provider sign-in, models, workspace and Connect to Claude.
# It also serves Swarm's AI gateway (Swarm Control MCP) on your tailnet at
# https://NAME.TAILNET.ts.net:8444/mcp; it answers only to AI keys you create in
# the app (none exist at first). Skip it with --no-ai-access.
# Nothing listens on the public internet and no secret is passed to this script.
#
# Later: `install.sh update` (also run by a 5-minute timer) rebuilds when the
# branch changes; `install.sh secret` prints the app's unlock secret again.
set -euo pipefail

STATE=/var/lib/swarm-headless
SRC=$STATE/src
PROJECT=$STATE/project
CONF=$STATE/install.env
UNITS=/usr/local/lib/systemd/system
CONTAINER=swarm

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
Usage: install.sh [install] [--relay URL] [--name NAME] [--ref BRANCH] [--lock-ssh] [--no-ai-access]
       install.sh update | up | secret | status

  --relay URL   your Swarm Control relay (prefills "Connect to Claude")
  --name NAME   tailnet machine name and Swarm machine name (default: swarm)
  --ref BRANCH  swarm branch to build (default: swarm-control)
  --lock-ssh    also close public SSH (use after `tailscale ssh` works)
  --no-ai-access  do not serve the AI gateway on the tailnet (port 8444)
USAGE
}

# Build both images from the checkout and (re)create the container on the
# same volumes. Recreating stops any agent run in progress.
up() {
  # shellcheck disable=SC1090
  . "$CONF"
  local commit
  commit=$(git -C "$SRC" rev-parse HEAD)
  say "Building Swarm ${SWARM_REF}@${commit:0:9} (the first build takes several minutes)"
  docker build --platform linux/amd64 -t swarm-headless:local \
    --build-arg VERSION="$SWARM_REF" --build-arg COMMIT="$commit" \
    --build-arg BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$SRC"
  docker build --platform linux/amd64 -t swarm-app:local \
    -f "$SRC/containers/headless/app/Dockerfile.local" "$SRC"
  if docker container inspect "$CONTAINER" >/dev/null 2>&1; then
    docker stop -t 15 "$CONTAINER" >/dev/null
    docker rm "$CONTAINER" >/dev/null
  fi
  # The AI gateway (scoped-token listener, port 7783) is published to host
  # loopback only; Tailscale Serve is its only way in.
  local ai=()
  [[ ${AI_ACCESS:-off} == on ]] && ai=(-p 127.0.0.1:7783:7783 -e APP_AI_URL="${APP_AI_URL:-}" -e APP_AI_IP="${APP_AI_IP:-}")
  docker run -d --name "$CONTAINER" --restart=unless-stopped --stop-timeout=15 \
    --cap-drop=ALL --security-opt=no-new-privileges --pids-limit=512 --memory=6g \
    -p 127.0.0.1:8443:8443 "${ai[@]}" \
    -e APP_ORIGIN="$APP_ORIGIN" -e APP_RELAY_URL="$APP_RELAY_URL" -e APP_DEVICE_NAME="$APP_DEVICE_NAME" \
    -v swarm-config:/etc/swarmd -v swarm-data:/var/lib/swarmd \
    -v swarm-cache:/var/cache/swarmd -v swarm-logs:/var/log/swarmd \
    --mount "type=bind,src=$PROJECT,dst=/project" \
    swarm-app:local >/dev/null
  echo "Swarm is running: ${SWARM_REF}@${commit:0:9}"
}

# Pull-based updates: rebuild only when the branch moved.
update() {
  # shellcheck disable=SC1090
  . "$CONF"
  git -C "$SRC" fetch -q origin "$SWARM_REF"
  local old new
  old=$(git -C "$SRC" rev-parse HEAD)
  new=$(git -C "$SRC" rev-parse FETCH_HEAD)
  [[ $old == "$new" ]] && return 0
  git -C "$SRC" reset -q --hard "$new"
  echo "swarm: ${old:0:9} -> ${new:0:9}"
  install_units
  up
}

install_units() {
  install -d "$UNITS"
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
  systemctl daemon-reload
  systemctl enable --now swarm-headless-update.timer >/dev/null
}

secret() {
  # The entrypoint creates it on first start.
  for _ in $(seq 1 60); do
    if docker exec "$CONTAINER" test -s /etc/swarmd/headless-app/login-secret 2>/dev/null; then
      docker exec "$CONTAINER" cat /etc/swarmd/headless-app/login-secret
      return 0
    fi
    sleep 1
  done
  die "the app has not started; check: docker logs $CONTAINER"
}

install_all() {
  local relay='' name=swarm ref=swarm-control lock_ssh=0 ai_access=on
  while (($#)); do
    case $1 in
      --relay) relay=${2:?--relay needs a URL}; shift 2 ;;
      --name) name=${2:?--name needs a value}; shift 2 ;;
      --ref) ref=${2:?--ref needs a branch}; shift 2 ;;
      --lock-ssh) lock_ssh=1; shift ;;
      --no-ai-access) ai_access=off; shift ;;
      -h|--help) usage; exit 0 ;;
      *) usage >&2; die "unknown option $1" ;;
    esac
  done
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  [[ $(uname -m) == x86_64 ]] || die "Swarm's headless build is x86_64 only (this machine is $(uname -m))"
  [[ $name =~ ^[a-z0-9][a-z0-9-]{0,40}$ ]] || die "--name must be lowercase letters, digits and dashes"
  [[ -z $relay || $relay =~ ^https://[A-Za-z0-9.-]+/?$ ]] || die "--relay must be an https origin such as https://swarm-relay.example.workers.dev"

  say "Installing Docker, Git and a firewall"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    docker.io docker-buildx git ufw python3 curl ca-certificates >/dev/null
  systemctl enable --now docker >/dev/null

  say "Joining your tailnet"
  command -v tailscale >/dev/null || curl -fsSL https://tailscale.com/install.sh | sh
  if ! tailscale status >/dev/null 2>&1; then
    echo "Open the login link below in your browser and approve this machine."
    tailscale up --ssh --hostname="$name"
  fi
  local dns
  dns=$(tailscale status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))')
  [[ $dns == *.ts.net ]] || die "no tailnet name yet: turn on MagicDNS in the Tailscale admin console (DNS page), then run this again"

  say "Firewall: nothing inbound except your tailnet"
  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  ufw allow in on tailscale0 >/dev/null
  ufw allow 41641/udp >/dev/null
  if ((lock_ssh)); then ufw delete allow OpenSSH >/dev/null 2>&1 || true; else ufw allow OpenSSH >/dev/null; fi
  ufw --force enable >/dev/null

  say "Fetching Swarm ($ref)"
  install -d -m 0755 "$STATE"
  if [[ -d $SRC/.git ]]; then
    git -C "$SRC" fetch -q origin "$ref" && git -C "$SRC" checkout -q -B "$ref" FETCH_HEAD
  else
    git clone -q --branch "$ref" https://github.com/swarm-agent/swarm.git "$SRC"
  fi
  install -d -o 10001 -g 10001 -m 0750 "$PROJECT"

  local previous_relay=''
  # shellcheck disable=SC1090
  [[ -f $CONF ]] && previous_relay=$(. "$CONF" && printf '%s' "${APP_RELAY_URL:-}")
  umask 077
  cat > "$CONF" <<CONF
SWARM_REF=$ref
APP_ORIGIN=https://$dns
APP_RELAY_URL=${relay:-$previous_relay}
APP_DEVICE_NAME=$name
AI_ACCESS=$ai_access
APP_AI_URL=https://$dns:8444/mcp
APP_AI_IP=$(tailscale ip -4 | head -n1)
CONF
  umask 022

  up
  install_units

  say "Serving the app on your tailnet"
  tailscale serve --bg 8443 >/dev/null ||
    die "tailscale serve failed: turn on HTTPS certificates in the Tailscale admin console (DNS page), then run this again"
  if [[ $ai_access == on ]]; then
    tailscale serve --bg --https=8444 http://127.0.0.1:7783 >/dev/null || die "tailscale serve for the AI gateway failed"
  else
    tailscale serve --https=8444 off >/dev/null 2>&1 || true
  fi

  local unlock
  unlock=$(secret)
  cat <<DONE

  ┌───────────────────────────────────────────────────────────────
  │ Swarm is ready. From any device on your tailnet, open:
  │
  │   https://$dns
  │
  │ Unlock secret (keep it private; you need it once per browser):
  │
  │   $unlock
  │
  │ The app walks you through the rest. AI keys for Claude Code are
  │ under "AI access over Tailscale" (https://$dns:8444/mcp).
  └───────────────────────────────────────────────────────────────
DONE
}

main() {
  case ${1:-install} in
    install) shift || true; install_all "$@" ;;
    -*) install_all "$@" ;;
    update) update ;;
    up) up ;;
    secret) secret ;;
    status) docker ps --filter "name=^${CONTAINER}$" --format '{{.Names}} {{.Status}}'; tailscale serve status ;;
    -h|--help|help) usage ;;
    *) usage >&2; exit 2 ;;
  esac
}

# Everything above is parsed before anything runs, so a partial download or an
# update replacing this file cannot execute half a script.
main "$@"
