//! The account user's one persistent Companion, as Swarm keeps it
//! (`GET`/`POST /v3/companion`, swarmd/internal/api/companion.go).
//!
//! The Companion is an ordinary V3 Swarm session that Swarm owns through a
//! durable identity record: one per account user on this machine. Its
//! history and live events are that session's canonical routes and realtime
//! stream; this module only reads the identity and its activity, and maps
//! what the follow loop already holds (the session, its tasks) into what the
//! Companion page shows. It keeps no state of its own.

use std::collections::HashMap;

use serde_json::{Value, json};

use super::client::Client;
use super::map::{self, list, s};
use crate::data::{LiveRef, Project, RunState, Session, Task, TaskState, TaskStatus};

/// The protected metadata Swarm sets on the Companion session only
/// (pebblestore.SessionCompanionMetadataKey).
pub const METADATA_KEY: &str = "swarm_v3_companion";

/// The Companion identity as Swarm reports it (CompanionView).
#[derive(Clone, Debug, PartialEq, Default)]
pub struct View {
    pub session_id: String,
    pub generation: u64,
    /// "ready" (its session exists) or "uncreated".
    pub state: String,
    /// Read on GET only; None after an ensure.
    pub activity: Option<Activity>,
}

impl View {
    pub fn ready(&self) -> bool {
        self.state == "ready" && !self.session_id.is_empty()
    }
}

/// What the Companion is doing now (CompanionActivity), read from Swarm's
/// records at request time.
#[derive(Clone, Debug, PartialEq, Default)]
pub struct Activity {
    pub run_active: bool,
    pub run_status: String,
    pub run_phase: String,
    pub stop_reason: String,
    /// The person's messages accepted during a run that the model has not
    /// received yet, oldest first.
    pub queued: Vec<Queued>,
    pub queued_more: bool,
    pub pending_permissions: u64,
    /// The delegated tasks the Companion follows, with exact identity.
    pub watches: Vec<Watch>,
    /// When a message sent now reaches the Companion: "starts_turn",
    /// "next_step" or "after_tool_calls" ("" from an older Swarm).
    pub input_delivery: String,
    /// The active run's tool calls in flight: (tool name, on a device).
    /// New input waits for them; it does not interrupt them.
    pub tool_calls: Vec<(String, bool)>,
    /// Background results handed to a run and not read yet: their status,
    /// "waiting_for_run" or "redelivering".
    pub deliveries: Vec<String>,
    /// Committed events the maintained context has not folded in yet, and
    /// its last maintenance failure (None from an older Swarm).
    pub context: Option<ContextFreshness>,
}

