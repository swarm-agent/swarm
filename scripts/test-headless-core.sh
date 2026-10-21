#!/usr/bin/env bash
# Requirement/threat/owners: see tests/headlesscore/main.go. This harness gives
# real packaged processes empty canonical storage and their own network/PID/IPC
# namespaces. It cannot address a host service or host owner socket. Packaging
# failure cases exercise the actual verifier and stage installer, not mocks.
set -euo pipefail
if [[ ${1:-} == --help || $# != 1 ]]; then
  echo 'Usage: bash scripts/test-headless-core.sh EXTRACTED_PACKAGE_DIRECTORY'
  echo 'Requires Bubblewrap user/network namespaces, the module Go toolchain and TMPDIR.'
  [[ ${1:-} == --help ]] && exit 0 || exit 2
fi
: "${TMPDIR:?set TMPDIR to isolated temporary storage}"
# The fixed layout the installer creates (containers/headless/core/install.sh).
opt_dir=/opt/swarm
unit_path=/etc/systemd/system/swarm-headless.service
package=$(realpath -e -- "$1")
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)
command -v bwrap >/dev/null
bash "$package/verify.sh" "$package"
test_root=$(mktemp -d "$TMPDIR/headless-core-test.XXXXXX")
trap 'rm -rf -- "$test_root"' EXIT
bash "$package/install.sh" --stage-root "$test_root/stage"
[[ -f "$test_root/stage$unit_path" && -L "$test_root/stage$opt_dir/current" ]]
if bash "$package/install.sh" --stage-root "$test_root/stage" > "$test_root/repeat.log" 2>&1; then
  echo 'FAIL: installer accepted existing destination.' >&2; exit 1
fi
cmp "$package/bin/swarmd" "$test_root/stage$opt_dir/current/bin/swarmd"
echo 'PASS: staged fresh install matches package; repeat install refused without overwriting files'
# Real payload mutation: the verifier must reject it without installing anything.
cp -R "$package" "$test_root/tampered"
printf 'tampered\n' >> "$test_root/tampered/doc/HEADLESS.md"
if bash "$package/verify.sh" "$test_root/tampered" > "$test_root/tamper.log" 2>&1; then
  echo 'FAIL: verifier accepted changed bytes.' >&2; exit 1
fi
echo 'PASS: payload corruption rejected'
# Hashes do not cover modes: reject writable payloads even with original bytes.
cp "$package/doc/HEADLESS.md" "$test_root/tampered/doc/HEADLESS.md"
chmod o+w "$test_root/tampered/doc/HEADLESS.md"
if bash "$package/verify.sh" "$test_root/tampered" > "$test_root/mode.log" 2>&1; then
  echo 'FAIL: verifier accepted unsafe payload permissions.' >&2; exit 1
fi
echo 'PASS: unsafe payload permissions rejected with checksum-valid bytes'
# A link is rejected even when its target has exactly the expected bytes.
cp -R "$package" "$test_root/linked"
mv "$test_root/linked/doc/HEADLESS.md" "$test_root/linked-guide"
ln -s "$test_root/linked-guide" "$test_root/linked/doc/HEADLESS.md"
if bash "$package/verify.sh" "$test_root/linked" > "$test_root/link.log" 2>&1; then
  echo 'FAIL: verifier accepted a payload symlink.' >&2; exit 1
fi
echo 'PASS: payload symlink rejected'
source "$script_dir/lib-go.sh"
swarm_require_go "$repo_root"
(cd "$repo_root" && "$GO_BIN" build -mod=readonly -o "$test_root/probe" ./tests/headlesscore)
# Only system executables/libraries are visible read-only. No host /etc, /home,
# /var, /run, Docker socket, credentials, source checkout or network is mounted.
# Node's runtime is masked: setup and boot must not require the old setup app.
node_mask=()
[[ ! -e /usr/bin/node ]] || node_mask=(--ro-bind /dev/null /usr/bin/node)
isolation=(--unshare-all --die-with-parent --new-session --clearenv \
  --ro-bind /usr /usr --symlink usr/bin /bin --symlink usr/sbin /sbin \
  --symlink usr/lib /lib --symlink usr/lib64 /lib64 \
  --proc /proc --dev /dev --tmpfs /etc --tmpfs /var --tmpfs /run \
  --tmpfs /tmp --tmpfs /home --dir /home/swarm --dir /project \
  --ro-bind "$test_root/stage/opt" /opt \
  --ro-bind "$test_root/stage/usr/local/bin/swarmctl" /ctl \
  --ro-bind "$test_root/stage$unit_path" /unit/swarm-headless.service \
  --setenv PATH "$opt_dir/current/bin":/usr/bin:/bin \
  --setenv HOME /var/lib/swarm --setenv LD_LIBRARY_PATH "$opt_dir/current/lib")
bwrap "${isolation[@]}" "${node_mask[@]}" --ro-bind "$test_root/probe" /probe \
  --setenv SWARM_MODEL_CATALOG embedded --setenv SWARM_DISABLE_MINT_REPORT 1 \
  --setenv SWARM_PACKAGE_PROBE_ISOLATED 1 --chdir /project /probe
# Repeat with Bubblewrap unavailable inside a fresh outer namespace. The host
# binary remains untouched; the outer harness is already running when masked.
bwrap "${isolation[@]}" "${node_mask[@]}" --ro-bind /dev/null /usr/bin/bwrap \
  --ro-bind "$test_root/probe" /probe --setenv SWARM_MODEL_CATALOG embedded \
  --setenv SWARM_DISABLE_MINT_REPORT 1 --setenv SWARM_PACKAGE_PROBE_ISOLATED 1 \
  --setenv SWARM_PACKAGE_PROBE_MISSING_BWRAP 1 --chdir /project /probe
if command -v systemd-analyze >/dev/null; then
  bwrap "${isolation[@]}" /usr/bin/systemd-analyze verify --man=no /unit/swarm-headless.service
  echo 'PASS: systemd verifies the staged unit and executable path (no service started)'
else
  echo 'FAIL: systemd-analyze is required to verify the service definition.' >&2; exit 1
fi
