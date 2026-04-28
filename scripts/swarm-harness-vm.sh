#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
LANE_LIB="${ROOT_DIR}/scripts/lib-lane.sh"

# shellcheck disable=SC1091
source "${LANE_LIB}"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/swarm-harness-vm.sh doctor
  ./scripts/swarm-harness-vm.sh install-host-deps
  ./scripts/swarm-harness-vm.sh setup [--repo-root PATH] [--no-sync] [--rebootstrap]
  ./scripts/swarm-harness-vm.sh track
  ./scripts/swarm-harness-vm.sh logs
  ./scripts/swarm-harness-vm.sh create
  ./scripts/swarm-harness-vm.sh start
  ./scripts/swarm-harness-vm.sh stop
  ./scripts/swarm-harness-vm.sh restart
  ./scripts/swarm-harness-vm.sh reset
  ./scripts/swarm-harness-vm.sh nuke
  ./scripts/swarm-harness-vm.sh fast
  ./scripts/swarm-harness-vm.sh status
  ./scripts/swarm-harness-vm.sh ssh [ssh-args...]
  ./scripts/swarm-harness-vm.sh shell [ssh-args...]
  ./scripts/swarm-harness-vm.sh bootstrap [--rebootstrap]
  ./scripts/swarm-harness-vm.sh sync [--repo-root PATH]
  ./scripts/swarm-harness-vm.sh provision [--repo-root PATH] [--no-sync] [--rebootstrap]
  ./scripts/swarm-harness-vm.sh run [--repo-root PATH] [--no-sync] -- <command...>
  ./scripts/swarm-harness-vm.sh local-replicate [--repo-root PATH] [--no-sync] -- [harness-args...]
  ./scripts/swarm-harness-vm.sh local-replicate-recovery [--repo-root PATH] [--no-sync] -- [harness-args...]
  ./scripts/swarm-harness-vm.sh prod-update-replay [--repo-root PATH] [--no-sync] -- [harness-args...]

Environment overrides:
  SWARM_HARNESS_VM_NAME            VM name. Default: swarm-harness
  SWARM_HARNESS_VM_USER            Guest username. Default: swarm
  SWARM_HARNESS_VM_SSH_PORT        Host loopback SSH port. Default: auto-pick from 4222-4299
  SWARM_HARNESS_VM_CPUS            Guest vCPU count. Default: 4
  SWARM_HARNESS_VM_MEMORY_MB       Guest memory in MiB. Default: 8192
  SWARM_HARNESS_VM_DISK_SIZE       Guest overlay disk size. Default: 80G
  SWARM_HARNESS_VM_IMAGE_URL       Ubuntu cloud image URL
  SWARM_HARNESS_VM_REPO_DIR        Guest repo checkout path. Default: ~/swarm-go inside the guest
  SWARM_HARNESS_VM_ALLOW_TCG       true to allow non-KVM fallback. Default: false
  SWARM_HARNESS_VM_FORCE_CREATE    true to recreate cloud-init seed/user data on create. Default: false

Behavior:
  - `setup` is the canonical one-command path for the singular reusable `swarm-harness` VM: doctor -> create/start -> bootstrap -> sync -> summary
  - `fast` is the repeat-use path: start/reuse -> verify readiness -> optional sync -> ready summary
  - `reset` restores a fresh guest from cached VM assets so you do not redownload Ubuntu or reinstall guest packages every time
  - `nuke` destroys all persisted `swarm-harness` VM state, including cached images, so the next run is a true cold start
  - `track` prints the current reusable VM summary, stamps, logs, and exact shell command so humans/agents do not need to rediscover it
  - `logs` prints the latest QEMU and serial log lines for quick failure diagnosis
  - uses a dedicated Ubuntu cloud-image guest with loopback-only SSH forwarding
  - keeps repo sync explicit via rsync; no host bind mounts are used
  - refuses to boot without writable /dev/kvm unless SWARM_HARNESS_VM_ALLOW_TCG=true
  - installs guest-side test prerequisites with apt during bootstrap, then skips repeat bootstrap when the stamp exists unless --rebootstrap is passed
  - run/local-replicate/local-replicate-recovery sync by default; pass --no-sync only when the existing guest checkout is already current
  - can install required Ubuntu host packages with install-host-deps
EOF
}

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  local name="${1:-}"
  command -v "${name}" > /dev/null 2>&1 || fail "required command not found: ${name}"
}

