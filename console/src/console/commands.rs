//! Slash commands typed in a composer: open a new session beside or below
//! the pane, stop its run, or go to a page. Typing "/" lists them; tab
//! completes the first match and enter runs it.

use gpui::{AnyElement, ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, Open, Page, PaneId, alpha, live::Act};

/// Every command with what it does, in the order the list shows them.
pub(super) const COMMANDS: &[(&str, &str)] = &[
    ("/new", "new session in a pane to the right"),
    ("/new bottom", "new session in a pane below"),
    ("/stop", "stop this session's run"),
    ("/compact", "compact this conversation's context; /compact 5% also sets auto-compact"),
    ("/context", "what this conversation's context holds"),
    ("/theme", "appearance; /theme <what> asks Swarm to change the project's theme"),
    ("/usage", "tokens, plan limits and spend"),
    ("/settings", "settings"),
    ("/media", "images, video and sound"),
    ("/agents", "models per role"),
    ("/fleet", "every machine, what needs you, spend"),
    ("/live", "agents at work and everything happening across the fleet, live"),
];

#[derive(Debug, PartialEq)]
pub(super) enum Slash {
    New(Open),
    Stop,
    /// Appearance, or a request for Swarm to change the theme.
    Theme(Option<String>),
    /// Compact the context now; with a threshold, Swarm's auto-compact
    /// threshold (percent of the context left) is set first.
    Compact { threshold: Option<f64>, note: String },
    /// Open or close the conversation's context panel.
    Context,
    Go(Page),
    Unknown,
}

/// A leading number the way JavaScript's parseFloat reads one ("5", "2.5",
/// "5abc" is 5), so `/compact` reads thresholds as Swarm's web UI does.
fn leading_number(text: &str) -> Option<f64> {
    let mut end = 0;
    let (mut digits, mut dot, mut exp) = (false, false, false);
    for (i, c) in text.char_indices() {
        let ok = match c {
            '0'..='9' => {
                digits = true;
                true
            }
            '+' | '-' => i == 0 || matches!(text[..i].chars().last(), Some('e' | 'E')),
            '.' if !dot && !exp => {
                dot = true;
                true
            }
            'e' | 'E' if digits && !exp => {
                exp = true;
                true
            }
            _ => false,
        };
        if !ok {
            break;
        }
        end = i + c.len_utf8();
    }
    // Trailing "e", "e-" or "." are not part of the number.
    let mut number = &text[..end];
    while let Some(last) = number.chars().last().filter(|c| matches!(*c, 'e' | 'E' | '+' | '-')) {
        number = &number[..number.len() - last.len_utf8()];
    }
    number.parse::<f64>().ok().filter(|n| n.is_finite())
}

/// "5%" as a threshold, clamped to 0..=100 (0 turns auto-compact off).
fn threshold_token(token: &str) -> Option<f64> {
    let raw = token.trim().strip_suffix('%')?.trim();
    let n = leading_number(raw)?;
    Some(n.clamp(0., 100.))
}

/// `/compact`'s words, as Swarm's web UI reads them
/// (`web/src/features/desktop/chat/services/compact-command.ts`): a first
/// word like "5%" (or "threshold 5%") is the threshold; the rest is the note.
fn compact(rest: &str) -> Slash {
    let mut words: Vec<&str> = rest.split_whitespace().collect();
    let mut threshold = words.first().and_then(|w| threshold_token(w));
    if threshold.is_some() {
        words.remove(0);
    } else if words.len() >= 2 && words[0].eq_ignore_ascii_case("threshold") {
        threshold = threshold_token(words[1]);
        if threshold.is_some() {
            words = words.split_off(2);
        }
    }
    Slash::Compact { threshold, note: words.join(" ") }
}

