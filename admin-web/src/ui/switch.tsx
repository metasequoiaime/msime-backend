import { Switch as RadixSwitch } from "radix-ui";
import { cn } from "./cn";

export function Switch({ checked, onCheckedChange, disabled, label, className }: { checked: boolean; onCheckedChange: (checked: boolean) => void; disabled?: boolean; label: string; className?: string }) {
  return <RadixSwitch.Root checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} aria-label={label}
    className={cn("relative inline-flex h-[22px] w-[38px] shrink-0 items-center rounded-full bg-hair-2 transition data-[state=checked]:bg-accent disabled:opacity-45", className)}>
    <RadixSwitch.Thumb className="block h-[18px] w-[18px] translate-x-[2px] rounded-full bg-white shadow-[0_1px_3px_rgba(0,0,0,.2)] transition-transform data-[state=checked]:translate-x-[18px]" />
  </RadixSwitch.Root>;
}
