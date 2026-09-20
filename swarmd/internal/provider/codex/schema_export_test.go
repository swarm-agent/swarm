package codex

// SanitizeToolParametersForTest exposes the production mapper only to external
// package tests, which may import the registered tool runtime without a cycle.
var SanitizeToolParametersForTest = sanitizeCodexToolParameters
