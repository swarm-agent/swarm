import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import {
  SwarmApiError,
  SwarmConflictError,
  SwarmForbiddenError,
  SwarmNotFoundError,
  SwarmTimeoutError,
} from '../errors.js';
import { SwarmProjectsNamespace } from '../projects.js';
import { SwarmTransport } from '../transport.js';
import type { ProjectRecord, ProjectTaskRecord } from '../types.js';

interface TestServerContext {
  server: http.Server;
  baseUrl: string;
  projects: SwarmProjectsNamespace;
  receivedRequests: Array<{ method: string; url: string; headers: http.IncomingHttpHeaders; body: any }>;
}

async function createTestServer(
  handler: (req: http.IncomingMessage, res: http.ServerResponse, body: any) => void
): Promise<TestServerContext> {
  const receivedRequests: Array<{ method: string; url: string; headers: http.IncomingHttpHeaders; body: any }> = [];

  const server = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      const rawText = Buffer.concat(chunks).toString('utf8');
      let body: any = null;
      if (rawText) {
        try {
          body = JSON.parse(rawText);
        } catch {
          body = rawText;
        }
      }
      receivedRequests.push({
        method: req.method || '',
        url: req.url || '',
        headers: req.headers,
        body,
      });
      handler(req, res, body);
    });
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;
  const baseUrl = `http://127.0.0.1:${port}`;
  const transport = new SwarmTransport({
    baseUrl,
    token: 'test_token',
    defaultHeaders: {},
    timeoutMs: 5000,
  });
  const projects = new SwarmProjectsNamespace(transport);

  return { server, baseUrl, projects, receivedRequests };
}

async function closeTestServer(ctx: TestServerContext): Promise<void> {
  await new Promise<void>((resolve) => ctx.server.close(() => resolve()));
}

// ============================================================================
// 1. Projects CRUD & Context Synthesis Tests
// ============================================================================

test('SwarmProjectsNamespace: Project CRUD, synthesis, and clear-context endpoints', async () => {
  // Written Purpose:
  // - Product Requirement: Typed client.projects must perform CRUD against /v3/projects,
  //   synthesize Markdown context, and clear orchestrator context with clean session replacement.
  // - Threat/regression: Path corruption, missing auth forwarding, or dropped project metadata.
  // - Boundary: Server.handleProjects in swarmd/internal/api/projects.go.
  // - Narrowest test layer: Hermetic HTTP transport test against canonical mock routes.

  const ctx = await createTestServer((req, res, body) => {
    const url = req.url || '';
    const method = req.method || '';

    if (method === 'GET' && url.startsWith('/v3/projects?limit=10')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        projects: [
          {
            id: 'proj_1',
            name: 'Alpha Project',
            description: 'Test description',
            created_at: 1000,
            updated_at: 1000,
          },
        ],
        count: 1,
      }));
      return;
    }

    if (method === 'POST' && url === '/v3/projects') {
      res.writeHead(201, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        project: {
          id: 'proj_new',
          name: body.name,
          description: body.description,
          workspaces: body.workspaces,
          created_at: 2000,
          updated_at: 2000,
        },
      }));
      return;
    }

    if (method === 'GET' && url === '/v3/projects/proj_new') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        project: {
          id: 'proj_new',
          name: 'Alpha Project',
          created_at: 2000,
          updated_at: 2000,
        },
      }));
      return;
    }

    if (method === 'PATCH' && url === '/v3/projects/proj_new') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        project: {
          id: 'proj_new',
          name: body.name,
          created_at: 2000,
          updated_at: 3000,
        },
      }));
      return;
    }

    if (method === 'DELETE' && url === '/v3/projects/proj_new') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ status: 'deleted', id: 'proj_new' }));
      return;
    }

    if (method === 'POST' && url === '/v3/projects/synthesize-context') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ project_context: `# Context for ${body.name}` }));
      return;
    }

    if (method === 'POST' && url === '/v3/projects/proj_new/orchestrator:clear-context') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ status: 'cleared', replacement_session_id: 'sess_clean' }));
      return;
    }

    res.writeHead(404);
    res.end();
  });

  try {
    // 1. List
    const projects = await ctx.projects.list({ limit: 10 });
    assert.equal(projects.length, 1);
    assert.equal(projects[0].id, 'proj_1');
    assert.equal(projects[0].name, 'Alpha Project');

    // 2. Create
    const created = await ctx.projects.create({
      name: 'Alpha Project',
      description: 'Test description',
      workspaces: [{ path: '/home/test', role: 'primary_code' }],
    });
    assert.equal(created.id, 'proj_new');
    assert.equal(created.name, 'Alpha Project');
    assert.equal(created.workspaces?.length, 1);

    // 3. Get
    const fetched = await ctx.projects.get('proj_new');
    assert.equal(fetched.id, 'proj_new');

    // 4. Update
    const updated = await ctx.projects.update('proj_new', { name: 'Renamed Alpha' });
    assert.equal(updated.name, 'Renamed Alpha');

    // 5. Synthesize context
    const ctxText = await ctx.projects.synthesizeContext({ name: 'Alpha', workspaces: ['/ws1'] });
    assert.equal(ctxText, '# Context for Alpha');

    // 6. Clear orchestrator context
    const clearRes = await ctx.projects.clearContext('proj_new');
    assert.equal(clearRes.status, 'cleared');

    // 7. Delete
    const delRes = await ctx.projects.delete('proj_new');
    assert.equal(delRes.status, 'deleted');
    assert.equal(delRes.id, 'proj_new');
  } finally {
    await closeTestServer(ctx);
  }
});

