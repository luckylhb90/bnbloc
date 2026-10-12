import { jobWearing } from "./agentJobLabels.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// What a job wears comes from the job projection (wire.AgentJob): the room's
// expert, then the chat's own.
const worn = jobWearing({ expertName: "Reviewer", expertVersion: "1.2.0", roomExpertName: "House style", roomExpertVersion: "2.0.0" });
assert(worn.join(" + ") === "House style (v2.0.0) + Reviewer (v1.2.0)", `wearing: ${worn}`);
assert(jobWearing({ expertName: "" }).length === 0, "no expert is not worn");
assert(jobWearing({ expertName: "Reviewer" }).join() === "Reviewer", "a version-less label");
