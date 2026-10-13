import {
  applyProgress,
  endProgress,
  currentStep,
  earlierSteps,
  formatElapsed,
  NOTE_EVERY_MS,
  pruneProgress,
  QUIET_AFTER_MS,
  quietNote,
  stepLabel,
  turnStart,
  type JobStep,
} from "./jobProgress.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function eq(got: unknown, want: unknown, what: string): void {
  if (JSON.stringify(got) !== JSON.stringify(want)) throw new Error(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

const step = (kind: JobStep["kind"], title: string | undefined, atMs: number): JobStep => ({ kind, ...(title ? { title } : {}), atMs });

// Folding updates: a new step moves changedAtMs; a repeat keeps it.
{
  const a = applyProgress({}, { jobId: "j1", startedAtMs: 100, activity: [step("tool", "Read: deck.pptx", 110)] }, 1_000);
  eq(a.j1?.changedAtMs, 1_000, "first sighting");
  const again = applyProgress(a, { jobId: "j1", startedAtMs: 100, activity: [step("tool", "Read: deck.pptx", 110)] }, 5_000);
  eq(again.j1?.changedAtMs, 1_000, "a repeated update does not wake a quiet turn");
  const more = applyProgress(again, { jobId: "j1", startedAtMs: 100, activity: [step("tool", "Read: deck.pptx", 110), step("thinking", undefined, 120)] }, 6_000);
  eq(more.j1?.changedAtMs, 6_000, "a new step does");
  eq(more.j1?.steps.length, 2, "steps kept");
  const bogus = applyProgress({}, { jobId: "j2", activity: [{ kind: "nope" as JobStep["kind"], atMs: 1 }] }, 1);
  eq(bogus.j2?.steps.length, 0, "unknown kinds dropped");
  eq(Object.keys(applyProgress(a, { jobId: "" }, 1)), ["j1"], "an update without a job is ignored");
}

// Nothing is kept past the job.
{
  const m = applyProgress(applyProgress({}, { jobId: "live" }, 1), { jobId: "done" }, 1);
  eq(Object.keys(pruneProgress(m, [{ jobId: "live", status: "reflowing" }, { jobId: "done", status: "replied" }])), ["live"], "ended jobs pruned");
  eq(Object.keys(pruneProgress(m, [])), [], "unknown jobs pruned");
  eq(Object.keys(endProgress(m, { jobId: "done", status: "replied" })), ["live"], "an ended job's progress goes, others stay");
  eq(endProgress(m, { jobId: "live", status: "running" }), m, "a live update keeps the map");
}

// Elapsed time and where it starts.
eq(formatElapsed(8_400), "8s", "seconds");
eq(formatElapsed(185_000), "3m 05s", "minutes");
eq(formatElapsed(3_720_000), "1h 02m", "hours");
eq(formatElapsed(-5), "0s", "never negative");
eq(turnStart({ createdAtMs: 1, claimedAtMs: 2, startedAtMs: 3 }, { jobId: "j", startedAtMs: 4, steps: [], changedAtMs: 0 }), 4, "the turn's own start first");
eq(turnStart({ createdAtMs: 1, claimedAtMs: 2 }), 2, "else when a bridge took it");
eq(turnStart({ createdAtMs: 1 }), 1, "else when it was asked");

// The steps shown: the newest, then up to five before it, newest first.
{
  const steps = Array.from({ length: 8 }, (_, i) => step("tool", `s${i}`, i));
  const p = { jobId: "j", steps, changedAtMs: 0 };
  eq(currentStep(p)?.title, "s7", "current = newest");
  eq(earlierSteps(p).map((s) => s.title), ["s6", "s5", "s4", "s3", "s2"], "five earlier, newest first");
  eq(earlierSteps({ jobId: "j", steps: [steps[0]!], changedAtMs: 0 }), [], "one step: nothing earlier");
  eq(currentStep(undefined), undefined, "no progress yet");
}
eq(stepLabel(step("thinking", undefined, 0)), "Thinking", "an untitled thought");
eq(stepLabel(step("text", "  ", 0)), "Writing", "a blank title reads as its kind");
eq(stepLabel(step("tool", "Bash: ls", 0)), "Bash: ls", "a title as given");

// Quiet notes: none while steps come in; then rotating every NOTE_EVERY_MS,
// naming the agent by provider and model, never its core.
{
  const job = { provider: "claude-code", model: "opus", createdAtMs: 0, startedAtMs: 0 };
  const p = { jobId: "j", steps: [step("tool", "x", 0)], changedAtMs: 40_000 };
  eq(quietNote(job, p, 40_000 + QUIET_AFTER_MS - 1), null, "not yet quiet");
  const at = (k: number) => quietNote(job, p, 40_000 + QUIET_AFTER_MS + k * NOTE_EVERY_MS);
  eq(at(0), "Complex tasks can take several minutes", "first note");
  eq(at(1), "You can leave this room; you'll be notified when it's done", "second note");
  eq(at(2), "claude-code · opus", "the agent as members know it");
  eq(at(3), "Working for 1 min", "how long so far");
  eq(at(4), "Complex tasks can take several minutes", "rotates");
  eq(quietNote({ provider: "codex", createdAtMs: 0 }, undefined, QUIET_AFTER_MS + 2 * NOTE_EVERY_MS), "codex is working", "no model: no dangling separator");
  eq(quietNote({ provider: "codex", createdAtMs: 0 }, undefined, QUIET_AFTER_MS + 3 * NOTE_EVERY_MS), "Still working", "under a minute");
}
