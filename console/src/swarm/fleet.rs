//! Your fleet: the Swarms your home Swarm controls. Home pairs with each one
//! itself (one way: home reaches them, they never reach home) and keeps the
//! list, GET /v3/fleet. A background thread rereads that list every 20
//! seconds and, through home's link to each machine
//! (/v3/fleet/machines/{name}/api/…), that machine's own summary
//! (GET /v3/box/summary) and its model per role (GET
//! /v1/agent-model-settings). The same tick reads what needs the person on
//! home and on each machine (swarm::needs), all at once off the UI thread.
//! The app never calls the other machines itself.
//! A Swarm without the fleet routes yet has no machines.

use std::sync::mpsc::{self, Receiver};
use std::time::Duration;

use serde_json::Value;

use super::client::{Client, Refusal};
use super::{map, needs};
use crate::data::{Health, Machine};

const EVERY: Duration = Duration::from_secs(20);

/// A machine home controls, as home lists it.
#[derive(Clone, Debug, PartialEq)]
pub struct Found {
    pub name: String,
    /// Its address on the network home reaches it on.
    pub address: String,
    /// When home last reached it (ms; 0 never).
    pub last_contact_at: u64,
    /// Why home's last call to it failed ("" when it did not).
    pub last_error: String,
}

/// The machines in home's GET /v3/fleet answer.
pub fn from_list(list: &Value) -> Vec<Found> {
    list.get("machines")
        .and_then(Value::as_array)
        .map(|machines| {
            machines
                .iter()
                .filter_map(|m| {
                    let name = m.get("name")?.as_str()?.to_string();
                    Some(Found {
                        name,
                        address: m.get("address").and_then(Value::as_str).unwrap_or("").to_string(),
                        last_contact_at: m.get("last_contact_at").and_then(Value::as_u64).unwrap_or(0),
                        last_error: m.get("last_error").and_then(Value::as_str).unwrap_or("").trim().to_string(),
                    })
                })
                .collect()
        })
        .unwrap_or_default()
}

/// The base of a linked machine's own API, through home.
pub fn machine_base(home: &str, name: &str) -> String {
    format!("{}/v3/fleet/machines/{}/api", home.trim_end_matches('/'), name)
}

/// Home's machines now.
pub fn read(home: &Client) -> Result<Vec<Found>, String> {
    match home.get("/v3/fleet") {
        Ok(v) => Ok(from_list(&v)),
        // This Swarm has no fleet routes: no machines.
        Err(Refusal::Status(404, _, _)) => Ok(Vec::new()),
        Err(e) => Err(e.to_string()),
    }
}

/// A linked machine as the console shows it: its own summary when home could
/// read it (`summary`), else offline with home's last contact and the reason;
/// its models when they were read.
pub fn machine(found: &Found, summary: Result<&Value, &str>, settings: Option<&Value>) -> Machine {
    let mut m = match summary {
        Ok(summary) => map::machine(summary, "paired"),
        Err(why) => {
            let mut m = Machine::unread("".into(), "paired".into(), "".into());
            m.health = Health::Offline;
            let why = if why.trim().is_empty() { found.last_error.as_str() } else { why };
            if !why.trim().is_empty() {
                m.reasons = vec![why.trim().to_string().into()];
            }
            m
        }
    };
    m.name = found.name.clone().into();
    m.access = "owner session held by home".into();
    m.gateway = found.address.clone().into();
    m.last_contact = (found.last_contact_at > 0).then_some(found.last_contact_at);
    m.models = settings.map(map::models);
    m
}

/// One machine read through home: its summary and models, and what on it
/// needs the person (from the same summary).
fn read_machine(home: &Client, found: &Found, recall: &needs::Recall) -> (Machine, needs::Reading) {
    let link = Client::new(machine_base(home.base(), &found.name), home.token());
    let summary = link.get("/v3/box/summary").map_err(|e| e.to_string());
    let settings = summary.as_ref().ok().and_then(|_| link.get("/v1/agent-model-settings").ok());
    let summary = summary.as_ref().map_err(String::as_str);
    let needs = needs::read(&link, false, &found.name, summary.map_err(|why| if why.trim().is_empty() { found.last_error.as_str() } else { why }), recall);
    (machine(found, summary, settings.as_ref()), needs)
}

/// What needs the person on home itself.
fn read_home(home: &Client, recall: &needs::Recall) -> needs::Reading {
    let summary = home.get("/v3/box/summary").map_err(|e| e.to_string());
    needs::read(home, true, "", summary.as_ref().map_err(String::as_str), recall)
}

/// One fleet tick: home's machines (None when home's list could not be
/// read; the last one stands) and what needs the person on home and on each.
pub struct Reading {
    pub machines: Option<Vec<Machine>>,
    /// Home first, then each machine in home's order.
    pub needs: Vec<needs::Reading>,
}

