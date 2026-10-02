# Google media error remediation

New Google media errors are sanitized before task/tool callers log or persist
failure text. Authentication moves from query strings to headers; downloads,
upload URLs and redirects are restricted to the configured API origin.
Cross-service Google download redirects now fail closed instead of forwarding
credentials. Confirm provider compatibility before deployment.

Project failures are stored in `ProjectTaskRecord.LastError` and failed
`Deliverables[].Description` by `projects_media.go` through
`updateProjectTaskWithRetry`, including `generateProjectVideoStory` failures.
The task JSON serializer protects new writes and legacy board/export presentation
of those fields without mutating an in-memory historical record. It does not
rewrite existing durable events.

`runtime_manage_artifact.go` wraps media errors for tool results; direct video
and image swarm services serialize failure reasons/results. Adapter sanitization
precedes these consumers. Existing session messages/tool results, V3 events,
realtime outbox payloads, artifact error metadata and raw log files are NOT
retroactively protected by the project task serializer. Historical session
replay/export remains a gap requiring a separate canonical presentation policy.

## Operator procedure (not executed)

- Rotate/revoke the exposed provider key separately; do not paste it into a
  terminal command, migration argument, log or ticket.
- Restrict access to affected logs/exports/backups. Select explicit account,
  project/task IDs and incident time bounds from operator metadata, not a global
  credential-content search. No production logs or records were inspected here.
- For mutable project snapshots, an authorized maintenance entrypoint can use
  `SessionStore.UpdateProjectTask` with revision checking. Bound a batch to at
  most 100 explicit task IDs, re-read each task within its account, sanitize only
  LastError and failed descriptions with `SanitizeDiagnostic`, skip unchanged
  tasks, and re-read on revision conflicts. Report counts, never original text.
  This is idempotent and preserves status, successful outputs and ownership.
  The maintenance entrypoint and operator authorization remain future work.
- Do not directly rewrite Pebble keys or immutable session events/outbox history.
  Session replay/export protection needs separate reviewed changes across the
  canonical hydration, replay and export boundaries. Restrict exports meanwhile.
- Use supported retention/log rotation only with approval for exact files/time
  ranges. Never globally delete logs. Logical redaction does not erase old
  WAL/SST files, journals, snapshots, rotated logs or backups; physical cleanup
  requires separate operator authority and storage-specific procedures.

No historical cleanup, key rotation, service restart or paid generation was
performed. Tests and formatting require parent execution before integration.
