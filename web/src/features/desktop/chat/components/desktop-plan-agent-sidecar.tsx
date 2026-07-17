import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { ArrowDown, Loader2, Mic, Send, Square, X } from "lucide-react";
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
import { DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME, DesktopV3CompactButton } from "./desktop-v3-agentic-composer";
import { selectRenderedSessionMessages } from "../../state/desktop-v3-cache-selectors";
import { requireDesktopV3RealtimeControllerReady } from "../../realtime/v3-realtime-controller";
import { useDesktopV3CacheSelector } from "../../state/desktop-v3-cache-store";
import { compactDesktopV3Session } from "../../session-v3/compact-session-flow";
import { formatContextWindow } from "../services/model-options";

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

type SpeechRecognitionLike = {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onend: (() => void) | null;
  onerror: ((event: { error?: string; message?: string }) => void) | null;
  onresult: ((event: { resultIndex: number; results: ArrayLike<{ isFinal: boolean; 0?: { transcript: string } }> }) => void) | null;
  start: () => void;
  stop: () => void;
  abort: () => void;
};

type SpeechRecognitionWindow = Window & typeof globalThis & {
  SpeechRecognition?: new () => SpeechRecognitionLike;
  webkitSpeechRecognition?: new () => SpeechRecognitionLike;
};

function speechRecognitionConstructor(): (new () => SpeechRecognitionLike) | null {
  if (typeof window === "undefined") return null;
  const speechWindow = window as SpeechRecognitionWindow;
  return speechWindow.SpeechRecognition ?? speechWindow.webkitSpeechRecognition ?? null;
}

