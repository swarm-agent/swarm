# Remote Workers, Distributed Swarms & Agent Mailbox Architecture Roadmap

## Executive Vision

Swarm is a local-first AI coding and agent workspace. As developers scale their workflows from local interactive sessions to recurring background tasks, Swarm expands from a single-machine assistant to a **local Master Node (orchestrator)** capable of dispatching, supervising, and coordinating autonomous worker swarms across heterogeneous remote environments—including **Bare VPS hosts (Hetzner, DigitalOcean), GCP, AWS, Cloudflare, and custom cloud infrastructure**.

The goal is to provide a complete, autonomous loop operated from the comfort of the local Swarm harness:
> *"Monitor social media analytics on a remote Hetzner VPS, generate video clips with Veo/Lyria, stage drafts in `swarm-social`, send them to my local Agent Mailbox for one-click approval, and automatically publish approved posts to Twitter/X."*

This roadmap defines the architectural invariants, failure-recovery mechanics, deliverable routing contracts, self-healing agent loops, connection providers, and implementation phases that turn Worker V2 into a fully distributed agent orchestration engine.

---

## 1. Core Architecture: Inverted Authority & Durability

The central challenge of distributed agent execution is: **"Where does the work pend? What if remote nodes go offline? How do we guarantee zero data loss?"**

Swarm solves this through **Inverted Authority**:

```
+----------------------------------------------------------------------------------------+
|                                   LOCAL MASTER NODE                                    |
|                                                                                        |
|   +-------------------------+   +-------------------------+   +--------------------+   |
|   |   Worker V2 Scheduler   |   |  Pebble Occurrence Log  |   |   Agent Mailbox    |   |
|   |   (Sweep, Triggers)     |   |  (Single State Root)    |   | (Triage / Approve) |   |
|   +------------+------------+   +------------^------------+   +---------^----------+   |
|                |                             |                          |              |
|                | Dispatches                  | Heartbeats &             | Delivers     |
|                |                             | Checkpoints              | Artifacts    |
|                v                             |                          |              |
|   +-------------------------+                |                          |              |
|   | Deployment & Lease Mgr  |----------------+--------------------------+              |
|   | (TTL, Heartbeats, SSH)  |                                                          |
|   +------------+------------+                                                          |
+----------------+-----------------------------------------------------------------------+
                 |
                 | Authenticated Tunnel / SSH / mTLS / Docker Exec
                 v
 +---------------------------------------------------------------------------------------+
 |                                REMOTE EXECUTION RUNNERS                               |
 |                                                                                       |
 |   +-----------------------+   +------------------------+   +----------------------+   |
 |   | Bare Linux VPS        |   | GCP Compute / CloudRun |   | AWS EC2 / ECS        |   |
 |   | (Hetzner, Linode)     |   | (Preemptible / GPU)    |   | (Spot / Graviton)    |   |
 |   +-----------------------+   +------------------------+   +----------------------+   |
 +---------------------------------------------------------------------------------------+
```

### Invariants of Distributed Execution
1. **The Master Node is the Single Source of Truth**:
   - The local daemon owns the Pebble DB, the worker definitions, cron/event triggers, active occurrences, and the deliverable mailbox.
   - Remote nodes are **stateless execution runners**. They hold no durable queues.
   - Work **always pends locally** on the Master node before dispatch and between retry attempts.

2. **Leases & Heartbeats Govern Execution**:
   - When the Master dispatches a job to a remote runner, it acquires a `DeploymentLease` with a strict `TTLMillis` (e.g. 60 seconds) renewed by periodic runner heartbeats.
   - If a remote node crashes, loses network, or is preempted by cloud autoscaling:
     - The lease expires on the Master.
     - The Master detects the dropped heartbeat and evaluates the task's retry policy:
       - **Idempotent Jobs** (e.g. scraping, drafting, compiling): Occurrence reverts to `pending` and is scheduled onto an alternate runner.
       - **Non-Idempotent Jobs** (e.g. external mutations): Occurrence transitions to `attention_alert` or `blocked` with diagnostic snapshot, alerting the user via the Mailbox.

