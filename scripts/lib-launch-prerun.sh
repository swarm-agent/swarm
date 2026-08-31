#!/usr/bin/env bash

# Generic bounded parallel scheduler for the canonical launch pre-run.
# Callers define swarm_launch_prerun_run_lane <lane> <log-path>.

swarm_launch_prerun_fail() {
  printf 'launch-prerun: %s\n' "$*" >&2
  return 1
}

swarm_launch_prerun_validate_jobs() {
  local jobs="$1"
  if [[ ! "${jobs}" =~ ^[1-9][0-9]*$ ]]; then
    swarm_launch_prerun_fail "--jobs must be a positive integer"
    return 1
  fi
  if (( jobs > 8 )); then
    swarm_launch_prerun_fail "--jobs must not exceed 8"
    return 1
  fi
}

swarm_launch_prerun_run_parallel() {
  local run_dir="$1"
  local jobs="$2"
  shift 2
  local -a lanes=("$@")
  local lane pid completed_pid status started_at finished_at now active_names running_pids
  local next_index=0 active=0 failures=0 last_heartbeat=0
  local -A lane_by_pid=()
  local -A started_by_pid=()
  local -A log_by_pid=()

  swarm_launch_prerun_validate_jobs "${jobs}" || return 2
  (( ${#lanes[@]} > 0 )) || { swarm_launch_prerun_fail "no suites selected"; return 2; }
  declare -F swarm_launch_prerun_run_lane >/dev/null || { swarm_launch_prerun_fail "caller must define swarm_launch_prerun_run_lane"; return 2; }
  mkdir -p -- "${run_dir}/logs" "${run_dir}/status"
  : >"${run_dir}/summary.tsv"

  while (( next_index < ${#lanes[@]} || active > 0 )); do
    while (( next_index < ${#lanes[@]} && active < jobs )); do
      lane="${lanes[next_index]}"
      next_index=$((next_index + 1))
      started_at="$(date +%s)"
      printf '[START] %s\n' "${lane}"
      (
        set +e
        swarm_launch_prerun_run_lane "${lane}" "${run_dir}/logs/${lane}.log"
        lane_status=$?
        printf '%s\n' "${lane_status}" >"${run_dir}/status/${lane}.exit"
        exit "${lane_status}"
      ) >"${run_dir}/logs/${lane}.log" 2>&1 &
      pid=$!
      lane_by_pid["${pid}"]="${lane}"
      started_by_pid["${pid}"]="${started_at}"
      log_by_pid["${pid}"]="${run_dir}/logs/${lane}.log"
      active=$((active + 1))
    done

    completed_pid=""
    while [[ -z "${completed_pid}" ]]; do
      running_pids="$(jobs -pr)"
      for pid in "${!lane_by_pid[@]}"; do
        if ! grep -qx "${pid}" <<<"${running_pids}"; then
          completed_pid="${pid}"
          break
        fi
      done
      [[ -n "${completed_pid}" ]] && break
      now="$(date +%s)"
      if (( now - last_heartbeat >= 15 )); then
        active_names="$(printf '%s\n' "${lane_by_pid[@]}" | sort | paste -sd, -)"
        printf '[WAIT] active=%s\n' "${active_names}"
        last_heartbeat="${now}"
      fi
      sleep 1
    done
    if wait "${completed_pid}"; then
      status=0
    else
      status=$?
    fi
    if [[ -z "${lane_by_pid[${completed_pid}]:-}" ]]; then
      swarm_launch_prerun_fail "could not identify a completed suite process"
      return 2
    fi
    lane="${lane_by_pid[${completed_pid}]}"
    finished_at="$(date +%s)"
    if [[ -f "${run_dir}/status/${lane}.exit" ]]; then
      status="$(cat "${run_dir}/status/${lane}.exit")"
    fi
    printf '%s\t%s\t%s\t%s\n' "${lane}" "${status}" "${started_by_pid[${completed_pid}]}" "${finished_at}" >>"${run_dir}/summary.tsv"
    if [[ "${status}" == "0" ]]; then
      printf '[PASS] %s (%ss)\n' "${lane}" "$((finished_at - started_by_pid[${completed_pid}]))"
    else
      failures=$((failures + 1))
      printf '[FAIL] %s exit=%s log=%s\n' "${lane}" "${status}" "${log_by_pid[${completed_pid}]}" >&2
      tail -n 30 "${log_by_pid[${completed_pid}]}" >&2 || true
    fi
    unset 'lane_by_pid['"${completed_pid}"']' 'started_by_pid['"${completed_pid}"']' 'log_by_pid['"${completed_pid}"']'
    active=$((active - 1))
  done

  if (( failures > 0 )); then
    printf 'launch-prerun: FAIL (%s/%s suites failed); summary=%s\n' "${failures}" "${#lanes[@]}" "${run_dir}/summary.tsv" >&2
    return 1
  fi
  printf 'launch-prerun: PASS (%s suites); summary=%s\n' "${#lanes[@]}" "${run_dir}/summary.tsv"
}
