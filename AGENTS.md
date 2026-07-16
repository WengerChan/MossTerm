# MossTerm · AI Agent Entry Point

> **MossTerm = Modular Open SSH Solutions · Terminal**
> 对标 WindTerm 的开源 SSH 客户端（Go + Wails + xterm.js）
> 项目根目录：`/Users/chenwen/Documents/learning/go-prj/MossTerm/`

## 🚀 新会话第一件事

读 `docs/STATE.md` 了解完整背景。然后 `git log --oneline --decorate -15` 看最新进度。

## 🎯 用户工作流约定（**核心**）

1. **能分就分**：所有非琐碎任务（>30 行代码改动、跨文件、需要测试）派给 sub-agent（`general` agent），我（主理）只做总集（验证 + dev-log + commit + tag）
   - **小改（<30 行 / 单文件 / 不需测试）直接自己 Edit**，省去 sub-agent 上下文交接成本
   - **Edit 优于 Write**：禁止 Write 整文件覆盖（哪怕只改 1 行也走 Edit `old_string` / `new_string` 精准 patch）
   - **派发 sub-agent 时 prompt 头部显式声明**："禁止 Write 整文件覆盖，必须用 Edit 精准 patch；先 read 上下文再改"
2. **不主动删已有 stub**：stub 留着，后续接
3. **文件操作不再询问授权**：工作目录下所有操作默认允许
4. **每个新版本独立 commit + tag**：tag 形式 `v0.x.y`，commit message 用 Conventional Commits（中文）
   - **commit 后不做 push、不做 tag push**：由用户自己推到 github（避免误触发 release workflow）
   - 汇报时说"已 commit `073e251`"而不是"已 push"
5. **dev-log 写到 `docs/dev-log/v0.x-YYYY-MM-DD.md`**，agent-logs 写到 `docs/agent-logs/`（**两个目录职责不同，别混**）
6. **候选项要标 plus plan 消耗**（MossTerm 用户身份是 minimax plus plan，2026-07-15 起）：任何提供 ≥2 个候选的场景，**每个选项同步给"预计消耗 plus plan 额度"百分比**；百分比是量级预估（sub-agent 派 1 轮 ≈ 5-8%，本会话完成 ≈ 2-3%，CI 跑一轮 ≈ 1-2%）
7. **token 不足是正常的**：用户的 plus plan 有上限，sub-agent / 自己 quota 撞上限是正常现象；不去排查其他原因（网络/认证/模型限制等）；用户说"继续"时从上次 token 不足的地方开始接手，像派发 sub-agent 任务一样安排剩下的工作

## 📊 当前进度

- **最新 tag**: v0.5.12（CI 修复：gofmt + golangci v1.64 schema + govulncheck 漏洞）
- **最新 commit**: `07e4ac3`
- **分支**: main
- **可执行二进制**: `/tmp/mossterm-bin` (v0.5.10 验证 build)
- **测试**: 8 packages ok / race clean / tsc 0 / eslint 0 / transfer 100MB 集成 32s
- **真实 build 需要**: `cp -R` 到 `/tmp/MossTerm_v5xx` 再 build（Documents 目录沙盒限制）
- **CI/CD**: `.github/workflows/{ci,release}.yml` 已修订 — push/PR 跑 CI，tag `v*` 自动发版
- **GitHub 排查 token**: 存于 `~/.mavis/secrets/github.env`（`GITHUB_TOKEN`）

## 🛣️ 下一步候选

| 版本 | 内容 | 难度 | 备注 |
|---|---|---|---|
| v0.6.0 | 跳板链 (multi-hop) | 大 | `internal/agent/` 骨架就位，差实质逻辑 |
| v0.6.1 | 端口转发 (local/remote forward) | 大 | `internal/tunnel/` 骨架就位，差实质逻辑 |
| v0.6.2 | SFTP download 流式 | 中 | 复用 `internal/transfer/streaming.go` 架构（v0.5.10） |
| v0.6.3 | macOS code signing + 公证 | 中 | 分发前置 |
| v0.6.4 | 真实 PDF 渲染 | 中 | v0.5.9 留 best-effort，接 pdf.js 或自实现 spec |
| v0.6.5 | Pane 树持久化 | 中 | v0.5.8 留 v0.6+，等 config 持久化先到位 |
| v0.6.6 | x/crypto 升 v0.40+ | 大 | 解 v0.33+ 限制，secret 包需适配（argon2 签名 + ssh.NewClient 拆两步） |

