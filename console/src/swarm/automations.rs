//! Workers and automations on a Swarm, as its web UI reads them:
//! GET /v3/workers (each worker with its own automations) and
//! GET /v3/automations/v2 (the accepted automations of the account). Read
//! only; every page Swarm has is followed by its cursor.

use serde_json::Value;

use super::client::Client;

/// One of the person's workers.
#[derive(Clone, Debug, PartialEq)]
pub struct Worker {
    pub id: String,
    pub name: String,
    pub description: String,
    /// What it is told to do.
    pub instructions: String,
    /// Swarm's revision of the record, which an accept names.
    pub revision: u64,
    /// The conversation that proposed it, if one did.
    pub source_session: String,
    /// Swarm's lifecycle state (active, pending, …).
    pub state: String,
    /// Plan (waits for approval) or auto.
    pub mode: String,
    /// The folders it may work in, by name.
    pub folders: Vec<String>,
    pub automations: Vec<WorkerAutomation>,
    pub updated_at: u64,
    /// A change to it waits for the person's review in Swarm, with the
    /// automations it would have.
    pub pending: Option<Vec<WorkerAutomation>>,
}

/// A worker's own automation: when it runs the worker.
#[derive(Clone, Debug, PartialEq)]
pub struct WorkerAutomation {
    pub id: String,
    pub name: String,
    pub when: String,
    pub enabled: bool,
}

/// An accepted automation (Automation V2): a plan Swarm runs on a schedule.
#[derive(Clone, Debug, PartialEq)]
pub struct Automation {
    pub id: String,
    pub title: String,
    pub goal: String,
    pub when: String,
    pub enabled: bool,
    pub cancelled: bool,
    pub archived: bool,
    pub project_id: String,
    /// The conversation it came from.
    pub session_id: String,
    /// When it runs next (unix ms), if Swarm says.
    pub next_due_at: Option<u64>,
    /// When it stops: never (None) or at a time (unix ms).
    pub expires_at: Option<u64>,
}

/// What one machine has.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Board {
    pub workers: Vec<Worker>,
    pub automations: Vec<Automation>,
    /// Recent runs of every worker, newest first.
    pub runs: Vec<Run>,
    /// What runs when a task or job ends (GET /v3/workflows).
    pub workflows: Vec<super::workflows::Workflow>,
    /// Swarm's stop switch for every workflow.
    pub workflows_stopped: bool,
    /// Why the workflows could not be read, if they could not.
    pub workflows_error: Option<String>,
}

/// Reads every worker, every automation and every workflow, page by page.
pub fn read(client: &Client) -> Result<Board, String> {
    let workers = pages(client, "/v3/workers", "workers")?.iter().map(|(w, page)| worker(w, page)).collect();
    let automations = pages(client, "/v3/automations/v2", "records")?.iter().map(|(a, _)| automation(a)).collect();
    let workers: Vec<Worker> = workers;
    let runs = runs(client, &workers)?;
    let (workflows, workflows_stopped, workflows_error) = match super::workflows::read(client) {
        Ok((list, stopped)) => (list, stopped, None),
        Err(e) => (Vec::new(), false, Some(e)),
    };
    Ok(Board { workers, automations, runs, workflows, workflows_stopped, workflows_error })
}

/// Every item of a paged list, with the page it came on (workers' folders
/// are listed beside them on the page).
fn pages(client: &Client, path: &str, field: &str) -> Result<Vec<(Value, Value)>, String> {
    let mut items = Vec::new();
    let mut cursor = String::new();
    loop {
        let url = if cursor.is_empty() { path.to_string() } else { format!("{path}?cursor={cursor}") };
        let page = client.get(&url).map_err(|e| e.to_string())?;
        let list = page.get(field).and_then(Value::as_array).cloned().unwrap_or_default();
        let next = page.get("next_cursor").and_then(Value::as_str).unwrap_or("").to_string();
        let head = page.clone();
        items.extend(list.into_iter().map(|item| (item, head.clone())));
        // A cursor that does not move would read the same page forever.
        if next.is_empty() || next == cursor {
            break;
        }
        cursor = next;
    }
    Ok(items)
}

fn text(v: &Value, key: &str) -> String {
    v.get(key).and_then(Value::as_str).unwrap_or("").to_string()
}

