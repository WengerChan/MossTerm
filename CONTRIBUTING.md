# 贡献指南

> 感谢你愿意让这片苔藓长大一点点。
> 任何形式的贡献都受到欢迎：bug 报告、功能建议、文档改进、代码 PR、翻译、测试。

---

## 1. 行为准则

参与本项目前请阅读 [`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md)。
所有互动都应保持尊重、友善、建设性。

---

## 2. 报告 Issue

在提 issue 前：

1. **搜索** 现有 issues，避免重复
2. 准备 **最小可复现** 步骤（系统、MossTerm 版本、配置片段）
3. 收集 **日志 / 截图**（注意打码敏感信息）

Issue 模板会引导你填写：

| 类型         | 适用场景                              |
| ------------ | ------------------------------------- |
| Bug Report   | 软件行为不符合预期                    |
| Feature      | 新功能建议                            |
| Question     | 使用问题 / 文档疑问                   |
| Discussion   | 架构选型 / RFC 类大方向讨论            |
| Security     | 安全漏洞（**请勿**公开，参考下节）    |

### 安全漏洞

**请勿**在公开 issue 报告安全漏洞。请私下联系维护者：
`security@mossterm.dev`（占位邮箱，正式上线前替换）。

---

## 3. 提交 Pull Request

### 3.1 流程

1. Fork 仓库 → 创建特性分支（`git checkout -b feat/awesome-thing`）
2. 完成改动 → 写测试 → `make fmt vet lint test` 全部通过
3. 写清晰的 commit message（见 §5）
4. 推送到你的 fork → 在 GitHub 提 PR
5. 等待 CI 通过 → 维护者 review → 合并

### 3.2 PR 检查清单

- [ ] 代码风格符合 `gofmt` + `golangci-lint`
- [ ] 单元测试覆盖新逻辑（target ≥ 60%）
- [ ] 公开 API 的导出符号带 GoDoc
- [ ] CHANGELOG.md 的 `[Unreleased]` 已更新
- [ ] 涉及 UI 的改动附带截图 / 录屏
- [ ] 不包含调试代码 / 注释掉的代码 / 私有密钥

### 3.3 Review SLA

- 首次 review 在 **7 个工作日** 内给到
- 维护者会请求改动（request changes）或直接批准
- 长期无响应的 PR 会被标记 `stale`，30 天后可能关闭

---

## 4. 开发环境搭建

完整步骤见 [`docs/DEVELOPMENT.md`](./docs/DEVELOPMENT.md)。速览：

```bash
# 0. 准备依赖
go version        # 1.22+
node -v           # 18+
wails version     # v2.9+

# 1. 克隆
git clone https://github.com/mossterm/mossterm.git
cd mossterm

# 2. 拉前端依赖
(cd frontend && npm install)

# 3. 跑测试
make test

# 4. 启动开发模式（HMR + 自动重编）
make dev
```

可执行 `make help` 查看所有可用目标。

---

## 5. 代码规范

### 5.1 Go

- **格式化**：`gofmt` + `goimports`（`make fmt`）
- **Lint**：`golangci-lint run`（`make lint`）
- **静态检查**：`go vet`（`make vet`）
- **命名**：
  - 包名小写、单词、避免下划线（`package session`，非 `package session_manager`）
  - 公开 API 必须有 GoDoc
  - 错误信息小写、不带标点（`fmt.Errorf("read config: %w", err)`）
- **错误处理**：禁止 `_ = someFunc()` 吞错；`errcheck` 强制
- **并发**：channel / mutex 走惯例；导出的字段需注明「非并发安全」
- **CGO**：能避免就避免；CGO 文件命名 `_cgo.go` 或 `_unix.go` / `_windows.go`

### 5.2 TypeScript / 前端

- **格式化**：`prettier`（`npx prettier --write .`）
- **Lint**：`eslint`（`npm run lint`）
- **类型**：`strict: true`，禁止 `any`（`@typescript-eslint/no-explicit-any: error`）
- **命名**：
  - 组件 PascalCase（`SessionList.tsx`）
  - 函数 / 变量 camelCase
  - 常量全大写下划线（`MAX_SPLIT_PANES`）
- **状态**：使用 Zustand store，**禁止**直接 mutate
- **样式**：Tailwind utility class，组件层用 `cva`（class-variance-authority）

### 5.3 目录约定

```
cmd/mossterm/         主入口
internal/             业务实现（禁止外部 import）
pkg/                  可被外部 import 的公共类型
frontend/             React + xterm.js
configs/              配置模板（提交 .example.toml，忽略真实 config.toml）
docs/                 设计 / 文档
scripts/              开发/构建脚本
```

---

## 6. 提交信息规范

遵循 [Conventional Commits 1.0](https://www.conventionalcommits.org/zh-hans/)。

### 格式

```
<type>(<scope>): <subject>

<body>

<footer>
```

### Type

| Type       | 说明                                         |
| ---------- | -------------------------------------------- |
| `feat`     | 新功能                                       |
| `fix`      | bug 修复                                     |
| `docs`     | 仅文档变更                                   |
| `style`    | 格式（不影响代码含义：空格、分号等）         |
| `refactor` | 既不修 bug 也不加功能的重构                 |
| `perf`     | 性能优化                                     |
| `test`     | 增加/修改测试                                |
| `build`    | 构建系统或外部依赖变更（go.mod、CI 等）     |
| `ci`       | CI 配置文件变更                              |
| `chore`    | 其他不修改 src 或测试的杂项                  |
| `revert`   | 回滚某次提交                                 |

### Scope

指明影响的模块（可选），例如 `ssh`、`sftp`、`ui`、`config`、`ci`、`docs`。

### Subject

- 中文 / 英文皆可，**项目内统一**（推荐中文）
- 不超过 72 字符
- 祈使语气，现在时：「新增」而非「新增了」
- 首字母不大写，末尾不加句号

### 示例

```
feat(ssh): 支持 Ed25519 私钥登录

- internal/sshclient 增加 ed25519 分支
- 配置文件新增 identity_file 字段

Closes #42
```

```
fix(sftp): 修复大目录列表时的 OOM

使用 streaming + 分页替代一次性 ReadDir。

Fixes #88
```

```
docs(readme): 补充 macOS 安装截图
```

### Breaking Change

破坏性变更必须在 footer 标注 `BREAKING CHANGE:`：

```
feat(api): 重命名 session.New 为 session.Create

BREAKING CHANGE: 旧 API session.New 移除，调用方需改用 session.Create
```

---

## 7. 发布流程（维护者）

1. 确认 `[Unreleased]` 段已更新
2. 执行 `make version` 检查 tag
3. 切到 main → `git tag -a vX.Y.Z -m "release: vX.Y.Z"`
4. `git push origin vX.Y.Z` 触发 release workflow
5. 在 GitHub Releases 写 changelog（从 CHANGELOG.md 复制）

---

## 8. 社区

- GitHub Discussions：提问、想法、Show & Tell
- GitHub Issues：明确的 bug / feature
- 邮件列表：`dev@mossterm.dev`（占位）

> 苔藓生长需要时间。我们更看重 **持续贡献** 而非单次巨献。
> 一周一个 5 行 PR，比一年一次大重写更有价值。

---

谢谢！🪴
