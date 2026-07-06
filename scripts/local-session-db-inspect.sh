#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/local-session-db-inspect.sh [options]

Reusable local Swarm session DB inspector. It searches or dumps sessions from
the local Pebble DB. It does not call the Swarm API. Use --copy-db to inspect
a temporary DB copy without stopping a running local Swarm service.

Examples:
  scripts/local-session-db-inspect.sh --latest 5
  scripts/local-session-db-inspect.sh --session <session-id> --dump
  scripts/local-session-db-inspect.sh --session <session-id> --copy-db --dump
  scripts/local-session-db-inspect.sh --session-url https://<provider-host>/<workspace>/<session-id>
  scripts/local-session-db-inspect.sh --query "session.diagnostic.provider" --events 40
  scripts/local-session-db-inspect.sh --session <session-id> --dump --json --out tmp/session-dump.json

Options:
  --db-path <path>      Pebble DB path. Default: /var/lib/swarmd/swarmd.pebble
  --service <unit>      Service unit for --stop-service. Default: swarm.service
  --stop-service        Stop active local service before opening DB, then restore it.
  --copy-db             Copy DB to a temporary directory first, then inspect copy.
  --copy-attempts <n>   Attempts for --copy-db if active DB copy is inconsistent. Default: 3
  --keep-copy           Keep the temporary copied DB and print its path.

Selection/search:
  --latest <n>          Inspect latest n sessions. Default: 5
  --session <id|url>    Inspect one exact session id. A URL uses its last path segment.
  --session-url <url>    Extract session id from URL, stop service, dump JSON to /tmp without copying DB.
  --query <text>        Search session id/title/workspace/metadata/messages/events.
  --all                 Inspect all sessions scanned by --scan-limit.
  --scan-limit <n>      Max sessions to scan from newest first. Default: 1000

Output shaping:
  --messages <n>        Tail messages per selected session. Default: 12
  --events <n>          Tail V3 events per selected session. Default: 24
  --dump                Dump all messages/events for selected sessions.
  --outbox              Include realtime outbox records. Off by default because it scans global outbox.
  --json                Emit compact JSON instead of human-readable text.
  --pretty-json         Pretty-print JSON output. Slower/larger; compact is default.
  --out <path>          Write full output to a local path instead of stdout.
                        When --session-url is used, defaults to a /tmp dump file.
  -h, --help            Show this help.
USAGE
}

