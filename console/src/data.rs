//! What the console shows: machines, projects, sessions, roles, media and
//! usage. `swarm::live` fills it from Swarm; the demo set below is for design
//! screenshots (SWARM_CONSOLE_DEMO).

use gpui::SharedString;

pub type Text = SharedString;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Health {
    Online,
    Busy,
    Offline,
}

#[derive(Clone)]
pub struct Machine {
    pub name: Text,
    /// How the console reaches it: this pc, tailnet, relay or ssh.
    pub kind: Text,
    pub health: Health,
    pub running: u32,
    pub slots: u32,
    pub cost_today: f32,
    /// Release branch and how it compares with the branch tip.
    pub build: Text,
    pub behind: u32,
    /// Key level the console holds: read, write or full.
    pub access: Text,
    pub sandbox: Text,
    pub uptime: Text,
    pub cpu: f32,
    pub mem: f32,
    /// Where a fleet machine's Swarm answers (empty for home and the demo).
    pub gateway: Text,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum RunState {
    Running,
    NeedsYou,
    Idle,
    Failed,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum TaskStatus {
    PendingApproval,
    Queued,
    Running,
    NeedsInput,
    NeedsReview,
    Completed,
    Failed,
    Rejected,
}

impl TaskStatus {
    pub fn label(self) -> &'static str {
        match self {
            TaskStatus::PendingApproval => "approve?",
            TaskStatus::Queued => "queued",
            TaskStatus::Running => "running",
            TaskStatus::NeedsInput => "question",
            TaskStatus::NeedsReview => "review",
            TaskStatus::Completed => "done",
            TaskStatus::Failed => "failed",
            TaskStatus::Rejected => "rejected",
        }
    }

    pub fn needs_you(self) -> bool {
        matches!(
            self,
            TaskStatus::PendingApproval | TaskStatus::NeedsInput | TaskStatus::NeedsReview
        )
    }
}

#[derive(Clone)]
pub struct Task {
    /// Swarm's task id, for approve and merge (empty in the demo set).
    pub key: Text,
    /// The session doing the task, for stop (empty in the demo set).
    pub session: Text,
    pub title: Text,
    pub role: Text,
    pub machine: usize,
    pub status: TaskStatus,
    /// What it is doing right now, one line.
    pub activity: Text,
    /// Background sessions this task ran: attempts, child agents, reviews.
    pub runs: Vec<Run>,
    /// What the task delivered, in its own words: the active attempt's summary
    /// (a Finder's report, a Coder's account of its commit).
    pub report: Text,
    /// What Swarm's delivery assessment allows for finished work.
    pub merge: Merge,
}

/// Swarm's word on a finished task's work (its delivery assessment): commits
/// to integrate, a recorded delta to recover, or nothing to merge, with its
/// reason (a report-only task, a lane Swarm cannot see).
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Merge {
    Integrate,
    Recover,
    None(Text),
}

/// One background session behind a task.
#[derive(Clone)]
pub struct Run {
    pub agent: Text,
    pub machine: usize,
    pub state: RunState,
    pub label: Text,
    pub age: Text,
    pub cost: f32,
}

fn run(agent: Text, machine: usize, state: RunState, label: Text, age: Text, cost: f32) -> Run {
    Run { agent, machine, state, label, age, cost }
}

/// Where a tool call stands, as Swarm's tool events say.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum ToolState {
    Running,
    Done,
    Failed,
    Cancelled,
}

#[derive(Clone)]
pub enum Entry {
    User(Text),
    Assistant(Text),
    Tool {
        name: Text,
        detail: Text,
        /// Swarm's call id ("" for notes such as attachments and errors).
        call_id: Text,
        state: ToolState,
        /// A brief result, or a failure's reason: one short line.
        summary: Option<Text>,
        /// How long the call took, as Swarm recorded it.
        duration_ms: Option<u64>,
        /// When Swarm recorded the call's start (ms since the epoch), for
        /// the time a running call has taken so far.
        started_at: Option<u64>,
    },
}

impl Entry {
    /// A finished tool line with no call behind it: a note (attached files,
    /// an error) or a demo line.
    pub fn tool(name: impl Into<Text>, detail: impl Into<Text>) -> Entry {
        Entry::Tool { name: name.into(), detail: detail.into(), call_id: "".into(), state: ToolState::Done, summary: None, duration_ms: None, started_at: None }
    }

