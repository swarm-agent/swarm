//! Markdown in the AI's messages: paragraphs, headings, lists, quotes, code
//! blocks and rules, with bold, italic, code, links and strikethrough inside.
//! `parse` turns the text into blocks of plain text with marked ranges;
//! `Console::markdown` draws them with the theme.

use std::ops::Range;

use gpui::{Div, Font, FontStyle, FontWeight, Hsla, StrikethroughStyle, TextRun, UnderlineStyle, div, font, prelude::*, px};
use pulldown_cmark::{Event, HeadingLevel, Options, Parser, Tag, TagEnd};

use super::{Console, alpha};

pub const BOLD: u8 = 1;
pub const ITALIC: u8 = 2;
pub const CODE: u8 = 4;
pub const LINK: u8 = 8;
pub const STRIKE: u8 = 16;

#[derive(Clone, Debug, PartialEq)]
pub enum Kind {
    Paragraph,
    Heading(u8),
    /// A list item's text: its nesting depth and its marker ("•", "1.");
    /// later paragraphs of the same item have an empty marker.
    Item { depth: usize, marker: String },
    Quote,
    Code,
    Rule,
}

#[derive(Clone, Debug, PartialEq)]
pub struct Block {
    pub kind: Kind,
    pub text: String,
    /// Ranges of `text` and their marks (BOLD | ITALIC ...).
    pub marks: Vec<(Range<usize>, u8)>,
    /// Ranges of `text` that open a link, and where each goes.
    pub links: Vec<(Range<usize>, String)>,
}

/// Links written bare in the text ("see https://…"), which Markdown does not
/// mark: each run from http(s):// to the next space, without the punctuation
/// a sentence closes it with.
fn bare_links(text: &str, marked: &[(Range<usize>, String)]) -> Vec<(Range<usize>, String)> {
    let mut out = Vec::new();
    let mut from = 0;
    while let Some(i) = text[from..].find("http").map(|i| i + from) {
        let rest = &text[i..];
        let scheme = if rest.starts_with("https://") { 8 } else if rest.starts_with("http://") { 7 } else { 0 };
        if scheme == 0 {
            from = i + 4;
            continue;
        }
        let mut end = i + rest.find(|c: char| c.is_whitespace() || matches!(c, '<' | '>' | '"' | '\'' | '`')).unwrap_or(rest.len());
        while end > i + scheme && matches!(text.as_bytes()[end - 1], b'.' | b',' | b';' | b':' | b')' | b']' | b'}' | b'!' | b'?') {
            end -= 1;
        }
        if end > i + scheme && !marked.iter().any(|(r, _)| r.start < end && i < r.end) {
            out.push((i..end, text[i..end].to_string()));
        }
        from = end.max(i + scheme);
    }
    out
}

/// Lists being read: each one's next number (None for bullets).
struct List(Option<u64>);

/// A quick 32-bit fingerprint of a text (FNV-1a), to tell changes apart.
pub(super) fn fnv(text: &str) -> u32 {
    let mut h: u32 = 0x811c9dc5;
    for b in text.bytes() {
        h ^= b as u32;
        h = h.wrapping_mul(0x01000193);
    }
    h
}

