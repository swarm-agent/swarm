//! What the console shows: machines, projects, sessions, roles, media and
//! usage. `swarm::live` fills it from Swarm; the demo set below is for design
//! screenshots (SWARM_CONSOLE_DEMO).

use gpui::SharedString;

pub type Text = SharedString;

/// A machine's state as its Swarm says it (GET /v3/box/summary `status`), or
/// Offline when it could not be read.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Health {
    /// "ok".
    Ok,
    /// "attention": something waits on a person or failed today.
    Attention,
    /// "degraded": a wanted sandbox could not start, or spending is over the limit.
    Degraded,
    /// It answered, without a status the console knows.
    Unknown,
    /// Not reached (or not read yet).
    Offline,
}

impl Health {
    pub fn label(self) -> &'static str {
        match self {
            Health::Ok => "ok",
            Health::Attention => "attention",
            Health::Degraded => "degraded",
            Health::Unknown => "status unknown",
            Health::Offline => "offline",
        }
    }
}

/// The installer's update state (the summary's `machine.update`).
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum UpdateState {
    /// The summary has no update state (no installer state on that machine).
    Unknown,
    /// Running the newest build of its channel.
    Current,
    /// A newer build is waiting (for checks, review, or was blocked).
    Behind { candidate: Text, reason: Text },
    /// A newer build is being built or the machine drains to switch to it.
    Updating { candidate: Text, reason: Text },
    /// The last update failed or was rolled back.
    Failed { reason: Text },
    /// Any other state, shown as Swarm names it.
    Other { state: Text, reason: Text },
}

impl UpdateState {
    /// Short words for a card: "current", "behind: 1a2b3c4d5", "updating", "update failed".
    pub fn short(&self) -> String {
        match self {
            UpdateState::Unknown => "update —".into(),
            UpdateState::Current => "current".into(),
            UpdateState::Behind { candidate, .. } => format!("behind: {candidate} waiting"),
            UpdateState::Updating { candidate, .. } if candidate.is_empty() => "updating".into(),
            UpdateState::Updating { candidate, .. } => format!("updating to {candidate}"),
            UpdateState::Failed { .. } => "update failed".into(),
            UpdateState::Other { state, .. } => format!("update {state}"),
        }
    }

    /// Why, as the installer wrote it ("" when it gave none).
    pub fn reason(&self) -> Text {
        match self {
            UpdateState::Behind { reason, .. } | UpdateState::Updating { reason, .. } | UpdateState::Failed { reason } | UpdateState::Other { reason, .. } => reason.clone(),
            UpdateState::Unknown | UpdateState::Current => "".into(),
        }
    }

    /// Wants a look: behind or failed.
    pub fn warns(&self) -> bool {
        matches!(self, UpdateState::Behind { .. } | UpdateState::Failed { .. })
    }
}

/// What a restart would interrupt now (the summary's `busy`).
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub struct Busy {
    pub sessions: u32,
    pub worker_runs: u32,
    pub reviews: u32,
    pub followups: u32,
    pub environment_builds: u32,
}

/// Today's worker runs (the summary's `runs`, today in UTC).
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub struct RunsToday {
    pub total: u32,
    pub succeeded: u32,
    pub failed: u32,
    pub running_now: u32,
}

/// One machine. Every `Option` is None when its Swarm did not say (shown as
/// "—"); nothing here is filled in by the console.
#[derive(Clone)]
pub struct Machine {
    pub name: Text,
    /// How the console reaches it: local, ssh, linked, or paired (through home).
    pub kind: Text,
    pub health: Health,
    /// Swarm's reasons for its status, or why it could not be read.
    pub reasons: Vec<Text>,
    pub busy: Option<Busy>,
    pub runs: Option<RunsToday>,
    pub cost_today: Option<f64>,
    /// Version and short commit.
    pub build: Option<Text>,
    pub update: UpdateState,
    /// The session the console (or home) holds on it.
    pub access: Text,
    pub sandbox: Option<Text>,
    pub uptime: Option<Text>,
    /// When home last reached it (ms), from GET /v3/fleet; linked machines only.
    pub last_contact: Option<u64>,
    /// Its model per role key (GET /v1/agent-model-settings); None when not read.
    pub models: Option<Vec<(Text, Assignment)>>,
    /// The installer's update setting (`machine.update`); None when it keeps none.
    pub updates: Option<Updates>,
    /// Whether it drains for an update (`machine.drain`); None when not said.
    pub drain: Option<Drain>,
    /// Where a fleet machine's Swarm answers (empty for home and the demo).
    pub gateway: Text,
}

/// The installer's update setting and when it last wrote its state (ms).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Updates {
    pub auto: Text,
    pub channel: Text,
    pub at: u64,
}

/// A drain for an update: new work waits while running work finishes.
/// Times are Unix ms, 0 when that Swarm does not give them.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Drain {
    pub draining: bool,
    pub since: u64,
    pub deadline: u64,
    pub timed_out: bool,
    pub blocking_runs: u32,
}

/// "just now", "4m", "3h", "2d" for a span of ms.
fn span(ms: u64) -> String {
    let m = ms / 60_000;
    match m {
        0 => "under a minute".into(),
        m if m < 60 => format!("{m}m"),
        m if m < 60 * 24 => format!("{}h", m / 60),
        m => format!("{}d", m / (60 * 24)),
    }
}

const UNKNOWN: &str = "—";

impl Machine {
    /// A machine nothing has been read about yet.
    pub fn unread(name: Text, kind: Text, access: Text) -> Machine {
        Machine {
            name,
            kind,
            health: Health::Offline,
            reasons: vec![],
            busy: None,
            runs: None,
            cost_today: None,
            build: None,
            update: UpdateState::Unknown,
            access,
            sandbox: None,
            uptime: None,
            last_contact: None,
            models: None,
            updates: None,
            drain: None,
            gateway: "".into(),
        }
    }

