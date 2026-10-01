import type { ReleaseCheck, ReleaseSummary } from "../../api/release";
import { noPermissionHint } from "../../shell/permissions";
import { Button } from "../../ui/button";
import { cn } from "../../ui/cn";
import { Pill } from "../../ui/pill";
import { platformErrorText, releaseDate, repoShortName, statusLabel, statusTone } from "./shared";

const checkIcon: Record<ReleaseCheck["state"], { icon: string; className: string; label: string }> = {
  passed: { icon: "✓", className: "bg-accent-soft text-accent-ink", label: "已通过" },
  failed: { icon: "✕", className: "bg-bad-soft text-bad", label: "未通过" },
  pending: { icon: "·", className: "bg-warn-soft text-warn", label: "进行中" },
  unknown: { icon: "·", className: "bg-panel-2 text-muted", label: "未知" },
  manual: { icon: "·", className: "bg-panel-2 text-muted", label: "需手动" },
};

// PlatformCard is one platform in the list view: current release, its checklist, and the history and trigger buttons. The platform name is a stretched button so the whole card opens the history.
export function PlatformCard({ item, canTrigger, onOpen, onTrigger }: { item: ReleaseSummary; canTrigger: boolean; onOpen: () => void; onTrigger: () => void }) {
  const latest = item.latest;
  const released = latest?.status === "released";
  const triggerTitle = !canTrigger ? noPermissionHint : !item.workflow ? "未配置发布流水线（release_workflow）" : undefined;
  return <article className="relative min-w-0 rounded-[18px] bg-panel p-5 ring-1 ring-hair transition-shadow hover:shadow-card hover:ring-accent-ring">
    <div className="flex items-center gap-2.5">
      <button type="button" onClick={onOpen} className="min-w-0 truncate text-left text-[17px] font-bold text-ink outline-none after:absolute after:inset-0 after:rounded-[18px] focus-visible:after:ring-2 focus-visible:after:ring-accent">{item.name}</button>
      {latest && <Pill tone={statusTone[latest.status]} className="ml-auto">{statusLabel[latest.status]}</Pill>}
    </div>
    {item.error ? <p className="m-0 mt-3 text-[13px] leading-relaxed text-bad" role="alert">{platformErrorText(item.error)}</p> : latest ? <>
      <div className="mt-2.5 flex items-baseline gap-2.5">
        <span className="font-mono text-[22px] font-medium text-ink">{latest.version}</span>
        <span className="text-[12.5px] text-muted">{releaseDate(latest)}</span>
      </div>
      <div className="mt-1 truncate font-mono text-xs text-muted">tag {latest.tag} · {repoShortName(item.repo)}</div>
    </> : <>
      <div className="mt-2.5 font-mono text-[22px] font-medium text-muted">—</div>
      <div className="mt-1 truncate font-mono text-xs text-muted">还没有 {item.tag_prefix}* 的 Release · {repoShortName(item.repo)}</div>
    </>}
    {item.checklist.length > 0 && <ul className="m-0 mt-3.5 list-none border-t border-hair p-0 pt-3">
      {item.checklist.map(check => {
        const icon = checkIcon[check.state];
        return <li key={check.key} className="flex items-center gap-2 py-[5px] text-[13px]">
          <span className={cn("grid h-[18px] w-[18px] shrink-0 place-items-center rounded-full text-[11px] font-bold", icon.className)} role="img" aria-label={icon.label}>{icon.icon}</span>
          <span className="whitespace-nowrap text-body">{check.label}</span>
          {check.note && <span className="ml-auto truncate text-xs text-muted">{check.note}</span>}
        </li>;
      })}
    </ul>}
    <div className="relative z-10 mt-3.5 flex gap-2">
      <Button variant="outline" className="flex-1" onClick={onOpen}>发布历史</Button>
      <Button variant="primary" className="flex-1" disabled={released || !canTrigger || !item.workflow || Boolean(item.error)} title={released ? undefined : triggerTitle} onClick={onTrigger}>{released ? "已发布" : "触发发布"}</Button>
    </div>
  </article>;
}