pub fn parse(src: &str) -> Vec<Block> {
    let mut blocks = Vec::new();
    let mut text = String::new();
    let mut marks: Vec<(Range<usize>, u8)> = Vec::new();
    let mut on = 0u8;
    let mut heading: Option<u8> = None;
    let mut quote = 0usize;
    let mut code: Option<String> = None;
    let mut lists: Vec<List> = Vec::new();
    // The marker the next text in a list item starts with.
    let mut marker: Option<String> = None;
    // Links: the one being read (where its text starts, its address) and
    // the finished ones of the block.
    let mut link: Option<(usize, String)> = None;
    let mut links: Vec<(Range<usize>, String)> = Vec::new();

    let flush = |blocks: &mut Vec<Block>, text: &mut String, marks: &mut Vec<(Range<usize>, u8)>, links: &mut Vec<(Range<usize>, String)>, kind: Kind| {
        let trimmed_end = text.trim_end().len();
        if trimmed_end == 0 {
            text.clear();
            marks.clear();
            links.clear();
            return;
        }
        text.truncate(trimmed_end);
        let len = text.len();
        let mut marks: Vec<(Range<usize>, u8)> = std::mem::take(marks)
            .into_iter()
            .filter_map(|(r, m)| (r.start < len).then(|| (r.start..r.end.min(len), m)))
            .collect();
        let mut links: Vec<(Range<usize>, String)> = std::mem::take(links)
            .into_iter()
            .filter_map(|(r, u)| (r.start < len).then(|| (r.start..r.end.min(len), u)))
            .collect();
        for (range, url) in bare_links(text, &links) {
            marks.push((range.clone(), LINK));
            links.push((range, url));
        }
        marks.sort_by_key(|(r, _)| r.start);
        links.sort_by_key(|(r, _)| r.start);
        blocks.push(Block { kind, text: std::mem::take(text), marks, links });
    };
    let kind_now = |heading: Option<u8>, quote: usize, lists: &Vec<List>, marker: &mut Option<String>| {
        if let Some(level) = heading {
            Kind::Heading(level)
        } else if !lists.is_empty() {
            Kind::Item { depth: lists.len() - 1, marker: marker.take().unwrap_or_default() }
        } else if quote > 0 {
            Kind::Quote
        } else {
            Kind::Paragraph
        }
    };

    for event in Parser::new_ext(src, Options::ENABLE_STRIKETHROUGH) {
        match event {
            Event::Start(Tag::Heading { level, .. }) => {
                heading = Some(match level {
                    HeadingLevel::H1 => 1,
                    HeadingLevel::H2 => 2,
                    _ => 3,
                })
            }
            Event::End(TagEnd::Heading(_)) => {
                let kind = kind_now(heading, quote, &lists, &mut marker);
                flush(&mut blocks, &mut text, &mut marks, &mut links, kind);
                heading = None;
            }
            Event::End(TagEnd::Paragraph) => {
                let kind = kind_now(heading, quote, &lists, &mut marker);
                flush(&mut blocks, &mut text, &mut marks, &mut links, kind);
            }
            Event::Start(Tag::BlockQuote(_)) => quote += 1,
            Event::End(TagEnd::BlockQuote(_)) => quote = quote.saturating_sub(1),
            Event::Start(Tag::CodeBlock(_)) => {
                code = Some(String::new());
            }
            Event::End(TagEnd::CodeBlock) => {
                if let Some(body) = code.take() {
                    let body = body.trim_end_matches('\n').to_string();
                    blocks.push(Block { kind: Kind::Code, text: body, marks: vec![], links: vec![] });
                }
            }
            Event::Start(Tag::List(start)) => {
                // Text before a nested list belongs to its parent item.
                if !text.trim().is_empty() {
                    let kind = kind_now(heading, quote, &lists, &mut marker);
                    flush(&mut blocks, &mut text, &mut marks, &mut links, kind);
                }
                lists.push(List(start));
            }
            Event::End(TagEnd::List(_)) => {
                lists.pop();
            }
            Event::Start(Tag::Item) => {
                if !text.trim().is_empty() {
                    let kind = kind_now(heading, quote, &lists, &mut marker);
                    flush(&mut blocks, &mut text, &mut marks, &mut links, kind);
                }
                marker = Some(match lists.last_mut() {
                    Some(List(Some(n))) => {
                        let m = format!("{n}.");
                        *n += 1;
                        m
                    }
                    _ => "•".to_string(),
                });
            }
            Event::End(TagEnd::Item) => {
                let kind = kind_now(heading, quote, &lists, &mut marker);
                flush(&mut blocks, &mut text, &mut marks, &mut links, kind);
                marker = None;
            }
            Event::Start(Tag::Emphasis) => on |= ITALIC,
            Event::End(TagEnd::Emphasis) => on &= !ITALIC,
            Event::Start(Tag::Strong) => on |= BOLD,
            Event::End(TagEnd::Strong) => on &= !BOLD,
            Event::Start(Tag::Strikethrough) => on |= STRIKE,
            Event::End(TagEnd::Strikethrough) => on &= !STRIKE,
            Event::Start(Tag::Link { dest_url, .. }) => {
                on |= LINK;
                link = Some((text.len(), dest_url.to_string()));
            }
            Event::End(TagEnd::Link) => {
                on &= !LINK;
                if let Some((start, url)) = link.take() {
                    links.push((start..text.len(), url));
                }
            }
            Event::Text(t) => match &mut code {
                Some(body) => body.push_str(&t),
                None => push(&mut text, &mut marks, &t, on),
            },
            Event::Code(t) => push(&mut text, &mut marks, &t, on | CODE),
            Event::SoftBreak => push(&mut text, &mut marks, " ", on),
            Event::HardBreak => push(&mut text, &mut marks, "\n", on),
            Event::Rule => blocks.push(Block { kind: Kind::Rule, text: String::new(), marks: vec![], links: vec![] }),
            Event::Html(t) | Event::InlineHtml(t) => push(&mut text, &mut marks, &t, on),
            _ => {}
        }
    }
    let kind = kind_now(heading, quote, &lists, &mut marker);
    flush(&mut blocks, &mut text, &mut marks, &mut links, kind);
    blocks
}

