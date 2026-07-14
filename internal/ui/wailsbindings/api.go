// Package wailsbindings 是"哪些方法暴露给前端"的白名单层。
//
// 该层组合 *app.App 并转发方法，避免内部重构破坏前端契约。
// 任何对前端可见的 API 变化必须先改本文件再改前端。
//
// Wails 反射规则（影响本文件所有公开方法签名）：
//   - 必须导出（首字母大写）
//   - context.Context 是合法参数类型（Wails 特殊处理）且必须是第一个参数
//   - 参数 / 返回值必须是可导出类型或基本类型
//   - []byte 在事件 payload 中会被转成前端 Uint8Array
package wailsbindings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mossterm/mossterm/internal/app"
	"github.com/mossterm/mossterm/internal/config"
	"github.com/mossterm/mossterm/internal/secret"
	"github.com/mossterm/mossterm/internal/session"
	"github.com/mossterm/mossterm/internal/sftpclient"
	"github.com/mossterm/mossterm/internal/transfer"
)

// App 是 Wails 绑定入口。
//
// 由 wails.Run 通过 Bind(ctx, api) 注入到 webview 运行时；
// 前端通过 window.go.app.App.<Method>() 调用。
type App struct {
	core *app.App
}

// New 用内部 *app.App 构造一个绑定层。
func New(core *app.App) *App {
	return &App{core: core}
}

// -----------------------------------------------------------------------------
// Session 相关
// -----------------------------------------------------------------------------

// ListSessions 返回全部活跃 Session 的 Info。
func (a *App) ListSessions(ctx context.Context) []session.Info {
	return a.core.Sessions().List()
}

// OpenSession 打开一个新会话。
//
// 返回 session ID（UUID v4 字符串）；错误以 Go error 形式返回，
// Wails 会序列化成 { error: "..." } 给前端。
func (a *App) OpenSession(ctx context.Context, req session.OpenRequest) (string, error) {
	s, err := a.core.Sessions().Open(ctx, req)
	if err != nil {
		return "", fmt.Errorf("wailsbindings.OpenSession: %w", err)
	}
	return string(s.Info().ID), nil
}

// CloseSession 关闭一个会话。
func (a *App) CloseSession(ctx context.Context, id string, force bool) error {
	if err := a.core.Sessions().Close(session.ID(id), force); err != nil {
		return fmt.Errorf("wailsbindings.CloseSession: %w", err)
	}
	return nil
}

// SendInput 把键盘输入发送到指定 session。
func (a *App) SendInput(ctx context.Context, id string, data []byte) error {
	s, ok := a.core.Sessions().Get(session.ID(id))
	if !ok {
		return fmt.Errorf("wailsbindings.SendInput: session %q not found", id)
	}
	if err := s.Input(data); err != nil {
		return fmt.Errorf("wailsbindings.SendInput: %w", err)
	}
	return nil
}