function appendDictation(base: string, addition: string): string {
  const next = addition.replace(/\s+/g, " ").trim();
  if (!next) return base;
  const current = base.replace(/[ \t]+$/g, "");
  return `${current}${current && !/[\s\n]$/.test(current) && !/^[,.;:!?]/.test(next) ? " " : ""}${next}`;
}

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
  const [compactStartedAt, setCompactStartedAt] = useState<number | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null);
  const dictationBaseRef = useRef("");
  const [dictationSupported, setDictationSupported] = useState(false);
  const [dictationEnabled, setDictationEnabled] = useState(false);
  const proposalRevision = pendingProposalRevision(permission, document);
  const realtimeMessages = useDesktopV3CacheSelector(
    (state) => sidechat.sessionId ? state.messagesBySession[sidechat.sessionId]?.items ?? [] : [],
    (left, right) => left === right,
  );
  const rendered = useDesktopV3CacheSelector((state) => selectRenderedSessionMessages(state, sidechat.sessionId));
  const rawUsage = useDesktopV3CacheSelector((state) => sidechat.sessionId ? state.usageBySession[sidechat.sessionId] : undefined);
  const contextWindow = Number((rawUsage as Record<string, unknown> | undefined)?.context_window ?? (rawUsage as Record<string, unknown> | undefined)?.contextWindow ?? 0);
  const remainingTokens = Number((rawUsage as Record<string, unknown> | undefined)?.remaining_tokens ?? (rawUsage as Record<string, unknown> | undefined)?.remainingTokens ?? 0);
  const hasContextUsage = Number.isFinite(contextWindow) && contextWindow > 0 && Number.isFinite(remainingTokens) && remainingTokens >= 0;
  const contextUsagePercent = hasContextUsage ? Math.max(0, Math.min(100, ((contextWindow - remainingTokens) / contextWindow) * 100)) : 0;
  const contextLabel = hasContextUsage ? `${Math.round(contextUsagePercent)}%` : "ctx";
  const contextTooltip = hasContextUsage
    ? `Remaining context ${formatContextWindow(remainingTokens)} of ${formatContextWindow(contextWindow)}.`
    : "Context window unavailable";

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

  const resizeTextarea = useCallback((textarea: HTMLTextAreaElement | null) => {
    if (!textarea) return;
    if (!textarea.value) {
      textarea.style.removeProperty("height");
      textarea.style.overflowY = "hidden";
      return;
    }
    textarea.style.height = "auto";
    const viewportMaxHeight = typeof window === "undefined" ? 360 : Math.max(120, Math.floor(window.innerHeight * 0.5));
    textarea.style.height = `${Math.min(textarea.scrollHeight, viewportMaxHeight)}px`;
    textarea.style.overflowY = textarea.scrollHeight > viewportMaxHeight ? "auto" : "hidden";
  }, []);

  useEffect(() => resizeTextarea(textareaRef.current), [draft, resizeTextarea]);

  useEffect(() => {
    const Recognition = speechRecognitionConstructor();
    setDictationSupported(Boolean(Recognition));
    if (!Recognition) return;
    const recognition = new Recognition();
    recognition.continuous = true;
    recognition.interimResults = true;
    recognition.lang = typeof navigator === "undefined" ? "en-US" : navigator.language || "en-US";
    recognition.onresult = (event) => {
      let transcript = "";
      for (let index = 0; index < event.results.length; index += 1) {
        transcript = appendDictation(transcript, event.results[index]?.[0]?.transcript ?? "");
      }
      setDraft(appendDictation(dictationBaseRef.current, transcript));
    };
    recognition.onerror = (event) => {
      setDictationEnabled(false);
      setSidechat((current) => ({ ...current, error: event.message || (event.error === "not-allowed" ? "Microphone permission was denied." : "Browser speech recognition failed.") }));
    };
    recognition.onend = () => setDictationEnabled(false);
    recognitionRef.current = recognition;
    return () => {
      try { recognition.abort(); } catch { /* ignore browser recognition teardown races */ }
      recognitionRef.current = null;
    };
  }, []);

  const toggleDictation = useCallback(() => {
    const recognition = recognitionRef.current;
    if (!recognition || sidechat.busy || !sidechat.sessionId) return;
    if (dictationEnabled) {
      recognition.stop();
      setDictationEnabled(false);
      return;
    }
    dictationBaseRef.current = draft;
    setSidechat((current) => ({ ...current, error: null }));
    try {
      recognition.start();
      setDictationEnabled(true);
    } catch (cause) {
      setSidechat((current) => ({ ...current, error: cause instanceof Error ? cause.message : "Browser speech recognition failed to start." }));
    }
  }, [dictationEnabled, draft, sidechat.busy, sidechat.sessionId]);

  const activeRun = rendered.liveRuns.find((run) => run.status === "running" || run.status === "pending_executor") ?? null;
  const renderItems = useMemo(() => buildDesktopV3ConversationRenderItems({
    ...rendered,
    committed: rendered.committed.length > 0 ? rendered.committed : sidechat.messages.map(chatMessageToMessageSnapshot),
  }), [rendered, sidechat.messages]);
  const { scrollContainerRef, contentRef, isAtBottom, scrollToBottom } = useDesktopV3StickyBottomScroll({
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

  const compact = async () => {
    if (!sidechat.sessionId || sidechat.busy || activeRun || compactStartedAt !== null) return;
    setCompactStartedAt(Date.now());
    setSidechat((current) => ({ ...current, error: null }));
    try {
      await compactDesktopV3Session({
        sessionId: sidechat.sessionId,
        note: draft,
        agentName: "system-plan-sidechat",
      });
    } catch (cause) {
      setSidechat((current) => ({ ...current, error: cause instanceof Error ? cause.message : "Unable to compact Plan context." }));
    } finally {
      setCompactStartedAt(null);
    }
  };

  const send = async () => {
    const content = draft.trim();
    if (!content || !sidechat.sessionId || sidechat.busy) return;
    setSidechat((current) => ({ ...current, busy: true, error: null }));
    try {
      const operation = createDesktopV3ExistingMessageOperation({ sessionId: sidechat.sessionId, prompt: content });
      setDraft("");
      if (dictationEnabled) {
        try { recognitionRef.current?.abort(); } catch { /* ignore browser recognition shutdown races */ }
        setDictationEnabled(false);
      }
      await continueDesktopV3Conversation(operation);
    } catch (cause) {
      setSidechat((current) => ({ ...current, error: cause instanceof Error ? cause.message : "Message failed." }));
    } finally {
      setSidechat((current) => ({ ...current, busy: false }));
    }
  };

  const handleComposerKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    if (draft.trim() && !sidechat.busy && sidechat.sessionId) void send();
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
          <div ref={scrollContainerRef} className="h-full min-h-0 overflow-x-hidden overflow-y-auto p-4 [scrollbar-gutter:stable]" data-testid="desktop-plan-agent-scroller" tabIndex={0}>
            <div ref={contentRef} className="flex min-h-full min-w-0 flex-col gap-5 [&>*:not(:last-child)]:[overflow-anchor:none]">
              <div className="rounded-xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] p-3 text-sm leading-5">Ask about the plan or request changes conversationally. Saved edits update the parent approval card live.</div>
              {sidechat.busy && renderItems.length === 0 ? <div className="flex items-center gap-2 text-sm text-[var(--app-text-muted)]"><Loader2 className="animate-spin" size={16} />Opening durable Plan sidechat…</div> : null}
              {renderItems.map((item, index) => <DesktopV3RenderItemView key={`${item.type}:${"id" in item ? item.id : item.type === "pending-user" ? item.message.clientRequestId : "message" in item ? item.message.id : index}`} item={item} thinkingTagsEnabled timerNow={Date.now()} index={index} />)}
              {sidechat.error ? <div role="alert" className="rounded-lg border border-[var(--app-danger)] p-3 text-sm text-[var(--app-danger)]">{sidechat.error}</div> : null}
              <div aria-hidden="true" data-testid="desktop-plan-agent-tail-anchor" className="h-px shrink-0 [overflow-anchor:auto]" />
            </div>
          </div>
          {!isAtBottom ? <button type="button" aria-label="Jump to latest Plan message" title="Jump to latest Plan message" onClick={() => scrollToBottom("smooth")} className="absolute bottom-3 right-3 z-10 inline-flex h-10 w-10 items-center justify-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface-elevated)] text-[var(--app-text)] shadow-lg"><ArrowDown size={18} aria-hidden="true" /></button> : null}
        </div>
        <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)]" data-testid="desktop-plan-composer">
          <div className={DESKTOP_V3_COMPOSER_FRAME_CLASS_NAME}>
            <div className="relative min-w-0 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] transition-colors focus-within:border-[var(--app-border-accent)]">
              <div className="flex min-w-0 items-end gap-3 px-4 py-2 sm:py-3 lg:py-2.5">
                <div className="min-w-0 flex-1">
                  <Textarea
                    ref={textareaRef}
                    value={draft}
                    onChange={(event) => {
                      if (dictationEnabled) dictationBaseRef.current = event.target.value;
                      setDraft(event.target.value);
                      resizeTextarea(event.target);
                    }}
                    onKeyDown={handleComposerKeyDown}
                    placeholder="Talk to your plan"
                    aria-label="Plan message"
                    className="max-h-[50vh] !min-h-[32px] resize-none overflow-y-hidden !rounded-none !border-0 !border-none bg-transparent px-0 py-0 !shadow-none !outline-none !ring-0 focus:!border-0 focus:!shadow-none focus:!ring-0 focus-visible:!border-0 focus-visible:!shadow-none focus-visible:!ring-0 focus-visible:!ring-offset-0 hover:!border-0 disabled:bg-transparent sm:!min-h-[56px] lg:!min-h-[52px]"
                    rows={1}
                    disabled={sidechat.busy || !sidechat.sessionId}
                  />
                </div>
              </div>
              <div className="flex min-w-0 items-center justify-between gap-2 overflow-hidden bg-transparent px-4 py-3 text-[11px]">
                <DesktopV3CompactButton
                  contextLabel={contextLabel}
                  contextTooltip={contextTooltip}
                  disabled={sidechat.busy || !sidechat.sessionId || Boolean(activeRun) || compactStartedAt !== null}
                  onClick={() => { void compact(); }}
                />
                <div className="flex items-center justify-end gap-2">
                  <button
                    type="button"
                    onClick={toggleDictation}
                    disabled={sidechat.busy || !sidechat.sessionId || !dictationSupported}
                    aria-pressed={dictationEnabled}
                    aria-label={dictationEnabled ? "Stop microphone dictation" : "Start microphone dictation"}
                    title={dictationSupported ? (dictationEnabled ? "Stop dictation" : "Start dictation") : "Speech recognition is not available in this browser"}
                    className={dictationEnabled ? "inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-[var(--app-border-accent)] bg-[var(--app-primary)] text-[var(--app-primary-text)]" : "inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-surface)] text-[var(--app-text-muted)] disabled:cursor-not-allowed disabled:opacity-50"}
                  ><Mic size={15} className={dictationEnabled ? "animate-pulse" : undefined} /></button>
                  <Button type="button" size="sm" className="h-10 w-10 shrink-0 rounded-lg p-0" disabled={activeRun ? !sidechat.runtimeSwarmId : sidechat.busy || !draft.trim() || !sidechat.sessionId} aria-label={activeRun ? "Stop Plan" : "Send to Plan"} onClick={() => activeRun ? void stop() : void send()}>
                    {activeRun ? <Square size={16} /> : sidechat.busy ? <Loader2 size={16} className="animate-spin" /> : <Send size={17} />}
                  </Button>
                </div>
              </div>
            </div>
          </div>
        </div>
      </aside>
    </div>
  );
}
