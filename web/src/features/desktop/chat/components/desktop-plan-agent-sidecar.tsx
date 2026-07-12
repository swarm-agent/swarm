import { useCallback, useEffect, useMemo, useState } from "react";
import { Bot, Loader2, Square, Sparkles, X } from "lucide-react";
import { Button } from "../../../../components/ui/button";
import { Textarea } from "../../../../components/ui/textarea";
import type { DesktopPermissionRecord } from "../../types/realtime";
import type { StructuredPlanDocument } from "./structured-plan-document";
import { ChatMarkdown } from "./chat-markdown";
import { DesktopPlanExecutionSidebar, type DesktopPlanExecutionSidebarProps } from "./desktop-plan-execution-sidebar";
import { ensureSystemSidechat, fetchSessionMessages, sendSessionMessage, type SystemSidechatKind } from "../queries/chat-queries";
import type { ChatMessageRecord } from "../types/chat";
import { stopSessionV3Run } from "../../session-v3/api";
import { selectRenderedSessionMessages } from "../../state/desktop-v3-cache-selectors";
import { requireDesktopV3RealtimeControllerReady } from "../../realtime/v3-realtime-controller";
import { useDesktopV3CacheSelector } from "../../state/desktop-v3-cache-store";

interface DesktopPlanAgentSidecarProps {
  parentSessionId: string;
  permission?: DesktopPermissionRecord | null;
  document?: StructuredPlanDocument | null;
  execution?: DesktopPlanExecutionSidebarProps;
  onClose?: () => void;
  embedded?: boolean;
  modelLabel?: string;
  mobileOpen?: boolean;
}

interface SidechatState {
  sessionId: string;
  messages: ChatMessageRecord[];
  modelLabel: string;
  runtimeSwarmId: string;
  busy: boolean;
  error: string | null;
}

const EMPTY_SIDECHAT: SidechatState = { sessionId: "", messages: [], modelLabel: "", runtimeSwarmId: "", busy: false, error: null };

function pendingProposalRevision(permission: DesktopPermissionRecord | null, document: StructuredPlanDocument | null): number {
  if (permission) {
    try {
      const payload = JSON.parse(permission.toolArguments) as { proposal_revision?: unknown };
      const revision = Number(payload.proposal_revision);
      if (Number.isSafeInteger(revision) && revision > 0) return revision;
    } catch {
      // The permission projection can briefly contain older arguments during hydration.
    }
  }
  return Math.max(1, Number.parseInt(document?.revisionId ?? "", 10) || 1);
}

