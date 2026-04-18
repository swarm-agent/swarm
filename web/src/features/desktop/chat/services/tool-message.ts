import type {
  EditDiffPreview,
  SearchToolData,
  SearchToolFileGroup,
  SearchToolLineGroup,
  StructuredToolMessage,
} from "../types/chat";

interface ToolHistoryPayload {
  path_id?: string;
  tool?: string;
  call_id?: string;
  arguments?: string;
  output?: string;
  completed_output?: string;
  error?: string;
  duration_ms?: number;
}

interface StructuredToolMessageInput {
  tool: string;
  callId?: string;
  argumentsText?: string;
  outputText?: string;
  completedOutputText?: string;
  error?: string;
  durationMs?: number;
  state?: StructuredToolMessage["state"];
}

function parseJsonRecord(value: string): Record<string, unknown> | null {
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

function jsonStr(obj: Record<string, unknown> | null, key: string): string {
  if (!obj) return "";
  const v = obj[key];
  return typeof v === "string" ? v.trim() : "";
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

function firstNonEmpty(...values: string[]): string {
  for (const value of values) {
    const trimmed = value.trim();
    if (trimmed) return trimmed;
  }
  return "";
}

function clamp(text: string, max: number): string {
  if (text.length <= max) return text;
  return text.slice(0, max - 1) + "…";
}

function resolveToolTarget(
  argumentsJson: Record<string, unknown> | null,
): string | null {
  if (!argumentsJson) {
    return null;
  }
  for (const key of ["path", "url", "command", "session_id", "cwd"]) {
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

  switch (tool) {
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
      const command =
        jsonStr(outputJson, "command") || jsonStr(argumentsJson, "command");
      const exitCode = hasJsonKey(outputJson, "exit_code")
        ? jsonNum(outputJson, "exit_code")
        : null;
      const timedOut = jsonBool(outputJson, "timed_out");
      const truncated = jsonBool(outputJson, "truncated");
      const binarySuppressed = jsonBool(outputJson, "binary_suppressed");
      let s = "bash";
      if (command) s += " " + clamp(command, 80);
      const notes: string[] = [];
      if (timedOut) notes.push("timed out");
      else if (typeof exitCode === "number" && exitCode !== 0)
        notes.push("failed");
      if (truncated) notes.push("partial output");
      if (binarySuppressed) notes.push("binary output hidden");
      return summaryWithNotes(s, notes);
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
      if (url) return summaryWithNotes(`webfetch ${clamp(url, 80)}`, notes);
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
      if (description) parts.push(description);
      if (agentType) parts.push("@" + agentType);
      if (launchCount > 1) parts.push(`(${launchCount} launches)`);
      if (status) parts.push("(" + status + ")");
      return parts.length ? "task " + parts.join(" ") : "task";
    }
    case "manage_todos": {
      const action = jsonStr(effective, "action");
      const ownerKind = jsonStr(effective, "owner_kind");
      const ownerSuffix = ownerKind ? ` [${ownerKind}]` : "";
      let summary = `manage_todos${ownerSuffix}`;
      if (action) summary += ` ${action}`;
      const notes: string[] = [];
      const summaryPayload =
        effective.summary &&
        typeof effective.summary === "object" &&
        !Array.isArray(effective.summary)
          ? (effective.summary as Record<string, unknown>)
          : null;
      const openCount = summaryPayload
        ? jsonNum(summaryPayload, "open_count")
        : 0;
      const taskCount = summaryPayload
        ? jsonNum(summaryPayload, "task_count")
        : 0;
      const inProgressCount = summaryPayload
        ? jsonNum(summaryPayload, "in_progress_count")
        : 0;
      if (openCount > 0 || taskCount > 0 || inProgressCount > 0) {
        notes.push(`${openCount} open · ${taskCount} total`);
        if (inProgressCount > 0) notes.push(`${inProgressCount} in progress`);
      }
      if (action === "batch") {
        const count = Math.max(
          jsonObjectSlice(effective, "operations").length,
          jsonNum(effective, "operation_count"),
        );
        if (count > 0) notes.unshift(`${count} ops`);
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
      if (text) return `${summary} · ${[clamp(text, 80), ...notes].join(" · ")}`;
      if (id) return `${summary} · ${[id, ...notes].join(" · ")}`;
      if (notes.length) return `${summary} (${notes.join(", ")})`;
      return summary;
    }
    case "ask-user":
    case "ask_user": {
      const question = jsonStr(effective, "question");
      if (question) return "ask-user " + clamp(question, 80);
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
      if (title) return "exit-plan-mode: " + clamp(title, 80);
      return tool === "permission" ? "permission" : "exit-plan-mode";
    }
    default:
      return toolName || "tool";
  }
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

function quotedSummary(value: string, max: number): string {
  return JSON.stringify(clamp(value, max));
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

function extractEditDiff(
  outputJson: Record<string, unknown> | null,
): EditDiffPreview | null {
  if (!outputJson) return null;
  const oldPreviewRaw = jsonStr(outputJson, "old_string_preview");
  const newPreviewRaw = jsonStr(outputJson, "new_string_preview");
  if (!oldPreviewRaw && !newPreviewRaw) return null;
  const oldTruncated = jsonBool(outputJson, "old_string_truncated");
  const newTruncated = jsonBool(outputJson, "new_string_truncated");
  const oldLines = expandEditPreviewLines(
    oldPreviewRaw || "(empty)",
    oldTruncated,
  );
  const newLines = expandEditPreviewLines(
    newPreviewRaw || "(empty)",
    newTruncated,
  );
  return { oldLines, newLines, oldTruncated, newTruncated };
}

function previewTextLines(value: string, maxLines: number): string[] {
  if (!value) return [];
  return value
    .replace(/\r\n/g, "\n")
    .replace(/\r/g, "\n")
    .split("\n")
    .map((line) => line.trimEnd())
    .filter((line) => line.trim() !== "")
    .slice(0, maxLines)
    .map((line) => clamp(line, 200));
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

function buildTaskToolRow(
  payload: Record<string, unknown> | null,
  fallbackLaunchIndex = 0,
): StructuredToolMessage["taskRows"][number] | null {
  if (!payload) return null;
  const status = jsonStr(payload, "status") || "pending";
  const launchIndex = Math.max(0, jsonNum(payload, "launch_index") || fallbackLaunchIndex);
  const agent = firstNonEmpty(
    jsonStr(payload, "resolved_agent_name"),
    jsonStr(payload, "requested_subagent_type"),
    jsonStr(payload, "agent_type"),
    jsonStr(payload, "subagent"),
    jsonStr(payload, "requested_subagent"),
    "subagent",
  );
  const rawPreviewKind = jsonStr(payload, "current_preview_kind");
  let tool = jsonStr(payload, "current_tool");
  if (!tool && rawPreviewKind.trim().toLowerCase() !== "reasoning") {
    const toolOrder = jsonStrArray(payload, "tool_order");
    tool = toolOrder[toolOrder.length - 1] || "-";
  }
  const currentToolMs = jsonNum(payload, "current_tool_ms");
  const elapsedMs = jsonNum(payload, "elapsed_ms");
  const time = formatDurationCompact(currentToolMs || elapsedMs);
  const previewText = taskPreviewText(payload);
  const normalized = normalizeTaskToolDisplay(tool, rawPreviewKind, previewText);
  const launchStartedAtMs = jsonNum(payload, "launch_started_at_ms");
  const currentToolStartedAtMs = jsonNum(payload, "current_tool_started_at_ms");
  if (!agent && normalized.tool === "-" && !time && !status && !normalized.previewText) return null;
  return {
    launchIndex,
    status,
    agent,
    tool: normalized.tool || "-",
    time,
    previewKind: normalized.previewKind,
    previewText: normalized.previewText,
    launchStartedAtMs,
    currentToolStartedAtMs,
    elapsedMs,
    currentToolMs,
  };
}

function buildTaskToolRows(
  payload: Record<string, unknown> | null,
): StructuredToolMessage["taskRows"] {
  if (!payload) return [];

  const launches = jsonObjectSlice(payload, "launches");
  if (launches.length > 0) {
    return launches
      .map((launch, index) => buildTaskToolRow(launch, index + 1))
      .filter((row): row is StructuredToolMessage["taskRows"][number] => Boolean(row));
  }

  const row = buildTaskToolRow(payload, 1);
  return row ? [row] : [];
}

function pushPreviewLine(
  lines: string[],
  value: string,
  maxLines: number,
): void {
  const trimmed = value.trim();
  if (!trimmed || lines.length >= maxLines) {
    return;
  }
  const next = clamp(trimmed, 200);
  if (lines.includes(next)) {
    return;
  }
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
  const queryCount = jsonNum(effective, "query_count");
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
  const items = jsonObjectSlice(outputJson, "files");
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
        { query, lines: [], extraLineCount: 0 },
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

function buildSearchContentFileGroups(
  outputJson: Record<string, unknown> | null,
): SearchToolFileGroup[] {
  const items = jsonObjectSlice(outputJson, "matches");
  const fileMap = new Map<
    string,
    {
      path: string;
      queryOrder: string[];
      queryMap: Map<
        string,
        { query: string; lines: number[]; seen: Set<number> }
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

    let fileGroup = fileMap.get(path);
    if (!fileGroup) {
      fileGroup = { path, queryOrder: [], queryMap: new Map() };
      fileMap.set(path, fileGroup);
    }

    let queryGroup = fileGroup.queryMap.get(queryKey);
    if (!queryGroup) {
      queryGroup = { query, lines: [], seen: new Set<number>() };
      fileGroup.queryMap.set(queryKey, queryGroup);
      fileGroup.queryOrder.push(queryKey);
    }

    if (!queryGroup.query && query) queryGroup.query = query;
    if (line > 0 && !queryGroup.seen.has(line)) {
      queryGroup.seen.add(line);
      queryGroup.lines.push(line);
    }
  }

  return Array.from(fileMap.values()).map((fileGroup) => {
    const displayedQueryKeys = fileGroup.queryOrder.slice(0, 3);
    const queryGroups: SearchToolLineGroup[] = displayedQueryKeys.map(
      (queryKey) => {
        const queryGroup = fileGroup.queryMap.get(queryKey);
        const allLines = queryGroup?.lines ?? [];
        return {
          query: queryGroup?.query ?? "",
          lines: allLines.slice(0, 6),
          extraLineCount: Math.max(0, allLines.length - 6),
        };
      },
    );
    const matchCount = Array.from(fileGroup.queryMap.values()).reduce(
      (sum, queryGroup) => sum + Math.max(queryGroup.lines.length, 1),
      0,
    );
    return {
      path: fileGroup.path,
      matchCount,
      queryGroups,
      extraQueryCount: Math.max(
        0,
        fileGroup.queryOrder.length - displayedQueryKeys.length,
      ),
    };
  });
}

function extractPreviewLines(
  toolName: string,
  outputJson: Record<string, unknown> | null,
  outputText: string,
  argumentsJson: Record<string, unknown> | null,
): string[] {
  const tool = toolName.toLowerCase();
  const effective = outputJson ?? argumentsJson;
  if (!effective) return [];

  switch (tool) {
    case "bash": {
      const lines: string[] = [];
      const command =
        jsonStr(outputJson, "command") || jsonStr(argumentsJson, "command");
      if (command) {
        pushPreviewLine(lines, `$ ${command}`, 6);
      }
      const stdout =
        jsonStr(outputJson, "output") ||
        jsonStr(outputJson, "stdout") ||
        jsonStr(outputJson, "output_text") ||
        (outputJson ? "" : outputText);
      for (const line of previewTextLines(stdout, 5)) {
        pushPreviewLine(lines, line, 6);
      }
      return lines;
    }
    case "read":
      return [];
    case "grep": {
      const out: string[] = [];
      const matches = outputJson?.matches;
      if (!Array.isArray(matches) || matches.length === 0) return out;
      for (let i = 0; i < matches.length && out.length < 6; i++) {
        const m = matches[i] as Record<string, unknown> | null;
        if (!m || typeof m !== "object") continue;
        const path = typeof m["path"] === "string" ? m["path"] : "";
        const line = typeof m["line"] === "number" ? m["line"] : 0;
        const text =
          typeof m["text"] === "string" ? (m["text"] as string).trim() : "";
        if (path && line > 0 && text) {
          out.push(clamp(`${path}:${line}: ${text}`, 200));
        } else if (text) {
          out.push(clamp(text, 200));
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
      for (let i = 0; i < queryResults.length && out.length < 6; i++) {
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
        for (let j = 0; j < hits.length && out.length < 6; j++) {
          const hit = hits[j] as Record<string, unknown> | null;
          if (!hit || typeof hit !== "object") continue;
          pushPreviewLine(out, webHitLabel(hit), 6);
          if (!multiQuery && out.length >= 3) break;
        }
      }
      return out;
    }
    case "webfetch": {
      const out: string[] = [];
      const results = outputJson?.results;
      if (!Array.isArray(results)) return out;
      for (let i = 0; i < results.length && out.length < 6; i++) {
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
      for (const row of buildTaskToolRows(effective).slice(0, 6)) {
        const status = row.status ? `[${row.status}]` : "";
        const tool = row.tool && row.tool !== "-" ? ` · ${row.tool}` : "";
        const time = row.time ? ` · ${row.time}` : "";
        pushPreviewLine(out, `${row.agent}${tool}${time} ${status}`.trim(), 6);
      }
      return out;
    }
    case "manage_todos": {
      const out: string[] = [];
      for (const line of buildManageTodosPreviewLines(effective, 6)) {
        pushPreviewLine(out, line, 6);
      }
      return out;
    }
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
  maxLines: number,
): string[] {
  if (!payload || maxLines <= 0) return [];
  const out: string[] = [];
  const action = jsonStr(payload, "action").toLowerCase();
  const summary =
    payload.summary &&
    typeof payload.summary === "object" &&
    !Array.isArray(payload.summary)
      ? (payload.summary as Record<string, unknown>)
      : null;
  if (summary && shouldShowManageTodosSummaryLines(action)) {
    for (const line of buildManageTodosSummaryLines(summary, maxLines - out.length)) {
      pushPreviewLine(out, line, maxLines);
      if (out.length >= maxLines) return out;
    }
  }
  for (const item of prioritizeManageTodosPreviewItems(payload)) {
    for (const line of manageTodosItemPreviewLines(item)) {
      pushPreviewLine(out, line, maxLines);
      if (out.length >= maxLines) return out;
    }
  }
  for (const line of manageTodosStatusPreviewLines(payload, maxLines - out.length)) {
    pushPreviewLine(out, line, maxLines);
    if (out.length >= maxLines) return out;
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
  return manageTodosPreviewItems(payload);
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

function manageTodosStatusPreviewLines(
  payload: Record<string, unknown>,
  maxLines: number,
): string[] {
  if (maxLines <= 0) return [];
  const action = jsonStr(payload, "action").toLowerCase();
  switch (action) {
    case "delete": {
      const id = jsonStr(payload, "id");
      return [id ? `Deleted ${id}.` : "Deleted todo."].slice(0, maxLines);
    }
    case "delete_done":
      return ["Deleted completed todos."].slice(0, maxLines);
    case "delete_all":
      return ["Deleted todos."].slice(0, maxLines);
    case "reorder":
      return ["Reordered todos."].slice(0, maxLines);
    case "batch":
      return manageTodosBatchStatusPreviewLines(payload).slice(0, maxLines);
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
  if (metadata.length > 0) lines.push(clamp(metadata.join(" · "), 200));
  lines.push(clamp(body, 200));
  return lines;
}

function buildManageTodosSummaryLines(
  summary: Record<string, unknown>,
  maxLines: number,
): string[] {
  const lines: string[] = [];
  const appendSummary = (
    label: string,
    value: Record<string, unknown> | null,
  ) => {
    if (!value || lines.length >= maxLines) return;
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
  return lines.slice(0, maxLines).map((line) => clamp(line, 200));
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
  if (published) parts.push(published.slice(0, 10));
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
  if (details.title)
    return `exit-plan-mode ${action} · ${clamp(details.title, 80)}`;
  return `exit-plan-mode ${action}`;
}

function extractExitPlanPreviewLines(
  toolName: string,
  outputJson: Record<string, unknown> | null,
  argumentsJson: Record<string, unknown> | null,
): string[] {
  const details = extractExitPlanDetails(toolName, outputJson, argumentsJson);
  if (!details) return [];
  const lines: string[] = [];
  pushPreviewLine(lines, `status: ${details.action || "updated"}`, 5);
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
  const outputJson =
    parseJsonRecord(outputText) ?? parseJsonRecord(completedOutputText);

  const summary = summarizeToolOutput(toolName, outputJson, argumentsJson);
  const editDiff =
    toolName.toLowerCase() === "edit" ? extractEditDiff(outputJson) : null;
  const searchData =
    toolName.toLowerCase() === "search"
      ? extractSearchToolData(outputJson, argumentsJson)
      : null;
  const previewLines = searchData
    ? []
    : extractPreviewLines(
        toolName,
        outputJson,
        outputText || completedOutputText,
        argumentsJson,
      );
  const taskRows =
    toolName.toLowerCase() === "task"
      ? buildTaskToolRows(outputJson ?? argumentsJson)
      : [];
  const error = String(input.error ?? "").trim();

  return {
    pathId: "run.tool-history.v2",
    tool: toolName,
    callId: String(input.callId ?? "").trim(),
    target: resolveToolTarget(argumentsJson),
    argumentsText,
    argumentsJson,
    output: outputText,
    completedOutput: completedOutputText || outputText,
    error,
    durationMs: typeof input.durationMs === "number" ? input.durationMs : 0,
    summary,
    state: input.state ?? (error ? "error" : "done"),
    editDiff,
    searchData,
    previewLines,
    taskRows,
  };
}

export function parseStructuredToolMessage(
  content: string,
): StructuredToolMessage | null {
  const payload = parseJsonRecord(content) as ToolHistoryPayload | null;
  if (
    !payload ||
    String(payload.path_id ?? "").trim() !== "run.tool-history.v2"
  ) {
    return null;
  }

  return buildStructuredToolMessage({
    tool: String(payload.tool ?? "").trim(),
    callId: String(payload.call_id ?? "").trim(),
    argumentsText: String(payload.arguments ?? "").trim(),
    outputText: String(payload.output ?? "").trim(),
    completedOutputText: String(payload.completed_output ?? "").trim(),
    error: String(payload.error ?? "").trim(),
    durationMs:
      typeof payload.duration_ms === "number" ? payload.duration_ms : 0,
  });
}
