import { memo, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { Archive, ArrowRight, CheckCircle2, ChevronDown, ChevronUp, CircleDot, Clock3, Copy, GitBranch, Layers3, LoaderCircle, MessageSquareText, Search, XCircle } from "lucide-react";
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
  thinkingTagsEnabled?: boolean;
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

const BASH_COLLAPSED_MIN_HEIGHT = 180;
const BASH_COLLAPSED_MAX_HEIGHT = 420;
const BASH_COLLAPSED_FALLBACK_HEIGHT = 320;
const BASH_EXPANDED_MAX_HEIGHT = "min(72vh, 48rem)";

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

  return joinBashOutputParts(parts) || data.outputText || data.completedOutput || toolMessage.output || toolMessage.completedOutput || "";
}

export function bashCopyText(output: string): string {
  return output;
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
  const copyPayload = useMemo(() => bashCopyText(output), [output]);
  const outputRef = useRef<HTMLDivElement | null>(null);
  const userScrolledAwayRef = useRef(false);
  const copyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [canExpand, setCanExpand] = useState(false);
  const [copied, setCopied] = useState(false);
  const [collapsedMaxHeight, setCollapsedMaxHeight] = useState(BASH_COLLAPSED_FALLBACK_HEIGHT);

  const measureOutput = useCallback(() => {
    const viewportHeight = typeof window === "undefined" ? BASH_COLLAPSED_FALLBACK_HEIGHT * 2 : window.innerHeight;
    const nextCollapsedHeight = Math.max(
      BASH_COLLAPSED_MIN_HEIGHT,
      Math.min(BASH_COLLAPSED_MAX_HEIGHT, Math.floor(viewportHeight * 0.5)),
    );
    setCollapsedMaxHeight(nextCollapsedHeight);
    const node = outputRef.current;
    if (node) setCanExpand(node.scrollHeight > nextCollapsedHeight + 8);
  }, []);

  useEffect(() => {
    measureOutput();
    if (typeof window === "undefined") return;
    const node = outputRef.current;
    const resizeObserver = typeof ResizeObserver !== "undefined" && node ? new ResizeObserver(measureOutput) : null;
    if (node && resizeObserver) resizeObserver.observe(node);
    window.addEventListener("resize", measureOutput);
    return () => {
      resizeObserver?.disconnect();
      window.removeEventListener("resize", measureOutput);
    };
  }, [measureOutput, output]);

  useEffect(() => {
    const node = outputRef.current;
    if (!node || userScrolledAwayRef.current) return;
    const frame = window.requestAnimationFrame(() => {
      node.scrollTop = node.scrollHeight;
    });
    return () => window.cancelAnimationFrame(frame);
  }, [collapsedMaxHeight, expanded, output, state]);

  useEffect(() => {
    return () => {
      if (copyTimerRef.current) clearTimeout(copyTimerRef.current);
    };
  }, []);

  const handleOutputScroll = useCallback(() => {
    const node = outputRef.current;
    if (!node) return;
    const distanceFromBottom = node.scrollHeight - node.scrollTop - node.clientHeight;
    userScrolledAwayRef.current = distanceFromBottom > 64;
  }, []);

  const handleCopy = useCallback(async () => {
    if (!copyPayload) return;
    await copyTextToClipboard(copyPayload);
    setCopied(true);
    if (copyTimerRef.current) clearTimeout(copyTimerRef.current);
    copyTimerRef.current = setTimeout(() => setCopied(false), 1400);
  }, [copyPayload]);

  const accentWash = toolAccentWash(toolTheme.color, 14);
  const statusText = bashStatusLabel(state);
  const exitCode = toolMessage.bashData?.exitCode;
  const outputMaxHeight = expanded ? BASH_EXPANDED_MAX_HEIGHT : `${collapsedMaxHeight}px`;

  return (
    <div className={cn(isGroupItem ? "py-2" : "mb-2 py-2", "w-full min-w-0")}>
      <div className="w-full min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] shadow-sm">
        <div className="flex min-w-0 flex-wrap items-center gap-2 border-b border-[var(--app-border)] px-3 py-2 text-xs">
          <span
            className="inline-flex h-6 shrink-0 items-center gap-1 rounded-md px-1.5 font-semibold"
            style={{ color: toolTheme.color, backgroundColor: accentWash }}
          >
            <ToolIcon size={12} className="shrink-0" />
            bash
          </span>
          <span className="inline-flex shrink-0 items-center gap-1 text-[11px] font-medium text-[var(--app-text-muted)]">
            <StateIcon
              size={12}
              className={cn(
                state === "running" ? "animate-spin text-[var(--app-primary)]" : state === "error" ? "text-[var(--app-danger)]" : "text-[var(--app-text-subtle)]",
              )}
            />
            {statusText}
          </span>
          {typeof exitCode === "number" ? (
            <span className="shrink-0 text-[11px] text-[var(--app-text-subtle)]">exit {exitCode}</span>
          ) : null}
          {toolMessage.durationMs > 0 ? (
            <span className="shrink-0 text-[11px] text-[var(--app-text-subtle)]">{formatDuration(toolMessage.durationMs)}</span>
          ) : null}
          <div className="ml-auto flex min-w-0 shrink-0 items-center gap-1">
            <button
              type="button"
              className="inline-flex h-7 items-center gap-1 rounded-md border border-[var(--app-border)] px-2 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface)] disabled:cursor-not-allowed disabled:opacity-50"
              onClick={handleCopy}
              disabled={!copyPayload}
              aria-label="Copy Bash output"
            >
              <Copy size={12} className="shrink-0" />
              <span className="hidden sm:inline">{copied ? "Copied" : "Copy"}</span>
            </button>
            {canExpand ? (
              <button
                type="button"
                className="inline-flex h-7 items-center gap-1 rounded-md border border-[var(--app-border)] px-2 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface)]"
                onClick={() => setExpanded((value) => !value)}
                aria-expanded={expanded}
              >
                {expanded ? <ChevronUp size={12} className="shrink-0" /> : <ChevronDown size={12} className="shrink-0" />}
                <span className="hidden sm:inline">{expanded ? "Collapse" : "Expand"}</span>
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
        {toolMessage.error ? (
          <div className="border-b border-[var(--app-border)] px-3 py-2 text-[12px] text-[var(--app-danger)]">
            {toolMessage.error}
          </div>
        ) : null}
        {output ? (
          <div
            ref={outputRef}
            className="min-w-0 overflow-auto overscroll-contain bg-[var(--app-code-bg)] text-[12px] leading-5 text-[var(--app-code-text)]"
            style={{ maxHeight: outputMaxHeight }}
            onScroll={handleOutputScroll}
          >
            <pre className="m-0 min-w-0 whitespace-pre-wrap break-words p-3 font-mono [overflow-wrap:anywhere]">
              <code>{output}</code>
            </pre>
          </div>
        ) : (
          <div className="px-3 py-2 text-[12px] text-[var(--app-text-subtle)]">
            {state === "running" ? "Waiting for output…" : "No output"}
          </div>
        )}
      </div>
    </div>
  );
}

function FilePathLine({ path, meta }: { path: string; meta?: string }) {
  return (
    <div className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-baseline gap-3">
      <span className="min-w-0 break-words font-mono text-[12px] font-medium text-[var(--app-text)] [overflow-wrap:anywhere]">
        {path}
      </span>
      {meta ? <span className="shrink-0 text-[10px] text-[var(--app-text-subtle)]">{meta}</span> : null}
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
      <div className="max-h-[28rem] overflow-auto py-1">
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
      ? "mt-1 min-w-0 py-0.5 font-mono text-[11px] leading-[18px] text-[var(--app-text-muted)]"
      : "mt-2 min-w-0 space-y-1.5"}
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
  const errorText = row.status.trim().toLowerCase() === 'failed' || row.status.trim().toLowerCase() === 'error' ? row.previewText.trim() : '';
  const previewText = errorText ? '' : row.previewText.trim();
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
        <div className="grid min-w-0 grid-cols-[3.25rem_minmax(0,1fr)] gap-x-2 px-3 pb-2 sm:grid-cols-[2.5rem_3.75rem_minmax(0,1fr)] sm:gap-x-3">
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
            <span className="break-words text-xs font-bold uppercase tracking-[0.12em] text-[var(--app-text)] [overflow-wrap:anywhere]">
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
  const agent = row.agent || 'subagent';
  const agentLabel = agent.startsWith('@') ? agent : `@${agent}`;
  const toolLabel = row.tool && row.tool !== '-' ? row.tool : taskStatusText(kind);
  const title = row.assignmentLabel && row.assignmentLabel !== row.agent ? row.assignmentLabel : '';

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
  );
}

const MemoizedTaskAgentListRow = memo(TaskAgentListRow, (previous, next) => (
  previous.index === next.index
  && previous.dense === next.dense
  && taskRowsEqual(previous.row, next.row)
));

const MemoizedTaskSwarmCompactRow = memo(TaskSwarmCompactRow, (previous, next) => (
  previous.index === next.index
  && taskRowsEqual(previous.row, next.row, { comparePreview: false })
));

function TaskSwarmRowsView({ rows }: { rows: TaskToolRow[] }) {
  const counts = taskRowsCounts(rows);
  return (
    <div className="mt-2 min-w-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-sm">
      <TaskRowsHeader counts={counts} swarm />
      <div className="grid min-w-0 grid-cols-1 gap-1.5 p-2 md:grid-cols-2 xl:grid-cols-3">
        {rows.map((row, index) => (
          <MemoizedTaskSwarmCompactRow
            key={taskRowKey(row, index)}
            row={row}
            index={index}
          />
        ))}
      </div>
    </div>
  );
}

function TaskAgentRowsView({ rows }: { rows: TaskToolRow[] }) {
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
          <MemoizedTaskAgentListRow
            key={taskRowKey(row, index)}
            row={row}
            index={index}
            dense={dense}
          />
        ))}
      </div>
    </div>
  );
}

