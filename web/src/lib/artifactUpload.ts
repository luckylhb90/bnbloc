import { apiUrl } from "./config.ts";
import { authedFetch, errorDetail, type FetchImpl } from "./session.ts";

// The ONE platform-artifact upload (super-admin, content-addressed blob store):
// used by the desktop release publisher and the component publish wizard.
// Bytes stream through XMLHttpRequest by default so callers get upload
// progress; authentication and the 401 re-auth retry come from authedFetch.
// The CLI's upload (cmd/bnbloc/artifacts.go) applies the same response check.

export interface ArtifactUploadOptions {
  readonly signal?: AbortSignal;
  // Human-readable status lines ("Uploading x…", "Uploaded x · SHA-256 …").
  readonly onProgress?: (message: string) => void;
  // Byte-level upload progress; total is null when not computable.
  readonly onBytes?: (progress: { readonly loaded: number; readonly total: number | null }) => void;
  readonly fetchImpl?: FetchImpl;
  readonly origin?: string;
}

export interface ArtifactUploadResult {
  readonly key: string;
  readonly sha256: string;
  readonly size: number;
  readonly url: string;
}

const ARTIFACT_PATH = "/v1/platform/artifacts";

export async function uploadArtifact(
  file: File,
  options: ArtifactUploadOptions = {},
): Promise<ArtifactUploadResult> {
  const controller = new AbortController();
  const abortFromCaller = () => controller.abort(options.signal?.reason);
  if (options.signal?.aborted) abortFromCaller();
  else options.signal?.addEventListener("abort", abortFromCaller, { once: true });
  const timeout = setTimeout(
    () => controller.abort(new DOMException("Artifact upload timed out", "TimeoutError")),
    5 * 60 * 1000,
  );
  const fetchImpl = options.fetchImpl ?? ((url: string, init: RequestInit) => xhrFetch(url, init, options.onBytes));

  try {
    options.onProgress?.(`Uploading ${file.name} (${file.size.toLocaleString()} bytes)…`);
    let response: Response;
    try {
      response = await authedFetch(
        apiUrl(ARTIFACT_PATH),
        {
          method: "POST",
          headers: { "Content-Type": "application/octet-stream" },
          body: file,
          signal: controller.signal,
        },
        fetchImpl,
      );
    } catch (failure) {
      if (controller.signal.aborted) throw uploadAbortError(controller.signal.reason);
      throw failure;
    }
    if (!response.ok) {
      throw new Error(`Artifact upload failed: ${await errorDetail(response)}`);
    }

    const uploaded = (await response.json()) as { key?: string; sha256?: string; size?: number };
    if (
      typeof uploaded.key !== "string" ||
      !/^sha256\/[0-9a-f]{64}$/i.test(uploaded.key) ||
      typeof uploaded.sha256 !== "string" ||
      uploaded.key.slice("sha256/".length).toLowerCase() !== uploaded.sha256.toLowerCase() ||
      uploaded.size !== file.size
    ) {
      throw new Error("Artifact upload response did not match the selected file");
    }

    const url = artifactUrl(uploaded.key, options.origin);
    options.onProgress?.(`Uploaded ${file.name} · SHA-256 ${uploaded.sha256}`);
    return { key: uploaded.key, sha256: uploaded.sha256, size: uploaded.size, url };
  } finally {
    clearTimeout(timeout);
    options.signal?.removeEventListener("abort", abortFromCaller);
  }
}

export function artifactUrl(key: string, origin?: string): string {
  const path = `/v1/artifacts/${key}`;
  const url = apiUrl(path);
  return /^https?:\/\//i.test(url) ? url : `${origin ?? window.location.origin}${url}`;
}

function uploadAbortError(reason: unknown): Error {
  if (reason instanceof DOMException && reason.name === "TimeoutError") {
    return new Error("Artifact upload timed out after 5 minutes. Check gateway/realtime/blobstore connectivity and retry.");
  }
  return new Error("Artifact upload was cancelled.");
}

// xhrFetch is a fetch-shaped sender over XMLHttpRequest, used only because
// fetch exposes no upload progress. It honors the abort signal and resolves
// a Response so authedFetch treats it like any other transport.
function xhrFetch(
  url: string,
  init: RequestInit,
  onBytes?: ArtifactUploadOptions["onBytes"],
): Promise<Response> {
  return new Promise((resolve, reject) => {
    const signal = init.signal;
    if (signal?.aborted) {
      reject(signal.reason);
      return;
    }
    const request = new XMLHttpRequest();
    request.open(init.method ?? "GET", url);
    new Headers(init.headers).forEach((value, name) => request.setRequestHeader(name, value));
    request.upload.onprogress = (event) => {
      onBytes?.({ loaded: event.loaded, total: event.lengthComputable ? event.total : null });
    };
    const onAbort = () => request.abort();
    signal?.addEventListener("abort", onAbort, { once: true });
    const settle = () => signal?.removeEventListener("abort", onAbort);
    request.onabort = () => {
      settle();
      reject(signal?.reason ?? new DOMException("Upload aborted", "AbortError"));
    };
    request.onerror = () => {
      settle();
      reject(new TypeError("Network error during artifact upload"));
    };
    request.onload = () => {
      settle();
      resolve(new Response(request.responseText, { status: request.status }));
    };
    request.send(init.body as XMLHttpRequestBodyInit | null | undefined);
  });
}
