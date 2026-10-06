// The legacy /new and /task browser/provider journeys have been retired.
// Fail closed for callers that still invoke this filename; an empty/skip-only
// browser suite must not qualify current Orchestrator or + New Task behavior.
throw new Error('Legacy Desktop launch journeys retired; Orchestrator browser/provider replacement required. See web/README.md.')

export {}
