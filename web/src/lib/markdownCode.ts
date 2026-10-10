// How the markdown renderer tells a code block from inline code. Pure,
// node-tested; markdown.tsx renders the result.

// codeLanguage is a fenced block's language ("language-ts" -> "ts").
export function codeLanguage(className: string | undefined): string | null {
  return /^language-([^\s]+)$/.exec(className ?? "")?.[1] ?? null;
}

// isCodeBlock: react-markdown (v9+) no longer says whether a <code> is inline,
// and a fence without a language carries no class either — treating those as
// inline code collapsed ASCII diagrams, logs and command output onto one line.
// CommonMark turns line endings inside inline code into spaces, while a
// fenced or indented block's text always ends with a newline, so a newline
// (or a language) means a block.
export function isCodeBlock(text: string, className: string | undefined): boolean {
  return text.includes("\n") || codeLanguage(className) !== null;
}
