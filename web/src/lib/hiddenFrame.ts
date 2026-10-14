// Runs one task in a hidden sandboxed frame (docs/prd/2026-10-14-file-prompts-and-compare.md
// rule 7): reading a version into units where its bytes may be parsed, never
// in the app's origin. The frame is mounted off-screen with the same sandbox
// as a visible one; only its own window's messages reach step, which posts
// to it and settles the task. The frame goes when the task settles, is
// aborted, or stalls: no message for stallMs (activity, not a total time
// limit: a long document keeps it alive while it answers).

export interface HiddenFrame {
  readonly window: Window | null;
  post(message: unknown, transfer?: Transferable[]): void;
}

export interface Settle<T> {
  resolve(value: T): void;
  reject(error: Error): void;
}

const DEFAULT_STALL_MS = 60_000;

export function runInHiddenFrame<T>(
  src: string,
  step: (ev: MessageEvent, frame: HiddenFrame, settle: Settle<T>) => void,
  opts: { readonly signal?: AbortSignal; readonly stallMs?: number; readonly title?: string } = {},
): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    if (opts.signal?.aborted) return reject(new DOMException("Aborted", "AbortError"));
    const iframe = document.createElement("iframe");
    iframe.setAttribute("sandbox", "allow-scripts");
    iframe.referrerPolicy = "no-referrer";
    iframe.title = opts.title ?? "File reader";
    iframe.tabIndex = -1;
    iframe.setAttribute("aria-hidden", "true");
    iframe.style.cssText = "position:fixed;left:-10000px;top:0;width:1024px;height:768px;border:0;visibility:hidden";
    let timer = 0;
    let done = false;
    const frame: HiddenFrame = {
      get window() {
        return iframe.contentWindow;
      },
      post(message, transfer) {
        // A sandboxed frame's origin is opaque: "*" is the only target; the
        // window reference pins the recipient.
        iframe.contentWindow?.postMessage(message, "*", transfer ?? []);
      },
    };
    const finish = (error: Error | null, value?: T) => {
      if (done) return;
      done = true;
      window.clearTimeout(timer);
      window.removeEventListener("message", onMessage);
      opts.signal?.removeEventListener("abort", onAbort);
      iframe.remove();
      if (error) reject(error);
      else resolve(value as T);
    };
    const arm = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => finish(new Error("The file reader stopped responding")), opts.stallMs ?? DEFAULT_STALL_MS);
    };
    const settle: Settle<T> = { resolve: (v) => finish(null, v), reject: (e) => finish(e) };
    const onMessage = (ev: MessageEvent) => {
      if (ev.source !== iframe.contentWindow) return;
      arm();
      step(ev, frame, settle);
    };
    const onAbort = () => finish(new DOMException("Aborted", "AbortError"));
    window.addEventListener("message", onMessage);
    opts.signal?.addEventListener("abort", onAbort);
    iframe.src = src;
    document.body.appendChild(iframe);
    arm();
  });
}
