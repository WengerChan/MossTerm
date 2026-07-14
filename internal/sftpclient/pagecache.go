// pagecache.go 是 sftpclient 的客户端分页缓存层（v0.5.3 新增）。
//
// 设计目标：
//   - 把 v0.5.0 的 "List 一次性返回全量" 升级为 "List 真正分页"
//   - pkg/sftp v1.13.6 不暴露 Opendir+Readdir(n)，无法在 SFTP 协议层做分页
//   - 客户端分页：第一次 List 全量 ReadDir 缓存，后续 List 用 token 切缓存
//   - LRU 防内存爆：maxCacheEntries=100 个 path
//
// 本文件 vs client.go 的职责：
//   - pagecache.go：纯数据 + 切分 + token 编解码，零 SFTP 依赖，单测全覆盖
//   - client.go：包装 *sftp.Client + 调 pageCache，集成测试覆盖
package sftpclient

import (
	"container/list"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// 容量 + token 格式常量
const (
	// maxCacheEntries 是 LRU 缓存的 path 数量上限。
	//
	// 100 个 path × 假设每目录 ~10k entries × 每 entry 200B ≈ 200 MB 峰值
	// 99% 场景下用户不会切超过 100 个目录，LRU 自然淘汰冷目录。
	// 真要保 1000+ 目录请在 v0.6+ 接真分页协议（Opendir+Readdir(n)）。
	maxCacheEntries = 100

	// tokenVersionPrefix 是 token 的版本前缀。
	//
	// v1: 显式版本号，给将来格式升级留位置（"v2:..."）。
	// "v1:" 前缀 + 末尾 :offset 这种结构，使路径里出现的 ':' 不会与
	// 分隔符冲突（offset 永远是最后一个 ':' 之后）。
	tokenVersionPrefix = "v1:"
)

// ErrInvalidPageToken 是 token 解析失败（base64 / version / path / offset）
// 时返回的错误。Client.List 把此错误包装后传给 wailsbindings 前端。
var ErrInvalidPageToken = errors.New("invalid page token")

// pageState 缓存一个 path 的全量 entries 切片。
//
// 简化：只缓存条目列表，不缓存条目顺序信息。客户端分页假设 "同一次
// List 序列期间，目录内容不变" —— 目录被并发修改时，后续 page 拿到
// 的是缓存时的快照，与新 ReadDir 不一致。
// 这是客户端分页的本质 trade-off，v0.6+ 接真分页协议后可消除。
type pageState struct {
	entries []Entry
}

// pageCache 是 path → pageState 的 LRU 缓存。
//
// 并发模型：
//   - mu 保护 entries + lru
//   - lru 是双向链表，Value 是 *pageCacheNode
//   - entries[path] 存 *list.Element，避免在淘汰时再做 map lookup
//   - get 命中后 MoveToFront（标准 LRU 语义）
type pageCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
}

// pageCacheNode 是 LRU 链表的 Value。path 字段省掉淘汰时的 map 二次查询。
type pageCacheNode struct {
	path  string
	state pageState
}

// newPageCache 构造一个空的 pageCache。
func newPageCache() *pageCache {
	return &pageCache{
		entries: make(map[string]*list.Element),
		lru:     list.New(),
	}
}

// get 拿 path 的 pageState，命中时把节点移到 LRU 头部。
// 未命中返回 (zero, false)；调用方决定是 ReadDir 重读还是报错。
func (pc *pageCache) get(path string) (pageState, bool) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	el, ok := pc.entries[path]
	if !ok {
		return pageState{}, false
	}
	pc.lru.MoveToFront(el)
	return el.Value.(*pageCacheNode).state, true
}

