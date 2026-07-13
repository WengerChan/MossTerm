# =============================================================================
# MossTerm —— Makefile
# 绿色基础设施：从源码到产物的标准动作
# 用法：make help
# =============================================================================

# —— 变量 ————————————————————————————————————————————————————————————————
APP          := mossterm
PKG          := github.com/mossterm/$(APP)
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT       ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
BUILD_DATE   ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS      := -s -w \
               -X '$(PKG)/internal/app.Version=$(VERSION)' \
               -X '$(PKG)/internal/app.Commit=$(COMMIT)' \
               -X '$(PKG)/internal/app.BuildDate=$(BUILD_DATE)'

# 跨平台构建矩阵
PLATFORMS    := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

# 路径
DIST_DIR     := dist
BIN_DIR      := bin
COVERAGE_OUT := coverage.out

# 工具
GO           ?= go
WAILS        ?= wails
NPM          ?= npm
GOLANGCI     ?= golangci-lint

# —— 默认目标 ————————————————————————————————————————————————————————
.DEFAULT_GOAL := help

# —— 帮助 ——————————————————————————————————————————————————————————
.PHONY: help
help: ## 打印所有可用的 make 目标与说明
	@echo ""
	@echo "  MossTerm —— Make 速查"
	@echo "  当前版本：$(VERSION)  commit: $(COMMIT)"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"; printf "  \033[36m%-18s\033[0m %s\n", "目标", "说明"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ""

# =============================================================================
# 开发与运行
# =============================================================================

.PHONY: dev
dev: ## wails dev —— 启动 Wails 热重载开发模式（前端 HMR + Go 自动重编）
	$(WAILS) dev

.PHONY: run
run: ## go run —— 仅运行 Go 后端（不渲染前端 webview，便于后端联调）
	$(GO) run ./cmd/$(APP)

.PHONY: wailsdev
wailsdev: ## 同 dev：明确 Wails 版本的开发命令
	$(WAILS) dev

.PHONY: wailsbuild
wailsbuild: ## wails build —— 构建完整的桌面应用（含 webview 资源）
	$(WAILS) build -clean -trimpath -ldflags "$(LDFLAGS)"

# =============================================================================
# 构建（跨平台）
# =============================================================================

.PHONY: build
build: ## 构建当前平台的二进制到 bin/
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
		-o $(BIN_DIR)/$(APP)$(shell $(GO) env GOEXE) ./cmd/$(APP)
	@echo "✓ 已生成 $(BIN_DIR)/$(APP)$(shell $(GO) env GOEXE)"

.PHONY: build-darwin
build-darwin: ## macOS 双架构构建（amd64 + arm64）→ dist/darwin/
	@$(MAKE) build-platform PLATFORM=darwin/amd64 OUT=$(DIST_DIR)/darwin/amd64/$(APP)
	@$(MAKE) build-platform PLATFORM=darwin/arm64 OUT=$(DIST_DIR)/darwin/arm64/$(APP)

.PHONY: build-linux
build-linux: ## Linux 双架构构建（amd64 + arm64）→ dist/linux/
	@$(MAKE) build-platform PLATFORM=linux/amd64  OUT=$(DIST_DIR)/linux/amd64/$(APP)
	@$(MAKE) build-platform PLATFORM=linux/arm64  OUT=$(DIST_DIR)/linux/arm64/$(APP)

.PHONY: build-windows
build-windows: ## Windows 双架构构建（amd64 + arm64）→ dist/windows/
	@$(MAKE) build-platform PLATFORM=windows/amd64 OUT=$(DIST_DIR)/windows/amd64/$(APP).exe
	@$(MAKE) build-platform PLATFORM=windows/arm64 OUT=$(DIST_DIR)/windows/arm64/$(APP).exe

.PHONY: build-all
build-all: build-darwin build-linux build-windows ## 一键构建所有平台产物
	@echo "✓ 全平台构建完成，产物在 $(DIST_DIR)/"

# 内部 target：单平台构建
.PHONY: build-platform
build-platform:
	@mkdir -p $(dir $(OUT))
	GOOS=$(word 1,$(subst /, ,$(PLATFORM))) \
	GOARCH=$(word 2,$(subst /, ,$(PLATFORM))) \
	CGO_ENABLED=1 \
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(OUT) ./cmd/$(APP)
	@echo "✓ $(PLATFORM) → $(OUT)"

# =============================================================================
# 代码质量
# =============================================================================

.PHONY: fmt
fmt: ## gofmt + goimports 格式化所有 Go 源文件
	$(GO) fmt ./...
	@command -v goimports >/dev/null 2>&1 && goimports -local $(PKG) -w . || echo "⚠ goimports 未安装，跳过（go install golang.org/x/tools/cmd/goimports@latest）"

.PHONY: vet
vet: ## go vet 静态检查
	$(GO) vet ./...

.PHONY: lint
lint: ## golangci-lint 全量检查
	@command -v $(GOLANGCI) >/dev/null 2>&1 || { \
		echo "⚠ golangci-lint 未安装：go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
		exit 1; }
	$(GOLANGCI) run --timeout=5m ./...

.PHONY: lint-fix
lint-fix: ## golangci-lint 自动修复可修问题
	$(GOLANGCI) run --timeout=5m --fix ./...

# =============================================================================
# 测试
# =============================================================================

.PHONY: test
test: ## go test ./...  -race -shuffle=on
	$(GO) test -race -shuffle=on -count=1 ./...

.PHONY: cover
cover: ## 跑测试并生成 HTML 覆盖率报告
	$(GO) test -race -shuffle=on -coverprofile=$(COVERAGE_OUT) -covermode=atomic ./...
	$(GO) tool cover -html=$(COVERAGE_OUT) -o coverage.html
	@echo "✓ 覆盖率报告：coverage.html"

# =============================================================================
# 依赖与安装
# =============================================================================

.PHONY: tidy
tidy: ## go mod tidy —— 同步依赖
	$(GO) mod tidy

.PHONY: install
install: ## go install —— 安装到 $(GOBIN)
	CGO_ENABLED=1 $(GO) install -ldflags "$(LDFLAGS)" ./cmd/$(APP)

.PHONY: uninstall
uninstall: ## 从 $(GOBIN) 卸载
	$(GO) clean -i github.com/mossterm/$(APP)

# =============================================================================
# 清理
# =============================================================================

.PHONY: clean
clean: ## 清理构建产物与缓存（dist/ bin/ coverage.*）
	rm -rf $(DIST_DIR) $(BIN_DIR) coverage.out coverage.html
	$(GO) clean -testcache

.PHONY: nuke
nuke: clean ## clean + 清空 Go 缓存（慎用）
	$(GO) clean -cache -modcache

# =============================================================================
# 杂项
# =============================================================================

.PHONY: version
version: ## 打印当前版本信息
	@echo "version : $(VERSION)"
	@echo "commit  : $(COMMIT)"
	@echo "date    : $(BUILD_DATE)"
	@echo "go      : $$($(GO) version)"

.PHONY: deps-check
deps-check: ## 打印关键依赖版本
	@echo "go        : $$($(GO) version)"
	@echo "wails     : $$($(WAILS) version 2>/dev/null || echo 'not installed')"
	@echo "node      : $$(node -v 2>/dev/null || echo 'not installed')"
	@echo "npm       : $$(npm -v 2>/dev/null || echo 'not installed')"
	@echo "golangci  : $$($(GOLANGCI) --version 2>/dev/null || echo 'not installed')"