    /// A tool call as it is drawn: one line in its colored parts, its time
    /// so far (running) or its duration (finished). None for other entries.
    pub fn tool_line(&self, now_ms: u64) -> Option<crate::swarm::tool_summary::Line> {
        use crate::swarm::tool_summary as ts;
        let Entry::Tool { name, detail, state, summary, duration_ms, started_at, .. } = self else { return None };
        let time = match state {
            ToolState::Running => started_at.and_then(|at| ts::elapsed(now_ms.saturating_sub(at))).unwrap_or_default(),
            _ => duration_ms.map(ts::duration).unwrap_or_default(),
        };
        let summary = match (state, summary) {
            (_, Some(s)) if !s.is_empty() => s.to_string(),
            (ToolState::Cancelled, _) => "stopped".into(),
            (ToolState::Failed, _) => "failed".into(),
            _ => String::new(),
        };
        Some(ts::line(name, detail, &summary, &time))
    }
}

/// Now, in ms since the epoch (for a running tool call's time so far).
pub fn now_ms() -> u64 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis() as u64).unwrap_or(0)
}

/// A project: a name and folders on one or more machines. Sessions belong to one.
#[derive(Clone)]
pub struct Project {
    pub name: Text,
    pub machines: Vec<usize>,
    /// Swarm's project id (empty in the demo set).
    pub id: Text,
    /// Its folders on the machine, the first one primary, and each one's
    /// role in Swarm ("primary_code", "auxiliary", "docs").
    pub workspaces: Vec<Text>,
    pub roles: Vec<Text>,
    /// project.md: the context Swarm wrote (or the person edited).
    pub context: Text,
    pub context_generation: ContextGeneration,
    /// The project's theme in Swarm (empty: the account's active theme).
    pub theme_id: Text,
}

#[derive(Clone, Default)]
pub struct ContextGeneration {
    pub status: Text,
    pub attempt: u64,
    pub error: Text,
}

impl ContextGeneration {
    pub fn updating(&self) -> bool {
        matches!(self.status.as_ref(), "pending" | "running" | "generating")
    }

    pub fn label(&self) -> String {
        if self.updating() { return "Swarm is updating project.md…".into(); }
        match self.status.as_ref() {
            "failed" => format!("Could not update project.md: {}", self.error),
            "ready" => "project.md is ready.".into(),
            _ => "Swarm has not written project.md yet.".into(),
        }
    }
}

/// Where a session lives in Swarm: the task that started it and its session.
#[derive(Clone, PartialEq, Eq)]
pub struct LiveRef {
    pub task_id: Text,
    pub session_id: Text,
}

/// Every session is an Orchestrator conversation in one project. Coder,
/// finder, designer and reviewer work appears as tasks inside it.
#[derive(Clone)]
pub struct Session {
    /// None for the demo set and for a new session not yet started.
    pub live: Option<LiveRef>,
    pub title: Text,
    pub project: usize,
    /// The machine whose Orchestrator runs this conversation.
    pub machine: usize,
    pub model: Text,
    pub state: RunState,
    pub age: Text,
    pub elapsed: Text,
    pub tokens: Text,
    pub entries: Vec<Entry>,
    pub tasks: Vec<Task>,
    pub activity: Vec<(Text, Text)>,
    /// The task status in Swarm, for review and approval actions.
    pub status: Option<TaskStatus>,
    /// Swarm's run record for the session (None in the demo set).
    pub run: Option<RunClock>,
    /// What the session waits on you for: a question or a tool approval.
    pub pending: Option<Pending>,
    /// Calls the Orchestrator made to your machine that wait on this app.
    pub calls: Vec<ClientCall>,
    /// The model picked for a session not started yet; it starts with it.
    pub model_choice: Option<Assignment>,
    /// When that pick is a favorite: its id in Swarm.
    pub model_favorite: Option<String>,
    /// Pinned to the top of its project's list (Swarm metadata `pinned`).
    pub pinned: bool,
    /// Messages you sent that Swarm's record does not show yet, each with how
    /// many times its text was already there. They stay on screen until it
    /// shows them, so a poll that read the conversation just before your
    /// message arrived never hides it.
    pub outbox: Vec<(Text, usize)>,
}

/// A call to your machine (machine_run, machine_read, machine_write,
/// manage_browser, a request for a folder or for access) that this app
/// answers. Swarm says which device it targets and whether it may run there.
#[derive(Clone, PartialEq, Debug)]
pub struct ClientCall {
    /// The pending request's id in Swarm.
    pub id: Text,
    pub tool: Text,
    /// The client session id of the device the call targets.
    pub target_device: Text,
    /// That device is this app (Swarm's this_device).
    pub this_device: bool,
    /// "awaiting_approval" (the person decides) or "ready" (the target runs it).
    pub execution: Text,
    pub args: serde_json::Value,
}

/// What a session waits on: an ask_user question (or several) or a tool
/// approval, read the way Swarm's SDK reads it (packages/sdk/src/permission-ui.ts).
#[derive(Clone, PartialEq, Eq)]
pub struct Pending {
    pub id: Text,
    /// An ask_user request, else a tool approval.
    pub question: bool,
    /// For an approval: the tool and what it wants to run.
    pub text: Text,
    pub title: Text,
    /// Several questions, answered together as {"answers": {id: value}}.
    pub structured: bool,
    pub questions: Vec<Question>,
}