// ============================================================================
// 2. Project Tasks CRUD, Previews & Model Previews
// ============================================================================

test('SwarmProjectsNamespace: Project Tasks CRUD and previews', async () => {
  // Written Purpose:
  // - Product Requirement: Typed client.projects must handle tasks collection, creation,
  //   updates, deletion, and both route planning and model preview endpoints.
  // - Threat/regression: Field omission during task creation or model preview deserialization failures.
  // - Boundary: Server.handleProjects in swarmd/internal/api/projects.go.
  // - Narrowest test layer: Mock HTTP transport asserting exact field payloads and responses.

  const ctx = await createTestServer((req, res, body) => {
    const url = req.url || '';
    const method = req.method || '';

    if (method === 'GET' && url === '/v3/projects/proj_1/tasks') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        tasks: [
          {
            id: 'task_1',
            project_id: 'proj_1',
            title: 'Audit CI',
            status: 'queued',
            agent: 'finder',
            created_at: 1000,
            updated_at: 1000,
          },
        ],
        count: 1,
      }));
      return;
    }

    if (method === 'POST' && url === '/v3/projects/proj_1/tasks') {
      res.writeHead(201, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        task: {
          id: 'task_created',
          project_id: 'proj_1',
          title: body.title,
          agent: body.agent,
          feature_size: body.feature_size,
          status: 'pending_approval',
          created_at: 2000,
          updated_at: 2000,
        },
        model_preview: {
          agent: body.agent,
          resolved_model: 'gemini-3.8-flash:low',
          provider: 'google',
        },
      }));
      return;
    }

    if (method === 'GET' && url === '/v3/projects/proj_1/tasks/task_created') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        task: {
          id: 'task_created',
          project_id: 'proj_1',
          title: 'Implement feature',
          status: 'pending_approval',
          created_at: 2000,
          updated_at: 2000,
        },
        model_preview: {
          resolved_model: 'gemini-3.8-flash:low',
        },
      }));
      return;
    }

    if (method === 'PATCH' && url === '/v3/projects/proj_1/tasks/task_created') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        task: {
          id: 'task_created',
          project_id: 'proj_1',
          title: body.title,
          status: 'pending_approval',
          created_at: 2000,
          updated_at: 3000,
        },
      }));
      return;
    }

    if (method === 'DELETE' && url === '/v3/projects/proj_1/tasks/task_created?revision=2') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ status: 'deleted', task_id: 'task_created' }));
      return;
    }

    if (method === 'POST' && url === '/v3/projects/proj_1/tasks:preview') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        task_plan: { agent: body.agent || 'coder', tier: 'direct' },
        model_preview: { resolved_model: 'gpt-6-astra' },
      }));
      return;
    }

    if (method === 'GET' && url === '/v3/projects/proj_1/tasks/task_1/model-preview') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        task: { id: 'task_1', title: 'Audit CI' },
        model_preview: { resolved_model: 'gemini-3.8-flash:low' },
      }));
      return;
    }

    res.writeHead(404);
    res.end();
  });

  try {
    // 1. List tasks
    const tasks = await ctx.projects.listTasks('proj_1');
    assert.equal(tasks.length, 1);
    assert.equal(tasks[0].id, 'task_1');

    // 2. Create task
    const createRes = await ctx.projects.createTask('proj_1', {
      title: 'Implement feature',
      agent: 'coder',
      feature_size: 'small',
      prompt: 'Write clean code',
    });
    assert.equal(createRes.task.id, 'task_created');
    assert.equal(createRes.task.status, 'pending_approval');
    assert.equal(createRes.model_preview.resolved_model, 'gemini-3.8-flash:low');

    // 3. Get task
    const getRes = await ctx.projects.getTask('proj_1', 'task_created');
    assert.equal(getRes.task.id, 'task_created');
    assert.equal(getRes.model_preview.resolved_model, 'gemini-3.8-flash:low');

    // 4. Update task
    const updated = await ctx.projects.updateTask('proj_1', 'task_created', {
      title: 'Implement feature updated',
    });
    assert.equal(updated.title, 'Implement feature updated');

    // 5. Preview task
    const preview = await ctx.projects.previewTask('proj_1', {
      title: 'Analyze performance',
      agent: 'finder',
    });
    assert.equal(preview.task_plan.agent, 'finder');

    // 6. Task model preview
    const modelPrev = await ctx.projects.getTaskModelPreview('proj_1', 'task_1');
    assert.equal(modelPrev.model_preview.resolved_model, 'gemini-3.8-flash:low');

    // 7. Delete task
    const delRes = await ctx.projects.deleteTask('proj_1', 'task_created', 2);
    assert.equal(delRes.status, 'deleted');
    assert.equal(delRes.task_id, 'task_created');
  } finally {
    await closeTestServer(ctx);
  }
});

