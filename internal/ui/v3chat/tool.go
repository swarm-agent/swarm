package v3chat

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ToolTimelineItem mirrors the Desktop V3 live-tool projection: tool lifecycle
// events are keyed by call ID and placed on the same event/message sequence used
// by the rest of the transcript. A durable role=tool message replaces the live
// projection once it arrives.
type ToolTimelineItem struct {
	ID                    string
	CallID                string
	ToolInstanceID        string
	GlobalSeq             uint64
	Name                  string
	Arguments             string
	Output                string
	Error                 string
	Status                string
	DurationMS            int64
	CreatedAt             int64
	Step                  int
	StepID                string
	OutputIndex           int
	HasOutputIndex        bool
	ProviderEventIndex    int64
	HasProviderEventIndex bool
	ProviderConstruction  bool
	RuntimeExecution      bool
	TaskStream            *TaskStreamState
}

type TaskStreamState struct {
	PathID              string
	Status              string
	LaunchCount         int
	TaskMode            string
	SwarmStrategy       string
	IntegrationContract string
	IntegrationRequired bool
	LaunchesByKey       map[string]map[string]any
	LaunchOrder         []string
	ProgramID           string
	ProgramState        string
	ActiveStageID       string
	ProgramStages       []TaskProgramStageState
	ProgramJobsByID     map[string]map[string]any
	ProgramJobOrder     []string
	ProgramJobStates    map[string]map[string]any
}

type TaskProgramStageState struct {
	ID        string
	DependsOn []string
}

type toolHistoryPayload struct {
	PathID          string `json:"path_id"`
	Tool            string `json:"tool"`
	ToolName        string `json:"tool_name"`
	CallID          string `json:"call_id"`
	ToolInstanceID  string `json:"tool_instance_id"`
	Arguments       string `json:"arguments"`
	Output          string `json:"output"`
	CompletedOutput string `json:"completed_output"`
	Error           string `json:"error"`
	DurationMS      int64  `json:"duration_ms"`
}

func parseToolMessage(message Message) (ToolTimelineItem, bool) {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
		return ToolTimelineItem{}, false
	}
	var payload toolHistoryPayload
	if json.Unmarshal([]byte(message.Content), &payload) != nil {
		return ToolTimelineItem{}, false
	}
	pathID := strings.TrimSpace(payload.PathID)
	if pathID != "run.tool-history.v2" && pathID != "run.v3.provider-tool-result.v1" {
		return ToolTimelineItem{}, false
	}
	name := firstNonEmpty(payload.Tool, payload.ToolName)
	if name == "" {
		return ToolTimelineItem{}, false
	}
	return ToolTimelineItem{
		ID:             message.ID,
		CallID:         strings.TrimSpace(payload.CallID),
		ToolInstanceID: strings.TrimSpace(payload.ToolInstanceID),
		GlobalSeq:      message.GlobalSeq,
		Name:           name,
		Arguments:      strings.TrimSpace(payload.Arguments),
		Output:         firstNonEmptyRaw(payload.Output, payload.CompletedOutput),
		Error:          strings.TrimSpace(payload.Error),
		Status:         toolTerminalStatus(payload.Error),
		DurationMS:     payload.DurationMS,
		CreatedAt:      message.CreatedAt,
	}, true
}

