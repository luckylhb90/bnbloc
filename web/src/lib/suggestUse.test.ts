import {
  candidatesOf,
  dismissSuggestion,
  dismissedKeys,
  MIN_DRAFT_CHARS,
  suggestUse,
  suggestionAction,
  suggestionText,
  tokens,
  type UseCandidate,
} from "./suggestUse.ts";
import type { ContentComponent, MemberComponent } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const content = (kind: "expert" | "skill", name: string, label: string, tags: string[], description: string, requires: string[] = []): ContentComponent =>
  ({ id: `cc_${name}`, kind, name, displayName: label, tags, description, requires: { skills: requires }, version: "1.0.0" }) as unknown as ContentComponent;
const mcp = (name: string, description: string, installed: boolean): MemberComponent => ({ kind: "mcp", name, description, installed }) as unknown as MemberComponent;

// A slice of a real organization's Library: the default content's English
// descriptions, the Chinese ones that organization's own experts carry, and an
// organization's localized tags on the App builder.
const catalog = candidatesOf({
  contentComponents: [
    content("expert", "app-builder", "App builder", ["apps", "pages", "ai", "polls", "forms", "网页", "页面", "投票", "问卷", "表单", "分享", "助手"],
      "Builds web pages and small apps with you that people open from a link: a landing page, an assistant that answers with the organization's AI, a poll, survey, sign-up sheet, form or board. bnbloc publishes and shares them, keeps their data, and you can ask for a report on what people entered.", ["interactive-apps@^1"]),
    content("skill", "interactive-apps", "Interactive apps", ["apps", "pages", "ai", "share", "polls", "forms", "网页", "页面", "投票", "问卷", "表单", "分享", "助手"],
      "Builds any page people open from a link (landing pages, assistants, polls, forms, boards, games) with the app SDK: bnbloc publishes and shares it, keeps its data and answers its AI calls with the organization's model, with no server or key."),
    content("expert", "weekly-report-writer", "周报助手", ["weekly report", "status", "writing"], "把零散的工作记录、提交记录、任务列表整理成结论先行的周报或月报：突出结果和影响，写清风险与求助，按读者（主管、团队、跨部门）调整详略。"),
    content("expert", "contract-reviewer", "合同条款审查助手", ["contract", "legal", "review", "risk"], "站在我方立场逐条审查商务合同：识别风险条款、缺失条款和对我方不利的措辞，按风险分级并给出修改建议与谈判要点。输出供业务参考，不构成法律意见。"),
    content("expert", "sql-helper", "SQL 与数据查询助手", ["sql", "data", "query"], "把业务问题翻译成正确、高效、可读的 SQL：先确认口径和表结构，再写查询、解释逻辑、提示性能与数据陷阱；也能解释、优化和排查已有 SQL。"),
    content("expert", "meeting-minutes", "会议纪要专家", ["meeting", "minutes", "action items"], "把会议录音转写、速记或聊天记录整理成清晰的会议纪要：议题结论、决策及决策人、行动项（负责人与截止时间）、未决问题，并能生成发给参会人的跟进消息。"),
    content("expert", "incident-postmortem", "故障复盘专家", ["incident", "postmortem", "sre"], "把故障过程整理成无责复盘报告：时间线、影响评估、根因（多层原因）、做得好与不足、可跟踪的改进项。也可在故障进行中维护事件记录。", ["risk-rubric@^1"]),
    content("expert", "support-reply", "客户沟通专家", ["customer support", "reply", "communication"], "起草客户支持回复和事件通报：准确理解问题、共情而不卑微、给出明确的处理步骤与预期时间；也能写故障公告、进展更新和内部升级说明。"),
    content("expert", "acme-deck-designer", "Acme Brand Deck Designer", ["ppt", "slides", "brand", "acme", "presentation"], "Builds on-brand Acme decks in Chinese or English from the official template: conclusion-first outline, a ready-to-use .pptx, an automatic brand check."),
    content("expert", "tech-design-reviewer", "技术方案评审专家", ["architecture", "rfc", "design review"], "以架构师视角评审技术方案与 RFC：需求是否被正确理解、方案是否简单可演进、容量与可靠性、安全、上线与回滚。", ["risk-rubric@^1"]),
    content("expert", "code-reviewer", "代码评审专家", ["code review", "engineering", "quality"], "像资深工程师一样评审代码改动：先看正确性和安全，再看可维护性、性能和测试，按严重程度给出可直接落地的修改建议。"),
    content("expert", "prd-writer", "PRD 撰写专家", ["prd", "product", "requirements"], "把一个想法或一段需求描述写成可评审、可开发、可验收的 PRD：问题与目标、用户与场景、功能需求与优先级、指标。"),
    content("skill", "data-chart-spec", "数据图表与指标口径", ["data", "chart", "metrics"], "按问题选图表并写出可复现的图表规格，以及把指标口径定义清楚。数据分析、用户反馈洞察、周报等场景共用。"),
  ],
  components: [
    mcp("browser", "Drive a real browser: open pages, click, read what is on screen.", false),
    mcp("media-tools", "Make images, audio and video.", true),
  ],
});

const pick = (draft: string, opts?: Parameters<typeof suggestUse>[2]) => suggestUse(draft, catalog, opts)?.candidate.name ?? null;

// The candidates are the experts, the skill packs and the tools not yet added.
{
  assert(catalog.length === 14, `a candidate for every expert, skill pack and tool not added (${catalog.length})`);
  assert(catalog.some((c) => c.key === "mcp/browser") && !catalog.some((c) => c.key === "mcp/media-tools"), "an added tool is not a candidate");
  assert(catalog.find((c) => c.name === "app-builder")?.wears[0] === "interactive-apps", "an expert records the skill packs it wears, without their range");
}