    /// "dev 67c69d05d · current".
    pub fn build_line(&self) -> String {
        format!("{} · {}", self.build.as_ref().map(|b| b.to_string()).unwrap_or_else(|| UNKNOWN.into()), self.update.short())
    }

    /// What runs now: "2 sessions · 1 worker run", "idle", or "—".
    pub fn now_line(&self) -> String {
        let Some(b) = self.busy else { return UNKNOWN.into() };
        let mut parts = Vec::new();
        let mut add = |n: u32, one: &str, many: &str| {
            if n > 0 {
                parts.push(format!("{n} {}", if n == 1 { one } else { many }));
            }
        };
        add(b.sessions, "session", "sessions");
        add(b.worker_runs, "worker run", "worker runs");
        add(b.reviews, "review", "reviews");
        add(b.followups, "follow-up", "follow-ups");
        add(b.environment_builds, "environment build", "environment builds");
        if parts.is_empty() { "idle".into() } else { parts.join(" · ") }
    }

    /// Today's worker runs: "4 runs · 3 ok · 1 failed", or "—".
    pub fn runs_line(&self) -> String {
        let Some(r) = self.runs else { return UNKNOWN.into() };
        let mut out = format!("{} run{}", r.total, if r.total == 1 { "" } else { "s" });
        if r.total > 0 {
            out.push_str(&format!(" · {} ok", r.succeeded));
        }
        if r.failed > 0 {
            out.push_str(&format!(" · {} failed", r.failed));
        }
        out
    }

    /// Today's spend: "$0.50", or "—".
    pub fn cost_line(&self) -> String {
        self.cost_today.map(|c| format!("${c:.2}")).unwrap_or_else(|| UNKNOWN.into())
    }

    pub fn uptime_line(&self) -> String {
        self.uptime.as_ref().map(|u| u.to_string()).unwrap_or_else(|| UNKNOWN.into())
    }

    pub fn sandbox_line(&self) -> String {
        self.sandbox.as_ref().map(|s| s.to_string()).unwrap_or_else(|| UNKNOWN.into())
    }

    /// "automatic · dev · state written 4m ago", or "—".
    pub fn updates_line(&self, now: u64) -> String {
        let Some(u) = &self.updates else { return UNKNOWN.into() };
        let mode = match u.auto.as_ref() {
            "on" => "automatic".to_string(),
            "off" => "manual".to_string(),
            "" => "update mode —".to_string(),
            other => format!("auto {other}"),
        };
        let mut parts = vec![mode];
        if !u.channel.is_empty() {
            parts.push(format!("channel {}", u.channel));
        }
        if u.at > 0 {
            parts.push(format!("state written {} ago", span(now.saturating_sub(u.at))));
        }
        parts.join(" · ")
    }

    /// "not draining", what a drain waits for and until when, or "—".
    pub fn drain_line(&self, now: u64) -> String {
        let Some(d) = self.drain else { return UNKNOWN.into() };
        if !d.draining {
            return "not draining".into();
        }
        let runs = match d.blocking_runs {
            0 => "nothing left running".to_string(),
            1 => "1 run to finish".to_string(),
            n => format!("{n} runs to finish"),
        };
        let since = if d.since > 0 { format!(" for {}", span(now.saturating_sub(d.since))) } else { String::new() };
        let end = match (d.timed_out, d.deadline) {
            (true, _) => " · its deadline passed".to_string(),
            (false, 0) => String::new(),
            (false, at) if at <= now => " · deadline reached".to_string(),
            (false, at) => format!(" · deadline in {}", span(at - now)),
        };
        format!("draining{since}: new work waits, {runs}{end}")
    }

    /// When home last reached it, against `now` (ms): "last contact 4m ago",
    /// "never reached", or "" when that is not known (home and this machine).
    pub fn contact_line(&self, now: u64) -> String {
        if self.kind.as_ref() != "paired" {
            return String::new();
        }
        match self.last_contact {
            Some(at) if at > 0 => {
                let m = now.saturating_sub(at) / 60_000;
                let ago = match m {
                    0 => "just now".to_string(),
                    m if m < 60 => format!("{m}m ago"),
                    m if m < 60 * 24 => format!("{}h ago", m / 60),
                    m => format!("{}d ago", m / (60 * 24)),
                };
                format!("last contact {ago}")
            }
            _ => "never reached".into(),
        }
    }

    /// Its setting for a role key, as its Swarm said: None when not read or not listed.
    pub fn model(&self, key: &str) -> Option<&Assignment> {
        self.models.as_ref()?.iter().find(|(k, _)| k.as_ref() == key).map(|(_, a)| a)
    }
}

/// Today's spend over machines: the sum of what is known, and whether any
/// machine's spend is unknown.
pub fn spend_today(machines: &[Machine]) -> (f64, bool) {
    let known: f64 = machines.iter().filter_map(|m| m.cost_today).sum();
    (known, machines.iter().any(|m| m.cost_today.is_none()))
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum RunState {
    Running,
    NeedsYou,
    /// A run left asleep by the retired pending `wait_tasks` (run intent
    /// status `waiting_tasks`). Swarm converts these to task watches at
    /// startup; a watch itself never puts a conversation in this state.
    Waiting,
    Idle,
    Failed,
}

/// What a conversation watches: its active task watches (`task_watches` in
/// Swarm's run state; watching never makes it pending), or the task wait of
/// a run left asleep by the retired pending `wait_tasks`.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct TaskWait {
    /// The waiting run of a retired pending wait (stop sends it); empty for
    /// task watches, which no run holds.
    pub run_id: Text,
    pub project_id: Text,
    /// The linked machine the tasks run on ("" for this machine).
    pub machine: Text,
    /// (task id, its title once known).
    pub tasks: Vec<(Text, Text)>,
}

