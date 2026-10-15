import { automateTaskSeed } from "./automate.ts";
import { recipientLabel } from "../../lib/schedules.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// An app's "Automate…" task names the app by title and id.
eq(automateTaskSeed({ artifactId: "artf_1", title: "Weekly report" }), 'For the app "Weekly report" (artf_1): ', "seed");
// An AI task is labelled AI in the schedules list.
eq(recipientLabel({ kind: "ai" }), "AI", "ai label");
eq(recipientLabel({ kind: "room" }), "room", "room label");
