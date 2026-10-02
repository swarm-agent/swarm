#!/usr/bin/env bash
# Called only after independent native promotion signature verification.
set -euo pipefail
[[ $# == 2 ]] || { echo 'usage: bash scripts/publish-headless-release.sh EVIDENCE_DIR VERSION' >&2; exit 2; }
evidence=$1 version=$2
python3 -B scripts/headless-release.py publication-gate
python3 -B scripts/headless-release.py verify --archive "$evidence/swarm-headless.oci.tar" \
  --metadata "$evidence/headless-image.json" --version "$version" --source "$GITHUB_SHA"
python3 -B - "$evidence" <<'PY'
import hashlib, json, pathlib, sys
def require(ok):
    if not ok:
        raise ValueError('qualified OCI publication binding mismatch')
p = pathlib.Path(sys.argv[1])
e = json.loads((p / 'gcp-qualification-evidence.json').read_bytes())
b = e['receipt']['binding']
proof = json.loads((p / 'gcp-build-provenance.json').read_bytes())
m = json.loads((p / 'headless-image.json').read_bytes())
for kind, name in [('oci_archive', 'swarm-headless.oci.tar'), ('oci_metadata', 'headless-image.json')]:
    require(hashlib.sha256((p / name).read_bytes()).hexdigest() == b['artifacts'][kind]['sha256'])
    require(proof['result']['outputs'][kind] == b['artifacts'][kind])
require(e['receipt']['headless'] == dict(schema='swarm.headless-qualification/v1', source_sha=b['source_sha'], run_id=b['run_id'], image=m, gates={k: 'passed' for k in ('startup','scoped-auth','sdk-session','restart')}))
PY
: "${GH_TOKEN:?GHCR token required}"
: "${TMPDIR:?scratch directory required}"
auth=$(mktemp "$TMPDIR/swarm-registry.XXXXXX")
trap 'rm -f -- "$auth"' EXIT
printf '{}\n' > "$auth"
printf '%s' "$GH_TOKEN" | skopeo login --authfile "$auth" --username "$GITHUB_ACTOR" --password-stdin ghcr.io
image=ghcr.io/swarm-agent/swarm-headless
# --preserve-digests fails if the registry requires conversion; never rebuild or normalize.
skopeo copy --authfile "$auth" --preserve-digests "oci-archive:$evidence/swarm-headless.oci.tar" "docker://$image:$version"
expected=$(jq -er .manifestDigest "$evidence/headless-image.json")
actual="sha256:$(skopeo inspect --authfile "$auth" --raw "docker://$image:$version" | sha256sum | cut -d' ' -f1)"
[[ "$actual" == "$expected" ]]
# Package only after image promotion succeeds. No source package files are mutated.
python3 -B scripts/pack-headless-release.py --evidence "$evidence" --version "$version" --source "$GITHUB_SHA"