export function DesktopPlanAgentSidecar({
  parentSessionId,
  permission = null,
  document = null,
  execution,
  onClose,
  embedded = false,
  modelLabel = "",
  mobileOpen = true,
}: DesktopPlanAgentSidecarProps) {
  const [activeTab, setActiveTab] = useState<SystemSidechatKind>("plan");
  const [sidechats, setSidechats] = useState<Record<SystemSidechatKind, SidechatState>>({ plan: EMPTY_SIDECHAT, ai: EMPTY_SIDECHAT });
  const [draft, setDraft] = useState("");
  const hasPlan = Boolean(document && permission);
  const proposalRevision = pendingProposalRevision(permission, document);
  const planSessionId = sidechats.plan.sessionId;
  const aiSessionId = sidechats.ai.sessionId;
  const planRealtimeMessages = useDesktopV3CacheSelector(
    (state) => planSessionId ? state.messagesBySession[planSessionId]?.items ?? [] : [],
    (left, right) => left === right,
  );
  const aiRealtimeMessages = useDesktopV3CacheSelector(
    (state) => aiSessionId ? state.messagesBySession[aiSessionId]?.items ?? [] : [],
    (left, right) => left === right,
  );
  const planRuns = useDesktopV3CacheSelector((state) => planSessionId ? selectRenderedSessionMessages(state, planSessionId).liveRuns : []);
  const aiRuns = useDesktopV3CacheSelector((state) => aiSessionId ? selectRenderedSessionMessages(state, aiSessionId).liveRuns : []);

  useEffect(() => {
    const apply = (kind: SystemSidechatKind, snapshots: typeof planRealtimeMessages) => {
      if (snapshots.length === 0) return;
      setSidechats((current) => ({
        ...current,
        [kind]: {
          ...current[kind],
          messages: snapshots.map((message) => ({
            id: message.id,
            sessionId: message.session_id,
            globalSeq: message.global_seq,
            role: message.role,
            content: message.content,
            createdAt: message.created_at,
          })),
        },
      }));
    };
    apply("plan", planRealtimeMessages);
    apply("ai", aiRealtimeMessages);
  }, [aiRealtimeMessages, planRealtimeMessages]);

  const refresh = useCallback(async (kind: SystemSidechatKind, sessionId: string) => {
    const result = await fetchSessionMessages(sessionId, undefined, 0, { sessionApi: "v3", tail: true, limit: 100 });
    setSidechats((current) => ({ ...current, [kind]: { ...current[kind], messages: result.messages } }));
  }, []);

  useEffect(() => {
    let cancelled = false;
    const ensure = async (kind: SystemSidechatKind) => {
      if (kind === "plan" && (!permission || !document)) return;
      setSidechats((current) => ({ ...current, [kind]: { ...current[kind], busy: true, error: null } }));
      try {
        const result = await ensureSystemSidechat({
          parentSessionId,
          kind,
          permissionId: permission?.id,
          planId: document?.id || permission?.id,
          planRevision: proposalRevision,
        });
        if (cancelled) return;
        setSidechats((current) => ({
          ...current,
          [kind]: { ...current[kind], sessionId: result.sessionId, modelLabel: result.model || modelLabel, runtimeSwarmId: result.runtimeSwarmId, busy: false, error: null },
        }));
        const controller = await requireDesktopV3RealtimeControllerReady();
        await controller.ensureSessionConnected(result.sessionId);
        await refresh(kind, result.sessionId);
      } catch (cause) {
        if (!cancelled) setSidechats((current) => ({
          ...current,
          [kind]: { ...current[kind], busy: false, error: cause instanceof Error ? cause.message : `Unable to open ${kind === "plan" ? "Plan" : "AI"}.` },
        }));
      }
    };
    void ensure("ai");
    void ensure("plan");
    return () => { cancelled = true; };
  }, [document, modelLabel, parentSessionId, permission?.id, proposalRevision, refresh]);

  const selected = sidechats[activeTab];
  const selectedRuns = activeTab === "plan" ? planRuns : aiRuns;
  const activeRun = selectedRuns.find((run) => run.status === "running" || run.status === "pending_executor") ?? null;
  const visibleMessages = useMemo(
    () => selected.messages.filter((message) => message.role === "user" || message.role === "assistant"),
    [selected.messages],
  );

  const stop = async () => {
    if (!activeRun || !selected.sessionId || !selected.runtimeSwarmId) return;
    setSidechats((current) => ({ ...current, [activeTab]: { ...current[activeTab], error: null } }));
    try {
      await stopSessionV3Run(selected.sessionId, { runId: activeRun.runId, targetSwarmId: selected.runtimeSwarmId });
    } catch (cause) {
      setSidechats((current) => ({ ...current, [activeTab]: { ...current[activeTab], error: cause instanceof Error ? cause.message : "Unable to stop run." } }));
    }
  };

  const send = async () => {
    const content = draft.trim();
    if (!content || !selected.sessionId || selected.busy) return;
    setSidechats((current) => ({ ...current, [activeTab]: { ...current[activeTab], busy: true, error: null } }));
    try {
      await sendSessionMessage(selected.sessionId, "user", content, null, { sessionApi: "v3" });
      setDraft("");
      await refresh(activeTab, selected.sessionId);
    } catch (cause) {
      setSidechats((current) => ({ ...current, [activeTab]: { ...current[activeTab], error: cause instanceof Error ? cause.message : "Message failed." } }));
    } finally {
      setSidechats((current) => ({ ...current, [activeTab]: { ...current[activeTab], busy: false } }));
    }
  };

  return (
    <div
      className={embedded
        ? mobileOpen
          ? "fixed inset-0 z-50 flex bg-black/30 xl:static xl:z-auto xl:min-h-0 xl:w-[360px] xl:max-w-[360px] xl:border-l xl:border-[var(--app-border)] xl:bg-[var(--app-surface)]"
          : "hidden xl:flex xl:min-h-0 xl:w-[360px] xl:max-w-[360px] xl:border-l xl:border-[var(--app-border)] xl:bg-[var(--app-surface)]"
        : "fixed inset-0 z-50 bg-black/30 md:left-auto md:w-[28rem]"}
      data-testid="desktop-plan-agent-sidecar"
      data-embedded={embedded ? "true" : "false"}
    >
      <aside className={embedded
        ? "absolute inset-x-0 bottom-0 flex max-h-[88vh] min-h-0 min-w-0 flex-col rounded-t-2xl bg-[var(--app-surface)] shadow-2xl xl:static xl:max-h-none xl:flex-1 xl:rounded-none xl:shadow-none"
        : "absolute inset-x-0 bottom-0 flex max-h-[88vh] flex-col rounded-t-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-2xl md:inset-y-0 md:right-0 md:max-h-none md:w-[28rem] md:rounded-none md:rounded-l-2xl"}>
        <header className="border-b border-[var(--app-border)] px-3 pt-3">
          <div className="flex items-center justify-between gap-2">
            <div className="font-semibold">Plan & AI</div>
            {onClose ? <Button type="button" variant="ghost" size="sm" className={embedded ? "h-9 w-9 px-0 xl:hidden" : "h-9 w-9 px-0"} aria-label="Close Plan and AI" onClick={onClose}><X size={18} /></Button> : null}
          </div>
          <div className="mt-2 flex gap-1" role="tablist" aria-label="Plan and AI sidechats">
            {(["plan", "ai"] as const).map((kind) => (
              <button key={kind} type="button" role="tab" aria-selected={activeTab === kind} className={`flex flex-1 items-center justify-center gap-2 border-b-2 px-3 py-2 text-sm font-medium ${activeTab === kind ? "border-[var(--app-primary)] text-[var(--app-primary)]" : "border-transparent text-[var(--app-text-muted)]"}`} onClick={() => setActiveTab(kind)}>
                {kind === "plan" ? <Bot size={16} /> : <Sparkles size={16} />}{kind === "plan" ? "Plan" : "AI"}
              </button>
            ))}
          </div>
        </header>
        <div className="min-h-0 flex-1 space-y-3 overflow-y-auto p-4">
          {activeTab === "plan" && execution ? <DesktopPlanExecutionSidebar {...execution} embedded /> : null}
          {activeTab === "plan" && !hasPlan ? <div className="rounded-xl border border-[var(--app-border)] p-3 text-sm text-[var(--app-text-muted)]">The Plan conversation is retained here. A pending or active plan is required to bind it.</div> : null}
          <div className="rounded-xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] p-3 text-sm leading-5">
            {activeTab === "plan" ? "Ask about the plan or request changes conversationally. Saved edits update the parent approval card live." : "Use this durable auto-only AI sidechat without leaving the parent conversation."}
          </div>
          {selected.busy && visibleMessages.length === 0 ? <div className="flex items-center gap-2 text-sm text-[var(--app-text-muted)]"><Loader2 className="animate-spin" size={16} />Opening durable {activeTab === "plan" ? "Plan" : "AI"} sidechat…</div> : null}
          {visibleMessages.map((message) => <div key={message.id} className={message.role === "user" ? "ml-8 rounded-xl bg-[var(--app-surface-hover)] p-3 text-sm" : "mr-4 rounded-xl border border-[var(--app-border)] p-3 text-sm"}><ChatMarkdown content={message.content} /></div>)}
          {activeRun?.assistantDraft?.content ? <div className="mr-4 rounded-xl border border-[var(--app-border)] p-3 text-sm"><ChatMarkdown content={activeRun.assistantDraft.content} /></div> : null}
          {selected.error ? <div role="alert" className="rounded-lg border border-[var(--app-danger)] p-3 text-sm text-[var(--app-danger)]">{selected.error}</div> : null}
        </div>
        <div className="space-y-2 border-t border-[var(--app-border)] p-4">
          <Textarea value={draft} onChange={(event) => setDraft(event.target.value)} placeholder={activeTab === "plan" ? "Ask about the plan or request changes…" : "Ask AI…"} disabled={selected.busy || !selected.sessionId} />
          {activeRun ? <Button type="button" variant="outline" className="w-full border-[var(--app-danger)] text-[var(--app-danger)]" disabled={!selected.runtimeSwarmId} onClick={() => void stop()}><Square size={14} /> Stop {activeTab === "plan" ? "Plan" : "AI"}</Button> : <Button type="button" variant="outline" className="w-full" disabled={selected.busy || !draft.trim() || !selected.sessionId} onClick={() => void send()}>{selected.busy ? "Waiting…" : `Send to ${activeTab === "plan" ? "Plan" : "AI"}`}</Button>}
        </div>
      </aside>
    </div>
  );
}