#[derive(Clone, PartialEq, Eq)]
pub struct Question {
    pub id: Text,
    pub header: Text,
    pub text: Text,
    pub required: bool,
    pub options: Vec<Choice>,
}

#[derive(Clone, PartialEq, Eq)]
pub struct Choice {
    pub label: Text,
    /// What the answer sends.
    pub value: Text,
    pub description: Text,
    /// "Custom response": you type the answer.
    pub custom: bool,
}

pub fn machines() -> Vec<Machine> {
    vec![
        Machine { name: "studio".into(), kind: "local".into(), health: Health::Busy, running: 3, slots: 6, cost_today: 4.12, build: "dev".into(), behind: 0, access: "full".into(), sandbox: "gVisor".into(), uptime: "3d 4h".into(), cpu: 0.62, mem: 0.48, gateway: "".into() },
        Machine { name: "forge".into(), kind: "tailnet".into(), health: Health::Online, running: 2, slots: 8, cost_today: 1.37, build: "dev".into(), behind: 3, access: "full".into(), sandbox: "gVisor".into(), uptime: "11d".into(), cpu: 0.31, mem: 0.36, gateway: "".into() },
        Machine { name: "edge-1".into(), kind: "relay".into(), health: Health::Online, running: 0, slots: 4, cost_today: 0.21, build: "main".into(), behind: 0, access: "write".into(), sandbox: "bubblewrap".into(), uptime: "26d".into(), cpu: 0.04, mem: 0.18, gateway: "".into() },
        Machine { name: "lab".into(), kind: "ssh".into(), health: Health::Offline, running: 0, slots: 4, cost_today: 0.0, build: "dev".into(), behind: 0, access: "full".into(), sandbox: "gVisor".into(), uptime: "—".into(), cpu: 0.0, mem: 0.0, gateway: "".into() },
    ]
}

pub fn projects() -> Vec<Project> {
    vec![
        Project { name: "swarm-go".into(), machines: vec![0, 1], id: "".into(), workspaces: vec![], roles: vec![], context: "".into(), context_generation: ContextGeneration::default(), theme_id: "".into() },
        Project { name: "site".into(), machines: vec![1, 2], id: "".into(), workspaces: vec![], roles: vec![], context: "".into(), context_generation: ContextGeneration::default(), theme_id: "".into() },
        Project { name: "console".into(), machines: vec![2], id: "".into(), workspaces: vec![], roles: vec![], context: "".into(), context_generation: ContextGeneration::default(), theme_id: "".into() },
    ]
}

const PLAN: &str = "opus-5.5 · plan";

