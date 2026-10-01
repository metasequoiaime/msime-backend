import { cn } from "./cn";

// ProgressBar width is set through React's style prop, which the admin CSP (style-src 'self') allows because it goes through CSSOM.
export function ProgressBar({ value, max = 100, tone = "accent", size = "md", label, className }: { value: number; max?: number; tone?: "accent" | "warn" | "bad" | "info"; size?: "sm" | "md"; label?: string; className?: string }) {
  const pct = max > 0 ? Math.max(0, Math.min(100, (value / max) * 100)) : 0;
  const fill = { accent: "bg-accent", warn: "bg-warn", bad: "bg-bad", info: "bg-info" }[tone];
  return <div role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={max} aria-valuenow={value} className={cn("w-full overflow-hidden bg-panel-2", size === "sm" ? "h-1.5 rounded-[3px]" : "h-2 rounded", className)}>
    <div className={cn("h-full rounded-[inherit] transition-[width] duration-300", fill)} style={{ width: `${pct}%` }} />
  </div>;
}