impl TaskWait {
    /// One line: "watching tasks: Fix the build, Add docs · on forge".
    pub fn line(&self) -> String {
        const SHOWN: usize = 3;
        fn shown(task: &(Text, Text)) -> &str {
            let (id, title) = task;
            if title.is_empty() { id.as_ref() } else { title.as_ref() }
        }
        let names: Vec<&str> = self.tasks.iter().take(SHOWN).map(shown).collect();
        let mut line = if names.is_empty() { "watching tasks".to_string() } else { format!("watching tasks: {}", names.join(", ")) };
        if self.tasks.len() > SHOWN {
            line.push_str(&format!(" +{} more", self.tasks.len() - SHOWN));
        }
        if !self.machine.is_empty() {
            line.push_str(&format!(" · on {}", self.machine));
        }
        line
    }

    /// The composer's badge: "👁 Watching · 2 tasks on forge".
    pub fn badge(&self) -> String {
        let n = self.tasks.len();
        let mut badge = match n {
            0 => "👁 Watching".to_string(),
            1 => "👁 Watching · 1 task".to_string(),
            n => format!("👁 Watching · {n} tasks"),
        };
        if !self.machine.is_empty() {
            badge.push_str(&format!(" on {}", self.machine));
        }
        badge
    }

    /// The badge on a narrow pane: "👁 2" (the full text is its tooltip).
    pub fn short_badge(&self) -> String {
        match self.tasks.len() {
            0 => "👁".to_string(),
            n => format!("👁 {n}"),
        }
    }
}

/// The composer's badge beside send/stop while the conversation watches
/// tasks, whether it works or is idle: "👁 Watching · 2 tasks on forge".
/// None with no watch: while it works the stop button already says so, and
/// idle needs no badge. Sending stays enabled: a message never ends a watch.
pub fn composer_badge(state: RunState, wait: Option<&TaskWait>) -> Option<String> {
    watch_shown(state, wait).map(|w| w.map(TaskWait::badge).unwrap_or_else(|| "👁 Watching".to_string()))
}

/// The badge's short form for a narrow pane ("👁 2"), when there is a badge.
pub fn composer_badge_short(state: RunState, wait: Option<&TaskWait>) -> Option<String> {
    watch_shown(state, wait).map(|w| w.map(TaskWait::short_badge).unwrap_or_else(|| "👁".to_string()))
}

/// Whether the composer shows a watch badge, and the watch it names: a watch
/// with tasks in any state, or a run left by the retired pending wait.
fn watch_shown(state: RunState, wait: Option<&TaskWait>) -> Option<Option<&TaskWait>> {
    match wait.filter(|w| !w.tasks.is_empty() || state == RunState::Waiting) {
        Some(w) => Some(Some(w)),
        None if state == RunState::Waiting => Some(None),
        None => None,
    }
}

/// The widths (px) of what sits in the composer's bottom row; an empty hint
/// or no badge is 0. `model` is the model button at full width (it can
/// truncate, so it never pushes send/stop out).
#[derive(Clone, Copy, Debug, Default)]
pub struct ComposerRow {
    pub model: f32,
    pub permissions: f32,
    pub hint: f32,
    pub commands: f32,
    pub badge: f32,
    pub badge_short: f32,
    /// The agents-at-work count ("● 3 agents · forge 2 · pc 1") and its
    /// short form ("● 3"); 0 before Swarm's first answer.
    pub agents: f32,
    pub agents_short: f32,
    /// Send/stop with their labels ("■ stop wait", "⏎ send").
    pub actions: f32,
    /// Send/stop as bare symbols ("■", "⏎").
    pub actions_compact: f32,
    /// Between two items.
    pub gap: f32,
}

/// What of the composer's bottom row shows at a width. Send/stop always
/// shows; `compact` only shortens its labels.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct ComposerFit {
    pub commands: bool,
    /// The agents count as "● 3" instead of its full text; it is never
    /// dropped.
    pub agents_short: bool,
    pub hint: bool,
    /// The badge as "👁 2" instead of its full text.
    pub badge_short: bool,
    pub permissions: bool,
    /// Send/stop as "■" / "⏎" (their names on hover).
    pub compact: bool,
}

impl ComposerFit {
    /// Everything, at full length.
    pub const ALL: ComposerFit = ComposerFit { commands: true, agents_short: false, hint: true, badge_short: false, permissions: true, compact: false };
}

/// Fits the composer's bottom row in `width`: as it narrows, "/ commands"
/// goes first, then the agents count shortens to "● 3", then the hint goes,
/// then the badge shortens, then the permissions chip goes, then send/stop
/// shows only its symbols. Send/stop and the agents count are never
/// dropped; below the last step only the model label truncates, and the
/// row's layout keeps send/stop outside the part that clips.
pub fn composer_fit(width: f32, row: ComposerRow) -> ComposerFit {
    let item = |w: f32| if w > 0. { w + row.gap } else { 0. };
    let need = |fit: ComposerFit| {
        item(row.model)
            + if fit.permissions { item(row.permissions) } else { 0. }
            + if fit.commands { item(row.commands) } else { 0. }
            + if fit.hint { item(row.hint) } else { 0. }
            + item(if fit.agents_short { row.agents_short } else { row.agents })
            + item(if fit.badge_short { row.badge_short } else { row.badge })
            + if fit.compact { row.actions_compact } else { row.actions }
    };
    let all = ComposerFit::ALL;
    let no_commands = ComposerFit { commands: false, ..all };
    let agents_short = ComposerFit { agents_short: true, ..no_commands };
    let no_hint = ComposerFit { hint: false, ..agents_short };
    let short = ComposerFit { badge_short: true, ..no_hint };
    let no_permissions = ComposerFit { permissions: false, ..short };
    let compact = ComposerFit { compact: true, ..no_permissions };
    [all, no_commands, agents_short, no_hint, short, no_permissions].into_iter().find(|fit| need(*fit) <= width).unwrap_or(compact)
}