pub fn sessions() -> Vec<Session> {
    vec![
        Session {
            live: None,
            status: None,
            pending: None,
            model_choice: None,
            model_favorite: None,
            pinned: false,
            calls: vec![],
            outbox: vec![],
            run: None,
            title: "Ship job automations".into(),
            project: 0,
            machine: 0,
            model: PLAN.into(),
            state: RunState::NeedsYou,
            age: "now".into(),
            elapsed: "14m 02s".into(),
            tokens: "412k".into(),
            entries: vec![
                Entry::User("Finish the automation rounds work and get a reviewer on it. Use forge for the heavy test run.".into()),
                Entry::Assistant("Read project.md and the open branch. Three pieces left: the reopen step, round limits, and docs. I'll split them so they can land independently.".into()),
                Entry::tool("finder", "search · automation reopen semantics · 6 files"),
                Entry::tool("manage_projects", "propose_task × 3 · swarm-go"),
                Entry::Assistant("The reopen step and docs are running. Round limits needs your approval because it changes a stored schema.".into()),
                Entry::tool("fleet", "forge · run_task coder · integration tests"),
            ],
            tasks: vec![
                Task { key: "".into(), session: "".into(), title: "Reopen step for blocked deliveries".into(), role: "coder".into(), machine: 0, status: TaskStatus::Running, activity: "editing automation/reopen.go · step 3 of 5".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![run("coder".into(), 0, RunState::Running, "attempt 2".into(), "now".into(), 0.41), run("coder".into(), 0, RunState::Failed, "attempt 1 · tests failed".into(), "9m".into(), 0.21)] },
                Task { key: "".into(), session: "".into(), title: "Round limits on automation steps".into(), role: "coder".into(), machine: 0, status: TaskStatus::PendingApproval, activity: "waiting for your approval · changes a stored schema".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![] },
                Task { key: "".into(), session: "".into(), title: "Document conditions and rounds".into(), role: "swarm".into(), machine: 0, status: TaskStatus::NeedsReview, activity: "ready to merge · 2 commits".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![run("swarm".into(), 0, RunState::Idle, "docs pass".into(), "4m".into(), 0.22), run("finder".into(), 0, RunState::Idle, "link check".into(), "3m".into(), 0.09)] },
                Task { key: "".into(), session: "".into(), title: "Integration test sweep".into(), role: "coder".into(), machine: 1, status: TaskStatus::Running, activity: "go test ./... · 212 of 640 packages".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![run("coder".into(), 1, RunState::Running, "test sweep".into(), "now".into(), 0.48)] },
                Task { key: "".into(), session: "".into(), title: "Independent review of T-418".into(), role: "reviewer".into(), machine: 1, status: TaskStatus::Queued, activity: "starts when T-418 lands".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![] },
            ],
            activity: vec![
                ("14:02".into(), "coder T-418 committed 2 files".into()),
                ("13:58".into(), "forge accepted T-421".into()),
                ("13:55".into(), "T-419 waiting for approval".into()),
                ("13:51".into(), "finder returned 6 sources".into()),
            ],
        },
        Session {
            live: None,
            status: None,
            pending: None,
            model_choice: None,
            model_favorite: None,
            pinned: false,
            calls: vec![],
            outbox: vec![],
            run: None,
            title: "Fix flaky sandbox test".into(),
            project: 0,
            machine: 0,
            model: PLAN.into(),
            state: RunState::Running,
            age: "1m".into(),
            elapsed: "5m 20s".into(),
            tokens: "97k".into(),
            entries: vec![
                Entry::User("The sandbox scratch test flakes on CI. Find out why and fix it.".into()),
                Entry::tool("finder", "reproduce · go test -run Scratch -count 20 · 3 failures"),
                Entry::Assistant("The cleanup races the scratch unmount. Small, contained fix, so it goes straight to a coder on studio.".into()),
            ],
            tasks: vec![
                Task { key: "".into(), session: "".into(), title: "Wait on the mount table before cleanup".into(), role: "coder".into(), machine: 0, status: TaskStatus::Running, activity: "go test -run Scratch -count 50 · 31 passed".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![run("finder".into(), 0, RunState::Idle, "reproduce".into(), "5m".into(), 0.05), run("coder".into(), 0, RunState::Running, "fix".into(), "now".into(), 0.16)] },
            ],
            activity: vec![("14:01".into(), "coder T-430 edited sandbox/scratch.go".into())],
        },
        Session {
            live: None,
            status: None,
            pending: None,
            model_choice: None,
            model_favorite: None,
            pinned: false,
            calls: vec![],
            outbox: vec![],
            run: None,
            title: "Where do retries live?".into(),
            project: 0,
            machine: 1,
            model: PLAN.into(),
            state: RunState::Idle,
            age: "26m".into(),
            elapsed: "1m 12s".into(),
            tokens: "22k".into(),
            entries: vec![
                Entry::User("Where are provider retries decided?".into()),
                Entry::tool("finder", "search · retry backoff · 4 files"),
                Entry::Assistant("In the provider adapters, not the orchestration layer. Backoff is bounded per adapter, and the run loop only sees the final error.".into()),
            ],
            tasks: vec![],
            activity: vec![],
        },
        Session {
            live: None,
            status: None,
            pending: None,
            model_choice: None,
            model_favorite: None,
            pinned: false,
            calls: vec![],
            outbox: vec![],
            run: None,
            title: "Landing page refresh".into(),
            project: 1,
            machine: 1,
            model: PLAN.into(),
            state: RunState::Running,
            age: "3m".into(),
            elapsed: "41m 10s".into(),
            tokens: "188k".into(),
            entries: vec![
                Entry::User("Three hero directions, dark and ominous. Keep the canonical logo.".into()),
                Entry::Assistant("Briefed the designer with the brand notes from project.md. Rendering three directions now.".into()),
                Entry::tool("swarm_design", "count 3 · hero.html"),
            ],
            tasks: vec![
                Task { key: "".into(), session: "".into(), title: "Hero directions ×3".into(), role: "designer".into(), machine: 1, status: TaskStatus::Running, activity: "rendering direction 3 of 3".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![run("designer".into(), 1, RunState::Running, "directions".into(), "now".into(), 0.55)] },
                Task { key: "".into(), session: "".into(), title: "Lighthouse pass".into(), role: "finder".into(), machine: 2, status: TaskStatus::NeedsInput, activity: "asks: which pages should it audit?".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![run("finder".into(), 2, RunState::NeedsYou, "lighthouse".into(), "2m".into(), 0.0)] },
            ],
            activity: vec![("13:40".into(), "designer rendered direction 2".into()), ("13:31".into(), "designer rendered direction 1".into())],
        },
        Session {
            live: None,
            status: None,
            pending: None,
            model_choice: None,
            model_favorite: None,
            pinned: false,
            calls: vec![],
            outbox: vec![],
            run: None,
            title: "Release notes draft".into(),
            project: 1,
            machine: 3,
            model: PLAN.into(),
            state: RunState::Failed,
            age: "1d".into(),
            elapsed: "0m 40s".into(),
            tokens: "4k".into(),
            entries: vec![
                Entry::User("Draft release notes from this week's merges.".into()),
                Entry::tool("error", "lab is offline · last seen 1d ago"),
            ],
            tasks: vec![],
            activity: vec![],
        },
        Session {
            live: None,
            status: None,
            pending: None,
            model_choice: None,
            model_favorite: None,
            pinned: false,
            calls: vec![],
            outbox: vec![],
            run: None,
            title: "Nightly dependency audit".into(),
            project: 2,
            machine: 2,
            model: PLAN.into(),
            state: RunState::Idle,
            age: "2h".into(),
            elapsed: "6m 48s".into(),
            tokens: "61k".into(),
            entries: vec![
                Entry::User("Set up a nightly audit that opens a task when a dependency has a security fix.".into()),
                Entry::Assistant("Created an automation on edge-1: every night at 02:00 a finder checks advisories and proposes a coder task when one applies.".into()),
                Entry::tool("manage_automation", "create · nightly-audit · 02:00"),
            ],
            tasks: vec![
                Task { key: "".into(), session: "".into(), title: "Bump ws to 8.21.3".into(), role: "coder".into(), machine: 2, status: TaskStatus::Completed, activity: "merged · 1 commit".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![run("coder".into(), 2, RunState::Idle, "bump".into(), "2h".into(), 0.09)] },
                Task { key: "".into(), session: "".into(), title: "Advisory feed check".into(), role: "finder".into(), machine: 2, status: TaskStatus::Failed, activity: "advisory feed timed out".into(), report: "".into(), merge: Merge::None("".into()), runs: vec![run("finder".into(), 2, RunState::Failed, "check".into(), "2h".into(), 0.01)] },
            ],
            activity: vec![("02:00".into(), "automation nightly-audit ran".into()), ("02:04".into(), "T-12 completed".into())],
        },
    ]
}

