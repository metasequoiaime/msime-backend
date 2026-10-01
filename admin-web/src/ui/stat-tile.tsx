import type { ReactNode } from "react";
import { cn } from "./cn";

export type StatTileProps = {
  label: ReactNode;
  value: ReactNode;
  // delta is shown next to the value; a leading "-" or "−" renders as bad, anything else as ok, unless deltaTone is given.
  delta?: string;
  deltaTone?: "ok" | "bad" | "warn" | "mute";
  sub?: ReactNode;
  // kpi is the overview card (28px value); stat is the compact tile above tables (22px value).
  size?: "kpi" | "stat";
  className?: string;
};

export function StatTile({ label, value, delta, deltaTone, sub, size = "stat", className }: StatTileProps) {
  const tone = deltaTone ?? (delta && /^[-−]/.test(delta) ? "bad" : "ok");
  const toneClass = { ok: "text-accent-ink", bad: "text-bad", warn: "text-warn", mute: "text-muted" }[tone];
  return <div className={cn("min-w-0 bg-panel ring-1 ring-hair", size === "kpi" ? "rounded-[18px] px-5 py-[18px]" : "rounded-2xl px-[18px] py-3.5", className)}>
    <div className={cn("text-muted", size === "kpi" ? "text-[13px]" : "text-[12.5px]")}>{label}</div>
    <div className="mt-1 flex items-baseline gap-2.5">
      <span className={cn("font-bold text-ink tabular-nums", size === "kpi" ? "text-[28px] leading-tight" : "text-[22px] leading-snug")}>{value}</span>
      {delta && <span className={cn("text-[12.5px] font-semibold", toneClass)}>{delta}</span>}
    </div>
    {sub && <div className="mt-1 text-xs text-muted">{sub}</div>}
  </div>;
}

// StatGrid lays tiles out like the design: 4 KPI columns on wide screens, auto-fit 170px tiles otherwise.
export function StatGrid({ children, kpi = false, className }: { children: ReactNode; kpi?: boolean; className?: string }) {
  return <div className={cn("grid gap-3", kpi ? "grid-cols-2 gap-3.5 min-[1100px]:grid-cols-4" : "grid-cols-[repeat(auto-fit,minmax(170px,1fr))]", className)}>{children}</div>;
}