fn push(text: &mut String, marks: &mut Vec<(Range<usize>, u8)>, s: &str, on: u8) {
    let start = text.len();
    text.push_str(s);
    if on != 0 && !s.is_empty() {
        marks.push((start..text.len(), on));
    }
}

impl Console {
    /// The runs of one block: its marks over the block's own color and weight.
    fn runs(&self, b: &Block, color: Hsla, bold: bool) -> Vec<TextRun> {
        let t = &self.theme;
        let base = TextRun {
            len: 0,
            font: Font { weight: if bold { FontWeight::BOLD } else { FontWeight::NORMAL }, ..font(t.mono) },
            color,
            background_color: None,
            underline: None,
            strikethrough: None,
        };
        let styled = |m: u8, len: usize| {
            let mut run = TextRun { len, ..base.clone() };
            if m & BOLD != 0 {
                run.font.weight = FontWeight::BOLD;
            }
            if m & ITALIC != 0 {
                run.font.style = FontStyle::Italic;
            }
            if m & CODE != 0 {
                run.color = t.info;
                run.background_color = Some(t.raised);
            }
            if m & LINK != 0 {
                run.color = t.accent;
                run.underline = Some(UnderlineStyle { thickness: px(1.), color: Some(alpha(t.accent, 0.6)), wavy: false });
            }
            if m & STRIKE != 0 {
                run.strikethrough = Some(StrikethroughStyle { thickness: px(1.), color: None });
            }
            run
        };
        let mut runs = Vec::new();
        let mut at = 0;
        for (range, m) in &b.marks {
            if range.start > at {
                runs.push(styled(0, range.start - at));
            }
            if range.end > range.start.max(at) {
                runs.push(styled(*m, range.end - range.start.max(at)));
            }
            at = at.max(range.end);
        }
        if b.text.len() > at {
            runs.push(styled(0, b.text.len() - at));
        }
        runs
    }

