import { Component, type ErrorInfo, type ReactNode } from "react";

// ErrorBoundary renders the crash instead of a blank window when a descendant
// throws during render. Without it a single render error unmounts the whole app
// (white screen) with no on-screen signal. It shows the message + stack so a
// failure is diagnosable in the packaged desktop app, where devtools are not
// available, and offers a reload.
interface State {
  error: Error | null;
  info: ErrorInfo | null;
}

export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null, info: null };

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    this.setState({ error, info });
    // Also log to the console for browser/devtools sessions.
    console.error("bnbloc render error:", error, info.componentStack);
  }

  render() {
    const { error, info } = this.state;
    if (!error) return this.props.children;
    return (
      <div className="min-h-[100dvh] overflow-auto bg-[var(--color-bg)] p-6 text-sm text-[var(--color-fg)]">
        <div className="mx-auto max-w-3xl">
          <h1 className="mb-2 text-lg font-semibold text-[var(--color-danger,#f66)]">
            Something went wrong
          </h1>
          <p className="mb-4 text-[var(--color-fg-muted)]">
            The interface hit an unexpected error. Reload to recover; the details
            below help diagnose it.
          </p>
          <button
            onClick={() => window.location.reload()}
            className="mb-4 rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 py-1.5 text-sm hover:bg-[var(--color-surface-2)]"
          >
            Reload
          </button>
          <pre className="whitespace-pre-wrap break-words rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 text-xs text-[var(--color-fg-muted)]">
            {String(error.stack || error.message)}
            {info?.componentStack ? "\n\nComponent stack:" + info.componentStack : ""}
          </pre>
        </div>
      </div>
    );
  }
}
