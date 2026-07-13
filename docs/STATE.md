# MossTerm · 状态文档（详细版）

> **AGENTS.md 是给新会话的快速入门。**
> **本文件是 AGENTS.md 不够时的完整背景。**
> 
> 如果你不是从 AGENTS.md 进来的，请先读 `AGENTS.md`。

## 📖 项目一句话定位

MossTerm = **M**odular **O**pen **S**SH **S**olutions · **Term**inal
对标 WindTerm 的开源 SSH 客户端（Go + Wails + xterm.js）。
运维人员用的全协议终端，开源、可审计、可扩展。

**对标**：
- WindTerm（半开源、二进制 30MB、不透明）→ **完全开源、二进制 15-20MB、可审计**
- Tabby（Electron、120MB）→ **更小更快、纯 Go 二进制**

## 🎯 核心设计决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 语言 | Go | 单二进制、跨平台、生态好 |
| GUI | Wails 2 | Go 核心 + Web 前端，比 Electron 小 5-10x |
| 前端 | React 18 + TypeScript + xterm.js | 工业级终端、生态成熟 |
| 架构 | 协议无关 + 依赖注入 | 未来加 Telnet/Serial 不动核心 |
| 会话 | 3 goroutine 模型（readLoop/writeLoop/fanoutLoop）| 解耦 IO 与事件分发 |
| 状态 | 6 状态机（Connecting → ... → Closed/Failed）| 清晰生命周期 |
| 凭据 | 系统 keyring + 加密文件 fallback | 跨平台、不丢数据 |
| 协议 | SSH 优先（SFTP/Serial v0.5+） | SSH 是核心 |
| License | MIT | 简洁、兼容性强 |

## 📊 当前状态（2026-07-13）

| 项 | 状态 |
|---|---|
| 最新 commit | `61d26a8` |
| 最新 tag | `v0.2.2` |
| Go 代码行 | ~5000 |
| 总文件 | 101+ |
| 单元测试 | 41/41 通过 |
| 公开 API | 完全向后兼容 |
| 实际可运行 | 7.51MB ARM64 二进制（参考） |

### v0.x 完成度

| 版本 | 完成 | 内容 |
|---|---|---|
| v0.0 | ✅ | 架构 + 16 包骨架 + 25 基础设施文件 |
| v0.1 | ✅ | sshclient + session + pty + connect 真实实现 |
| v0.1.1 | ✅ | build 修复（17 个 bug） |
| v0.1.2 | ✅ | publickey auth 接通 |
| v0.1.3 | ✅ | known_hosts 持久化（消除 MITM 风险） |
| v0.1.4 | ✅ | keepalive（消除长 idle 断连） |
| v0.2.0 | ✅ | events 批处理（16ms + overflow 事件） |
| v0.2.1 | ✅ | Manager.Open 异步化（subscriber 看到完整状态） |
| v0.2.2 | ✅ | known_hosts OpenSSH 兼容（通配符/端口/IP 范围/多 host） |

## 🔐 安全基线

| 风险 | 当前状态 |
|---|---|
| MITM（host key 校验） | ✅ 已消除（v0.1.3） |
| 长 idle 断连 | ✅ 已消除（v0.1.4） |
| publickey 内存泄露 | ✅ 不存明文，只存已解析 ssh.Signer |
| 凭据泄露 | ✅ 系统 keyring + 加密文件 fallback |
| 关停时幽灵 overflow | ✅ 已修（v0.2.0） |
| first-use trust 无 GUI 确认 | ⚠️ 简化策略（v0.5+ 加弹窗） |
| known_hosts 大文件性能 | ⚠️ O(n) 扫（v0.5+ 改 trie） |

## 🛣️ 路线图

