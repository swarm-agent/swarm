#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/ssh-seed-v3-load-test-sessions.sh <ssh-alias> [options]

Stops remote Swarm via systemd, writes durable V3 synthetic AI sessions directly
through the Pebble V3 session mutation boundary, then leaves Swarm stopped unless
--restart is provided. This is for Desktop/TUI large-history load, scrollback,
cache, and session-switching tests.

Common examples:
  scripts/ssh-seed-v3-load-test-sessions.sh <ssh-alias>
  scripts/ssh-seed-v3-load-test-sessions.sh <ssh-alias> --counts 500,500,500,500,500 --restart
  scripts/ssh-seed-v3-load-test-sessions.sh <ssh-alias> --workspace-path /path/to/workspace

Options:
  --remote-dir <path>        Remote swarm-go checkout path; auto-discovered by default.
  --service <unit>           Remote service unit. Default: swarm.service
  --db-path <path>           Pebble DB path. Default: /var/lib/swarmd/swarmd.pebble
  --counts <csv>             Message counts to seed, newest/listed first. Default: 500,500,500,500,500
  --workspace-path <path>    Workspace path for seeded sessions. Default: latest DB session workspace, then remote checkout.
  --workspace-name <name>    Workspace display name. Default: basename of workspace path.
  --account-scope-id <id>    Account scope for seeded sessions. Default: inferred from latest DB session.
  --user-id <id>             User id for seeded sessions. Default: inferred from latest DB session.
  --title-prefix <text>      Seeded session title prefix. Default: Load Test AI Session
  --allow-empty-account      Allow seeding without an account_scope_id if inference fails.
  --no-stop                  Do not stop Swarm before opening Pebble; unsafe if service is running.
  --restart                  Restart Swarm after seeding.
  -h, --help                 Show this help.
USAGE
}

