//! Fleet keys: the home is its fleet's key manager for provider API keys
//! (docs/frontends.md "Fleet keys", swarmd/internal/api/fleet_keys.go).
//!
//! The console never handles a key value. It asks the home for a one-time
//! link to Swarm's own key page (POST /v3/fleet/keys/requests), opens it in
//! the person's browser, and shows what the home records: each key's
//! fingerprint and, per machine and for the home, pending, installed,
//! failed (with Swarm's reason) or removed (GET /v3/fleet/keys). Installing
//! a kept key on more machines and revoking it are the home's own routes
//! (POST /v3/fleet/keys/{id}/install, …/revoke). Everything here is home
//! routes only; the machine-side routes are never called.

use serde_json::{Value, json};

use super::client::Client;
use super::map::{list, s};

/// A key's state on one machine (or on the home), as the home records it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum State {
    Pending,
    Installed,
    Failed,
    Removed,
    /// A value this app does not know, kept as Swarm wrote it.
    Other(String),
}

impl State {
    fn of(v: &str) -> State {
        match v {
            "pending" => State::Pending,
            "installed" => State::Installed,
            "failed" => State::Failed,
            "removed" => State::Removed,
            other => State::Other(other.to_string()),
        }
    }

    pub fn label(&self) -> String {
        match self {
            State::Pending => "pending".into(),
            State::Installed => "installed".into(),
            State::Failed => "failed".into(),
            State::Removed => "removed".into(),
            State::Other(v) if v.is_empty() => "no state".into(),
            State::Other(v) => v.clone(),
        }
    }
}

/// One machine (or the home) a key was sent to.
#[derive(Clone, Debug, PartialEq)]
pub struct Target {
    /// The machine's name; for the home, the home's name ("" when Swarm
    /// gives none).
    pub machine: String,
    pub home: bool,
    pub state: State,
    /// Swarm's reason (a failure, or "not removed: …"); never key material.
    pub error: String,
    pub verified: bool,
    pub installed_at: u64,
    pub removed_at: u64,
    pub updated_at: u64,
}

/// One provider key the home manages; never its value.
#[derive(Clone, Debug, PartialEq)]
pub struct Key {
    pub id: String,
    pub provider: String,
    pub label: String,
    pub fingerprint: String,
    pub last4: String,
    pub created_at: u64,
    pub created_by: String,
    /// Removed everywhere and the home deleted its value: it cannot be
    /// installed again.
    pub revoked_at: u64,
    /// The home first (when it has a target), then machines by name.
    pub targets: Vec<Target>,
}

/// The home's answer to GET /v3/fleet/keys.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct List {
    pub revision: u64,
    /// The home's own name as Swarm gives it ("" when it has none).
    pub home: String,
    pub keys: Vec<Key>,
}

