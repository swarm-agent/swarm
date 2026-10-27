//! One brief line for a tool call: what it returned (or why it failed) and
//! how long it took, from what Swarm recorded for it (its name, arguments,
//! output and error). Never a dump: at most one short line.

use serde_json::{Map, Value};

/// The longest summary, in characters (with its ellipsis).
pub const SUMMARY_MAX: usize = 60;
/// The longest whole tool line (name, detail, summary, time), in characters.
pub const LINE_MAX: usize = 100;

/// `text` on one line: control characters and runs of spaces become one
/// space, cut to `max` characters with an ellipsis.
pub fn shorten(text: &str, max: usize) -> String {
    let mut one = String::with_capacity(text.len().min(max * 4));
    let mut space = false;
    for c in text.chars() {
        if c.is_whitespace() || c.is_control() {
            space = !one.is_empty();
        } else {
            if space {
                one.push(' ');
                space = false;
            }
            one.push(c);
        }
    }
    if one.chars().count() <= max {
        return one;
    }
    let mut cut: String = one.chars().take(max.saturating_sub(1)).collect();
    cut.truncate(cut.trim_end().len());
    cut.push('…');
    cut
}

/// A line that says something: not blank, not only fences or brackets, not
/// Swarm's truncation marker.
fn meaningful(line: &str) -> bool {
    let line = line.trim();
    !line.is_empty() && line != "[tool output truncated]" && line.chars().any(char::is_alphanumeric)
}

fn first_line(text: &str) -> Option<&str> {
    text.lines().map(str::trim).find(|l| meaningful(l))
}

fn last_line(text: &str) -> Option<&str> {
    text.lines().rev().map(str::trim).find(|l| meaningful(l))
}

/// A JSON value that may arrive as a JSON string.
fn json(value: &Value) -> Value {
    match value {
        Value::String(t) => serde_json::from_str::<Value>(t).unwrap_or(Value::Null),
        v => v.clone(),
    }
}

fn text<'a>(v: &'a Value, key: &str) -> Option<&'a str> {
    v.get(key).and_then(Value::as_str).map(str::trim).filter(|s| !s.is_empty())
}

fn number(v: &Value, key: &str) -> Option<i64> {
    v.get(key).and_then(|n| n.as_i64().or_else(|| n.as_f64().map(|f| f as i64)))
}

/// The tool's own name without an MCP server prefix, lower case, `-` as `_`.
fn kind(name: &str) -> String {
    let name = name.rsplit("__").next().unwrap_or(name);
    name.trim().to_lowercase().replace('-', "_")
}

fn file_name(path: &str) -> &str {
    path.trim_end_matches('/').rsplit(['/', '\\']).next().unwrap_or(path)
}

fn plural(n: i64, one: &str, many: &str) -> String {
    format!("{n} {}", if n == 1 { one } else { many })
}

/// The number of entries a result lists: its count, else the length of its
/// first list.
fn count(out: &Value, keys: &[&str]) -> Option<i64> {
    number(out, "count")
        .or_else(|| number(out, "total"))
        .or_else(|| keys.iter().find_map(|k| out.get(*k).and_then(Value::as_array).map(|a| a.len() as i64)))
}

