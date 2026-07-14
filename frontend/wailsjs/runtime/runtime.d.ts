// =====================================================================
// Wails Runtime 占位类型
// ---------------------------------------------------------------------
// 真实情况：Wails 启动 webview 时会自动注入 `window.runtime`，本文件
// 只是为了让 TypeScript 知道它的存在。完整定义见 Wails 官方。
// =====================================================================

/** 事件额外参数 */
export interface RuntimeEvent<T = unknown> {
  name: string;
  data: T;
}

/** 日志级别 */
export type LogLevel = "Trace" | "Debug" | "Info" | "Warning" | "Error";

/**
 * Wails 全局 runtime 接口（与 window['runtime'] 对齐）
 */
export interface WailsRuntime {
  EventsOn(eventName: string, callback: (data: unknown) => void): () => void;
  EventsOff(eventName: string, ...additionalEventNames: string[]): void;
  EventsEmit(eventName: string, ...data: unknown[]): void;
  EventsNotify(eventName: string, ...data: unknown[]): void;
  EventsOnce(eventName: string, callback: (data: unknown) => void): () => void;
  EventsOnMultiple(eventName: string, callback: (data: unknown) => void, maxCallbacks: number): () => void;

  LogPrint(message: string): void;
  LogTrace(message: string): void;
  LogDebug(message: string): void;
  LogInfo(message: string): void;
  LogWarning(message: string): void;
  LogError(message: string): void;
  LogFatal(message: string): void;

  WindowReload(): void;
  WindowReloadApp(): void;
  WindowSetTitle(title: string): void;
  WindowShow(): void;
  WindowHide(): void;
  WindowSetSize(width: number, height: number): void;
  WindowGetSize(): Promise<{ w: number; h: number }>;
  WindowSetPosition(x: number, y: number): void;
  WindowGetPosition(): Promise<{ x: number; y: number }>;
  WindowCenter(): void;
  WindowFullscreen(): void;
  WindowUnfullscreen(): void;
  WindowIsFullscreen(): Promise<boolean>;
  WindowMaximise(): void;
  WindowUnmaximise(): void;
  WindowIsMaximised(): Promise<boolean>;
  WindowMinimise(): void;
  WindowUnminimise(): void;
  WindowIsMinimised(): Promise<boolean>;
  WindowSetSystemDefaultTheme(): void;
  WindowSetLightTheme(): void;
  WindowSetDarkTheme(): void;

  BrowserOpenURL(url: string): void;
  Environment(): Promise<{ buildType: string; platform: string; arch: string }>;

  ClipboardSetText(text: string): Promise<boolean>;
  ClipboardGetText(): Promise<string>;

  OnFileDrop(callback: (x: number, y: number, paths: string[]) => void, useDropTarget: boolean): void;
  OnFileDropOff(): void;

  // v2.5+ 新 API
  Quit(): void;
  Hide(): void;
  Show(): void;
  IsDarkMode(): Promise<boolean>;
}

declare global {
  interface Window {
    runtime: WailsRuntime;
    go: {
      app: {
        App: typeof import("../go/main/App").App;
      };
    };
  }
}

export {};