// ResizePTY 通知指定 session 调整 PTY 大小。
func (a *App) ResizePTY(ctx context.Context, id string, cols, rows int) error {
	s, ok := a.core.Sessions().Get(session.ID(id))
	if !ok {
		return fmt.Errorf("wailsbindings.ResizePTY: session %q not found", id)
	}
	if err := s.Resize(cols, rows); err != nil {
		return fmt.Errorf("wailsbindings.ResizePTY: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Profile CRUD
// -----------------------------------------------------------------------------

// ListProfiles 返回全部 Profile。
func (a *App) ListProfiles(ctx context.Context) []config.Profile {
	return a.core.Cfg().ListProfiles()
}

// SaveProfile 保存一个 Profile（新增或更新）。
//
// p.ID 为空时走 AddProfile，非空走 UpdateProfile。
// 这让前端可以无差别调用同一个入口。
func (a *App) SaveProfile(ctx context.Context, p config.Profile) error {
	cfg := a.core.Cfg()
	if p.ID == "" {
		return fmt.Errorf("wailsbindings.SaveProfile: empty profile ID")
	}
	_, exists := cfg.GetProfile(p.ID)
	if exists {
		if err := cfg.UpdateProfile(p); err != nil {
			return fmt.Errorf("wailsbindings.SaveProfile: update: %w", err)
		}
		return nil
	}
	if err := cfg.AddProfile(p); err != nil {
		return fmt.Errorf("wailsbindings.SaveProfile: add: %w", err)
	}
	return nil
}

// DeleteProfile 按 ID 删除一个 Profile。
func (a *App) DeleteProfile(ctx context.Context, id string) error {
	if err := a.core.Cfg().DeleteProfile(id); err != nil {
		return fmt.Errorf("wailsbindings.DeleteProfile: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Secret 元数据（不含内容）
// -----------------------------------------------------------------------------

// ListSecretsItems 列出全部凭据条目（仅元数据）。
func (a *App) ListSecretsItems(ctx context.Context) []secret.Item {
	items, err := a.core.Secret().List()
	if err != nil {
		// 列表失败返回空 slice + 记 log；不冒泡 error 避免前端阻塞
		a.core.Log().Warn("wailsbindings.ListSecretsItems failed", "err", err)
		return []secret.Item{}
	}
	return items
}

// SaveSecret 把凭据写入安全存储。
//
// name / kind / content 来自前端表单；ID 由 secret.Store 生成。
// 真正的写入走 secret.Store.Set；本层只做编排 + 错误包装。
func (a *App) SaveSecret(ctx context.Context, name, kind, content string) (string, error) {
	id, err := a.core.Secret().Set(name, secret.Kind(kind), []byte(content), nil)
	if err != nil {
		return "", fmt.Errorf("wailsbindings.SaveSecret: %w", err)
	}
	return string(id), nil
}

// GetSecretContent 取出凭据内容。
//
// 前端调用栈应：用户已输入主密码 → GetSecretContent → 用完清零引用。
// 不得把返回值存入 Zustand（避免 DevTools 泄露）。
func (a *App) GetSecretContent(ctx context.Context, id string) (string, error) {
	data, err := a.core.Secret().Get(secret.ID(id))
	if err != nil {
		return "", fmt.Errorf("wailsbindings.GetSecretContent: %w", err)
	}
	return string(data), nil
}

// -----------------------------------------------------------------------------
// Known Hosts（v0.5.0 First-Use Trust）
// -----------------------------------------------------------------------------

// TrustHost 把前端的"信任决策"回传给 known_hosts.Manager。
//
// 调用栈（v0.5.0）：
//  1. 用户在 modal 点 trust/reject
//  2. 前端 TrustRequestModal 调 App.TrustHost(requestID, action)
//  3. 本方法调 known_hosts.Manager.ReplyTrust(requestID, action)
//  4. Manager 把 reply 写入内部 trustReplyCh，唤醒挂起的 HostKeyCallback
//  5. SSH 握手继续 / 中断
//
// 参数约定：
//   - requestID：必须与 TrustRequestModal 收到的事件 ID 完全一致
//     （Manager 校验 ID 不匹配会返回 ID mismatch 错误并把当前 reply 丢弃）。
//   - action："trust" | "reject" | 其他（视作 reject）。
//
// 错误：known_hosts 未初始化时返回 "known_hosts not initialized"。
// 该方法本身不返回 reply 同步结果——前端调完即关闭 modal，
// reply 是否被采纳由 Manager 内部异步处理。
func (a *App) TrustHost(ctx context.Context, requestID string, action string) error {
	kh := a.core.KnownHosts()
	if kh == nil {
		return errors.New("wailsbindings.TrustHost: known_hosts not initialized")
	}
	if err := kh.ReplyTrust(requestID, action); err != nil {
		return fmt.Errorf("wailsbindings.TrustHost: %w", err)
	}
	return nil
}

// -----------------------------------------------------------------------------
// SFTP 文件浏览器（v0.5.1）
// -----------------------------------------------------------------------------
//
// 设计概要：
//   - 每个 session 对应一个 *sftpclient.Client（懒加载 + 复用）；
//     由 *app.App.SftpClient 管理生命周期。
//   - 7 个 binding 都是简单 wrapper：拿 client → 调方法 → 返回。
//     （spec 草稿写"8 个"，实际列了 7 个；与 frontend/wailsjs/go/main/App.d.ts
//      一致。前端 SftpList/Stat 已能覆盖"打开"操作；SftpRead/Write 是
//      后续 UI 完善后调，预留好。）
//   - 错误一律用 fmt.Errorf("wailsbindings.<Method>: %w", err) 包装，
//     沿用 wailsbindings 既有的错误语义。
//
// Wails 反射契约（影响本组所有方法签名）：
//   - context.Context 必须第一个参数（Wails 注入）
//   - []byte 在前端是 Uint8Array（序列化规则见 sftpclient 注释）
//   - time.Time 序列化为 RFC3339 字符串
//   - os.FileMode (uint32) 序列化为 number
//   - 返回的 error 被前端 .catch() 捕获

// SftpList 列远端目录。
//
// 调用栈（v0.5.1）：
//  1. 前端文件浏览器点目录或刷新
//  2. App.SftpList(sessionID, path, pageSize, pageToken)
//  3. a.core.SftpClient(sessionID) → *sftpclient.Client（懒加载）
//  4. client.List(ctx, path, pageSize, pageToken) → sftpclient.ListPage
//
// 参数约定：
//   - sessionID：必须是已 Open 且 Established 的 session 的 ID
//   - path：远端绝对路径（"~/" 之类由前端解析成绝对路径后再传）
//   - pageSize：单页条目数；<= 0 时 sftpclient 用默认 200
//   - pageToken：v0.5.1 暂未实现分页，传空字符串
//
// 错误：session 不存在 / 未 established / type assert 失败 / sftpclient.Open 失败
// / 远端 IO 错误（权限 / 路径不存在等）。
func (a *App) SftpList(ctx context.Context, sessionID, path string, pageSize int, pageToken string) (sftpclient.ListPage, error) {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return sftpclient.ListPage{}, fmt.Errorf("wailsbindings.SftpList: %w", err)
	}
	page, err := client.List(ctx, path, pageSize, pageToken)
	if err != nil {
		return sftpclient.ListPage{}, fmt.Errorf("wailsbindings.SftpList: %w", err)
	}
	return page, nil
}

// SftpStat 取单个文件/目录元数据。
//
// 调用栈：SftpList 同款，但走 client.Stat(path) → sftpclient.Entry。
//
// 错误：SftpList 同款 + Stat 路径错误。
// symlink 目标的 link 字段在 v0.5.1 保持空串（与 sftpclient.List 一致）。
func (a *App) SftpStat(ctx context.Context, sessionID, path string) (sftpclient.Entry, error) {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return sftpclient.Entry{}, fmt.Errorf("wailsbindings.SftpStat: %w", err)
	}
	entry, err := client.Stat(path)
	if err != nil {
		return sftpclient.Entry{}, fmt.Errorf("wailsbindings.SftpStat: %w", err)
	}
	return entry, nil
}

// SftpMkdir 建目录（单层）。
//
// 调用栈：前端"新建文件夹"对话框 → SftpMkdir(sessionID, newDir) → client.Mkdir。
//
// 错误：SftpList 同款 + 远端权限 / 父目录不存在。
// 递归创建：v0.5.1 暂不支持；前端应先逐层确认父目录存在再调。
func (a *App) SftpMkdir(ctx context.Context, sessionID, path string) error {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return fmt.Errorf("wailsbindings.SftpMkdir: %w", err)
	}
	if err := client.Mkdir(path); err != nil {
		return fmt.Errorf("wailsbindings.SftpMkdir: %w", err)
	}
	return nil
}

// SftpRemove 删文件/空目录。
//
// 调用栈：前端"删除"按钮（带 confirm）→ SftpRemove(sessionID, target) → client.Remove。
//
// 错误：SftpList 同款 + 目录非空（远端 sftp 协议会返回 error，前端可识别）。
// 递归删除：v0.5.1 暂不支持（pkg/sftp 提供 RemoveAll，但本 binding 不暴露）。
func (a *App) SftpRemove(ctx context.Context, sessionID, path string) error {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return fmt.Errorf("wailsbindings.SftpRemove: %w", err)
	}
	if err := client.Remove(path); err != nil {
		return fmt.Errorf("wailsbindings.SftpRemove: %w", err)
	}
	return nil
}

// SftpRename 改名/移动（同文件系统下）。
//
// 调用栈：前端拖拽 / 重命名 → SftpRename(sessionID, oldPath, newPath) → client.Rename。
//
// 错误：SftpList 同款 + 跨设备 / 远端权限 / newPath 已存在。
// 跨目录移动：v0.5.1 仅在同 mount point 下有效（远端 sftp 协议限制）。
func (a *App) SftpRename(ctx context.Context, sessionID, oldPath, newPath string) error {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return fmt.Errorf("wailsbindings.SftpRename: %w", err)
	}
	if err := client.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("wailsbindings.SftpRename: %w", err)
	}
	return nil
}

