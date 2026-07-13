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

## [v0.2.0] - 2026-07-13

### Added
- events 批处理：16ms ticker + 64KB accumulator
  - 1000+ events/sec 压缩到 ~60 events/sec
  - 典型终端吞吐上限 4MB/s，超出触发 overflow
- `session:overflow` 事件类型
  - `Event.OverflowBytes int64` 字段
  - `EventTypeOverflow` 常量 + `newOverflowEvent` helper
  - fanoutLoop 旁路 emit（不走 events 通道）
- `internal/session/session_impl_test.go` — 6 个单元测试

### Changed
- `readLoop` 重写为 reader goroutine + main loop 双层结构
  - reader goroutine 持续 `sess.Read(32KB)`
  - dataCh(cap=8) 把 data 传给 main loop
  - main loop accumulator + ticker 16ms flush
  - 超 64KB 立即 flush（避免单次 broadcast 引发前端卡顿）
- `tryPublish` 累计 overflow 字节数（不再静默丢）
- `fanoutLoop` 每次 broadcast 后调 `maybeEmitOverflow`

### Fixed
- 🐛 `tryPublish` 关停时 1/3 概率误报 overflow（v0.1.4 就有）
  - Go runtime 在多 case select 中均匀随机选，1/3 概率选到 default
  - 修复：default 分支加内层 `select { case <-s.done: ... default: ... }` 区分

### Performance
- 1000+ events/sec → ~60 events/sec（16ms 批处理 + 64KB accumulator）
- events 通道打满概率显著降低
- 单 session 稳态吞吐 4MB/s（典型终端 < 100KB/s）

详细 dev-log：[`docs/dev-log/v0.2.0-2026-07-13.md`](./docs/dev-log/v0.2.0-2026-07-13.md)

## [v0.1.4] - 2026-07-13

### Added
- `internal/sshclient/keepalive.go` — `runKeepAlive` 协程 + 3s 超时
- `internal/sshclient/keepalive_test.go` — 3 个单元测试
- `(*Connector).Close()` — 关闭 keepalive 协程，`sync.Once` 保护幂等

### Changed
- `Dial` 启用 keepalive 协程（之前被注释）
- `Connector` 新增 `done chan struct{}` + `closeOnce sync.Once` 字段
- `New` 初始化 `done` 通道 + 启用时打 INFO 日志

### Security
- 长 idle 连接不再被中间设备单方面断开
- 网络断连通过 keepalive 失败在 3s 内感知

### Known Limitations
- ⚠️ SendRequest 超时靠 goroutine + Timer，timeout 触发时启动 goroutine 短暂泄漏（v0.33+ 升级到 ctx 版可彻底解决）
- ⚠️ 单元测试未覆盖 SendRequest 失败路径（要起真实 ssh.Server，留给 v0.2）

详细 dev-log：[`docs/dev-log/v0.1.4-2026-07-13.md`](./docs/dev-log/v0.1.4-2026-07-13.md)

## [v0.1.3] - 2026-07-13

### Added
- `internal/knownhosts` 包：known_hosts 持久化 + 智能 HostKeyCallback
  - 自动创建文件（父目录 0700，文件 0600）
  - 解析 OpenSSH 格式（忽略 marker / 通配符）
  - `HostKeyCallback()` 返回 `ssh.HostKeyCallback`（error 语义）
  - `Add(host, key, comment)` 显式添加
  - `Authorize(host, key)` 显式校验
  - `ErrHostUnknown` / `ErrHostKeyMismatch` 错误
- `connect.Deps.KnownHosts *knownhosts.Manager` 字段
- `sshclient.Connector.knownHosts` 字段 + 3 级 host key 优先级解析
- `session.MemoryManager.WithKnownHosts(kh)` 注入器
- `cmd/mossterm/main.go` 初始化 known_hosts（默认 `~/.config/mossterm/known_hosts`）
- 5 个 knownhosts 单元测试（empty path / 创建文件 / Add+callback / load / 持久化）

### Changed
- `sshclient.New` 不再做 `InsecureIgnoreHostKey` 兜底（已移到 `New` 集中处理）
- host key 校验逻辑三层优先级：KnownHosts > HostKeyCb > 兜底

### Security
- 🔴 **消除 MITM 风险**：host key 不再默认放行
- 首次连接自动信任 + 写入（first-use trust）
- host key 改变时**明确拒绝**并返回 `ErrHostKeyMismatch`
- v0.1.1 / v0.1.2 的 `InsecureIgnoreHostKey` 兜底仅在 `KnownHosts=nil && HostKeyCb=nil` 时生效

### Known Limitations
- ⚠️ 不支持 OpenSSH 完整格式（通配符 / 端口 / IP 范围）—— v0.2 评估
- ⚠️ first-use trust 无 GUI 确认 —— v0.2 加弹窗

详细 dev-log：[`docs/dev-log/v0.1.3-2026-07-13.md`](./docs/dev-log/v0.1.3-2026-07-13.md)

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

详细 dev-log：[`docs/dev-log/v0.1.2-2026-07-13.md`](./docs/dev-log/v0.1.2-2026-07.13.md)

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
