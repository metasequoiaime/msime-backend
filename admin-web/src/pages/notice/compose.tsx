import type { ReactNode } from "react";
import { noticeChannels, noticePlatforms } from "../../api/notice";
import type { Notice, NoticeValue } from "../../api/notice";
import { cn } from "../../ui/cn";

// Form is the editor state: a new notice when id is undefined, otherwise the draft being edited.
export type Form = { id?: string; title: string; body: string; all: boolean; platforms: string[]; channels: string[] };

export const emptyForm: Form = { title: "", body: "", all: true, platforms: [], channels: ["site", "app"] };

export function formFromNotice(notice: Notice): Form {
  const all = notice.targets.includes("all");
  return { id: notice.id, title: notice.title, body: notice.body, all, platforms: all ? [] : notice.targets, channels: notice.channels };
}

export function formValue(form: Form): NoticeValue {
  const targets = form.all ? ["all"] : noticePlatforms.map(p => p.key).filter(key => form.platforms.includes(key));
  const channels = noticeChannels.map(c => c.key).filter(key => form.channels.includes(key));
  return { title: form.title.trim(), body: form.body.trim(), targets, channels };
}

const fieldClass = "block w-full rounded-[10px] bg-panel-2 px-3 text-sm text-ink outline-none placeholder:text-muted focus:ring-[1.5px] focus:ring-accent";

// ToggleChip is the multi-select pill used for targets and channels.
function ToggleChip({ pressed, onClick, disabled, title, children }: { pressed: boolean; onClick: () => void; disabled?: boolean; title?: string; children: ReactNode }) {
  return <button type="button" aria-pressed={pressed} disabled={disabled} title={title} onClick={onClick}
    className={cn("inline-flex h-[30px] items-center gap-1 rounded-full px-3 text-[13px] transition disabled:cursor-not-allowed disabled:opacity-45",
      pressed ? "bg-accent-soft font-semibold text-accent-ink inset-ring inset-ring-accent-ring" : "text-body inset-ring inset-ring-hair-2 hover:bg-panel-2")}>
    {children}
  </button>;
}

const labelClass = "text-[12.5px] font-semibold text-muted";

function Field({ label, htmlFor, children }: { label: string; htmlFor: string; children: ReactNode }) {
  return <div className="grid gap-1.5">
    <label htmlFor={htmlFor} className={labelClass}>{label}</label>
    {children}
  </div>;
}

// ChipGroup is a labelled set of toggle chips; the fieldset legend names the group for assistive technology.
function ChipGroup({ label, children, note }: { label: string; children: ReactNode; note?: ReactNode }) {
  return <fieldset className="m-0 grid min-w-0 gap-1.5 border-0 p-0">
    <legend className={cn(labelClass, "mb-1.5 p-0")}>{label}</legend>
    <div className="flex flex-wrap gap-1.5">{children}</div>
    {note}
  </fieldset>;
}

// NoticeFields renders title, body, targets and channels. telegram is whether the server has a Telegram channel; undefined while loading.
export function NoticeFields({ form, onChange, telegram }: { form: Form; onChange: (next: Form) => void; telegram: boolean | undefined }) {
  // 全部平台 toggles on its own and clears single platforms; a single platform turns 全部平台 off.
  const togglePlatform = (key: string) => {
    const platforms = form.platforms.includes(key) ? form.platforms.filter(p => p !== key) : [...form.platforms, key];
    onChange({ ...form, all: false, platforms });
  };
  const toggleChannel = (key: string) => onChange({ ...form, channels: form.channels.includes(key) ? form.channels.filter(c => c !== key) : [...form.channels, key] });
  return <div className="grid gap-4">
    <Field label="标题" htmlFor="notice-title">
      <input id="notice-title" value={form.title} maxLength={200} onChange={event => onChange({ ...form, title: event.target.value })}
        placeholder="例如：Windows v0.5.5 已发布" className={cn(fieldClass, "h-10")} />
    </Field>
    <Field label="内容" htmlFor="notice-body">
      <textarea id="notice-body" rows={4} value={form.body} onChange={event => onChange({ ...form, body: event.target.value })}
        placeholder="公告正文，支持简单 Markdown" className={cn(fieldClass, "resize-y py-2.5 leading-relaxed")} />
    </Field>
    <ChipGroup label="投放范围">
      <ToggleChip pressed={form.all} onClick={() => onChange({ ...form, all: !form.all, platforms: [] })}>全部平台</ToggleChip>
      {noticePlatforms.map(p => <ToggleChip key={p.key} pressed={!form.all && form.platforms.includes(p.key)} onClick={() => togglePlatform(p.key)}>{p.label}</ToggleChip>)}
    </ChipGroup>
    <ChipGroup label="渠道" note={<p className="m-0 text-[12px] leading-relaxed text-muted">官网横幅和 App 内通知由官网与客户端拉取公开接口 /v1/notices 展示，接入前公告只保存在后台；Telegram 在发布时立即推送，无法撤回。</p>}>
      {noticeChannels.map(c => {
        const unavailable = c.key === "telegram" && !telegram;
        const pressed = form.channels.includes(c.key);
        // An unconfigured Telegram channel stays clickable while selected so a draft that has it can drop it.
        return <ToggleChip key={c.key} pressed={pressed} disabled={unavailable && !pressed} onClick={() => toggleChannel(c.key)}
          title={unavailable ? "服务端未配置 admin.telegram" : undefined}>
          {c.label}{unavailable && telegram === false && <span className="text-[11.5px] font-normal text-muted">· 未配置</span>}
        </ToggleChip>;
      })}
    </ChipGroup>
  </div>;
}
