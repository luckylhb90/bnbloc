import {
  BracketsCurlyIcon,
  FileAudioIcon,
  FileCodeIcon,
  FileCsvIcon,
  FileIcon,
  FileImageIcon,
  FilePdfIcon,
  FileTextIcon,
  FileVideoIcon,
  FileZipIcon,
  GlobeIcon,
} from "@phosphor-icons/react";
import type { FileCategory } from "@/lib/artifactFiles";

// One icon per file category (tabs, cards, the deliverables list).
export const categoryIcons: Readonly<Record<FileCategory, typeof FileIcon>> = {
  image: FileImageIcon,
  pdf: FilePdfIcon,
  markdown: FileTextIcon,
  code: FileCodeIcon,
  text: FileTextIcon,
  table: FileCsvIcon,
  json: BracketsCurlyIcon,
  web: GlobeIcon,
  audio: FileAudioIcon,
  video: FileVideoIcon,
  archive: FileZipIcon,
  file: FileIcon,
};
