//! What you chose for your fleet, kept in ~/.config/swarm-console/fleet.json:
//! which machine is home (where your conversations live). Written by
//! onboarding; its presence says onboarding chose a home. What agents may use
//! is the account's rules in Swarm (the Permissions page), not this file.

use std::path::PathBuf;

use serde::{Deserialize, Serialize};

/// The name this PC goes by in the fleet.
pub const THIS_PC: &str = "this-pc";

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct FleetConfig {
    /// The machine your conversations live on; THIS_PC or a fleet machine's name.
    pub home: String,
}

impl FleetConfig {
    fn path() -> PathBuf {
        let home = std::env::var_os("HOME").map(PathBuf::from).unwrap_or_default();
        home.join(".config/swarm-console/fleet.json")
    }

    /// The saved choices, or None before onboarding made them.
    pub fn load() -> Option<FleetConfig> {
        let text = std::fs::read_to_string(Self::path()).ok()?;
        serde_json::from_str(&text).ok()
    }

    pub fn save(&self) -> Result<(), String> {
        let path = Self::path();
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        }
        let text = serde_json::to_string_pretty(self).map_err(|e| e.to_string())?;
        std::fs::write(&path, text).map_err(|e| e.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trip_and_older_files() {
        let c = FleetConfig { home: THIS_PC.into() };
        let text = serde_json::to_string(&c).unwrap();
        assert_eq!(serde_json::from_str::<FleetConfig>(&text).unwrap(), c);
        // A file written before the account's rules still loads: its home.
        let old = r#"{"home": "this-pc", "access": {"forge": "full"}, "folders": ["/srv/code"]}"#;
        assert_eq!(serde_json::from_str::<FleetConfig>(old).unwrap(), c);
    }
}
