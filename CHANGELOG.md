# 更新日志

本项目的所有重要变更都会记录在此文件。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

> 苔藓不急，但它一直在长。

## [Unreleased]

### Added
- 初始化项目骨架：后端 Go + Wails + 前端目录占位
- 架构设计文档（[`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md)）
- 基础设施文件：Makefile、CI、配置模板、贡献指南、行为准则
- GitHub Actions CI 矩阵：ubuntu / macOS / windows × lint / test / build

### v0.1 · 核心 SSH 通路（进行中）

**目标**：能 SSH 进去看东西。

**已完成**：

#### 后端核心（Task A）
- `internal/sshclient` — `Connector.Dial` / `OpenSession` 真实实现
  - 两阶段拨号（TCP + SSH 握手）
  - `RequestPty` / `Setenv` / `Shell` 完整流程
  - `sshConn` / `sshSession` 包装类型
  - `authMethods` 支持 password / agent / keyboard-interactive（publickey 待 secret.Store 接通）
- `internal/pty/pty_unix.go` — creack/pty 平台实现
  - `unixFactory` + `unixPTY`（Read/Write/Resize/PID/TTYName）
  - 默认 80x24，TERM 强制 `xterm-256color`
- `internal/pty/pty_windows.go` — Windows stub（编译通过，运行时返回 not implemented）
- `internal/connect/auth_convert.go` — `AuthMethodFromSpec` 把 `session.AuthSpec` 转 `connect.AuthMethod`
- `internal/session` — `MemoryManager.Open` 真实实现
  - `sessionImpl` 完整实现 `Session` 接口
  - 三 goroutine 模型：readLoop / writeLoop / fanoutLoop
  - atomic 状态同步 + events 通道 + 订阅者 fanout
  - closeOnce 保护 + 优雅关停
  - 输入背压：inputCh cap 64，满时返回 `ErrInputFull`
- `internal/app/wire.go` — `WireDefaultConnectors` 集中注册 sshclient factory
- `internal/session/dial_convert.go` — `DialParamsFrom` 把 `OpenRequest` 转 `connect.DialParams`

#### 绑定层 + 应用整合（Task B）
- `internal/config/manager.go` — Manager 完整实现
  - CRUD：AddProfile / UpdateProfile / DeleteProfile / GetProfile / ListProfiles
  - 首次启动自动写默认 + 复制 `config.example.toml`
  - Save / Load / Update / SetSetting
- `internal/config/loader.go` — 路径解析、默认数据工厂
- `internal/secret/keyring.go` — 系统 keyring 实现
  - `keyringStore` 包装 `zalando/go-keyring`
  - 内存 unlockedCache，Close 时清零
- `internal/secret/file.go` — 加密文件 fallback
  - AES-256-GCM + Argon2id 派生
  - JSON 持久化
- `internal/agent/agent.go` — 跳板链 registry（v0.1 空注册表）
- `internal/ui/wailsbindings/api.go` — 全部方法真实实现
  - Session：ListSessions / OpenSession / CloseSession / SendInput / ResizePTY
  - Profile：ListProfiles / SaveProfile / DeleteProfile
  - Secret：ListSecretsItems / SaveSecret / GetSecretContent
- `internal/app/app.go` — `EventEmitter` interface + `Emit` helper + `OnDomReady` 触发 `app:ready`
- `cmd/mossterm/main.go` — 完整启动流程
  - 配置 → 凭据 → agent → connector → session → app → wails → 信号处理

### Changed
- License 从 Apache-2.0 统一为 MIT（与 README / LICENSE 一致）
- `session.MemoryManager` 加 `WithConnectors` 注入器

### Known Limitations
- ⚠️ Host key 校验默认放行（`ssh.InsecureIgnoreHostKey`，v0.2 接入 known_hosts）
- ⚠️ Keepalive 注释掉了（v0.2 启用）
- ⚠️ events 通道满会丢事件（`session:overflow` 事件类型已定义，v0.2 实现）
- ⚠️ `Manager.Open` 同步执行 dial，订阅者看不到 `Connecting` / `Authenticating` 中间状态
- ⚠️ SFTP 客户端未实现（推到 v0.5）

## [v0.1.2] - 2026-07-13

### Added
- `connect.Deps.Secrets` 字段，注入 secret.Store 到 sshclient
- `connect.PublicKeyAuth.KeyID` 字段，支持"未解析 + 延迟加载"模式
- `sshclient.Connector.loadSigner(keyID, passphrase)` 真实实现：缓存 → secret.Get → loadSignerFromBytes → 写缓存
- `session.MemoryManager.WithSecrets(sec)` 注入器
- `cmd/mossterm/main.go` 调用 `WithSecrets(sec)` 把凭据存储接入 session manager

### Changed
- `authMethods` 从 free function 改为 `*Connector` 方法，能用 `c.loadSigner`
- `connect.AuthMethodFromSpec` publickey 路径返回 `PublicKeyAuth{KeyID, Passphrase}`（不再返回 "not yet wired" 错误）
- `session.MemoryManager.Open` 构造 `connect.Deps` 时填 `Secrets: m.secrets`

### Fixed
- publickey auth 端到端通路打通：Profile.Kind="publickey" + KeyID 即可登录

### Security
- publickey 私钥在内存中只存已解析的 `ssh.Signer`，不存明文
- `signerCache` 是 per-connector LRU（cap 64），不是全局共享

详细 dev-log：[`docs/dev-log/v0.1.2-2026-07-13.md`](./docs/dev-log/v0.1.2-2026-07-13.md)

---

## 版本说明模板

后续发布时，按以下模板新增章节：

```markdown
## [X.Y.Z] - YYYY-MM-DD

### Added
- 新增了 X 功能

### Changed
- 改动了 Y 行为

### Deprecated
- 标记了 Z 为不推荐

### Removed
- 移除了 Q

### Fixed
- 修复了 bug #N

### Security
- 修复了漏洞 CVE-XXXX-XXXX
```

[Unreleased]: https://github.com/mossterm/mossterm/compare/main...HEAD