/// Every machine, read at once so one slow machine does not hold the rest.
pub fn read_all(home: &Client, found: &[Found], recall: &needs::Recall) -> (Vec<Machine>, Vec<needs::Reading>) {
    std::thread::scope(|scope| {
        let reads: Vec<_> = found.iter().map(|f| scope.spawn(move || read_machine(home, f, recall))).collect();
        reads
            .into_iter()
            .zip(found)
            .map(|(r, f)| {
                r.join().unwrap_or_else(|_| {
                    let why = "reading it failed";
                    (machine(f, Err(why), None), needs::Reading { machine: f.name.clone(), unread: vec![why.into()], ..Default::default() })
                })
            })
            .unzip()
    })
}

/// Watches home's fleet from a background thread.
pub struct Fleet {
    rx: Receiver<Reading>,
}

impl Fleet {
    pub fn start(home: Client) -> Fleet {
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            let mut recall = needs::Recall::default();
            loop {
                let (here, fleet) = std::thread::scope(|scope| {
                    let here = scope.spawn(|| read_home(&home, &recall));
                    let fleet = read(&home).map(|found| read_all(&home, &found, &recall));
                    let here = here.join().unwrap_or_else(|_| needs::Reading { home: true, unread: vec!["reading it failed".into()], ..Default::default() });
                    (here, fleet)
                });
                recall.turn();
                let mut needs = vec![here];
                let machines = match fleet {
                    Ok((machines, linked)) => {
                        needs.extend(linked);
                        Some(machines)
                    }
                    Err(_) => None,
                };
                if tx.send(Reading { machines, needs }).is_err() {
                    return; // the console is gone
                }
                std::thread::sleep(EVERY);
            }
        });
        Fleet { rx }
    }

    /// The newest reading since the last call, if any.
    pub fn latest(&self) -> Option<Reading> {
        self.rx.try_iter().last()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::data::{Assignment, Busy, Cell, RunsToday, UpdateState};
    use serde_json::json;

    /// The shape swarmd/internal/api/box_summary.go writes.
    fn summary() -> Value {
        json!({
            "ok": true, "generated_at": 1792540800000u64, "status": "attention",
            "reasons": ["agents are waiting on a person", "workers failed today"],
            "machine": {"name": "forge-host", "commit": "5a667f28a1b2c3d4", "version": "dev", "uptime_ms": 90_000_000,
                "update": {"auto": "on", "channel": "dev", "state": "waiting_review", "reason": "ai-review has not passed 3ce146eae", "running": "5a667f28a", "candidate": "3ce146eae99887766", "at": 1792540000000u64}},
            "busy": {"busy": true, "sessions": 2, "worker_runs": 1, "reviews": 0, "followups": 0, "environment_builds": 0},
            "runs": {"since": 0, "until": 0, "total": 5, "succeeded": 3, "failed": 1, "cancelled": 0, "unfinished": 1, "running_now": 1},
            "usage": {"today_cost_usd": 0.5, "today_tokens": 1200, "daily_cost_limit_usd": 0, "limit_enabled": false, "limit_exceeded": false},
            "sandbox": {"mode": "auto", "active": true, "runtime": "runsc"},
            "attention": {"blocked_sessions": 1}, "workers": {}, "automations": {}, "workspaces": {}, "keys": {}, "signals": {}, "outbox": {}
        })
    }

    fn found(name: &str, contact: u64, error: &str) -> Found {
        Found { name: name.into(), address: "10.0.0.5:7792".into(), last_contact_at: contact, last_error: error.into() }
    }

    #[test]
    fn home_list_to_found() {
        let list = json!({"machines": [
            {"name": "forge", "address": "10.0.0.5:7792", "last_contact_at": 1792540800000u64},
            {"name": "social", "address": "10.0.0.6:7792", "last_error": "connection refused"},
            {"address": "no name"}
        ]});
        let found = from_list(&list);
        assert_eq!(found.iter().map(|f| f.name.as_str()).collect::<Vec<_>>(), vec!["forge", "social"]);
        assert_eq!((found[0].last_contact_at, found[0].last_error.as_str()), (1792540800000, ""));
        assert_eq!((found[1].last_contact_at, found[1].last_error.as_str()), (0, "connection refused"));
    }

    #[test]
    fn linked_machine_from_its_summary() {
        let settings = json!({"agent_model_settings": {"system_agents": {"coder": {"provider": "codex", "model": "gpt-6.1-sol", "thinking": "medium"}}}});
        let m = machine(&found("forge", 1792540800000, ""), Ok(&summary()), Some(&settings));
        assert_eq!(m.name.as_ref(), "forge", "home's name for it, not the machine's own");
        assert_eq!(m.health, Health::Attention);
        assert_eq!(m.reasons.iter().map(|r| r.as_ref()).collect::<Vec<_>>(), vec!["agents are waiting on a person", "workers failed today"]);
        assert_eq!(m.build.as_ref().map(ToString::to_string).as_deref(), Some("dev 5a667f28a"));
        assert_eq!(m.update, UpdateState::Behind { candidate: "3ce146eae".into(), reason: "ai-review has not passed 3ce146eae".into() });
        assert_eq!(m.build_line(), "dev 5a667f28a · behind: 3ce146eae waiting");
        assert_eq!(m.busy, Some(Busy { sessions: 2, worker_runs: 1, ..Default::default() }));
        assert_eq!(m.now_line(), "2 sessions · 1 worker run");
        assert_eq!(m.runs, Some(RunsToday { total: 5, succeeded: 3, failed: 1, running_now: 1 }));
        assert_eq!(m.runs_line(), "5 runs · 3 ok · 1 failed");
        assert_eq!(m.cost_line(), "$0.50");
        assert_eq!(m.uptime_line(), "1d 1h");
        assert_eq!(m.sandbox_line(), "runsc");
        assert_eq!(m.gateway.as_ref(), "10.0.0.5:7792");
        assert_eq!(m.contact_line(1792540800000 + 4 * 60_000), "last contact 4m ago");
        assert_eq!(m.model("system-coder").map(Assignment::short).as_deref(), Some("gpt-6.1-sol · medium"));
        assert_eq!(m.model("system-finder"), None);
    }

    #[test]
    fn unreachable_machine_is_offline_with_its_last_contact() {
        let m = machine(&found("social", 1792540800000, "dial tcp: i/o timeout"), Err("Swarm is not answering."), None);
        assert_eq!(m.health, Health::Offline);
        assert_eq!(m.reasons.iter().map(|r| r.as_ref()).collect::<Vec<_>>(), vec!["Swarm is not answering."]);
        assert_eq!(m.contact_line(1792540800000 + 3 * 3_600_000), "last contact 3h ago");
        // Nothing else is known, and nothing says otherwise.
        assert_eq!((m.busy, m.runs, m.cost_today, m.build.clone(), m.uptime.clone(), m.sandbox.clone()), (None, None, None, None, None, None));
        assert_eq!(m.update, UpdateState::Unknown);
        assert_eq!(m.build_line(), "— · update —");
        assert_eq!((m.now_line(), m.runs_line(), m.cost_line(), m.uptime_line()), ("—".into(), "—".into(), "—".into(), "—".into()));
        assert!(m.models.is_none());

        // Never reached, and home's own error when the read gave none.
        let never = machine(&found("new", 0, "connection refused"), Err(""), None);
        assert_eq!(never.contact_line(1792540800000), "never reached");
        assert_eq!(never.reasons[0].as_ref(), "connection refused");
    }

    #[test]
    fn cells_compare_with_the_default_or_stay_unknown() {
        let home_settings = json!({"agent_model_settings": {"system_agents": {
            "coder": {"provider": "codex", "model": "gpt-6.1-sol", "thinking": "medium"},
            "finder": {"provider": "google", "model": "gemini-3.6-flash", "thinking": "low"}}}});
        let roles = map::roles(&home_settings);
        let mut home = map::machine(&summary(), "local");
        home.models = Some(map::models(&home_settings));
        let same_coder = json!({"agent_model_settings": {"system_agents": {
            "coder": {"provider": "codex", "model": "gpt-6.1-sol", "thinking": "medium"},
            "finder": {"provider": "google", "model": "gemini-3.8-flash", "thinking": "low"}}}});
        let machines = vec![
            home,
            machine(&found("forge", 1, ""), Ok(&summary()), Some(&same_coder)),
            machine(&found("social", 1, "refused"), Err("refused"), None),
            machine(&found("old", 1, ""), Ok(&summary()), Some(&json!({}))),
        ];
        let coder = &roles[0];
        let finder = &roles[1];
        assert_eq!(crate::data::cell(coder, &machines, 0), Cell::InSync);
        assert_eq!(crate::data::cell(coder, &machines, 1), Cell::InSync);
        assert!(matches!(crate::data::cell(finder, &machines, 1), Cell::Custom(a) if a.model.as_ref() == "gemini-3.8-flash"));
        assert_eq!(crate::data::cell(coder, &machines, 2), Cell::Unknown, "not read is unknown, never the default");
        assert_eq!(crate::data::cell(coder, &machines, 3), Cell::Unknown, "a role it did not list is unknown");
        assert_eq!(crate::data::cell(coder, &machines, 9), Cell::Unknown);
    }
}