function TaskRowsView({ rows }: { rows: TaskToolRow[] }) {
  if (rows.length === 0) return null;
  if (rows.length > TASK_SWARM_THRESHOLD) return <TaskSwarmRowsView rows={rows} />;
  return <TaskAgentRowsView rows={rows} />;
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
  const displayMatches = group.matches.length > 0;
  const items = displayMatches ? group.matches : group.lines.map((line) => ({ line, text: "" }));

  return (
    <div className="mt-2 min-w-0 text-[12px] leading-5 text-[var(--app-text-muted)]">
      <div className="mb-1 font-sans text-[11px] font-medium text-[var(--app-text-subtle)]">
        {group.query || "match"}
      </div>
      {items.length > 0 ? (
        <div className="space-y-1">
          {items.map((item, index) => (
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
    <div className="min-w-0 border-t border-[var(--app-border)] px-3 py-2.5 first:border-t-0">
      <FilePathLine
        path={file.path}
        meta={mode === "files"
          ? `${file.matchCount} ${file.matchCount === 1 ? "hit" : "hits"}`
          : `${file.matchCount} ${file.matchCount === 1 ? "match" : "matches"}`}
      />
      <div className="mt-1.5 space-y-1">
        {file.queryGroups.map((group, index) => (
          <SearchLineList
            key={`${file.path}:${group.query}:${index}`}
            group={group}
          />
        ))}
      </div>
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
  const clean = typeof record.clean === "boolean" ? (record.clean ? "Clean" : `${manageSessionNumber(record, "dirty_count") ?? 0} changes`) : "";
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

function ManageSessionsCard({ toolMessage }: { toolMessage: StructuredToolMessage }) {
  const output = parseToolJSON(toolMessage.output) ?? parseToolJSON(toolMessage.completedOutput);
  if (!output) return null;
  const args = toolMessage.argumentsJson ?? null;
  const action = toolJsonString(output, "action") || toolJsonString(args, "action") || "sessions";
  const deployResults = Array.isArray(output.results) ? output.results : [];
  const rawItems = Array.isArray(output.items) ? output.items : deployResults.length ? deployResults : output.id || output.session_id ? [output] : [];
  const items = rawItems.map(manageSessionItem).filter((item): item is ManageSessionCardItem => Boolean(item));
  const failedDeployments = action === "deploy" ? deployResults.flatMap((value): Array<{ proposal: string; error: string }> => {
    if (!value || typeof value !== "object" || Array.isArray(value)) return [];
    const record = value as Record<string, unknown>;
    const error = toolJsonString(record, "error");
    if (!error) return [];
    return [{ proposal: toolJsonString(record, "proposal_id") || toolJsonString(record, "title") || "Session", error }];
  }) : [];
  const messages = Array.isArray(output.messages) ? output.messages : [];
  const archivedIds = Array.isArray(output.archived_session_ids) ? output.archived_session_ids.filter((id): id is string => typeof id === "string") : [];
  const commits = Array.isArray(output.commits) ? output.commits.flatMap((entry) => {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) return [];
    const commit = entry as Record<string, unknown>;
    return [{ sessionId: typeof commit.session_id === "string" ? commit.session_id : "", message: typeof commit.message === "string" ? commit.message : "", hash: typeof commit.commit_hash === "string" ? commit.commit_hash : "", files: Array.isArray(commit.files) ? commit.files.filter((file): file is string => typeof file === "string") : [] }];
  }) : [];
  const unarchivedIds = Array.isArray(output.unarchived_session_ids) ? output.unarchived_session_ids.filter((id): id is string => typeof id === "string") : [];
  const title = action === "deploy" ? "Session deployment" : action === "search" ? "Session search" : action === "list" ? "Your sessions" : action === "read_messages" ? "Session context" : action === "git_status" ? "Worktree status" : action === "archive" ? "Sessions archived" : action === "unarchive" ? "Sessions unarchived" : action === "commit" ? "Session commits ready for testing" : action === "inspect" ? "Session manager" : "Session details";
  const HeaderIcon = action === "search" ? Search : action === "archive" || action === "unarchive" ? Archive : action === "read_messages" ? MessageSquareText : action === "git_status" ? GitBranch : Layers3;

  return (
    <section className="mt-2 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[linear-gradient(145deg,color-mix(in_srgb,var(--app-primary)_8%,var(--app-surface)),var(--app-surface)_45%)] shadow-[0_12px_35px_rgba(0,0,0,0.08)]">
      <header className="flex items-center justify-between gap-3 border-b border-[var(--app-border)] px-4 py-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <span className="grid h-8 w-8 shrink-0 place-items-center rounded-xl bg-[color-mix(in_srgb,var(--app-primary)_15%,transparent)] text-[var(--app-primary)]"><HeaderIcon size={15} /></span>
          <div className="min-w-0"><h4 className="truncate text-sm font-semibold text-[var(--app-text)]">{title}</h4><p className="text-[11px] text-[var(--app-text-subtle)]">{items.length ? `${items.length} ${items.length === 1 ? "session" : "sessions"}` : action.split("_").join(" ")}</p></div>
        </div>
        {output.has_more === true ? <span className="rounded-full border border-[var(--app-border)] px-2 py-1 text-[10px] font-medium text-[var(--app-text-muted)]">More available</span> : null}
      </header>
      {items.length > 0 ? <div className="grid gap-2 p-2.5">{items.map((item, index) => (
        <article key={item.id || `${item.title}-${index}`} className="group rounded-xl border border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-surface)_88%,transparent)] p-3 transition hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)]">
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
      {messages.length > 0 ? <div className="max-h-80 space-y-2 overflow-auto p-3">{messages.map((value, index) => {
        const message = value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
        const role = toolJsonString(message, "role") || "message";
        const content = toolJsonString(message, "content");
        const seq = manageSessionNumber(message, "seq");
        return <div key={`${seq ?? index}`} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3"><div className="mb-1 text-[10px] font-semibold uppercase tracking-wide text-[var(--app-text-subtle)]">{role}{seq !== null ? ` · #${seq}` : ""}</div><div className="whitespace-pre-wrap break-words text-xs leading-5 text-[var(--app-text-muted)]">{content}</div></div>;
      })}</div> : null}
      {failedDeployments.length > 0 ? <div className="grid gap-2 border-t border-[var(--app-border)] p-3">{failedDeployments.map((failure, index) => <div key={`${failure.proposal}:${index}`} className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-xs text-[var(--app-danger)]"><span className="font-semibold">{failure.proposal} failed:</span> {failure.error}</div>)}</div> : null}
      {archivedIds.length > 0 ? <div className="p-4 text-xs text-[var(--app-text-muted)]">Archived {archivedIds.length} {archivedIds.length === 1 ? "session" : "sessions"} durably.</div> : null}
      {commits.length > 0 ? <div className="grid gap-2 border-t border-[var(--app-border)] p-3">{commits.map((commit, index) => <article key={`${commit.hash}:${index}`} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-xs"><div className="font-semibold text-[var(--app-text)]">{commit.message || `Commit ${index + 1}`}</div><div className="mt-1 font-mono text-[var(--app-text-muted)]">{commit.hash}</div>{commit.files.length > 0 ? <div className="mt-2 text-[var(--app-text-muted)]">{commit.files.length} changed {commit.files.length === 1 ? "file" : "files"} · session remains in needs review</div> : null}</article>)}</div> : null}
      {unarchivedIds.length > 0 ? <div className="p-4 text-xs text-[var(--app-text-muted)]">Unarchived {unarchivedIds.length} {unarchivedIds.length === 1 ? "session" : "sessions"} durably.</div> : null}
      {action === "inspect" ? <div className="p-4 text-xs leading-5 text-[var(--app-text-muted)]">Search and read are bounded for efficient context. Archive and unarchive require approval.</div> : null}
    </section>
  );
}

function shouldRenderPreviewAsPlain(toolName: string): boolean {
  switch (toolName.trim().toLowerCase()) {
    case "manage_todos":
    case "manage-todos":
    case "manage-image":
    case "manage_image":
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

function PlanManageToolView({ toolMessage }: { toolMessage: StructuredToolMessage }) {
  const payload = parseToolJSON(toolMessage.output) ?? parseToolJSON(toolMessage.completedOutput);
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
    <div className="mb-2 min-w-0 py-1.5" data-plan-tool-transition data-plan-transition-tone={tone}>
      <div className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5">
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
        <div className="mt-3 min-w-0 overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] font-mono">
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
}: {
  toolMessage: StructuredToolMessage;
  isGroupItem?: boolean;
  thinkingTagsEnabled?: boolean;
}) {
  if (toolMessage.tool.trim().toLowerCase() === "bash") {
    return <BashToolCard toolMessage={toolMessage} isGroupItem={isGroupItem} />;
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
  const isTaskSwarm = toolMessage.tool === "task" && toolMessage.taskRows.length > TASK_SWARM_THRESHOLD;
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
  const isFileAction = ["read", "list", "search", "edit"].includes(normalizedTool);
  const fileSummary = isFileAction && toolMessage.target
    ? summary.replace(toolMessage.target, "").replace(/\s+in\s+(?=\()/, " ").trim()
    : summary;
  const showPreview = normalizedTool !== 'thinking' || thinkingTagsEnabled;
  const isWindup = state === "running" && !toolMessage.output.trim() && !toolMessage.error.trim();
  if (isPlanManage) return <PlanManageToolView toolMessage={toolMessage} />;
  const hasBody = Boolean(
    toolMessage.error
    || toolMessage.editDiff
    || (normalizedTool === "task" && toolMessage.taskRows.length > 0)
    || (normalizedTool === "search" && toolMessage.searchData)
    || (showPreview && !isManageSessions && (toolMessage.previewLines.length > 0 || toolMessage.commandText))
    || normalizedTool === "manage-image"
    || normalizedTool === "manage_image"
    || isManageSessions,
  );

  return (
    <div className={cn(isGroupItem ? "py-1.5" : "mb-2 min-w-0 py-1.5", isFileAction && "w-full")}>
      <div className={cn(
        "min-w-0",
        isFileAction && "overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]",
      )}>
        <div className={cn(
          "flex min-w-0 items-start gap-2 text-xs",
          isFileAction ? "px-3 py-2.5" : "items-center",
        )}>
          <span
            className={cn(
              "inline-flex shrink-0 items-center justify-center font-semibold",
              isFileAction ? "h-7 w-7 rounded-lg" : "h-5 gap-1 rounded-md px-1.5",
            )}
            style={{ color: toolTheme.color, backgroundColor: accentWash }}
          >
            <ToolIcon size={isFileAction ? 13 : 12} className="shrink-0" />
            {!isFileAction ? label : null}
          </span>
          <div className="min-w-0 flex-1">
            {isFileAction ? (
              <div className="font-semibold capitalize leading-4 text-[var(--app-text)]">{label}</div>
            ) : null}
            {fileSummary ? (
              <div className={cn(
                "min-w-0 break-words [overflow-wrap:anywhere]",
                isFileAction ? "mt-0.5 text-[11px] font-normal leading-4 text-[var(--app-text-muted)]" : "font-medium text-[var(--app-text)]",
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
        </div>
        <div className={cn("min-w-0", isFileAction && hasBody && "border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2.5")}>
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
        {toolMessage.tool.trim().toLowerCase() === 'manage-image' || toolMessage.tool.trim().toLowerCase() === 'manage_image' ? (
          <ImageToolAction toolMessage={toolMessage} />
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
}: ChatMarkdownProps) {
  if (toolMessage) {
    return <ToolMessageView toolMessage={toolMessage} thinkingTagsEnabled={thinkingTagsEnabled} />;
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
