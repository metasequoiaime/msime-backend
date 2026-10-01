import type { Item, Section } from "../../api/community";
import { moderationLabels, moderationTones } from "../../api/community";
import { noPermissionHint } from "../../shell/permissions";
import { Button } from "../../ui/button";
import { cn } from "../../ui/cn";
import { Pill } from "../../ui/pill";
import { itemMeta, miniSkin, previewLines } from "./format";

const keyCount = Array.from({ length: 30 }, (_, index) => index);

// MiniKeyboard is the card preview of a skin: its real background and key colors on a 10 x 3 key grid.
function MiniKeyboard({ design }: { design: unknown }) {
  const skin = miniSkin(design);
  if (!skin) return <div className="grid h-full place-items-center bg-panel-2 text-[12.5px] text-muted">该皮肤格式暂不支持预览</div>;
  return <div aria-hidden="true" className="flex h-full flex-col justify-end p-3" style={{ background: skin.background }}>
    <div className="grid grid-cols-10 gap-1">
      {keyCount.map(key => <span key={key} className="block h-[18px] shadow-[0_1px_0_rgba(0,0,0,.12)]" style={{ background: skin.key, borderRadius: skin.radius }} />)}
    </div>
  </div>;
}

export type CardProps = {
  section: Section;
  item: Item;
  canReview: boolean;
  onOpen: () => void;
  onApprove: () => void;
  onRemove: () => void;
  onRestore: () => void;
};

// ItemCard is one community item in the moderation grid: preview, name and state, stats, the automatic flag or removal reason, and the two review buttons.
export function ItemCard({ section, item, canReview, onOpen, onApprove, onRemove, onRestore }: CardProps) {
  const lines = previewLines(section, item);
  const meta = itemMeta(section, item);
  const approved = item.moderation === "approved";
  const removed = item.moderation === "removed";
  const hint = canReview ? undefined : noPermissionHint;
  return <article className="group flex min-w-0 flex-col overflow-hidden rounded-[18px] bg-panel ring-1 ring-hair transition hover:shadow-card hover:ring-accent-ring">
    <button type="button" onClick={onOpen} aria-label={`查看「${item.name}」详情`} className="block h-[120px] w-full cursor-pointer overflow-hidden text-left outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-inset">
      {section === "skins" ? <MiniKeyboard design={item.design} /> : <div className="h-full bg-panel-2 px-4 py-3 text-[13px] leading-[1.8] text-body">
        {lines.length ? lines.slice(0, 3).map((line, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: preview lines are static text without an identity
          <div key={index} className={cn("truncate", (section === "candidate-skins" || section === "plugins") && index === 0 && "font-mono text-[12.5px]")}>{line}</div>
        )) : <div className="text-muted">没有可预览的内容</div>}
      </div>}
    </button>
    <div className="flex flex-1 flex-col px-4 pt-3.5 pb-4">
      <div className="flex min-w-0 items-center gap-2">
        <button type="button" onClick={onOpen} className="min-w-0 flex-1 cursor-pointer truncate text-left font-bold text-ink hover:underline">{item.name}</button>
        <Pill tone={moderationTones[item.moderation]} className="text-[11.5px]">{moderationLabels[item.moderation]}</Pill>
      </div>
      <p className="m-0 mt-1 truncate text-[12.5px] text-muted" title={meta ? `${item.author} · ${meta}` : item.author}>{item.author}{meta && ` · ${meta}`}</p>
      {item.flag && !removed && <p className="m-0 mt-2.5 rounded-lg bg-warn-soft px-2.5 py-1.5 text-[12.5px] leading-relaxed text-warn">{item.flag}</p>}
      {removed && item.moderation_reason && <p className="m-0 mt-2.5 rounded-lg bg-bad-soft px-2.5 py-1.5 text-[12.5px] leading-relaxed text-bad">下架原因：{item.moderation_reason === "owner_banned" ? "作者账号被封禁" : item.moderation_reason}</p>}
      <div className="mt-auto grid grid-cols-2 gap-2 pt-3.5">
        {removed
          ? <Button size="md" variant="outline" disabled={!canReview} title={hint} onClick={onRestore}>恢复</Button>
          : <Button size="md" variant="danger-outline" disabled={!canReview} title={hint} onClick={onRemove}>{approved ? "下架" : "驳回"}</Button>}
        <Button size="md" variant="primary" disabled={!canReview || approved} title={hint} onClick={onApprove}>{approved ? "已上架" : "通过并上架"}</Button>
      </div>
    </div>
  </article>;
}
