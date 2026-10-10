#!/usr/bin/env bash
# One-line installer for a private, headless Swarm with its setup app.
#
# On a fresh Ubuntu 24.04 x86_64 server, as root:
#   curl -fsSL https://raw.githubusercontent.com/swarm-agent/swarm/dev/containers/headless/app/install.sh \
#     | bash -s -- --relay https://swarm-relay.YOU.workers.dev
#
# It installs Docker, gVisor and Tailscale, joins your tailnet (you open one
# login link), builds Swarm and the app from source, and serves the app only
# on your tailnet at https://NAME.TAILNET.ts.net. The app then walks you
# through the rest: owner, provider sign-in, models, workspace and Connect to
# Claude. It also serves Swarm's AI gateway (Swarm Control MCP) on your tailnet
# at https://NAME.TAILNET.ts.net:8444/mcp; it answers to AI keys you create in
# the app (none exist at first) and to tailnet devices your Tailscale policy
# grants swarmagent.dev/cap/swarm (read or write; see
# containers/headless/app/TAILNET.md). Skip it with --no-ai-access.
# To join the tailnet without opening a login link, run with TS_AUTHKEY set in
# the environment (it is passed to Tailscale through a private file, never on
# a command line); --tag TAG joins as that tag (for example tag:swarm).
# Nothing listens on the public internet and no secret is passed to this script.
#
# Swarm runs as a system service under its own "swarm" user and keeps its keys,
# login and local socket to itself. Agents' commands, and Swarm's Git on their
# projects, run in one sandbox per project: a container (under gVisor when it
# works on this machine) with no capabilities, Swarm's non-root user, memory
# and process limits, only that project and its worktrees mounted, and a
# network that reaches the internet but never this server, your tailnet,
# private networks or cloud metadata. Agents may run without permission
# prompts only while that sandbox is active.
#
# The first visit to the app creates your login (username, password and an
# optional authenticator code) from a device on your tailnet.
# Later: `install.sh update` (also run by a 5-minute timer) rebuilds when the
# branch changes; `install.sh reset-login` deletes the login so you can create
# a new one (Swarm itself and your work are kept).
# `install.sh reinstall` wipes Swarm's state and installs it fresh with the same
# settings; `install.sh uninstall` only removes it. Both keep Docker, gVisor,
# Tailscale, ufw and (unless --delete-projects) the project folder.
set -euo pipefail

STATE=/var/lib/swarm-headless
SRC=$STATE/src
PROJECT=$STATE/project
CONF=$STATE/install.env
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

# The earlier layout ran Swarm itself inside a container; reinstall/uninstall
# remove what it left behind.
LEGACY_CONTAINER=swarm
LEGACY_NETWORK=swarm-net
LEGACY_BRIDGE=br-swarm

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
Usage: install.sh [install] [--relay URL] [--name NAME] [--ref BRANCH] [--tag TAG] [--lock-ssh] [--no-ai-access] [--secrets-gateway on|off]
       install.sh reinstall [--yes] [--delete-projects] [install options]
       install.sh uninstall [--yes] [--delete-projects]
       install.sh update | up | reset-login | status | check-isolation | check-sandbox
       install.sh tailnet-identity on|off
       install.sh secrets-gateway on|off

  --relay URL   your Swarm Control relay (prefills "Connect to Claude")
  --name NAME   tailnet machine name and Swarm machine name (default: swarm)
  --ref BRANCH  swarm branch to build (default: dev)
  --tag TAG     join the tailnet as this tag, e.g. tag:swarm (the tailnet
                policy must list it under tagOwners); first join only
  --lock-ssh    also close public SSH (use after `tailscale ssh` works)
  --no-ai-access  do not serve the AI gateway on the tailnet (port 8444)
  --secrets-gateway on|off
                inject granted secrets through the egress gateway (default off;
                see secrets-gateway below)

  reinstall / uninstall remove Swarm's services, binaries, sandboxes and state
  (owner, provider sign-in, AI keys, relay pairing, agent worktrees), its
  update timer and its Tailscale Serve entries. reinstall then installs
  again, reusing the saved --relay, --name and --ref unless you pass new ones.
  --yes              do not ask for confirmation
  --delete-projects  also delete the project folder (your workspaces)

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
                   stand-ins (off by default)
USAGE
}

