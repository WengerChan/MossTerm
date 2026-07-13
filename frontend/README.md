# MossTerm Frontend

> Wails + React + TypeScript + Tailwind + xterm.js 的前端工程。
> v0.1 骨架阶段：仅完成目录结构与最小可运行的 UI 框架。

## 技术栈

| 层       | 选型                                        |
| -------- | ------------------------------------------- |
| 构建     | Vite 5 + @vitejs/plugin-react               |
| 框架     | React 18 + TypeScript 5                     |
| 样式     | Tailwind CSS 3                              |
| 终端     | @xterm/xterm + addon-fit/web-links/webgl    |
| 状态     | Zustand 4                                   |
| 图标     | lucide-react                                |
| 工具     | clsx                                        |

## 目录结构

```text
frontend/
├── package.json
├── tsconfig.json / tsconfig.node.json
├── vite.config.ts
├── tailwind.config.js / postcss.config.js
├── index.html
├── wailsjs/                  # 由 `wails generate module` 自动生成
│   ├── go/                   # 后端绑定的 TS 声明
│   └── runtime/              # Wails runtime 类型
└── src/
    ├── main.tsx              # React 入口
    ├── App.tsx               # 顶层组件
    ├── index.css             # Tailwind 入口
    ├── components/           # 视图组件
    │   ├── layout/           # 主框架、标题栏、状态栏、侧栏
    │   ├── terminal/         # xterm.js 封装
    │   ├── tabs/             # Tab（v0.2+）
    │   ├── session/          # Profile 树 / 表单
    │   ├── sftp/             # SFTP 面板（v0.2+）
    │   ├── palette/          # 命令面板
    │   └── common/           # Button / Modal / Tooltip
    ├── stores/               # Zustand 全局状态
    ├── hooks/                # 自定义 hooks
    ├── types/                # 与后端 DTO 镜像
    ├── utils/                # 工具函数
    └── styles/               # globals.css / theme.css
```

## 常用命令

```bash
# 开发模式（HMR）—— 仅前端，推荐用 `wails dev` 跑全栈
npm run dev

# 类型检查 + 生产构建
npm run build

# 预览构建产物
npm run preview

# 全栈开发（Wails 启动 Go 后端并嵌入 webview）
npm run wails:dev

# 全栈打包（产出原生可执行文件）
npm run wails:build

# 仅重新生成 wailsjs/ 类型（不重启后端）
npm run wails:generate
```

## 主题色（设计令牌）

| Token          | 值        | 用途             |
| -------------- | --------- | ---------------- |
| `moss.bg`      | `#1a1d23` | 主背景           |
| `moss.surface` | `#22262e` | 次背景 / 卡片    |
| `moss.border`  | `#2e333d` | 边框 / 分隔线    |
| `moss.hover`   | `#2a2f38` | hover 态         |
| `accent.500`   | `#7CB342` | 品牌主色 / Moss 绿 |
| `ink`          | `#e4e6eb` | 文字主色         |
| `ink.muted`    | `#9aa0a6` | 文字次色         |

详细配色见 `src/styles/theme.css` 与 `tailwind.config.js`。

## 状态管理约定

- **server state**（profile、session 元数据）：Zustand + Wails 调用。
- **UI state**（命令面板、侧栏折叠）：Zustand。
- **PTY 字节流（热路径）**：**不走** Zustand；直接 `term.write(Uint8Array)`。

更多架构细节参见 `../docs/ARCHITECTURE.md`。
