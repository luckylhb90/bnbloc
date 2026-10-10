import type { ComponentPackageEnvelope } from "@/lib/apiTypes";

export type ReviewPackage = {
  readonly fileName: string;
  readonly fileSize: number;
  readonly envelope: ComponentPackageEnvelope;
};
