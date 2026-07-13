/**
 * Tooltip —— 简易 tooltip
 * --------------------------------------------------------------------
 * 纯 CSS 实现：hover / focus 时显示。不依赖 portal，
 * 简单场景够用；复杂定位（边界检测）v0.2 引入 @radix-ui/react-tooltip。
 */
import { useState, type ReactNode } from "react";
import clsx from "clsx";

export type TooltipPlacement = "top" | "bottom" | "left" | "right";

export interface TooltipProps {
  content: ReactNode;
  /** 显示位置 */
  placement?: TooltipPlacement;
  /** 延迟显示毫秒 */
  delayMs?: number;
  children: ReactNode;
  className?: string;
}

const PLACEMENT_CLASS: Record<TooltipPlacement, string> = {
  top:    "bottom-full left-1/2 -translate-x-1/2 mb-1.5",
  bottom: "top-full left-1/2 -translate-x-1/2 mt-1.5",
  left:   "right-full top-1/2 -translate-y-1/2 mr-1.5",
  right:  "left-full top-1/2 -translate-y-1/2 ml-1.5",
};

export function Tooltip({
  content,
  placement = "top",
  delayMs = 250,
  children,
  className,
}: TooltipProps): JSX.Element {
  const [visible, setVisible] = useState(false);
  const [timer, setTimer]     = useState<number | null>(null);

  const onEnter = (): void => {
    const t = window.setTimeout(() => setVisible(true), delayMs);
    setTimer(t);
  };
  const onLeave = (): void => {
    if (timer !== null) window.clearTimeout(timer);
    setVisible(false);
  };

  return (
    <span
      onMouseEnter={onEnter}
      onMouseLeave={onLeave}
      onFocus={onEnter}
      onBlur={onLeave}
      className={clsx("relative inline-flex", className)}
    >
      {children}
      {visible && (
        <span
          role="tooltip"
          className={clsx(
            "pointer-events-none absolute z-50 max-w-xs whitespace-nowrap rounded border border-moss-border bg-moss-bg px-2 py-1 text-[10px] text-ink shadow-lg",
            PLACEMENT_CLASS[placement],
          )}
        >
          {content}
        </span>
      )}
    </span>
  );
}
