import type { ReactNode } from "react";
import { cva } from "class-variance-authority";
import { cn } from "./cn";

export type Tone = "ok" | "warn" | "bad" | "info" | "mute" | "accent";

export const pillVariants = cva("inline-flex h-[22px] shrink-0 items-center gap-1 whitespace-nowrap rounded-full px-2.5 text-xs font-semibold", {
  variants: {
    tone: {
      ok: "bg-accent-soft text-accent-ink",
      warn: "bg-warn-soft text-warn",
      bad: "bg-bad-soft text-bad",
      info: "bg-info-soft text-info",
      mute: "bg-panel-2 text-muted",
      accent: "bg-accent text-btn-fg",
    },
  },
  defaultVariants: { tone: "mute" },
});

export function Pill({ tone = "mute", children, className, title }: { tone?: Tone; children: ReactNode; className?: string; title?: string }) {
  return <span className={cn(pillVariants({ tone }), className)} title={title}>{children}</span>;
}

// Dot is the 8px status indicator used in service lists and the sidebar footer.
export function Dot({ tone = "ok", className }: { tone?: Tone; className?: string }) {
  const color = { ok: "bg-accent", accent: "bg-accent", warn: "bg-warn", bad: "bg-bad", info: "bg-info", mute: "bg-muted" }[tone];
  return <span aria-hidden="true" className={cn("inline-block h-2 w-2 shrink-0 rounded-full", color, className)} />;
}
