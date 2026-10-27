//! Your fleet: the Swarms your home Swarm controls. Home pairs with each one
//! itself (one way: home reaches them, they never reach home), keeps the
//! list, and follows their tasks. This app only shows that list, read from
//! home at GET /v3/fleet; it never talks to the other machines. A background
//! thread rereads it every 20 seconds. A Swarm without the fleet routes yet
//! has no machines.

use std::sync::mpsc::{self, Receiver};
use std::time::Duration;

use serde_json::Value;

use super::client::{Client, Refusal};
use crate::data::{Health, Machine};

const EVERY: Duration = Duration::from_secs(20);

/// A machine home controls, as home lists it.
#[derive(Clone, Debug, PartialEq)]
pub struct Found {
    pub name: String,
    /// Its address on the network home reaches it on.
    pub address: String,
    /// False when home's last call to it failed.
    pub online: bool,
    pub open_tasks: u64,
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
                        online: m.get("last_error").and_then(Value::as_str).is_none_or(str::is_empty),
                        open_tasks: m.get("open_tasks").and_then(Value::as_u64).unwrap_or(0),
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

/// A machine as the console shows it.
pub fn machine(found: &Found) -> Machine {
    Machine {
        name: found.name.clone().into(),
        kind: "paired".into(),
        health: if !found.online {
            Health::Offline
        } else if found.open_tasks > 0 {
            Health::Busy
        } else {
            Health::Online
        },
        running: found.open_tasks as u32,
        slots: 0,
        cost_today: 0.,
        build: "".into(),
        behind: 0,
        access: "paired".into(),
        sandbox: "".into(),
        uptime: "".into(),
        cpu: 0.,
        mem: 0.,
        gateway: found.address.clone().into(),
    }
}

/// Watches home's fleet from a background thread.
pub struct Fleet {
    rx: Receiver<Vec<Machine>>,
}

impl Fleet {
    pub fn start(home: Client) -> Fleet {
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            loop {
                if let Ok(found) = read(&home) {
                    if tx.send(found.iter().map(machine).collect()).is_err() {
                        return; // the console is gone
                    }
                }
                std::thread::sleep(EVERY);
            }
        });
        Fleet { rx }
    }

    /// The newest reading since the last call, if any.
    pub fn latest(&self) -> Option<Vec<Machine>> {
        self.rx.try_iter().last()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn home_list_to_machines() {
        let list = json!({"machines": [
            {"name": "forge", "address": "10.0.0.5:7792", "open_tasks": 2},
            {"name": "social", "address": "10.0.0.6:7792", "last_error": "connection refused"},
            {"address": "no name"}
        ]});
        let found = from_list(&list);
        assert_eq!(found.iter().map(|f| f.name.as_str()).collect::<Vec<_>>(), vec!["forge", "social"]);
        assert!(found[0].online && !found[1].online);
        assert_eq!(machine(&found[0]).health, Health::Busy);
        assert_eq!(machine(&found[1]).health, Health::Offline);
    }
}
