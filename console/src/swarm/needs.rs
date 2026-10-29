//! What needs the person, across the fleet: permissions an agent waits on,
//! tasks whose state is needs_you, and runs that failed today. Every
//! item is a Swarm record read from that machine (a linked machine's through
//! home's link), with the machine's name; nothing is made up here.
//!
//! Per machine, each fleet tick (swarm::fleet):
//! - permissions: the box summary's `attention.sessions` (Swarm lists at
//!   most 20 there and counts them all), then each listed session's pending
//!   records (GET /v3/sessions/{id}/permissions?status=pending) for what it
//!   asks; read again only when the session's pending count or oldest
//!   request changed;
//! - tasks: GET /v3/projects, then each project's GET
//!   /v3/projects/{id}/tasks; a task waits on you when its derived state
//!   (`progress.task_state`, docs/task-progress.md) is needs_you. The raw
//!   `status` is only the run's status (needs_review: the agent's run ended,
//!   and it stays so after the work landed), so it decides only for a Swarm
//!   that gives no state;
//! - failed runs: the box summary's `runs.failed_runs` (today's, newest
//!   first; `runs.failed` counts them all), each with its error from GET
//!   /v3/workers/{worker}/runs/{run}, read once (a failed run does not change).
//!
//! At most PER_KIND items per machine and kind are kept; the counts are
//! Swarm's totals.

use std::collections::HashMap;
use std::sync::Mutex;

use serde_json::Value;

use super::client::Client;
use super::map::{self, list, s};

/// Items kept per machine and kind.
pub const PER_KIND: usize = 20;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    Permission,
    Task,
    FailedRun,
}

impl Kind {
    pub fn label(self) -> &'static str {
        match self {
            Kind::Permission => "permission",
            Kind::Task => "task",
            Kind::FailedRun => "failed",
        }
    }
}

/// Where a click on an item goes, on the machine it belongs to.
#[derive(Clone, Debug, PartialEq)]
pub enum Target {
    /// A session waiting on a permission; `origin` is the conversation that
    /// started its task, when the session is a task's.
    Session { session_id: String, origin: String },
    Task { project_id: String, task_id: String, session_id: String, origin: String },
    Run { worker_id: String, run_id: String, session_id: String },
}

#[derive(Clone, Debug, PartialEq)]
pub struct Item {
    /// On home (this window's Swarm); else on the linked machine `machine`.
    pub home: bool,
    pub machine: String,
    pub kind: Kind,
    /// When it started waiting or failed (ms), for newest first.
    pub at: u64,
    /// What it is: the request, the task, the worker.
    pub what: String,
    /// Where or why: the task or session, the project, the run's error.
    pub detail: String,
    pub target: Target,
}

/// One machine's reading. A count is None when it was not read.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Reading {
    pub home: bool,
    pub machine: String,
    /// Sessions waiting on a person (the summary's count).
    pub waiting_sessions: Option<usize>,
    /// Tasks waiting on the person (state needs_you), in every project read.
    pub waiting_tasks: Option<usize>,
    pub failed_today: Option<usize>,
    /// Newest first, at most PER_KIND of each kind.
    pub items: Vec<Item>,
    /// What could not be read, in Swarm's words.
    pub unread: Vec<String>,
}

impl Reading {
    /// Everything waiting on the person here, or None when part of it was not read.
    pub fn waiting(&self) -> Option<usize> {
        Some(self.waiting_sessions? + self.waiting_tasks?)
    }
}

/// A session the summary lists as waiting on a person.
#[derive(Clone, Debug, PartialEq)]
pub struct Blocked {
    pub session_id: String,
    pub pending: usize,
    pub oldest_at: u64,
}

