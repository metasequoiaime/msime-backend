import { forwardRef } from "react";
import type { ButtonHTMLAttributes } from "react";
import { cva } from "class-variance-authority";
import type { VariantProps } from "class-variance-authority";
import { cn } from "./cn";

export const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap font-semibold transition select-none disabled:pointer-events-none disabled:opacity-45 active:scale-[.98]",
  {
    variants: {
      variant: {
        primary: "bg-btn text-btn-fg hover:opacity-90",
        danger: "bg-bad text-white hover:opacity-90",
        "danger-outline": "bg-transparent text-bad inset-ring inset-ring-bad-soft hover:bg-bad-soft",
        default: "bg-panel text-ink inset-ring inset-ring-hair-2 hover:bg-panel-2",
        outline: "bg-transparent text-ink inset-ring inset-ring-hair-2 hover:bg-panel-2",
        ghost: "bg-transparent text-body hover:bg-panel-2 hover:text-ink",
      },
      size: {
        sm: "h-7 rounded-lg px-2.5 text-[12.5px]",
        md: "h-9 rounded-[10px] px-3.5 text-[13.5px]",
        lg: "h-[38px] rounded-[11px] px-4 text-sm",
        icon: "h-9 w-9 rounded-[10px] p-0",
      },
    },
    defaultVariants: { variant: "default", size: "md" },
  },
);

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants>;

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button({ className, variant, size, type = "button", ...props }, ref) {
  return <button ref={ref} type={type} className={cn(buttonVariants({ variant, size }), className)} {...props} />;
});
