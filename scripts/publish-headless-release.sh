#!/usr/bin/env bash
# Called only after independent native promotion signature verification.
set +x # Credentials below must never be emitted by inherited shell tracing.
set -euo pipefail
[[ $# == 2 ]] || { echo 'usage: bash scripts/publish-headless-release.sh EVIDENCE_DIR VERSION' >&2; exit 2; }
evidence=$1 version=$2
python3 -B scripts/headless-release.py publication-gate
python3 -B scripts/headless-release.py verify --archive "$evidence/swarm-headless.oci.tar" \
  --metadata "$evidence/headless-image.json" --version "$version" --source "$GITHUB_SHA"
python3 -B - "$evidence" <<'PY'
import hashlib, importlib.util, json, os, pathlib, sys
def require(ok):
    if not ok:
        raise ValueError('qualified OCI publication binding mismatch')
p = pathlib.Path(sys.argv[1])
e = json.loads((p / 'gcp-qualification-evidence.json').read_bytes())
if e.get('schema') == 'swarm.gcp.runner-qualification/v1':
    spec = importlib.util.spec_from_file_location('release', 'scripts/gcp-release-input.py')
    r = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(r)
    r.runner.reverify(r, p, os.environ['GITHUB_SHA'], r.decode((p / 'promotion-predicate.json').read_bytes()))
    sys.exit(0)
b = e['receipt']['binding']
proof = json.loads((p / 'gcp-build-provenance.json').read_bytes())
m = json.loads((p / 'headless-image.json').read_bytes())
for kind, name in [('oci_archive', 'swarm-headless.oci.tar'), ('oci_metadata', 'headless-image.json')]:
    require(hashlib.sha256((p / name).read_bytes()).hexdigest() == b['artifacts'][kind]['sha256'])
    require(proof['result']['outputs'][kind] == b['artifacts'][kind])
require(e['receipt']['headless'] == dict(schema='swarm.headless-qualification/v1', source_sha=b['source_sha'], run_id=b['run_id'], image=m, gates={k: 'passed' for k in ('startup','scoped-auth','sdk-session','restart')}))
PY
# Complete packaging before any external publication.
python3 -B scripts/pack-headless-release.py --evidence "$evidence" --version "$version" --source "$GITHUB_SHA"
python3 -B scripts/verify-npm-release-packs.py "$evidence" "$version"
: "${GH_TOKEN:?GHCR token required}"
: "${TMPDIR:?scratch directory required}"
auth=$(mktemp "$TMPDIR/swarm-registry.XXXXXX")
trap 'rm -f -- "$auth"' EXIT
printf '{}\n' > "$auth"
printf '%s' "$GH_TOKEN" | skopeo login --authfile "$auth" --username "$GITHUB_ACTOR" --password-stdin ghcr.io
image=ghcr.io/swarm-agent/swarm-headless
expected=$(jq -er .manifestDigest "$evidence/headless-image.json")
# Only an authenticated, explicit registry absence permits first publication.
# Protected workflow concurrency serializes our publisher; registry admins remain trusted.
state=$(python3 -B scripts/probe-headless-registry.py "$version" "$expected")
if [[ "$state" == absent ]]; then
  skopeo copy --authfile "$auth" --preserve-digests "oci-archive:$evidence/swarm-headless.oci.tar" "docker://$image:$version"
fi
actual="sha256:$(skopeo inspect --authfile "$auth" --raw "docker://$image:$version" | sha256sum | cut -d' ' -f1)"
[[ "$actual" == "$expected" ]]
# Anonymous inspection and blob pull prove fresh-machine access, not merely writer access.
pull_dir=$(mktemp -d "$TMPDIR/swarm-image-pull.XXXXXX")
trap 'rm -f -- "$auth"; rm -rf -- "$pull_dir"' EXIT
printf '{}\n' > "$pull_dir/anonymous.json"
if ! skopeo copy --authfile "$pull_dir/anonymous.json" --src-no-creds --preserve-digests "docker://$image@$expected" "oci:$pull_dir/image:verified"; then
  echo 'GHCR anonymous pull failed. Review package visibility and user/org creation policy; publication stopped. This workflow does not change visibility.' >&2
  exit 1
fi
