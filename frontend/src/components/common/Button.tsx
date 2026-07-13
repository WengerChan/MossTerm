/**
 * Button —— 统一按钮
 * --------------------------------------------------------------------
 * 封装 moss-btn 系列，支持 variant / size / icon。
 */
import { type ButtonHTMLAttributes, forwardRef } from "react";
import clsx from "clsx";
import type { ReactNode } from "react";

export type ButtonVariant = "default" | "primary" | "ghost" | "danger";
export type ButtonSize = "sm" | "md" | "lg";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  icon?: ReactNode;
  active?: boolean;
  block?: boolean;
}

const VARIANT_CLASS: Record<ButtonVariant, string> = {
  default: "bg-moss-surface border border-moss-border text-ink hover:bg-moss-hover hover:border-accent/40",
  primary: "bg-accent/20 border border-accent/60 text-accent hover:bg-accent/30",
  ghost:   "bg-transparent border border-transparent text-ink-muted hover:bg-moss-hover hover:text-ink",
  danger:  "bg-state-err/10 border border-state-err/40 text-state-err hover:bg-state-err/20",
};

const SIZE_CLASS: Record<ButtonSize, string> = {
  sm: "h-6 px-2 text-[11px] gap-1",
  md: "h-8 px-3 text-xs gap-1.5",
  lg: "h-10 px-4 text-sm gap-2",
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "default", size = "md", icon, active, block, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      disabled={disabled}
      className={clsx(
        "inline-flex items-center justify-center rounded transition-colors duration-100",
        "focus:outline-none focus:ring-1 focus:ring-accent/60",
        "disabled:opacity-40 disabled:cursor-not-allowed",
        VARIANT_CLASS[variant],
        SIZE_CLASS[size],
        active && "bg-accent/25 border-accent text-accent",
        block && "w-full",
        className,
      )}
      {...rest}
    >
      {icon}
      {children}
    </button>
  );
});