fail() {
  printf 'ssh-seed-v3-load-test-sessions: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

b64() {
  printf '%s' "$1" | base64 | tr -d '\n'
}

if [[ $# -eq 1 && ( "$1" == "-h" || "$1" == "--help" ) ]]; then
  usage
  exit 0
fi

if [[ $# -lt 1 ]]; then
  usage
  exit 2
fi

SSH_ALIAS="$1"
shift
REMOTE_DIR=""
SERVICE_UNIT="swarm.service"
DB_PATH="/var/lib/swarmd/swarmd.pebble"
COUNTS="500,500,500,500,500"
WORKSPACE_PATH=""
WORKSPACE_NAME=""
ACCOUNT_SCOPE_ID=""
USER_ID=""
TITLE_PREFIX="Load Test AI Session"
ALLOW_EMPTY_ACCOUNT="false"
STOP_SERVICE="true"
RESTART_SERVICE="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --remote-dir)
      [[ $# -ge 2 ]] || fail "--remote-dir requires a value"
      REMOTE_DIR="$2"
      shift 2
      ;;
    --service)
      [[ $# -ge 2 ]] || fail "--service requires a value"
      SERVICE_UNIT="$2"
      shift 2
      ;;
    --db-path)
      [[ $# -ge 2 ]] || fail "--db-path requires a value"
      DB_PATH="$2"
      shift 2
      ;;
    --counts)
      [[ $# -ge 2 ]] || fail "--counts requires a value"
      COUNTS="$2"
      shift 2
      ;;
    --workspace-path)
      [[ $# -ge 2 ]] || fail "--workspace-path requires a value"
      WORKSPACE_PATH="$2"
      shift 2
      ;;
    --workspace-name)
      [[ $# -ge 2 ]] || fail "--workspace-name requires a value"
      WORKSPACE_NAME="$2"
      shift 2
      ;;
    --account-scope-id)
      [[ $# -ge 2 ]] || fail "--account-scope-id requires a value"
      ACCOUNT_SCOPE_ID="$2"
      shift 2
      ;;
    --user-id)
      [[ $# -ge 2 ]] || fail "--user-id requires a value"
      USER_ID="$2"
      shift 2
      ;;
    --title-prefix)
      [[ $# -ge 2 ]] || fail "--title-prefix requires a value"
      TITLE_PREFIX="$2"
      shift 2
      ;;
    --allow-empty-account)
      ALLOW_EMPTY_ACCOUNT="true"
      shift
      ;;
    --no-stop)
      STOP_SERVICE="false"
      shift
      ;;
    --restart)
      RESTART_SERVICE="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[[ -n "${DB_PATH}" ]] || fail "empty database path"
[[ -n "${COUNTS}" ]] || fail "empty counts"

require_command ssh

if [[ -z "${REMOTE_DIR}" ]]; then
  REMOTE_DIR="$(ssh "${SSH_ALIAS}" 'set -euo pipefail
for candidate in "$HOME/swarm-go" "$HOME/src/swarm-go" "$HOME/work/swarm-go"; do
  if [ -d "$candidate" ] && [ -f "$candidate/AGENTS.md" ] && [ -d "$candidate/swarmd" ]; then
    printf "%s\n" "$candidate"
    exit 0
  fi
done
find "$HOME" /opt /srv /tmp -maxdepth 4 -type d -name swarm-go 2>/dev/null \
  | while IFS= read -r candidate; do
      if [ -f "$candidate/AGENTS.md" ] && [ -d "$candidate/swarmd" ]; then
        printf "%s\n" "$candidate"
        exit 0
      fi
    done
exit 1' 2>/dev/null)" || fail "could not find remote swarm-go checkout on ${SSH_ALIAS}; pass --remote-dir"
fi

[[ -n "${REMOTE_DIR}" ]] || fail "empty remote checkout path"
printf 'ssh-seed-v3-load-test-sessions: remote=%s dir=%s service=%s db=%s counts=%s restart=%s\n' \
  "${SSH_ALIAS}" "${REMOTE_DIR}" "${SERVICE_UNIT}" "${DB_PATH}" "${COUNTS}" "${RESTART_SERVICE}" >&2

ssh "${SSH_ALIAS}" 'bash -s' -- \
  "remote_dir_b64=$(b64 "${REMOTE_DIR}")" \
  "service_unit_b64=$(b64 "${SERVICE_UNIT}")" \
  "db_path_b64=$(b64 "${DB_PATH}")" \
  "counts_b64=$(b64 "${COUNTS}")" \
  "workspace_path_b64=$(b64 "${WORKSPACE_PATH}")" \
  "workspace_name_b64=$(b64 "${WORKSPACE_NAME}")" \
  "account_scope_id_b64=$(b64 "${ACCOUNT_SCOPE_ID}")" \
  "user_id_b64=$(b64 "${USER_ID}")" \
  "title_prefix_b64=$(b64 "${TITLE_PREFIX}")" \
  "allow_empty_account=${ALLOW_EMPTY_ACCOUNT}" \
  "stop_service=${STOP_SERVICE}" \
  "restart_service=${RESTART_SERVICE}" <<'REMOTE_SEED_V3_LOAD_TEST'
set -euo pipefail

remote_dir=""
service_unit="swarm.service"
db_path="/var/lib/swarmd/swarmd.pebble"
counts="500,500,500,500,500"
workspace_path=""
workspace_name=""
account_scope_id=""
user_id=""
title_prefix="Load Test AI Session"
allow_empty_account="false"
stop_service="true"
restart_service="false"

decode_b64() {
  if [ -z "$1" ]; then
    return 0
  fi
  printf '%s' "$1" | base64 -d
}

for arg in "$@"; do
  case "$arg" in
    remote_dir_b64=*) remote_dir="$(decode_b64 "${arg#remote_dir_b64=}")" ;;
    service_unit_b64=*) service_unit="$(decode_b64 "${arg#service_unit_b64=}")" ;;
    db_path_b64=*) db_path="$(decode_b64 "${arg#db_path_b64=}")" ;;
    counts_b64=*) counts="$(decode_b64 "${arg#counts_b64=}")" ;;
    workspace_path_b64=*) workspace_path="$(decode_b64 "${arg#workspace_path_b64=}")" ;;
    workspace_name_b64=*) workspace_name="$(decode_b64 "${arg#workspace_name_b64=}")" ;;
    account_scope_id_b64=*) account_scope_id="$(decode_b64 "${arg#account_scope_id_b64=}")" ;;
    user_id_b64=*) user_id="$(decode_b64 "${arg#user_id_b64=}")" ;;
    title_prefix_b64=*) title_prefix="$(decode_b64 "${arg#title_prefix_b64=}")" ;;
    allow_empty_account=*) allow_empty_account="${arg#allow_empty_account=}" ;;
    stop_service=*) stop_service="${arg#stop_service=}" ;;
    restart_service=*) restart_service="${arg#restart_service=}" ;;
  esac
done

[ -n "${remote_dir}" ] || { printf 'missing remote checkout path\n' >&2; exit 1; }
[ -d "${remote_dir}/swarmd" ] || { printf 'remote checkout missing swarmd directory: %s\n' "${remote_dir}" >&2; exit 1; }
[ -n "${db_path}" ] || { printf 'missing db path\n' >&2; exit 1; }

go_bin=""
for candidate in "${remote_dir}/.tools/go/bin/go" "${remote_dir}/tools/go/bin/go"; do
  if [ -x "${candidate}" ]; then
    go_bin="${candidate}"
    break
  fi
done
if [ -z "${go_bin}" ] && command -v go >/dev/null 2>&1; then
  go_bin="$(command -v go)"
fi
[ -n "${go_bin}" ] || { printf 'remote host missing go command\n' >&2; exit 1; }

service_scope=""
service_was_active="false"
if [ "${stop_service}" = 'true' ]; then
  if systemctl --user cat "${service_unit}" >/dev/null 2>&1; then
    service_scope="user"
    if systemctl --user is-active --quiet "${service_unit}"; then
      service_was_active="true"
    fi
    printf 'ssh-seed-v3-load-test-sessions: stopping user service %s\n' "${service_unit}" >&2
    systemctl --user stop "${service_unit}" >/dev/null 2>&1 || true
  elif command -v sudo >/dev/null 2>&1 && sudo -n systemctl cat "${service_unit}" >/dev/null 2>&1; then
    service_scope="system"
    if sudo -n systemctl is-active --quiet "${service_unit}"; then
      service_was_active="true"
    fi
    printf 'ssh-seed-v3-load-test-sessions: stopping system service %s\n' "${service_unit}" >&2
    sudo -n systemctl stop "${service_unit}" >/dev/null 2>&1 || true
  else
    printf 'ssh-seed-v3-load-test-sessions: service %s not found in user or sudo systemd scope; continuing\n' "${service_unit}" >&2
  fi
fi

mkdir -p "${remote_dir}/swarmd/.tmp"
tmp_dir="$(mktemp -d "${remote_dir}/swarmd/.tmp/seed-v3-load-test.XXXXXX")"
cleanup() {
  rm -rf -- "${tmp_dir}"
}
trap cleanup EXIT

cat >"${tmp_dir}/main.go" <<'GO_SEEDER'
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type seedConfig struct {
	dbPath            string
	countsRaw         string
	workspacePath     string
	workspaceName     string
	fallbackWorkspace string
	accountScopeID    string
	userID            string
	titlePrefix       string
	allowEmptyAccount bool
}

func main() {
	var cfg seedConfig
	flag.StringVar(&cfg.dbPath, "db-path", "/var/lib/swarmd/swarmd.pebble", "Pebble DB path")
	flag.StringVar(&cfg.countsRaw, "counts", "500,500,500,500,500", "comma-separated message counts")
	flag.StringVar(&cfg.workspacePath, "workspace-path", "", "workspace path for seeded sessions")
	flag.StringVar(&cfg.workspaceName, "workspace-name", "", "workspace name for seeded sessions")
	flag.StringVar(&cfg.fallbackWorkspace, "fallback-workspace-path", "", "fallback workspace path")
	flag.StringVar(&cfg.accountScopeID, "account-scope-id", "", "account scope id")
	flag.StringVar(&cfg.userID, "user-id", "", "user id")
	flag.StringVar(&cfg.titlePrefix, "title-prefix", "Load Test AI Session", "session title prefix")
	flag.BoolVar(&cfg.allowEmptyAccount, "allow-empty-account", false, "allow empty account scope id")
	flag.Parse()

	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "seed v3 load-test sessions: %v\n", err)
		os.Exit(1)
	}
}

func run(cfg seedConfig) error {
	counts, err := parseCounts(cfg.countsRaw)
	if err != nil {
		return err
	}
	store, err := pebblestore.Open(cfg.dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	sessions := pebblestore.NewSessionStore(store)
	if err := hydrateContext(sessions, &cfg); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.accountScopeID) == "" && !cfg.allowEmptyAccount {
		return errors.New("could not infer account_scope_id from existing sessions; pass --account-scope-id or --allow-empty-account")
	}
	if strings.TrimSpace(cfg.workspacePath) == "" {
		return errors.New("could not infer workspace path; pass --workspace-path")
	}
	if strings.TrimSpace(cfg.workspaceName) == "" {
		cfg.workspaceName = filepath.Base(strings.TrimRight(cfg.workspacePath, string(os.PathSeparator)))
		if cfg.workspaceName == "." || cfg.workspaceName == string(os.PathSeparator) || cfg.workspaceName == "" {
			cfg.workspaceName = "Load Test Workspace"
		}
	}
	if strings.TrimSpace(cfg.titlePrefix) == "" {
		cfg.titlePrefix = "Load Test AI Session"
	}

	fmt.Printf("seed context: db=%s account_scope_id=%s user_id=%s workspace=%s\n", cfg.dbPath, cfg.accountScopeID, cfg.userID, cfg.workspacePath)
	stamp := time.Now().UTC().Format("20060102-150405")
	baseNow := time.Now().Add(-time.Duration(totalCount(counts)+300) * time.Second).UnixMilli()
	for index, count := range counts {
		// Desktop lists newer sessions first, so keep the user-provided count order
		// as the expected load order by making the first count the newest session.
		startMs := baseNow + int64(len(counts)-index-1)*86_400_000
		summary, err := seedSession(sessions, cfg, index+1, count, stamp, startMs)
		if err != nil {
			return err
		}
		fmt.Println(summary)
	}
	return nil
}

func hydrateContext(sessions *pebblestore.SessionStore, cfg *seedConfig) error {
	existing, err := sessions.ListSessions(2000)
	if err != nil {
		return err
	}
	for _, session := range existing {
		if cfg.accountScopeID == "" && strings.TrimSpace(session.AccountScopeID) != "" {
			cfg.accountScopeID = strings.TrimSpace(session.AccountScopeID)
		}
		if cfg.userID == "" && strings.TrimSpace(session.UserID) != "" {
			cfg.userID = strings.TrimSpace(session.UserID)
		}
		if cfg.workspacePath == "" && strings.TrimSpace(session.WorkspacePath) != "" {
			cfg.workspacePath = strings.TrimSpace(session.WorkspacePath)
		}
		if cfg.workspaceName == "" && strings.TrimSpace(session.WorkspaceName) != "" {
			cfg.workspaceName = strings.TrimSpace(session.WorkspaceName)
		}
		if cfg.accountScopeID != "" && cfg.userID != "" && cfg.workspacePath != "" && cfg.workspaceName != "" {
			break
		}
	}
	if cfg.workspacePath == "" {
		cfg.workspacePath = strings.TrimSpace(cfg.fallbackWorkspace)
	}
	return nil
}

func seedSession(sessions *pebblestore.SessionStore, cfg seedConfig, ordinal, messageCount int, stamp string, startMs int64) (string, error) {
	sessionID := fmt.Sprintf("loadtest-v3-ai-%04d-%s-%02d", messageCount, stamp, ordinal)
	title := fmt.Sprintf("%s - %d messages - %s", cfg.titlePrefix, messageCount, stamp)
	runID := fmt.Sprintf("%s-run-0001", sessionID)

	session := pebblestore.SessionSnapshot{
		ID:             sessionID,
		UserID:         cfg.userID,
		AccountScopeID: cfg.accountScopeID,
		WorkspacePath:  cfg.workspacePath,
		WorkspaceName:  cfg.workspaceName,
		Title:          title,
		Mode:           "readwrite",
		Metadata: map[string]any{
			"load_test":      true,
			"seeded_by":      "ssh-seed-v3-load-test-sessions",
			"scenario":       "large-ai-session-history",
			"message_target": messageCount,
			"synthetic":      true,
		},
		CreatedAt: startMs,
		UpdatedAt: startMs,
	}
	if _, err := sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          cfg.userID,
		AccountScopeID:  cfg.accountScopeID,
		ClientRequestID: sessionID + "-create",
		IdempotencyKey:  sessionID + "-create",
		PayloadHash:     payloadHash("session.create", sessionID, title),
		Kind:            pebblestore.V3SessionMutationCreateSession,
		Session:         &session,
		NowUnixMs:       startMs,
	}); err != nil {
		return "", fmt.Errorf("create session %s: %w", sessionID, err)
	}

	if err := recordRunIntent(sessions, cfg, sessionID, runID, pebblestore.V3RunIntentPendingExecutor, startMs+100); err != nil {
		return "", err
	}
	if err := recordRunIntent(sessions, cfg, sessionID, runID, pebblestore.V3RunIntentRunning, startMs+200); err != nil {
		return "", err
	}

	for i := 1; i <= messageCount; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		content := syntheticContent(messageCount, i, role)
		message := pebblestore.MessageSnapshot{
			ID:             fmt.Sprintf("%s-msg-%05d", sessionID, i),
			SessionID:      sessionID,
			UserID:         cfg.userID,
			AccountScopeID: cfg.accountScopeID,
			Role:           role,
			Content:        content,
			Metadata: map[string]any{
				"load_test": true,
				"index":     i,
				"turn":      (i + 1) / 2,
			},
			CreatedAt: startMs + int64(i)*1000,
		}
		requestID := fmt.Sprintf("%s-message-%05d", sessionID, i)
		if _, err := sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:       sessionID,
			UserID:          cfg.userID,
			AccountScopeID:  cfg.accountScopeID,
			ClientRequestID: requestID,
			IdempotencyKey:  requestID,
			PayloadHash:     payloadHash("message.append", sessionID, strconv.Itoa(i), role, content),
			Kind:            pebblestore.V3SessionMutationAppendMessage,
			Message:         &message,
			NowUnixMs:       message.CreatedAt,
		}); err != nil {
			return "", fmt.Errorf("append message %d/%d to %s: %w", i, messageCount, sessionID, err)
		}
	}

	endMs := startMs + int64(messageCount+2)*1000
	if err := recordRunIntent(sessions, cfg, sessionID, runID, pebblestore.V3RunIntentCompleted, endMs); err != nil {
		return "", err
	}
	lifecycle := pebblestore.SessionLifecycleSnapshot{
		SessionID:      sessionID,
		UserID:         cfg.userID,
		AccountScopeID: cfg.accountScopeID,
		RunID:          runID,
		Active:         false,
		Phase:          "completed",
		StartedAt:      startMs,
		EndedAt:        endMs,
		UpdatedAt:      endMs,
		StopReason:     "completed",
		OwnerTransport: "load-test-seed",
	}
	if _, err := sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          cfg.userID,
		AccountScopeID:  cfg.accountScopeID,
		ClientRequestID: sessionID + "-lifecycle-completed",
		IdempotencyKey:  sessionID + "-lifecycle-completed",
		PayloadHash:     payloadHash("lifecycle.completed", sessionID, runID),
		Kind:            pebblestore.V3SessionMutationUpsertLifecycle,
		Lifecycle:       &lifecycle,
		NowUnixMs:       endMs + 1,
	}); err != nil {
		return "", fmt.Errorf("record lifecycle for %s: %w", sessionID, err)
	}

	return fmt.Sprintf("seeded session_id=%s title=%q messages=%d", sessionID, title, messageCount), nil
}

