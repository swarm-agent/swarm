//! Models as Swarm offers them: its catalog per ready provider
//! (GET /v1/model/catalog), the person's favorites (Swarm's model profiles,
//! /v1/model-profiles: a name, a model, its thinking and priority) and the
//! agent roles' defaults (/v1/agent-model-settings). The picker shows one
//! list built from these, the way Swarm's own web UI builds it.

use serde_json::{Value, json};

use super::client::{Client, Refusal};
use super::map::{list, s};
use crate::data::Assignment;

/// One model the person can pick.
#[derive(Clone, Debug, PartialEq)]
pub struct ModelOption {
    pub provider: String,
    pub model: String,
    pub display_name: String,
    /// Thinking levels the model takes, in Swarm's words (off, low, medium,
    /// high, xhigh, max); empty when the model does not think.
    pub thinking_options: Vec<String>,
    pub default_thinking: String,
    /// The provider's service tiers (priority): empty when it has none.
    pub service_tiers: Vec<String>,
    pub default_service_tier: String,
    pub context_window: u64,
    /// Swarm's own recommendations: which roles it suggests this model for.
    pub recommended_for: Vec<String>,
}

impl ModelOption {
    pub fn key(&self) -> String {
        format!("{}:{}", self.provider, self.model)
    }

    /// What the person sees: the display name, else the model id.
    pub fn title(&self) -> &str {
        if self.display_name.is_empty() { &self.model } else { &self.display_name }
    }

    /// The thinking a choice starts with: the model's default, else the
    /// first it takes, else off.
    pub fn thinking(&self) -> String {
        if !self.default_thinking.is_empty() {
            self.default_thinking.clone()
        } else {
            self.thinking_options.first().cloned().unwrap_or_else(|| "off".into())
        }
    }

    pub fn assignment(&self, thinking: &str, tier: &str) -> Assignment {
        Assignment { provider: self.provider.clone().into(), model: self.model.clone().into(), thinking: thinking.to_string().into(), tier: tier.to_string().into() }
    }

    /// Does the search text match this model? Every word must appear in the
    /// provider, model or name.
    pub fn matches(&self, query: &str) -> bool {
        let hay = format!("{} {} {}", self.provider, self.model, self.display_name).to_lowercase();
        query.split_whitespace().all(|w| hay.contains(&w.to_lowercase()))
    }
}

/// A favorite: Swarm's saved model profile.
#[derive(Clone, Debug, PartialEq)]
pub struct Favorite {
    pub id: String,
    pub name: String,
    pub assignment: Assignment,
    pub is_default: bool,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Catalog {
    /// Providers that are ready to run, in Swarm's order.
    pub providers: Vec<String>,
    pub options: Vec<ModelOption>,
    /// The person's favorites, in their order.
    pub favorites: Vec<Favorite>,
}

impl Catalog {
    pub fn find(&self, provider: &str, model: &str) -> Option<&ModelOption> {
        self.options.iter().find(|o| o.provider == provider && o.model == model)
    }

    /// Is the model in any favorite?
    pub fn is_favorite(&self, provider: &str, model: &str) -> bool {
        self.favorites.iter().any(|f| f.assignment.provider.as_ref() == provider && f.assignment.model.as_ref() == model)
    }

