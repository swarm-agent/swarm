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

function testDefaultModeStillClassifiesCodeKeywords(): void {
  const spans = syntaxSpans("for item in list", "ts");

  assert(
    spans.some((span) => span.text === "for" && span.role === "keyword"),
    `expected code mode to keep keyword highlighting: ${JSON.stringify(spans)}`,
  );
}

function main(): void {
  testPlainModeDoesNotClassifyCodeKeywords();
  testDefaultModeStillClassifiesCodeKeywords();
  console.log("tool-syntax tests passed");
}

main();