/// A brief summary of a finished tool call: a failure's reason, else what
/// the call returned, in at most [`SUMMARY_MAX`] characters on one line.
/// `exit_code` is the one Swarm reported beside the event, if any. Empty
/// when there is nothing to say.
pub fn summarize(name: &str, arguments: &Value, output: &str, error: &str, exit_code: Option<i64>) -> String {
    if let Some(line) = first_line(error) {
        return shorten(line, SUMMARY_MAX);
    }
    let args = json(arguments);
    let out = serde_json::from_str::<Value>(output.trim()).unwrap_or(Value::Null);
    // A linked machine's answer comes wrapped ({action, machine, data}); the
    // result is its data.
    let out = match out.get("data") {
        Some(data) if data.is_object() || data.is_array() => data.clone(),
        _ => out,
    };
    let said = match kind(name).as_str() {
        "bash" | "shell" | "sh" | "exec" | "run" | "run_command" | "command" | "terminal" => shell(&out, output, exit_code),
        k if k.starts_with("git_") => shell(&out, output, exit_code),
        "read" | "read_file" | "view" | "cat" | "open" => read(&out, output),
        "find" | "glob" | "list" | "ls" | "list_files" => found(&out, output, "file", "files"),
        "search" | "grep" | "rg" | "agentic_search" | "websearch" | "web_search" => found(&out, output, "match", "matches"),
        "write" | "write_file" | "create_file" => saved(&args, &out, "saved"),
        "edit" | "multi_edit" | "multiedit" | "str_replace" | "apply_patch" | "update" => edited(&args, &out),
        k if k.contains("media") || k.contains("image") || k.contains("video") || k.contains("design") || k.contains("artifact") => media(&args, &out),
        k if k.starts_with("manage_") || k.contains("task") || k.contains("todo") || k.contains("project") => managed(&args, &out),
        _ => None,
    };
    let said = said.or_else(|| generic(&out)).or_else(|| first_line(output).map(str::to_string)).unwrap_or_default();
    shorten(&said, SUMMARY_MAX)
}

/// A command: its exit code and the last line it printed.
fn shell(out: &Value, output: &str, exit_code: Option<i64>) -> Option<String> {
    let code = exit_code.or_else(|| number(out, "exit_code")).or_else(|| scanned_exit_code(output));
    let printed = if out.is_object() {
        ["output", "stdout", "combined_output", "stderr"].iter().find_map(|k| text(out, k)).and_then(last_line).map(str::to_string)
    } else if out.is_null() && !output.trim_start().starts_with('{') {
        last_line(output).map(str::to_string)
    } else {
        None
    };
    let timed_out = out.get("timed_out").and_then(Value::as_bool) == Some(true);
    let mut parts = Vec::new();
    if timed_out {
        parts.push("timed out".to_string());
    }
    if let Some(code) = code {
        parts.push(format!("exit {code}"));
    }
    parts.extend(printed);
    (!parts.is_empty()).then(|| parts.join(" · "))
}

/// The exit code of a command result cut short (no longer valid JSON).
fn scanned_exit_code(output: &str) -> Option<i64> {
    let rest = output.split_once("\"exit_code\":")?.1.trim_start();
    let digits: String = rest.chars().take_while(|c| c.is_ascii_digit() || *c == '-').collect();
    digits.parse().ok()
}

/// A file read: how many lines.
fn read(out: &Value, output: &str) -> Option<String> {
    if out.is_object() {
        let got = number(out, "count").or_else(|| out.get("lines").and_then(Value::as_array).map(|a| a.len() as i64));
        let total = number(out, "total_lines");
        return match (got, total) {
            (Some(got), Some(total)) if total > got => Some(format!("{got} of {total} lines")),
            (_, Some(total)) => Some(plural(total, "line", "lines")),
            (Some(got), None) => Some(plural(got, "line", "lines")),
            _ => None,
        };
    }
    if out.is_null() && !output.trim().is_empty() {
        return Some(plural(output.trim_end().lines().count() as i64, "line", "lines"));
    }
    None
}

/// A search or listing: how many it found.
fn found(out: &Value, output: &str, one: &str, many: &str) -> Option<String> {
    if out.is_object() {
        return count(out, &["matches", "results", "files", "entries", "items"]).map(|n| plural(n, one, many));
    }
    if let Some(a) = out.as_array() {
        return Some(plural(a.len() as i64, one, many));
    }
    if out.is_null() {
        let n = output.lines().filter(|l| meaningful(l)).count() as i64;
        return Some(plural(n, one, many));
    }
    None
}