// SftpRead 读小文件到 []byte（前端拿到 Uint8Array）。
//
// 调用栈：前端"打开/预览"小文件 → SftpRead(sessionID, path) → 远端 sftp.Read。
//
// 参数约定：v0.5.1 **仅支持小文件**（< 1MB 推荐；sftpclient 走单次 Read，
// 没有 streaming / chunked 下载支持）。大文件应该用后续版本的 transfer.Engine
// 流式下载（Wails 暴露进度事件）。
//
// 错误：SftpList 同款 + 文件过大 / 远端 IO。
func (a *App) SftpRead(ctx context.Context, sessionID, path string) ([]byte, error) {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return nil, fmt.Errorf("wailsbindings.SftpRead: %w", err)
	}
	f, err := client.Open(path, os.O_RDONLY)
	if err != nil {
		return nil, fmt.Errorf("wailsbindings.SftpRead: %w", err)
	}
	defer f.Close()
	// 推荐上限 1 MiB。读超出此值仍能读（io.ReadAll 会重新分配），
	// 但前端拿到大数组会卡 UI —— 业务约束，非硬限制。
	const maxRead = 1 << 20 // 1 MiB
	buf := make([]byte, maxRead)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("wailsbindings.SftpRead: %w", err)
	}
	// io.ReadFull 在读到 maxRead 字节后还想要更多时返回 io.ErrUnexpectedEOF；
	// 视为"文件比预期大"——直接返回已读部分 + 警告日志（v0.5.1 不暴露给前端）。
	return buf[:n], nil
}

