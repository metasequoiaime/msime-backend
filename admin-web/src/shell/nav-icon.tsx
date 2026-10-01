import { cn } from "../ui/cn";

export function NavIcon({ d, className }: { d: string; className?: string }) {
  return <svg aria-hidden="true" focusable="false" viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" className={cn("shrink-0", className)}><path d={d} /></svg>;
}
