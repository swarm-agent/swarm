//! The agents at work across the fleet: the home's one read,
//! GET /v3/fleet/agents (swarmd/internal/api/fleet_agents.go), and what the
//! console shows from it: the composer's bottom-row count and the Live
//! page's "Agents at work" panel (cards, a header of counts, a workflow's
//! pipeline). The read is the truth; the fleet stream's tool events only
//! patch it between reads, and a run starting or ending asks for a new read.

use serde_json::Value;

use super::client::Client;
use super::live_stream::Activity;
use super::needs::Target;

pub const PATH: &str = "/v3/fleet/agents";
/// The tool calls a card keeps, as Swarm sends them (fleetAgentsTimeline).
pub const TIMELINE: usize = 12;

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Tool {
    /// read, edit, command, commit or other.
    pub kind: String,
    pub tool: String,
    pub target: String,
    pub at: u64,
    /// running, done or failed.
    pub state: String,
}

#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct Counts {
    pub reads: u64,
    pub edits: u64,
    pub commands: u64,
    pub commits: u64,
    pub other: u64,
    pub last_minute: u64,
}

/// A workflow's steps, in order.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Step {
    Code,
    Review,
    Fix,
    Push,
    Landed,
}

impl Step {
    pub const ALL: [Step; 5] = [Step::Code, Step::Review, Step::Fix, Step::Push, Step::Landed];

    fn parse(s: &str) -> Option<Step> {
        Some(match s {
            "code" => Step::Code,
            "review" => Step::Review,
            "fix" => Step::Fix,
            "push" => Step::Push,
            "landed" => Step::Landed,
            _ => return None,
        })
    }

