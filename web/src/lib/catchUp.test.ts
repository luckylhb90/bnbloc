import { catchUpAsk, catchUpLines, catchUpOf, translateAsk } from "./catchUp.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const feed = [
  { seq: 1, author: "jack", text: "old" },
  { seq: 4, author: "jack", text: "ship friday?" },
  { seq: 5, author: "maya", text: "" },
  { seq: 6, author: "erin", text: "yes, @maya owns the notes" },
];
eq(catchUpLines(feed, null), [], "nothing unread, nothing to catch up on");
eq(catchUpLines(feed, 4).map((l) => l.seq), [4, 6], "from the first unread, empty messages left out");
const ask = catchUpAsk("maya", "release", catchUpLines(feed, 4));
eq(ask.includes("[4] jack: ship friday?") && ask.includes("[6] erin") && !ask.includes("[1] jack: old"), true, "the unread messages in the ask");
const long = Array.from({ length: 50 }, (_, i) => ({ seq: i + 1, author: "jack", text: "x".repeat(400) }));
const cut = catchUpAsk("maya", "c", long, 3000);
eq(cut.length <= 3000 + 100 && cut.includes("earlier messages left out") && cut.includes("[50]"), true, "the newest kept within the budget");
eq(catchUpOf({ summary: "s", decided: ["a"], open: [], forYou: ["b"] }), { summary: "s", decided: ["a"], open: [], forYou: ["b"] }, "the answer read");
eq(catchUpOf({}), { summary: "", decided: [], open: [], forYou: [] }, "a missing answer is empty");
eq(translateAsk("zh-CN", "hello").startsWith("Language: zh-CN\nMessage:\nhello"), true, "translate names the language");
console.log("catchUp tests passed");
