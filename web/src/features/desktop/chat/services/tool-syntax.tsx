import { memo, type ReactNode } from "react";

export type ToolSyntaxRole =
  | "plain"
  | "command"
  | "flag"
  | "path"
  | "string"
  | "number"
  | "keyword"
  | "operator"
  | "comment"
  | "function"
  | "type";

interface ToolSyntaxSpan {
  text: string;
  role: ToolSyntaxRole;
}

interface ToolSyntaxLineProps {
  text: string;
  language?: string;
  shell?: boolean;
  className?: string;
}

const MAX_HIGHLIGHT_CHARS = 1200;

const CODE_KEYWORDS = new Set([
  "as", "async", "await", "break", "case", "catch", "class", "const", "continue", "default", "defer", "delete",
  "do", "else", "enum", "export", "extends", "fallthrough", "false", "finally", "fn", "for", "from", "func", "function",
  "go", "if", "import", "in", "interface", "let", "map", "match", "module", "new", "nil", "null", "package", "private",
  "protected", "public", "range", "return", "select", "static", "struct", "switch", "this", "throw", "true", "try", "type",
  "undefined", "use", "var", "while", "yield",
]);

const TYPE_KEYWORDS = new Set([
  "any", "bool", "boolean", "byte", "char", "double", "error", "float", "float32", "float64", "int", "int8", "int16",
  "int32", "int64", "number", "object", "rune", "string", "symbol", "uint", "uint8", "uint16", "uint32", "uint64", "unknown", "void",
]);

const SHELL_KEYWORDS = new Set([
  "case", "do", "done", "elif", "else", "esac", "fi", "for", "function", "if", "in", "select", "then", "until", "while",
]);

const SHELL_BUILTINS = new Set([
  "awk", "cat", "cd", "chmod", "chown", "cp", "curl", "echo", "find", "git", "go", "grep", "jq", "ls", "make", "mkdir",
  "mv", "node", "npm", "pnpm", "rm", "sed", "ssh", "sudo", "tar", "touch", "yarn",
]);

function normalizeLanguage(language: string | undefined): string {
  const lang = (language ?? "").trim().toLowerCase();
  switch (lang) {
    case "golang":
      return "go";
    case "javascript":
    case "jsx":
    case "mjs":
    case "cjs":
      return "js";
    case "typescript":
    case "tsx":
      return "ts";
    case "py":
      return "python";
    case "rs":
      return "rust";
    case "shell":
    case "sh":
    case "zsh":
      return "bash";
    case "yml":
      return "yaml";
    default:
      return lang;
  }
}

