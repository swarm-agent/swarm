//! A project folder must be a Git repository with a first commit before any
//! agent may work in it. Swarm's own routes decide and do that; this module
//! only calls them the way Swarm's web UI does.
//!
//! - `GET /v1/workspace/repository?path=` says the folder's state.
//! - `POST /v1/workspace/repository/setup` makes a repository with an empty
//!   starting commit in an empty folder.
//! - `GET /v1/workspace/repository/review?path=` lists a folder's files, and
//!   `POST /v1/workspace/repository/baseline` commits the ones the person
//!   picked as the first commit.

use serde_json::{json, Value};

use super::client::Client;

#[derive(Clone, Debug, PartialEq)]
pub struct State {
    pub state: String,
    pub path: String,
    pub head: String,
    pub can_setup: bool,
    pub message: String,
}

#[derive(Clone, Debug, PartialEq)]
pub struct File {
    pub path: String,
    pub size: u64,
    pub selectable: bool,
}

/// What Swarm shows the person before the first commit of a folder with files.
#[derive(Clone, Debug, PartialEq)]
pub struct Review {
    pub path: String,
    pub digest: String,
    pub files: Vec<File>,
    pub warning: String,
}

/// The outcome of preparing one folder.
#[derive(Clone, Debug, PartialEq)]
pub enum Prepared {
    /// The folder is a committed repository (`note` says what was done, if anything).
    Ready { note: Option<String> },
    /// The folder has files; the person picks the first commit.
    NeedsReview(Review),
    /// Swarm's user cannot see the folder (or it is not there): the app
    /// must let Swarm read it first.
    Invisible(String),
}

fn s(v: &Value, key: &str) -> String {
    v.get(key).and_then(Value::as_str).unwrap_or("").to_string()
}

fn state_of(v: &Value) -> State {
    State {
        state: s(v, "state"),
        path: s(v, "path"),
        head: s(v, "head_commit"),
        can_setup: v.get("can_setup").and_then(Value::as_bool).unwrap_or(false),
        message: s(v, "message"),
    }
}

fn query(path: &str) -> String {
    let mut out = String::new();
    for b in path.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' | b'/' => out.push(b as char),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

/// Swarm's answer, whether it came as a refusal (with the repository state
/// inside) or as a reply.
fn repository(result: Result<Value, super::client::Refusal>) -> Result<Value, String> {
    match result {
        Ok(v) => Ok(v.get("repository").cloned().unwrap_or(Value::Null)),
        // A refusal that carries the repository state is an answer too: a
        // folder Swarm cannot see says so this way.
        Err(super::client::Refusal::Status(_, _, answer)) if answer.get("repository").is_some_and(|r| r.get("state").is_some()) => Ok(answer["repository"].clone()),
        Err(e) => Err(e.to_string()),
    }
}

pub fn inspect(client: &Client, path: &str) -> Result<State, String> {
    repository(client.get(&format!("/v1/workspace/repository?path={}", query(path)))).map(|v| state_of(&v))
}

pub fn setup(client: &Client, path: &str) -> Result<State, String> {
    repository(client.post("/v1/workspace/repository/setup", json!({"path": path, "expected_resolved_path": path}))).map(|v| state_of(&v))
}

pub fn review(client: &Client, path: &str) -> Result<Review, String> {
    let v = client.get(&format!("/v1/workspace/repository/review?path={}", query(path))).map_err(|e| e.to_string())?;
    let review = v.get("review").cloned().unwrap_or(Value::Null);
    let files = review
        .get("files")
        .and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .map(|f| File { path: s(f, "path"), size: f.get("size").and_then(Value::as_u64).unwrap_or(0), selectable: f.get("selectable").and_then(Value::as_bool).unwrap_or(false) })
                .collect()
        })
        .unwrap_or_default();
    let digest = s(&review, "digest");
    if digest.is_empty() {
        return Err("Swarm did not return a complete file review".into());
    }
    Ok(Review { path: path.to_string(), digest, files, warning: s(&review, "warning") })
}

/// Commits `selected` as the folder's first commit. `omitted` confirms that
/// files left out stay out of the first commit.
pub fn baseline(client: &Client, review: &Review, selected: &[String]) -> Result<State, String> {
    let omitted = review.files.iter().any(|f| !selected.contains(&f.path));
    let state = repository(client.post(
        "/v1/workspace/repository/baseline",
        json!({
            "path": review.path,
            "expected_resolved_path": review.path,
            "review_digest": review.digest,
            "selected_paths": selected,
            "confirm_baseline": true,
            "confirm_omissions": omitted,
        }),
    ))
    .map(|v| state_of(&v))?;
    if state.state != "ready" || state.head.is_empty() {
        return Err(if state.message.is_empty() { "Swarm did not make the first commit".into() } else { state.message });
    }
    Ok(state)
}

/// Makes one folder ready for agents, or says what the person must decide.
pub fn prepare(client: &Client, path: &str) -> Result<Prepared, String> {
    let state = inspect(client, path)?;
    match state.state.as_str() {
        "ready" => Ok(Prepared::Ready { note: None }),
        "not_repository" if state.can_setup => {
            let after = match setup(client, path) {
                Ok(after) => after,
                // A folder registered before it had Git (an earlier build of
                // this app did that) cannot go through setup; Swarm's review
                // commits an empty first commit in it instead.
                Err(e) if e.contains("already saved") => baseline(client, &review(client, path)?, &[])?,
                Err(e) => return Err(e),
            };
            if after.state != "ready" {
                return Err(if after.message.is_empty() { format!("Swarm could not set up Git in {path}") } else { after.message });
            }
            Ok(Prepared::Ready { note: Some(format!("Set up Git in {path} with an empty first commit.")) })
        }
        "needs_assisted_setup" | "needs_initial_commit" => {
            let review = review(client, path)?;
            if review.files.is_empty() {
                // A repository with nothing to commit (an empty folder the
                // person's Git was set up in): nothing to pick, so Swarm's
                // empty first commit goes in now.
                baseline(client, &review, &[])?;
                return Ok(Prepared::Ready { note: Some(format!("Made the first commit in {path}.")) });
            }
            Ok(Prepared::NeedsReview(review))
        }
        "directory_missing" | "access_denied" | "trust_required" => Ok(Prepared::Invisible(state.message)),
        _ => Err(if state.message.is_empty() { format!("{path}: {}", state.state) } else { format!("{path}: {}", state.message) }),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_are_query_escaped() {
        assert_eq!(query("/srv/work/my project"), "/srv/work/my%20project");
        assert_eq!(query("/srv/work/a&b"), "/srv/work/a%26b");
    }

    #[test]
    fn states_read_swarms_fields() {
        let v = json!({"state": "not_repository", "path": "/x", "can_setup": true, "message": "m"});
        let st = state_of(&v);
        assert_eq!(st.state, "not_repository");
        assert!(st.can_setup);
        assert_eq!(st.head, "");
    }
}