```
v0.0 ── v0.1 ── v0.1.1 ── v0.1.2 ── v0.1.3 ── v0.1.4  ✅ 完成
  │      │      │         │         │         │
  │      │      │         │         │         └─ keepalive
  │      │      │         │         └─ known_hosts 持久化
  │      │      │         └─ publickey auth
  │      │      └─ build 修复
  │      └─ 核心 SSH 通路
  └─ 架构 + 骨架

v0.2.0 ── v0.2.1 ── v0.2.2 ── ...  ✅ v0.2 系列完成
  │        │         │
  │        │         └─ known_hosts 完整 OpenSSH 兼容
  │        └─ Manager.Open 异步化
  └─ events 批处理

v0.5.0（下一个 milestone）
  - SFTP 客户端
  - 多 tab + split pane
  - 跳板链
  - first-use trust GUI
  - events 拥塞监控
  - stale event 修

v1.0.0
  - 全协议（Telnet / Serial / Tcp）
  - 性能压测（PTY 字节流 vs WindTerm）
  - 命令面板
  - sync input
  - 主题商店

v2.0.0
  - WASM 插件系统
  - AI 辅助
  - 团队配置同步
```

## 🔧 工作流约定（user-imposed）

### 1. 派活原则
- **能分就分**：所有非琐碎任务派给 sub-agent（`general` agent）
- 主理（我）只做**总集**：验证（build/vet/test）+ dev-log + commit + tag
- 例外：< 30 行、单文件、不需要测试的任务自己干

### 2. 文件操作权限
- **工作目录所有操作默认允许**（user 已明确授权）
- 不再询问授权确认

### 3. 文档归档

| 类型 | 目录 | 命名 |
|---|---|---|
| 版本 dev-log | `docs/dev-log/` | `v0.x-YYYY-MM-DD.md` |
| sub-agent 任务记录 | `docs/agent-logs/` | `v0.x-task-name.md` |
| 项目状态 | `docs/STATE.md` | (本文件) |
| AI 入口 | `AGENTS.md` | (项目根) |

### 4. 提交规范
- Conventional Commits：`feat` / `fix` / `perf` / `refactor` / `docs` / `test` / `build` / `chore`
- 中文 commit message
- 每个新版本独立 commit + tag

### 5. 测试
- 每个 task 包含单元测试
- 主理做总集时**必须跑** `go build` + `go vet` + `go test`
- 修任何发现的 bug（小弟的或历史的）

## 🛠️ 环境约束

### Go 环境
- Go 1.26.4 darwin/arm64
- 路径：`/opt/homebrew/bin/go`
- GOMODCACHE 默认指向 `~/Documents/go`（**沙盒写不进去**）

### 必备 export
```bash
export GOMODCACHE=$HOME/go/pkg/mod
export GOCACHE=$HOME/go/cache
export GOPATH=$HOME/go
export GOPROXY=https://goproxy.cn
export GOSUMDB=off
export PATH=/opt/homebrew/bin:$PATH
```

### Build 流程（关键！）
```bash
# 1. Documents 目录的 go embed 不可用 → cp 到 /tmp
cp -R /Users/chenwen/Documents/learning/go-prj/MossTerm /tmp/MossTerm
mkdir -p /tmp/MossTerm/cmd/mossterm/frontend/dist
cp /Users/chenwen/Documents/learning/go-prj/MossTerm/frontend/dist/index.html /tmp/MossTerm/cmd/mossterm/frontend/dist/

# 2. build
cd /tmp/MossTerm && go build -o /tmp/mossterm-bin ./cmd/mossterm

# 3. 验证
go vet ./...
go test ./...
```

## ⚠️ 已踩过的坑

| 坑 | 修复 |
|---|---|
| `golang.org/x/crypto` v0.33+ 移除了 argon2 类型 | 锁 v0.22.0 |
| `ssh.NewClient` v0.33+ 拆两步 | 锁 v0.22.0 |
| `wails 2.9.2` 依赖 `leaanthony/u v1.4.0` 镜像缺失 | 升 wails 2.12.0 + 改 leaanthony/u v1.1.1 |
| macOS 沙盒拒绝 `~/Documents/go/pkg/mod` 写 | export GOMODCACHE=$HOME/go |
| `embed frontend/dist` 报"no matching files" | 在 `cmd/mossterm/frontend/dist/` 放占位 index.html |
| `ssh.HostKeyCallback` v0.22+ 返回 `error` 不是 `bool` | 全用 error 语义 |
| `*ssh.Session` 不直接是 `io.ReadWriteCloser` | 拿 StdinPipe/StdoutPipe 包成 |
| `*ssh.Session.Pid` 不存在 | 永远返回 0 |
| Go 1.26 不允许 named func type 隐式转换 | 用 `type X = Y` alias |
| `sync.Once.CompareAndSwap` 在某版本移除 | 用 `atomic.Bool` 替代 |
| `connect.HostKeyCallback` 不接受 `ssh.HostKeyCallback` | type alias |

