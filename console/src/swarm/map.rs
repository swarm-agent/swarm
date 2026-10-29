//! Swarm's records (its HTTP API and sync snapshots) to what the console
//! shows. Pure functions over the JSON Swarm returns, so they are tested
//! against real answers.

use serde_json::Value;

use crate::data::{
    Assignment, Busy, Choice, ClientCall, Day, Entry, Health, LiveRef, Machine, ModelUse, Pending, Plan,
    Credential, Drain, Found, PlanWindow, Progress, ProgressState, Project, Provider, Question, Role, RunClock, RunState, RunsToday, Session, SessionUse, Task, TaskState, TaskStatus, TaskWait, Text, Tokens, ToolState, UpdateState, Updates, Usage,
};

pub(crate) fn s<'a>(v: &'a Value, key: &str) -> &'a str {
    v.get(key).and_then(Value::as_str).unwrap_or("")
}

fn f(v: &Value, key: &str) -> f32 {
    v.get(key).and_then(Value::as_f64).unwrap_or(0.) as f32
}

/// A number that needs full precision: timestamps, token counts.
fn n(v: &Value, key: &str) -> f64 {
    v.get(key).and_then(Value::as_f64).unwrap_or(0.)
}

pub(crate) fn list<'a>(v: &'a Value, key: &str) -> &'a [Value] {
    v.get(key).and_then(Value::as_array).map(Vec::as_slice).unwrap_or(&[])
}

/// 1234567 → "1.2M", 63220 → "63k".
pub fn tokens(n: f64) -> String {
    if n >= 1e6 {
        format!("{:.1}M", n / 1e6)
    } else if n >= 1e3 {
        format!("{:.0}k", n / 1e3)
    } else {
        format!("{n:.0}")
    }
}

/// A project task's status as Swarm stores it.
pub fn task_status(status: &str) -> TaskStatus {
    match status {
        "pending_approval" => TaskStatus::PendingApproval,
        "queued" | "pending" | "planning" => TaskStatus::Queued,
        "needs_input" => TaskStatus::NeedsInput,
        "needs_review" => TaskStatus::NeedsReview,
        "completed" | "integrated" | "done" => TaskStatus::Completed,
        "failed" | "blocked" => TaskStatus::Failed,
        "rejected" | "cancelled" | "canceled" | "archived" => TaskStatus::Rejected,
        _ => TaskStatus::Running,
    }
}

/// Role keys as Swarm names them, to the console's labels.
fn role_label(role: &str) -> &str {
    match role {
        "swarm" | "action" => "Swarm",
        "plan" | "system-orchestrator" | "project_orchestrator" => "Orchestrator",
        "system-coder" | "coder" => "Coder",
        "system-reviewer" | "reviewer" => "Reviewer",
        "system-finder" | "finder" => "Finder",
        "system-designer" | "designer" => "Designer",
        "system-compact" | "compact" => "Compact",
        "system-router" | "router" => "Router",
        other => other,
    }
}

fn role_purpose(role: &str) -> &'static str {
    match role {
        "action" => "big tasks, coordinates children",
        "plan" => "runs project conversations",
        "system-coder" => "edits code",
        "system-reviewer" => "independent read-only review",
        "system-finder" => "searches, reads, reports",
        "system-designer" => "UI, HTML designs, images",
        "system-compact" => "summarises long sessions",
        "system-router" => "titles and small routing",
        _ => "",
    }
}

/// GET /v3/projects.
pub fn projects(v: &Value) -> Vec<Project> {
    list(v, "projects")
        .iter()
        .map(|p| Project {
            name: s(p, "name").to_string().into(),
            machines: vec![0],
            id: s(p, "id").to_string().into(),
            workspaces: list(p, "workspaces").iter().map(|w| Text::from(s(w, "path").to_string())).collect(),
            roles: list(p, "workspaces").iter().map(|w| Text::from(s(w, "role").to_string())).collect(),
            context: s(p, "project_context").to_string().into(),
            context_generation: crate::data::ContextGeneration {
                status: s(&p["context_generation"], "status").to_string().into(),
                attempt: p["context_generation"]["attempt"].as_u64().unwrap_or(0),
                error: s(&p["context_generation"], "error").to_string().into(),
            },
            theme_id: s(p, "theme_id").to_string().into(),
        })
        .collect()
}

/// The person's words in a task's first message: Swarm wraps them in a
/// mission brief whose "User Objective" section holds them.
pub fn objective(content: &str) -> String {
    match content.split_once("## User Objective") {
        Some((_, rest)) => rest.trim_start().split("\n## ").next().unwrap_or("").trim().to_string(),
        None => content.trim().to_string(),
    }
}

/// One line for a tool call from its arguments: what it worked on.
pub fn tool_detail(args: &Value) -> String {
    let args = match args {
        Value::String(t) => serde_json::from_str::<Value>(t).unwrap_or(Value::Null),
        v => v.clone(),
    };
    let pick = ["command", "path", "pattern", "query", "url", "title", "brief", "prompt"]
        .iter()
        .find_map(|k| args.get(k).and_then(Value::as_str).filter(|v| !v.is_empty()).map(str::to_string));
    let detail = pick
        .or_else(|| {
            args.get("queries")
                .and_then(Value::as_array)
                .map(|q| q.iter().filter_map(Value::as_str).collect::<Vec<_>>().join(", "))
        })
        .unwrap_or_default();
    // A management call says what it did by its action, and where when it
    // reaches a linked machine ("list · forge").
    let detail = if detail.is_empty() {
        let action = args.get("action").and_then(Value::as_str).unwrap_or("");
        match args.get("machine").and_then(Value::as_str).filter(|m| !m.is_empty()) {
            Some(machine) if !action.is_empty() => format!("{action} · {machine}"),
            _ => action.to_string(),
        }
    } else {
        detail
    };
    let line = detail.lines().next().unwrap_or("").to_string();
    if line.chars().count() > 160 { line.chars().take(160).collect::<String>() + "…" } else { line }
}

/// Is this session shell an Orchestrator conversation of a project? Its
/// project id when so.
pub fn orchestrator_project(shell: &Value) -> Option<String> {
    let meta = shell.get("metadata").cloned().unwrap_or(Value::Null);
    if s(&meta, "role") != "project_orchestrator" && s(&meta, "agent_name") != "system-orchestrator" {
        return None;
    }
    let id = if s(&meta, "project_id").is_empty() { s(&meta, "swarm_v3_project_id") } else { s(&meta, "project_id") };
    (!id.is_empty()).then(|| id.to_string())
}

/// A run's clock from Swarm's run state or run intent (sync's
/// current_run_state_by_session, or a session's active_run_intent).
pub fn run_clock(run: &Value, last_message_at: u64) -> RunClock {
    let ms = |k: &str| run.get(k).and_then(Value::as_f64).unwrap_or(0.) as u64;
    // A run asleep on wait_tasks is not active: Swarm reports it so, and a
    // run intent (which has no `active`) says so by its status.
    let active = run.get("active").and_then(Value::as_bool).unwrap_or(false)
        || matches!(s(run, "status"), "pending_executor" | "running" | "dispatch_blocked");
    RunClock { active, started_at: ms("started_at"), cumulative_ms: ms("cumulative_duration_ms"), last_ms: ms("duration_ms"), last_message_at }
}

/// What a run asleep on tasks waits for: its `task_wait` while its status is
/// `waiting_tasks` (sync's run state or a run intent carry both). None for
/// any other run. Titles are left for the caller, who knows the tasks.
pub fn task_wait(run: &Value) -> Option<TaskWait> {
    if s(run, "status") != "waiting_tasks" {
        return None;
    }
    let wait = run.get("task_wait").cloned().unwrap_or(Value::Null);
    Some(TaskWait {
        run_id: s(run, "run_id").to_string().into(),
        project_id: s(&wait, "project_id").to_string().into(),
        machine: s(&wait, "machine").to_string().into(),
        tasks: list(&wait, "tasks").iter().map(|t| s(t, "task_id")).filter(|id| !id.is_empty()).map(|id| (id.to_string().into(), "".into())).collect(),
    })
}

