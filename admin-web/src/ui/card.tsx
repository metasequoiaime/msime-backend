import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "./cn";

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("min-w-0 rounded-[18px] bg-panel p-5 ring-1 ring-hair", className)} {...props} />;
}

// CardHeader renders the 15px card title with an optional right-hand slot (legend, actions).
export function CardHeader({ title, sub, actions, className }: { title: ReactNode; sub?: ReactNode; actions?: ReactNode; className?: string }) {
  return <div className={cn("mb-3 flex flex-wrap items-start justify-between gap-3", className)}>
    <div className="min-w-0">
      <h2 className="m-0 text-[15px] font-bold text-ink">{title}</h2>
      {sub && <p className="m-0 mt-1 text-[12.5px] text-muted">{sub}</p>}
    </div>
    {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
  </div>;
}

// Banner is the full-width tinted note (privacy boundary, unconfigured hints).
export function Banner({ tone = "info", children, className }: { tone?: "info" | "warn" | "bad" | "ok"; children: ReactNode; className?: string }) {
  const tones = { info: "bg-info-soft", warn: "bg-warn-soft", bad: "bg-bad-soft", ok: "bg-ok-soft" };
  return <div className={cn("rounded-[18px] px-5 py-[18px] text-[13.5px] leading-[1.8] text-body", tones[tone], className)}>{children}</div>;
}
