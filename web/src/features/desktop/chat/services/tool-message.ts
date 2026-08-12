import type {
  EditDiffPreview,
  SearchToolData,
  SearchToolFileGroup,
  SearchToolLineGroup,
  StructuredToolMessage,
  TodoToolData,
  TodoToolSummaryCounts,
  WebFetchStatusData,
  WebFetchToolData,
  WebResourceData,
  WebSearchQueryData,
  WebSearchToolData,
} from "../types/chat";

interface ToolHistoryPayload {
  path_id?: string;
  tool?: string;
  tool_name?: string;
  call_id?: string;
  run_id?: string;
  tool_instance_id?: string;
  arguments?: string;
  output?: string;
  completed_output?: string;
  error?: string;
  duration_ms?: number;
}

interface StructuredToolMessageInput {
  pathId?: StructuredToolMessage["pathId"];
  tool: string;
  callId?: string;
  runId?: string;
  toolInstanceId?: string;
  argumentsText?: string;
  outputText?: string;
  completedOutputText?: string;
  taskStream?: {
    launchesByKey: Record<string, Record<string, unknown>>;
    launchOrder: string[];
    taskMode?: string;
    programId?: string;
    programState?: string;
    activeStageId?: string;
    nextAction?: string;
    program?: Record<string, unknown>;
    programStatus?: Record<string, unknown>;
    swarmStrategy?: string;
    integrationContract?: string;
    integrationRequired?: boolean;
  };
  error?: string;
  durationMs?: number;
  state?: StructuredToolMessage["state"];
  lifecycleStatus?: string;
}

const MAX_STRUCTURED_OUTPUT_PARSE_BYTES = 1_000_000;
const MAX_PREVIEW_LINES = 12;

export type ToolActivitySemanticKind = "edit" | "plan" | "task" | "investigation" | "generic";

export interface ToolActivityDescriptor {
  kind: ToolActivitySemanticKind;
  label: string;
  activeLabel: string;
}

export function describeToolActivity(toolName: string): ToolActivityDescriptor {
  const normalized = String(toolName ?? "").trim().toLowerCase().replace(/-/g, "_");
  if (normalized === "edit" || normalized === "write") {
    return { kind: "edit", label: "Edit", activeLabel: "Editing" };
  }
  if (normalized === "plan" || normalized === "plan_manage" || normalized === "exit_plan_mode") {
    return { kind: "plan", label: "Plan", activeLabel: "Planning" };
  }
  if (normalized === "task" || normalized === "subagent" || normalized === "launch_subagent") {
    return { kind: "task", label: "Subagents", activeLabel: "Launching subagents" };
  }
  if (normalized === "search" || normalized === "read") {
    return { kind: "investigation", label: "Investigation", activeLabel: "Investigating" };
  }
  const label = normalized
    ? normalized.split("_").filter(Boolean).map((part) => part[0]?.toUpperCase() + part.slice(1)).join(" ")
    : "Tool";
  return { kind: "generic", label, activeLabel: normalized ? `Running ${label}` : "Starting tool" };
}
const MAX_PREVIEW_SCAN_BYTES = 32_000;
const MAX_PREVIEW_LINE_BYTES = 2_000;

function parseJsonRecord(value: string, maxBytes = Number.POSITIVE_INFINITY): Record<string, unknown> | null {
  if (value.length > maxBytes) return null;
  const trimmed = value.trim();
  if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) {
    return null;
  }
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

function jsonStr(obj: Record<string, unknown> | null | undefined, key: string): string {
  if (!obj) return "";
  const v = obj[key];
  return typeof v === "string" ? v.trim() : "";
}

function jsonRawStr(obj: Record<string, unknown> | null, key: string): string {
  if (!obj) return "";
  const v = obj[key];
  return typeof v === "string" ? v : "";
}

function jsonNum(obj: Record<string, unknown> | null, key: string): number {
  if (!obj) return 0;
  const v = obj[key];
  return typeof v === "number" ? v : 0;
}

function jsonBool(obj: Record<string, unknown> | null, key: string): boolean {
  if (!obj) return false;
  return obj[key] === true;
}

function jsonStrArray(
  obj: Record<string, unknown> | null,
  key: string,
): string[] {
  if (!obj) return [];
  const value = obj[key];
  if (!Array.isArray(value)) return [];
  return value
    .map((entry) => (typeof entry === "string" ? entry.trim() : ""))
    .filter(Boolean);
}

function hasJsonKey(obj: Record<string, unknown> | null, key: string): boolean {
  return Boolean(obj) && Object.prototype.hasOwnProperty.call(obj, key);
}

function jsonObjectSlice(
  obj: Record<string, unknown> | null,
  key: string,
): Record<string, unknown>[] {
  if (!obj) return [];
  const value = obj[key];
  if (!Array.isArray(value)) return [];
  return value.filter(
    (entry): entry is Record<string, unknown> =>
      Boolean(entry) && typeof entry === "object" && !Array.isArray(entry),
  );
}

function jsonRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function nestedObjectSlice(
  obj: Record<string, unknown> | null,
  key: string,
): Record<string, unknown>[] {
  const direct = jsonObjectSlice(obj, key);
  if (direct.length > 0) return direct;
  for (const nestedKey of ["data", "response", "output"]) {
    const nested = jsonRecord(obj?.[nestedKey]);
    const found = jsonObjectSlice(nested, key);
    if (found.length > 0) return found;
  }
  return [];
}

function webError(value: unknown): string {
  if (typeof value === "string") return value.trim();
  const record = jsonRecord(value);
  return firstNonEmpty(
    jsonStr(record, "message"),
    jsonStr(record, "error"),
    jsonStr(record, "tag"),
  );
}

function webDomain(url: string): string {
  try {
    const parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:"
      ? parsed.hostname.replace(/^www\./i, "")
      : "";
  } catch {
    return "";
  }
}

function extractWebResource(value: Record<string, unknown>): WebResourceData {
  const url = firstNonEmpty(jsonStr(value, "url"), jsonStr(value, "source_url"));
  const highlights = jsonStrArray(value, "highlights");
  const subpages = jsonObjectSlice(value, "subpages").map(extractWebResource);
  return {
    url,
    title: firstNonEmpty(jsonStr(value, "title"), jsonStr(value, "name")),
    domain: webDomain(url),
    author: jsonStr(value, "author"),
    publishedDate: firstNonEmpty(jsonStr(value, "published_date"), jsonStr(value, "publishedDate")),
    summary: jsonStr(value, "summary"),
    text: firstNonEmptyRaw(jsonRawStr(value, "text"), jsonRawStr(value, "content")),
    highlights,
    error: webError(value.error),
    status: jsonStr(value, "status"),
    subpages,
  };
}

function extractWebSearchToolData(
  outputJson: Record<string, unknown> | null,
  argumentsJson: Record<string, unknown> | null,
): WebSearchToolData | null {
  const effective = outputJson ?? argumentsJson;
  if (!effective) return null;

  const requestedQueries = [
    ...jsonStrArray(outputJson, "queries"),
    ...jsonStrArray(argumentsJson, "queries"),
  ];
  const singleQuery = firstNonEmpty(jsonStr(outputJson, "query"), jsonStr(argumentsJson, "query"));
  const queries = Array.from(new Set([...requestedQueries, ...(singleQuery ? [singleQuery] : [])]));
  const rawResults = nestedObjectSlice(outputJson, "results");
  const wrapperResults = rawResults.filter((item) => Array.isArray(item.results) || Boolean(jsonStr(item, "query")));
  const directHits = wrapperResults.length === 0 ? rawResults : [];
  const queryResults: WebSearchQueryData[] = wrapperResults.map((item, index) => {
    const results = jsonObjectSlice(item, "results").map(extractWebResource);
    const query = jsonStr(item, "query") || queries[index] || "";
    return {
      query,
      count: hasJsonKey(item, "count") ? jsonNum(item, "count") : results.length,
      searchType: firstNonEmpty(jsonStr(item, "resolved_search_type"), jsonStr(item, "requested_search_type")),
      timedOut: jsonBool(item, "timed_out"),
      error: webError(item.error),
      results,
    };
  });
  if (directHits.length > 0) {
    queryResults.push({
      query: queries[0] || "",
      count: directHits.length,
      searchType: firstNonEmpty(jsonStr(outputJson, "search_type"), jsonStr(argumentsJson, "search_type")),
      timedOut: jsonBool(outputJson, "timed_out"),
      error: webError(outputJson?.error),
      results: directHits.map(extractWebResource),
    });
  }
  if (queryResults.length === 0 && queries.length > 0) {
    queryResults.push(...queries.map((query) => ({
      query,
      count: 0,
      searchType: firstNonEmpty(jsonStr(outputJson, "search_type"), jsonStr(argumentsJson, "search_type")),
      timedOut: jsonBool(outputJson, "timed_out"),
      error: "",
      results: [],
    })));
  }

  const resultCount = queryResults.reduce((sum, item) => sum + item.results.length, 0);
  const failedCount = queryResults.filter((item) => Boolean(item.error)).length;
  const searchTypes = jsonStrArray(outputJson, "resolved_search_types");
  return {
    queries: queries.length > 0 ? queries : queryResults.map((item) => item.query).filter(Boolean),
    queryCount: jsonNum(outputJson, "query_count") || queryResults.length || queries.length,
    totalResults: hasJsonKey(outputJson, "total_results") ? jsonNum(outputJson, "total_results") : resultCount,
    failedQueries: hasJsonKey(outputJson, "failed_queries") ? jsonNum(outputJson, "failed_queries") : failedCount,
    truncated: jsonBool(outputJson, "details_truncated") || jsonBool(outputJson, "truncated_queries") || jsonBool(outputJson, "truncated"),
    searchType: searchTypes.join(", ") || firstNonEmpty(jsonStr(outputJson, "requested_search_type"), jsonStr(argumentsJson, "search_type")),
    queryResults,
  };
}

