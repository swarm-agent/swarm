#!/usr/bin/env bash
# Local vital suites. External-machine tests remain release-pipeline-only.
set -uo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1
# CPU override regression check (without running suites):
# SWARM_TEST_CPUS=7 bash -c 'source <(sed "/^status=0/,$ d" scripts/test-vital.sh | tail -n +6); test "$GOMAXPROCS" = "$SWARM_TEST_CPUS"'
cpus=${SWARM_TEST_CPUS:-4}
export GOMAXPROCS="$cpus"
status=0
output_dir=$(mktemp -d -t swarm-vital.XXXXXX) || exit 1
trap 'rm -rf -- "$output_dir"' EXIT
run_group() {
  local index=$1
  shift
  if "$@" >"$output_dir/$index.log" 2>&1; then
    printf '0\n' >"$output_dir/$index.status"
  else
    printf '1\n' >"$output_dir/$index.status"
  fi
}
release_gate() {
  python3 - <<'PY'
import os, pathlib, re, subprocess, sys
from concurrent.futures import ThreadPoolExecutor
cpus = os.environ['GOMAXPROCS']
root = pathlib.Path.cwd()
def run_line(line):
    if not line or line.startswith('#'):
        return False
    module, package, names = line.split('|')
    names = names.split()
    cwd = root / module
    listing = subprocess.run(['go', 'test', '-p', cpus, package, '-list', '^Test'], cwd=cwd, text=True, capture_output=True, timeout=300)
    missing = set(names) - set(listing.stdout.splitlines())
    if listing.returncode or missing:
        print(listing.stderr, file=sys.stderr)
        print(f'{module}/{package}: missing release tests: {sorted(missing)}', file=sys.stderr)
        return True
    pattern = '^(' + '|'.join(re.escape(name) for name in names) + ')$'
    result = subprocess.run(['go', 'test', '-p', cpus, '-timeout=120s', package, '-run', pattern], cwd=cwd, timeout=300)
    return result.returncode != 0
with ThreadPoolExecutor(max_workers=4) as pool:
    results = list(pool.map(run_line, (root / 'scripts/test-vital-release.txt').read_text().splitlines()))
sys.exit(int(any(results)))
PY
}
# Preserve the additional selections of all checked-in critical tiers.
critical_gate() {
  python3 - <<'PY'
import os, pathlib, re, subprocess, sys
cpus = os.environ['GOMAXPROCS']
root = pathlib.Path.cwd()
text = (root / 'scripts/run-critical-tests.sh').read_text()
entries = re.findall(r"run_swarmd_tests\s+(\d+s)\s+\\\s*'([^']+)'\s+\\\s*'([^']+)'", text)
if not entries:
    raise SystemExit('critical manifest contains no literal selections')
entries = [('30s', './pkg/startupconfig ./pkg/storagecontract', 'Test(ResolveRoots|ResolveRoot|ValidateRoot|Overrides|JoinRejects|WritePreservesIdentity)', root),
           ('45s', './internal/app ./internal/client', '^Test(TaskCommand|ParseTaskCommand|CreateRoutedTaskSession)', root)] + [(*row, root / 'swarmd') for row in entries]
failed = False
for timeout, packages, pattern, cwd in entries:
    args = ['go', 'test', '-p', cpus, '-timeout='+timeout, *packages.split(), '-run', pattern]
    result = subprocess.run(args, cwd=cwd, timeout=300)
    failed |= result.returncode != 0
sys.exit(int(failed))
PY
}
extras_gate() {
  python3 - <<'PY'
import os, pathlib, re, subprocess, sys
from concurrent.futures import ThreadPoolExecutor
cpus = os.environ['GOMAXPROCS']
root = pathlib.Path.cwd()
def run_line(line):
    if not line or line.startswith('#'):
        return False
    module, package, names = line.split('|')
    names = [n for n in names.split() if n not in {'TestProjectCacheDocker', 'TestCPUDockerGitUnderLoad'}]
    pattern = '^(' + '|'.join(map(re.escape, names)) + ')$'
    return subprocess.run(['go', 'test', '-p', cpus, '-timeout=120s', package, '-run', pattern], cwd=root / module, timeout=300).returncode != 0
with ThreadPoolExecutor(max_workers=4) as pool:
    results = list(pool.map(run_line, (root / 'scripts/test-vital-extras.txt').read_text().splitlines()))
sys.exit(int(any(results)))
PY
}
web_group() { (cd web && pnpm run "$1"); }
names=('release gate' 'critical Go' 'core-path extras' 'web critical'
       'web agents' 'web orchestrator selection' 'web orchestrator'
       'installer artifact integrity')
pids=()
run_group 0 release_gate &
pids+=("$!")
run_group 1 critical_gate &
pids+=("$!")
run_group 2 extras_gate &
pids+=("$!")
(
  run_group 3 web_group test:critical
  run_group 4 web_group test:agents
  run_group 5 web_group test:orchestrator:selection
  run_group 6 web_group test:orchestrator
) &
pids+=("$!")
run_group 7 bash scripts/test-verify-fff-release.sh &
pids+=("$!")
for pid in "${pids[@]}"; do
  wait "$pid" || status=1
done
for index in "${!names[@]}"; do
  cat "$output_dir/$index.log" || status=1
  if [[ -f "$output_dir/$index.status" ]] && [[ $(<"$output_dir/$index.status") == 0 ]]; then
    printf 'PASS %s\n' "${names[$index]}"
  else
    printf 'FAIL %s\n' "${names[$index]}" >&2
    status=1
  fi
done
exit "$status"