// Agents: one model table for the fleet, with per-machine overrides.

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Assignment {
    pub provider: Text,
    pub model: Text,
    pub thinking: Text,
    /// The provider's service tier (priority), when one was chosen.
    pub tier: Text,
}

impl Assignment {
    pub fn short(&self) -> String {
        let mut out = self.model.to_string();
        if !self.thinking.is_empty() {
            out.push_str(&format!(" · {}", self.thinking));
        }
        if !self.tier.is_empty() {
            out.push_str(&format!(" · {}", self.tier));
        }
        out
    }
}

#[derive(Clone, PartialEq, Eq)]
pub enum Cell {
    /// Runs the fleet default.
    InSync,
    /// Deliberately different on this machine.
    Custom(Assignment),
    /// Differs from what it should run; Apply fixes it.
    Drift(Assignment),
    /// The provider is not connected on this machine.
    Unavailable,
    /// The machine could not be reached.
    Unknown,
}

#[derive(Clone)]
pub struct Role {
    pub key: Text,
    pub label: Text,
    pub purpose: Text,
    pub default: Assignment,
    /// One cell per machine, in fleet order.
    pub cells: Vec<Cell>,
}

fn a(provider: Text, model: Text, thinking: Text) -> Assignment {
    Assignment { provider, model, thinking, tier: "".into() }
}

pub fn roles() -> Vec<Role> {
    use Cell::*;
    vec![
        Role { key: "plan".into(), label: "Orchestrator".into(), purpose: "runs your sessions, delegates".into(), default: a("anthropic".into(), "opus-5.5".into(), "high".into()), cells: vec![InSync, InSync, InSync, Unknown] },
        Role { key: "action".into(), label: "Swarm".into(), purpose: "big tasks, coordinates children".into(), default: a("anthropic".into(), "opus-5.5".into(), "medium".into()), cells: vec![InSync, InSync, Custom(a("openai".into(), "gpt-5.5".into(), "high".into())), Unknown] },
        Role { key: "coder".into(), label: "Coder".into(), purpose: "edits code in a worktree".into(), default: a("openai".into(), "gpt-5.5".into(), "high".into()), cells: vec![InSync, Drift(a("openai".into(), "gpt-5.4".into(), "high".into())), InSync, Unknown] },
        Role { key: "finder".into(), label: "Finder".into(), purpose: "searches, reads, reports".into(), default: a("anthropic".into(), "sonnet-5.5".into(), "".into()), cells: vec![InSync, InSync, InSync, Unknown] },
        Role { key: "designer".into(), label: "Designer".into(), purpose: "UI, HTML designs, images".into(), default: a("google".into(), "gemini-3.5-pro".into(), "".into()), cells: vec![InSync, InSync, Unavailable, Unknown] },
        Role { key: "reviewer".into(), label: "Reviewer".into(), purpose: "independent read-only review".into(), default: a("anthropic".into(), "opus-5.5".into(), "high".into()), cells: vec![InSync, Custom(a("openai".into(), "gpt-5.5".into(), "xhigh".into())), InSync, Unknown] },
        Role { key: "compact".into(), label: "Compact".into(), purpose: "summarises long sessions".into(), default: a("anthropic".into(), "haiku-5.5".into(), "".into()), cells: vec![InSync, InSync, InSync, Unknown] },
        Role { key: "router".into(), label: "Router".into(), purpose: "titles and small routing".into(), default: a("anthropic".into(), "haiku-5.5".into(), "".into()), cells: vec![InSync, Drift(a("google".into(), "gemini-3.5-flash".into(), "".into())), InSync, Unknown] },
    ]
}

