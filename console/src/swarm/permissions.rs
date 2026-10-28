//! The account's permissions as Swarm returns them (GET /v3/permissions),
//! mapped to the rows of the Permissions page: this device, other devices,
//! home, linked machines, in the order control flows. Pure: nothing is read
//! here. Each row words its stored value against the live state Swarm
//! computed, so the page never says more than Swarm enforces
//! (docs/access-model.md, "No bluffing").

use serde_json::{Value, json};

use super::map::{list, s};

/// A device rule: off or on (no asking: docs/access-model.md).
pub const DEVICE: &[(&str, &str)] = &[("off", "off"), ("allowed", "on")];
/// A linked machine: off, ask before sending it a task, or auto.
pub const LINKED: &[(&str, &str)] = &[("off", "off"), ("ask", "ask before sending tasks"), ("auto", "auto")];
/// Start tasks: each new task waits for approval, or starts on its own.
pub const TASKS: &[(&str, &str)] = &[("approve", "ask before sending tasks"), ("auto", "auto")];

/// One rule Swarm stores, as PATCH /v3/permissions names it.
#[derive(Clone, PartialEq, Eq, Hash, Debug)]
pub enum Rule {
    Tasks,
    /// A device's files and terminal, by its client session id.
    Files(String),
    /// A device's browser, by its client session id.
    Browser(String),
    /// Handing work to a linked machine, by its name.
    Work(String),
}

impl Rule {
    /// The PATCH body that sets this rule to `value` at `revision`.
    pub fn patch(&self, value: &str, revision: u64, client_request_id: &str) -> Value {
        let mut body = match self {
            Rule::Tasks => json!({"tasks": value}),
            Rule::Files(id) => json!({"devices": {id: {"files": value}}}),
            Rule::Browser(id) => json!({"devices": {id: {"browser": value}}}),
            Rule::Work(name) => json!({"linked": {name: {"work": value}}}),
        };
        body["expected_revision"] = json!(revision);
        body["client_request_id"] = json!(client_request_id);
        body
    }
}

/// One rule on the page.
#[derive(Clone, PartialEq, Debug)]
pub struct Row {
    pub rule: Rule,
    pub title: String,
    /// What the rule protects, in plain words.
    pub protects: String,
    /// The choices as (value, label).
    pub choices: &'static [(&'static str, &'static str)],
    /// The value Swarm stores.
    pub value: String,
    /// Another device's rule: changed only on that device.
    pub read_only: bool,
    /// "Right now: …", from the stored value and Swarm's live state.
    pub now: String,
    /// The rule has no effect right now (its machine is away, or Swarm
    /// stores a value this app does not know).
    pub unavailable: bool,
    /// Who changed it and when, and when a call last used it.
    pub changed: String,
}

#[derive(Clone, PartialEq, Debug)]
pub struct Group {
    pub title: String,
    pub note: String,
    pub rows: Vec<Row>,
    /// Said when the group has no rows.
    pub empty: &'static str,
}

#[derive(Clone, PartialEq, Debug)]
pub struct Board {
    pub revision: u64,
    pub groups: Vec<Group>,
}

/// The page from Swarm's answer, worded for the time `now` (unix ms).
pub fn board(v: &Value, now: u64) -> Result<Board, String> {
    let revision = v.get("revision").and_then(Value::as_u64).ok_or("Swarm's answer carries no permissions revision.")?;
    let devices = list(v, "devices");
    let mine: Vec<&Value> = devices.iter().filter(|d| flag(d, "this_device")).collect();
    let others: Vec<&Value> = devices.iter().filter(|d| !flag(d, "this_device")).collect();
    let this_title = match mine.first() {
        Some(d) if flag(d, "home") => format!("This device · {} · runs this Swarm", s(d, "name")),
        Some(d) => format!("This device · {}", s(d, "name")),
        None => "This device".to_string(),
    };
    let home = v.get("home").cloned().unwrap_or(Value::Null);
    let home_name = s(&home, "name");
    Ok(Board {
        revision,
        groups: vec![
            Group {
                title: this_title,
                note: "The computer this app runs on: its files, its terminal and its browser, used as you, outside Swarm's sandbox.".into(),
                rows: mine.iter().flat_map(|d| device_rows(d, false, now)).collect(),
                empty: "Swarm has not identified this app as a device, so no call reaches this computer. Connect the app again to name this computer.",
            },
            Group {
                title: "Other devices".into(),
                note: "Read-only here: change these on that device.".into(),
                rows: others.iter().flat_map(|d| device_rows(d, true, now)).collect(),
                empty: "No other device is connected to this Swarm.",
            },
            Group {
                title: if home_name.is_empty() { "Home".into() } else { format!("Home · {home_name}") },
                note: "This Swarm: its conversations, the Orchestrator, its agents, folders and sandbox.".into(),
                rows: vec![tasks_row(&home, now)],
                empty: "",
            },
            Group {
                title: "Linked machines".into(),
                note: "Other Swarms this home hands work to. They are never asked to integrate, deploy or delete, and never run commands for this home.".into(),
                rows: list(v, "linked").iter().map(|m| linked_row(m, now)).collect(),
                empty: "No machine is linked to this Swarm.",
            },
        ],
    })
}