/// What a typed line asks for. A unique prefix is enough ("/us" is /usage).
pub(super) fn parse(text: &str) -> Slash {
    let text = text.trim();
    let (word, rest) = text.split_once(char::is_whitespace).map(|(w, r)| (w, r.trim())).unwrap_or((text, ""));
    let names = ["/new", "/stop", "/compact", "/context", "/theme", "/usage", "/settings", "/media", "/agents", "/fleet", "/live"];
    let hits: Vec<&str> = names.iter().copied().filter(|n| n.starts_with(word) && word.len() > 1).collect();
    let name = if names.contains(&word) { word } else if hits.len() == 1 { hits[0] } else { return Slash::Unknown };
    match (name, rest) {
        ("/new", "" | "side" | "right") => Slash::New(Open::Right),
        ("/new", "bottom" | "below" | "down") => Slash::New(Open::Down),
        ("/stop", "") => Slash::Stop,
        ("/compact", rest) => compact(rest),
        ("/context", "") => Slash::Context,
        ("/theme", "") => Slash::Theme(None),
        ("/theme", what) => Slash::Theme(Some(what.to_string())),
        ("/usage", "") => Slash::Go(Page::Usage),
        ("/settings", "") => Slash::Go(Page::Settings),
        ("/media", "") => Slash::Go(Page::Media),
        ("/agents", "") => Slash::Go(Page::Agents),
        ("/fleet", "") => Slash::Go(Page::Fleet),
        ("/live", "") => Slash::Go(Page::Live),
        _ => Slash::Unknown,
    }
}

/// The commands a typed line could still become.
pub(super) fn matching(text: &str) -> Vec<(&'static str, &'static str)> {
    let text = text.trim_start();
    if !text.starts_with('/') {
        return vec![];
    }
    let typed: Vec<_> = COMMANDS.iter().copied().filter(|(name, _)| name.starts_with(text.trim_end())).collect();
    if !typed.is_empty() {
        return typed;
    }
    // A command with words after it: "/theme blue".
    COMMANDS.iter().copied().filter(|(name, _)| text.starts_with(name) && text[name.len()..].starts_with(' ')).take(1).collect()
}

impl Console {
    /// Opens a new, empty session in a pane beside or below `pid`.
    pub(super) fn new_pane(&mut self, pid: usize, how: Open, cx: &mut Context<Self>) {
        self.active = PaneId(pid);
        let project = self.panes.iter().find(|p| p.id.0 == pid).map(|p| self.sessions[p.session].project).unwrap_or(self.project);
        let unused = (0..self.sessions.len()).find(|i| {
            let s = &self.sessions[*i];
            s.live.is_none() && s.entries.is_empty() && s.project == project && !self.is_open(*i)
        });
        let si = unused.unwrap_or_else(|| {
            self.sessions.push(super::live::draft(project));
            self.sessions.len() - 1
        });
        self.open(si, how);
        self.page = Page::Sessions;
        self.pending_focus = true;
        cx.notify();
    }

    /// Runs a slash command typed in pane `pid`.
    pub(super) fn slash(&mut self, pid: usize, text: &str, cx: &mut Context<Self>) {
        let Some(si) = self.panes.iter().find(|p| p.id.0 == pid).map(|p| p.session) else { return };
        match parse(text) {
            Slash::New(how) => self.new_pane(pid, how, cx),
            Slash::Stop => {
                if self.sessions[si].live.is_some() {
                    self.act(si, Act::Stop, cx);
                } else {
                    self.notice = Some(("This session has not started.".into(), true));
                }
            }
            Slash::Compact { threshold, note } => self.compact(si, threshold, note, cx),
            Slash::Context => self.toggle_context(pid, cx),
            Slash::Theme(None) => self.page = Page::Settings,
            Slash::Theme(Some(what)) => {
                let message = format!("Change this project's theme: {what}");
                if let Some(input) = self.composers.get(&pid).cloned() {
                    input.update(cx, |i, cx| i.set_text(message, cx));
                }
                self.send(pid, cx);
                return;
            }
            Slash::Go(page) => self.page = page,
            Slash::Unknown => {
                self.notice = Some((format!("No command {}. Type / to see them.", text.split_whitespace().next().unwrap_or("")).into(), true));
                cx.notify();
                return;
            }
        }
        if let Some(input) = self.composers.get(&pid).cloned() {
            input.update(cx, |i, cx| i.set_text("", cx));
        }
        cx.notify();
    }

    /// Tab: the typed line becomes the first matching command.
    pub(super) fn complete(&mut self, pid: usize, cx: &mut Context<Self>) {
        let Some(input) = self.composers.get(&pid).cloned() else { return };
        let text = input.read(cx).text();
        if let Some((name, _)) = matching(&text).first() {
            if text.trim() != *name {
                input.update(cx, |i, cx| i.set_text(name.to_string(), cx));
            }
        }
    }

