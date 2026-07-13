# MossTerm Agent Logs

> 按 milestone 归档的 sub-agent 任务交付记录。

## 与 dev-log 的区别

| 目录 | 组织方式 | 内容 | 回答的问题 |
|---|---|---|---|
| `docs/dev-log/` | 按 **版本号 v0.x** | 代码改动 + 设计决策 + commit message | v0.x 改了哪些代码？怎么设计的？ |
| `docs/agent-logs/`（本目录） | 按 **task 派发顺序** | 派活 prompt + 关键交付物 + commit hash | v0.x 那个功能是派给谁干的？怎么溯源？ |

**类比**：
- `dev-log/` = git log（按 commit 看）
- `agent-logs/` = 团队会议纪要（按"谁负责什么"看）

## 命名规范

每个 task 一个 `.md` 文件：
- `v0.x-task-name.md`（如 `v0.1.2-publickey.md`）
- 同一阶段多 task 用 `-A` / `-B` 区分（如 `v0.1-core-A.md`）

## 索引

| 文件 | 阶段 | 任务 | commit |
|---|---|---|---|
| `v0.0-arch.md` | 骨架 | 架构设计 | 44d5824 |
| `v0.0-backend.md` | 骨架 | 后端 16 包 stub | 44d5824 |
| `v0.0-frontend.md` | 骨架 | 前端 React 骨架 | 44d5824 |
| `v0.0-infra.md` | 骨架 | Makefile / CI / 脚本 | 44d5824 |
| `v0.1-core-A.md` | 核心 | sshclient + session + pty + connect | 44d5824 |
| `v0.1-core-B.md` | 核心 | wailsbindings + cmd + config + secret | 44d5824 |
| `v0.1.1-build.md` | 修 bug | 17 个 build / API 错位修复 | 44d5824 |
| `v0.1.2-publickey.md` | v0.1.2 | 接通 publickey auth | 51e208b |
| `v0.1.3-known_hosts.md` | v0.1.3 | 接入 known_hosts | 7575f14 |
| `v0.1.4-keepalive.md` | v0.1.4 | 启用 keepalive | 6a2b83f |
| `v0.2.0-events.md` | v0.2.0 | 16ms events 批处理 + overflow | 58bcd22 |

## 每个文件包含什么

精简记录（30-50 行）：
- **派活时间 / 目标**：一句话说明
- **任务描述**：3-5 个 bullet
- **关键设计决策**：1-3 个
- **改动统计**：文件数 / 行数
- **commit hash + tag**：可一键跳转
- **review 发现**：bug / 边界 / 教训

## 为什么单独建

- task 工具的 sub-agent 完成任务后会清理，但**会话历史**仍占空间
- 把核心信息固化到文件系统：人可查、git 可追溯、agent 团队不臃肿
- 未来如果有新成员加入，看 agent-logs 能 30 分钟了解每个 task 的来龙去脉
