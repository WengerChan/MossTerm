/**
 * TrustRequestModal —— 首次连接信任弹窗
 * --------------------------------------------------------------------
 * 监听后端 "knownhosts:trust-request" 事件（v0.5.0 C）。
 *
 * 触发流程：
 *   1. SSH 握手时遇到未知 host key
 *   2. 后端 knownhosts.Manager.HostKeyCallback emit 事件
 *   3. 本组件显示 modal + host / fingerprint / keytype
 *   4. 用户点 trust / reject → App.TrustHost(id, action) → 后端 ReplyTrust
 *   5. HostKeyCallback 解除阻塞，SSH 继续 / 中断
 *
 * 设计要点：
 *   - 全局只渲染一个 modal（同一时刻只会有一个 trust 请求 v0.5.0 不处理并发）
 *   - modal 不能被 backdrop click / Esc 关闭 —— 用户必须明确选 trust/reject
 *   - 后端 60s 超时：前端不重复计时（信任用户感知：过了 60s 关闭就连接失败）
 *   - 失败不阻塞 UI：调 App.TrustHost 失败时弹 toast 提示，然后强制关 modal
 */
import { useCallback, useEffect, useState } from "react";
import { Check, Copy, Shield, ShieldOff, X } from "lucide-react";
import { useWailsEvent } from "@hooks/useWailsEvent";
import { useUIStore } from "@stores/uiStore";
import { EventTopic, type TrustRequestEvent } from "@types/events";
import { App } from "@wails/go/main/App";

/** 复制按钮的"已复制"反馈持续时间（ms）。 */
const COPIED_FEEDBACK_MS = 1500;