fn flag(v: &Value, key: &str) -> bool {
    v.get(key).and_then(Value::as_bool).unwrap_or(false)
}

fn time(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

/// A device's two rules: files and terminal, then browser.
fn device_rows(d: &Value, read_only: bool, now: u64) -> Vec<Row> {
    let (id, name) = (s(d, "id").to_string(), s(d, "name"));
    let connected = flag(d, "connected");
    let changed = changed_line(d, now);
    let title = |what: &str| if read_only { format!("{name} · {what}") } else { what.to_string() };
    let files = s(d, "files").to_string();
    let browser = s(d, "browser").to_string();
    let (files_now, files_away) = device_now(&files, name, connected, "its files or terminal", &format!("commands and file changes run on {name} as you"));
    let (browser_now, browser_away) = device_now(
        &browser,
        name,
        connected,
        "its browser",
        &format!("calls go to {name}'s browser; Swarm cannot tell whether the extension is attached"),
    );
    vec![
        Row {
            rule: Rule::Files(id.clone()),
            title: title("Files and terminal"),
            protects: format!("Client tools that read and write files and run commands on {name}, as you, outside Swarm's sandbox (machine_read, machine_write, machine_run)."),
            choices: DEVICE,
            value: files,
            read_only,
            now: files_now,
            unavailable: files_away,
            changed: changed.clone(),
        },
        Row {
            rule: Rule::Browser(id),
            title: title("Browser"),
            protects: format!("Client tools that drive {name}'s browser (manage_browser)."),
            choices: DEVICE,
            value: browser,
            read_only,
            now: browser_now,
            unavailable: browser_away,
            changed,
        },
    ]
}

/// A device rule right now: off, unavailable while its client is away, or
/// what on does. True when it has no effect now.
fn device_now(value: &str, name: &str, connected: bool, what: &str, allowed: &str) -> (String, bool) {
    match value {
        "off" => (format!("Right now: off, no agent reaches {what}."), false),
        "allowed" if !connected => (format!("Right now: unavailable, {name} is not connected."), true),
        "allowed" => (format!("Right now: on, {allowed}."), false),
        other => (unknown(other), true),
    }
}

fn tasks_row(home: &Value, now: u64) -> Row {
    let value = s(home, "tasks").to_string();
    let waiting = time(home, "waiting_tasks");
    let (line, away) = match value.as_str() {
        "approve" => (format!("Right now: new tasks wait for your approval; {}.", if waiting == 0 { "none waiting".to_string() } else { format!("{waiting} waiting") }), false),
        "auto" if waiting > 0 => (format!("Right now: new tasks start on their own; {waiting} still waiting for approval."), false),
        "auto" => ("Right now: new tasks start on their own.".into(), false),
        other => (unknown(other), true),
    };
    Row {
        rule: Rule::Tasks,
        title: "Start tasks".into(),
        protects: "Delegating work to this home's own agents, media tasks included.".into(),
        choices: TASKS,
        value,
        read_only: false,
        now: line,
        unavailable: away,
        // Swarm records no change for this rule, only its use.
        changed: used(time(home, "last_used"), now),
    }
}

fn linked_row(m: &Value, now: u64) -> Row {
    let name = s(m, "name");
    // An older Swarm calls auto "allowed".
    let value = match s(m, "work") {
        "allowed" => "auto".to_string(),
        v => v.to_string(),
    };
    let contact = match time(m, "last_contact") {
        0 => "never reached".to_string(),
        at => format!("last contact {}", ago(now, at)),
    };
    let reachable = flag(m, "reachable");
    let (line, away) = match value.as_str() {
        "off" => (format!("Right now: off, no work is handed to {name}."), false),
        "ask" | "auto" if !reachable => (format!("Right now: unavailable, {name} can't be reached ({contact})."), true),
        "ask" => (format!("Right now: a new task for {name} waits for your approval; everything else runs ({contact})."), false),
        "auto" => (format!("Right now: work goes to {name} on its own ({contact})."), false),
        other => (unknown(other), true),
    };
    Row {
        rule: Rule::Work(name.to_string()),
        title: name.to_string(),
        protects: format!("Handing {name} work: its projects, tasks, workflows and workers. Only sending it a new task can wait for you."),
        choices: LINKED,
        value,
        read_only: false,
        now: line,
        unavailable: away,
        changed: changed_line(m, now),
    }
}

fn unknown(value: &str) -> String {
    if value.is_empty() {
        "Right now: Swarm returned no value for this rule.".into()
    } else {
        format!("Right now: Swarm stores \"{value}\", which this app does not know.")
    }
}

/// "changed by laptop 12 min ago · last used 3 min ago".
fn changed_line(v: &Value, now: u64) -> String {
    let change = v.get("changed").cloned().unwrap_or(Value::Null);
    let (by, at) = (s(&change, "by"), time(&change, "at"));
    let first = match (at, by) {
        (0, _) => "never changed (Swarm's default)".to_string(),
        (at, "") => format!("changed {}", ago(now, at)),
        (at, by) => format!("changed by {by} {}", ago(now, at)),
    };
    format!("{first} · {}", used(time(v, "last_used"), now))
}

fn used(at: u64, now: u64) -> String {
    if at == 0 { "never used".into() } else { format!("last used {}", ago(now, at)) }
}

/// How long before `now` a unix-ms time was, in words.
pub fn ago(now: u64, at: u64) -> String {
    let secs = now.saturating_sub(at) / 1000;
    match secs / 60 {
        0 => "just now".into(),
        m if m < 60 => format!("{m} min ago"),
        m if m < 60 * 24 => format!("{} h ago", m / 60),
        m if m < 60 * 48 => "1 day ago".into(),
        m => format!("{} days ago", m / (60 * 24)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const NOW: u64 = 1_793_000_000_000;
    const MIN: u64 = 60_000;

    fn answer() -> Value {
        json!({
            "revision": 7,
            "home": {"name": "desktop", "tasks": "approve", "waiting_tasks": 2, "last_used": NOW - 3 * MIN},
            "devices": [
                {"id": "csess_a", "name": "desktop", "this_device": true, "home": true, "connected": true,
                 "files": "allowed", "browser": "off", "changed": {"by": "desktop", "at": NOW - 12 * MIN}, "last_used": NOW - 3 * MIN},
                {"id": "csess_b", "name": "laptop", "this_device": false, "home": false, "connected": false,
                 "files": "allowed", "browser": "allowed", "changed": {"by": "", "at": 0}, "last_used": 0}
            ],
            "linked": [
                {"name": "social", "work": "allowed", "reachable": false, "last_contact": NOW - 12 * MIN, "changed": {"by": "desktop", "at": NOW - 2 * 60 * MIN}, "last_used": 0},
                {"name": "build-box", "work": "ask", "reachable": true, "last_contact": NOW - 30_000, "changed": {"by": "ops-key", "at": NOW - 3 * 24 * 60 * MIN}, "last_used": NOW - MIN}
            ]
        })
    }

    #[test]
    fn groups_follow_the_flow_of_control() {
        let b = board(&answer(), NOW).unwrap();
        assert_eq!(b.revision, 7);
        let titles: Vec<&str> = b.groups.iter().map(|g| g.title.as_str()).collect();
        assert_eq!(titles, ["This device · desktop · runs this Swarm", "Other devices", "Home · desktop", "Linked machines"]);
        let mine = &b.groups[0].rows;
        assert_eq!(mine.iter().map(|r| r.title.as_str()).collect::<Vec<_>>(), ["Files and terminal", "Browser"]);
        assert_eq!((mine[0].rule.clone(), mine[0].value.as_str(), mine[0].read_only), (Rule::Files("csess_a".into()), "allowed", false));
        assert_eq!(mine[1].rule, Rule::Browser("csess_a".into()));
        let other = &b.groups[1].rows;
        assert_eq!(other[0].title, "laptop · Files and terminal");
        assert!(other.iter().all(|r| r.read_only));
        assert_eq!(b.groups[2].rows[0].rule, Rule::Tasks);
        assert_eq!(b.groups[3].rows.iter().map(|r| r.title.as_str()).collect::<Vec<_>>(), ["social", "build-box"]);
    }

    #[test]
    fn right_now_words_the_live_state() {
        let b = board(&answer(), NOW).unwrap();
        let mine = &b.groups[0].rows;
        assert_eq!(mine[0].now, "Right now: on, commands and file changes run on desktop as you.");
        assert_eq!(mine[1].now, "Right now: off, no agent reaches its browser.");
        // Allowed on a device that is away is unavailable, not allowed.
        let laptop = &b.groups[1].rows;
        assert_eq!((laptop[0].now.as_str(), laptop[0].unavailable), ("Right now: unavailable, laptop is not connected.", true));
        assert_eq!(laptop[1].now, "Right now: unavailable, laptop is not connected.");
        let tasks = &b.groups[2].rows[0];
        assert_eq!(tasks.now, "Right now: new tasks wait for your approval; 2 waiting.");
        let social = &b.groups[3].rows[0];
        assert_eq!((social.now.as_str(), social.unavailable), ("Right now: unavailable, social can't be reached (last contact 12 min ago).", true));
        let build = &b.groups[3].rows[1];
        assert_eq!((build.now.as_str(), build.unavailable), ("Right now: a new task for build-box waits for your approval; everything else runs (last contact just now).", false));
    }

    #[test]
    fn right_now_for_the_other_values() {
        assert_eq!(device_now("allowed", "desktop", true, "x", "commands run").0, "Right now: on, commands run.");
        assert_eq!(device_now("allowed", "desktop", false, "x", "y"), ("Right now: unavailable, desktop is not connected.".into(), true));
        assert_eq!(device_now("full", "desktop", true, "x", "y"), ("Right now: Swarm stores \"full\", which this app does not know.".into(), true));
        let auto = tasks_row(&json!({"tasks": "auto", "waiting_tasks": 1}), NOW);
        assert_eq!(auto.now, "Right now: new tasks start on their own; 1 still waiting for approval.");
        let none = tasks_row(&json!({"tasks": "approve", "waiting_tasks": 0}), NOW);
        assert_eq!(none.now, "Right now: new tasks wait for your approval; none waiting.");
        let fresh = linked_row(&json!({"name": "edge", "work": "allowed", "reachable": true, "last_contact": NOW - 5 * MIN}), NOW);
        assert_eq!(fresh.now, "Right now: work goes to edge on its own (last contact 5 min ago).", "an older Swarm's allowed is auto");
        assert_eq!(fresh.value, "auto");
        let never = linked_row(&json!({"name": "edge", "work": "ask", "reachable": false, "last_contact": 0}), NOW);
        assert_eq!(never.now, "Right now: unavailable, edge can't be reached (never reached).");
        let off = linked_row(&json!({"name": "edge", "work": "off", "reachable": false}), NOW);
        assert_eq!((off.now.as_str(), off.unavailable), ("Right now: off, no work is handed to edge.", false));
    }

    #[test]
    fn who_changed_it_and_when_it_was_used() {
        let b = board(&answer(), NOW).unwrap();
        assert_eq!(b.groups[0].rows[0].changed, "changed by desktop 12 min ago · last used 3 min ago");
        assert_eq!(b.groups[1].rows[0].changed, "never changed (Swarm's default) · never used");
        assert_eq!(b.groups[2].rows[0].changed, "last used 3 min ago");
        assert_eq!(b.groups[3].rows[0].changed, "changed by desktop 2 h ago · never used");
        assert_eq!(b.groups[3].rows[1].changed, "changed by ops-key 3 days ago · last used 1 min ago");
        assert_eq!(ago(NOW, NOW + 5), "just now");
        assert_eq!(ago(NOW, NOW - 25 * 60 * MIN), "1 day ago");
    }

    #[test]
    fn without_this_device_or_a_revision() {
        let b = board(&json!({"revision": 1, "home": {"name": "srv", "tasks": "auto"}, "devices": [], "linked": []}), NOW).unwrap();
        assert_eq!(b.groups[0].title, "This device");
        assert!(b.groups[0].rows.is_empty() && b.groups[1].rows.is_empty() && b.groups[3].rows.is_empty());
        assert!(b.groups[0].empty.starts_with("Swarm has not identified this app as a device"));
        // Nothing is shown that Swarm did not return.
        assert!(board(&json!({"home": {"tasks": "auto"}}), NOW).is_err());
        assert!(board(&Value::Null, NOW).is_err());
    }

    #[test]
    fn patches_name_one_rule() {
        assert_eq!(Rule::Tasks.patch("auto", 7, "r1"), json!({"tasks": "auto", "expected_revision": 7, "client_request_id": "r1"}));
        assert_eq!(Rule::Files("csess_a".into()).patch("allowed", 7, "r2"), json!({"devices": {"csess_a": {"files": "allowed"}}, "expected_revision": 7, "client_request_id": "r2"}));
        assert_eq!(Rule::Browser("csess_a".into()).patch("off", 8, "r3"), json!({"devices": {"csess_a": {"browser": "off"}}, "expected_revision": 8, "client_request_id": "r3"}));
        assert_eq!(Rule::Work("social".into()).patch("auto", 9, "r4"), json!({"linked": {"social": {"work": "auto"}}, "expected_revision": 9, "client_request_id": "r4"}));
    }
}