fn path_of<'a>(args: &'a Value, out: &'a Value) -> Option<&'a str> {
    ["path", "file_path", "filename", "file"].iter().find_map(|k| text(args, k).or_else(|| text(out, k)))
}

/// A file written: "saved" and its file name.
fn saved(args: &Value, out: &Value, verb: &str) -> Option<String> {
    Some(match path_of(args, out) {
        Some(path) => format!("{verb} {}", file_name(path)),
        None => verb.to_string(),
    })
}

/// A file edited: "edited", its file name and how many replacements.
fn edited(args: &Value, out: &Value) -> Option<String> {
    let mut line = saved(args, out, "edited")?;
    if let Some(n) = number(out, "replacements").filter(|n| *n > 1) {
        line.push_str(&format!(" · {n} replacements"));
    }
    Some(line)
}

/// The first id a result names.
fn id_of(out: &Value) -> Option<&str> {
    ["task_id", "project_id", "session_id", "run_id", "id"].iter().find_map(|k| text(out, k))
        .or_else(|| ["task", "project", "result"].iter().find_map(|k| out.get(*k).and_then(|o| text(o, "id"))))
}

fn status_of(out: &Value) -> Option<&str> {
    ["status", "state"].iter().find_map(|k| text(out, k))
        .or_else(|| ["task", "project", "result"].iter().find_map(|k| out.get(*k).and_then(|o| text(o, "status"))))
}

/// A management tool (projects, tasks, automations): its action, the
/// resulting status and id.
fn managed(_args: &Value, out: &Value) -> Option<String> {
    // The action is already in the line's detail; the summary is the result.
    let mut parts: Vec<String> = Vec::new();
    if let Some(status) = status_of(out) {
        parts.push(status.to_string());
    }
    if let Some(id) = id_of(out) {
        parts.push(id.to_string());
    } else if let Some(listed) = listed(out) {
        parts.push(listed);
    }
    (!parts.is_empty()).then(|| parts.join(" · "))
}

/// The first list in a result, counted under its own name: "3 machines",
/// "0 automations", "1 project".
fn listed(out: &Value) -> Option<String> {
    let (key, items) = out.as_object()?.iter().find_map(|(k, v)| v.as_array().map(|a| (k.as_str(), a.len() as i64)))?;
    let many = key.replace('_', " ");
    let one = many.strip_suffix("ies").map(|s| format!("{s}y")).or_else(|| many.strip_suffix('s').map(str::to_string)).unwrap_or_else(|| many.clone());
    Some(plural(items, &one, &many))
}

/// A media request: its status and how many files it made.
fn media(args: &Value, out: &Value) -> Option<String> {
    let mut parts: Vec<String> = Vec::new();
    if let Some(action) = text(args, "action") {
        parts.push(action.to_string());
    }
    if let Some(status) = status_of(out) {
        parts.push(status.to_string());
    }
    let files = ["files", "media", "artifacts", "outputs", "images", "videos", "items"]
        .iter()
        .find_map(|k| out.get(*k).and_then(Value::as_array).map(|a| a.len() as i64))
        .or_else(|| number(out, "count"));
    if let Some(n) = files {
        parts.push(plural(n, "file", "files"));
    }
    (!parts.is_empty()).then(|| parts.join(" · "))
}

/// Any JSON result: its own words (summary, message, status, error, count,
/// id), else its size.
fn generic(out: &Value) -> Option<String> {
    match out {
        Value::Object(map) if map.is_empty() => None,
        Value::Object(map) => Some(object(map)),
        Value::Array(items) => Some(plural(items.len() as i64, "item", "items")),
        _ => None,
    }
}

