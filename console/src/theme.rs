//! Visual concepts. Each concept is a full token set; views read only tokens.

use gpui::{Hsla, rgb};


#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Concept {
    Graphite,
    Paper,
    Phosphor,
}

impl Concept {
    pub const ALL: [Concept; 3] = [Concept::Graphite, Concept::Paper, Concept::Phosphor];

    pub fn label(self) -> &'static str {
        match self {
            Concept::Graphite => "graphite",
            Concept::Paper => "paper",
            Concept::Phosphor => "phosphor",
        }
    }
}

#[derive(Clone)]
pub struct Theme {
    /// Window background, behind everything.
    pub bg: Hsla,
    /// Sidebars and pane chrome.
    pub panel: Hsla,
    /// Pane body, where transcripts live.
    pub surface: Hsla,
    /// Raised rows: hover, selection, cards.
    pub raised: Hsla,
    pub hover: Hsla,
    pub border: Hsla,
    pub border_strong: Hsla,
    pub text: Hsla,
    pub muted: Hsla,
    pub faint: Hsla,
    pub accent: Hsla,
    pub accent_text: Hsla,
    pub ok: Hsla,
    pub warn: Hsla,
    pub err: Hsla,
    pub info: Hsla,
    pub mono: &'static str,
    pub radius: f32,
}

fn c(hex: u32) -> Hsla {
    rgb(hex).into()
}

impl Theme {
    pub fn for_concept(concept: Concept) -> Theme {
        match concept {
            // Neutral dark, quiet chrome, one cool accent.
            Concept::Graphite => Theme {
                bg: c(0x0d0e10),
                panel: c(0x121316),
                surface: c(0x0f1013),
                raised: c(0x1a1c20),
                hover: c(0x17191d),
                border: c(0x23252a),
                border_strong: c(0x2f3238),
                text: c(0xe6e7ea),
                muted: c(0x9a9da5),
                faint: c(0x5f636b),
                accent: c(0x7aa2ff),
                accent_text: c(0x0d0e10),
                ok: c(0x5fd08c),
                warn: c(0xe7b45a),
                err: c(0xf06a6a),
                info: c(0x7aa2ff),
                mono: "JetBrains Mono",
                radius: 4.0,
            },
            // Light, printed-page feel, ink accent.
            Concept::Paper => Theme {
                bg: c(0xf3f2ee),
                panel: c(0xeceae4),
                surface: c(0xfbfaf7),
                raised: c(0xffffff),
                hover: c(0xe4e1d9),
                border: c(0xdcd9d0),
                border_strong: c(0xc9c5ba),
                text: c(0x1d1d1b),
                muted: c(0x5d5b55),
                faint: c(0x96938a),
                accent: c(0x2a3bd1),
                accent_text: c(0xffffff),
                ok: c(0x1f8a4c),
                warn: c(0xa86a00),
                err: c(0xc0352b),
                info: c(0x2a3bd1),
                mono: "JetBrains Mono",
                radius: 3.0,
            },
            // Terminal green on near-black, square corners.
            Concept::Phosphor => Theme {
                bg: c(0x050806),
                panel: c(0x08100b),
                surface: c(0x060a07),
                raised: c(0x0e1a12),
                hover: c(0x0b150f),
                border: c(0x15281c),
                border_strong: c(0x1f3b29),
                text: c(0xc8f5d4),
                muted: c(0x7fb08c),
                faint: c(0x4a6b53),
                accent: c(0x3cff8f),
                accent_text: c(0x031008),
                ok: c(0x3cff8f),
                warn: c(0xffd166),
                err: c(0xff5f56),
                info: c(0x6fd3ff),
                mono: "JetBrains Mono",
                radius: 0.0,
            },
        }
    }
}

impl Theme {
    /// The app's colors from a Swarm theme palette (Swarm's catalog: builtin
    /// themes and ones made with manage-theme), each token from the palette
    /// color of the same job. Colors the palette leaves out keep `base`.
    pub fn from_palette(palette: &serde_json::Value, base: &Theme) -> Theme {
        let get = |key: &str| {
            let hex = palette.get(key)?.as_str()?.trim_start_matches('#');
            let n = u32::from_str_radix(hex, 16).ok().filter(|_| hex.len() == 6)?;
            Some(c(n))
        };
        let mix = |a: Hsla, b: Hsla, t: f32| -> Hsla {
            let (a, b) = (gpui::Rgba::from(a), gpui::Rgba::from(b));
            gpui::Rgba { r: a.r + (b.r - a.r) * t, g: a.g + (b.g - a.g) * t, b: a.b + (b.b - a.b) * t, a: 1. }.into()
        };
        let bg = get("background").unwrap_or(base.bg);
        let text = get("text").unwrap_or(base.text);
        let muted = get("text_muted").unwrap_or(base.muted);
        let element = get("element").unwrap_or(base.raised);
        Theme {
            bg,
            panel: get("panel").unwrap_or(base.panel),
            surface: bg,
            raised: element,
            hover: mix(element, text, 0.04),
            border: get("border").unwrap_or(base.border),
            border_strong: get("border_active").unwrap_or(base.border_strong),
            text,
            muted,
            faint: mix(muted, bg, 0.35),
            accent: get("primary").or_else(|| get("accent")).unwrap_or(base.accent),
            accent_text: bg,
            ok: get("success").unwrap_or(base.ok),
            warn: get("warning").unwrap_or(base.warn),
            err: get("error").unwrap_or(base.err),
            info: get("secondary").or_else(|| get("accent")).unwrap_or(base.info),
            ..base.clone()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn palette_colors_land_on_their_tokens() {
        let base = Theme::for_concept(Concept::Graphite);
        let t = Theme::from_palette(&serde_json::json!({"background": "#101418", "text": "#E0E0E0", "primary": "#33AAFF", "error": "#FF0000"}), &base);
        assert_eq!(t.bg, c(0x101418));
        assert_eq!(t.accent, c(0x33aaff));
        assert_eq!(t.err, c(0xff0000));
        assert_eq!(t.ok, base.ok, "a color the palette leaves out keeps the base");
        let bad = Theme::from_palette(&serde_json::json!({"background": "nope"}), &base);
        assert_eq!(bad.bg, base.bg);
    }
}