func applyToolEvent(state State, event clientSessionV3Event, payload map[string]json.RawMessage) State {
	eventType := strings.ToLower(strings.TrimSpace(event.EventType))
	if !isToolTimelineEvent(eventType) {
		return state
	}
	construction := strings.HasPrefix(eventType, "session.provider_tool_call.")
	callID := rawString(payload, "call_id", "tool_call_id")
	toolInstanceID := rawString(payload, "tool_instance_id")
	step := int(rawInt64(payload, "step"))
	stepID := rawString(payload, "step_id")
	outputIndex, hasOutputIndex := rawOptionalInt(payload, "output_index")
	key := findToolTimelineKey(state.Tools, callID, toolInstanceID, step, stepID, outputIndex, hasOutputIndex, construction)
	if key == "" {
		return state
	}

	item := state.Tools[key]
	if item.ID == "" {
		item = ToolTimelineItem{ID: "live-tool:" + key, CreatedAt: event.Timestamp}
	}
	item.CallID = firstNonEmpty(callID, item.CallID)
	item.ToolInstanceID = firstNonEmpty(toolInstanceID, item.ToolInstanceID)
	item.Name = firstNonEmpty(rawString(payload, "tool_name"), item.Name, "tool")
	item.CreatedAt = firstPositiveInt64(item.CreatedAt, rawInt64(payload, "started_at"), rawInt64(payload, "recorded_at"), event.Timestamp)
	item.Step = firstPositiveInt(item.Step, step)
	item.StepID = firstNonEmpty(stepID, item.StepID)
	if hasOutputIndex {
		item.OutputIndex, item.HasOutputIndex = outputIndex, true
	}
	item.ProviderConstruction = item.ProviderConstruction || construction
	item.RuntimeExecution = item.RuntimeExecution || !construction
	if item.GlobalSeq == 0 && event.Seq != 0 {
		item.GlobalSeq = event.Seq
	}

	providerEventIndex, hasProviderEventIndex := rawOptionalInt64(payload, "event_index")
	freshConstruction := !construction || !hasProviderEventIndex || !item.HasProviderEventIndex || providerEventIndex > item.ProviderEventIndex
	arguments := rawString(payload, "arguments", "arguments_snapshot")
	argumentsDelta := rawText(payload, "arguments_delta")
	if freshConstruction {
		if arguments != "" {
			item.Arguments = arguments
		} else if argumentsDelta != "" {
			item.Arguments += argumentsDelta
		}
	}
	if construction && hasProviderEventIndex {
		if !item.HasProviderEventIndex || providerEventIndex > item.ProviderEventIndex {
			item.ProviderEventIndex = providerEventIndex
		}
		item.HasProviderEventIndex = true
	}

	if eventType == "session.tool.delta" {
		delta := firstNonEmptyRaw(
			rawText(payload, "output"),
			rawText(payload, "raw_output"),
			rawText(payload, "output_delta"),
			rawText(payload, "delta"),
		)
		if !applyTaskStreamPatch(&item, delta) {
			// Non-task progress remains byte-for-byte so Bash behaves like a live terminal.
			item.Output += delta
		}
	} else if !construction {
		output := firstNonEmptyRaw(rawText(payload, "completed_output"), rawText(payload, "raw_output"), rawText(payload, "output"))
		outputDelta := firstNonEmptyRaw(rawText(payload, "output_delta"), rawText(payload, "delta"))
		if output != "" {
			item.Output = output
		} else if outputDelta != "" {
			item.Output += outputDelta
		}
	}
	item.Error = firstNonEmpty(rawString(payload, "error"), item.Error)
	if duration := rawInt64(payload, "duration_ms"); duration != 0 {
		item.DurationMS = duration
	}
	item.Status = mergeToolEventStatus(item.Status, toolEventStatus(eventType, rawString(payload, "status"), item.Error), construction)

	preferredKey := key
	if callID != "" {
		preferredKey = callID
	}
	if preferredKey != key {
		delete(state.Tools, key)
	}
	if hasDurableToolMessage(state.Messages, item) {
		delete(state.Tools, preferredKey)
		return state
	}
	item.ID = "live-tool:" + preferredKey
	state.Tools[preferredKey] = item
	return boundLiveTools(state)
}

// Keep this small adapter local to v3chat so tool projection logic can be
// tested without importing backend store types.
type clientSessionV3Event struct {
	Seq       uint64
	EventType string
	Timestamp int64
}

func isToolTimelineEvent(eventType string) bool {
	switch eventType {
	case "session.tool.started", "session.tool.delta", "session.tool.completed", "session.tool.failed", "session.tool.cancelled", "session.tool.canceled",
		"session.provider_tool_call.started", "session.provider_tool_call.arguments.delta", "session.provider_tool_call.arguments.snapshot", "session.provider_tool_call.completed":
		return true
	default:
		return false
	}
}

func toolEventStatus(eventType, payloadStatus, errorText string) string {
	payloadStatus = strings.ToLower(strings.TrimSpace(payloadStatus))
	switch eventType {
	case "session.tool.completed":
		return "completed"
	case "session.tool.failed":
		return "failed"
	case "session.tool.cancelled", "session.tool.canceled":
		return "cancelled"
	case "session.tool.started", "session.tool.delta":
		return "running"
	case "session.provider_tool_call.completed":
		return "ready"
	case "session.provider_tool_call.started", "session.provider_tool_call.arguments.delta", "session.provider_tool_call.arguments.snapshot":
		return "constructing"
	default:
		if strings.TrimSpace(errorText) != "" {
			return "failed"
		}
		if payloadStatus != "" {
			return payloadStatus
		}
		return "running"
	}
}

