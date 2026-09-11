#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

# A clean direct-root machine has no runtime owner yet. Build the native
# artifact without provisioning daemon state; first `swarm` owns account setup.
if [[ "${EUID}" == 0 && -z "${SUDO_UID:-}" ]]; then
  fresh=true
  for path in /usr/local/share/swarm /etc/swarmd /var/lib/swarmd /var/cache/swarmd /run/swarmd /var/log/swarmd /etc/systemd/system/swarm.service; do
    if [[ -e "${path}" || -L "${path}" ]]; then fresh=false; fi
  done
  if [[ "${fresh}" == true ]]; then
    case "$*" in
      s|systemd|"main s"|"main systemd") ;;
      *) echo 'Fresh root setup requires: ./rebuild s' >&2; exit 1 ;;
    esac
    # Do not publish a privileged executable through a writable checkout parent.
    path="${ROOT_DIR}"
    while :; do
      [[ ! -L "${path}" && "$(stat -c %u "${path}")" == 0 ]] || { echo 'Fresh root build requires a root-owned checkout and parents.' >&2; exit 1; }
      mode="$(stat -c %a "${path}")"
      (( (8#${mode} & 0022) == 0 )) || { echo 'Fresh root build refuses group/other-writable checkout parents.' >&2; exit 1; }
      [[ "${path}" == / ]] && break
      path="$(dirname -- "${path}")"
    done
    for name in swarm swarmdev rebuild swarmsetup; do
      target="/usr/local/bin/${name}"
      if [[ -e "${target}" || -L "${target}" ]]; then
        [[ -L "${target}" && "$(readlink "${target}")" == "${ROOT_DIR}/dist/linux-amd64/root/${name}" ]] || { echo "Refusing to replace existing ${target}" >&2; exit 1; }
      fi
    done
    bash "${SCRIPT_DIR}/run-critical-tests.sh" fast
    bash "${SCRIPT_DIR}/build-main-dist.sh"
    install -d -m 0755 /usr/local/bin
    for name in swarm swarmdev rebuild swarmsetup; do
      ln -sfn "${ROOT_DIR}/dist/linux-amd64/root/${name}" "/usr/local/bin/${name}"
    done
    printf '\nNative build ready. Type swarm to begin account selection and onboarding.\nNo runtime account or daemon service has been created or started.\n'
    exit 0
  fi
fi

# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib-lane.sh"

lane="$(swarm_lane_default)"
swarm_lane_export_profile "${lane}" "${ROOT_DIR}"

bash "${ROOT_DIR}/scripts/build-tools.sh"
exec "${SWARM_TOOL_BIN_DIR}/rebuild" "${SWARM_LANE}" "$@"