trim() {
  local value="${1-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

shell_quote() {
  printf '%q' "$1"
}

join_quoted() {
  local out=""
  local arg
  for arg in "$@"; do
    if [[ -n "${out}" ]]; then
      out+=" "
    fi
    out+="$(shell_quote "${arg}")"
  done
  printf '%s' "${out}"
}

state_home() {
  printf '%s\n' "$(swarm_lane_state_home)/swarm"
}

data_home() {
  printf '%s\n' "$(swarm_lane_data_home)/swarm"
}

config_home() {
  printf '%s\n' "$(swarm_lane_config_home)/swarm"
}

VM_NAME="${SWARM_HARNESS_VM_NAME:-swarm-harness}"
GUEST_USER="${SWARM_HARNESS_VM_USER:-swarm}"
VM_CPUS="${SWARM_HARNESS_VM_CPUS:-4}"
VM_MEMORY_MB="${SWARM_HARNESS_VM_MEMORY_MB:-8192}"
VM_DISK_SIZE="${SWARM_HARNESS_VM_DISK_SIZE:-80G}"
VM_IMAGE_URL="${SWARM_HARNESS_VM_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
GUEST_HOME_DIR="$(printf '/%s/%s' home "${GUEST_USER}")"
GUEST_REPO_DIR="${SWARM_HARNESS_VM_REPO_DIR:-${GUEST_HOME_DIR}/${ROOT_DIR##*/}}"
ALLOW_TCG="${SWARM_HARNESS_VM_ALLOW_TCG:-false}"
FORCE_CREATE="${SWARM_HARNESS_VM_FORCE_CREATE:-false}"
SSH_CONNECT_TIMEOUT="${SWARM_HARNESS_VM_SSH_CONNECT_TIMEOUT:-5}"
SSH_READY_TIMEOUT="${SWARM_HARNESS_VM_SSH_READY_TIMEOUT:-90}"
FORCE_REBOOTSTRAP="false"
NO_SYNC="false"
REPO_ROOT_OVERRIDE=""

VM_STATE_ROOT="$(state_home)/harness-vm/${VM_NAME}"
VM_DATA_ROOT="$(data_home)/harness-vm/${VM_NAME}"
VM_CONFIG_ROOT="$(config_home)/harness-vm/${VM_NAME}"
VM_CONFIG_FILE="${VM_CONFIG_ROOT}/vm.env"
PID_FILE="${VM_STATE_ROOT}/qemu.pid"
SERIAL_LOG="${VM_STATE_ROOT}/serial.log"
QEMU_LOG="${VM_STATE_ROOT}/qemu.log"
KNOWN_HOSTS_FILE="${VM_STATE_ROOT}/known_hosts"
SSH_KEY_FILE="${VM_CONFIG_ROOT}/id_ed25519"
SSH_PUBKEY_FILE="${SSH_KEY_FILE}.pub"
USER_DATA_FILE="${VM_CONFIG_ROOT}/user-data"
META_DATA_FILE="${VM_CONFIG_ROOT}/meta-data"
SEED_IMAGE_FILE="${VM_DATA_ROOT}/seed.img"
BASE_IMAGE_FILE="${VM_DATA_ROOT}/ubuntu-cloudimg-amd64.img"
BOOTSTRAP_IMAGE_FILE="${VM_DATA_ROOT}/bootstrap.qcow2"
OVERLAY_IMAGE_FILE="${VM_DATA_ROOT}/overlay.qcow2"
BOOTSTRAP_STAMP_FILE="${VM_CONFIG_ROOT}/bootstrap-complete"
SYNC_STAMP_FILE="${VM_STATE_ROOT}/last-sync.txt"

mkdir -p "${VM_STATE_ROOT}" "${VM_DATA_ROOT}" "${VM_CONFIG_ROOT}"

resolve_repo_root() {
  local root="${REPO_ROOT_OVERRIDE:-${ROOT_DIR}}"
  root="$(cd -- "${root}" && pwd)"
  [[ -d "${root}" ]] || fail "repo root does not exist: ${root}"
  [[ -f "${root}/go.mod" ]] || fail "repo root does not look like swarm-go: ${root}"
  printf '%s\n' "${root}"
}

write_vm_config() {
  cat >"${VM_CONFIG_FILE}" <<EOF
VM_NAME=$(shell_quote "${VM_NAME}")
GUEST_USER=$(shell_quote "${GUEST_USER}")
VM_CPUS=$(shell_quote "${VM_CPUS}")
VM_MEMORY_MB=$(shell_quote "${VM_MEMORY_MB}")
VM_DISK_SIZE=$(shell_quote "${VM_DISK_SIZE}")
VM_IMAGE_URL=$(shell_quote "${VM_IMAGE_URL}")
GUEST_REPO_DIR=$(shell_quote "${GUEST_REPO_DIR}")
ALLOW_TCG=$(shell_quote "${ALLOW_TCG}")
SSH_PORT=$(shell_quote "${SSH_PORT}")
EOF
}

load_vm_config() {
  if [[ ! -f "${VM_CONFIG_FILE}" ]]; then
    return 1
  fi
  # shellcheck disable=SC1090
  source "${VM_CONFIG_FILE}"
  : "${SSH_PORT:=}"
  [[ -n "${SSH_PORT}" ]] || fail "vm config is missing SSH_PORT: ${VM_CONFIG_FILE}"
  return 0
}

stamp_value() {
  local path="${1:-}"
  if [[ -f "${path}" ]]; then
    tr -d '\n' <"${path}"
    return 0
  fi
  printf 'missing'
}

ssh_command_string() {
  load_vm_config || fail "vm has not been created yet"
  printf 'ssh -i %q -o %q -o %q -o %q -o %q -o %q -o %q -o %q -p %q %q' \
    "${SSH_KEY_FILE}" \
    'BatchMode=yes' \
    'StrictHostKeyChecking=accept-new' \
    "UserKnownHostsFile=${KNOWN_HOSTS_FILE}" \
    "ConnectTimeout=${SSH_CONNECT_TIMEOUT}" \
    'ConnectionAttempts=1' \
    'ServerAliveInterval=30' \
    'ServerAliveCountMax=10' \
    "${SSH_PORT}" \
    "${GUEST_USER}@127.0.0.1"
}

port_in_use() {
  local port="${1:-}"
  require_command ss
  ss -H -ltn "sport = :${port}" 2>/dev/null | grep -q .
}

find_free_loopback_port() {
  local port
  for port in $(seq 4222 4299); do
    if ! port_in_use "${port}"; then
      printf '%s\n' "${port}"
      return 0
    fi
  done
  fail "unable to find a free loopback SSH port in 4222-4299"
}

ensure_ssh_keypair() {
  require_command ssh-keygen
  if [[ -f "${SSH_KEY_FILE}" && -f "${SSH_PUBKEY_FILE}" ]]; then
    return 0
  fi
  mkdir -p "$(dirname -- "${SSH_KEY_FILE}")"
  ssh-keygen -q -t ed25519 -N '' -f "${SSH_KEY_FILE}" > /dev/null
}

write_cloud_init() {
  local public_key
  public_key="$(cat -- "${SSH_PUBKEY_FILE}")"
  cat >"${USER_DATA_FILE}" <<EOF
#cloud-config
hostname: ${VM_NAME}
manage_etc_hosts: true
ssh_pwauth: false
disable_root: true
users:
  - default
  - name: ${GUEST_USER}
    gecos: Swarm Harness
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: [adm, sudo, users]
    lock_passwd: true
    ssh_authorized_keys:
      - ${public_key}
package_update: false
package_upgrade: false
EOF
  cat >"${META_DATA_FILE}" <<EOF
instance-id: ${VM_NAME}
local-hostname: ${VM_NAME}
EOF
}

build_seed_image() {
  if command -v cloud-localds > /dev/null 2>&1; then
    cloud-localds "${SEED_IMAGE_FILE}" "${USER_DATA_FILE}" "${META_DATA_FILE}"
    return 0
  fi
  if command -v genisoimage > /dev/null 2>&1; then
    genisoimage -quiet -output "${SEED_IMAGE_FILE}" -volid cidata -joliet -rock "${USER_DATA_FILE}" "${META_DATA_FILE}"
    return 0
  fi
  if command -v mkisofs > /dev/null 2>&1; then
    mkisofs -quiet -output "${SEED_IMAGE_FILE}" -volid cidata -joliet -rock "${USER_DATA_FILE}" "${META_DATA_FILE}"
    return 0
  fi
  fail "missing cloud-init seed builder; install cloud-localds or genisoimage/mkisofs"
}

ensure_base_image() {
  require_command curl
  if [[ -f "${BASE_IMAGE_FILE}" ]]; then
    return 0
  fi
  log "Downloading Ubuntu cloud image"
  curl -fL --retry 3 --retry-delay 1 "${VM_IMAGE_URL}" -o "${BASE_IMAGE_FILE}"
}

ensure_overlay_image() {
  require_command qemu-img
  if [[ -f "${OVERLAY_IMAGE_FILE}" && "${FORCE_CREATE}" != "true" ]]; then
    return 0
  fi
  local backing_file="${BASE_IMAGE_FILE}"
  if [[ -f "${BOOTSTRAP_IMAGE_FILE}" ]]; then
    backing_file="${BOOTSTRAP_IMAGE_FILE}"
  fi
  rm -f "${OVERLAY_IMAGE_FILE}"
  qemu-img create -f qcow2 -F qcow2 -b "${backing_file}" "${OVERLAY_IMAGE_FILE}" "${VM_DISK_SIZE}" > /dev/null
}

create_vm() {
  require_command qemu-img
  require_command ssh
  require_command rsync
  if ! load_vm_config; then
    SSH_PORT="${SWARM_HARNESS_VM_SSH_PORT:-}"
    if [[ -z "${SSH_PORT}" ]]; then
      SSH_PORT="$(find_free_loopback_port)"
    fi
  fi
  ensure_ssh_keypair
  ensure_base_image
  write_cloud_init
  build_seed_image
  ensure_overlay_image
  write_vm_config
  log "VM assets ready"
  log "  config: ${VM_CONFIG_FILE}"
  log "  ssh: ssh -i ${SSH_KEY_FILE} -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=${KNOWN_HOSTS_FILE} -p ${SSH_PORT} ${GUEST_USER}@127.0.0.1"
}

is_running() {
  if [[ ! -f "${PID_FILE}" ]]; then
    return 1
  fi
  local pid
  pid="$(cat -- "${PID_FILE}")"
  [[ -n "${pid}" ]] || return 1
  kill -0 "${pid}" > /dev/null 2>&1
}

require_kvm_or_allow_tcg() {
  if [[ -w /dev/kvm ]]; then
    return 0
  fi
  if [[ "${ALLOW_TCG}" == "true" ]]; then
    log "warning: /dev/kvm is not writable; using slow TCG fallback because SWARM_HARNESS_VM_ALLOW_TCG=true"
    return 0
  fi
  fail "writable /dev/kvm is required; add the current user to the kvm group or set SWARM_HARNESS_VM_ALLOW_TCG=true to accept slow fallback"
}

start_vm() {
  load_vm_config || fail "vm has not been created yet; run: ./scripts/swarm-harness-vm.sh create"
  require_command qemu-system-x86_64
  require_kvm_or_allow_tcg
  if is_running; then
    log "VM already running"
    wait_for_ssh
    return 0
  fi

  local accel_args cpu_arg
  accel_args=(-machine q35,accel=kvm)
  cpu_arg="host"
  if [[ ! -w /dev/kvm ]]; then
    accel_args=(-machine q35,accel=tcg)
    cpu_arg="max"
  fi

  : >"${QEMU_LOG}"
  : >"${SERIAL_LOG}"
  qemu-system-x86_64 \
    "${accel_args[@]}" \
    -cpu "${cpu_arg}" \
    -smp "${VM_CPUS}" \
    -m "${VM_MEMORY_MB}" \
    -name "${VM_NAME}" \
    -device virtio-rng-pci \
    -display none \
    -daemonize \
    -pidfile "${PID_FILE}" \
    -D "${QEMU_LOG}" \
    -serial "file:${SERIAL_LOG}" \
    -nic "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:${SSH_PORT}-:22" \
    -drive "if=virtio,format=qcow2,file=${OVERLAY_IMAGE_FILE}" \
    -drive "if=virtio,format=raw,file=${SEED_IMAGE_FILE},readonly=on"

  wait_for_ssh
  log "VM started"
}

stop_vm() {
  if ! load_vm_config; then
    fail "vm has not been created yet"
  fi
  if ! is_running; then
    log "VM already stopped"
    return 0
  fi
  remote_ssh true > /dev/null 2>&1 && remote_ssh sudo poweroff > /dev/null 2>&1 || true
  local pid
  pid="$(cat -- "${PID_FILE}")"
  local waited=0
  while kill -0 "${pid}" > /dev/null 2>&1; do
    sleep 1
    waited=$((waited + 1))
    if (( waited >= 30 )); then
      kill "${pid}" > /dev/null 2>&1 || true
      break
    fi
  done
  rm -f "${PID_FILE}"
  log "VM stopped"
}

status_vm() {
  load_vm_config || fail "vm has not been created yet"
  local state="stopped"
  if is_running; then
    state="running"
  fi
  cat <<EOF
name=${VM_NAME}
state=${state}
ssh_port=${SSH_PORT}
guest_user=${GUEST_USER}
guest_repo_dir=${GUEST_REPO_DIR}
config=${VM_CONFIG_FILE}
base_image=${BASE_IMAGE_FILE}
bootstrap_image=${BOOTSTRAP_IMAGE_FILE}
overlay_image=${OVERLAY_IMAGE_FILE}
serial_log=${SERIAL_LOG}
qemu_log=${QEMU_LOG}
bootstrap_stamp=${BOOTSTRAP_STAMP_FILE}
last_sync_stamp=${SYNC_STAMP_FILE}
EOF
}

track_vm() {
  load_vm_config || fail "vm has not been created yet; run: ./scripts/swarm-harness-vm.sh setup"
  local state="stopped"
  if is_running; then
    state="running"
  fi
  cat <<EOF
name=${VM_NAME}
state=${state}
ssh_port=${SSH_PORT}
guest_user=${GUEST_USER}
guest_repo_dir=${GUEST_REPO_DIR}
bootstrap_complete=$(stamp_value "${BOOTSTRAP_STAMP_FILE}")
last_sync=$(stamp_value "${SYNC_STAMP_FILE}")
config=${VM_CONFIG_FILE}
bootstrap_image=${BOOTSTRAP_IMAGE_FILE}
serial_log=${SERIAL_LOG}
qemu_log=${QEMU_LOG}
ssh_command=$(ssh_command_string)
next_shell=./scripts/swarm-harness-vm.sh shell
next_sync=./scripts/swarm-harness-vm.sh sync
example_run=./scripts/swarm-harness-vm.sh run -- pwd
EOF
}

show_logs() {
  load_vm_config || fail "vm has not been created yet; run: ./scripts/swarm-harness-vm.sh setup"
  printf '== serial log (%s) ==\n' "${SERIAL_LOG}"
  if [[ -f "${SERIAL_LOG}" ]]; then
    tail -n 40 "${SERIAL_LOG}"
  else
    printf '(missing)\n'
  fi
  printf '\n== qemu log (%s) ==\n' "${QEMU_LOG}"
  if [[ -f "${QEMU_LOG}" ]]; then
    tail -n 40 "${QEMU_LOG}"
  else
    printf '(missing)\n'
  fi
}

ssh_base_args() {
  load_vm_config || fail "vm has not been created yet"
  printf '%s\n' \
    -i "${SSH_KEY_FILE}" \
    -o "BatchMode=yes" \
    -o "StrictHostKeyChecking=accept-new" \
    -o "UserKnownHostsFile=${KNOWN_HOSTS_FILE}" \
    -o "ConnectTimeout=${SSH_CONNECT_TIMEOUT}" \
    -o "ConnectionAttempts=1" \
    -o "ServerAliveInterval=30" \
    -o "ServerAliveCountMax=10" \
    -p "${SSH_PORT}" \
    "${GUEST_USER}@127.0.0.1"
}

remote_ssh() {
  local ssh_args=()
  while IFS= read -r line; do
    ssh_args+=("${line}")
  done < <(ssh_base_args)
  ssh "${ssh_args[@]}" "$@"
}

remote_ssh_command() {
  local command_string="${1:-}"
  [[ -n "${command_string}" ]] || fail "remote_ssh_command requires a command string"
  remote_ssh "bash -lc $(shell_quote "${command_string}")"
}

wait_for_ssh() {
  local attempts=0
  local deadline=$((SECONDS + SSH_READY_TIMEOUT))
  until remote_ssh true > /dev/null 2>&1; do
    attempts=$((attempts + 1))
    if (( SECONDS >= deadline )); then
      fail "VM SSH did not become ready within ${SSH_READY_TIMEOUT}s on 127.0.0.1:${SSH_PORT}; try './scripts/swarm-harness-vm.sh restart' or './scripts/swarm-harness-vm.sh reset', then inspect ${SERIAL_LOG} and ${QEMU_LOG}"
    fi
    sleep 2
  done
}

bootstrap_vm() {
  start_vm
  if [[ -f "${BOOTSTRAP_STAMP_FILE}" && "${FORCE_REBOOTSTRAP}" != "true" ]]; then
    log "Guest bootstrap already complete; skipping package install (pass --rebootstrap to force)"
    return 0
  fi
  if [[ -f "${BOOTSTRAP_STAMP_FILE}" && "${FORCE_REBOOTSTRAP}" == "true" ]]; then
    log "Reinstalling guest prerequisites (--rebootstrap)"
  else
    log "Installing guest prerequisites"
  fi
  remote_ssh bash -se <<'EOF'
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
sudo apt-get update
sudo apt-get install -y \
  build-essential \
  ca-certificates \
  curl \
  docker.io \
  fuse-overlayfs \
  git \
  jq \
  npm \
  podman \
  pkg-config \
  rsync \
  slirp4netns \
  uidmap
sudo systemctl enable --now docker > /dev/null 2>&1 || true
sudo usermod -aG docker "$(id -un)" || true
EOF
  require_command qemu-img
  local tmp_bootstrap_image="${BOOTSTRAP_IMAGE_FILE}.tmp"
  stop_vm
  rm -f "${tmp_bootstrap_image}"
  qemu-img convert -f qcow2 -O qcow2 "${OVERLAY_IMAGE_FILE}" "${tmp_bootstrap_image}"
  mv -f "${tmp_bootstrap_image}" "${BOOTSTRAP_IMAGE_FILE}"
  date -u +%Y-%m-%dT%H:%M:%SZ >"${BOOTSTRAP_STAMP_FILE}"
  log "Guest bootstrap complete"
}

rsync_ssh_command() {
  printf 'ssh -i %q -o %q -o %q -o %q -o %q -o %q -o %q -o %q -p %q' \
    "${SSH_KEY_FILE}" \
    'BatchMode=yes' \
    'StrictHostKeyChecking=accept-new' \
    "UserKnownHostsFile=${KNOWN_HOSTS_FILE}" \
    "ConnectTimeout=${SSH_CONNECT_TIMEOUT}" \
    'ConnectionAttempts=1' \
    'ServerAliveInterval=30' \
    'ServerAliveCountMax=10' \
    "${SSH_PORT}"
}

sync_repo() {
  start_vm
  local repo_root
  repo_root="$(resolve_repo_root)"
  remote_ssh mkdir -p "${GUEST_REPO_DIR}"
  log "Syncing repo into guest"
  rsync -az --delete \
    --exclude '.git/' \
    --exclude '.cache/' \
    --exclude '.swarm/' \
    --exclude '.tmp/' \
    --exclude 'web/dist/' \
    --exclude 'web/tsconfig.tsbuildinfo' \
    -e "$(rsync_ssh_command)" \
    "${repo_root}/" "${GUEST_USER}@127.0.0.1:${GUEST_REPO_DIR}/"
  date -u +%Y-%m-%dT%H:%M:%SZ >"${SYNC_STAMP_FILE}"
}

maybe_sync_repo() {
  if [[ "${NO_SYNC}" == "true" ]]; then
    log "Skipping repo sync (--no-sync); using existing guest checkout at ${GUEST_REPO_DIR}"
    return 0
  fi
  sync_repo
}

provision_vm() {
  create_vm
  bootstrap_vm
  maybe_sync_repo
  log "Provisioned ${VM_NAME}"
}

verify_guest_ready() {
  start_vm
  local repo_dir_literal="${GUEST_REPO_DIR/#\~/$HOME}"
  remote_ssh_command "test -d $(shell_quote "${repo_dir_literal}") && test -f $(shell_quote "${repo_dir_literal}/go.mod") && test -x $(shell_quote "${repo_dir_literal}/.tools/go/bin/go") && command -v bash > /dev/null && command -v git > /dev/null && command -v npm > /dev/null && command -v podman > /dev/null && command -v docker > /dev/null"
}

fast_vm() {
  start_vm
  if [[ ! -f "${BOOTSTRAP_STAMP_FILE}" ]]; then
    bootstrap_vm
  fi
  maybe_sync_repo
  verify_guest_ready
  track_vm
}

reset_vm() {
  require_command qemu-img
  if load_vm_config && is_running; then
    stop_vm || true
  fi
  rm -rf "${VM_STATE_ROOT}"
  mkdir -p "${VM_STATE_ROOT}" "${VM_DATA_ROOT}" "${VM_CONFIG_ROOT}"
  rm -f "${OVERLAY_IMAGE_FILE}" "${KNOWN_HOSTS_FILE}"
  if [[ -f "${BOOTSTRAP_IMAGE_FILE}" ]]; then
    qemu-img create -f qcow2 -F qcow2 -b "${BOOTSTRAP_IMAGE_FILE}" "${OVERLAY_IMAGE_FILE}" "${VM_DISK_SIZE}" > /dev/null
    log "Reset ${VM_NAME} to a fresh reusable guest; next step: ./scripts/swarm-harness-vm.sh fast"
    return 0
  fi
  log "No cached bootstrap image found for ${VM_NAME}; next step: ./scripts/swarm-harness-vm.sh setup"
}

nuke_vm() {
  if load_vm_config && is_running; then
    stop_vm || true
  fi
  rm -rf "${VM_STATE_ROOT}" "${VM_DATA_ROOT}" "${VM_CONFIG_ROOT}"
  mkdir -p "${VM_STATE_ROOT}" "${VM_DATA_ROOT}" "${VM_CONFIG_ROOT}"
  log "Nuked ${VM_NAME}; next step: ./scripts/swarm-harness-vm.sh setup"
}

setup_vm() {
  doctor
  provision_vm
  verify_guest_ready
  track_vm
}

run_in_guest_repo() {
  local repo_root
  repo_root="$(resolve_repo_root)"
  : "${repo_root}"
  maybe_sync_repo
  local guest_repo_dir
  guest_repo_dir="${GUEST_REPO_DIR/#\~/$HOME}"
  local command_string
  command_string="cd $(shell_quote "${guest_repo_dir}") && $(join_quoted "$@")"
  log "[swarm-harness run] ${command_string}"
  remote_ssh_command "${command_string}"
}

ssh_in_guest() {
  start_vm
  if (( ${#ARGS[@]} == 0 )); then
    remote_ssh
    return 0
  fi
  remote_ssh_command "$(join_quoted "${ARGS[@]}")"
}

run_local_replicate() {
  run_in_guest_repo ./tests/swarmd/local_replicate_e2e.sh "$@"
}

run_local_replicate_recovery() {
  run_in_guest_repo ./tests/swarmd/local_replicate_recovery_e2e.sh "$@"
}

run_prod_update_replay() {
  run_in_guest_repo ./tests/swarmd/prod_update_replay_e2e.sh "$@"
}

install_host_deps() {
  require_command apt-get
  local sudo_cmd=()
  if [[ "$(id -u)" != "0" ]]; then
    require_command sudo
    sudo_cmd=(sudo)
  fi
  "${sudo_cmd[@]}" apt-get update
  "${sudo_cmd[@]}" apt-get install -y \
    cloud-image-utils \
    curl \
    genisoimage \
    openssh-client \
    qemu-system-x86 \
    qemu-utils \
    rsync
  log "Host packages installed."
  if [[ -e /dev/kvm && ! -w /dev/kvm ]]; then
    log "Next step: add the current user to the kvm group and log out/in: sudo usermod -aG kvm ${USER}"
  fi
}

doctor() {
  local missing=0
  local cmd
  for cmd in curl qemu-img qemu-system-x86_64 rsync ssh ssh-keygen ss; do
    if command -v "${cmd}" > /dev/null 2>&1; then
      printf 'ok      %s -> %s\n' "${cmd}" "$(command -v "${cmd}")"
    else
      printf 'missing %s\n' "${cmd}"
      missing=1
    fi
  done
  if command -v cloud-localds > /dev/null 2>&1 || command -v genisoimage > /dev/null 2>&1 || command -v mkisofs > /dev/null 2>&1; then
    printf 'ok      seed-image builder available\n'
  else
    printf 'missing cloud-localds or genisoimage/mkisofs\n'
    missing=1
  fi
  if [[ -e /dev/kvm ]]; then
    if [[ -w /dev/kvm ]]; then
      printf 'ok      /dev/kvm writable\n'
    else
      printf 'warn    /dev/kvm exists but is not writable by the current user\n'
      missing=1
    fi
  else
    printf 'missing /dev/kvm\n'
    missing=1
  fi
  printf '\nRecommended Ubuntu host packages:\n'
  printf '  sudo apt-get install -y qemu-system-x86 qemu-utils cloud-image-utils genisoimage rsync openssh-client curl\n'
  printf '  sudo usermod -aG kvm "$USER"   # then log out/in before starting the VM\n'
  if (( missing != 0 )); then
    fail "host is not ready for swarm-harness yet"
  fi
  log "Host is ready for swarm-harness"
}

COMMAND="${1:-}"
if [[ $# -gt 0 ]]; then
  shift
fi

case "${COMMAND}" in
  create|start|stop|restart|reset|nuke|fast|status|track|logs|doctor|install-host-deps|setup|bootstrap|sync|provision|ssh|shell|run|local-replicate|local-replicate-recovery|prod-update-replay)
    ;;
  ""|help|-h|--help)
    usage
    exit 0
    ;;
  *)
    fail "unknown command: ${COMMAND}"
    ;;
esac

ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo-root)
      shift
      [[ $# -gt 0 ]] || fail "--repo-root requires a value"
      REPO_ROOT_OVERRIDE="$1"
      ;;
    --rebootstrap)
      FORCE_REBOOTSTRAP="true"
      ;;
    --no-sync)
      NO_SYNC="true"
      ;;
    --)
      shift
      break
      ;;
    *)
      ARGS+=("$1")
      ;;
  esac
  shift
