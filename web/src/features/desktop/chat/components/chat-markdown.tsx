import { memo, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type MouseEvent, type ReactNode } from "react";
import { Archive, ArrowRight, Bot, CheckCircle2, ChevronDown, ChevronUp, CircleDot, CircleStop, Clock3, Copy, Download, ExternalLink, GitBranch, Layers3, LoaderCircle, MessageSquareText, Search, XCircle } from "lucide-react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { cn } from "../../../../lib/cn";
import { MarkdownRenderer } from "../markdown/render";
import type {
  StructuredToolMessage,
  SearchToolFileGroup,
  SearchToolLineGroup,
  WebFetchToolData,
  WebResourceData,
  WebSearchToolData,
  TaskToolRow,
  TaskChildCardActions,
} from "../types/chat";
import { useDesktopV3CacheSelector } from "../../state/desktop-v3-cache-store";
import { selectDesktopV3TaskChildViewModel, type DesktopV3TaskChildViewModel } from "../../state/desktop-v3-cache-selectors";
import { hydrateDesktopV3ChildCard } from "../../state/desktop-v3-session-hydrator";
import { requireDesktopV3RealtimeControllerReady, type DesktopV3RealtimeSessionDemandLease } from "../../realtime/v3-realtime-controller";
import { stopSubagentSessionV3Run } from "../../session-v3/api";
import { getToolTheme, type ToolState } from "../services/tool-theme";
import { ToolSyntaxLine, inferToolSyntaxLanguage, pathFromToolSummary } from "../services/tool-syntax";
import { displayAgentName } from "../services/agent-display";

