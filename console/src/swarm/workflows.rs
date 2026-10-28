//! Workflows on a Swarm: what runs when a task of Swarm's own agents or a
//! worker job ends (review, send back, deliver, a script), as Swarm keeps
//! them. GET /v3/workflows answers each workflow with its steps and its
//! latest deliveries. Read only; the Orchestrator makes and changes them.

use serde_json::Value;

use super::client::Client;

/// One workflow, as Swarm runs it.
#[derive(Clone, Debug, PartialEq)]
pub struct Workflow {
    pub id: String,
    pub name: String,
    pub revision: u64,
    pub enabled: bool,
    /// active, disabled or proposed (waits for the person to accept or discard).
    pub state: String,
    pub scope: Scope,
    /// succeeded, failed or always.
    pub when: String,
    pub steps: Vec<Step>,
    /// Newest first.
    pub deliveries: Vec<Delivery>,
    /// An outside event that starts a worker's job, when the workflow has one.
    pub on: Option<EventTrigger>,
    /// The trigger's recent events, newest first.
    pub events: Vec<TriggerEvent>,
}

/// What starts the workflow from outside Swarm: a feed Swarm reads (pull) or a
/// signed request to it (webhook). Each new item starts one job of `worker`.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct EventTrigger {
    /// pull or webhook.
    pub source: String,
    pub url: String,
    pub every_seconds: u64,
    /// The secret's name only; Swarm never shows its value.
    pub secret: String,
    /// The item field that identifies an event.
    pub id_field: String,
    pub worker: String,
    pub brief: String,
}

/// One outside event and what Swarm did with it.
#[derive(Clone, Debug, PartialEq)]
pub struct TriggerEvent {
    pub item_id: String,
    pub received_at: u64,
    /// queued, started or failed.
    pub state: String,
    pub job_id: String,
    pub error: String,
}

/// The work whose end starts it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Scope {
    pub folder: String,
    pub role: String,
    pub worker: String,
    pub job: String,
    pub schedule: String,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Step {
    pub id: String,
    /// agent, reopen, run or deliver.
    pub kind: String,
    /// agent: one of Swarm's own agents or a worker id.
    pub worker: String,
    pub brief: String,
    pub fixes: String,
    pub checkout: String,
    /// reopen: the step whose findings go back, and how many rounds.
    pub from: String,
    pub max_rounds: u64,
    /// run: the script.
    pub command: String,
    /// deliver: where to.
    pub to: String,
    pub path: String,
    /// Conditions that must all hold, in words.
    pub conditions: Vec<String>,
}

/// One time the workflow ran for a job.
#[derive(Clone, Debug, PartialEq)]
pub struct Delivery {
    pub id: String,
    pub job_id: String,
    pub state: String,
    pub round: u64,
    pub created_at: u64,
    pub blocked_reason: String,
    pub steps: Vec<StepRun>,
}

#[derive(Clone, Debug, PartialEq)]
pub struct StepRun {
    pub id: String,
    pub state: String,
    /// ok or block, for an agent step that recorded one.
    pub verdict: String,
}

/// Every workflow of the account, and whether Swarm's stop switch is on.
pub fn read(client: &Client) -> Result<(Vec<Workflow>, bool), String> {
    let v = client.get("/v3/workflows").map_err(|e| e.to_string())?;
    let list = v.get("automations").and_then(Value::as_array).cloned().unwrap_or_default();
    let stopped = v.get("stopped").and_then(Value::as_bool).unwrap_or(false);
    Ok((list.iter().map(workflow).collect(), stopped))
}

fn text(v: &Value, key: &str) -> String {
    v.get(key).and_then(Value::as_str).unwrap_or("").to_string()
}

