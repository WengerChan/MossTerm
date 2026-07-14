# MossTerm · AI Agent Entry Point

> **MossTerm = Modular Open SSH Solutions · Terminal**
> 对标 WindTerm 的开源 SSH 客户端（Go + Wails + xterm.js）
> 项目根目录：`/Users/chenwen/Documents/learning/go-prj/MossTerm/`

## 🚀 新会话第一件事

读 `docs/STATE.md` 了解完整背景。然后 `git log --oneline --decorate -15` 看最新进度。

## 🎯 用户工作流约定（**核心**）

1. **能分就分**：所有非琐碎任务（>30 行代码改动、跨文件、需要测试）派给 sub-agent（`general` agent），我（主理）只做总集（验证 + dev-log + commit + tag）
2. **不主动删已有 stub**：stub 留着，后续接
3. **文件操作不再询问授权**：工作目录下所有操作默认允许
4. **每个新版本独立 commit + tag**：tag 形式 `v0.x.y`，commit message 用 Conventional Commits（中文）
5. **dev-log 写到 `docs/dev-log/v0.x-YYYY-MM-DD.md`**，agent-logs 写到 `docs/agent-logs/`（**两个目录职责不同，别混**）

## 📊 当前进度

- **最新 tag**: v0.5.2（OpenSession 端到端 test + 并发 trust）
- **最新 commit**: 见 `git log --oneline -1`（提交后回填 hash）
- **分支**: main
- **可执行二进制**: `/tmp/mossterm-v052` (7.64 MB ARM64)
- **测试**: 94/95 通过 + 1 SKIP（race detector 干净，+8 PASS vs v0.5.1）
- **真实 build 需要**: `cp -R` 到 `/tmp/MossTerm_test` 后跑 `go build`（Documents 目录沙盒限制）

## 🛣️ 下一步候选

| 版本 | 内容 | 难度 |
|---|---|---|
| v0.5.3 | profile 编辑 UI（manager 早好了只差前端） | 中 |
| v0.5.3 | SFTP 真实分页（pkg/sftp 替代 ReadDir 模拟） | 中 |
| v0.5.3 | SFTP binary preview + 拖拽上传 | 中 |
| v0.6.0 | 跳板链 (multi-hop) | 大 |
| v0.6.0 | 端口转发 (local/remote forward) | 大 |
| v0.6.0 | SFTP 集成到 Pane 树（双 pane: 左 SSH / 右 SFTP） | 中 |
| v0.6.0 | x/crypto 升级到 v0.31+（社区版已稳定 argon2） | 小 |
| v0.6.0 | Wails v2 → v3（如有重大收益） | 中 |

## ⚠️ 不要踩的坑

- **`go build` 在 Documents 目录下失败**：必须 `cp -R` 到 `/tmp/MossTerm_test` 再 build
- **GOMODCACHE 默认指向 `~/Documents/go/pkg/mod`**（沙盒写不进去）：export `GOMODCACHE=$HOME/go/pkg/mod` + `GOCACHE=$HOME/go/cache` + `GOPATH=$HOME/go` + `GOPROXY=https://goproxy.cn` + `GOSUMDB=off`
- **`go build` ≠ `wails build`**：裸 `go build` 只验 Go 编译，**不会**打 webview 资源；
  完整桌面包必须用 `wails build`。`cmd/mossterm/frontend/dist/index.html` 是
  占位文件（让 `go build` 能过），`wails build` 会覆盖它
- **x/crypto 锁 v0.22.0**（v0.33+ 移除了 argon2 类型、ssh.NewClient 拆两步等）
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

- `cmd/mossterm/main.go` — 启动入口
- `internal/sshclient/` — SSH 协议实现
- `internal/knownhosts/` — host key 校验（自实现，OpenSSH 兼容）
- `internal/session/` — 业务层会话（异步化 + events 批处理 + state machine）
- `internal/connect/` — 协议无关连接抽象
- `internal/secret/` — 凭据存储（keyring + 加密文件）
- `internal/ui/wailsbindings/` — 前端 API
- `frontend/` — React + xterm.js
- `docs/ARCHITECTURE.md` — 架构设计（1364 行）
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
