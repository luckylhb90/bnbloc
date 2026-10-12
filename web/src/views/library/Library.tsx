import { useState } from "react";
import { takeLibraryTab, type LibraryTabName } from "@/lib/team";
import { Segment } from "@/ui/Segment";
import { ContentTab } from "@/views/library/ContentTab";
import { PromptsTab } from "@/views/library/PromptsTab";
import { TemplatesTab } from "@/views/library/TemplatesTab";

type LibraryTab = LibraryTabName;
const LIBRARY_TABS = [
  { value: "experts" as const, label: "Experts" },
  { value: "skills" as const, label: "Skills" },
  { value: "prompts" as const, label: "Prompts" },
  { value: "templates" as const, label: "Templates" },
];

// Library holds the content components (experts, skill packs, prompt
// packs) and the room-template library (N3).
export function Library({ onOpenRoom }: { onOpenRoom: (roomId: string) => void }) {
  // "Open in Library" from a save toast opens the saved kind's tab.
  const [tab, setTab] = useState<LibraryTab>(() => takeLibraryTab() ?? "experts");
  const tabs = <Segment options={LIBRARY_TABS} value={tab} onChange={setTab} className="w-[26rem] max-w-full" />;
  if (tab === "experts") return <ContentTab key="expert" kind="expert" tabs={tabs} onOpenRoom={onOpenRoom} />;
  if (tab === "skills") return <ContentTab key="skill" kind="skill" tabs={tabs} onOpenRoom={onOpenRoom} />;
  if (tab === "prompts") return <PromptsTab tabs={tabs} />;
  return <TemplatesTab tabs={tabs} onOpenRoom={onOpenRoom} />;
}