func mergeToolEventStatus(existing, incoming string, construction bool) string {
	existing = canonicalToolStatus(existing)
	incoming = canonicalToolStatus(incoming)
	if existing == "" {
		return incoming
	}
	if toolStatusRank(existing) > toolStatusRank(incoming) {
		return existing
	}
	if toolStatusRank(existing) == 3 && (construction || toolStatusRank(incoming) == 3) {
		return existing
	}
	return incoming
}

func canonicalToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "started", "building", "constructing":
		return "constructing"
	case "ready":
		return "ready"
	case "pending", "active", "in_progress", "running":
		return "running"
	case "done", "success", "completed":
		return "completed"
	case "error", "failed":
		return "failed"
	case "canceled", "cancelled":
		return "cancelled"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func toolStatusRank(status string) int {
	switch canonicalToolStatus(status) {
	case "constructing":
		return 0
	case "ready":
		return 1
	case "running":
		return 2
	case "completed", "failed", "cancelled":
		return 3
	default:
		return 0
	}
}

func applyTaskStreamPatch(item *ToolTimelineItem, output string) bool {
	if item == nil || !strings.EqualFold(strings.TrimSpace(item.Name), "task") {
		return false
	}
	var payload map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &payload) != nil || strings.TrimSpace(anyString(payload["path_id"])) != "tool.task.stream.v2" || strings.TrimSpace(anyString(payload["tool"])) != "task" {
		return false
	}
	launch, _ := payload["launch"].(map[string]any)
	if len(launch) == 0 && !taskStreamPayloadHasProgramMetadata(payload) {
		return false
	}
	launchKey := firstNonEmpty(
		anyString(payload["launch_key"]),
		anyString(launch["launch_key"]),
		anyString(payload["child_session_id"]),
		anyString(launch["child_session_id"]),
	)
	if launchKey == "" {
		if launchIndex := anyInt(launch["launch_index"]); launchIndex > 0 {
			launchKey = fmt.Sprintf("launch:%d", launchIndex)
		}
	}
	if launchKey == "" && len(launch) > 0 {
		return false
	}
	stream := item.TaskStream
	if stream == nil {
		stream = &TaskStreamState{PathID: "tool.task.stream.v2", LaunchesByKey: make(map[string]map[string]any)}
	}
	if stream.LaunchesByKey == nil {
		stream.LaunchesByKey = make(map[string]map[string]any)
	} else {
		launchesByKey := make(map[string]map[string]any, len(stream.LaunchesByKey))
		for key, existingLaunch := range stream.LaunchesByKey {
			cloned := make(map[string]any, len(existingLaunch))
			for field, value := range existingLaunch {
				cloned[field] = value
			}
			launchesByKey[key] = cloned
		}
		stream.LaunchesByKey = launchesByKey
		stream.LaunchOrder = append([]string(nil), stream.LaunchOrder...)
	}
	if len(launch) > 0 {
		merged := make(map[string]any, len(stream.LaunchesByKey[launchKey])+len(launch)+1)
		for key, value := range stream.LaunchesByKey[launchKey] {
			merged[key] = value
		}
		for key, value := range launch {
			if key == "current_tool" && anyString(value) == "" && anyString(merged["current_tool"]) != "" {
				continue
			}
			merged[key] = value
		}
		merged["launch_key"] = launchKey
		stream.LaunchesByKey[launchKey] = merged
		if !containsString(stream.LaunchOrder, launchKey) {
			stream.LaunchOrder = append(stream.LaunchOrder, launchKey)
		}
	}
	sort.SliceStable(stream.LaunchOrder, func(i, j int) bool {
		left := anyInt(stream.LaunchesByKey[stream.LaunchOrder[i]]["launch_index"])
		right := anyInt(stream.LaunchesByKey[stream.LaunchOrder[j]]["launch_index"])
		if left > 0 && right > 0 && left != right {
			return left < right
		}
		if left > 0 && right <= 0 {
			return true
		}
		if left <= 0 && right > 0 {
			return false
		}
		return stream.LaunchOrder[i] < stream.LaunchOrder[j]
	})
	stream.Status = firstNonEmpty(anyString(payload["status"]), stream.Status)
	stream.LaunchCount = maxInt(anyInt(payload["launch_count"]), len(stream.LaunchOrder))
	stream.TaskMode = firstNonEmpty(anyString(payload["task_mode"]), stream.TaskMode)
	stream.SwarmStrategy = firstNonEmpty(anyString(payload["swarm_strategy"]), stream.SwarmStrategy)
	stream.IntegrationContract = firstNonEmpty(anyString(payload["integration_contract"]), stream.IntegrationContract)
	if required, ok := payload["integration_required"].(bool); ok {
		stream.IntegrationRequired = required
	}
	programLaunch := launch
	if launchKey != "" && stream.LaunchesByKey[launchKey] != nil {
		programLaunch = stream.LaunchesByKey[launchKey]
	}
	applyTaskProgramMetadata(stream, payload, programLaunch)
	item.TaskStream = stream
	return true
}

