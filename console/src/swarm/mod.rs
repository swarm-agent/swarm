//! Talking to a Swarm machine as a client, the way Swarm's own web UI does:
//! its HTTP API as the owner and its realtime stream. Everything here blocks;
//! views run it on the background executor.

pub mod automations;
pub mod client;
pub mod live;
pub mod live_stream;
pub mod map;
pub mod media;
pub mod models;
pub mod needs;
pub mod permissions;
pub mod fleet;
pub mod fleet_keys;
pub mod diff;
pub mod fleet_models;
pub mod progress;
pub mod realtime;
pub mod repository;
pub mod target;
pub mod tool_summary;
pub mod workflows;

/// The name this app connects under: its session is
/// ~/.config/swarm/clients/swarm-console.token.
pub const APP_NAME: &str = "swarm-console";
