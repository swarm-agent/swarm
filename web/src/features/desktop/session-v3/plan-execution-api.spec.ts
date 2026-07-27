import test from "node:test";
import assert from "node:assert/strict";

import {
  acceptAndContinueDesktopPlanCheckpoint,
  acceptDesktopPlanCheckpoint,
  archiveDesktopV3Sessions,
  continueDesktopPlanCheckpoint,
  pauseDesktopPlanRun,
  resolveDesktopPlanBlockedCheckpoint,
  restartDesktopPlanCheckpoint,
  resumeDesktopPlanCheckpoint,
  resumeDesktopPlanAutomatic,
  resumeDesktopPlanCheckpointed,
  rewindDesktopPlanCheckpoint,
  jumpDesktopPlanToRevisionCheckpoint,
  startDesktopPlanAutomatic,
  startDesktopPlanCheckpoint,
  startDesktopPlanCheckpointed,
} from "./plan-execution-api";
import { resetDesktopV3CacheForTests } from "../state/desktop-v3-cache-store";
import { createEmptyDesktopV3CacheState } from "../state/desktop-v3-cache-reducer";
import { selectDesktopSidebarGroupedRows } from "../state/desktop-v3-cache-selectors";
import { sessionA } from "../state/desktop-v3-cache.backend-fixtures";

const originalFetch = globalThis.fetch;

test("archiveDesktopV3Sessions posts to the batch archive endpoint and applies the archive result", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  const state = createEmptyDesktopV3CacheState();
  state.desktopSidebarBootstrap = { status: "ready", scopeId: "scope-a" };
  state.sessionOrderByScope["scope-a"] = [sessionA.id];
  state.sessionsById[sessionA.id] = {
    kind: "full",
    session: { ...sessionA, title: "Cached title" },
    needsHydrate: false,
  };
  resetDesktopV3CacheForTests(state);
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    archived: true,
    results: [
      {
        session_id: sessionA.id,
        archived: true,
        tombstone: {
          session_id: sessionA.id,
          kind: "archived",
          archived: true,
        },
      },
    ],
  });

  try {
    await archiveDesktopV3Sessions([` ${sessionA.id} `]);

    assert.deepEqual(calls, [
      {
        url: "/v3/sessions:archive",
        body: { session_ids: [sessionA.id] },
      },
    ]);
    const grouped = selectDesktopSidebarGroupedRows(state);
    assert.deepEqual(grouped.active_chats, []);
    assert.deepEqual(
      grouped.archived.map((row) => row.sessionId),
      [sessionA.id],
    );
    assert.equal(
      grouped.archived[0].record.kind === "full"
        ? grouped.archived[0].record.session.title
        : "",
      "Cached title",
    );
  } finally {
    globalThis.fetch = originalFetch;
    resetDesktopV3CacheForTests();
  }
});

test("startDesktopPlanAutomatic calls the dedicated start-automatic lifecycle endpoint only", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: "plan-1",
    transition: "start_plan_automatic",
  });

  try {
    await startDesktopPlanAutomatic("session-1", "plan-1", {
      checkpointId: "cp-final",
      executionGranularity: "checkpointed",
      continuationPolicy: "automatic",
      continueAutomatically: true,
    });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/plans/plan-1/start-automatic",
      body: {
        checkpoint_id: "cp-final",
        execution_granularity: "checkpointed",
        continuation_policy: "automatic",
        continue_automatically: true,
      },
    },
  ]);
});

test("startDesktopPlanCheckpointed calls the dedicated start-checkpointed lifecycle endpoint only", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: "plan-1",
    transition: "start_plan_checkpointed",
  });

  try {
    await startDesktopPlanCheckpointed("session-1", "plan-1", {
      checkpointId: "cp-2",
      executionGranularity: "checkpointed",
      continuationPolicy: "review_each_checkpoint",
      continueAutomatically: false,
    });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/plans/plan-1/start-checkpointed",
      body: {
        checkpoint_id: "cp-2",
        execution_granularity: "checkpointed",
        continuation_policy: "review_each_checkpoint",
        continue_automatically: false,
      },
    },
  ]);
});

test("checkpoint buttons call checkpoint-specific lifecycle endpoints without run stream chaining", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: "plan-1",
    transition: "continue_checkpoint",
    checkpoint_id: "cp-1",
    run_intent: { run_id: "run-1" },
    run_queued: true,
  });

  try {
    await continueDesktopPlanCheckpoint("session-1", "cp-1");
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-1/continue",
      body: {},
    },
  ]);
});

test("acceptDesktopPlanCheckpoint ignores stale fresh-run hints from the response", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: "plan-1",
    transition: "accept_checkpoint",
    checkpoint_id: "cp-2",
    next_action: "run_checkpoint_with_fresh_context",
    run_request: {
      plan_checkpoint_context: {
        plan_id: "plan-1",
        checkpoint_id: "cp-2",
        attempt_id: "cp-2:attempt-1",
      },
    },
  });

  try {
    await acceptDesktopPlanCheckpoint("session-1", "cp-2");
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-2/accept",
      body: {},
    },
  ]);
});

