#!/usr/bin/env bash
# Purpose: `install.sh dev-sync /path/to/checkout` runs as root and builds a
# Swarm checkout that usually belongs to another user. Git refuses to read a
# repository owned by someone else ("dubious ownership"), and a local fetch
# reads the source in a separate `git upload-pack` that does not see `-c`
# options given to the outer git, so the source must be trusted inside
# upload-pack itself, for that one fetch, and never with `*` or a written
# config. The real dev_sync runs here against a temporary checkout;
# GIT_TEST_ASSUME_DIFFERENT_OWNER makes every repository look foreign without
# root or a second user, and a test-only global config trusts only the
# installer's own $SRC, as on a real machine. No network, no root.
# Run: bash install-dev-sync.test.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:?}/dev-sync.XXXXXX")
trap 'rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# The installer's functions, without running it.
# shellcheck disable=SC1090
source <(sed '$d' "$here/install.sh")

STATE=$work/state SRC=$work/state/src CONF=$work/state/install.env
mkdir -p "$STATE"
printf 'SWARM_REF=dev\n' >"$CONF"

# The other user's checkout: a Swarm checkout whose installer only records
# that dev_sync handed over to it.
checkout=$work/checkout
git init -q -b dev "$checkout"
mkdir -p "$checkout/containers/headless/app"
cat >"$checkout/containers/headless/app/install.sh" <<EOF
#!/usr/bin/env bash
echo "\$1" >"$work/handed-over"
EOF
git -C "$checkout" add -A
git -C "$checkout" -c user.name=t -c user.email=t@example.invalid commit -q -m dev-change
want=$(git -C "$checkout" rev-parse HEAD)

# The machine's own source repository, at an older commit.
git init -q -b dev "$SRC"
git -C "$SRC" -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m installed

# From here every repository is owned by someone else; only $SRC is trusted,
# through a global config that belongs to this test alone.
export GIT_CONFIG_GLOBAL=$work/gitconfig GIT_CONFIG_NOSYSTEM=1
git config --file "$GIT_CONFIG_GLOBAL" safe.directory "$SRC"
export GIT_TEST_ASSUME_DIFFERENT_OWNER=1
! git -C "$checkout" rev-parse HEAD >/dev/null 2>&1 || fail "the checkout is trusted; the test would prove nothing"
git -C "$SRC" rev-parse HEAD >/dev/null || fail "the installer's own source is not trusted"

rc=0; (dev_sync "$checkout") >"$work/out" 2>&1 || rc=$?
[[ $rc == 0 ]] || { cat "$work/out" >&2; fail "dev_sync could not read a checkout owned by another user ($rc)"; }
[[ $(git -C "$SRC" rev-parse HEAD) == "$want" ]] || fail "\$SRC is not at the checkout's HEAD"
[[ $(cat "$work/handed-over" 2>/dev/null) == apply-now ]] || fail "dev_sync did not hand over to the checkout's installer"
grep -qx "DEV_SOURCE=$(realpath "$checkout")" "$CONF" || fail "DEV_SOURCE not recorded"

# Purpose: main changes to / before dispatching dev_sync; relative checkout
# arguments must still resolve from the caller. Exercise the real fetch and
# handover from another folder (and the checkout itself), rather than a path
# parser spy, to prove DEV_SOURCE and the installed commit identify that checkout.
mkdir "$work/caller"
for relative in ../checkout ../caller/../checkout; do
  rc=0; (cd "$work/caller"; main dev-sync "$relative") >"$work/out" 2>&1 || rc=$?
  [[ $rc == 0 ]] || { cat "$work/out" >&2; fail "relative dev-sync failed ($rc)"; }
  [[ $(git -C "$SRC" rev-parse HEAD) == "$want" ]] || fail "relative dev-sync selected the wrong commit"
  grep -qx "DEV_SOURCE=$(realpath "$checkout")" "$CONF" || fail "relative DEV_SOURCE not resolved from caller"
  [[ $(cat "$work/handed-over") == apply-now ]] || fail "relative dev-sync did not hand over"
done
rc=0; (cd "$checkout"; main dev-sync .) >"$work/out" 2>&1 || rc=$?
[[ $rc == 0 ]] || { cat "$work/out" >&2; fail "dev-sync . failed ($rc)"; }
grep -qx "DEV_SOURCE=$(realpath "$checkout")" "$CONF" || fail "dev-sync . selected the wrong checkout"

# The trust is for that fetch only: nothing was written to any config.
[[ $(git config --file "$GIT_CONFIG_GLOBAL" --get-all safe.directory) == "$SRC" ]] || fail "global safe.directory changed"
! git -C "$SRC" config --local --get-all safe.directory >/dev/null || fail "safe.directory written to \$SRC's config"
! git -C "$checkout" rev-parse HEAD >/dev/null 2>&1 || fail "the checkout became trusted beyond the fetch"
! grep -q "safe.directory=['\"]\?\*" "$here/install.sh" || fail "install.sh trusts every repository"
echo 'installer dev-sync tests passed'
