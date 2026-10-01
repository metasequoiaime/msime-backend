import { ToggleGroup } from "radix-ui";
import { cn } from "./cn";

export type SegmentOption<V extends string> = { value: V; label: string };

// Segmented is a single-choice toggle (the 浅色/深色/跟随系统 control); it never allows an empty selection.
export function Segmented<V extends string>({ options, value, onChange, label, className, size = "md" }: { options: readonly SegmentOption<V>[]; value: V; onChange: (value: V) => void; label?: string; className?: string; size?: "sm" | "md" }) {
  return <ToggleGroup.Root type="single" value={value} aria-label={label} onValueChange={next => { if (next) onChange(next as V); }}
    className={cn("inline-flex rounded-[10px] bg-panel-2 p-[3px]", className)}>
    {options.map(option => <ToggleGroup.Item key={option.value} value={option.value}
      className={cn("flex-1 rounded-lg px-3 text-[13px] whitespace-nowrap text-muted transition data-[state=on]:bg-panel data-[state=on]:font-semibold data-[state=on]:text-ink data-[state=on]:shadow-[0_1px_3px_rgba(0,0,0,.1)]", size === "sm" ? "h-[26px]" : "h-[30px]")}>
      {option.label}
    </ToggleGroup.Item>)}
  </ToggleGroup.Root>;
}