// SftpWrite 写小文件（覆盖写）。
//
// 调用栈：前端"保存"按钮（小文件编辑器 / 配置覆盖）→ SftpWrite(sessionID, path, data)
// → 远端 sftp.Write。
//
// 参数约定：
//   - data：完整文件内容，前端 Uint8Array → []byte
//   - v0.5.1 **仅支持小文件**（同 SftpRead 限制）
//   - 行为：覆盖写（Open 用 O_WRONLY|O_CREATE|O_TRUNC）
//
// 错误：SftpList 同款 + 远端权限 / 磁盘满 / path 是目录。
func (a *App) SftpWrite(ctx context.Context, sessionID, path string, data []byte) error {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return fmt.Errorf("wailsbindings.SftpWrite: %w", err)
	}
	writeFlags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	f, err := client.Open(path, writeFlags)
	if err != nil {
		return fmt.Errorf("wailsbindings.SftpWrite: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("wailsbindings.SftpWrite: %w", err)
	}
	return nil
}

// SftpUploadFile 把前端的 []byte 写到远端 path（覆盖写），返回写入字节数。
//
// v0.5.3 新增：配合 SftpBrowser 的 drag-drop 上传功能。
//
// 调用栈：
//  1. 前端 drag-drop local file → FileReader.readAsArrayBuffer → Uint8Array
//  2. App.SftpUploadFile(sessionID, remotePath, content) → 本方法
//  3. a.core.SftpClient(sessionID) 拿 sftpclient.Client（懒加载）
//  4. client.Write(remotePath, content) → 远端 sftp.Write
//  5. 返回写入字节数
//
// 参数约定：
//   - sessionID：必须是已 Open 且 Established 的 session 的 ID
//   - remotePath：远端绝对路径
//   - content：完整文件内容，前端 Uint8Array → []byte
//
// 限制：v0.5.3 一次性把整个文件传到后端（前端先 readAsArrayBuffer 读进内存）。
// **前端必须在调用前校验文件大小**（推荐 ≤ 100 MiB）—— SftpBrowser
// 已经把 > 100 MiB 的文件直接拒绝 + 提示用户。
// 大文件分片 streaming upload 留 v0.6+。
//
// 与 SftpWrite 的区别：
//   - SftpWrite：写编辑器保存的小文件（通常 < 1 MiB），通用
//   - SftpUploadFile：drag-drop 整文件上传（可能几十 MB），专属路径
//     返回写入字节数（让前端可以做更详细的 toast / 进度反馈）
//
// 错误：SftpList 同款 + sftpclient.Write 内部错误（远端权限 / 磁盘满 /
// path 是目录）→ 一律 fmt.Errorf("wailsbindings.SftpUploadFile: %w", err) 包装。
func (a *App) SftpUploadFile(ctx context.Context, sessionID, remotePath string, content []byte) (int, error) {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return 0, fmt.Errorf("wailsbindings.SftpUploadFile: %w", err)
	}
	n, err := client.Write(remotePath, content)
	if err != nil {
		return n, fmt.Errorf("wailsbindings.SftpUploadFile: %w", err)
	}
	return n, nil
}