/// About how wide a line of the console's monospaced text is, in px: emoji
/// and other wide symbols take two cells. Enough to decide what fits.
pub fn text_width(text: &str, font_px: f32) -> f32 {
    let cells: usize = text
        .chars()
        .map(|c| match c as u32 {
            0x2300..=0x23FF | 0x2600..=0x27BF | 0x1F000.. => 2,
            _ => 1,
        })
        .sum();
    cells as f32 * font_px * 0.6
}

/// The composer's hint text. Idle shows none; the badge carries the run's
/// state and its task watches. A message never ends a watch.
pub fn composer_hint(demo: bool, live: bool, question: bool, state: RunState) -> &'static str {
    match (live, demo) {
        (_, true) => "demo data",
        (false, false) => "starts a session in this project",
        (true, false) if question => "your message answers the question above",
        (true, false) if state == RunState::Running => "your message is queued for its next step",
        (true, false) if state == RunState::Waiting => "send anytime; Swarm watches the tasks",
        (true, false) => "",
    }
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

    /// Only a task held before it starts, when Start tasks is set to ask.
    /// Review and questions are the session's AI's to handle, not the person's.
    pub fn needs_you(self) -> bool {
        matches!(self, TaskStatus::PendingApproval)
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
    /// Where the task is and what happened to it, as Swarm tracks it
    /// (docs/task-progress.md).
    pub progress: Progress,
}

/// A task's tracked state (`progress` on Swarm's task record).
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct Progress {
    pub state: ProgressState,
    /// The task's state as Swarm derives it (`progress.task_state`,
    /// docs/task-progress.md): what rows and badges show. The task's status
    /// is only its run's status.
    pub task_state: TaskState,
    /// Swarm's one-line reason for `task_state` (`progress.task_state_reason`).
    pub task_state_reason: Text,
    /// One short line, as Swarm words it.
    pub summary: Text,
    /// What happened, oldest first: (Unix ms, what).
    pub history: Vec<(u64, Text)>,
    pub updated_at: u64,
}

/// A task's state, one word Swarm derives from its run status and outcome
/// (pebblestore.ProjectTaskState): the same word its MCP views and the
/// Orchestrator see.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum TaskState {
    Running,
    Waiting,
    NeedsYou,
    Landed,
    Done,
    NotIntegrated,
    Stopped,
    /// A Swarm that derives no state, or a state this app does not know.
    #[default]
    Unknown,
}

impl TaskState {
    pub fn of(v: &str) -> TaskState {
        match v.trim() {
            "running" => TaskState::Running,
            "waiting" => TaskState::Waiting,
            "needs_you" => TaskState::NeedsYou,
            "landed" => TaskState::Landed,
            "done" => TaskState::Done,
            "not_integrated" => TaskState::NotIntegrated,
            "stopped" => TaskState::Stopped,
            _ => TaskState::Unknown,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            TaskState::Running => "running",
            TaskState::Waiting => "waiting",
            TaskState::NeedsYou => "needs you",
            TaskState::Landed => "landed",
            TaskState::Done => "done",
            TaskState::NotIntegrated => "not integrated",
            TaskState::Stopped => "stopped",
            TaskState::Unknown => "",
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug, Default)]
pub enum ProgressState {
    Waiting,
    Working,
    Workflow,
    NotIntegrated,
    Integrated,
    Delivered,
    Done,
    Failed,
    /// A Swarm that tracks no progress, or a state this app does not know.
    #[default]
    Unknown,
}

impl ProgressState {
    pub fn of(v: &str) -> ProgressState {
        match v {
            "waiting" => ProgressState::Waiting,
            "working" => ProgressState::Working,
            "workflow" => ProgressState::Workflow,
            "not_integrated" => ProgressState::NotIntegrated,
            "integrated" => ProgressState::Integrated,
            "delivered" => ProgressState::Delivered,
            "done" => ProgressState::Done,
            "failed" => ProgressState::Failed,
            _ => ProgressState::Unknown,
        }
    }

    /// Finished one way or another: folded away in the session's tray.
    pub fn finished(self) -> bool {
        matches!(self, ProgressState::Integrated | ProgressState::Delivered | ProgressState::Done)
    }

    /// Order of open tasks in the tray: what went wrong first.
    pub fn rank(self) -> u8 {
        match self {
            ProgressState::Failed => 0,
            ProgressState::NotIntegrated => 1,
            ProgressState::Workflow => 2,
            ProgressState::Working => 3,
            ProgressState::Waiting => 4,
            ProgressState::Unknown => 5,
            ProgressState::Integrated | ProgressState::Delivered | ProgressState::Done => 6,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            ProgressState::Waiting => "waiting",
            ProgressState::Working => "working",
            ProgressState::Workflow => "workflow",
            ProgressState::NotIntegrated => "not integrated",
            ProgressState::Integrated => "integrated",
            ProgressState::Delivered => "delivered",
            ProgressState::Done => "done",
            ProgressState::Failed => "failed",
            ProgressState::Unknown => "",
        }
    }
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
    /// The person's message sent while a run was active: Swarm queued it for
    /// the run's next step. It becomes a `User` entry once Swarm records that
    /// it reached the model (a `feedback_delivery` receipt naming `id`).
    Queued {
        id: Text,
        text: Text,
    },
    Assistant(Text),
    /// A status line Swarm records for the person, not a turn: a task wait
    /// starting ("This conversation is now waiting on…") or ending.
    Notice(Text),
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
    /// Media this conversation asked for (manage_media generate or retry, or
    /// a task for the image, video or sound agent), drawn as a card under the
    /// call that started it. Its state and files are not kept here: they come
    /// from the media list (`media_card`), where each file's id is this task
    /// id, so the card follows the job and is the same after a reload.
    Media {
        task_id: Text,
        kind: MediaKind,
        title: Text,
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
        let (name, detail, summary, time) = self.tool_parts(now_ms)?;
        Some(crate::swarm::tool_summary::line(name, detail, &summary, &time))
    }

