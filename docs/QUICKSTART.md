# MossTerm 5 分钟上手

> 从 0 到连上第一台服务器，刚好 5 分钟。

---

## 0. 前置：安装（仅需一次）

| 平台     | 命令                                                |
| -------- | --------------------------------------------------- |
| macOS    | `./scripts/install.sh --with-brew`                  |
| Linux    | `./scripts/install.sh`                              |
| Windows  | `.\scripts\install.ps1`（管理员 PowerShell）        |

> 没装脚本依赖也没关系：手动装 Go 1.22+、Node 18+、Wails v2 即可，参见 [`DEVELOPMENT.md`](./DEVELOPMENT.md)。

---

## 1. 启动（约 1 分钟）

```bash
git clone https://github.com/mossterm/mossterm.git
cd mossterm
make dev
```

第一次启动会编译几秒，看到桌面窗口弹出来就成功了 🪴。

---

## 2. 创建第一个 session（约 1 分钟）

> **session**：MossTerm 里的「一台服务器连接」。

1. 启动后左侧栏是空白的
2. 点击左上角 **+** 按钮（或按 `⌘ N` / `Ctrl N`）
3. 弹出「新建 Session」对话框，填写：

   | 字段        | 填什么                              | 例             |
   | ----------- | ----------------------------------- | -------------- |
   | Name        | 你能认出来的名字                    | `blog-server`  |
   | Host        | 服务器 IP 或域名                    | `1.2.3.4`      |
   | Port        | SSH 端口（默认 22）                 | `22`           |
   | User        | 登录用户名                          | `root`         |
   | Auth        | 认证方式                            | 见下 ↓         |

4. 选认证方式：

   - **Password**：弹窗输入密码（密码只存 Keyring，不入配置文件）
   - **Public Key**：选私钥路径（默认 `~/.ssh/id_ed25519` 或 `id_rsa`）
   - **Agent**：复用本机 ssh-agent（最推荐，零摩擦）

5. 点 **Save & Connect**

---

## 3. 连接 SSH（约 1 分钟）

- 新 tab 出现，xterm 跑起来
- 第一次连接如果服务端密钥未知，会弹「trust host key?」→ 选 **Yes**
- 如果用 Public Key，私钥有 passphrase 会弹窗问一次（记住后不再问）
- 看到 `$` 或 `#` 提示符 = 成功 🎉

试试在终端里：

```bash
uname -a
ls -la
```

### 快速命令面板

任何时候按 `⌘ K` / `Ctrl K`：

- 输 `> ` 跑命令
- 输 `?` 看帮助
- 输 `@host` 切到另一台

---

## 4. 打开 SFTP（约 1 分钟）

在连上的 session 里：

- 按 `⌘ P` / `Ctrl P` → 命令面板 → 选 `Open SFTP`
- **或** 直接按 `⌘ D` / `Ctrl D`（默认 SFTP 面板快捷键）
- **或** 顶栏点 **Split → SFTP Pane**

出现双窗格：

```
┌──────────────┬──────────────┐
│   Local      │   Remote     │
│  ~/          │  /           │
│              │              │
│  ← drag →    │  ← drag →    │
└──────────────┴──────────────┘
```

拖拽文件即传。右键支持：

- 上传 / 下载
- 重命名 / 删除
- 权限 / 属主修改（v0.5+）

---

## 5. 快捷键一览

### 全局

| 操作                | macOS       | Windows/Linux   |
| ------------------- | ----------- | --------------- |
| 新建 Session        | `⌘ N`       | `Ctrl N`        |
| 快速命令面板        | `⌘ K`       | `Ctrl K`        |
| 切换 Tab            | `⌃ Tab`     | `Ctrl Tab`      |
| 关闭 Tab            | `⌘ W`       | `Ctrl W`        |
| 打开设置            | `⌘ ,`       | `Ctrl ,`        |
| 退出                | `⌘ Q`       | `Ctrl Q`        |

### 终端

| 操作                | macOS       | Windows/Linux   |
| ------------------- | ----------- | --------------- |
| 搜索                | `⌘ F`       | `Ctrl F`        |
| 复制                | `⌘ C`       | `Ctrl C`        |
| 粘贴                | `⌘ V`       | `Ctrl V`        |
| 全选                | `⌘ A`       | `Ctrl A`        |
| 清屏                | `⌘ K`       | `Ctrl L`        |
| 放大字号            | `⌘ +`       | `Ctrl +`        |
| 缩小字号            | `⌘ -`       | `Ctrl -`        |
| 重置字号            | `⌘ 0`       | `Ctrl 0`        |
| 分屏                | `⌘ D`       | `Ctrl D`        |

### SFTP

| 操作                | macOS       | Windows/Linux   |
| ------------------- | ----------- | --------------- |
| 新建文件夹          | `⌘ Shift N` | `Ctrl Shift N`  |
| 删除                | `⌫`         | `Del`           |
| 重命名              | `Enter`     | `F2`            |
| 刷新                | `⌘ R`       | `Ctrl R`        |
| 显示隐藏文件        | `⌘ Shift .` | `Ctrl Shift .`  |

> 完整快捷键与自定义键位：见 [`docs/user-guide/keymap.md`](./user-guide/)（待补）。

---

## 6. 下一步

- 📖 看 [架构文档](./ARCHITECTURE.md) 了解 MossTerm 内部
- 🛠️ 想改 / 提 PR？看 [开发文档](./DEVELOPMENT.md)
- 🐛 踩坑了？搜 [GitHub Issues](https://github.com/mossterm/mossterm/issues)
- 💬 想聊天 / 提问？去 [GitHub Discussions](https://github.com/mossterm/mossterm/discussions)

---

> 苔藓第一步：先活着。
> 接下来，慢慢长。

🪴
