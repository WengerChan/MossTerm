/**
 * React 应用入口
 * --------------------------------------------------------------------
 * - 挂载根组件
 * - 注入全局样式（Tailwind + 自定义主题）
 * - 监听 webview 加载完成事件（v0.1 仅打印日志）
 */
import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./index.css";
import "@styles/globals.css";
import "@styles/theme.css";

// 仅在开发模式下打印 Wails runtime 信息
if (import.meta.env.DEV) {
  console.info("[MossTerm] DEV mode, Vite HMR active");
}

// Wails webview 就绪后会注入 window.runtime / window.go
// type 由 wailsjs/runtime/runtime.d.ts 全局 declare，不重复声明

const root = document.getElementById("root");
if (!root) {
  throw new Error("[MossTerm] #root element not found in index.html");
}

ReactDOM.createRoot(root).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