fn object(map: &Map<String, Value>) -> String {
    let out = Value::Object(map.clone());
    let mut parts: Vec<String> = Vec::new();
    if let Some(status) = text(&out, "status").or_else(|| text(&out, "state")) {
        parts.push(status.to_string());
    }
    if let Some(words) = ["summary", "message"].iter().find_map(|k| text(&out, k)).and_then(first_line) {
        parts.push(words.to_string());
    }
    if let Some(error) = text(&out, "error").and_then(first_line) {
        parts.push(format!("error: {error}"));
    }
    if let Some(n) = number(&out, "count") {
        parts.push(format!("count {n}"));
    } else if let Some(n) = number(&out, "total") {
        parts.push(format!("total {n}"));
    }
    if parts.is_empty() {
        if let Some(id) = text(&out, "id") {
            parts.push(id.to_string());
        }
    }
    if parts.is_empty() {
        return listed(&out).unwrap_or_else(|| plural(map.len() as i64, "field", "fields"));
    }
    parts.join(" · ")
}

/// A finished call's time: "320ms", "1.2s", "2m 05s".
pub fn duration(ms: u64) -> String {
    if ms < 1000 {
        format!("{ms}ms")
    } else if ms < 60_000 {
        format!("{:.1}s", ms as f64 / 1000.)
    } else {
        format!("{}m {:02}s", ms / 60_000, (ms % 60_000) / 1000)
    }
}

/// A running call's time so far, in whole seconds; none before 2 s.
pub fn elapsed(ms: u64) -> Option<String> {
    if ms < 2000 {
        return None;
    }
    let secs = ms / 1000;
    Some(if secs < 60 { format!("{secs}s") } else { format!("{}m {:02}s", secs / 60, secs % 60) })
}

/// A tool call as one line, in its parts' byte lengths for coloring:
/// "name detail · summary · time", at most [`LINE_MAX`] characters (the
/// detail gives way first).
#[derive(Debug, PartialEq, Eq)]
pub struct Line {
    pub text: String,
    pub name: usize,
    pub detail: usize,
    pub summary: usize,
    pub time: usize,
}

