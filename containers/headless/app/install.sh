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
# The first visit to the app creates your login (username, password and an
# optional authenticator code) from a device on your tailnet.
# Later: `install.sh update` (also run by a 5-minute timer) rebuilds when the
# branch changes; `install.sh reset-login` deletes the login so you can create
# a new one (Swarm itself and your work are kept).
# `install.sh reinstall` wipes Swarm's state and installs it fresh with the same
# settings; `install.sh uninstall` only removes it. Both keep Docker, Tailscale,
# the firewall and (unless --delete-projects) the project folder.
set -euo pipefail

STATE=/var/lib/swarm-headless
SRC=$STATE/src
PROJECT=$STATE/project
CONF=$STATE/install.env
UNITS=/usr/local/lib/systemd/system
CONTAINER=swarm
# Swarm's own Docker network. Agents run inside the container, so traffic from
# this bridge may reach the internet (model providers, Git hosts) but never
# your tailnet, private networks, cloud metadata, or this server itself.
NETWORK=swarm-net
BRIDGE=br-swarm
SUBNET=172.31.250.0/24
BLOCKED_NETS=(100.64.0.0/10 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16)

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
Usage: install.sh [install] [--relay URL] [--name NAME] [--ref BRANCH] [--lock-ssh] [--no-ai-access]
       install.sh reinstall [--yes] [--delete-projects] [install options]
       install.sh uninstall [--yes] [--delete-projects]
       install.sh update | up | reset-login | status | check-isolation

  --relay URL   your Swarm Control relay (prefills "Connect to Claude")
  --name NAME   tailnet machine name and Swarm machine name (default: swarm)
  --ref BRANCH  swarm branch to build (default: swarm-control)
  --lock-ssh    also close public SSH (use after `tailscale ssh` works)
  --no-ai-access  do not serve the AI gateway on the tailnet (port 8444)

  reinstall / uninstall remove Swarm's container, images and state volumes
  (owner, provider sign-in, AI keys, relay pairing), its update timer and its
  Tailscale Serve entries. reinstall then installs again, reusing the saved
  --relay, --name and --ref unless you pass new ones.
  --yes              do not ask for confirmation
  --delete-projects  also delete the project folder (your workspaces)
USAGE
}

# Network isolation for the container (idempotent; also run at boot by
# swarm-headless-firewall.service, since iptables rules do not survive reboot).
# DOCKER-USER sees forwarded traffic (to the tailnet, other networks, the
# internet); INPUT sees traffic to this server itself. Replies to connections
# the host opened (the published app and gateway ports) stay allowed.
firewall() {
  docker network inspect "$NETWORK" >/dev/null 2>&1 ||
    docker network create --driver bridge --subnet "$SUBNET" \
      -o com.docker.network.bridge.name="$BRIDGE" "$NETWORK" >/dev/null
  iptables -N SWARM-ISOLATE 2>/dev/null || iptables -F SWARM-ISOLATE
  iptables -A SWARM-ISOLATE -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  local net
  for net in "${BLOCKED_NETS[@]}"; do iptables -A SWARM-ISOLATE -d "$net" -j DROP; done
  iptables -A SWARM-ISOLATE -j RETURN
  iptables -N DOCKER-USER 2>/dev/null || true
  iptables -C DOCKER-USER -i "$BRIDGE" -j SWARM-ISOLATE 2>/dev/null ||
    iptables -I DOCKER-USER -i "$BRIDGE" -j SWARM-ISOLATE
  iptables -N SWARM-HOST 2>/dev/null || iptables -F SWARM-HOST
  iptables -A SWARM-HOST -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  iptables -A SWARM-HOST -j DROP
  iptables -C INPUT -i "$BRIDGE" -j SWARM-HOST 2>/dev/null ||
    iptables -I INPUT -i "$BRIDGE" -j SWARM-HOST
}

