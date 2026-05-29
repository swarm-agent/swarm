import { memo, useEffect, useMemo, useState } from "react";
import { ArrowRight, CheckCircle2, XCircle, LoaderCircle } from "lucide-react";
import { cn } from "../../../../lib/cn";
import { MarkdownRenderer } from "../markdown/render";
import type {
  StructuredToolMessage,
  SearchToolFileGroup,
  SearchToolLineGroup,
  TaskToolRow,
} from "../types/chat";
import { getToolTheme, type ToolState } from "../services/tool-theme";
import { ToolSyntaxLine, inferToolSyntaxLanguage, pathFromToolSummary } from "../services/tool-syntax";

interface ChatMarkdownProps {
  content: string;
  className?: string;
  toolMessage?: StructuredToolMessage | null;
}

function resolveToolState(toolMessage: StructuredToolMessage): ToolState {
  return toolMessage.state;
}

function toolAccentWash(color: string, amount = 12): string {
  return `color-mix(in srgb, ${color} ${amount}%, transparent)`;
}

function toolSummaryRemainder(summary: string, label: string): string {
  const trimmed = summary.trim();
  const normalizedLabel = label.trim().toLowerCase();
  if (!trimmed || !normalizedLabel) return trimmed;
  if (trimmed.toLowerCase() === normalizedLabel) return "";
  if (trimmed.toLowerCase().startsWith(normalizedLabel + " ")) {
    return trimmed.slice(label.length).trim();
  }
  return trimmed;
}