// Media made by agents across the fleet.

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum MediaKind {
    Image,
    Video,
    Sound,
    Design,
}

impl MediaKind {
    pub fn label(self) -> &'static str {
        match self {
            MediaKind::Image => "image".into(),
            MediaKind::Video => "video".into(),
            MediaKind::Sound => "sound".into(),
            MediaKind::Design => "design".into(),
        }
    }
}

#[derive(Clone)]
pub struct Media {
    /// Swarm's artifact id, and the session that made it (empty in the demo set).
    pub id: Text,
    pub session_id: Text,
    /// That session's place in the console's list, once known.
    pub session: Option<usize>,
    /// This app's copy of the file, once downloaded.
    pub file: Option<std::path::PathBuf>,
    /// A small copy of an image for cards: the full image is decoded only to
    /// preview it.
    pub thumb: Option<std::path::PathBuf>,
    /// An image's width and height, read from the downloaded file.
    pub dims: Option<(u32, u32)>,
    pub title: Text,
    pub kind: MediaKind,
    pub project: usize,
    pub machine: usize,
    pub model: Text,
    pub meta: Text,
    pub age: Text,
    /// Placeholder tint until real thumbnails load.
    pub hue: f32,
    pub pending: bool,
}

pub fn media() -> Vec<Media> {
    vec![
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Hero direction 1 · eclipse".into(), kind: MediaKind::Design, project: 1, machine: 1, model: "gemini-3.5-pro".into(), meta: "hero.html · 1440×900".into(), age: "31m".into(), hue: 0.72, pending: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Hero direction 2 · swarm field".into(), kind: MediaKind::Design, project: 1, machine: 1, model: "gemini-3.5-pro".into(), meta: "hero.html · 1440×900".into(), age: "22m".into(), hue: 0.58, pending: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Hero direction 3".into(), kind: MediaKind::Design, project: 1, machine: 1, model: "gemini-3.5-pro".into(), meta: "rendering".into(), age: "now".into(), hue: 0.66, pending: true },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: Some(0), file: None, dims: None, title: "Launch teaser".into(), kind: MediaKind::Video, project: 1, machine: 0, model: "veo-4".into(), meta: "12s · 1080p · sound".into(), age: "2h".into(), hue: 0.04, pending: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: Some(0), file: None, dims: None, title: "Logo mark, dark".into(), kind: MediaKind::Image, project: 1, machine: 0, model: "imagen-5".into(), meta: "2048² · png".into(), age: "1d".into(), hue: 0.0, pending: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Notification chime".into(), kind: MediaKind::Sound, project: 2, machine: 2, model: "lyria-3".into(), meta: "1.4s · wav".into(), age: "2d".into(), hue: 0.36, pending: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Fleet diagram".into(), kind: MediaKind::Image, project: 0, machine: 0, model: "imagen-5".into(), meta: "1600×900 · png".into(), age: "3d".into(), hue: 0.52, pending: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Onboarding walkthrough".into(), kind: MediaKind::Video, project: 2, machine: 1, model: "veo-4".into(), meta: "48s · 1080p".into(), age: "4d".into(), hue: 0.12, pending: false },
    ]
}

// Usage: tokens, spend and sign-in plan limits.

