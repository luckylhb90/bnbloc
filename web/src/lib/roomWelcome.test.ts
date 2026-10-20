import { cleanQuickPrompts, roomStartOf } from "./roomWelcome.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// An empty room shows its welcome and the quick prompts that have text;
// a room with neither (or a preview, which carries none) shows nothing.
{
  eq(
    roomStartOf({ welcome: " Ask me. ", quickPrompts: [{ title: "Recap", prompt: "Recap the week" }, { title: "Empty", prompt: "  " }] }),
    { welcome: "Ask me.", prompts: [{ title: "Recap", prompt: "Recap the week" }] },
    "the welcome and the prompts with text",
  );
  eq(roomStartOf({ welcome: "", quickPrompts: [{ title: "Recap", prompt: "Recap" }] })?.welcome, "", "quick prompts alone");
  eq(roomStartOf({ welcome: " ", quickPrompts: [] }), null, "nothing to show");
  eq(roomStartOf({}), null, "a preview");
  eq(roomStartOf(null), null, "no room yet");
}

// The settings send trimmed rows, without the wholly empty ones; a half
// row stays for the server to name what it lacks.
{
  eq(
    cleanQuickPrompts([{ title: " Recap ", prompt: " Recap the week " }, { title: " ", prompt: "" }, { title: "Half", prompt: "" }]),
    [{ title: "Recap", prompt: "Recap the week" }, { title: "Half", prompt: "" }],
    "trimmed, empty rows dropped",
  );
}
