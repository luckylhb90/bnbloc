import { useState } from "react";
import { takeLibraryTab, type LibraryTabName } from "@/lib/team";
import { Segment } from "@/ui/Segment";
import { WorksTab } from "@/views/library/WorksTab";
import { ContentTab } from "@/views/library/ContentTab";
import { PromptsTab } from "@/views/library/PromptsTab";
import { TemplatesTab } from "@/views/library/TemplatesTab";
import { ToolsTab } from "@/views/library/ToolsTab";

type LibraryTab = LibraryTabName;
const LIBRARY_TABS = [
  { value: "experts" as const, label: "Experts" },
  { value: "skills" as const, label: "Skills" },
  { value: "prompts" as const, label: "Prompts" },
  { value: "assists" as const, label: "AI assists" },
  { value: "tools" as const, label: "Tools" },
  { value: "templates" as const, label: "Templates" },
  { value: "works" as const, label: "Works" },
];

// Library holds the content components (experts, skill packs, prompt
// packs, AI assists), the MCP tools they can require, the room-template library (N3)
// and the member's works ("Works": what they published to the gallery,
// and the interactive apps across their rooms).
export function Library({ onOpenRoom }: { onOpenRoom: (roomId: string) => void }) {
  // "Open in Library" from a save toast opens the saved kind's tab.
  const [tab, setTab] = useState<LibraryTab>(() => takeLibraryTab() ?? "experts");
  const tabs = <Segment options={LIBRARY_TABS} value={tab} onChange={setTab} className="w-[42rem] max-w-full" />;
  if (tab === "experts") return <ContentTab key="expert" kind="expert" tabs={tabs} onOpenRoom={onOpenRoom} />;
  if (tab === "skills") return <ContentTab key="skill" kind="skill" tabs={tabs} onOpenRoom={onOpenRoom} />;
  if (tab === "prompts") return <PromptsTab tabs={tabs} onOpenRoom={onOpenRoom} />;
  if (tab === "assists") return <ContentTab key="assist" kind="assist" tabs={tabs} onOpenRoom={onOpenRoom} />;
  if (tab === "tools") return <ToolsTab tabs={tabs} />;
  if (tab === "works") return <WorksTab tabs={tabs} onOpenRoom={onOpenRoom} />;
  return <TemplatesTab tabs={tabs} onOpenRoom={onOpenRoom} />;
}
