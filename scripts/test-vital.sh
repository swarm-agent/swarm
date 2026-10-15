#!/usr/bin/env bash
# Local vital suites. External-machine tests remain release-pipeline-only.
set -uo pipefail
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1
export GOMAXPROCS=4
status=0
run_group() {
  local name=$1
  shift
  if "$@"; then
    printf 'PASS %s\n' "$name"
  else
    printf 'FAIL %s\n' "$name" >&2
    status=1
  fi
}
release_gate() {
  python3 - <<'PY'
import pathlib, re, subprocess, sys
root = pathlib.Path.cwd()
failed = False
for line in (root / 'scripts/test-vital-release.txt').read_text().splitlines():
    if not line or line.startswith('#'):
        continue
    module, package, names = line.split('|')
    names = names.split()
    cwd = root / module
    listing = subprocess.run(['go', 'test', '-p', '4', package, '-list', '^Test'], cwd=cwd, text=True, capture_output=True, timeout=300)
    missing = set(names) - set(listing.stdout.splitlines())
    if listing.returncode or missing:
        print(listing.stderr, file=sys.stderr)
        print(f'{module}/{package}: missing release tests: {sorted(missing)}', file=sys.stderr)
        failed = True
        continue
    pattern = '^(' + '|'.join(re.escape(name) for name in names) + ')$'
    result = subprocess.run(['go', 'test', '-p', '4', '-timeout=120s', package, '-run', pattern], cwd=cwd, timeout=300)
    failed |= result.returncode != 0
sys.exit(int(failed))
PY
}
# Preserve the additional selections of all checked-in critical tiers.
critical_gate() {
  python3 - <<'PY'
import pathlib, re, subprocess, sys
root = pathlib.Path.cwd()
text = (root / 'scripts/run-critical-tests.sh').read_text()
entries = re.findall(r"run_swarmd_tests\s+(\d+s)\s+\\\s*'([^']+)'\s+\\\s*'([^']+)'", text)
if not entries:
    raise SystemExit('critical manifest contains no literal selections')
entries = [('30s', './pkg/startupconfig ./pkg/storagecontract', 'Test(ResolveRoots|ResolveRoot|ValidateRoot|Overrides|JoinRejects|WritePreservesIdentity)', root),
           ('45s', './internal/app ./internal/client', '^Test(TaskCommand|ParseTaskCommand|CreateRoutedTaskSession)', root)] + [(*row, root / 'swarmd') for row in entries]
failed = False
for timeout, packages, pattern, cwd in entries:
    args = ['go', 'test', '-p', '4', '-timeout='+timeout, *packages.split(), '-run', pattern]
    result = subprocess.run(args, cwd=cwd, timeout=300)
    failed |= result.returncode != 0
sys.exit(int(failed))
PY
}
extras_gate() {
  python3 - <<'PY'
import pathlib, re, subprocess, sys
root = pathlib.Path.cwd()
failed = False
for line in (root / 'scripts/test-vital-extras.txt').read_text().splitlines():
    if not line or line.startswith('#'):
        continue
    module, package, names = line.split('|')
    names = [n for n in names.split() if n not in {'TestProjectCacheDocker', 'TestCPUDockerGitUnderLoad'}]
    pattern = '^(' + '|'.join(map(re.escape, names)) + ')$'
    failed |= subprocess.run(['go', 'test', '-p', '4', '-timeout=120s', package, '-run', pattern], cwd=root / module, timeout=300).returncode != 0
sys.exit(int(failed))
PY
}
web_group() { (cd web && pnpm run "$1"); }
run_group 'release gate' release_gate
run_group 'critical Go' critical_gate
run_group 'core-path extras' extras_gate
run_group 'web critical' web_group test:critical
run_group 'web agents' web_group test:agents
run_group 'web orchestrator selection' web_group test:orchestrator:selection
run_group 'web orchestrator' web_group test:orchestrator
run_group 'installer artifact integrity' bash scripts/test-verify-fff-release.sh
exit "$status"
