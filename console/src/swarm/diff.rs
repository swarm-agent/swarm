//! What a task changed, for the person to look at before merging it:
//! GET /v3/projects/{id}/tasks/{task}/diff, and with ?path= one file in full.
//! Swarm bounds the answer and says where it cut (`truncated`); the text is
//! the repository's own, shown as it is, never acted on.

use serde_json::Value;

use super::client::Client;
use super::map::{list, s};

/// Swarm's answer for one task: its base and head commits and its files.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct TaskDiff {
    pub base: String,
    pub head: String,
    pub files: Vec<DiffFile>,
    /// Swarm cut some file's text (its per-file or total bound).
    pub truncated: bool,
    /// More files changed than Swarm lists.
    pub files_truncated: bool,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct DiffFile {
    pub path: String,
    /// The path before a rename ("" otherwise).
    pub old_path: String,
    /// added, modified, deleted, renamed, … as Swarm says it.
    pub status: String,
    pub additions: u64,
    pub deletions: u64,
    /// Binary: Swarm sends no text for it.
    pub binary: bool,
    /// The file's hunks, from its first "@@" line.
    pub diff: String,
    pub truncated: bool,
}

impl DiffFile {
    /// One letter for the file list: A, M, D, R, or the status's first.
    pub fn letter(&self) -> char {
        match self.status.as_str() {
            "added" => 'A',
            "modified" => 'M',
            "deleted" => 'D',
            "renamed" => 'R',
            other => other.chars().next().map(|c| c.to_ascii_uppercase()).unwrap_or('?'),
        }
    }
}

fn count(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_u64).unwrap_or(0)
}

fn flag(v: &Value, key: &str) -> bool {
    v.get(key).and_then(Value::as_bool).unwrap_or(false)
}

pub fn parse(v: &Value) -> TaskDiff {
    TaskDiff {
        base: s(v, "base").to_string(),
        head: s(v, "head").to_string(),
        files: list(v, "files")
            .iter()
            .map(|f| DiffFile {
                path: s(f, "path").to_string(),
                old_path: s(f, "old_path").to_string(),
                status: s(f, "status").to_string(),
                additions: count(f, "additions"),
                deletions: count(f, "deletions"),
                binary: flag(f, "binary"),
                diff: s(f, "diff").to_string(),
                truncated: flag(f, "truncated"),
            })
            .filter(|f| !f.path.is_empty())
            .collect(),
        truncated: flag(v, "truncated"),
        files_truncated: flag(v, "files_truncated"),
    }
}

/// How one diff line is colored.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LineKind {
    Added,
    Removed,
    Hunk,
    Context,
    /// "\ No newline at end of file" and other lines Git adds.
    Note,
}

/// A unified diff's lines, each with how it is colored; tabs become four
/// spaces so columns line up in the console's monospace.
pub fn lines(diff: &str) -> Vec<(LineKind, String)> {
    diff.lines()
        .map(|line| {
            let kind = match line.as_bytes().first() {
                Some(b'+') => LineKind::Added,
                Some(b'-') => LineKind::Removed,
                Some(b'@') if line.starts_with("@@") => LineKind::Hunk,
                Some(b'\\') => LineKind::Note,
                _ => LineKind::Context,
            };
            (kind, line.replace('\t', "    "))
        })
        .collect()
}

/// The route for a task's changes, or for one of its files.
pub fn route(project_id: &str, task_id: &str, file: Option<&str>) -> String {
    let mut path = format!("/v3/projects/{}/tasks/{}/diff", super::realtime::urlencode(project_id), super::realtime::urlencode(task_id));
    if let Some(file) = file {
        path.push_str("?path=");
        path.push_str(&super::realtime::urlencode(file));
    }
    path
}

/// Reads a task's changes (all files, or one in full) from the Swarm the
/// client talks to: this machine, or a linked one through its link.
pub fn read(client: &Client, project_id: &str, task_id: &str, file: Option<&str>) -> Result<TaskDiff, String> {
    client.get(&route(project_id, task_id, file)).map(|v| parse(&v)).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn parses_swarms_answer() {
        let v = json!({
            "project_id": "p", "task_id": "t", "base": "aaa", "head": "bbb", "truncated": true, "files_truncated": false,
            "total_bytes_limit": 409600, "file_bytes_limit": 65536,
            "files": [
                {"path": "src/a.rs", "status": "modified", "additions": 2, "deletions": 1, "diff": "@@ -1 +1,2 @@\n-a\n+b\n+c\n", "truncated": false},
                {"path": "new.txt", "old_path": "old.txt", "status": "renamed", "additions": 0, "deletions": 0, "diff": "", "truncated": false},
                {"path": "logo.png", "status": "added", "additions": 0, "deletions": 0, "binary": true, "diff": "", "truncated": false},
                {"path": "big.txt", "status": "added", "additions": 9000, "deletions": 0, "diff": "@@ -0,0 +1,9000 @@\n+x\n", "truncated": true},
                {"status": "added"}
            ]
        });
        let d = parse(&v);
        assert_eq!((d.base.as_str(), d.head.as_str(), d.truncated, d.files_truncated), ("aaa", "bbb", true, false));
        assert_eq!(d.files.len(), 4, "a file without a path is dropped");
        assert_eq!((d.files[0].additions, d.files[0].deletions, d.files[0].letter()), (2, 1, 'M'));
        assert_eq!((d.files[1].old_path.as_str(), d.files[1].letter()), ("old.txt", 'R'));
        assert!(d.files[2].binary && d.files[2].diff.is_empty());
        assert!(d.files[3].truncated && !d.files[0].truncated);
        assert_eq!(parse(&json!({})), TaskDiff::default());
    }

    #[test]
    fn splits_a_unified_diff_into_colored_lines() {
        let got = lines("@@ -1,3 +1,3 @@ fn main\n keep\n-old\tvalue\n+new\n\\ No newline at end of file\n--- not a header here\n");
        let kinds: Vec<LineKind> = got.iter().map(|(k, _)| *k).collect();
        assert_eq!(kinds, [LineKind::Hunk, LineKind::Context, LineKind::Removed, LineKind::Added, LineKind::Note, LineKind::Removed]);
        assert_eq!(got[2].1, "-old    value");
        assert!(lines("").is_empty());
        assert_eq!(lines("@ not a hunk")[0].0, LineKind::Context);
    }

    #[test]
    fn routes_escape_ids_and_paths() {
        assert_eq!(route("p1", "t1", None), "/v3/projects/p1/tasks/t1/diff");
        assert_eq!(route("p1", "t1", Some("src/a b.rs")), "/v3/projects/p1/tasks/t1/diff?path=src%2Fa%20b.rs");
    }
}