3. **Sealed Deliverables Before Completion**:
   - A remote worker cannot mark its checkpoint `completed` until its deliverables are either:
     - Committed and pushed to an authenticated Git branch (`agent/worker-...`),
     - Uploaded to configured object storage (S3/GCS/R2) with cryptographic hash verification, or
     - Streamed and staged directly into the Master's local artifact store.
   - Once the deliverable digest is recorded in the Master's Pebble store, the work is durable—even if the remote VM powers off immediately after.

---

## 2. Deliverable Accounting & Destination Routing

Workers must explicitly declare **what** they produce and **where** it gets delivered.

### The Deliverable Contract in Worker V2
In the Worker V2 plan document, checkpoints specify deliverable expectations, destinations, and reactive triggers:

```json
{
  "worker_v2": {
    "schema_version": 2,
    "schedule": { "kind": "cron", "cron": "0 9 * * 1-5", "timezone": "America/New_York" },
    "target": {
      "mode": "remote",
      "connection_id": "conn-hetzner-vps",
      "environment_id": "env-social-maker"
    },
    "delivery": {
      "destination": "mailbox",
      "channel": "social_media_queue",
      "require_approval": true,
      "auto_publish_on_accept": {
        "action": "publish_x_post",
        "workspace_path": "swarm-social"
      }
    },
    "on_alert": {
      "action": "spawn_ephemeral_fix_session",
      "agent": "coder",
      "prompt": "Test failed with alert {occurrence_id}. Investigate in worktree, fix bug, verify green, and propose fix PR.",
      "max_attempts": 2
    }
  }
}
```

### Destination Types
1. **`mailbox` (Default & Recommended)**:
   - Lands in the Swarm **Agent Mailbox**.
   - Held in a pending triage queue until approved, edited, or dismissed by the user.
2. **`workspace_git`**:
   - Commits changes to a target branch in a local or remote repository checkout (e.g. `swarm-social/drafts/weekly-roundup.md`).
3. **`external_webhook`**:
   - Posts a structured JSON payload to an authenticated endpoint (Slack, Discord, internal ERP).
4. **`object_storage`**:
   - Uploads compiled media (MP4, PNG, audio) directly to an S3/GCS/R2 bucket with signed URLs generated for the Mailbox.

---

## 3. The Workers Page as a Deliverables & Action Control Center

The `/workers` interface is currently focused on schedules and execution history. To fulfill the user vision, it evolves into an active **Deliverables & Action Control Center**:

```
+----------------------------------------------------------------------------------------+
| WORKERS & DELIVERABLES HUB                                    [Workspace: swarm-social]|
+----------------------------------------------------------------------------------------+
| ACTIVE WORKERS (4)     PENDING DELIVERABLES (3)     ALERTS REQUIRING ACTION (1)        |
+----------------------------------------------------------------------------------------+
| DELIVERABLE INBOX (Ready for Review & Action)                                          |
|                                                                                        |
| [PENDING REVIEW] Launch Announcement Thread (3 posts + 1 video)                        |
| Worker: Daily Social Generator (Run #42)                       Delivered: 12m ago      |
| Destination: X/Twitter (Scheduled for 11:00 AM EDT)                                    |
| Preview: "Local-first AI agents are here. Introducing Swarm V3..."                     |
| Media: [8s Veo Animation: Swarm Logo Murmuration (1080p)]                              |
|                                                                                        |
| [Approve & Publish to X]    [Revamp / Sidechat]    [Edit Inline]    [Dismiss]          |
|                                                                                        |
| -------------------------------------------------------------------------------------- |
| [ALERT ACTION NEEDED] Test Matrix Failure: CAS Race Condition                          |
| Worker: CI/CD Candidate Testbench                              Detected: 35m ago       |
| Cause: HTTP 409 conflict detected during parallel worker review proposal.              |
| Automated Loop: Spawned Ephemeral Coder Session (attempt 1 of 2)...                    |
| Status: Coder isolated worktree created, re-running testbench with fix.                |
|                                                                                        |
| [View Live Debug Session]   [Abort Loop]           [Manual Override]                   |
+----------------------------------------------------------------------------------------+
```