/// Usage as Swarm records it: the last 30 days, today, and each sign-in
/// plan's rate limits.
#[derive(Clone, Default)]
pub struct Usage {
    pub plans: Vec<Plan>,
    pub month: Tokens,
    pub today: Tokens,
    /// Billed spend; sign-in plans add nothing to it.
    pub cost: f32,
    /// The same use at API prices, where Swarm knows them.
    pub nominal: f32,
    pub today_cost: f32,
    /// Why spend reads as it does (Swarm's own note).
    pub cost_note: Text,
    pub turns: u32,
    pub sessions: u32,
    /// 0 means no limit.
    pub daily_cost_limit: f32,
    pub daily_tokens_limit: f64,
    /// Each day with use, oldest first.
    pub daily: Vec<Day>,
    pub models: Vec<ModelUse>,
    /// The most recently active sessions, newest first.
    pub top_sessions: Vec<SessionUse>,
    /// Images, videos, sounds and what they cost.
    pub media: (u32, u32, u32, f32),
    /// Worker runs today: finished, failed, running.
    pub runs_today: (u32, u32, u32),
}

#[derive(Clone, Copy, Default)]
pub struct Tokens {
    pub total: f64,
    pub input: f64,
    pub output: f64,
    pub cached: f64,
    pub thinking: f64,
}

#[derive(Clone)]
pub struct Day {
    pub date: Text,
    /// The day's start, Unix ms.
    pub at: u64,
    pub tokens: f64,
    pub cost: f32,
}

#[derive(Clone)]
pub struct ModelUse {
    pub model: Text,
    /// subscription, metered or unpriced.
    pub billing: Text,
    pub tokens: Tokens,
    pub turns: u32,
    pub sessions: u32,
    pub cost: f32,
    pub nominal: f32,
}

#[derive(Clone)]
pub struct SessionUse {
    pub title: Text,
    pub model: Text,
    pub tokens: f64,
    pub turns: u32,
    /// Unix ms.
    pub last_active: u64,
}

/// A sign-in plan (ChatGPT, Claude) and how much of its limits is used.
#[derive(Clone)]
pub struct Plan {
    pub name: Text,
    /// "pro", "max"; empty when unknown.
    pub kind: Text,
    pub connected: bool,
    /// Why it is not connected, or when the reading is from.
    pub note: Text,
    pub limit_reached: bool,
    pub windows: Vec<PlanWindow>,
}

#[derive(Clone)]
pub struct PlanWindow {
    /// "5 hours", "week".
    pub label: Text,
    /// 0 to 100.
    pub used: f32,
    /// Unix seconds; 0 when unknown.
    pub resets_at: u64,
}

pub fn usage() -> Usage {
    let tokens = |total: f64| Tokens { total, input: total * 0.86, output: total * 0.04, cached: total * 0.6, thinking: total * 0.1 };
    let model = |model: &str, billing: &str, total: f64, turns: u32, sessions: u32, cost: f32, nominal: f32| ModelUse {
        model: model.to_string().into(),
        billing: billing.to_string().into(),
        tokens: tokens(total),
        turns,
        sessions,
        cost,
        nominal,
    };
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
    Usage {
        plans: vec![
            Plan {
                name: "ChatGPT".into(),
                kind: "pro".into(),
                connected: true,
                note: "".into(),
                limit_reached: false,
                windows: vec![
                    PlanWindow { label: "5 hours".into(), used: 41., resets_at: now + 2 * 3600 + 14 * 60 },
                    PlanWindow { label: "week".into(), used: 18., resets_at: now + 4 * 86400 },
                ],
            },
            Plan { name: "Claude".into(), kind: "max".into(), connected: true, note: "".into(), limit_reached: false, windows: vec![PlanWindow { label: "5 hours".into(), used: 63., resets_at: now + 3600 }] },
        ],
        month: tokens(48_200_000.),
        today: tokens(2_100_000.),
        cost: 92.4,
        nominal: 311.9,
        today_cost: 5.7,
        cost_note: "subscription (sign-in) providers report no per-token cost.".into(),
        turns: 1840,
        sessions: 212,
        daily_cost_limit: 15.,
        daily_tokens_limit: 0.,
        daily: [3.1, 4.4, 2.2, 6.8, 5.1, 1.2, 0.9, 4.7, 7.9, 5.6, 3.3, 6.1, 8.4, 5.7]
            .iter()
            .enumerate()
            .map(|(i, m)| Day { date: format!("2026-10-{:02}", 8 + i).into(), at: (now / 86_400 - 13 + i as u64) * 86_400_000, tokens: m * 1_000_000., cost: *m as f32 * 0.9 })
            .collect(),
        models: vec![
            model("gpt-6-astra", "subscription", 21_400_000., 820, 96, 0., 140.2),
            model("claude-opus-5-5", "subscription", 14_900_000., 510, 61, 0., 120.7),
            model("gpt-6.1-sol", "subscription", 8_300_000., 390, 44, 0., 39.1),
            model("gemini-3-pro", "metered", 3_600_000., 120, 11, 92.4, 0.),
        ],
        top_sessions: vec![
            SessionUse { title: "Ship job automations".into(), model: "claude-opus-5-5".into(), tokens: 4_120_000., turns: 88, last_active: now * 1000 },
            SessionUse { title: "Fix flaky sandbox test".into(), model: "gpt-6.1-sol".into(), tokens: 970_000., turns: 31, last_active: (now - 60) * 1000 },
            SessionUse { title: "Where do retries live?".into(), model: "gpt-6-astra".into(), tokens: 220_000., turns: 6, last_active: (now - 26 * 60) * 1000 },
        ],
        media: (14, 2, 0, 3.1),
        runs_today: (41, 3, 5),
    }
}

