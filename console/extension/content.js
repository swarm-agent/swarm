// Swarm Browser, in a leased tab. Reads what the page shows (visible text and
// numbered elements to act on), clicks and types by number, and shows the
// banner. It never reads password fields, hides what looks like a key or
// token, counts hidden text instead of reading it, and holds an action that
// looks critical (paying, publishing, keys, account settings) until the
// person confirmed it, unless the lease is full.

(() => {
  if (globalThis.__swarmPage) return;
  globalThis.__swarmPage = true;

  // The result must stay under Swarm's 64KB client result limit.
  const TEXT_KEEP = 40000;
  const REFS = new Map();

  const SECRET = /\b(?:sk|rk|pk)[-_][A-Za-z0-9_-]{16,}|\bghp_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}|\bswk_[A-Za-z0-9_-]{8,}|\bAKIA[0-9A-Z]{16}\b|\bxox[baprs]-[A-Za-z0-9-]{10,}|\beyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}|\bAIza[0-9A-Za-z_-]{30,}/g;
  const SECRET_FIELD = /pass|pwd|token|secret|api[-_ ]?key|cvc|cvv|card|otp|code|pin/i;
  const CRITICAL = /\b(pay|payment|purchase|buy|checkout|place order|confirm order|order now|subscribe|donate|publish|deploy|release|merge|delete|remove|destroy|erase|transfer|send money|withdraw|api key|access token|secret|password|passcode|2fa|two-factor|verification code|authorize|allow access|grant access|sign in with|continue with|connect account|link account|revoke|close account|billing|card number|invite|permissions|share|make public)\b/i;
  const CRITICAL_PATH = /(settings|account|billing|payment|checkout|cart|keys|tokens|credentials|oauth|authorize|consent|login|signin|sign-in|signup|password|security)/i;

  function visible(el) {
    if (!(el instanceof Element)) return false;
    if (el.closest("[aria-hidden='true']")) return false;
    if (typeof el.checkVisibility === "function" && !el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) return false;
    const r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return false;
    // Parked far off the page: hidden to the person, so hidden here.
    if (r.right < -50 || r.bottom < -50 || r.left > Math.max(innerWidth, document.documentElement.scrollWidth) + 50) return false;
    const cs = getComputedStyle(el);
    if (parseFloat(cs.fontSize) < 1) return false;
    if (cs.color === cs.backgroundColor && cs.backgroundColor !== "rgba(0, 0, 0, 0)") return false;
    return true;
  }

  function redact(text) {
    return text.replace(SECRET, "[secret hidden]");
  }

  function clean(text) {
    return redact(String(text || "").replace(/\s+/g, " ").trim());
  }

  function isInteractive(el) {
    const tag = el.tagName;
    if (tag === "A" && el.hasAttribute("href")) return true;
    if (tag === "BUTTON" || tag === "SELECT" || tag === "TEXTAREA" || tag === "SUMMARY") return true;
    if (tag === "INPUT" && el.type !== "hidden") return true;
    if (el.isContentEditable) return true;
    const role = el.getAttribute("role");
    if (role && /^(button|link|textbox|checkbox|radio|tab|menuitem|option|switch|combobox|searchbox)$/.test(role)) return true;
    if ((tag === "DIV" || tag === "SPAN") && el.tabIndex >= 0 && (role || el.hasAttribute("onclick"))) return true;
    return false;
  }

  function label(el) {
    const tag = el.tagName.toLowerCase();
    let kind = tag;
    if (tag === "input") kind = "input:" + (el.type || "text");
    if (tag === "a") kind = "link";
    const parts = [];
    const aria = el.getAttribute("aria-label");
    if (aria) parts.push(clean(aria));
    if (tag === "input" || tag === "textarea" || tag === "select") {
      const id = el.id;
      const lab = (id && document.querySelector("label[for='" + CSS.escape(id) + "']")) || el.closest("label");
      if (lab) parts.push(clean(lab.textContent));
      if (el.placeholder) parts.push("placeholder: " + clean(el.placeholder));
      if (el.name) parts.push("name: " + clean(el.name));
      if (tag !== "select" && el.type !== "password" && !SECRET_FIELD.test(el.name + " " + el.id + " " + (el.autocomplete || ""))) {
        if (el.value) parts.push("value: " + clean(el.value).slice(0, 200));
      } else if (el.value) {
        parts.push("value: [not read]");
      }
      if (tag === "select") {
        const opts = Array.from(el.options).slice(0, 30).map((o) => clean(o.textContent)).filter(Boolean);
        parts.push("options: " + opts.join(" | "));
        if (el.selectedIndex >= 0) parts.push("selected: " + clean(el.options[el.selectedIndex].textContent));
      }
      if (el.type === "checkbox" || el.type === "radio") parts.push(el.checked ? "checked" : "unchecked");
    } else {
      const text = clean(el.innerText || el.textContent);
      if (text) parts.push(text.slice(0, 200));
      if (tag === "a") {
        const href = el.getAttribute("href") || "";
        if (href && !href.startsWith("javascript:")) parts.push("→ " + redact(el.href).slice(0, 200));
      }
      const title = el.getAttribute("title");
      if (!text && title) parts.push(clean(title));
    }
    return { kind, text: parts.join(" · ") };
  }

  // The page as the person sees it: text in order, interactive elements
  // numbered inline as [n], and how much text was hidden.
  function snapshot(query) {
    REFS.clear();
    const lines = [];
    let hiddenChars = 0;
    let n = 0;
    const seen = new Set();
    const walker = document.createTreeWalker(document.body || document.documentElement, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT, {
      acceptNode(node) {
        if (node.nodeType === Node.ELEMENT_NODE) {
          const tag = node.tagName;
          if (tag === "SCRIPT" || tag === "STYLE" || tag === "NOSCRIPT" || tag === "TEMPLATE" || tag === "SVG" || node.id === "swarm-browser-banner") return NodeFilter.FILTER_REJECT;
          if (!visible(node)) {
            hiddenChars += (node.textContent || "").replace(/\s+/g, " ").trim().length;
            return NodeFilter.FILTER_REJECT;
          }
          return NodeFilter.FILTER_ACCEPT;
        }
        return NodeFilter.FILTER_ACCEPT;
      },
    });
    let node;
    while ((node = walker.nextNode())) {
      if (node.nodeType === Node.ELEMENT_NODE) {
        if (isInteractive(node) && !seen.has(node)) {
          seen.add(node);
          n += 1;
          REFS.set(n, node);
          const l = label(node);
          lines.push("[" + n + "] " + l.kind + (l.text ? ": " + l.text : ""));
        }
        continue;
      }
      const parent = node.parentElement;
      if (!parent || seen.has(parent) || parent.closest("a[href], button, select, textarea, summary, [contenteditable='true']")) continue;
      const text = clean(node.nodeValue);
      if (text) lines.push(text);
    }
    let text = lines.join("\n");
    let truncated = false;
    if (query) {
      const q = query.toLowerCase();
      const hits = lines.filter((l) => l.toLowerCase().includes(q));
      text = hits.join("\n");
      return { found: hits.length, text, elements: n, hidden_text_chars: hiddenChars };
    }
    if (text.length > TEXT_KEEP) {
      text = text.slice(0, TEXT_KEEP);
      truncated = true;
    }
    return { text, elements: n, hidden_text_chars: hiddenChars, truncated };
  }

  // Why an action on this element waits for the person, or null.
  function critical(el, typed) {
    if (el.tagName === "INPUT" && el.type === "password") return "typing into a password field";
    if (el.tagName === "INPUT" && SECRET_FIELD.test(el.name + " " + el.id + " " + (el.autocomplete || "") + " " + (el.placeholder || ""))) return "typing into a field for a secret or a code";
    const words = clean((el.innerText || el.textContent || "") + " " + (el.getAttribute("aria-label") || "") + " " + (el.value || "") + " " + (el.getAttribute("title") || ""));
    const m = words.match(CRITICAL);
    if (m) return "the element says \"" + m[0] + "\"";
    if (typed && SECRET.test(typed)) return "the text to type looks like a key or token";
    const form = el.closest("form");
    if (form) {
      const fm = clean(form.innerText || "").match(CRITICAL);
      if (fm && (el.tagName === "BUTTON" || (el.tagName === "INPUT" && (el.type === "submit" || el.type === "button")))) return "it submits a form about \"" + fm[0] + "\"";
    }
    if (CRITICAL_PATH.test(location.pathname + location.search)) return "this page is about " + location.pathname.match(CRITICAL_PATH)[0];
    return null;
  }

  function refOf(n) {
    const el = REFS.get(Number(n));
    if (!el) return { error: "No element [" + n + "]: read the page first, then use a number from it." };
    if (!el.isConnected) return { error: "Element [" + n + "] is gone; read the page again." };
    return { el };
  }

  function click(msg) {
    const got = refOf(msg.ref);
    if (got.error) return got;
    const el = got.el;
    if (!msg.confirmed && msg.level !== "full") {
      const why = critical(el, "");
      if (why) return { needs_confirm: "Click [" + msg.ref + "] (" + label(el).text.slice(0, 120) + "): " + why + "." };
    }
    el.scrollIntoView({ block: "center", inline: "nearest" });
    el.focus();
    el.click();
    return { did: "clicked [" + msg.ref + "] " + label(el).text.slice(0, 120) };
  }

  function type(msg) {
    const got = refOf(msg.ref);
    if (got.error) return got;
    const el = got.el;
    if (!msg.confirmed && msg.level !== "full") {
      const why = critical(el, msg.text);
      if (why) return { needs_confirm: "Type into [" + msg.ref + "] (" + label(el).text.slice(0, 120) + "): " + why + "." };
    }
    const editable = el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable;
    if (!editable) return { error: "Element [" + msg.ref + "] takes no text." };
    el.scrollIntoView({ block: "center", inline: "nearest" });
    el.focus();
    if (el.isContentEditable) {
      el.textContent = msg.text;
      el.dispatchEvent(new InputEvent("input", { bubbles: true, data: msg.text, inputType: "insertText" }));
    } else {
      const setter = Object.getOwnPropertyDescriptor(el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype, "value");
      if (setter && setter.set) setter.set.call(el, msg.text); else el.value = msg.text;
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    }
    let did = "typed into [" + msg.ref + "]";
    if (msg.enter) {
      for (const kind of ["keydown", "keypress", "keyup"]) {
        el.dispatchEvent(new KeyboardEvent(kind, { key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true }));
      }
      const form = el.form || el.closest("form");
      if (form && typeof form.requestSubmit === "function") form.requestSubmit();
      did += " and pressed enter";
    }
    return { did };
  }

  function banner(lease) {
    let el = document.getElementById("swarm-browser-banner");
    if (!lease) {
      if (el) el.remove();
      return { ok: true };
    }
    if (!el) {
      el = document.createElement("div");
      el.id = "swarm-browser-banner";
      el.setAttribute("aria-hidden", "true");
      el.style.cssText = "position:fixed;top:0;left:0;right:0;z-index:2147483647;background:#1f6feb;color:#fff;font:13px/1.4 system-ui,sans-serif;padding:6px 12px;display:flex;align-items:center;gap:12px;box-shadow:0 1px 4px rgba(0,0,0,.3)";
      const text = document.createElement("span");
      text.id = "swarm-browser-banner-text";
      text.style.flex = "1";
      const stop = document.createElement("button");
      stop.textContent = "Stop Swarm";
      stop.style.cssText = "background:#fff;color:#1f6feb;border:0;border-radius:4px;padding:3px 10px;font:inherit;font-weight:600;cursor:pointer";
      stop.addEventListener("click", () => chrome.runtime.sendMessage({ type: "stop" }));
      el.append(text, stop);
      (document.body || document.documentElement).append(el);
    }
    const ends = new Date(lease.until);
    document.getElementById("swarm-browser-banner-text").textContent = "Swarm is using this tab (" + lease.level + ") until " + ends.getHours().toString().padStart(2, "0") + ":" + ends.getMinutes().toString().padStart(2, "0") + ". Pages here are read by an AI agent.";
    return { ok: true };
  }

  chrome.runtime.onMessage.addListener((msg, sender, reply) => {
    try {
      switch (msg && msg.cmd) {
        case "read": reply(snapshot(msg.query)); break;
        case "click": reply(click(msg)); break;
        case "type": reply(type(msg)); break;
        case "banner": reply(banner(msg.lease)); break;
        default: reply({ error: "unknown command" });
      }
    } catch (e) {
      reply({ error: String(e && e.message || e) });
    }
    return true;
  });
})();