done
if [[ $# -gt 0 ]]; then
  ARGS+=("$@")
fi

case "${COMMAND}" in
  bootstrap)
    [[ "${NO_SYNC}" != "true" ]] || fail "--no-sync is not supported for bootstrap"
    ;;
  sync)
    [[ "${NO_SYNC}" != "true" ]] || fail "--no-sync is not supported for sync"
    [[ "${FORCE_REBOOTSTRAP}" != "true" ]] || fail "--rebootstrap is not supported for sync"
    ;;
  run|local-replicate|local-replicate-recovery|prod-update-replay)
    [[ "${FORCE_REBOOTSTRAP}" != "true" ]] || fail "--rebootstrap is not supported for ${COMMAND}; run bootstrap/provision --rebootstrap first"
    ;;
  create|start|stop|restart|reset|nuke|fast|status|track|logs|doctor|install-host-deps|ssh|shell)
    [[ "${NO_SYNC}" != "true" ]] || fail "--no-sync is not supported for ${COMMAND}"
    [[ "${FORCE_REBOOTSTRAP}" != "true" ]] || fail "--rebootstrap is not supported for ${COMMAND}"
    ;;
  setup)
    ;;
  provision)
    ;;
esac

case "${COMMAND}" in
  doctor)
    doctor
    ;;
  install-host-deps)
    install_host_deps
    ;;
  setup)
    setup_vm
    ;;
  create)
    create_vm
    ;;
  start)
    load_vm_config || create_vm
    start_vm
    ;;
  stop)
    stop_vm
    ;;
  restart)
    stop_vm || true
    load_vm_config || create_vm
    start_vm
    ;;
  reset)
    reset_vm
    ;;
  nuke)
    nuke_vm
    ;;
  fast)
    load_vm_config || create_vm
    fast_vm
    ;;
  status)
    status_vm
    ;;
  track)
    track_vm
    ;;
  logs)
    show_logs
    ;;
  ssh|shell)
    load_vm_config || fail "vm has not been created yet"
    ssh_in_guest
    ;;
  bootstrap)
    load_vm_config || create_vm
    bootstrap_vm
    ;;
  sync)
    load_vm_config || create_vm
    sync_repo
    ;;
  provision)
    provision_vm
    ;;
  run)
    (( ${#ARGS[@]} > 0 )) || fail "run requires a command after --"
    load_vm_config || create_vm
    run_in_guest_repo "${ARGS[@]}"
    ;;
  local-replicate)
    load_vm_config || create_vm
    run_local_replicate "${ARGS[@]}"
    ;;
  local-replicate-recovery)
    load_vm_config || create_vm
    run_local_replicate_recovery "${ARGS[@]}"
    ;;
  prod-update-replay)
    load_vm_config || create_vm
    run_prod_update_replay "${ARGS[@]}"
    ;;
esac