function extractWebFetchToolData(
  outputJson: Record<string, unknown> | null,
  argumentsJson: Record<string, unknown> | null,
): WebFetchToolData | null {
  const effective = outputJson ?? argumentsJson;
  if (!effective) return null;
  const urls = Array.from(new Set([
    ...jsonStrArray(outputJson, "urls"),
    ...jsonStrArray(argumentsJson, "urls"),
    ...[firstNonEmpty(jsonStr(outputJson, "url"), jsonStr(argumentsJson, "url"))].filter(Boolean),
  ]));
  const results = nestedObjectSlice(outputJson, "results").map(extractWebResource);
  const statuses: WebFetchStatusData[] = nestedObjectSlice(outputJson, "statuses").map((item) => ({
    id: jsonStr(item, "id"),
    status: jsonStr(item, "status"),
    source: jsonStr(item, "source"),
    error: webError(item.error),
  }));
  const inferredSuccess = results.filter((item) => !item.error).length;
  return {
    urls,
    count: hasJsonKey(outputJson, "count") ? jsonNum(outputJson, "count") : results.length,
    successCount: hasJsonKey(outputJson, "success_count") ? jsonNum(outputJson, "success_count") : inferredSuccess,
    timedOut: jsonBool(outputJson, "timed_out"),
    truncated: jsonBool(outputJson, "details_truncated") || jsonBool(outputJson, "truncated_urls") || jsonBool(outputJson, "truncated"),
    results,
    statuses,
  };
}

function firstNonEmpty(...values: string[]): string {
  for (const value of values) {
    const trimmed = value.trim();
    if (trimmed) return trimmed;
  }
  return "";
}

function firstNonEmptyRaw(...values: string[]): string {
  for (const value of values) {
    if (value.trim()) return value;
  }
  return "";
}

function resolveToolTarget(
  argumentsJson: Record<string, unknown> | null,
): string | null {
  if (!argumentsJson) {
    return null;
  }
  for (const key of ["path", "url", "open_url", "thread_id", "command", "session_id", "cwd"]) {
    const value = argumentsJson[key];
    if (typeof value === "string" && value.trim() !== "") {
      return value.trim();
    }
  }
  return null;
}

function summarizeToolOutput(
  toolName: string,
  outputJson: Record<string, unknown> | null,
  argumentsJson: Record<string, unknown> | null,
): string {
  if (!outputJson && !argumentsJson) return toolName || "tool";

  const effective = outputJson ?? argumentsJson;
  if (!effective) return toolName || "tool";
  const tool = toolName.toLowerCase();
  if (tool === "thinking") {
    return "THINKING";
  }

  switch (tool) {
    case "compact": {
      const label = jsonStr(argumentsJson, "label") || "Compact";
      return label;
    }
    case "read": {
      const path = jsonStr(effective, "path");
      const lineStart = jsonNum(effective, "line_start");
      const count = jsonNum(effective, "count");
      const bytes = jsonNum(effective, "bytes");
      const truncated = jsonBool(effective, "truncated");
      const binarySuppressed = jsonBool(effective, "binary_suppressed");
      if (!path && count <= 0 && bytes <= 0) return "read";
      let s = "read";
      if (path) s += " " + path;
      if (count > 0) {
        const start = lineStart > 0 ? lineStart : 1;
        const end = start + count - 1;
        s += count === 1 ? ` (line ${start}` : ` (lines ${start}-${end}`;
        if (truncated) s += ", truncated";
        s += ")";
      } else if (bytes > 0) {
        s += ` (${formatBytes(bytes)}`;
        if (truncated) s += ", truncated";
        s += ")";
      }
      if (binarySuppressed) s += " [binary]";
      return s;
    }
    case "write": {
      const path = jsonStr(effective, "path");
      const written = jsonNum(effective, "bytes_written");
      const appendMode = jsonBool(effective, "append");
      const action = appendMode ? "append" : "write";
      if (written > 0) return `${action} ${path} (${formatBytes(written)})`;
      if (path) return `${action} ${path}`;
      return action;
    }
    case "edit": {
      const path = jsonStr(effective, "path") || jsonStr(argumentsJson, "path");
      if (path) return `edit ${path}`;
      return "edit";
    }
    case "bash": {
      const exitCode = hasJsonKey(outputJson, "exit_code")
        ? jsonNum(outputJson, "exit_code")
        : null;
      const timedOut = jsonBool(outputJson, "timed_out");
      const truncated = jsonBool(outputJson, "truncated");
      const binarySuppressed = jsonBool(outputJson, "binary_suppressed");
      const notes: string[] = [];
      if (timedOut) notes.push("timed out");
      else if (typeof exitCode === "number" && exitCode !== 0)
        notes.push("failed");
      if (truncated) notes.push("partial output");
      if (binarySuppressed) notes.push("binary output hidden");
      return summaryWithNotes("bash", notes);
    }
    case "grep": {
      const pattern =
        jsonStr(outputJson, "pattern") || jsonStr(argumentsJson, "pattern");
      const root =
        jsonStr(outputJson, "path") || jsonStr(argumentsJson, "path");
      const count = hasJsonKey(outputJson, "count")
        ? jsonNum(outputJson, "count")
        : null;
      const truncated = jsonBool(outputJson, "truncated");
      const timedOut = jsonBool(outputJson, "timed_out");
      let s = "grep";
      if (pattern) s += ` ${quotedSummary(pattern, 80)}`;
      if (root) s += " in " + root;
      const notes: string[] = [];
      if (typeof count === "number")
        notes.push(countLabel(count, "match", "matches"));
      if (timedOut) notes.push("timed out");
      else if (truncated) notes.push("partial results");
      return summaryWithNotes(s, notes);
    }
    case "list": {
      const path = jsonStr(effective, "path");
      const mode = jsonStr(effective, "mode");
      const count = jsonNum(effective, "count");
      const totalFound = jsonNum(effective, "total_found");
      const truncated = jsonBool(effective, "truncated");
      const scanLimited = jsonBool(effective, "scan_limited");
      let s = "list";
      if (path) s += " " + path;
      const notes: string[] = [];
      if (totalFound > count)
        notes.push(`showing ${count} of ${totalFound} entries`);
      else notes.push(countLabel(count, "entry", "entries"));
      const view = listModeLabel(mode);
      if (view) notes.push(view);
      if (truncated) notes.push("partial results");
      if (scanLimited) notes.push("scan limited");
      return summaryWithNotes(s, notes);
    }
    case "glob": {
      const pattern = jsonStr(effective, "pattern");
      const root = jsonStr(effective, "path");
      const count = jsonNum(effective, "count");
      const truncated = jsonBool(effective, "truncated");
      const timedOut = jsonBool(effective, "timed_out");
      let s = "glob";
      if (pattern) s += ` ${quotedSummary(pattern, 80)}`;
      if (root) s += " in " + root;
      const notes: string[] = [countLabel(count, "file", "files")];
      if (timedOut) notes.push("timed out");
      else if (truncated) notes.push("partial results");
      return summaryWithNotes(s, notes);
    }
    case "websearch": {
      const queryCount = jsonNum(effective, "query_count");
      const totalResults =
        jsonNum(effective, "total_results") ||
        jsonNum(effective, "results_count") ||
        jsonNum(effective, "count");
      const requestedSearchType =
        jsonStr(effective, "requested_search_type") ||
        jsonStr(effective, "search_type");
      const resolvedSearchTypes = jsonStrArray(
        effective,
        "resolved_search_types",
      );
      const searchType =
        resolvedSearchTypes.length === 1 &&
        requestedSearchType &&
        resolvedSearchTypes[0].toLowerCase() !==
          requestedSearchType.toLowerCase()
          ? `${requestedSearchType} -> ${resolvedSearchTypes[0]}`
          : resolvedSearchTypes[0] || requestedSearchType;
      const notes: string[] = [];
      if (queryCount > 1) notes.push(`${queryCount} queries`);
      if (totalResults > 0 || hasJsonKey(effective, "total_results"))
        notes.push(countLabel(totalResults, "result", "results"));
      if (searchType) notes.push(searchType);
      const query =
        jsonStr(effective, "query") ||
        (queryCount <= 1 ? jsonStrArray(effective, "queries")[0] || "" : "");
      if (query)
        return summaryWithNotes(`websearch ${quotedSummary(query, 60)}`, notes);
      return summaryWithNotes("websearch", notes);
    }
    case "search": {
      const mode = jsonStr(effective, "search_mode").toLowerCase();
      const root = jsonStr(effective, "path");
      const count = jsonNum(effective, "count");
      const totalMatched = jsonNum(effective, "total_matched");
      const queryCount = jsonNum(effective, "query_count");
      const truncated =
        jsonBool(effective, "truncated") ||
        jsonBool(effective, "details_truncated") ||
        jsonBool(effective, "truncated_queries");
      const timedOut = jsonBool(effective, "timed_out");
      let s = "search";
      const query = jsonStr(effective, "query");
      if (query && queryCount <= 1) s += ` ${quotedSummary(query, 60)}`;
      else if (queryCount > 1) s += ` (${queryCount} queries)`;
      if (root) s += " in " + root;
      const notes: string[] = [];
      if (count > 0)
        notes.push(
          countLabel(
            count,
            mode === "files" ? "file" : "match",
            mode === "files" ? "files" : "matches",
          ),
        );
      if (totalMatched > count) notes.push(`${totalMatched} total`);
      if (timedOut) notes.push("timed out");
      else if (truncated) notes.push("partial");
      return summaryWithNotes(s, notes);
    }
    case "webfetch": {
      const url =
        jsonStr(effective, "url") || jsonStrArray(effective, "urls")[0] || "";
      const count = jsonNum(effective, "count");
      const successCount = jsonNum(effective, "success_count");
      const notes: string[] = [];
      if (count > 0 || hasJsonKey(effective, "count"))
        notes.push(countLabel(count, "record", "records"));
      if (successCount > 0) notes.push(`${successCount} ok`);
      if (url) return summaryWithNotes(`webfetch ${url}`, notes);
      return summaryWithNotes("webfetch", notes);
    }
    case "task": {
      const description =
        jsonStr(effective, "description") || jsonStr(effective, "goal");
      const status = jsonStr(effective, "status");
      const agentType =
        jsonStr(effective, "resolved_agent_name") ||
        jsonStr(effective, "agent_type") ||
        jsonStr(effective, "subagent");
      const launchCount = jsonNum(effective, "launch_count");
      const parts: string[] = [];
      if (description) {
        parts.push(description);
      } else if (agentType) {
        parts.push("@" + agentType);
      }
      if (launchCount > 1) parts.push(`(${launchCount} launches)`);
      if (status) parts.push("(" + status + ")");
      return parts.length ? "task " + parts.join(" ") : "task";
    }
    case "plan_manage":
    case "plan-manage": {
      return summarizePlanManageToolOutput(effective);
    }
    case "manage_theme":
    case "manage-theme": {
      const action = jsonStr(effective, "action").replace(/_/g, " ");
      const generatedNames = jsonStrArray(effective, "generated_names");
      const generatedCount = jsonNum(effective, "generated_count") || generatedNames.length;
      const resultSummary = jsonStr(effective, "summary");
      if (generatedCount > 0) {
        const count = countLabel(generatedCount, "theme", "themes");
        const names = generatedNames.length > 0 ? `: ${generatedNames.join(", ")}` : "";
        return `theme generated ${count}${names}`;
      }
      if (resultSummary) return `theme · ${resultSummary}`;
      return action ? `theme ${action}` : "theme";
    }
    case "manage_todos":
    case "manage-todos": {
      const todoData = extractTodoToolData(effective);
      if (!todoData) return "todo";
      const action = todoData.action;
      const ownerSuffix = todoData.ownerKind ? ` [${todoData.ownerKind}]` : "";
      let summary = `todo${ownerSuffix}`;
      if (action) summary += ` ${action}`;
      const notes = todoSummaryNotes(todoData.summary);
      if (action === "batch") {
        if (todoData.operationCount > 0) notes.unshift(`${todoData.operationCount} ops`);
        return notes.length ? `${summary} (${notes.join(", ")})` : summary;
      }
      const item =
        effective.item &&
        typeof effective.item === "object" &&
        !Array.isArray(effective.item)
          ? (effective.item as Record<string, unknown>)
          : null;
      const text = item ? jsonStr(item, "text") : "";
      const id = item ? jsonStr(item, "id") : jsonStr(effective, "id");
      if (text) return `${summary} · ${[text, ...notes].join(" · ")}`;
      if (id) return `${summary} · ${[id, ...notes].join(" · ")}`;
      if (notes.length) return `${summary} (${notes.join(", ")})`;
      return summary;
    }
    case "ask-user":
    case "ask_user": {
      const question = jsonStr(effective, "question");
      if (question) return "ask-user " + question;
      return "ask-user";
    }
    case "exit-plan-mode":
    case "exit_plan_mode":
    case "permission": {
      const exitPlanSummary = summarizeExitPlanToolOutput(
        tool,
        outputJson,
        argumentsJson,
      );
      if (exitPlanSummary) return exitPlanSummary;
      const title = jsonStr(effective, "title");
      if (title) return "plan " + title;
      return tool === "permission" ? "permission" : "plan";
    }
    default:
      return toolName || "tool";
  }
}

