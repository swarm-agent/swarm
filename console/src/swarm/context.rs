//! A conversation's context as Swarm reports it, and compacting it, through
//! the routes Swarm's web UI uses (no others):
//!
//! - the session detail, `GET /v3/sessions/{id}?message_limit=N&event_limit=0`:
//!   its `usage_summary` (the latest provider request's context occupancy:
//!   input, output, cache and thinking tokens, `total_tokens` and
//!   `remaining_tokens` of `context_window`), the current model's
//!   `context_window`, the session's metadata and its recent messages (where
//!   Swarm's compaction checkpoints are);
//! - compacting now, `POST /v3/sessions/{id}/compact` (`client_request_id`,
//!   `note`);
//! - the auto-compact threshold, `POST /v3/sessions/{id}/metadata` with
//!   `context_compaction_threshold_percent` (remaining context, in percent).
//!
//! Swarm reports totals only. What each part of the context weighs (the
//! agent's prompt, project.md, the injected task list, account memory,
//! history, tool output) is not in any of its answers, so it is not shown.

use serde_json::{Value, json};

use super::client::{Client, Refusal};
use super::map::{list, s};

/// Session metadata Swarm reads for the auto-compact threshold.
pub const THRESHOLD_KEY: &str = "context_compaction_threshold_percent";
/// How Swarm's compaction checkpoint message starts ("[context-compact]
/// index=2 origin=manual").
const MARKER: &str = "[context-compact]";
/// Swarm's usage source after a compaction reset the occupancy: the new size
/// is measured by the next provider request.
pub const RESET_SOURCE: &str = "context_compaction_reset";
/// A usage reading written by a model change, not by a provider request
/// (Swarm's web UI does not show it as occupancy either).
const SETTINGS_SOURCE: &str = "settings_mutation";
/// Messages read back to find the last checkpoint: the same tail the console
/// hydrates a conversation with.
const TAIL: usize = 200;

/// One compaction checkpoint in the conversation.
#[derive(Clone, Debug, PartialEq, Default)]
pub struct Checkpoint {
    /// When Swarm wrote it (ms).
    pub at: u64,
    /// manual, threshold, overflow or plan_guard.
    pub origin: String,
    pub index: u64,
}

/// A conversation's context as Swarm last reported it.
#[derive(Clone, Debug, PartialEq, Default)]
pub struct Reading {
    /// The current model's context window in tokens (0: Swarm did not say).
    pub window: u64,
    /// Tokens in context at the latest provider request; None when Swarm has
    /// no reading for this model yet.
    pub used: Option<u64>,
    pub input: u64,
    pub output: u64,
    pub thinking: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    pub model: String,
    /// Where the reading came from (a provider's usage, or a compaction reset).
    pub source: String,
    /// When Swarm last updated it (ms).
    pub updated_at: u64,
    /// Remaining-context percent at which Swarm is asked to compact; None when unset.
    pub threshold: Option<f64>,
    /// The newest checkpoint among the recent messages read.
    pub checkpoint: Option<Checkpoint>,
}

impl Reading {
    /// Percent of the window in use, when both are known.
    pub fn percent(&self) -> Option<f64> {
        let used = self.used?;
        (self.window > 0).then(|| used.min(self.window) as f64 * 100. / self.window as f64)
    }

    /// True when the occupancy was reset by a compaction and not measured since.
    pub fn reset(&self) -> bool {
        self.source == RESET_SOURCE
    }

    /// The meter's text: "62% · 124k / 200k", "— / 200k" without a reading.
    pub fn meter(&self) -> String {
        let window = if self.window > 0 { super::map::tokens(self.window as f64) } else { "?".into() };
        match (self.used, self.percent()) {
            _ if self.reset() => format!("compacted · — / {window}"),
            (Some(used), Some(p)) => format!("{p:.0}% · {} / {window}", super::map::tokens(used as f64)),
            (Some(used), None) => format!("{} / {window}", super::map::tokens(used as f64)),
            (None, _) => format!("— / {window}"),
        }
    }
}

fn u(v: &Value, key: &str) -> u64 {
    v.get(key).and_then(Value::as_f64).filter(|n| n.is_finite() && *n > 0.).unwrap_or(0.) as u64
}

/// A threshold value as Swarm accepts it: a number, or a string like "5" or "5%".
fn threshold(v: &Value) -> Option<f64> {
    let n = match v {
        Value::Number(n) => n.as_f64(),
        Value::String(t) => t.trim().trim_end_matches('%').trim().parse().ok(),
        _ => None,
    }?;
    (n > 0.).then_some(n.min(100.))
}

