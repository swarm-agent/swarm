#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ssh-fast-test.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR%/}/swarm-ssh-fast-test-preserve.XXXXXX")"
cleanup() {
  chmod -R u+w -- "${TMP_ROOT}" 2>/dev/null || true
  rm -rf -- "${TMP_ROOT}"
}
trap cleanup EXIT

HOME="${TMP_ROOT}/home"
export HOME
mkdir -p "${HOME}/bin" "${HOME}/remote"
export PATH="${HOME}/bin:${PATH}"

SOURCE_REPO="${HOME}/remote/swarm-go"
SEED_REPO="${TMP_ROOT}/seed"
mkdir -p "${SEED_REPO}"
git -C "${SEED_REPO}" init -q
git -C "${SEED_REPO}" config user.name test
git -C "${SEED_REPO}" config user.email test@example.invalid
printf '#!/usr/bin/env bash\nexit 0\n' >"${SEED_REPO}/rebuild"
printf 'fixture\n' >"${SEED_REPO}/AGENTS.md"
chmod +x "${SEED_REPO}/rebuild"
git -C "${SEED_REPO}" add AGENTS.md rebuild
git -C "${SEED_REPO}" commit -qm base
git clone -q "${SEED_REPO}" "${SOURCE_REPO}"
git -C "${SOURCE_REPO}" checkout -qb user-feature
SOURCE_HEAD="$(git -C "${SOURCE_REPO}" rev-parse HEAD)"

cat >"${HOME}/bin/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
shift
if [[ "${1:-}" == "bash -c "* ]]; then
  exec bash -c "${1}" "${@:2}"
fi
if [[ "${1:-}" == "bash -s" ]]; then
  shift
  if [[ "${1:-}" == "--" ]]; then shift; fi
  exec bash -s -- "$@"
fi
exec bash -c "$*"
SH
chmod +x "${HOME}/bin/ssh"

cat >"${HOME}/bin/systemctl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
exit 0
SH
chmod +x "${HOME}/bin/systemctl"

"${SCRIPT}" local --remote-dir "${SOURCE_REPO}" --deploy-dir "${HOME}/deploy/swarm-go" --prepare-only --allow-dirty-committed-ref >/dev/null

[[ "$(git -C "${SOURCE_REPO}" branch --show-current)" == "user-feature" ]]
[[ "$(git -C "${SOURCE_REPO}" rev-parse HEAD)" == "${SOURCE_HEAD}" ]]
[[ "$(git -C "${HOME}/deploy/swarm-go" rev-parse HEAD)" == "$(git -C "${ROOT_DIR}" rev-parse HEAD)" ]]
printf 'ssh-fast-test source preservation: ok\n'