export function inferToolSyntaxLanguage(path: string | null | undefined): string {
  const clean = (path ?? "").split(/[?#]/, 1)[0].trim().toLowerCase();
  const file = clean.split(/[\\/]/).pop() ?? clean;
  if (!file) return "";
  if (["dockerfile", "makefile", "justfile", "gemfile", "rakefile"].includes(file)) return file;
  const ext = file.includes(".") ? file.slice(file.lastIndexOf(".") + 1) : "";
  switch (ext) {
    case "go": return "go";
    case "ts": case "tsx": return "ts";
    case "js": case "jsx": case "mjs": case "cjs": return "js";
    case "json": return "json";
    case "md": case "mdx": return "markdown";
    case "css": case "scss": case "sass": case "less": return "css";
    case "html": case "xml": case "svg": return "html";
    case "py": return "python";
    case "rs": return "rust";
    case "rb": return "ruby";
    case "java": return "java";
    case "c": case "h": return "c";
    case "cc": case "cpp": case "cxx": case "hpp": case "hxx": return "cpp";
    case "sh": case "bash": case "zsh": return "bash";
    case "yaml": case "yml": return "yaml";
    case "toml": return "toml";
    case "sql": return "sql";
    default: return "";
  }
}

export function pathFromToolSummary(summary: string): string {
  const trimmed = summary.trim();
  const match = trimmed.match(/^(?:read|write|append|edit)\s+([^\s(]+)/i);
  return match?.[1] ?? "";
}

function looksLikePathToken(value: string): boolean {
  const token = value.replace(/^["'({[]+|["')},;\]]+$/g, "");
  return /^(\.{1,2}\/|\/|~\/|[\w.-]+\/)[\w./@+-]+$/.test(token) || /\.[a-z0-9]{1,8}(?::\d+)?$/i.test(token);
}

function appendSpan(out: ToolSyntaxSpan[], text: string, role: ToolSyntaxRole) {
  if (!text) return;
  const last = out[out.length - 1];
  if (last?.role === role) {
    last.text += text;
  } else {
    out.push({ text, role });
  }
}

function classifyShellWord(word: string, index: number): ToolSyntaxRole {
  const clean = word.replace(/^[`$({[]+|[`),;\]]+$/g, "");
  if (clean.startsWith("#")) return "comment";
  if (clean.startsWith("-")) return "flag";
  if (/^(['"]).*\1$/.test(clean)) return "string";
  if (/^\$[A-Za-z_][\w]*$/.test(clean)) return "keyword";
  if (/^\d+(?:\.\d+)?$/.test(clean)) return "number";
  if (looksLikePathToken(clean)) return "path";
  if (SHELL_KEYWORDS.has(clean)) return "keyword";
  if (index === 0 || SHELL_BUILTINS.has(clean)) return "command";
  return "plain";
}

function highlightShell(text: string): ToolSyntaxSpan[] {
  const spans: ToolSyntaxSpan[] = [];
  const parts = text.match(/\s+|\S+/g) ?? [];
  let wordIndex = 0;
  let inComment = false;
  for (const part of parts) {
    if (/^\s+$/.test(part)) {
      appendSpan(spans, part, inComment ? "comment" : "plain");
      continue;
    }
    if (inComment || part.startsWith("#")) {
      inComment = true;
      appendSpan(spans, part, "comment");
      continue;
    }
    appendSpan(spans, part, classifyShellWord(part, wordIndex));
    wordIndex += 1;
  }
  return spans;
}

function highlightCode(text: string, language: string): ToolSyntaxSpan[] {
  const spans: ToolSyntaxSpan[] = [];
  const isMarkup = language === "html" || language === "xml" || language === "svg";
  const isConfig = language === "json" || language === "yaml" || language === "toml";
  const pattern = /(\/\/.*$|#.*$|\/\*.*?\*\/|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`(?:\\.|[^`\\])*`|\b\d+(?:\.\d+)?\b|[A-Za-z_$][\w$-]*|[{}()[\];,.<>:+\-*\/%=&|!?]+|\s+|.)/g;
  let match: RegExpExecArray | null;
  let previousNonSpace = "";
  while ((match = pattern.exec(text)) !== null) {
    const token = match[0];
    let role: ToolSyntaxRole = "plain";
    if (/^\s+$/.test(token)) role = "plain";
    else if (token.startsWith("//") || token.startsWith("#") || token.startsWith("/*")) role = "comment";
    else if (/^["'`]/.test(token)) role = "string";
    else if (/^\d/.test(token)) role = "number";
    else if (/^[{}()[\];,.<>:+\-*\/%=&|!?]+$/.test(token)) role = "operator";
    else if (looksLikePathToken(token)) role = "path";
    else if (TYPE_KEYWORDS.has(token)) role = "type";
    else if (CODE_KEYWORDS.has(token) || (isConfig && /^(true|false|null)$/.test(token))) role = "keyword";
    else if (isMarkup && /^[A-Za-z][\w-]*$/.test(token) && previousNonSpace === "<") role = "type";
    else if (/^[A-Za-z_$][\w$-]*$/.test(token) && text.slice(pattern.lastIndex).trimStart().startsWith("(")) role = "function";
    appendSpan(spans, token, role);
    if (!/^\s+$/.test(token)) previousNonSpace = token;
  }
  return spans;
}

function roleClassName(role: ToolSyntaxRole): string {
  switch (role) {
    case "command": return "text-[var(--app-code-function)]";
    case "flag": return "text-[var(--app-code-keyword)]";
    case "path": return "text-[var(--app-code-path)]";
    case "string": return "text-[var(--app-code-string)]";
    case "number": return "text-[var(--app-code-number)]";
    case "keyword": return "text-[var(--app-code-keyword)]";
    case "operator": return "text-[var(--app-code-operator)]";
    case "comment": return "text-[var(--app-code-comment)]";
    case "function": return "text-[var(--app-code-function)]";
    case "type": return "text-[var(--app-code-type)]";
    default: return "text-[var(--app-code-text)]";
  }
}

function syntaxSpans(text: string, language?: string, shell?: boolean): ToolSyntaxSpan[] {
  if (!text) return [];
  if (text.length > MAX_HIGHLIGHT_CHARS) return [{ text, role: "plain" }];
  const lang = normalizeLanguage(language);
  if (shell || lang === "bash" || lang === "shell") return highlightShell(text);
  if (!lang) return highlightCode(text, "");
  return highlightCode(text, lang);
}

function renderSyntaxSpans(spans: ToolSyntaxSpan[]): ReactNode[] {
  return spans.map((span, index) => (
    <span key={`${index}:${span.role}`} className={roleClassName(span.role)}>
      {span.text}
    </span>
  ));
}

function ToolSyntaxLineInner({ text, language, shell, className }: ToolSyntaxLineProps) {
  const spans = syntaxSpans(text, language, shell);
  return <span className={className}>{renderSyntaxSpans(spans)}</span>;
}

export const ToolSyntaxLine = memo(ToolSyntaxLineInner);
