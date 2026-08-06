#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT_DIR="$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd)"
DEST_DIR="${ROOT_DIR}/swarmd/internal/model/snapshotdata"
BASE_URL="${SWARM_MODELS_BASE_URL:-https://models.swarmagent.dev/v1}"
VERSION_URL="${BASE_URL%/}/snapshot-version.json"
SNAPSHOT_URL="${BASE_URL%/}/snapshot.json"

usage() {
  cat <<'EOF'
Usage: ./scripts/update-model-snapshot.sh [--check]

Fetch and validate the public Swarm models snapshot. By default, install the
validated snapshot as the daemon's embedded pinned snapshot. With --check,
validate the live snapshot without changing repository files.

The update is rejected unless the snapshot is internally consistent, fully
hydrated, and declares and recommends the router agent for every hydrated
provider.
EOF
}

mode=install
case "${1:-}" in
  "") ;;
  --check) mode=check ;;
  --help|-h) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac
if [ "$#" -gt 1 ]; then
  usage >&2
  exit 2
fi

require_tool() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required tool: $1" >&2
    exit 1
  fi
}

require_tool curl
require_tool install
require_tool mktemp
require_tool python3

: "${TMPDIR:?TMPDIR must be set by the run environment}"
TMP_DIR="$(mktemp -d "${TMPDIR%/}/swarm-model-snapshot.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

curl --fail --silent --show-error --location --max-time 30 \
  "$VERSION_URL" -o "${TMP_DIR}/snapshot-version.json"
curl --fail --silent --show-error --location --max-time 120 \
  "$SNAPSHOT_URL" -o "${TMP_DIR}/snapshot.json"

python3 - "${TMP_DIR}/snapshot-version.json" "${TMP_DIR}/snapshot.json" <<'PY'
import json
import pathlib
import sys

version_path = pathlib.Path(sys.argv[1])
snapshot_path = pathlib.Path(sys.argv[2])
try:
    version = json.loads(version_path.read_text(encoding="utf-8"))
    snapshot = json.loads(snapshot_path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError) as exc:
    raise SystemExit(f"invalid Swarm model snapshot JSON: {exc}")

errors = []
identity_fields = (
    "schema_version",
    "api_version",
    "snapshot_schema_version",
    "snapshot_id",
    "snapshot_version",
    "generated_at",
    "model_count",
    "provider_count",
    "hydrated_provider_count",
)
for field in identity_fields:
    if version.get(field) != snapshot.get(field):
        errors.append(
            f"{field} differs: version={version.get(field)!r} snapshot={snapshot.get(field)!r}"
        )

models = snapshot.get("models")
providers = snapshot.get("providers")
if not isinstance(models, list) or not models:
    errors.append("snapshot models must be a non-empty array")
    models = []
if not isinstance(providers, list) or not providers:
    errors.append("snapshot providers must be a non-empty array")
    providers = []
if snapshot.get("model_count") != len(models):
    errors.append(
        f"model_count={snapshot.get('model_count')!r} but models has {len(models)} records"
    )
if snapshot.get("provider_count") != len(providers):
    errors.append(
        f"provider_count={snapshot.get('provider_count')!r} but providers has {len(providers)} records"
    )

provider_model_counts = {}
model_ids_by_provider = {}
resource_names_by_provider = {}
for model in models:
    if not isinstance(model, dict):
        errors.append("snapshot contains a non-object model record")
        continue
    provider_id = str(model.get("provider_id") or "").strip()
    model_id = str(model.get("model_id") or "").strip()
    catalog_id = str(model.get("catalog_id") or "").strip()
    if not provider_id or not model_id:
        errors.append("snapshot contains a model without provider_id or model_id")
        continue
    provider_model_counts[provider_id] = provider_model_counts.get(provider_id, 0) + 1
    aliases = model_ids_by_provider.setdefault(provider_id, set())
    aliases.update(value for value in (model_id, catalog_id) if value)
    provider_specific = model.get("provider_specific")
    if isinstance(provider_specific, dict):
        specific = provider_specific.get(provider_id)
        if isinstance(specific, dict):
            resource_name = str(specific.get("resource_name") or "").strip()
            if resource_name:
                resource_names_by_provider.setdefault(provider_id, set()).add(resource_name)