func taskStreamPayloadHasProgramMetadata(payload map[string]any) bool {
	return anyString(payload["program_id"]) != "" || toolObjectAny(payload["program"]) != nil || toolObjectAny(payload["program_status"]) != nil
}

func applyTaskProgramMetadata(stream *TaskStreamState, payload, launch map[string]any) {
	if stream == nil {
		return
	}
	program := toolObjectAny(payload["program"])
	status := toolObjectAny(payload["program_status"])
	definition := toolObjectAny(status["definition"])
	if program == nil {
		program = definition
	}
	stream.ProgramID = firstNonEmpty(anyString(payload["program_id"]), anyString(program["id"]), anyString(status["program_id"]), stream.ProgramID)
	stream.ProgramState = firstNonEmpty(anyString(payload["program_state"]), anyString(status["program_state"]), stream.ProgramState)
	stream.ActiveStageID = firstNonEmpty(anyString(payload["active_stage_id"]), anyString(status["active_stage_id"]), stream.ActiveStageID)
	stages, _ := program["stages"].([]any)
	if len(stages) == 0 {
		stages, _ = status["stages"].([]any)
	}
	if len(stages) > 0 {
		stream.ProgramStages = nil
		for _, raw := range stages {
			stage := toolObjectAny(raw)
			if id := anyString(stage["id"]); id != "" {
				stream.ProgramStages = append(stream.ProgramStages, TaskProgramStageState{ID: id, DependsOn: anyStringSlice(stage["depends_on"])})
			}
		}
	}
	if stream.ProgramJobsByID == nil {
		stream.ProgramJobsByID = make(map[string]map[string]any)
	}
	jobs, _ := program["jobs"].([]any)
	if len(jobs) == 0 {
		jobs, _ = status["job_definitions"].([]any)
	}
	if len(jobs) > 0 {
		for _, raw := range jobs {
			job := toolObjectAny(raw)
			id := anyString(job["id"])
			if id == "" {
				continue
			}
			stream.ProgramJobsByID[id] = cloneAnyObject(job)
			if !containsString(stream.ProgramJobOrder, id) {
				stream.ProgramJobOrder = append(stream.ProgramJobOrder, id)
			}
		}
	}
	if stream.ProgramJobStates == nil {
		stream.ProgramJobStates = make(map[string]map[string]any)
	}
	if jobs, ok := status["jobs"].([]any); ok {
		for _, raw := range jobs {
			job := toolObjectAny(raw)
			id := anyString(job["job_id"])
			if id != "" {
				stream.ProgramJobStates[id] = cloneAnyObject(job)
			}
		}
	}
	if len(launch) > 0 {
		source := toolObjectAny(launch["source_arguments"])
		jobID := firstNonEmpty(anyString(launch["program_job_id"]), anyString(source["program_job_id"]))
		stageID := firstNonEmpty(anyString(launch["program_stage_id"]), anyString(source["program_stage_id"]))
		if jobID != "" {
			launch["program_job_id"] = jobID
			launch["program_stage_id"] = stageID
			if _, ok := stream.ProgramJobsByID[jobID]; !ok {
				stream.ProgramJobsByID[jobID] = map[string]any{"id": jobID, "stage_id": stageID, "title": anyString(launch["assignment_label"]), "agent_type": anyString(launch["requested_subagent_type"]), "depends_on": source["depends_on"]}
				stream.ProgramJobOrder = append(stream.ProgramJobOrder, jobID)
			}
		}
	}
}

