import type { HTMLAttributes, ReactNode } from "react";
import { formatDistanceToNow } from "date-fns";
import { zhCN } from "date-fns/locale";
import { cn } from "../../ui/cn";

// ago is relativeTime from shell/notifications, repeated here because importing that module together with shell/permissions from a page makes the bundler split zod into a chunk that runs before zod-config, which the CSP smoke test reports as an eval violation.
export function ago(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : formatDistanceToNow(date, { locale: zhCN, addSuffix: true });
}

// SectionCard is the design's list card: a 15px title over hairline-separated rows, edge to edge.
export function SectionCard({ title, actions, children, className }: { title: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return <section className={cn("min-w-0 overflow-hidden rounded-[18px] bg-panel ring-1 ring-hair", className)}>
    <div className="flex items-center justify-between gap-3 border-b border-hair px-5 py-4">
      <h2 className="m-0 text-[15px] font-bold text-ink">{title}</h2>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
    {children}
  </section>;
}

// SectionRow is one row of a SectionCard; the last row drops its hairline.
export function SectionRow({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("flex items-center gap-3 border-b border-hair px-5 py-[13px] last:border-b-0", className)} {...props} />;
}

// Avatar is the initial-letter circle; Google avatars are never loaded because the console's CSP only allows its own images.
export function Avatar({ letter, size = "sm" }: { letter: string; size?: "sm" | "lg" }) {
  return <span aria-hidden="true" className={cn("inline-flex shrink-0 items-center justify-center rounded-full font-bold",
    size === "lg" ? "h-16 w-16 bg-accent text-[26px] text-btn-fg" : "h-[30px] w-[30px] bg-accent-soft text-[12.5px] text-accent-ink")}>{letter}</span>;
}