/// A session's time as Swarm records it (Unix ms). While a run is `active`
/// its time so far is now - `started_at` and `cumulative_ms` holds the runs
/// before it; once it finishes, `cumulative_ms` includes it and `last_ms` is
/// its own time.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub struct RunClock {
    pub active: bool,
    pub started_at: u64,
    pub cumulative_ms: u64,
    pub last_ms: u64,
    /// When the session last had a message.
    pub last_message_at: u64,
}

impl RunClock {
    /// (this run, all runs) in ms at `now`: the current run while one is on,
    /// else the last finished one.
    pub fn times(&self, now: u64) -> (u64, u64) {
        if self.active && self.started_at > 0 {
            let current = now.saturating_sub(self.started_at);
            (current, self.cumulative_ms + current)
        } else {
            (self.last_ms, self.cumulative_ms)
        }
    }
}

/// 42_000 → "42s", 312_000 → "5m 12s", 4_000_000 → "1h 06m".
pub fn clock(ms: u64) -> String {
    let s = ms / 1000;
    match s {
        s if s < 60 => format!("{s}s"),
        s if s < 3600 => format!("{}m {:02}s", s / 60, s % 60),
        s => format!("{}h {:02}m", s / 3600, (s / 60) % 60),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn run_times_follow_swarms_record() {
        // "hey what's up" as Swarm recorded it after its last run.
        let done = RunClock { active: false, started_at: 1_792_570_372_722, cumulative_ms: 149_459, last_ms: 17_892, last_message_at: 0 };
        assert_eq!(done.times(1_792_570_500_000), (17_892, 149_459));
        // A run on for 30s after 100s of earlier runs.
        let on = RunClock { active: true, started_at: 1_000_000, cumulative_ms: 100_000, last_ms: 0, last_message_at: 0 };
        assert_eq!(on.times(1_030_000), (30_000, 130_000));
        assert_eq!(clock(149_459), "2m 29s");
        assert_eq!(clock(42_000), "42s");
        assert_eq!(clock(4_000_000), "1h 06m");
    }
}

impl Session {
    /// How many of your messages in Swarm's record read `text`.
    pub fn sent_count(&self, text: &str) -> usize {
        self.entries.iter().filter(|e| matches!(e, Entry::User(u) if u.trim() == text.trim())).count()
    }

    /// Drops the outbox messages Swarm's record now shows.
    pub fn settle_outbox(&mut self) {
        let mut outbox = std::mem::take(&mut self.outbox);
        outbox.retain(|(text, before)| self.sent_count(text) <= *before);
        self.outbox = outbox;
    }
}

/// A file waiting in a message box, sent with the next message.
#[derive(Clone, Debug)]
pub struct Attachment {
    pub name: String,
    pub mime: String,
    pub data: std::sync::Arc<Vec<u8>>,
}

/// A model provider as Swarm reports it (GET /v1/providers), with its stored
/// keys (GET /v1/auth/credentials). One key per provider is active.
#[derive(Clone, Debug)]
pub struct Provider {
    pub id: Text,
    pub ready: bool,
    /// Why it is not ready, in Swarm's words.
    pub reason: Text,
    pub default_model: Text,
    /// Takes an API key.
    pub accepts_key: bool,
    /// Signs in with a ChatGPT plan (the device code).
    pub oauth: bool,
    pub keys: Vec<Credential>,
}

/// One stored key or sign-in, as Swarm describes it; never the secret.
#[derive(Clone, Debug)]
pub struct Credential {
    pub id: Text,
    pub provider: Text,
    pub active: bool,
    pub auth_type: Text,
    pub label: Text,
    pub last4: Text,
    pub created_at: u64,
    pub expires_at: u64,
    /// The last verify: connected, its message, when (ms).
    pub connection: Option<(bool, Text, u64)>,
}

/// A session Swarm's search found (POST /v3/sessions:search): active or
/// archived, with the first matching snippet.
#[derive(Clone, Debug)]
pub struct Found {
    pub id: Text,
    pub title: Text,
    pub project_id: Text,
    /// The project's name as this app knows it ("" when it does not).
    pub project: Text,
    pub archived: bool,
    pub updated_at: u64,
    pub snippet: Text,
}
