import { clsx } from "clsx";
import type { ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

// The custom token utilities (text-ink, text-muted, ...) must be registered as colors, otherwise tailwind-merge treats text-muted as a font-size class and drops it when combined with text-[13px].
const merge = extendTailwindMerge({
  extend: {
    theme: {
      color: ["bg", "panel", "panel-2", "ink", "body", "muted", "accent", "accent-ink", "accent-soft", "accent-ring", "btn", "btn-fg", "hair", "hair-2", "warn", "warn-soft", "bad", "bad-soft", "info", "info-soft", "scrim", "chart-2", "chart-3"],
      shadow: ["card", "pop", "dialog"],
    },
  },
});

export function cn(...inputs: ClassValue[]): string {
  return merge(clsx(inputs));
}
