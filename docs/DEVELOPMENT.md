# MossTerm 开发文档

> 写给想给苔藓添一片叶子的开发者。
> 从环境搭建到调试技巧，再到常见问题——一次说清。

---

## 目录

1. [前置知识](#1-前置知识)
2. [环境搭建](#2-环境搭建)
3. [第一次运行](#3-第一次运行)
4. [项目结构](#4-项目结构)
5. [开发工作流](#5-开发工作流)
6. [调试技巧](#6-调试技巧)
7. [测试](#7-测试)
8. [构建与打包](#8-构建与打包)
9. [常见问题 FAQ](#9-常见问题-faq)

---

## 1. 前置知识

- **Go**：基本语法、module、cgo 概念
- **TypeScript / React**：基本组件、Hooks
- **xterm.js**：终端原理（VT100、ANSI 转义序列）
- **Wails**：了解 [wails.io 文档](https://wails.io/docs/introduction) 即可

> 如果你 SSH、SFTP、agent forwarding 这些概念还模糊，先翻一下 OpenSSH 手册。

---

## 2. 环境搭建

### 2.1 通用工具

| 工具       | 版本      | 用途                  |
| ---------- | --------- | --------------------- |
| Go         | 1.22+     | 后端编译/运行         |
| Node.js    | 18+（建议 20 LTS） | 前端构建 |
| Wails CLI  | v2.9+     | 桌面壳子              |
| Git        | 2.30+     | 版本控制              |
| Make       | 任意      | 跑 Makefile           |
| golangci-lint | latest | 静态检查              |

### 2.2 macOS

```bash
# 1. 安装 Homebrew（如未安装）
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

# 2. 安装依赖
brew install go node git

# 3. 安装 Wails
go install github.com/wailsapp/wails/v2/cmd/wails@v2.9.2

# 4. 安装 golangci-lint
brew install golangci-lint

# 5. 验证
wails doctor
```

也可一键执行：

```bash
./scripts/install.sh --with-brew
```

### 2.3 Linux（Ubuntu / Debian）

```bash
# 1. 基础工具
sudo apt-get update
sudo apt-get install -y build-essential pkg-config curl git

# 2. Go（推荐官方二进制）
wget https://go.dev/dl/go1.22.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.22.0.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc

# 3. Node.js（推荐 nvm）
curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.39.7/install.sh | bash
nvm install 20 && nvm use 20

# 4. Wails + GTK/WebKit 桌面依赖
go install github.com/wailsapp/wails/v2/cmd/wails@v2.9.2
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.0-dev

# 5. golangci-lint
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sudo sh -s -- -b /usr/local/bin

# 6. 验证
wails doctor
```

> **Fedora / RHEL**：`sudo dnf install gcc pkgconf-pkg-config gtk3-devel webkit2gtk4.0-devel`
> **Arch**：`sudo pacman -S base-devel pkgconf gtk3 webkit2gtk`

### 2.4 Windows

以 **管理员身份** 打开 PowerShell：

```powershell
# 推荐：先装 Windows Package Manager
winget install --id Microsoft.WindowsTerminal -e
winget install --id Git.Git -e

# 依赖
winget install --id GoLang.Go -e
winget install --id OpenJS.NodeJS.LTS -e

# Wails
go install github.com/wailsapp/wails/v2/cmd/wails@v2.9.2

# golangci-lint
Invoke-WebRequest -UseBasicParsing https://raw.githubusercontent.com/golangci/golangci-lint/master/install.ps1 | Invoke-Expression

# 验证
wails doctor
```

> **MSVC 构建工具**：Wails 在 Windows 上需要 CGO + C 编译器。
> 安装 [Visual Studio Build Tools 2022](https://visualstudio.microsoft.com/downloads/#build-tools-for-visual-studio-2022) 时勾选「**使用 C++ 的桌面开发**」。
> `wails doctor` 会自动检测。

也可一键执行：

```powershell
.\scripts\install.ps1
```

---

## 3. 第一次运行

### 3.1 克隆 & 拉依赖

```bash
git clone https://github.com/mossterm/mossterm.git
cd mossterm

# 拉前端依赖
make tidy            # 同步 go.mod
(cd frontend && npm install)
```

### 3.2 配置示例

```bash
cp configs/config.example.toml configs/config.toml
# 按需编辑（首次可不动）
```

### 3.3 启动开发模式

```bash
make dev             # 等价于 wails dev
# 或
./scripts/dev.sh
```

首次启动会：

1. 编译 Go 后端
2. 启动 Vite 前端 dev server（默认 5173 端口）
3. 弹出 Wails 桌面窗口
4. 监听前后端文件变化，HMR 热更新

### 3.4 验证

- 窗口能正常打开
- 左侧栏出现空白 session 列表
- 顶栏出现菜单 / 工具栏
- 按 `⌘ K` / `Ctrl K` 能弹出命令面板

如果出现红屏，参见 [§9 FAQ](#9-常见问题-faq)。

---

## 4. 项目结构

完整结构说明见 [`docs/ARCHITECTURE.md`](./ARCHITECTURE.md)。速览：

```
MossTerm/
├── cmd/mossterm/        主入口（main.go ≤ 60 行）
├── internal/            业务实现
│   ├── app/             DI 容器 + 生命周期
│   ├── session/         tab / pane
│   ├── connect/         协议抽象
│   ├── sshclient/       SSH 实现
│   ├── sftpclient/      SFTP 实现
│   ├── pty/             跨平台 PTY
│   ├── config/          TOML 配置
│   ├── secret/          keyring 凭据
│   ├── transfer/        文件传输引擎
│   ├── tunnel/          端口转发
│   ├── agent/           跳板链
│   ├── plugin/          WASM 插件宿主
│   ├── ai/              AI 辅助
│   └── ui/wailsbindings/ 暴露给前端的 Go 方法
├── pkg/                 可被外部 import 的公共类型
├── frontend/            React + TS + Tailwind
├── configs/             配置模板
├── docs/                设计 / 文档
└── scripts/             构建/安装脚本
```

> `internal/` 不可被外部 import；`pkg/` 可以。

---

## 5. 开发工作流

### 5.1 日常开发

```bash
# 编辑后端代码 → 自动重编（wails dev 自带）
# 编辑前端代码 → Vite HMR 自动热替换

# 跑测试
make test

# 跑 lint
make lint

# 修复 lint
make lint-fix

# 跑带覆盖率的测试
make cover
```

### 5.2 添加一个新模块

示例：加一个 `internal/foo` 模块。

```bash
mkdir -p internal/foo
cat > internal/foo/foo.go <<'GO'
// Package foo 提供示例功能。
package foo

// Greet 返回问候语。
func Greet(name string) string {
    return "hello, " + name
}
GO

# 写测试
cat > internal/foo/foo_test.go <<'GO'
package foo

import "testing"

func TestGreet(t *testing.T) {
    if got := Greet("moss"); got != "hello, moss" {
        t.Errorf("Greet() = %q, want %q", got, "hello, moss")
    }
}
GO

# 跑测试
go test ./internal/foo/...
```

### 5.3 添加一个 Wails binding（暴露给前端）

```go
// internal/foo/api.go
package foo

import "context"

// API 是暴露给前端的 API。
type API struct{}

// Greet 暴露给前端的方法。
// 注意：参数 / 返回值必须是 wails 能序列化的类型。
func (a *API) Greet(ctx context.Context, name string) (string, error) {
    return Greet(name), nil
}
```

```go
// internal/app/app.go
import "github.com/mossterm/mossterm/internal/foo"

type App struct {
    Foo *foo.API
    // ...
}

func New() *App {
    return &App{Foo: &foo.API{}}
}
```

```go
// internal/ui/wailsbindings/bind.go
func Bind(app *app.App) {
    bindings.Bind(app.Foo)  // 把 foo.API 暴露为 Foo.Greet
}
```

前端调用会自动生成到 `frontend/wailsjs/go/foo/API.d.ts`：

```ts
import { Greet } from "../../wailsjs/go/foo/API";
const msg = await Greet("moss");
```

---

## 6. 调试技巧

### 6.1 Go 后端

```bash
# Delve（推荐）
go install github.com/go-delve/delve/cmd/dlv@latest
dlv debug ./cmd/mossterm

# 在 VSCode 里：
#   - 安装 Go 扩展
#   - .vscode/launch.json 已有 Delve 配置
#   - F5 启动调试
```

日志在 stderr 输出（`make run` 时直接看；`wails dev` 在终端 tab 旁边）。

### 6.2 前端

- Chrome DevTools：`wails dev` 时右键 → Inspect
- React DevTools：装浏览器扩展
- 状态调试：Zustand devtools（`make dev` 时自动启用）

### 6.3 PTY / SSH 协议

```bash
# 调试 SSH 连接：开 trace
export MOSSTERM_SSH_TRACE=1
make dev

# 用 packet capture 抓 ssh 流量
tcpdump -i lo0 -w ssh.pcap port 22

# 复现某个 issue 时：打印可复现的 client hello
go run ./cmd/mossterm --debug --dump-config
```

### 6.4 凭据 / Keyring

```bash
# macOS：看 Keychain
security find-generic-password -s mossterm

# Linux：看 Secret Service
secret-tool search service mossterm

# Windows：凭据管理器 → Web 凭据 / Windows 凭据
```

---

## 7. 测试

### 7.1 单元测试

```bash
make test             # 全量
go test ./internal/sshclient/...   # 某个包
go test -run TestFoo ./...        # 某个测试
go test -race ./...               # race detector
```

### 7.2 集成测试（需要真实环境）

```bash
# build tag: integration
go test -tags=integration ./test/integration/...

# 起一个本地 sshd（macOS）
sudo systemsetup -setremotelogin on

# 用 docker 起一个 linux sshd
docker run -d -p 2222:22 --rm \
  -e USER_NAME=test -e USER_PASSWORD=test \
  -e PASSWORD_ACCESS=true \
  ghcr.io/linuxserver/openssh-server:latest
```

### 7.3 覆盖率

```bash
make cover
# 打开 coverage.html
```

CI 把 coverage 上传到 Codecov（见 `.github/workflows/ci.yml`）。

---

## 8. 构建与打包

### 8.1 本机构建

```bash
make build            # 当前平台 → bin/
make install          # 安装到 $GOBIN
```

### 8.2 跨平台构建

```bash
# Makefile
make build-darwin
make build-linux
make build-windows
make build-all

# 或脚本
./scripts/build.sh --all
./scripts/build.sh --platform=darwin/arm64,linux/amd64
./scripts/build.sh --all --clean --out=release/
```

### 8.3 Wails 桌面打包

```bash
make wailsbuild       # 完整 .app / .exe
# 产物在 build/bin/
```

打包配置在 `wails.json`。

---

## 9. 常见问题 FAQ

### Q1. `wails dev` 启动后白屏 / 红屏

**A**：

1. 检查终端：`wails doctor`
2. 浏览器单独打开前端 dev server（默认 `http://localhost:5173`）看控制台报错
3. 强制清理：
   ```bash
   rm -rf frontend/node_modules frontend/dist build/bin
   (cd frontend && npm install)
   make dev
   ```

### Q2. macOS 上 `cgo: C compiler not found`

**A**：装 Xcode Command Line Tools：

```bash
xcode-select --install
```

### Q3. Linux 上 `Package webkit2gtk-4.0 was not found`

**A**：

```bash
# Debian/Ubuntu
sudo apt-get install libwebkit2gtk-4.0-dev

# Fedora
sudo dnf install webkit2gtk4.0-devel
```

### Q4. Go build 报 `undefined: xxx` 但代码里明明有

**A**：

```bash
go clean -cache
go mod tidy
make build
```

### Q5. 前端 `npm install` 报 EACCES

**A**：

```bash
# 不要用 sudo；改用 nvm
# 或修权限
sudo chown -R $(whoami) ~/.npm
```

### Q6. 如何清理全部产物

```bash
make clean         # 删 dist/ bin/ coverage.*
make nuke          # 上面 + 清空 go 缓存（重下载依赖）
```

### Q7. `wails build` 在 macOS 上签名为 0

**A**：开发期是正常的。正式发布需要 Apple Developer 证书，配置 `wails.json` 的 `info` 字段。

### Q8. 怎么测跨平台但本机只有 mac

**A**：

```bash
# Linux：docker 静态构建
./scripts/build.sh --platform=linux/amd64 --docker

# Windows：用 GitHub Actions（见 .github/workflows/ci.yml）
```

### Q9. SSH 连接立刻断

**A**：

1. 看 `~/.ssh/known_hosts` 是否被改（提示 REMOTE HOST IDENTIFICATION HAS CHANGED）
2. 检查防火墙 / 代理
3. 启 trace：`MOSSTERM_SSH_TRACE=1 make dev`

### Q10. Keyring 写不进去

**A**：

- macOS：检查 Keychain Access 是否锁定
- Linux：缺 `gnome-keyring` 或 `kwallet`
  ```bash
  sudo apt-get install gnome-keyring
  ```
- Windows：检查凭据管理器是否被企业策略禁用

---

## 10. 进阶主题

- **插件开发**：见 `docs/plugin-dev.md`（待补）
- **AI 端点接入**：见 `docs/ai-providers.md`（待补）
- **架构演进 / RFC**：见 `docs/rfcs/`（待补）

---

## 11. 贡献流程

参见 [`CONTRIBUTING.md`](../CONTRIBUTING.md)。

> 苔藓不急，但一直在长。
> 你的每一个 PR，都是一滴雨。

---

🪴