## 🐛 顺手修的 v0.1.x bug（history）

- **v0.1.1**：17 个 build/API 错位（详见 `docs/agent-logs/v0.1.1-build.md`）
- **v0.2.0**：`tryPublish` 关停时 1/3 概率误报 overflow（Go runtime 多 case select 均匀随机选）
- **v0.2.1**：`Info().State` 状态缓存（v0.1.x 改为初始 state 后再没更新）+ `waitForSess` StateFailed 死循环
- **v0.2.2**：`wildcardMatch("*", "")` 循环开头先判空 str 提前返回

## 📁 完整目录结构

```
MossTerm/
├── AGENTS.md                  # AI 入口（本会话第一句读这个）
├── README.md                  # 用户向 README
├── LICENSE                    # MIT
├── CHANGELOG.md               # 按版本号
├── Makefile
├── go.mod / go.sum
├── cmd/mossterm/              # 启动入口
│   ├── main.go
│   ├── frontend/dist/         # embed 占位
│   └── wailsbindings/         # 暴露给前端的 API
├── internal/
│   ├── app/                   # 生命周期
│   ├── session/                # 业务层会话（异步 + events 批处理 + state machine）
│   ├── connect/                # 协议无关连接抽象
│   ├── sshclient/              # SSH 协议实现（含 keepalive）
│   ├── sftpclient/             # SFTP 客户端（v0.5 计划）
│   ├── pty/                    # PTY 封装
│   ├── config/                 # TOML 配置管理
│   ├── secret/                 # 凭据存储
│   ├── knownhosts/             # host key 校验（自实现 + OpenSSH 兼容）
│   ├── transfer/               # 文件传输（v0.5 计划）
│   ├── tunnel/                 # 端口转发（v0.5 计划）
│   ├── agent/                  # 跳板链（v0.5 计划）
│   ├── plugin/                 # WASM 插件（v2.0 计划）
│   └── ai/                     # AI 辅助（v2.0 计划）
├── frontend/                  # React + xterm.js（48 文件 4230 行）
├── pkg/mossterm/              # 公共 API
├── docs/
│   ├── ARCHITECTURE.md         # 1364 行架构
│   ├── DEVELOPMENT.md          # 开发指南
│   ├── QUICKSTART.md           # 快速上手
│   ├── STATE.md                # 详细状态（本文件）
│   ├── dev-log/                # 按版本号的 dev-log
│   ├── agent-logs/             # 按 task 的交付记录
│   ├── user-guide/             # 用户文档
│   └── overview.html           # 架构总览图
├── configs/                   # 配置示例
├── scripts/                   # 安装/构建/开发脚本
└── .github/workflows/         # CI
```

## 🎓 关键经验（pass-down）

1. **x/crypto 升级破坏性极大**：在 go.mod 锁紧版本
2. **embed 路径相对 main.go 所在目录**：不是项目根
3. **macOS Documents 沙盒**：export GOMODCACHE 到 ~/go
4. **Go 1.26 严格类型**：named func type 用 type alias
5. **派活优先于自己干**：除非 < 30 行
6. **每个 sub-agent 任务都包含测试**：主理做总集时跑全量 test
7. **dev-log 写关键设计决策 + 边界 + 教训**：不只写"做了什么"

## 🆘 紧急恢复

如果完全失忆：
1. 读 `AGENTS.md`（项目根）
2. 读 `docs/STATE.md`（本文件）
3. `cd /Users/chenwen/Documents/learning/go-prj/MossTerm && git log --oneline --decorate -20`
4. 看 `docs/ARCHITECTURE.md` 了解架构
5. 看 `docs/agent-logs/` 了解历史 task

如果想继续推进：
1. 选一个 v0.5 候选
2. 派活给 `general` agent
3. 同步到 /tmp build
4. 主理做总集：build/vet/test + dev-log + commit + tag

---

> 🪴 *Moss is green — 这是苔藓的根系，新会话从这里生长。*