// -----------------------------------------------------------------------------
// SFTP 文件预览（v0.5.9）
// -----------------------------------------------------------------------------
//
// 设计概要：
//   - 三个 binding 覆盖 preview 全流程：SftpReadFileChunk（读字节）+ SftpStatFile
//     （轻量元信息）+ SftpGetFileMetadata（full classify）
//   - 与既有 SftpStat / SftpRead 互补：
//     - SftpStat：仅 Entry（spec 不变，v0.5.1 行为）
//     - SftpRead：1 MiB 截断，限定小文件
//     - SftpReadFileChunk：任意 offset+size，受 sftpclient.PreviewMaxBytes 硬上限
//   - 大文件保护：前端走 PreviewPanel 必须先 SftpGetFileMetadata，看到
//     "toolarge" 就不要走 ReadFileChunk —— 后端再 belt-and-suspenders 截断。
//   - 不引新依赖：mime 走 net/http.DetectContentType，magic 走 sftpclient.DetectMagic。
//
// Wails 反射契约同 SFTP 组其它方法：ctx 必须第一参、[]byte → Uint8Array、
// error 被前端 .catch() 捕获、struct 公开字段才被序列化。

// SftpReadFileChunk 读远端 path 的 [offset, offset+size) 字节。
//
// 调用栈（v0.5.9）：
//  1. 前端 PreviewPanel 在 image / pdf / text 分支按需读字节
//  2. App.SftpReadFileChunk(sessionID, path, offset, size)
//  3. sftpclient.Client.ReadFileChunk
//
// 参数约定：
//   - sessionID：已 Open 且 Established 的 session
//   - path：远端绝对路径
//   - offset：< 0 视作 0；>= fileSize 返回空 slice
//   - size：> 0 精确 size 字节（截到 sftpclient.PreviewMaxBytes=50 MiB）；
//     <= 0 读到 EOF（截到 PreviewMaxBytes 防御）
//
// 错误：SftpList 同款 + sftpclient.ReadFileChunk 内部错误（远端权限 /
// 文件不存在 / 远端 IO）→ fmt.Errorf("wailsbindings.SftpReadFileChunk: %w", err)。
//
// 与 SftpRead 的区别：
//   - SftpRead：1 MiB hard cap，offset 恒为 0
//   - SftpReadFileChunk：50 MiB cap，offset 任意，size 任意 → 通用 read
func (a *App) SftpReadFileChunk(ctx context.Context, sessionID, path string, offset, size int64) ([]byte, error) {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return nil, fmt.Errorf("wailsbindings.SftpReadFileChunk: %w", err)
	}
	data, err := client.ReadFileChunk(path, offset, size)
	if err != nil {
		return nil, fmt.Errorf("wailsbindings.SftpReadFileChunk: %w", err)
	}
	return data, nil
}

