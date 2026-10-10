import { codeLanguage, isCodeBlock } from "./markdownCode.ts";

function assert(cond: boolean, message: string): void {
  if (!cond) throw new Error(message);
}

assert(isCodeBlock("┌──┐\n│ A │\n└──┘\n", undefined), "a fence without a language is a block (ASCII diagram)");
assert(isCodeBlock("one line\n", undefined), "a one-line fence is still a block");
assert(isCodeBlock("x", "language-ts"), "a language means a block");
assert(!isCodeBlock("npm install", undefined), "inline code stays inline");
assert(codeLanguage("language-mermaid") === "mermaid" && codeLanguage(undefined) === null, "language parsing");
