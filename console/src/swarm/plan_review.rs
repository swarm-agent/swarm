//! A waiting task's plan, read and decided by the person (Swarm's
//! `/v3/task-plan-review`, swarmd/internal/api/task_plan_review.go).
//!
//! Swarm answers one review for this Swarm's own tasks and for the tasks a
//! conversation here sent to a linked machine: the task's structured plan
//! document, markdown Swarm derived from it, the exact plan version
//! (`plan_binding`) and what can be decided now. Accepting names that exact
//! version; Swarm reads the task again and refuses a plan that changed,
//! answering the current review instead, so the person sees the new version
//! and decides again. Nothing here accepts by itself.
//!
//! The conversation's linked tasks come from the home's durable records of
//! what it sent (`GET /v3/fleet/linked-tasks?session_id=`): they show as task
//! cards in that conversation and in the Companion's work, wherever the
//! person is, and survive a reconnect or restart because Swarm keeps them.

use serde_json::{Value, json};

use super::client::Refusal;
use super::map::{self, list, s};
use crate::data::{Progress, ProgressState, ReviewRef, Task, TaskState, TaskStatus};

pub const READ_PATH: &str = "/v3/task-plan-review";
pub const DECISION_PATH: &str = "/v3/task-plan-review/decision";

/// What the person can decide on the task now (Swarm's `decision`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Decision {
    /// Accept the bound plan at exactly its current version.
    AcceptPlan,
    /// An ordinary task (no plan) waits to start.
    Start,
    /// Its plan could not be read whole: nothing can be accepted.
    Reload,
    /// Nothing waits on the person.
    None,
}

impl Decision {
    fn of(text: &str) -> Decision {
        match text {
            "accept_plan" => Decision::AcceptPlan,
            "start" => Decision::Start,
            "reload" => Decision::Reload,
            _ => Decision::None,
        }
    }
}

/// The exact plan version a decision names.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Binding {
    pub session_id: String,
    pub plan_id: String,
    pub revision: u64,
}

/// A task's plan review as Swarm answered it.
#[derive(Clone, Debug, PartialEq)]
pub struct Review {
    /// The linked machine; empty for this Swarm.
    pub machine: String,
    pub project_id: String,
    pub task_id: String,
    pub title: String,
    pub run_status: String,
    pub plan_bound: bool,
    pub binding: Option<Binding>,
    /// The plan's title, from its document.
    pub plan_title: String,
    /// Markdown Swarm derived from the plan document, field by field.
    pub markdown: String,
    pub decision: Decision,
    /// Why nothing can be decided, in Swarm's words.
    pub problem: String,
    pub last_error: String,
}

impl Review {
    /// Where the task is: "this Swarm" or "machine forge", with project and task.
    pub fn place(&self) -> String {
        let machine = if self.machine.is_empty() { "this Swarm".to_string() } else { format!("machine {}", self.machine) };
        format!("{machine} · project {} · task {}", self.project_id, self.task_id)
    }

    /// The version line: "version 2 of plan plan-a".
    pub fn version(&self) -> String {
        match &self.binding {
            Some(b) => format!("version {} of plan {}", b.revision, b.plan_id),
            None => String::new(),
        }
    }

    pub fn target(&self) -> ReviewRef {
        ReviewRef { machine: self.machine.clone().into(), project_id: self.project_id.clone().into(), task_id: self.task_id.clone().into(), plan: self.plan_bound }
    }
}

