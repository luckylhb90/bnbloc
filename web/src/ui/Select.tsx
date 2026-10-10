import type { SelectHTMLAttributes } from "react";
import { cn } from "@/lib/cn";

// Select is the app's one dropdown: a native <select> (keyboard, mobile
// pickers and a11y for free) without the platform's own chrome — WebKit draws
// a glossy gradient control otherwise — and with a caret of our own
// (`.select-control` in styles.css). Callers size and colour it as before;
// the right padding always leaves room for the caret.
export function Select({ className, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={cn("select-control", className, "pr-7")} />;
}