# Prove the isolation from inside the container: the tailnet, private ranges,
# cloud metadata and the host must be unreachable; the internet reachable.
check_isolation() {
  docker container inspect "$CONTAINER" >/dev/null 2>&1 || die "Swarm is not running"
  local host_ts gateway target name want got failed=0
  host_ts=$(tailscale ip -4 2>/dev/null | head -n1)
  gateway=${SUBNET%.*}.1
  printf '%-34s %-10s %s\n' "From inside the container to" "expected" "result"
  for target in \
    "${host_ts:-100.100.100.100}:443|this server's tailnet address|blocked" \
    "100.100.100.100:53|Tailscale DNS|blocked" \
    "169.254.169.254:80|cloud metadata|blocked" \
    "$gateway:22|this server (SSH)|blocked" \
    "1.1.1.1:443|the internet|reachable"; do
    IFS='|' read -r target name want <<<"$target"
    if docker exec "$CONTAINER" timeout 4 bash -c "exec 3<>/dev/tcp/${target%:*}/${target#*:}" 2>/dev/null; then got=reachable; else got=blocked; fi
    [[ $got == "$want" ]] || failed=1
    printf '%-34s %-10s %s\n' "$name ($target)" "$want" "$got$([[ $got == "$want" ]] || echo '  <-- NOT AS EXPECTED')"
  done
  ((failed == 0)) || die "isolation check failed"
  echo "Isolation is as expected."
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
  firewall
  # Public DNS: the host's resolver may be Tailscale's (MagicDNS), which the
  # container must not reach and which would reveal tailnet names.
  docker run -d --name "$CONTAINER" --restart=unless-stopped --stop-timeout=15 \
    --cap-drop=ALL --security-opt=no-new-privileges --pids-limit=512 --memory=6g \
    --network "$NETWORK" --dns 1.1.1.1 --dns 9.9.9.9 \
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
  # Continue in the installer just fetched, not this already-loaded copy, so
  # changes to the installer itself (units, firewall, run flags) apply now.
  exec bash "$SRC/containers/headless/app/install.sh" apply
}

install_units() {
  install -d "$UNITS"
  cat > "$UNITS/swarm-headless-firewall.service" <<UNIT
[Unit]
Description=Keep headless Swarm's container off the tailnet and private networks
After=docker.service ufw.service
Requires=docker.service
PartOf=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/bash $SRC/containers/headless/app/install.sh firewall

[Install]
WantedBy=multi-user.target docker.service
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
  systemctl daemon-reload
  systemctl enable --now swarm-headless-update.timer >/dev/null
  systemctl enable swarm-headless-firewall.service >/dev/null
}

# Wait until the app answers on its loopback port.
wait_ready() {
  # shellcheck disable=SC1090
  . "$CONF"
  for _ in $(seq 1 90); do
    curl -fsS -o /dev/null -H "Host: ${APP_ORIGIN#https://}" http://127.0.0.1:8443/ 2>/dev/null && return 0
    sleep 1
  done
  die "the app has not started; check: docker logs $CONTAINER"
}

# Forgotten password or lost authenticator: delete the login (not Swarm or
# your work) and restart the app, which also signs out every browser. The next
# visit from a device on your tailnet creates a new login.
reset_login() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  docker exec "$CONTAINER" rm -f /etc/swarmd/headless-app/account.json
  docker restart -t 15 "$CONTAINER" >/dev/null
  wait_ready
  # shellcheck disable=SC1090
  . "$CONF"
  echo "Login deleted. Open $APP_ORIGIN from your own device to create a new one."
}

