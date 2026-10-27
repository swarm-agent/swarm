//! What you chose for your fleet, kept in ~/.config/swarm-console/fleet.json:
//! which machine is home (where your conversations live) and what the
//! Orchestrator may do on each machine. Written by onboarding and Settings.

use std::collections::BTreeMap;
use std::path::PathBuf;

use serde::{Deserialize, Serialize};

/// The name this PC goes by in the fleet.
pub const THIS_PC: &str = "this-pc";

/// What the Orchestrator may do on a machine.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Level {
    /// It uses the machine freely.
    Full,
    /// Each action waits for you to run or deny it.
    Ask,
    /// No access; it may ask for access when it needs it.
    Off,
}

impl Level {
    pub fn label(self) -> &'static str {
        match self {
            Level::Full => "full",
            Level::Ask => "ask",
            Level::Off => "off",
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct FleetConfig {
    /// The machine your conversations live on; THIS_PC or a fleet machine's name.
    pub home: String,
    /// Access per machine name (THIS_PC is your own PC).
    pub access: BTreeMap<String, Level>,
    /// Folders on this PC the Orchestrator may work in (absolute paths).
    #[serde(default)]
    pub folders: Vec<String>,
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

    /// A machine's level: as chosen, else the default (your PC off, others ask).
    pub fn level(&self, machine: &str) -> Level {
        self.access.get(machine).copied().unwrap_or(if machine == THIS_PC { Level::Off } else { Level::Ask })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn defaults_and_round_trip() {
        let mut c = FleetConfig { home: THIS_PC.into(), ..Default::default() };
        assert_eq!((c.level(THIS_PC), c.level("forge")), (Level::Off, Level::Ask));
        c.access.insert("forge".into(), Level::Full);
        let text = serde_json::to_string(&c).unwrap();
        assert!(text.contains(r#""forge":"full""#));
        assert_eq!(serde_json::from_str::<FleetConfig>(&text).unwrap(), c);
    }
}

impl FleetConfig {
    /// Whether `path` is one of the allowed folders or inside one ("~/" is
    /// your home folder). With no folders allowed, nothing is.
    pub fn allows(&self, path: &str) -> bool {
        // Real paths: a link inside an allowed folder may point out of it.
        let path = real(&expand(path));
        self.folders.iter().any(|f| {
            let folder = real(&expand(f));
            path == folder || path.starts_with(&format!("{}/", folder.trim_end_matches('/')))
        })
    }
}

/// "~/x" as an absolute path, with "." and ".." resolved by text (a path that
/// climbs out of a folder is outside it).
pub fn expand(path: &str) -> String {
    let home = std::env::var("HOME").unwrap_or_default();
    let full = match path.strip_prefix('~') {
        Some(rest) if rest.is_empty() || rest.starts_with('/') => format!("{home}{rest}"),
        _ => path.to_string(),
    };
    let mut parts: Vec<&str> = Vec::new();
    for part in full.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                parts.pop();
            }
            p => parts.push(p),
        }
    }
    format!("/{}", parts.join("/"))
}

#[cfg(test)]
mod folder_tests {
    use super::*;

    #[test]
    fn inside_allowed_folders_only() {
        let c = FleetConfig { folders: vec!["/srv/work/code".into()], ..Default::default() };
        assert!(c.allows("/srv/work/code"));
        assert!(c.allows("/srv/work/code/app/src/main.rs"));
        assert!(!c.allows("/srv/work/codex/x"));
        assert!(!c.allows("/srv/work/code/../.ssh/id_ed25519"));
        assert!(!FleetConfig::default().allows("/tmp/x"));
    }
}

/// A path with its links resolved: the path itself when it exists, else its
/// nearest existing parent's real path with the rest appended.
fn real(path: &str) -> String {
    let mut existing = std::path::PathBuf::from(path);
    let mut rest: Vec<std::ffi::OsString> = Vec::new();
    loop {
        if let Ok(resolved) = std::fs::canonicalize(&existing) {
            let mut out = resolved;
            for part in rest.iter().rev() {
                out.push(part);
            }
            return out.to_string_lossy().into_owned();
        }
        match (existing.file_name().map(|n| n.to_os_string()), existing.parent().map(|p| p.to_path_buf())) {
            (Some(name), Some(parent)) => {
                rest.push(name);
                existing = parent;
            }
            _ => return path.to_string(),
        }
    }
}

#[cfg(test)]
mod link_tests {
    use super::*;

    #[test]
    fn a_link_out_of_a_folder_is_outside_it() {
        let base = std::env::temp_dir().join(format!("swarm-console-folders-{}", std::process::id()));
        let inside = base.join("code");
        let secret = base.join("secret");
        std::fs::create_dir_all(&inside).unwrap();
        std::fs::create_dir_all(&secret).unwrap();
        std::os::unix::fs::symlink(&secret, inside.join("escape")).unwrap();
        let c = FleetConfig { folders: vec![inside.display().to_string()], ..Default::default() };
        assert!(c.allows(&inside.join("new-file.txt").display().to_string()));
        assert!(!c.allows(&inside.join("escape/key").display().to_string()));
        std::fs::remove_dir_all(&base).unwrap();
    }
}
