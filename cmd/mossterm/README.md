# cmd/mossterm/

MossTerm 的主入口目录（Go 端）。

## 文件

| 文件 | 作用 |
|---|---|
| `main.go` | 解析 CLI flag；构造 `*app.App`；调用 `wails.Run` |
| `_embed_test.go` | 历史：早期调试 `//go:embed` 时的临时文件，保留以避免 git 误判 |
| `embed_test_main.go.bak` | 历史：同上 |
| `frontend/dist/` | embed 打包的前端 dist（dev 模式空目录，build 模式 wails 生成） |
| `wailsbindings/` | 暴露给前端的 API（通过 `wails.Bind` 注入到 webview） |

## embed 资源

```go
//go:embed frontend/dist
var assets embed.FS
```

- **dev 模式**：`wails dev` 走 vite dev server，dist 留空
- **build 模式**：`wails build` 生成 dist 后再 `go build`

## 历史

- **2026-07-13** `_embed_test.go` / `embed_test_main.go.bak` 是早期调试 embed 模式时的临时文件，已转为占位 README，保留以避免后续误删时被 git 误判为新增。