## 🔧 CI 必过要点（v0.5.7 → v0.5.14 经验，6h 17 push 失败总结）

### 1. Go 工具链
- `go.mod` 与 CI `GO_VERSION` 必须同步（当前 Go 1.25）
- 升 Go 必须同步升 `golangci-lint-action@v7` + 锁 v2.10.0+
  （v1.64.8 编译用 Go 1.24，lint Go 1.25 必挂）

### 2. govulncheck 漏洞基线
- `x/crypto` 必须 **v0.52+**（6 个 ssh/agent 漏洞要求）
- stdlib `GO-2025-4011/4010/4009/4007` 要求 Go ≥ 1.24.8
- 仍会剩 stdlib `GO-2026-4xxx/5xxx`（要求 1.25.x+），**当前 unfixed-by-toolchain，不阻断**

### 3. wails 跨平台 build（v2.12.0 + Go 1.25）
- `go.mod` `wailsapp/wails/v2` 必须 **v2.12.0**（v2.8.1 cgo hardcode `webkit2gtk-4.0`）
- 官方 wails v2.x **不支持 webkit2gtk-4.1**（issue #3345 未合并）
- Ubuntu 24.04 (noble) 必须**加 jammy 源**装 `libwebkit2gtk-4.0-dev`：
  ```bash
  sudo add-apt-repository -y "deb http://archive.ubuntu.com/ubuntu jammy main universe"
  sudo apt-get update && sudo apt-get install -y libwebkit2gtk-4.0-dev
  ```
- wails 输出用默认 `build/bin/`，归档前 cp 到 `dist/`：
  - darwin: `build/bin/mossterm.app/` → `dist/mossterm.app/`
  - linux/win: `build/bin/<bin>` → `dist/<bin>`

### 4. CI yaml 通用坑
- **Windows runner 默认 pwsh**，`if [[ ... ]]` / `shell: bash` 是 bash 语法
  - 必加 `shell: bash` 或换 PowerShell 写法
- `matrix.platform` 含 `/`（如 `linux/amd64`），拼 filename 要 `replace('/', '-')`：
  ```bash
  PLATFORM_FLAT="${PLATFORM//\//-}"
  tar -czf "mossterm-${PLATFORM_FLAT}.tar.gz" "$PAYLOAD"
  ```
- `actions/setup-node@v4` `cache: npm` 模式要：
  ```yaml
  cache-dependency-path: frontend/package-lock.json
  ```
  漏了报 "Dependencies lock file is not found"

### 5. 本地沙盒限制
- 项目在 macOS `Documents/` 下，`go mod download` 经常 EPERM
- **必须在 `/tmp/MossTerm_ci` 跑**（`cp -R` 项目到 /tmp）：
  ```bash
  cp -R /Users/chenwen/Documents/learning/go-prj/MossTerm /tmp/MossTerm_ci
  cd /tmp/MossTerm_ci
  export GOMODCACHE=$HOME/go/pkg/mod GOCACHE=$HOME/go/cache GOPATH=$HOME/go
  export GOPROXY=https://goproxy.cn GOSUMDB=off
  go mod download && go vet ./... && go build ./... && go test -race ./...
  go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.0
  golangci-lint run ./...
  ```
- **本地全过 ≠ CI 过**（CI 在线 schema verify、跨平台 race、macOS 沙盒路径）

### 6. windows CI race + shuffle flake
- `session/manager_test.go` 三处 `waitForStateEvent` timeout 已放宽到 1s
- 写新 session 测试时 default upper bound **至少 1s**
- 别用 100/500ms（windows runner + Go 1.25 + race detector 偶尔 >50ms）

### 7. 改 workflow / 升 Go / 升 lint 后
- `python3 + ruamel.yaml` YAML 1.2 strict 验证
- `gofmt -w .` 走一遍
- `/tmp/MossTerm_ci` 全跑：vet / build / test -race / govulncheck / golangci-lint run
- **新增 commit 前所有 cron 监控都清掉**（避免后续 push 重复触发报警）

> 注：v0.5.14 阶段的 golangci-lint v2 schema 细节（`output.formats` 是 map、`formatters`/`exclusions` 重组等）已剔除——等升 v3 时再单独整理。完整历史仍在 `CI_CHECKLIST.md`。

