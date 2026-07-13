/**
 * Tailwind CSS 配置
 * --------------------------------------------------------------------
 * - 暗色主题（终端场景下深色更护眼）
 * - Moss 绿（#7CB342）作为主品牌色
 * - 自定义 monospace 字体栈（JetBrains Mono 优先）
 *
 * 主题色取自设计令牌（见 src/styles/theme.css），所有色彩集中维护。
 */

/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{ts,tsx}",
  ],
  darkMode: "class", // 显式 class 模式，避免 html 默认属性被 OS 主题污染
  theme: {
    extend: {
      colors: {
        // 背景层级
        moss: {
          bg: "#1a1d23",         // 主背景（深石板）
          surface: "#22262e",    // 次背景（卡片 / 侧栏）
          border: "#2e333d",     // 边框
          hover: "#2a2f38",      // hover 态
        },
        // 品牌主色（Moss 绿）
        accent: {
          DEFAULT: "#7CB342",
          50: "#f3faec",
          100: "#e3f3d2",
          200: "#c8e7a8",
          300: "#a8d878",
          400: "#8fc957",
          500: "#7CB342",         // 品牌主色
          600: "#5e9131",
          700: "#476e25",
          800: "#34511b",
          900: "#1f3210",
        },
        // 文字
        ink: {
          DEFAULT: "#e4e6eb",    // 文字主色
          muted: "#9aa0a6",      // 文字次色
          subtle: "#6c7280",     // 极弱文字
        },
        // 状态色
        state: {
          ok: "#7CB342",
          warn: "#f0a830",
          err: "#e5484d",
          info: "#5aa9e6",
        },
      },
      fontFamily: {
        // 终端 / 代码用
        mono: [
          "JetBrains Mono",
          "Fira Code",
          "SF Mono",
          "Cascadia Mono",
          "Menlo",
          "Consolas",
          "Liberation Mono",
          "monospace",
        ],
        // UI 用
        sans: [
          "Inter",
          "PingFang SC",
          "Hiragino Sans GB",
          "Microsoft YaHei",
          "system-ui",
          "sans-serif",
        ],
      },
      fontSize: {
        // 终端默认 14px；其它行高稍紧
        "term-sm": ["12px", "1.4"],
        "term":    ["14px", "1.35"],
        "term-lg": ["16px", "1.3"],
      },
      borderRadius: {
        // 终端风偏直角，但保留小圆角给按钮
        DEFAULT: "4px",
        lg: "6px",
      },
      boxShadow: {
        // 终端的"光晕"风格（无阴影）
        none: "0 0 #0000",
      },
      // 自定义 ring 颜色（聚焦边框）
      ringColor: {
        DEFAULT: "#7CB342",
      },
    },
  },
  plugins: [],
};