fn num(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

/// The summary's `attention`: how many sessions wait, and those it lists.
/// None when the summary has no attention block.
pub fn blocked(summary: &Value) -> Option<(usize, Vec<Blocked>)> {
    let a = summary.get("attention").filter(|a| a.is_object())?;
    let listed: Vec<Blocked> = list(a, "sessions")
        .iter()
        .filter(|b| !s(b, "session_id").is_empty())
        .map(|b| Blocked { session_id: s(b, "session_id").to_string(), pending: num(b, "pending") as usize, oldest_at: num(b, "oldest_pending_at") })
        .collect();
    let total = a.get("blocked_sessions").and_then(Value::as_u64).map(|n| n as usize).unwrap_or(listed.len());
    Some((total, listed))
}

/// A run the summary lists as failed today.
#[derive(Clone, Debug, PartialEq)]
pub struct FailedRun {
    pub worker_id: String,
    pub worker: String,
    pub run_id: String,
    pub source: String,
    pub at: u64,
}

/// The summary's failed runs today: Swarm's count and the runs it lists.
/// None when that Swarm keeps no run records.
pub fn failed(summary: &Value) -> Option<(usize, Vec<FailedRun>)> {
    let r = summary.get("runs").filter(|r| r.get("total").is_some())?;
    let listed: Vec<FailedRun> = list(r, "failed_runs")
        .iter()
        .filter(|b| !s(b, "run_id").is_empty())
        .map(|b| FailedRun {
            worker_id: s(b, "worker_id").to_string(),
            worker: s(b, "worker_name").to_string(),
            run_id: s(b, "run_id").to_string(),
            source: s(b, "request_source").to_string(),
            at: num(b, "completed_at").max(num(b, "created_at")),
        })
        .collect();
    let total = r.get("failed").and_then(Value::as_u64).map(|n| n as usize).unwrap_or(listed.len());
    Some((total, listed))
}

/// A project task, as far as an item needs it.
#[derive(Clone, Debug, PartialEq)]
pub struct TaskRef {
    pub project_id: String,
    pub project: String,
    pub task_id: String,
    pub title: String,
    /// The run's status (`status`): only a fallback when `state` is empty.
    pub status: String,
    /// The task's state as Swarm derives it (`progress.task_state`: running,
    /// waiting, needs_you, landed, done, not_integrated, stopped); empty from
    /// a Swarm that does not derive one.
    pub state: String,
    /// Swarm's one-line reason for `state`.
    pub state_reason: String,
    pub session_id: String,
    pub origin: String,
    /// Made by a workflow, automation or worker run, which handles it itself.
    pub automated: bool,
    pub at: u64,
}

/// A project's task board (GET /v3/projects/{id}/tasks).
pub fn tasks(project_id: &str, project: &str, board: &Value) -> Vec<TaskRef> {
    list(board, "tasks")
        .iter()
        .filter(|t| !s(t, "id").is_empty())
        .map(|t| TaskRef {
            project_id: if s(t, "project_id").is_empty() { project_id.to_string() } else { s(t, "project_id").to_string() },
            project: project.to_string(),
            task_id: s(t, "id").to_string(),
            title: s(t, "title").trim().to_string(),
            status: s(t, "status").to_string(),
            state: s(&t["progress"], "task_state").trim().to_string(),
            state_reason: s(&t["progress"], "task_state_reason").trim().to_string(),
            session_id: s(t, "session_id").to_string(),
            origin: s(t, "origin_session_id").to_string(),
            automated: !s(t, "automation_id").is_empty() || !s(t, "worker_run_id").is_empty(),
            at: num(t, "updated_at").max(num(t, "created_at")),
        })
        .collect()
}

/// A task waits on you when Swarm's derived state says so (needs_you:
/// approval, a question, a blocked or failed run or workflow). A landed,
/// done, not_integrated or stopped task does not, whatever its run status.
fn waits_on_you(t: &TaskRef) -> bool {
    if !t.state.is_empty() {
        return t.state == "needs_you";
    }
    // No state: an older Swarm (here or on a linked machine) that does not
    // derive one. Fall back to the old run-status rule: an approval request,
    // or a finished run of work you started; reviews a workflow or
    // automation made are handled by that workflow.
    match t.status.as_str() {
        "pending_approval" => true,
        "needs_review" => !t.automated && !t.origin.is_empty(),
        _ => false,
    }
}

/// Newest first, at most `n`.
pub fn newest(mut items: Vec<Item>, n: usize) -> Vec<Item> {
    items.sort_by(|a, b| b.at.cmp(&a.at).then_with(|| a.what.cmp(&b.what)));
    items.truncate(n);
    items
}

/// One line of text from Swarm's: the first line, at most `max` characters.
pub fn short(text: &str, max: usize) -> String {
    let line = text.trim().lines().next().unwrap_or("").trim();
    if line.chars().count() <= max {
        return line.to_string();
    }
    let cut: String = line.chars().take(max.saturating_sub(1)).collect();
    format!("{}…", cut.trim_end())
}

/// What a waiting task's item says it waits for: approval, else Swarm's
/// reason for its state, else (no state) a review.
fn waits_for(t: &TaskRef) -> String {
    if t.status == "pending_approval" {
        "approve?".to_string()
    } else if !t.state.is_empty() {
        if t.state_reason.is_empty() { "needs you".to_string() } else { short(&t.state_reason, 80) }
    } else {
        "review".to_string()
    }
}

/// Tasks waiting on the person: the count and the items.
pub fn task_items(home: bool, machine: &str, all: &[TaskRef]) -> (usize, Vec<Item>) {
    let waiting: Vec<&TaskRef> = all.iter().filter(|t| waits_on_you(t)).collect();
    let items = waiting
        .iter()
        .map(|t| Item {
            home,
            machine: machine.to_string(),
            kind: Kind::Task,
            at: t.at,
            what: if t.title.is_empty() { format!("task {}", t.task_id) } else { short(&t.title, 120) },
            detail: format!("{} · {}", waits_for(t), if t.project.is_empty() { t.project_id.as_str() } else { t.project.as_str() }),
            target: Target::Task { project_id: t.project_id.clone(), task_id: t.task_id.clone(), session_id: t.session_id.clone(), origin: t.origin.clone() },
        })
        .collect();
    (waiting.len(), newest(items, PER_KIND))
}

/// A waiting session as an item: what its oldest pending request asks
/// (`records`, its GET …/permissions?status=pending answer, when read), and
/// which task it is, when one of `all` runs in it.
pub fn permission_item(home: bool, machine: &str, b: &Blocked, records: Option<&Value>, all: &[TaskRef]) -> Item {
    let task = all.iter().find(|t| t.session_id == b.session_id);
    let what = match records.map(|r| list(r, "permissions")) {
        None => "a request (not read)".to_string(),
        Some(records) => match map::pending(records) {
            Some(p) if p.question => {
                let q = if p.title.is_empty() { p.questions.first().map(|q| q.text.to_string()).unwrap_or_default() } else { p.title.to_string() };
                if q.trim().is_empty() { "asks you a question".to_string() } else { format!("asks: {}", short(&q, 120)) }
            }
            Some(p) => short(&p.text, 120),
            None => match records.iter().find(|r| map::is_client_tool(s(r, "tool_name"))) {
                Some(r) => format!("{} waits on your computer", s(r, "tool_name")),
                None => "a request".to_string(),
            },
        },
    };
    let more = if b.pending > 1 { format!(" (+{} more)", b.pending - 1) } else { String::new() };
    let short_id: String = b.session_id.chars().take(8).collect();
    let detail = match task {
        Some(t) if !t.title.is_empty() => format!("task {}", short(&t.title, 80)),
        _ => format!("session {short_id}"),
    };
    Item {
        home,
        machine: machine.to_string(),
        kind: Kind::Permission,
        at: b.oldest_at,
        what: format!("{what}{more}"),
        detail,
        target: Target::Session { session_id: b.session_id.clone(), origin: task.map(|t| t.origin.clone()).unwrap_or_default() },
    }
}

/// A failed run as an item, with its error when its record was read.
pub fn run_item(home: bool, machine: &str, r: &FailedRun, record: Option<&Value>) -> Item {
    let run = record.and_then(|v| v.get("run"));
    let error = run
        .and_then(|run| run.get("error"))
        .and_then(|e| e.as_str().map(str::to_string).or_else(|| e.get("message").and_then(Value::as_str).map(str::to_string)))
        .unwrap_or_default();
    let detail = match (record, error.trim()) {
        (None, _) => "reason not read".to_string(),
        (Some(_), "") => "no reason recorded".to_string(),
        (Some(_), e) => short(e, 120),
    };
    let worker = if r.worker.is_empty() { format!("worker {}", r.worker_id) } else { r.worker.clone() };
    let source = match r.source.as_str() {
        "" | "direct" | "direct_request" => String::new(),
        other => format!(" ({})", other.replace('_', " ")),
    };
    Item {
        home,
        machine: machine.to_string(),
        kind: Kind::FailedRun,
        at: r.at,
        what: format!("{worker}{source}"),
        detail,
        target: Target::Run { worker_id: r.worker_id.clone(), run_id: r.run_id.clone(), session_id: run.map(|v| s(v, "session_id").to_string()).unwrap_or_default() },
    }
}

/// Every machine's items in one list, newest first.
pub fn merged(readings: &[Reading]) -> Vec<Item> {
    let all: Vec<Item> = readings.iter().flat_map(|r| r.items.iter().cloned()).collect();
    let n = all.len();
    newest(all, n)
}

/// A total, and whether some machine's share is unknown.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Count {
    pub known: usize,
    pub unknown: bool,
}