func recordRunIntent(sessions *pebblestore.SessionStore, cfg seedConfig, sessionID, runID, status string, ts int64) error {
	intent := pebblestore.V3SessionRunIntent{
		SessionID:      sessionID,
		UserID:         cfg.userID,
		AccountScopeID: cfg.accountScopeID,
		RunID:          runID,
		Status:         status,
		CreatedAt:      ts,
		UpdatedAt:      ts,
	}
	requestID := fmt.Sprintf("%s-run-%s", sessionID, strings.ReplaceAll(status, "_", "-"))
	if _, err := sessions.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          cfg.userID,
		AccountScopeID:  cfg.accountScopeID,
		ClientRequestID: requestID,
		IdempotencyKey:  requestID,
		PayloadHash:     payloadHash("run_intent", sessionID, runID, status),
		Kind:            pebblestore.V3SessionMutationRecordRunIntent,
		RunIntent:       &intent,
		NowUnixMs:       ts,
	}); err != nil {
		return fmt.Errorf("record run intent %s for %s: %w", status, sessionID, err)
	}
	return nil
}

func syntheticContent(total, index int, role string) string {
	turn := (index + 1) / 2
	if role == "user" {
		return fmt.Sprintf("Load-test user prompt %04d in a synthetic %d-message AI session. Please analyze the current project state, reference earlier decisions, and continue with the next implementation step. This message intentionally exercises scrollback, cache hydration, and session switching.", turn, total)
	}
	if index%50 == 0 {
		return fmt.Sprintf("Assistant response %04d for the %d-message load-test session.\n\nSummary:\n- Preserved durable V3 session history ordering.\n- Referenced earlier context without assuming stream completion from message events.\n- Included a moderate code block so rendering and scrollback measure realistic content.\n\n```ts\nconst checkpoint = { turn: %d, durable: true, source: 'pebble-v3' }\nconsole.log(checkpoint)\n```\n\nNext: continue validating long-history loading and route switching behavior.", turn, total, turn)
	}
	return fmt.Sprintf("Assistant response %04d in a synthetic %d-message AI session. The durable Pebble V3 message record is the source of truth for this history item. It contains enough prose to look like a normal AI answer while remaining deterministic for performance comparisons.", turn, total)
}

