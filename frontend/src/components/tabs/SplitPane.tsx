/**
 * SplitPane —— 横向 / 纵向分屏容器
 * --------------------------------------------------------------------
 * 递归组件的中间节点：children 是两个 PaneView。
 *
 * 行为：
 *   - 方向 horizontal → flex-row（左 / 右），分隔条可左右拖
 *   - 方向 vertical   → flex-col（上 / 下），分隔条可上下拖
 *   - 右上角浮出 split / close 工具栏（hover 时显示）
 *
 * v0.5.0 B 范围：
 *   - 分隔条视觉已就位（cursor-col-resize / cursor-row-resize）
 *   - 实际拖拽改 size 留到 v0.5.0 C（需要后端同步 cols/rows）
 *   - 当前 size 固定 50/50，由 store 写入
 *
 * 注意 React Fragment `key` 陷阱：`<>...</>` 不接受 key；
 * 这里用 `React.Fragment key={i}` 显式包一层。
 */
import { Fragment, useCallback, useEffect, useRef } from "react";
import {
  SplitSquareHorizontal,
  SplitSquareVertical,
  X,
} from "lucide-react";
import clsx from "clsx";
import type { PaneSplitDirection } from "./tabsStore";

export interface SplitPaneProps {
  direction: PaneSplitDirection;
  /** 两个子节点（必须是 PaneView） */
  children: React.ReactNode[];
  /** 用户在工具栏点了 "horizontal split" */
  onSplit: (dir: PaneSplitDirection) => void;
  /** 用户在工具栏点了 "close" —— 关闭整个 split 节点 */
  onClose: () => void;
  className?: string;
}

export function SplitPane({
  direction,
  children,
  onSplit,
  onClose,
  className,
}: SplitPaneProps): JSX.Element {
  const isHoriz = direction === "horizontal";
  const containerRef = useRef<HTMLDivElement | null>(null);

  // ----- 拖拽分隔条改 size (v0.5.0 C 占位) -----
  // 监听 mousedown 启动 drag，drag 时把鼠标偏移写回 child 的 size。
  // 当前 store 还未暴露 setPaneSize，故只做视觉捕获并 dispatch 一个
  // 自定义事件；store 升级后接上即可。
  const onDividerMouseDown = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      e.preventDefault();
      // TODO: 接到 store.setPaneSize(tabId, paneId, newSize)
      // 当前 v0.5 B：先不实现 drag，单纯 preventDefault 避免文本选中
    },
    [],
  );

  // 防止 split 容器里的 input 失焦（如果在 pane 里有 <input>）
  // —— 这里只是确保 keyboard event 能正常冒泡到 PaneView 的 click handler
  useEffect(() => {
    return () => {
      /* no-op */
    };
  }, []);

  return (
    <div
      ref={containerRef}
      className={clsx(
        "relative h-full w-full min-h-0 min-w-0",
        isHoriz ? "flex flex-row" : "flex flex-col",
        className,
      )}
    >
      {children.map((child, i) => (
        <Fragment key={i}>
          <div className="flex-1 min-w-0 min-h-0 overflow-hidden">
            {child}
          </div>
          {i < children.length - 1 && (
            <div
              onMouseDown={onDividerMouseDown}
              className={clsx(
                "shrink-0 bg-moss-border transition-colors hover:bg-accent",
                isHoriz
                  ? "w-1 cursor-col-resize"
                  : "h-1 cursor-row-resize",
              )}
              role="separator"
              aria-orientation={isHoriz ? "vertical" : "horizontal"}
              title="拖拽改 size（v0.5.0 C）"
            />
          )}
        </Fragment>
      ))}

      {/* 工具栏：hover 时浮出 */}
      <div
        className={clsx(
          "absolute right-1 top-1 z-10 flex gap-1 rounded border border-moss-border",
          "bg-moss-surface/90 p-0.5 opacity-0 backdrop-blur transition-opacity",
          "group-hover:opacity-100 hover:opacity-100",
        )}
      >
        <button
          onClick={() => onSplit("horizontal")}
          title="横向拆分（Ctrl+Shift+H）"
          className="rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
        >
          <SplitSquareHorizontal size={12} />
        </button>
        <button
          onClick={() => onSplit("vertical")}
          title="纵向拆分（Ctrl+Shift+V）"
          className="rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-ink"
        >
          <SplitSquareVertical size={12} />
        </button>
        <button
          onClick={onClose}
          title="关闭此分屏"
          className="rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-state-err"
        >
          <X size={12} />
        </button>
      </div>
    </div>
  );
}