fn num(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

fn workflow(a: &Value) -> Workflow {
    let scope = a.get("scope").cloned().unwrap_or(Value::Null);
    let mut deliveries: Vec<Delivery> = a.get("latest_deliveries").and_then(Value::as_array).map(|d| d.iter().map(delivery).collect()).unwrap_or_default();
    deliveries.sort_by(|x, y| y.created_at.cmp(&x.created_at));
    Workflow {
        id: text(a, "id"),
        name: text(a, "name"),
        revision: num(a, "revision"),
        enabled: a.get("enabled").and_then(Value::as_bool).unwrap_or(false),
        state: text(a, "state"),
        scope: Scope {
            folder: text(&scope, "folder"),
            role: text(&scope, "role"),
            worker: text(&scope, "worker"),
            job: text(&scope, "job"),
            schedule: text(&scope, "schedule"),
        },
        when: text(a, "when"),
        steps: a.get("steps").and_then(Value::as_array).map(|s| s.iter().map(step).collect()).unwrap_or_default(),
        deliveries,
        on: a.get("on").filter(|o| o.is_object()).map(|o| {
            let event = o.get("event").cloned().unwrap_or(Value::Null);
            let start = o.get("start").cloned().unwrap_or(Value::Null);
            EventTrigger {
                source: text(&event, "source"),
                url: text(&event, "url"),
                every_seconds: num(&event, "every_seconds"),
                secret: text(&event, "secret"),
                id_field: text(&event, "id"),
                worker: text(&start, "worker"),
                brief: text(&start, "brief"),
            }
        }),
        events: {
            let mut events: Vec<TriggerEvent> = a
                .get("events")
                .and_then(Value::as_array)
                .map(|e| {
                    e.iter()
                        .map(|e| TriggerEvent {
                            item_id: text(e, "item_id"),
                            received_at: num(e, "received_at"),
                            state: text(e, "state"),
                            job_id: text(e, "job_id"),
                            error: text(e, "error"),
                        })
                        .collect()
                })
                .unwrap_or_default();
            events.sort_by(|x, y| y.received_at.cmp(&x.received_at));
            events
        },
    }
}

fn step(s: &Value) -> Step {
    let conditions = s
        .get("if")
        .and_then(Value::as_array)
        .map(|c| {
            c.iter()
                .map(|c| {
                    let on = text(c, "step");
                    match (text(c, "verdict").as_str(), text(c, "status").as_str()) {
                        ("block", _) => format!("if {on} blocks"),
                        ("ok", _) => format!("if {on} is OK"),
                        (_, "failed") => format!("if {on} failed"),
                        (_, "succeeded") => format!("if {on} succeeded"),
                        _ => format!("after {on}"),
                    }
                })
                .collect()
        })
        .unwrap_or_default();
    Step {
        id: text(s, "id"),
        kind: text(s, "type"),
        worker: text(s, "worker"),
        brief: text(s, "brief"),
        fixes: text(s, "fixes"),
        checkout: text(s, "checkout"),
        from: text(s, "from"),
        max_rounds: num(s, "max_rounds"),
        command: text(s, "command"),
        to: text(s, "to"),
        path: text(s, "path"),
        conditions,
    }
}

fn delivery(d: &Value) -> Delivery {
    Delivery {
        id: text(d, "delivery_id"),
        job_id: text(d, "job_id"),
        state: text(d, "state"),
        round: num(d, "round"),
        created_at: num(d, "created_at"),
        blocked_reason: text(d, "blocked_reason"),
        steps: d
            .get("steps")
            .and_then(Value::as_array)
            .map(|s| {
                s.iter()
                    .map(|s| StepRun {
                        id: text(s, "id"),
                        state: text(s, "state"),
                        // The listing's one-line delivery carries the verdict
                        // itself; a detailed delivery keeps it in its outcome.
                        verdict: s.get("verdict").and_then(Value::as_str).map(str::to_string).unwrap_or_else(|| s.get("outcome").map(|o| text(o, "verdict")).unwrap_or_default()),
                    })
                    .collect()
            })
            .unwrap_or_default(),
    }
}

/// Accepts a proposed workflow at the revision the person saw: it runs from now on.
pub fn accept(client: &Client, id: &str, revision: u64) -> Result<Value, String> {
    client.post(&format!("/v3/workflows/{id}/accept"), serde_json::json!({"expected_revision": revision})).map_err(|e| e.to_string())
}

/// Discards a proposed workflow.
pub fn discard(client: &Client, id: &str) -> Result<Value, String> {
    client.delete(&format!("/v3/workflows/{id}")).map_err(|e| e.to_string())
}

/// The last part of a folder path, for saying where.
fn folder_name(folder: &str) -> &str {
    folder.trim_end_matches('/').rsplit('/').next().unwrap_or(folder)
}

/// What starts the workflow, in words. `worker_name` turns a worker id into
/// its name.
pub fn trigger(w: &Workflow, worker_name: &dyn Fn(&str) -> String) -> String {
    if let Some(on) = &w.on {
        let place = folder_name(&w.scope.folder);
        let what = match on.source.as_str() {
            "webhook" => "a signed request comes in".to_string(),
            _ => {
                let host = on.url.split("://").nth(1).unwrap_or(&on.url).split('/').next().unwrap_or("");
                let every = match on.every_seconds {
                    0 => String::new(),
                    s if s % 60 == 0 => format!(" every {} min", s / 60),
                    s => format!(" every {s} s"),
                };
                format!("a new item shows up in {host}'s feed (checked{every})")
            }
        };
        return format!("When {what}: worker {} starts a job in {place}", worker_name(&on.worker));
    }
    let end = match w.when.as_str() {
        "failed" => "fails",
        "always" => "ends",
        _ => "succeeds",
    };
    let place = folder_name(&w.scope.folder);
    let s = &w.scope;
    if !s.job.is_empty() {
        format!("When job {} {end}", s.job)
    } else if !s.schedule.is_empty() {
        format!("When a run of schedule {} in {place} {end}", s.schedule)
    } else if !s.worker.is_empty() {
        format!("When a job of worker {} in {place} {end}", worker_name(&s.worker))
    } else if !s.role.is_empty() {
        format!("When a {} task in {place} {end}", s.role)
    } else {
        format!("When any task or job in {place} {end}")
    }
}

/// What a step does, in words.
pub fn describe(s: &Step, worker_name: &dyn Fn(&str) -> String) -> String {
    match s.kind.as_str() {
        "agent" => {
            let who = match s.worker.as_str() {
                "reviewer" => "Swarm's reviewer".to_string(),
                "coder" | "designer" | "finder" | "swarm" => format!("Swarm's {}", s.worker),
                id => format!("worker {}", worker_name(id)),
            };
            let where_ = match s.checkout.as_str() {
                "new" => ", in its own copy at the work's head",
                "none" => ", in the folder",
                "same" => ", read-only where the work was done",
                _ => "",
            };
            let fixes = if s.fixes == "security" { ", may commit small security fixes" } else { "" };
            format!("{who}{where_}{fixes}")
        }
        "reopen" => {
            let rounds = if s.max_rounds > 0 { s.max_rounds } else { 2 };
            let from = if s.from.is_empty() { String::new() } else { format!(" with the findings of {}", s.from) };
            format!("send the work back{from}, at most {rounds} rounds")
        }
        "run" => format!("run the machine's script {}", s.command),
        "deliver" => match s.to.as_str() {
            "folder" => format!("copy the deliverables into {}", if s.path.is_empty() { "the folder" } else { &s.path }),
            "merge" => "merge the work into the folder's branch".to_string(),
            "publish" => "open a pull request".to_string(),
            "outbox" => "put the deliverables in the machine's outbox".to_string(),
            other => format!("deliver to {other}"),
        },
        other => other.to_string(),
    }
}

/// A delivery's steps as a chain: review block → send-back → review OK → push.
/// Skipped and waiting steps are left out.
pub fn chain(d: &Delivery) -> String {
    d.steps
        .iter()
        .filter(|s| !matches!(s.state.as_str(), "skipped" | "pending" | ""))
        .map(|s| match (s.verdict.as_str(), s.state.as_str()) {
            ("ok", _) => format!("{} OK", s.id),
            ("block", _) => format!("{} blocked", s.id),
            (_, "succeeded") => s.id.clone(),
            (_, "running") => format!("{} running", s.id),
            (_, state) => format!("{} {state}", s.id),
        })
        .collect::<Vec<_>>()
        .join(" → ")
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn loop_json() -> Value {
        json!({
            "id": "jauto_1", "name": "swarm-go review loop", "revision": 6, "enabled": true, "when": "succeeded",
            "scope": {"folder": "/srv/work/swarm-go", "worker": "worker_c"},
            "steps": [
                {"id": "review", "type": "agent", "worker": "reviewer", "brief": "Review it."},
                {"id": "send-back", "type": "reopen", "from": "review", "max_rounds": 2, "if": [{"step": "review", "verdict": "block"}]},
                {"id": "push", "type": "run", "command": "/usr/local/lib/push-dev", "if": [{"step": "review", "verdict": "ok"}]},
                {"id": "rebase", "type": "reopen", "if": [{"step": "review", "verdict": "ok"}, {"step": "push", "status": "failed"}]}
            ],
            "latest_deliveries": [
                {"delivery_id": "d1", "job_id": "worker/worker_c/r1", "state": "succeeded", "round": 0, "created_at": 1,
                 "steps": [{"id": "review", "state": "succeeded", "outcome": {"verdict": "block"}}, {"id": "send-back", "state": "succeeded"}, {"id": "push", "state": "skipped"}]},
                {"delivery_id": "d2", "job_id": "worker/worker_c/r1", "state": "succeeded", "round": 1, "created_at": 2,
                 "steps": [{"id": "review", "state": "succeeded", "verdict": "ok"}, {"id": "send-back", "state": "skipped"}, {"id": "push", "state": "succeeded"}]}
            ]
        })
    }

    fn names(id: &str) -> String {
        if id == "worker_c" { "Coder".into() } else { id.into() }
    }

    #[test]
    fn a_workflow_in_words() {
        let w = workflow(&loop_json());
        assert_eq!(trigger(&w, &names), "When a job of worker Coder in swarm-go succeeds");
        assert_eq!(describe(&w.steps[0], &names), "Swarm's reviewer");
        assert_eq!(describe(&w.steps[1], &names), "send the work back with the findings of review, at most 2 rounds");
        assert_eq!(w.steps[1].conditions, vec!["if review blocks"]);
        assert_eq!(w.steps[3].conditions, vec!["if review is OK", "if push failed"]);
        assert_eq!(describe(&w.steps[2], &names), "run the machine's script /usr/local/lib/push-dev");
    }

    #[test]
    fn deliveries_newest_first_as_chains() {
        let w = workflow(&loop_json());
        assert_eq!(w.deliveries[0].id, "d2");
        assert_eq!(chain(&w.deliveries[0]), "review OK → push");
        assert_eq!(chain(&w.deliveries[1]), "review blocked → send-back");
    }

    #[test]
    fn an_event_trigger_in_words() {
        let w = workflow(&json!({"name": "socials-post", "when": "succeeded", "scope": {"folder": "/srv/work/socials"},
            "on": {"event": {"source": "pull", "url": "https://socials.example.dev/v1/posts?status=changes_requested", "every_seconds": 60,
                             "secret": "SOCIALS_DRAFT_TOKEN", "items": "posts", "id": "id"},
                   "start": {"worker": "worker_c", "brief": "Revise {{event.key}}"}},
            "events": [{"item_id": "p1", "received_at": 1, "state": "started", "job_id": "worker/worker_c/r1"},
                       {"item_id": "p2", "received_at": 2, "state": "queued"}],
            "steps": []}));
        assert_eq!(trigger(&w, &names), "When a new item shows up in socials.example.dev's feed (checked every 1 min): worker Coder starts a job in socials");
        assert_eq!(w.on.as_ref().unwrap().secret, "SOCIALS_DRAFT_TOKEN");
        assert_eq!(w.events[0].item_id, "p2");
        assert_eq!(w.events[1].job_id, "worker/worker_c/r1");
        let hook = workflow(&json!({"name": "h", "scope": {"folder": "/x/site"}, "on": {"event": {"source": "webhook", "id": "id"}, "start": {"worker": "worker_c"}}}));
        assert_eq!(trigger(&hook, &names), "When a signed request comes in: worker Coder starts a job in site");
    }

    #[test]
    fn a_role_scope_and_a_custom_reviewer() {
        let w = workflow(&json!({"name": "r", "when": "always", "scope": {"folder": "/x/notes/", "role": "coder"},
            "steps": [{"id": "check", "type": "agent", "worker": "worker_r", "checkout": "same", "fixes": "security"}]}));
        assert_eq!(trigger(&w, &names), "When a coder task in notes ends");
        assert_eq!(describe(&w.steps[0], &names), "worker worker_r, read-only where the work was done, may commit small security fixes");
    }
}