/// How far the Companion's maintained context follows its event log.
#[derive(Clone, Debug, PartialEq, Default)]
pub struct ContextFreshness {
    pub pending: u64,
    pub running: bool,
    pub last_failure: String,
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct Queued {
    pub message_id: String,
    /// "queued" (reaches the running turn) or "waiting_next_turn".
    pub delivery_status: String,
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct Watch {
    pub project_id: String,
    /// "" for this machine; a linked machine's name otherwise.
    pub machine: String,
    pub task_ids: Vec<String>,
    /// Titles Swarm gave, in task_ids order (untrusted for linked machines).
    pub titles: Vec<String>,
}

/// The `companion` object of a /v3/companion answer; None when Swarm has no
/// Companion for this user yet (`"companion": null`).
pub fn view(answer: &Value) -> Option<View> {
    let c = answer.get("companion").filter(|c| c.is_object())?;
    let activity = c.get("activity").filter(|a| a.is_object()).map(|a| {
        let run = a.get("run").cloned().unwrap_or(Value::Null);
        Activity {
            run_active: run.get("active").and_then(Value::as_bool).unwrap_or(false),
            run_status: s(&run, "status").to_string(),
            run_phase: s(&run, "phase").to_string(),
            stop_reason: s(&run, "stop_reason").to_string(),
            queued: list(a, "queued_inputs").iter().map(|q| Queued { message_id: s(q, "message_id").to_string(), delivery_status: s(q, "delivery_status").to_string() }).collect(),
            queued_more: a.get("queued_inputs_more").and_then(Value::as_bool).unwrap_or(false),
            pending_permissions: a.get("pending_permissions").and_then(Value::as_u64).unwrap_or(0),
            watches: list(a, "task_watches")
                .iter()
                .map(|w| Watch {
                    project_id: s(w, "project_id").to_string(),
                    machine: s(w, "machine").to_string(),
                    task_ids: list(w, "task_ids").iter().filter_map(Value::as_str).map(str::to_string).collect(),
                    titles: list(w, "titles").iter().filter_map(Value::as_str).map(str::to_string).collect(),
                })
                .collect(),
            input_delivery: s(a, "input_delivery").to_string(),
            tool_calls: list(a, "tool_calls").iter().map(|c| (s(c, "tool_name").to_string(), c.get("device").and_then(Value::as_bool).unwrap_or(false))).collect(),
            deliveries: list(a, "background_deliveries").iter().map(|d| s(d, "status").to_string()).collect(),
            context: a.get("context").filter(|c| c.is_object()).map(|c| ContextFreshness {
                pending: c.get("pending").and_then(Value::as_u64).unwrap_or(0),
                running: c.get("running").and_then(Value::as_bool).unwrap_or(false),
                last_failure: c.get("last_failure").map(|f| s(f, "error").to_string()).unwrap_or_default(),
            }),
        }
    });
    Some(View { session_id: s(c, "session_id").to_string(), generation: c.get("generation").and_then(Value::as_u64).unwrap_or(0), state: s(c, "state").to_string(), activity })
}

/// Reads the Companion and its activity (GET /v3/companion). Creates nothing.
pub fn read(client: &Client) -> Result<Option<View>, String> {
    let answer = client.get("/v3/companion").map_err(|e| e.to_string())?;
    Ok(view(&answer))
}

/// The person opened the Companion: Swarm's get-or-create (POST
/// /v3/companion), idempotent across retries, windows and restarts, so a
/// second call returns the same session.
pub fn ensure(client: &Client) -> Result<View, String> {
    let answer = client.post("/v3/companion", json!({})).map_err(|e| e.to_string())?;
    view(&answer).filter(View::ready).ok_or_else(|| "Swarm did not return a Companion".to_string())
}

/// Is this session shell the Companion (Swarm's protected marker)?
pub fn is_companion(shell: &Value) -> bool {
    shell.get("metadata").and_then(|m| m.get(METADATA_KEY)).is_some_and(|v| !v.is_null())
}

/// The Companion session as the console shows it. It belongs to no project:
/// `project` is 0 only to stay a valid index, and every project list leaves
/// it out (Console::is_companion).
pub fn session(shell: &Value, run: &Value, pending: bool) -> Session {
    let last = shell.get("last_message_at").and_then(Value::as_f64).unwrap_or(0.) as u64;
    let clock = map::run_clock(run, last);
    let failed = matches!(s(run, "status"), "failed") || !s(run, "error").is_empty();
    Session {
        live: Some(LiveRef { task_id: "".into(), session_id: s(shell, "id").to_string().into() }),
        title: "Companion".into(),
        project: 0,
        machine: 0,
        model: "".into(),
        state: if clock.active {
            RunState::Running
        } else if pending {
            RunState::NeedsYou
        } else if failed {
            RunState::Failed
        } else {
            RunState::Idle
        },
        age: "".into(),
        elapsed: "".into(),
        tokens: "".into(),
        entries: vec![],
        tasks: vec![],
        activity: vec![],
        status: None,
        pending: None,
        model_choice: None,
        model_favorite: None,
        pinned: false,
        calls: vec![],
        outbox: vec![],
        // A task watch keeps the Companion idle; its work shows beside it.
        waiting_on: map::task_wait(run),
        earlier: Default::default(),
        run: Some(clock),
    }
}

/// Where a piece of the Companion's work stands, for the work list.
#[derive(Clone, Copy, PartialEq, Eq, Debug, PartialOrd, Ord)]
pub enum Group {
    /// Waits on the person or cannot go on without someone acting.
    Attention,
    Running,
    Done,
}

impl Group {
    pub const ALL: [Group; 3] = [Group::Attention, Group::Running, Group::Done];

    pub fn label(self) -> &'static str {
        match self {
            Group::Attention => "NEEDS ATTENTION",
            Group::Running => "RUNNING",
            Group::Done => "RESULTS",
        }
    }

    /// From Swarm's derived task state; from the run status only when this
    /// Swarm derives none.
    pub fn of(task: &Task) -> Group {
        match task.progress.task_state {
            TaskState::NeedsYou | TaskState::BlockedInfra | TaskState::WorkflowFailed | TaskState::NotIntegrated => Group::Attention,
            TaskState::Running | TaskState::Waiting => Group::Running,
            TaskState::Landed | TaskState::Done | TaskState::Stopped => Group::Done,
            TaskState::Unknown => match task.status {
                TaskStatus::PendingApproval | TaskStatus::NeedsInput | TaskStatus::NeedsReview | TaskStatus::Failed => Group::Attention,
                TaskStatus::Queued | TaskStatus::Running => Group::Running,
                TaskStatus::Completed | TaskStatus::Rejected => Group::Done,
            },
        }
    }
}

/// One task the Companion started, with the project it lives in (a
/// Companion's tasks may be in any project).
#[derive(Clone)]
pub struct Work {
    /// The linked machine the task is on; empty for this machine.
    pub machine: String,
    pub project_id: String,
    pub project: String,
    pub task: Task,
    pub group: Group,
    pub updated_at: u64,
}

impl Work {
    /// Stable identity for selection across updates.
    pub fn key(&self) -> String {
        if self.machine.is_empty() { format!("{}/{}", self.project_id, self.task.key) } else { format!("{}:{}/{}", self.machine, self.project_id, self.task.key) }
    }
}

/// The Companion's work with the tasks it sent to linked machines (the
/// home's records), one item each, in the same order.
pub fn with_linked(mut out: Vec<Work>, records: &[Value]) -> Vec<Work> {
    let mut seen = std::collections::HashSet::new();
    for r in records {
        let (machine, project_id) = (s(r, "machine").to_string(), s(r, "project_id").to_string());
        if !seen.insert((machine.clone(), project_id.clone(), s(r, "task_id").to_string())) {
            continue;
        }
        let task = super::plan_review::linked_task(r);
        let updated_at = task.progress.updated_at;
        out.push(Work { project: format!("{project_id} on {machine}"), machine, project_id, group: Group::of(&task), task, updated_at });
    }
    out.sort_by(|a, b| a.group.cmp(&b.group).then(b.updated_at.cmp(&a.updated_at)).then(a.key().cmp(&b.key())));
    out
}

/// The Companion's tasks on this machine, from every project's task list the
/// follow loop holds: attention first, then running, then results, newest
/// first inside each group.
pub fn work(tasks: &HashMap<String, Vec<Value>>, projects: &[Project], session_id: &str) -> Vec<Work> {
    if session_id.is_empty() {
        return vec![];
    }
    let mut out: Vec<Work> = Vec::new();
    for (project_id, records) in tasks {
        let project = projects.iter().find(|p| p.id.as_ref() == project_id.as_str()).map(|p| p.name.to_string()).unwrap_or_else(|| project_id.clone());
        for t in records.iter().filter(|t| s(t, "origin_session_id") == session_id) {
            let mut task = map::task(t);
            if let Some(review) = task.review.as_mut().filter(|r| r.project_id.is_empty()) {
                review.project_id = project_id.clone().into();
            }
            let updated_at = ["updated_at", "created_at"].iter().find_map(|k| t.get(*k).and_then(Value::as_u64)).unwrap_or(0);
            out.push(Work { machine: String::new(), project_id: project_id.clone(), project: project.clone(), group: Group::of(&task), task, updated_at });
        }
    }
    out.sort_by(|a, b| a.group.cmp(&b.group).then(b.updated_at.cmp(&a.updated_at)).then(a.key().cmp(&b.key())));
    out
}

/// Watched tasks this machine's lists do not hold (on a linked machine, or
/// in a project not read): (machine, project id, task id, title).
pub fn watched_elsewhere(activity: &Activity, work: &[Work]) -> Vec<(String, String, String, String)> {
    let mut out = Vec::new();
    for w in &activity.watches {
        for (i, id) in w.task_ids.iter().enumerate() {
            let here = w.machine.is_empty() && work.iter().any(|x| x.project_id == w.project_id && x.task.key.as_ref() == id.as_str());
            if !here {
                out.push((w.machine.clone(), w.project_id.clone(), id.clone(), w.titles.get(i).cloned().unwrap_or_default()));
            }
        }
    }
    out
}

/// One line about the Companion's own turn, from its activity.
pub fn activity_line(a: &Activity) -> String {
    let mut parts: Vec<String> = Vec::new();
    if a.run_active {
        parts.push(if a.run_phase.is_empty() { "working".to_string() } else { format!("working ({})", a.run_phase) });
        if let Some((name, device)) = a.tool_calls.first() {
            let more = if a.tool_calls.len() > 1 { format!(" +{}", a.tool_calls.len() - 1) } else { String::new() };
            let place = if *device { " on the device" } else { "" };
            parts.push(format!("running {name}{place}{more}; new messages wait for it"));
        }
    } else if !a.stop_reason.is_empty() {
        parts.push(format!("last turn: {}", a.stop_reason));
    }
    let queued = a.queued.iter().filter(|q| q.delivery_status == "queued").count();
    let next = a.queued.iter().filter(|q| q.delivery_status == "waiting_next_turn").count();
    let more = if a.queued_more { "+" } else { "" };
    if queued > 0 {
        parts.push(format!("{queued}{more} message(s) queued for this turn"));
    }
    if next > 0 {
        parts.push(format!("{next}{more} message(s) wait for the next turn"));
    }
    let waiting = a.deliveries.iter().filter(|d| d.as_str() == "waiting_for_run").count();
    let again = a.deliveries.len() - waiting;
    if waiting > 0 {
        parts.push(format!("{waiting} background result(s) for this turn"));
    }
    if again > 0 {
        parts.push(format!("{again} background result(s) coming as their own turn"));
    }
    if a.pending_permissions > 0 {
        parts.push(format!("{} approval(s) waiting", a.pending_permissions));
    }
    let watched: usize = a.watches.iter().map(|w| w.task_ids.len()).sum();
    if watched > 0 {
        parts.push(format!("watching {watched} task(s)"));
    }
    if let Some(c) = &a.context {
        if !c.last_failure.is_empty() {
            parts.push("context may be stale (maintenance failed)".to_string());
        } else if c.pending > 0 {
            parts.push(format!("context catching up ({} event(s))", c.pending));
        }
    }
    parts.join(" · ")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn project(id: &str, name: &str) -> Project {
        Project {
            name: name.to_string().into(),
            machines: vec![0],
            id: id.to_string().into(),
            workspaces: vec![],
            roles: vec![],
            context: "".into(),
            context_generation: Default::default(),
            theme_id: "".into(),
        }
    }

    /// Requirement: the console reads Swarm's Companion contract as served
    /// (CompanionView with activity) and treats `companion: null` as "none
    /// yet", never inventing an identity. Threat: a parse that defaults a
    /// missing session to "ready" would open a pane on no session.
    #[test]
    fn companion_view_reads_swarms_answer() {
        let answer = json!({"ok": true, "companion": {
            "session_id": "cmp_1", "generation": 2, "state": "ready",
            "activity": {"run": {"active": true, "run_id": "r1", "status": "running", "phase": "tool"},
                "queued_inputs": [{"message_id": "m1", "message_seq": 9, "delivery_status": "queued"}],
                "queued_inputs_more": false, "pending_permissions": 1,
                "task_watches": [{"watch_id": "w", "project_id": "p1", "machine": "lab", "task_ids": ["t1", "t2"], "titles": ["One", "Two"]}],
                "read_at": 5}}});
        let v = view(&answer).expect("a companion");
        assert!(v.ready());
        assert_eq!((v.session_id.as_str(), v.generation), ("cmp_1", 2));
        let a = v.activity.expect("activity");
        assert!(a.run_active);
        assert_eq!(a.run_phase, "tool");
        assert_eq!(a.queued, vec![Queued { message_id: "m1".into(), delivery_status: "queued".into() }]);
        assert_eq!(a.pending_permissions, 1);
        assert_eq!(a.watches[0].machine, "lab");
        assert_eq!(a.watches[0].task_ids, vec!["t1", "t2"]);
        assert_eq!(activity_line(&a), "working (tool) · 1 message(s) queued for this turn · 1 approval(s) waiting · watching 2 task(s)");

        assert!(view(&json!({"ok": true, "companion": null})).is_none());
        let uncreated = view(&json!({"companion": {"session_id": "cmp_2", "generation": 3, "state": "uncreated"}})).unwrap();
        assert!(!uncreated.ready());
        assert!(uncreated.activity.is_none());
    }

    /// Requirement: the activity line tells the truth about a busy turn: a
    /// tool call in flight (on the device) that new input waits for, background
    /// results waiting for this turn or coming as their own, and a context that
    /// is catching up or may be stale. An answer from an older Swarm without
    /// those fields reads as before. Threat: a line claiming a message reaches
    /// the model at once while a device command runs, or hiding that the
    /// maintained context failed.
    #[test]
    fn companion_activity_line_reports_tool_calls_deliveries_and_freshness() {
        let answer = json!({"companion": {"session_id": "cmp", "generation": 1, "state": "ready",
            "activity": {"run": {"active": true, "phase": "provider_first_event"},
                "input_delivery": "after_tool_calls",
                "tool_calls": [{"call_id": "c1", "tool_name": "machine_run", "started_at": 3, "device": true}, {"call_id": "c2", "tool_name": "read", "started_at": 4}],
                "background_deliveries": [{"wake_id": "w1", "source": "task_watch", "run_id": "r1", "status": "waiting_for_run"},
                                          {"wake_id": "w2", "source": "background_command", "run_id": "r0", "status": "redelivering"}],
                "queued_inputs": [{"message_id": "m1", "delivery_status": "queued"}], "pending_permissions": 0, "task_watches": [],
                "context": {"cursor": 10, "latest_seq": 12, "pending": 2, "freshness": "pending", "running": true}}}});
        let a = view(&answer).unwrap().activity.unwrap();
        assert_eq!(a.input_delivery, "after_tool_calls");
        assert_eq!(a.tool_calls, vec![("machine_run".to_string(), true), ("read".to_string(), false)]);
        assert_eq!(a.context, Some(ContextFreshness { pending: 2, running: true, last_failure: String::new() }));
        assert_eq!(
            activity_line(&a),
            "working (provider_first_event) · running machine_run on the device +1; new messages wait for it · 1 message(s) queued for this turn · 1 background result(s) for this turn · 1 background result(s) coming as their own turn · context catching up (2 event(s))"
        );
        let failed = view(&json!({"companion": {"session_id": "cmp", "state": "ready", "activity": {"run": {"active": false},
            "context": {"pending": 0, "last_failure": {"at": 1, "error": "boom"}}}}}))
        .unwrap()
        .activity
        .unwrap();
        assert_eq!(activity_line(&failed), "context may be stale (maintenance failed)");
        let older = view(&json!({"companion": {"session_id": "cmp", "state": "ready", "activity": {"run": {"active": false}}}})).unwrap().activity.unwrap();
        assert!(older.context.is_none() && older.tool_calls.is_empty() && older.input_delivery.is_empty());
        assert_eq!(activity_line(&older), "");
    }

    /// Requirement: only Swarm's protected marker makes a session the
    /// Companion; an ordinary chat, an Orchestrator or a null marker is not.
    #[test]
    fn companion_session_is_recognised_by_its_marker_only() {
        assert!(is_companion(&json!({"id": "c", "metadata": {"swarm_v3_companion": {"generation": 1}, "navigation_hidden": true}})));
        assert!(!is_companion(&json!({"id": "o", "metadata": {"role": "project_orchestrator", "project_id": "p"}})));
        assert!(!is_companion(&json!({"id": "h", "metadata": {"navigation_hidden": true}})));
        assert!(!is_companion(&json!({"id": "n", "metadata": {"swarm_v3_companion": null}})));
        let s = session(&json!({"id": "c", "title": "Anything", "metadata": {"swarm_v3_companion": {}}}), &json!({"active": true}), false);
        assert_eq!(s.live.unwrap().session_id.as_ref(), "c");
        assert_eq!(s.title.as_ref(), "Companion");
        assert_eq!(s.state, RunState::Running);
        let idle = session(&json!({"id": "c", "metadata": {"swarm_v3_companion": {}}}), &Value::Null, true);
        assert_eq!(idle.state, RunState::NeedsYou);
    }

    /// Requirement: the work list shows only tasks this Companion started,
    /// across every project, grouped attention → running → results with the
    /// project they belong to; Swarm's derived state decides the group.
    /// Threat: listing another conversation's tasks, or losing the project
    /// id that approve/stop needs.
    #[test]
    fn companion_work_groups_its_own_tasks_across_projects() {
        let mut tasks: HashMap<String, Vec<Value>> = HashMap::new();
        tasks.insert(
            "p1".into(),
            vec![
                json!({"id": "t_run", "title": "Build", "status": "in_progress", "origin_session_id": "cmp", "updated_at": 30}),
                json!({"id": "t_other", "title": "Not ours", "status": "pending_approval", "origin_session_id": "orch"}),
                json!({"id": "t_done", "title": "Report", "status": "completed", "origin_session_id": "cmp", "updated_at": 10,
                       "progress": {"state": "done", "task_state": "done"}}),
            ],
        );
        tasks.insert(
            "p2".into(),
            vec![
                json!({"id": "t_ask", "title": "Approve me", "status": "pending_approval", "origin_session_id": "cmp", "updated_at": 5}),
                json!({"id": "t_infra", "title": "Step broke", "status": "completed", "origin_session_id": "cmp", "updated_at": 50,
                       "progress": {"state": "failed", "task_state": "blocked_infra"}}),
            ],
        );
        let projects = vec![project("p1", "Alpha"), project("p2", "Beta")];
        let w = work(&tasks, &projects, "cmp");
        let keys: Vec<String> = w.iter().map(Work::key).collect();
        assert_eq!(keys, vec!["p2/t_infra", "p2/t_ask", "p1/t_run", "p1/t_done"]);
        assert_eq!(w.iter().map(|x| x.group).collect::<Vec<_>>(), vec![Group::Attention, Group::Attention, Group::Running, Group::Done]);
        assert_eq!(w[1].project, "Beta");
        assert!(work(&tasks, &projects, "").is_empty());
        assert!(work(&tasks, &projects, "nobody").is_empty());
    }

    /// Requirement: watched tasks this machine cannot show (a linked
    /// machine's) still appear by exact identity; a local watched task that
    /// is already in the work list is not listed twice.
    #[test]
    fn companion_watches_elsewhere_keep_exact_identity() {
        let mut tasks: HashMap<String, Vec<Value>> = HashMap::new();
        tasks.insert("p1".into(), vec![json!({"id": "t1", "status": "in_progress", "origin_session_id": "cmp"})]);
        let w = work(&tasks, &[project("p1", "Alpha")], "cmp");
        let a = Activity {
            watches: vec![
                Watch { project_id: "p1".into(), machine: "".into(), task_ids: vec!["t1".into()], titles: vec![] },
                Watch { project_id: "p9".into(), machine: "lab".into(), task_ids: vec!["t1".into(), "t2".into()], titles: vec!["Remote".into()] },
            ],
            ..Default::default()
        };
        assert_eq!(
            watched_elsewhere(&a, &w),
            vec![("lab".to_string(), "p9".to_string(), "t1".to_string(), "Remote".to_string()), ("lab".to_string(), "p9".to_string(), "t2".to_string(), String::new())]
        );
    }
}