load_conf() {
  # shellcheck disable=SC1090
  . "$CONF"
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
  install -m 0755 /dev/stdin /usr/local/bin/swarmctl <<WRAPPER
#!/bin/sh
# swarmctl talks to Swarm's private socket, so it runs as Swarm's own user.
if [ "\$(id -un)" = $SERVICE_USER ]; then exec $OPT/current/bin/swarmctl "\$@"; fi
exec runuser -u $SERVICE_USER -- env HOME=$SERVICE_HOME LD_LIBRARY_PATH=$OPT/current/lib $OPT/current/bin/swarmctl "\$@"
WRAPPER

  firewall
  choose_runtime
  sed -i '/^SANDBOX_RUNTIME=/d' "$CONF" && echo "SANDBOX_RUNTIME=$SANDBOX_RUNTIME" >>"$CONF"
  install_units
  systemctl restart "$DAEMON_UNIT"
  systemctl restart "$APP_UNIT"
  echo "Swarm is running: ${SWARM_REF}@${commit:0:9}"
}

# Pull-based updates: rebuild only when the branch moved.
update() {
  load_conf
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
  load_conf
  local sdk_args='' runtime_arg='' secrets_arg='' release
  # The app starts only when run as the main module, which Node decides on
  # the symlink-resolved path, so its unit names the release itself.
  release=$(readlink -f "$OPT/current")
  [[ ${AI_ACCESS:-off} == on ]] && sdk_args='--container-sdk-port=7783 --container-sdk-host=127.0.0.1'
  [[ ${AI_ACCESS:-off} == on && ${TAILNET_IDENTITY:-off} == on ]] && sdk_args+=' --tailnet-identity'
  [[ ${SANDBOX_RUNTIME:-default} == default ]] && runtime_arg='--sandbox-runtime=runc'
  [[ ${SECRETS_GATEWAY:-off} == on ]] && secrets_arg=--secrets-gateway=on
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
UMask=0077
Environment=HOME=$SERVICE_HOME SWARM_DISABLE_MINT_REPORT=1
Environment=LD_LIBRARY_PATH=$OPT/current/lib
Environment=PATH=$OPT/current/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
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
ExecStart=$OPT/current/bin/swarmd --desktop-port=0 --cwd=$PROJECT --sandbox=required $runtime_arg $sdk_args $secrets_arg
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
  # loopback only; Tailscale Serve is its only way in.
  cat > "$UNITS/$APP_UNIT" <<UNIT
[Unit]
Description=Swarm setup app (tailnet only)
After=$DAEMON_UNIT
BindsTo=$DAEMON_UNIT

[Service]
User=$SERVICE_USER
Group=$SERVICE_USER
UMask=0077
Environment=HOME=$SERVICE_HOME SWARM_DISABLE_MINT_REPORT=1 APP_LISTEN=127.0.0.1 APP_PROJECT=$PROJECT
Environment=APP_ORIGIN=${APP_ORIGIN} APP_RELAY_URL=${APP_RELAY_URL} APP_DEVICE_NAME=${APP_DEVICE_NAME}
Environment=APP_AI_URL=$([[ ${AI_ACCESS:-off} == on ]] && echo "${APP_AI_URL:-}") APP_AI_IP=$([[ ${AI_ACCESS:-off} == on ]] && echo "${APP_AI_IP:-}")
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

# Wait until the app answers on its loopback port.
wait_ready() {
  load_conf
  for _ in $(seq 1 120); do
    curl -fsS -o /dev/null -H "Host: ${APP_ORIGIN#https://}" http://127.0.0.1:8443/ 2>/dev/null && return 0
    sleep 1
  done
  die "the app has not started; check: journalctl -u $DAEMON_UNIT -u $APP_UNIT"
}

# Forgotten password or lost authenticator: delete the login (not Swarm or
# your work) and restart the app, which also signs out every browser. The next
# visit from a device on your tailnet creates a new login.
reset_login() {
  [[ $EUID -eq 0 ]] || die "run as root (sudo bash)"
  rm -f "$APP_CONFIG/account.json"
  systemctl restart "$APP_UNIT"
  wait_ready
  load_conf
  echo "Login deleted. Open $APP_ORIGIN from your own device to create a new one."
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
  systemctl disable --now swarm-headless-update.timer >/dev/null 2>&1 || true
  systemctl disable --now "$APP_UNIT" "$DAEMON_UNIT" >/dev/null 2>&1 || true
  systemctl disable "$FIREWALL_UNIT" >/dev/null 2>&1 || true
  rm -f "$UNITS/swarm-headless-update.service" "$UNITS/swarm-headless-update.timer" \
    "$UNITS/$APP_UNIT" "$UNITS/$DAEMON_UNIT" "$UNITS/$FIREWALL_UNIT"
  remove_legacy_layout
  systemctl daemon-reload
  if command -v tailscale >/dev/null; then
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
  rm -rf "${DATA_DIRS[@]}" /run/swarmd "$SERVICE_HOME" "$OPT" "$STAGE" /usr/local/bin/swarmctl
  rm -f "$CONF"
  if ((delete_projects)); then
    rm -rf "$PROJECT"
  elif [[ -d $PROJECT ]]; then
    echo "Kept your projects in $PROJECT"
  fi
  echo "Swarm is removed. Docker, gVisor, Tailscale and ufw are unchanged."
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
    mapfile -t saved < <(. "$CONF" && printf '%s\n' "${APP_RELAY_URL:-}" "${APP_DEVICE_NAME:-}" "${SWARM_REF:-}" "${AI_ACCESS:-}" "${SECRETS_GATEWAY:-}")
  fi
  [[ -n ${saved[0]:-} ]] && args+=(--relay "${saved[0]}")
  [[ -n ${saved[1]:-} ]] && args+=(--name "${saved[1]}")
  # The retired container layout's branch: everything it carried is on dev.
  [[ -n ${saved[2]:-} && ${saved[2]} != swarm-control ]] && args+=(--ref "${saved[2]}")
  [[ ${saved[3]:-} == off ]] && args+=(--no-ai-access)
  [[ ${saved[4]:-} == on ]] && args+=(--secrets-gateway on)
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
  docker info --format '{{json .Runtimes}}' 2>/dev/null | grep -q '"runsc"' && return 0
  runsc install >/dev/null && systemctl restart docker
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

install_all() {
  local relay='' name=swarm ref=dev lock_ssh=0 ai_access=on tag='' secrets_gateway=off
  while (($#)); do
    case $1 in
      --relay) relay=${2:?--relay needs a URL}; shift 2 ;;
      --name) name=${2:?--name needs a value}; shift 2 ;;
      --ref) ref=${2:?--ref needs a branch}; shift 2 ;;
      --tag) tag=${2:?--tag needs a tag such as tag:swarm}; shift 2 ;;
      --lock-ssh) lock_ssh=1; shift ;;
      --no-ai-access) ai_access=off; shift ;;
      --ai-access) ai_access=on; shift ;;
      --secrets-gateway) secrets_gateway=${2:?--secrets-gateway needs on or off}; shift 2 ;;
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

  say "Installing Docker, gVisor, Git and a firewall"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    docker.io docker-buildx git ufw python3 curl ca-certificates gnupg iproute2 >/dev/null
  systemctl enable --now docker >/dev/null
  install_gvisor
  register_gvisor

  say "Joining your tailnet"
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
  local tailnet_identity=off
  if [[ $ai_access == on ]]; then
    tailscale_new_enough || tailscale update --yes >/dev/null 2>&1 || true
    if tailscale_new_enough; then
      tailnet_identity=on
    else
      echo "warning: Tailscale $(tailscale version | head -n1) is older than 1.92; the AI gateway accepts AI keys only" >&2
    fi
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
TAILNET_IDENTITY=$tailnet_identity
SECRETS_GATEWAY=$secrets_gateway
APP_AI_URL=https://$dns:8444/mcp
APP_AI_IP=$(tailscale ip -4 | head -n1)
SANDBOX_RUNTIME=default
CONF
  umask 022

  remove_legacy_layout
  up

  say "Serving the app on your tailnet"
  tailscale serve --bg 8443 >/dev/null ||
    die "tailscale serve failed: turn on HTTPS certificates in the Tailscale admin console (DNS page), then run this again"
  if [[ $ai_access == on ]]; then
    serve_ai_gateway "$tailnet_identity"
  else
    tailscale serve --https=8444 off >/dev/null 2>&1 || true
  fi

  wait_ready
  say "Checking that agent sandboxes cannot reach this server or your tailnet"
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

main() {
  case ${1:-install} in
    install) shift || true; install_all "$@" ;;
    -*) install_all "$@" ;;
    reinstall) shift; reinstall_all "$@" ;;
    uninstall) shift; uninstall_all "$@" ;;
    update) update ;;
    up) up ;;
    reset-login) reset_login ;;
    apply) remove_legacy_layout; up ;;
    firewall) firewall ;;
    check-isolation) check_isolation ;;
    check-sandbox) check_sandbox ;;
    tailnet-identity) shift; set_tailnet_identity "$@" ;;
    secrets-gateway) shift; set_secrets_gateway "$@" ;;
    status) systemctl --no-pager status "$DAEMON_UNIT" "$APP_UNIT" | grep -E '●|Active:' || true; check_sandbox; tailscale serve status ;;
    -h|--help|help) usage ;;
    *) usage >&2; exit 2 ;;
  esac
}

# Everything above is parsed before anything runs, so a partial download or an
# update replacing this file cannot execute half a script.
main "$@"
