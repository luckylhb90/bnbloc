import { jobContentMissing, jobWearing } from "./agentJobLabels.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const diag = { expert: "Reviewer (v1.2.0)", roomExpert: "House style (v2.0.0)", contentMissing: ["lint-kit@^1", "", 7, "room expert Old style"] };
assert(jobContentMissing(diag).join("|") === "lint-kit@^1|room expert Old style", `missing: ${jobContentMissing(diag)}`);
assert(jobContentMissing({}).length === 0 && jobContentMissing(null).length === 0, "nothing missing");
assert(jobWearing(diag).join(" + ") === "House style (v2.0.0) + Reviewer (v1.2.0)", `wearing: ${jobWearing(diag)}`);
assert(jobWearing({ expert: "" }).length === 0, "an empty label is not worn");