    pub fn label(self) -> &'static str {
        match self {
            Step::Code => "Code",
            Step::Review => "Review",
            Step::Fix => "Fix",
            Step::Push => "Push",
            Step::Landed => "Landed",
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub struct Stage {
    pub step: Step,
    pub round: u64,
    pub workflow: String,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Agent {
    pub machine: String,
    pub agent: String,
    pub session_id: String,
    pub run_id: String,
    pub title: String,
    pub project_id: String,
    pub task_id: String,
    pub task_title: String,
    pub worker_name: String,
    pub started_at: u64,
    pub now: Option<Tool>,
    pub timeline: Vec<Tool>,
    pub counts: Counts,
    pub files: Vec<String>,
    pub files_total: u64,
    pub stage: Option<Stage>,
    pub partial: bool,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Finished {
    pub machine: String,
    pub project_id: String,
    pub task_id: String,
    pub session_id: String,
    pub title: String,
    /// Swarm's derived task state: landed, done, needs_you, not_integrated…
    pub state: String,
    pub reason: String,
    pub at: u64,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Machine {
    pub name: String,
    /// online, offline or not_shown; `reason` says why for the last two.
    pub state: String,
    pub reason: String,
    pub agents: u64,
    pub landed_today: u64,
    pub failed_today: u64,
}

#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct Totals {
    pub agents: u64,
    pub last_minute: u64,
    pub landed_today: u64,
    pub failed_today: u64,
}

/// One answer of GET /v3/fleet/agents.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct AgentsNow {
    pub home: String,
    pub at: u64,
    pub agents: Vec<Agent>,
    pub machines: Vec<Machine>,
    pub recent: Vec<Finished>,
    pub totals: Totals,
}

fn s(v: &Value, key: &str) -> String {
    v.get(key).and_then(Value::as_str).unwrap_or("").to_string()
}

fn n(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

fn list<'a>(v: &'a Value, key: &str) -> impl Iterator<Item = &'a Value> {
    v.get(key).and_then(Value::as_array).into_iter().flatten()
}

fn tool(v: &Value) -> Tool {
    Tool { kind: s(v, "kind"), tool: s(v, "tool"), target: s(v, "target"), at: n(v, "at"), state: s(v, "state") }
}

/// Reads Swarm's answer; what is missing is empty, never invented.
pub fn parse(v: &Value) -> AgentsNow {
    let agents = list(v, "agents")
        .map(|a| {
            let c = a.get("counts").cloned().unwrap_or(Value::Null);
            Agent {
                machine: s(a, "machine"),
                agent: s(a, "agent"),
                session_id: s(a, "session_id"),
                run_id: s(a, "run_id"),
                title: s(a, "title"),
                project_id: s(a, "project_id"),
                task_id: s(a, "task_id"),
                task_title: s(a, "task_title"),
                worker_name: s(a, "worker_name"),
                started_at: n(a, "started_at"),
                now: a.get("now").filter(|t| t.is_object()).map(tool),
                timeline: list(a, "timeline").map(tool).collect(),
                counts: Counts { reads: n(&c, "reads"), edits: n(&c, "edits"), commands: n(&c, "commands"), commits: n(&c, "commits"), other: n(&c, "other"), last_minute: n(&c, "last_minute") },
                files: list(a, "files").filter_map(Value::as_str).map(str::to_string).collect(),
                files_total: n(a, "files_total"),
                stage: a.get("stage").and_then(|st| Some(Stage { step: Step::parse(st.get("step")?.as_str()?)?, round: n(st, "round"), workflow: s(st, "workflow") })),
                partial: a.get("partial").and_then(Value::as_bool).unwrap_or(false),
            }
        })
        .collect();
    let t = v.get("totals").cloned().unwrap_or(Value::Null);
    AgentsNow {
        home: s(v, "home"),
        at: n(v, "at"),
        agents,
        machines: list(v, "machines")
            .map(|m| Machine { name: s(m, "name"), state: s(m, "state"), reason: s(m, "reason"), agents: n(m, "agents"), landed_today: n(m, "landed_today"), failed_today: n(m, "failed_today") })
            .collect(),
        recent: list(v, "recent")
            .map(|f| Finished { machine: s(f, "machine"), project_id: s(f, "project_id"), task_id: s(f, "task_id"), session_id: s(f, "session_id"), title: s(f, "title"), state: s(f, "state"), reason: s(f, "reason"), at: n(f, "at") })
            .collect(),
        totals: Totals { agents: n(&t, "agents"), last_minute: n(&t, "tool_calls_last_minute"), landed_today: n(&t, "landed_today"), failed_today: n(&t, "failed_today") },
    }
}

/// Where "today" starts on this computer's clock (Unix ms), for `since`.
pub fn local_midnight(now_ms: u64, offset_secs: i64) -> u64 {
    let local = (now_ms / 1000) as i64 + offset_secs;
    let into_day = local.rem_euclid(86_400) as u64;
    now_ms.saturating_sub(into_day * 1000 + now_ms % 1000)
}

pub fn path(since_ms: u64) -> String {
    format!("{PATH}?since={since_ms}")
}

/// One read from the home. Blocks: run it on the background executor.
pub fn read(home: &Client, since_ms: u64) -> Result<AgentsNow, String> {
    home.get(&path(since_ms)).map(|v| parse(&v)).map_err(|e| e.to_string())
}

/// What a tool does to the work, the same rule Swarm applies
/// (fleetAgentToolKind).
pub fn tool_kind(name: &str) -> &'static str {
    match name.trim().to_ascii_lowercase().as_str() {
        "read" | "search" | "find" | "list" | "grep" | "glob" | "ls" | "webfetch" | "websearch" | "webdownload" => "read",
        "edit" | "write" | "multiedit" | "multi_edit" | "apply_patch" => "edit",
        "bash" | "shell" | "exec_command" => "command",
        "git_commit" => "commit",
        _ => "other",
    }
}

/// Whether a fleet stream record changes which runs are active (a new read
/// is due), as opposed to a tool call inside a known run.
pub fn moves_runs(kind: &str) -> bool {
    kind.starts_with("session.run.")
        || kind.starts_with("job.")
        || kind.starts_with("session.task.")
        || matches!(kind, "session.created" | "session.assistant.started" | "session.assistant.completed" | "session.lifecycle.updated" | "session.delivery.updated" | "project.updated" | "review.recorded")
}

/// Whether a home realtime frame (`kind`, and `event_type` for events)
/// changes which runs are active.
pub fn realtime_moves_runs(kind: &str, event_type: &str) -> bool {
    match kind {
        "task.lifecycle.updated" | "project.updated" | "worker.updated" | "session.lifecycle.updated" => true,
        "event" | "live.patch" => moves_runs(event_type),
        _ => false,
    }
}

impl AgentsNow {
    /// Patches a card with a tool event from the fleet stream until the
    /// next read. True when a new read is due: the record moves runs, or
    /// names a run this answer does not have.
    pub fn apply(&mut self, a: &Activity) -> bool {
        if moves_runs(&a.kind) {
            return true;
        }
        if !a.kind.starts_with("session.tool.") {
            return false;
        }
        let Some(card) = self.agents.iter_mut().find(|c| c.machine == a.machine && c.session_id == a.session_id) else {
            return !a.session_id.is_empty();
        };
        let kind = tool_kind(&a.tool_name);
        match a.kind.as_str() {
            "session.tool.started" => {
                let t = Tool { kind: kind.to_string(), tool: a.tool_name.clone(), target: a.file_path.clone(), at: a.at, state: "running".into() };
                match kind {
                    "read" => card.counts.reads += 1,
                    "edit" => {
                        card.counts.edits += 1;
                        if !a.file_path.is_empty() && !card.files.contains(&a.file_path) {
                            card.files.insert(0, a.file_path.clone());
                            card.files_total += 1;
                        }
                    }
                    "command" => card.counts.commands += 1,
                    "commit" => {}
                    _ => card.counts.other += 1,
                }
                card.counts.last_minute += 1;
                card.timeline.push(t.clone());
                if card.timeline.len() > TIMELINE {
                    card.timeline.remove(0);
                }
                card.now = Some(t);
            }
            ended => {
                let state = if ended == "session.tool.completed" { "done" } else { "failed" };
                if state == "done" && kind == "commit" {
                    card.counts.commits += 1;
                }
                if let Some(t) = card.timeline.iter_mut().rev().find(|t| t.state == "running" && t.tool == a.tool_name) {
                    t.state = state.into();
                }
                if let Some(now) = card.now.as_mut().filter(|t| t.state == "running" && t.tool == a.tool_name) {
                    now.state = state.into();
                }
            }
        }
        false
    }
}

// The composer's bottom row

/// The bottom row's count: full ("● 3 agents · forge 2 · pc 1"), short
/// ("● 3") and whether anything runs (dim when not).
#[derive(Clone, Debug, PartialEq)]
pub struct Indicator {
    pub full: String,
    pub short: String,
    pub active: bool,
    /// Machines that could not be read, for the tooltip.
    pub unread: Vec<String>,
}

pub fn indicator(now: &AgentsNow) -> Indicator {
    let total = now.totals.agents.max(now.agents.len() as u64);
    let unread: Vec<String> = now
        .machines
        .iter()
        .filter(|m| m.state != "online")
        .map(|m| if m.reason.is_empty() { format!("{}: {}", m.name, m.state.replace('_', " ")) } else { format!("{}: {} ({})", m.name, m.state.replace('_', " "), m.reason) })
        .collect();
    if total == 0 {
        return Indicator { full: "0 agents".into(), short: "0".into(), active: false, unread };
    }
    let mut full = format!("● {total} agent{}", if total == 1 { "" } else { "s" });
    // The split names each machine with something running, busiest first;
    // a fleet of one says nothing more.
    if now.machines.len() > 1 {
        let mut busy: Vec<&Machine> = now.machines.iter().filter(|m| m.agents > 0).collect();
        busy.sort_by(|a, b| b.agents.cmp(&a.agents).then(a.name.cmp(&b.name)));
        for m in busy {
            full.push_str(&format!(" · {} {}", if m.name.is_empty() { "here" } else { &m.name }, m.agents));
        }
    }
    Indicator { full, short: format!("● {total}"), active: true, unread }
}

// The panel

/// A card's elapsed time: "42s", "4m 05s", "1h 03m".
pub fn elapsed(ms: u64) -> String {
    let secs = ms / 1000;
    match secs {
        0..=59 => format!("{secs}s"),
        60..=3599 => format!("{}m {:02}s", secs / 60, secs % 60),
        _ => format!("{}h {:02}m", secs / 3600, (secs % 3600) / 60),
    }
}

/// What a tool call does, in a few words: "edit console/src/data.rs",
/// "bash: go test ./...".
pub fn doing(t: &Tool) -> String {
    let target = t.target.trim();
    match (t.kind.as_str(), target.is_empty()) {
        (_, true) => t.tool.clone(),
        ("command", false) => format!("{}: {target}", t.tool),
        ("commit", false) => format!("commit: {target}"),
        _ => format!("{} {target}", t.tool),
    }
}

fn plural(n: u64, one: &str) -> String {
    format!("{n} {one}{}", if n == 1 { "" } else { "s" })
}

pub fn counts_text(c: &Counts) -> String {
    format!("{} · {} · {} · {}", plural(c.reads, "read"), plural(c.edits, "edit"), plural(c.commands, "command"), plural(c.commits, "commit"))
}

/// One step of a pipeline strip.
#[derive(Clone, Debug, PartialEq)]
pub struct PipeStep {
    pub label: String,
    pub current: bool,
    pub done: bool,
}

/// Code → Review → Fix → Push → Landed, the stage's step current, the ones
/// before it done; Fix carries its round ("Fix · round 2").
pub fn pipeline(stage: &Stage) -> Vec<PipeStep> {
    let at = Step::ALL.iter().position(|s| *s == stage.step).unwrap_or(0);
    Step::ALL
        .iter()
        .enumerate()
        .map(|(i, step)| {
            let label = match step {
                Step::Fix if stage.round > 0 => format!("Fix · round {}", stage.round),
                _ => step.label().to_string(),
            };
            // A landed task did every step it needed.
            PipeStep { label, current: i == at, done: i < at || stage.step == Step::Landed }
        })
        .collect()
}

#[derive(Clone, Debug, PartialEq)]
pub struct Card {
    pub machine: String,
    pub agent: String,
    pub title: String,
    pub elapsed: String,
    pub doing: String,
    /// Oldest first: (kind, state).
    pub timeline: Vec<(String, String)>,
    pub counts: String,
    pub files: Vec<String>,
    /// Files edited beyond those listed.
    pub more_files: u64,
    pub pipeline: Option<Vec<PipeStep>>,
    pub partial: bool,
    /// Where a click goes.
    pub open: Target,
}

/// The header's four numbers, as shown.
#[derive(Clone, Debug, PartialEq)]
pub struct Header {
    pub working: String,
    pub per_minute: String,
    pub landed: String,
    pub failed: String,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Panel {
    Working { header: Header, cards: Vec<Card> },
    /// Nothing runs: the last finished jobs with Swarm's derived state.
    Quiet { header: Header, recent: Vec<Finished> },
}

pub fn header(now: &AgentsNow) -> Header {
    let t = &now.totals;
    let working = t.agents.max(now.agents.len() as u64);
    Header {
        working: format!("{working} agent{} working", if working == 1 { "" } else { "s" }),
        per_minute: format!("{} tool call{}/min", t.last_minute, if t.last_minute == 1 { "" } else { "s" }),
        landed: format!("{} landed today", t.landed_today),
        failed: format!("{} failed today", t.failed_today),
    }
}

pub fn card(a: &Agent, now_ms: u64) -> Card {
    let title = [&a.task_title, &a.title, &a.worker_name].into_iter().find(|t| !t.trim().is_empty()).cloned().unwrap_or_else(|| "untitled session".into());
    let open = if !a.task_id.is_empty() && !a.project_id.is_empty() {
        Target::Task { project_id: a.project_id.clone(), task_id: a.task_id.clone(), session_id: a.session_id.clone(), origin: String::new() }
    } else {
        Target::Session { session_id: a.session_id.clone(), origin: String::new() }
    };
    Card {
        machine: a.machine.clone(),
        agent: if a.agent.is_empty() { "swarm".into() } else { a.agent.clone() },
        title,
        elapsed: if a.started_at == 0 { String::new() } else { elapsed(now_ms.saturating_sub(a.started_at)) },
        doing: a.now.as_ref().map(|t| doing(t)).unwrap_or_else(|| "thinking".into()),
        timeline: a.timeline.iter().map(|t| (t.kind.clone(), t.state.clone())).collect(),
        counts: counts_text(&a.counts),
        files: a.files.clone(),
        more_files: a.files_total.saturating_sub(a.files.len() as u64),
        pipeline: a.stage.as_ref().map(pipeline),
        partial: a.partial,
        open,
    }
}

pub fn panel(now: &AgentsNow, now_ms: u64) -> Panel {
    let header = header(now);
    if now.agents.is_empty() {
        return Panel::Quiet { header, recent: now.recent.clone() };
    }
    Panel::Working { header, cards: now.agents.iter().map(|a| card(a, now_ms)).collect() }
}

/// A finished job's state as a short word for its chip.
pub fn state_word(state: &str) -> String {
    match state {
        "" => "finished".into(),
        s => s.replace('_', " "),
    }
}

/// The design set's answer (SWARM_CONSOLE_DEMO).
pub fn demo(now_ms: u64) -> AgentsNow {
    let v = serde_json::json!({
        "home": "pc", "at": now_ms,
        "agents": [
            {"machine": "forge", "agent": "coder", "session_id": "s2", "run_id": "r2", "title": "Fleet agents route", "project_id": "p1", "task_id": "t1",
             "task_title": "Add the agents-at-work read", "started_at": now_ms - 252_000,
             "now": {"kind": "command", "tool": "bash", "target": "go test ./internal/api/", "at": now_ms - 4_000, "state": "running"},
             "timeline": [{"kind": "read", "tool": "read", "state": "done"}, {"kind": "read", "tool": "search", "state": "done"}, {"kind": "edit", "tool": "edit", "state": "done"},
                          {"kind": "edit", "tool": "write", "state": "done"}, {"kind": "commit", "tool": "git_commit", "state": "done"}, {"kind": "command", "tool": "bash", "state": "running"}],
             "counts": {"reads": 14, "edits": 6, "commands": 3, "commits": 1, "last_minute": 7},
             "files": ["swarmd/internal/api/fleet_agents.go", "swarmd/internal/api/fleet.go"], "files_total": 2,
             "stage": {"step": "fix", "round": 2, "workflow": "review and push"}},
            {"machine": "forge", "agent": "reviewer", "session_id": "s4", "run_id": "r4", "title": "Review composer row", "started_at": now_ms - 61_000,
             "now": {"kind": "read", "tool": "read", "target": "console/src/console.rs", "at": now_ms - 2_000, "state": "running"},
             "timeline": [{"kind": "read", "tool": "read", "state": "done"}, {"kind": "read", "tool": "read", "state": "running"}],
             "counts": {"reads": 9, "last_minute": 4}, "files": [], "files_total": 0, "stage": {"step": "review"}},
            {"machine": "pc", "agent": "swarm", "session_id": "s1", "run_id": "r1", "title": "Orchestrator", "started_at": now_ms - 15_000,
             "timeline": [], "counts": {}, "files": [], "files_total": 0}
        ],
        "machines": [{"name": "pc", "state": "online", "agents": 1, "landed_today": 1}, {"name": "forge", "state": "online", "agents": 2, "landed_today": 2, "failed_today": 1},
                     {"name": "swarm-deploy", "state": "not_shown", "reason": "linked work rule is off"}],
        "recent": [{"machine": "forge", "project_id": "p1", "task_id": "t0", "title": "Pushed fix", "state": "landed", "reason": "pushed 5711ae410abc", "at": now_ms - 900_000}],
        "totals": {"agents": 3, "tool_calls_last_minute": 11, "landed_today": 3, "failed_today": 1}
    });
    parse(&v)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    // A fixture shaped like GET /v3/fleet/agents (fleet_agents.go's view).
    fn fixture(now: u64) -> AgentsNow {
        parse(&json!({
            "home": "pc", "at": now,
            "agents": [
                {"machine": "forge", "agent": "coder", "session_id": "s1", "run_id": "r1", "title": "session", "project_id": "p", "task_id": "t",
                 "task_title": "Fit the composer row", "started_at": now - 125_000,
                 "now": {"kind": "edit", "tool": "edit", "target": "console/src/data.rs", "at": now - 1_000, "state": "running"},
                 "timeline": [{"kind": "read", "tool": "read", "target": "a", "state": "done"}, {"kind": "edit", "tool": "edit", "target": "console/src/data.rs", "state": "running"}],
                 "counts": {"reads": 1, "edits": 1, "commands": 0, "commits": 0, "other": 0, "last_minute": 2},
                 "files": ["console/src/data.rs"], "files_total": 3,
                 "stage": {"step": "fix", "round": 2, "workflow": "review"}},
                {"machine": "forge", "agent": "", "session_id": "s2", "run_id": "r2", "title": "", "started_at": 0, "timeline": [], "counts": {}, "files": [], "files_total": 0},
                {"machine": "pc", "agent": "swarm", "session_id": "s3", "run_id": "r3", "title": "Orchestrator", "started_at": now - 5_000,
                 "now": {"kind": "command", "tool": "bash", "target": "go test ./...", "at": now, "state": "running"}, "timeline": [], "counts": {}, "files": [], "files_total": 0,
                 "stage": {"step": "nonsense"}}
            ],
            "machines": [{"name": "pc", "state": "online", "agents": 1}, {"name": "forge", "state": "online", "agents": 2}, {"name": "lab", "state": "offline", "reason": "HTTP 502", "agents": 0}],
            "recent": [{"machine": "pc", "task_id": "t9", "title": "Old", "state": "needs_you", "at": 5}],
            "totals": {"agents": 3, "tool_calls_last_minute": 14, "landed_today": 2, "failed_today": 1}
        }))
    }

    // Purpose: the bottom row's count reads the home's answer as the person
    // needs it: dim "0 agents" when nothing runs, "● 1 agent" for one,
    // "● N agents" with the per-machine split, busiest first, when the fleet
    // has more than one machine, and the short "● N" for a narrow pane.
    // Machines that could not be read are named for the tooltip, not
    // counted. Threat: a count that disagrees with Swarm's totals, a split
    // naming idle machines, or a missing unreachable machine. Boundary:
    // agents::indicator, the pure rule the composer renders from.
    #[test]
    fn indicator_counts_agents_per_machine() {
        let none = AgentsNow { machines: vec![Machine { name: "pc".into(), state: "online".into(), ..Default::default() }], ..Default::default() };
        assert_eq!(indicator(&none), Indicator { full: "0 agents".into(), short: "0".into(), active: false, unread: vec![] });

        let one = AgentsNow {
            agents: vec![Agent { machine: "pc".into(), ..Default::default() }],
            machines: vec![Machine { name: "pc".into(), state: "online".into(), agents: 1, ..Default::default() }],
            totals: Totals { agents: 1, ..Default::default() },
            ..Default::default()
        };
        let i = indicator(&one);
        assert_eq!((i.full.as_str(), i.short.as_str(), i.active), ("● 1 agent", "● 1", true));

        let many = fixture(1_000_000);
        let i = indicator(&many);
        assert_eq!(i.full, "● 3 agents · forge 2 · pc 1");
        assert_eq!(i.short, "● 3");
        assert_eq!(i.unread, vec!["lab: offline (HTTP 502)".to_string()]);

        // Two machines with one each: the order is by name.
        let even = AgentsNow {
            machines: vec![Machine { name: "zeta".into(), state: "online".into(), agents: 1, ..Default::default() }, Machine { name: "alpha".into(), state: "online".into(), agents: 1, ..Default::default() }],
            totals: Totals { agents: 2, ..Default::default() },
            ..Default::default()
        };
        assert_eq!(indicator(&even).full, "● 2 agents · alpha 1 · zeta 1");
    }

    // Purpose: the panel is built from the answer alone: one card per
    // running agent with its machine, the task's title (else the session's),
    // the elapsed time, what it does now, its timeline oldest first, its
    // counts and files (with how many more), its pipeline, and where a click
    // opens (the task when there is one); "All quiet" with the recent jobs
    // and their derived state when nothing runs. Threat: a card for the
    // wrong run, a click opening the wrong conversation, an invented title or
    // step. Boundary: agents::panel and agents::card.
    #[test]
    fn panel_from_fixture_cards_and_all_quiet() {
        let now = 1_000_000;
        let Panel::Working { header, cards } = panel(&fixture(now), now) else { panic!("want cards") };
        assert_eq!(header, Header { working: "3 agents working".into(), per_minute: "14 tool calls/min".into(), landed: "2 landed today".into(), failed: "1 failed today".into() });
        assert_eq!(cards.len(), 3);
        let c = &cards[0];
        assert_eq!((c.machine.as_str(), c.agent.as_str(), c.title.as_str()), ("forge", "coder", "Fit the composer row"));
        assert_eq!(c.elapsed, "2m 05s");
        assert_eq!(c.doing, "edit console/src/data.rs");
        assert_eq!(c.timeline, vec![("read".to_string(), "done".to_string()), ("edit".to_string(), "running".to_string())]);
        assert_eq!(c.counts, "1 read · 1 edit · 0 commands · 0 commits");
        assert_eq!((c.files.clone(), c.more_files), (vec!["console/src/data.rs".to_string()], 2));
        assert_eq!(c.open, Target::Task { project_id: "p".into(), task_id: "t".into(), session_id: "s1".into(), origin: String::new() });
        let steps = c.pipeline.as_ref().expect("a workflow's task has a pipeline");
        assert_eq!(steps.iter().map(|s| s.label.as_str()).collect::<Vec<_>>(), ["Code", "Review", "Fix · round 2", "Push", "Landed"]);
        assert_eq!(steps.iter().position(|s| s.current), Some(2));
        assert!(steps[0].done && steps[1].done && !steps[3].done);
        // No task, no title, no start, no tool yet: nothing invented.
        let bare = &cards[1];
        assert_eq!((bare.agent.as_str(), bare.title.as_str(), bare.elapsed.as_str(), bare.doing.as_str()), ("swarm", "untitled session", "", "thinking"));
        assert_eq!(bare.open, Target::Session { session_id: "s2".into(), origin: String::new() });
        assert!(bare.pipeline.is_none());
        // A step Swarm does not name is no pipeline.
        assert_eq!(cards[2].doing, "bash: go test ./...");
        assert!(cards[2].pipeline.is_none());

        let mut quiet = fixture(now);
        quiet.agents.clear();
        quiet.totals.agents = 0;
        let Panel::Quiet { header, recent } = panel(&quiet, now) else { panic!("want all quiet") };
        assert_eq!(header.working, "0 agents working");
        assert_eq!(recent.len(), 1);
        assert_eq!((recent[0].title.as_str(), state_word(&recent[0].state)), ("Old", "needs you".to_string()));
    }

    // Purpose: the pipeline strip highlights exactly the stage's step, marks
    // the ones before it done, carries the round on Fix only, and a landed
    // task shows every step done. Threat: two steps lit, or a round on the
    // wrong step. Boundary: agents::pipeline.
    #[test]
    fn pipeline_marks_the_current_step() {
        let at = |step, round| pipeline(&Stage { step, round, workflow: String::new() });
        let review = at(Step::Review, 0);
        assert_eq!(review.iter().filter(|s| s.current).count(), 1);
        assert!(review[1].current && review[0].done && !review[2].done);
        assert_eq!(review[2].label, "Fix");
        let landed = at(Step::Landed, 0);
        assert!(landed[4].current && landed.iter().all(|s| s.done));
        assert_eq!(at(Step::Push, 3)[2].label, "Fix · round 3");
        assert!(at(Step::Code, 0)[0].current);
    }

    // Purpose: between reads the fleet stream's tool events patch the card
    // they name (now doing, timeline, counts, files), and a record that moves
    // runs, or a tool call of a run the answer does not have, asks for a new
    // read; the realtime frames that move runs are recognised. Threat: a
    // stale card after a run starts or ends (no read), or a tool call
    // patched into another machine's card. Boundary: AgentsNow::apply,
    // moves_runs, realtime_moves_runs.
    #[test]
    fn stream_patches_cards_and_asks_for_reads() {
        let mut now = fixture(1_000_000);
        let started = Activity { machine: "forge".into(), kind: "session.tool.started".into(), session_id: "s1".into(), tool_name: "write".into(), file_path: "console/README.md".into(), at: 1_000_500, ..Default::default() };
        assert!(!now.apply(&started));
        let c = &now.agents[0];
        assert_eq!(c.now.as_ref().map(doing), Some("write console/README.md".to_string()));
        assert_eq!((c.counts.edits, c.files_total, c.files[0].as_str(), c.timeline.len()), (2, 4, "console/README.md", 3));
        let done = Activity { kind: "session.tool.completed".into(), ..started.clone() };
        assert!(!now.apply(&done));
        assert_eq!(now.agents[0].now.as_ref().map(|t| t.state.as_str()), Some("done"));
        // Same session id on another machine: not this card; unknown, so a read.
        assert!(now.apply(&Activity { machine: "pc".into(), ..started.clone() }));
        assert_eq!(now.agents[2].counts.edits, 0);
        assert!(now.apply(&Activity { machine: "forge".into(), kind: "session.run.completed".into(), session_id: "s1".into(), ..Default::default() }));
        assert!(now.apply(&Activity { machine: "pc".into(), kind: "job.started".into(), ..Default::default() }));
        assert!(!now.apply(&Activity { machine: "pc".into(), kind: "session.permission.requested".into(), ..Default::default() }));
        assert!(realtime_moves_runs("event", "session.run.failed"));
        assert!(realtime_moves_runs("task.lifecycle.updated", ""));
        assert!(!realtime_moves_runs("event", "session.message.delta"));
        assert!(!realtime_moves_runs("keepalive", ""));
    }

    // Purpose: the wire format parses as Swarm writes it, and "today" starts
    // at this computer's local midnight. Threat: a missing field read as a
    // made-up value; a day boundary in the wrong zone. Boundary: parse,
    // local_midnight, path, elapsed, tool_kind.
    #[test]
    fn parse_and_small_rules() {
        let a = parse(&json!({"agents": [{"machine": "pc", "counts": {"reads": 2}, "stage": {"step": "push"}}]}));
        assert_eq!(a.agents[0].counts.reads, 2);
        assert_eq!(a.agents[0].stage.as_ref().map(|s| s.step), Some(Step::Push));
        assert!(a.agents[0].now.is_none() && a.machines.is_empty() && a.totals == Totals::default());
        // 2026-10-29 13:30:00 UTC, at UTC+2: local midnight is 22:00 UTC the day before.
        let now = 1_793_280_600_000;
        assert_eq!(local_midnight(now, 0), 1_793_232_000_000);
        assert_eq!(local_midnight(now, 7200), 1_793_224_800_000);
        assert_eq!(path(5), "/v3/fleet/agents?since=5");
        assert_eq!((elapsed(42_000).as_str(), elapsed(3_599_000).as_str(), elapsed(3_780_000).as_str()), ("42s", "59m 59s", "1h 03m"));
        assert_eq!((tool_kind("Bash"), tool_kind("git_commit"), tool_kind("search"), tool_kind("todo")), ("command", "commit", "read", "other"));
    }
}
