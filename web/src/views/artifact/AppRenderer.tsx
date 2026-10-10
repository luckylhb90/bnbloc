import { useState } from "react";
import { ArtifactRunner } from "@/views/ArtifactRunner";
import { DeviceFrame } from "./DeviceFrame";
import type { ArtifactViewerProps } from "./registry";
import { useViewerToolbar } from "./toolbar";

// Live HTML/app viewer: the room's sandboxed runner (opaque origin, CSP with
// no network, validated postMessage) filling the tab, with reload and
// device-width presets. Fullscreen is the workbench's own toggle.

const WIDTHS = [
  { value: "fill", label: "Fill" },
  { value: "1280", label: "Desktop 1280" },
  { value: "768", label: "Tablet 768" },
  { value: "375", label: "Phone 375" },
] as const;

export default function AppRenderer({ roomId, actor, artifactId, version, content, toolbar, onLocalState, remoteState }: ArtifactViewerProps) {
  const [reload, setReload] = useState(0);
  const [width, setWidth] = useState<string>("fill");
  useViewerToolbar(
    toolbar,
    [
      { id: "reload", kind: "button", icon: "reload", label: "Reload" },
      { id: "width", kind: "select", icon: "device", label: "Device width", options: WIDTHS, selected: width },
    ],
    (id, value) => {
      if (id === "reload") setReload((n) => n + 1);
      else if (id === "width" && typeof value === "string") setWidth(value);
    },
  );
  return (
    <DeviceFrame width={width}>
      <ArtifactRunner
        roomId={roomId}
        actor={actor}
        artifactId={artifactId}
        version={version}
        src={content}
        height="fill"
        autoRun
        reloadToken={reload}
        onState={onLocalState}
        remoteState={remoteState}
      />
    </DeviceFrame>
  );
}
