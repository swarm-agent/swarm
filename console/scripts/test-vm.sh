#!/bin/bash
# A throwaway Ubuntu 24.04 machine on this PC for dogfooding a fresh Swarm
# install and onboarding, without touching the Swarm already installed here.
#
#   scripts/test-vm.sh up       boot it (downloads the cloud image once)
#   scripts/test-vm.sh ssh      shell inside it
#   scripts/test-vm.sh tunnel   open the Swarm ports through ssh (in the background)
#   scripts/test-vm.sh seed REPO BRANCH   make the installer build BRANCH of a local
#                            swarm-go checkout (pass the installer --ref BRANCH)
#   scripts/test-vm.sh status   running or not, and its ports
#   scripts/test-vm.sh down     power it off (keeps its disk)
#   scripts/test-vm.sh reset    power off and throw the disk away: next "up" is fresh
#
# VM_NAME=b runs a second machine beside the first (its own disk, ssh on
# 127.0.0.1:4224), to link two Swarms. Both have a second network card on a
# private network of their own (UDP between the two QEMUs on 127.0.0.1, no
# host network involved): the first is 192.168.77.10, b is 192.168.77.20.
#
# QEMU with KVM, user-mode networking: nothing changes on the host network and
# every port listens on 127.0.0.1 only. Swarm inside listens on its own
# loopback, so it is reached through an SSH tunnel, as a real server would be:
#   127.0.0.1:4223  -> ssh
#   scripts/test-vm.sh tunnel: 127.0.0.1:27783 -> Swarm Control, 127.0.0.1:28443 -> setup page
# State lives in target/vm (ignored by git). Size with VM_CPUS / VM_MEMORY_MB.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
name=${VM_NAME:-}
dir=$root/target/vm${name:+-$name}
base=$root/target/vm/noble-server-cloudimg-amd64.img
disk=$dir/disk.qcow2
seed=$dir/seed.iso
key=$root/target/vm/id_ed25519
pidfile=$dir/qemu.pid
cpus=${VM_CPUS:-4}
mem=${VM_MEMORY_MB:-6144}
ssh_port=4223 host=swarm-test link_ip=192.168.77.10 link_mac=52:54:00:77:00:0a link_local=4231 link_remote=4232
if [[ $name == b ]]; then
  ssh_port=4224 host=swarm-test-b link_ip=192.168.77.20 link_mac=52:54:00:77:00:0b link_local=4232 link_remote=4231
elif [[ -n $name ]]; then
  echo "VM_NAME is empty (the first machine) or b" >&2; exit 1
fi
image_url=https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img

