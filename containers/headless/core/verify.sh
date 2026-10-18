#!/usr/bin/env bash
# Verify a reviewed, extracted package before installing any bytes.
set -euo pipefail
if [[ ${1:-} == --help || $# != 1 ]]; then
  echo 'Usage: bash verify.sh EXTRACTED_PACKAGE_DIRECTORY'
  [[ ${1:-} == --help ]] && exit 0 || exit 2
fi
package=$(realpath -e -- "$1")
[[ -d "$package" && ! -L "$1" ]] || { echo 'Package must be a real directory.' >&2; exit 1; }
cd "$package"
# Reject symlinks, devices and all unlisted payloads, including UI assets.
expected=$(printf '%s\n' SHA256SUMS manifest.json bin/swarmd bin/swarmctl bin/swarm-fff-search lib/libfff_c.so doc/LICENSE doc/THIRD_PARTY_NOTICES.md doc/FFF-LICENSE doc/HEADLESS.md systemd/swarm-headless.service install.sh verify.sh | LC_ALL=C sort)
actual=$(find . -mindepth 1 ! -type d -printf '%P\n' | LC_ALL=C sort)
[[ "$actual" == "$expected" ]] || { echo 'Unexpected or missing package payload.' >&2; exit 1; }
[[ $(find . -mindepth 1 -type d -printf '%P\n' | LC_ALL=C sort) == $(printf '%s\n' bin doc lib systemd) ]] || { echo 'Unexpected package directory.' >&2; exit 1; }
[[ -z $(find . -mindepth 1 ! -type d ! -type f -print -quit) ]] || { echo 'Package links/special files forbidden.' >&2; exit 1; }
[[ -z $(find . -perm /7022 -print -quit) ]] || { echo 'Unsafe package permissions.' >&2; exit 1; }
for executable in bin/swarmd bin/swarmctl bin/swarm-fff-search install.sh verify.sh; do
  [[ -x "$executable" ]] || { echo "Missing executable permission: $executable" >&2; exit 1; }
done
jq -e '.schema == "swarm.headless-core/v1" and .platform == "linux/amd64" and .composition == "headless" and .entrypoint == "bin/swarmd" and .qualification == "local-candidate" and (.version | test("^candidate-[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$")) and (.sourceCommit | test("^[0-9a-f]{40}$")) and (.checksumsSHA256 | test("^[0-9a-f]{64}$"))' manifest.json >/dev/null
[[ $(sha256sum SHA256SUMS | cut -d' ' -f1) == $(jq -r .checksumsSHA256 manifest.json) ]] || { echo 'Checksum inventory does not match manifest.' >&2; exit 1; }
# Validate names and fixed inventory before letting sha256sum resolve paths.
[[ $(awk 'NF != 2 || $1 !~ /^[0-9a-f]+$/ || length($1) != 64 {bad=1} END {print bad+0}' SHA256SUMS) == 0 ]]
checksum_names=$(awk '{print $2}' SHA256SUMS | LC_ALL=C sort)
required_names=$(printf '%s\n' "$expected" | sed '/^SHA256SUMS$/d; /^manifest.json$/d')
[[ "$checksum_names" == "$required_names" ]] || { echo 'Invalid checksum paths.' >&2; exit 1; }
sha256sum --check --status SHA256SUMS
printf 'PASS: fixed Headless payload and checksums verified.\n'