func toolObjectAny(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func cloneAnyObject(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for key, field := range value {
		out[key] = field
	}
	return out
}

func anyStringSlice(value any) []string {
	switch values := value.(type) {
	case []string:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text := strings.TrimSpace(value); text != "" {
				out = append(out, text)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text := anyString(value); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func anyString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func anyInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func findToolTimelineKey(tools map[string]ToolTimelineItem, callID, toolInstanceID string, step int, stepID string, outputIndex int, hasOutputIndex, construction bool) string {
	if callID != "" {
		if _, ok := tools[callID]; ok {
			return callID
		}
		for key, item := range tools {
			if item.CallID == callID || (toolInstanceID != "" && item.ToolInstanceID == toolInstanceID) {
				return key
			}
		}
		if hasOutputIndex {
			if key := findToolTimelineOutputKey(tools, step, stepID, outputIndex); key != "" {
				return key
			}
		}
		if !construction {
			if key := findUniqueConstructionToolKey(tools, step, stepID); key != "" {
				return key
			}
		}
		return callID
	}
	if toolInstanceID != "" {
		for key, item := range tools {
			if item.ToolInstanceID == toolInstanceID {
				return key
			}
		}
		return "instance:" + toolInstanceID
	}
	if construction && hasOutputIndex {
		if key := findToolTimelineOutputKey(tools, step, stepID, outputIndex); key != "" {
			return key
		}
		return fmt.Sprintf("construction:%s:output-%d", toolStepKey(step, stepID), outputIndex)
	}
	return ""
}

func findToolTimelineOutputKey(tools map[string]ToolTimelineItem, step int, stepID string, outputIndex int) string {
	for key, item := range tools {
		if item.ProviderConstruction && !item.RuntimeExecution && item.HasOutputIndex && item.OutputIndex == outputIndex && sameToolTimelineStep(item, step, stepID) {
			return key
		}
	}
	return ""
}

func findUniqueConstructionToolKey(tools map[string]ToolTimelineItem, step int, stepID string) string {
	match := ""
	for key, item := range tools {
		if !item.ProviderConstruction || item.RuntimeExecution || !sameToolTimelineStep(item, step, stepID) {
			continue
		}
		if match != "" {
			return ""
		}
		match = key
	}
	return match
}

func sameToolTimelineStep(item ToolTimelineItem, step int, stepID string) bool {
	if step > 0 && item.Step > 0 {
		return step == item.Step
	}
	return stepID != "" && item.StepID != "" && stepID == item.StepID
}

func toolStepKey(step int, stepID string) string {
	if step > 0 {
		return fmt.Sprintf("step-%d", step)
	}
	if stepID != "" {
		return stepID
	}
	return "step-0"
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func hasDurableToolMessage(messages []Message, item ToolTimelineItem) bool {
	for _, message := range messages {
		durable, ok := parseToolMessage(message)
		if !ok {
			continue
		}
		if (item.CallID != "" && durable.CallID == item.CallID) || (item.ToolInstanceID != "" && durable.ToolInstanceID == item.ToolInstanceID) {
			return true
		}
	}
	return false
}

func toolTerminalStatus(errorText string) string {
	if strings.TrimSpace(errorText) != "" {
		return "failed"
	}
	return "completed"
}

func rawString(payload map[string]json.RawMessage, keys ...string) string {
	return strings.TrimSpace(rawText(payload, keys...))
}

func rawText(payload map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if raw := payload[key]; len(raw) > 0 && json.Unmarshal(raw, &value) == nil {
			if strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return ""
}

func rawInt64(payload map[string]json.RawMessage, key string) int64 {
	value, _ := rawOptionalInt64(payload, key)
	return value
}

func rawOptionalInt(payload map[string]json.RawMessage, key string) (int, bool) {
	value, ok := rawOptionalInt64(payload, key)
	return int(value), ok
}

func rawOptionalInt64(payload map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := payload[key]
	if !ok || len(raw) == 0 {
		return 0, false
	}
	var value int64
	if json.Unmarshal(raw, &value) == nil {
		return value, true
	}
	return 0, false
}

func firstNonEmptyRaw(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func boundLiveTools(state State) State {
	if len(state.Tools) <= maxResidentMessages {
		return state
	}
	var oldestKey string
	var oldestSeq uint64
	for key, item := range state.Tools {
		if oldestKey == "" || (item.GlobalSeq != 0 && (oldestSeq == 0 || item.GlobalSeq < oldestSeq)) {
			oldestKey, oldestSeq = key, item.GlobalSeq
		}
	}
	delete(state.Tools, oldestKey)
	return state
}

func toolDurationLabel(ms int64) string {
	if ms <= 0 {
		return ""
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
