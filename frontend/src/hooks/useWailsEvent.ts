/**
 * useWailsEvent
 * --------------------------------------------------------------------
 * 订阅 Wails runtime 事件，组件卸载时自动取消订阅。
 * - 兼容 dev（HMR）场景：每次 callback 变化用 ref 包装避免重订阅。
 * - 兼容 raw payload（snake_case）→ 已转换 payload 的需求，使用 transform。
 */
import { useEffect, useRef } from "react";
import type { WailsRuntime } from "@wails/runtime/runtime";

function getRuntime(): WailsRuntime | undefined {
  if (typeof window === "undefined") return undefined;
  return window.runtime;
}

/**
 * 订阅事件
 * @param topic      事件名
 * @param callback   接收已规范化（前端 camelCase）的 payload
 * @param transform  可选：把 Wails 的 snake_case raw payload 转成 callback 期望的类型
 */
export function useWailsEvent<T = unknown, R = unknown>(
  topic: string,
  callback: (payload: T) => void,
  transform?: (raw: R) => T,
): void {
  // 用 ref 保存 callback / transform，避免依赖变化导致重复订阅
  const cbRef  = useRef(callback);
  const tfRef  = useRef(transform);
  cbRef.current = callback;
  tfRef.current = transform;

  useEffect(() => {
    const r = getRuntime();
    if (!r) {
      // 在纯浏览器（无 Wails）开发模式下不报错
      // eslint-disable-next-line no-console
      console.debug(`[useWailsEvent] runtime not available, skip ${topic}`);
      return;
    }

    const off = r.EventsOn(topic, (raw: unknown) => {
      try {
        const value = tfRef.current
          ? tfRef.current(raw as R)
          : (raw as T);
        cbRef.current(value);
      } catch (err: unknown) {
        // eslint-disable-next-line no-console
        console.error(`[useWailsEvent] ${topic} handler error:`, err);
      }
    });

    return () => {
      off();
    };
  }, [topic]);
}

/**
 * 主动 emit 事件（用于前端 ↔ 前端通讯）
 */
export function emitWailsEvent(topic: string, ...data: unknown[]): void {
  const r = getRuntime();
  if (!r) {
    // eslint-disable-next-line no-console
    console.debug(`[emitWailsEvent] runtime not available, skip ${topic}`, data);
    return;
  }
  r.EventsEmit(topic, ...data);
}