fn ms(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

fn target(machine: &str, home: bool, v: &Value) -> Target {
    Target {
        machine: machine.to_string(),
        home,
        state: State::of(s(v, "state")),
        error: s(v, "error").trim().to_string(),
        verified: v.get("verified").and_then(Value::as_bool).unwrap_or(false),
        installed_at: ms(v, "installed_at"),
        removed_at: ms(v, "removed_at"),
        updated_at: ms(v, "updated_at"),
    }
}

/// One key record, with the home's name for its home target.
pub fn key(v: &Value, home: &str) -> Key {
    let mut targets = Vec::new();
    if let Some(h) = v.get("home").filter(|h| h.is_object()) {
        targets.push(target(home, true, h));
    }
    let mut machines: Vec<Target> = v
        .get("machines")
        .and_then(Value::as_object)
        .map(|m| m.iter().map(|(name, t)| target(name, false, t)).collect())
        .unwrap_or_default();
    machines.sort_by(|a, b| a.machine.cmp(&b.machine));
    targets.extend(machines);
    Key {
        id: s(v, "id").to_string(),
        provider: s(v, "provider").to_string(),
        label: s(v, "label").to_string(),
        fingerprint: s(v, "fingerprint").to_string(),
        last4: s(v, "last4").to_string(),
        created_at: ms(v, "created_at"),
        created_by: s(v, "created_by").to_string(),
        revoked_at: ms(v, "revoked_at"),
        targets,
    }
}

/// The list as Swarm returns it, in Swarm's order; records without an id
/// are left out.
pub fn list_of(v: &Value) -> List {
    let home = s(v, "home").to_string();
    List {
        revision: ms(v, "revision"),
        keys: list(v, "keys").iter().map(|k| key(k, &home)).filter(|k| !k.id.is_empty()).collect(),
        home,
    }
}

impl Key {
    /// The home's target, if the key was ever meant for the home.
    pub fn home_target(&self) -> Option<&Target> {
        self.targets.iter().find(|t| t.home)
    }

    pub fn machine_target(&self, name: &str) -> Option<&Target> {
        self.targets.iter().find(|t| !t.home && t.machine == name)
    }

    /// Where "install on…" may send this key: the home and each linked
    /// machine where it is not installed or on its way (a machine where it
    /// failed or was removed may get it again). Nothing once it is revoked
    /// everywhere: the home no longer keeps it.
    pub fn offer_install(&self, linked: &[String]) -> (bool, Vec<String>) {
        if self.revoked_at != 0 {
            return (false, Vec::new());
        }
        let open = |t: Option<&Target>| !t.is_some_and(|t| matches!(t.state, State::Installed | State::Pending));
        let home = open(self.home_target());
        let machines = linked.iter().filter(|name| open(self.machine_target(name))).cloned().collect();
        (home, machines)
    }

    /// What "revoke" may take it from: every target it was sent to that is
    /// not removed (Swarm refuses a machine the key was never sent to).
    pub fn revocable(&self) -> Vec<&Target> {
        if self.revoked_at != 0 {
            return Vec::new();
        }
        self.targets.iter().filter(|t| t.state != State::Removed).collect()
    }
}

/// The body of POST /v3/fleet/keys/requests.
pub fn request_body(provider: &str, label: &str, machines: &[String], include_home: bool) -> Value {
    let mut body = json!({"provider": provider, "machines": machines, "include_home": include_home});
    let label = label.trim();
    if !label.is_empty() {
        body["label"] = json!(label);
    }
    body
}

/// The body of an install or revoke: the machines named, and the home.
pub fn targets_body(machines: &[String], include_home: bool) -> Value {
    json!({"machines": machines, "include_home": include_home})
}

/// Providers that take an API key, from the home's GET /v1/providers, in
/// Swarm's order.
pub fn api_key_providers(providers: &Value) -> Vec<String> {
    list(providers, "providers")
        .iter()
        .filter(|p| list(p, "auth_methods").iter().any(|m| s(m, "credential_type") == "api"))
        .map(|p| s(p, "id").to_string())
        .filter(|id| !id.is_empty())
        .collect()
}

/// The home's answer to a key request: its one-time page.
#[derive(Clone, Debug, PartialEq)]
pub struct Requested {
    pub id: String,
    pub provider: String,
    pub url: String,
    pub expires_in_seconds: u64,
    /// Swarm's own words for the person.
    pub tell: String,
}

pub fn requested(v: &Value) -> Result<Requested, String> {
    let url = s(v, "url").to_string();
    if url.is_empty() {
        return Err("Swarm returned no key page link.".into());
    }
    Ok(Requested {
        id: s(v, "id").to_string(),
        provider: s(v, "provider").to_string(),
        url,
        expires_in_seconds: ms(v, "expires_in_seconds"),
        tell: s(v, "tell_the_person").to_string(),
    })
}

/// GET /v3/fleet/keys on the home.
pub fn read(home: &Client) -> Result<List, String> {
    home.get("/v3/fleet/keys").map(|v| list_of(&v)).map_err(|e| e.to_string())
}

/// The providers the person may pick for a new key.
pub fn read_providers(home: &Client) -> Result<Vec<String>, String> {
    home.get("/v1/providers").map(|v| api_key_providers(&v)).map_err(|e| e.to_string())
}

/// POST /v3/fleet/keys/requests: a one-time key page link.
pub fn request(home: &Client, provider: &str, label: &str, machines: &[String], include_home: bool) -> Result<Requested, String> {
    home.post("/v3/fleet/keys/requests", request_body(provider, label, machines, include_home)).map_err(|e| e.to_string()).and_then(|v| requested(&v))
}

/// POST /v3/fleet/keys/{id}/install; Swarm answers once every machine did.
pub fn install(home: &Client, id: &str, machines: &[String], include_home: bool) -> Result<Value, String> {
    home.post(&format!("/v3/fleet/keys/{id}/install"), targets_body(machines, include_home)).map_err(|e| e.to_string())
}

/// POST /v3/fleet/keys/{id}/revoke; Swarm answers once every machine did.
pub fn revoke(home: &Client, id: &str, machines: &[String], include_home: bool) -> Result<Value, String> {
    home.post(&format!("/v3/fleet/keys/{id}/revoke"), targets_body(machines, include_home)).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The shape handleFleetKeyList writes (pebblestore.FleetKeys).
    fn answer() -> Value {
        json!({"revision": 7, "home": "pc", "keys": [
            {"id": "fk_1", "provider": "openai", "label": "team", "fingerprint": "1a2b3c4d", "last4": "WXYZ",
             "created_at": 1792540800000u64, "created_by": "owner",
             "home": {"state": "installed", "verified": true, "installed_at": 1792540801000u64, "updated_at": 1792540801000u64},
             "machines": {
                "social": {"state": "failed", "error": "provider verification failed: 401", "updated_at": 1792540802000u64},
                "forge": {"state": "installed", "installed_at": 1792540803000u64, "updated_at": 1792540803000u64},
                "old": {"state": "removed", "removed_at": 1792540804000u64, "updated_at": 1792540804000u64}
             }},
            {"id": "fk_2", "provider": "anthropic", "fingerprint": "deadbeef", "last4": "abcd", "created_at": 1,
             "machines": {"forge": {"state": "pending", "updated_at": 2}}},
            {"id": "fk_3", "provider": "google", "fingerprint": "00ff00ff", "created_at": 1, "revoked_at": 9,
             "machines": {"forge": {"state": "removed", "removed_at": 9}}},
            {"provider": "no id"}
        ]})
    }

    #[test]
    fn list_maps_every_target_as_swarm_wrote_it() {
        let l = list_of(&answer());
        assert_eq!((l.revision, l.home.as_str()), (7, "pc"));
        assert_eq!(l.keys.iter().map(|k| k.id.as_str()).collect::<Vec<_>>(), vec!["fk_1", "fk_2", "fk_3"], "Swarm's order, records without an id left out");
        let k = &l.keys[0];
        assert_eq!((k.provider.as_str(), k.label.as_str(), k.fingerprint.as_str(), k.last4.as_str(), k.created_by.as_str()), ("openai", "team", "1a2b3c4d", "WXYZ", "owner"));
        assert_eq!(k.created_at, 1792540800000);
        // The home first, then machines by name.
        let names: Vec<(&str, bool)> = k.targets.iter().map(|t| (t.machine.as_str(), t.home)).collect();
        assert_eq!(names, vec![("pc", true), ("forge", false), ("old", false), ("social", false)]);
        let home = k.home_target().unwrap();
        assert_eq!((&home.state, home.verified, home.installed_at), (&State::Installed, true, 1792540801000));
        let social = k.machine_target("social").unwrap();
        assert_eq!((&social.state, social.error.as_str()), (&State::Failed, "provider verification failed: 401"));
        assert_eq!(k.machine_target("old").map(|t| (t.state.clone(), t.removed_at)), Some((State::Removed, 1792540804000)));
        // No home target when the key was never meant for the home.
        assert!(l.keys[1].home_target().is_none());
        assert_eq!(l.keys[1].machine_target("forge").map(|t| t.state.clone()), Some(State::Pending));
        assert_eq!(l.keys[2].revoked_at, 9);
        // An unknown state stays Swarm's word.
        let odd = key(&json!({"id": "x", "machines": {"m": {"state": "sideways"}}}), "");
        assert_eq!(odd.targets[0].state.label(), "sideways");
        assert_eq!(list_of(&json!({})), List::default());
    }

    #[test]
    fn install_is_offered_where_the_key_is_not_installed_or_pending() {
        let l = list_of(&answer());
        let linked: Vec<String> = ["forge", "social", "old", "new"].iter().map(|s| s.to_string()).collect();
        // fk_1: home installed; forge installed; social failed, old removed, new never: offered.
        assert_eq!(l.keys[0].offer_install(&linked), (false, vec!["social".into(), "old".into(), "new".into()]));
        // fk_2: never on the home; forge pending.
        assert_eq!(l.keys[1].offer_install(&linked), (true, vec!["social".into(), "old".into(), "new".into()]));
        // Revoked everywhere: the home no longer keeps it.
        assert_eq!(l.keys[2].offer_install(&linked), (false, vec![]));
    }

    #[test]
    fn revoke_offers_every_target_not_removed() {
        let l = list_of(&answer());
        let names = |k: &Key| k.revocable().iter().map(|t| (t.machine.clone(), t.home)).collect::<Vec<_>>();
        assert_eq!(names(&l.keys[0]), vec![("pc".into(), true), ("forge".into(), false), ("social".into(), false)]);
        assert_eq!(names(&l.keys[1]), vec![("forge".into(), false)]);
        assert!(l.keys[2].revocable().is_empty());
    }

    #[test]
    fn requests_carry_exactly_what_was_chosen() {
        let machines = vec!["build-box".to_string()];
        assert_eq!(
            request_body("openai", " team ", &machines, true),
            json!({"provider": "openai", "label": "team", "machines": ["build-box"], "include_home": true})
        );
        assert_eq!(request_body("openai", "  ", &[], true), json!({"provider": "openai", "machines": [], "include_home": true}), "no label when none was typed");
        assert_eq!(targets_body(&machines, false), json!({"machines": ["build-box"], "include_home": false}));
    }

    #[test]
    fn only_api_key_providers_are_offered() {
        let providers = json!({"providers": [
            {"id": "openai", "auth_methods": [{"credential_type": "api"}]},
            {"id": "codex", "auth_methods": [{"credential_type": "oauth"}]},
            {"id": "anthropic", "auth_methods": [{"credential_type": "oauth"}, {"credential_type": "api"}]},
            {"id": "claudecode"},
            {"auth_methods": [{"credential_type": "api"}]}
        ]});
        assert_eq!(api_key_providers(&providers), vec!["openai", "anthropic"]);
    }

    #[test]
    fn the_key_page_link_is_read_from_swarms_answer() {
        let r = requested(&json!({"id": "fk_9", "provider": "openai", "url": "http://127.0.0.1:7781/keys/abc", "expires_in_seconds": 600, "tell_the_person": "Open it."})).unwrap();
        assert_eq!((r.id.as_str(), r.url.as_str(), r.expires_in_seconds, r.tell.as_str()), ("fk_9", "http://127.0.0.1:7781/keys/abc", 600, "Open it."));
        assert!(requested(&json!({"id": "fk_9"})).is_err(), "no link, no page");
    }
}
