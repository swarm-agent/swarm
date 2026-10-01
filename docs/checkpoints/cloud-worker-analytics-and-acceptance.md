# Cloud Worker Analytics, Durable State & Leased Acceptance Architecture

## 1. Overview & Core Philosophy: Inverted Authority

Autonomous cloud workers frequently fail adoption in production due to two critical operator anxieties:
1. **Over-Permissioning**: Giving an agent long-lived administrative cloud credentials (e.g. `roles/compute.admin`, `roles/iam.admin`, or wildcard write permissions) where a hallucination or compromised prompt could spawn arbitrary resources or mutate production infrastructure.
2. **Runaway Overspend**: Giving an agent unrestricted API access where a retry loop, unbounded token generation, or unmonitored background process burns through hundreds or thousands of dollars before anyone notices.

Swarm solves both anxieties through **Inverted Authority**:
- **Stateless, Ephemeral Cloud Execution**: The cloud worker is a lightweight, short-lived container (Cloud Run Job) that scales to zero ($0.00 idle cost).
- **Private Object Storage as the Durable State Root**: A private GCS/S3 bucket (`gs://swarm-social-20260926-hub`) serves as the immutable state machine and rendezvous point.
- **Zero Inbound Workstation Ports**: The operator's desktop workstation never exposes listening ports to the Internet. It communicates with the cloud entirely through outbound SigV4 signed requests to the storage bucket.
- **The Immutable "Pend" Primitive**: When a worker finishes drafting content, it writes an immutable manifest to storage with `status: "pending_review"` and immediately terminates. It possesses **zero** authority to publish to external platforms (such as Twitter, LinkedIn, or YouTube).
- **Leased Outcome Triggers**: Publishing only occurs after the human operator inspects the draft and token/compute cost locally in Swarm Desktop, and explicitly triggers publication either via an ephemeral, short-lived cloud lease or via a local script.

---

## 2. Durable Storage Hierarchy & Telemetry Schema

The bucket layout forms a verifiable filesystem of workers, sessions, deliverables, and financial ledgers:

```text
gs://<canonical-bucket>/
├── workers/
│   └── <worker_id>/
│       ├── worker.json                      # Worker registration & metadata
│       ├── base/                            # Context, instructions, tools, memory
│       └── sessions/
│           └── <session_id>/
│               ├── state.json               # Live execution progress (0-100%)
│               └── trace.jsonl              # Granular execution steps & reasoning
├── deliverables/
│   └── <worker_id>/
│       └── <deliverable_id>/
│           ├── manifest.json                # Cryptographic manifest with SHA-256 digests,
│           │                                # status ("pending_review", "approved", "published"),
│           │                                # and granular token/compute telemetry
│           └── files/                       # Actual output artifacts (markdown, JSON, images)
└── telemetry/
    └── daily/
        └── <YYYY-MM-DD>.json                # Daily spend ledger & run aggregation
```

### Manifest Telemetry Schema (`manifest.json`)
```json
{
  "id": "deliv_1790415900",
  "worker_id": "social-campaign-worker",
  "session_id": "sess_01j9x...",
  "title": "Swarm v1.4 Launch Thread",
  "summary": "5-tweet launch thread highlighting Storage Hub and Cloud Workers",
  "status": "pending_review",
  "created_at": 1790415900000,
  "updated_at": 1790415900000,
  "review": {
    "decision": "pending",
    "reviewed_by": null,
    "reviewed_at": null,
    "target": null
  },
  "telemetry": {
    "model": "gemini-3.8-flash",
    "thinking_level": "high",
    "prompt_tokens": 3420,
    "candidate_tokens": 1280,
    "thinking_tokens": 2640,
    "total_tokens": 7340,
    "compute_duration_ms": 5840,
    "cost_usd": 0.003005
  },
  "files": [
    {
      "name": "thread.md",
      "size": 1820,
      "sha256": "4a7f1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a"
    }
  ]
}
```