    /// A tool call as it is drawn in a conversation: the name with its
    /// summary and time on one row, the detail under it. None for other
    /// entries.
    pub fn tool_stack(&self, now_ms: u64) -> Option<crate::swarm::tool_summary::Stacked> {
        let (name, detail, summary, time) = self.tool_parts(now_ms)?;
        Some(crate::swarm::tool_summary::stacked(name, detail, &summary, &time))
    }

    /// A tool call's name, detail, summary (or what its state says) and time.
    fn tool_parts(&self, now_ms: u64) -> Option<(&str, &str, String, String)> {
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
        Some((name.as_ref(), detail.as_ref(), summary, time))
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
    /// While the run is asleep on tasks: what it waits for.
    pub waiting_on: Option<TaskWait>,
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
        demo_machine("studio", "local", Health::Ok, Busy { sessions: 3, ..Default::default() }, 4.12, "dev 67c69d05d", UpdateState::Current, "gVisor", "3d 4h", None),
        demo_machine(
            "forge",
            "paired",
            Health::Attention,
            Busy { worker_runs: 2, ..Default::default() },
            1.37,
            "dev 5a667f28a",
            UpdateState::Behind { candidate: "3ce146eae".into(), reason: "waiting for its checks".into() },
            "gVisor",
            "11d 2h",
            Some(("coder", a("openai".into(), "gpt-5.4".into(), "high".into()))),
        ),
        demo_machine("edge-1", "paired", Health::Ok, Busy::default(), 0.21, "v0.1.56 965ba5a3c", UpdateState::Current, "bubblewrap", "26d 1h", None),
        Machine { last_contact: Some(now_ms().saturating_sub(42 * 60_000)), ..Machine::unread("lab".into(), "paired".into(), "owner".into()) },
    ]
}

#[allow(clippy::too_many_arguments)]
fn demo_machine(name: &str, kind: &str, health: Health, busy: Busy, cost: f64, build: &str, update: UpdateState, sandbox: &str, uptime: &str, differs: Option<(&str, Assignment)>) -> Machine {
    let models = roles()
        .into_iter()
        .map(|r| match &differs {
            Some((key, a)) if r.key.as_ref() == *key => (r.key, a.clone()),
            _ => (r.key, r.default),
        })
        .collect();
    Machine {
        health,
        reasons: if health == Health::Attention { vec!["workers failed today".into()] } else { vec![] },
        busy: Some(busy),
        runs: Some(RunsToday { total: 6, succeeded: 5, failed: u32::from(health == Health::Attention), running_now: busy.worker_runs }),
        cost_today: Some(cost),
        build: Some(build.to_string().into()),
        update,
        sandbox: Some(sandbox.to_string().into()),
        uptime: Some(uptime.to_string().into()),
        last_contact: (kind == "paired").then(now_ms),
        models: Some(models),
        ..Machine::unread(name.to_string().into(), kind.to_string().into(), "owner".into())
    }
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
            waiting_on: None,
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
                Task { key: "".into(), session: "".into(), title: "Reopen step for blocked deliveries".into(), role: "coder".into(), machine: 0, status: TaskStatus::Running, activity: "editing automation/reopen.go · step 3 of 5".into(), report: "".into(), progress: Default::default(), runs: vec![run("coder".into(), 0, RunState::Running, "attempt 2".into(), "now".into(), 0.41), run("coder".into(), 0, RunState::Failed, "attempt 1 · tests failed".into(), "9m".into(), 0.21)] },
                Task { key: "".into(), session: "".into(), title: "Round limits on automation steps".into(), role: "coder".into(), machine: 0, status: TaskStatus::PendingApproval, activity: "waiting for your approval · changes a stored schema".into(), report: "".into(), progress: Default::default(), runs: vec![] },
                Task { key: "".into(), session: "".into(), title: "Document conditions and rounds".into(), role: "swarm".into(), machine: 0, status: TaskStatus::NeedsReview, activity: "ready to merge · 2 commits".into(), report: "".into(), progress: Default::default(), runs: vec![run("swarm".into(), 0, RunState::Idle, "docs pass".into(), "4m".into(), 0.22), run("finder".into(), 0, RunState::Idle, "link check".into(), "3m".into(), 0.09)] },
                Task { key: "".into(), session: "".into(), title: "Integration test sweep".into(), role: "coder".into(), machine: 1, status: TaskStatus::Running, activity: "go test ./... · 212 of 640 packages".into(), report: "".into(), progress: Default::default(), runs: vec![run("coder".into(), 1, RunState::Running, "test sweep".into(), "now".into(), 0.48)] },
                Task { key: "".into(), session: "".into(), title: "Independent review of T-418".into(), role: "reviewer".into(), machine: 1, status: TaskStatus::Queued, activity: "starts when T-418 lands".into(), report: "".into(), progress: Default::default(), runs: vec![] },
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
            waiting_on: None,
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
                Task { key: "".into(), session: "".into(), title: "Wait on the mount table before cleanup".into(), role: "coder".into(), machine: 0, status: TaskStatus::Running, activity: "go test -run Scratch -count 50 · 31 passed".into(), report: "".into(), progress: Default::default(), runs: vec![run("finder".into(), 0, RunState::Idle, "reproduce".into(), "5m".into(), 0.05), run("coder".into(), 0, RunState::Running, "fix".into(), "now".into(), 0.16)] },
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
            waiting_on: None,
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
            waiting_on: None,
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
                Task { key: "".into(), session: "".into(), title: "Hero directions ×3".into(), role: "designer".into(), machine: 1, status: TaskStatus::Running, activity: "rendering direction 3 of 3".into(), report: "".into(), progress: Default::default(), runs: vec![run("designer".into(), 1, RunState::Running, "directions".into(), "now".into(), 0.55)] },
                Task { key: "".into(), session: "".into(), title: "Lighthouse pass".into(), role: "finder".into(), machine: 2, status: TaskStatus::NeedsInput, activity: "asks: which pages should it audit?".into(), report: "".into(), progress: Default::default(), runs: vec![run("finder".into(), 2, RunState::NeedsYou, "lighthouse".into(), "2m".into(), 0.0)] },
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
            waiting_on: None,
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
            waiting_on: None,
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
                Task { key: "".into(), session: "".into(), title: "Bump ws to 8.21.3".into(), role: "coder".into(), machine: 2, status: TaskStatus::Completed, activity: "merged · 1 commit".into(), report: "".into(), progress: Default::default(), runs: vec![run("coder".into(), 2, RunState::Idle, "bump".into(), "2h".into(), 0.09)] },
                Task { key: "".into(), session: "".into(), title: "Advisory feed check".into(), role: "finder".into(), machine: 2, status: TaskStatus::Failed, activity: "advisory feed timed out".into(), report: "".into(), progress: Default::default(), runs: vec![run("finder".into(), 2, RunState::Failed, "check".into(), "2h".into(), 0.01)] },
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

/// One role on one machine, against the first machine's setting (the
/// Swarm this console is connected to).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Cell {
    /// The same model as the default column.
    InSync,
    /// Its Swarm runs another model for this role.
    Custom(Assignment),
    /// Not read: the machine was not reached or did not list the role.
    Unknown,
}

#[derive(Clone)]
pub struct Role {
    pub key: Text,
    pub label: Text,
    pub purpose: Text,
    /// The setting on the Swarm this console is connected to.
    pub default: Assignment,
}

/// A role on a machine: the first machine is the default itself; any other
/// is compared with it from what its own Swarm said.
pub fn cell(role: &Role, machines: &[Machine], mi: usize) -> Cell {
    if mi == 0 {
        return Cell::InSync;
    }
    match machines.get(mi).and_then(|m| m.model(&role.key)) {
        Some(a) if *a == role.default => Cell::InSync,
        Some(a) => Cell::Custom(a.clone()),
        None => Cell::Unknown,
    }
}

fn a(provider: Text, model: Text, thinking: Text) -> Assignment {
    Assignment { provider, model, thinking, tier: "".into() }
}

pub fn roles() -> Vec<Role> {
    vec![
        Role { key: "plan".into(), label: "Orchestrator".into(), purpose: "runs your sessions, delegates".into(), default: a("anthropic".into(), "opus-5.5".into(), "high".into()) },
        Role { key: "action".into(), label: "Swarm".into(), purpose: "big tasks, coordinates children".into(), default: a("anthropic".into(), "opus-5.5".into(), "medium".into()) },
        Role { key: "coder".into(), label: "Coder".into(), purpose: "edits code in a worktree".into(), default: a("openai".into(), "gpt-5.5".into(), "high".into()) },
        Role { key: "finder".into(), label: "Finder".into(), purpose: "searches, reads, reports".into(), default: a("anthropic".into(), "sonnet-5.5".into(), "".into()) },
        Role { key: "designer".into(), label: "Designer".into(), purpose: "UI, HTML designs, images".into(), default: a("google".into(), "gemini-3.5-pro".into(), "".into()) },
        Role { key: "reviewer".into(), label: "Reviewer".into(), purpose: "independent read-only review".into(), default: a("anthropic".into(), "opus-5.5".into(), "high".into()) },
        Role { key: "compact".into(), label: "Compact".into(), purpose: "summarises long sessions".into(), default: a("anthropic".into(), "haiku-5.5".into(), "".into()) },
        Role { key: "router".into(), label: "Router".into(), purpose: "titles and small routing".into(), default: a("anthropic".into(), "haiku-5.5".into(), "".into()) },
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
    /// The task ended without a file: kept only so its conversation card can
    /// say so (`meta` holds the reason); the Media page leaves it out.
    pub failed: bool,
}

/// Where a conversation's media card stands.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum MediaCardState {
    Generating,
    Ready,
    Failed,
}

impl MediaCardState {
    pub fn label(self) -> &'static str {
        match self {
            MediaCardState::Generating => "generating",
            MediaCardState::Ready => "ready",
            MediaCardState::Failed => "failed",
        }
    }
}

