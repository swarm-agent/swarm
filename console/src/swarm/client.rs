//! Swarm's HTTP API as the owner: this app's client session, sent as
//! X-Swarm-Token to the loopback API (127.0.0.1:7781 here, or that port on a
//! server through the app's SSH tunnel). The same routes Swarm's own web UI
//! uses; errors never contain the token.

use std::time::Duration;

use serde_json::Value;

#[derive(Clone)]
pub struct Client {
    base: String,
    token: String,
    agent: ureq::Agent,
}

/// What Swarm answered when it refused or failed.
#[derive(Debug, Clone, PartialEq)]
pub enum Refusal {
    /// 401: the client session is revoked, expired or unknown.
    SignedOut,
    /// 403: the owner may not do this (never expected for a client session).
    Forbidden(String),
    /// Any other HTTP status with Swarm's message and its whole answer
    /// (some refusals carry the state that explains them).
    Status(u16, String, Value),
    /// Nothing answered.
    Unreachable,
}

impl std::fmt::Display for Refusal {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Refusal::SignedOut => write!(f, "Swarm no longer accepts this app's session. Connect the app again."),
            Refusal::Forbidden(m) if m.is_empty() => write!(f, "Swarm refused that."),
            Refusal::Forbidden(m) => write!(f, "Swarm refused that: {m}"),
            Refusal::Status(code, m, _) if m.is_empty() => write!(f, "Swarm answered HTTP {code}."),
            Refusal::Status(_, m, _) => write!(f, "{m}"),
            Refusal::Unreachable => write!(f, "Swarm is not answering."),
        }
    }
}

impl Client {
    pub fn new(base: impl Into<String>, token: impl Into<String>) -> Client {
        Client {
            base: base.into().trim_end_matches('/').to_string(),
            token: token.into(),
            agent: ureq::AgentBuilder::new().timeout_connect(Duration::from_secs(3)).timeout(Duration::from_secs(60)).build(),
        }
    }

    pub fn base(&self) -> &str {
        &self.base
    }

    pub fn token(&self) -> &str {
        &self.token
    }

    pub fn get(&self, path: &str) -> Result<Value, Refusal> {
        self.send(self.agent.get(&format!("{}{path}", self.base)), None)
    }

    pub fn post(&self, path: &str, body: Value) -> Result<Value, Refusal> {
        self.send(self.agent.post(&format!("{}{path}", self.base)), Some(body))
    }

    pub fn delete(&self, path: &str) -> Result<Value, Refusal> {
        self.send(self.agent.delete(&format!("{}{path}", self.base)), None)
    }

    pub fn put(&self, path: &str, body: Value) -> Result<Value, Refusal> {
        self.send(self.agent.put(&format!("{}{path}", self.base)), Some(body))
    }

    pub fn patch(&self, path: &str, body: Value) -> Result<Value, Refusal> {
        self.send(self.agent.request("PATCH", &format!("{}{path}", self.base)), Some(body))
    }

    /// POST of raw bytes with its own headers (a file sent with a message).
    pub fn post_bytes(&self, path: &str, data: &[u8], headers: &[(&str, &str)]) -> Result<Value, Refusal> {
        let mut request = self.agent.post(&format!("{}{path}", self.base)).set("X-Swarm-Token", &self.token).set("Accept", "application/json");
        for (name, value) in headers {
            request = request.set(name, value);
        }
        let text = request.send_bytes(data).map_err(refusal)?.into_string().map_err(|_| Refusal::Unreachable)?;
        serde_json::from_str(&text).map_err(|_| Refusal::Status(200, "unreadable answer from Swarm".into(), Value::Null))
    }

    /// GET that returns the raw bytes and content type (media files).
    pub fn get_bytes(&self, path: &str) -> Result<(Vec<u8>, String), Refusal> {
        let resp = self
            .agent
            .get(&format!("{}{path}", self.base))
            .set("X-Swarm-Token", &self.token)
            .call()
            .map_err(refusal)?;
        let mime = resp.content_type().to_string();
        let mut data = Vec::new();
        std::io::Read::read_to_end(&mut resp.into_reader(), &mut data).map_err(|_| Refusal::Unreachable)?;
        Ok((data, mime))
    }

    fn send(&self, request: ureq::Request, body: Option<Value>) -> Result<Value, Refusal> {
        let request = request.set("X-Swarm-Token", &self.token).set("Accept", "application/json");
        let resp = match body {
            Some(body) => request.send_json(body),
            None => request.call(),
        }
        .map_err(refusal)?;
        let text = resp.into_string().map_err(|_| Refusal::Unreachable)?;
        if text.trim().is_empty() {
            return Ok(Value::Null);
        }
        serde_json::from_str(&text).map_err(|_| Refusal::Status(200, "unreadable answer from Swarm".into(), Value::Null))
    }
}

fn refusal(err: ureq::Error) -> Refusal {
    match err {
        ureq::Error::Status(401, _) => Refusal::SignedOut,
        ureq::Error::Status(code, resp) => {
            let body = resp.into_string().unwrap_or_default();
            let answer = serde_json::from_str::<Value>(&body).unwrap_or(Value::Null);
            let message = answer
                .get("error")
                .and_then(|e| e.as_str().map(String::from).or_else(|| e.get("message").and_then(Value::as_str).map(String::from)))
                .or_else(|| answer.get("message").and_then(Value::as_str).map(String::from))
                .unwrap_or_else(|| body.trim().chars().take(300).collect());
            if code == 403 { Refusal::Forbidden(message) } else { Refusal::Status(code, message, answer) }
        }
        ureq::Error::Transport(_) => Refusal::Unreachable,
    }
}

/// Swarm answers /healthz without a session: is it running at `base`?
pub fn running(base: &str) -> bool {
    ureq::AgentBuilder::new()
        .timeout_connect(Duration::from_millis(800))
        .timeout(Duration::from_secs(3))
        .build()
        .get(&format!("{}/healthz", base.trim_end_matches('/')))
        .call()
        .ok()
        .and_then(|r| r.into_json::<Value>().ok())
        .and_then(|v| v.get("ok").and_then(Value::as_bool))
        .unwrap_or(false)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nothing_listening_is_unreachable() {
        let c = Client::new("http://127.0.0.1:1", "t");
        assert_eq!(c.get("/v3/projects"), Err(Refusal::Unreachable));
        assert!(!running("http://127.0.0.1:1"));
    }
}
