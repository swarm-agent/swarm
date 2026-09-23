package tool

// WorkerV2AuthoringInstructions is shared by the provider schema and harness.
const WorkerV2AuthoringInstructions = `Worker V2 uses the canonical executable plan document with worker_v2, never an ordinary plan merely titled hourly. From a primary conversation submit the complete document using manage_workers action=propose in either Plan or Auto. Worker proposals do not change the session mode, create an ordinary active plan, or start a run. No existing plan, worker, save, approval or grant is required. This stores only a pending Worker plan review; stop authoring and let the user explicitly Accept worker. Acceptance creates and activates the disclosed schedule; the first run follows that schedule, not immediate one-shot execution. Never approve or activate your own proposal. Preserve exact instructions and cadence. Intervals are elapsed seconds (60–31622400); cron has five numeric, * or */n fields and requires an explicit IANA timezone. No ranges, lists, names or simultaneous restricted day-of-month/day-of-week. Ask about missing wall-clock time/timezone or genuinely ambiguous timing; never approximate unsupported cadence. If expiration was unspecified use {"kind":"indefinite"}; only requested finite expiration uses {"kind":"at","expires_at":<future Unix milliseconds>}. Read manage_workers review for the current exact review before editing; resubmit the complete document with worker_review unchanged, preserving unrelated instructions/settings. Edits remain pending and do not alter active execution. Do not invent IDs or turn a recurring review into one-shot approval. Structured run closing states classify worker execution outcomes: routine_clean (routine check or maintenance completed with no anomalies, warnings, or action needed; renders as a calm minimal status), deliverable_ready (run produced new or updated deliverables/artifacts for user review), attention_alert (run detected actionable warnings, drift, threshold alerts, or issues requiring attention; renders an alert badge), or blocked (run cannot proceed due to missing external dependencies or permissions). When authoring a worker plan, propose checkpoints whose acceptance criteria clearly define which closing state should be chosen on completion and what criteria distinguish a calm routine run from an alert or deliverable.
Complete fresh proposal example: {"action":"propose","document":{"title":"Worker plan: hourly repository report","info":{"goal":"Report repository status without modifying files"},"worker_v2":{"schema_version":2,"schedule":{"kind":"interval","interval_seconds":3600},"missed":"skip","overlap":"serialize","activate_on_accept":true,"expiration":{"kind":"indefinite"}},"checkpoints":[{"id":"report","title":"Report repository status","status":"pending","order":1,"tasks":["Inspect repository status and report changes; do not modify files"],"acceptance_criteria":["A factual status report is returned with closing_state routine_clean when no anomalies are found or attention_alert when issues require attention"]}]}}. The same action works in Plan and Auto. Daily 18:00 UTC instead uses schedule={"kind":"cron","cron":"0 18 * * *","timezone":"UTC"}.`

// AutomationV2AuthoringInstructions provides backward compatibility.
const AutomationV2AuthoringInstructions = WorkerV2AuthoringInstructions

func sessionPlanAutomationV2ToolSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Worker V2 / Automation V2 schedule specification. Call action='help' for schema.",
	}
}

func automationV2ReviewSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Worker V2 / Automation V2 review context. Call action='help' for schema.",
	}
}

func manageWorkersV2Definition() Definition {
	return Definition{Type: "function", Name: "manage_workers", Description: "Inspect Worker V2 state or submit a dedicated pending worker review with action='propose' and document (in Plan or Auto). Proposal does not change session mode or create an active plan; only user acceptance activates it. Call action='help' for scheduling and review syntax.", Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"review", "context", "list", "progress", "propose", "help"}}, "cursor": map[string]any{"type": "string"}, "timezone": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50}, "document": map[string]any{"description": "Complete executable Worker V2 document, as an object or JSON-encoded string; required for propose."}, "worker_review": map[string]any{"description": "Exact current review for editing a pending proposal; omit on first proposal."}}}}
}

func manageAutomationV2Definition() Definition {
	d := manageWorkersV2Definition()
	d.Name = "manage_automation"
	return d
}