/// The `review` of a review or decision answer; None when there is none.
pub fn parse(answer: &Value) -> Option<Review> {
    let r = answer.get("review").filter(|r| r.is_object())?;
    if s(r, "task_id").is_empty() {
        return None;
    }
    let binding = r.get("plan_binding").filter(|b| b.is_object()).and_then(|b| {
        let revision = b.get("definition_revision").and_then(Value::as_u64).unwrap_or(0);
        (!s(b, "plan_id").is_empty() && !s(b, "session_id").is_empty() && revision > 0).then(|| Binding {
            session_id: s(b, "session_id").to_string(),
            plan_id: s(b, "plan_id").to_string(),
            revision,
        })
    });
    let markdown = s(r, "plan_markdown").to_string();
    let decision = Decision::of(s(r, "decision"));
    // A plan offered for acceptance must carry its plan: otherwise it is
    // treated as unreadable, never accepted blind.
    let (decision, problem) = if decision == Decision::AcceptPlan && (binding.is_none() || markdown.trim().is_empty()) {
        (Decision::Reload, "Swarm offered a plan to accept without its plan or version; read it again.".to_string())
    } else {
        (decision, s(r, "problem").to_string())
    };
    Some(Review {
        machine: s(r, "machine").to_string(),
        project_id: s(r, "project_id").to_string(),
        task_id: s(r, "task_id").to_string(),
        title: s(r, "title").to_string(),
        run_status: s(r, "run_status").to_string(),
        plan_bound: r.get("plan_bound").and_then(Value::as_bool).unwrap_or(false),
        binding,
        plan_title: r.get("plan_document").map(|d| s(d, "title").to_string()).unwrap_or_default(),
        markdown,
        decision,
        problem,
        last_error: s(r, "last_error").to_string(),
    })
}

