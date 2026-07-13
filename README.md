<div align="center">

```
   __  ___                  __    __                __
  /  |/  /__  ____ ___  ___/ /___/ /  ___  ___ ____/ /__ ____
 / /|_/ / _ \/ __/ -_) _  / __/ _ \/ _ \/ _ `/ _  / -_) __/
/_/  /_/\___/_/  \__/\_,_/_/  \___/_//_/\_,_/\_,_/\__/_/
                                                            🪴
```

**MossTerm** — 苔藓般安静生长的 SSH 客户端

> 在浏览器般的体验里，用真正的终端。
> 运维的活儿，本不该这么重。

[![License: MIT](https://img.shields.io/badge/License-MIT-2e7d32.svg)](./LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go)](./go.mod)
[![Wails](https://img.shields.io/badge/Wails-v2-red?logo=webassembly)](https://wails.io)
[![Status](https://img.shields.io/badge/status-pre--alpha-7cb342)](./CHANGELOG.md)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](./CONTRIBUTING.md)

</div>

---

## 📸 截图

> 占位 —— 欢迎补一张主窗口截图。
> 推荐尺寸 1280×800，主题选择亮/暗各一。

<p align="center">
  <i>（待补图：主界面 + 终端 + SFTP 文件管理）</i>
</p>

---

## ✨ 特性

### 现在（v0.x）

- 🪴 **超轻量原生应用**：Go + Wails，告别 Electron 的 150MB 内脏
- 🖥️ **现代终端体验**：xterm.js + WebGL，命令面板、多 tab、split pane
- 🔐 **凭据本地守护**：密码、key 全部走 OS Keyring，不落盘明文
- 🧰 **SSH 协议优先**：V2 协议、Ed25519 / RSA / ECDSA、agent forwarding、跳板链
- 📁 **SFTP 文件管理**：双窗格、拖拽、断点续传、远端编辑
- 🎨 **亮 / 暗主题**：内置多套色板，可自定义 token
- 🌐 **i18n 友好**：界面文本全走 i18n key
- 🧩 **可扩展架构**：插件宿主（WASM 沙箱）已在路线图上

### 即将到来（看 [Roadmap](#-roadmap)）

- 🤖 **AI 辅助**：命令解释、错误诊断、日志总结（默认走本地）
- 🔌 **多协议**：Telnet、RDP、Serial（v1.0 之后）
- 📦 **跨设备同步**：加密配置 + 凭据同步（v1.0）
- 🪶 **极致小包**：目标 < 20MB

---

## 🛣️ Roadmap

| 版本  | 时间        | 主题              | 关键里程碑                                                                    |
| ----- | ----------- | ----------------- | ----------------------------------------------------------------------------- |
| v0.1  | 2025 Q1     | **MVP · 第一片苔** | 单 SSH 连接、xterm 渲染、Keyring 存凭据、配置读写                           |
| v0.5  | 2025 Q3     | **密林初成**      | SFTP 文件管理、跳板链、命令面板、主题系统、亮/暗主题                          |
| v1.0  | 2026 Q1     | **稳固版**        | 多协议、插件宿主 v1、AI 辅助、跨平台分发、单元/集成测试覆盖率 ≥ 60%          |
| v2.0  | 2026 H2     | **生态版**        | 配置云同步、团队共享片段、远程协作（tmux 接管）、移动端伴侣                 |

> 详细路线图见 [`docs/ARCHITECTURE.md` §7 MVP 范围](./docs/ARCHITECTURE.md)。

---

## 🚀 快速开始

### 依赖

| 工具     | 版本     | 说明                          |
| -------- | -------- | ----------------------------- |
| Go       | 1.22+    | 后端编译与运行                |
| Node.js  | 18+      | 前端构建（仅开发需要）        |
| Wails    | v2.9+    | 桌面壳子 CLI                  |
| GCC/Clang | 任意     | CGO 依赖（macOS 自带 CLT）    |

> 各平台安装命令详见 [`docs/DEVELOPMENT.md`](./docs/DEVELOPMENT.md)。

### 安装

```bash
# 1. 安装 Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 2. 克隆并构建
git clone https://github.com/mossterm/mossterm.git
cd mossterm

# 3. 拉前端依赖
cd frontend && npm install && cd ..

# 4. 开发模式（HMR + Go 自动重编）
make dev
# 等价于：wails dev
```

### 5 分钟入门

想要 5 分钟从 0 到连上一台服务器？请看 [`docs/QUICKSTART.md`](./docs/QUICKSTART.md)。

---

## 📖 使用说明

### 基本操作

| 操作              | macOS          | Windows / Linux  |
| ----------------- | -------------- | ---------------- |
| 新建连接          | `⌘ N`          | `Ctrl N`         |
| 快速命令面板      | `⌘ K`          | `Ctrl K`         |
| 关闭当前 Tab      | `⌘ W`          | `Ctrl W`         |
| 切换 Tab          | `⌃ Tab`        | `Ctrl Tab`       |
| 分屏              | `⌘ D`          | `Ctrl D`         |
| 打开 SFTP         | `⌘ P` → SFTP   | `Ctrl P` → SFTP  |
| 复制              | `⌘ C`          | `Ctrl C`         |
| 粘贴              | `⌘ V`          | `Ctrl V`         |

> 完整快捷键与高级用法见 [`docs/user-guide`](./docs/user-guide/README.md)。

### 配置文件

`configs/config.example.toml` 给出了所有可调字段。复制为 `config.toml` 后按需修改：

```bash
cp configs/config.example.toml configs/config.toml
```

---

## 🏗️ 架构

MossTerm 采用「前端 Webview + Go 后端 + Wails 桥」三层架构。

```
┌──────────────────────────────────────────────┐
│  Webview (React + xterm.js + Tailwind)       │
│  ─ Wails Bridge (JSON-RPC) ─                │
│  Go Backend (session/connect/ssh/sftp/...)  │
└──────────────────────────────────────────────┘
```

完整设计：

- 模块划分、数据流、接口契约 → [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md)
- 开发环境搭建、调试技巧、FAQ → [`docs/DEVELOPMENT.md`](./docs/DEVELOPMENT.md)

---

## 🤝 贡献

欢迎所有形式的贡献：bug 报告、功能建议、文档改进、代码 PR。

- 阅读 [`CONTRIBUTING.md`](./CONTRIBUTING.md)
- 遵守 [`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md)
- 提交规范：Conventional Commits（`feat:` / `fix:` / `docs:` / `refactor:` ...）

> 一棵苔藓长成森林，需要很多雨滴。我们珍视每一颗。

---

## 🔒 安全

发现安全漏洞？**请勿**在公开 issue 提及。请按 [`SECURITY.md`](./SECURITY.md)（待补）流程私密报告。

---

## 📜 许可证

本项目基于 **MIT License** 开源 —— 详见 [`LICENSE`](./LICENSE)。

---

## 🙏 致谢

MossTerm 站在巨人的肩膀上：

- [Wails](https://wails.io) — Go + Web 的桥梁
- [xterm.js](https://xtermjs.org) — 浏览器里的真终端
- [golang.org/x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh) — Go 官方 SSH 协议栈
- [pkg/sftp](https://github.com/pkg/sftp) — 纯 Go SFTP 实现
- [BurntSushi/toml](https://github.com/BurntSushi/toml) — 配置文件解析

> 还有所有为我们提交过 issue / PR / discussion 的朋友们 —— 谢谢你们的耐心与善意。

---

<div align="center">

<sub>愿你的服务器永远平稳，愿你的终端永远干净。🪴</sub>

</div>