    /// Markdown drawn with the theme, every block selectable. Each entry's
    /// text is parsed once and kept; only an entry whose text changed (the
    /// one streaming) is parsed again.
    pub(super) fn markdown(&self, src: &str, pid: usize, ei: usize, cx: &mut gpui::Context<Self>) -> Div {
        let t = self.theme.clone();
        let key = format!("{pid}-{ei}");
        let blocks = {
            let mut cache = self.md_cache.borrow_mut();
            let stamp = (src.len() as u64) << 32 | u64::from(fnv(src));
            let hit = cache.get(&key).filter(|(s, _)| *s == stamp).map(|(_, b)| b.clone());
            match hit {
                Some(b) => b,
                None => {
                    let b = std::rc::Rc::new(parse(src));
                    cache.insert(key.clone(), (stamp, b.clone()));
                    b
                }
            }
        };
        let mut out = div().flex().flex_col().gap_2().min_w_0();
        for (bi, b) in blocks.iter().enumerate() {
            let (color, bold) = match &b.kind {
                Kind::Heading(_) => (t.text, true),
                Kind::Quote | Kind::Code => (t.muted, false),
                _ => (t.text, false),
            };
            let text = self.selectable(pid, ei, bi, b.text.clone().into(), self.runs(b, color, bold), b.links.clone(), cx);
            let block = match &b.kind {
                Kind::Paragraph => div().line_height(px(19.)).child(text),
                Kind::Heading(level) => div()
                    .pt_1()
                    .text_size(px(match level {
                        1 => 16.,
                        2 => 14.5,
                        _ => 13.,
                    }))
                    .child(text),
                Kind::Item { depth, marker } => div()
                    .flex()
                    .gap_2()
                    .pl(px(*depth as f32 * 16.))
                    .line_height(px(19.))
                    .child(div().flex_none().min_w(px(14.)).text_color(t.faint).child(marker.clone()))
                    .child(div().flex_1().min_w_0().child(text)),
                Kind::Quote => div().pl_3().border_l_2().border_color(t.border_strong).line_height(px(19.)).child(text),
                Kind::Code => div()
                    .group(gpui::SharedString::from(format!("entry-{key}-code-{bi}")))
                    .flex()
                    .gap_2()
                    .px_3()
                    .py_2()
                    .rounded(px(t.radius))
                    .bg(t.panel)
                    .border_1()
                    .border_color(t.border)
                    .text_size(px(12.))
                    .line_height(px(18.))
                    .child(div().flex_1().min_w_0().child(text))
                    .child(self.copy_button(format!("copy-{key}-code-{bi}"), b.text.clone().into(), cx)),
                Kind::Rule => div().h(px(1.)).my_1().bg(t.border),
            };
            out = out.child(block);
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn inline_marks_land_on_the_right_text() {
        let b = &parse("Proposed **“Add MIT LICENSE”** for a `coder`, *pending*.")[0];
        assert_eq!(b.kind, Kind::Paragraph);
        assert_eq!(b.text, "Proposed “Add MIT LICENSE” for a coder, pending.");
        let marked: Vec<(&str, u8)> = b.marks.iter().map(|(r, m)| (&b.text[r.clone()], *m)).collect();
        assert_eq!(marked, vec![("“Add MIT LICENSE”", BOLD), ("coder", CODE), ("pending", ITALIC)]);
    }

    #[test]
    fn blocks_in_order() {
        let src = "# Plan\n\nTwo steps:\n\n1. Read it\n2. Fix **it**\n   - nested\n\n> careful\n\n```sh\ngo test ./...\n```\n\n---\n";
        let kinds: Vec<Kind> = parse(src).into_iter().map(|b| b.kind).collect();
        assert_eq!(
            kinds,
            vec![
                Kind::Heading(1),
                Kind::Paragraph,
                Kind::Item { depth: 0, marker: "1.".into() },
                Kind::Item { depth: 0, marker: "2.".into() },
                Kind::Item { depth: 1, marker: "•".into() },
                Kind::Quote,
                Kind::Code,
                Kind::Rule,
            ]
        );
        let blocks = parse(src);
        assert_eq!(blocks[4].text, "nested");
        assert_eq!(blocks[6].text, "go test ./...");
    }

    #[test]
    fn plain_text_stays_plain() {
        let blocks = parse("Added it.\nVerified the file.");
        assert_eq!(blocks.len(), 1);
        assert_eq!(blocks[0].text, "Added it. Verified the file.");
        assert!(blocks[0].marks.is_empty());
    }
}

#[cfg(test)]
mod link_tests {
    use super::*;

    #[test]
    fn links_keep_their_address() {
        let b = &parse("See [the docs](https://example.com/docs) and https://example.org/x. Done")[0];
        let links: Vec<(&str, &str)> = b.links.iter().map(|(r, u)| (&b.text[r.clone()], u.as_str())).collect();
        assert_eq!(links, vec![("the docs", "https://example.com/docs"), ("https://example.org/x", "https://example.org/x")]);
        let marked: Vec<(&str, u8)> = b.marks.iter().map(|(r, m)| (&b.text[r.clone()], *m)).collect();
        assert_eq!(marked, vec![("the docs", LINK), ("https://example.org/x", LINK)]);
    }

    #[test]
    fn bare_links_stop_before_closing_punctuation() {
        let text = "(https://a.b/c), http://d.e/f?g=1.";
        let links = bare_links(text, &[]);
        let found: Vec<(&str, &str)> = links.iter().map(|(r, u)| (&text[r.clone()], u.as_str())).collect();
        assert_eq!(found, vec![("https://a.b/c", "https://a.b/c"), ("http://d.e/f?g=1", "http://d.e/f?g=1")]);
        assert_eq!(bare_links("http alone, https://", &[]), vec![]);
    }
}