function normalizePlanManageAction(action: string): string {
  switch (action.trim().toLowerCase()) {
    case "active":
    case "current":
      return "get-active";
    case "activate":
    case "use":
      return "set-active";
    case "revisions":
      return "history";
    case "update_section":
      return "update-section";
    default:
      return action.trim().toLowerCase();
  }
}

function planManageActionDisplay(action: string): string {
  switch (normalizePlanManageAction(action)) {
    case "get-active":
      return "active";
    case "set-active":
      return "activate";
    case "update-section":
      return "update section";
    default:
      return normalizePlanManageAction(action).replace(/_/g, " ");
  }
}

function firstPlanPreviewLine(planBody: string): string {
  return previewTextLines(planBody, 1)[0] ?? "";
}

function summarizePlanManageToolOutput(payload: Record<string, unknown>): string {
  const action = normalizePlanManageAction(jsonStr(payload, "action"));
  const plan = jsonRecord(payload.plan);
  const title = jsonStr(plan, "title");
  const planId = firstNonEmpty(
    jsonStr(plan, "id"),
    jsonStr(payload, "plan_id"),
    jsonStr(payload, "active_plan_id"),
  );
  const status = jsonStr(payload, "status") || jsonStr(plan, "status");
  let summary = "plan";
  if (action) summary += ` ${planManageActionDisplay(action)}`;

  const notes: string[] = [];
  if (title) notes.push(title);
  else if (planId && action !== "list" && action !== "history") notes.push(planId);

  if (action === "list") {
    const count = Math.max(jsonObjectSlice(payload, "plans").length, jsonNum(payload, "count"));
    notes.push(countLabel(count, "plan", "plans"));
    if (planId) notes.push(`active ${planId}`);
  } else if (action === "history") {
    const count = Math.max(jsonObjectSlice(payload, "revisions").length, jsonNum(payload, "count"));
    notes.push(countLabel(count, "revision", "revisions"));
    if (planId) notes.push(planId);
  } else if (action === "get-active" && status.toLowerCase() === "empty") {
    notes.push("no active plan");
  } else if (action === "get" && status.toLowerCase() === "not_found") {
    notes.push("not found");
  }

  if (notes.length) return `${summary} (${notes.join(", ")})`;
  const fallback = jsonStr(payload, "summary");
  return fallback ? `plan · ${fallback}` : summary;
}

function planListPreviewLine(plan: Record<string, unknown>): string {
  const parts: string[] = [];
  if (jsonBool(plan, "active")) parts.push("active");
  const title = jsonStr(plan, "title");
  const id = jsonStr(plan, "id");
  const status = jsonStr(plan, "status");
  if (title) parts.push(title);
  if (id) parts.push(id);
  if (status) parts.push(status);
  return parts.join(" · ");
}

function planRevisionPreviewLine(revision: Record<string, unknown>): string {
  const parts: string[] = [];
  const version = jsonNum(revision, "version");
  if (version > 0) parts.push(`v${version}`);
  const title = jsonStr(revision, "title");
  const update = jsonStr(revision, "update_summary");
  const id = jsonStr(revision, "id");
  if (title) parts.push(title);
  if (update) parts.push(update);
  if (!parts.length && id) parts.push(id);
  return parts.join(" · ");
}

function buildPlanManagePreviewLines(
  payload: Record<string, unknown> | null,
  maxLines: number,
): string[] {
  if (!payload) return [];
  const out: string[] = [];
  const action = normalizePlanManageAction(jsonStr(payload, "action"));
  if (action) pushPreviewLine(out, `action: ${planManageActionDisplay(action)}`, maxLines);
  const status = jsonStr(payload, "status");
  if (status) pushPreviewLine(out, `status: ${status}`, maxLines);

  const plan = jsonRecord(payload.plan);
  if (plan) {
    const title = jsonStr(plan, "title");
    const planId = jsonStr(plan, "id");
    const update = jsonStr(plan, "update_summary");
    const preview = firstPlanPreviewLine(jsonStr(plan, "plan"));
    if (title) pushPreviewLine(out, `title: ${title}`, maxLines);
    if (planId) pushPreviewLine(out, `plan: ${planId}`, maxLines);
    if (update) pushPreviewLine(out, `update: ${update}`, maxLines);
    if (preview) pushPreviewLine(out, preview, maxLines);
  }

  const activeId = jsonStr(payload, "active_plan_id");
  if (activeId) pushPreviewLine(out, `active: ${activeId}`, maxLines);
  for (const item of jsonObjectSlice(payload, "plans")) {
    pushPreviewLine(out, planListPreviewLine(item), maxLines);
  }
  for (const item of jsonObjectSlice(payload, "revisions")) {
    pushPreviewLine(out, planRevisionPreviewLine(item), maxLines);
  }
  if (!out.length) pushPreviewLine(out, jsonStr(payload, "summary"), maxLines);
  return out;
}

function extractTodoToolData(
  payload: Record<string, unknown> | null,
): TodoToolData | null {
  if (!payload) return null;
  const summaryPayload = todoSummaryPayload(payload);
  return {
    action: jsonStr(payload, "action"),
    ownerKind: jsonStr(payload, "owner_kind"),
    operationCount: Math.max(
      jsonObjectSlice(payload, "operations").length,
      jsonNum(payload, "operation_count"),
    ),
    summary: summaryPayload
      ? {
          taskCount: jsonNum(summaryPayload, "task_count"),
          openCount: jsonNum(summaryPayload, "open_count"),
          inProgressCount: jsonNum(summaryPayload, "in_progress_count"),
        }
      : null,
  };
}

function todoSummaryPayload(
  payload: Record<string, unknown>,
): Record<string, unknown> | null {
  const summary = payload.summary;
  return summary && typeof summary === "object" && !Array.isArray(summary)
    ? (summary as Record<string, unknown>)
    : null;
}