impl Count {
    pub fn of(values: impl IntoIterator<Item = Option<usize>>) -> Count {
        let mut c = Count::default();
        for v in values {
            match v {
                Some(n) => c.known += n,
                None => c.unknown = true,
            }
        }
        c
    }

    /// "3", or "3+?" when some machine was not read.
    pub fn text(self) -> String {
        format!("{}{}", self.known, if self.unknown { "+?" } else { "" })
    }
}

/// Machines that need a look: attention or degraded by their own Swarm, or
/// not reached.
pub fn attention(machines: &[crate::data::Machine]) -> usize {
    use crate::data::Health;
    machines.iter().filter(|m| matches!(m.health, Health::Attention | Health::Degraded | Health::Offline)).count()
}

/// The page's top line: "4 machines · 1 needs attention · 3 waiting on you · 2 failed today".
pub fn glance(machines: usize, attention: usize, waiting: Count, failed: Count) -> String {
    format!(
        "{machines} machine{} · {attention} need{} attention · {} waiting on you · {} failed today",
        if machines == 1 { "" } else { "s" },
        if attention == 1 { "s" } else { "" },
        waiting.text(),
        failed.text()
    )
}

/// Records read on an earlier tick that are still current, so an unchanged
/// request or a failed run is not read again. Kept for one tick: what a tick
/// does not use is dropped.
#[derive(Default)]
pub struct Recall {
    old: HashMap<String, Value>,
    new: Mutex<HashMap<String, Value>>,
}