### Daily Spend Ledger (`telemetry/daily/YYYY-MM-DD.json`)
```json
{
  "date": "2026-09-26",
  "worker_id": "social-campaign-worker",
  "budget_cap_usd": 0.50,
  "total_runs": 3,
  "successful_runs": 3,
  "failed_runs": 0,
  "total_tokens": 22020,
  "total_thinking_tokens": 7920,
  "total_compute_seconds": 17.5,
  "total_spend_usd": 0.009015,
  "runs": [
    {
      "deliverable_id": "deliv_1790415900",
      "timestamp": 1790415900000,
      "cost_usd": 0.003005,
      "status": "pending_review"
    }
  ]
}
```

---

## 3. The Dual-Trigger Acceptance Mechanism

When Swarm Desktop's `storagehub.Service` scans the bucket and detects a deliverable with `status: "pending_review"`, it automatically constructs a rich **AI Deliverable Card** in Desktop Notifications / AI Inbox.

The card displays:
- Title and summary of the draft.
- Cryptographic SHA-256 badges.
- **Granular Cost Badge**: Run Cost (`$0.003`), Thinking Tokens (`2.6k`), Total Tokens (`7.3k`), and Duration (`5.8s`).

The operator has two concrete trigger choices:

```text
┌────────────────────────────────────────────────────────────────────────┐
│  AI DELIVERABLE: Swarm v1.4 Launch Thread                              │
├────────────────────────────────────────────────────────────────────────┤
│  Worker: social-campaign-worker · 9:00 AM UTC                          │
│                                                                        │
│  [💳 $0.0030]  [🧠 2.6k Thinking]  [📊 7.3k Tokens]  [⏱ 5.8s]         │
│                                                                        │
│  Files: thread.md (1.8 KB · SHA-256 verified)                          │
│                                                                        │
│  [✓ Approve for Cloud Dispatch]  [⚡ Run Local Script]  [Discard]     │
└────────────────────────────────────────────────────────────────────────┘
```

### Trigger Option A: Cloud Dispatch via Ephemeral Lease
1. The operator clicks **[Approve for Cloud Dispatch]**.
2. Swarm Desktop calls `POST /v1/storage/deliverables/{id}/accept` with `target: "cloud"`.
3. Swarm updates `manifest.json` in GCS to `status: "approved"`.
4. Cloud Run Job executes in `publish` mode:
   - Fetches the Twitter API credentials through an on-demand, short-lived lease or scoped Secret Manager accessor.
   - Dispatches the post to Twitter API v2.
   - Writes the live tweet URL and receipt into `manifest.json` with `status: "published"`.
   - Shuts down. The lease closes immediately.

### Trigger Option B: Local Script Execution
1. The operator clicks **[Run Local Script]** (or prompts the AI: *"Post today's pending thread"*).
2. Swarm Desktop executes the local registered action:
   `npx tsx scripts/publish-local.ts --deliverable <id>`.
3. The local script:
   - Downloads the approved content from GCS.
   - Dispatches the post using local browser session cookies or local API keys.
   - Calls `POST /v1/storage/deliverables/{id}/accept` with `target: "local"`, marking `status: "published"` in both GCS and the local Git worktree.

---

## 4. Phased Implementation Roadmap

- **Phase A (Current)**:
  - Update `@swarm/sdk` types: add `telemetry` fields to `DeliverableManifest` and `PublishDeliverableOptions`.
  - Update Go backend: extend `StorageDiscoveredDeliverableRecord` with `telemetry` in `storage_hub_types.go` and `pebble_storage_hub.go`.
  - Implement `POST /v1/storage/deliverables/{id}/accept` in `storagehub.go` and `service.go` to update GCS `manifest.json` with operator signature, timestamp, and target (`cloud` or `local`).
  - Unit tests covering telemetry serialization and manifest write-back.

- **Phase B (Current)**:
  - Update Desktop UI types (`storage/types.ts` and `notifications/types.ts`).
  - Update Desktop Notifications Modal (`desktop-notifications-modal.tsx`):
    - Render telemetry pills (Cost USD, Thinking Tokens, Duration).
    - Provide dual action buttons: **Approve for Cloud Dispatch** and **Run Local Script**.
  - Update Cloud Settings tab to display telemetry stats.
  - Verify Web build and tests.

- **Phase C (Follow-up)**:
  - Author `campaign-worker` in `swarm-social` using Gemini 3.8 Flash with thinking: high.
  - Build minimal Containerfile and deploy Cloud Run Job on `swarm-social-20260926`.
  - Configure daily Cloud Scheduler and run live 0-to-1 test.
