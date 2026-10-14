// The desktop shell's local documents (docs/prd/2026-10-14-ai-document-editing.md
// §4, M3; apps/desktop/src-tauri/src/local_files.rs). The shell keeps every
// path: the web gets an opaque token and the file name, reads the bytes
// through the token, records which room file the copy became (the
// binding), and saves a version back through the token. Outside the shell
// nothing here is available.

import { tauri } from "./desktop";

/** The room file an opened document became. */
export interface LocalBinding {
  readonly gateway: string;
  readonly roomId: string;
  readonly artifactId: string;
  readonly version: number;
}

/** An opened document, as the shell describes it. */
export interface LocalOpened {
  readonly token: string;
  readonly name: string;
  readonly sizeBytes: number;
  readonly binding: LocalBinding | null;
  /** Bound, and its bytes are not the ones last synced. */
  readonly changed: boolean;
}

/** A save back: written over the opened file, refused because the file
 *  changed (or is gone) since, or written beside it as a copy. */
export type LocalSaved =
  | { readonly status: "written" }
  | { readonly status: "changed" }
  | { readonly status: "missing" }
  | { readonly status: "copied"; readonly name: string };

export type LocalSaveMode = "check" | "overwrite" | "copy";

const OPENED_EVENT = "local-file-opened";

function shell() {
  const t = tauri();
  if (!t) throw new Error("Local files open in the desktop app only");
  return t;
}

export const localFiles = {
  available: (): boolean => tauri() !== null,

  /** The Open dialog; null when the member cancels. */
  pick: async (): Promise<LocalOpened | null> => (await shell().invoke("local_file_pick")) as LocalOpened | null,

  /** The documents the OS opened the app with since the last call. */
  takePending: async (): Promise<LocalOpened[]> => (await shell().invoke("local_file_take_pending")) as LocalOpened[],

  /** Calls handler whenever the OS opens a document in the app. */
  onOpened: (handler: () => void): Promise<() => void> => shell().listen(OPENED_EVENT, () => handler()),

  read: async (token: string): Promise<ArrayBuffer> => (await shell().invoke("local_file_read", { token })) as ArrayBuffer,

  bind: async (token: string, binding: LocalBinding): Promise<void> => {
    await shell().invoke("local_file_bind", { token, binding });
  },

  /** The opened document a room file came from (this gateway), if any. */
  forArtifact: async (gateway: string, artifactId: string): Promise<{ token: string; name: string } | null> =>
    (await shell().invoke("local_file_for_artifact", { gateway, artifactId })) as { token: string; name: string } | null,

  save: async (token: string, bytes: ArrayBuffer, version: number, mode: LocalSaveMode): Promise<LocalSaved> =>
    (await shell().invoke("local_file_save", new Uint8Array(bytes), {
      headers: { "x-bnbloc-token": token, "x-bnbloc-version": String(version), "x-bnbloc-mode": mode },
    })) as LocalSaved,
};
