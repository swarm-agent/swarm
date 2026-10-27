//! The apps connected to this Swarm as its owner: the client sessions
//! `swarmctl client connect` hands out (GET /v1/auth/clients), and
//! disconnecting one (DELETE /v1/auth/clients/{id}). Swarm stops accepting
//! a disconnected app's token at once. A session is made only on the machine
//! itself, so nothing here can add one.

use serde_json::Value;

use super::client::Client;
use super::map::{list, s};

/// One client session, as Swarm records it (Unix ms; 0 when Swarm has none).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct App {
    pub session_id: String,
    pub name: String,
    /// The computer it was connected for ("" when none was named).
    pub device: String,
    pub created_at: u64,
    pub expires_at: u64,
    pub last_seen_at: u64,
    pub revoked_at: u64,
}

/// What an app's session is right now.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Standing {
    Connected,
    Expired,
    Disconnected,
}

impl App {
    pub fn standing(&self, now: u64) -> Standing {
        if self.revoked_at != 0 {
            Standing::Disconnected
        } else if self.expires_at != 0 && self.expires_at <= now {
            Standing::Expired
        } else {
            Standing::Connected
        }
    }
}

fn ms(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

fn app(v: &Value) -> App {
    App {
        session_id: s(v, "session_id").to_string(),
        name: s(v, "name").trim().to_string(),
        device: s(v, "device").trim().to_string(),
        created_at: ms(v, "created_at"),
        expires_at: ms(v, "expires_at"),
        last_seen_at: ms(v, "last_seen_at"),
        revoked_at: ms(v, "revoked_at"),
    }
}

/// Swarm's answer, connected apps first (most recently seen first), then
/// expired and disconnected ones, most recent first.
pub fn apps(v: &Value, now: u64) -> Vec<App> {
    let mut out: Vec<App> = list(v, "clients").iter().map(app).filter(|a| !a.session_id.is_empty()).collect();
    out.sort_by_key(|a| {
        let rank = match a.standing(now) {
            Standing::Connected => 0,
            Standing::Expired => 1,
            Standing::Disconnected => 2,
        };
        (rank, std::cmp::Reverse(a.last_seen_at.max(a.revoked_at).max(a.created_at)))
    });
    out
}

pub fn read(home: &Client) -> Result<Vec<App>, String> {
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis() as u64).unwrap_or(0);
    home.get("/v1/auth/clients").map(|v| apps(&v, now)).map_err(|e| e.to_string())
}

/// Disconnects one app; Swarm answers with its record.
pub fn revoke(home: &Client, session_id: &str) -> Result<(), String> {
    home.delete(&format!("/v1/auth/clients/{session_id}")).map(|_| ()).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn orders_connected_first_and_words_standing() {
        let now = 10_000;
        let v = json!({"clients": [
            {"session_id": "a", "name": "old", "created_at": 1, "expires_at": 5_000, "last_seen_at": 2},
            {"session_id": "b", "name": "swarm-console", "device": "pc", "created_at": 3, "expires_at": 99_000, "last_seen_at": 9_000},
            {"session_id": "c", "name": "gone", "created_at": 4, "expires_at": 99_000, "revoked_at": 8_000},
            {"session_id": "d", "name": "laptop app", "created_at": 5, "expires_at": 0, "last_seen_at": 9_500},
            {"name": "no id"}
        ]});
        let got = apps(&v, now);
        assert_eq!(got.iter().map(|a| a.session_id.as_str()).collect::<Vec<_>>(), ["d", "b", "a", "c"]);
        assert_eq!(got[0].standing(now), Standing::Connected);
        assert_eq!(got[1].device, "pc");
        assert_eq!(got[2].standing(now), Standing::Expired);
        assert_eq!(got[3].standing(now), Standing::Disconnected);
    }

    #[test]
    fn empty_answer_is_no_apps() {
        assert!(apps(&json!({}), 1).is_empty());
        assert!(apps(&json!({"clients": null}), 1).is_empty());
    }
}