# Remove everything install_all created for Swarm itself. Docker, Tailscale
# and the firewall stay: Tailscale may be how you reach this server.
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
    local what='Swarm, its sign-ins, AI keys and relay pairing'
    ((delete_projects)) && what="$what, and every project in $PROJECT"
    { : </dev/tty; } 2>/dev/null || die "no terminal to confirm on; pass --yes to continue"
    printf 'This deletes %s. Type yes to continue: ' "$what" >/dev/tty
    local answer
    read -r answer </dev/tty
    [[ $answer == yes ]] || die "cancelled; nothing was changed"
  fi

  say "Removing Swarm"
  systemctl disable --now swarm-headless-update.timer >/dev/null 2>&1 || true
  systemctl disable swarm-headless-firewall.service >/dev/null 2>&1 || true
  rm -f "$UNITS/swarm-headless-update.service" "$UNITS/swarm-headless-update.timer" "$UNITS/swarm-headless-firewall.service"
  systemctl daemon-reload
  if command -v tailscale >/dev/null; then
    tailscale serve --https=443 off >/dev/null 2>&1 || true
    tailscale serve --https=8444 off >/dev/null 2>&1 || true
  fi
  if command -v docker >/dev/null; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker volume rm swarm-config swarm-data swarm-cache swarm-logs >/dev/null 2>&1 || true
    docker image rm swarm-app:local swarm-headless:local >/dev/null 2>&1 || true
    docker network rm "$NETWORK" >/dev/null 2>&1 || true
  fi
  if command -v iptables >/dev/null; then
    iptables -D DOCKER-USER -i "$BRIDGE" -j SWARM-ISOLATE 2>/dev/null || true
    iptables -D INPUT -i "$BRIDGE" -j SWARM-HOST 2>/dev/null || true
    iptables -F SWARM-ISOLATE 2>/dev/null && iptables -X SWARM-ISOLATE 2>/dev/null || true
    iptables -F SWARM-HOST 2>/dev/null && iptables -X SWARM-HOST 2>/dev/null || true
  fi
  rm -f "$CONF"
  if ((delete_projects)); then
    rm -rf "$PROJECT"
  elif [[ -d $PROJECT ]]; then
    echo "Kept your projects in $PROJECT"
  fi
  echo "Swarm is removed. Docker, Tailscale and the firewall are unchanged."
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
    mapfile -t saved < <(. "$CONF" && printf '%s\n' "${APP_RELAY_URL:-}" "${APP_DEVICE_NAME:-}" "${SWARM_REF:-}" "${AI_ACCESS:-}")
  fi
  [[ -n ${saved[0]:-} ]] && args+=(--relay "${saved[0]}")
  [[ -n ${saved[1]:-} ]] && args+=(--name "${saved[1]}")
  [[ -n ${saved[2]:-} ]] && args+=(--ref "${saved[2]}")
  [[ ${saved[3]:-} == off ]] && args+=(--no-ai-access)
  # Later options win, so anything passed now overrides the saved settings.
  uninstall_all "${uninstall[@]}"
  install_all "${args[@]}" "${install[@]}"
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
      --ai-access) ai_access=on; shift ;;
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

  wait_ready
  say "Checking that Swarm's container cannot reach your tailnet"
  check_isolation
  cat <<DONE

  ┌───────────────────────────────────────────────────────────────
  │ Swarm is ready. From your own device on your tailnet, open:
  │
  │   https://$dns
  │
  │ Create your login there (save it in your password manager);
  │ the app then walks you through the rest.
  └───────────────────────────────────────────────────────────────
DONE
}

main() {
  case ${1:-install} in
    install) shift || true; install_all "$@" ;;
    -*) install_all "$@" ;;
    reinstall) shift; reinstall_all "$@" ;;
    uninstall) shift; uninstall_all "$@" ;;
    update) update ;;
    up) up ;;
    reset-login) reset_login ;;
    apply) install_units; up ;;
    firewall) firewall ;;
    check-isolation) check_isolation ;;
    status) docker ps --filter "name=^${CONTAINER}$" --format '{{.Names}} {{.Status}}'; tailscale serve status ;;
    -h|--help|help) usage ;;
    *) usage >&2; exit 2 ;;
  esac
}

# Everything above is parsed before anything runs, so a partial download or an
# update replacing this file cannot execute half a script.
main "$@"