// ============================================================================
// 3. Task Plan Review: Approval with Stale Guards Forwarding & Rejection
// ============================================================================

test('SwarmProjectsNamespace: approveTask forwards exact revision guards and rejects stale plans', async () => {
  // Written Purpose:
  // - Product Requirement: Task approval must forward exact session_id, plan_id, and
  //   definition_revision guards. The backend rejects stale plans with 400 Bad Request.
  // - Threat/regression: Approving stale or superseded plan revisions leading to execution of wrong tasks.
  // - Boundary: Server.ApproveProjectTask in swarmd/internal/api/project_task_program.go.
  // - Narrowest test layer: Verification that SDK faithfully forwards guards and surfaces stale plan errors.

  let lastApproveGuards: any = null;

  const ctx = await createTestServer((req, res, body) => {
    const url = req.url || '';
    const method = req.method || '';

    if (method === 'POST' && url === '/v3/projects/proj_1/tasks/task_guarded/approve') {
      lastApproveGuards = body;
      // Stale revision rejection test
      if (body.definition_revision === 1) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          error: 'plan definition is stale (guarded revision 1, current 2)',
        }));
        return;
      }
      // Missing guards rejection test
      if (!body.session_id || !body.plan_id || !body.definition_revision) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          error: 'exact session, plan and definition revision guards are required for plan acceptance',
        }));
        return;
      }
      // Valid approval
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        status: 'approved',
        task: {
          id: 'task_guarded',
          project_id: 'proj_1',
          status: 'in_progress',
          revision: body.definition_revision,
          created_at: 1000,
          updated_at: 2000,
        },
      }));
      return;
    }

    if (method === 'POST' && url === '/v3/projects/proj_1/tasks/task_guarded/reject') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        status: 'rejected',
        task: {
          id: 'task_guarded',
          project_id: 'proj_1',
          status: 'rejected',
          action_needed: 'Task rejected by user',
          created_at: 1000,
          updated_at: 2500,
        },
      }));
      return;
    }

    res.writeHead(404);
    res.end();
  });

  try {
    // Case 1: Stale definition revision -> must throw SwarmApiError with exact stale message
    await assert.rejects(
      async () => {
        await ctx.projects.approveTask('proj_1', 'task_guarded', {
          session_id: 'sess_123',
          plan_id: 'plan_abc',
          definition_revision: 1, // Stale revision
        });
      },
      (err: any) => {
        assert.ok(err instanceof SwarmApiError);
        assert.equal(err.status, 400);
        assert.ok(err.message.includes('plan definition is stale'));
        return true;
      }
    );
    assert.equal(lastApproveGuards.definition_revision, 1);
    assert.equal(lastApproveGuards.session_id, 'sess_123');

    // Case 2: Missing guards when structured plan is bound -> must throw SwarmApiError
    await assert.rejects(
      async () => {
        await ctx.projects.approveTask('proj_1', 'task_guarded');
      },
      (err: any) => {
        assert.ok(err instanceof SwarmApiError);
        assert.equal(err.status, 400);
        assert.ok(err.message.includes('exact session, plan and definition revision guards are required'));
        return true;
      }
    );

    // Case 3: Valid guarded approval -> succeeds with status 'approved'
    const approved = await ctx.projects.approveTask('proj_1', 'task_guarded', {
      session_id: 'sess_123',
      plan_id: 'plan_abc',
      definition_revision: 2, // Current revision
    });
    assert.equal(approved.status, 'approved');
    assert.equal(approved.task.status, 'in_progress');
    assert.equal(approved.task.revision, 2);

    // Case 4: acceptTask alias forwards identically
    const accepted = await ctx.projects.acceptTask('proj_1', 'task_guarded', {
      session_id: 'sess_123',
      plan_id: 'plan_abc',
      definition_revision: 2,
    });
    assert.equal(accepted.status, 'approved');

    // Case 5: Reject task -> returns status 'rejected'
    const rejected = await ctx.projects.rejectTask('proj_1', 'task_guarded');
    assert.equal(rejected.status, 'rejected');
    assert.equal(rejected.task.status, 'rejected');
  } finally {
    await closeTestServer(ctx);
  }
});

