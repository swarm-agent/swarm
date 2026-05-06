import { memo, useMemo, useState } from "react";
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

interface ChatMarkdownProps {
  content: string;
  className?: string;
  toolMessage?: StructuredToolMessage | null;
  nowMs?: number;
}

function resolveToolState(toolMessage: StructuredToolMessage): ToolState {
  return toolMessage.state;
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

function EditDiffView({ toolMessage }: { toolMessage: StructuredToolMessage }) {
  const diff = toolMessage.editDiff;
  if (!diff) return null;
  const hunks = diff.hunks.length > 0
    ? diff.hunks
    : [{ index: 1, oldLines: diff.oldLines, newLines: diff.newLines, oldTruncated: diff.oldTruncated, newTruncated: diff.newTruncated }];
  const showHunkLabels = hunks.length > 1;

  return (
    <div className="mt-1.5 space-y-1.5 font-mono text-[12px] leading-5">
      {hunks.map((hunk, hunkIndex) => (
        <div key={`hunk-${hunk.index}-${hunkIndex}`}>
          {showHunkLabels ? (
            <div className="text-[11px] uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
              edit {hunk.index}
            </div>
          ) : null}
          {hunk.oldLines.map((line, i) => (
            <div
              key={`old-${hunk.index}-${i}`}
              className="text-[var(--app-danger)] whitespace-pre-wrap break-all"
            >
              - {line}
            </div>
          ))}
          {hunk.newLines.map((line, i) => (
            <div
              key={`new-${hunk.index}-${i}`}
              className="text-[var(--app-success)] whitespace-pre-wrap break-all"
            >
              + {line}
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

const PREVIEW_LIMIT = 8;

function PreviewLinesView({
  lines,
  compact = true,
  commandText = "",
}: {
  lines: string[];
  compact?: boolean;
  commandText?: string;
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
        <div className="mb-1 min-w-0 text-[11px] leading-5 text-[var(--app-text-muted)]">
          <span className="whitespace-pre-wrap break-words font-mono [overflow-wrap:anywhere]">{commandText}</span>
        </div>
      ) : null}
      {display.map((line, i) => (
        <div
          key={i}
          className={compact
            ? "whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
            : "whitespace-pre-wrap break-words rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-1.5 text-[12px] leading-5 text-[var(--app-text)] [overflow-wrap:anywhere]"}
        >
          {line}
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
      return "ER";
    case "running":
    case "active":
    case "in_progress":
      return "RN";
    case "pending":
    case "":
      return "..";
    default:
      return status.slice(0, 2).toUpperCase();
  }
}

function liveTaskElapsedLabel(row: TaskToolRow, nowMs: number): string {
  const status = row.status.trim().toLowerCase();
  const running = status === 'running' || status === 'active' || status === 'in_progress';
  if (!running) {
    return row.time || '-';
  }
  const startedAt = row.currentToolStartedAtMs || row.launchStartedAtMs;
  if (startedAt > 0 && nowMs > startedAt) {
    return formatDuration(Math.max(0, nowMs - startedAt));
  }
  const fallbackMs = row.currentToolMs || row.elapsedMs;
  return fallbackMs > 0 ? formatDuration(fallbackMs) : row.time || '-';
}

function TaskRowsView({ rows, nowMs }: { rows: TaskToolRow[]; nowMs: number }) {
  if (rows.length === 0) return null;

  return (
    <div className="mt-1.5 grid gap-1 font-mono text-[11px] leading-[18px]">
      {rows.map((row, index) => {
        const statusLabel = taskStatusLabel(row);
        const previewLabel = row.previewKind.trim().toLowerCase() === 'reasoning'
          ? 'thinking'
          : row.previewKind.trim() || 'live';
        const rowKey = row.childSessionId.trim() || `launch-index:${row.launchIndex || index + 1}`;
        return (
          <div key={`launch:${rowKey}`} className="grid gap-1 text-[var(--app-text-muted)]">
            <div className="flex items-center gap-3">
              <div
                className={`w-6 font-bold ${statusLabel === 'OK' ? 'text-[var(--app-success)]' : statusLabel === 'ER' ? 'text-[var(--app-danger)]' : 'text-[var(--app-primary)]'}`}
              >
                {statusLabel}
              </div>
              <div className="w-20 truncate font-medium text-[var(--app-text)]">
                {row.agent || 'subagent'}
              </div>
              <div className="flex-1 truncate">{row.tool || '-'}</div>
              <div className="text-right text-[var(--app-text-subtle)]">
                {liveTaskElapsedLabel(row, nowMs)}
              </div>
            </div>
            {row.previewText ? (
              <div className="ml-9 border-l-[1.5px] border-[var(--app-border)] pl-3 whitespace-pre-wrap break-all text-[var(--app-text-subtle)]">
                <span className="mr-1 uppercase tracking-[0.08em] text-[10px] text-[var(--app-text-subtle)]">
                  {previewLabel}:
                </span>
                {row.previewText}
              </div>
            ) : null}
          </div>
        );
      })}
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
                {item.text || (item.line > 0 ? "line match" : "file match")}
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
  nowMs = 0,
}: {
  toolMessage: StructuredToolMessage;
  isGroupItem?: boolean;
  nowMs?: number;
}) {
  const { icon: ToolIcon } = getToolTheme(toolMessage.tool);
  const state = resolveToolState(toolMessage);
  const StateIcon =
    state === "error"
      ? XCircle
      : state === "running"
        ? LoaderCircle
        : CheckCircle2;

  return (
    <div
      className={
        isGroupItem
          ? "py-2 border-t border-[var(--app-border)] first:border-0 first:pt-0"
          : "p-3 mb-2 min-w-0 bg-[var(--app-surface-subtle)] border border-[var(--app-border)] rounded-md"
      }
    >
      <div className="flex min-w-0 items-center gap-2 text-xs">
        <ToolIcon size={14} className="text-[var(--app-text-muted)] shrink-0" />
        <span className="font-semibold text-[var(--app-text)] truncate">
          {toolMessage.summary || toolMessage.tool || "tool"}
        </span>
        {toolMessage.durationMs > 0 ? (
          <span className="text-[var(--app-text-subtle)] text-[11px]">
            {formatDuration(toolMessage.durationMs)}
          </span>
        ) : null}
        <div className="ml-auto flex items-center gap-1.5">
          <StateIcon
            size={12}
            className={
              state === "running"
                ? "animate-spin text-[var(--app-primary)]"
                : state === "error"
                  ? "text-[var(--app-danger)]"
                  : "text-[var(--app-text-muted)]"
            }
          />
        </div>
      </div>
      <div className="min-w-0 pl-[22px]">
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
          <TaskRowsView rows={toolMessage.taskRows} nowMs={nowMs} />
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
            compact={toolMessage.tool !== 'exit_plan_mode' && toolMessage.tool !== 'permission'}
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
  const { icon: ToolIcon } = getToolTheme(toolName);
  const hasErrors = messages.some((m) => m.error);
  const displayedMessages = expanded ? messages : messages.slice(0, 3);

  return (
    <div className="mb-2 bg-[var(--app-surface-subtle)] border border-[var(--app-border)] rounded-md p-3">
      <div className="flex items-center gap-2 text-xs text-[var(--app-text-muted)] mb-2 pb-2 border-b border-[var(--app-border)]">
        <ToolIcon size={14} className="shrink-0" />
        <span className="font-semibold text-[var(--app-text)]">
          {toolName}{" "}
          <span className="opacity-80 font-normal ml-1">
            ×{messages.length}
          </span>
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
            nowMs={0}
          />
        ))}
        {messages.length > 3 ? (
          <button
            onClick={() => setExpanded(!expanded)}
            className="text-left text-[11px] text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:underline pt-2 border-t border-[var(--app-border)] mt-1 block"
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
  nowMs = 0,
}: ChatMarkdownProps) {
  if (toolMessage) {
    return <ToolMessageView toolMessage={toolMessage} nowMs={nowMs} />;
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