/// A schedule in words: every 2h, cron 0 9 * * 1-5 (Europe/Berlin), on demand.
pub fn when(schedule: Option<&Value>, mode: &str) -> String {
    let Some(s) = schedule.filter(|s| s.is_object()) else {
        return match mode {
            "manual" => "by hand".into(),
            "external_trigger" => "on a trigger".into(),
            "" => "no schedule".into(),
            other => other.replace('_', " "),
        };
    };
    match text(s, "kind").as_str() {
        "interval" => {
            let secs = s.get("interval_seconds").and_then(Value::as_u64).unwrap_or(0);
            format!("every {}", span(secs))
        }
        "cron" => {
            let tz = text(s, "timezone");
            if tz.is_empty() { format!("cron {}", text(s, "cron")) } else { format!("cron {} ({tz})", text(s, "cron")) }
        }
        "trigger" => "on a trigger".into(),
        other => other.to_string(),
    }
}

/// Seconds as the largest whole unit: 90s, 15m, 2h, 3d.
fn span(secs: u64) -> String {
    match secs {
        0 => "0s".into(),
        s if s % 86_400 == 0 => format!("{}d", s / 86_400),
        s if s % 3_600 == 0 => format!("{}h", s / 3_600),
        s if s % 60 == 0 => format!("{}m", s / 60),
        s => format!("{s}s"),
    }
}

fn worker(w: &Value, page: &Value) -> Worker {
    let id = text(w, "id");
    // The page lists each worker's folders beside it; the record may carry them too.
    let folders_of = |v: Option<&Value>| -> Vec<String> {
        v.and_then(Value::as_object)
            .map(|m| m.values().map(|f| { let n = text(f, "name"); if n.is_empty() { text(f, "path") } else { n } }).filter(|n| !n.is_empty()).collect())
            .unwrap_or_default()
    };
    let mut folders = folders_of(page.get("workspaces").and_then(|ws| ws.get(&id)));
    if folders.is_empty() {
        folders = folders_of(w.get("authorized_workspaces"));
    }
    let automations_of = |w: &Value| -> Vec<WorkerAutomation> {
        w.get("automations")
        .and_then(Value::as_array)
        .map(|list| {
            list.iter()
                .map(|a| WorkerAutomation {
                    id: text(a, "id"),
                    name: text(a, "name"),
                    when: when(a.get("schedule"), &text(a, "activation_mode")),
                    enabled: a.get("enabled").and_then(Value::as_bool).unwrap_or(false),
                })
                .collect()
        })
        .unwrap_or_default()
    };
    let automations = automations_of(w);
    let pending = w.get("pending_review").filter(|p| p.is_object()).map(automations_of);
    Worker {
        instructions: text(w, "instructions"),
        revision: w.get("revision").and_then(Value::as_u64).unwrap_or(0),
        source_session: w.get("metadata").map(|m| text(m, "source_session_id")).unwrap_or_default(),
        pending,
        name: text(w, "name"),
        description: text(w, "description"),
        state: text(w, "lifecycle_state"),
        mode: text(w, "execution_mode"),
        folders,
        automations,
        updated_at: w.get("updated_at").and_then(Value::as_u64).unwrap_or(0),
        id,
    }
}