// ============================================================================
// 4. Selected Task Context: Reopen, Refine, and Complete Operations
// ============================================================================

test('SwarmProjectsNamespace: Reopen, refine, and complete operations carry task context and handle 409 conflict', async () => {
  // Written Purpose:
  // - Product Requirement: Reopening a task carries optional feedback directives; if the task
  //   requires structured plan review, reopen is rejected with 409 Conflict.
  //   Refining a task forwards user feedback, agent overrides, and error summaries.
  // - Threat/regression: Dropping feedback directives during reopen/refine, or silently bypassing plan review.
  // - Boundary: Server.handleProjects in swarmd/internal/api/projects.go.
  // - Narrowest test layer: Verification that feedback is serialized into POST body and 409 Conflict is mapped.

  let lastReopenBody: any = null;
  let lastRefineBody: any = null;

  const ctx = await createTestServer((req, res, body) => {
    const url = req.url || '';
    const method = req.method || '';

    // Reopen bound task (conflict)
    if (method === 'POST' && url === '/v3/projects/proj_1/tasks/task_bound/reopen') {
      res.writeHead(409, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        error: 'task requires structured plan review or revision; reopen cannot bypass approval',
      }));
      return;
    }

    // Reopen completed task (success with feedback)
    if (method === 'POST' && url === '/v3/projects/proj_1/tasks/task_completed/reopen') {
      lastReopenBody = body;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        status: 'reopened',
        task: {
          id: 'task_completed',
          project_id: 'proj_1',
          status: 'in_progress',
          what_did_do: ['Task reopened for further execution'],
        },
      }));
      return;
    }

    // Complete task
    if (method === 'POST' && url === '/v3/projects/proj_1/tasks/task_completed/complete') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        status: 'completed',
        task: {
          id: 'task_completed',
          project_id: 'proj_1',
          status: 'completed',
        },
      }));
      return;
    }

    // Refine task with feedback directives & error recovery
    if (method === 'POST' && url === '/v3/projects/proj_1/tasks/task_review/refine') {
      lastRefineBody = body;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        status: 'refined',
        task: {
          id: 'task_review',
          project_id: 'proj_1',
          status: 'pending_approval',
          revision: 2,
          feedback_history: [body.feedback],
          last_error: body.error_summary || '',
          agent: body.agent || 'coder',
          feature_size: body.feature_size || 'small',
        },
      }));
      return;
    }

    res.writeHead(404);
    res.end();
  });

  try {
    // 1. Reopen with structured plan -> throws SwarmConflictError (409)
    await assert.rejects(
      async () => {
        await ctx.projects.reopenTask('proj_1', 'task_bound', {
          feedback: 'Please retry',
        });
      },
      (err: any) => {
        assert.ok(err instanceof SwarmConflictError);
        assert.equal(err.status, 409);
        assert.ok(err.message.includes('task requires structured plan review or revision'));
        return true;
      }
    );

    // 2. Reopen completed task with selected task feedback
    const reopenRes = await ctx.projects.reopenTask('proj_1', 'task_completed', {
      feedback: 'The unit tests failed on node 20; please address.',
    });
    assert.equal(reopenRes.status, 'reopened');
    assert.equal(reopenRes.task.status, 'in_progress');
    assert.equal(lastReopenBody.feedback, 'The unit tests failed on node 20; please address.');

    // 3. Refine task plan with feedback directives and agent override
    const refineRes = await ctx.projects.refineTask('proj_1', 'task_review', {
      feedback: 'Please split into two phases: audit then patch.',
      agent: 'coder',
      feature_size: 'small',
      error_summary: 'Prior execution had test timeouts',
    });
    assert.equal(refineRes.status, 'refined');
    assert.equal(refineRes.task.revision, 2);
    assert.equal(lastRefineBody.feedback, 'Please split into two phases: audit then patch.');
    assert.equal(lastRefineBody.agent, 'coder');
    assert.equal(lastRefineBody.error_summary, 'Prior execution had test timeouts');

    // 4. Complete task
    const completeRes = await ctx.projects.completeTask('proj_1', 'task_completed');
    assert.equal(completeRes.status, 'completed');
    assert.equal(completeRes.task.status, 'completed');
  } finally {
    await closeTestServer(ctx);
  }
});

