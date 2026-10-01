import type { ReactNode } from "react";
import { errorMessage, isGithubDisabled } from "../api/client";
import { Button } from "./button";
import { cn } from "./cn";

export function Empty({ title, children, className }: { title: ReactNode; children?: ReactNode; className?: string }) {
  return <div className={cn("px-4 py-10 text-center text-muted", className)} role="status">
    <p className="m-0 text-sm">{title}</p>
    {children && <div className="mt-2 text-[12.5px] leading-relaxed">{children}</div>}
  </div>;
}

// NotConfigured is the explicit state for features whose server config is absent (admin.github, admin.services, telegram).
export function NotConfigured({ title = "未配置", children, className }: { title?: ReactNode; children?: ReactNode; className?: string }) {
  return <div className={cn("rounded-[18px] bg-panel px-6 py-10 text-center ring-1 ring-hair", className)} role="status">
    <p className="m-0 text-[15px] font-bold text-ink">{title}</p>
    {children && <div className="mx-auto mt-2 max-w-[460px] text-[13px] leading-relaxed text-muted">{children}</div>}
  </div>;
}

// ErrorState renders a failed query. A 404 github_disabled error becomes the 未配置 state instead of an error.
export function ErrorState({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  if (isGithubDisabled(error)) return <NotConfigured className={className}>服务端未配置 GitHub 集成（admin.github），配置 GitHub App 后这里会显示真实数据。</NotConfigured>;
  return <div className={cn("flex flex-wrap items-center justify-between gap-3 rounded-[14px] bg-bad-soft px-4 py-3 text-[13.5px] text-bad", className)} role="alert">
    <span>{errorMessage(error)}</span>
    {onRetry && <Button size="sm" variant="danger-outline" onClick={onRetry}>重试</Button>}
  </div>;
}

export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden="true" className={cn("animate-pulse rounded-[10px] bg-panel-2", className)} />;
}

// SkeletonRows is the loading placeholder for a table or list card.
export function SkeletonRows({ rows = 5, className }: { rows?: number; className?: string }) {
  return <div className={cn("grid gap-2.5 p-4", className)} role="status" aria-label="正在加载">
    {Array.from({ length: rows }, (_, index) => (
      // biome-ignore lint/suspicious/noArrayIndexKey: placeholder rows have no identity
      <Skeleton key={index} className="h-9" />
    ))}
  </div>;
}