function todoSummaryNotes(
  summary: TodoToolSummaryCounts | null,
): string[] {
  if (!summary) return [];
  const notes: string[] = [];
  notes.push(`${summary.openCount} open · ${summary.taskCount} total`);
  notes.push(`${summary.inProgressCount} in progress`);
  return notes;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function countLabel(count: number, singular: string, plural: string): string {
  return `${count} ${count === 1 ? singular : plural}`;
}

function summaryWithNotes(label: string, notes: string[]): string {
  const filtered = notes.map((note) => note.trim()).filter(Boolean);
  if (!filtered.length) return label;
  return `${label} (${filtered.join(", ")})`;
}

function formatDurationCompact(ms: number): string {
  if (ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

function quotedSummary(value: string, _max: number): string {
  return JSON.stringify(value);
}

function listModeLabel(mode: string): string {
  switch (mode.trim().toLowerCase()) {
    case "tree":
      return "tree view";
    case "flat":
      return "flat view";
    case "":
      return "";
    default:
      return `${mode} view`;
  }
}

function expandEditPreviewLines(value: string, truncated: boolean): string[] {
  let text = value.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  text = text.replace(/\\n/g, "\n").replace(/\\t/g, "\t");
  text = text.replace(/\n+$/, "");
  if (text === "") return ["(empty)"];
  const lines = text.split("\n");
  if (lines.length === 0) return ["(empty)"];
  if (truncated) {
    lines[lines.length - 1] += " ...";
  }
  return lines;
}

function editDiffHunkFromPayload(
  payload: Record<string, unknown>,
  fallbackIndex: number,
): EditDiffPreview["hunks"][number] | null {
  const oldPreviewRaw = jsonStr(payload, "old_string_preview");
  const newPreviewRaw = jsonStr(payload, "new_string_preview");
  if (!oldPreviewRaw && !newPreviewRaw) return null;
  const oldTruncated = jsonBool(payload, "old_string_truncated");
  const newTruncated = jsonBool(payload, "new_string_truncated");
  return {
    index: Math.max(1, jsonNum(payload, "index") || fallbackIndex),
    oldLines: expandEditPreviewLines(oldPreviewRaw || "(empty)", oldTruncated),
    newLines: expandEditPreviewLines(newPreviewRaw || "(empty)", newTruncated),
    oldTruncated,
    newTruncated,
  };
}

function extractEditDiff(
  outputJson: Record<string, unknown> | null,
): EditDiffPreview | null {
  if (!outputJson) return null;
  const hunks = jsonObjectSlice(outputJson, "edits")
    .map((item, index) => editDiffHunkFromPayload(item, index + 1))
    .filter((hunk): hunk is EditDiffPreview["hunks"][number] => Boolean(hunk));
  if (hunks.length === 0) {
    const hunk = editDiffHunkFromPayload(outputJson, 1);
    if (hunk) hunks.push(hunk);
  }
  if (hunks.length === 0) return null;
  return {
    oldLines: hunks[0].oldLines,
    newLines: hunks[0].newLines,
    oldTruncated: hunks.some((hunk) => hunk.oldTruncated),
    newTruncated: hunks.some((hunk) => hunk.newTruncated),
    hunks,
  };
}

function previewTextLines(value: string, maxLines = MAX_PREVIEW_LINES): string[] {
  if (!value || maxLines <= 0) return [];
  const scan = value.slice(0, MAX_PREVIEW_SCAN_BYTES);
  const lines: string[] = [];
  let start = 0;
  while (start < scan.length && lines.length < maxLines) {
    const newline = scan.indexOf("\n", start);
    const end = newline === -1 ? scan.length : newline;
    const line = scan.slice(start, end).replace(/\r$/, "").trimEnd();
    if (line.trim()) lines.push(line.slice(0, MAX_PREVIEW_LINE_BYTES));
    if (newline === -1) break;
    start = newline + 1;
  }
  return lines;
}

function taskPreviewText(payload: Record<string, unknown> | null): string {
  if (!payload) return "";
  const previewKind = jsonStr(payload, "current_preview_kind").toLowerCase();
  const previewText = jsonStr(payload, "current_preview_text");
  if (previewKind === "reasoning" || previewKind === "assistant") {
    return "";
  }
  if (previewText) return previewText;
  return "";
}

function normalizeTaskToolDisplay(
  tool: string,
  previewKind: string,
  previewText: string,
): { tool: string; previewKind: string; previewText: string } {
  const normalizedKind = previewKind.trim().toLowerCase();
  if (normalizedKind === "reasoning") {
    return {
      tool: "thinking",
      previewKind: "thinking",
      previewText: "",
    };
  }
  if (normalizedKind === "assistant") {
    return {
      tool,
      previewKind: "assistant",
      previewText: "",
    };
  }
  return {
    tool,
    previewKind,
    previewText,
  };
}

function normalizeSwarmStrategy(...values: string[]): "explore" | "assembly" | undefined {
  for (const value of values) {
    const normalized = value.trim().toLowerCase();
    if (normalized === "assembly") return "assembly";
    if (normalized === "explore") return "explore";
  }
  return undefined;
}

function taskAssemblyPart(payload: Record<string, unknown> | null): StructuredToolMessage["taskRows"][number]["assemblyPart"] {
  const part = jsonRecord(payload?.assembly_part);
  if (!part) return null;
  const name = jsonStr(part, "name");
  if (!name) return null;
  return {
    name,
    instructions: jsonStr(part, "instructions"),
    ownedScope: jsonStrArray(part, "owned_scope"),
  };
}

function buildTaskToolRow(
  payload: Record<string, unknown> | null,
  fallbackLaunchIndex = 0,
): StructuredToolMessage["taskRows"][number] | null {
  if (!payload) return null;
  const status = jsonStr(payload, "status") || "pending";
  const phase = jsonStr(payload, "phase");
  const normalizedStatus = status.trim().toLowerCase();
  const terminal = ["done", "ok", "success", "completed", "complete", "error", "failed", "cancelled", "canceled"].includes(normalizedStatus);
  const launchKey = jsonStr(payload, "launch_key");
  const sourceArguments = jsonRecord(payload.source_arguments);
  const launchIndex = Math.max(0, jsonNum(payload, "launch_index") || fallbackLaunchIndex);
  const childSessionId = firstNonEmpty(
    jsonStr(payload, "session_id"),
    jsonStr(payload, "child_session_id"),
  );
  const agent = firstNonEmpty(
    jsonStr(payload, "resolved_agent_name"),
    jsonStr(payload, "requested_subagent_type"),
    jsonStr(payload, "agent_type"),
    jsonStr(payload, "subagent"),
    jsonStr(payload, "requested_subagent"),
    "subagent",
  );
  const assemblyPart = taskAssemblyPart(payload);
  const assignmentLabel = firstNonEmpty(assemblyPart?.name ?? "", jsonStr(payload, "assignment_label"));
  const providerLabel = jsonStr(payload, "subagent_provider");
  const model = jsonStr(payload, "subagent_model");
  const modelLabel = providerLabel && model && model.toLowerCase().startsWith(`${providerLabel.toLowerCase()}/`)
    ? model
    : [providerLabel, model].filter(Boolean).join(" / ");
  const rawPreviewKind = jsonStr(payload, "current_preview_kind");
  const error = jsonStr(payload, "error");
  let tool = firstNonEmpty(jsonStr(payload, "current_tool_display"), jsonStr(payload, "current_tool"));
  if (!tool && rawPreviewKind.trim().toLowerCase() !== "reasoning") {
    const toolOrder = jsonStrArray(payload, "tool_order");
    tool = toolOrder[toolOrder.length - 1] || "-";
  }
  const currentToolMs = jsonNum(payload, "current_tool_ms");
  const elapsedMs = jsonNum(payload, "elapsed_ms");
  const time = terminal ? formatDurationCompact(elapsedMs || currentToolMs) : "";
  const toolOrder = jsonStrArray(payload, "tool_order").filter((entry) => entry.trim() && entry.trim() !== "-");
  const liveToolCalls = toolOrder.length > 0
    ? toolOrder.slice(-12).map((entry, index, recent) => {
      if (index !== recent.length - 1) return entry;
      const activeTool = firstNonEmpty(jsonStr(payload, "current_tool_display"), jsonStr(payload, "current_tool"), entry);
      return terminal ? activeTool : `${activeTool} · ${status.trim().toLowerCase() || "running"}`;
    }).join("\n")
    : "";
  const previewText = error || taskPreviewText(payload);
  const normalized = normalizeTaskToolDisplay(tool, error ? "error" : rawPreviewKind, previewText);
  const launchStartedAtMs = jsonNum(payload, "launch_started_at_ms");
  const currentToolStartedAtMs = jsonNum(payload, "current_tool_started_at_ms");
  if (!agent && normalized.tool === "-" && !time && !status && !normalized.previewText) return null;
  return {
    launchKey: launchKey || undefined,
    launchIndex,
    childSessionId,
    programId: firstNonEmpty(jsonStr(payload, "program_id"), jsonStr(sourceArguments, "program_id")) || undefined,
    programJobId: firstNonEmpty(jsonStr(payload, "program_job_id"), jsonStr(payload, "job_id"), jsonStr(sourceArguments, "program_job_id")) || undefined,
    programStageId: firstNonEmpty(jsonStr(payload, "program_stage_id"), jsonStr(payload, "stage_id"), jsonStr(sourceArguments, "program_stage_id")) || undefined,
    dependsOn: jsonStrArray(sourceArguments, "depends_on").length > 0 ? jsonStrArray(sourceArguments, "depends_on") : jsonStrArray(payload, "depends_on"),
    status,
    phase,
    agent,
    assignmentLabel,
    modelLabel,
    tool: normalized.tool || "-",
    toolActivitySummary: normalized.tool && normalized.tool !== "-" ? normalized.tool : undefined,
    liveToolCalls: liveToolCalls || undefined,
    time,
    previewKind: normalized.previewKind,
    previewText: normalized.previewText,
    launchStartedAtMs,
    currentToolStartedAtMs,
    elapsedMs,
    currentToolMs,
    terminal,
    swarmMode: jsonBool(payload, "swarm_mode"),
    swarmStrategy: normalizeSwarmStrategy(jsonStr(payload, "swarm_strategy")) ?? (jsonBool(payload, "swarm_mode") ? "explore" : undefined),
    assemblyPart,
    integrationContract: jsonStr(payload, "integration_contract"),
    integrationRequired: jsonBool(payload, "integration_required"),
  };
}

function isTerminalTaskPayload(payload: Record<string, unknown> | null): boolean {
  if (!payload || jsonStr(payload, "path_id") !== "tool.task.v1") return false;
  const status = jsonStr(payload, "status").toLowerCase();
  return ["done", "ok", "success", "completed", "complete", "error", "failed", "cancelled", "canceled"].includes(status);
}

function buildTaskToolRows(
  payload: Record<string, unknown> | null,
  taskStream?: StructuredToolMessageInput["taskStream"],
): StructuredToolMessage["taskRows"] {
  if (taskStream && !isTerminalTaskPayload(payload)) {
    return taskStream.launchOrder
      .map((launchKey, index) => {
        const launch = taskStream.launchesByKey[launchKey] ?? null;
        return buildTaskToolRow(launch ? {
          ...launch,
          launch_key: jsonStr(launch, "launch_key") || launchKey,
          program_id: jsonStr(launch, "program_id") || taskStream.programId,
        } : null, index + 1);
      })
      .filter((row): row is StructuredToolMessage["taskRows"][number] => Boolean(row));
  }
  if (!payload) return [];

  const launches = jsonObjectSlice(payload, "launches");
  if (launches.length > 0) {
    return launches
      .map((launch, index) => buildTaskToolRow(launch, index + 1))
      .filter((row): row is StructuredToolMessage["taskRows"][number] => Boolean(row));
  }

  if (payload.path_id === "tool.task.stream.v2") {
    const launch = payload.launch && typeof payload.launch === "object" && !Array.isArray(payload.launch)
      ? payload.launch as Record<string, unknown>
      : null;
    const row = buildTaskToolRow(launch ? { ...launch, launch_key: jsonStr(launch, "launch_key") || jsonStr(payload, "launch_key") } : payload, 1);
    return row ? [row] : [];
  }

  const row = buildTaskToolRow(payload, 1);
  return row ? [row] : [];
}

function taskProgramJobStateToRowStatus(state: string): string {
  switch (state.trim().toLowerCase()) {
    case "integrated":
    case "completed":
      return "completed";
    case "handoff_ready":
      return "pending";
    case "running":
      return "running";
    case "failed":
    case "blocked":
    case "cancelled":
      return state.trim().toLowerCase();
    default:
      return "pending";
  }
}

function taskProgramRecord(
  outputJson: Record<string, unknown> | null,
  taskStream?: StructuredToolMessageInput["taskStream"],
): Record<string, unknown> | null {
  const terminalStatus = jsonRecord(outputJson?.program_status);
  if (terminalStatus) return terminalStatus;
  const outputHasProgramStatus = Boolean(
    jsonStr(outputJson, "program_state")
    || jsonStr(outputJson, "active_stage_id")
    || jsonObjectSlice(outputJson, "jobs").length > 0,
  );
  if (outputHasProgramStatus) return outputJson;
  return taskStream?.programStatus ?? outputJson ?? null;
}

function buildTaskProgram(
  argumentsJson: Record<string, unknown> | null,
  outputJson: Record<string, unknown> | null,
  taskStream: StructuredToolMessageInput["taskStream"] | undefined,
  rows: StructuredToolMessage["taskRows"],
): StructuredToolMessage["taskProgram"] {
  const argumentProgram = jsonRecord(argumentsJson?.program);
  const streamProgram = taskStream?.program ?? null;
  const definition = argumentProgram ?? streamProgram;
  const status = taskProgramRecord(outputJson, taskStream);
  const definitionProgramId = jsonStr(definition, "id");
  const programId = firstNonEmpty(
    jsonStr(status, "program_id"),
    jsonStr(outputJson, "program_id"),
    taskStream?.programId ?? "",
    definitionProgramId,
  );
  if (!programId || !definition || definitionProgramId !== programId) return null;

  const stageSpecs = jsonObjectSlice(definition, "stages");
  const jobSpecs = jsonObjectSlice(definition, "jobs");
  if (stageSpecs.length === 0 || jobSpecs.length === 0) return null;

  const statusJobs = jsonObjectSlice(status, "jobs");
  const statusByJob = new Map(statusJobs.map((job) => [jsonStr(job, "job_id"), job]));
  const rowByJob = new Map(
    rows
      .filter((row) => (!row.programId || row.programId === programId) && Boolean(row.programJobId))
      .map((row) => [row.programJobId ?? "", row]),
  );
  for (const row of rows) {
    if (row.programJobId || (row.programId && row.programId !== programId)) continue;
    const matchingJob = jobSpecs.find((job) => {
      const jobId = jsonStr(job, "id");
      const statusJob = statusByJob.get(jobId);
      return Boolean(row.childSessionId)
        && row.childSessionId === jsonStr(statusJob, "child_session_id");
    });
    const jobId = jsonStr(matchingJob, "id");
    if (jobId && !rowByJob.has(jobId)) rowByJob.set(jobId, row);
  }
  const activeStageId = firstNonEmpty(
    jsonStr(status, "active_stage_id"),
    jsonStr(outputJson, "active_stage_id"),
    taskStream?.activeStageId ?? "",
  );
  const programState = firstNonEmpty(
    jsonStr(status, "program_state"),
    jsonStr(outputJson, "program_state"),
    taskStream?.programState ?? "",
    "running",
  );

  const stages = stageSpecs.map((stage) => {
    const id = jsonStr(stage, "id");
    const stageJobs = jobSpecs.filter((job) => jsonStr(job, "stage_id") === id);
    const stageRows = stageJobs.map((job) => {
      const jobId = jsonStr(job, "id");
      const liveRow = rowByJob.get(jobId);
      const jobStatus = statusByJob.get(jobId);
      const state = firstNonEmpty(jsonStr(jobStatus, "state"), liveRow?.status ?? "", "declared");
      const base: StructuredToolMessage["taskRows"][number] = liveRow ?? {
        launchIndex: Math.max(1, jobSpecs.findIndex((candidate) => jsonStr(candidate, "id") === jobId) + 1),
        childSessionId: jsonStr(jobStatus, "child_session_id"),
        status: taskProgramJobStateToRowStatus(state),
        phase: state,
        agent: firstNonEmpty(jsonStr(job, "agent_type"), "subagent"),
        assignmentLabel: firstNonEmpty(jsonStr(job, "title"), jobId),
        modelLabel: "",
        tool: "-",
        time: "",
        previewKind: "",
        previewText: "",
        launchStartedAtMs: 0,
        currentToolStartedAtMs: 0,
        elapsedMs: 0,
        currentToolMs: 0,
        terminal: ["integrated", "completed", "failed", "cancelled"].includes(state.toLowerCase()),
      };
      return {
        ...base,
        programId,
        programJobId: jobId,
        programStageId: id,
        dependsOn: jsonStrArray(job, "depends_on"),
        status: jobStatus ? taskProgramJobStateToRowStatus(state) : liveRow?.status || taskProgramJobStateToRowStatus(state),
      };
    });
    const normalizedStates = stageRows.map((row) => row.status.trim().toLowerCase());
    const allDone = normalizedStates.length > 0 && normalizedStates.every((state) => ["done", "ok", "success", "completed", "complete"].includes(state));
    const hasFailure = normalizedStates.some((state) => state === "failed" || state === "error");
    const hasBlocked = normalizedStates.some((state) => state === "blocked");
    const stageState = hasFailure ? "failed"
      : hasBlocked ? "blocked"
      : allDone ? "done"
      : id === activeStageId ? "active"
      : jsonStrArray(stage, "depends_on").length > 0 ? "waiting"
      : "pending";
    return {
      id,
      dependsOn: jsonStrArray(stage, "depends_on"),
      dependencyEvidence: jsonStr(stage, "dependency_evidence"),
      state: stageState,
      rows: stageRows,
    } satisfies NonNullable<StructuredToolMessage["taskProgram"]>["stages"][number];
  });

  return {
    id: programId,
    state: programState,
    activeStageId,
    nextAction: firstNonEmpty(jsonStr(status, "next_action"), jsonStr(outputJson, "next_action"), taskStream?.nextAction ?? ""),
    stages,
  };
}

function pushPreviewLine(
  lines: string[],
  value: string,
  maxLines: number,
): void {
  if (lines.length >= maxLines) return;
  const next = value.trim().slice(0, MAX_PREVIEW_LINE_BYTES);
  if (!next || lines.includes(next)) return;
  lines.push(next);
}

function extractSearchToolData(
  outputJson: Record<string, unknown> | null,
  argumentsJson: Record<string, unknown> | null,
): SearchToolData | null {
  const effective = outputJson ?? argumentsJson;
  if (!effective) return null;

  const mode = jsonStr(effective, "search_mode").toLowerCase();
  const path = jsonStr(effective, "path");
  const queries = [
    ...jsonStrArray(argumentsJson, "queries"),
    jsonStr(argumentsJson, "query"),
    ...jsonStrArray(outputJson, "queries"),
    jsonStr(outputJson, "query"),
    ...jsonObjectSlice(effective, "query_results").map((result) => jsonStr(result, "query")),
  ].filter((query, index, all) => query && all.indexOf(query) === index);
  const queryCount = Math.max(
    queries.length,
    jsonNum(effective, "query_count"),
    jsonObjectSlice(effective, "query_results").length,
  );
  const count = jsonNum(effective, "count");
  const totalMatched = jsonNum(effective, "total_matched");
  const truncated =
    jsonBool(effective, "truncated") ||
    jsonBool(effective, "details_truncated") ||
    jsonBool(effective, "truncated_queries");
  const timedOut = jsonBool(effective, "timed_out");
  const files = buildSearchFileGroups(outputJson, mode);

  if (!files.length && !count && !totalMatched && !path) return null;

  return {
    mode,
    path,
    queries,
    queryCount,
    count,
    totalMatched,
    truncated,
    timedOut,
    files,
  };
}

function buildSearchFileGroups(
  outputJson: Record<string, unknown> | null,
  mode: string,
): SearchToolFileGroup[] {
  if (!outputJson) return [];
  if (mode === "files") {
    return buildSearchFileModeGroups(outputJson);
  }
  return buildSearchContentFileGroups(outputJson);
}

function buildSearchFileModeGroups(
  outputJson: Record<string, unknown> | null,
): SearchToolFileGroup[] {
  const items = [
    ...jsonObjectSlice(outputJson, "files"),
    ...jsonObjectSlice(outputJson, "results"),
  ];
  return items
    .map((item) => {
      const path = firstNonEmpty(
        jsonStr(item, "relative_path"),
        jsonStr(item, "path"),
      );
      if (!path) return null;
      const query = jsonStr(item, "query");
      const matchCount = Math.max(1, jsonNum(item, "count"));
      const queryGroups: SearchToolLineGroup[] = [
        { query, lines: [], matches: [], extraLineCount: 0 },
      ];
      return {
        path,
        matchCount,
        queryGroups,
        extraQueryCount: 0,
      } satisfies SearchToolFileGroup;
    })
    .filter((item): item is SearchToolFileGroup => Boolean(item));
}

function compactSearchContentItems(
  outputJson: Record<string, unknown> | null,
): Record<string, unknown>[] {
  const legacy = jsonObjectSlice(outputJson, "matches");
  const compact = jsonObjectSlice(outputJson, "results").flatMap((group) => {
    const path = firstNonEmpty(jsonStr(group, "relative_path"), jsonStr(group, "path"));
    return jsonObjectSlice(group, "items").map((item) => ({ ...item, path }));
  });
  return [...legacy, ...compact];
}

function buildSearchContentFileGroups(
  outputJson: Record<string, unknown> | null,
): SearchToolFileGroup[] {
  const items = compactSearchContentItems(outputJson);
  const fileMap = new Map<
    string,
    {
      path: string;
      queryOrder: string[];
      queryMap: Map<
        string,
        {
          query: string;
          lines: number[];
          matches: { line: number; column: number; text: string }[];
          seen: Set<string>;
        }
      >;
    }
  >();

  for (const item of items) {
    const path = firstNonEmpty(
      jsonStr(item, "relative_path"),
      jsonStr(item, "path"),
    );
    if (!path) continue;
    const query = jsonStr(item, "query");
    const queryKey = query.toLowerCase();
    const line = jsonNum(item, "line");
    const column = jsonNum(item, "column");

    let fileGroup = fileMap.get(path);
    if (!fileGroup) {
      fileGroup = { path, queryOrder: [], queryMap: new Map() };
      fileMap.set(path, fileGroup);
    }

    let queryGroup = fileGroup.queryMap.get(queryKey);
    if (!queryGroup) {
      queryGroup = { query, lines: [], matches: [], seen: new Set<string>() };
      fileGroup.queryMap.set(queryKey, queryGroup);
      fileGroup.queryOrder.push(queryKey);
    }

    if (!queryGroup.query && query) queryGroup.query = query;
    const text = jsonStr(item, "text");
    const matchKey = `${line}:${column}:${text}`;
    if (line > 0 && !queryGroup.seen.has(matchKey)) {
      queryGroup.seen.add(matchKey);
      if (!queryGroup.lines.includes(line)) queryGroup.lines.push(line);
      queryGroup.matches.push({ line, column, text });
    } else if (line <= 0 && text) {
      queryGroup.matches.push({ line: 0, column, text });
    }
  }

  return Array.from(fileMap.values()).map((fileGroup) => {
    const displayedQueryKeys = fileGroup.queryOrder;
    const queryGroups: SearchToolLineGroup[] = displayedQueryKeys.map(
      (queryKey) => {
        const queryGroup = fileGroup.queryMap.get(queryKey);
        const allLines = queryGroup?.lines ?? [];
        return {
          query: queryGroup?.query ?? "",
          lines: allLines,
          matches: queryGroup?.matches ?? [],
          extraLineCount: 0,
        };
      },
    );
    const matchCount = Array.from(fileGroup.queryMap.values()).reduce(
      (sum, queryGroup) => sum + Math.max(queryGroup.matches.length, queryGroup.lines.length, 1),
      0,
    );
    return {
      path: fileGroup.path,
      matchCount,
      queryGroups,
      extraQueryCount: 0,
    };
  });
}

function extractBashToolData(
  outputJson: Record<string, unknown> | null,
  outputText: string,
  argumentsJson: Record<string, unknown> | null,
): NonNullable<StructuredToolMessage["bashData"]> {
  const output = firstNonEmptyRaw(
    jsonRawStr(outputJson, "output"),
    jsonRawStr(outputJson, "stdout"),
    jsonRawStr(outputJson, "output_text"),
  );
  const stdout = jsonRawStr(outputJson, "stdout");
  const stderr = jsonRawStr(outputJson, "stderr");
  const exitCode = hasJsonKey(outputJson, "exit_code")
    ? jsonNum(outputJson, "exit_code")
    : null;
  return {
    command: jsonStr(outputJson, "command") || jsonStr(argumentsJson, "command"),
    output: output || (outputJson ? "" : outputText),
    stdout: stdout === output ? "" : stdout,
    stderr: stderr === output ? "" : stderr,
    exitCode,
  };
}

function extractPreviewLines(
  toolName: string,
  outputJson: Record<string, unknown> | null,
  outputText: string,
  argumentsJson: Record<string, unknown> | null,
): string[] {
  const tool = toolName.toLowerCase();
  if (tool === "thinking") {
    return previewTextLines(outputText);
  }
  const effective = outputJson ?? argumentsJson;
  if (!effective) return [];

  switch (tool) {
    case "compact": {
      const lines: string[] = [];
      for (const line of previewTextLines(outputText)) {
        pushPreviewLine(lines, line, 6);
      }
      return lines;
    }
    case "bash": {
      const lines: string[] = [];
      const bashData = extractBashToolData(outputJson, outputText, argumentsJson);
      for (const line of previewTextLines(bashData.output || bashData.stdout || bashData.stderr)) {
        pushPreviewLine(lines, line, 6);
      }
      return lines;
    }
    case "read":
      return [];
    case "list": {
      const entries = jsonObjectSlice(outputJson, "entries");
      if (entries.length === 0) return [];
      const visible = entries.slice(0, 12).flatMap((entry) => {
        const path = firstNonEmpty(jsonStr(entry, "path"), jsonStr(entry, "relative_path"));
        if (!path) return [];
        const type = jsonStr(entry, "type").toLowerCase();
        return [type === "dir" && !path.endsWith("/") ? `${path}/` : path];
      });
      if (entries.length > 12) visible.push(`+${entries.length - 12} more`);
      return visible;
    }
    case "grep": {
      const out: string[] = [];
      const matches = outputJson?.matches;
      if (!Array.isArray(matches) || matches.length === 0) return out;
      for (let i = 0; i < matches.length; i++) {
        const m = matches[i] as Record<string, unknown> | null;
        if (!m || typeof m !== "object") continue;
        const path = typeof m["path"] === "string" ? m["path"] : "";
        const line = typeof m["line"] === "number" ? m["line"] : 0;
        const text =
          typeof m["text"] === "string" ? (m["text"] as string).trim() : "";
        if (path && line > 0 && text) {
          out.push(`${path}:${line}: ${text}`);
        } else if (text) {
          out.push(text);
        }
      }
      return out;
    }
    case "search": {
      return [];
    }
    case "websearch": {
      const out: string[] = [];
      const queryResults = outputJson?.results;
      if (!Array.isArray(queryResults)) return out;
      const multiQuery = queryResults.length > 1;
      for (let i = 0; i < queryResults.length; i++) {
        const item = queryResults[i] as Record<string, unknown> | null;
        if (!item || typeof item !== "object") continue;
        const query =
          typeof item["query"] === "string" ? item["query"].trim() : "";
        const count = typeof item["count"] === "number" ? item["count"] : 0;
        const err =
          typeof item["error"] === "string" ? item["error"].trim() : "";
        if (multiQuery) {
          const parts: string[] = [];
          if (query) parts.push(query);
          parts.push(err ? "failed" : countLabel(count, "result", "results"));
          pushPreviewLine(out, parts.join(" · "), 6);
        }
        const hits = item["results"];
        if (!Array.isArray(hits)) continue;
        for (let j = 0; j < hits.length; j++) {
          const hit = hits[j] as Record<string, unknown> | null;
          if (!hit || typeof hit !== "object") continue;
          pushPreviewLine(out, webHitLabel(hit), 6);

        }
      }
      return out;
    }
    case "webfetch": {
      const out: string[] = [];
      const results = outputJson?.results;
      if (!Array.isArray(results)) return out;
      for (let i = 0; i < results.length; i++) {
        const item = results[i] as Record<string, unknown> | null;
        if (!item || typeof item !== "object") continue;
        pushPreviewLine(out, webHitLabel(item), 6);
        const summary =
          typeof item["summary"] === "string" ? item["summary"].trim() : "";
        if (summary) pushPreviewLine(out, summary, 6);
      }
      return out;
    }
    case "task": {
      const out: string[] = [];
      for (const row of buildTaskToolRows(effective)) {
        const status = row.status ? `[${row.status}]` : "";
        const tool = row.tool && row.tool !== "-" ? ` · ${row.tool}` : "";
        const time = row.time ? ` · ${row.time}` : "";
        const label = row.assignmentLabel || row.agent;
        const model = row.modelLabel ? ` · ${row.modelLabel}` : "";
        pushPreviewLine(out, `${label}${model}${tool}${time} ${status}`.trim(), 6);
      }
      return out;
    }
    case "manage_theme":
    case "manage-theme": {
      const out: string[] = [];
      const names = jsonStrArray(effective, "generated_names");
      const count = jsonNum(effective, "generated_count") || names.length;
      if (count > 0) pushPreviewLine(out, `Generated ${countLabel(count, "theme", "themes")}.`, 8);
      for (const name of names) pushPreviewLine(out, name, 8);
      if (out.length === 0) pushPreviewLine(out, jsonStr(effective, "summary"), 8);
      return out;
    }
    case "manage_todos": {
      return buildManageTodosPreviewLines(effective, 6);
    }
    case "plan_manage":
    case "plan-manage":
      return buildPlanManagePreviewLines(effective, 6);
    case "exit-plan-mode":
    case "exit_plan_mode":
    case "permission":
      return extractExitPlanPreviewLines(tool, outputJson, argumentsJson);
    default:
      return [];
  }
}

function buildManageTodosPreviewLines(
  payload: Record<string, unknown> | null,
  _maxLines: number,
): string[] {
  if (!payload) return [];
  const out: string[] = [];
  const action = jsonStr(payload, "action").toLowerCase();
  const summary =
    payload.summary &&
    typeof payload.summary === "object" &&
    !Array.isArray(payload.summary)
      ? (payload.summary as Record<string, unknown>)
      : null;
  if (summary && shouldShowManageTodosSummaryLines(action)) {
    for (const line of buildManageTodosSummaryLines(summary)) {
      pushPreviewLine(out, line, _maxLines);
    }
  }
  for (const item of prioritizeManageTodosPreviewItems(payload)) {
    for (const line of manageTodosItemPreviewLines(item)) {
      pushPreviewLine(out, line, _maxLines);
    }
  }
  for (const line of manageTodosStatusPreviewLines(payload)) {
    pushPreviewLine(out, line, _maxLines);
  }
  if (out.length > 0) return out;
  const emptyLine = manageTodosEmptyPreviewLine(payload);
  return emptyLine ? [emptyLine] : [];
}

function shouldShowManageTodosSummaryLines(action: string): boolean {
  return action === "summary";
}

function prioritizeManageTodosPreviewItems(
  payload: Record<string, unknown>,
): Record<string, unknown>[] {
  const items = manageTodosPreviewItems(payload);
  if (items.length <= 1 || !isAgentManageTodosPayload(payload, items)) {
    return items;
  }
  const prioritized = [...items].sort(
    (a, b) => manageTodosItemPriority(a) - manageTodosItemPriority(b),
  );
  const openItems = prioritized.filter((item) => !jsonBool(item, "done"));
  return openItems.length > 0 ? openItems : prioritized;
}

function isAgentManageTodosPayload(
  payload: Record<string, unknown>,
  items: Record<string, unknown>[],
): boolean {
  if (jsonStr(payload, "owner_kind").toLowerCase() === "agent") return true;
  return items.some((item) => jsonStr(item, "owner_kind").toLowerCase() === "agent");
}

function manageTodosItemPriority(item: Record<string, unknown>): number {
  if (!jsonBool(item, "done") && jsonBool(item, "in_progress")) return 0;
  if (!jsonBool(item, "done")) return 1;
  return 2;
}

function manageTodosPreviewItems(
  payload: Record<string, unknown>,
): Record<string, unknown>[] {
  const action = jsonStr(payload, "action").toLowerCase();
  switch (action) {
    case "batch":
      return manageTodosPreviewItemsFromResults(payload);
    case "create":
    case "update":
    case "in_progress": {
      const item =
        payload.item &&
        typeof payload.item === "object" &&
        !Array.isArray(payload.item)
          ? (payload.item as Record<string, unknown>)
          : null;
      return item ? [item] : [];
    }
    case "list":
      return manageTodosListPreviewItems(payload);
    default:
      return [];
  }
}

function manageTodosPreviewItemsFromResults(
  payload: Record<string, unknown>,
): Record<string, unknown>[] {
  const results = jsonObjectSlice(payload, "results");
  if (results.length === 0) return [];
  const items: Record<string, unknown>[] = [];
  const seen = new Set<string>();
  for (const result of results) {
    const item =
      result.item &&
      typeof result.item === "object" &&
      !Array.isArray(result.item)
        ? (result.item as Record<string, unknown>)
        : null;
    if (!item) continue;
    const key = firstNonEmpty(jsonStr(item, "id"), jsonStr(item, "text"));
    if (key && seen.has(key)) continue;
    if (key) seen.add(key);
    items.push(item);
  }
  return items;
}

function manageTodosListPreviewItems(
  payload: Record<string, unknown>,
): Record<string, unknown>[] {
  const items = jsonObjectSlice(payload, "items");
  if (items.length === 0) return [];
  const ownerKind = jsonStr(payload, "owner_kind").toLowerCase();
  const sessionId = jsonStr(payload, "session_id");
  if (ownerKind !== "agent" || !sessionId) return items;
  const filtered = items.filter((item) => jsonStr(item, "session_id") === sessionId);
  return filtered.length > 0 ? filtered : [];
}

function manageTodosStatusPreviewLines(payload: Record<string, unknown>): string[] {

  const action = jsonStr(payload, "action").toLowerCase();
  switch (action) {
    case "delete": {
      const id = jsonStr(payload, "id");
      return [id ? `Deleted ${id}.` : "Deleted todo."];
    }
    case "delete_done":
      return ["Deleted completed todos."];
    case "delete_all":
      return ["Deleted todos."];
    case "reorder":
      return ["Reordered todos."];
    case "batch":
      return manageTodosBatchStatusPreviewLines(payload);
    default:
      return [];
  }
}

function manageTodosBatchStatusPreviewLines(
  payload: Record<string, unknown>,
): string[] {
  return jsonObjectSlice(payload, "results")
    .map((result) => manageTodosBatchResultStatusLine(result))
    .filter(Boolean);
}

function manageTodosBatchResultStatusLine(
  result: Record<string, unknown>,
): string {
  switch (jsonStr(result, "action").toLowerCase()) {
    case "delete": {
      const id = jsonStr(result, "id");
      return id ? `Deleted ${id}.` : "Deleted todo.";
    }
    case "delete_done": {
      const count = manageTodosDeletedCount(result);
      return count > 0
        ? `Deleted ${count} completed ${count === 1 ? "todo" : "todos"}.`
        : "Deleted completed todos.";
    }
    case "delete_all": {
      const count = manageTodosDeletedCount(result);
      return count > 0
        ? `Deleted ${count} ${count === 1 ? "todo" : "todos"}.`
        : "Deleted todos.";
    }
    case "reorder":
      return "Reordered todos.";
    default:
      return "";
  }
}

function manageTodosDeletedCount(payload: Record<string, unknown>): number {
  const id = jsonStr(payload, "id");
  if (!id.startsWith("deleted:")) return 0;
  const count = Number.parseInt(id.slice("deleted:".length), 10);
  return Number.isFinite(count) && count > 0 ? count : 0;
}

function manageTodosEmptyPreviewLine(
  payload: Record<string, unknown>,
): string {
  if (jsonStr(payload, "action").toLowerCase() !== "list") return "";
  const ownerKind = jsonStr(payload, "owner_kind").toLowerCase();
  const sessionId = jsonStr(payload, "session_id");
  if (ownerKind === "agent" && sessionId) return "No agent todos for this session.";
  return "No todos.";
}

function manageTodosItemPreviewLines(
  item: Record<string, unknown>,
): string[] {
  const done = jsonBool(item, "done");
  const inProgress = jsonBool(item, "in_progress");
  const checkbox = done ? "[x]" : "[ ]";
  const prefix = !done && inProgress ? `> ${checkbox}` : checkbox;
  const text = firstNonEmpty(
    jsonStr(item, "text"),
    jsonStr(item, "id"),
    "Todo",
  );
  const metadata: string[] = [];
  const group = jsonStr(item, "group");
  if (group) metadata.push(group);
  const tags = jsonStrArray(item, "tags");
  if (tags.length > 0) metadata.push(`#${tags.join(" #")}`);
  let body = `${prefix} ${text}`;
  const priority = jsonStr(item, "priority");
  if (priority) body += ` · ${priority}`;
  const lines: string[] = [];
  if (metadata.length > 0) lines.push(metadata.join(" · "));
  lines.push(body);
  return lines;
}

function buildManageTodosSummaryLines(summary: Record<string, unknown>): string[] {
  const lines: string[] = [];
  const appendSummary = (
    label: string,
    value: Record<string, unknown> | null,
  ) => {
    if (!value) return;
    const total = jsonNum(value, "task_count");
    const open = jsonNum(value, "open_count");
    const inProgress = jsonNum(value, "in_progress_count");
    const parts = [`${label}: ${open} open · ${total} total`];
    if (inProgress > 0) parts.push(`${inProgress} in progress`);
    lines.push(parts.join(" · "));
  };
  appendSummary("All Todos", summary);
  appendSummary(
    "User Todos",
    summary.user &&
      typeof summary.user === "object" &&
      !Array.isArray(summary.user)
      ? (summary.user as Record<string, unknown>)
      : null,
  );
  appendSummary(
    "Agent Checklist",
    summary.agent &&
      typeof summary.agent === "object" &&
      !Array.isArray(summary.agent)
      ? (summary.agent as Record<string, unknown>)
      : null,
  );
  return lines;
}

function webHitLabel(item: Record<string, unknown>): string {
  const title = typeof item["title"] === "string" ? item["title"].trim() : "";
  const url = typeof item["url"] === "string" ? item["url"].trim() : "";
  const published =
    typeof item["published_date"] === "string"
      ? item["published_date"].trim()
      : "";
  const host = hostLabel(url);
  const headline = title || host || url;
  const parts = [headline];
  if (host && host !== headline) parts.push(host);
  if (published) parts.push(published);
  return parts.filter(Boolean).join(" · ");
}

function hostLabel(value: string): string {
  if (!value) return "";
  try {
    const url = new URL(value);
    return url.hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
}

function isExitPlanPermissionPayload(
  outputJson: Record<string, unknown> | null,
): boolean {
  if (!outputJson) return false;
  const tool = outputJson.tool;
  if (!tool || typeof tool !== "object") return false;
  const toolName = jsonStr(
    tool as Record<string, unknown>,
    "name",
  ).toLowerCase();
  return toolName === "exit_plan_mode" || toolName === "exit-plan-mode";
}

function normalizeExitPlanAction(
  ...values: Array<string | null | undefined>
): string {
  for (const raw of values) {
    const normalized = String(raw ?? "")
      .trim()
      .toLowerCase();
    switch (normalized) {
      case "approved":
      case "approve":
      case "allow":
      case "allowed":
      case "yes":
        return "approved";
      case "denied":
      case "deny":
      case "rejected":
      case "reject":
      case "no":
      case "not_in_plan_mode":
        return "denied";
      case "cancelled":
      case "canceled":
      case "cancel":
        return "cancelled";
      case "submitted":
      case "pending_review":
        return "pending review";
      case "error":
      case "failed":
      case "failure":
        return "error";
    }
  }
  return "";
}

function normalizeExitPlanFeedback(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "";
  switch (trimmed.toLowerCase()) {
    case "approved by user":
    case "approved":
    case "allow":
    case "allowed":
    case "yes":
    case "denied by user":
    case "denied":
    case "deny":
    case "rejected":
    case "reject":
    case "no":
    case "cancelled":
    case "canceled":
    case "not in plan mode":
      return "";
    default:
      return trimmed;
  }
}

function jsonStringArrayFromUnknown(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value
    .map((item) => (typeof item === "string" ? item.trim() : ""))
    .filter(Boolean);
}

function firstExitPlanRequestedModification(
  payload: Record<string, unknown> | null,
): string {
  return jsonStringArrayFromUnknown(payload?.requested_modifications)[0] || "";
}

function extractExitPlanDetails(
  toolName: string,
  outputJson: Record<string, unknown> | null,
  argumentsJson: Record<string, unknown> | null,
): {
  action: string;
  title: string;
  planId: string;
  targetMode: string;
  approvalState: string;
  feedback: string;
  requestedModification: string;
} | null {
  if (!outputJson && !argumentsJson) return null;

  let action = normalizeExitPlanAction(
    jsonStr(outputJson, "status"),
    jsonStr(outputJson, "approval_state"),
  );
  let title = jsonStr(outputJson, "title") || jsonStr(argumentsJson, "title");
  let planId =
    jsonStr(outputJson, "plan_id") ||
    jsonStr(outputJson, "planID") ||
    jsonStr(argumentsJson, "plan_id") ||
    jsonStr(argumentsJson, "planID");
  let targetMode = jsonStr(outputJson, "target_mode");
  let approvalState = jsonStr(outputJson, "approval_state")
    .trim()
    .toLowerCase();
  let feedback = normalizeExitPlanFeedback(jsonStr(outputJson, "user_message"));
  let requestedModification = firstExitPlanRequestedModification(outputJson);

  if (
    (toolName === "permission" || !action) &&
    isExitPlanPermissionPayload(outputJson)
  ) {
    const permission =
      outputJson?.permission && typeof outputJson.permission === "object"
        ? (outputJson.permission as Record<string, unknown>)
        : null;
    const tool =
      outputJson?.tool && typeof outputJson.tool === "object"
        ? (outputJson.tool as Record<string, unknown>)
        : null;
    const permissionAction = normalizeExitPlanAction(
      jsonStr(permission, "status"),
      jsonStr(permission, "decision"),
    );
    if (!action) action = permissionAction;
    if (!approvalState)
      approvalState = jsonStr(permission, "status").trim().toLowerCase();
    if (!feedback)
      feedback = normalizeExitPlanFeedback(jsonStr(permission, "reason"));
    const nestedArgs = parseJsonRecord(jsonStr(tool, "arguments"));
    if (!title) title = jsonStr(nestedArgs, "title");
    if (!planId) {
      planId = jsonStr(nestedArgs, "plan_id") || jsonStr(nestedArgs, "planID");
    }
  }

  if (
    !action &&
    !title &&
    !planId &&
    !targetMode &&
    !approvalState &&
    !feedback &&
    !requestedModification
  ) {
    return null;
  }

  return {
    action,
    title,
    planId,
    targetMode,
    approvalState,
    feedback,
    requestedModification,
  };
}

function summarizeExitPlanToolOutput(
  toolName: string,
  outputJson: Record<string, unknown> | null,
  argumentsJson: Record<string, unknown> | null,
): string {
  const details = extractExitPlanDetails(toolName, outputJson, argumentsJson);
  if (!details) return "";
  const action = details.action || "updated";
  if (details.title) return `plan ${action} · ${details.title}`;
  return `plan ${action}`;
}

function extractExitPlanPreviewLines(
  toolName: string,
  outputJson: Record<string, unknown> | null,
  argumentsJson: Record<string, unknown> | null,
): string[] {
  const details = extractExitPlanDetails(toolName, outputJson, argumentsJson);
  if (!details) return [];
  const lines: string[] = [];
  pushPreviewLine(lines, `action: ${details.action || "updated"}`, 5);
  if (details.title) pushPreviewLine(lines, `title: ${details.title}`, 5);
  if (details.planId) pushPreviewLine(lines, `plan: ${details.planId}`, 5);
  if (details.targetMode)
    pushPreviewLine(lines, `next mode: ${details.targetMode}`, 5);
  if (details.feedback)
    pushPreviewLine(lines, `feedback: ${details.feedback}`, 5);
  if (details.requestedModification) {
    pushPreviewLine(lines, `requested: ${details.requestedModification}`, 5);
  }
  return lines;
}

export function buildStructuredToolMessage(
  input: StructuredToolMessageInput,
): StructuredToolMessage | null {
  const toolName = String(input.tool ?? "").trim();
  if (!toolName) {
    return null;
  }

  const argumentsText = String(input.argumentsText ?? "").trim();
  const argumentsJson = argumentsText ? parseJsonRecord(argumentsText) : null;
  const outputText = String(input.outputText ?? "").trim();
  const completedOutputText = String(input.completedOutputText ?? "").trim();
  const normalizedToolName = toolName.toLowerCase();
  const outputParseLimit = normalizedToolName === "bash"
    ? Number.POSITIVE_INFINITY
    : MAX_STRUCTURED_OUTPUT_PARSE_BYTES;
  const parsedOutputJson = parseJsonRecord(outputText, outputParseLimit);
  const parsedCompletedOutputJson = parseJsonRecord(completedOutputText, outputParseLimit);
  const outputJson =
    parsedOutputJson && jsonBool(parsedOutputJson, "result_details_omitted") && parsedCompletedOutputJson
      ? parsedCompletedOutputJson
      : parsedOutputJson ?? parsedCompletedOutputJson;

  const summary = summarizeToolOutput(toolName, outputJson, argumentsJson);
  const editDiff =
    normalizedToolName === "edit" ? extractEditDiff(outputJson) : null;
  const searchData =
    normalizedToolName === "search"
      ? extractSearchToolData(outputJson, argumentsJson)
      : null;
  const webSearchData = normalizedToolName === "websearch"
    ? extractWebSearchToolData(outputJson, argumentsJson)
    : null;
  const webFetchData = normalizedToolName === "webfetch"
    ? extractWebFetchToolData(outputJson, argumentsJson)
    : null;
  const todoData =
    normalizedToolName === "manage_todos" || normalizedToolName === "manage-todos"
      ? extractTodoToolData(outputJson ?? argumentsJson)
      : null;
  const bashData =
    normalizedToolName === "bash"
      ? extractBashToolData(outputJson, outputText || completedOutputText, argumentsJson)
      : null;
  const previewLines = searchData || webSearchData || webFetchData
    ? []
    : extractPreviewLines(
        toolName,
        outputJson,
        outputText || completedOutputText,
        argumentsJson,
      );
  const taskRows =
    toolName.toLowerCase() === "task"
      ? buildTaskToolRows(outputJson, input.taskStream)
      : [];
  const taskMode = firstNonEmpty(jsonStr(outputJson, "task_mode"), input.taskStream?.taskMode ?? "", jsonStr(argumentsJson, "mode"));
  const taskProgram = normalizedToolName === "task" && taskMode !== "swarm"
    ? buildTaskProgram(argumentsJson, outputJson, input.taskStream, taskRows)
    : null;
  const isSwarm = taskMode === "swarm" || taskRows.some((row) => row.swarmMode);
  const swarmStrategy = normalizeSwarmStrategy(
    jsonStr(outputJson, "swarm_strategy"),
    input.taskStream?.swarmStrategy ?? "",
    jsonStr(argumentsJson, "swarm_strategy"),
    ...taskRows.map((row) => row.swarmStrategy ?? ""),
  ) ?? (isSwarm ? "explore" : undefined);
  const integrationContract = firstNonEmpty(
    jsonStr(outputJson, "integration_contract"),
    input.taskStream?.integrationContract ?? "",
    jsonStr(argumentsJson, "integration_contract"),
    ...taskRows.map((row) => row.integrationContract ?? ""),
  );
  const integrationRequired = jsonBool(outputJson, "integration_required")
    || input.taskStream?.integrationRequired === true
    || taskRows.some((row) => row.integrationRequired);
  const error = String(input.error ?? "").trim();

  const retainOutputJson = [
    "manage-sessions",
    "manage_sessions",
    "plan-manage",
    "plan_manage",
    "exit-plan-mode",
    "exit_plan_mode",
  ].includes(normalizedToolName);
  const outputWasStructured = outputJson !== null;

  return {
    pathId: input.pathId ?? "run.tool-history.v2",
    tool: toolName,
    callId: String(input.callId ?? "").trim(),
    runId: String(input.runId ?? "").trim(),
    toolInstanceId: String(input.toolInstanceId ?? "").trim(),
    target: resolveToolTarget(argumentsJson) ?? resolveToolTarget(outputJson),
    commandText: bashData?.command ?? "",
    argumentsText,
    argumentsJson,
    outputJson: retainOutputJson ? outputJson : null,
    output: normalizedToolName === "bash" || (outputWasStructured && !retainOutputJson) ? "" : outputText,
    completedOutput: normalizedToolName === "bash"
      || completedOutputText === outputText
      || (parsedCompletedOutputJson !== null && !retainOutputJson)
      ? ""
      : completedOutputText,
    error,
    durationMs: typeof input.durationMs === "number" ? input.durationMs : 0,
    summary,
    state: input.state ?? (error ? "error" : "done"),
    lifecycleStatus: String(input.lifecycleStatus ?? "").trim(),
    editDiff,
    searchData,
    webSearchData,
    webFetchData,
    todoData,
    bashData,
    previewLines,
    taskRows,
    taskProgram,
    taskMode,
    swarmStrategy,
    integrationContract,
    integrationRequired,
    integrationStatus: jsonStr(outputJson, "integration_status"),
    readyForDependentWork: jsonBool(outputJson, "ready_for_dependent_work"),
  };
}

export function parseStructuredToolMessage(
  content: string,
): StructuredToolMessage | null {
  const payload = parseJsonRecord(content) as ToolHistoryPayload | null;
  if (!payload) {
    return null;
  }
  const pathId = String(payload.path_id ?? "").trim();
  if (
    pathId !== "run.tool-history.v2" &&
    pathId !== "run.v3.provider-tool-result.v1"
  ) {
    return null;
  }

  return buildStructuredToolMessage({
    pathId,
    tool: String(payload.tool ?? payload.tool_name ?? "").trim(),
    callId: String(payload.call_id ?? "").trim(),
    runId: String(payload.run_id ?? "").trim(),
    toolInstanceId: String(payload.tool_instance_id ?? "").trim(),
    argumentsText: String(payload.arguments ?? "").trim(),
    outputText: String(payload.output ?? "").trim(),
    completedOutputText: String(payload.completed_output ?? "").trim(),
    error: String(payload.error ?? "").trim(),
    durationMs:
      typeof payload.duration_ms === "number" ? payload.duration_ms : 0,
  });
}