// ============================================================================
// 5. Integration, Task Program, and Media Sub-resource
// ============================================================================

test('SwarmProjectsNamespace: Task integration, program deploy/redeploy, and media management', async () => {
  // Written Purpose:
  // - Product Requirement: Typed client.projects must support git integration, task program
  //   deployment/redeploy, and project media attachment/removal.
  // - Threat/regression: Dropped job_id during program redeploy or incorrect media sub-resource paths.
  // - Boundary: Server.handleProjects in swarmd/internal/api/projects.go.
  // - Narrowest test layer: HTTP mock asserting exact sub-resource routes and payloads.

  let lastRedeployBody: any = null;

  const ctx = await createTestServer((req, res, body) => {
    const url = req.url || '';
    const method = req.method || '';

    // Task integrate
    if (method === 'POST' && url === '/v3/projects/proj_1/tasks/task_1/integrate') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        status: 'integrated',
        message: 'Integrated 3 commits into main',
        resulting_target_head: 'head_sha_123',
      }));
      return;
    }

    // Task program deploy
    if (method === 'POST' && url === '/v3/projects/proj_1/tasks/task_1/program:deploy') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        status: 'deployed',
        task: { id: 'task_1', status: 'in_progress', task_program_id: 'prog_123' },
      }));
      return;
    }

    // Task program redeploy-job
    if (method === 'POST' && url === '/v3/projects/proj_1/tasks/task_1/program:redeploy-job') {
      lastRedeployBody = body;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        status: 'redeploying',
        task: { id: 'task_1', status: 'in_progress' },
      }));
      return;
    }

    // Task program inspection
    if (method === 'GET' && url === '/v3/projects/proj_1/tasks/task_1/program') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        program: {
          id: 'prog_123',
          jobs: [{ id: 'job_audit', status: 'completed' }],
        },
      }));
      return;
    }

    // Project media: list
    if (method === 'GET' && url === '/v3/projects/proj_1/media') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        media: [{ id: 'med_1', title: 'Architecture Diagram', kind: 'image' }],
        count: 1,
      }));
      return;
    }

    // Project media: upload
    if (method === 'POST' && url === '/v3/projects/proj_1/media') {
      res.writeHead(201, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        media: { id: 'med_2', title: body.title, kind: body.kind },
        uploaded_media: [
          { id: 'med_1', title: 'Architecture Diagram', kind: 'image' },
          { id: 'med_2', title: body.title, kind: body.kind },
        ],
      }));
      return;
    }

    // Project media: delete
    if (method === 'DELETE' && url === '/v3/projects/proj_1/media/med_1') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        removed: true,
        media_id: 'med_1',
        uploaded_media: [],
      }));
      return;
    }

    res.writeHead(404);
    res.end();
  });

  try {
    // 1. Task integrate
    const intRes = await ctx.projects.integrateTask('proj_1', 'task_1');
    assert.equal(intRes.status, 'integrated');
    assert.equal(intRes.resulting_target_head, 'head_sha_123');

    // 2. Task program deploy
    const depRes = await ctx.projects.deployTaskProgram('proj_1', 'task_1');
    assert.equal(depRes.status, 'deployed');

    // 3. Task program redeploy-job
    const redepRes = await ctx.projects.redeployTaskProgramJob('proj_1', 'task_1', {
      job_id: 'job_failed',
      feedback: 'Retry with increased memory',
    });
    assert.equal(redepRes.status, 'redeploying');
    assert.equal(lastRedeployBody.job_id, 'job_failed');
    assert.equal(lastRedeployBody.feedback, 'Retry with increased memory');

    // 4. Task program get
    const progRes = await ctx.projects.getTaskProgram('proj_1', 'task_1');
    assert.equal(progRes.program.id, 'prog_123');

    // 5. Media list
    const media = await ctx.projects.listMedia('proj_1');
    assert.equal(media.length, 1);
    assert.equal(media[0].id, 'med_1');

    // 6. Media upload
    const uploaded = await ctx.projects.uploadMedia('proj_1', {
      title: 'Spec PDF',
      kind: 'doc',
    });
    assert.equal(uploaded.id, 'med_2');

    // 7. Media delete
    const delMedia = await ctx.projects.deleteMedia('proj_1', 'med_1');
    assert.equal(delMedia.removed, true);
    assert.equal(delMedia.media_id, 'med_1');
  } finally {
    await closeTestServer(ctx);
  }
});