fail() {
  printf 'local-session-db-inspect: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

session_input_is_url_or_path() {
  [[ "$1" == *"://"* || "$1" == */* ]]
}

extract_session_id() {
  local value="$1"
  value="${value%%#*}"
  value="${value%%\?*}"
  value="${value%/}"
  value="${value##*/}"
  printf '%s' "${value}"
}

safe_session_filename() {
  local value="$1"
  value="$(printf '%s' "${value}" | tr -c 'A-Za-z0-9_.-' '_')"
  value="${value:0:80}"
  [[ -n "${value}" ]] || value="session"
  printf '%s' "${value}"
}

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
SWARMD_DIR="${ROOT_DIR}/swarmd"
GO_LIB="${ROOT_DIR}/scripts/lib-go.sh"
ROOT_MODULE="$(awk '$1 == "module" { print $2; exit }' "${ROOT_DIR}/go.mod")"

DB_PATH="/var/lib/swarmd/swarmd.pebble"
SERVICE_UNIT="swarm.service"
STOP_SERVICE="false"
COPY_DB="false"
COPY_ATTEMPTS="3"
KEEP_COPY="false"
LATEST="5"
SESSION_ID=""
SESSION_URL_MODE="false"
QUERY=""
ALL="false"
SCAN_LIMIT="1000"
MESSAGE_LIMIT="12"
EVENT_LIMIT="24"
DUMP="false"
INCLUDE_OUTBOX="false"
JSON_OUTPUT="false"
PRETTY_JSON="false"
OUT_PATH=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --db-path)
      [[ $# -ge 2 ]] || fail "--db-path requires a value"
      DB_PATH="$2"
      shift 2
      ;;
    --service)
      [[ $# -ge 2 ]] || fail "--service requires a value"
      SERVICE_UNIT="$2"
      shift 2
      ;;
    --stop-service)
      STOP_SERVICE="true"
      shift
      ;;
    --copy-db)
      COPY_DB="true"
      shift
      ;;
    --copy-attempts)
      [[ $# -ge 2 ]] || fail "--copy-attempts requires a value"
      COPY_ATTEMPTS="$2"
      shift 2
      ;;
    --keep-copy)
      KEEP_COPY="true"
      shift
      ;;
    --latest)
      [[ $# -ge 2 ]] || fail "--latest requires a value"
      LATEST="$2"
      shift 2
      ;;
    --session)
      [[ $# -ge 2 ]] || fail "--session requires a value"
      if session_input_is_url_or_path "$2"; then
        SESSION_URL_MODE="true"
        SESSION_ID="$(extract_session_id "$2")"
      else
        SESSION_ID="$2"
      fi
      shift 2
      ;;
    --session-url)
      [[ $# -ge 2 ]] || fail "--session-url requires a value"
      SESSION_URL_MODE="true"
      SESSION_ID="$(extract_session_id "$2")"
      shift 2
      ;;
    --query)
      [[ $# -ge 2 ]] || fail "--query requires a value"
      QUERY="$2"
      shift 2
      ;;
    --all)
      ALL="true"
      shift
      ;;
    --scan-limit)
      [[ $# -ge 2 ]] || fail "--scan-limit requires a value"
      SCAN_LIMIT="$2"
      shift 2
      ;;
    --messages)
      [[ $# -ge 2 ]] || fail "--messages requires a value"
      MESSAGE_LIMIT="$2"
      shift 2
      ;;
    --events)
      [[ $# -ge 2 ]] || fail "--events requires a value"
      EVENT_LIMIT="$2"
      shift 2
      ;;
    --dump)
      DUMP="true"
      shift
      ;;
    --outbox)
      INCLUDE_OUTBOX="true"
      shift
      ;;
    --json)
      JSON_OUTPUT="true"
      shift
      ;;
    --pretty-json)
      PRETTY_JSON="true"
      JSON_OUTPUT="true"
      shift
      ;;
    --out)
      [[ $# -ge 2 ]] || fail "--out requires a value"
      OUT_PATH="$2"
      shift 2
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

for pair in "latest:${LATEST}" "scan-limit:${SCAN_LIMIT}" "messages:${MESSAGE_LIMIT}" "events:${EVENT_LIMIT}" "copy-attempts:${COPY_ATTEMPTS}"; do
  name="${pair%%:*}"
  value="${pair#*:}"
  [[ "${value}" =~ ^[0-9]+$ ]] || fail "--${name} must be an integer"
done
if [[ "${SESSION_URL_MODE}" == "true" ]]; then
  [[ -n "${SESSION_ID}" ]] || fail "could not extract session id from URL"
  if [[ "${COPY_DB}" != "true" ]]; then
    STOP_SERVICE="true"
  fi
  DUMP="true"
  JSON_OUTPUT="true"
  if [[ -z "${OUT_PATH}" ]]; then
    OUT_PATH="${TMPDIR:-/tmp}/swarm-sessiondump-$(safe_session_filename "${SESSION_ID}")-$(date -u +%Y%m%dT%H%M%SZ)-$$.json"
  fi
fi

[[ -n "${DB_PATH}" ]] || fail "empty database path"
[[ -d "${SWARMD_DIR}" ]] || fail "missing swarmd directory at ${SWARMD_DIR}"
[[ -f "${GO_LIB}" ]] || fail "missing go resolver script at ${GO_LIB}"
[[ -n "${ROOT_MODULE}" ]] || fail "could not resolve root Go module path"
[[ -d "${DB_PATH}" ]] || fail "db path does not exist or is not a directory: ${DB_PATH}"
if [[ "${STOP_SERVICE}" == "true" && "${COPY_DB}" == "true" ]]; then
  fail "--stop-service and --copy-db are mutually exclusive"
fi
if [[ -n "${OUT_PATH}" && "${OUT_PATH}" != /* ]]; then
  OUT_PATH="${ROOT_DIR}/${OUT_PATH}"
fi

# shellcheck disable=SC1091
source "${GO_LIB}"
swarm_require_go "${ROOT_DIR}"

service_was_active="false"
restore_service() {
  if [[ "${STOP_SERVICE}" == "true" && "${service_was_active}" == "true" ]]; then
    printf 'local-session-db-inspect: restoring system service %s\n' "${SERVICE_UNIT}" >&2
    sudo systemctl start "${SERVICE_UNIT}" >/dev/null
  fi
}
trap restore_service EXIT

if [[ "${STOP_SERVICE}" == "true" ]]; then
  require_command systemctl
  if systemctl is-active --quiet "${SERVICE_UNIT}"; then
    service_was_active="true"
    printf 'local-session-db-inspect: stopping system service %s\n' "${SERVICE_UNIT}" >&2
    sudo systemctl stop "${SERVICE_UNIT}" >/dev/null
  else
    printf 'local-session-db-inspect: service %s not active; inspecting without stop\n' "${SERVICE_UNIT}" >&2
  fi
fi

TMP_BASE="$(cd -- "${TMPDIR:-/tmp}" && pwd)"
tmpdir="$(mktemp -d "${TMP_BASE}/swarm-sessiondbinspect.XXXXXX")"
INSPECT_DB_PATH="${DB_PATH}"
COPIED_DB_PATH=""
safe_tmpdir_path() {
  [[ -n "${tmpdir:-}" && "${tmpdir}" == "${TMP_BASE}/swarm-sessiondbinspect."* && -d "${tmpdir}" ]]
}
safe_copied_db_path() {
  [[ -n "${COPIED_DB_PATH:-}" && "${COPIED_DB_PATH}" == "${tmpdir}/"* && "$(basename -- "${COPIED_DB_PATH}")" == "swarmd.pebble.copy" && "${COPIED_DB_PATH}" != "${DB_PATH}" ]]
}
cleanup_tmpdir() {
  if [[ "${KEEP_COPY}" == "true" && -n "${COPIED_DB_PATH}" ]]; then
    printf 'local-session-db-inspect: kept copied db %s\n' "${COPIED_DB_PATH}" >&2
    return
  fi
  if [[ -n "${COPIED_DB_PATH}" && -e "${COPIED_DB_PATH}" ]]; then
    if safe_copied_db_path; then
      rm -rf "${COPIED_DB_PATH}"
      printf 'local-session-db-inspect: deleted copied db %s\n' "${COPIED_DB_PATH}" >&2
    else
      printf 'local-session-db-inspect: refusing to delete unsafe copied db path %s\n' "${COPIED_DB_PATH}" >&2
      return
    fi
  fi
  if safe_tmpdir_path; then
    rm -rf "${tmpdir}"
  else
    printf 'local-session-db-inspect: refusing to delete unsafe tmpdir path %s\n' "${tmpdir:-}" >&2
  fi
}
trap 'cleanup_tmpdir; restore_service' EXIT

if [[ "${COPY_DB}" == "true" ]]; then
  require_command cp
  COPIED_DB_PATH="${tmpdir}/swarmd.pebble.copy"
  printf 'local-session-db-inspect: copying active db %s -> %s\n' "${DB_PATH}" "${COPIED_DB_PATH}" >&2
  copy_ok="false"
  for attempt in $(seq 1 "${COPY_ATTEMPTS}"); do
    rm -rf "${COPIED_DB_PATH}"
    mkdir -p "${COPIED_DB_PATH}"
    if cp -a "${DB_PATH}/." "${COPIED_DB_PATH}/"; then
      copy_ok="true"
      break
    fi
    printf 'local-session-db-inspect: copy attempt %s/%s failed; retrying\n' "${attempt}" "${COPY_ATTEMPTS}" >&2
    sleep 0.2
  done
  [[ "${copy_ok}" == "true" ]] || fail "failed to copy db after ${COPY_ATTEMPTS} attempts"
  INSPECT_DB_PATH="${COPIED_DB_PATH}"
fi

cat >"${tmpdir}/go.mod" <<EOF_GO_MOD
module swarm/packages/swarmd/cmd/sessiondbinspectlocal

go 1.25.11

require swarm/packages/swarmd v0.0.0

replace swarm/packages/swarmd => ${SWARMD_DIR}
replace ${ROOT_MODULE} => ${ROOT_DIR}
EOF_GO_MOD

cat >"${tmpdir}/main.go" <<'EOF_GO'
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type report struct {
	GeneratedAtUnixMs int64         `json:"generated_at_unix_ms"`
	DBPath            string        `json:"db_path"`
	SourceDBPath      string        `json:"source_db_path,omitempty"`
	SessionCount      int           `json:"session_count"`
	ScannedCount      int           `json:"scanned_count"`
	MatchedCount      int           `json:"matched_count"`
	Selection         selection     `json:"selection"`
	Sessions          []sessionDump `json:"sessions"`
}

type selection struct {
	Latest       int    `json:"latest"`
	SessionID    string `json:"session_id,omitempty"`
	Query        string `json:"query,omitempty"`
	All          bool   `json:"all"`
	ScanLimit    int    `json:"scan_limit"`
	MessageLimit int    `json:"message_limit"`
	EventLimit   int    `json:"event_limit"`
	Dump         bool   `json:"dump"`
}

type sessionDump struct {
	Session        pebblestore.SessionSnapshot          `json:"session"`
	LegacyMessages []pebblestore.MessageSnapshot       `json:"legacy_messages,omitempty"`
	Permissions    []pebblestore.PermissionRecord      `json:"permissions,omitempty"`
	PendingPermissions []pebblestore.PermissionRecord  `json:"pending_permissions,omitempty"`
	V3Messages     []pebblestore.MessageSnapshot       `json:"v3_messages,omitempty"`
	V3Projection   *pebblestore.V3SessionProjection    `json:"v3_projection,omitempty"`
	V3RunIntents   []pebblestore.V3SessionRunIntent    `json:"v3_run_intents,omitempty"`
	V3Events       []pebblestore.V3SessionEvent        `json:"v3_events,omitempty"`
	V3Outbox       []pebblestore.V3RealtimeOutboxRecord `json:"v3_outbox,omitempty"`
	MatchedReason  string                              `json:"matched_reason,omitempty"`
}

func main() {
	dbPath := flag.String("db", "", "path to swarmd Pebble DB")
	sourceDBPath := flag.String("source-db", "", "original source DB path when inspecting a copy")
	latest := flag.Int("latest", 5, "latest sessions to inspect when no search is provided")
	sessionID := flag.String("session", "", "exact session id")
	query := flag.String("query", "", "case-insensitive search text")
	all := flag.Bool("all", false, "include all scanned sessions")
	scanLimit := flag.Int("scan-limit", 1000, "max newest sessions to scan")
	messageLimit := flag.Int("messages", 12, "tail messages per session")
	eventLimit := flag.Int("events", 24, "tail events per session")
	dump := flag.Bool("dump", false, "dump all messages and events for matched sessions")
	includeOutbox := flag.Bool("outbox", false, "include realtime outbox records")
	jsonOutput := flag.Bool("json", false, "emit json")
	prettyJSON := flag.Bool("pretty-json", false, "pretty-print json output")
	flag.Parse()

	if strings.TrimSpace(*dbPath) == "" {
		fmt.Fprintln(os.Stderr, "--db is required")
		os.Exit(2)
	}

	store, err := pebblestore.OpenReadOnly(strings.TrimSpace(*dbPath))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db read-only: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	sessions := pebblestore.NewSessionStore(store)
	permissions := pebblestore.NewPermissionStore(store)
	selected := make([]sessionDump, 0)
	needle := strings.ToLower(strings.TrimSpace(*query))
	exactSessionID := strings.TrimSpace(*sessionID)
	latestLimit := positive(*latest, 5)
	allSessions := []pebblestore.SessionSnapshot{}
	scanned := []pebblestore.SessionSnapshot{}

	if exactSessionID != "" {
		session, ok, err := sessions.GetSession(exactSessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "get session %s: %v\n", exactSessionID, err)
			os.Exit(1)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "session not found: %s\n", exactSessionID)
			os.Exit(1)
		}
		allSessions = append(allSessions, session)
		scanned = append(scanned, session)
		selected = append(selected, buildSessionDump(sessions, permissions, session, "session id", nil, nil, nil, false, *messageLimit, *eventLimit, *dump, *includeOutbox))
	} else {
		var err error
		allSessions, err = sessions.ListSessions(10000)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list sessions: %v\n", err)
			os.Exit(1)
		}
		sort.Slice(allSessions, func(i, j int) bool { return allSessions[i].UpdatedAt > allSessions[j].UpdatedAt })
		scanned = bounded(allSessions, positive(*scanLimit, 1000))

		for index, session := range scanned {
			var legacyMessages []pebblestore.MessageSnapshot
			var v3Messages []pebblestore.MessageSnapshot
			var v3Events []pebblestore.V3SessionEvent
			historyLoaded := false
			include, reason := shouldInclude(session, legacyMessages, v3Messages, v3Events, exactSessionID, needle, *all, index, latestLimit)
			if !include && needle != "" {
				legacyMessages, _ = sessions.ListMessages(session.ID, 0, 100000)
				v3Messages, _ = sessions.ListV3SessionMessages(session.ID, 0, 100000)
				v3Events, _ = sessions.ListV3SessionEvents(session.ID, 0, 100000)
				historyLoaded = true
				include, reason = shouldInclude(session, legacyMessages, v3Messages, v3Events, exactSessionID, needle, *all, index, latestLimit)
			}
			if !include {
				continue
			}
			selected = append(selected, buildSessionDump(sessions, permissions, session, reason, legacyMessages, v3Messages, v3Events, historyLoaded, *messageLimit, *eventLimit, *dump, *includeOutbox))
		}
	}

	if exactSessionID != "" && len(selected) == 0 {
		fmt.Fprintf(os.Stderr, "session not found: %s\n", exactSessionID)
		os.Exit(1)
	}

	result := report{
		GeneratedAtUnixMs: time.Now().UnixMilli(),
		DBPath:            strings.TrimSpace(*dbPath),
		SourceDBPath:      strings.TrimSpace(*sourceDBPath),
		SessionCount:      len(allSessions),
		ScannedCount:      len(scanned),
		MatchedCount:      len(selected),
		Selection: selection{
			Latest:       latestLimit,
			SessionID:    exactSessionID,
			Query:        strings.TrimSpace(*query),
			All:          *all,
			ScanLimit:    positive(*scanLimit, 1000),
			MessageLimit: positive(*messageLimit, 12),
			EventLimit:   positive(*eventLimit, 24),
			Dump:         *dump,
		},
		Sessions: selected,
	}

	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		if *prettyJSON {
			encoder.SetIndent("", "  ")
		}
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "encode json: %v\n", err)
			os.Exit(1)
		}
		return
	}
	printText(result)
}

func buildSessionDump(sessions *pebblestore.SessionStore, permissions *pebblestore.PermissionStore, session pebblestore.SessionSnapshot, reason string, legacyMessages, v3Messages []pebblestore.MessageSnapshot, v3Events []pebblestore.V3SessionEvent, historyLoaded bool, messageLimit, eventLimit int, dump bool, includeOutbox bool) sessionDump {
	if !historyLoaded {
		legacyMessages, _ = sessions.ListMessages(session.ID, 0, 100000)
		v3Messages, _ = sessions.ListV3SessionMessages(session.ID, 0, 100000)
		v3Events, _ = sessions.ListV3SessionEvents(session.ID, 0, 100000)
	}
	v3Intents, _ := sessions.ListV3SessionRunIntents(session.ID, 0, 1000)
	v3Outbox := []pebblestore.V3RealtimeOutboxRecord{}
	if includeOutbox {
		v3Outbox, _ = sessions.ListV3RealtimeOutboxForSessionAfterSeq(session.ID, 0, 100000)
	}
	permissionRecords, _ := permissions.ListPermissions(session.ID, 1000)
	pendingPermissions, _ := permissions.ListPendingPermissions(session.ID, 1000)
	projection, hasProjection, _ := sessions.GetV3SessionProjection(session.ID)
	candidate := sessionDump{
		Session:            session,
		LegacyMessages:    trimMessages(legacyMessages, messageLimit, dump),
		Permissions:       permissionRecords,
		PendingPermissions: pendingPermissions,
		V3Messages:        trimMessages(v3Messages, messageLimit, dump),
		V3RunIntents:      v3Intents,
		V3Events:          trimEvents(v3Events, eventLimit, dump),
		V3Outbox:          trimOutbox(v3Outbox, eventLimit, dump),
		MatchedReason:     reason,
	}
	if hasProjection {
		p := projection
		candidate.V3Projection = &p
	}
	return candidate
}

func shouldInclude(session pebblestore.SessionSnapshot, legacyMessages, v3Messages []pebblestore.MessageSnapshot, v3Events []pebblestore.V3SessionEvent, sessionID, needle string, all bool, index, latest int) (bool, string) {
	if sessionID != "" {
		if session.ID == sessionID {
			return true, "session id"
		}
		return false, ""
	}
	if needle != "" {
		if sessionMatches(session, needle) {
			return true, "session fields"
		}
		for _, message := range append(append([]pebblestore.MessageSnapshot{}, legacyMessages...), v3Messages...) {
			if strings.Contains(strings.ToLower(message.ID), needle) || strings.Contains(strings.ToLower(message.Role), needle) || strings.Contains(strings.ToLower(message.Content), needle) {
				return true, "message"
			}
		}
		for _, event := range v3Events {
			if strings.Contains(strings.ToLower(event.EventType), needle) || strings.Contains(strings.ToLower(string(event.Payload)), needle) {
				return true, "v3 event"
			}
		}
		return false, ""
	}
	if all {
		return true, "all"
	}
	if index < latest {
		return true, "latest"
	}
	return false, ""
}

func sessionMatches(session pebblestore.SessionSnapshot, needle string) bool {
	metadata, _ := json.Marshal(session.Metadata)
	parts := []string{session.ID, session.Title, session.WorkspacePath, session.WorkspaceName, session.Mode, string(metadata)}
	for _, part := range parts {
		if strings.Contains(strings.ToLower(part), needle) {
			return true
		}
	}
	return false
}

func printText(r report) {
	fmt.Printf("generated_at=%s db=%s", unixMilli(r.GeneratedAtUnixMs), r.DBPath)
	if strings.TrimSpace(r.SourceDBPath) != "" {
		fmt.Printf(" source_db=%s", r.SourceDBPath)
	}
	fmt.Printf(" sessions=%d scanned=%d matched=%d\n", r.SessionCount, r.ScannedCount, r.MatchedCount)
	fmt.Printf("selection latest=%d session_id=%q query=%q all=%t dump=%t\n", r.Selection.Latest, r.Selection.SessionID, r.Selection.Query, r.Selection.All, r.Selection.Dump)
	for index, item := range r.Sessions {
		s := item.Session
		fmt.Printf("\n=== SESSION %d matched_by=%s ===\n", index+1, item.MatchedReason)
		fmt.Printf("id=%s title=%q workspace=%q mode=%s updated_at=%s message_count=%d\n", s.ID, s.Title, s.WorkspacePath, s.Mode, unixMilli(s.UpdatedAt), s.MessageCount)
		if item.V3Projection != nil {
			p := item.V3Projection
			fmt.Printf("v3_projection last_event_seq=%d high_watermark=%d updated_at=%s\n", p.LastEventSeq, p.ProjectionHighWatermarkSeq, unixMilli(p.UpdatedAt))
		}
		if len(item.V3RunIntents) > 0 {
			fmt.Printf("v3_run_intents=%d\n", len(item.V3RunIntents))
			for _, intent := range item.V3RunIntents {
				fmt.Printf("- run_id=%s status=%s updated_at=%s blocked_reason=%s\n", intent.RunID, intent.Status, unixMilli(intent.UpdatedAt), oneLine(intent.BlockedReason, 180))
			}
		}
		printMessages("legacy_messages", item.LegacyMessages)
		printPermissions("permissions", item.Permissions)
		printPermissions("pending_permissions", item.PendingPermissions)
		printMessages("v3_messages", item.V3Messages)
		printEvents(item.V3Events)
		printOutbox(item.V3Outbox)
	}
}

func printPermissions(label string, permissions []pebblestore.PermissionRecord) {
	if len(permissions) == 0 {
		return
	}
	fmt.Printf("%s=%d\n", label, len(permissions))
	for _, permission := range permissions {
		fmt.Printf("- perm id=%s run=%s tool=%s status=%s exec=%s mode=%s requested_at=%s args=%s\n", permission.ID, permission.RunID, permission.ToolName, permission.Status, permission.ExecutionStatus, permission.Mode, unixMilli(permission.PermissionRequested), oneLine(permission.ToolArguments, 260))
	}
}

func printMessages(label string, messages []pebblestore.MessageSnapshot) {
	if len(messages) == 0 {
		return
	}
	fmt.Printf("%s=%d\n", label, len(messages))
	for _, message := range messages {
		fmt.Printf("- msg seq=%d id=%s role=%s at=%s content=%s\n", message.GlobalSeq, message.ID, message.Role, unixMilli(message.CreatedAt), oneLine(message.Content, 260))
	}
}

func printEvents(events []pebblestore.V3SessionEvent) {
	if len(events) == 0 {
		return
	}
	fmt.Printf("v3_events=%d\n", len(events))
	for _, event := range events {
		payload := map[string]any{}
		_ = json.Unmarshal(event.Payload, &payload)
		if isDiagnosticPayload(payload) {
			fmt.Printf("- DIAG seq=%d type=%s run_id=%s stage=%s source=%s sequence=%s payload=%s\n", event.Seq, event.EventType, stringField(payload, "run_id"), stringField(payload, "stage"), stringField(payload, "source"), stringField(payload, "sequence_label"), jsonOneLine(payload["payload"], 20000))
			continue
		}
		fmt.Printf("- event seq=%d type=%s run_id=%s delta=%q message=%s payload=%s\n", event.Seq, event.EventType, stringField(payload, "run_id"), oneLine(stringField(payload, "delta"), 100), messagePayloadSummary(payload), jsonOneLine(payload, 3000))
	}
}

func printOutbox(records []pebblestore.V3RealtimeOutboxRecord) {
	if len(records) == 0 {
		return
	}
	fmt.Printf("v3_outbox=%d\n", len(records))
	for _, record := range records {
		payload := map[string]any{}
		_ = json.Unmarshal(record.Event.Payload, &payload)
		fmt.Printf("- outbox endpoint_seq=%d cursor=%s event_seq=%d type=%s diagnostic=%t payload=%s\n", record.EndpointSeq, record.EndpointCursor, record.Event.Seq, record.Event.EventType, isDiagnosticPayload(payload), jsonOneLine(payload, 3000))
	}
}

func trimMessages(messages []pebblestore.MessageSnapshot, limit int, dump bool) []pebblestore.MessageSnapshot {
	if dump {
		return messages
	}
	limit = positive(limit, 12)
	if len(messages) <= limit {
		return messages
	}
	return messages[len(messages)-limit:]
}

func trimEvents(events []pebblestore.V3SessionEvent, limit int, dump bool) []pebblestore.V3SessionEvent {
	if dump {
		return events
	}
	limit = positive(limit, 24)
	if len(events) <= limit {
		return events
	}
	return events[len(events)-limit:]
}

func trimOutbox(records []pebblestore.V3RealtimeOutboxRecord, limit int, dump bool) []pebblestore.V3RealtimeOutboxRecord {
	if dump {
		return records
	}
	limit = positive(limit, 24)
	if len(records) <= limit {
		return records
	}
	return records[len(records)-limit:]
}

func bounded[T any](items []T, limit int) []T {
	limit = positive(limit, 1000)
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

func positive(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func stringField(record map[string]any, key string) string {
	if value, ok := record[key].(string); ok {
		return value
	}
	return ""
}

func messagePayloadSummary(payload map[string]any) string {
	message, ok := payload["message"]
	if !ok {
		return ""
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return ""
	}
	return oneLine(string(encoded), 220)
}

func isDiagnosticPayload(payload map[string]any) bool {
	value, ok := payload["diagnostic"].(bool)
	return ok && value
}

func jsonOneLine(value any, limit int) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return oneLine(string(encoded), limit)
}

func unixMilli(ms int64) string {
	if ms <= 0 {
		return "0"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func oneLine(value string, limit int) string {
	clean := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " "))
	if limit <= 0 || len(clean) <= limit {
		return clean
	}
	if limit <= 3 {
		return clean[:limit]
	}
	return clean[:limit-3] + "..."
}
EOF_GO

cmd=("${GO_BIN}" run -mod=mod "." --db "${INSPECT_DB_PATH}" --source-db "${DB_PATH}" --latest "${LATEST}" --scan-limit "${SCAN_LIMIT}" --messages "${MESSAGE_LIMIT}" --events "${EVENT_LIMIT}")
if [[ -n "${SESSION_ID}" ]]; then cmd+=(--session "${SESSION_ID}"); fi
if [[ -n "${QUERY}" ]]; then cmd+=(--query "${QUERY}"); fi
if [[ "${ALL}" == "true" ]]; then cmd+=(--all); fi
if [[ "${DUMP}" == "true" ]]; then cmd+=(--dump); fi
if [[ "${INCLUDE_OUTBOX}" == "true" ]]; then cmd+=(--outbox); fi
if [[ "${JSON_OUTPUT}" == "true" ]]; then cmd+=(--json); fi
if [[ "${PRETTY_JSON}" == "true" ]]; then cmd+=(--pretty-json); fi

(
  cd "${tmpdir}"
  export GOCACHE="${GOCACHE:-${ROOT_DIR}/.cache/go-build}"
  mkdir -p "${GOCACHE}"
  if [[ -n "${OUT_PATH}" ]]; then
    mkdir -p "$(dirname "${OUT_PATH}")"
    "${cmd[@]}" >"${OUT_PATH}"
    bytes="$(wc -c <"${OUT_PATH}" | tr -d ' ')"
    printf 'local-session-db-inspect: wrote output %s (%s bytes)\n' "${OUT_PATH}" "${bytes}" >&2
    printf 'session_id=%s output_path=%s output_bytes=%s\n' "${SESSION_ID}" "${OUT_PATH}" "${bytes}"
  else
    "${cmd[@]}"
  fi
)