// Requests that name what they need are placed, in either language.
{
  assert(pick("help me write a SQL query to count users per day") === "sql-helper", "SQL");
  assert(pick("Review this contract and list the risky clauses") === "contract-reviewer", "a contract");
  assert(pick("write a weekly report from these notes") === "weekly-report-writer", "a weekly report");
  assert(pick("summarize the meeting minutes and list action items") === "meeting-minutes", "meeting minutes");
  assert(pick("reply to this customer complaint politely") === "support-reply", "a customer reply");
  assert(pick("make a slide deck about the Q3 results") === "acme-deck-designer", "slides");
  assert(pick("we had an outage last night, write the postmortem") === "incident-postmortem", "a postmortem (one rare word of a candidate's name)");
  assert(pick("我要写一份合同审查，找出风险条款") === "contract-reviewer", "Chinese against Chinese descriptions");
  assert(pick("把这周的工作记录整理成周报") === "weekly-report-writer", "Chinese weekly report");
}

// The case that started this: a page with an assistant, sharing it, in both languages.
{
  const first =
    "build the main page similar to https://america.gov/ where there is a chat interface to ask and get access to all sort of the Acme AI products. how to build a safe and isolated LLM model";
  assert(pick(first) === "app-builder", "a page with an AI assistant goes to the App builder, not to an Acme expert the product name happens to match");
  assert(pick("how can I share this website to my cs-api team?") === "app-builder", "sharing a website");
  assert(pick("给我们团队做一个带AI助手的网页") === "app-builder", "Chinese: a page with an AI assistant (tags)");
  assert(pick("做一个投票页面，让大家选午餐") === "app-builder", "Chinese: a poll page");
  assert(pick("make a poll page for the team lunch with three options") === "app-builder", "poll and page");
  assert(pick("make a poll for team lunch with three options") === null, "one tag word alone is not enough");
}

// An expert that wears a skill pack stands for it; the skill pack alone is offered
// when no expert wearing it is left to offer.
{
  assert(pick("make a poll page for the team lunch with three options") === "app-builder", "the expert, not its skill pack");
  const noExpert = suggestUse("make a poll page for the team lunch with three options", catalog, { exclude: new Set(["expert/cc_app-builder"]) });
  assert(noExpert?.candidate.name === "interactive-apps", "the skill pack when its expert is excluded");
  const worn = suggestUse("make a poll page for the team lunch with three options", catalog, { exclude: new Set(["expert/cc_app-builder"]), worn: new Set(["interactive-apps"]) });
  assert(worn === null, "a skill pack an expert in effect already wears is not offered");
}

// Better silent than wrong.
{
  assert(pick("hello") === null, "a greeting");
  assert(pick("what is the weather today") === null, "nothing in the Library fits");
  assert(pick("improve the design, reference from america.gov website. modern and polished with well intuitive scrolling animation") === null, "one design word in a long request is not enough");
  assert(pick("search the web for competitors of our product") === null, "one generic word that is only a tag");
  assert(pick("") === null && pick("   ") === null, "an empty draft");
  assert(suggestUse("write a sql query for the contract review", catalog) === null, "two candidates with the same coverage are not told apart");
}

// What is already on or dismissed is never offered.
{
  assert(pick("help me write a SQL query to count users per day", { exclude: new Set(["expert/cc_sql-helper"]) }) === null, "an excluded candidate");
  assert(pick("use a real browser to open the site and click through the sign-in") === "browser", "a tool the member has not added");
  assert(pick("use a real browser to open the site and click through the sign-in", { exclude: new Set(["mcp/browser"]) }) !== "browser", "a dismissed tool");
}

// The row's words.
{
  const sql = catalog.find((c) => c.name === "sql-helper") as UseCandidate;
  const skill = catalog.find((c) => c.name === "interactive-apps") as UseCandidate;
  const tool = catalog.find((c) => c.name === "browser") as UseCandidate;
  assert(suggestionAction(sql) === "Use" && suggestionAction(skill) === "Turn on" && suggestionAction(tool) === "Add", "a button per kind");
  assert(suggestionText(sql) === "SQL 与数据查询助手 fits this request", "the sentence names the candidate");
  assert(MIN_DRAFT_CHARS > 0, "a request must say something before it is placed");
}

// Tokens: stop words, stems, synonyms and Chinese pairs.
{
  assert(tokens("Please build a website for the team").join(",") === "page,team", "filler dropped, website is page");
  assert(tokens("polls and surveys").join(",") === "poll,survey", "plurals");
  assert(tokens("做一个投票页面").includes("投票") && tokens("做一个投票页面").includes("页面") && !tokens("做一个投票页面").includes("一个"), "keyword pairs kept, 一个 dropped");
}

// Dismissals: per member key, a month, pruned.
{
  const data = new Map<string, string>();
  const storage = { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) };
  const now = 1_000_000;
  assert(dismissedKeys(storage, "k", now).size === 0, "nothing dismissed at first");
  dismissSuggestion(storage, "k", "expert/cc_a", now);
  dismissSuggestion(storage, "other", "expert/cc_b", now);
  assert(dismissedKeys(storage, "k", now + 1000).has("expert/cc_a") && !dismissedKeys(storage, "k", now).has("expert/cc_b"), "kept per key");
  assert(dismissedKeys(storage, "k", now + 29 * 86_400_000).has("expert/cc_a"), "quiet for a month");
  assert(!dismissedKeys(storage, "k", now + 31 * 86_400_000).has("expert/cc_a"), "suggested again after a month");
  data.set("bad", "{not json");
  assert(dismissedKeys(storage, "bad", now).size === 0, "an unreadable store is empty");
  assert(dismissedKeys(null, "k", now).size === 0, "no storage, nothing dismissed");
  dismissSuggestion(null, "k", "x", now);
}