// ============================================================================
// 6. Malformed Responses and HTTP Error Mapping
// ============================================================================

test('SwarmProjectsNamespace: Defensively handles malformed responses and standard HTTP errors', async () => {
  // Written Purpose:
  // - Product Requirement: Client must map 401 (Auth), 403 (Forbidden), 404 (NotFound),
  //   409 (Conflict), and 500 (ApiError) to typed exceptions, and fail clearly on malformed bodies.
  // - Threat/regression: Unhandled null pointer exceptions or silent fallbacks on server errors.
  // - Boundary: SwarmTransport.handleErrorResponse and SwarmProjectsNamespace response validation.
  // - Narrowest test layer: Hermetic mock server returning empty or invalid JSON payloads and HTTP statuses.

  const ctx = await createTestServer((req, res) => {
    const url = req.url || '';

    if (url === '/v3/projects/401') {
      res.writeHead(401, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'unauthorized access' }));
      return;
    }
    if (url === '/v3/projects/403') {
      res.writeHead(403, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'cross-account project access forbidden' }));
      return;
    }
    if (url === '/v3/projects/404') {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'project not found' }));
      return;
    }
    if (url === '/v3/projects/500') {
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'database lock timeout' }));
      return;
    }
    if (url === '/v3/projects/malformed') {
      // Returns 200 with empty body or empty object when project was expected
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({}));
      return;
    }

    res.writeHead(404);
    res.end();
  });

  try {
    // 401
    await assert.rejects(
      async () => ctx.projects.get('401'),
      (err: any) => {
        assert.equal(err.status, 401);
        assert.ok(err.message.includes('unauthorized'));
        return true;
      }
    );

    // 403
    await assert.rejects(
      async () => ctx.projects.get('403'),
      (err: any) => {
        assert.ok(err instanceof SwarmForbiddenError);
        assert.equal(err.status, 403);
        assert.ok(err.message.includes('cross-account'));
        return true;
      }
    );

    // 404
    await assert.rejects(
      async () => ctx.projects.get('404'),
      (err: any) => {
        assert.ok(err instanceof SwarmNotFoundError);
        assert.equal(err.status, 404);
        assert.ok(err.message.includes('not found'));
        return true;
      }
    );

    // 500
    await assert.rejects(
      async () => ctx.projects.get('500'),
      (err: any) => {
        assert.ok(err instanceof SwarmApiError);
        assert.equal(err.status, 500);
        assert.ok(err.message.includes('database lock'));
        return true;
      }
    );

    // Malformed response (200 OK but missing expected project object)
    await assert.rejects(
      async () => ctx.projects.get('malformed'),
      (err: any) => {
        assert.ok(err instanceof SwarmApiError);
        assert.ok(err.message.includes('Malformed response'));
        return true;
      }
    );
  } finally {
    await closeTestServer(ctx);
  }
});

