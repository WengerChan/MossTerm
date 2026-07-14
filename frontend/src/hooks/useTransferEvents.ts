/**
 * useTransferEvents
 * --------------------------------------------------------------------
 * 订阅 Wails runtime 上的 transfer:progress / transfer:done / transfer:error
 * 三个事件，自动写 transferStore。
 *
 * 用法（一般在 SftpBrowserContent 顶层挂一次；多次挂载 Wails 会
 * 重复触发回调，注意 dedup）：
 *   useTransferEvents();
 */
import { useEffect } from "react";
import type { UploadJobInfo, UploadProgress } from "@wails/go/main/App";
import { useTransferStore, normalizeJobInfo, normalizeProgress } from "@stores/transferStore";
import { useWailsEvent } from "@hooks/useWailsEvent";
import { logger } from "@utils/logger";

/**
 * 内部辅助：从 raw payload 取 transferID。
 *
 * 兼容 camelCase / snake_case（Wails 默认按 Go 字段 tag 是 camelCase，
 * 但 v0.5.10 留口）。
 */
function getTransferID(raw: unknown): string {
  const r = raw as Record<string, unknown>;
  return String(r.transferID ?? r.transfer_id ?? "");
}

export function useTransferEvents(): void {
  // progress 事件：每片完成（节流 200ms）emit 一次。
  useWailsEvent<UploadProgress>("transfer:progress", (p) => {
    const id = p.transferID;
    if (!id) return;
    useTransferStore.getState().applyProgress(p);
  });

  // done 事件：传输完成
  useWailsEvent<UploadJobInfo>("transfer:done", (info) => {
    if (!info.transferID) return;
    useTransferStore.getState().upsertJob(info);
    logger.info(`[useTransferEvents] done: ${info.transferID} (${info.bytesSent}/${info.totalBytes})`);
  });

  // error 事件：失败 / 取消
  useWailsEvent<UploadJobInfo>("transfer:error", (info) => {
    if (!info.transferID) return;
    useTransferStore.getState().upsertJob(info);
    logger.warn(`[useTransferEvents] error: ${info.transferID} state=${info.state} err=${info.error ?? ""}`);
  });

  // 首次挂载：拉一次 list 兜底（如果后端 Manager 已经有 active jobs）
  useEffect(() => {
    void useTransferStore.getState().refreshList();
  }, []);
}

/**
 * useTransferProgress（局部订阅）
 *
 * 单个 transferID 的进度流（用于 UploadProgress 组件局部订阅）。
 * 内部仍调 useTransferEvents 的全局 store；提供 selector 拿单条进度。
 */
export function useTransferProgress(transferID: string | null) {
  return useTransferStore((s) => (transferID ? s.jobs[transferID] : undefined));
}

// 显式 re-export normalize 避免 lint 警告未使用
void normalizeJobInfo;
void normalizeProgress;
void getTransferID;
