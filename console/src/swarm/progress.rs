//! The installer's --progress=json lines: {"swarm_progress":{...}}.

#[derive(Clone, Debug, PartialEq)]
pub enum Event {
    Step { id: String, text: String },
    Done(String),
    Error(String),
}

/// One stdout line from the installer; human lines are ignored.
pub fn parse_line(line: &str) -> Option<Event> {
    let line = line.trim();
    if !line.starts_with("{\"swarm_progress\"") {
        return None;
    }
    let v: serde_json::Value = serde_json::from_str(line).ok()?;
    let p = v.get("swarm_progress")?;
    if let Some(e) = p.get("error").and_then(|x| x.as_str()) {
        return Some(Event::Error(e.to_string()));
    }
    let id = p.get("step")?.as_str()?.to_string();
    let text = p.get("text").and_then(|x| x.as_str()).unwrap_or("").to_string();
    Some(if id == "done" { Event::Done(text) } else { Event::Step { id, text } })
}

/// The install steps in order, with the label the app shows for each.
pub const STEPS: [(&str, &str); 8] = [
    ("prerequisites", "Git and tools"),
    ("fetch", "Finding Swarm"),
    ("service-user", "Swarm's own account"),
    ("build", "Getting Swarm"),
    ("start", "Starting Swarm"),
    ("isolation", "Checking environments"),
    ("connect-ai", "Your AI's key"),
    ("connect-app", "This app's key"),
];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_progress_lines() {
        assert_eq!(parse_line("==> Building"), None);
        assert_eq!(
            parse_line(r#"{"swarm_progress":{"step":"build","text":"Building \"x\""}}"#),
            Some(Event::Step { id: "build".into(), text: "Building \"x\"".into() })
        );
        assert_eq!(parse_line(r#"{"swarm_progress":{"step":"done","text":"ok"}}"#), Some(Event::Done("ok".into())));
        assert_eq!(parse_line(r#"{"swarm_progress":{"error":"no disk"}}"#), Some(Event::Error("no disk".into())));
    }
}
