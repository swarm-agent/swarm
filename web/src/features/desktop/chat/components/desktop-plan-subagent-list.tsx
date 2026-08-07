import { Bot, ChevronRight, Square } from "lucide-react";
import type { TaskChildCardActions, TaskToolRow } from "../types/chat";
import type { DesktopV3TaskChildViewModel } from "../../state/desktop-v3-cache-selectors";
import { stopSubagentSessionV3Run } from "../../session-v3/api";
import { cn } from "../../../../lib/cn";

interface DesktopPlanSubagentListProps {
  children: Array<{ row: TaskToolRow; view: DesktopV3TaskChildViewModel | null }>;
  actions?: TaskChildCardActions;
  mode: "full" | "compact" | "thin";
}

function statusTone(status: string, terminal: boolean): string {
  const value = status.trim().toLowerCase();
  if (value === "failed" || value === "error" || value === "blocked") return "bg-[var(--app-danger)]";
  if (value === "needs_review" || value === "needs_approval") return "bg-[var(--app-warning)]";
  if (terminal || value === "completed" || value === "done") return "bg-[var(--app-success)]";
  if (value === "running" || value === "pending_executor") return "bg-[var(--app-primary)] animate-pulse";
  return "bg-[var(--app-text-subtle)]";
}

function formatElapsed(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function desktopPlanSubagentActivityLabel(
  view: DesktopV3TaskChildViewModel | null,
  row: TaskToolRow,
  status: string,
  terminal: boolean,
): string {
  if (view?.loading) return "loading";
  if (view?.unavailable) return "unavailable";
  if (view?.stale) return "stale";
  if (view?.toolActivitySummary.trim()) return view.toolActivitySummary.trim();
  if (terminal) return status;
  if (view?.currentTool) return view.currentTool;
  if (row.tool && row.tool !== "-") return row.tool;
  return status;
}

function contextPercent(view: DesktopV3TaskChildViewModel | null): number | null {
  if (!view || view.contextWindow <= 0 || view.remainingTokens === null) return null;
  return Math.max(0, Math.min(100, ((view.contextWindow - view.remainingTokens) / view.contextWindow) * 100));
}

export function DesktopPlanSubagentList({ children, actions, mode }: DesktopPlanSubagentListProps) {
  if (children.length === 0) return null;
  const label = `${children.length} subagent${children.length === 1 ? "" : "s"}`;
  return (
    <details className={cn("group min-w-0 overflow-hidden", mode !== "thin" && "mx-4 my-3")} data-plan-subagent-list data-display-mode={mode} aria-label="Subagents">
      <summary
        className={cn(
          "flex min-h-9 cursor-pointer list-none items-center rounded-lg border border-[var(--app-border)] text-xs font-medium text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)] [&::-webkit-details-marker]:hidden",
          mode === "thin"
            ? "justify-center px-1"
            : "min-h-10 gap-2 border-[var(--app-border)]/60 bg-[var(--app-surface-subtle)] px-3 text-[var(--app-text)]/90 transition-all hover:border-[var(--app-border)]/80 hover:bg-[var(--app-surface)] focus-visible:ring-1 focus-visible:ring-[var(--app-primary)]/40",
        )}
        aria-label={`Show ${label}`}
      >
        <Bot aria-hidden="true" className={cn("size-3.5 shrink-0 text-[var(--app-text-muted)]", mode !== "thin" && "text-[var(--app-text-subtle)] opacity-80")} />
        <span className={mode === "thin" ? undefined : "font-medium tracking-normal"}>{mode === "thin" ? children.length : label}</span>
        <ChevronRight aria-hidden="true" className={cn("size-3.5 text-[var(--app-text-muted)] transition-transform group-open:rotate-90", mode === "thin" ? "absolute ml-7" : "ml-auto text-[var(--app-text-subtle)] opacity-70")} />
      </summary>
      <div className={cn("min-w-0 overflow-x-hidden overflow-y-auto", mode === "thin" ? "mt-2 grid gap-2" : "mt-2.5 grid max-h-64 gap-2 pr-0.5")}>
        {children.map(({ row, view }) => {
          const title = row.assignmentLabel || row.agent || "Subagent";
          const status = view?.loading ? "loading" : view?.unavailable ? "unavailable" : view?.stale ? "stale" : view?.status || row.status || "pending";
          const terminal = view?.terminal ?? row.terminal;
          const activity = desktopPlanSubagentActivityLabel(view, row, status, terminal);
          const percent = contextPercent(view);
          const canStop = Boolean(row.childSessionId.trim() && view && !view.terminal);
          const details = `${title}. ${activity}. ${percent === null ? "Context unavailable" : `${Math.round(percent)}% context used`}. ${formatElapsed(view?.elapsedMs || row.elapsedMs)} elapsed.`;
          if (mode === "thin") {
            return <button key={row.launchKey || row.childSessionId || row.launchIndex} type="button" className="relative grid min-h-11 place-items-center rounded-lg border border-[var(--app-border)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]" title={details} aria-label={`Open ${details}`} onClick={() => actions?.onNavigate(row.childSessionId, view?.workspacePath || "")} disabled={!row.childSessionId}>
              <span aria-hidden="true" className={cn("size-2.5 rounded-full", statusTone(status, terminal))} />
              {percent !== null ? <span className="text-[9px] text-[var(--app-text-muted)]">{Math.round(percent)}</span> : null}
            </button>;
          }
          return <div key={row.launchKey || row.childSessionId || row.launchIndex} className="group/row grid min-h-12 min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 rounded-lg border border-[var(--app-border)]/40 bg-[var(--app-surface)]/45 px-3 py-2 shadow-sm transition-all duration-200 hover:border-[var(--app-border)]/70 hover:bg-[var(--app-surface)] focus-within:ring-1 focus-within:ring-[var(--app-primary)]/45">
            <span aria-hidden="true" className={cn("size-1.5 shrink-0 rounded-full shadow-sm ring-2 ring-white/5", statusTone(status, terminal))} />
            <button type="button" className="min-w-0 cursor-pointer text-left focus-visible:outline-none" title={title} aria-label={`Open ${details}`} onClick={() => actions?.onNavigate(row.childSessionId, view?.workspacePath || "")} disabled={!row.childSessionId}>
              <div className="truncate text-xs font-medium leading-snug tracking-tight text-[var(--app-text)]/90">{title}</div>
              <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[10px] text-[var(--app-text-muted)]/85">
                <span className="truncate">{activity}</span><span>·</span><span className="shrink-0">{formatElapsed(view?.elapsedMs || row.elapsedMs)}</span>
              </div>
              <div className="mt-1.5 h-1 overflow-hidden rounded-full bg-[var(--app-border)]/20" title={percent === null ? "Context unavailable" : `${Math.round(percent)}% context used`} aria-label={percent === null ? "Context unavailable" : `${Math.round(percent)} percent context used`}>
                {percent !== null ? <div className="h-full rounded-full bg-[var(--app-primary)]/60 transition-all" style={{ width: `${percent}%` }} /> : null}
              </div>
            </button>
            {canStop ? <button type="button" className="grid h-8 w-8 place-items-center rounded-md text-[var(--app-text-subtle)] opacity-100 transition-colors hover:bg-[var(--app-danger-bg)]/80 hover:text-[var(--app-danger)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-primary)]/40 md:opacity-0 md:group-hover/row:opacity-100 md:group-focus-within/row:opacity-100" aria-label={`Stop ${title}`} title={`Stop ${title}`} onClick={() => { void stopSubagentSessionV3Run(row.childSessionId); }}><Square size={13} /></button> : null}
          </div>;
        })}
      </div>
    </details>
  );
}