// ============================================================================
// 7. Cancellation, Timeouts, and Opaque Event Replay
// ============================================================================

test('SwarmProjectsNamespace: AbortSignal cancellation, waitForTask polling, and opaque event stream/replay', async () => {
  // Written Purpose:
  // - Product Requirement: Operations must honor AbortSignal cancellation and deadlines;
  //   waitForTask must poll until satisfied or aborted; streamEvents must pass opaque cursors.
  // - Threat/regression: Hung requests on agent cancellation or cursor arithmetic corrupting replay.
  // - Boundary: SwarmTransport AbortController wiring and syncStream endpoint.
  // - Narrowest test layer: Fast mock server with AbortSignal cancellation assertions.

  let pollCount = 0;

  const ctx = await createTestServer((req, res, body) => {
    const url = req.url || '';
    const method = req.method || '';

    // Delayed endpoint for direct AbortSignal testing
    if (url === '/v3/projects/hang') {
      setTimeout(() => {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ project: { id: 'hang', name: 'Slow' } }));
      }, 500);
      return;
    }

    // Polling task endpoint: transitions from in_progress to completed after 3 calls
    if (url === '/v3/projects/proj_1/tasks/task_poll') {
      pollCount++;
      const status = pollCount >= 3 ? 'completed' : 'in_progress';
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        task: {
          id: 'task_poll',
          project_id: 'proj_1',
          status,
          created_at: 1000,
          updated_at: 2000,
        },
      }));
      return;
    }

    // Sync stream endpoint: verifies endpoint_cursor is passed opaquely
    if (method === 'POST' && url === '/v3/sync/stream') {
      const cursor = body.endpoint_cursor;
      assert.ok(typeof cursor === 'string', 'cursor must remain an opaque string');

      if (cursor === 'opaque_cursor_0') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          ok: true,
          endpoint_cursor: 'opaque_cursor_1',
          events: [{ session_id: 'sess_1', event_type: 'task.updated' }],
          has_more: true,
        }));
        return;
      }

      if (cursor === 'opaque_cursor_1') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({
          ok: true,
          endpoint_cursor: 'opaque_cursor_2',
          events: [{ session_id: 'sess_1', event_type: 'project.updated' }],
          has_more: false,
        }));
        return;
      }
    }

    res.writeHead(404);
    res.end();
  });

  try {
    // 1. Direct AbortSignal cancellation
    const controller = new AbortController();
    setTimeout(() => controller.abort(), 20);

    await assert.rejects(
      async () => {
        await ctx.projects.get('hang', { signal: controller.signal });
      },
      (err: any) => {
        assert.ok(err instanceof SwarmApiError);
        assert.ok(err.message.includes('aborted by caller'));
        return true;
      }
    );

    // 2. waitForTask polling condition satisfaction
    pollCount = 0;
    const completedTask = await ctx.projects.waitForTask(
      'proj_1',
      'task_poll',
      (t) => t.status === 'completed',
      { pollIntervalMs: 20, timeoutMs: 2000 }
    );
    assert.equal(completedTask.status, 'completed');
    assert.ok(pollCount >= 3, `Expected at least 3 poll calls, got ${pollCount}`);

    // 3. waitForTask abort cancellation
    const abortPollCtrl = new AbortController();
    abortPollCtrl.abort(); // already aborted

    await assert.rejects(
      async () => {
        await ctx.projects.waitForTask(
          'proj_1',
          'task_poll',
          () => false,
          { signal: abortPollCtrl.signal, pollIntervalMs: 50, timeoutMs: 2000 }
        );
      },
      (err: any) => {
        assert.ok(err instanceof SwarmApiError);
        assert.ok(err.message.includes('aborted by caller'));
        return true;
      }
    );

    // 4. waitForTask timeout
    await assert.rejects(
      async () => {
        await ctx.projects.waitForTask(
          'proj_1',
          'task_poll',
          () => false, // never satisfied
          { pollIntervalMs: 20, timeoutMs: 100 }
        );
      },
      (err: any) => {
        assert.ok(err instanceof SwarmTimeoutError);
        assert.ok(err.message.includes('did not satisfy condition within 100ms'));
        return true;
      }
    );

    // 5. syncStream and replayEvents preserve opaque cursors
    const replayRes = await ctx.projects.replayEvents('opaque_cursor_0', { limit: 10 });
    assert.equal(replayRes.ok, true);
    assert.equal(replayRes.endpoint_cursor, 'opaque_cursor_1');
    assert.equal(replayRes.events.length, 1);
    assert.equal(replayRes.events[0].event_type, 'task.updated');

    // 6. streamEvents async generator consumes multiple pages and yields events
    const streamAbort = new AbortController();
    const collectedEvents: any[] = [];
    for await (const event of ctx.projects.streamEvents('opaque_cursor_0', {}, { signal: streamAbort.signal })) {
      collectedEvents.push(event);
      if (collectedEvents.length >= 2) {
        break;
      }
    }
    assert.equal(collectedEvents.length, 2);
    assert.equal(collectedEvents[0].event_type, 'task.updated');
    assert.equal(collectedEvents[1].event_type, 'project.updated');
  } finally {
    await closeTestServer(ctx);
  }
});

