import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowDown, Loader2, Square, X } from "lucide-react";
import { Button } from "../../../../components/ui/button";
import { Textarea } from "../../../../components/ui/textarea";
import type { DesktopPermissionRecord } from "../../types/realtime";
import type { StructuredPlanDocument } from "./structured-plan-document";
import { ensureSystemSidechat, fetchSessionMessages } from "../queries/chat-queries";
import type { ChatMessageRecord } from "../types/chat";
import { createDesktopV3ExistingMessageOperation, continueDesktopV3Conversation } from "../../session-v3/existing-session-flow";
import {
  buildDesktopV3ConversationRenderItems,
  chatMessageToMessageSnapshot,
  DesktopV3RenderItemView,
  useDesktopV3StickyBottomScroll,
} from "./desktop-v3-existing-conversation-pane";
import { stopSessionV3Run } from "../../session-v3/api";
import { selectRenderedSessionMessages } from "../../state/desktop-v3-cache-selectors";
import { requireDesktopV3RealtimeControllerReady } from "../../realtime/v3-realtime-controller";
import { useDesktopV3CacheSelector } from "../../state/desktop-v3-cache-store";

interface DesktopPlanAgentSidecarProps {
  parentSessionId: string;
  permission: DesktopPermissionRecord;
  document: StructuredPlanDocument;
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

function pendingProposalRevision(permission: DesktopPermissionRecord, document: StructuredPlanDocument): number {
  try {
    const payload = JSON.parse(permission.toolArguments) as { proposal_revision?: unknown };
    const revision = Number(payload.proposal_revision);
    if (Number.isSafeInteger(revision) && revision > 0) return revision;
  } catch {
    // The permission projection can briefly contain older arguments during hydration.
  }
  return Math.max(1, Number.parseInt(document.revisionId ?? "", 10) || 1);
}

export function DesktopPlanAgentSidecar({
  parentSessionId,
  permission,
  document,
  onClose,
  embedded = false,
  modelLabel = "",
  mobileOpen = true,
}: DesktopPlanAgentSidecarProps) {
  const [sidechat, setSidechat] = useState<SidechatState>(EMPTY_SIDECHAT);
  const [draft, setDraft] = useState("");
  const proposalRevision = pendingProposalRevision(permission, document);
  const realtimeMessages = useDesktopV3CacheSelector(
    (state) => sidechat.sessionId ? state.messagesBySession[sidechat.sessionId]?.items ?? [] : [],
    (left, right) => left === right,
  );
  const rendered = useDesktopV3CacheSelector((state) => selectRenderedSessionMessages(state, sidechat.sessionId));

  useEffect(() => {
    if (realtimeMessages.length === 0) return;
    setSidechat((current) => ({
      ...current,
      messages: realtimeMessages.map((message) => ({
        id: message.id,
        sessionId: message.session_id,
        globalSeq: message.global_seq,
        role: message.role,
        content: message.content,
        createdAt: message.created_at,
      })),
    }));
  }, [realtimeMessages]);

  const refresh = useCallback(async (sessionId: string) => {
    const result = await fetchSessionMessages(sessionId, undefined, 0, { sessionApi: "v3", tail: true, limit: 100 });
    setSidechat((current) => ({ ...current, messages: result.messages }));
  }, []);

  useEffect(() => {
    let cancelled = false;
    setSidechat((current) => ({ ...current, busy: true, error: null }));
    void (async () => {
      try {
        const result = await ensureSystemSidechat({
          parentSessionId,
          kind: "plan",
          permissionId: permission.id,
          planId: document.id || permission.id,
          planRevision: proposalRevision,
        });
        if (cancelled) return;
        setSidechat((current) => ({ ...current, sessionId: result.sessionId, modelLabel: result.model || modelLabel, runtimeSwarmId: result.runtimeSwarmId, busy: false, error: null }));
        const controller = await requireDesktopV3RealtimeControllerReady();
        await controller.ensureSessionConnected(result.sessionId);
        await refresh(result.sessionId);
      } catch (cause) {
        if (!cancelled) setSidechat((current) => ({ ...current, busy: false, error: cause instanceof Error ? cause.message : "Unable to open Plan." }));
      }
    })();
    return () => { cancelled = true; };
  }, [document.id, modelLabel, parentSessionId, permission.id, proposalRevision, refresh]);

  const activeRun = rendered.liveRuns.find((run) => run.status === "running" || run.status === "pending_executor") ?? null;
  const renderItems = useMemo(() => buildDesktopV3ConversationRenderItems({
    ...rendered,
    committed: rendered.committed.length > 0 ? rendered.committed : sidechat.messages.map(chatMessageToMessageSnapshot),
  }), [rendered, sidechat.messages]);
  const { scrollContainerRef, contentRef, isAtBottom, hasUnseenLatest, scrollToBottom } = useDesktopV3StickyBottomScroll({
    resetKey: sidechat.sessionId || `plan:${parentSessionId}`,
    itemCount: renderItems.length,
  });

  const stop = async () => {
    if (!activeRun || !sidechat.sessionId || !sidechat.runtimeSwarmId) return;
    setSidechat((current) => ({ ...current, error: null }));
    try {
      await stopSessionV3Run(sidechat.sessionId, { runId: activeRun.runId, targetSwarmId: sidechat.runtimeSwarmId });
    } catch (cause) {
      setSidechat((current) => ({ ...current, error: cause instanceof Error ? cause.message : "Unable to stop run." }));
    }
  };

  const send = async () => {
    const content = draft.trim();
    if (!content || !sidechat.sessionId || sidechat.busy) return;
    setSidechat((current) => ({ ...current, busy: true, error: null }));
    try {
      const operation = createDesktopV3ExistingMessageOperation({ sessionId: sidechat.sessionId, prompt: content });
      setDraft("");
      await continueDesktopV3Conversation(operation);
    } catch (cause) {
      setSidechat((current) => ({ ...current, error: cause instanceof Error ? cause.message : "Message failed." }));
    } finally {
      setSidechat((current) => ({ ...current, busy: false }));
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
        <header className="flex items-center justify-between gap-2 border-b border-[var(--app-border)] px-3 py-3">
          <div className="font-semibold">Plan</div>
          {onClose ? <Button type="button" variant="ghost" size="sm" className={embedded ? "h-9 w-9 px-0 xl:hidden" : "h-9 w-9 px-0"} aria-label="Close Plan" onClick={onClose}><X size={18} /></Button> : null}
        </header>
        <div className="relative min-h-0 flex-1 overflow-hidden">
          <div ref={scrollContainerRef} className="h-full min-h-0 overflow-x-hidden overflow-y-auto p-4 [scrollbar-gutter:stable]" data-testid="desktop-plan-agent-scroller">
            <div ref={contentRef} className="flex min-h-full min-w-0 flex-col gap-5">
              <div className="rounded-xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] p-3 text-sm leading-5">Ask about the plan or request changes conversationally. Saved edits update the parent approval card live.</div>
              {sidechat.busy && renderItems.length === 0 ? <div className="flex items-center gap-2 text-sm text-[var(--app-text-muted)]"><Loader2 className="animate-spin" size={16} />Opening durable Plan sidechat…</div> : null}
              {renderItems.map((item, index) => <DesktopV3RenderItemView key={`${item.type}:${"id" in item ? item.id : item.type === "pending-user" ? item.message.clientRequestId : "message" in item ? item.message.id : index}`} item={item} thinkingTagsEnabled timerNow={Date.now()} index={index} />)}
              {sidechat.error ? <div role="alert" className="rounded-lg border border-[var(--app-danger)] p-3 text-sm text-[var(--app-danger)]">{sidechat.error}</div> : null}
            </div>
          </div>
          {!isAtBottom && hasUnseenLatest ? <button type="button" aria-label="Jump to latest Plan message" title="Jump to latest Plan message" onClick={() => scrollToBottom("smooth")} className="absolute bottom-3 right-3 z-10 inline-flex h-10 w-10 items-center justify-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface-elevated)] text-[var(--app-text)] shadow-lg"><ArrowDown size={18} aria-hidden="true" /></button> : null}
        </div>
        <div className="space-y-2 border-t border-[var(--app-border)] p-4">
          <Textarea value={draft} onChange={(event) => setDraft(event.target.value)} placeholder="Ask about the plan or request changes…" disabled={sidechat.busy || !sidechat.sessionId} />
          {activeRun ? <Button type="button" variant="outline" className="w-full border-[var(--app-danger)] text-[var(--app-danger)]" disabled={!sidechat.runtimeSwarmId} onClick={() => void stop()}><Square size={14} /> Stop Plan</Button> : <Button type="button" variant="outline" className="w-full" disabled={sidechat.busy || !draft.trim() || !sidechat.sessionId} onClick={() => void send()}>{sidechat.busy ? "Waiting…" : "Send to Plan"}</Button>}
        </div>
      </aside>
    </div>
  );
}