/// A media card as the media list has it now: its state and the places in
/// that list of what it shows (the files once ready; the job while it is
/// made or after it failed, when the list has it).
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct MediaCard {
    pub state: MediaCardState,
    pub items: Vec<usize>,
}

impl MediaCard {
    /// What clicking its `n`th tile opens: a place in the media list, the
    /// one the preview overlay shows.
    pub fn preview(&self, n: usize) -> Option<usize> {
        self.items.get(n).copied()
    }
}

/// The card of the media task `task_id` (`Entry::Media`): ready with its files
/// once any is there, failed when the task ended without one, else generating
/// (also before the media list has read the task at all).
pub fn media_card(media: &[Media], task_id: &str) -> MediaCard {
    let mine: Vec<usize> = media
        .iter()
        .enumerate()
        .filter(|(_, m)| !task_id.is_empty() && (m.id.as_ref() == task_id || m.id.strip_prefix(task_id).is_some_and(|rest| rest.starts_with('#'))))
        .map(|(i, _)| i)
        .collect();
    let ready: Vec<usize> = mine.iter().copied().filter(|i| !media[*i].pending && !media[*i].failed).collect();
    if !ready.is_empty() {
        return MediaCard { state: MediaCardState::Ready, items: ready };
    }
    if let Some(i) = mine.iter().copied().find(|i| media[*i].failed) {
        return MediaCard { state: MediaCardState::Failed, items: vec![i] };
    }
    MediaCard { state: MediaCardState::Generating, items: mine }
}

