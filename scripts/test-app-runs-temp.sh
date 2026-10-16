#!/usr/bin/env bash
# Purpose: test test-app-runs.sh's temp ownership boundary without invoking Go
# or the daemon. A shell node probe observes the child environment and exit
# status; unset/empty TMPDIR must be private and removed even on child failure,
# while caller-owned state must survive. This is not an app/agent smoke test.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
: "${TMPDIR:?reproduction needs a caller-owned scratch directory}"
caller_tmp=$TMPDIR
probe=$(mktemp -d "$TMPDIR/app-runs-probe.XXXXXXXX")
trap 'rm -rf -- "$probe"' EXIT
cat >"$probe/node" <<'NODE'
#!/usr/bin/env bash
  [[ -n ${TMPDIR:-} && -d $TMPDIR ]] || exit 91
  printf '%s\n' "$TMPDIR"
  if [[ ${PROBE_OWNED:-0} == 1 ]]; then
    [[ $(stat -c %a "$TMPDIR") == 700 ]] || exit 92
    printf disposable >"$TMPDIR/probe"
  fi
  exit "${PROBE_STATUS:-0}"
NODE
chmod 700 "$probe/node"
export PATH="$probe:$PATH"
for mode in unset empty supplied; do
  for expected in 0 37; do
    status=0
    result=$(
      export PROBE_STATUS=$expected PROBE_OWNED=1
      case $mode in
        unset) unset TMPDIR ;;
        empty) export TMPDIR='' ;;
        supplied) export TMPDIR=$caller_tmp PROBE_OWNED=0 ;;
      esac
      bash "$root/scripts/test-app-runs.sh"
    ) || status=$?
    [[ $status == "$expected" ]] || { echo "$mode: expected $expected, got $status" >&2; exit 1; }
    if [[ $mode == supplied ]]; then
      [[ $result == "$caller_tmp" && -d $caller_tmp ]]
    else
      [[ -n $result && ! -e $result ]]
    fi
  done
done
echo 'app-runs temp ownership: PASS'
