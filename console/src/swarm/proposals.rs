//! Proposals waiting for the person: workflows and workers an AI proposed,
//! on the machine the console is connected to or on a machine linked to it.
//! GET /v3/proposals?machine= lists them (the home reads a linked machine's
//! over the link); POST /v3/proposals/decide accepts or discards one at the
//! revision the person saw, there or on a linked machine. Only the person
//! decides: the console's owner session, never an AI.

use serde_json::Value;

use super::client::Client;

/// One proposal waiting for the person.
#[derive(Clone, Debug, PartialEq)]
pub struct Proposal {
    /// workflow or worker.
    pub kind: String,
    /// The linked machine it is on, or "" for the connected one.
    pub machine: String,
    pub id: String,
    pub name: String,
    pub revision: u64,
    pub created_at: u64,
    /// new (accepting adds it) or change (accepting replaces `replaces`).
    pub change: String,
    pub replaces: String,
}

fn text(v: &Value, key: &str) -> String {
    v.get(key).and_then(Value::as_str).unwrap_or("").to_string()
}

fn num(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

/// The proposals of a GET /v3/proposals answer.
pub fn parse(v: &Value) -> Vec<Proposal> {
    v.get("proposals")
        .and_then(Value::as_array)
        .map(|list| {
            list.iter()
                .map(|p| Proposal {
                    kind: text(p, "kind"),
                    machine: text(p, "machine"),
                    id: text(p, "id"),
                    name: text(p, "name"),
                    revision: num(p, "revision"),
                    created_at: num(p, "created_at"),
                    change: text(p, "change"),
                    replaces: text(p, "replaces"),
                })
                .filter(|p| !p.id.is_empty())
                .collect()
        })
        .unwrap_or_default()
}

/// A machine name in a query (names are letters, digits and dashes; anything
/// else is escaped).
fn query_value(value: &str) -> String {
    value.bytes().map(|b| if b.is_ascii_alphanumeric() || b == b'-' || b == b'_' { (b as char).to_string() } else { format!("%{b:02X}") }).collect()
}

/// The proposals on `machine` ("" for the connected one).
pub fn list(client: &Client, machine: &str) -> Result<Vec<Proposal>, String> {
    let path = if machine.is_empty() { "/v3/proposals".to_string() } else { format!("/v3/proposals?machine={}", query_value(machine)) };
    client.get(&path).map(|v| parse(&v)).map_err(|e| e.to_string())
}

/// The body of POST /v3/proposals/decide for `p`.
pub fn decide_body(p: &Proposal, accept: bool) -> Value {
    serde_json::json!({
        "machine": p.machine,
        "kind": p.kind,
        "id": p.id,
        "expected_revision": p.revision,
        "decision": if accept { "accept" } else { "discard" },
    })
}

/// Accepts or discards `p` at the revision the person saw.
pub fn decide(client: &Client, p: &Proposal, accept: bool) -> Result<Value, String> {
    client.post("/v3/proposals/decide", decide_body(p, accept)).map_err(|e| e.to_string())
}

/// The proposal in words: what accepting does.
pub fn describe(p: &Proposal) -> String {
    match p.change.as_str() {
        "change" => format!("a change to {} {} (revision {})", p.kind, p.name, p.revision),
        _ => format!("a new {} {} (revision {})", p.kind, p.name, p.revision),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn proposals_read_from_the_route() {
        let v = json!({"ok": true, "machine": "box", "proposals": [
            {"kind": "workflow", "machine": "box", "id": "jauto_1", "name": "review", "revision": 2, "created_at": 7, "waiting_on": "person", "change": "change", "replaces": "jauto_0"},
            {"kind": "worker", "machine": "box", "id": "", "name": "no id", "revision": 1}
        ]});
        let list = parse(&v);
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].machine, "box");
        assert_eq!(list[0].revision, 2);
        assert_eq!(describe(&list[0]), "a change to workflow review (revision 2)");
    }

    #[test]
    fn a_decision_names_the_machine_and_the_revision_seen() {
        let p = Proposal {
            kind: "worker".into(),
            machine: "box".into(),
            id: "w1".into(),
            name: "nightly".into(),
            revision: 3,
            created_at: 0,
            change: "new".into(),
            replaces: String::new(),
        };
        assert_eq!(decide_body(&p, true), json!({"machine": "box", "kind": "worker", "id": "w1", "expected_revision": 3, "decision": "accept"}));
        assert_eq!(decide_body(&p, false)["decision"], "discard");
        assert_eq!(query_value("box-1"), "box-1");
        assert_eq!(query_value("a b&c"), "a%20b%26c");
    }
}
