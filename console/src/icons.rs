//! Icons: one name, several drawing styles. Every SVG is a single-color mask
//! that GPUI tints with the current text color, so any theme (or a user's own
//! colors) restyles icons without new artwork.

use std::borrow::Cow;

use gpui::{AssetSource, Result, SharedString};

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum IconStyle {
    /// 1.75px rounded strokes, 24px grid.
    Line,
    /// Filled shapes with cut-outs.
    Solid,
    /// 12x12 one-bit pixels, from assets/icons/pixel.txt.
    Pixel,
    /// Unicode characters in the UI font; no artwork at all.
    Glyph,
}

impl IconStyle {
    pub const ALL: [IconStyle; 4] = [IconStyle::Line, IconStyle::Solid, IconStyle::Pixel, IconStyle::Glyph];

    pub fn label(self) -> &'static str {
        match self {
            IconStyle::Line => "line",
            IconStyle::Solid => "solid",
            IconStyle::Pixel => "pixel",
            IconStyle::Glyph => "glyph",
        }
    }

    pub fn next(self) -> IconStyle {
        let i = IconStyle::ALL.iter().position(|s| *s == self).unwrap_or(0);
        IconStyle::ALL[(i + 1) % IconStyle::ALL.len()]
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Icon {
    Sessions,
    Media,
    Usage,
    Agents,
    Settings,
    Search,
    Plus,
    SplitRight,
    SplitDown,
    Close,
    Collapse,
    Expand,
    Project,
    Machine,
    Tasks,
    ChevronDown,
    ChevronRight,
    Copy,
    Shield,
    Live,
}

impl Icon {
    pub fn name(self) -> &'static str {
        match self {
            Icon::Sessions => "sessions",
            Icon::Media => "media",
            Icon::Usage => "usage",
            Icon::Agents => "agents",
            Icon::Settings => "settings",
            Icon::Search => "search",
            Icon::Plus => "plus",
            Icon::SplitRight => "split-right",
            Icon::SplitDown => "split-down",
            Icon::Close => "close",
            Icon::Collapse => "collapse",
            Icon::Expand => "expand",
            Icon::Project => "project",
            Icon::Machine => "machine",
            Icon::Tasks => "tasks",
            Icon::ChevronDown => "chevron-down",
            Icon::ChevronRight => "chevron-right",
            Icon::Copy => "copy",
            Icon::Shield => "shield",
            Icon::Live => "live",
        }
    }

    /// The text fallback used by the glyph style.
    pub fn glyph(self) -> &'static str {
        match self {
            Icon::Sessions => "▤",
            Icon::Media => "▧",
            Icon::Usage => "▥",
            Icon::Agents => "◎",
            Icon::Settings => "⚙",
            Icon::Search => "⌕",
            Icon::Plus => "+",
            Icon::SplitRight => "◫",
            Icon::SplitDown => "⊟",
            Icon::Close => "×",
            Icon::Collapse => "«",
            Icon::Expand => "»",
            Icon::Project => "▣",
            Icon::Machine => "▭",
            Icon::Tasks => "≣",
            Icon::ChevronDown => "▾",
            Icon::ChevronRight => "▸",
            Icon::Copy => "⧉",
            Icon::Shield => "⛨",
            Icon::Live => "∿",
        }
    }

    pub fn path(self, style: IconStyle) -> SharedString {
        format!("icons/{}/{}.svg", style.label(), self.name()).into()
    }
}

macro_rules! icon_table {
    ($($name:literal),* $(,)?) => {
        &[
            $((concat!("icons/line/", $name, ".svg"), include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/assets/icons/line/", $name, ".svg")) as &[u8]),)*
            $((concat!("icons/solid/", $name, ".svg"), include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/assets/icons/solid/", $name, ".svg")) as &[u8]),)*
            $((concat!("icons/pixel/", $name, ".svg"), include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/assets/icons/pixel/", $name, ".svg")) as &[u8]),)*
        ]
    };
}

static ICONS: &[(&str, &[u8])] = icon_table!(
    "sessions", "media", "usage", "agents", "settings", "search", "plus", "split-right",
    "split-down", "close", "collapse", "expand", "project", "machine", "tasks",
    "chevron-down", "chevron-right", "copy", "shield", "live",
);

/// Swarm's mark, the brand SVG as it is (its nested squares and their
/// opacities), drawn in the theme's accent.
pub const BRAND_MARK: &str = "brand/swarm-mark.svg";

/// Serves the embedded icon SVGs and the brand mark to GPUI.
pub struct Assets;

impl AssetSource for Assets {
    fn load(&self, path: &str) -> Result<Option<Cow<'static, [u8]>>> {
        if path == BRAND_MARK {
            return Ok(Some(Cow::Borrowed(include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/assets/brand/swarm-mark.svg")))));
        }
        Ok(ICONS.iter().find(|(p, _)| *p == path).map(|(_, bytes)| Cow::Borrowed(*bytes)))
    }

    fn list(&self, path: &str) -> Result<Vec<SharedString>> {
        Ok(ICONS
            .iter()
            .filter(|(p, _)| p.starts_with(path))
            .map(|(p, _)| SharedString::from(*p))
            .collect())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_icon_has_every_style() {
        let all = [
            Icon::Sessions, Icon::Media, Icon::Usage, Icon::Agents, Icon::Settings, Icon::Search,
            Icon::Plus, Icon::SplitRight, Icon::SplitDown, Icon::Close, Icon::Collapse,
            Icon::Expand, Icon::Project, Icon::Machine, Icon::Tasks, Icon::ChevronDown,
            Icon::ChevronRight, Icon::Copy, Icon::Shield, Icon::Live,
        ];
        for icon in all {
            for style in [IconStyle::Line, IconStyle::Solid, IconStyle::Pixel] {
                let path = icon.path(style);
                let bytes = Assets.load(&path).unwrap().unwrap_or_else(|| panic!("missing {path}"));
                assert!(std::str::from_utf8(&bytes).unwrap().starts_with("<svg"), "{path}");
            }
        }
        assert_eq!(ICONS.len(), all.len() * 3);
    }
}