interface ChatMarkdownProps {
  content: string;
  className?: string;
  toolMessage?: StructuredToolMessage | null;
  thinkingTagsEnabled?: boolean;
  taskChildActions?: TaskChildCardActions;
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

const BASH_COLLAPSED_MAX_HEIGHT = 320;
const BASH_COLLAPSED_PREVIEW_LINES = 120;
const BASH_COLLAPSED_PREVIEW_CHARS = 32 * 1024;
const BASH_COLLAPSED_VISIBLE_LINE_ESTIMATE = 16;
const BASH_OUTPUT_LINE_HEIGHT = 20;
const BASH_EXPANDED_HEIGHT = "50vh";
export const TOOL_RESULT_BODY_CLASS = "max-h-[50vh] min-w-0 overflow-y-auto overflow-x-hidden overscroll-contain";

function joinBashOutputParts(parts: string[]): string {
  let out = "";
  for (const part of parts) {
    if (!part) continue;
    if (!out) {
      out = part;
      continue;
    }
    out += out.endsWith("\n") ? part : `\n${part}`;
  }
  return out;
}

function bashOutputText(toolMessage: StructuredToolMessage): string {
  const data = toolMessage.bashData;
  if (!data) return toolMessage.output || toolMessage.completedOutput || "";

  const parts: string[] = [];
  const addPart = (value: string) => {
    if (!value) return;
    if (parts.some((part) => part === value || part.includes(value))) return;
    parts.push(value);
  };

  addPart(data.output);
  if (!data.output || !data.output.includes(data.stdout)) addPart(data.stdout);
  if (!data.output || !data.output.includes(data.stderr)) addPart(data.stderr);

  return joinBashOutputParts(parts) || toolMessage.output || toolMessage.completedOutput || "";
}

export function bashCopyText(output: string): string {
  return output;
}

interface BashOutputIndex {
  lineStarts: number[];
  preview: string;
  previewedLineCount: number;
  previewStartsMidLine: boolean;
  canExpand: boolean;
}

export function indexBashOutput(output: string): BashOutputIndex {
  const lineStarts = output ? [0] : [];
  for (let index = 0; index < output.length; index += 1) {
    if (output.charCodeAt(index) === 10) lineStarts.push(index + 1);
  }

  const lineLimitedStart = lineStarts[Math.max(0, lineStarts.length - BASH_COLLAPSED_PREVIEW_LINES)] ?? 0;
  const charLimitedStart = Math.max(0, output.length - BASH_COLLAPSED_PREVIEW_CHARS);
  const previewStart = Math.max(lineLimitedStart, charLimitedStart);
  let previewLineIndex = 0;
  for (let index = lineStarts.length - 1; index >= 0; index -= 1) {
    const lineStart = lineStarts[index];
    if (lineStart !== undefined && lineStart <= previewStart) {
      previewLineIndex = index;
      break;
    }
  }
  const previewStartsMidLine = previewStart > (lineStarts[previewLineIndex] ?? 0);
  const previewedLineCount = Math.max(0, lineStarts.length - previewLineIndex);
  const preview = output.slice(previewStart);
  const canExpand = previewStart > 0 || lineStarts.length > BASH_COLLAPSED_VISIBLE_LINE_ESTIMATE;

  return { lineStarts, preview, previewedLineCount, previewStartsMidLine, canExpand };
}

function bashOutputLine(output: string, lineStarts: number[], index: number): string {
  const start = lineStarts[index] ?? 0;
  const nextStart = lineStarts[index + 1];
  const end = nextStart === undefined ? output.length : nextStart - 1;
  return output.slice(start, end);
}

function downloadBashOutput(output: string): void {
  if (typeof document === "undefined" || typeof URL === "undefined") return;
  const url = URL.createObjectURL(new Blob([output], { type: "text/plain;charset=utf-8" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "bash-output.txt";
  anchor.style.display = "none";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

function bashStatusLabel(state: ToolState): string {
  switch (state) {
    case "running":
      return "running";
    case "error":
      return "error";
    default:
      return "done";
  }
}

async function copyTextToClipboard(text: string): Promise<void> {
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }
  if (typeof document === "undefined") return;
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "true");
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  document.body.appendChild(textarea);
  textarea.select();
  document.execCommand("copy");
  document.body.removeChild(textarea);
}

function BashToolCard({ toolMessage, isGroupItem }: { toolMessage: StructuredToolMessage; isGroupItem?: boolean }) {
  const toolTheme = getToolTheme(toolMessage.tool);
  const ToolIcon = toolTheme.icon;
  const state = resolveToolState(toolMessage);
  const StateIcon = state === "error" ? XCircle : state === "running" ? LoaderCircle : CheckCircle2;
  const command = toolMessage.bashData?.command || toolMessage.commandText;
  const output = useMemo(() => bashOutputText(toolMessage), [toolMessage]);
  const outputIndex = useMemo(() => indexBashOutput(output), [output]);
  const outputRef = useRef<HTMLDivElement | null>(null);
  const userScrolledAwayRef = useRef(false);
  const copyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [copied, setCopied] = useState(false);
  const virtualizer = useVirtualizer({
    count: expanded ? outputIndex.lineStarts.length : 0,
    getScrollElement: () => outputRef.current,
    estimateSize: () => BASH_OUTPUT_LINE_HEIGHT,
    overscan: 12,
  });

  useEffect(() => {
    if (!expanded || userScrolledAwayRef.current || outputIndex.lineStarts.length === 0) return;
    const frame = window.requestAnimationFrame(() => {
      virtualizer.scrollToIndex(outputIndex.lineStarts.length - 1, { align: "end" });
    });
    return () => window.cancelAnimationFrame(frame);
  }, [expanded, output, outputIndex.lineStarts.length, state, virtualizer]);

  useEffect(() => () => {
    if (copyTimerRef.current) clearTimeout(copyTimerRef.current);
  }, []);

  const handleOutputScroll = useCallback(() => {
    const node = outputRef.current;
    if (!node) return;
    const distanceFromBottom = node.scrollHeight - node.scrollTop - node.clientHeight;
    userScrolledAwayRef.current = distanceFromBottom > 64;
  }, []);

  const handleCopy = useCallback(async () => {
    if (!output) return;
    await copyTextToClipboard(bashCopyText(output));
    setCopied(true);
    if (copyTimerRef.current) clearTimeout(copyTimerRef.current);
    copyTimerRef.current = setTimeout(() => setCopied(false), 1400);
  }, [output]);

  const handleDownload = useCallback(() => {
    if (output) downloadBashOutput(output);
  }, [output]);

  const toggleExpanded = useCallback(() => {
    userScrolledAwayRef.current = false;
    setExpanded((value) => !value);
  }, []);

  const accentWash = toolAccentWash(toolTheme.color, 14);
  const statusText = bashStatusLabel(state);
  const exitCode = toolMessage.bashData?.exitCode;
  const previewPrefix = outputIndex.previewStartsMidLine ? "…" : "";

  return (
    <div className={cn(isGroupItem ? "py-2" : "mb-2 py-2", "w-full min-w-0")}>
      <div className="w-full min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] shadow-sm">
        <div className="flex min-w-0 flex-wrap items-center gap-2 border-b border-[var(--app-border)] px-3 py-2 text-xs">
          <span className="inline-flex h-6 shrink-0 items-center gap-1 rounded-md px-1.5 font-semibold" style={{ color: toolTheme.color, backgroundColor: accentWash }}>
            <ToolIcon size={12} className="shrink-0" />
            bash
          </span>
          <span className="inline-flex shrink-0 items-center gap-1 text-[11px] font-medium text-[var(--app-text-muted)]">
            <StateIcon size={12} className={cn(state === "running" ? "animate-spin text-[var(--app-primary)]" : state === "error" ? "text-[var(--app-danger)]" : "text-[var(--app-text-subtle)]")} />
            {statusText}
          </span>
          {typeof exitCode === "number" ? <span className="shrink-0 text-[11px] text-[var(--app-text-subtle)]">exit {exitCode}</span> : null}
          {toolMessage.durationMs > 0 ? <span className="shrink-0 text-[11px] text-[var(--app-text-subtle)]">{formatDuration(toolMessage.durationMs)}</span> : null}
          <div className="ml-auto flex min-w-0 shrink-0 items-center gap-1">
            <button type="button" className="inline-flex h-7 items-center gap-1 rounded-md border border-[var(--app-border)] px-2 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface)] disabled:cursor-not-allowed disabled:opacity-50" onClick={handleCopy} disabled={!output} aria-label="Copy Bash output">
              <Copy size={12} className="shrink-0" />
              <span className="hidden sm:inline">{copied ? "Copied" : "Copy"}</span>
            </button>
            <button type="button" className="inline-flex h-7 items-center gap-1 rounded-md border border-[var(--app-border)] px-2 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface)] disabled:cursor-not-allowed disabled:opacity-50" onClick={handleDownload} disabled={!output} aria-label="Download exact Bash output">
              <Download size={12} className="shrink-0" />
              <span className="hidden sm:inline">Download</span>
            </button>
            {outputIndex.canExpand ? (
              <button type="button" className="inline-flex h-7 items-center gap-1 rounded-md border border-[var(--app-border)] px-2 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface)]" onClick={toggleExpanded} aria-expanded={expanded}>
                {expanded ? <ChevronUp size={12} className="shrink-0" /> : <ChevronDown size={12} className="shrink-0" />}
                <span className="hidden sm:inline">{expanded ? "Collapse" : "View all"}</span>
              </button>
            ) : null}
          </div>
        </div>
        {command ? (
          <div className="min-w-0 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-[12px] leading-5 text-[var(--app-text)]">
            <span className="mr-1 select-none text-[var(--app-accent)]">$</span>
            <ToolSyntaxLine text={command} language="bash" className="whitespace-pre-wrap break-words font-mono [overflow-wrap:anywhere]" />
          </div>
        ) : null}
        {toolMessage.error ? <div className="border-b border-[var(--app-border)] px-3 py-2 text-[12px] text-[var(--app-danger)]">{toolMessage.error}</div> : null}
        {output ? expanded ? (
          <div ref={outputRef} className="min-w-0 overflow-y-auto overflow-x-hidden overscroll-contain bg-[var(--app-code-bg)] font-mono text-[12px] leading-5 text-[var(--app-code-text)]" style={{ height: BASH_EXPANDED_HEIGHT }} onScroll={handleOutputScroll} data-bash-output="virtualized">
            <div className="relative min-w-0" style={{ height: `${virtualizer.getTotalSize()}px` }}>
              {virtualizer.getVirtualItems().map((virtualLine) => (
                <div key={virtualLine.key} ref={virtualizer.measureElement} data-index={virtualLine.index} className="absolute left-0 top-0 w-full min-w-0 whitespace-pre-wrap break-words px-3 [overflow-wrap:anywhere]" style={{ transform: `translateY(${virtualLine.start}px)` }}>
                  {bashOutputLine(output, outputIndex.lineStarts, virtualLine.index) || "\u00a0"}
                </div>
              ))}
            </div>
          </div>
        ) : (
          <div className="min-w-0 overflow-y-auto overflow-x-hidden overscroll-contain bg-[var(--app-code-bg)] text-[12px] leading-5 text-[var(--app-code-text)]" style={{ maxHeight: `${BASH_COLLAPSED_MAX_HEIGHT}px` }} data-bash-output="bounded-preview">
            {outputIndex.canExpand ? <div className="sticky top-0 z-[1] border-b border-[var(--app-border)] bg-[var(--app-code-bg)] px-3 py-1 font-sans text-[10px] text-[var(--app-text-subtle)]">Showing the last {outputIndex.previewedLineCount.toLocaleString()} lines · View all for exact output</div> : null}
            <pre className="m-0 min-w-0 whitespace-pre-wrap break-words p-3 font-mono [overflow-wrap:anywhere]"><code>{previewPrefix}{outputIndex.preview}</code></pre>
          </div>
        ) : (
          <div className="px-3 py-2 text-[12px] text-[var(--app-text-subtle)]">{state === "running" ? "Waiting for output…" : "No output"}</div>
        )}
      </div>
    </div>
  );
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
    <div className="mt-3 overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-code-bg)] font-mono text-[12px] leading-5">
      <div className="flex items-center gap-2 border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 font-sans text-[11px]">
        <span className="font-medium text-[var(--app-text-muted)]">Changes</span>
        <span className="ml-auto font-mono font-semibold text-[var(--app-danger)]">−{removedCount}</span>
        <span className="font-mono font-semibold text-[var(--app-success)]">+{addedCount}</span>
      </div>
      <div className={cn(TOOL_RESULT_BODY_CLASS, "py-1")}>
        {hunks.map((hunk, hunkIndex) => (
          <div key={`hunk-${hunk.index}-${hunkIndex}`} className={cn(hunkIndex > 0 && "border-t border-[var(--app-border)] pt-1")}>
            {showHunkLabels ? (
              <div className="px-3 py-1 font-sans text-[10px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                Edit {hunk.index}
              </div>
            ) : null}
            {hunk.oldLines.map((line, i) => (
              <div
                key={`old-${hunk.index}-${i}`}
                className="grid min-w-0 grid-cols-[1.75rem_minmax(0,1fr)] border-l-2 border-[color-mix(in_srgb,var(--app-danger)_55%,transparent)] bg-[color-mix(in_srgb,var(--app-danger)_7%,transparent)] px-2 text-[var(--app-code-text)]"
              >
                <span className="select-none text-center text-[var(--app-danger)] opacity-70">−</span>
                <span className="min-w-0 whitespace-pre-wrap break-words [overflow-wrap:anywhere]"><ToolSyntaxLine text={line} language={language} /></span>
              </div>
            ))}
            {hunk.newLines.map((line, i) => (
              <div
                key={`new-${hunk.index}-${i}`}
                className="grid min-w-0 grid-cols-[1.75rem_minmax(0,1fr)] border-l-2 border-[color-mix(in_srgb,var(--app-success)_55%,transparent)] bg-[color-mix(in_srgb,var(--app-success)_7%,transparent)] px-2 text-[var(--app-code-text)]"
              >
                <span className="select-none text-center text-[var(--app-success)] opacity-70">+</span>
                <span className="min-w-0 whitespace-pre-wrap break-words [overflow-wrap:anywhere]"><ToolSyntaxLine text={line} language={language} /></span>
              </div>
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

const TASK_SWARM_THRESHOLD = 10;

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

  return (
    <div className={compact
      ? cn(TOOL_RESULT_BODY_CLASS, "mt-1 py-0.5 font-mono text-[11px] leading-[18px] text-[var(--app-text-muted)]")
      : cn(TOOL_RESULT_BODY_CLASS, "mt-2 space-y-1.5")}
    >
      {commandText ? (
        <div className="mb-1.5 min-w-0 rounded-md bg-[var(--app-surface)] px-2 py-1 text-[11px] leading-5 text-[var(--app-text)]">
          <span className="mr-1 select-none text-[var(--app-accent)]">$</span>
          <ToolSyntaxLine text={commandText} language="bash" className="whitespace-pre-wrap break-words font-mono [overflow-wrap:anywhere]" />
        </div>
      ) : null}
      {lines.map((line, i) => (
        <div
          key={i}
          className={compact
            ? "whitespace-pre-wrap break-words border-t border-[var(--app-border)] px-3 py-1.5 first:border-t-0 [overflow-wrap:anywhere]"
            : "whitespace-pre-wrap break-words rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-1.5 text-[12px] leading-5 text-[var(--app-text)] [overflow-wrap:anywhere]"}
        >
          <ToolSyntaxLine text={line} language={language} shell={shell} plain={plain} />
        </div>
      ))}
    </div>
  );
}

function normalizeTaskStatus(row: TaskToolRow): string {
  const status = row.status.trim().toLowerCase();
  if (status) return status;
  const phase = row.phase.trim().toLowerCase();
  switch (phase) {
    case "spawned":
      return "pending";
    case "tool.started":
    case "tool.completed":
      return "running";
    case "tool.failed":
      return "failed";
    case "completed":
    case "cancelled":
    case "canceled":
    case "failed":
      return phase;
    default:
      return "pending";
  }
}

function taskStatusLabel(row: TaskToolRow): string {
  const status = normalizeTaskStatus(row);
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
    case "cancelled":
    case "canceled":
      return "STOP";
    case "running":
    case "active":
    case "in_progress":
      return "RUN";
    case "pending":
    case "":
      return "WAIT";
    default:
      return status.toUpperCase();
  }
}

function taskStatusKind(row: TaskToolRow): "success" | "error" | "running" | "pending" | "other" {
  const status = normalizeTaskStatus(row);
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
    case "cancelled":
    case "canceled":
      return "other";
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

function taskStatusDotClass(kind: ReturnType<typeof taskStatusKind>): string {
  switch (kind) {
    case "success":
      return "bg-[var(--app-success)]";
    case "error":
      return "bg-[var(--app-danger)]";
    case "running":
      return "bg-[var(--app-primary)] animate-pulse";
    default:
      return "bg-[var(--app-text-subtle)]";
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

function taskRowKey(row: TaskToolRow, index: number): string {
  return row.launchKey?.trim() || row.childSessionId.trim() || `launch-index:${row.launchIndex || index + 1}`;
}

export const TASK_ELAPSED_TICK_MS = 1_000;

let taskElapsedNow = Date.now();
let taskElapsedIntervalId: number | null = null;
const taskElapsedSubscribers = new Set<() => void>();

function emitTaskElapsedTick(): void {
  taskElapsedNow = Date.now();
  for (const subscriber of taskElapsedSubscribers) subscriber();
}

function subscribeTaskElapsedClock(subscriber: () => void): () => void {
  taskElapsedSubscribers.add(subscriber);
  if (taskElapsedIntervalId === null) {
    taskElapsedIntervalId = window.setInterval(emitTaskElapsedTick, TASK_ELAPSED_TICK_MS);
    emitTaskElapsedTick();
  }
  return () => {
    taskElapsedSubscribers.delete(subscriber);
    if (taskElapsedSubscribers.size === 0 && taskElapsedIntervalId !== null) {
      window.clearInterval(taskElapsedIntervalId);
      taskElapsedIntervalId = null;
    }
  };
}

function getTaskElapsedSnapshot(): number {
  return taskElapsedNow;
}

function getTaskElapsedServerSnapshot(): number {
  return 0;
}

function useTaskElapsedNow(enabled: boolean): number {
  const now = useSyncExternalStore(
    enabled ? subscribeTaskElapsedClock : () => () => undefined,
    getTaskElapsedSnapshot,
    getTaskElapsedServerSnapshot,
  );
  return enabled ? now : 0;
}

function taskRowsEqual(left: TaskToolRow, right: TaskToolRow, options: { comparePreview?: boolean } = {}): boolean {
  if (left.launchKey !== right.launchKey
    || left.launchIndex !== right.launchIndex
    || left.childSessionId !== right.childSessionId
    || left.status !== right.status
    || left.phase !== right.phase
    || left.agent !== right.agent
    || left.assignmentLabel !== right.assignmentLabel
    || left.modelLabel !== right.modelLabel
    || left.tool !== right.tool
    || left.toolActivitySummary !== right.toolActivitySummary
    || left.time !== right.time
    || left.terminal !== right.terminal) return false;

  if (options.comparePreview !== false
    && (left.previewKind !== right.previewKind || left.previewText !== right.previewText)) return false;

  const leftRunning = taskStatusKind(left) === "running" && !left.terminal;
  const rightRunning = taskStatusKind(right) === "running" && !right.terminal;
  if (leftRunning && rightRunning) return true;

  return left.launchStartedAtMs === right.launchStartedAtMs
    && left.currentToolStartedAtMs === right.currentToolStartedAtMs
    && left.elapsedMs === right.elapsedMs
    && left.currentToolMs === right.currentToolMs;
}

function TaskElapsedTime({ row }: { row: TaskToolRow }) {
  const running = taskStatusKind(row) === "running" && !row.terminal;
  const nowMs = useTaskElapsedNow(running);
  return <>{taskElapsedLabel(row, nowMs)}</>;
}

function taskPreviewLabel(row: TaskToolRow): string {
  const previewKind = row.previewKind.trim().toLowerCase();
  if (previewKind === 'reasoning') return 'thinking';
  return row.previewKind.trim() || 'live';
}

const TASK_CARD_HOVER_DEBOUNCE_MS = 180;

function taskChildModelsEqual(left: DesktopV3TaskChildViewModel | null, right: DesktopV3TaskChildViewModel | null): boolean {
  if (left === right) return true;
  if (!left || !right) return false;
  return Object.keys(left).every((key) => left[key as keyof DesktopV3TaskChildViewModel] === right[key as keyof DesktopV3TaskChildViewModel]);
}

function taskRowWithChildState(row: TaskToolRow, child: DesktopV3TaskChildViewModel | null): TaskToolRow {
  if (!child?.hydrated) return row;
  return {
    ...row,
    status: child.status || row.status,
    tool: child.currentTool || row.tool,
    toolActivitySummary: child.toolActivitySummary || row.toolActivitySummary,
    modelLabel: child.modelLabel || row.modelLabel,
    launchStartedAtMs: child.startedAt || row.launchStartedAtMs,
    elapsedMs: child.elapsedMs || row.elapsedMs,
    terminal: child.terminal,
    previewKind: child.error ? 'error' : row.previewKind,
    previewText: child.error || row.previewText,
  };
}

export function taskActivityLabel(row: TaskToolRow): string {
  const kind = taskStatusKind(row);
  if (kind === 'running' && row.toolActivitySummary?.trim()) return row.toolActivitySummary.trim();
  if (row.tool && row.tool !== '-') return row.tool;
  return taskStatusText(kind);
}

function taskContextLabel(child: DesktopV3TaskChildViewModel | null): string {
  if (!child?.hydrated || child.contextWindow <= 0 || child.remainingTokens === null) return 'Context unavailable';
  const used = Math.max(0, child.contextWindow - child.remainingTokens);
  return `${Math.round((used / child.contextWindow) * 100)}% context · ${child.remainingTokens.toLocaleString()} left`;
}

function TaskChildInteractiveRow({
  row,
  actions,
  className,
  children,
}: {
  row: TaskToolRow;
  actions?: TaskChildCardActions;
  className: string;
  children: (effectiveRow: TaskToolRow, child: DesktopV3TaskChildViewModel | null) => ReactNode;
}) {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const hoverTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const demandRef = useRef<DesktopV3RealtimeSessionDemandLease | null>(null);
  const [visible, setVisible] = useState(false);
  const [focused, setFocused] = useState(false);
  const [hovered, setHovered] = useState(false);
  const [stopPending, setStopPending] = useState(false);
  const [stopMessage, setStopMessage] = useState('');
  const selectChild = useCallback(
    (state: Parameters<typeof selectDesktopV3TaskChildViewModel>[0]) => selectDesktopV3TaskChildViewModel(state, row),
    [row],
  );
  const child = useDesktopV3CacheSelector(selectChild, taskChildModelsEqual);
  const effectiveRow = useMemo(() => taskRowWithChildState(row, child), [child, row]);
  const engaged = Boolean(actions && row.childSessionId.trim() && !effectiveRow.terminal && (visible || focused || hovered));
  const ownerKey = `${actions?.parentSessionId || 'parent'}:task-card:${row.launchKey || row.childSessionId}`;

  useEffect(() => {
    const node = rootRef.current;
    if (!node) return;
    if (typeof IntersectionObserver === 'undefined') {
      setVisible(true);
      return;
    }
    const observer = new IntersectionObserver((entries) => setVisible(entries.some((entry) => entry.isIntersecting)), { threshold: 0.01 });
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    if (!engaged) {
      demandRef.current?.release();
      demandRef.current = null;
      return;
    }
    let cancelled = false;
    void hydrateDesktopV3ChildCard(row.childSessionId).catch(() => undefined);
    void requireDesktopV3RealtimeControllerReady().then((controller) => {
      if (cancelled) return;
      demandRef.current?.release();
      demandRef.current = controller.acquireSessionDemand(ownerKey, row.childSessionId);
    }).catch(() => undefined);
    return () => {
      cancelled = true;
      demandRef.current?.release();
      demandRef.current = null;
    };
  }, [engaged, ownerKey, row.childSessionId]);

  useEffect(() => () => {
    if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
    demandRef.current?.release();
  }, []);

  const navigate = useCallback(() => {
    if (!actions || !row.childSessionId.trim()) return;
    actions.onNavigate(row.childSessionId, child?.workspacePath || '');
  }, [actions, child?.workspacePath, row.childSessionId]);

  const stop = useCallback(async (event: MouseEvent<HTMLButtonElement>) => {
    event.preventDefault();
    event.stopPropagation();
    if (!row.childSessionId.trim() || child?.terminal || stopPending) return;
    setStopPending(true);
    setStopMessage('Stopping…');
    try {
      await stopSubagentSessionV3Run(row.childSessionId);
      setStopMessage('Stop requested');
    } catch (error) {
      setStopMessage(error instanceof Error ? error.message : 'Stop failed');
    } finally {
      setStopPending(false);
    }
  }, [child?.terminal, row.childSessionId, stopPending]);

  const canNavigate = Boolean(actions && row.childSessionId.trim());
  const canStop = Boolean(row.childSessionId.trim() && child?.hydrated && !child.terminal);
  return (
    <div
      ref={rootRef}
      className={cn(className, canNavigate && 'cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--app-primary)]')}
      role={canNavigate ? 'link' : undefined}
      tabIndex={canNavigate ? 0 : undefined}
      onClick={navigate}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          navigate();
        }
      }}
      onFocusCapture={() => setFocused(true)}
      onBlurCapture={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setFocused(false);
      }}
      onPointerEnter={(event) => {
        if ((event.target as HTMLElement).closest('[data-task-stop]')) return;
        if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
        hoverTimerRef.current = setTimeout(() => setHovered(true), TASK_CARD_HOVER_DEBOUNCE_MS);
      }}
      onPointerLeave={() => {
        if (hoverTimerRef.current) clearTimeout(hoverTimerRef.current);
        hoverTimerRef.current = null;
        setHovered(false);
      }}
      data-task-child-row
      data-child-session-id={row.childSessionId || undefined}
    >
      {children(effectiveRow, child)}
      {row.childSessionId ? (
        <div className="task-card-child-context flex min-w-0 items-center gap-2 px-3 pb-2 text-[10px] text-[var(--app-text-subtle)]">
          <span className="min-w-0 truncate" title={taskContextLabel(child)}>{child?.loading ? 'Loading live state…' : child?.unavailable ? 'Session unavailable' : child?.stale ? 'Live state stale' : taskContextLabel(child)}</span>
          {child?.contextWindow && child.remainingTokens !== null ? (
            <span className="h-1 w-16 shrink-0 overflow-hidden rounded-full bg-[var(--app-border)]" aria-hidden="true">
              <span className="block h-full bg-[var(--app-primary)]" style={{ width: `${Math.max(0, Math.min(100, ((child.contextWindow - child.remainingTokens) / child.contextWindow) * 100))}%` }} />
            </span>
          ) : null}
          <button
            type="button"
            data-task-stop
            className="ml-auto inline-flex min-h-7 shrink-0 items-center gap-1 rounded-md border border-[var(--app-border)] px-2 text-[10px] font-medium text-[var(--app-text-muted)] opacity-100 transition hover:border-[var(--app-danger)] hover:text-[var(--app-danger)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)] sm:opacity-0 sm:group-hover:opacity-100 sm:group-focus-within:opacity-100"
            aria-label={`Stop ${row.assignmentLabel || row.agent || 'subagent'}`}
            title={canStop ? 'Stop subagent run' : 'No active resolvable child run'}
            disabled={!canStop || stopPending}
            onClick={stop}
            onPointerEnter={(event) => event.stopPropagation()}
          >
            <CircleStop size={12} aria-hidden="true" />
            <span className="sm:hidden">Stop</span>
          </button>
          {stopMessage ? <span role="status" className={cn('shrink-0', stopMessage.toLowerCase().includes('fail') ? 'text-[var(--app-danger)]' : '')}>{stopMessage}</span> : null}
        </div>
      ) : null}
    </div>
  );
}

function TaskAgentListRow({ row, index, dense, actions }: { row: TaskToolRow; index: number; dense: boolean; actions?: TaskChildCardActions }) {
  return (
    <TaskChildInteractiveRow
      row={row}
      actions={actions}
      className="group min-w-0 border-t border-[var(--app-border)] transition-colors hover:bg-[color-mix(in_srgb,var(--app-text-muted)_5%,transparent)]"
    >
      {(effectiveRow) => <TaskAgentListRowContent row={effectiveRow} index={index} dense={dense} />}
    </TaskChildInteractiveRow>
  );
}

function TaskNarrowRowContent({ row, index }: { row: TaskToolRow; index: number }) {
  const kind = taskStatusKind(row);
  const rowNumber = row.launchIndex || index + 1;
  const detail = taskActivityLabel(row);

  return (
    <div className="task-card-narrow-only min-w-0 items-center gap-2 px-2 py-2">
      <span className={cn("size-2 shrink-0 rounded-full", taskStatusDotClass(kind))} aria-hidden="true" />
      <span className="min-w-0 flex-1 truncate text-[11px] font-semibold text-[var(--app-text)]">
        Subagent {rowNumber}
      </span>
      <span className="task-card-narrow-detail min-w-0 truncate font-mono text-[10px] text-[var(--app-text-muted)]">
        {detail}
      </span>
      <span className={cn("task-card-narrow-detail shrink-0 font-mono text-[10px] tabular-nums", taskStatusTextClass(kind))}>
        <TaskElapsedTime row={row} />
      </span>
    </div>
  );
}

function TaskAgentListRowContent({ row, index, dense }: { row: TaskToolRow; index: number; dense: boolean }) {
  const kind = taskStatusKind(row);
  const statusLabel = taskStatusLabel(row);
  const primaryLabel = row.assignmentLabel || row.agent || 'subagent';
  const displayAgent = displayAgentName(row.agent);
  const agentLabel = displayAgent && row.assignmentLabel ? `@${displayAgent}` : displayAgent;
  const secondaryLabel = [agentLabel, row.modelLabel].filter(Boolean).join(' · ');
  const toolLabel = taskActivityLabel(row);
  const errorText = row.status.trim().toLowerCase() === 'failed' || row.status.trim().toLowerCase() === 'error' ? row.previewText.trim() : '';
  const previewText = errorText ? '' : row.previewText.trim();
  const rowNumber = row.launchIndex || index + 1;

  return (
    <div className={cn(kind === "running" ? "bg-[color-mix(in_srgb,var(--app-primary)_5%,transparent)]" : "")}>
      <TaskNarrowRowContent row={row} index={index} />
      <div className={cn(
        "task-card-wide-only",
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
          <div className="min-w-0 break-words text-[12px] font-semibold text-[var(--app-text)] [overflow-wrap:anywhere]" title={primaryLabel}>
            {primaryLabel}
          </div>
          {secondaryLabel ? (
            <div className="mt-0.5 min-w-0 break-words text-[10px] text-[var(--app-text-subtle)] [overflow-wrap:anywhere]" title={secondaryLabel}>
              {secondaryLabel}
            </div>
          ) : null}
        </div>
        <div className="col-start-2 col-span-2 row-start-2 min-w-0 break-words font-mono text-[11px] text-[var(--app-text-muted)] [overflow-wrap:anywhere] sm:col-start-4 sm:col-span-1 sm:row-start-1" title={toolLabel}>
          {toolLabel}
        </div>
        <div className={cn("col-start-3 row-start-1 min-w-[4.75rem] text-right font-mono text-[11px] tabular-nums sm:col-start-5", taskStatusTextClass(kind))}>
          <TaskElapsedTime row={row} />
        </div>
      </div>
      {(previewText || errorText) && !dense ? (
        <div className="task-card-wide-only grid min-w-0 grid-cols-[3.25rem_minmax(0,1fr)] gap-x-2 px-3 pb-2 sm:grid-cols-[2.5rem_3.75rem_minmax(0,1fr)] sm:gap-x-3">
          <div />
          <div className="hidden sm:block" />
          <div className={cn("col-start-2 min-w-0 break-words border-l pl-2 text-[11px] leading-4 [overflow-wrap:anywhere] sm:col-start-3", errorText ? "border-[var(--app-danger)] text-[var(--app-danger)]" : "border-[var(--app-border)] text-[var(--app-text-subtle)]")}>
            <span className="mr-1 font-mono uppercase tracking-[0.08em] text-[9px]">
              {errorText ? 'error' : taskPreviewLabel(row)}:
            </span>
            {errorText || previewText}
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

function TaskRowsHeader({ counts }: { counts: ReturnType<typeof taskRowsCounts> }) {
  return (
    <div className="flex min-w-0 flex-wrap items-center justify-between gap-2 border-b border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-bg-alt)_72%,transparent)] px-3 py-2" data-task-card-header>
      <div className="flex min-w-0 items-center gap-2">
        <span className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-[color-mix(in_srgb,var(--app-primary)_28%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-primary)_10%,transparent)] text-[var(--app-primary)]">
          <Bot size={14} aria-hidden="true" />
        </span>
        <span className="break-words text-xs font-bold uppercase tracking-[0.12em] text-[var(--app-text)] [overflow-wrap:anywhere]">
          Subagent stream
        </span>
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

function TaskSwarmCompactRow({ row, index, actions }: { row: TaskToolRow; index: number; actions?: TaskChildCardActions }) {
  return (
    <TaskChildInteractiveRow
      row={row}
      actions={actions}
      className="group min-w-0 rounded-lg border border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-bg-alt)_34%,transparent)]"
    >
      {(effectiveRow) => <TaskSwarmCompactRowContent row={effectiveRow} index={index} />}
    </TaskChildInteractiveRow>
  );
}

function TaskSwarmCompactRowContent({ row, index }: { row: TaskToolRow; index: number }) {
  const kind = taskStatusKind(row);
  const statusLabel = taskStatusLabel(row);
  const rowNumber = row.launchIndex || index + 1;
  const agent = displayAgentName(row.agent) || 'subagent';
  const agentLabel = agent.startsWith('@') ? agent : `@${agent}`;
  const toolLabel = taskActivityLabel(row);
  const title = row.assignmentLabel && row.assignmentLabel !== row.agent ? row.assignmentLabel : '';

  return (
    <div className={cn(
      "min-w-0",
      kind === "running" ? "border-[color-mix(in_srgb,var(--app-primary)_34%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-primary)_6%,transparent)]" : "",
    )}>
      <TaskNarrowRowContent row={row} index={index} />
      <div className="task-card-wide-only px-2 py-1.5">
      <div className="flex min-w-0 items-center gap-1.5">
        <span className="shrink-0 font-mono text-[10px] text-[var(--app-text-subtle)] tabular-nums">
          {rowNumber.toString().padStart(2, '0')}
        </span>
        <span className={cn("inline-flex h-5 shrink-0 items-center gap-1 rounded-md border px-1.5 font-mono text-[9px] font-bold tracking-[0.08em]", taskStatusBadgeClass(kind))}>
          <span className={cn("h-1.5 w-1.5 rounded-full bg-current", kind === "running" ? "animate-pulse" : "opacity-70")} />
          {statusLabel}
        </span>
        <span className="min-w-0 break-words text-[11px] font-semibold text-[var(--app-text)] [overflow-wrap:anywhere]">
          {agentLabel}
        </span>
        <span className="min-w-0 break-words font-mono text-[10px] text-[var(--app-text-muted)] [overflow-wrap:anywhere]">
          {toolLabel}
        </span>
        <span className={cn("ml-auto min-w-[4.25rem] shrink-0 text-right font-mono text-[10px] tabular-nums", taskStatusTextClass(kind))}>
          <TaskElapsedTime row={row} />
        </span>
      </div>
      {title ? (
        <div className="mt-1 min-w-0 break-words pl-[4.8rem] text-[10px] leading-4 text-[var(--app-text-subtle)] [overflow-wrap:anywhere] sm:pl-[5.25rem]">
          {title}
        </div>
      ) : null}
      </div>
    </div>
  );
}

const MemoizedTaskAgentListRow = memo(TaskAgentListRow, (previous, next) => (
  previous.index === next.index
  && previous.dense === next.dense
  && previous.actions === next.actions
  && taskRowsEqual(previous.row, next.row)
));

const MemoizedTaskSwarmCompactRow = memo(TaskSwarmCompactRow, (previous, next) => (
  previous.index === next.index
  && previous.actions === next.actions
  && taskRowsEqual(previous.row, next.row, { comparePreview: false })
));

function TaskSwarmRowsView({ rows, actions }: { rows: TaskToolRow[]; actions?: TaskChildCardActions }) {
  const counts = taskRowsCounts(rows);
  return (
    <div className="task-card-container min-w-0 overflow-hidden" data-task-card data-task-rows>
      <TaskRowsHeader counts={counts} />
      <div className={cn(TOOL_RESULT_BODY_CLASS, "task-card-swarm-grid grid grid-cols-1 gap-1.5 p-2 md:grid-cols-2 xl:grid-cols-3")}>
        {rows.map((row, index) => (
          <MemoizedTaskSwarmCompactRow
            key={taskRowKey(row, index)}
            row={row}
            index={index}
            actions={actions}
          />
        ))}
      </div>
    </div>
  );
}

function TaskAgentRowsView({ rows, actions }: { rows: TaskToolRow[]; actions?: TaskChildCardActions }) {
  const counts = taskRowsCounts(rows);
  const dense = rows.length >= 50;

  return (
    <div className="task-card-container min-w-0 overflow-hidden" data-task-card data-task-rows>
      <TaskRowsHeader counts={counts} />
      <div className="task-card-column-header hidden min-w-0 grid-cols-[2.5rem_3.75rem_minmax(0,1.5fr)_minmax(0,0.9fr)_4.75rem] items-center gap-x-3 border-b border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-bg-alt)_46%,transparent)] px-3 py-1.5 text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)] sm:grid">
        <div className="min-w-0 font-mono tabular-nums">#</div>
        <div className="min-w-0">Status</div>
        <div className="min-w-0">Subagent</div>
        <div className="min-w-0">Current</div>
        <div className="min-w-0 text-right">Time</div>
      </div>
      <div className={TOOL_RESULT_BODY_CLASS}>
        {rows.map((row, index) => (
          <MemoizedTaskAgentListRow
            key={taskRowKey(row, index)}
            row={row}
            index={index}
            dense={dense}
            actions={actions}
          />
        ))}
      </div>
    </div>
  );
}

function TaskRowsView({ rows, actions }: { rows: TaskToolRow[]; actions?: TaskChildCardActions }) {
  if (rows.length === 0) return null;
  if (rows.length > TASK_SWARM_THRESHOLD) return <TaskSwarmRowsView rows={rows} actions={actions} />;
  return <TaskAgentRowsView rows={rows} actions={actions} />;
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
    <div className="mt-1 flex min-w-0 items-baseline gap-1 text-[11px] leading-5 text-[var(--app-text-subtle)]">
      <span className="shrink-0">{summary}</span>
      {data.path ? <span className="min-w-0 truncate" title={data.path}> · {data.path}</span> : null}
    </div>
  );
}

function compactSearchPreview(value: string, maxLength = 240): string {
  const compact = value.replace(/\s+/g, " ").trim();
  return compact.length > maxLength ? `${compact.slice(0, maxLength - 1)}…` : compact;
}

function SearchLineList({ group }: { group: SearchToolLineGroup }) {
  const displayMatches = group.matches.length > 0;
  const items = displayMatches ? group.matches : group.lines.map((line) => ({ line, column: 0, text: "" }));

  return (
    <div className="min-w-0 text-[11px] leading-4 text-[var(--app-text-muted)]">
      {group.query ? (
        <div className="mb-1 truncate font-sans text-[10px] font-medium text-[var(--app-text-subtle)]" title={group.query}>
          {group.query}
        </div>
      ) : null}
      {items.length > 0 ? (
        <div className="divide-y divide-[var(--app-border)] overflow-hidden rounded-md border border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-surface)_72%,transparent)]">
          {items.map((item, index) => {
            const location = item.line > 0 ? `${item.line}${item.column > 0 ? `:${item.column}` : ""}` : "";
            const preview = compactSearchPreview(item.text || (item.line > 0 ? "line match" : "file match"));
            return (
              <div key={`${item.line}:${item.column}:${index}`} className="grid min-w-0 grid-cols-[3.75rem_minmax(0,1fr)] gap-2 px-2 py-1">
                <span className="select-none text-right font-mono text-[10px] tabular-nums text-[var(--app-text-subtle)]">
                  {location}
                </span>
                <span className="line-clamp-2 min-w-0 break-all font-mono text-[var(--app-text-muted)]" title={preview}>
                  <ToolSyntaxLine text={preview} language={inferToolSyntaxLanguage(group.query)} />
                </span>
              </div>
            );
          })}
        </div>
      ) : (
        <div className="font-mono text-[var(--app-text-subtle)]">file match</div>
      )}
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
  return (
    <section className="min-w-0 overflow-hidden rounded-lg border border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-surface)_62%,transparent)]">
      <div className="flex min-w-0 items-baseline gap-2 border-b border-[var(--app-border)] px-2.5 py-1.5 text-[11px]">
        <span className="min-w-0 flex-1 truncate font-mono font-medium text-[var(--app-text)]" title={file.path}>
          {file.path}
        </span>
        <span className="shrink-0 text-[10px] text-[var(--app-text-subtle)]">
          {mode === "files"
            ? `${file.matchCount} ${file.matchCount === 1 ? "hit" : "hits"}`
            : `${file.matchCount} ${file.matchCount === 1 ? "match" : "matches"}`}
        </span>
      </div>
      <div className="grid gap-1.5 p-1.5">
        {file.queryGroups.map((group, index) => (
          <SearchLineList
            key={`${file.path}:${group.query}:${index}`}
            group={group}
          />
        ))}
      </div>
    </section>
  );
}

function toolJsonString(record: Record<string, unknown> | null | undefined, key: string): string {
  const value = record?.[key];
  return typeof value === "string" ? value.trim() : "";
}

interface ManageSessionNavigation {
  sessionId: string;
  href: string;
}

interface ManageSessionCardItem {
  id: string;
  title: string;
  state: string;
  workspace: string;
  branch: string;
  messageCount: number | null;
  updatedAt: number | null;
  navigation: ManageSessionNavigation | null;
  snippet: string;
  gitSummary: string;
  reviewKind: "follow_up" | "archive_candidate" | "";
  clean: boolean | null;
  dirtyCount: number | null;
  missingCommitCount: number | null;
  missingCommitSubjects: string[];
  missingCommitsTruncated: boolean;
}

function manageSessionNumber(record: Record<string, unknown>, key: string): number | null {
  const value = record[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function manageSessionItem(value: unknown): ManageSessionCardItem | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const id = toolJsonString(record, "id") || toolJsonString(record, "session_id");
  const title = toolJsonString(record, "title") || "Untitled session";
  const rawSnippets = Array.isArray(record.snippets) ? record.snippets : [];
  const snippet = rawSnippets.map((entry) => {
    if (typeof entry === "string") return entry;
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) return "";
    const item = entry as Record<string, unknown>;
    return toolJsonString(item, "text") || toolJsonString(item, "snippet");
  }).find(Boolean) || "";
  const cleanState = typeof record.clean === "boolean" ? record.clean : null;
  const dirtyCount = manageSessionNumber(record, "dirty_count");
  const clean = cleanState !== null ? (cleanState ? "Clean" : `${dirtyCount ?? 0} changes`) : "";
  const ahead = manageSessionNumber(record, "ahead");
  const behind = manageSessionNumber(record, "behind");
  const gitSummary = [clean, ahead ? `${ahead} ahead` : "", behind ? `${behind} behind` : ""].filter(Boolean).join(" · ");
  return {
    id,
    title,
    state: toolJsonString(record, "state") || toolJsonString(record, "status"),
    workspace: toolJsonString(record, "workspace_name") || toolJsonString(record, "workspace_path"),
    branch: toolJsonString(record, "worktree_branch") || toolJsonString(record, "branch"),
    messageCount: manageSessionNumber(record, "message_count"),
    updatedAt: manageSessionNumber(record, "updated_at"),
    navigation: manageSessionNavigation(record.navigation),
    snippet,
    gitSummary,
    reviewKind: toolJsonString(record, "classification") === "follow_up" ? "follow_up" : toolJsonString(record, "classification") === "archive_candidate" ? "archive_candidate" : "",
    clean: cleanState,
    dirtyCount,
    missingCommitCount: manageSessionNumber(record, "missing_commit_count"),
    missingCommitSubjects: (Array.isArray(record.missing_commits) ? record.missing_commits : []).flatMap((value) => {
      if (!value || typeof value !== "object" || Array.isArray(value)) return [];
      const subject = compactSearchPreview(toolJsonString(value as Record<string, unknown>, "subject"), 120);
      return subject ? [subject] : [];
    }),
    missingCommitsTruncated: record.missing_commits_truncated === true,
  };
}

function manageSessionDate(value: number | null): string {
  if (!value) return "";
  const milliseconds = value < 10_000_000_000 ? value * 1000 : value;
  const date = new Date(milliseconds);
  return Number.isNaN(date.getTime()) ? "" : date.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

function manageSessionNavigation(record: unknown): ManageSessionNavigation | null {
  if (!record || typeof record !== "object" || Array.isArray(record)) return null;
  const navigation = record as Record<string, unknown>;
  const sessionId = toolJsonString(navigation, "session_id");
  const href = toolJsonString(navigation, "href");
  if (!sessionId || !/^\/[a-z0-9][a-z0-9-]*\/[^/?#]+(?:[?#].*)?$/i.test(href)) return null;
  return { sessionId, href };
}

function ReviewWorktreeRow({ item }: { item: ManageSessionCardItem }) {
  const missingCount = item.missingCommitCount ?? 0;
  const cleanliness = item.clean === true
    ? "Clean"
    : item.clean === false
      ? `Dirty${item.dirtyCount !== null ? ` · ${item.dirtyCount} ${item.dirtyCount === 1 ? "change" : "changes"}` : ""}`
      : "Cleanliness unknown";
  const missingLabel = `${missingCount} missing ${missingCount === 1 ? "commit" : "commits"}`;
  const subjectSuffix = item.missingCommitsTruncated || missingCount > item.missingCommitSubjects.length ? " …" : "";

  return (
    <article className="min-w-0 border-t border-[var(--app-border)] px-3 py-2 first:border-t-0">
      <div className="flex min-w-0 items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="truncate text-[12px] font-semibold text-[var(--app-text)]" title={item.title}>{item.title}</div>
          <div className="mt-0.5 flex min-w-0 items-center gap-1 text-[10px] text-[var(--app-text-muted)]">
            <GitBranch size={10} className="shrink-0" />
            <span className="truncate font-mono" title={item.branch}>{item.branch || "Unknown branch"}</span>
          </div>
        </div>
        <span className={cn(
          "shrink-0 rounded-full px-2 py-0.5 text-[9px] font-semibold",
          item.reviewKind === "archive_candidate"
            ? "bg-[color-mix(in_srgb,var(--app-success)_12%,transparent)] text-[var(--app-success)]"
            : "bg-[color-mix(in_srgb,var(--app-warning)_12%,transparent)] text-[var(--app-warning)]",
        )}>
          {item.reviewKind === "archive_candidate" ? "Archive ready" : "Follow up"}
        </span>
      </div>
      <div className="mt-1 flex min-w-0 flex-wrap gap-x-3 gap-y-0.5 text-[10px] text-[var(--app-text-subtle)]">
        <span>{cleanliness}</span>
        <span>{missingLabel}</span>
      </div>
      {item.missingCommitSubjects.length > 0 ? (
        <p className="mt-1 line-clamp-2 min-w-0 break-words text-[10px] leading-4 text-[var(--app-text-muted)] [overflow-wrap:anywhere]" title={item.missingCommitSubjects.join(" · ")}>
          {item.missingCommitSubjects.join(" · ")}{subjectSuffix}
        </p>
      ) : null}
      {item.navigation ? (
        <a href={item.navigation.href} className="mt-1.5 inline-flex items-center gap-1 text-[10px] font-semibold text-[var(--app-primary)] hover:underline" title={item.navigation.href}>
          Open session <ArrowRight size={10} />
        </a>
      ) : null}
    </article>
  );
}

function ManageSessionsCard({ toolMessage }: { toolMessage: StructuredToolMessage }) {
  const output = toolMessage.outputJson ?? parseToolJSON(toolMessage.output) ?? parseToolJSON(toolMessage.completedOutput);
  if (!output) return null;
  const args = toolMessage.argumentsJson ?? null;
  const action = toolJsonString(output, "action") || toolJsonString(args, "action") || "sessions";
  const deployResults = Array.isArray(output.results) ? output.results : [];
  const followUpCandidates = Array.isArray(output.follow_up_candidates) ? output.follow_up_candidates : [];
  const archiveCandidates = Array.isArray(output.archive_candidates) ? output.archive_candidates : [];
  const reviewCandidates = action === "review_worktrees" ? [...followUpCandidates, ...archiveCandidates] : [];
  const rawItems = reviewCandidates.length > 0 ? reviewCandidates : Array.isArray(output.items) ? output.items : deployResults.length ? deployResults : output.id || output.session_id ? [output] : [];
  const items = rawItems.map(manageSessionItem).filter((item): item is ManageSessionCardItem => Boolean(item));
  const isReviewWorktrees = action === "review_worktrees";
  const failedDeployments = action === "deploy" ? deployResults.flatMap((value): Array<{ proposal: string; error: string }> => {
    if (!value || typeof value !== "object" || Array.isArray(value)) return [];
    const record = value as Record<string, unknown>;
    const error = toolJsonString(record, "error");
    if (!error) return [];
    return [{ proposal: toolJsonString(record, "proposal_id") || toolJsonString(record, "title") || "Session", error }];
  }) : [];
  const messages = Array.isArray(output.messages) ? output.messages : [];
  const durableEvents = Array.isArray(output.events) ? output.events : [];
  const isDurableLogSearch = action === "search" && toolJsonString(output, "search_mode") === "durable_log";
  const archivedIds = Array.isArray(output.archived_session_ids) ? output.archived_session_ids.filter((id): id is string => typeof id === "string") : [];
  const commits = Array.isArray(output.commits) ? output.commits.flatMap((entry) => {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) return [];
    const commit = entry as Record<string, unknown>;
    return [{ sessionId: typeof commit.session_id === "string" ? commit.session_id : "", message: typeof commit.message === "string" ? commit.message : "", hash: typeof commit.commit_hash === "string" ? commit.commit_hash : "", files: Array.isArray(commit.files) ? commit.files.filter((file): file is string => typeof file === "string") : [] }];
  }) : [];
  const unarchivedIds = Array.isArray(output.unarchived_session_ids) ? output.unarchived_session_ids.filter((id): id is string => typeof id === "string") : [];
  const title = action === "deploy" ? "Session deployment" : isDurableLogSearch ? "Durable event-log search" : action === "search" ? "Session search" : action === "list" ? "Your sessions" : action === "read_messages" ? "Session context" : action === "git_status" ? "Worktree status" : action === "review_worktrees" ? "Worktrees needing review" : action === "archive" ? "Sessions archived" : action === "unarchive" ? "Sessions unarchived" : action === "commit" ? "Session commits ready for testing" : action === "inspect" ? "Session manager" : "Session details";
  const HeaderIcon = action === "search" ? Search : action === "archive" || action === "unarchive" ? Archive : action === "read_messages" ? MessageSquareText : action === "git_status" || action === "review_worktrees" ? GitBranch : Layers3;
  const needsReviewCount = manageSessionNumber(output, "needs_review_count");
  const worktreeSessionCount = manageSessionNumber(output, "worktree_session_count");
  const followUpCount = manageSessionNumber(output, "follow_up_candidate_count") ?? followUpCandidates.length;
  const archiveCount = manageSessionNumber(output, "archive_candidate_count") ?? archiveCandidates.length;
  const inspectionErrorCount = manageSessionNumber(output, "inspection_error_count");
  const reviewSummary = [
    needsReviewCount !== null ? `${needsReviewCount} total` : "",
    worktreeSessionCount !== null ? `${worktreeSessionCount} worktrees` : "",
    `${followUpCount} follow up`,
    `${archiveCount} archive ready`,
    inspectionErrorCount ? `${inspectionErrorCount} errors` : "",
  ].filter(Boolean).join(" · ");
  const headerSummary = isReviewWorktrees
    ? reviewSummary
    : isDurableLogSearch
      ? `${durableEvents.length} ${durableEvents.length === 1 ? "event" : "events"} · durable V3 log`
      : items.length
        ? `${items.length} ${items.length === 1 ? "session" : "sessions"}`
        : action.split("_").join(" ");

  return (
    <section className="mt-2 min-w-0 max-w-full overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[linear-gradient(145deg,color-mix(in_srgb,var(--app-primary)_8%,var(--app-surface)),var(--app-surface)_45%)] shadow-[0_12px_35px_rgba(0,0,0,0.08)]">
      <header className="flex items-center justify-between gap-3 border-b border-[var(--app-border)] px-4 py-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <span className="grid h-8 w-8 shrink-0 place-items-center rounded-xl bg-[color-mix(in_srgb,var(--app-primary)_15%,transparent)] text-[var(--app-primary)]"><HeaderIcon size={15} /></span>
          <div className="min-w-0"><h4 className="truncate text-sm font-semibold text-[var(--app-text)]">{title}</h4><p className="truncate text-[11px] text-[var(--app-text-subtle)]" title={headerSummary}>{headerSummary}</p></div>
        </div>
        {output.has_more === true ? <span className="rounded-full border border-[var(--app-border)] px-2 py-1 text-[10px] font-medium text-[var(--app-text-muted)]">More available</span> : null}
      </header>
      <div className={TOOL_RESULT_BODY_CLASS}>
      {items.length > 0 ? <div className={isReviewWorktrees ? "min-w-0 divide-y divide-[var(--app-border)]" : "grid min-w-0 gap-2 p-2.5"}>{items.map((item, index) => isReviewWorktrees ? (
        <ReviewWorktreeRow key={item.id || `${item.title}-${index}`} item={item} />
      ) : (
        <article key={item.id || `${item.title}-${index}`} className="group min-w-0 rounded-xl border border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-surface)_88%,transparent)] p-3 transition hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)]">
          <div className="flex min-w-0 items-start justify-between gap-3">
            <div className="min-w-0"><div className="truncate text-[13px] font-semibold text-[var(--app-text)]">{item.title}</div>{item.id ? <div className="mt-0.5 truncate font-mono text-[9px] text-[var(--app-text-subtle)]">{item.id}</div> : null}</div>
            {item.state ? <span className="shrink-0 rounded-full bg-[color-mix(in_srgb,var(--app-primary)_10%,transparent)] px-2 py-1 text-[10px] font-medium capitalize text-[var(--app-text-muted)]">{item.state.split("_").join(" ")}</span> : null}
          </div>
          {item.snippet ? <p className="mt-2 line-clamp-2 text-xs leading-5 text-[var(--app-text-muted)]">{item.snippet}</p> : null}
          <div className="mt-2 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-[10px] text-[var(--app-text-subtle)]">
            {item.workspace ? <span className="max-w-56 truncate">{item.workspace}</span> : null}{item.branch ? <span className="inline-flex items-center gap-1"><GitBranch size={10} />{item.branch}</span> : null}{item.messageCount !== null ? <span className="inline-flex items-center gap-1"><MessageSquareText size={10} />{item.messageCount}</span> : null}{item.updatedAt ? <span className="inline-flex items-center gap-1"><Clock3 size={10} />{manageSessionDate(item.updatedAt)}</span> : null}{item.gitSummary ? <span>{item.gitSummary}</span> : null}
          </div>
          {item.navigation ? <a href={item.navigation.href} className="mt-2.5 inline-flex items-center gap-1.5 text-xs font-semibold text-[var(--app-primary)] hover:underline" title={item.navigation.href}>Open session <ArrowRight size={12} /></a> : null}
        </article>
      ))}</div> : null}
      {durableEvents.length > 0 ? <div className="min-w-0 space-y-2 p-3">{durableEvents.map((value, index) => {
        const event = value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
        const seq = manageSessionNumber(event, "seq");
        const eventType = toolJsonString(event, "event_type") || "event";
        const payload = typeof event.payload === "string" ? event.payload : JSON.stringify(event.payload ?? null);
        return <div key={`${seq ?? index}`} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3"><div className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-[var(--app-primary)]">{eventType}{seq !== null ? ` · #${seq}` : ""}</div><pre className="min-w-0 whitespace-pre-wrap break-words font-mono text-[10px] leading-4 text-[var(--app-text-muted)] [overflow-wrap:anywhere]">{payload}</pre></div>;
      })}</div> : null}
      {isDurableLogSearch ? <div className="border-t border-[var(--app-border)] px-3 py-2 text-[10px] text-[var(--app-text-subtle)]">Source: durable V3 session events{output.scan_truncated === true ? " · scan truncated" : ""}{output.character_truncated === true ? " · character limit reached" : ""}{output.result_truncated === true ? " · result limit reached" : ""}</div> : null}
      {messages.length > 0 ? <div className="min-w-0 space-y-2 p-3">{messages.map((value, index) => {
        const message = value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
        const role = toolJsonString(message, "role") || "message";
        const content = toolJsonString(message, "content");
        const seq = manageSessionNumber(message, "seq");
        return <div key={`${seq ?? index}`} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3"><div className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-[var(--app-text-subtle)]">{role}{seq !== null ? ` · #${seq}` : ""}</div><div className="min-w-0 whitespace-pre-wrap break-words text-xs leading-5 text-[var(--app-text-muted)] [overflow-wrap:anywhere]">{content}</div></div>;
      })}</div> : null}
      {failedDeployments.length > 0 ? <div className="grid gap-2 border-t border-[var(--app-border)] p-3">{failedDeployments.map((failure, index) => <div key={`${failure.proposal}:${index}`} className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]"><span className="font-semibold">{failure.proposal} failed:</span> {failure.error}</div>)}</div> : null}
      {archivedIds.length > 0 ? <div className="p-4 text-xs text-[var(--app-text-muted)]">Archived {archivedIds.length} {archivedIds.length === 1 ? "session" : "sessions"} durably.</div> : null}
      {commits.length > 0 ? <div className="grid min-w-0 gap-2 border-t border-[var(--app-border)] p-3">{commits.map((commit, index) => <article key={`${commit.hash}:${index}`} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-xs"><div className="font-semibold text-[var(--app-text)]">{commit.message || `Commit ${index + 1}`}</div><div className="mt-1 font-mono text-[var(--app-text-muted)]">{commit.hash}</div>{commit.files.length > 0 ? <div className="mt-2 text-[var(--app-text-muted)]">{commit.files.length} changed {commit.files.length === 1 ? "file" : "files"} · session remains in needs review</div> : null}</article>)}</div> : null}
      {unarchivedIds.length > 0 ? <div className="p-4 text-xs text-[var(--app-text-muted)]">Unarchived {unarchivedIds.length} {unarchivedIds.length === 1 ? "session" : "sessions"} durably.</div> : null}
      {action === "inspect" ? <div className="p-4 text-xs leading-5 text-[var(--app-text-muted)]">Search and read are bounded for efficient context. Archive and unarchive require approval.</div> : null}
      </div>
    </section>
  );
}

function shouldRenderPreviewAsPlain(toolName: string): boolean {
  switch (toolName.trim().toLowerCase()) {
    case "manage_todos":
    case "manage-todos":
    case "manage-sessions":
    case "manage_sessions":
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

function planTransitionLabel(action: string): string {
  switch (action.trim().toLowerCase().replace(/-/g, "_")) {
    case "start_session_checkpoint":
    case "start_checkpoint":
      return "Checkpoint started";
    case "continue_checkpoint":
      return "Checkpoint continuing";
    case "restart_checkpoint":
      return "Checkpoint restarted";
    case "complete_subtask":
      return "Task completed";
    case "complete_checkpoint":
    case "checkpoint_outcome":
      return "Checkpoint completed";
    case "mark_needs_review":
      return "Ready for review";
    case "mark_blocked":
      return "Checkpoint blocked";
    case "mark_failed":
      return "Checkpoint failed";
    case "approve_and_start":
      return "Plan approved";
    case "request_followup_checkpoint":
      return "Checkpoint added";
    default:
      return action ? action.replace(/[-_]+/g, " ") : "Plan updated";
  }
}

function planTransitionStatus(payload: Record<string, unknown> | null): string {
  if (!payload) return "";
  const summary = payload.execution_summary && typeof payload.execution_summary === "object" && !Array.isArray(payload.execution_summary)
    ? payload.execution_summary as Record<string, unknown>
    : null;
  if (!summary) return toolJsonString(payload, "status");
  if (summary.review_required === true) return "Waiting review";
  if (summary.blocked === true) return "Blocked";
  if (summary.failed === true) return "Failed";
  if (summary.plan_complete === true) return "Complete";
  return toolJsonString(summary, "next_checkpoint_status") || toolJsonString(summary, "next_action") || toolJsonString(payload, "status");
}

function ExitPlanModeToolView({ toolMessage }: { toolMessage: StructuredToolMessage }) {
  const payload = toolMessage.outputJson ?? parseToolJSON(toolMessage.output) ?? parseToolJSON(toolMessage.completedOutput);
  const args = toolMessage.argumentsJson ?? parseToolJSON(toolMessage.argumentsText);
  const title = toolJsonString(payload, "title") || toolJsonString(args, "title") || "Approved plan";
  const targetMode = toolJsonString(payload, "target_mode") || "auto";
  const summary = payload?.execution_summary && typeof payload.execution_summary === "object" && !Array.isArray(payload.execution_summary)
    ? payload.execution_summary as Record<string, unknown>
    : null;
  const checkpointId = toolJsonString(summary, "active_checkpoint_id") || toolJsonString(summary, "next_checkpoint_id") || toolJsonString(payload, "checkpoint_id");
  const nextStatus = toolJsonString(summary, "next_checkpoint_status").replace(/[-_]+/g, " ");
  const transitioned = payload?.mode_changed === true || toolJsonString(payload, "status").toLowerCase() === "approved";
  const isRunning = toolMessage.state === "running" && !transitioned;
  const transitionLabel = isRunning ? "Approving plan…" : transitioned ? "Plan approved" : "Plan transition";
  const modeLabel = `${targetMode.charAt(0).toUpperCase()}${targetMode.slice(1)} mode`;

  return (
    <div className="mb-2 w-full min-w-0 py-1.5" data-exit-plan-mode-transition>
      <div className="w-full min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5">
        <div className="flex min-w-0 items-center gap-2.5">
          <span className="grid h-6 w-6 shrink-0 place-items-center rounded-md bg-[var(--app-success-bg)] text-[var(--app-success)]">
            {isRunning ? <LoaderCircle size={13} className="animate-spin" /> : <CheckCircle2 size={13} />}
          </span>
          <div className="min-w-0 flex-1">
            <div className="text-[13px] font-semibold leading-5 text-[var(--app-text)]">{transitionLabel}</div>
            <div className="truncate text-[11px] leading-4 text-[var(--app-text-muted)]" title={title}>{title}</div>
          </div>
          <ArrowRight size={14} className="shrink-0 text-[var(--app-text-subtle)]" aria-hidden="true" />
          <div className="flex shrink-0 items-center gap-1.5 text-[var(--app-primary)]">
            <CircleDot size={12} aria-hidden="true" />
            <span className="text-[11px] font-medium">{modeLabel}</span>
          </div>
        </div>
        {!isRunning && (checkpointId || nextStatus) ? (
          <div className="mt-2 border-t border-[color-mix(in_srgb,var(--app-border)_75%,transparent)] pt-2 text-[10px] text-[var(--app-text-subtle)]">
            Execution continues{checkpointId ? <> with <span className="font-mono text-[var(--app-text-muted)]">{checkpointId}</span></> : null}{nextStatus ? ` · ${nextStatus}` : ""}
          </div>
        ) : null}
      </div>
    </div>
  );
}

function PlanManageToolView({ toolMessage }: { toolMessage: StructuredToolMessage }) {
  const payload = toolMessage.outputJson ?? parseToolJSON(toolMessage.output) ?? parseToolJSON(toolMessage.completedOutput);
  const args = toolMessage.argumentsJson ?? parseToolJSON(toolMessage.argumentsText);
  const action = toolJsonString(payload, "action") || toolJsonString(args, "action");
  const summary = payload?.execution_summary && typeof payload.execution_summary === "object" && !Array.isArray(payload.execution_summary)
    ? payload.execution_summary as Record<string, unknown>
    : null;
  const plan = payload?.plan && typeof payload.plan === "object" && !Array.isArray(payload.plan)
    ? payload.plan as Record<string, unknown>
    : null;
  const document = plan?.document && typeof plan.document === "object" && !Array.isArray(plan.document)
    ? plan.document as Record<string, unknown>
    : null;
  const checkpointId = toolJsonString(summary, "active_checkpoint_id")
    || toolJsonString(summary, "next_checkpoint_id")
    || toolJsonString(args, "checkpoint_id")
    || toolJsonString(document, "active_checkpoint_id");
  const title = toolJsonString(plan, "title") || toolJsonString(payload, "title");
  const status = planTransitionStatus(payload);
  const normalizedStatus = status.trim().toLowerCase().replace(/[-_]+/g, " ");
  const tone = toolMessage.state === "error" || normalizedStatus === "failed"
    ? "danger"
    : normalizedStatus === "blocked"
      ? "warning"
      : normalizedStatus === "complete" || normalizedStatus === "completed"
        ? "success"
        : "primary";
  const toneClasses = tone === "danger"
    ? "bg-[var(--app-danger-bg)] text-[var(--app-danger)]"
    : tone === "warning"
      ? "bg-[var(--app-warning-bg)] text-[var(--app-warning)]"
      : tone === "success"
        ? "bg-[var(--app-success-bg)] text-[var(--app-success)]"
        : "bg-[color-mix(in_srgb,var(--app-primary)_10%,transparent)] text-[var(--app-primary)]";
  const toneTextClass = tone === "danger"
    ? "text-[var(--app-danger)]"
    : tone === "warning"
      ? "text-[var(--app-warning)]"
      : tone === "success"
        ? "text-[var(--app-success)]"
        : "text-[var(--app-text-subtle)]";

  return (
    <div className="mb-2 w-full min-w-0 py-1.5" data-plan-tool-transition data-plan-transition-tone={tone}>
      <div className="w-full min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5">
        <div className="flex min-w-0 items-center gap-2.5">
          <span className={cn("grid h-6 w-6 shrink-0 place-items-center rounded-md", toneClasses)}>
            <CircleDot size={13} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="text-[13px] font-semibold leading-5 text-[var(--app-text)]">{planTransitionLabel(action)}</div>
            {(checkpointId || title) ? (
              <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-[11px] leading-4 text-[var(--app-text-muted)]">
                {checkpointId ? <span className="font-mono text-[var(--app-text-muted)]">{checkpointId}</span> : null}
                {checkpointId && title ? <span className="text-[var(--app-text-subtle)]">·</span> : null}
                {title ? <span className="min-w-0 truncate">{title}</span> : null}
              </div>
            ) : null}
          </div>
          {status ? <span className={cn("shrink-0 text-[10px] font-medium capitalize", toneTextClass)}>{normalizedStatus}</span> : null}
        </div>
      </div>
    </div>
  );
}

function safeExternalURL(value: string): string | null {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : null;
  } catch {
    return null;
  }
}

function webStateLabel(state: ToolState): string {
  return state === "running" ? "running" : state === "error" ? "error" : "complete";
}

function WebResourceRow({ resource, fetchResult = false }: { resource: WebResourceData; fetchResult?: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const href = safeExternalURL(resource.url);
  const previewParts = Array.from(new Set([
    resource.summary,
    ...resource.highlights,
    resource.text,
  ].map((value) => value.trim()).filter(Boolean)));
  const preview = previewParts.join("\n\n");
  const expandable = preview.length > 280 || previewParts.length > 1;
  const failed = Boolean(resource.error) || resource.status.toLowerCase() === "error" || resource.status.toLowerCase() === "failed";
  const title = resource.title || resource.domain || resource.url || (fetchResult ? "Fetched page" : "Search result");

  return (
    <article className={cn(
      "min-w-0 rounded-xl border bg-[color-mix(in_srgb,var(--app-surface)_90%,transparent)] p-3",
      failed ? "border-[var(--app-danger-border)]" : "border-[var(--app-border)]",
    )}>
      <div className="flex min-w-0 items-start gap-2.5">
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            {href ? (
              <a
                href={href}
                target="_blank"
                rel="noopener noreferrer"
                className="min-w-0 break-words text-[13px] font-semibold leading-5 text-[var(--app-text)] hover:text-[var(--app-primary)] hover:underline [overflow-wrap:anywhere]"
                title={resource.url}
              >
                {title}
              </a>
            ) : (
              <span className="min-w-0 break-words text-[13px] font-semibold leading-5 text-[var(--app-text)] [overflow-wrap:anywhere]">{title}</span>
            )}
            {fetchResult ? (
              <span className={cn(
                "shrink-0 rounded-full px-1.5 py-0.5 text-[9px] font-semibold uppercase tracking-wide",
                failed
                  ? "bg-[var(--app-danger-bg)] text-[var(--app-danger)]"
                  : "bg-[var(--app-success-bg)] text-[var(--app-success)]",
              )}>
                {failed ? "failed" : resource.status || "fetched"}
              </span>
            ) : null}
          </div>
          <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px] text-[var(--app-text-subtle)]">
            {resource.domain ? <span className="font-medium text-[var(--app-text-muted)]">{resource.domain}</span> : null}
            {resource.author ? <span className="break-words [overflow-wrap:anywhere]">{resource.author}</span> : null}
            {resource.publishedDate ? <span>{resource.publishedDate}</span> : null}
          </div>
        </div>
        {href ? (
          <a
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            aria-label={`Open ${title} in browser`}
            className="grid h-7 w-7 shrink-0 place-items-center rounded-lg text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-primary)]"
          >
            <ExternalLink size={13} />
          </a>
        ) : null}
      </div>
      {resource.url ? (
        href ? (
          <a href={href} target="_blank" rel="noopener noreferrer" className="mt-1.5 block min-w-0 break-all text-[10px] leading-4 text-[var(--app-primary)] hover:underline" title={resource.url}>
            {resource.url}
          </a>
        ) : (
          <div className="mt-1.5 min-w-0 break-all text-[10px] leading-4 text-[var(--app-text-subtle)]">{resource.url}</div>
        )
      ) : null}
      {resource.error ? <div className="mt-2 break-words text-xs leading-5 text-[var(--app-danger)] [overflow-wrap:anywhere]">{resource.error}</div> : null}
      {preview ? (
        <div className="mt-2 min-w-0">
          <div className={cn(
            "min-w-0 whitespace-pre-wrap break-words text-xs leading-5 text-[var(--app-text-muted)] [overflow-wrap:anywhere]",
            !expanded && "line-clamp-3",
          )}>
            {preview}
          </div>
          {expandable ? (
            <button
              type="button"
              className="mt-1.5 inline-flex items-center gap-1 text-[10px] font-semibold text-[var(--app-primary)] hover:underline"
              onClick={() => setExpanded((value) => !value)}
              aria-expanded={expanded}
            >
              {expanded ? <ChevronUp size={11} /> : <ChevronDown size={11} />}
              {expanded ? "Show less" : "Read full preview"}
            </button>
          ) : null}
        </div>
      ) : null}
      {resource.subpages.length > 0 ? (
        <div className="mt-2 border-t border-[var(--app-border)] pt-2">
          <div className="mb-1 text-[9px] font-semibold uppercase tracking-wide text-[var(--app-text-subtle)]">Subpages</div>
          <div className="grid min-w-0 gap-1">
            {resource.subpages.map((subpage, index) => {
              const subpageHref = safeExternalURL(subpage.url);
              const subpageLabel = subpage.title || subpage.domain || subpage.url || `Subpage ${index + 1}`;
              return subpageHref ? (
                <a key={`${subpage.url}:${index}`} href={subpageHref} target="_blank" rel="noopener noreferrer" className="min-w-0 break-words text-[11px] text-[var(--app-primary)] hover:underline [overflow-wrap:anywhere]">{subpageLabel}</a>
              ) : (
                <span key={`${subpage.url}:${index}`} className="min-w-0 break-words text-[11px] text-[var(--app-text-muted)] [overflow-wrap:anywhere]">{subpageLabel}</span>
              );
            })}
          </div>
        </div>
      ) : null}
    </article>
  );
}

function WebToolCardHeader({
  toolMessage,
  title,
  metadata,
  partial,
}: {
  toolMessage: StructuredToolMessage;
  title: string;
  metadata: string[];
  partial: boolean;
}) {
  const toolTheme = getToolTheme(toolMessage.tool);
  const ToolIcon = toolTheme.icon;
  const state = resolveToolState(toolMessage);
  const StateIcon = state === "error" ? XCircle : state === "running" ? LoaderCircle : CheckCircle2;
  return (
    <header className="flex min-w-0 flex-wrap items-center gap-2 border-b border-[var(--app-border)] px-3 py-2.5 text-xs">
      <span className="grid h-7 w-7 shrink-0 place-items-center rounded-lg" style={{ color: toolTheme.color, backgroundColor: toolAccentWash(toolTheme.color, 14) }}>
        <ToolIcon size={13} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="font-semibold text-[var(--app-text)]">{title}</div>
        {metadata.length > 0 ? <div className="mt-0.5 flex min-w-0 flex-wrap gap-x-1.5 text-[10px] text-[var(--app-text-subtle)]">{metadata.map((item, index) => <span key={`${item}:${index}`}>{index ? `· ${item}` : item}</span>)}</div> : null}
      </div>
      {partial ? <span className="shrink-0 rounded-full bg-[var(--app-warning-bg)] px-2 py-1 text-[9px] font-semibold text-[var(--app-warning)]">Partial</span> : null}
      {toolMessage.durationMs > 0 ? <span className="shrink-0 text-[10px] text-[var(--app-text-subtle)]">{formatDuration(toolMessage.durationMs)}</span> : null}
      <span className={cn(
        "inline-flex shrink-0 items-center gap-1 text-[10px] font-medium",
        state === "error" ? "text-[var(--app-danger)]" : state === "running" ? "text-[var(--app-primary)]" : "text-[var(--app-text-muted)]",
      )}>
        <StateIcon size={12} className={cn(state === "running" && "animate-spin")} />
        {webStateLabel(state)}
      </span>
    </header>
  );
}

function WebSearchToolCard({ toolMessage, data, isGroupItem }: { toolMessage: StructuredToolMessage; data: WebSearchToolData; isGroupItem?: boolean }) {
  const metadata = [
    `${data.queryCount} ${data.queryCount === 1 ? "query" : "queries"}`,
    `${data.totalResults} ${data.totalResults === 1 ? "result" : "results"}`,
    data.failedQueries ? `${data.failedQueries} failed` : "",
    data.searchType,
  ].filter(Boolean);
  return (
    <div className={cn(isGroupItem ? "py-1.5" : "mb-2 py-1.5", "w-full min-w-0")} data-web-tool-card="websearch">
      <section className="min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] shadow-sm">
        <WebToolCardHeader toolMessage={toolMessage} title="Web Search" metadata={metadata} partial={data.truncated} />
        {data.queries.length > 0 ? (
          <div className="flex min-w-0 flex-wrap gap-1.5 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
            {data.queries.map((query, index) => <span key={`${query}:${index}`} className="max-w-full break-words rounded-lg bg-[color-mix(in_srgb,var(--app-accent)_9%,transparent)] px-2 py-1 text-[11px] leading-4 text-[var(--app-text)] [overflow-wrap:anywhere]">{query}</span>)}
          </div>
        ) : null}
        {toolMessage.error ? <div className="border-b border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{toolMessage.error}</div> : null}
        <div className={cn(TOOL_RESULT_BODY_CLASS, "min-w-0 p-2.5")}>
          {data.queryResults.length > 0 ? <div className="grid min-w-0 gap-3">{data.queryResults.map((group, groupIndex) => (
            <section key={`${group.query}:${groupIndex}`} className="min-w-0">
              {(data.queryResults.length > 1 || group.error || group.timedOut) ? (
                <div className="mb-1.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 px-0.5">
                  <div className="min-w-0 flex-1 break-words text-[11px] font-semibold text-[var(--app-text)] [overflow-wrap:anywhere]">{group.query || `Query ${groupIndex + 1}`}</div>
                  <span className="shrink-0 text-[10px] text-[var(--app-text-subtle)]">{group.error ? "failed" : group.timedOut ? "timed out" : `${group.count} results`}</span>
                </div>
              ) : null}
              {group.error ? <div className="mb-2 break-words rounded-lg border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)] [overflow-wrap:anywhere]">{group.error}</div> : null}
              {group.results.length > 0 ? <div className="grid min-w-0 gap-2">{group.results.map((resource, index) => <WebResourceRow key={`${resource.url}:${index}`} resource={resource} />)}</div> : null}
            </section>
          ))}</div> : <div className="px-1 py-2 text-xs text-[var(--app-text-subtle)]">{toolMessage.state === "running" ? "Searching the web…" : "No structured results returned."}</div>}
        </div>
      </section>
    </div>
  );
}

function WebFetchToolCard({ toolMessage, data, isGroupItem }: { toolMessage: StructuredToolMessage; data: WebFetchToolData; isGroupItem?: boolean }) {
  const failedCount = Math.max(0, data.count - data.successCount);
  const metadata = [
    `${data.urls.length || data.count} ${(data.urls.length || data.count) === 1 ? "URL" : "URLs"}`,
    `${data.successCount} fetched`,
    failedCount ? `${failedCount} failed` : "",
    data.timedOut ? "timed out" : "",
  ].filter(Boolean);
  return (
    <div className={cn(isGroupItem ? "py-1.5" : "mb-2 py-1.5", "w-full min-w-0")} data-web-tool-card="webfetch">
      <section className="min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] shadow-sm">
        <WebToolCardHeader toolMessage={toolMessage} title="Web Fetch" metadata={metadata} partial={data.truncated || data.timedOut} />
        {data.urls.length > 0 ? (
          <div className="grid min-w-0 gap-1 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">
            {data.urls.map((url, index) => {
              const href = safeExternalURL(url);
              return href ? <a key={`${url}:${index}`} href={href} target="_blank" rel="noopener noreferrer" className="min-w-0 break-all text-[10px] leading-4 text-[var(--app-primary)] hover:underline">{url}</a> : <span key={`${url}:${index}`} className="min-w-0 break-all text-[10px] leading-4 text-[var(--app-text-subtle)]">{url}</span>;
            })}
          </div>
        ) : null}
        {toolMessage.error ? <div className="border-b border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]">{toolMessage.error}</div> : null}
        <div className={cn(TOOL_RESULT_BODY_CLASS, "min-w-0 p-2.5")}>
          {data.results.length > 0 ? <div className="grid min-w-0 gap-2">{data.results.map((resource, index) => <WebResourceRow key={`${resource.url}:${index}`} resource={resource} fetchResult />)}</div> : null}
          {data.statuses.filter((status) => status.error || status.status).length > 0 ? (
            <div className={cn("grid min-w-0 gap-1.5", data.results.length > 0 && "mt-2 border-t border-[var(--app-border)] pt-2")}>
              {data.statuses.map((status, index) => <div key={`${status.id}:${index}`} className={cn("min-w-0 break-words rounded-lg px-2.5 py-1.5 text-[11px] [overflow-wrap:anywhere]", status.error ? "bg-[var(--app-danger-bg)] text-[var(--app-danger)]" : "bg-[var(--app-surface)] text-[var(--app-text-muted)]")}><span className="font-semibold">{status.source || status.id || `URL ${index + 1}`}</span>{status.status ? ` · ${status.status}` : ""}{status.error ? ` · ${status.error}` : ""}</div>)}
            </div>
          ) : null}
          {data.results.length === 0 && data.statuses.length === 0 ? <div className="px-1 py-2 text-xs text-[var(--app-text-subtle)]">{toolMessage.state === "running" ? "Fetching page content…" : "No structured page content returned."}</div> : null}
        </div>
      </section>
    </div>
  );
}

function SearchToolView({
  toolMessage,
}: {
  toolMessage: StructuredToolMessage;
}) {
  const data = toolMessage.searchData;
  if (!data) return null;

  const sections = useMemo(() => data.files, [data.files]);

  return (
    <div className="min-w-0">
      <SearchSummaryLine toolMessage={toolMessage} />
      {sections.length > 0 ? (
        <div className={cn(TOOL_RESULT_BODY_CLASS, "mt-2 grid gap-2 font-mono pr-1")}>
          {sections.map((file, index) => (
            <SearchFileSection
              key={`${file.path}:${index}`}
              file={file}
              mode={data.mode}
            />
          ))}
        </div>
      ) : null}
    </div>
  );
}

export function ToolMessageView({
  toolMessage,
  isGroupItem,
  thinkingTagsEnabled = true,
  taskChildActions,
}: {
  toolMessage: StructuredToolMessage;
  isGroupItem?: boolean;
  thinkingTagsEnabled?: boolean;
  taskChildActions?: TaskChildCardActions;
}) {
  const normalizedToolName = toolMessage.tool.trim().toLowerCase();
  if (normalizedToolName === "bash") {
    return <BashToolCard toolMessage={toolMessage} isGroupItem={isGroupItem} />;
  }
  if (normalizedToolName === "websearch" && toolMessage.webSearchData) {
    return <WebSearchToolCard toolMessage={toolMessage} data={toolMessage.webSearchData} isGroupItem={isGroupItem} />;
  }
  if (normalizedToolName === "webfetch" && toolMessage.webFetchData) {
    return <WebFetchToolCard toolMessage={toolMessage} data={toolMessage.webFetchData} isGroupItem={isGroupItem} />;
  }

  const toolTheme = getToolTheme(toolMessage.tool);
  const ToolIcon = toolTheme.icon;
  const state = resolveToolState(toolMessage);
  const StateIcon =
    state === "error"
      ? XCircle
      : state === "running"
        ? LoaderCircle
        : CheckCircle2;
  const normalizedTool = toolMessage.tool.trim().toLowerCase();
  const label = toolTheme.label || toolMessage.tool || "tool";
  const isTask = normalizedTool === "task";
  const hasTaskRows = isTask && toolMessage.taskRows.length > 0;
  const isTaskSwarm = hasTaskRows && toolMessage.taskRows.length > TASK_SWARM_THRESHOLD;
  const todoCounts = formatTodoCounts(toolMessage.todoData?.summary ?? null);
  const summary = isTaskSwarm
    ? ""
    : todoCounts || toolSummaryRemainder(toolMessage.summary || toolMessage.tool || "tool", label);
  const accentWash = toolAccentWash(toolTheme.color, 14);
  const previewLanguage = inferToolSyntaxLanguage(toolMessage.target || pathFromToolSummary(toolMessage.summary));
  const shellPreview = false;
  const plainPreview = shouldRenderPreviewAsPlain(toolMessage.tool);
  const isManageSessions = ["manage-sessions", "manage_sessions"].includes(normalizedTool);
  const isPlanManage = ["plan-manage", "plan_manage"].includes(normalizedTool);
  const isExitPlanMode = ["exit-plan-mode", "exit_plan_mode"].includes(normalizedTool);
  const isFileAction = ["read", "list", "search", "edit"].includes(normalizedTool);
  const fileSummary = isFileAction && toolMessage.target
    ? summary.replace(toolMessage.target, "").replace(/\s+in\s+(?=\()/, " ").trim()
    : summary;
  const showPreview = normalizedTool !== 'thinking' || thinkingTagsEnabled;
  const isWindup = !isTask && state === "running" && !toolMessage.output.trim() && !toolMessage.error.trim();
  if (isExitPlanMode) return <ExitPlanModeToolView toolMessage={toolMessage} />;
  if (isPlanManage) return <PlanManageToolView toolMessage={toolMessage} />;
  const hasBody = Boolean(
    toolMessage.error
    || toolMessage.editDiff
    || (normalizedTool === "task" && toolMessage.taskRows.length > 0)
    || (normalizedTool === "search" && toolMessage.searchData)
    || (showPreview && !isManageSessions && (toolMessage.previewLines.length > 0 || toolMessage.commandText))
    || isManageSessions,
  );

  return (
    <div className={cn(isGroupItem ? "py-1.5" : "mb-2 min-w-0 py-1.5", isFileAction && "w-full")}>
      <div className={cn(
        "min-w-0",
        isFileAction && "overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]",
        isTask && "overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-sm",
      )} data-task-tool-card={isTask || undefined}>
        {!hasTaskRows ? (
          <div className={cn(
            "flex min-w-0 items-start gap-2 text-xs",
            isFileAction || isTask ? "px-3 py-2.5" : "items-center",
          )}>
            <span
              className={cn(
                "inline-flex shrink-0 items-center justify-center font-semibold",
                isFileAction || isTask ? "h-7 w-7 rounded-lg" : "h-5 gap-1 rounded-md px-1.5",
              )}
              style={{ color: toolTheme.color, backgroundColor: accentWash }}
            >
              {isTask ? <Bot size={14} className="shrink-0" aria-hidden="true" /> : <ToolIcon size={isFileAction ? 13 : 12} className="shrink-0" />}
              {!isFileAction && !isTask ? label : null}
            </span>
            <div className="min-w-0 flex-1">
              {isFileAction || isTask ? (
                <div className="font-semibold capitalize leading-4 text-[var(--app-text)]">{label}</div>
              ) : null}
              {fileSummary ? (
                <div className={cn(
                  "min-w-0 break-words [overflow-wrap:anywhere]",
                  isFileAction || isTask ? "mt-0.5 text-[11px] font-normal leading-4 text-[var(--app-text-muted)]" : "font-medium text-[var(--app-text)]",
                )}>
                  {fileSummary}
                </div>
              ) : null}
              {isFileAction && toolMessage.target ? (
                <div className="mt-2 min-w-0 break-words border-t border-[var(--app-border)] pt-2 font-mono text-[11px] leading-4 text-[var(--app-text)] [overflow-wrap:anywhere]">
                  {toolMessage.target}
                </div>
              ) : null}
            </div>
            <div className="flex shrink-0 items-center gap-1.5 pt-0.5 text-[10px] text-[var(--app-text-subtle)]">
              {isWindup ? <span>starting…</span> : null}
              {toolMessage.durationMs > 0 ? <span>{formatDuration(toolMessage.durationMs)}</span> : null}
              {!isTask ? (
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
              ) : null}
            </div>
          </div>
        ) : null}
        <div className={cn(
          "min-w-0",
          isFileAction && hasBody && "border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2.5",
          isTask && hasBody && !hasTaskRows && "border-t border-[var(--app-border)]",
        )}>
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
          <TaskRowsView rows={toolMessage.taskRows} actions={taskChildActions} />
        ) : null}
        {!toolMessage.editDiff &&
        toolMessage.tool === "search" &&
        toolMessage.searchData ? (
          <SearchToolView toolMessage={toolMessage} />
        ) : null}
        {!toolMessage.editDiff &&
        toolMessage.tool !== "search" &&
        !(toolMessage.tool === "task" && toolMessage.taskRows.length > 0) &&
        showPreview &&
        !isManageSessions &&
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
        {isManageSessions ? (
          <ManageSessionsCard toolMessage={toolMessage} />
        ) : null}
        </div>
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
  const toolTheme = getToolTheme(toolName);
  const ToolIcon = toolTheme.icon;
  const hasErrors = messages.some((m) => m.error);
  const accentWash = toolAccentWash(toolTheme.color, 14);

  return (
    <div className="mb-2 py-2">
      <div className="mb-2 flex items-center gap-2 text-xs text-[var(--app-text-muted)]">
        <span
          className="inline-flex h-6 shrink-0 items-center gap-1.5 rounded-lg px-2 font-semibold"
          style={{ color: toolTheme.color, backgroundColor: accentWash }}
        >
          <ToolIcon size={12} className="shrink-0" />
          {toolTheme.label || toolName}
        </span>
        <span className="text-[11px] text-[var(--app-text-subtle)]">
          {messages.length} actions
        </span>
        {hasErrors ? (
          <span className="ml-auto text-[10px] font-semibold text-[var(--app-danger)]">
            Needs attention
          </span>
        ) : null}
      </div>
      <div className="flex min-w-0 flex-col gap-2">
        {messages.map((msg, i) => (
          <ToolMessageView
            key={msg.toolInstanceId || msg.callId || i}
            toolMessage={msg}
            isGroupItem={true}
          />
        ))}
      </div>
    </div>
  );
}

function ChatMarkdownInner({
  content,
  className,
  toolMessage,
  thinkingTagsEnabled = true,
  taskChildActions,
}: ChatMarkdownProps) {
  if (toolMessage) {
    return <ToolMessageView toolMessage={toolMessage} thinkingTagsEnabled={thinkingTagsEnabled} taskChildActions={taskChildActions} />;
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
