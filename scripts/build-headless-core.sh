#!/usr/bin/env bash
# Build a local, explicitly unqualified Headless host candidate. No installer or
# service action runs here; publication remains a separate release gate.
set -euo pipefail
umask 022
if [[ ${1:-} == --help || $# != 2 ]]; then
  echo 'Usage: bash scripts/build-headless-core.sh CANDIDATE_VERSION OUTPUT_DIRECTORY'
  echo 'Builds Linux amd64 core + operational CLI + FFF; no UI, Node, npm or Python.'
  echo 'Requires the module Go toolchain, CGO compiler, tar, jq, sha256sum and TMPDIR.'
  [[ ${1:-} == --help ]] && exit 0 || exit 2
fi
version=$1
[[ $version =~ ^candidate-[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$ ]] || { echo 'Use an explicit candidate-* version; this is not a release qualifier.' >&2; exit 2; }
: "${TMPDIR:?set TMPDIR to an isolated build directory}"
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)
output=$(realpath -m -- "$2")
[[ ! -e "$output" ]] || { echo 'Output directory must not exist.' >&2; exit 1; }
[[ $(uname -s)/$(uname -m) == Linux/x86_64 ]] || { echo 'Only native Linux amd64 is supported.' >&2; exit 1; }
source "$script_dir/lib-go.sh"
swarm_require_go "$repo_root"
for tool in jq tar sha256sum gcc; do command -v "$tool" >/dev/null; done
source_commit=$(git -C "$repo_root" rev-parse HEAD)
source_dirty=false
[[ -z $(git -C "$repo_root" status --porcelain --untracked-files=all) ]] || source_dirty=true
built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
mkdir -p "$output"
package="$output/swarm-headless-core-$version-linux-amd64"
mkdir -p "$package/bin" "$package/lib" "$package/doc" "$package/systemd"
flags="-X swarm-refactor/swarmtui/pkg/buildinfo.Version=$version -X swarm-refactor/swarmtui/pkg/buildinfo.Commit=$source_commit -X swarm-refactor/swarmtui/pkg/buildinfo.BuiltAt=$built_at"
(
  cd "$repo_root/swarmd"
  CGO_ENABLED=1 "$GO_BIN" build -mod=readonly -p 2 -trimpath -ldflags "$flags" -o "$package/bin/swarmd" ./cmd/swarm-headless
  for command in swarmctl swarm-fff-search; do
    CGO_ENABLED=1 "$GO_BIN" build -mod=readonly -p 2 -trimpath -ldflags "$flags" -o "$package/bin/$command" "./cmd/$command"
  done
  "$GO_BIN" list -mod=readonly -deps ./cmd/swarm-headless > "$output/headless-imports.txt"
)
"$GO_BIN" tool nm "$package/bin/swarmd" > "$output/headless-symbols.txt"
if grep -E 'internal/api\.ConfigureDesktop|internal/api\.\(\*Server\)\.(desktopHandler|withDesktopAssets|issueDesktopLocalSession)|internal/runtime\.New$' "$output/headless-symbols.txt"; then
  echo 'Headless composition check failed.' >&2; exit 1
fi
if grep -E 'swarm-refactor/swarmtui/internal/(app|launcher|ui)(/|$)|github.com/(gdamore/tcell|rivo/tview|charmbracelet/bubbletea)(/|$)' "$output/headless-imports.txt"; then
  echo 'Headless imports UI dependencies.' >&2; exit 1
fi
install -m 0644 "$repo_root/swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so" "$package/lib/libfff_c.so"
cp "$repo_root/LICENSE" "$repo_root/THIRD_PARTY_NOTICES.md" "$repo_root/containers/headless/FFF-LICENSE" "$package/doc/"
cp "$repo_root/containers/headless/core/README.md" "$package/doc/HEADLESS.md"
cp "$repo_root/containers/headless/core/swarm-headless.service" "$package/systemd/"
cp "$repo_root/containers/headless/core/install.sh" "$repo_root/containers/headless/core/verify.sh" "$package/"
chmod 0755 "$package/bin/"* "$package/install.sh" "$package/verify.sh"
(cd "$package" && sha256sum bin/swarmd bin/swarmctl bin/swarm-fff-search lib/libfff_c.so doc/LICENSE doc/THIRD_PARTY_NOTICES.md doc/FFF-LICENSE doc/HEADLESS.md systemd/swarm-headless.service install.sh verify.sh) > "$package/SHA256SUMS"
jq -n --arg version "$version" --arg source "$source_commit" --argjson dirty "$source_dirty" --arg built "$built_at" --arg go "$("$GO_BIN" version)" --arg libc "$(getconf GNU_LIBC_VERSION)" --arg checksum "$(sha256sum "$package/SHA256SUMS" | cut -d' ' -f1)" '{schema:"swarm.headless-core/v1",version:$version,sourceCommit:$source,sourceDirty:$dirty,builtAt:$built,platform:"linux/amd64",goToolchain:$go,buildLibc:$libc,qualification:"local-candidate",entrypoint:"bin/swarmd",composition:"headless",checksumsSHA256:$checksum,requirements:{startup:["bash","git","CA certificates","glibc and resolved shared libraries"],installation:["systemd","jq","sha256sum","useradd/runuser"],coding:["selected workspace toolchain","Bubblewrap (bwrap), unprivileged user/mount/PID/network namespaces (default local isolation; unavailable isolation blocks commands)"],media:["/opt/google/chrome/chrome for HTML/SVG capture","ffmpeg and ffprobe for video","Node when required by the user artifact"]}}' > "$package/manifest.json"
bash "$package/verify.sh" "$package"
tar -C "$output" -czf "$package.tar.gz" "$(basename "$package")"
(cd "$output" && sha256sum "$(basename "$package").tar.gz") > "$package.tar.gz.sha256"
printf 'Candidate package: %s\nNo installation or release qualification performed.\n' "$package.tar.gz"