fn automation(a: &Value) -> Automation {
    let doc = a.get("document").cloned().unwrap_or(Value::Null);
    let settings = doc.get("automation_v2").cloned().unwrap_or(Value::Null);
    let expires_at = settings.get("expiration").filter(|e| text(e, "kind") == "at").and_then(|e| e.get("expires_at")).and_then(Value::as_u64);
    Automation {
        id: text(a, "automation_id"),
        title: text(&doc, "title"),
        goal: doc.get("info").map(|i| text(i, "goal")).unwrap_or_default(),
        when: when(settings.get("schedule"), ""),
        enabled: a.get("enabled").and_then(Value::as_bool).unwrap_or(false),
        cancelled: a.get("cancelled").and_then(Value::as_bool).unwrap_or(false),
        archived: a.get("archived").and_then(Value::as_bool).unwrap_or(false),
        project_id: text(a, "project_id"),
        session_id: text(a, "session_id"),
        next_due_at: a.get("next_due_at").and_then(Value::as_u64).filter(|n| *n > 0),
        expires_at,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn schedules_in_words() {
        assert_eq!(when(Some(&json!({"kind": "interval", "interval_seconds": 7200})), ""), "every 2h");
        assert_eq!(when(Some(&json!({"kind": "interval", "interval_seconds": 90})), ""), "every 90s");
        assert_eq!(when(Some(&json!({"kind": "cron", "cron": "0 9 * * 1", "timezone": "Asia/Jerusalem"})), ""), "cron 0 9 * * 1 (Asia/Jerusalem)");
        assert_eq!(when(Some(&json!({"kind": "trigger"})), ""), "on a trigger");
        assert_eq!(when(None, "manual"), "by hand");
        assert_eq!(when(Some(&Value::Null), "external_trigger"), "on a trigger");
    }

    #[test]
    fn a_worker_with_its_automations_and_folders() {
        let page = json!({"workspaces": {"w1": {"ws1": {"workspace_id": "ws1", "available": true, "name": "site"}}}});
        let w = json!({"id": "w1", "name": "Deployer", "lifecycle_state": "active", "execution_mode": "auto", "updated_at": 5,
            "automations": [{"name": "nightly", "activation_mode": "cron", "schedule": {"kind": "cron", "cron": "0 2 * * *", "timezone": "UTC"}, "enabled": true}]});
        let got = worker(&w, &page);
        assert_eq!(got.name, "Deployer");
        assert_eq!(got.folders, vec!["site".to_string()]);
        assert_eq!(got.automations, vec![WorkerAutomation { id: String::new(), name: "nightly".into(), when: "cron 0 2 * * * (UTC)".into(), enabled: true }]);
    }

    #[test]
    fn an_accepted_automation() {
        let a = json!({"automation_id": "a1", "enabled": true, "cancelled": false, "project_id": "p1", "session_id": "s1", "next_due_at": 1000,
            "document": {"title": "Daily report", "info": {"goal": "Sum up the day"},
                "automation_v2": {"schedule": {"kind": "interval", "interval_seconds": 86400}, "expiration": {"kind": "at", "expires_at": 99}}}});
        let got = automation(&a);
        assert_eq!(got.title, "Daily report");
        assert_eq!(got.goal, "Sum up the day");
        assert_eq!(got.when, "every 1d");
        assert_eq!(got.next_due_at, Some(1000));
        assert_eq!(got.expires_at, Some(99));
        assert!(got.enabled && !got.cancelled && !got.archived);
    }
}

/// One run of a worker, as Swarm records it.
#[derive(Clone, Debug, PartialEq)]
pub struct Run {
    pub id: String,
    pub worker_id: String,
    pub worker: String,
    /// admitted, running, succeeded, failed or cancelled.
    pub status: String,
    pub error: String,
    /// What started it: a schedule (its name), a direct request, a trigger.
    pub source: String,
    pub session_id: String,
    pub started_at: u64,
    pub completed_at: u64,
    pub cancel_requested: bool,
    /// What it handed back, in words (a label, a path or a url each).
    pub deliverables: Vec<String>,
    /// For a run in progress: its latest action, from its session.
    pub latest: String,
}

impl Run {
    pub fn active(&self) -> bool {
        matches!(self.status.as_str(), "admitted" | "running")
    }
}

/// Each worker's recent runs (the first page Swarm gives), newest first,
/// with the latest action of the ones in progress.
pub fn runs(client: &Client, workers: &[Worker]) -> Result<Vec<Run>, String> {
    let mut all = Vec::new();
    for w in workers {
        let page = client.get(&format!("/v3/workers/{}/runs", w.id)).map_err(|e| e.to_string())?;
        let list = page.get("runs").and_then(Value::as_array).cloned().unwrap_or_default();
        for r in &list {
            let mut run = run(r, w);
            if run.active() && !run.session_id.is_empty() {
                if let Ok(entries) = transcript(client, &run.session_id) {
                    run.latest = latest_action(&entries);
                }
            }
            all.push(run);
        }
    }
    all.sort_by(|a, b| b.started_at.max(b.completed_at).cmp(&a.started_at.max(a.completed_at)));
    Ok(all)
}

fn run(r: &Value, w: &Worker) -> Run {
    let num = |k: &str| r.get(k).and_then(Value::as_u64).unwrap_or(0);
    let automation = text(r, "automation_id");
    let source = if !automation.is_empty() {
        w.automations.iter().find(|a| a.id == automation).map(|a| format!("schedule {}", a.name)).unwrap_or_else(|| "a schedule".into())
    } else {
        match text(r, "request_source").as_str() {
            "" | "direct" | "direct_request" => "asked directly".into(),
            other => other.replace('_', " "),
        }
    };
    let deliverables = r
        .get("deliverables")
        .and_then(Value::as_array)
        .map(|list| {
            list.iter()
                .filter_map(|d| ["label", "title", "path", "url", "name"].iter().map(|k| text(d, k)).find(|v| !v.is_empty()))
                .collect()
        })
        .unwrap_or_default();
    Run {
        id: text(r, "id"),
        worker_id: w.id.clone(),
        worker: w.name.clone(),
        status: text(r, "status"),
        error: r.get("error").and_then(|e| e.as_str().map(str::to_string).or_else(|| e.get("message").and_then(Value::as_str).map(str::to_string))).unwrap_or_default(),
        source,
        session_id: text(r, "session_id"),
        started_at: num("started_at").max(num("created_at")),
        completed_at: num("completed_at"),
        cancel_requested: r.get("cancel_requested").and_then(Value::as_bool).unwrap_or(false),
        deliverables,
        latest: String::new(),
    }
}

/// A run's conversation: what its agent said and every action it took.
pub fn transcript(client: &Client, session_id: &str) -> Result<Vec<crate::data::Entry>, String> {
    let answer = client.get(&format!("/v3/sessions/{session_id}")).map_err(|e| e.to_string())?;
    let messages = answer.get("messages").and_then(Value::as_array).cloned().unwrap_or_default();
    // A run's plan progress and its report come as system messages.
    Ok(messages
        .iter()
        .enumerate()
        .flat_map(|(i, m)| match text(m, "role").as_str() {
            "system" => {
                let t = text(m, "content");
                if t.trim().is_empty() { vec![] } else { vec![crate::data::Entry::Assistant(t.trim().to_string().into())] }
            }
            _ => super::map::entry(m, i == 0),
        })
        .collect())
}

/// The newest action in a transcript, in a line.
pub fn latest_action(entries: &[crate::data::Entry]) -> String {
    entries
        .iter()
        .rev()
        .find_map(|e| match e {
            crate::data::Entry::Tool { name, detail, .. } => Some(format!("{name} {detail}").trim().to_string()),
            _ => None,
        })
        .unwrap_or_default()
}

/// Asks Swarm to stop a run.
pub fn cancel(client: &Client, worker_id: &str, run_id: &str) -> Result<Value, String> {
    client.post(&format!("/v3/workers/{worker_id}/runs/{run_id}/cancel"), serde_json::json!({})).map_err(|e| e.to_string())
}

#[cfg(test)]
mod run_tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_failed_run() {
        let w = Worker { id: "w1".into(), name: "Census".into(), description: String::new(), instructions: String::new(), revision: 1, source_session: String::new(), state: "active".into(), mode: String::new(), folders: vec![], automations: vec![], updated_at: 0, pending: None };
        let r = run(&json!({"id": "r1", "status": "failed", "error": "workspace gone", "session_id": "s1", "started_at": 5, "completed_at": 9, "request_source": "direct",
            "deliverables": [{"label": "report.md"}, {"url": "https://x"}]}), &w);
        assert_eq!(r.status, "failed");
        assert_eq!(r.error, "workspace gone");
        assert_eq!(r.source, "asked directly");
        assert_eq!(r.deliverables, vec!["report.md".to_string(), "https://x".to_string()]);
        assert!(!r.active());
    }

    #[test]
    fn the_latest_action() {
        use crate::data::Entry;
        let e = vec![Entry::tool("bash", "ls"), Entry::Assistant("ok".into()), Entry::tool("read", "a.md")];
        assert_eq!(latest_action(&e), "read a.md");
    }
}

