# Runner-only installer helpers, sourced by install.sh. No hub setup or keys.
RUNNER_STATE=/var/lib/swarm-runner-install
RUNNER_OPT=/opt/swarm-runner
RUNNER_CONF=/etc/swarm-runner
RUNNER_RUNTIME_STATE=/run/swarm-runner/state

runner_require_root() { [[ $EUID -eq 0 ]] || die "run as root"; }

# Use the same isolated bridge and boot-time firewall as the headless host.
# Runners have no secrets gateway, so no host port is opened.
runner_sandbox_setup() {
  install -d "$UNITS/swarm-runner.service.d"
  cat >"$UNITS/swarm-sandbox-firewall.service" <<UNIT
[Unit]
Description=Keep Swarm's agent sandboxes off this server and private networks
After=docker.service ufw.service
Requires=docker.service
PartOf=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
Environment=SWARM_SANDBOX_NETWORK=swarm-sandbox SWARM_SANDBOX_BRIDGE=br-swarm-sbx SWARM_SANDBOX_SUBNET=172.31.251.0/24 SWARM_SANDBOX_GATEWAY_PORT=
ExecStart=/bin/bash $RUNNER_STATE/src/containers/sandbox/firewall.sh

[Install]
WantedBy=multi-user.target docker.service
UNIT
  cat >"$UNITS/swarm-runner.service.d/sandbox.conf" <<'UNIT'
[Unit]
After=swarm-sandbox-firewall.service
Requires=swarm-sandbox-firewall.service
UNIT
  systemctl daemon-reload
  systemctl enable swarm-sandbox-firewall.service
  systemctl restart swarm-sandbox-firewall.service || die "runner sandbox firewall setup failed"
}

runner_sandbox_build() {
  local commit=$1
  if ! docker image inspect "swarm-sandbox:$commit" >/dev/null 2>&1; then
    docker build --platform linux/amd64 -t "swarm-sandbox:$commit" "$RUNNER_STATE/src/containers/sandbox" ||
      die "runner sandbox image build failed"
  fi
}

runner_service_exists() {
  local state
  state=$(systemctl show --property=LoadState --value swarm-runner.service) || die "could not inspect runner service"
  [[ -n $state ]] || die "could not determine runner service load state"
  [[ $state != not-found ]]
}

runner_build() {
  local ref=$1 commit
  install -d -m 0755 "$RUNNER_STATE" "$RUNNER_OPT/releases"
  if [[ -d $RUNNER_STATE/src/.git ]]; then
    git -C "$RUNNER_STATE/src" fetch -q origin "$ref"
    git -C "$RUNNER_STATE/src" checkout -q --detach FETCH_HEAD
  else
    git clone -q --branch "$ref" https://github.com/swarm-agent/swarm.git "$RUNNER_STATE/src"
  fi
  commit=$(git -C "$RUNNER_STATE/src" rev-parse HEAD)
  docker build --platform linux/amd64 --target runner-export \
    -f "$RUNNER_STATE/src/containers/headless/app/Dockerfile.host" \
    --build-arg VERSION="$ref" --build-arg COMMIT="$commit" \
    --build-arg BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -o "type=local,dest=$RUNNER_OPT/releases/$commit" "$RUNNER_STATE/src"
  chmod -R u=rwX,go=rX "$RUNNER_OPT/releases/$commit"
  runner_sandbox_build "$commit"
  RUNNER_RELEASE=$RUNNER_OPT/releases/$commit
}

runner_activate() {
  runner_sandbox_setup
  docker tag "swarm-sandbox:${RUNNER_RELEASE##*/}" swarm-sandbox:local || die "runner sandbox image activation failed"
  ln -sfn "$RUNNER_RELEASE" "$RUNNER_OPT/current.new"
  mv -Tf "$RUNNER_OPT/current.new" "$RUNNER_OPT/current"
  ln -sfn "$RUNNER_OPT/current/bin/swarmctl" "$SWARMCTL"
}

install_runner() {
  local ref=main
  while (($#)); do
    case $1 in
      --ref) ref=${2:?--ref needs a branch or tag}; shift 2 ;;
      *) die "runner accepts only --ref REF" ;;
    esac
  done
  runner_require_root
  [[ $(uname -m) == x86_64 ]] || die "runner source build supports x86_64 only"
  [[ $ref =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "invalid ref"
  [[ ! -e $CONF ]] || die "do not install a runner over a hub"
  if command -v snap >/dev/null && snap list docker >/dev/null 2>&1; then
    die "remove snap Docker before installing a runner"
  fi
  local packages=(git curl ca-certificates gnupg iptables)
  if ! command -v docker >/dev/null || ! docker buildx version >/dev/null 2>&1; then
    packages+=(docker.io docker-buildx)
  fi
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${packages[@]}"
  systemctl enable --now docker
  install_gvisor
  register_gvisor
  id swarm-runner >/dev/null 2>&1 || useradd --system --user-group --home-dir /var/lib/swarm-runner --create-home --shell /usr/sbin/nologin swarm-runner
  usermod -aG docker swarm-runner
  install -d -o root -g swarm-runner -m 0750 "$RUNNER_CONF"
  runner_build "$ref"
  runner_activate
  printf '%s\n' "$ref" >"$RUNNER_CONF/ref"
  chmod 0600 "$RUNNER_CONF/ref"
  echo 'On the hub: sudo swarmctl runner token --name RUNNER_NAME'
  echo 'Then here: sudo swarmctl runner join https://HUB --name RUNNER_NAME < join-token'
  echo 'Use a private tailnet to reach the hub; no network exposure is configured here.'
}

update_runner() {
  runner_require_root
  local ref elapsed=0 service_present=no
  ref=$(cat "$RUNNER_CONF/ref")
  [[ $ref =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "invalid saved runner ref"
  runner_build "$ref"
  # Wait for idle, then stop synchronously: SIGTERM drains a claim that races
  # the idle observation. Never replace binaries under a running landing.
  while systemctl is-active --quiet swarm-runner.service; do
    if [[ -f $RUNNER_RUNTIME_STATE ]] && [[ $(cat "$RUNNER_RUNTIME_STATE") == idle ]]; then break; fi
    ((elapsed < 1800)) || die "runner did not become idle within 30 minutes; release built but not activated"
    sleep 2
    elapsed=$((elapsed + 2))
  done
  if runner_service_exists; then
    service_present=yes
    systemctl stop swarm-runner.service || die "runner stop failed; release built but not activated"
  fi
  runner_activate
  if [[ $service_present == yes && -f $RUNNER_CONF/credential ]]; then systemctl start swarm-runner.service; fi
}