### Core Features of the Transformed Page:
1. **Deliverable-First Overview**:
   - Workers that produce deliverables (social posts, videos, reports) present their outputs directly at the top of the page.
   - Users don't need to read execution transcripts—they evaluate the finished deliverable.
2. **One-Click Action Triggers**:
   - `[Approve & Publish]`: Executes the configured publication action (Git commit, API post, PR merge).
   - `[Revamp / Sidechat]`: Opens an isolated child conversation attached to that deliverable so the user can instruct adjustments without breaking the worker's parent context.
3. **Action-Oriented Alert Cards**:
   - Alerts (`closing_state: attention_alert`) display the exact failure and the active or proposed remediation loop.

---

## 4. Autonomous Self-Healing Loops: Bug -> Ephemeral Session -> Rebuild -> Deliver

One of the most powerful paradigms enabled by Swarm's Git worktree isolation is **Autonomous Self-Healing Loops**:

```
+----------------------------------------------------------------------------------------+
|                                AUTONOMOUS FIX LOOP                                     |
|                                                                                        |
|   1. Scheduled Worker runs test suite on remote/local environment.                     |
|      └──> Test fails: Occurrence finishes with `closing_state: attention_alert`.       |
|                                                                                        |
|   2. Worker checks `on_alert` policy:                                                  |
|      └──> Action: `spawn_ephemeral_fix_session`.                                       |
|                                                                                        |
|   3. Daemon spawns isolated Coder Session in a fresh Git worktree:                     |
|      ├── Analyzes error stack trace and test output.                                   |
|      ├── Edits source code in its isolated worktree.                                   |
|      └── Executes testbench inside the environment (`manage_deployments exec`).        |
|                                                                                        |
|   4. Evaluation:                                                                       |
|      ├── PASS: Commits to branch `agent/auto-fix-...`, creates a Deliverable Card      |
|      │         in the Mailbox: "Bug auto-repaired. [Review & Promote to Dev]".         |
|      └── FAIL: Terminates worktree, retries up to `max_attempts`, then escalates       |
|                to user with detailed failure diagnostics.                              |
+----------------------------------------------------------------------------------------+
```

### Why This Works Cleanly in Swarm:
- **No Workspace Pollution**: The debug session operates in its own session-owned worktree. If the AI's fix is broken, the main codebase remains 100% clean and untouched.
- **Leased Environment Reuse**: The fix session leases the exact same test environment deployment (`manage_deployments action="ensure"`), runs tests inside the container/VPS, and releases the lease when done.

---

## 5. Economic & Deployment Blueprint: The $10-$50 VPS Architecture

A major strength of Swarm compared to hosted SaaS platforms is **extreme resource efficiency**:

```
+----------------------------------------------------------------------------------------+
|                       SINGLE $10 - $50 VPS DEPLOYMENT FOOTPRINT                        |
|                                                                                        |
|   Hetzner CPX31 (4 vCPU, 8GB RAM, ~$15/mo) or DigitalOcean ($24/mo)                    |
|                                                                                        |
|   +--------------------------------------------------------------------------------+   |
|   | Host Linux OS (Ubuntu 24.04 LTS / Debian 12)                                   |   |
|   |                                                                                |   |
|   |  +---------------------------+   +------------------------------------------+  |   |
|   |  | Swarm Go Daemon (swarmd)  |   | Embedded Pebble DB (<50MB RAM)           |  |   |
|   |  | Idle RAM: ~35MB           |   | Zero external DBs (no Postgres/Redis)    |  |   |
|   |  +---------------------------+   +------------------------------------------+  |   |
|   |                                                                                |   |
|   |  +---------------------------+   +------------------------------------------+  |   |
|   |  | Git Repository Worktrees  |   | Headless Browser / Media Renderer        |  |   |
|   |  | Fast NVMe local storage   |   | Puppeteer/Chromium on-demand             |  |   |
|   |  +---------------------------+   +------------------------------------------+  |   |
|   +--------------------------------------------------------------------------------+   |
|                                          |                                             |
|                                          | Outbound API Calls (Pay-per-Token/Per-Gen)  |
|                                          v                                             |
|   +--------------------------------------------------------------------------------+   |
|   | External AI Providers:                                                         |   |
|   | - Reasoning: Anthropic Claude 3.5 / OpenAI GPT-5 / Google Gemini 2.0 Flash    |   |
|   | - Video Generation: Google Veo 3.1 (Image-to-Video, keyframe chaining)         |   |
|   | - Audio Generation: Google Lyria (Soundtracks, Foley)                          |   |
|   | - Search & Retrieval: Exa API                                                  |   |
|   +--------------------------------------------------------------------------------+   |
+----------------------------------------------------------------------------------------+
```

### Key Economic Takeaways:
- **No Expensive Idle GPUs**: High-end media generation (Veo 3.1 video, Lyria music) is offloaded to provider APIs. The VPS never pays for an idle A100/H100.
- **Full Social Media / Ops Automation on One Box**: A single $15-$50 VPS can run:
  - Hourly social analytics monitors.
  - Daily draft generation for X, LinkedIn, and blog posts.
  - Video story pipelines chaining Veo clips and audio into MP4 deliverables.
  - Nightly test matrix execution and CI/CD validation runs.
- **Air-Tight Control**: The user accesses the VPS via an authenticated SSH tunnel to loopback Desktop (`127.0.0.1:15655`), maintaining strict privacy with zero public open ports.

---

## 6. Remote Connection Providers & AI-Assisted IAM

### Provider Support Matrix
| Provider | Target Mechanism | Authentication Model | Key Capabilities |
|---|---|---|---|
| **Bare VPS (Hetzner / Linode / DO)** | SSH (`bare_ssh`) or SSH+Docker | SSH Agent / Identity File reference | Low cost, dedicated CPU/GPU, persistent disk |
| **GCP (Google Cloud)** | Compute Engine / Cloud Run Jobs | Service Account Key file (path only) + Project ID | High GPU availability, spot instances, VPC peering |
| **AWS (Amazon Web Services)** | EC2 Spot / ECS Fargate | AWS Profile / Credential path + Role ARN | Global scale, Graviton/ARM, least-privilege IAM |
| **Cloudflare** | Cloudflare Workers / Sandbox Containers | API Token (path/env reference) + Account ID | Ultra-low latency edge triggers, serverless execution |

### Zero-Secret Invariant & AI IAM Wizard
Swarm strictly forbids storing plaintext private keys, cloud tokens, or API secrets in its database. Instead:
1. **AI IAM Wizard**:
   - The AI generates exact, minimal, copyable provisioning commands with spending caps ($50/month hard stop) and IP/subnet restrictions:
     ```bash
     # Example AI-generated GCP setup
     gcloud iam service-accounts create swarm-worker-runner \
       --description="Minimal runner for Swarm background workers"
     gcloud projects add-iam-policy-binding my-project \
       --member="serviceAccount:swarm-worker-runner@my-project.iam.gserviceaccount.com" \
       --role="roles/compute.instanceAdmin.v1"
     ```
2. **Verification Probe**:
   - The user provides the local credential path (e.g. `~/.config/gcp/swarm-key.json`).
   - Swarm runs `manage_connections action="check"` to verify read/write/lease permissions without persisting the secret contents.
   - The connection is marked `healthy` and registered in `/connections`.

