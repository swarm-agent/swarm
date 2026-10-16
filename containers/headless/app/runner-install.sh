# Runner-only installer helpers, sourced by install.sh. No hub setup or keys.
RUNNER_STATE=/var/lib/swarm-runner-install
RUNNER_OPT=/opt/swarm-runner
RUNNER_CONF=/etc/swarm-runner

runner_require_root() { [[ $EUID -eq 0 ]] || die "run as root"; }

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
  RUNNER_RELEASE=$RUNNER_OPT/releases/$commit
}

runner_activate() {
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
  local packages=(git curl ca-certificates gnupg)
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
  install -d -m 0700 "$RUNNER_CONF"
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
  local ref elapsed=0
  ref=$(cat "$RUNNER_CONF/ref")
  [[ $ref =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || die "invalid saved runner ref"
  runner_build "$ref"
  # No runner drain/idle handshake exists yet. Never infer idle from old logs
  # or race a new claim. Fail closed until the operator stops the service.
  while systemctl is-active --quiet swarm-runner.service; do
    ((elapsed < 120)) || die "runner still active; automatic idle/drain handshake unavailable; release built but not activated"
    sleep 2
    elapsed=$((elapsed + 2))
  done
  runner_activate
  if [[ -f $RUNNER_CONF/credential ]]; then systemctl start swarm-runner.service; fi
}