    /// The commands the composer's text could become, listed above it.
    pub(super) fn render_commands(&self, pid: usize, text: &str, cx: &mut Context<Self>) -> Option<Div> {
        let hits = matching(text);
        if hits.is_empty() {
            return None;
        }
        let t = &self.theme;
        let hover = t.hover;
        Some(
            div()
                .flex()
                .flex_col()
                .py_1()
                .mb_1()
                .rounded(px(t.radius))
                .border_1()
                .border_color(t.border)
                .bg(t.panel)
                .children(hits.into_iter().enumerate().map(|(i, (name, help))| -> AnyElement {
                    div()
                        .id(SharedString::from(format!("cmd-{pid}-{i}")))
                        .flex()
                        .gap_3()
                        .px_2()
                        .py(px(2.))
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .when(i == 0, |d| d.bg(alpha(t.accent, 0.12)))
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.slash(pid, name, cx)))
                        .child(div().w(px(96.)).flex_none().text_color(t.accent).child(name))
                        .child(div().flex_1().min_w_0().truncate().text_size(px(11.)).text_color(t.faint).child(help))
                        .into_any_element()
                })),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn commands_and_prefixes() {
        assert_eq!(parse("/new"), Slash::New(Open::Right));
        assert_eq!(parse("/new side"), Slash::New(Open::Right));
        assert_eq!(parse("/new bottom"), Slash::New(Open::Down));
        assert_eq!(parse("/us"), Slash::Go(Page::Usage));
        assert_eq!(parse("/s"), Slash::Unknown, "/s could be /stop or /settings");
        assert_eq!(parse("/stop"), Slash::Stop);
        assert_eq!(parse("/theme"), Slash::Theme(None));
        assert_eq!(parse("/theme dark purple"), Slash::Theme(Some("dark purple".into())));
        assert_eq!(parse("/nope"), Slash::Unknown);
        assert_eq!(parse("/live"), Slash::Go(Page::Live));
        assert_eq!(parse("/li"), Slash::Go(Page::Live));
    }

    #[test]
    fn compact_reads_like_the_web() {
        let c = |threshold: Option<f64>, note: &str| Slash::Compact { threshold, note: note.into() };
        assert_eq!(parse("/compact"), c(None, ""));
        assert_eq!(parse("/compact 5%"), c(Some(5.), ""));
        assert_eq!(parse("/compact 5% keep task ids"), c(Some(5.), "keep task ids"));
        assert_eq!(parse("/compact keep task ids"), c(None, "keep task ids"));
        assert_eq!(parse("/compact threshold 12.5% keep ids"), c(Some(12.5), "keep ids"));
        assert_eq!(parse("/compact 150%"), c(Some(100.), ""));
        assert_eq!(parse("/compact 0%"), c(Some(0.), ""));
        assert_eq!(parse("/compact 5abc%"), c(Some(5.), ""), "parseFloat reads the leading number");
        assert_eq!(parse("/compact %"), c(None, "%"));
        assert_eq!(parse("/compact keep 5%"), c(None, "keep 5%"), "only the first word is a threshold");
        assert_eq!(parse("/context"), Slash::Context);
        // Unique prefixes: /com and /con are distinct; /c and /co could be either.
        assert_eq!(parse("/com"), c(None, ""));
        assert_eq!(parse("/com 5%"), c(Some(5.), ""));
        assert_eq!(parse("/con"), Slash::Context);
        assert_eq!(parse("/c"), Slash::Unknown);
        assert_eq!(parse("/co"), Slash::Unknown);
        // Existing commands keep their prefixes.
        assert_eq!(parse("/st"), Slash::Stop);
        assert_eq!(parse("/se"), Slash::Go(Page::Settings));
        assert_eq!(parse("/us"), Slash::Go(Page::Usage));
        assert_eq!(parse("/context now"), Slash::Unknown);
        let names: Vec<&str> = matching("/compact 5% keep").into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["/compact"]);
        let names: Vec<&str> = matching("/co").into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["/compact", "/context"]);
    }

    #[test]
    fn the_list_narrows_as_you_type() {
        assert_eq!(matching("hello").len(), 0);
        assert_eq!(matching("/").len(), COMMANDS.len());
        let names: Vec<&str> = matching("/ne").into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["/new", "/new bottom"]);
        let names: Vec<&str> = matching("/new b").into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["/new bottom"]);
        let names: Vec<&str> = matching("/theme blue").into_iter().map(|(n, _)| n).collect();
        assert_eq!(names, vec!["/theme"]);
    }
}