---

## 7. Implementation Roadmap (Phases)

```
Phase 1: Deliverables Hub & Agent Mailbox UI (Pebble Schema & Desktop View)
    |
    v
Phase 2: Reactive Worker Chains (Self-Healing Debug Loops & Actions)
    |
    v
Phase 3: Bare VPS / SSH Execution Provider (Beyond Docker)
    |
    v
Phase 4: Worker V2 Remote Target Binding & Lease Timeout Recovery
    |
    v
Phase 5: Cloud Connectors (GCP, AWS, Hetzner, Cloudflare) & AI IAM Wizard
    |
    v
Phase 6: Multi-Node Swarm Distribution & Concurrency Budgeting
```

### Phase 1: Deliverables Hub & Agent Mailbox UI
- **Deliverable Schema**: Add structured `DeliverablePayload` to V3 session checkpoints (kind, content, media references, actions).
- **Mailbox Store**: Create Pebble index for unreviewed deliverables and alerts across all account workspaces.
- **Desktop UI**: Transform `/workers` to include a prominent Deliverables Feed with filterable tabs (All, Deliverables, Alerts), rich artifact previews, and one-click [Approve] / [Dismiss] buttons.
- **Sidechat Revamping**: Connect the [Revamp] button to the isolated child sidechat endpoint (`/v3/sessions/{id}/sidechats/plan`).

### Phase 2: Reactive Worker Chains & Self-Healing Loops
- **On-Alert Policy Contract**: Extend Worker V2 document schema with `on_alert` triggers.
- **Ephemeral Session Dispatcher**: Implement daemon logic to spawn an isolated Coder session in a fresh worktree upon `attention_alert`.
- **Remediation Reporting**: Capture fix attempts, test validation results, and propose automated PR deliverables to the Mailbox.

### Phase 3: Bare VPS / SSH Execution Provider
- **Connection Kind `bare_ssh`**: Add non-Docker SSH runner provider to `pkg/environments/connection.go` and `internal/environments/provider/`.
- **Command Execution & Remote Staging**: Implement remote working-directory allocation, git clone/fetch, and command execution via SSH batch mode.
- **Probe & Capability Detection**: Detect OS, CPU/Arch, installed runtimes (Node.js, Go, Python, Git) during connection check.

### Phase 4: Worker V2 Remote Target Binding & Resilience
- **Target Specification**: Allow `worker_v2` plan documents to declare `target: { connection_id, environment_id }`.
- **Automated Lease Lifecycle**: `AutomationExecutionHost` acquires a lease via `manage_deployments action="ensure"`, starts the remote session, and monitors heartbeats.
- **Offline / Preemption Recovery**: Automatic retry on timeout for idempotent occurrences; clean transition to `attention_alert` when human intervention is needed.

### Phase 5: Cloud Connectors & AI IAM Wizard
- **GCP Provider**: Support launching and terminating on-demand Compute Engine VMs or Cloud Run jobs.
- **AWS Provider**: Support launching EC2 Spot instances or ECS Fargate tasks with automatic termination on lease release.
- **Cloudflare Integration**: Support edge trigger webhooks and sandbox execution.
- **Interactive IAM Assistant**: Built-in tool for the AI to guide users through 2-minute cloud account setup with budget safety gates.

### Phase 6: Swarm Mode Remote Distribution
- **Distributed Swarms**: Extend `task mode=swarm` to distribute worker waves across multiple remote deployment instances concurrently.
- **Fleet Monitoring**: Real-time dashboard showing active runners, CPU/memory usage, active leases, and hourly cloud spend.

---

## Summary
With this architecture, Swarm transforms from a local code-generation harness into a **decentralized, autonomous agent operating system**. A solo developer or small team can manage an entire software, content, and infrastructure pipeline—running 24/7 on an inexpensive VPS or cloud spot fleet—while retaining 100% local authority, privacy, and approval control.
