# CI 必过要点（v0.5.14 经验，已归档）

> ⚠️ **历史归档**。核心 6 节已并入 `AGENTS.md` 的 `## 🔧 CI 必过要点` 章节。
> 这里保留 v0.5.14 阶段的 golangci-lint v2 schema 细节等历史包袱，升 lint v3 / Go 1.26+ 时再翻。

跨 v0.5.7 → v0.5.14 共 6 小时排查 17 个 push 失败总结。

## 1. Go 工具链
- go.mod `go 1.25`，CI `GO_VERSION: "1.25"`
- 升 Go 必须同步升 `golangci-lint-action@v7` + 锁 v2.10.0+
  （v1.64.8 编译用 Go 1.24，lint Go 1.25 必挂）

## 2. golangci-lint v2 schema
v1 写法已废弃。`.golangci.yml` 关键字段位置：
- 顶层 `version: "2"`
- `output.formats` 是 **map**（不是 list）：
  ```yaml
  output:
    formats:
      text: { path: stdout, print-issued-lines: true, print-linter-name: true, colors: true }
  ```
- `gofmt`/`goimports` 不在 `linters.enable` → 改放 `formatters.enable` + `formatters.settings`
- `goimports.local-prefixes` 在 v2 是 **array** 不是 string
- `linters.disable-all` → `linters.default: none`
- `linters.presets` / `fast` → 删（v2 已弃）
- `linters-settings`（顶层）→ `linters.settings`
- `issues.exclude-rules` / `exclude-use-default` → `linters.exclusions.rules` / `linters.exclusions.warn-unused`

**本地验证陷阱**：
- v2 离线拉不到 jsonschema 会静默通过 `config verify`
- `golangci-lint run` 报 0 issues 不代表 schema OK
- **必须在线让 `config verify` 报错**，或对 https://golangci-lint.run/jsonschema/golangci.v2.10.jsonschema.json 校
- CI 报的 `additional properties X not allowed` = schema strict，nested 错会级联

**YAML 验证**：
- 用 `python3 + ruamel.yaml`（PyYAML 默认 1.1 与 GitHub 不一致）
- duplicate key（一个 step 两个 `shell: bash`）= GitHub 完全不解析，0 jobs 失败

## 3. govulncheck 漏洞
- x/crypto 必须 **v0.52+**（6 个 ssh/agent 漏洞要求）
- stdlib GO-2025-4011/4010/4009/4007 要求 Go ≥ 1.24.8
- 仍会剩 stdlib GO-2026-4xxx/5xxx（要求 1.25.x+），当前 unfixed-by-toolchain，不阻断

## 4. wails 跨平台 build（wails v2.12.0 + Go 1.25）
- go.mod `wailsapp/wails/v2` 必须 **v2.12.0**
  - v2.8.1 内部 cgo hardcode `webkit2gtk-4.0`
- 官方 wails v2.x **不支持 webkit2gtk-4.1**（issue #3345 未合并）
- Ubuntu 24.04 (noble) 必须**加 jammy 源**装 `libwebkit2gtk-4.0-dev`：
  ```bash
  sudo add-apt-repository -y "deb http://archive.ubuntu.com/ubuntu jammy main universe"
  sudo apt-get update
  sudo apt-get install -y libwebkit2gtk-4.0-dev
  ```
- wails 输出用默认 `build/bin/`，归档前 cp 到 dist/：
  - darwin: `build/bin/mossterm.app/` → `dist/mossterm.app/`
  - linux/win: `build/bin/<bin>` → `dist/<bin>`

## 5. CI yaml 通用坑
- **Windows runner 默认 pwsh**，`if [[ ... ]]` / `shell: bash` 是 bash 语法
  - 必加 `shell: bash` 或换 PowerShell 写法
- `matrix.platform` 含 `/`（如 `linux/amd64`），拼 filename 要 `replace('/', '-')`：
  ```bash
  PLATFORM_FLAT="${PLATFORM//\//-}"
  tar -czf "mossterm-${PLATFORM_FLAT}.tar.gz" "$PAYLOAD"
  ```
- `actions/setup-node@v4` `cache: npm` 模式要：
  ```yaml
  cache-dependency-path: frontend/package-lock.json
  ```
  漏了报 "Dependencies lock file is not found"

## 6. 本地沙盒限制
- 项目在 macOS `Documents/` 下，`go mod download` 经常 EPERM
- **必须在 `/tmp/MossTerm_ci` 跑**（`cp -R` 项目到 /tmp）：
  ```bash
  cp -R /Users/chenwen/Documents/learning/go-prj/MossTerm /tmp/MossTerm_ci
  cd /tmp/MossTerm_ci
  export GOMODCACHE=$HOME/go/pkg/mod GOCACHE=$HOME/go/cache GOPATH=$HOME/go
  export GOPROXY=https://goproxy.cn GOSUMDB=off
  go mod download && go vet ./... && go build ./... && go test -race ./...
  go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.0
  golangci-lint run ./...
  ```
- 本地全过 ≠ CI 过（CI 在线 schema verify、跨平台 race、macOS 沙盒路径）

## 7. windows CI race + shuffle flake
session/manager_test.go 三处 `waitForStateEvent` timeout 已放宽到 1s。
- 写新 session 测试时 default upper bound **至少 1s**
- 别用 100/500ms（windows runner + Go 1.25 + race detector 偶尔 >50ms）

## 8. 新增/改 workflow 后
- `python3 + ruamel.yaml` YAML 1.2 strict 验证
- `gofmt -w .` 走一遍
- `/tmp/MossTerm_ci` 全跑：vet / build / test -race / govulncheck
- **新增 commit 前所有 cron 监控都清掉**（避免后续 push 重复触发报警）

## 9. 跨 commit 版本映射
- v0.5.7  → CI 真绿基线
- v0.5.12 → gofmt 批量修
- v0.5.13 → 升 Go 1.25 + x/crypto v0.52.0
- v0.5.14 → golangci-lint v2.10.0 + wails v2.12.0（+ 各种 yaml/timeout 修复）✅