/// Percent-encodes a query value.
pub(crate) fn query_value(v: &str) -> String {
    let mut out = String::new();
    for b in v.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

/// The review route for a task.
pub fn read_path(target: &ReviewRef) -> String {
    let mut path = format!("{READ_PATH}?project_id={}&task_id={}", query_value(&target.project_id), query_value(&target.task_id));
    if !target.machine.is_empty() {
        path.push_str(&format!("&machine={}", query_value(&target.machine)));
    }
    path
}

/// The person's decision, as Swarm's decision route reads it. Accepting a
/// plan names exactly the version shown; an ordinary task is started with no
/// plan named; asking for changes needs what to change. Anything else is
/// refused here, before it is sent.
pub fn decision_body(review: &Review, accept: bool, feedback: &str) -> Result<Value, String> {
    let mut body = json!({"project_id": review.project_id, "task_id": review.task_id, "decision": if accept { "accept" } else { "revise" }});
    if !review.machine.is_empty() {
        body["machine"] = json!(review.machine);
    }
    match (review.decision, accept) {
        (Decision::AcceptPlan, _) => {
            let b = review.binding.as_ref().ok_or("The plan's version is not known; read it again.")?;
            body["plan_binding"] = json!({"session_id": b.session_id, "plan_id": b.plan_id, "definition_revision": b.revision});
        }
        (Decision::Start, true) => {}
        (Decision::Start, false) => return Err("This task has no plan to change.".into()),
        (Decision::Reload, _) => return Err(if review.problem.is_empty() { "Read the plan again before deciding.".into() } else { review.problem.clone() }),
        (Decision::None, _) => return Err(if review.problem.is_empty() { "Nothing waits for you on this task.".into() } else { review.problem.clone() }),
    }
    if !accept {
        let feedback = feedback.trim();
        if feedback.is_empty() {
            return Err("Say what to change first.".into());
        }
        body["feedback"] = json!(feedback);
    }
    Ok(body)
}

/// What a decision's answer says is known (the decision route's answers,
/// task_plan_review.go). Only `Done` says the decision was carried out.
#[derive(Clone, Debug, PartialEq)]
pub enum Decided {
    /// Carried out on exactly `binding` (None: an ordinary task started).
    /// `review` is the task read after it; None when that read failed
    /// (`refresh_error`): its state is then unknown, never the one before.
    Done { binding: Option<Binding>, review: Option<Review>, refresh_error: String },
    /// Refused because of the task's state (409): the plan changed, the task
    /// started. `review` is the current one, when Swarm could read it.
    Refused { error: String, review: Option<Review> },
    /// Refused for another reason (not allowed, not found, a bad request):
    /// nothing was done.
    Denied { status: u16, error: String },
    /// Sent, and no answer says whether it was carried out (the link or
    /// Swarm did not answer). Never retried: the task is read again.
    Unknown { error: String },
}

fn binding_of(v: Option<&Value>) -> Option<Binding> {
    let b = v.filter(|b| b.is_object())?;
    let revision = b.get("definition_revision").and_then(Value::as_u64).unwrap_or(0);
    (!s(b, "plan_id").is_empty() && revision > 0).then(|| Binding { session_id: s(b, "session_id").to_string(), plan_id: s(b, "plan_id").to_string(), revision })
}

/// Sorts the decision route's answer.
pub fn decision_outcome(result: Result<Value, Refusal>) -> Decided {
    match result {
        Ok(answer) if answer.get("ok").and_then(Value::as_bool) == Some(true) => {
            let current = answer.get("review_current").and_then(Value::as_bool).unwrap_or(false);
            Decided::Done {
                binding: binding_of(answer.get("decided").and_then(|d| d.get("plan_binding"))),
                review: if current { parse(&answer) } else { None },
                refresh_error: if current { String::new() } else { s(&answer, "refresh_error").to_string() },
            }
        }
        // A 2xx that does not say ok is not a known outcome.
        Ok(_) => Decided::Unknown { error: "Swarm's answer did not say whether the decision was carried out.".into() },
        Err(Refusal::Status(409, error, answer)) => {
            let current = answer.get("review_current").and_then(Value::as_bool).unwrap_or(false);
            Decided::Refused { error, review: if current { parse(&answer) } else { None } }
        }
        Err(Refusal::Status(code, error, answer)) if code >= 500 || answer.get("outcome_unknown").and_then(Value::as_bool) == Some(true) => {
            Decided::Unknown { error: if error.is_empty() { format!("Swarm answered HTTP {code}.") } else { error } }
        }
        Err(Refusal::Status(code, error, _)) => Decided::Denied { status: code, error },
        Err(Refusal::Forbidden(error)) => Decided::Denied { status: 403, error: if error.is_empty() { "Swarm refused that.".into() } else { error } },
        Err(e @ Refusal::SignedOut) => Decided::Denied { status: 401, error: e.to_string() },
        Err(e @ Refusal::Unreachable) => Decided::Unknown { error: e.to_string() },
    }
}

/// A review read's or decision's answer, for the request that asked it.
#[derive(Clone, Debug, PartialEq)]
pub enum Answer {
    Read(Result<Value, String>),
    Decided(Decided),
}

/// The linked-task records of one conversation
/// (`GET /v3/fleet/linked-tasks?session_id=`).
pub fn linked_records(answer: &Value) -> Vec<Value> {
    list(answer, "linked_tasks").iter().filter(|r| !s(r, "task_id").is_empty() && !s(r, "machine").is_empty()).cloned().collect()
}

/// A linked-task record as a task card: on its machine, with its last
/// observed status. A task waiting there opens its review; nothing on the
/// card decides it.
pub fn linked_task(r: &Value) -> Task {
    let status_text = s(r, "status");
    let missing = r.get("missing").and_then(Value::as_bool).unwrap_or(false);
    let archived = r.get("archived").and_then(Value::as_bool).unwrap_or(false);
    let waiting = status_text == "pending_approval" && !missing && !archived;
    // Only a task still waiting on its machine shows as waiting: an archived
    // one is not, so no card offers this Swarm's approve/reject for it.
    let status = if missing {
        TaskStatus::Failed
    } else if archived {
        map::task_status("archived")
    } else {
        map::task_status(status_text)
    };
    let machine = s(r, "machine").to_string();
    let activity = if missing {
        format!("{machine} answered that this task no longer exists")
    } else if archived {
        format!("archived on {machine}")
    } else {
        [s(r, "last_error"), s(r, "action_needed"), s(r, "progress")].into_iter().find(|x| !x.trim().is_empty()).unwrap_or("").lines().next().unwrap_or("").to_string()
    };
    let title = if s(r, "title").trim().is_empty() { format!("task {}", s(r, "task_id")) } else { s(r, "title").trim().to_string() };
    let updated = r.get("observed_at").and_then(Value::as_u64).unwrap_or(0).max(r.get("created_at").and_then(Value::as_u64).unwrap_or(0));
    Task {
        key: s(r, "task_id").to_string().into(),
        // Its session is on its machine: nothing here stops or opens it.
        session: "".into(),
        title: title.into(),
        role: format!("on {machine}").into(),
        machine: 0,
        status,
        activity: activity.into(),
        runs: vec![],
        report: "".into(),
        progress: Progress {
            state: match status {
                TaskStatus::PendingApproval | TaskStatus::Queued => ProgressState::Waiting,
                TaskStatus::Running => ProgressState::Working,
                TaskStatus::Failed => ProgressState::Failed,
                _ => ProgressState::Unknown,
            },
            task_state: if waiting { TaskState::NeedsYou } else { TaskState::Unknown },
            task_state_reason: if waiting { format!("waits for you on {machine}").into() } else { "".into() },
            updated_at: updated,
            ..Default::default()
        },
        review: waiting.then(|| ReviewRef {
            machine: machine.clone().into(),
            project_id: s(r, "project_id").to_string().into(),
            task_id: s(r, "task_id").to_string().into(),
            plan: false,
        }),
    }
}

/// A task's review reference from this Swarm's own task record, when it
/// waits to start: plan-bound when it has a plan binding or is a plan task.
pub fn home_review(t: &Value, project_id: &str) -> Option<ReviewRef> {
    if s(t, "status") != "pending_approval" || s(t, "id").is_empty() {
        return None;
    }
    let bound = t.get("plan_binding").is_some_and(|b| !s(b, "plan_id").is_empty());
    let plan = bound || s(t, "agent") == "plan" || s(t, "outcome_type") == "plan_spec";
    let project = if s(t, "project_id").is_empty() { project_id } else { s(t, "project_id") };
    Some(ReviewRef { machine: "".into(), project_id: project.to_string().into(), task_id: s(t, "id").to_string().into(), plan })
}

/// The linked tasks as cards, one per machine/project/task, newest first.
pub fn linked_tasks(records: &[Value]) -> Vec<Task> {
    let mut seen = std::collections::HashSet::new();
    let mut out: Vec<Task> =
        records.iter().filter(|r| seen.insert((s(r, "machine").to_string(), s(r, "project_id").to_string(), s(r, "task_id").to_string()))).map(linked_task).collect();
    out.sort_by(|a, b| b.progress.updated_at.cmp(&a.progress.updated_at));
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Swarm's review answer as task_plan_review.go writes it.
    fn answer(decision: &str, revision: u64) -> Value {
        json!({"review": {
            "machine": "forge", "project_id": "proj 1", "task_id": "t/1", "title": "Plan task", "run_status": "pending_approval",
            "plan_bound": true, "plan_binding": {"session_id": "s1", "plan_id": "plan-a", "definition_revision": revision},
            "plan_document": {"id": "plan-a", "title": "Exact plan", "info": {"goal": "The goal"}},
            "plan_markdown": "# Exact plan\n\n## Objective\n\nThe goal\n", "decision": decision, "untrusted_content": true
        }})
    }

    // Purpose: the console shows the plan Swarm reviewed (the document's own
    // markdown, its exact version, the machine, project and task) and sends
    // back exactly that version; a refusal keeps the current review so the
    // person decides again. Threat: the old empty-body approve, an approve of
    // a version nobody saw, or a plan acceptance on a review without the
    // plan. Owner: parse, decision_body, decided. Narrowest layer: the pure
    // mapping the GPUI panel and commands use.
    #[test]
    fn a_plan_review_carries_its_plan_and_exact_version() {
        let r = parse(&answer("accept_plan", 2)).unwrap();
        assert_eq!(r.decision, Decision::AcceptPlan);
        assert_eq!(r.place(), "machine forge · project proj 1 · task t/1");
        assert_eq!(r.version(), "version 2 of plan plan-a");
        assert_eq!(r.plan_title, "Exact plan");
        assert!(r.markdown.contains("## Objective"));

        let body = decision_body(&r, true, "").unwrap();
        assert_eq!(
            body,
            json!({"machine": "forge", "project_id": "proj 1", "task_id": "t/1", "decision": "accept",
            "plan_binding": {"session_id": "s1", "plan_id": "plan-a", "definition_revision": 2}})
        );
        assert!(body.get("plan_binding").is_some(), "never an empty body for a plan");

        assert_eq!(decision_body(&r, false, "  ").unwrap_err(), "Say what to change first.");
        let revise = decision_body(&r, false, "split step two").unwrap();
        assert_eq!((revise["decision"].as_str(), revise["feedback"].as_str()), (Some("revise"), Some("split step two")));
        assert_eq!(revise["plan_binding"]["definition_revision"], 2);

        // The route encodes the task's place.
        assert_eq!(read_path(&r.target()), "/v3/task-plan-review?project_id=proj%201&task_id=t%2F1&machine=forge");

        // An accept_plan without the plan or its version is never acceptable.
        let mut bare = answer("accept_plan", 2);
        bare["review"]["plan_markdown"] = json!("");
        let bare = parse(&bare).unwrap();
        assert_eq!(bare.decision, Decision::Reload);
        assert!(decision_body(&bare, true, "").is_err());
        let mut unversioned = answer("accept_plan", 0);
        unversioned["review"]["plan_binding"]["definition_revision"] = json!(0);
        assert!(decision_body(&parse(&unversioned).unwrap(), true, "").is_err());

        // A refusal (409) keeps the current review: the new version to decide on.
        let refused = json!({"ok": false, "error": "the plan changed: ... (current definition_revision 3)", "review_current": true, "review": answer("accept_plan", 3)["review"]});
        match decision_outcome(Err(Refusal::Status(409, "the plan changed: ... (current definition_revision 3)".into(), refused))) {
            Decided::Refused { error, review } => {
                assert!(error.contains("plan changed"));
                assert_eq!(review.unwrap().binding.unwrap().revision, 3);
            }
            other => panic!("a 409 is a refusal: {other:?}"),
        }
        // Nothing left to decide.
        let started =
            parse(&json!({"review": {"project_id": "p", "task_id": "t", "run_status": "in_progress", "decision": "none", "problem": "the task is in_progress"}})).unwrap();
        assert_eq!(decision_body(&started, true, "").unwrap_err(), "the task is in_progress");
    }

    // Purpose: the console tells the person only what Swarm's decision
    // answer says. Requirements: ok with review_current is Done with the
    // review after it and the exact version decided; ok without
    // review_current is Done with no review (the state is unknown, never the
    // pending one before); 409 is a refusal carrying the current review only
    // when Swarm says it is current; 403/404/400 are refusals that did
    // nothing; 502 outcome_unknown, any 5xx, no answer, or a 2xx without ok
    // are Unknown. Threat: "Accepted ... pending approval" with accept
    // offered again, a lost answer taken as success or as failure, every
    // failure shown as a plan change. Owner: decision_outcome. Narrowest
    // layer: the pure mapping of the route's answers.
    #[test]
    fn a_decision_answer_says_only_what_is_known() {
        let done = decision_outcome(Ok(json!({"ok": true, "decision": "accept", "review_current": true,
            "decided": {"machine": "forge", "project_id": "proj 1", "task_id": "t/1", "plan_binding": {"session_id": "s1", "plan_id": "plan-a", "definition_revision": 2}},
            "review": {"machine": "forge", "project_id": "proj 1", "task_id": "t/1", "run_status": "in_progress", "decision": "none"}})));
        match done {
            Decided::Done { binding: Some(b), review: Some(r), refresh_error } => {
                assert_eq!((b.plan_id.as_str(), b.revision, r.run_status.as_str(), refresh_error.as_str()), ("plan-a", 2, "in_progress", ""));
            }
            other => panic!("done: {other:?}"),
        }
        // Decided, the read after it failed: no review, even if one is sent.
        let stale = decision_outcome(Ok(json!({"ok": true, "review_current": false, "refresh_error": "box did not answer",
            "decided": {"project_id": "p", "task_id": "t", "plan_binding": {"session_id": "s", "plan_id": "p", "definition_revision": 4}},
            "review": answer("accept_plan", 4)["review"]})));
        assert_eq!(
            stale,
            Decided::Done { binding: Some(Binding { session_id: "s".into(), plan_id: "p".into(), revision: 4 }), review: None, refresh_error: "box did not answer".into() }
        );
        // A 409 whose review Swarm could not read again carries none.
        assert_eq!(
            decision_outcome(Err(Refusal::Status(409, "accept refused".into(), json!({"ok": false, "review_current": false})))),
            Decided::Refused { error: "accept refused".into(), review: None }
        );
        // Refused for permission or a missing task: nothing done, status kept.
        assert_eq!(decision_outcome(Err(Refusal::Forbidden("linked work on forge is off".into()))), Decided::Denied { status: 403, error: "linked work on forge is off".into() });
        assert_eq!(
            decision_outcome(Err(Refusal::Status(404, "no task t sent to forge from here".into(), Value::Null))),
            Decided::Denied { status: 404, error: "no task t sent to forge from here".into() }
        );
        // Unknown: the link timed out, Swarm failed, nothing answered.
        assert!(matches!(
            decision_outcome(Err(Refusal::Status(502, "the accept was sent but its outcome is unknown".into(), json!({"outcome_unknown": true})))),
            Decided::Unknown { .. }
        ));
        assert!(matches!(decision_outcome(Err(Refusal::Status(500, String::new(), Value::Null))), Decided::Unknown { .. }));
        assert!(matches!(decision_outcome(Err(Refusal::Unreachable)), Decided::Unknown { .. }));
        assert!(matches!(decision_outcome(Ok(json!({"review": answer("accept_plan", 2)["review"]}))), Decided::Unknown { .. }));
    }

    // Purpose: an ordinary task keeps its start decision (no plan named) and
    // a home task waiting to start gets a review reference only when it
    // waits; a plan-bound one is marked so its card opens the plan. Owner:
    // decision_body, home_review. Narrowest layer: the pure mapping.
    #[test]
    fn ordinary_tasks_start_without_a_plan() {
        let plain = parse(&json!({"review": {"project_id": "p", "task_id": "t", "run_status": "pending_approval", "decision": "start", "plan_bound": false}})).unwrap();
        assert_eq!(decision_body(&plain, true, "").unwrap(), json!({"project_id": "p", "task_id": "t", "decision": "accept"}));
        assert!(decision_body(&plain, false, "change it").is_err());

        let bound = json!({"id": "t1", "status": "pending_approval", "plan_binding": {"plan_id": "plan-a", "session_id": "s", "definition_revision": 1}});
        assert_eq!(home_review(&bound, "p1"), Some(ReviewRef { machine: "".into(), project_id: "p1".into(), task_id: "t1".into(), plan: true }));
        let ordinary = json!({"id": "t2", "status": "pending_approval", "agent": "coder"});
        assert_eq!(home_review(&ordinary, "p1").map(|r| r.plan), Some(false));
        assert_eq!(home_review(&json!({"id": "t3", "status": "in_progress"}), "p1"), None);
    }

    // Purpose: a task a conversation sent to a linked machine shows in that
    // conversation (and the Companion's work) from the home's durable
    // records, once per task however often the tool result repeats, with
    // its machine and a review that opens only while it waits. Threat: the
    // person told of a plan card no surface shows, or duplicate cards.
    // Owner: linked_records, linked_tasks, linked_task. Narrowest layer:
    // the pure mapping of the records route's answer.
    #[test]
    fn linked_records_become_one_card_each() {
        let answer = json!({"count": 3, "untrusted_content": true, "linked_tasks": [
            {"machine": "forge", "project_id": "rp", "task_id": "rt", "title": "Remote plan", "home_session_id": "s1", "status": "pending_approval", "created_at": 10, "observed_at": 20},
            {"machine": "forge", "project_id": "rp", "task_id": "rt", "title": "Remote plan", "home_session_id": "s1", "status": "pending_approval", "created_at": 10},
            {"machine": "box", "project_id": "bp", "task_id": "done", "title": "Finished", "status": "completed", "created_at": 5},
            {"machine": "box", "project_id": "bp", "task_id": "gone", "status": "pending_approval", "missing": true, "created_at": 1}
        ]});
        let records = linked_records(&answer);
        let cards = linked_tasks(&records);
        assert_eq!(cards.len(), 3, "the repeated record shows once");
        let plan = &cards[0];
        assert_eq!((plan.key.as_ref(), plan.title.as_ref(), plan.role.as_ref()), ("rt", "Remote plan", "on forge"));
        assert_eq!(plan.status, TaskStatus::PendingApproval);
        assert_eq!(plan.progress.task_state, TaskState::NeedsYou);
        assert_eq!(plan.review, Some(ReviewRef { machine: "forge".into(), project_id: "rp".into(), task_id: "rt".into(), plan: false }));
        let done = cards.iter().find(|t| t.key.as_ref() == "done").unwrap();
        assert_eq!((done.status, done.review.is_none()), (TaskStatus::Completed, true));
        let gone = cards.iter().find(|t| t.key.as_ref() == "gone").unwrap();
        assert!(gone.review.is_none(), "a task its machine says is gone has nothing to decide");
        assert!(gone.activity.contains("no longer exists"));
    }

    // Purpose: a linked task's card never offers this Swarm's approve/reject.
    // Invariant: a linked card is PendingApproval only while its review is
    // attached, so an archived record that still says pending_approval (the
    // home keeps archived linked records) is not waiting and has no review.
    // Threat: generic approve/reject that posts an empty-body approve to the
    // home project with the remote task id, or a Companion button that does
    // nothing. Owner: linked_task. Narrowest layer: the pure record mapping
    // that task_actions and the Companion card read.
    #[test]
    fn an_archived_linked_task_offers_no_decision() {
        let records = linked_records(&json!({"linked_tasks": [
            {"machine": "box", "project_id": "bp", "task_id": "old", "title": "Old plan", "status": "pending_approval", "archived": true, "created_at": 3},
            {"machine": "box", "project_id": "bp", "task_id": "gone", "status": "pending_approval", "missing": true, "archived": true, "created_at": 2},
            {"machine": "box", "project_id": "bp", "task_id": "live", "status": "pending_approval", "created_at": 1}
        ]}));
        let cards = linked_tasks(&records);
        assert_eq!(cards.len(), 3);
        for card in &cards {
            assert_eq!(card.status == TaskStatus::PendingApproval, card.review.is_some(), "{}: waiting only with its review", card.key);
        }
        let old = cards.iter().find(|t| t.key.as_ref() == "old").unwrap();
        assert_eq!((old.status, old.review.is_none()), (TaskStatus::Rejected, true));
        assert!(!old.status.needs_you());
        assert_eq!(old.progress.task_state, TaskState::Unknown);
        assert!(old.activity.contains("archived"));
        let live = cards.iter().find(|t| t.key.as_ref() == "live").unwrap();
        assert!(live.review.as_ref().is_some_and(|r| r.opens_review()), "a waiting linked task still opens its review");
    }
}
