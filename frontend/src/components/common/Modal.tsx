/**
 * Modal —— 模态框
 * --------------------------------------------------------------------
 * 居中浮层，带遮罩。Esc 关闭。点遮罩关闭（默认）。
 * 通过 uiStore.openModal({ componentKey, props }) 触发；
 * componentKey 对应的具体组件由调用方在自己的 Modal 容器里分发。
 */
import { useEffect, useRef, type ReactNode } from "react";
import { X } from "lucide-react";
import clsx from "clsx";
import { useUIStore } from "@stores/uiStore";
import { useShortcut } from "@hooks/useShortcut";

export interface ModalProps {
  /** 必须与 uiStore.openModal 传入的 id 一致 */
  id: string;
  title: string;
  children: ReactNode;
  /** 宽度（px 或 Tailwind 类） */
  width?: string;
  /** 是否允许点击遮罩关闭 */
  dismissOnBackdrop?: boolean;
  /** 自定义底部 */
  footer?: ReactNode;
}

export function Modal({
  id,
  title,
  children,
  width = "min(640px, 90vw)",
  dismissOnBackdrop = true,
  footer,
}: ModalProps): JSX.Element | null {
  const modal       = useUIStore((s) => s.modal);
  const closeModal  = useUIStore((s) => s.closeModal);
  const ref         = useRef<HTMLDivElement | null>(null);

  const isOpen = modal?.id === id;

  // Esc 关闭
  useShortcut({
    key: "escape",
    handler: () => isOpen && closeModal(),
    requireFocus: false,
    ignoreEditable: false,
  });

  // 打开时 body 锁滚动
  useEffect(() => {
    if (!isOpen) return;
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = prev; };
  }, [isOpen]);

  if (!isOpen) return null;

  return (
    <div
      className="fixed inset-0 z-40 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      onClick={() => dismissOnBackdrop && closeModal()}
    >
      <div
        ref={ref}
        role="dialog"
        aria-modal
        onClick={(e) => e.stopPropagation()}
        style={{ width }}
        className={clsx(
          "max-h-[85vh] overflow-hidden rounded-lg border border-moss-border bg-moss-surface shadow-2xl",
          "flex flex-col",
        )}
      >
        {/* 头部 */}
        <div className="flex items-center justify-between border-b border-moss-border px-4 py-2.5">
          <h2 className="text-sm font-semibold text-ink">{title}</h2>
          <button
            onClick={closeModal}
            className="rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
            title="关闭"
          >
            <X size={14} />
          </button>
        </div>

        {/* 内容 */}
        <div className="flex-1 min-h-0 overflow-y-auto">{children}</div>

        {/* 底部 */}
        {footer && (
          <div className="border-t border-moss-border bg-moss-bg px-4 py-2">{footer}</div>
        )}
      </div>
    </div>
  );
}
