import type { ReactNode } from "react";
import { Dialog } from "radix-ui";
import { X } from "lucide-react";
import { Button } from "./button";
import type { ButtonProps } from "./button";
import { cn } from "./cn";
import { Pill } from "./pill";
import type { Tone } from "./pill";
import { keepOpenForToast, useRegisterOverlay } from "./overlay";

export type DrawerField = { label: string; value: ReactNode; mono?: boolean };
export type DrawerItem = { text: ReactNode; meta?: ReactNode };
export type DrawerSection = { title: string; items?: readonly DrawerItem[]; content?: ReactNode; empty?: string };
export type DrawerAction = { label: string; onClick: () => void; variant?: "primary" | "danger" | "default"; disabled?: boolean; keepOpen?: boolean };

export type DetailDrawerProps = {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  sub?: ReactNode;
  pills?: readonly { text: string; tone: Tone }[];
  fields?: readonly DrawerField[];
  sections?: readonly DrawerSection[];
  actions?: readonly DrawerAction[];
  // composer is rendered above the footer, separated by a hairline (the issue reply box).
  composer?: ReactNode;
  // children are rendered at the top of the body (a KeyboardPreview, loading or error state).
  children?: ReactNode;
};

const actionVariants: Record<NonNullable<DrawerAction["variant"]>, ButtonProps["variant"]> = { primary: "primary", danger: "danger-outline", default: "outline" };

// DetailDrawer is the right-hand detail panel. Footer actions close the drawer before running unless keepOpen is set, as in the prototype.
export function DetailDrawer({ open, onClose, title, sub, pills, fields, sections, actions, composer, children }: DetailDrawerProps) {
  useRegisterOverlay(open, onClose);
  return <Dialog.Root open={open} onOpenChange={next => { if (!next) onClose(); }}>
    <Dialog.Portal>
      {/* Plain scrim instead of Dialog.Overlay: the overlay's scroll lock injects a <style> element that the admin CSP blocks. */}
      <div aria-hidden="true" className="fixed inset-0 z-65 bg-[rgba(10,20,14,.3)]" />
      <Dialog.Content aria-describedby={undefined} onInteractOutside={keepOpenForToast} className="fixed inset-y-0 right-0 z-66 flex w-[min(440px,100vw)] animate-drawer-in flex-col bg-panel shadow-dialog outline-none">
        <div className="flex items-start gap-3 px-[22px] pt-5 pb-3">
          <div className="min-w-0 flex-1">
            <Dialog.Title className="m-0 text-[17px] font-bold text-pretty text-ink">{title}</Dialog.Title>
            {sub && <p className="m-0 mt-1 text-[12.5px] text-muted">{sub}</p>}
          </div>
          <Dialog.Close asChild><Button variant="ghost" size="icon" className="h-8 w-8" title="关闭" aria-label="关闭"><X size={18} aria-hidden="true" /></Button></Dialog.Close>
        </div>
        {pills && pills.length > 0 && <div className="flex flex-wrap gap-1.5 border-b border-hair px-[22px] pb-3">{pills.map(pill => <Pill key={pill.text} tone={pill.tone}>{pill.text}</Pill>)}</div>}
        <div className="min-h-0 flex-1 overflow-y-auto px-[22px] py-[18px]">
          {children}
          {fields && fields.length > 0 && <dl className="m-0 mb-5 grid grid-cols-2 gap-x-4 gap-y-3">
            {fields.map(field => <div key={field.label} className="min-w-0">
              <dt className="text-xs text-muted">{field.label}</dt>
              <dd className={cn("m-0 mt-0.5 text-[13.5px] [overflow-wrap:anywhere] text-ink", field.mono && "font-mono text-[12.5px]")}>{field.value}</dd>
            </div>)}
          </dl>}
          {sections?.map(section => <section key={section.title} className="mb-5">
            <h3 className="m-0 mb-2 text-[13px] font-bold text-ink">{section.title}</h3>
            {section.content}
            {section.items && <div className="grid gap-1.5">
              {section.items.length === 0 && <div className="rounded-[10px] bg-panel-2 px-3 py-2.5 text-[13.5px] text-muted">{section.empty ?? "暂无"}</div>}
              {section.items.map((item, index) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: drawer items are static display rows without an identity of their own
                <div key={index} className="rounded-[10px] bg-panel-2 px-3 py-2.5">
                  <div className="text-[13.5px] leading-[1.7] [overflow-wrap:anywhere] whitespace-pre-wrap text-body">{item.text}</div>
                  {item.meta && <div className="mt-0.5 text-xs text-muted">{item.meta}</div>}
                </div>
              ))}
            </div>}
          </section>)}
        </div>
        {composer && <div className="border-t border-hair px-[22px] py-3">{composer}</div>}
        {actions && actions.length > 0 && <div className="flex flex-wrap justify-end gap-2 border-t border-hair px-[22px] py-3.5">
          {actions.map(action => <Button key={action.label} size="lg" variant={actionVariants[action.variant ?? "default"]} disabled={action.disabled}
            onClick={() => { if (!action.keepOpen) onClose(); action.onClick(); }}>{action.label}</Button>)}
        </div>}
      </Dialog.Content>
    </Dialog.Portal>
  </Dialog.Root>;
}

export type ReplyTemplate = { name: string; text: string };

// DrawerComposer is the reply box with template chips used by the issue drawer.
export function DrawerComposer({ value, onChange, onSend, sending = false, templates = [], placeholder = "回复提交者…" }: { value: string; onChange: (value: string) => void; onSend: () => void; sending?: boolean; templates?: readonly ReplyTemplate[]; placeholder?: string }) {
  const empty = !value.trim();
  return <div>
    {templates.length > 0 && <div className="mb-2 flex flex-wrap gap-1.5">
      {templates.map(template => <button key={template.name} type="button" onClick={() => onChange(template.text)} className="h-[26px] rounded-full px-2.5 text-xs text-body inset-ring inset-ring-hair-2 hover:bg-panel-2">{template.name}</button>)}
    </div>}
    <div className="flex items-end gap-2">
      <textarea rows={2} value={value} onChange={event => onChange(event.target.value)} placeholder={placeholder} aria-label={placeholder}
        className="block min-w-0 flex-1 resize-y rounded-[10px] bg-panel-2 px-3 py-2 text-[13.5px] text-ink outline-none placeholder:text-muted focus:ring-[1.5px] focus:ring-accent" />
      <Button variant="primary" className={cn(empty && "opacity-45")} disabled={sending} aria-disabled={empty} onClick={() => { if (!empty) onSend(); }}>{sending ? "发送中…" : "发送"}</Button>
    </div>
  </div>;
}
