#!/usr/bin/env bash
# Requirement: direct-root customer installation must provision its own non-root
# identity, start the real systemd service, and remain usable after reinstallation.
# Boundary: install.sh -> swarm install -> launcher system paths/systemd unit.
# This destructive scenario is only invoked INSIDE the disposable distro runner.
# It is not agent/provider execution evidence; that requires an authenticated run.
set -euo pipefail
[[ $# == 3 && $(id -u) == 0 && -d /candidate-source && -d /run/systemd/system ]]
archive_name=$1 checksum_name=$2 expected_digest=$3
[[ "$archive_name" != */* && "$checksum_name" == "$archive_name.sha256" ]]
[[ "$expected_digest" =~ ^[0-9a-fA-F]{64}$ ]]
! getent passwd swarm >/dev/null
! getent group swarm >/dev/null
[[ ! -e /usr/local/bin/swarm && ! -e /var/lib/swarm ]]
export TMPDIR=/run
candidate_root=$(mktemp -d "$TMPDIR/root-install.XXXXXX")
trap 'rm -rf -- "$candidate_root"' EXIT
cp "/candidate-source/$archive_name" "/candidate-source/$checksum_name" "$candidate_root/"
cd "$candidate_root"
[[ $(sha256sum "$archive_name" | awk '{print $1}') == "$expected_digest" ]]
printf '%s  %s\n' "$expected_digest" "$archive_name" | sha256sum -c -
mkdir extract
tar -xzf "$archive_name" -C extract
artifact="$candidate_root/extract/${archive_name%.tar.gz}"
[[ -x "$artifact/install.sh" ]]
install_candidate() {
  timeout --signal=TERM --kill-after=10s 300s env -u SUDO_USER -u SUDO_UID -u SUDO_GID \
    -u SWARM_SKIP_SYSTEMD_UNIT -u TMPDIR HOME=/root \
    "$artifact/install.sh" --artifact-root "$artifact" --service --yes
}
verify_service() {
  systemctl is-active --quiet swarm.service
  uid=$(id -u swarm) gid=$(id -g swarm)
  [[ "$uid" != 0 && "$gid" != 0 ]]
  [[ $(systemctl show -p User --value swarm.service) == "$uid" ]]
  [[ $(systemctl show -p Group --value swarm.service) == "$gid" ]]
  pid=$(systemctl show -p MainPID --value swarm.service)
  [[ "$pid" =~ ^[1-9][0-9]*$ && -r /proc/$pid/status ]]
  # Check actual real/effective/saved/fs IDs, not merely the unit declaration.
  awk -v uid="$uid" -v gid="$gid" '
    /^Uid:/ { if (NF != 5) bad=1; for (i=2;i<=5;i++) if ($i != uid) bad=1; u=1 }
    /^Gid:/ { if (NF != 5) bad=1; for (i=2;i<=5;i++) if ($i != gid) bad=1; g=1 }
    END { if (bad || !u || !g) exit 1 }
  ' "/proc/$pid/status"
  # Inspect every process in the service cgroup, including child cgroups.
  cgroup=$(systemctl show -p ControlGroup --value swarm.service)
  [[ "$cgroup" == /system.slice/swarm.service && -d "/sys/fs/cgroup$cgroup" ]]
  process_count=0
  while IFS= read -r process_file; do
    while IFS= read -r child; do
      [[ "$child" =~ ^[1-9][0-9]*$ && -r /proc/$child/status ]]
      awk -v uid="$uid" -v gid="$gid" '
        /^Uid:/ { if (NF != 5) bad=1; for (i=2;i<=5;i++) if ($i != uid) bad=1; u=1 }
        /^Gid:/ { if (NF != 5) bad=1; for (i=2;i<=5;i++) if ($i != gid) bad=1; g=1 }
        END { if (bad || !u || !g) exit 1 }
      ' "/proc/$child/status"
      process_count=$((process_count + 1))
    done < "$process_file"
  done < <(find "/sys/fs/cgroup$cgroup" -name cgroup.procs -type f)
  (( process_count > 0 ))
  # The fresh container must have no non-loopback TCP listener. Check kernel
  # socket tables rather than configured listener strings or health alone.
  awk 'NR > 1 && $4 == "0A" { split($2, address, ":");
    if (address[1] !~ /7F$/) exit 1 }' "/proc/$pid/net/tcp"
  awk 'NR > 1 && $4 == "0A" { split($2, address, ":");
    if (address[1] != "00000000000000000000000001000000") exit 1 }' "/proc/$pid/net/tcp6"
  home=$(getent passwd swarm | cut -d: -f6)
  [[ "$home" == /var/lib/swarm && $(stat -c %u:%g "$home") == "$uid:$gid" ]]
  status=$(runuser -u swarm -- env HOME="$home" /usr/local/bin/swarm status)
  grep -Fxq 'daemon_health=healthy' <<<"$status"
  grep -Fxq 'daemon_status=running' <<<"$status"
}
install_candidate
verify_service
# Create once: verification after reinstall must not repair destroyed user data.
runuser -u swarm -- env HOME="$home" bash -se <<'WORKSPACE'
set -euo pipefail
mkdir "$HOME/root-install-workspace"
cd "$HOME/root-install-workspace"
printf 'workspace-write-%s\n' "$(cat /proc/sys/kernel/random/uuid)" > probe
git init -q
[[ $(id -u) != 0 ]]
WORKSPACE
identity_before=$(getent passwd swarm)
group_before=$(getent group swarm)
workspace_before=$(sha256sum "$home/root-install-workspace/probe")
install_candidate
verify_service
[[ $(getent passwd swarm) == "$identity_before" ]]
[[ $(getent group swarm) == "$group_before" ]]
[[ -d "$home/root-install-workspace/.git" ]]
[[ $(stat -c %u:%g "$home/root-install-workspace/probe") == "$uid:$gid" ]]
[[ $(sha256sum "$home/root-install-workspace/probe") == "$workspace_before" ]]
printf 'root_identity=provisioned\nservice_ids=verified\nworkspace_owner_write=passed\nrepeat_install=passed\n'
