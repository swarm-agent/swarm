#!/usr/bin/env bash

# The canonical entrypoint owns suite selection. This adapter passes structured
# argv to the Linux descendant-aware supervisor, never persisted shell strings.
swarm_launch_prerun_validate_jobs() {
  [[ "$1" =~ ^[1-8]$ ]] || { printf 'launch-prerun: --jobs must be between 1 and 8\n' >&2; return 1; }
}

swarm_launch_prerun_run_parallel() {
  local run_dir="$1" jobs="$2" lane
  shift 2
  swarm_launch_prerun_validate_jobs "${jobs}" || return 2
  declare -F swarm_launch_prerun_lane_command >/dev/null || { printf 'launch-prerun: caller must define swarm_launch_prerun_lane_command\n' >&2; return 2; }
  local -a command=() records=()
  for lane in "$@"; do
    command=()
    swarm_launch_prerun_lane_command "${lane}" command || return 2
    local cleanup_seconds=0.5
    if declare -F swarm_launch_prerun_lane_cleanup_seconds >/dev/null; then
      cleanup_seconds="$(swarm_launch_prerun_lane_cleanup_seconds "${lane}")" || return 2
    fi
    records+=("$(python3 -c 'import json,sys; print(json.dumps({"id":sys.argv[1],"cleanup_seconds":float(sys.argv[2]),"argv":sys.argv[3:]}))' "${lane}" "${cleanup_seconds}" "${command[@]}")")
  done
  local supervisor
  supervisor="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/launch-prerun-supervisor.py"
  local manifest supervisor_pid status=0 old_int old_term
  manifest="$(printf '%s\n' "${records[@]}" | python3 -c 'import json,sys; print(json.dumps([json.loads(s) for s in sys.stdin if s.strip()]))')"
  old_int="$(trap -p INT)"; old_term="$(trap -p TERM)"
  python3 "${supervisor}" "${run_dir}" "${jobs}" <<<"${manifest}" &
  supervisor_pid=$!
  trap 'kill -TERM "${supervisor_pid}" 2>/dev/null || true; wait "${supervisor_pid}" || true; exit 130' INT TERM
  wait "${supervisor_pid}" || status=$?
  trap - INT TERM
  [[ -z "${old_int}" ]] || eval "${old_int}"
  [[ -z "${old_term}" ]] || eval "${old_term}"
  return "${status}"
}