running() { [[ -f $pidfile ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; }
vm_ssh() {
  ssh -i "$key" -p "$ssh_port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -o ConnectTimeout=5 dev@127.0.0.1 "$@"
}

up() {
  mkdir -p "$dir" "$root/target/vm"
  if running; then echo "already running"; return; fi
  [[ -w /dev/kvm ]] || { echo "/dev/kvm is not usable by this account" >&2; exit 1; }
  if [[ ! -f $base ]]; then
    echo "downloading Ubuntu 24.04 cloud image (once)"
    curl -fsSL -o "$base.part" "$image_url"
    mv "$base.part" "$base"
  fi
  [[ -f $key ]] || ssh-keygen -q -t ed25519 -N '' -C swarm-console-test-vm -f "$key"
  if [[ ! -f $disk ]]; then
    qemu-img create -q -f qcow2 -F qcow2 -b "$base" "$disk" 40G
    cat >"$dir/user-data" <<CLOUD
#cloud-config
hostname: $host
users:
  - name: dev
    groups: [sudo]
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys: [$(cat "$key.pub")]
package_update: false
CLOUD
    printf 'instance-id: %s-%s\nlocal-hostname: %s\n' "$host" "$(date +%s)" "$host" >"$dir/meta-data"
    cloud-localds "$seed" "$dir/user-data" "$dir/meta-data"
  fi
  qemu-system-x86_64 -enable-kvm -cpu host -smp "$cpus" -m "$mem" \
    -drive file="$disk",if=virtio -drive file="$seed",if=virtio,format=raw \
    -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$ssh_port-:22" \
    -device virtio-net-pci,netdev=n0 \
    -netdev "socket,id=n1,udp=127.0.0.1:$link_remote,localaddr=127.0.0.1:$link_local" \
    -device virtio-net-pci,netdev=n1,mac=$link_mac \
    -display none -serial file:"$dir/console.log" -daemonize -pidfile "$pidfile"
  echo -n "booting"
  for _ in $(seq 90); do
    if vm_ssh true 2>/dev/null; then echo; link_net; echo "ready: ${name:+VM_NAME=$name }scripts/test-vm.sh ssh"; return; fi
    echo -n .; sleep 2
  done
  echo; echo "no ssh after 3 minutes; see $dir/console.log" >&2; exit 1
}

# The private link network: the second card gets its fixed address (netplan,
# so it stays across reboots).
link_net() {
  vm_ssh "sudo tee /etc/netplan/60-link.yaml >/dev/null <<NET
network:
  version: 2
  ethernets:
    link0:
      match: {macaddress: \"$link_mac\"}
      addresses: [$link_ip/24]
NET
sudo chmod 600 /etc/netplan/60-link.yaml && sudo netplan apply" && echo "link network: $link_ip"
}

tunnel() {
  running || { echo "the VM is not running" >&2; exit 1; }
  if [[ -f $dir/tunnel.pid ]] && kill -0 "$(cat "$dir/tunnel.pid")" 2>/dev/null; then echo "tunnel already open"; return; fi
  ssh -i "$key" -p "$ssh_port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -o ExitOnForwardFailure=yes -N -f -L 127.0.0.1:27783:127.0.0.1:7783 -L 127.0.0.1:28443:127.0.0.1:8443 dev@127.0.0.1
  pgrep -n -f "127.0.0.1:27783:127.0.0.1:7783" >"$dir/tunnel.pid"
  echo "tunnel open: Swarm Control 127.0.0.1:27783, setup page 127.0.0.1:28443"
}

down() {
  [[ -f $dir/tunnel.pid ]] && kill "$(cat "$dir/tunnel.pid")" 2>/dev/null; rm -f "$dir/tunnel.pid"
  if running; then
    vm_ssh sudo poweroff 2>/dev/null || true
    for _ in $(seq 30); do running || break; sleep 1; done
    running && kill "$(cat "$pidfile")"
  fi
  rm -f "$pidfile"
  echo "stopped"
}

case ${1:-status} in
  up) up ;;
  ssh) shift; vm_ssh "$@" ;;
  tunnel) tunnel ;;
  seed)
    repo=${2:?seed needs a swarm-go checkout}; branch=${3:?seed needs a branch}
    running || { echo "the VM is not running" >&2; exit 1; }
    bundle=$(mktemp "${TMPDIR:-/tmp}/swarm.XXXXXX.bundle")
    git -C "$repo" bundle create "$bundle" "$branch" 2>/dev/null
    vm_ssh 'cat > ~/swarm.bundle' <"$bundle"
    rm -f "$bundle"
    vm_ssh "sudo rm -rf /var/lib/swarm-headless/src && sudo mkdir -p /var/lib/swarm-headless && sudo git clone -q --branch $branch ~/swarm.bundle /var/lib/swarm-headless/src"
    echo "seeded: the installer builds $branch (pass --ref $branch)" ;;
  down) down ;;
  reset) down; rm -f "$disk" "$seed"; echo "disk discarded: next up starts from a fresh Ubuntu" ;;
  status)
    if running; then echo "running (pid $(cat "$pidfile")) · ssh 127.0.0.1:$ssh_port · tunnel: scripts/test-vm.sh tunnel"
    else echo "stopped"; fi ;;
  *) sed -n '2,20p' "$0"; exit 1 ;;
esac