/// A session as the sync snapshot lists it (its shell and run state), in its
/// project (by index into `projects`). None for sessions that are not
/// Orchestrator conversations of a known project.
pub fn session(shell: &Value, run: &Value, pending: bool, projects: &[Project]) -> Option<Session> {
    let project_id = orchestrator_project(shell)?;
    let project = projects.iter().position(|p| p.id.as_ref() == project_id)?;
    let clock = run_clock(run, n(shell, "last_message_at") as u64);
    let failed = matches!(s(run, "status"), "failed") || !s(run, "error").is_empty();
    let waiting_on = task_wait(run);
    Some(Session {
        live: Some(LiveRef { task_id: "".into(), session_id: s(shell, "id").to_string().into() }),
        title: if s(shell, "title").is_empty() { "Untitled".into() } else { s(shell, "title").to_string().into() },
        project,
        machine: 0,
        model: "".into(),
        state: if clock.active {
            RunState::Running
        } else if pending {
            RunState::NeedsYou
        } else if waiting_on.is_some() {
            RunState::Waiting
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
        pinned: shell.get("metadata").and_then(|m| m.get("pinned")).and_then(Value::as_bool).unwrap_or(false),
        calls: vec![],
        outbox: vec![],
        waiting_on,
        run: Some(clock),
    })
}

/// Tools this app runs on your machine for the Orchestrator.
pub fn is_client_tool(tool: &str) -> bool {
    matches!(
        tool,
        "request_machine_access" | "machine_run" | "machine_read" | "machine_write" | "request_fleet_access" | "request_workspace_setup" | "request_browser_access" | "manage_browser"
    )
}

fn tool_arguments(p: &Value) -> Value {
    match p.get("tool_arguments") {
        Some(Value::String(t)) => serde_json::from_str::<Value>(t).unwrap_or(Value::Null),
        Some(v) => v.clone(),
        None => Value::Null,
    }
}

/// What a session waits on you for, from its pending permission records
/// (PermissionRecord), read the way Swarm's SDK reads it: one question
/// {title, context, question, options} or several {questions: [{id, header,
/// question, required, options}]}; an option is a string or {label, value,
/// description, allow_custom}.
pub fn pending(records: &[Value]) -> Option<Pending> {
    let p = records.iter().filter(|p| s(p, "status").is_empty() || s(p, "status") == "pending").find(|p| !is_client_tool(s(p, "tool_name")))?;
    let args = tool_arguments(p);
    let id: Text = s(p, "id").to_string().into();
    if s(p, "tool_name").replace('-', "_") != "ask_user" {
        let what = ["command", "path", "url"].iter().find_map(|k| args.get(k).and_then(Value::as_str)).map(str::to_string);
        let text = format!("{} wants to run: {}", s(p, "tool_name"), what.unwrap_or_else(|| args.to_string()));
        return Some(Pending { id, question: false, text: text.into(), title: "".into(), structured: false, questions: vec![] });
    }
    let structured = args.get("questions").is_some();
    let raw: Vec<Value> = if structured { list(&args, "questions").to_vec() } else { vec![args.clone()] };
    let questions = raw
        .iter()
        .enumerate()
        .map(|(i, q)| Question {
            id: if s(q, "id").is_empty() { format!("q_{}", i + 1).into() } else { s(q, "id").to_string().into() },
            header: s(q, "header").to_string().into(),
            text: s(q, "question").to_string().into(),
            required: q.get("required").and_then(Value::as_bool) != Some(false),
            options: list(q, "options").iter().filter_map(choice).collect(),
        })
        .collect();
    Some(Pending { id, question: true, text: "".into(), title: s(&args, "title").to_string().into(), structured, questions })
}

/// The calls to your machine waiting on this app.
pub fn client_calls(records: &[Value]) -> Vec<ClientCall> {
    records
        .iter()
        .filter(|p| (s(p, "status").is_empty() || s(p, "status") == "pending") && is_client_tool(s(p, "tool_name")))
        .map(|p| ClientCall { id: s(p, "id").to_string().into(), tool: s(p, "tool_name").to_string().into(), target_device: s(p, "target_device").to_string().into(), this_device: false, execution: s(p, "execution").to_string().into(), args: tool_arguments(p) })
        .collect()
}

fn choice(item: &Value) -> Option<Choice> {
    let custom_marker = |v: &str| v == "__custom__" || v == "Custom response";
    if let Some(t) = item.as_str().map(str::trim).filter(|t| !t.is_empty()) {
        return Some(Choice { label: t.to_string().into(), value: t.to_string().into(), description: "".into(), custom: custom_marker(t) });
    }
    let value = if s(item, "value").is_empty() { s(item, "label") } else { s(item, "value") };
    if value.is_empty() {
        return None;
    }
    Some(Choice {
        label: if s(item, "label").is_empty() { value.to_string().into() } else { s(item, "label").to_string().into() },
        value: value.to_string().into(),
        description: s(item, "description").to_string().into(),
        custom: item.get("allow_custom").and_then(Value::as_bool) == Some(true) || custom_marker(value),
    })
}

/// The resolve reason that answers `pending`: plain text for one question,
/// {"answers": {id: value}} for several. None while a required answer is missing.
pub fn answer(pending: &Pending, answers: &std::collections::HashMap<Text, String>) -> Option<String> {
    if !pending.structured {
        let q = pending.questions.first()?;
        return answers.get(&q.id).map(|a| a.trim().to_string()).filter(|a| !a.is_empty());
    }
    let mut map = serde_json::Map::new();
    for q in &pending.questions {
        match answers.get(&q.id).map(|a| a.trim()).filter(|a| !a.is_empty()) {
            Some(a) => {
                map.insert(q.id.to_string(), Value::String(a.to_string()));
            }
            None if q.required => return None,
            None => {}
        }
    }
    (!map.is_empty()).then(|| serde_json::json!({"answers": map}).to_string())
}

/// One conversation entry from a message record (MessageSnapshot): the
/// person's words, the agent's answer, or a tool it used.
pub fn entry(m: &Value, first: bool) -> Vec<Entry> {
    let mut out = Vec::new();
    let meta = m.get("metadata").cloned().unwrap_or(Value::Null);
    match s(m, "role") {
        "user" => {
            let content = s(m, "content");
            let text = if first || content.starts_with("# Mission") { objective(content) } else { content.trim().to_string() };
            if !text.is_empty() {
                if meta.get("queued_note").and_then(Value::as_bool) == Some(true) && !s(m, "id").is_empty() {
                    out.push(Entry::Queued { id: s(m, "id").to_string().into(), text: text.into() });
                } else {
                    out.push(Entry::User(text.into()));
                }
            }
            let files: Vec<String> = list(m, "media")
                .iter()
                .map(|f| if s(f, "file_name").is_empty() { s(f, "modality").to_string() } else { s(f, "file_name").to_string() })
                .collect();
            if !files.is_empty() {
                out.push(Entry::tool("attached", files.join(", ")));
            }
        }
        "assistant" => {
            let text = s(m, "content").trim();
            if !text.is_empty() {
                out.push(Entry::Assistant(text.to_string().into()));
            }
        }
        // A task wait's start or end line: Swarm records it for the person
        // (never a model turn). One line; linked-machine titles are untrusted.
        "system" if s(&meta, "source") == "task_wait_notice" => {
            let text = s(m, "content").split_whitespace().collect::<Vec<_>>().join(" ");
            if !text.is_empty() {
                out.push(Entry::Notice(text.into()));
            }
        }
        "tool" => {
            let name = s(&meta, "tool_name");
            if !name.is_empty() {
                let call = meta.get("tool_call").cloned().unwrap_or(Value::Null);
                // A run records the call inside the message (a provider tool
                // result: its arguments beside its output).
                let recorded = serde_json::from_str::<Value>(s(m, "content")).unwrap_or(Value::Null);
                let args = call.get("arguments").or(call.get("input")).or(recorded.get("arguments")).cloned().unwrap_or(Value::Null);
                // Recent runs store the arguments as JSON text.
                let args = match args {
                    Value::String(text) => serde_json::from_str::<Value>(&text).unwrap_or(Value::String(text)),
                    other => other,
                };
                out.push(finished_tool(name, &args, &recorded, &meta, s(m, "content")));
            }
        }
        _ => {}
    }
    out
}

/// A finished tool call from its stored message: its arguments, and the
/// result record Swarm stored as the content (or the content itself when it
/// is not one), with its brief summary and duration.
fn finished_tool(name: &str, args: &Value, recorded: &Value, meta: &Value, content: &str) -> Entry {
    let is_record = recorded.is_object() && (recorded.get("call_id").is_some() || recorded.get("tool_name").is_some() || recorded.get("tool").is_some());
    let (output, error) = if is_record {
        let output = if s(recorded, "output").is_empty() { s(recorded, "completed_output") } else { s(recorded, "output") };
        (output, if s(recorded, "error").is_empty() { s(meta, "error") } else { s(recorded, "error") })
    } else {
        (content, s(meta, "error"))
    };
    let ms = |v: &Value| v.get("duration_ms").and_then(Value::as_u64).filter(|n| *n > 0);
    let call_id = [s(recorded, "call_id"), s(meta, "call_id"), s(meta, "tool_call_id")].into_iter().find(|x| !x.is_empty()).unwrap_or("");
    let summary = super::tool_summary::summarize(name, args, output, error, None);
    Entry::Tool {
        name: name.to_string().into(),
        detail: tool_detail(args).into(),
        call_id: call_id.to_string().into(),
        state: if error.trim().is_empty() { ToolState::Done } else { ToolState::Failed },
        summary: (!summary.is_empty()).then(|| summary.into()),
        duration_ms: ms(recorded).or_else(|| ms(meta)),
        started_at: None,
    }
}

/// A finished, failed or cancelled call from Swarm's terminal tool event.
pub fn tool_event_end(kind: &str, payload: &Value) -> (ToolState, Option<Text>, Option<u64>) {
    let state = match kind {
        "session.tool.failed" => ToolState::Failed,
        "session.tool.cancelled" | "session.tool.canceled" => ToolState::Cancelled,
        _ if !s(payload, "error").is_empty() => ToolState::Failed,
        _ => ToolState::Done,
    };
    let args = payload.get("arguments").cloned().unwrap_or(Value::Null);
    let exit_code = payload.get("exit_code").and_then(Value::as_i64);
    let summary = super::tool_summary::summarize(s(payload, "tool_name"), &args, s(payload, "output"), s(payload, "error"), exit_code);
    let duration = payload.get("duration_ms").and_then(Value::as_u64).filter(|n| *n > 0);
    (state, (!summary.is_empty()).then(|| summary.into()), duration)
}

/// A conversation from its message records, oldest first.
pub fn entries(messages: &[Value]) -> Vec<Entry> {
    let mut out = Vec::new();
    for (i, m) in messages.iter().enumerate() {
        apply(&mut out, m, i == 0);
    }
    out
}

/// Adds one message record to a conversation. A delivery receipt Swarm
/// writes when a queued message reached the model turns that message's
/// queued entry into an ordinary one; it adds no entry of its own.
pub fn apply(entries: &mut Vec<Entry>, m: &Value, first: bool) {
    let meta = m.get("metadata").cloned().unwrap_or(Value::Null);
    if s(m, "role") == "system" && s(&meta, "source") == "feedback_delivery" {
        let id = s(&meta, "message_id");
        if let Some(at) = entries.iter().position(|e| matches!(e, Entry::Queued { id: queued, .. } if !id.is_empty() && queued.as_ref() == id)) {
            if let Entry::Queued { text, .. } = entries[at].clone() {
                entries[at] = Entry::User(text);
            }
        }
        return;
    }
    entries.extend(entry(m, first));
}

/// A project task (ProjectTaskRecord) as a card.
pub fn task(t: &Value) -> Task {
    let role = if s(t, "agent").is_empty() { "swarm" } else { s(t, "agent") };
    let activity = [s(t, "last_error"), s(t, "action_needed"), s(t, "what_did_do")]
        .into_iter()
        .find(|x| !x.is_empty())
        .unwrap_or("")
        .lines()
        .next()
        .unwrap_or("")
        .to_string();
    Task {
        key: s(t, "id").to_string().into(),
        session: s(t, "session_id").to_string().into(),
        title: if s(t, "title").is_empty() { role.to_string().into() } else { s(t, "title").to_string().into() },
        role: role_label(role).to_lowercase().into(),
        machine: 0,
        status: task_status(s(t, "status")),
        activity: activity.into(),
        runs: vec![],
        report: task_report(t).into(),
        progress: task_progress(t),
    }
}

/// The active attempt's summary: what the agent says it delivered.
fn task_report(t: &Value) -> String {
    let active = s(t, "active_attempt_id");
    let attempts = t.get("attempts").and_then(Value::as_array).cloned().unwrap_or_default();
    attempts
        .iter()
        .find(|a| s(a, "id") == active)
        .or(attempts.last())
        .map(|a| s(a, "summary").trim().to_string())
        .unwrap_or_default()
}

/// The task's tracked state (`progress`, docs/task-progress.md). A Swarm
/// that does not track it gives only the task's status, so only what that
/// status says for sure is shown: waiting, working or failed; a finished
/// task's outcome stays unknown rather than guessed.
fn task_progress(t: &Value) -> Progress {
    let Some(p) = t.get("progress").filter(|p| p.is_object()) else {
        let state = match s(t, "status") {
            "pending_approval" | "queued" => ProgressState::Waiting,
            "in_progress" | "planning" => ProgressState::Working,
            "failed" => ProgressState::Failed,
            _ => ProgressState::Unknown,
        };
        return Progress { state, ..Default::default() };
    };
    let history = list(p, "history")
        .iter()
        .filter_map(|h| {
            let what = s(h, "what").trim();
            (!what.is_empty()).then(|| (h.get("at").and_then(Value::as_u64).unwrap_or(0), what.to_string().into()))
        })
        .collect();
    Progress {
        state: ProgressState::of(s(p, "state")),
        task_state: TaskState::of(s(p, "task_state")),
        task_state_reason: s(p, "task_state_reason").trim().to_string().into(),
        summary: s(p, "summary").trim().to_string().into(),
        history,
        updated_at: p.get("updated_at").and_then(Value::as_u64).unwrap_or(0),
    }
}

/// The cards of one conversation: the project's tasks it started.
pub fn session_tasks(project_tasks: &[Value], session_id: &str) -> Vec<Task> {
    project_tasks.iter().filter(|t| s(t, "origin_session_id") == session_id).map(task).collect()
}

/// The model line for a session header from its view's agentic settings:
/// "gpt-6.1-sol · high".
pub fn model_line(view: &Value) -> String {
    let settings = view.get("agentic_settings").cloned().unwrap_or(Value::Null);
    let m = settings.get("effective_preference").or(settings.get("stored_preference")).cloned().unwrap_or(Value::Null);
    if s(&m, "model").is_empty() {
        return String::new();
    }
    Assignment { provider: "".into(), model: s(&m, "model").to_string().into(), thinking: s(&m, "thinking").to_string().into(), tier: s(&m, "service_tier").to_string().into() }.short()
}

fn uptime(ms: f64) -> String {
    let m = (ms / 60_000.) as u64;
    match m {
        0 => "just started".into(),
        m if m < 60 => format!("{m}m"),
        m if m < 60 * 24 => format!("{}h {}m", m / 60, m % 60),
        m => format!("{}d {}h", m / (60 * 24), (m / 60) % 24),
    }
}

/// A number Swarm gave, or None when the key is absent.
fn count(v: &Value, key: &str) -> Option<u32> {
    v.get(key).and_then(Value::as_u64).map(|n| n.min(u32::MAX as u64) as u32)
}

/// A commit shortened for a card.
fn short_commit(c: &str) -> String {
    c.trim().chars().take(9).collect()
}

/// The installer's update state from the summary's `machine.update` (the
/// states containers/headless/app/install.sh writes).
pub fn update_state(update: &Value) -> UpdateState {
    let state = s(update, "state").trim();
    let reason: Text = s(update, "reason").trim().to_string().into();
    let candidate: Text = short_commit(s(update, "candidate")).into();
    match state {
        "" => UpdateState::Unknown,
        "current" | "installed" => UpdateState::Current,
        "waiting" | "waiting_review" | "blocked" if !candidate.is_empty() => UpdateState::Behind { candidate, reason },
        "building" | "draining" => UpdateState::Updating { candidate, reason },
        "failed" | "rolled_back" => UpdateState::Failed { reason },
        other => UpdateState::Other { state: other.replace('_', " ").into(), reason },
    }
}

/// The machine from GET /v3/box/summary. `kind` says how the console reaches
/// it. What the summary leaves out stays unknown.
pub fn machine(status: &Value, kind: &str) -> Machine {
    let m = status.get("machine").cloned().unwrap_or(Value::Null);
    let health = match s(status, "status") {
        "ok" => Health::Ok,
        "attention" => Health::Attention,
        "degraded" => Health::Degraded,
        _ => Health::Unknown,
    };
    let reasons = list(status, "reasons").iter().filter_map(Value::as_str).map(|r| r.to_string().into()).collect();
    let busy = status.get("busy").filter(|b| b.is_object()).map(|b| Busy {
        sessions: count(b, "sessions").unwrap_or(0),
        worker_runs: count(b, "worker_runs").unwrap_or(0),
        reviews: count(b, "reviews").unwrap_or(0),
        followups: count(b, "followups").unwrap_or(0),
        environment_builds: count(b, "environment_builds").unwrap_or(0),
    });
    // `runs` is empty when that Swarm keeps no run records.
    let runs = status.get("runs").and_then(|r| {
        Some(RunsToday {
            total: count(r, "total")?,
            succeeded: count(r, "succeeded").unwrap_or(0),
            failed: count(r, "failed").unwrap_or(0),
            running_now: count(r, "running_now").unwrap_or(0),
        })
    });
    let usage = status.get("usage").cloned().unwrap_or(Value::Null);
    let sandbox = status.get("sandbox").filter(|v| v.is_object()).map(|sb| {
        if sb.get("active").and_then(Value::as_bool) == Some(true) {
            if s(sb, "runtime").is_empty() { "on".to_string() } else { s(sb, "runtime").to_string() }
        } else if s(sb, "mode") == "off" {
            "off".to_string()
        } else {
            "not running".to_string()
        }
        .into()
    });
    let build = format!("{} {}", s(&m, "version").trim(), short_commit(s(&m, "commit"))).trim().to_string();
    Machine {
        name: if s(&m, "name").is_empty() { "swarm".into() } else { s(&m, "name").to_string().into() },
        health,
        reasons,
        busy,
        runs,
        cost_today: usage.get("today_cost_usd").and_then(Value::as_f64),
        build: (!build.is_empty()).then(|| build.into()),
        update: m.get("update").map(update_state).unwrap_or(UpdateState::Unknown),
        updates: m.get("update").filter(|u| u.is_object()).map(|u| Updates {
            auto: s(u, "auto").trim().to_string().into(),
            channel: s(u, "channel").trim().to_string().into(),
            at: u.get("at").and_then(Value::as_u64).unwrap_or(0),
        }),
        drain: m.get("drain").filter(|d| d.is_object()).map(|d| Drain {
            draining: d.get("draining").and_then(Value::as_bool).unwrap_or(false),
            since: d.get("since_ms").and_then(Value::as_u64).unwrap_or(0),
            deadline: d.get("deadline_ms").and_then(Value::as_u64).unwrap_or(0),
            timed_out: d.get("timed_out").and_then(Value::as_bool).unwrap_or(false),
            blocking_runs: count(d, "blocking_runs").unwrap_or(0),
        }),
        sandbox,
        uptime: m.get("uptime_ms").and_then(Value::as_f64).map(|ms| uptime(ms).into()),
        ..Machine::unread("".into(), kind.to_string().into(), "owner".into())
    }
}

/// Each role key's model from GET /v1/agent-model-settings.
pub fn models(settings: &Value) -> Vec<(Text, Assignment)> {
    roles(settings).into_iter().map(|r| (r.key, r.default)).collect()
}

/// Agent roles and their models from GET /v1/agent-model-settings.
pub fn roles(settings: &Value) -> Vec<Role> {
    let root = settings.get("agent_model_settings").cloned().unwrap_or(Value::Null);
    let assignment = |a: &Value| Assignment {
        provider: s(a, "provider").to_string().into(),
        model: s(a, "model").to_string().into(),
        thinking: s(a, "thinking").to_string().into(),
        tier: s(a, "service_tier").to_string().into(),
    };
    let role = |key: &str, a: &Value| Role {
        key: key.to_string().into(),
        label: role_label(key).to_string().into(),
        purpose: role_purpose(key).into(),
        default: assignment(a),
    };
    let mut out = Vec::new();
    let swarm = root.get("swarm").cloned().unwrap_or(Value::Null);
    for key in ["plan", "action"] {
        if let Some(a) = swarm.get(key) {
            out.push(role(key, a));
        }
    }
    let agents = root.get("system_agents").cloned().unwrap_or(Value::Null);
    for key in ["coder", "finder", "designer", "reviewer", "compact", "router"] {
        if let Some(a) = agents.get(key) {
            out.push(role(&format!("system-{key}"), a));
        }
    }
    out
}

/// Usage from GET /v3/usage (today, and 30 days), run counts from the box
/// summary and ChatGPT's limits from GET /v1/codex/account/usage.
pub fn usage(today: &Value, month: &Value, status: &Value, chatgpt: Result<&Value, &str>) -> Usage {
    let ts = today.get("summary").cloned().unwrap_or(Value::Null);
    let ms = month.get("summary").cloned().unwrap_or(Value::Null);
    let limits = today.get("limits").cloned().unwrap_or(Value::Null);
    let workers = status.get("workers").cloned().unwrap_or(Value::Null);
    let failed = f(&workers, "today_failed") as u32;
    let running = f(&workers, "active_runs") as u32;
    let total = f(&workers, "today_runs") as u32;
    let media = month.get("media").cloned().unwrap_or(Value::Null);
    Usage {
        plans: plans(chatgpt),
        month: token_split(&ms),
        today: token_split(&ts),
        cost: f(&ms, "total_cost_usd"),
        nominal: f(&ms, "codex_nominal_cost_usd"),
        today_cost: f(&ts, "total_cost_usd"),
        cost_note: if ms.get("has_unknown_pricing").and_then(Value::as_bool) == Some(true) {
            "some models have no known price; subscription use costs nothing extra.".into()
        } else {
            "".into()
        },
        turns: f(&ms, "total_turns") as u32,
        sessions: f(&ms, "total_sessions") as u32,
        daily_cost_limit: if limits.get("enabled").and_then(Value::as_bool) == Some(false) { 0. } else { f(&limits, "daily_cost_limit_usd") },
        daily_tokens_limit: if limits.get("enabled").and_then(Value::as_bool) == Some(false) { 0. } else { n(&limits, "daily_tokens_limit") },
        daily: list(month, "daily")
            .iter()
            .map(|d| Day { date: s(d, "date").to_string().into(), at: n(d, "timestamp") as u64, tokens: n(d, "total_tokens"), cost: f(d, "cost_usd") })
            .collect(),
        models: list(month, "by_model")
            .iter()
            .map(|m| ModelUse {
                model: s(m, "model").to_string().into(),
                billing: if f(m, "cost_usd") > 0. {
                    "metered".into()
                } else if f(m, "codex_nominal_cost_usd") > 0. {
                    "subscription".into()
                } else {
                    "unpriced".into()
                },
                tokens: token_split(m),
                turns: f(m, "turns") as u32,
                sessions: f(m, "sessions") as u32,
                cost: f(m, "cost_usd"),
                nominal: f(m, "codex_nominal_cost_usd"),
            })
            .collect(),
        top_sessions: list(month, "recent_sessions")
            .iter()
            .map(|x| SessionUse {
                title: s(x, "title").to_string().into(),
                model: s(x, "model").to_string().into(),
                tokens: n(x, "total_tokens"),
                turns: f(x, "turn_count") as u32,
                last_active: n(x, "last_active_at") as u64,
            })
            .collect(),
        media: (f(&media, "image_count") as u32, f(&media, "video_count") as u32, f(&media, "audio_count") as u32, f(&media, "total_cost_usd")),
        runs_today: (total.saturating_sub(failed + running), failed, running),
    }
}

fn token_split(v: &Value) -> Tokens {
    let n = |k: &str| v.get(k).and_then(Value::as_f64).unwrap_or(0.);
    Tokens { total: n("total_tokens"), input: n("input_tokens"), output: n("output_tokens"), cached: n("cached_tokens"), thinking: n("thinking_tokens") }
}

/// ChatGPT's plan and its windows from GET /v1/codex/account/usage
/// (used_percent, 0 to 100), or why it could not be read.
pub fn plans(chatgpt: Result<&Value, &str>) -> Vec<Plan> {
    let window_label = |seconds: f64| -> Text {
        match seconds as u64 {
            0 => "window".into(),
            s if s % 604_800 == 0 => if s == 604_800 { "week".into() } else { format!("{} weeks", s / 604_800).into() },
            s if s % 86_400 == 0 => if s == 86_400 { "day".into() } else { format!("{} days", s / 86_400).into() },
            s if s % 3_600 == 0 => format!("{} hours", s / 3_600).into(),
            s => format!("{} min", s / 60).into(),
        }
    };
    match chatgpt {
        Err(reason) => vec![Plan { name: "ChatGPT".into(), kind: "".into(), connected: false, note: reason.to_string().into(), limit_reached: false, windows: vec![] }],
        Ok(c) => {
            let limit = c.get("rate_limit").cloned().unwrap_or(Value::Null);
            let windows = ["primary_window", "secondary_window"]
                .iter()
                .filter_map(|k| limit.get(k).filter(|w| !w.is_null()))
                .map(|w| PlanWindow { label: window_label(n(w, "limit_window_seconds")), used: f(w, "used_percent"), resets_at: n(w, "reset_at") as u64 })
                .collect();
            vec![Plan {
                name: "ChatGPT".into(),
                kind: s(c, "plan_type").to_string().into(),
                connected: true,
                note: "".into(),
                limit_reached: limit.get("limit_reached").and_then(Value::as_bool).unwrap_or(false),
                windows,
            }]
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    // Purpose: the wait line Swarm records in history (system message, source
    // task_wait_notice) shows in the transcript as one status line, start and
    // end alike, while other system messages (the outcome result the model
    // reads) add no line. Threat: the person never seeing the wait in the
    // conversation, or untrusted titles adding lines. Boundary: map::entries
    // over Swarm's message records, as a reload reads them.
    #[test]
    fn task_wait_lines_from_history_become_notices() {
        let got = entries(&[
            json!({"id": "m1", "role": "user", "content": "start"}),
            json!({"id": "n1", "role": "system", "content": "This conversation is now waiting on 2 task(s) on forge: Fix login,\nAdd docs. It wakes when they settle; a new message ends the wait.", "metadata": {"source": "task_wait_notice", "task_wait_notice": "waiting", "task_count": 2, "machine": "forge"}}),
            json!({"id": "r1", "role": "system", "content": "Delegated project task outcomes:\n{}", "metadata": {"task_wait_owner_run_id": "run-1"}}),
            json!({"id": "n2", "role": "system", "content": "Wait ended: tasks settled.", "metadata": {"source": "task_wait_notice", "task_wait_notice": "ended"}}),
        ]);
        assert_eq!(got.len(), 3);
        assert!(matches!(&got[1], Entry::Notice(t) if t.as_ref() == "This conversation is now waiting on 2 task(s) on forge: Fix login, Add docs. It wakes when they settle; a new message ends the wait."));
        assert!(matches!(&got[2], Entry::Notice(t) if t.as_ref() == "Wait ended: tasks settled."));
    }

    // Purpose: a message Swarm queued during a run shows as queued until
    // Swarm's delivery receipt names it, then as an ordinary message; the
    // receipt adds no line of its own and touches no other message.
    // Threat: a queued message shown as delivered too early, or never.
    // Boundary: map::entry / map::apply over Swarm's message records.
    #[test]
    fn queued_message_turns_delivered_on_its_receipt() {
        let mut got = entries(&[
            json!({"id": "m1", "role": "user", "content": "start"}),
            json!({"id": "m2", "role": "user", "content": "steer left", "metadata": {"feedback_note": true, "queued_note": true, "delivery_status": "queued"}}),
            json!({"id": "m3", "role": "user", "content": "and right", "metadata": {"feedback_note": true, "queued_note": true}}),
        ]);
        assert!(matches!(&got[1], Entry::Queued { id, text } if id.as_ref() == "m2" && text.as_ref() == "steer left"));
        apply(&mut got, &json!({"role": "system", "content": "Feedback m2 delivered", "metadata": {"source": "feedback_delivery", "message_id": "m2", "delivery_status": "delivered"}}), false);
        assert_eq!(got.len(), 3);
        assert!(matches!(&got[1], Entry::User(t) if t.as_ref() == "steer left"));
        assert!(matches!(&got[2], Entry::Queued { id, .. } if id.as_ref() == "m3"));
        apply(&mut got, &json!({"role": "system", "metadata": {"source": "feedback_delivery", "message_id": "unknown"}}), false);
        assert_eq!(got.len(), 3);
        assert!(matches!(&got[2], Entry::Queued { .. }));
    }

    #[test]
    fn messages_become_entries() {
        let messages = vec![
            json!({"role": "user", "content": "# Mission: Greeting\n\n## User Objective\nAdd a line to README.md.\n\n## Injected Context Pool\n- lots"}),
            json!({"role": "tool", "content": "{}", "metadata": {"tool_name": "read", "tool_call": {"arguments": {"path": "/p/README.md"}}}}),
            json!({"role": "tool", "content": "{}", "metadata": {"tool_name": "find", "tool_call": {"arguments": {"queries": ["README.md", "AGENTS.md"]}}}}),
            json!({"role": "assistant", "content": "Added it."}),
        ];
        let entries = entries(&messages);
        assert!(matches!(&entries[0], Entry::User(t) if t.as_ref() == "Add a line to README.md."));
        assert!(matches!(&entries[1], Entry::Tool { name, detail, .. } if name.as_ref() == "read" && detail.as_ref() == "/p/README.md"));
        assert!(matches!(&entries[2], Entry::Tool { detail, .. } if detail.as_ref() == "README.md, AGENTS.md"));
        assert!(matches!(&entries[3], Entry::Assistant(t) if t.as_ref() == "Added it."));
        let view = json!({"agentic_settings": {"effective_preference": {"model": "gpt-6.1-sol", "provider": "codex", "thinking": "high"}}});
        assert_eq!(model_line(&view), "gpt-6.1-sol · high");
    }

    #[test]
    fn stored_tool_results_get_a_summary() {
        let output = json!({"exit_code": 0, "output": "built\nok  pkg 0.4s\n"}).to_string();
        let record = json!({"type": "v3_provider_tool_result", "tool_name": "bash", "call_id": "c9", "arguments": "{\"command\":\"go test ./pkg\"}", "output": output, "duration_ms": 2400}).to_string();
        let got = entry(&json!({"role": "tool", "content": record, "metadata": {"tool_name": "bash", "call_id": "c9"}}), false);
        let [Entry::Tool { detail, call_id, state, summary, duration_ms, .. }] = got.as_slice() else { panic!("one tool line") };
        assert_eq!((detail.as_ref(), call_id.as_ref(), *state, *duration_ms), ("go test ./pkg", "c9", ToolState::Done, Some(2400)));
        assert_eq!(summary.as_ref().map(|s| s.as_ref()), Some("exit 0 · ok pkg 0.4s"));
        assert_eq!(got[0].tool_line(0).unwrap().text, "bash go test ./pkg · exit 0 · ok pkg 0.4s · 2.4s");

        let failed = json!({"tool_name": "read", "call_id": "c10", "arguments": "{\"path\":\"/p/x.md\"}", "error": "open /p/x.md: no such file"}).to_string();
        let got = entry(&json!({"role": "tool", "content": failed, "metadata": {"tool_name": "read"}}), false);
        assert!(matches!(got.as_slice(), [Entry::Tool { state: ToolState::Failed, summary: Some(s), .. }] if s.as_ref() == "open /p/x.md: no such file"));

        // Plain stored content (no record) is the output itself.
        let got = entry(&json!({"role": "tool", "content": "a.rs\nb.rs\nc.rs", "metadata": {"tool_name": "glob", "tool_call": {"arguments": {"pattern": "*.rs"}}}}), false);
        assert!(matches!(got.as_slice(), [Entry::Tool { summary: Some(s), .. }] if s.as_ref() == "3 files"));
    }

    #[test]
    fn orchestrator_sessions_and_their_cards() {
        let projects = projects(&json!({"projects": [{"id": "proj_1", "name": "MYPROJECT", "workspaces": [{"path": "/p/demo-app"}]}]}));
        let shells = [
            json!({"id": "s1", "title": "Orchestrator test", "metadata": {"agent_name": "system-orchestrator", "role": "project_orchestrator", "project_id": "proj_1"}}),
            json!({"id": "s2", "title": "[MYPROJECT] hey", "metadata": {"agent_name": "swarm", "role": "project_task", "project_id": "proj_1"}}),
            json!({"id": "s3", "metadata": {"role": "project_orchestrator", "project_id": "proj_gone"}}),
        ];
        let running = json!({"active": true, "status": "running", "started_at": 1000});
        let got: Vec<Session> = shells.iter().filter_map(|sh| session(sh, &running, false, &projects)).collect();
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].live.as_ref().unwrap().session_id.as_ref(), "s1");
        assert_eq!(got[0].state, RunState::Running);
        assert_eq!(session(&shells[0], &json!({}), true, &projects).unwrap().state, RunState::NeedsYou);
        let tasks = session_tasks(
            &[json!({"id": "task_b4784618", "session_id": "77d9a980", "origin_session_id": "s1", "agent": "coder", "status": "pending_approval", "title": "Add MIT LICENSE"}),
              json!({"id": "task_other", "origin_session_id": "s9", "status": "completed"})],
            "s1",
        );
        assert_eq!(tasks.len(), 1);
        assert_eq!(tasks[0].key.as_ref(), "task_b4784618");
        assert_eq!(tasks[0].status, TaskStatus::PendingApproval);
        assert_eq!(tasks[0].role.as_ref(), "coder");
    }

    // Purpose: Swarm's run state for a run asleep on wait_tasks (status
    // waiting_tasks, active false) maps to Waiting with what it waits on, not
    // to Running, so the state survives a reconnect from the snapshot alone.
    // A question still comes first. Authority: run_clock, task_wait, session.
    #[test]
    fn a_run_waiting_on_tasks_is_waiting_not_running() {
        let projects = projects(&json!({"projects": [{"id": "proj_1", "name": "App", "workspaces": [{"path": "/p/app"}]}]}));
        let shell = json!({"id": "s1", "title": "Ship it", "metadata": {"role": "project_orchestrator", "project_id": "proj_1"}});
        let waiting = json!({"active": false, "run_id": "run-1", "status": "waiting_tasks", "task_wait": {"project_id": "proj_1", "machine": "forge", "tasks": [{"task_id": "t1"}, {"task_id": "t2"}]}});
        assert!(!run_clock(&waiting, 0).active);
        let got = session(&shell, &waiting, false, &projects).unwrap();
        assert_eq!(got.state, RunState::Waiting);
        let wait = got.waiting_on.unwrap();
        assert_eq!((wait.project_id.as_ref(), wait.machine.as_ref(), wait.tasks.len()), ("proj_1", "forge", 2));
        // Stop needs the waiting run's id (the session detail has none).
        assert_eq!(wait.run_id.as_ref(), "run-1");
        assert_eq!(wait.line(), "waiting for tasks: t1, t2 · on forge");
        // The composer's badge comes from the snapshot alone (reconnect safe).
        assert_eq!(crate::data::composer_badge(got.state, Some(&wait)).as_deref(), Some("⏳ Waiting · 2 tasks on forge"));
        // A run intent from a live event has no `active`: its status decides.
        let intent = json!({"status": "waiting_tasks"});
        assert!(!run_clock(&intent, 0).active);
        assert_eq!(session(&shell, &intent, false, &projects).unwrap().state, RunState::Waiting);
        assert_eq!(session(&shell, &waiting, true, &projects).unwrap().state, RunState::NeedsYou);
        // Running and finished runs wait on nothing.
        assert!(task_wait(&json!({"active": true, "status": "running"})).is_none());
        assert_eq!(session(&shell, &json!({"status": "running"}), false, &projects).unwrap().state, RunState::Running);
        assert_eq!(session(&shell, &json!({"status": "completed"}), false, &projects).unwrap().state, RunState::Idle);
    }

    #[test]
    fn pending_questions_and_approvals() {
        let args = r#"{"options":[{"description":"Add a short, friendly welcome line to README.md.","label":"README update"},{"description":"Have a Coder inspect the repository and report what exists, without changing files.","label":"Repository inspection"},{"label":"Custom response","value":"__custom__","description":"Type your own response.","allow_custom":true}],"question":"What should we delegate to a Coder as a test?"}"#;
        let q = pending(&[json!({"id": "perm_1", "status": "pending", "tool_name": "ask_user", "tool_arguments": args})]).unwrap();
        assert!(q.question && !q.structured);
        assert_eq!(q.questions[0].id.as_ref(), "q_1");
        let labels: Vec<&str> = q.questions[0].options.iter().map(|o| o.label.as_ref()).collect();
        assert_eq!(labels, vec!["README update", "Repository inspection", "Custom response"]);
        assert!(q.questions[0].options[2].custom && !q.questions[0].options[0].custom);
        let mut answers = std::collections::HashMap::new();
        assert_eq!(answer(&q, &answers), None);
        answers.insert(Text::from("q_1"), "README update".to_string());
        assert_eq!(answer(&q, &answers).as_deref(), Some("README update"));

        // Swarm names it ask-user too.
        assert!(pending(&[json!({"id": "perm_9", "status": "pending", "tool_name": "ask-user", "tool_arguments": args})]).unwrap().question);
        let several = pending(&[json!({"id": "perm_3", "tool_name": "ask_user", "tool_arguments":
            {"questions": [{"id": "branch", "question": "Which branch?", "options": ["main", "dev"]},
                           {"question": "Run tests?", "required": false, "options": ["yes", "no"]}]}})]).unwrap();
        assert!(several.structured);
        assert_eq!(several.questions[1].id.as_ref(), "q_2");
        let mut answers = std::collections::HashMap::new();
        answers.insert(Text::from("branch"), "dev".to_string());
        assert_eq!(answer(&several, &answers).as_deref(), Some(r#"{"answers":{"branch":"dev"}}"#));

        let records = [
            json!({"id": "perm_c", "tool_name": "machine_run", "tool_arguments": {"command": "ls"}}),
            json!({"id": "perm_2", "tool_name": "bash", "tool_arguments": {"command": "rm -rf build"}}),
            json!({"id": "perm_old", "status": "approved", "tool_name": "bash", "tool_arguments": {}}),
        ];
        let a = pending(&records).unwrap();
        assert!(!a.question);
        assert_eq!(a.text.as_ref(), "bash wants to run: rm -rf build");
        let calls = client_calls(&records);
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].tool.as_ref(), "machine_run");
        assert!(pending(&[]).is_none());
    }

    #[test]
    fn machine_roles_and_usage() {
        let status = json!({"status": "ok", "machine": {"name": "swarm", "commit": "67c69d05dee58cf0", "version": "dev", "uptime_ms": 7_500_000},
            "workers": {"active_runs": 1, "today_runs": 4, "today_failed": 1}, "usage": {"today_cost_usd": 0.5},
            "sandbox": {"active": false, "mode": "off"}});
        let m = machine(&status, "ssh");
        assert_eq!(m.build.as_ref().map(ToString::to_string).as_deref(), Some("dev 67c69d05d"));
        assert_eq!(m.uptime.as_ref().map(ToString::to_string).as_deref(), Some("2h 5m"));
        assert_eq!(m.health, Health::Ok);
        assert_eq!(m.cost_today, Some(0.5));
        assert_eq!(m.sandbox.as_ref().map(ToString::to_string).as_deref(), Some("off"));
        let r = roles(&json!({"agent_model_settings": {"swarm": {"plan": {"provider": "anthropic", "model": "opus"}}, "system_agents": {"coder": {"provider": "codex", "model": "gpt-6.1-sol", "thinking": "medium"}}}}));
        assert_eq!(r[0].label.as_ref(), "Orchestrator");
        assert_eq!(r[1].key.as_ref(), "system-coder");
        assert_eq!(r[1].default.short(), "gpt-6.1-sol · medium");
        let chatgpt = json!({"plan_type": "pro", "rate_limit": {"limit_reached": false, "primary_window": {"used_percent": 8, "limit_window_seconds": 604800, "reset_at": 1793087152u64}}});
        let u = usage(
            &json!({"summary": {"total_cost_usd": 0, "total_tokens": 63220}, "limits": {"daily_cost_limit_usd": 0, "daily_tokens_limit": 0, "enabled": false}}),
            &json!({"summary": {"total_cost_usd": 0, "codex_nominal_cost_usd": 8.22481, "total_tokens": 1611031, "cached_tokens": 1160704, "total_turns": 101, "total_sessions": 12},
                "by_model": [{"model": "gpt-6-astra", "total_tokens": 1252870, "codex_nominal_cost_usd": 8.22481, "turns": 57, "sessions": 7}],
                "daily": [{"date": "2026-10-21", "timestamp": 1792540800000u64, "total_tokens": 1611031, "cost_usd": 0}],
                "recent_sessions": [{"title": "hey what's up", "model": "gpt-6-astra", "total_tokens": 488776, "turn_count": 20, "last_active_at": 1792580000000u64}],
                "media": {"image_count": 0, "video_count": 0, "audio_count": 0, "total_cost_usd": 0}}),
            &status,
            Ok(&chatgpt),
        );
        assert_eq!(u.today.total, 63220.);
        assert_eq!(u.daily_cost_limit, 0.);
        assert_eq!(u.runs_today, (2, 1, 1));
        assert_eq!((u.month.cached, u.turns, u.sessions), (1160704., 101, 12));
        assert_eq!(u.models[0].billing.as_ref(), "subscription");
        assert_eq!(u.top_sessions[0].turns, 20);
        assert_eq!((u.plans[0].kind.as_ref(), u.plans[0].connected), ("pro", true));
        assert_eq!((u.plans[0].windows[0].label.as_ref(), u.plans[0].windows[0].used), ("week", 8.));
        assert!(!plans(Err("not signed in"))[0].connected);
    }

    #[test]
    fn summary_status_and_reasons() {
        for (status, health) in [("ok", Health::Ok), ("attention", Health::Attention), ("degraded", Health::Degraded), ("", Health::Unknown), ("new-word", Health::Unknown)] {
            let m = machine(&json!({"status": status, "reasons": ["daily spending limit reached"]}), "local");
            assert_eq!(m.health, health, "{status}");
            assert_eq!(m.reasons, vec![Text::from("daily spending limit reached")]);
        }
        // Degraded is its own state, never offline.
        assert_ne!(machine(&json!({"status": "degraded"}), "local").health, Health::Offline);
    }

    #[test]
    fn summary_update_setting_and_drain() {
        let now = 10 * 3_600_000;
        let m = machine(
            &json!({"machine": {
                "update": {"auto": "on", "channel": "dev", "state": "installed", "at": now - 4 * 60_000},
                "drain": {"draining": true, "since_ms": now - 5 * 60_000, "deadline_ms": now + 25 * 60_000, "timed_out": false, "blocking_runs": 2}
            }}),
            "paired",
        );
        assert_eq!(m.updates_line(now), "automatic · channel dev · state written 4m ago");
        assert_eq!(m.drain_line(now), "draining for 5m: new work waits, 2 runs to finish · deadline in 25m");
        let idle = machine(&json!({"machine": {"drain": {"draining": false, "since_ms": 0, "deadline_ms": 0}}}), "local");
        assert_eq!(idle.drain_line(now), "not draining");
        assert_eq!(idle.updates_line(now), "—");
        // An older Swarm without the ms times still says it drains.
        let old = machine(&json!({"machine": {"drain": {"draining": true, "timed_out": true, "blocking_runs": 1}}}), "local");
        assert_eq!(old.drain_line(now), "draining: new work waits, 1 run to finish · its deadline passed");
        assert_eq!(machine(&json!({}), "local").drain_line(now), "—");
    }

    #[test]
    fn summary_without_fields_leaves_them_unknown() {
        let m = machine(&json!({"ok": true, "status": "ok", "reasons": [], "machine": {}, "runs": {}, "usage": {}}), "local");
        assert_eq!(m.name.as_ref(), "swarm");
        assert_eq!((m.build.clone(), m.uptime.clone(), m.sandbox.clone(), m.cost_today, m.busy, m.runs), (None, None, None, None, None, None));
        assert_eq!(m.update, UpdateState::Unknown);
        assert_eq!(m.build_line(), "— · update —");
        assert_eq!(m.now_line(), "—");
        assert_eq!(m.runs_line(), "—");
        assert_eq!(m.cost_line(), "—");
        assert!(m.reasons.is_empty());
        // Idle is a reading, not a default.
        let idle = machine(&json!({"busy": {"busy": false, "sessions": 0, "worker_runs": 0}, "runs": {"total": 0, "succeeded": 0, "failed": 0, "running_now": 0}}), "local");
        assert_eq!(idle.now_line(), "idle");
        assert_eq!(idle.runs_line(), "0 runs");
        let wanted = machine(&json!({"sandbox": {"mode": "required", "active": false, "reason": "runsc missing"}}), "local");
        assert_eq!(wanted.sandbox.as_ref().map(ToString::to_string).as_deref(), Some("not running"));
    }

    #[test]
    fn every_update_state() {
        let state = |s: &str, candidate: &str| update_state(&json!({"auto": "on", "channel": "dev", "state": s, "reason": "why", "running": "aaaaaaaaa", "candidate": candidate}));
        assert_eq!(update_state(&json!({})), UpdateState::Unknown);
        assert_eq!(state("current", ""), UpdateState::Current);
        assert_eq!(state("installed", "bbbbbbbbbbbb"), UpdateState::Current);
        for s in ["waiting", "waiting_review", "blocked"] {
            assert_eq!(state(s, "bbbbbbbbbbbb"), UpdateState::Behind { candidate: "bbbbbbbbb".into(), reason: "why".into() }, "{s}");
        }
        // Waiting with nothing named is not "behind": it says so in its own words.
        assert_eq!(state("waiting", ""), UpdateState::Other { state: "waiting".into(), reason: "why".into() });
        for s in ["building", "draining"] {
            assert_eq!(state(s, "bbbbbbbbbbbb"), UpdateState::Updating { candidate: "bbbbbbbbb".into(), reason: "why".into() }, "{s}");
        }
        for s in ["failed", "rolled_back"] {
            assert_eq!(state(s, "bbbbbbbbbbbb"), UpdateState::Failed { reason: "why".into() }, "{s}");
        }
        assert_eq!(state("something_new", ""), UpdateState::Other { state: "something new".into(), reason: "why".into() });

        assert_eq!(UpdateState::Unknown.short(), "update —");
        assert_eq!(state("current", "").short(), "current");
        assert_eq!(state("blocked", "bbbbbbbbbbbb").short(), "behind: bbbbbbbbb waiting");
        assert_eq!(state("building", "bbbbbbbbbbbb").short(), "updating to bbbbbbbbb");
        assert_eq!(state("draining", "").short(), "updating");
        assert_eq!(state("rolled_back", "c").short(), "update failed");
        assert!(state("failed", "").warns() && state("blocked", "b").warns() && !state("current", "").warns() && !state("building", "b").warns());

        let m = machine(&json!({"status": "ok", "machine": {"version": "v0.1.56", "commit": "965ba5a3c0ffee", "update": {"state": "current"}}}), "local");
        assert_eq!(m.build_line(), "v0.1.56 965ba5a3c · current");
    }

    #[test]
    fn spend_sums_only_what_is_known() {
        let known = machine(&json!({"usage": {"today_cost_usd": 1.25}}), "local");
        let unknown = machine(&json!({"status": "ok"}), "paired");
        assert_eq!(crate::data::spend_today(&[known.clone()]), (1.25, false));
        assert_eq!(crate::data::spend_today(&[known, unknown]), (1.25, true));
    }
}

/// Providers with their keys, from GET /v1/providers and GET /v1/auth/credentials.
pub fn providers(providers: &Value, credentials: &Value) -> Vec<Provider> {
    let keys: Vec<Credential> = list(credentials, "records").iter().map(credential).collect();
    list(providers, "providers")
        .iter()
        .map(|p| {
            let methods = list(p, "auth_methods");
            let id = s(p, "id").to_string();
            Provider {
                keys: keys.iter().filter(|k| k.provider.as_ref() == id).cloned().collect(),
                id: id.into(),
                ready: p.get("ready").and_then(Value::as_bool).unwrap_or(false),
                reason: if s(p, "reason").is_empty() { s(p, "run_reason") } else { s(p, "reason") }.to_string().into(),
                default_model: s(p, "default_model").to_string().into(),
                accepts_key: methods.iter().any(|m| s(m, "credential_type") == "api"),
                oauth: methods.iter().any(|m| s(m, "credential_type") == "oauth"),
            }
        })
        .collect()
}

fn credential(c: &Value) -> Credential {
    let ms = |key: &str| c.get(key).and_then(Value::as_u64).unwrap_or(0);
    let connection = c.get("connection").filter(|v| v.is_object()).map(|v| (v.get("connected").and_then(Value::as_bool).unwrap_or(false), s(v, "message").to_string().into(), v.get("verified_at").and_then(Value::as_u64).unwrap_or(0)));
    Credential {
        id: s(c, "id").to_string().into(),
        provider: s(c, "provider").to_string().into(),
        active: c.get("active").and_then(Value::as_bool).unwrap_or(false),
        auth_type: s(c, "auth_type").to_string().into(),
        label: if s(c, "label").is_empty() { s(c, "id") } else { s(c, "label") }.to_string().into(),
        last4: s(c, "last4").to_string().into(),
        created_at: ms("created_at"),
        expires_at: ms("expires_at"),
        connection,
    }
}

/// What a search found, newest first as Swarm lists it.
pub fn found(answer: &Value, projects: &[Project]) -> Vec<Found> {
    list(answer, "items")
        .iter()
        // Conversations only: a task's own session is not one this app opens.
        .filter(|item| orchestrator_project(item).is_some())
        .map(|item| {
            let meta = item.get("metadata").cloned().unwrap_or(Value::Null);
            let project_id = if s(&meta, "project_id").is_empty() { s(&meta, "swarm_v3_project_id") } else { s(&meta, "project_id") }.to_string();
            let project = projects.iter().find(|p| p.id.as_ref() == project_id).map(|p| p.name.to_string()).unwrap_or_default();
            let snippet = list(item, "snippets")
                .iter()
                .map(|sn| s(sn, "text").split_whitespace().collect::<Vec<_>>().join(" "))
                .find(|text| !text.is_empty())
                .unwrap_or_default();
            Found {
                id: s(item, "id").to_string().into(),
                title: if s(item, "title").is_empty() { "Untitled".into() } else { s(item, "title").to_string().into() },
                project_id: project_id.into(),
                project: project.into(),
                archived: item.get("archived").and_then(Value::as_bool).unwrap_or(false),
                updated_at: item.get("updated_at").and_then(Value::as_u64).unwrap_or(0),
                snippet: snippet.into(),
            }
        })
        .collect()
}

#[cfg(test)]
mod project_context_tests {
    use super::projects;
    use serde_json::json;

    // Purpose: map::projects must retain the daemon's generation attempt/error,
    // and ContextGeneration::label must never call a pending refresh ready.
    // Pure mapping is the narrowest layer proving the Projects page's status
    // contract without a window or a second client-owned generation state.
    #[test]
    fn project_generation_status() {
        for status in ["pending", "running", "generating", "ready", "failed"] {
            let records = projects(&json!({"projects": [{
                "id": "project", "workspaces": [{"path": "/srv/project", "role": "docs"}],
                "project_context": "# Old document",
                "context_generation": {"status": status, "attempt": 3, "error": "Provider unavailable"}
            }]}));
            let p = &records[0];
            assert_eq!(p.workspaces[0].as_ref(), "/srv/project");
            assert_eq!(p.roles[0].as_ref(), "docs");
            assert_eq!(p.context_generation.status.as_ref(), status);
            assert_eq!(p.context_generation.attempt, 3);
            assert_eq!(p.context_generation.error.as_ref(), "Provider unavailable");
            match status {
                "ready" => assert_eq!(p.context_generation.label(), "project.md is ready."),
                "failed" => assert_eq!(p.context_generation.label(), "Could not update project.md: Provider unavailable"),
                _ => assert_eq!(p.context_generation.label(), "Swarm is updating project.md…"),
            }
            assert_eq!(p.context_generation.updating(), !matches!(status, "ready" | "failed"));
        }
        let missing = projects(&json!({"projects": [{}]}));
        assert_eq!(missing[0].context_generation.attempt, 0);
        assert!(!missing[0].context_generation.updating());
        assert_eq!(missing[0].context_generation.label(), "Swarm has not written project.md yet.");
    }


    #[test]
    fn task_progress_from_swarm_and_without_it() {
        use crate::data::ProgressState;
        let t = super::task(&json!({"id": "t1", "status": "completed", "progress": {
            "state": "not_integrated", "summary": "1 commit not in dev · no workflow", "updated_at": 900,
            "history": [{"at": 100, "what": "started"}, {"at": 800, "what": "finished · 1 commit"}, {"at": 0, "what": "  "}]
        }}));
        assert_eq!(t.progress.state, ProgressState::NotIntegrated);
        assert_eq!(t.progress.summary.as_ref(), "1 commit not in dev · no workflow");
        assert_eq!(t.progress.history.len(), 2, "an empty entry is dropped");
        assert_eq!(t.progress.updated_at, 900);
        // An older Swarm without progress: only what the status says for sure.
        let state = |status: &str| super::task(&json!({"id": "t", "status": status})).progress.state;
        assert_eq!(state("pending_approval"), ProgressState::Waiting);
        assert_eq!(state("in_progress"), ProgressState::Working);
        assert_eq!(state("failed"), ProgressState::Failed);
        assert_eq!(state("completed"), ProgressState::Unknown, "a finished task's outcome is not guessed");
        assert_eq!(state("needs_review"), ProgressState::Unknown);
        // The tray's order: what went wrong first; finished ones fold away.
        assert!(ProgressState::Failed.rank() < ProgressState::NotIntegrated.rank());
        assert!(ProgressState::NotIntegrated.rank() < ProgressState::Working.rank());
        assert!(ProgressState::Integrated.finished() && !ProgressState::NotIntegrated.finished());
    }

    /// Purpose: a task card carries the state Swarm derives
    /// (`progress.task_state` and its reason), so rows show it instead of the
    /// run status. Threat: a pushed task whose run status stays needs_review
    /// shown as waiting for review. Without it (an older Swarm) the state is
    /// Unknown and rows fall back as before.
    #[test]
    fn task_state_from_swarm_and_without_it() {
        use crate::data::{TaskState, TaskStatus};
        let t = super::task(&json!({"id": "t1", "status": "needs_review", "progress": {
            "state": "integrated", "task_state": "landed", "task_state_reason": " pushed 5711ae410abc "
        }}));
        assert_eq!(t.status, TaskStatus::NeedsReview, "the run status is kept");
        assert_eq!(t.progress.task_state, TaskState::Landed);
        assert_eq!(t.progress.task_state_reason.as_ref(), "pushed 5711ae410abc");
        assert_eq!(t.progress.task_state.label(), "landed");
        let state = |word: &str| super::task(&json!({"id": "t", "status": "needs_review", "progress": {"task_state": word}})).progress.task_state;
        assert_eq!(state("needs_you"), TaskState::NeedsYou);
        assert_eq!(state("not_integrated"), TaskState::NotIntegrated);
        assert_eq!(state("something_new"), TaskState::Unknown);
        assert_eq!(super::task(&json!({"id": "t", "status": "needs_review"})).progress.task_state, TaskState::Unknown);
    }
}