pub fn line(name: &str, detail: &str, summary: &str, time: &str) -> Line {
    let name = shorten(name, 40);
    let summary = shorten(summary, SUMMARY_MAX);
    let time = shorten(time, 12);
    let fixed = name.chars().count()
        + if summary.is_empty() { 0 } else { 3 + summary.chars().count() }
        + if time.is_empty() { 0 } else { 3 + time.chars().count() };
    let room = LINE_MAX.saturating_sub(fixed + 1).max(16);
    let detail = shorten(detail, room);
    // Separators only between parts that are there: a line without a name
    // starts with its detail.
    let detail = match (name.is_empty(), detail.is_empty()) {
        (_, true) => String::new(),
        (true, false) => detail,
        (false, false) => format!(" {detail}"),
    };
    let lead = name.is_empty() && detail.is_empty();
    let summary = match (summary.is_empty(), lead) {
        (true, _) => String::new(),
        (false, true) => summary,
        (false, false) => format!(" · {summary}"),
    };
    let lead = lead && summary.is_empty();
    let time = match (time.is_empty(), lead) {
        (true, _) => String::new(),
        (false, true) => time,
        (false, false) => format!(" · {time}"),
    };
    Line {
        text: format!("{name}{detail}{summary}{time}"),
        name: name.len(),
        detail: detail.len(),
        summary: summary.len(),
        time: time.len(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn args(v: Value) -> Value {
        Value::String(v.to_string())
    }

    #[test]
    fn bash_shows_its_exit_code_and_last_line() {
        let out = json!({"command": "go test ./...", "exit_code": 0, "timed_out": false, "output": "ok  pkg/a\t0.3s\nok  pkg/b\t1.1s\n\n", "summary": "Command succeeded"}).to_string();
        assert_eq!(summarize("bash", &args(json!({"command": "go test ./..."})), &out, "", None), "exit 0 · ok pkg/b 1.1s");
    }

    #[test]
    fn bash_exit_code_from_the_event_and_plain_text() {
        assert_eq!(summarize("bash", &Value::Null, "building\nerror: no such file\n", "", Some(2)), "exit 2 · error: no such file");
    }

    #[test]
    fn bash_cut_short_still_has_its_exit_code() {
        let out = "{\"binary_suppressed\":false,\"command\":\"cat big\",\"exit_code\":1,\"output\":\"aaaa\n[tool output truncated]";
        assert_eq!(summarize("bash", &Value::Null, out, "", None), "exit 1");
    }

    #[test]
    fn bash_timed_out() {
        let out = json!({"exit_code": -1, "timed_out": true, "output": ""}).to_string();
        assert_eq!(summarize("bash", &Value::Null, &out, "", None), "timed out · exit -1");
    }

    #[test]
    fn read_counts_lines() {
        let out = json!({"path": "/p/a.rs", "count": 120, "total_lines": 800, "lines": []}).to_string();
        assert_eq!(summarize("read", &args(json!({"path": "/p/a.rs"})), &out, "", None), "120 of 800 lines");
        let whole = json!({"count": 42, "total_lines": 42}).to_string();
        assert_eq!(summarize("read", &Value::Null, &whole, "", None), "42 lines");
        assert_eq!(summarize("read", &Value::Null, "one\ntwo\nthree\n", "", None), "3 lines");
    }

    #[test]
    fn search_counts_matches() {
        let out = json!({"path": ".", "count": 7, "matches": []}).to_string();
        assert_eq!(summarize("search", &args(json!({"pattern": "fn main"})), &out, "", None), "7 matches");
        let one = json!({"results": [{"path": "a"}]}).to_string();
        assert_eq!(summarize("grep", &Value::Null, &one, "", None), "1 match");
        assert_eq!(summarize("find", &Value::Null, "a.rs\nb.rs\n", "", None), "2 files");
        assert_eq!(summarize("mcp__fs__glob", &Value::Null, "[\"a\",\"b\",\"c\"]", "", None), "3 files");
    }

    #[test]
    fn write_and_edit_name_the_file() {
        let out = json!({"path": "/p/src/main.rs", "bytes_written": 120}).to_string();
        assert_eq!(summarize("write", &args(json!({"path": "/p/src/main.rs", "content": "…"})), &out, "", None), "saved main.rs");
        let edit = json!({"path": "/p/lib.rs", "matches": 3, "replacements": 3}).to_string();
        assert_eq!(summarize("edit", &args(json!({"path": "/p/lib.rs"})), &edit, "", None), "edited lib.rs · 3 replacements");
        assert_eq!(summarize("edit", &json!({"file_path": "C:\\x\\y.txt"}), "", "", None), "edited y.txt");
    }

    #[test]
    fn management_tools_show_status_id_and_lists() {
        let out = json!({"task": {"id": "task_8f2", "status": "queued"}}).to_string();
        assert_eq!(summarize("manage_projects", &args(json!({"action": "propose_task"})), &out, "", None), "queued · task_8f2");
        let list = json!({"tasks": [1, 2, 3]}).to_string();
        assert_eq!(summarize("manage-project-tasks", &args(json!({"action": "list"})), &list, "", None), "3 tasks");
        let machines = json!({"machines": [{"name": "build-box"}, {"name": "media-box"}, {"name": "edge-1"}]}).to_string();
        assert_eq!(summarize("manage_projects", &args(json!({"action": "list_machines"})), &machines, "", None), "3 machines");
        let none = json!({"automations": [], "stopped": false}).to_string();
        assert_eq!(summarize("manage_workflows", &args(json!({"action": "list"})), &none, "", None), "0 automations");
        let one = json!({"projects": [{"id": "p"}]}).to_string();
        assert_eq!(summarize("manage_projects", &args(json!({"action": "list"})), &one, "", None), "1 project");
        assert_eq!(summarize("weird", &Value::Null, &json!({"entries": [1, 2], "x": 1}).to_string(), "", None), "2 entries");
        let remote = json!({"action": "list", "machine": "build-box", "data": {"count": 4, "projects": [1, 2, 3, 4]}, "untrusted_content": true}).to_string();
        assert_eq!(summarize("manage_projects", &args(json!({"action": "list", "machine": "build-box"})), &remote, "", None), "4 projects");
    }

    #[test]
    fn media_shows_status_and_count() {
        let out = json!({"status": "completed", "files": [{"name": "a.png"}, {"name": "b.png"}]}).to_string();
        assert_eq!(summarize("manage_media", &args(json!({"action": "generate"})), &out, "", None), "generate · completed · 2 files");
    }

    #[test]
    fn generic_json_prefers_its_own_words() {
        let out = json!({"status": "ok", "message": "Theme applied\nmore", "prompt_injection_tag": "x"}).to_string();
        assert_eq!(summarize("weird", &Value::Null, &out, "", None), "ok · Theme applied");
        assert_eq!(summarize("weird", &Value::Null, &json!({"count": 4}).to_string(), "", None), "count 4");
        assert_eq!(summarize("weird", &Value::Null, &json!({"a": 1, "b": 2}).to_string(), "", None), "2 fields");
        assert_eq!(summarize("weird", &Value::Null, "[1,2]", "", None), "2 items");
        assert_eq!(summarize("weird", &Value::Null, &json!({"id": "x1"}).to_string(), "", None), "x1");
    }

    #[test]
    fn plain_text_gives_its_first_meaningful_line() {
        assert_eq!(summarize("webfetch", &Value::Null, "\n```\n# Title here\nbody", "", None), "# Title here");
        assert_eq!(summarize("webfetch", &Value::Null, "   \n", "", None), "");
    }

    #[test]
    fn a_failure_gives_its_reason() {
        assert_eq!(summarize("bash", &Value::Null, "{}", "\npermission denied: /etc\nstack…", None), "permission denied: /etc");
    }

    #[test]
    fn every_summary_is_one_short_line() {
        let long = "x".repeat(500) + "\nsecond";
        for got in [
            summarize("bash", &Value::Null, &("first\n".to_string() + &"y".repeat(500)), "", Some(0)),
            summarize("webfetch", &Value::Null, &long, "", None),
            summarize("bash", &Value::Null, "", &long, None),
            summarize("x", &Value::Null, &json!({"summary": long}).to_string(), "", None),
        ] {
            assert!(got.chars().count() <= SUMMARY_MAX, "{got}");
            assert!(!got.contains('\n'));
            assert!(got.ends_with('…'));
        }
    }

    #[test]
    fn shorten_collapses_space_and_cuts() {
        assert_eq!(shorten("a\t b\n\nc", 10), "a b c");
        assert_eq!(shorten("abcdefghij", 5), "abcd…");
        assert_eq!(shorten("abc  defgh", 5), "abc…");
    }

    #[test]
    fn times() {
        assert_eq!(duration(320), "320ms");
        assert_eq!(duration(1234), "1.2s");
        assert_eq!(duration(125_000), "2m 05s");
        assert_eq!(elapsed(1500), None);
        assert_eq!(elapsed(3400).as_deref(), Some("3s"));
        assert_eq!(elapsed(61_000).as_deref(), Some("1m 01s"));
    }

    #[test]
    fn a_line_fits_and_keeps_its_parts() {
        let l = line("bash", "go test ./...", "exit 0 · ok", "1.2s");
        assert_eq!(l.text, "bash go test ./... · exit 0 · ok · 1.2s");
        assert_eq!(l.name + l.detail + l.summary + l.time, l.text.len());
        assert_eq!(&l.text[l.name..l.name + l.detail], " go test ./...");
        let long = line("read", &"/very/long/path/".repeat(20), &"s".repeat(200), "12.3s");
        assert!(long.text.chars().count() <= LINE_MAX, "{}", long.text);
        assert_eq!(long.name + long.detail + long.summary + long.time, long.text.len());
        assert_eq!(line("attached", "a.png", "", "").text, "attached a.png");
    }
}