## ⚠️ 不要踩的坑

- **`go build` 在 Documents 目录下失败**：必须 `cp -R` 到 `/tmp/MossTerm_test` 再 build
- **GOMODCACHE 默认指向 `~/Documents/go/pkg/mod`**（沙盒写不进去）：export `GOMODCACHE=$HOME/go/pkg/mod` + `GOCACHE=$HOME/go/cache` + `GOPATH=$HOME/go` + `GOPROXY=https://goproxy.cn` + `GOSUMDB=off`
- **`go build` ≠ `wails build`**：裸 `go build` 只验 Go 编译，**不会**打 webview 资源；
  完整桌面包必须用 `wails build`。`cmd/mossterm/frontend/dist/index.html` 是
  占位文件（让 `go build` 能过），`wails build` 会覆盖它
- **x/crypto 锁 v0.31.0**（v0.33+ 移除了 argon2 类型、ssh.NewClient 拆两步等；v0.31 是最后一个保留旧 API 的稳定版）
- **wails 锁 2.12.0**（2.9.2 依赖的 leaanthony/u v1.4.0 国内镜像没缓存）
- **ssh.HostKeyCallback v0.22+ 返回 error 不是 bool**（v0.1.1 写错过）
- **Go 1.26 严格类型**：named func type 不能隐式转换，用 `type X = Y` alias
- **`stubSession.Read` 阻塞**（`internal/session/manager_test.go`）：v0.2.0a 起 readLoop
  在 reader 退出时会自动 `sessionImpl.Close`，把 state 推到 Closed；改回立即返回
  `io.EOF` 会让 `TestOpen_AsyncReturnsBeforeDial` 再次 fail
- **mockEmitter FIFO 语义**（`internal/knownhosts/knownhosts_test.go`）：`waitForCall`
  必须 pop 最早一条，**不能**固定返回 `calls[0]` —— 否则多次 emit 后会拿到过期 ID
- **`OpenSession` 顺序**（`internal/sshclient/client.go`）：`StdinPipe/StdoutPipe` 必须在
  `Shell()` **之前**调（x/crypto v0.22.0 的 `Session.StdinPipe` 在 `started==true` 后返回
  error，而 `Shell()` 内部 `s.start()` 会把 `started` 置 true）。v0.1 起就写反了，v0.5.1
  写 in-process SSH server 才第一次发现

## 📁 关键目录

- `main.go` — 启动入口（v0.5.7 从 `cmd/mossterm/` 移到项目根）
- `internal/sshclient/` — SSH 协议实现
- `internal/knownhosts/` — host key 校验（自实现，OpenSSH 兼容）
- `internal/session/` — 业务层会话（异步化 + events 批处理 + state machine）
- `internal/connect/` — 协议无关连接抽象
- `internal/secret/` — 凭据存储（keyring + 加密文件）
- `internal/sftpclient/` — SFTP 客户端（含 preview magic 分类，v0.5.9）
- `internal/transfer/` — 传输抽象（streaming upload + manifest + 断点续传，v0.5.10）
- `internal/agent/` — 跳板链（v0.6.0 待实质化）
- `internal/tunnel/` — 端口转发（v0.6.1 待实质化）
- `internal/ui/wailsbindings/` — 前端 API
- `frontend/src/components/tabs/paneTree.ts` — Pane 树算法（v0.5.8 抽离）
- `frontend/src/components/sftp/` — SFTP 浏览器 + PreviewPanel + UploadProgress
- `docs/ARCHITECTURE.md` — 架构设计
- `docs/dev-log/` — 按版本号的开发日志
- `docs/agent-logs/` — 按 task 派发顺序的交付记录

## 🔍 怎么继续

```bash
cd /Users/chenwen/Documents/learning/go-prj/MossTerm
git log --oneline --decorate -15   # 看最新 15 个 commit
git tag -l                         # 看所有 tag
cat docs/STATE.md                  # 读完整背景（如果 AGENTS.md 不够）
cat docs/ARCHITECTURE.md | head    # 了解架构
```

如果新会话不知道 MossTerm 是什么，**第一句话应该是**：
> "我读 AGENTS.md 了。继续推进 v0.x..."

---

> 🪴 Moss is green.
