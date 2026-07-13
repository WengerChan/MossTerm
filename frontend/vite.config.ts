import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

/**
 * Vite 配置
 * --------------------------------------------------------------------
 * 参考 Wails 官方推荐的开发服务器设置：
 *   - 固定端口 34115（Wails 默认约定）
 *   - 关闭 host 限制
 *   - 关闭清屏，让后端日志可读
 *
 * 同时配置 @/ 路径别名指向 src/，所有业务模块可统一导入。
 */
export default defineConfig({
  plugins: [react()],

  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
      "@components": path.resolve(__dirname, "src/components"),
      "@stores": path.resolve(__dirname, "src/stores"),
      "@hooks": path.resolve(__dirname, "src/hooks"),
      "@types": path.resolve(__dirname, "src/types"),
      "@utils": path.resolve(__dirname, "src/utils"),
      "@styles": path.resolve(__dirname, "src/styles"),
      "@wails": path.resolve(__dirname, "wailsjs"),
    },
  },

  server: {
    port: 34115,
    strictPort: true,
    host: "0.0.0.0",
    open: false,
    clearScreen: false,
    // Wails 在 dev 模式下通过 webview 加载，关闭 CORS 限制更安全
    cors: false,
  },

  build: {
    target: "es2022",
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: true,
    // 拆分 xterm.js 到独立 chunk，方便大终端场景按需加载
    rollupOptions: {
      output: {
        manualChunks: {
          xterm: ["@xterm/xterm", "@xterm/addon-fit", "@xterm/addon-web-links", "@xterm/addon-webgl"],
          react: ["react", "react-dom"],
        },
      },
    },
  },

  // xterm.js / Tailwind 之类的 CJS 依赖需要 esbuild 预构建
  optimizeDeps: {
    include: ["react", "react-dom", "zustand", "@xterm/xterm", "@xterm/addon-fit"],
  },
});