impl Recall {
    fn get_or(&self, key: String, read: impl FnOnce() -> Option<Value>) -> Option<Value> {
        let value = match self.old.get(&key) {
            Some(v) => Some(v.clone()),
            None => read(),
        };
        if let Some(v) = &value {
            self.new.lock().unwrap().insert(key, v.clone());
        }
        value
    }

    /// Ends a tick: only what it used is kept.
    pub fn turn(&mut self) {
        self.old = std::mem::take(self.new.get_mut().unwrap());
    }
}

/// Reads one machine's items. `summary` is its box summary as just read
/// (Err: why it could not be read).
pub fn read(client: &Client, home: bool, machine: &str, summary: Result<&Value, &str>, recall: &Recall) -> Reading {
    let mut out = Reading { home, machine: machine.to_string(), ..Default::default() };
    let summary = match summary {
        Ok(v) => v,
        Err(why) => {
            out.unread.push(if why.trim().is_empty() { "not reached".to_string() } else { why.trim().to_string() });
            return out;
        }
    };

    // Tasks first: a waiting session is named by the task it runs.
    let mut all = Vec::new();
    match client.get("/v3/projects") {
        Ok(projects) => {
            let projects: Vec<(String, String)> = list(&projects, "projects")
                .iter()
                .filter(|p| !s(p, "id").is_empty())
                .map(|p| (s(p, "id").to_string(), s(p, "name").to_string()))
                .collect();
            let boards: Vec<Result<Vec<TaskRef>, String>> = std::thread::scope(|scope| {
                let reads: Vec<_> = projects
                    .iter()
                    .map(|(id, name)| scope.spawn(move || client.get(&format!("/v3/projects/{id}/tasks")).map(|b| tasks(id, name, &b)).map_err(|e| e.to_string())))
                    .collect();
                reads.into_iter().map(|r| r.join().unwrap_or_else(|_| Err("reading it failed".into()))).collect()
            });
            let mut failed_boards = 0;
            for board in boards {
                match board {
                    Ok(t) => all.extend(t),
                    Err(e) => {
                        failed_boards += 1;
                        if failed_boards == 1 {
                            out.unread.push(format!("tasks: {e}"));
                        }
                    }
                }
            }
            let (count, items) = task_items(home, machine, &all);
            out.waiting_tasks = (failed_boards == 0).then_some(count);
            out.items.extend(items);
        }
        Err(e) => out.unread.push(format!("projects: {e}")),
    }

    match blocked(summary) {
        Some((total, listed)) => {
            out.waiting_sessions = Some(total);
            let items: Vec<Item> = std::thread::scope(|scope| {
                let reads: Vec<_> = listed
                    .iter()
                    .take(PER_KIND)
                    .map(|b| {
                        let all = &all;
                        scope.spawn(move || {
                            let key = format!("{machine}\u{1f}perm\u{1f}{}\u{1f}{}\u{1f}{}", b.session_id, b.pending, b.oldest_at);
                            let records = recall.get_or(key, || client.get(&format!("/v3/sessions/{}/permissions?status=pending&limit={PER_KIND}", b.session_id)).ok());
                            permission_item(home, machine, b, records.as_ref(), all)
                        })
                    })
                    .collect();
                reads.into_iter().filter_map(|r| r.join().ok()).collect()
            });
            out.items.extend(newest(items, PER_KIND));
        }
        None => out.unread.push("waiting sessions: the summary has none".into()),
    }

    match failed(summary) {
        Some((total, listed)) => {
            out.failed_today = Some(total);
            let items: Vec<Item> = std::thread::scope(|scope| {
                let reads: Vec<_> = listed
                    .iter()
                    .take(PER_KIND)
                    .map(|r| {
                        scope.spawn(move || {
                            let key = format!("{machine}\u{1f}run\u{1f}{}\u{1f}{}", r.worker_id, r.run_id);
                            let record = recall.get_or(key, || client.get(&format!("/v3/workers/{}/runs/{}", r.worker_id, r.run_id)).ok());
                            run_item(home, machine, r, record.as_ref())
                        })
                    })
                    .collect();
                reads.into_iter().filter_map(|r| r.join().ok()).collect()
            });
            out.items.extend(newest(items, PER_KIND));
        }
        None => out.unread.push("runs: this Swarm keeps no run records".into()),
    }

    out.items = merged(std::slice::from_ref(&out));
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// The parts of the box summary (swarmd/internal/api/box_summary.go) read here.
    fn summary() -> Value {
        json!({
            "status": "attention",
            "attention": {"blocked_sessions": 3, "oldest_pending_at": 1000, "sessions": [
                {"session_id": "sess-task-1", "pending": 2, "oldest_pending_at": 1000},
                {"session_id": "sess-orch-22", "pending": 1, "oldest_pending_at": 5000}
            ]},
            "runs": {"total": 6, "succeeded": 3, "failed": 2, "failed_runs": [
                {"worker_id": "w1", "worker_name": "nightly", "run_id": "r9", "request_source": "automation", "status": "failed", "created_at": 7000, "completed_at": 7600},
                {"worker_id": "w2", "run_id": "r3", "request_source": "direct", "status": "failed", "created_at": 2000}
            ]}
        })
    }

    fn board() -> Value {
        json!({"count": 4, "tasks": [
            {"id": "t1", "project_id": "p1", "title": "Add MIT LICENSE", "status": "pending_approval", "session_id": "sess-task-1", "origin_session_id": "orch-1", "created_at": 100, "updated_at": 4000},
            {"id": "t2", "project_id": "p1", "title": "Fix login", "status": "needs_review", "session_id": "sess-t2", "origin_session_id": "orch-1", "created_at": 200, "updated_at": 6000},
            {"id": "t3", "project_id": "p1", "title": "Running one", "status": "in_progress", "created_at": 300, "updated_at": 9000},
            {"id": "t4", "project_id": "p1", "title": "Done", "status": "completed", "updated_at": 9500}
        ]})
    }

    #[test]
    fn summary_attention_and_failed_runs() {
        let (total, listed) = blocked(&summary()).unwrap();
        assert_eq!(total, 3, "Swarm's count, not the listed length");
        assert_eq!(listed[0], Blocked { session_id: "sess-task-1".into(), pending: 2, oldest_at: 1000 });
        let (failed_total, runs) = failed(&summary()).unwrap();
        assert_eq!(failed_total, 2);
        assert_eq!(runs[0], FailedRun { worker_id: "w1".into(), worker: "nightly".into(), run_id: "r9".into(), source: "automation".into(), at: 7600 });
        assert_eq!(runs[1].at, 2000, "created_at when it has no completed_at");
        // A summary without those blocks is unknown, never zero.
        assert_eq!(blocked(&json!({"status": "ok"})), None);
        assert_eq!(failed(&json!({"runs": {}})), None);
    }

    #[test]
    fn tasks_waiting_for_approval_or_review() {
        let all = tasks("p1", "web", &board());
        assert_eq!(all.len(), 4);
        let (count, items) = task_items(false, "forge", &all);
        assert_eq!(count, 2);
        assert_eq!(items.iter().map(|i| i.what.as_str()).collect::<Vec<_>>(), vec!["Fix login", "Add MIT LICENSE"], "newest first");
        assert_eq!(items[0].detail, "review · web");
        assert_eq!(items[1].detail, "approve? · web");
        assert_eq!(items[1].target, Target::Task { project_id: "p1".into(), task_id: "t1".into(), session_id: "sess-task-1".into(), origin: "orch-1".into() });
        assert!(items.iter().all(|i| i.machine == "forge" && !i.home && i.kind == Kind::Task));
        // A review a workflow made (its automation handles it) does not wait
        // on you; an approval request always does.
        let made = json!({"tasks": [
            {"id": "r1", "title": "Review this job", "status": "needs_review", "automation_id": "workflow:jdel_1.review", "created_at": 9000},
            {"id": "r2", "title": "Worker output", "status": "needs_review", "worker_run_id": "wrun_1", "origin_session_id": "orch-1", "created_at": 9000},
            {"id": "a1", "title": "Run the media job", "status": "pending_approval", "automation_id": "workflow:jdel_2", "created_at": 9000}
        ]});
        let (count, items) = task_items(false, "social", &tasks("p2", "Socials", &made));
        assert_eq!(count, 1);
        assert_eq!(items[0].what, "Run the media job");
    }

    /// Purpose: the "needs you" list and count follow the task state Swarm
    /// derives (`progress.task_state` on GET /v3/projects/{id}/tasks,
    /// pebblestore.ProjectTaskState), not the run status. Threat: a task
    /// whose run ended (needs_review) but whose work already landed on dev,
    /// or that finished with nothing to hand over, staying in the list and
    /// the counts. Rows without a state (an older Swarm) keep the old rule.
    #[test]
    fn tasks_wait_on_you_by_their_derived_state() {
        let board = json!({"tasks": [
            {"id": "landed", "title": "Pushed fix", "status": "needs_review", "origin_session_id": "orch-1", "created_at": 100,
             "progress": {"state": "integrated", "task_state": "landed", "task_state_reason": "pushed 5711ae410abc"}},
            {"id": "done", "title": "Read-only look", "status": "needs_review", "origin_session_id": "orch-1", "created_at": 200,
             "progress": {"task_state": "done", "task_state_reason": "finished; nothing to integrate"}},
            {"id": "blocked", "title": "Blocked review", "status": "needs_review", "origin_session_id": "orch-1", "created_at": 300,
             "progress": {"task_state": "needs_you", "task_state_reason": "workflow blocked rounds_exhausted: step send-back"}},
            {"id": "branch", "title": "Unmerged branch", "status": "needs_review", "origin_session_id": "orch-1", "created_at": 400,
             "progress": {"task_state": "not_integrated", "task_state_reason": "commits on branch agent/x not handed over"}},
            {"id": "approve", "title": "Approve me", "status": "pending_approval", "origin_session_id": "orch-1", "created_at": 500,
             "progress": {"task_state": "needs_you", "task_state_reason": "waiting for approval"}},
            // A Swarm that derives no state: the old run-status rule.
            {"id": "old", "title": "Old daemon review", "status": "needs_review", "origin_session_id": "orch-1", "created_at": 600, "progress": {"state": "working"}},
            {"id": "old-auto", "title": "Old workflow review", "status": "needs_review", "automation_id": "workflow:w1", "created_at": 700}
        ]});
        let all = tasks("p1", "web", &board);
        assert_eq!((all[0].status.as_str(), all[0].state.as_str(), all[0].state_reason.as_str()), ("needs_review", "landed", "pushed 5711ae410abc"));
        assert_eq!(all[5].state, "", "no task_state: no state");
        assert!(!waits_on_you(&all[0]), "needs_review but landed: not yours");
        assert!(!waits_on_you(&all[1]), "done: not yours");
        assert!(waits_on_you(&all[2]), "needs_you");
        assert!(!waits_on_you(&all[3]), "not_integrated is not counted as needs you");
        assert!(waits_on_you(&all[4]), "pending_approval maps to needs_you");
        assert!(waits_on_you(&all[5]), "no state: the old rule (needs_review, started by you)");
        assert!(!waits_on_you(&all[6]), "no state: the old rule (a workflow's review)");

        let (count, items) = task_items(true, "", &all);
        assert_eq!(count, 3);
        assert_eq!(items.iter().map(|i| i.what.as_str()).collect::<Vec<_>>(), vec!["Old daemon review", "Approve me", "Blocked review"]);
        assert_eq!(items[0].detail, "review · web", "no state: as before");
        assert_eq!(items[1].detail, "approve? · web");
        assert_eq!(items[2].detail, "workflow blocked rounds_exhausted: step send-back · web", "the state's reason");
    }

    #[test]
    fn a_waiting_session_says_what_it_asks() {
        let all = tasks("p1", "web", &board());
        let (_, listed) = blocked(&summary()).unwrap();
        let records = json!({"ok": true, "permissions": [
            {"id": "perm_1", "status": "pending", "tool_name": "bash", "tool_arguments": "{\"command\":\"git push origin dev\"}", "created_at": 1000}
        ]});
        let item = permission_item(true, "", &listed[0], Some(&records), &all);
        assert_eq!(item.what, "bash wants to run: git push origin dev (+1 more)");
        assert_eq!(item.detail, "task Add MIT LICENSE", "named by the task running in that session");
        assert_eq!(item.target, Target::Session { session_id: "sess-task-1".into(), origin: "orch-1".into() });
        assert_eq!(item.at, 1000);

        let question = json!({"permissions": [{"id": "perm_2", "status": "pending", "tool_name": "ask_user",
            "tool_arguments": {"title": "Which database?", "question": "Postgres or SQLite?", "options": ["Postgres", "SQLite"]}}]});
        let item = permission_item(false, "forge", &listed[1], Some(&question), &all);
        assert_eq!(item.what, "asks: Which database?");
        assert_eq!(item.detail, "session sess-orc");
        assert_eq!(item.target, Target::Session { session_id: "sess-orch-22".into(), origin: "".into() });

        // Not read: said so, nothing guessed.
        assert_eq!(permission_item(false, "forge", &listed[1], None, &all).what, "a request (not read)");
    }

    #[test]
    fn a_failed_run_with_its_reason() {
        let (_, runs) = failed(&summary()).unwrap();
        let record = json!({"run": {"id": "r9", "status": "failed", "error": "workspace gone\nstack…", "session_id": "s-run"}});
        let item = run_item(false, "forge", &runs[0], Some(&record));
        assert_eq!((item.what.as_str(), item.detail.as_str()), ("nightly (automation)", "workspace gone"));
        assert_eq!(item.target, Target::Run { worker_id: "w1".into(), run_id: "r9".into(), session_id: "s-run".into() });
        let unnamed = run_item(false, "forge", &runs[1], Some(&json!({"run": {"id": "r3"}})));
        assert_eq!((unnamed.what.as_str(), unnamed.detail.as_str()), ("worker w2", "no reason recorded"));
        assert_eq!(run_item(false, "forge", &runs[1], None).detail, "reason not read");
    }

    fn item(machine: &str, at: u64) -> Item {
        Item { home: false, machine: machine.into(), kind: Kind::Task, at, what: format!("{machine}-{at}"), detail: "".into(), target: Target::Session { session_id: "".into(), origin: "".into() } }
    }

    #[test]
    fn newest_first_and_bounded() {
        let many: Vec<Item> = (0..30).map(|i| item("a", i)).collect();
        let kept = newest(many, PER_KIND);
        assert_eq!(kept.len(), PER_KIND);
        assert_eq!((kept[0].at, kept[PER_KIND - 1].at), (29, 10));

        let readings = vec![
            Reading { machine: "a".into(), items: vec![item("a", 5), item("a", 1)], ..Default::default() },
            Reading { machine: "b".into(), items: vec![item("b", 9), item("b", 3)], ..Default::default() },
        ];
        let all = merged(&readings);
        assert_eq!(all.iter().map(|i| i.at).collect::<Vec<_>>(), vec![9, 5, 3, 1]);
    }

    #[test]
    fn counts_mark_unknown_machines() {
        let read = Reading { waiting_sessions: Some(1), waiting_tasks: Some(2), failed_today: Some(0), ..Default::default() };
        let half = Reading { waiting_sessions: Some(4), waiting_tasks: None, failed_today: Some(2), ..Default::default() };
        let gone = Reading::default();
        assert_eq!(read.waiting(), Some(3));
        assert_eq!(half.waiting(), None, "part of it was not read");
        let readings = [read, half, gone];
        let waiting = Count::of(readings.iter().map(Reading::waiting));
        let failed = Count::of(readings.iter().map(|r| r.failed_today));
        assert_eq!(waiting, Count { known: 3, unknown: true });
        assert_eq!(failed.text(), "2+?");
        assert_eq!(glance(4, 1, Count { known: 3, unknown: false }, Count { known: 2, unknown: false }), "4 machines · 1 needs attention · 3 waiting on you · 2 failed today");
        assert_eq!(glance(1, 0, waiting, failed), "1 machine · 0 need attention · 3+? waiting on you · 2+? failed today");
    }

    #[test]
    fn machines_that_need_a_look() {
        use crate::data::{Health, Machine};
        let with = |h: Health| {
            let mut m = Machine::unread("m".into(), "paired".into(), "".into());
            m.health = h;
            m
        };
        let machines = [with(Health::Ok), with(Health::Attention), with(Health::Degraded), with(Health::Offline), with(Health::Unknown)];
        assert_eq!(attention(&machines), 3);
    }

    #[test]
    fn short_lines() {
        assert_eq!(short("  one\ntwo", 10), "one");
        assert_eq!(short("abcdefghijkl", 6), "abcde…");
    }

    #[test]
    fn recall_keeps_one_tick() {
        let mut recall = Recall::default();
        let mut reads = 0;
        let mut get = |recall: &Recall, key: &str| recall.get_or(key.into(), || { reads += 1; Some(json!(reads)) });
        assert_eq!(get(&recall, "a"), Some(json!(1)));
        recall.turn();
        assert_eq!(get(&recall, "a"), Some(json!(1)), "unchanged: not read again");
        recall.turn();
        recall.turn(); // a tick that did not use it drops it
        assert_eq!(get(&recall, "a"), Some(json!(2)));
    }
}