test('SwarmProjectsNamespace: ensureProject and getOrchestratorSession helpers', async () => {
  let createdProjectBody: any = null;
  let createdSessionBody: any = null;
  const ctx = await createTestServer((req, res, body) => {
    if (req.method === 'GET' && req.url === '/v3/projects?limit=50') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          projects: [
            {
              id: 'proj_existing_1',
              name: 'existing-project',
              created_at: 1000,
              updated_at: 1000,
            },
          ],
        })
      );
    } else if (req.method === 'POST' && req.url === '/v3/projects') {
      createdProjectBody = body;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          project: {
            id: 'proj_new_swarmtest',
            name: body.name,
            description: body.description,
            workspaces: body.workspaces,
            created_at: 2000,
            updated_at: 2000,
          },
        })
      );
    } else if (req.method === 'GET' && req.url === '/v3/projects/proj_existing_1/sessions') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          sessions: [
            {
              id: 'sess_orch_existing',
              title: 'Orchestrator AI',
              agent_name: 'system-orchestrator',
              created_at: 1000,
              updated_at: 1000,
            },
          ],
        })
      );
    } else if (req.method === 'GET' && req.url === '/v3/projects/proj_new_swarmtest/sessions') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ sessions: [] }));
    } else if (req.method === 'POST' && req.url === '/v3/projects/proj_new_swarmtest/sessions') {
      createdSessionBody = body;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          session: {
            id: 'sess_orch_new',
            title: body.title,
            agent_name: body.agent_name,
            created_at: 2000,
            updated_at: 2000,
          },
        })
      );
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  try {
    // 1. ensureProject returns existing if found
    const existing = await ctx.projects.ensureProject('existing-project');
    assert.equal(existing.id, 'proj_existing_1');
    assert.equal(createdProjectBody, null);

    // 2. ensureProject creates new project if not found
    const created = await ctx.projects.ensureProject('swarmtest', { workspacePath: '/project/swarmtest' });
    assert.equal(created.id, 'proj_new_swarmtest');
    assert.equal(createdProjectBody?.name, 'swarmtest');
    assert.deepEqual(createdProjectBody?.workspaces, [{ path: '/project/swarmtest' }]);

    // 3. getOrchestratorSession returns existing session
    const existingSess = await ctx.projects.getOrchestratorSession('proj_existing_1');
    assert.equal(existingSess.id, 'sess_orch_existing');

    // 4. getOrchestratorSession provisions new session if none exists
    const newSess = await ctx.projects.getOrchestratorSession('proj_new_swarmtest');
    assert.equal(newSess.id, 'sess_orch_new');
    assert.equal(createdSessionBody?.agent_name, 'system-orchestrator');
  } finally {
    await closeTestServer(ctx);
  }
});