#[cfg(test)]
mod transcript_tests {
    use crate::data::Entry;
    use serde_json::json;

    /// A run's tool message carries its arguments inside its content.
    #[test]
    fn a_run_tool_message_shows_its_command() {
        let content = json!({"type": "v3_provider_tool_result", "tool_name": "bash", "arguments": "{\"command\":\"ls -la\"}"}).to_string();
        let got = crate::swarm::map::entry(&json!({"role": "tool", "content": content, "metadata": {"tool_name": "bash"}}), false);
        assert!(matches!(got.as_slice(), [Entry::Tool { name, detail, .. }] if name.as_ref() == "bash" && detail.as_ref() == "ls -la"));
    }
}

impl Worker {
    /// It waits for the person: a new worker not yet accepted, or a change to
    /// one.
    pub fn waiting(&self) -> bool {
        self.state == "pending" || self.pending.is_some()
    }
}

/// Accepts a pending worker (or its pending change) at the revision the
/// person saw: its schedules and triggers start.
pub fn accept(client: &Client, worker_id: &str, revision: u64) -> Result<Value, String> {
    client.post(&format!("/v3/workers/{worker_id}/accept"), serde_json::json!({"expected_revision": revision})).map_err(|e| e.to_string())
}

/// Discards a worker that was never accepted, at the revision the person saw.
pub fn discard(client: &Client, worker_id: &str, revision: u64) -> Result<Value, String> {
    client.delete(&format!("/v3/workers/{worker_id}?expected_revision={revision}")).map_err(|e| e.to_string())
}

/// Pauses a worker (its schedules stop; runs in progress go on) or resumes
/// it, at the revision the person saw.
pub fn set_paused(client: &Client, worker_id: &str, revision: u64, pause: bool) -> Result<Value, String> {
    let action = if pause { "pause" } else { "resume" };
    client.post(&format!("/v3/workers/{worker_id}/{action}"), serde_json::json!({"expected_revision": revision})).map_err(|e| e.to_string())
}
