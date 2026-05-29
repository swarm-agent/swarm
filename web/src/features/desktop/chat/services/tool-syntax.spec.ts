import { syntaxSpans } from "./tool-syntax";

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message);
  }
}

function testPlainModeDoesNotClassifyCodeKeywords(): void {
  const text = "> [ ] Revamp sidebar subagent hierarchy into clearer stacked groups · high for as if";
  const spans = syntaxSpans(text, "", false, true);

  assert(spans.length === 1, `expected one plain span, got ${spans.length}`);
  assert(spans[0]?.role === "plain", `expected plain span role, got ${spans[0]?.role}`);
  assert(spans[0]?.text === text, "plain span should preserve the original todo preview text");
}

function testUnknownLanguageKeepsStreamingTextPlain(): void {
  const text = "compacting full chat with memory agent and writing checkpoint";
  const spans = syntaxSpans(text, "");

  assert(spans.length === 1, `expected one plain span, got ${spans.length}: ${JSON.stringify(spans)}`);
  assert(spans[0]?.role === "plain", `expected unknown language to render plain, got ${spans[0]?.role}`);
  assert(spans[0]?.text === text, "plain unknown-language span should preserve streaming text");
}

function testBashOutputCanBeForcedPlain(): void {
  const text = "downloaded 12 files from cache and wrote output";
  const spans = syntaxSpans(text, "", false, true);

  assert(spans.length === 1, `expected one plain bash output span, got ${spans.length}: ${JSON.stringify(spans)}`);
  assert(spans[0]?.role === "plain", `expected bash output preview to render plain, got ${spans[0]?.role}`);
  assert(spans[0]?.text === text, "plain bash output span should preserve output text");
}

function testDefaultModeStillClassifiesCodeKeywords(): void {
  const spans = syntaxSpans("for item in list", "ts");

  assert(
    spans.some((span) => span.text === "for" && span.role === "keyword"),
    `expected code mode to keep keyword highlighting: ${JSON.stringify(spans)}`,
  );
}

function main(): void {
  testPlainModeDoesNotClassifyCodeKeywords();
  testUnknownLanguageKeepsStreamingTextPlain();
  testBashOutputCanBeForcedPlain();
  testDefaultModeStillClassifiesCodeKeywords();
  console.log("tool-syntax tests passed");
}

main();