provider_ids = []
hydrated_provider_ids = []
for provider in providers:
    if not isinstance(provider, dict):
        errors.append("snapshot contains a non-object provider record")
        continue
    provider_id = str(provider.get("provider_id") or "").strip()
    if not provider_id:
        errors.append("snapshot contains a provider without provider_id")
        continue
    provider_ids.append(provider_id)
    declared_count = provider.get("model_count")
    actual_count = provider_model_counts.get(provider_id, 0)
    if declared_count != actual_count:
        errors.append(
            f"provider {provider_id!r} declares model_count={declared_count!r} but has {actual_count} records"
        )
    if actual_count > 0:
        hydrated_provider_ids.append(provider_id)

if snapshot.get("hydrated_provider_count") != len(hydrated_provider_ids):
    errors.append(
        "hydrated_provider_count="
        f"{snapshot.get('hydrated_provider_count')!r} but {len(hydrated_provider_ids)} providers contain models"
    )

roles = snapshot.get("definitions", {}).get("recommendation_roles", {})
agent_defaults = roles.get("agent_defaults") if isinstance(roles, dict) else None
if not isinstance(agent_defaults, list) or "router" not in agent_defaults:
    errors.append("definitions.recommendation_roles.agent_defaults does not include router")

recommendations = snapshot.get("recommendations")
if not isinstance(recommendations, dict):
    errors.append("snapshot recommendations must be an object")
    recommendations = {}

router_summary = {}
for provider_id in hydrated_provider_ids:
    provider_recommendations = recommendations.get(provider_id)
    if not isinstance(provider_recommendations, dict):
        errors.append(f"hydrated provider {provider_id!r} has no recommendations object")
        continue
    router = provider_recommendations.get("router")
    if not isinstance(router, dict):
        errors.append(f"hydrated provider {provider_id!r} has no router recommendation")
        continue
    recommended_model = str(router.get("model") or "").strip()
    if not recommended_model:
        errors.append(f"hydrated provider {provider_id!r} has an empty router model")
        continue
    known_models = model_ids_by_provider.get(provider_id, set())
    known_resources = resource_names_by_provider.get(provider_id, set())
    if recommended_model not in known_models and recommended_model not in known_resources:
        errors.append(
            f"router recommendation {provider_id!r}/{recommended_model!r} does not resolve to a model record"
        )
    router_summary[provider_id] = recommended_model

if errors:
    print("Swarm model snapshot verification failed:", file=sys.stderr)
    for error in errors:
        print(f"- {error}", file=sys.stderr)
    raise SystemExit(1)

print(f"snapshot_id={snapshot['snapshot_id']}")
print(f"snapshot_version={snapshot['snapshot_version']}")
print(f"generated_at={snapshot['generated_at']}")
print(f"models={len(models)}")
print(f"providers={len(provider_ids)}")
print(f"hydrated_providers={len(hydrated_provider_ids)}")
print("agent_defaults=" + ",".join(agent_defaults))
print(
    "router_recommendations="
    + ",".join(f"{provider}={router_summary[provider]}" for provider in sorted(router_summary))
)
PY

if [ "$mode" = check ]; then
  echo "Snapshot verified; repository files were not changed."
  exit 0
fi

install -m 0644 "${TMP_DIR}/snapshot.json" "${DEST_DIR}/snapshot.json"
install -m 0644 "${TMP_DIR}/snapshot-version.json" "${DEST_DIR}/snapshot-version.json"
echo "Installed verified snapshot in ${DEST_DIR#"${ROOT_DIR}/"}."
