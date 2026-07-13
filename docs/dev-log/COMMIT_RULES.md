# MossTerm Commit 规范

> 规范 MossTerm 项目的 git 提交历史，确保每个版本都有清晰的 git 记录。

## 版本与 commit 的关系

| 范围 | 规则 |
|---|---|
| **v0.x / v1.x** | 每个新版本（v0.1.2、v0.2、v0.5、v1.0）作为**独立 commit** |
| **patch 版本**（v0.1.2、v0.1.3） | bug fix、build 修复、文档更新等小改动 |
| **minor 版本**（v0.2、v0.5） | 新功能、性能优化、breaking change |
| **major 版本**（v1.0、v2.0） | 跨多个 commit 的重大里程碑 |

## Commit Message 格式（Conventional Commits）

```
<type>(<scope>): <subject>

<body>

<footer>
```

### Type

| Type | 用途 | 示例 |
|---|---|---|
| `feat` | 新功能 | `feat(sftp): 实现 List / Read / Write 接口` |
| `fix` | bug 修复 | `fix(secret): argon2.Params 不存在，改用本地 Params` |
| `refactor` | 重构（既不是新功能也不是修 bug） | `refactor(session): 拆分 readLoop / writeLoop / fanoutLoop` |
| `perf` | 性能优化 | `perf(terminal): events 通道批处理 + 16ms 合并` |
| `docs` | 仅文档 | `docs(arch): 更新 v0.2 模块图` |
| `test` | 测试相关 | `test(session): 覆盖状态机 + 背压 + 关闭` |
| `build` | 构建系统/CI | `build(ci): 加 Linux ARM64 runner` |
| `chore` | 杂项 | `chore(gitignore): 忽略 frontend/build/` |

### Scope

可选，限定改动影响的模块。常用：

- `ssh` / `sftp` / `session` / `config` / `secret` / `agent` / `ui` / `wails` / `frontend`
- `arch` / `docs` / `ci` / `build`

### Subject

- 中文/英文均可，但**项目内保持一致**（MossTerm 暂用中文）
- 50 字符内
- 动词开头（实现 / 修复 / 重构 / 添加 / 升级）
- 不带句号

### Body

- 详细说明"为什么"改，而不是"改了什么"（diff 已经显示了"改了什么"）
- 列出关键设计决策
- 引用相关 issue / RFC / 文档

### Footer

- `BREAKING CHANGE: <description>` 标记不兼容变更
- `Closes #123` 关闭 issue
- `Refs: docs/dev-log/v0.x-xxx.md` 引用 dev-log

## 版本 Tag 规则

```bash
# 发版时打 tag
git tag -a v0.1.2 -m "v0.1.2: 接通 publickey auth"
git tag -a v0.2.0 -m "v0.2.0: 多 tab + SFTP 侧栏 + 跳板链"
```

- tag 名严格 `v<MAJOR>.<MINOR>.<PATCH>`
- 用 annotated tag (`-a`) 而不是 lightweight tag
- tag message 写一两句概述

## 例子：v0.1.2 commit

```bash
git add internal/sshclient/ internal/secret/
git commit -m "feat(ssh): 接通 publickey auth

把 sshclient.loadSigner 从 stub 改为真实实现：
  1. secret.Store.Get(keyID) 拿私钥 bytes
  2. ssh.ParsePrivateKeyWithPassphrase 解析
  3. 解析结果写入 signerCache

测试：
  - 加 ~/.ssh/id_ed25519
  - 用密码短语保护
  - openSession 用 AuthSpec{Kind: \"publickey\", KeyID: ...}
  - 验证能连

Closes: #5
Refs: docs/dev-log/v0.1.2-2026-XX-XX.md"
```

## Release Process

每个 minor/major 版本发布：

1. 更新 `CHANGELOG.md` 的 `## [Unreleased]` → `## [vX.Y.Z] - YYYY-MM-DD`
2. 写一个 `docs/dev-log/vX.Y.Z-YYYY-MM-DD.md` 记录
3. `git commit -m "release: v0.X.Y"`
4. `git tag -a v0.X.Y -m "v0.X.Y: <title>"`
5. `git push origin main --tags`

## 特殊：v0.0 / v0.1 / v0.1.1 合并 commit

历史原因：v0.0（骨架）、v0.1（核心实现）、v0.1.1（build 修复）三个阶段
在同一 commit `44d5824` 内提交，3 个 tag 指向同一 commit。

后续版本严格遵循"一个版本 = 一个 commit + 一个 tag"。
