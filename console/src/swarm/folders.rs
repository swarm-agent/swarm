//! A project's folders, changed from the Projects page: listing a directory
//! to pick folders from, and adding or removing several at once.
//!
//! On the machine this app runs on, a directory is read from disk as the
//! person (their home folder, everything they can see). In a linked
//! machine's window it is Swarm's own browser (GET /v1/workspace/browse),
//! which lists what Swarm on that machine can see. Adding readies each folder
//! the way the first run does (repository::prepare), registers the ones Swarm
//! does not know in place (POST /v1/workspace/add, nothing in them changes)
//! and saves the project's folder list (PATCH /v3/projects/{id}).

use std::path::{Path, PathBuf};

use serde_json::{Value, json};

use super::client::Client;
use super::map::{list, s};
use super::repository::{self, Prepared};

/// One folder in a listing.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Entry {
    pub name: String,
    pub path: String,
    pub git: bool,
    /// "AGENTS.md" or "CLAUDE.md" when the folder has one at its top.
    pub guide: Option<&'static str>,
}

/// A directory's folders and where it sits.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Listing {
    pub path: String,
    pub parent: Option<String>,
    pub entries: Vec<Entry>,
}

/// Folders with an AGENTS.md or CLAUDE.md first, then Git repositories,
/// then the rest; by name within each.
pub fn rank(entries: &mut [Entry]) {
    entries.sort_by(|a, b| {
        let key = |e: &Entry| (u8::from(e.guide.is_none()), u8::from(!e.git), e.name.to_lowercase());
        key(a).cmp(&key(b))
    });
}

fn guide_in(dir: &Path) -> Option<&'static str> {
    ["AGENTS.md", "CLAUDE.md"].into_iter().find(|f| dir.join(f).is_file())
}

/// Reads a directory on this computer: its folders, hidden ones left out.
pub fn read_local(dir: &Path) -> Result<Listing, String> {
    let rd = std::fs::read_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
    let mut entries: Vec<Entry> = rd
        .filter_map(Result::ok)
        .filter(|e| e.file_type().map(|t| t.is_dir()).unwrap_or(false))
        .filter_map(|e| {
            let name = e.file_name().to_string_lossy().to_string();
            if name.starts_with('.') {
                return None;
            }
            let path = e.path();
            Some(Entry { git: path.join(".git").exists(), guide: guide_in(&path), name, path: path.display().to_string() })
        })
        .collect();
    rank(&mut entries);
    Ok(Listing { path: dir.display().to_string(), parent: dir.parent().map(|p| p.display().to_string()), entries })
}

/// The person's home folder on this computer.
pub fn local_home() -> PathBuf {
    std::env::var_os("HOME").map(PathBuf::from).unwrap_or_else(|| PathBuf::from("/"))
}

/// Swarm's browser answer as a listing ("" asks for its home folder).
pub fn browse_answer(v: &Value) -> Listing {
    let b = v.get("browser").cloned().unwrap_or(Value::Null);
    let path = [s(&b, "resolved_path"), s(&b, "requested_path"), s(&b, "home_path")].into_iter().find(|p| !p.is_empty()).unwrap_or("").to_string();
    let mut entries: Vec<Entry> = list(&b, "entries")
        .iter()
        .filter(|e| e.get("is_directory").and_then(Value::as_bool).unwrap_or(true))
        .filter(|e| !s(e, "name").starts_with('.') && !s(e, "path").is_empty())
        .map(|e| Entry { name: s(e, "name").to_string(), path: s(e, "path").to_string(), git: e.get("is_git_repo").and_then(Value::as_bool).unwrap_or(false), guide: None })
        .collect();
    rank(&mut entries);
    let parent = Some(s(&b, "parent_path").to_string()).filter(|p| !p.is_empty());
    Listing { path, parent, entries }
}

/// Reads a directory through Swarm's browser; "" is Swarm's home folder.
pub fn browse(client: &Client, path: &str) -> Result<Listing, String> {
    let q = if path.is_empty() { String::new() } else { format!("?path={}", super::realtime::urlencode(path)) };
    let v = client.get(&format!("/v1/workspace/browse{q}")).map_err(|e| e.to_string())?;
    let listing = browse_answer(&v);
    if path.is_empty() && listing.entries.is_empty() {
        if let Some(home) = v.get("browser").map(|b| s(b, "home_path")).filter(|h| !h.is_empty()) {
            let again = client.get(&format!("/v1/workspace/browse?path={}", super::realtime::urlencode(home))).map_err(|e| e.to_string())?;
            return Ok(browse_answer(&again));
        }
    }
    Ok(listing)
}

/// What adding folders came to.
#[derive(Debug, Default)]
pub struct Added {
    /// Added to the project.
    pub added: Vec<String>,
    /// Swarm cannot read these yet; the person makes them visible.
    pub invisible: Vec<String>,
    /// Folders with files and no first commit: not added; the person picks
    /// the first commit through the new-project setup.
    pub need_first_commit: Vec<String>,
    /// What Swarm did on the way (Git set up in an empty folder).
    pub notes: Vec<String>,
}

/// The project's folder list with `adding` appended: the first folder of a
/// project with none becomes its primary code folder.
pub fn with_added(current: &[(String, String)], adding: &[String]) -> Vec<Value> {
    let mut out: Vec<Value> = current.iter().map(|(p, r)| json!({"path": p, "role": r})).collect();
    for p in adding {
        if current.iter().any(|(c, _)| c == p) {
            continue;
        }
        let role = if out.is_empty() { "primary_code" } else { "auxiliary" };
        out.push(json!({"path": p, "role": role}));
    }
    out
}