pub fn media() -> Vec<Media> {
    vec![
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Hero direction 1 · eclipse".into(), kind: MediaKind::Design, project: 1, machine: 1, model: "gemini-3.5-pro".into(), meta: "hero.html · 1440×900".into(), age: "31m".into(), hue: 0.72, pending: false, failed: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Hero direction 2 · swarm field".into(), kind: MediaKind::Design, project: 1, machine: 1, model: "gemini-3.5-pro".into(), meta: "hero.html · 1440×900".into(), age: "22m".into(), hue: 0.58, pending: false, failed: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Hero direction 3".into(), kind: MediaKind::Design, project: 1, machine: 1, model: "gemini-3.5-pro".into(), meta: "rendering".into(), age: "now".into(), hue: 0.66, pending: true, failed: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: Some(0), file: None, dims: None, title: "Launch teaser".into(), kind: MediaKind::Video, project: 1, machine: 0, model: "veo-4".into(), meta: "12s · 1080p · sound".into(), age: "2h".into(), hue: 0.04, pending: false, failed: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: Some(0), file: None, dims: None, title: "Logo mark, dark".into(), kind: MediaKind::Image, project: 1, machine: 0, model: "imagen-5".into(), meta: "2048² · png".into(), age: "1d".into(), hue: 0.0, pending: false, failed: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Notification chime".into(), kind: MediaKind::Sound, project: 2, machine: 2, model: "lyria-3".into(), meta: "1.4s · wav".into(), age: "2d".into(), hue: 0.36, pending: false, failed: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Fleet diagram".into(), kind: MediaKind::Image, project: 0, machine: 0, model: "imagen-5".into(), meta: "1600×900 · png".into(), age: "3d".into(), hue: 0.52, pending: false, failed: false },
        Media { id: "".into(), session_id: "".into(), thumb: None, session: None, file: None, dims: None, title: "Onboarding walkthrough".into(), kind: MediaKind::Video, project: 2, machine: 1, model: "veo-4".into(), meta: "48s · 1080p".into(), age: "4d".into(), hue: 0.12, pending: false, failed: false },
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

    // Purpose: the composer says Watching at a glance and nothing redundant.
    // Requirement: a conversation watching tasks badges their count (and
    // machine when linked), working or idle; with no watch a running run has
    // no badge (its stop button already says it is working) and idle and
    // failed have none; the short badge is "👁 N"; the
    // idle hint is empty (no filler text) while the others still say what a
    // message does. Threat: the waiting state hiding in faint hint text, the
    // redundant "● Working" badge returning, or filler hints returning. Pure
    // functions are the narrowest layer for the text; the GPUI row only places it.
    #[test]
    fn composer_badge_and_hint_name_the_run_state() {
        let wait = TaskWait { project_id: "p".into(), machine: "forge".into(), tasks: vec![("t1".into(), "Fix login".into()), ("t2".into(), "".into())], ..Default::default() };
        assert_eq!(composer_badge(RunState::Waiting, Some(&wait)).as_deref(), Some("👁 Watching · 2 tasks on forge"));
        assert_eq!(composer_badge(RunState::Idle, Some(&wait)).as_deref(), Some("👁 Watching · 2 tasks on forge"));
        assert_eq!(composer_badge(RunState::Running, Some(&wait)).as_deref(), Some("● Working · 👁 Watching · 2 tasks on forge"));
        let here = TaskWait { tasks: vec![("t1".into(), "".into())], ..Default::default() };
        assert_eq!(composer_badge(RunState::Waiting, Some(&here)).as_deref(), Some("👁 Watching · 1 task"));
        assert_eq!(composer_badge(RunState::Waiting, None).as_deref(), Some("👁 Watching"));
        assert_eq!(composer_badge(RunState::Running, Some(&wait)).as_deref(), Some("👁 Watching · 2 tasks on forge"));
        assert_eq!(composer_badge(RunState::Running, None), None);
        assert_eq!(composer_badge(RunState::Idle, None), None);
        assert_eq!(composer_badge(RunState::Failed, None), None);
        assert_eq!(composer_badge_short(RunState::Waiting, Some(&wait)).as_deref(), Some("👁 2"));
        assert_eq!(composer_badge_short(RunState::Idle, Some(&wait)).as_deref(), Some("👁 2"));
        assert_eq!(composer_badge_short(RunState::Waiting, None).as_deref(), Some("👁"));
        assert_eq!(composer_badge_short(RunState::Running, None), None);
        assert_eq!(composer_badge_short(RunState::Idle, None), None);
        assert_eq!(composer_hint(false, true, false, RunState::Idle), "");
        assert_eq!(composer_hint(false, true, false, RunState::Waiting), "send anytime; Swarm watches the tasks");
        assert_eq!(composer_hint(false, true, false, RunState::Running), "your message is queued for its next step");
        assert_eq!(composer_hint(false, true, true, RunState::Idle), "your message answers the question above");
        assert_eq!(composer_hint(false, false, false, RunState::Idle), "starts a session in this project");
        assert_eq!(composer_hint(true, true, false, RunState::Idle), "demo data");
    }

    // Purpose: the composer's bottom row never overflows a narrow pane.
    // Requirement: as the width shrinks, "/ commands" goes first, then the
    // hint, then the badge shortens, then the permissions chip goes, then
    // send/stop shortens to its symbols; send/stop is never dropped, and every
    // step is the widest that fits. Below every step (down to 0px) the fit is
    // the compact one, whose width is model + badge + compact send/stop; the
    // model label is the only shrinkable item, so send/stop still fits.
    // Threat: send/stop clipped off a narrow pane (panes shrink to 12% of a
    // split), or items dropped out of order. `composer_fit` is the pure rule
    // the row renders from, the narrowest layer that decides it.
    #[test]
    fn composer_fit_drops_commands_then_hint_then_shortens_badge() {
        let row = ComposerRow { model: 80., permissions: 90., hint: 150., commands: 60., badge: 120., badge_short: 30., agents: 0., agents_short: 0., actions: 140., actions_compact: 40., gap: 10. };
        let all = ComposerFit::ALL;
        let no_commands = ComposerFit { commands: false, ..all };
        // No agents count yet (Swarm has not answered): its step changes nothing.
        let agents_short = ComposerFit { agents_short: true, ..no_commands };
        let no_hint = ComposerFit { hint: false, ..agents_short };
        let short = ComposerFit { badge_short: true, ..no_hint };
        let no_permissions = ComposerFit { permissions: false, ..short };
        let compact = ComposerFit { compact: true, ..no_permissions };
        // Everything: 90 + 100 + 70 + 160 + 130 + 140 = 690.
        assert_eq!(composer_fit(690., row), all);
        assert_eq!(composer_fit(2000., row), all);
        assert_eq!(composer_fit(689., row), no_commands);
        // Without "/ commands": 620.
        assert_eq!(composer_fit(620., row), no_commands);
        assert_eq!(composer_fit(619., row), no_hint);
        // Without the hint: 460.
        assert_eq!(composer_fit(460., row), no_hint);
        assert_eq!(composer_fit(459., row), short);
        // Short badge: 370.
        assert_eq!(composer_fit(370., row), short);
        assert_eq!(composer_fit(369., row), no_permissions);
        // Without permissions: 270.
        assert_eq!(composer_fit(270., row), no_permissions);
        assert_eq!(composer_fit(269., row), compact);
        // Narrower than anything: compact send/stop, which is never dropped.
        assert_eq!(composer_fit(0., row), compact);
        // Narrowing never brings an item back or lengthens one.
        let mut last = all;
        for w in (0..=800).rev() {
            let fit = composer_fit(w as f32, row);
            let back = (fit.commands && !last.commands)
                || (!fit.agents_short && last.agents_short)
                || (fit.hint && !last.hint)
                || (!fit.badge_short && last.badge_short)
                || (fit.permissions && !last.permissions)
                || (!fit.compact && last.compact);
            assert!(!back, "{w}: {fit:?} after {last:?}");
            last = fit;
        }
        // No badge (idle or working): the hint stays as long as it fits, and
        // the permissions chip and labels as long as they fit after it.
        let idle = ComposerRow { badge: 0., badge_short: 0., ..row };
        assert_eq!(composer_fit(560., idle), all);
        assert_eq!(composer_fit(490., idle), no_commands);
        assert!(!composer_fit(489., idle).hint);
        assert!(composer_fit(330., idle).permissions);
        assert!(composer_fit(331., idle).permissions);
        assert!(!composer_fit(329., idle).permissions);
        assert!(!composer_fit(230., idle).compact);
        assert!(composer_fit(229., idle).compact);
        // Rough widths: wide symbols take two cells.
        assert!((text_width("ab", 10.) - 12.).abs() < 0.01);
        assert!((text_width("👁 2", 10.) - 24.).abs() < 0.01);
    }

    // Purpose: the agents count plugs into the same width rule: after
    // "/ commands" goes it shortens to "● 3", before the hint is dropped; it
    // is never dropped, and at any width the row's fixed items (model,
    // agents, send/stop) need no more than the compact fit, so the count
    // never pushes send/stop out. Threat: the new item crowding send/stop off
    // a narrow pane, or the hint dropped while the full count still shows.
    // Boundary: composer_fit, the pure rule the row renders from.
    #[test]
    fn composer_fit_shortens_the_agents_count_before_dropping_the_hint() {
        let row = ComposerRow { model: 80., permissions: 90., hint: 150., commands: 60., badge: 0., badge_short: 0., agents: 200., agents_short: 40., actions: 100., actions_compact: 40., gap: 10. };
        let all = ComposerFit::ALL;
        let no_commands = ComposerFit { commands: false, ..all };
        let agents_short = ComposerFit { agents_short: true, ..no_commands };
        let no_hint = ComposerFit { hint: false, ..agents_short };
        // Everything: 90 + 100 + 160 + 70 + 210 + 100 = 730.
        assert_eq!(composer_fit(730., row), all);
        assert_eq!(composer_fit(729., row), no_commands);
        // Without "/ commands": 660; the count shortens next (to 500)...
        assert_eq!(composer_fit(659., row), agents_short);
        assert!(composer_fit(659., row).hint, "the hint stays while the short count fits");
        assert_eq!(composer_fit(500., row), agents_short);
        // ...and only then does the hint go (340).
        assert_eq!(composer_fit(499., row), no_hint);
        // Narrower than anything: the short count stays with compact send/stop.
        let tiny = composer_fit(0., row);
        assert!(tiny.agents_short && tiny.compact && !tiny.hint && !tiny.commands);
        let mut last = all;
        for w in (0..=900).rev() {
            let fit = composer_fit(w as f32, row);
            assert!(!(!fit.agents_short && last.agents_short), "{w}: the count grew back");
            assert!(fit.hint || fit.agents_short, "{w}: the hint dropped while the full count shows");
            last = fit;
        }
    }
}

impl Session {
    /// How many of your messages in Swarm's record read `text`.
    pub fn sent_count(&self, text: &str) -> usize {
        self.entries.iter().filter(|e| matches!(e, Entry::User(u) | Entry::Queued { text: u, .. } if u.trim() == text.trim())).count()
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
