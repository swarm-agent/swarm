//! Fleet model settings: a linked machine runs another model for a role than
//! the home; "apply home's setting" sends the home's setting for that role to
//! the machine, the same request the home's own "change default…" sends
//! (PATCH /v1/agent-model-settings, models::default_body), through home's
//! link to it (/v3/fleet/machines/{name}/api/…), then reads the machine's
//! settings again. Nothing is changed without the person's click, and never
//! while the home's linked work rule for that machine is off.

use serde_json::Value;

use super::client::Client;
use super::fleet::machine_base;
use super::map::{self, list, s};
use super::models;
use crate::data::{Assignment, Cell, Machine, Role, Text};

/// The machines (by index, never home's 0) whose own setting for `role`
/// differs from the home's. A machine whose setting was not read is not
/// one of them: nothing is known to differ.
pub fn differing(role: &Role, machines: &[Machine]) -> Vec<usize> {
    (1..machines.len()).filter(|mi| matches!(crate::data::cell(role, machines, *mi), Cell::Custom(_))).collect()
}

/// Why sending work to `machine` is not allowed now, from the home's
/// GET /v3/permissions (linked.{name}.work); None when it is ask or allowed.
pub fn work_blocked(permissions: Option<&Value>, machine: &str) -> Option<String> {
    let Some(permissions) = permissions else {
        return Some("The home's permissions are not read yet.".into());
    };
    let Some(rule) = list(permissions, "linked").iter().find(|l| s(l, "name") == machine) else {
        return Some(format!("The home's permissions name no rule for {machine}."));
    };
    match s(rule, "work") {
        "ask" | "allowed" => None,
        "off" => Some(format!("linked.{machine}.work is off on the Permissions page.")),
        "" => Some(format!("Swarm returned no work rule for {machine}.")),
        other => Some(format!("linked.{machine}.work is \"{other}\", which this app does not know.")),
    }
}

/// The path, on the home, of a linked machine's own model settings.
pub fn settings_path(machine: &str) -> String {
    format!("/v3/fleet/machines/{machine}/api/v1/agent-model-settings")
}

/// Sends the home's setting for `role` to `machine` (Err: the machine's or
/// home's refusal), then reads the machine's settings back: its models per
/// role as it now reports them, or why they could not be read.
pub fn apply(home: &Client, machine: &str, role: &str, a: &Assignment) -> Result<Result<Vec<(Text, Assignment)>, String>, String> {
    home.patch(&settings_path(machine), models::default_body(role, a)).map_err(|e| e.to_string())?;
    Ok(read(home, machine))
}

/// A linked machine's models per role, read through home.
pub fn read(home: &Client, machine: &str) -> Result<Vec<(Text, Assignment)>, String> {
    let link = Client::new(machine_base(home.base(), machine), home.token());
    link.get("/v1/agent-model-settings").map(|v| map::models(&v)).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn settings(coder: &str, finder: &str) -> Value {
        json!({"agent_model_settings": {
            "swarm": {"plan": {"provider": "anthropic", "model": "opus-5.5", "thinking": "high"}},
            "system_agents": {
                "coder": {"provider": "codex", "model": coder, "thinking": "medium"},
                "finder": {"provider": "google", "model": finder, "thinking": "low", "service_tier": "priority"}}}})
    }

    fn machine(name: &str, models: Option<&Value>) -> Machine {
        let mut m = Machine::unread(name.to_string().into(), "paired".into(), "".into());
        m.models = models.map(map::models);
        m
    }

    #[test]
    fn only_machines_known_to_differ_are_offered() {
        let home = settings("gpt-6.1-sol", "gemini-3.6-flash");
        let roles = map::roles(&home);
        let machines = vec![
            machine("pc", Some(&home)),
            machine("forge", Some(&settings("gpt-6.1-sol", "gemini-3.8-flash"))),
            machine("social", None),
            machine("box", Some(&settings("gpt-5-mini", "gemini-3.8-flash"))),
            machine("old", Some(&json!({}))),
        ];
        let by_key = |k: &str| roles.iter().find(|r| r.key.as_ref() == k).unwrap();
        assert_eq!(differing(by_key("system-coder"), &machines), vec![3]);
        assert_eq!(differing(by_key("system-finder"), &machines), vec![1, 3], "not read and not listed are not offered");
        assert!(differing(by_key("plan"), &machines).is_empty());
    }

    #[test]
    fn the_request_is_the_homes_own_setting_for_that_role() {
        let roles = map::roles(&settings("gpt-6.1-sol", "gemini-3.6-flash"));
        let by_key = |k: &str| roles.iter().find(|r| r.key.as_ref() == k).unwrap();
        let finder = by_key("system-finder");
        assert_eq!(
            models::default_body(&finder.key, &finder.default),
            json!({"system_agents": {"finder": {"provider": "google", "model": "gemini-3.6-flash", "thinking": "low", "service_tier": "priority"}}})
        );
        let plan = by_key("plan");
        assert_eq!(models::default_body(&plan.key, &plan.default), json!({"swarm": {"plan": {"provider": "anthropic", "model": "opus-5.5", "thinking": "high"}}}));
        assert_eq!(settings_path("forge"), "/v3/fleet/machines/forge/api/v1/agent-model-settings");
        assert_eq!(machine_base("http://127.0.0.1:7781", "forge") + "/v1/agent-model-settings", format!("http://127.0.0.1:7781{}", settings_path("forge")));
    }

    #[test]
    fn the_linked_work_rule_gates_the_action() {
        let p = json!({"revision": 3, "linked": [
            {"name": "forge", "work": "ask"},
            {"name": "box", "work": "allowed"},
            {"name": "social", "work": "off"},
            {"name": "odd", "work": "sometimes"}
        ]});
        assert_eq!(work_blocked(Some(&p), "forge"), None);
        assert_eq!(work_blocked(Some(&p), "box"), None);
        assert_eq!(work_blocked(Some(&p), "social").as_deref(), Some("linked.social.work is off on the Permissions page."));
        assert!(work_blocked(Some(&p), "odd").is_some());
        assert!(work_blocked(Some(&p), "new").is_some(), "no rule read, no change");
        assert!(work_blocked(None, "forge").is_some(), "not read yet, no change");
    }
}