/// The project's folder list without `removing`; when its primary code
/// folder goes, the first one left takes that role.
pub fn without(current: &[(String, String)], removing: &[String]) -> Vec<Value> {
    let kept: Vec<&(String, String)> = current.iter().filter(|(p, _)| !removing.contains(p)).collect();
    let has_primary = kept.iter().any(|(_, r)| r == "primary_code");
    kept.iter()
        .enumerate()
        .map(|(i, (p, r))| json!({"path": p, "role": if !has_primary && i == 0 { "primary_code" } else { r.as_str() }}))
        .collect()
}

/// Readies, registers and adds `paths` to the project.
pub fn add(client: &Client, project_id: &str, current: &[(String, String)], paths: &[String]) -> Result<Added, String> {
    let mut out = Added::default();
    let mut ready = Vec::new();
    for path in paths {
        match repository::prepare(client, path) {
            Ok(Prepared::Ready { note }) => {
                out.notes.extend(note);
                ready.push(path.clone());
            }
            Ok(Prepared::NeedsReview(_)) => out.need_first_commit.push(path.clone()),
            Ok(Prepared::Invisible(_)) => out.invisible.push(path.clone()),
            Err(e) if e.contains("workspace_not_visible") || e.contains("not visible") || e.contains("cannot see") => out.invisible.push(path.clone()),
            Err(e) => return Err(e),
        }
    }
    if ready.is_empty() {
        return Ok(out);
    }
    let known = client.get("/v1/workspace/list?limit=500").map_err(|e| e.to_string())?;
    let registered: Vec<String> = list(&known, "workspaces").iter().map(|w| s(w, "path").to_string()).collect();
    for path in ready.iter().filter(|p| !registered.contains(p)) {
        client.post("/v1/workspace/add", json!({"path": path, "context_only": true, "make_current": false})).map_err(|e| e.to_string())?;
    }
    client.patch(&format!("/v3/projects/{project_id}"), json!({"workspaces": with_added(current, &ready)})).map_err(|e| e.to_string())?;
    out.added = ready;
    Ok(out)
}

/// The command that lets Swarm read folders it cannot see yet.
pub fn visible_command(paths: &[String]) -> String {
    let quoted: Vec<String> = paths.iter().map(|p| if p.contains(' ') { format!("'{p}'") } else { p.clone() }).collect();
    format!("sudo swarmctl workspace add {}", quoted.join(" "))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn e(name: &str, git: bool, guide: Option<&'static str>) -> Entry {
        Entry { name: name.into(), path: format!("/h/{name}"), git, guide }
    }

    #[test]
    fn guided_repositories_come_first() {
        let mut v = vec![e("zeta", false, None), e("beta", true, None), e("alpha", true, Some("CLAUDE.md")), e("Gamma", true, Some("AGENTS.md")), e("delta", false, None)];
        rank(&mut v);
        assert_eq!(v.iter().map(|e| e.name.as_str()).collect::<Vec<_>>(), ["alpha", "Gamma", "beta", "delta", "zeta"]);
    }

    #[test]
    fn reads_a_local_directory() {
        let root = std::env::temp_dir().join(format!("swarm-console-folders-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&root);
        for d in ["plain", "repo/.git", "guided/.git", ".hidden"] {
            std::fs::create_dir_all(root.join(d)).unwrap();
        }
        std::fs::write(root.join("guided/AGENTS.md"), "x").unwrap();
        std::fs::write(root.join("file.txt"), "x").unwrap();
        let l = read_local(&root).unwrap();
        assert_eq!(l.entries.iter().map(|e| (e.name.as_str(), e.git, e.guide)).collect::<Vec<_>>(), [("guided", true, Some("AGENTS.md")), ("repo", true, None), ("plain", false, None)]);
        assert_eq!(l.parent.as_deref(), root.parent().map(|p| p.display().to_string()).as_deref());
        std::fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn swarm_browser_answer() {
        let v = json!({"ok": true, "browser": {"resolved_path": "/srv/work", "parent_path": "/srv", "home_path": "/srv/work", "entries": [
            {"name": "swarm-web", "path": "/srv/work/swarm-web", "is_directory": true, "is_git_repo": true},
            {"name": "notes", "path": "/srv/work/notes", "is_directory": true, "is_git_repo": false},
            {"name": "a.txt", "path": "/srv/work/a.txt", "is_directory": false},
            {"name": ".cache", "path": "/srv/work/.cache", "is_directory": true}
        ]}});
        let l = browse_answer(&v);
        assert_eq!(l.path, "/srv/work");
        assert_eq!(l.parent.as_deref(), Some("/srv"));
        assert_eq!(l.entries.iter().map(|e| e.name.as_str()).collect::<Vec<_>>(), ["swarm-web", "notes"]);
    }

    #[test]
    fn folder_lists_for_adding_and_removing() {
        let cur = vec![("/a".to_string(), "primary_code".to_string()), ("/b".to_string(), "auxiliary".to_string())];
        assert_eq!(with_added(&cur, &["/b".into(), "/c".into()]), vec![json!({"path": "/a", "role": "primary_code"}), json!({"path": "/b", "role": "auxiliary"}), json!({"path": "/c", "role": "auxiliary"})]);
        assert_eq!(with_added(&[], &["/x".into(), "/y".into()]), vec![json!({"path": "/x", "role": "primary_code"}), json!({"path": "/y", "role": "auxiliary"})]);
        assert_eq!(without(&cur, &["/b".into()]), vec![json!({"path": "/a", "role": "primary_code"})]);
        // The primary goes: the first one left takes its role.
        assert_eq!(without(&cur, &["/a".into()]), vec![json!({"path": "/b", "role": "primary_code"})]);
        assert_eq!(visible_command(&["/srv/work/x".into(), "/srv/work/my notes".into()]), "sudo swarmctl workspace add /srv/work/x '/srv/work/my notes'");
    }
}
