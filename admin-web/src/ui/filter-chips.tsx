import { ToggleGroup } from "radix-ui";
import { cn } from "./cn";

export type ChipOption<K extends string> = { key: K; label: string; count?: number };

// FilterChips is the single-select pill row in table toolbars; counts render at reduced opacity after the label.
export function FilterChips<K extends string>({ options, value, onChange, label, className }: { options: readonly ChipOption<K>[]; value: K; onChange: (key: K) => void; label?: string; className?: string }) {
  return <ToggleGroup.Root type="single" value={value} aria-label={label} onValueChange={next => { if (next) onChange(next as K); }} className={cn("flex flex-wrap items-center gap-1.5", className)}>
    {options.map(option => <ToggleGroup.Item key={option.key} value={option.key}
      className="inline-flex h-[30px] items-center gap-1.5 rounded-full px-3 text-[13px] text-body transition hover:bg-panel-2 data-[state=on]:bg-btn data-[state=on]:font-semibold data-[state=on]:text-btn-fg">
      {option.label}
      {option.count !== undefined && <span className="tabular-nums opacity-70">{option.count}</span>}
    </ToggleGroup.Item>)}
  </ToggleGroup.Root>;
}