export function TrustRequestModal(): JSX.Element | null {
  const [request, setRequest] = useState<TrustRequestEvent | null>(null);
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const pushToast = useUIStore((s) => s.pushToast);

  // 订阅后端 "knownhosts:trust-request" 事件
  //
  // Wails 默认按 Go struct 的 PascalCase 字段名序列化；后端 TrustRequest
  // 显式打了 camelCase json tag（id / host / keyType / fingerprint / fullKey），
  // 所以前端收到的 payload 直接是 TrustRequestEvent 形状，无需 transform。
  //
  // useWailsEvent 内部用 ref 缓存 callback，传普通函数即可 —— 避免
  // useCallback 在每次渲染产生新引用反而触发不必要的重订阅。
  useWailsEvent<TrustRequestEvent>(
    EventTopic.KnownHostsTrustRequest,
    (req) => {
      setCopied(false);
      setRequest(req);
    },
  );

  // 通知后端决策，然后关 modal
  //
  // 即使 App.TrustHost 返回 error 也要关 modal（让 SSH 连接走 timeout 失败
  // 路径），但要把错误透给用户（toast）。
  const respond = useCallback(
    async (action: "trust" | "reject"): Promise<void> => {
      if (!request) return;
      setBusy(true);
      try {
        await App.TrustHost(request.id, action);
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : String(err);
        pushToast({
          level: "error",
          message: `trust 通知失败：${msg}`,
          durationMs: 5000,
        });
      } finally {
        setBusy(false);
        setRequest(null);
      }
    },
    [request, pushToast],
  );

  // 复制完整 key 到剪贴板
  const copyFullKey = useCallback(async (): Promise<void> => {
    if (!request) return;
    try {
      await navigator.clipboard.writeText(request.fullKey);
      setCopied(true);
      // 自动清除 "已复制" 提示
      window.setTimeout(() => setCopied(false), COPIED_FEEDBACK_MS);
    } catch (err: unknown) {
      // 剪贴板权限被拒（罕见）—— 不阻塞用户操作
      const msg = err instanceof Error ? err.message : String(err);
      pushToast({
        level: "warn",
        message: `复制失败：${msg}`,
        durationMs: 3000,
      });
    }
  }, [request, pushToast]);

  // modal 关闭时清理内部状态
  useEffect(() => {
    if (request !== null) return;
    setCopied(false);
  }, [request]);

  if (!request) return null;

  return (
    <div
      role="dialog"
      aria-modal
      aria-labelledby="trust-request-title"
      // 强制阻挡 backdrop click / Esc —— 用户必须明确决策
      // （wails 弹窗无背后点击逻辑，所以这里直接禁掉 z-50 上的其他交互）
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
      data-testid="trust-request-modal"
    >
      <div
        // 阻止冒泡到外层（如未来叠加别的层）
        onClick={(e) => e.stopPropagation()}
        className="w-[500px] max-w-[92vw] overflow-hidden rounded-lg border border-moss-border bg-moss-surface shadow-2xl"
      >
        {/* 标题栏 */}
        <div className="flex items-center justify-between border-b border-moss-border px-4 py-3">
          <div className="flex items-center gap-2">
            <Shield size={18} className="text-state-warn" aria-hidden />
            <h2
              id="trust-request-title"
              className="text-sm font-semibold text-ink"
            >
              未知主机密钥
            </h2>
          </div>
          {/* "X" 按钮 = reject 语义（用户主动放弃） */}
          <button
            onClick={() => void respond("reject")}
            disabled={busy}
            className="rounded p-1 text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-50"
            title="拒绝连接（等同 reject）"
            aria-label="拒绝"
          >
            <X size={14} />
          </button>
        </div>

        {/* 内容 */}
        <div className="space-y-3 px-4 py-4 text-sm">
          <p className="text-ink-muted">
            首次连接到 <span className="font-mono text-ink">{request.host}</span>。
            验证下方指纹与远端服务器实际指纹一致后再决定是否信任。
          </p>

          {/* 关键信息卡片 */}
          <div className="space-y-2 rounded-md border border-moss-border bg-moss-bg p-3">
            <Row label="主机" value={request.host} mono />
            <Row label="类型" value={request.keyType} mono />
            <Row label="指纹" value={request.fingerprint} mono small />
            <div className="flex items-center justify-between gap-2">
              <span className="text-ink-muted">完整 Key</span>
              <button
                onClick={() => void copyFullKey()}
                disabled={busy}
                className="group flex min-w-0 items-center gap-1.5 rounded px-1.5 py-0.5 text-ink-muted hover:bg-moss-hover hover:text-ink disabled:opacity-50"
                title="复制完整 base64 key"
              >
                <span className="truncate font-mono text-[11px]">
                  {request.fullKey}
                </span>
                {copied ? (
                  <Check
                    size={12}
                    className="shrink-0 text-accent"
                    aria-label="已复制"
                  />
                ) : (
                  <Copy
                    size={12}
                    className="shrink-0 opacity-60 group-hover:opacity-100"
                    aria-hidden
                  />
                )}
              </button>
            </div>
          </div>

          {/* 安全提示 */}
          <p className="text-[11px] leading-relaxed text-ink-subtle">
            信任后将写入{" "}
            <span className="font-mono text-ink-muted">
              ~/.config/mossterm/known_hosts
            </span>
            ，下次自动放行。若指纹不匹配（可能的 MITM 攻击）会拒绝连接。
          </p>
        </div>

        {/* 底部操作 */}
        <div className="flex items-center justify-end gap-2 border-t border-moss-border bg-moss-bg px-4 py-3">
          <button
            onClick={() => void respond("reject")}
            disabled={busy}
            className="inline-flex items-center gap-1.5 rounded border border-moss-border bg-moss-surface px-3 py-1.5 text-xs text-ink-muted hover:border-state-err/40 hover:text-state-err disabled:opacity-50"
            data-testid="trust-request-reject"
          >
            <ShieldOff size={14} aria-hidden />
            拒绝
          </button>
          <button
            onClick={() => void respond("trust")}
            disabled={busy}
            className="inline-flex items-center gap-1.5 rounded bg-accent px-3 py-1.5 text-xs font-medium text-moss-bg hover:bg-accent-600 disabled:opacity-50"
            data-testid="trust-request-trust"
          >
            <Shield size={14} aria-hidden />
            信任
          </button>
        </div>
      </div>
    </div>
  );
}

interface RowProps {
  label: string;
  value: string;
  /** 是否用等宽字体（host / keytype / fingerprint 都该 mono） */
  mono?: boolean;
  /** 小字号（fingerprint 视觉降权） */
  small?: boolean;
}

function Row({ label, value, mono, small }: RowProps): JSX.Element {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="shrink-0 text-ink-muted">{label}</span>
      <span
        className={
          "truncate text-right " +
          (mono ? "font-mono " : "") +
          (small ? "text-[11px] " : "text-xs ") +
          "text-ink"
        }
      >
        {value}
      </span>
    </div>
  );
}