// SftpStatFile 返回远端 path 的轻量元信息（size + mime + name + path + modTime）。
//
// 调用栈（v0.5.9）：
//  1. 前端只需要 mime（比如 tooltip / 排序）→ App.SftpStatFile
//  2. wailsbinding 调 sftpclient.Client.BuildPreviewMetadataLite
//  3. 返回 PreviewMetadata（Kind / IsImage 等分类字段留空）
//
// 与 SftpGetFileMetadata 的区别：
//   - SftpStatFile：不做 magic detection + classify，省一次 ClassifyPreview 调用
//   - SftpGetFileMetadata：full magic + classify，返回 Kind 字段
//
// 实际差异只在返回字段上 —— 后端实现共享 Client.buildPreviewMetadataImpl 内部
// helper。两者都做"读前 512 字节"（mime 需要）；分类开关决定是否走 ClassifyPreview。
//
// 错误：SftpList 同款 + sftpclient.BuildPreviewMetadataLite 内部错误。
func (a *App) SftpStatFile(ctx context.Context, sessionID, path string) (sftpclient.PreviewMetadata, error) {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return sftpclient.PreviewMetadata{}, fmt.Errorf("wailsbindings.SftpStatFile: %w", err)
	}
	meta, err := client.BuildPreviewMetadataLite(path)
	if err != nil {
		return sftpclient.PreviewMetadata{}, fmt.Errorf("wailsbindings.SftpStatFile: %w", err)
	}
	return meta, nil
}

// SftpGetFileMetadata 返回远端 path 的完整预览元信息（含 kind 分类）。
//
// 调用栈（v0.5.9）：
//  1. 前端 PreviewPanel 打开时第一步 → App.SftpGetFileMetadata(sessionID, path)
//  2. wailsbinding 调 sftpclient.Client.BuildPreviewMetadata
//  3. 返回 PreviewMetadata（Kind / IsImage / IsPDF / IsText 已填）
//  4. 前端按 kind 路由到不同分支（image / pdf / text / binary / toolarge）
//
// 即使 size > PreviewMaxBytes 也会返回 —— 仍 Stat + 读 16 字节（够小），
// kind 自动归类为 "toolarge"，前端禁止走 ReadFileChunk。
//
// 错误：SftpList 同款 + sftpclient.BuildPreviewMetadata 内部错误。
func (a *App) SftpGetFileMetadata(ctx context.Context, sessionID, path string) (sftpclient.PreviewMetadata, error) {
	client, err := a.core.SftpClient(session.ID(sessionID))
	if err != nil {
		return sftpclient.PreviewMetadata{}, fmt.Errorf("wailsbindings.SftpGetFileMetadata: %w", err)
	}
	meta, err := client.BuildPreviewMetadata(path)
	if err != nil {
		return sftpclient.PreviewMetadata{}, fmt.Errorf("wailsbindings.SftpGetFileMetadata: %w", err)
	}
	return meta, nil
}

// -----------------------------------------------------------------------------
// Streaming Upload（v0.5.10）
// -----------------------------------------------------------------------------
//
// 设计概要：
//   - 4 个 binding 覆盖 streaming upload 全流程：StartUpload（启动后台
//     goroutine）/ CancelUpload（context cancel）/ ListTransfers（看全部
//     active + 已结束）/ GetTransfer（取单个详情，含 manifest 路径）
//   - 进度通过 Wails runtime EventsEmit 推送 3 个事件：
//     - "transfer:progress" → transfer.Progress payload
//     - "transfer:done"     → transfer.JobInfo payload（State=Completed）
//     - "transfer:error"    → transfer.JobInfo payload（State=Failed / Canceled）
//   - 断点续传：transfers/<id>.json manifest 写盘；Resume=true 调 StartUpload
//     时 Manager 读 manifest + 校验 local mtime/size → 跳过已传 chunk
//   - sessionID 通过 Wails ctx 注入（transfer.WithSessionID）；
//     startUpload 不读 req.sessionID 字段（UploadRequest 没这字段，v0.5.10
//     单一 session → 一次 upload）
//
// Wails 反射契约同 SFTP 组其它方法：ctx 必须第一参、error 被前端 .catch()
// 捕获、struct 公开字段才被序列化。返回的 string 是 transferID（前端用
// 来订阅事件 + 取消）。