    /// Models in favorites first, then the rest by provider and name.
    pub fn sorted(&self) -> Vec<&ModelOption> {
        let mut out: Vec<&ModelOption> = self.options.iter().collect();
        out.sort_by(|a, b| {
            self.is_favorite(&b.provider, &b.model)
                .cmp(&self.is_favorite(&a.provider, &a.model))
                .then(a.provider.cmp(&b.provider))
                .then(a.title().to_lowercase().cmp(&b.title().to_lowercase()))
        });
        out
    }
}

/// One provider's catalog records into options.
pub fn options_of(provider: &str, catalog: &Value) -> Vec<ModelOption> {
    list(catalog, "records")
        .iter()
        .map(|r| ModelOption {
            provider: provider.to_string(),
            model: s(r, "model").to_string(),
            display_name: s(r, "display_name").to_string(),
            thinking_options: list(r, "thinking_options").iter().filter_map(Value::as_str).map(String::from).collect(),
            default_thinking: s(r, "default_thinking").to_string(),
            service_tiers: list(r, "service_tiers").iter().filter_map(Value::as_str).map(String::from).collect(),
            default_service_tier: s(r, "default_service_tier").to_string(),
            context_window: r.get("context_window").and_then(Value::as_u64).unwrap_or(0),
            recommended_for: list(r, "recommendations").iter().map(|x| s(x, "role").to_string()).filter(|x| !x.is_empty()).collect(),
        })
        .filter(|o| !o.model.is_empty())
        .collect()
}

/// Favorites from GET /v1/model-profiles.
pub fn favorites_of(v: &Value) -> Vec<Favorite> {
    list(v, "model_profiles")
        .iter()
        .map(|p| Favorite {
            id: s(p, "profile_id").to_string(),
            name: s(p, "name").to_string(),
            assignment: Assignment {
                provider: s(p, "provider").to_string().into(),
                model: s(p, "model").to_string().into(),
                thinking: s(p, "thinking").to_string().into(),
                tier: s(p, "service_tier").to_string().into(),
            },
            is_default: p.get("is_default").and_then(Value::as_bool).unwrap_or(false),
        })
        .filter(|f| !f.id.is_empty())
        .collect()
}

/// Reads the catalog of every provider that is ready to run, and the favorites.
pub fn read(client: &Client) -> Result<Catalog, Refusal> {
    let providers = client.get("/v1/providers")?;
    let ready: Vec<String> = list(&providers, "providers")
        .iter()
        .filter(|p| p.get("ready").and_then(Value::as_bool) == Some(true) && p.get("runnable").and_then(Value::as_bool) != Some(false))
        .map(|p| s(p, "id").to_string())
        .filter(|id| !id.is_empty())
        .collect();
    let mut options = Vec::new();
    for provider in &ready {
        let catalog = client.get(&format!("/v1/model/catalog?provider={provider}&limit=500")).unwrap_or(Value::Null);
        options.extend(options_of(provider, &catalog));
    }
    let favorites = favorites_of(&client.get("/v1/model-profiles").unwrap_or(Value::Null));
    Ok(Catalog { providers: ready, options, favorites })
}

fn body_of(a: &Assignment) -> Value {
    let mut body = json!({"provider": a.provider.as_ref(), "model": a.model.as_ref(), "thinking": if a.thinking.is_empty() { "off" } else { a.thinking.as_ref() }});
    if !a.tier.is_empty() {
        body["service_tier"] = json!(a.tier.as_ref());
    }
    body
}

/// Saves a favorite (POST /v1/model-profiles).
pub fn save_favorite(client: &Client, name: &str, a: &Assignment) -> Result<Value, Refusal> {
    let mut body = body_of(a);
    body["name"] = json!(name);
    client.post("/v1/model-profiles", body)
}

/// Removes a favorite (DELETE /v1/model-profiles/{id}).
pub fn remove_favorite(client: &Client, id: &str) -> Result<Value, Refusal> {
    client.delete(&format!("/v1/model-profiles/{id}"))
}

/// Sets one role's default (PATCH /v1/agent-model-settings): the Orchestrator
/// ("plan"), Swarm ("action") or a system agent ("system-coder", …).
pub fn set_default(client: &Client, role: &str, a: &Assignment) -> Result<Value, Refusal> {
    client.patch("/v1/agent-model-settings", default_body(role, a))
}

/// The PATCH /v1/agent-model-settings body that sets one role.
pub fn default_body(role: &str, a: &Assignment) -> Value {
    let assignment = body_of(a);
    match role {
        "plan" | "action" => json!({"swarm": {role: assignment}}),
        other => json!({"system_agents": {other.trim_start_matches("system-"): assignment}}),
    }
}

/// What a conversation runs on: a choice of its own, or a favorite.
#[derive(Clone, Debug, PartialEq)]
pub enum Choice {
    Temporary(Assignment),
    Favorite(String),
}

impl Choice {
    fn wire(&self) -> Value {
        match self {
            Choice::Temporary(a) => {
                let mut t = body_of(a);
                t["name"] = json!("This chat");
                json!({"temporary": t})
            }
            Choice::Favorite(id) => json!({"saved_profile_id": id}),
        }
    }
}

/// The model for one conversation only (PUT /v3/sessions/{id}/model-profile):
/// nothing else changes.
pub fn set_session_model(client: &Client, session_id: &str, choice: &Choice, request_id: &str) -> Result<Value, Refusal> {
    client.put(&format!("/v3/sessions/{session_id}/model-profile"), json!({"client_request_id": request_id, "choice": choice.wire()}))
}

/// What a new session is created with (POST /v3/sessions model_profile).
pub fn new_session_profile(choice: &Choice) -> Value {
    choice.wire()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn catalog_and_favorites_become_options() {
        let catalog = json!({"records": [
            {"model": "gpt-6-astra", "display_name": "GPT-6 Astra", "thinking_options": ["low", "medium", "high"], "default_thinking": "medium", "context_window": 400000,
             "service_tiers": ["default", "priority"], "default_service_tier": "default", "recommendations": [{"role": "coder"}]},
            {"model": "gpt-5-mini", "context_window": 128000}
        ]});
        let options = options_of("codex", &catalog);
        assert_eq!(options.len(), 2);
        let astra = &options[0];
        assert_eq!(astra.thinking(), "medium");
        assert_eq!(astra.title(), "GPT-6 Astra");
        assert_eq!(astra.service_tiers, vec!["default", "priority"]);
        assert_eq!(astra.recommended_for, vec!["coder"]);
        assert_eq!(options[1].thinking(), "off");
        assert!(astra.matches("gpt astra") && !astra.matches("claude"));
        let favorites = favorites_of(&json!({"model_profiles": [
            {"profile_id": "mp_1", "name": "Fast", "provider": "codex", "model": "gpt-5-mini", "thinking": "off", "service_tier": "priority", "is_default": true}
        ]}));
        assert_eq!(favorites[0].assignment.short(), "gpt-5-mini · off · priority");
        let cat = Catalog { providers: vec!["codex".into()], options, favorites };
        let sorted = cat.sorted();
        assert_eq!(sorted[0].model, "gpt-5-mini", "favorites first");
        assert!(cat.is_favorite("codex", "gpt-5-mini") && !cat.is_favorite("codex", "gpt-6-astra"));
        let a = cat.find("codex", "gpt-6-astra").unwrap().assignment("high", "priority");
        assert_eq!(Choice::Temporary(a).wire()["temporary"]["service_tier"], "priority");
        assert_eq!(Choice::Favorite("mp_1".into()).wire()["saved_profile_id"], "mp_1");
    }
}