func parseCounts(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	counts := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		count, err := strconv.Atoi(part)
		if err != nil || count <= 0 {
			return nil, fmt.Errorf("invalid message count %q", part)
		}
		counts = append(counts, count)
	}
	if len(counts) == 0 {
		return nil, errors.New("at least one message count is required")
	}
	return counts, nil
}

func totalCount(counts []int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func payloadHash(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
GO_SEEDER

cd "${remote_dir}/swarmd"
printf 'ssh-seed-v3-load-test-sessions: seeding Pebble V3 sessions\n' >&2
"${go_bin}" run "${tmp_dir}/main.go" \
  --db-path "${db_path}" \
  --counts "${counts}" \
  --workspace-path "${workspace_path}" \
  --workspace-name "${workspace_name}" \
  --fallback-workspace-path "${remote_dir}" \
  --account-scope-id "${account_scope_id}" \
  --user-id "${user_id}" \
  --title-prefix "${title_prefix}" \
  --allow-empty-account="${allow_empty_account}"

if [ "${restart_service}" = 'true' ]; then
  if [ "${service_scope}" = 'user' ]; then
    printf 'ssh-seed-v3-load-test-sessions: restarting user service %s\n' "${service_unit}" >&2
    systemctl --user daemon-reload
    systemctl --user restart "${service_unit}"
    sleep 2
    systemctl --user --no-pager --full status "${service_unit}" | sed -n '1,18p'
  elif [ "${service_scope}" = 'system' ]; then
    printf 'ssh-seed-v3-load-test-sessions: restarting system service %s\n' "${service_unit}" >&2
    sudo -n systemctl daemon-reload
    sudo -n systemctl restart "${service_unit}"
    sleep 2
    sudo -n systemctl --no-pager --full status "${service_unit}" | sed -n '1,18p'
  else
    printf 'ssh-seed-v3-load-test-sessions: cannot restart; service scope was not detected\n' >&2
  fi
else
  if [ "${service_was_active}" = 'true' ]; then
    printf 'ssh-seed-v3-load-test-sessions: seeded successfully; %s remains stopped. Start it when ready, or rerun with --restart.\n' "${service_unit}" >&2
  else
    printf 'ssh-seed-v3-load-test-sessions: seeded successfully; %s was not active before seeding.\n' "${service_unit}" >&2
  fi
fi
REMOTE_SEED_V3_LOAD_TEST
