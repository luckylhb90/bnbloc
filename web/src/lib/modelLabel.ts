// Model id display labels for pickers and run summaries (pure; node-tested).

// prettyModel renders a model id as a display label ("gpt-5.5" -> "GPT-5.5",
// "claude-opus-5-5[1m]" -> "Claude Opus 5.5 [1m]"), so the picker reads
// naturally without hard-coding a label per model: a dash between two
// numbers is a version dot, GPT keeps its dash, a [suffix] stands apart.
export function prettyModel(id: string): string {
  if (!id) return "";
  const parts: string[] = [];
  for (const part of id.split("-")) {
    const prev = parts[parts.length - 1];
    if (prev !== undefined && /^\d+$/.test(prev) && /^\d+(\[[^\]]*\])?$/.test(part)) {
      parts[parts.length - 1] = `${prev}.${part}`;
    } else {
      parts.push(part);
    }
  }
  return parts
    .map((part) => (/^(gpt|o\d)/i.test(part) ? part.toUpperCase() : part.charAt(0).toUpperCase() + part.slice(1)))
    .join(" ")
    .replace(/^GPT (\d)/, "GPT-$1")
    .replace(/(\S)\[([^\]]*)\]$/, "$1 [$2]");
}

// modelLabel is the label the picker shows for a model: the name its agent gave it when
// there is one, else prettyModel's rendering of the id. The platform names no model.
export function modelLabel(id: string, names?: Readonly<Record<string, string>>): string {
  const name = names?.[id]?.trim();
  return name ? name : prettyModel(id);
}