/// The newest compaction checkpoint in `messages` (Swarm's system message
/// that starts with its marker).
fn checkpoint(messages: &[Value]) -> Option<Checkpoint> {
    messages
        .iter()
        .filter(|m| s(m, "role") == "system" && s(m, "content").starts_with(MARKER))
        .max_by_key(|m| u(m, "created_at"))
        .map(|m| {
            let head = s(m, "content").lines().next().unwrap_or("");
            let field = |k: &str| head.split_whitespace().find_map(|w| w.strip_prefix(k)).unwrap_or("").to_string();
            Checkpoint { at: u(m, "created_at"), origin: field("origin="), index: field("index=").parse().unwrap_or(0) }
        })
}

/// A session detail (`GET /v3/sessions/{id}`) as a reading.
pub fn reading(detail: &Value) -> Reading {
    let usage = detail.get("usage_summary").filter(|v| v.is_object()).cloned().unwrap_or(Value::Null);
    let model_window = u(detail, "context_window");
    let usage_window = u(&usage, "context_window");
    let source = s(&usage, "source").to_string();
    // A reading belongs to the model it was taken with: one for another
    // window, or written by a settings change, is not this model's occupancy.
    let current = usage.is_object() && source != SETTINGS_SOURCE && (model_window == 0 || usage_window == 0 || usage_window == model_window);
    let window = if model_window > 0 { model_window } else { usage_window };
    let used = current.then(|| {
        let total = u(&usage, "total_tokens");
        if total > 0 || window == 0 { total } else { window.saturating_sub(u(&usage, "remaining_tokens")) }
    });
    let metadata = detail.get("session").and_then(|x| x.get("metadata")).cloned().unwrap_or(Value::Null);
    Reading {
        window,
        used,
        input: if current { u(&usage, "input_tokens") } else { 0 },
        output: if current { u(&usage, "output_tokens") } else { 0 },
        thinking: if current { u(&usage, "thinking_tokens") } else { 0 },
        cache_read: if current { u(&usage, "cache_read_tokens") } else { 0 },
        cache_write: if current { u(&usage, "cache_write_tokens") } else { 0 },
        model: s(&usage, "model").to_string(),
        source,
        updated_at: u(&usage, "updated_at"),
        threshold: metadata.get(THRESHOLD_KEY).and_then(threshold),
        checkpoint: checkpoint(list(detail, "messages")),
    }
}

fn request_id(what: &str, session_id: &str) -> String {
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0);
    format!("swarm-console:{what}:{session_id}:{now}")
}

fn detail(client: &Client, session_id: &str) -> Result<Value, Refusal> {
    client.get(&format!("/v3/sessions/{session_id}?message_limit={TAIL}&event_limit=0"))
}

/// Reads a conversation's context. The answer always names the session; a
/// failure is its `error` (the console shows it on that conversation).
pub fn read(client: &Client, session_id: &str) -> Value {
    match detail(client, session_id) {
        Ok(d) => json!({"session_id": session_id, "detail": d}),
        Err(e) => json!({"session_id": session_id, "error": e.to_string()}),
    }
}

/// Compacts a conversation now, first setting its auto-compact threshold
/// when one is given. The answer names the session and carries the context
/// before and after (`before`, `after`: session details) and Swarm's answer
/// to the compaction (`compact`), or the `error` Swarm refused it with.
pub fn compact(client: &Client, session_id: &str, note: &str, threshold: Option<f64>) -> Value {
    let fail = |e: String| json!({"session_id": session_id, "error": e});
    let before = detail(client, session_id).ok();
    if let Some(percent) = threshold {
        let body = json!({"client_request_id": request_id("compact-threshold", session_id), "metadata": {THRESHOLD_KEY: percent}});
        if let Err(e) = client.post(&format!("/v3/sessions/{session_id}/metadata"), body) {
            return fail(format!("Swarm did not set the auto-compact threshold: {e}"));
        }
    }
    let body = json!({"client_request_id": request_id("compact", session_id), "note": note.trim()});
    let answer = match client.post(&format!("/v3/sessions/{session_id}/compact"), body) {
        Ok(a) => a,
        // The request outlived the client's wait: Swarm may still be compacting.
        Err(Refusal::Unreachable) => return fail("Swarm did not answer the compact request in time; it may still be compacting. The context is read again when the conversation changes.".into()),
        Err(e) => return fail(e.to_string()),
    };
    if answer.get("ok").and_then(Value::as_bool) == Some(false) || !matches!(s(&answer, "status"), "" | "completed") {
        let why = if s(&answer, "error").is_empty() { format!("compaction ended {}", s(&answer, "status")) } else { s(&answer, "error").to_string() };
        return fail(why);
    }
    let after = detail(client, session_id).ok();
    json!({"session_id": session_id, "before": before, "after": after, "threshold": threshold, "compact": answer})
}

