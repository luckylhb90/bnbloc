// Local skills (Settings → Skills): the member's own skill folders, chosen for
// their agents' sandbox and their library. The host renders any component that
// serves `bnbloc.skills.provider/v1`; these are the contract's shapes and the
// pure edits the view makes to a provider's settings
// (docs/prd/2026-10-16-local-skills.md). The member's settings are echoed in
// the provider's state and sent back whole.

export interface PublishedSkill {
  readonly hash: string;
  readonly componentId: string;
  readonly version: string;
}

export interface AllowedFile {
  readonly file: string;
  readonly hash: string;
}

export interface SkillChoice {
  readonly vm: boolean;
  readonly borrowers: boolean;
  readonly published?: PublishedSkill;
  readonly allowed?: readonly AllowedFile[];
}

export interface SkillsSettings {
  readonly enabled: boolean;
  readonly sources: readonly string[];
  readonly agentDirs: readonly string[];
  readonly skills: Readonly<Record<string, SkillChoice>>;
  readonly policyFile?: string;
}

export interface SkillSource {
  readonly path: string;
  readonly error?: string;
  readonly truncated?: boolean;
}

export interface SkillRow {
  readonly name: string;
  readonly description: string;
  readonly source: string;
  readonly hasScripts: boolean;
  readonly files: number;
  readonly bytes: number;
  readonly eligible: boolean;
  readonly reason?: string;
  readonly publishProblem?: string;
  /** The file a content rule flagged; allowable unless it was flagged by name. */
  readonly finding?: { readonly file: string; readonly hash: string; readonly allowable: boolean };
  readonly allowed?: readonly string[];
  readonly hiddenBy?: string;
  readonly hash?: string;
  readonly choice: SkillChoice;
  readonly changed?: boolean;
}

export interface SkillsState {
  readonly contract: string;
  readonly settings: SkillsSettings;
  readonly enabled: boolean;
  readonly sources: readonly SkillSource[];
  readonly agentDirs: readonly string[];
  readonly suggested: readonly string[];
  readonly skills: readonly SkillRow[];
  readonly notes: readonly string[];
}

/** An installed plugin that provides skills (the shell's `skills_providers`). */
export interface SkillsProvider {
  readonly plugin: string;
  readonly version: string;
  readonly state: string;
  readonly message: string;
  readonly running: boolean;
}

const OFF: SkillChoice = { vm: false, borrowers: false };

function keep(choice: SkillChoice): boolean {
  return choice.vm || choice.borrowers || choice.published !== undefined || (choice.allowed?.length ?? 0) > 0;
}

function withSkills(s: SkillsSettings, skills: Record<string, SkillChoice>): SkillsSettings {
  return { ...s, skills };
}

export function withEnabled(s: SkillsSettings, enabled: boolean): SkillsSettings {
  return { ...s, enabled };
}

/** Add a source folder (once). */
export function withSource(s: SkillsSettings, path: string): SkillsSettings {
  const p = path.trim();
  return p === "" || s.sources.includes(p) ? s : { ...s, sources: [...s.sources, p] };
}

export function withoutSource(s: SkillsSettings, path: string): SkillsSettings {
  return { ...s, sources: s.sources.filter((x) => x !== path) };
}

/** Change what the member chose for one skill. Borrowers need the sandbox:
 *  taking the skill out of the member's own sandbox takes it from borrowers too. */
export function withChoice(s: SkillsSettings, name: string, patch: Partial<Pick<SkillChoice, "vm" | "borrowers">>): SkillsSettings {
  const before = s.skills[name] ?? OFF;
  const vm = patch.vm ?? before.vm;
  const next: SkillChoice = { ...before, vm, borrowers: vm && (patch.borrowers ?? before.borrowers) };
  const skills = { ...s.skills };
  if (keep(next)) skills[name] = next;
  else delete skills[name];
  return withSkills(s, skills);
}

/** Record a publish of `name`, so a later change to the skill shows. */
export function withPublished(s: SkillsSettings, name: string, published: PublishedSkill): SkillsSettings {
  return withSkills(s, { ...s.skills, [name]: { ...(s.skills[name] ?? OFF), published } });
}

/** Let one flagged file through for the sandbox (never for the library). The
 *  allowance is for these bytes: the provider checks the file again if it changes. */
export function withAllowed(s: SkillsSettings, name: string, file: AllowedFile): SkillsSettings {
  const before = s.skills[name] ?? OFF;
  const rest = (before.allowed ?? []).filter((a) => a.file !== file.file);
  return withSkills(s, { ...s.skills, [name]: { ...before, allowed: [...rest, file] } });
}

/** Stop allowing a file. */
export function withoutAllowed(s: SkillsSettings, name: string, file: string): SkillsSettings {
  const before = s.skills[name];
  if (!before) return s;
  const allowed = (before.allowed ?? []).filter((a) => a.file !== file);
  const next: SkillChoice = { ...before, allowed: allowed.length > 0 ? allowed : undefined };
  const skills = { ...s.skills };
  if (keep(next)) skills[name] = next;
  else delete skills[name];
  return withSkills(s, skills);
}

/** How many skills reach the member's sandbox / a borrower's. */
export function reachCounts(rows: readonly SkillRow[]): { readonly own: number; readonly borrowers: number } {
  const used = rows.filter((r) => !r.hiddenBy && r.eligible);
  return { own: used.filter((r) => r.choice.vm).length, borrowers: used.filter((r) => r.choice.vm && r.choice.borrowers).length };
}

/** The one line under a skill: what it is, and why it cannot be used. */
export function skillLine(r: SkillRow): string {
  if (r.hiddenBy) return `Hidden: ${r.hiddenBy} has a skill of the same name.`;
  if (!r.eligible) return r.reason ? `Not shared: ${r.reason}` : "Not shared.";
  const bits = [r.description];
  if (r.hasScripts) bits.push("runs scripts");
  if (r.publishProblem) bits.push(`not for the library: ${r.publishProblem}`);
  return bits.filter((b) => b !== "").join(" · ");
}

/** What the publish button says. */
export function publishLabel(r: SkillRow): string {
  if (r.publishProblem) return "Cannot add to library";
  if (!r.choice.published) return "Add to my library";
  return r.changed ? "Publish changes" : "In your library";
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/** A packed skill as the shell frames it: the hash, a line feed, the tar. */
export function unframePackedSkill(bytes: Uint8Array): { readonly hash: string; readonly archive: Uint8Array } {
  const at = bytes.indexOf(0x0a);
  if (at < 0) throw new Error("the packed skill is malformed");
  return { hash: new TextDecoder().decode(bytes.subarray(0, at)), archive: bytes.subarray(at + 1) };
}