test("acceptAndContinueDesktopPlanCheckpoint accepts review then starts the next checkpoint run", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = sequenceFetch(calls, [
    {
      ok: true,
      plan_id: "plan-1",
      transition: "accept_checkpoint",
      checkpoint_id: "cp-1",
      execution_summary: {
        next_checkpoint_id: "cp-2",
        next_checkpoint_status: "pending",
        plan_complete: false,
        review_required: false,
      },
      plan: {
        id: "plan-1",
        document: {
          id: "plan-1",
          active_checkpoint_id: "cp-2",
          checkpoints: [],
        },
      },
    },
    {
      ok: true,
      plan_id: "plan-1",
      transition: "continue_checkpoint",
      checkpoint_id: "cp-2",
      run_intent: { run_id: "run-2" },
      run_queued: true,
    },
  ]);

  try {
    await acceptAndContinueDesktopPlanCheckpoint("session-1", "cp-1");
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-1/accept",
      body: {},
    },
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-2/continue",
      body: { plan_id: "plan-1", suppress_lifecycle_message: true },
    },
  ]);
});

test("acceptAndContinueDesktopPlanCheckpoint does not start another run when accept completes the plan", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = sequenceFetch(calls, [
    {
      ok: true,
      plan_id: "plan-1",
      transition: "accept_checkpoint",
      checkpoint_id: "cp-2",
      execution_summary: { plan_complete: true },
    },
  ]);

  try {
    await acceptAndContinueDesktopPlanCheckpoint("session-1", "cp-2");
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-2/accept",
      body: {},
    },
  ]);
});

test("current-run lifecycle controls call dedicated endpoints without run stream chaining", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: "plan-1",
    transition: "resume_automatic",
  });

  try {
    await pauseDesktopPlanRun("session-1", { planId: "plan-1" });
    await resumeDesktopPlanAutomatic("session-1", { planId: "plan-1" });
    await resumeDesktopPlanCheckpointed("session-1", { planId: "plan-1" });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/runs/current/pause",
      body: { plan_id: "plan-1" },
    },
    {
      url: "/v3/sessions/session-1/plan-mode/runs/current/resume-automatic",
      body: { plan_id: "plan-1" },
    },
    {
      url: "/v3/sessions/session-1/plan-mode/runs/current/resume-checkpointed",
      body: { plan_id: "plan-1" },
    },
  ]);
});

test("sidebar policy switch endpoints persist policy without requiring a checkpoint start payload", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: "plan-1",
    transition: "resume_checkpointed",
  });

  try {
    await resumeDesktopPlanAutomatic("session-1");
    await resumeDesktopPlanCheckpointed("session-1");
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/runs/current/resume-automatic",
      body: {},
    },
    {
      url: "/v3/sessions/session-1/plan-mode/runs/current/resume-checkpointed",
      body: {},
    },
  ]);
});

test("jumpDesktopPlanToRevisionCheckpoint calls the explicit jump lifecycle endpoint with skip_prior", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: "plan-1",
    transition: "jump_to_checkpoint",
  });

  try {
    await jumpDesktopPlanToRevisionCheckpoint("session-1", {
      planId: "plan-1",
      version: 3,
      checkpointId: "cp-9",
    });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/lifecycle/jump-to-checkpoint",
      body: {
        plan_id: "plan-1",
        version: 3,
        checkpoint_id: "cp-9",
        restart: true,
        start: true,
        skip_prior: true,
      },
    },
  ]);
});

test("resolveDesktopPlanBlockedCheckpoint calls resolve-block with optional start_next", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: "plan-1",
    transition: "resolve_blocked_checkpoint",
  });

  try {
    await resolveDesktopPlanBlockedCheckpoint("session-1", "cp-1", {
      planId: "plan-1",
      startNext: true,
      notes: "fixed",
    });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-1/resolve-block",
      body: { plan_id: "plan-1", notes: "fixed", start_next: true },
    },
  ]);
});

test("checkpoint start reset controls call dedicated endpoints without run stream chaining", async () => {
  const calls: Array<{ url: string; body: unknown }> = [];
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    plan_id: "plan-1",
    run_intent: { run_id: "run-1" },
    run_queued: true,
  });

  try {
    await startDesktopPlanCheckpoint("session-1", "cp-1", { planId: "plan-1" });
    await resumeDesktopPlanCheckpoint("session-1", "cp-1", { planId: "plan-1" });
    await restartDesktopPlanCheckpoint("session-1", "cp-1", {
      planId: "plan-1",
    });
    await rewindDesktopPlanCheckpoint("session-1", "cp-1", {
      planId: "plan-1",
    });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-1/start",
      body: { plan_id: "plan-1" },
    },
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-1/resume",
      body: { plan_id: "plan-1" },
    },
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-1/restart",
      body: { plan_id: "plan-1" },
    },
    {
      url: "/v3/sessions/session-1/plan-mode/checkpoints/cp-1/rewind",
      body: { plan_id: "plan-1" },
    },
  ]);
});

function jsonFetch(
  calls: Array<{ url: string; body: unknown }>,
  payload: Record<string, unknown>,
): typeof fetch {
  return sequenceFetch(calls, [payload]);
}

function sequenceFetch(
  calls: Array<{ url: string; body: unknown }>,
  payloads: Record<string, unknown>[],
): typeof fetch {
  let index = 0;
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({
      url,
      body: init?.body ? JSON.parse(String(init.body)) : null,
    });
    assert.doesNotMatch(url, /\/plans\/execution$/);
    assert.doesNotMatch(url, /\/run\/stream$/);
    const payload = payloads[Math.min(index, payloads.length - 1)] ?? {};
    index += 1;
    return new Response(JSON.stringify(payload), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;
}