function formatTodoCounts(summary: NonNullable<StructuredToolMessage["todoData"]>["summary"]): string {
  if (!summary) return "";
  return `${summary.openCount} open · ${summary.taskCount} total · ${summary.inProgressCount} in progress`;
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

function EditDiffView({ toolMessage }: { toolMessage: StructuredToolMessage }) {
  const diff = toolMessage.editDiff;
  if (!diff) return null;
  const language = inferToolSyntaxLanguage(pathFromToolSummary(toolMessage.summary));
  const hunks = diff.hunks.length > 0
    ? diff.hunks
    : [{ index: 1, oldLines: diff.oldLines, newLines: diff.newLines, oldTruncated: diff.oldTruncated, newTruncated: diff.newTruncated }];
  const showHunkLabels = hunks.length > 1;
  const removedCount = hunks.reduce((sum, hunk) => sum + hunk.oldLines.length, 0);
  const addedCount = hunks.reduce((sum, hunk) => sum + hunk.newLines.length, 0);

  return (
    <div className="mt-2 space-y-2 font-mono text-[12px] leading-5">
      <div className="flex items-center gap-2 font-sans text-[11px] text-[var(--app-text-subtle)]">
        <span className="rounded-md bg-[color-mix(in_srgb,var(--app-danger)_12%,transparent)] px-1.5 py-0.5 font-semibold text-[var(--app-danger)]">
          -{removedCount}
        </span>
        <span className="rounded-md bg-[color-mix(in_srgb,var(--app-success)_12%,transparent)] px-1.5 py-0.5 font-semibold text-[var(--app-success)]">
          +{addedCount}
        </span>
      </div>
      {hunks.map((hunk, hunkIndex) => (
        <div key={`hunk-${hunk.index}-${hunkIndex}`} className="overflow-hidden rounded-md">
          {showHunkLabels ? (
            <div className="font-sans text-[11px] uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
              edit {hunk.index}
            </div>
          ) : null}
          {hunk.oldLines.map((line, i) => (
            <div
              key={`old-${hunk.index}-${i}`}
              className="whitespace-pre-wrap break-all bg-[color-mix(in_srgb,var(--app-danger)_8%,transparent)] px-2 text-[var(--app-danger)]"
            >
              <span className="mr-2 select-none opacity-70">-</span><ToolSyntaxLine text={line} language={language} />
            </div>
          ))}
          {hunk.newLines.map((line, i) => (
            <div
              key={`new-${hunk.index}-${i}`}
              className="whitespace-pre-wrap break-all bg-[color-mix(in_srgb,var(--app-success)_8%,transparent)] px-2 text-[var(--app-success)]"
            >
              <span className="mr-2 select-none opacity-70">+</span><ToolSyntaxLine text={line} language={language} />
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

const PREVIEW_LIMIT = 8;
const TASK_SWARM_THRESHOLD = 10;
const TASK_SWARM_TITLE_MAX = 72;
const TASK_SWARM_AGENT_MAX = 30;
const TASK_SWARM_TOOL_MAX = 34;

function truncateMiddle(text: string, maxLength: number): string {
  const trimmed = text.trim();
  if (trimmed.length <= maxLength) return trimmed;
  if (maxLength <= 1) return "…";
  return `${trimmed.slice(0, maxLength - 1)}…`;
}

function PreviewLinesView({
  lines,
  compact = true,
  commandText = "",
  language = "",
  shell = false,
  plain = false,
}: {
  lines: string[];
  compact?: boolean;
  commandText?: string;
  language?: string;
  shell?: boolean;
  plain?: boolean;
}) {
  if (lines.length === 0 && !commandText) return null;

  const isLarge = lines.length > PREVIEW_LIMIT;
  const [expanded, setExpanded] = useState(false);
  const display = isLarge && !expanded ? lines.slice(0, PREVIEW_LIMIT) : lines;

  return (
    <div className={compact
      ? "mt-1 min-w-0 py-0.5 font-mono text-[11px] leading-[18px] text-[var(--app-text-muted)]"
      : "mt-2 min-w-0 space-y-1.5"}
    >
      {commandText ? (
        <div className="mb-1.5 min-w-0 rounded-md bg-[var(--app-surface)] px-2 py-1 text-[11px] leading-5 text-[var(--app-text)]">
          <span className="mr-1 select-none text-[var(--app-accent)]">$</span>
          <ToolSyntaxLine text={commandText} language="bash" className="whitespace-pre-wrap break-words font-mono [overflow-wrap:anywhere]" />
        </div>
      ) : null}
      {display.map((line, i) => (
        <div
          key={i}
          className={compact
            ? "whitespace-pre-wrap break-words rounded-sm px-1.5 py-0.5 [overflow-wrap:anywhere] odd:bg-[color-mix(in_srgb,var(--app-text-muted)_6%,transparent)]"
            : "whitespace-pre-wrap break-words rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-1.5 text-[12px] leading-5 text-[var(--app-text)] [overflow-wrap:anywhere]"}
        >
          <ToolSyntaxLine text={line} language={language} shell={shell} plain={plain} />
        </div>
      ))}
      {isLarge ? (
        <button
          onClick={() => setExpanded(!expanded)}
          className="mt-1 text-[11px] text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:underline block"
        >
          {expanded
            ? "collapse"
            : `... show ${lines.length - PREVIEW_LIMIT} more lines`}
        </button>
      ) : null}
    </div>
  );
}

function taskStatusLabel(row: TaskToolRow): string {
  const status = row.status.trim().toLowerCase();
  switch (status) {
    case "done":
    case "ok":
    case "success":
    case "completed":
    case "complete":
      return "OK";
    case "error":
    case "failed":
      return "ERR";
    case "running":
    case "active":
    case "in_progress":
      return "RUN";
    case "pending":
    case "":
      return "WAIT";
    default:
      return status.slice(0, 4).toUpperCase();
  }
}

function taskStatusKind(row: TaskToolRow): "success" | "error" | "running" | "pending" | "other" {
  const status = row.status.trim().toLowerCase();
  switch (status) {
    case "done":
    case "ok":
    case "success":
    case "completed":
    case "complete":
      return "success";
    case "error":
    case "failed":
      return "error";
    case "running":
    case "active":
    case "in_progress":
      return "running";
    case "pending":
    case "":
      return "pending";
    default:
      return "other";
  }
}

function taskStatusText(kind: ReturnType<typeof taskStatusKind>): string {
  switch (kind) {
    case "success":
      return "done";
    case "error":
      return "error";
    case "running":
      return "running";
    case "pending":
      return "queued";
    default:
      return "active";
  }
}

function taskStatusTextClass(kind: ReturnType<typeof taskStatusKind>): string {
  switch (kind) {
    case "success":
      return "text-[var(--app-success)]";
    case "error":
      return "text-[var(--app-danger)]";
    case "running":
      return "text-[var(--app-primary)]";
    case "pending":
      return "text-[var(--app-text-subtle)]";
    default:
      return "text-[var(--app-text-muted)]";
  }
}

function taskStatusBadgeClass(kind: ReturnType<typeof taskStatusKind>): string {
  switch (kind) {
    case "success":
      return "border-[color-mix(in_srgb,var(--app-success)_32%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-success)_10%,transparent)] text-[var(--app-success)]";
    case "error":
      return "border-[color-mix(in_srgb,var(--app-danger)_36%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-danger)_10%,transparent)] text-[var(--app-danger)]";
    case "running":
      return "border-[color-mix(in_srgb,var(--app-primary)_40%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-primary)_12%,transparent)] text-[var(--app-primary)]";
    case "pending":
      return "border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-text-muted)_7%,transparent)] text-[var(--app-text-subtle)]";
    default:
      return "border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)]";
  }
}

function taskElapsedMs(row: TaskToolRow, nowMs: number): number {
  if (row.terminal) {
    return Math.max(0, row.elapsedMs || row.currentToolMs);
  }
  const startedAt = row.launchStartedAtMs || row.currentToolStartedAtMs;
  if (startedAt > 0 && nowMs > startedAt) {
    return Math.max(0, nowMs - startedAt);
  }
  return 0;
}

function taskElapsedLabel(row: TaskToolRow, nowMs: number): string {
  const elapsedMs = taskElapsedMs(row, nowMs);
  if (elapsedMs > 0) {
    return formatDuration(elapsedMs);
  }
  return row.terminal ? row.time || '-' : '0ms';
}

function TaskElapsedTime({ row }: { row: TaskToolRow }) {
  const [timerNow, setTimerNow] = useState(() => Date.now());
  const running = taskStatusKind(row) === "running" && !row.terminal;

  useEffect(() => {
    if (!running || row.launchStartedAtMs <= 0) {
      return;
    }
    setTimerNow(Date.now());
    const intervalID = window.setInterval(() => setTimerNow(Date.now()), 100);
    return () => window.clearInterval(intervalID);
  }, [running, row.launchStartedAtMs]);

  return <>{taskElapsedLabel(row, running ? timerNow : 0)}</>;
}

function taskPreviewLabel(row: TaskToolRow): string {
  const previewKind = row.previewKind.trim().toLowerCase();
  if (previewKind === 'reasoning') return 'thinking';
  return row.previewKind.trim() || 'live';
}

function TaskAgentListRow({
  row,
  index,
  dense,
}: {
  row: TaskToolRow;
  index: number;
  dense: boolean;
}) {
  const kind = taskStatusKind(row);
  const statusLabel = taskStatusLabel(row);
  const primaryLabel = row.assignmentLabel || row.agent || 'subagent';
  const agentLabel = row.agent && row.assignmentLabel ? `@${row.agent}` : row.agent;
  const secondaryLabel = [agentLabel, row.modelLabel].filter(Boolean).join(' · ');
  const toolLabel = row.tool && row.tool !== '-' ? row.tool : taskStatusText(kind);
  const previewText = row.previewText.trim();
  const rowNumber = row.launchIndex || index + 1;

  return (
    <div className={cn(
      "group min-w-0 border-t border-[var(--app-border)] transition-colors hover:bg-[color-mix(in_srgb,var(--app-text-muted)_5%,transparent)]",
      kind === "running" ? "bg-[color-mix(in_srgb,var(--app-primary)_5%,transparent)]" : "",
    )}>
      <div className={cn(
        "grid min-w-0 grid-cols-[3.25rem_minmax(0,1fr)_4.25rem] items-center gap-x-2 gap-y-1 px-3 sm:grid-cols-[2.5rem_3.75rem_minmax(0,1.5fr)_minmax(0,0.9fr)_4.75rem] sm:gap-x-3",
        dense ? "py-1.5" : "py-2",
      )}>
        <div className="hidden font-mono text-[10px] text-[var(--app-text-subtle)] tabular-nums sm:col-start-1 sm:row-start-1 sm:block">
          {rowNumber.toString().padStart(2, '0')}
        </div>
        <div className={cn("col-start-1 row-start-1 inline-flex h-6 w-fit items-center gap-1 rounded-md border px-1.5 font-mono text-[10px] font-bold tracking-[0.08em] sm:col-start-2", taskStatusBadgeClass(kind))}>
          <span className={cn("h-1.5 w-1.5 rounded-full bg-current", kind === "running" ? "animate-pulse" : "opacity-70")} />
          {statusLabel}
        </div>
        <div className="col-start-2 row-start-1 min-w-0 sm:col-start-3">
          <div className="min-w-0 truncate text-[12px] font-semibold text-[var(--app-text)]" title={primaryLabel}>
            {primaryLabel}
          </div>
          {secondaryLabel ? (
            <div className="mt-0.5 min-w-0 truncate text-[10px] text-[var(--app-text-subtle)]" title={secondaryLabel}>
              {secondaryLabel}
            </div>
          ) : null}
        </div>
        <div className="col-start-2 col-span-2 row-start-2 min-w-0 truncate font-mono text-[11px] text-[var(--app-text-muted)] sm:col-start-4 sm:col-span-1 sm:row-start-1" title={toolLabel}>
          {toolLabel}
        </div>
        <div className={cn("col-start-3 row-start-1 min-w-[4.75rem] text-right font-mono text-[11px] tabular-nums sm:col-start-5", taskStatusTextClass(kind))}>
          <TaskElapsedTime row={row} />
        </div>
      </div>
      {previewText && !dense ? (
        <div className="grid min-w-0 grid-cols-[3.25rem_minmax(0,1fr)] gap-x-2 px-3 pb-2 sm:grid-cols-[2.5rem_3.75rem_minmax(0,1fr)] sm:gap-x-3">
          <div />
          <div className="hidden sm:block" />
          <div className="col-start-2 min-w-0 truncate border-l border-[var(--app-border)] pl-2 text-[11px] leading-4 text-[var(--app-text-subtle)] sm:col-start-3">
            <span className="mr-1 font-mono uppercase tracking-[0.08em] text-[9px]">
              {taskPreviewLabel(row)}:
            </span>
            {previewText}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function taskRowsCounts(rows: TaskToolRow[]) {
  return rows.reduce(
    (acc, row) => {
      const kind = taskStatusKind(row);
      acc.total += 1;
      if (kind === "running") acc.running += 1;
      else if (kind === "success") acc.done += 1;
      else if (kind === "error") acc.error += 1;
      else acc.pending += 1;
      return acc;
    },
    { total: 0, running: 0, done: 0, error: 0, pending: 0 },
  );
}

function TaskRowsHeader({
  counts,
  swarm,
}: {
  counts: ReturnType<typeof taskRowsCounts>;
  swarm: boolean;
}) {
  return (
    <div className="flex min-w-0 flex-wrap items-center justify-between gap-2 border-b border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-bg-alt)_72%,transparent)] px-3 py-2">
      <div className="flex min-w-0 items-center gap-2">
        <LoaderCircle size={14} className={counts.running > 0 ? "animate-spin text-[var(--app-primary)]" : "text-[var(--app-text-subtle)]"} />
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate text-xs font-bold uppercase tracking-[0.12em] text-[var(--app-text)]">
              {swarm ? "Swarm mode" : "Subagent stream"}
            </span>
          </div>
          <div className="mt-0.5 text-[11px] text-[var(--app-text-subtle)]">
            {counts.total} launched · {counts.running} running · {counts.done} done{counts.error > 0 ? ` · ${counts.error} errors` : ''}
          </div>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1.5 font-mono text-[10px]">
        <span className="rounded-md bg-[color-mix(in_srgb,var(--app-primary)_12%,transparent)] px-1.5 py-0.5 text-[var(--app-primary)]">RUN {counts.running}</span>
        <span className="rounded-md bg-[color-mix(in_srgb,var(--app-success)_12%,transparent)] px-1.5 py-0.5 text-[var(--app-success)]">OK {counts.done}</span>
        {counts.pending > 0 ? <span className="rounded-md bg-[color-mix(in_srgb,var(--app-text-muted)_10%,transparent)] px-1.5 py-0.5 text-[var(--app-text-subtle)]">WAIT {counts.pending}</span> : null}
        {counts.error > 0 ? <span className="rounded-md bg-[color-mix(in_srgb,var(--app-danger)_12%,transparent)] px-1.5 py-0.5 text-[var(--app-danger)]">ERR {counts.error}</span> : null}
      </div>
    </div>
  );
}

function TaskSwarmCompactRow({
  row,
  index,
}: {
  row: TaskToolRow;
  index: number;
}) {
  const kind = taskStatusKind(row);
  const statusLabel = taskStatusLabel(row);
  const rowNumber = row.launchIndex || index + 1;
  const agent = truncateMiddle(row.agent || 'subagent', TASK_SWARM_AGENT_MAX);
  const agentLabel = agent.startsWith('@') ? agent : `@${agent}`;
  const toolLabel = truncateMiddle(row.tool && row.tool !== '-' ? row.tool : taskStatusText(kind), TASK_SWARM_TOOL_MAX);
  const title = row.assignmentLabel && row.assignmentLabel !== row.agent
    ? truncateMiddle(row.assignmentLabel, TASK_SWARM_TITLE_MAX)
    : '';

  return (
    <div className={cn(
      "min-w-0 rounded-lg border border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-bg-alt)_34%,transparent)] px-2 py-1.5",
      kind === "running" ? "border-[color-mix(in_srgb,var(--app-primary)_34%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-primary)_6%,transparent)]" : "",
    )}>
      <div className="flex min-w-0 items-center gap-1.5">
        <span className="shrink-0 font-mono text-[10px] text-[var(--app-text-subtle)] tabular-nums">
          {rowNumber.toString().padStart(2, '0')}
        </span>
        <span className={cn("inline-flex h-5 shrink-0 items-center gap-1 rounded-md border px-1.5 font-mono text-[9px] font-bold tracking-[0.08em]", taskStatusBadgeClass(kind))}>
          <span className={cn("h-1.5 w-1.5 rounded-full bg-current", kind === "running" ? "animate-pulse" : "opacity-70")} />
          {statusLabel}
        </span>
        <span className="min-w-0 truncate text-[11px] font-semibold text-[var(--app-text)]">
          {agentLabel}
        </span>
        <span className="min-w-0 truncate font-mono text-[10px] text-[var(--app-text-muted)]">
          {toolLabel}
        </span>
        <span className={cn("ml-auto min-w-[4.25rem] shrink-0 text-right font-mono text-[10px] tabular-nums", taskStatusTextClass(kind))}>
          <TaskElapsedTime row={row} />
        </span>
      </div>
      {title ? (
        <div className="mt-1 min-w-0 truncate pl-[4.8rem] text-[10px] leading-4 text-[var(--app-text-subtle)] sm:pl-[5.25rem]">
          {title}
        </div>
      ) : null}
    </div>
  );
}

function TaskSwarmRowsView({ rows }: { rows: TaskToolRow[] }) {
  const counts = taskRowsCounts(rows);
  return (
    <div className="mt-2 min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-sm">
      <TaskRowsHeader counts={counts} swarm />
      <div className="grid min-w-0 grid-cols-1 gap-1.5 p-2 md:grid-cols-2 xl:grid-cols-3">
        {rows.map((row, index) => (
          <TaskSwarmCompactRow
            key={row.childSessionId.trim() || `launch-index:${row.launchIndex || index + 1}`}
            row={row}
            index={index}
          />
        ))}
      </div>
    </div>
  );
}

function TaskRowsView({ rows }: { rows: TaskToolRow[] }) {
  if (rows.length === 0) return null;

  if (rows.length > TASK_SWARM_THRESHOLD) {
    return <TaskSwarmRowsView rows={rows} />;
  }

  const counts = taskRowsCounts(rows);
  const dense = rows.length >= 50;

  return (
    <div className="mt-2 min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-sm">
      <TaskRowsHeader counts={counts} swarm={false} />
      <div className="hidden min-w-0 grid-cols-[2.5rem_3.75rem_minmax(0,1.5fr)_minmax(0,0.9fr)_4.75rem] items-center gap-x-3 border-b border-[var(--app-border)] px-3 py-1.5 text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)] sm:grid">
        <div className="min-w-0 font-mono tabular-nums">#</div>
        <div className="min-w-0">Status</div>
        <div className="min-w-0">Subagent</div>
        <div className="min-w-0">Current</div>
        <div className="min-w-0 text-right">Time</div>
      </div>
      <div className="min-w-0">
        {rows.map((row, index) => (
          <TaskAgentListRow
            key={row.childSessionId.trim() || `launch-index:${row.launchIndex || index + 1}`}
            row={row}
            index={index}
            dense={dense}
          />
        ))}
      </div>
    </div>
  );
}

function SearchSummaryLine({
  toolMessage,
}: {
  toolMessage: StructuredToolMessage;
}) {
  const data = toolMessage.searchData;
  if (!data) return null;

  const parts: string[] = [];
  if (data.queryCount > 1) parts.push(`${data.queryCount} queries`);
  if (data.count > 0) {
    parts.push(
      `${data.count} ${data.mode === "files" ? (data.count === 1 ? "file" : "files") : data.count === 1 ? "match" : "matches"}`,
    );
  }
  if (data.totalMatched > data.count) parts.push(`${data.totalMatched} total`);
  if (data.timedOut) parts.push("timed out");
  else if (data.truncated) parts.push("partial results");

  const summary = parts.length > 0 ? parts.join(" · ") : "no matches";

  return (
    <div className="mt-1 text-[11px] leading-5 text-[var(--app-text-subtle)]">
      {summary}
      {data.path ? <span className="hidden sm:inline"> · {data.path}</span> : null}
    </div>
  );
}

function SearchLineList({ group }: { group: SearchToolLineGroup }) {
  const [expanded, setExpanded] = useState(false);
  const displayMatches = group.matches.length > 0;
  const items = displayMatches ? group.matches : group.lines.map((line) => ({ line, text: "" }));
  const visibleItems = expanded ? items : items.slice(0, 3);
  const hiddenCount = Math.max(0, items.length - visibleItems.length + (expanded ? 0 : group.extraLineCount));
  const showExpand = items.length > 3 || group.extraLineCount > 0;

  return (
    <div className="mt-2 min-w-0 text-[12px] leading-5 text-[var(--app-text-muted)]">
      <div className="mb-1 font-sans text-[11px] font-medium text-[var(--app-text-subtle)]">
        {group.query || "match"}
      </div>
      {visibleItems.length > 0 ? (
        <div className="space-y-1">
          {visibleItems.map((item, index) => (
            <div key={`${item.line}:${index}`} className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-2">
              {item.line > 0 ? (
                <span className="select-none font-mono text-[11px] text-[var(--app-text-subtle)]">
                  {item.line}
                </span>
              ) : (
                <span />
              )}
              <span className="min-w-0 whitespace-pre-wrap break-words font-mono text-[var(--app-text-muted)] [overflow-wrap:anywhere]">
                <ToolSyntaxLine text={item.text || (item.line > 0 ? "line match" : "file match")} language={inferToolSyntaxLanguage(group.query)} />
              </span>
            </div>
          ))}
        </div>
      ) : (
        <div className="font-mono text-[var(--app-text-subtle)]">file match</div>
      )}
      {showExpand ? (
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="mt-1 text-[11px] text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:underline"
        >
          {expanded
            ? "collapse matches"
            : `show ${hiddenCount > 0 ? hiddenCount : "more"} more`}
        </button>
      ) : null}
    </div>
  );
}

function SearchFileSection({
  file,
  mode,
}: {
  file: SearchToolFileGroup;
  mode: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const visibleGroups = expanded
    ? file.queryGroups
    : file.queryGroups.slice(0, 2);
  const hiddenGroupCount = Math.max(
    0,
    file.queryGroups.length -
      visibleGroups.length +
      (expanded ? 0 : file.extraQueryCount),
  );
  const showExpand = file.queryGroups.length > 2 || file.extraQueryCount > 0;

  return (
    <div className="min-w-0 border-t border-[var(--app-border)] py-2 first:border-t-0 first:pt-0 last:pb-0">
      <div className="grid min-w-0 gap-1 text-[12px] sm:flex sm:items-baseline sm:gap-2">
        <span className="min-w-0 break-words font-mono text-[var(--app-text)] [overflow-wrap:anywhere]">
          {file.path}
        </span>
        <span className="text-[10px] text-[var(--app-text-subtle)]">
          {mode === "files"
            ? `${file.matchCount} ${file.matchCount === 1 ? "hit" : "hits"}`
            : `${file.matchCount} ${file.matchCount === 1 ? "match" : "matches"}`}
        </span>
      </div>
      <div className="mt-1.5 space-y-1">
        {visibleGroups.map((group, index) => (
          <SearchLineList
            key={`${file.path}:${group.query}:${index}`}
            group={group}
          />
        ))}
      </div>
      {showExpand ? (
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="mt-1.5 text-[11px] text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:underline"
        >
          {expanded
            ? "collapse queries"
            : `show more queries${hiddenGroupCount > 0 ? ` (${hiddenGroupCount})` : ""}`}
        </button>
      ) : null}
    </div>
  );
}

function toolJsonString(record: Record<string, unknown> | null | undefined, key: string): string {
  const value = record?.[key];
  return typeof value === "string" ? value.trim() : "";
}

function ImageToolAction({ toolMessage }: { toolMessage: StructuredToolMessage }) {
  const outputJson = parseToolJSON(toolMessage.output) ?? parseToolJSON(toolMessage.completedOutput);
  const argsJson = toolMessage.argumentsJson ?? null;
  const threadId = toolJsonString(outputJson, "thread_id") || toolJsonString(argsJson, "thread_id");
  if (!threadId) return null;
  const href = `/tools/image/${encodeURIComponent(threadId)}`;
  return (
    <a
      href={href}
      className="mt-2 inline-flex h-8 items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 text-xs font-medium text-[var(--app-text)] hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)]"
    >
      <ArrowRight size={13} />
      Open image session
    </a>
  );
}

function shouldRenderPreviewAsPlain(toolName: string): boolean {
  switch (toolName.trim().toLowerCase()) {
    case "manage_todos":
    case "manage-todos":
    case "manage-image":
    case "manage_image":
    case "websearch":
    case "webfetch":
    case "task":
    case "plan-manage":
    case "plan_manage":
    case "exit-plan-mode":
    case "exit_plan_mode":
    case "permission":
      return true;
    default:
      return false;
  }
}

function parseToolJSON(value: string): Record<string, unknown> | null {
  const trimmed = value.trim();
  if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) return null;
  try {
    const parsed = JSON.parse(trimmed) as unknown;
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : null;
  } catch {
    return null;
  }
}

function SearchToolView({
  toolMessage,
}: {
  toolMessage: StructuredToolMessage;
}) {
  const data = toolMessage.searchData;
  const [expanded, setExpanded] = useState(false);
  if (!data) return null;

  const visibleFiles = expanded ? data.files : data.files.slice(0, 6);
  const hiddenFileCount = Math.max(0, data.files.length - visibleFiles.length);
  const showExpand = data.files.length > 6;
  const sections = useMemo(() => visibleFiles, [visibleFiles]);

  return (
    <div className="mt-2 min-w-0">
      <SearchSummaryLine toolMessage={toolMessage} />
      {sections.length > 0 ? (
        <div className="mt-2 min-w-0 font-mono">
          {sections.map((file, index) => (
            <SearchFileSection
              key={`${file.path}:${index}`}
              file={file}
              mode={data.mode}
            />
          ))}
        </div>
      ) : null}
      {showExpand ? (
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="mt-2 text-[11px] text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:underline"
        >
          {expanded
            ? "collapse results"
            : `show more files (${hiddenFileCount})`}
        </button>
      ) : null}
    </div>
  );
}

export function ToolMessageView({
  toolMessage,
  isGroupItem,
}: {
  toolMessage: StructuredToolMessage;
  isGroupItem?: boolean;
}) {
  const toolTheme = getToolTheme(toolMessage.tool);
  const ToolIcon = toolTheme.icon;
  const state = resolveToolState(toolMessage);
  const StateIcon =
    state === "error"
      ? XCircle
      : state === "running"
        ? LoaderCircle
        : CheckCircle2;
  const label = toolTheme.label || toolMessage.tool || "tool";
  const isTaskSwarm = toolMessage.tool === "task" && toolMessage.taskRows.length > TASK_SWARM_THRESHOLD;
  const todoCounts = formatTodoCounts(toolMessage.todoData?.summary ?? null);
  const summary = isTaskSwarm
    ? ""
    : todoCounts || toolSummaryRemainder(toolMessage.summary || toolMessage.tool || "tool", label);
  const accentWash = toolAccentWash(toolTheme.color, 14);
  const previewLanguage = inferToolSyntaxLanguage(toolMessage.target || pathFromToolSummary(toolMessage.summary));
  const shellPreview = false;
  const plainPreview = shouldRenderPreviewAsPlain(toolMessage.tool);

  return (
    <div className={isGroupItem ? "py-2" : "mb-2 min-w-0 py-2"}>
      <div className="flex min-w-0 items-center gap-2 text-xs">
        <span
          className="inline-flex h-5 shrink-0 items-center gap-1 rounded-md px-1.5 font-semibold"
          style={{ color: toolTheme.color, backgroundColor: accentWash }}
        >
          <ToolIcon size={12} className="shrink-0" />
          {label}
        </span>
        {summary ? (
          <span className="min-w-0 flex-1 truncate font-medium text-[var(--app-text)]">
            {summary}
          </span>
        ) : null}
        {toolMessage.durationMs > 0 ? (
          <span className="shrink-0 text-[var(--app-text-subtle)] text-[11px]">
            {formatDuration(toolMessage.durationMs)}
          </span>
        ) : null}
        <StateIcon
          size={12}
          className={cn(
            "shrink-0",
            state === "running"
              ? "animate-spin text-[var(--app-primary)]"
              : state === "error"
                ? "text-[var(--app-danger)]"
                : "text-[var(--app-text-subtle)]",
          )}
        />
      </div>
      <div className="min-w-0">
        {toolMessage.error ? (
          <div className="mt-1 break-words text-[12px] text-[var(--app-danger)]">
            {toolMessage.error}
          </div>
        ) : null}
        {toolMessage.editDiff ? (
          <EditDiffView toolMessage={toolMessage} />
        ) : null}
        {!toolMessage.editDiff &&
        toolMessage.tool === "task" &&
        toolMessage.taskRows.length > 0 ? (
          <TaskRowsView rows={toolMessage.taskRows} />
        ) : null}
        {!toolMessage.editDiff &&
        toolMessage.tool === "search" &&
        toolMessage.searchData ? (
          <SearchToolView toolMessage={toolMessage} />
        ) : null}
        {!toolMessage.editDiff &&
        toolMessage.tool !== "search" &&
        !(toolMessage.tool === "task" && toolMessage.taskRows.length > 0) &&
        (toolMessage.previewLines.length > 0 || toolMessage.commandText) ? (
          <PreviewLinesView
            lines={toolMessage.previewLines}
            commandText={toolMessage.commandText}
            compact
            language={previewLanguage}
            shell={shellPreview}
            plain={plainPreview}
          />
        ) : null}
        {toolMessage.tool.trim().toLowerCase() === 'manage-image' || toolMessage.tool.trim().toLowerCase() === 'manage_image' ? (
          <ImageToolAction toolMessage={toolMessage} />
        ) : null}
      </div>
    </div>
  );
}

export function ToolGroupView({
  toolName,
  messages,
}: {
  toolName: string;
  messages: StructuredToolMessage[];
}) {
  const [expanded, setExpanded] = useState(false);
  const toolTheme = getToolTheme(toolName);
  const ToolIcon = toolTheme.icon;
  const hasErrors = messages.some((m) => m.error);
  const displayedMessages = expanded ? messages : messages.slice(0, 3);
  const accentWash = toolAccentWash(toolTheme.color, 14);

  return (
    <div className="mb-2 py-2">
      <div className="mb-1.5 flex items-center gap-2 text-xs text-[var(--app-text-muted)]">
        <span
          className="inline-flex h-5 shrink-0 items-center gap-1 rounded-md px-1.5 font-semibold"
          style={{ color: toolTheme.color, backgroundColor: accentWash }}
        >
          <ToolIcon size={12} className="shrink-0" />
          {toolTheme.label || toolName}
        </span>
        <span className="text-[11px] text-[var(--app-text-subtle)]">
          ×{messages.length}
        </span>
        {hasErrors ? (
          <span className="text-[var(--app-danger)] ml-2 text-[10px] font-bold uppercase">
            Errors
          </span>
        ) : null}
      </div>
      <div className="grid gap-0">
        {displayedMessages.map((msg, i) => (
          <ToolMessageView
            key={msg.callId || i}
            toolMessage={msg}
            isGroupItem={true}
          />
        ))}
        {messages.length > 3 ? (
          <button
            onClick={() => setExpanded(!expanded)}
            className="text-left text-[11px] text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:underline pt-1 mt-1 block"
          >
            {expanded
              ? "collapse group"
              : `+ ${messages.length - 3} more calls`}
          </button>
        ) : null}
      </div>
    </div>
  );
}

function ChatMarkdownInner({
  content,
  className,
  toolMessage,
}: ChatMarkdownProps) {
  if (toolMessage) {
    return <ToolMessageView toolMessage={toolMessage} />;
  }

  return (
    <div
      className={cn(
        "chat-markdown min-w-0 max-w-full break-words text-sm leading-6",
        !className?.includes("text-") && "text-[var(--app-text)]",
        className,
      )}
    >
      <MarkdownRenderer content={content} />
    </div>
  );
}

export const ChatMarkdown = memo(ChatMarkdownInner);