// put 写 path 的 pageState。
//   - path 已存在：覆盖 state + MoveToFront
//   - path 不存在且未满：直接 PushFront
//   - path 不存在且已满：淘汰 LRU 尾部（多次淘汰直到 len < maxCacheEntries）
func (pc *pageCache) put(path string, state pageState) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if el, ok := pc.entries[path]; ok {
		el.Value.(*pageCacheNode).state = state
		pc.lru.MoveToFront(el)
		return
	}
	// 新 path：先腾位置
	for pc.lru.Len() >= maxCacheEntries {
		old := pc.lru.Back()
		if old == nil {
			break
		}
		oldPath := old.Value.(*pageCacheNode).path
		pc.lru.Remove(old)
		delete(pc.entries, oldPath)
	}
	node := &pageCacheNode{path: path, state: state}
	el := pc.lru.PushFront(node)
	pc.entries[path] = el
}

// size 返回当前缓存的 path 数量（测试 + 诊断用）。
func (pc *pageCache) size() int {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.lru.Len()
}

// clear 清空缓存（Client.Close 时调）。
func (pc *pageCache) clear() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.entries = make(map[string]*list.Element)
	pc.lru = list.New()
}

// lruOrder 返回当前 LRU 的 path 顺序（队首 = 最近使用）。
// 仅供测试断言使用；生产代码不需要遍历。
func (pc *pageCache) lruOrder() []string {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	out := make([]string, 0, pc.lru.Len())
	for e := pc.lru.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value.(*pageCacheNode).path)
	}
	return out
}

// slicePage 把 entries 切分成一页。
//
//   - offset < 0：调用方 bug，按 0 处理
//   - offset >= len(entries)：返回空页 + (-1 表示无下一页)
//   - end < len(entries)：返回 [offset, end) + nextOffset=end
//   - end >= len(entries)：返回 [offset, len) + nextOffset=-1
//
// 设计：nextOffset 用 -1 而不是 len(entries)，让 Client.List 知道
// "该返回空 token 了"，避免和 "正好切到末尾" 的合法状态混淆。
func slicePage(entries []Entry, offset, pageSize int) (page []Entry, nextOffset int) {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(entries) {
		return []Entry{}, -1
	}
	end := offset + pageSize
	if end >= len(entries) {
		return entries[offset:], -1
	}
	return entries[offset:end], end
}

// encodeToken 把 (path, offset) 编码成 base64 token。
//
// 格式：base64.RawURLEncoding("v1:" + path + ":" + offset)
//
// 为什么 RawURLEncoding：
//   - 不用 '+' '/' '=' padding → URL/header/cookie 安全
//   - 仍保持 base64 的紧凑（比 hex 短 1/3）
//
// 为什么 path + ":" + offset 而不是 path + offset 中间用别的字符：
//   - ':' 在 SFTP 路径里合法但不常见（POSIX 允许）—— 用 LastIndex 切，
//     offset 永远在最后一个 ':' 之后，path 里有多少 ':' 都不冲突
func encodeToken(path string, offset int) string {
	raw := tokenVersionPrefix + path + ":" + strconv.Itoa(offset)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeToken 解析 encodeToken 生成的 token。
//
// 错误一律映射为 ErrInvalidPageToken（不区分 base64 错 / version 错 / 格式错 /
// 数字错）—— 上层只需要知道 "token 不可用"，细分原因对前端无意义。
//
// 严格性：
//   - 必须能 base64 decode
//   - 必须以 "v1:" 开头
//   - 最后一个 ':' 之后必须是合法的非负整数
//   - path 可以为空字符串（理论可能：token for path "" + offset N）
func decodeToken(token string) (string, int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", 0, ErrInvalidPageToken
	}
	s := string(raw)
	if !strings.HasPrefix(s, tokenVersionPrefix) {
		return "", 0, ErrInvalidPageToken
	}
	rest := s[len(tokenVersionPrefix):]
	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return "", 0, ErrInvalidPageToken
	}
	path := rest[:idx]
	offsetStr := rest[idx+1:]
	if offsetStr == "" {
		return "", 0, ErrInvalidPageToken
	}
	offset, err := strconv.Atoi(offsetStr)
	if err != nil || offset < 0 {
		return "", 0, ErrInvalidPageToken
	}
	return path, offset, nil
}