/// The line a finished compaction leaves on its conversation, from Swarm's
/// answers: the occupancy before and after, and the threshold set.
pub fn outcome(answer: &Value) -> String {
    let before = answer.get("before").filter(|v| v.is_object()).map(reading);
    let after = answer.get("after").filter(|v| v.is_object()).map(reading);
    let index = answer.get("compact").and_then(|c| c.get("compaction")).map(|c| u(c, "compact_index")).unwrap_or(0);
    let mut line = match (before.as_ref().and_then(Reading::percent), after.as_ref()) {
        (Some(b), Some(a)) if !a.reset() && a.percent().is_some() => format!("Compacted: {b:.0}% → {:.0}% of context", a.percent().unwrap_or(0.)),
        (Some(b), _) => format!("Compacted: {b:.0}% of context before; Swarm measures the new size on the next turn"),
        (None, _) => "Compacted; Swarm measures the new size on the next turn".to_string(),
    };
    if index > 0 {
        line.push_str(&format!(" (compaction {index})"));
    }
    if let Some(t) = answer.get("threshold").and_then(Value::as_f64) {
        line.push_str(&if t > 0. { format!(". Auto-compact set for {t}% context left") } else { ". Auto-compact off".to_string() });
    }
    line.push('.');
    line
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A session detail as Swarm answers it, trimmed to what the meter reads.
    fn fixture() -> Value {
        json!({
            "ok": true,
            "session": {"id": "s1", "metadata": {"agent_name": "system-orchestrator", THRESHOLD_KEY: 5}},
            "context_window": 200000,
            "usage_summary": {"provider": "p", "model": "m", "source": "anthropic_api_usage", "context_window": 200000,
                              "input_tokens": 120000, "output_tokens": 4000, "cache_read_tokens": 90000, "cache_write_tokens": 1000,
                              "thinking_tokens": 500, "total_tokens": 124000, "remaining_tokens": 76000, "updated_at": 1792540800000u64},
            "messages": [
                {"role": "system", "content": "[context-compact] index=2 origin=manual\nThis checkpoint supersedes…", "created_at": 1792540000000u64},
                {"role": "user", "content": "hello", "created_at": 1792540100000u64}
            ]
        })
    }

    #[test]
    fn the_meter_reads_a_snapshot() {
        let r = reading(&fixture());
        assert_eq!(r.window, 200000);
        assert_eq!(r.used, Some(124000));
        assert_eq!(r.percent().map(|p| p.round()), Some(62.));
        assert_eq!(r.meter(), "62% · 124k / 200k");
        assert_eq!(r.threshold, Some(5.));
        assert_eq!(r.checkpoint, Some(Checkpoint { at: 1792540000000, origin: "manual".into(), index: 2 }));
        assert_eq!((r.input, r.output, r.cache_read, r.cache_write, r.thinking), (120000, 4000, 90000, 1000, 500));
    }

    #[test]
    fn no_reading_is_not_shown_as_empty() {
        let mut d = fixture();
        d["usage_summary"] = Value::Null;
        let r = reading(&d);
        assert_eq!(r.used, None);
        assert_eq!(r.meter(), "— / 200k");
        // Another model's window, or a settings change, is not this model's occupancy.
        let mut d = fixture();
        d["context_window"] = json!(1000000);
        assert_eq!(reading(&d).used, None);
        let mut d = fixture();
        d["usage_summary"]["source"] = json!(SETTINGS_SOURCE);
        assert_eq!(reading(&d).used, None);
    }

    #[test]
    fn a_compaction_reset_is_named() {
        let mut d = fixture();
        d["usage_summary"]["source"] = json!(RESET_SOURCE);
        d["usage_summary"]["total_tokens"] = json!(0);
        d["usage_summary"]["remaining_tokens"] = json!(200000);
        let r = reading(&d);
        assert!(r.reset());
        assert_eq!(r.meter(), "compacted · — / 200k");
    }

    #[test]
    fn the_outcome_says_what_swarm_reported() {
        let mut after = fixture();
        after["usage_summary"]["total_tokens"] = json!(18000);
        let answer = json!({"before": fixture(), "after": after, "threshold": 5.0, "compact": {"compaction": {"compact_index": 3}}});
        assert_eq!(outcome(&answer), "Compacted: 62% → 9% of context (compaction 3). Auto-compact set for 5% context left.");
        let mut reset = fixture();
        reset["usage_summary"]["source"] = json!(RESET_SOURCE);
        let answer = json!({"before": fixture(), "after": reset, "threshold": null, "compact": {}});
        assert_eq!(outcome(&answer), "Compacted: 62% of context before; Swarm measures the new size on the next turn.");
    }
}