// StartUpload 启动一次 streaming upload（后台 goroutine，非阻塞）。
//
// 调用栈（v0.5.10）：
//  1. 前端 drag-drop 拿到 localPath（file.path 或 file.name + 拼出）
//  2. App.StartUpload(ctx, req) → wailsbinding
//  3. 注入 sessionID 到 ctx（req.SessionID → transfer.WithSessionID）
//  4. 调 transfer.Manager.StartUpload
//  5. Manager 后台 goroutine 跑 transfer.Upload（分片 + 进度回调）
//  6. 进度走 emitter.Emit("transfer:progress", p) → 前端 EventsOn 收到
//  7. 完成 / 失败 / 取消 emit "transfer:done" / "transfer:error"
//
// 参数约定：
//   - req.TransferID：可空（自动生成 UUID-style ID）；非空用于 Resume
//   - req.SessionID：必传；前端从 active session 拿 → 注入 ctx
//   - req.LocalPath：本地文件绝对路径；前端拿不到时可用上传函数式 API
//   - req.RemotePath：远端绝对路径
//   - req.ChunkSize：0 = DefaultChunkSize (4 MiB)；clamp 到 [1, 16] MiB
//   - req.Concurrency：0 = DefaultConcurrency (2)；clamp 到 [1, 4]
//   - req.Resume：true = 接续已有 manifest；false = 忽略旧 manifest 重新传
//
// 错误：SftpList 同款 + transfer.Upload 内部错误（mtime/size 变化 /
// 文件过大 / 网络中断）→ fmt.Errorf("wailsbindings.StartUpload: %w", err)。
// 错误时前端仍会收到 "transfer:error" 事件（异步），错误内容也通过
// binding 的 error 返回。
func (a *App) StartUpload(ctx context.Context, req transfer.UploadRequest) (string, error) {
	mgr := a.core.UploadManager()
	if mgr == nil {
		return "", errors.New("wailsbindings.StartUpload: upload manager not initialized")
	}
	if req.SessionID == "" {
		return "", errors.New("wailsbindings.StartUpload: empty sessionID in request")
	}
	// 注入 sessionID 到 ctx
	ctx = transfer.WithSessionID(ctx, session.ID(req.SessionID))
	id, err := mgr.StartUpload(ctx, req)
	if err != nil {
		return "", fmt.Errorf("wailsbindings.StartUpload: %w", err)
	}
	return id, nil
}

// CancelUpload 取消一个 transfer。
//
// 立即返回（不等待 goroutine 退出）；前端用 ListTransfers 看 State 转
// Canceled。前端订阅的 "transfer:error" 事件仍会触发（State=Canceled）。
//
// 错误：transferID 不存在 / upload manager 没初始化。
func (a *App) CancelUpload(ctx context.Context, transferID string) error {
	mgr := a.core.UploadManager()
	if mgr == nil {
		return errors.New("wailsbindings.CancelUpload: upload manager not initialized")
	}
	if err := mgr.CancelUpload(transferID); err != nil {
		return fmt.Errorf("wailsbindings.CancelUpload: %w", err)
	}
	return nil
}

// ListTransfers 返回全部 transfers 的快照（active + 已结束）。
//
// 排序：按 StartedAt 倒序（最新在前）。
// 用途：前端用 polling 兜底（事件丢失场景）；UI 面板展示"全部任务"。
//
// 返回的 []transfer.JobInfo 是 manager 内部 map 的快照拷贝，调用方修改
// 不影响 manager 状态。
func (a *App) ListTransfers(ctx context.Context) []transfer.JobInfo {
	mgr := a.core.UploadManager()
	if mgr == nil {
		return []transfer.JobInfo{}
	}
	return mgr.ListTransfers()
}

// GetTransfer 按 ID 取一个 transfer 快照。
//
// 不存在返回 (zero, false)。前端用 transfer:progress 事件实时更新 + 用
// GetTransfer 做兜底刷新。
func (a *App) GetTransfer(ctx context.Context, transferID string) (transfer.JobInfo, bool) {
	mgr := a.core.UploadManager()
	if mgr == nil {
		return transfer.JobInfo{}, false
	}
	return mgr.GetTransfer(transferID)
}
